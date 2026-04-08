package statusreport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const subject = "dcm.container"

// DCM Container Status values per service-provider-status-reporting enhancement.
const (
	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusUnknown   = "UNKNOWN"
	StatusDeleted   = "DELETED"
)

type cloudEvent struct {
	SpecVersion     string         `json:"specversion"`
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	Type            string         `json:"type"`
	Time            string         `json:"time"`
	DataContentType string         `json:"datacontenttype"`
	Data            cloudEventData `json:"data"`
}

type cloudEventData struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Publisher sends status updates to DCM via CloudEvents over NATS.
type Publisher struct {
	conn         *nats.Conn
	providerName string
	lastSent     map[string]string
	mu           sync.RWMutex
	logger       *slog.Logger
}

// NewPublisher creates a Publisher that publishes CloudEvents to NATS subject
// "dcm.container". When natsURL is empty, publishing is disabled (Publish is a no-op).
func NewPublisher(natsURL, providerName string, logger *slog.Logger) (*Publisher, error) {
	if natsURL == "" {
		return &Publisher{logger: logger}, nil
	}
	provider := providerName
	if provider == "" {
		provider = "three-tier-demo-sp"
	}
	conn, err := nats.Connect(natsURL,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.RetryOnFailedConnect(true),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Error("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS at %s: %w", natsURL, err)
	}
	return &Publisher{
		conn:         conn,
		providerName: provider,
		lastSent:     make(map[string]string),
		logger:       logger,
	}, nil
}

// Publish sends a CloudEvent with the given status. Debounces repeated identical
// statuses for the same instance.
func (p *Publisher) Publish(_ context.Context, instanceID, status, message string) {
	if p.conn == nil {
		return
	}
	p.mu.Lock()
	if prev, ok := p.lastSent[instanceID]; ok && prev == status {
		p.mu.Unlock()
		return
	}
	p.lastSent[instanceID] = status
	p.mu.Unlock()

	data, err := p.marshal(instanceID, status, message)
	if err != nil {
		p.logger.Error("failed to marshal status event", "instance_id", instanceID, "error", err)
		return
	}
	if err := p.conn.Publish(subject, data); err != nil {
		p.logger.Error("failed to publish status event", "instance_id", instanceID, "error", err)
	}
}

// PublishDeleted removes instanceID from the debounce cache and sends a DELETED event.
func (p *Publisher) PublishDeleted(ctx context.Context, instanceID string) {
	if p.conn == nil {
		return
	}
	p.mu.Lock()
	delete(p.lastSent, instanceID)
	p.mu.Unlock()

	p.Publish(ctx, instanceID, StatusDeleted, "3-tier app deleted")
}

// Close closes the underlying NATS connection.
func (p *Publisher) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}

func (p *Publisher) marshal(instanceID, status, message string) ([]byte, error) {
	ce := cloudEvent{
		SpecVersion:     "1.0",
		ID:              uuid.NewString(),
		Source:          "dcm/providers/" + p.providerName,
		Type:            "dcm.status.container",
		Time:            time.Now().UTC().Format(time.RFC3339),
		DataContentType: "application/json",
		Data: cloudEventData{
			ID:      instanceID,
			Status:  status,
			Message: message,
		},
	}
	return json.Marshal(ce)
}

// ToDCMStatus maps a raw status string to a DCM Container Status.
func ToDCMStatus(s string) string {
	switch s {
	case "PENDING":
		return StatusPending
	case "RUNNING":
		return StatusRunning
	case "FAILED":
		return StatusFailed
	case "DELETED":
		return StatusDeleted
	default:
		return StatusUnknown
	}
}
