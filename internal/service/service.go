// Package service implements the 3-tier app business logic (handler→service→store).
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/statusreport"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
)

// Sentinel errors returned by ThreeTierAppService; handlers map these to HTTP status codes.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// StatusReporter publishes status events to DCM. Nil disables reporting.
type StatusReporter interface {
	Publish(ctx context.Context, instanceID, status, message string)
	PublishDeleted(ctx context.Context, instanceID string)
}

const (
	provisionTimeout = 15 * time.Minute
	pollInterval     = 3 * time.Second
)

// DeletionNotifier is notified after an app is successfully deleted so that
// the monitoring goroutine can publish the DELETED status event.
type DeletionNotifier interface {
	NotifyDeleted(instanceID string)
}

// ThreeTierAppService encapsulates 3-tier provisioning and persistence logic.
type ThreeTierAppService struct {
	store     store.AppStore
	container containerclient.ContainerClient
	status    StatusReporter
	monitor   DeletionNotifier
}

// New creates a ThreeTierAppService backed by the given dependencies.
func New(st store.AppStore, cc containerclient.ContainerClient, sr StatusReporter) *ThreeTierAppService {
	return &ThreeTierAppService{store: st, container: cc, status: sr}
}

// WithMonitor attaches a DeletionNotifier that will be called after each
// successful deletion so the monitoring goroutine can publish the DELETED event.
func (s *ThreeTierAppService) WithMonitor(m DeletionNotifier) *ThreeTierAppService {
	s.monitor = m
	return s
}

// Create stores a PENDING record and returns immediately. Provisioning runs in
// the background; status is updated to RUNNING (or FAILED) when provisioning completes.
// Returns ErrConflict when id already exists.
func (s *ThreeTierAppService) Create(ctx context.Context, id string, spec v1alpha1.ThreeTierSpec) (v1alpha1.ThreeTierApp, error) {
	if _, ok := s.store.Get(ctx, id); ok {
		return v1alpha1.ThreeTierApp{}, ErrConflict
	}

	pending := v1alpha1.PENDING
	path := "three-tier-apps/" + id
	app := v1alpha1.ThreeTierApp{
		Id:     &id,
		Path:   &path,
		Spec:   spec,
		Status: &pending,
	}

	created, err := s.store.Create(ctx, app)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return v1alpha1.ThreeTierApp{}, ErrConflict
		}
		return v1alpha1.ThreeTierApp{}, fmt.Errorf("store create: %w", err)
	}

	go s.provision(context.Background(), id, spec, created)
	return created, nil
}

// provision runs in the background after Create returns. It provisions the
// containers and updates the stored record to RUNNING or FAILED.
func (s *ThreeTierAppService) provision(ctx context.Context, id string, spec v1alpha1.ThreeTierSpec, app v1alpha1.ThreeTierApp) {
	if err := s.container.CreateContainers(ctx, id, spec); err != nil {
		failed := v1alpha1.FAILED
		app.Status = &failed
		_, _ = s.store.Update(ctx, app)
		return
	}

	waitCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	if err := s.waitForRunning(waitCtx, id); err != nil {
		_ = s.container.DeleteContainers(context.Background(), id)
		_ = s.store.Delete(context.Background(), id)
		return
	}

	running := v1alpha1.RUNNING
	app.Status = &running
	app.WebEndpoint = s.container.GetWebEndpoint(ctx, id)
	if updated, err := s.store.Update(ctx, app); err == nil {
		app = updated
	}
	// Notify NATS as soon as the row is RUNNING (subscribers should not wait for the
	// monitor’s poll interval). While provisioning, the DB still says PENDING but
	// containers may already be RUNNING; the monitor’s reconcile would see that drift
	// and could publish RUNNING too—so it deliberately ignores PENDING vs live RUNNING,
	// and we publish that transition exactly here instead.
	if s.status != nil {
		s.status.Publish(ctx, id, statusreport.StatusRunning, statusMessage(statusreport.StatusRunning))
	}
}

// Get returns the stored app with its live container status refreshed.
// Returns ErrNotFound when the app does not exist.
func (s *ThreeTierAppService) Get(ctx context.Context, id string) (v1alpha1.ThreeTierApp, error) {
	app, ok := s.store.Get(ctx, id)
	if !ok {
		return v1alpha1.ThreeTierApp{}, ErrNotFound
	}
	if st, ok := s.container.GetStatus(ctx, id); ok {
		app.Status = &st
	}
	return app, nil
}

// List returns paginated apps with live statuses refreshed.
// Status events are NOT published for list calls (read-only probe).
func (s *ThreeTierAppService) List(ctx context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool) {
	apps, hasMore := s.store.List(ctx, maxPageSize, offset)
	for i, app := range apps {
		if app.Id != nil {
			if st, ok := s.container.GetStatus(ctx, *app.Id); ok {
				apps[i].Status = &st
			}
		}
	}
	return apps, hasMore
}

// Delete removes the 3-tier app and its containers.
// Returns ErrNotFound when the app does not exist.
// After successful deletion the monitoring goroutine is notified to publish the
// DELETED status event.
func (s *ThreeTierAppService) Delete(ctx context.Context, id string) error {
	if _, ok := s.store.Get(ctx, id); !ok {
		return ErrNotFound
	}
	if err := s.container.DeleteContainers(ctx, id); err != nil {
		return fmt.Errorf("delete containers: %w", err)
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	if s.monitor != nil {
		s.monitor.NotifyDeleted(id)
	}
	return nil
}

func (s *ThreeTierAppService) waitForRunning(ctx context.Context, id string) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		st, ok := s.container.GetStatus(ctx, id)
		if ok && st == v1alpha1.RUNNING {
			return nil
		}
		if ok && st == v1alpha1.FAILED {
			return fmt.Errorf("one or more containers are not healthy")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func statusMessage(status string) string {
	switch status {
	case statusreport.StatusRunning:
		return "3-tier app running"
	case statusreport.StatusPending:
		return "3-tier app pending"
	case statusreport.StatusFailed:
		return "3-tier app failed"
	case statusreport.StatusUnknown:
		return "3-tier app status unknown"
	default:
		return "3-tier app " + status
	}
}
