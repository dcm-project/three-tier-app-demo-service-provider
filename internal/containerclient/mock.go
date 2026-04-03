package containerclient

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
)

// ErrConflict is returned when CreateContainers is called for a stack that already exists.
var ErrConflict = errors.New("container already exists")

// ErrNotFound is returned when DeleteContainers is called for a stack that was not created.
var ErrNotFound = errors.New("container not found")

// MockClient simulates container SP calls aligned with k8s-container-service-provider behavior.
// Used when CONTAINER_SP_URL is empty. Tracks created stacks; Create returns ErrConflict for
// duplicates, Delete returns ErrNotFound for non-existent stacks.
type MockClient struct {
	mu      sync.RWMutex
	created map[string]struct{}
}

// CreateContainers returns synthetic container IDs (stackID-db, stackID-app, stackID-web),
// matching the naming used by PodmanClient and k8s container API. Returns ErrConflict if
// the stack was already created.
func (m *MockClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) (dbID, appID, webID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.created == nil {
		m.created = make(map[string]struct{})
	}
	if _, exists := m.created[stackID]; exists {
		return "", "", "", ErrConflict
	}
	m.created[stackID] = struct{}{}

	dbID = fmt.Sprintf("%s-db", stackID)
	appID = fmt.Sprintf("%s-app", stackID)
	webID = fmt.Sprintf("%s-web", stackID)
	_ = spec // unused in mock; k8s SP would use image, resources, etc.
	return dbID, appID, webID, nil
}

// GetStatus returns RUNNING for mock (simulated containers are always "running").
func (m *MockClient) GetStatus(ctx context.Context, stackID string) (v1alpha1.ThreeTierAppStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.created == nil {
		return v1alpha1.FAILED, true
	}
	if _, exists := m.created[stackID]; !exists {
		return v1alpha1.FAILED, true
	}
	return v1alpha1.RUNNING, true
}

// GetWebEndpoint always returns nil for the mock (no external IP in tests).
func (m *MockClient) GetWebEndpoint(_ context.Context, _ string) *string { return nil }

// DeleteContainers removes the stack from the mock's tracked state. Returns ErrNotFound
// if the stack was never created, matching k8s container SP 404 behavior.
func (m *MockClient) DeleteContainers(ctx context.Context, stackID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.created == nil {
		return ErrNotFound
	}
	if _, exists := m.created[stackID]; !exists {
		return ErrNotFound
	}
	delete(m.created, stackID)
	return nil
}
