package statusreport

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"
)

// DCM Container Status values per service-provider-status-reporting enhancement.
const (
	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusUnknown   = "UNKNOWN"
	StatusDeleted   = "DELETED"
)

// ContainerStatus is the payload for status CloudEvents per the enhancement.
type ContainerStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Publisher sends status updates to DCM via CloudEvents.
type Publisher struct {
	client       cloudevents.Client
	targetURL    string
	providerName string
	serviceType  string
	source       string
	lastSent     map[string]string
	mu           sync.RWMutex
}

// NewPublisher creates a publisher that POSTs CloudEvents to targetURL.
// When targetURL is empty, publishing is disabled (Publish is a no-op).
func NewPublisher(targetURL, providerName string) (*Publisher, error) {
	if targetURL == "" {
		return &Publisher{}, nil
	}
	c, err := cloudevents.NewClientHTTP()
	if err != nil {
		return nil, fmt.Errorf("create http client: %w", err)
	}
	provider := providerName
	if provider == "" {
		provider = "three-tier-demo-sp"
	}
	source := provider + "-demo"
	return &Publisher{
		client:       c,
		targetURL:    targetURL,
		providerName: provider,
		serviceType:  "three_tier_app_demo",
		source:       source,
		lastSent:     make(map[string]string),
	}, nil
}

// Publish sends a CloudEvent with the given status. Only publishes when status
// differs from last sent (debounce). Uses DCM-compliant status values.
func (p *Publisher) Publish(ctx context.Context, instanceID, status, message string) {
	if p.targetURL == "" {
		return
	}
	p.mu.Lock()
	if prev, ok := p.lastSent[instanceID]; ok && prev == status {
		p.mu.Unlock()
		return
	}
	p.lastSent[instanceID] = status
	p.mu.Unlock()

	eventType := fmt.Sprintf("dcm.providers.%s.%s.instances.%s.status",
		p.providerName, p.serviceType, instanceID)

	event := cloudevents.NewEvent()
	event.SetID(fmt.Sprintf("%s-%d", instanceID, time.Now().UnixNano()))
	event.SetSource(p.source)
	event.SetType(eventType)
	event.SetTime(time.Now())
	if err := event.SetData(cloudevents.ApplicationJSON, ContainerStatus{
		Status:  status,
		Message: message,
	}); err != nil {
		log.Printf("[status] failed to set event data: %v", err)
		return
	}

	reqCtx := cloudevents.ContextWithTarget(ctx, p.targetURL)
	result := p.client.Send(reqCtx, event)
	if cloudevents.IsUndelivered(result) {
		log.Printf("[status] failed to send event for %s: %v", instanceID, result)
		return
	}
	var res *cehttp.Result
	if result != nil && cloudevents.ResultAs(result, &res) && res.StatusCode != 0 &&
		res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		log.Printf("[status] status report returned %d for %s", res.StatusCode, instanceID)
	}
}

// PublishDeleted removes instanceID from lastSent and sends a DELETED event.
func (p *Publisher) PublishDeleted(ctx context.Context, instanceID string) {
	if p.targetURL == "" {
		return
	}
	p.mu.Lock()
	delete(p.lastSent, instanceID)
	p.mu.Unlock()

	eventType := fmt.Sprintf("dcm.providers.%s.%s.instances.%s.status",
		p.providerName, p.serviceType, instanceID)

	event := cloudevents.NewEvent()
	event.SetID(fmt.Sprintf("%s-deleted-%d", instanceID, time.Now().UnixNano()))
	event.SetSource(p.source)
	event.SetType(eventType)
	event.SetTime(time.Now())
	_ = event.SetData(cloudevents.ApplicationJSON, ContainerStatus{
		Status:  StatusDeleted,
		Message: "3-tier app deleted",
	})

	reqCtx := cloudevents.ContextWithTarget(ctx, p.targetURL)
	_ = p.client.Send(reqCtx, event)
}

// ToDCMStatus maps StackStatus to DCM Container Status (PENDING, RUNNING, FAILED, UNKNOWN).
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
