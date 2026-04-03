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

// ThreeTierAppService encapsulates 3-tier provisioning and persistence logic.
type ThreeTierAppService struct {
	store     store.AppStore
	container containerclient.ContainerClient
	status    StatusReporter
}

// New creates a ThreeTierAppService backed by the given dependencies.
func New(st store.AppStore, cc containerclient.ContainerClient, sr StatusReporter) *ThreeTierAppService {
	return &ThreeTierAppService{store: st, container: cc, status: sr}
}

// Create provisions a new 3-tier app with the given id and spec.
// It persists a PENDING record first so a crash mid-provision leaves a
// reconcilable record, then provisions containers and updates to RUNNING.
// Returns ErrConflict when id already exists.
func (s *ThreeTierAppService) Create(ctx context.Context, id string, spec v1alpha1.ThreeTierSpec) (v1alpha1.ThreeTierApp, error) {
	if _, ok := s.store.Get(ctx, id); ok {
		return v1alpha1.ThreeTierApp{}, ErrConflict
	}

	now := time.Now()
	pending := v1alpha1.PENDING
	path := "three-tier-apps/" + id
	app := v1alpha1.ThreeTierApp{
		Id:         &id,
		Path:       &path,
		Spec:       spec,
		Status:     &pending,
		CreateTime: &now,
		UpdateTime: &now,
	}

	created, err := s.store.Create(ctx, app)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return v1alpha1.ThreeTierApp{}, ErrConflict
		}
		return v1alpha1.ThreeTierApp{}, fmt.Errorf("store create: %w", err)
	}

	// ErrConflict from the container SP means containers already exist (e.g.
	// SP restarted and cleared its store). Continue waiting rather than rolling
	// back — the containers are still alive.
	if _, _, _, err := s.container.CreateContainers(ctx, id, spec); err != nil &&
		!errors.Is(err, containerclient.ErrConflict) {
		_, _ = s.store.Delete(ctx, id)
		return v1alpha1.ThreeTierApp{}, fmt.Errorf("provision: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	if err := s.waitForRunning(waitCtx, id); err != nil {
		_ = s.container.DeleteContainers(context.Background(), id)
		_, _ = s.store.Delete(ctx, id)
		return v1alpha1.ThreeTierApp{}, fmt.Errorf("wait: %w", err)
	}

	running := v1alpha1.RUNNING
	updateTime := time.Now()
	created.Status = &running
	created.UpdateTime = &updateTime
	created.WebEndpoint = s.container.GetWebEndpoint(ctx, id)
	if updated, err := s.store.Update(ctx, created); err == nil {
		created = updated
	}

	if s.status != nil {
		s.status.Publish(ctx, id, statusreport.StatusRunning, statusMessage(statusreport.StatusRunning))
	}
	return created, nil
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
		if s.status != nil {
			dcm := statusreport.ToDCMStatus(string(st))
			s.status.Publish(ctx, id, dcm, statusMessage(dcm))
		}
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
func (s *ThreeTierAppService) Delete(ctx context.Context, id string) error {
	if _, ok := s.store.Get(ctx, id); !ok {
		return ErrNotFound
	}
	if err := s.container.DeleteContainers(ctx, id); err != nil {
		return fmt.Errorf("delete containers: %w", err)
	}
	if _, err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	if s.status != nil {
		s.status.PublishDeleted(ctx, id)
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
