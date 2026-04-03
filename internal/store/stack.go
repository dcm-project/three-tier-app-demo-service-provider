package store

import (
	"context"
	"sync"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
)

// AppStore persists 3-tier app records.
type AppStore interface {
	Create(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error)
	Get(ctx context.Context, id string) (v1alpha1.ThreeTierApp, bool)
	List(ctx context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool)
	// Update replaces a stored app record. Returns ErrNotFound when id is missing.
	Update(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error)
	Delete(ctx context.Context, id string) (bool, error)
}

type memoryStore struct {
	mu   sync.RWMutex
	apps map[string]v1alpha1.ThreeTierApp
}

// NewMemoryStore returns an in-memory app store.
func NewMemoryStore() AppStore {
	return &memoryStore{apps: make(map[string]v1alpha1.ThreeTierApp)}
}

func (s *memoryStore) Create(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[*app.Id]; ok {
		return app, ErrAlreadyExists
	}
	s.apps[*app.Id] = app
	return app, nil
}

func (s *memoryStore) Get(ctx context.Context, id string) (v1alpha1.ThreeTierApp, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, ok := s.apps[id]
	return app, ok
}

func (s *memoryStore) List(ctx context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []v1alpha1.ThreeTierApp
	for _, a := range s.apps {
		list = append(list, a)
	}
	if offset >= len(list) {
		return nil, false
	}
	end := offset + maxPageSize
	if end > len(list) {
		end = len(list)
	}
	return list[offset:end], end < len(list)
}

func (s *memoryStore) Update(_ context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[*app.Id]; !ok {
		return app, ErrNotFound
	}
	s.apps[*app.Id] = app
	return app, nil
}

func (s *memoryStore) Delete(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		return false, nil
	}
	delete(s.apps, id)
	return true, nil
}
