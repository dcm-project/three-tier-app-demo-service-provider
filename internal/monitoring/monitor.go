// Package monitoring watches active 3-tier apps and publishes status changes
// via CloudEvents over NATS, decoupled from the HTTP handler path.
package monitoring

import (
	"context"
	"log/slog"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/statusreport"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
)

const (
	defaultPollInterval = 10 * time.Second
	deleteChanBuffer    = 64
)

// StatusReporter is the subset of statusreport.Publisher used by the monitor.
type StatusReporter interface {
	Publish(ctx context.Context, instanceID, status, message string)
	PublishDeleted(ctx context.Context, instanceID string)
}

// StatusMonitor polls active apps for status changes and publishes DELETED
// events received from the service layer after successful deletion.
type StatusMonitor struct {
	store     store.AppStore
	container containerclient.ContainerClient
	reporter  StatusReporter
	deleteCh  chan string
	interval  time.Duration
	logger    *slog.Logger
}

// New creates a StatusMonitor. interval controls how often active apps are polled;
// pass 0 to use the default (10 s).
func New(st store.AppStore, cc containerclient.ContainerClient, reporter StatusReporter, interval time.Duration, logger *slog.Logger) *StatusMonitor {
	if interval == 0 {
		interval = defaultPollInterval
	}
	return &StatusMonitor{
		store:     st,
		container: cc,
		reporter:  reporter,
		deleteCh:  make(chan string, deleteChanBuffer),
		interval:  interval,
		logger:    logger,
	}
}

// NotifyDeleted signals that instanceID was deleted. Called by the service layer
// after containers and store record are removed. Non-blocking.
func (m *StatusMonitor) NotifyDeleted(instanceID string) {
	select {
	case m.deleteCh <- instanceID:
	default:
		m.logger.Warn("deleted notification dropped, channel full", "instance_id", instanceID)
	}
}

func (m *StatusMonitor) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// Start runs the monitor until ctx is cancelled. Should be called in a goroutine.
func (m *StatusMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.deleteCh:
			if m.reporter != nil {
				m.reporter.PublishDeleted(ctx, id)
			}
			m.log().Debug("published DELETED event", "instance_id", id)
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// reconcile fetches all apps from the store, queries their live container status,
// and publishes a status event when the live status differs from the stored one.
func (m *StatusMonitor) reconcile(ctx context.Context) {
	const maxApps = 1000
	apps, _ := m.store.List(ctx, maxApps, 0)
	for _, app := range apps {
		if app.Id == nil {
			continue
		}
		id := *app.Id

		live, ok := m.container.GetStatus(ctx, id)
		if !ok {
			continue // transport error — skip this cycle
		}

		stored := ""
		if app.Status != nil {
			stored = string(*app.Status)
		}
		if string(live) == stored {
			continue
		}
		// While Create's background provision runs, the store stays PENDING until tiers are
		// ready; the service updates the DB and publishes RUNNING. Skip here to avoid a second
		// NATS event for the same transition (reconcile runs on a separate schedule).
		if stored == string(v1alpha1.PENDING) && live == v1alpha1.RUNNING {
			continue
		}

		if m.reporter != nil {
			m.reporter.Publish(ctx, id, statusreport.ToDCMStatus(string(live)), "status changed")
		}
		m.log().Debug("published status change", "instance_id", id, "from", stored, "to", live)
	}
}
