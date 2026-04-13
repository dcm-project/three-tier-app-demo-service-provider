package containerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
)

// MockContainerServer returns an httptest.Server that implements the k8s container
// API (POST/GET/DELETE /api/v1alpha1/containers[/{id}]).
// TEST-ONLY: Used for contract testing in http_test.go. Not used by runtime.
// Stateful: Create returns 409 for duplicate IDs, Delete returns 404 for non-existent.
//
// Create also supports deterministic failure injection via container id (query ?id=):
//   - id prefix "mock-400-" → 400 application/problem+json (body not recorded as created)
//   - id prefix "mock-500-" → 500 application/problem+json
// Use stack IDs like "mock-400" / "mock-500" so tier ids become mock-400-db, etc.
func MockContainerServer() *httptest.Server {
	mux := http.NewServeMux()
	state := &mockServerState{created: make(map[string]struct{})}

	mux.HandleFunc("/api/v1alpha1/containers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			state.handleCreate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/api/v1alpha1/containers/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1alpha1/containers/")
		switch r.Method {
		case http.MethodDelete:
			state.handleDelete(w, id)
		case http.MethodGet:
			state.handleGet(w, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}

type mockServerState struct {
	mu      sync.RWMutex
	created map[string]struct{}
}

func (s *mockServerState) handleCreate(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id query param required for idempotent create", http.StatusBadRequest)
		return
	}

	var body k8sapi.Container
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.created[id]; exists {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "ALREADY_EXISTS", "detail": "container already exists"})
		return
	}
	if strings.HasPrefix(id, "mock-400-") {
		writeMockProblem(w, http.StatusBadRequest, "mock create rejected (400)")
		return
	}
	if strings.HasPrefix(id, "mock-500-") {
		writeMockProblem(w, http.StatusInternalServerError, "mock create failed (500)")
		return
	}
	s.created[id] = struct{}{}

	now := time.Now()
	resp := k8sapi.Container{
		Id:         &id,
		Path:       ptr("containers/" + id),
		Status:     ptr(k8sapi.RUNNING),
		CreateTime: &now,
		UpdateTime: &now,
		Spec: k8sapi.ContainerSpec{
			ServiceType: k8sapi.ContainerSpecServiceTypeContainer,
			Metadata:    body.Spec.Metadata,
			Image:       body.Spec.Image,
			Resources:   body.Spec.Resources,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *mockServerState) handleGet(w http.ResponseWriter, id string) {
	s.mu.RLock()
	_, exists := s.created[id]
	s.mu.RUnlock()
	if !exists {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "NOT_FOUND", "detail": "container not found"})
		return
	}
	now := time.Now()
	st := k8sapi.RUNNING
	resp := k8sapi.Container{
		Id:         &id,
		Path:       ptr("containers/" + id),
		Status:     &st,
		CreateTime: &now,
		UpdateTime: &now,
		Spec: k8sapi.ContainerSpec{
			ServiceType: k8sapi.ContainerSpecServiceTypeContainer,
			Metadata:    k8sapi.ContainerMetadata{Name: id},
			Image:       k8sapi.ContainerImage{Reference: "mock"},
			Resources: k8sapi.ContainerResources{
				Cpu:    k8sapi.ContainerCpu{Min: 1, Max: 2},
				Memory: k8sapi.ContainerMemory{Min: "256MB", Max: "512MB"},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *mockServerState) handleDelete(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.created[id]; !exists {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "NOT_FOUND", "detail": "container not found"})
		return
	}
	delete(s.created, id)
	w.WriteHeader(http.StatusNoContent)
}

func writeMockProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
	})
}

func ptr[T any](v T) *T { return &v }
