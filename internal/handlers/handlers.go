package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/statusreport"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
)

// StatusReporter publishes status to DCM. When nil, reporting is disabled.
type StatusReporter interface {
	Publish(ctx context.Context, instanceID, status, message string)
	PublishDeleted(ctx context.Context, instanceID string)
}

// Handlers implements server.ServerInterface.
type Handlers struct {
	Store     store.StackStore
	Container containerclient.ContainerClient
	Status    StatusReporter
}

var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (h *Handlers) GetHealth(w http.ResponseWriter, r *http.Request) {
	path := "health"
	ht := "3-tier-demo-service-provider.dcm.io/health"
	resp := v1alpha1.Health{
		Type:  &ht,
		State: "healthy",
		Path:  &path,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) ListThreeTierApps(w http.ResponseWriter, r *http.Request, params v1alpha1.ListThreeTierAppsParams) {
	maxPageSize := int32(50)
	if params.MaxPageSize != nil && *params.MaxPageSize >= 1 && *params.MaxPageSize <= 100 {
		maxPageSize = *params.MaxPageSize
	}
	offset := 0
	if params.PageToken != nil && *params.PageToken != "" {
		// simple offset-based pagination
		offset = 0 // TODO: decode page_token
	}
	stacks, hasMore := h.Store.List(r.Context(), int(maxPageSize), offset)
	list := make([]v1alpha1.Stack, len(stacks))
	for i, s := range stacks {
		list[i] = s
		if s.Id != nil {
			if status, ok := h.Container.GetStatus(r.Context(), *s.Id); ok {
				list[i].Status = status
				if h.Status != nil {
					dcm := statusreport.ToDCMStatus(string(status))
					h.Status.Publish(r.Context(), *s.Id, dcm, statusMessage(dcm))
				}
			}
		}
	}
	var nextToken *string
	if hasMore {
		t := ""
		nextToken = &t
	}
	resp := v1alpha1.StackList{Stacks: &list, NextPageToken: nextToken}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) CreateThreeTierApp(w http.ResponseWriter, r *http.Request, params v1alpha1.CreateThreeTierAppParams) {
	var req v1alpha1.CreateStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}
	id := req.Metadata.Name
	if params.Id != nil && *params.Id != "" {
		if !idPattern.MatchString(*params.Id) {
			writeError(w, http.StatusBadRequest, "Invalid id", "id must match pattern")
			return
		}
		id = *params.Id
	}
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "Invalid name", "metadata.name must match pattern")
		return
	}

	if existing, ok := h.Store.Get(r.Context(), id); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(existing)
		return
	}

	dbID, appID, webID, err := h.Container.CreateContainers(r.Context(), id, req.Spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Container creation failed", err.Error())
		return
	}
	_ = dbID
	_ = appID
	_ = webID

	now := time.Now()
	path := "three-tier-apps/" + id
	status := v1alpha1.RUNNING
	stack := v1alpha1.Stack{
		Id:         &id,
		Path:       &path,
		Spec:       req.Spec,
		Status:     status,
		CreateTime: &now,
		UpdateTime: &now,
	}

	created, err := h.Store.Create(r.Context(), stack)
	if err != nil {
		if err == store.ErrStackExists {
			existing, _ := h.Store.Get(r.Context(), id)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(existing)
			return
		}
		writeError(w, http.StatusConflict, "Stack exists", err.Error())
		return
	}

	if h.Status != nil {
		h.Status.Publish(r.Context(), id, statusreport.StatusRunning, "3-tier app running")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

func (h *Handlers) GetThreeTierApp(w http.ResponseWriter, r *http.Request, threeTierAppId string) {
	stack, found := h.Store.Get(r.Context(), threeTierAppId)
	if !found {
		writeError(w, http.StatusNotFound, "Not found", "3-tier app not found")
		return
	}
	if status, ok := h.Container.GetStatus(r.Context(), threeTierAppId); ok {
		stack.Status = status
		if h.Status != nil {
			dcm := statusreport.ToDCMStatus(string(status))
			h.Status.Publish(r.Context(), threeTierAppId, dcm, statusMessage(dcm))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(stack)
}

func (h *Handlers) DeleteThreeTierApp(w http.ResponseWriter, r *http.Request, threeTierAppId string) {
	_, ok := h.Store.Get(r.Context(), threeTierAppId)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found", "3-tier app not found")
		return
	}
	if err := h.Container.DeleteContainers(r.Context(), threeTierAppId); err != nil {
		writeError(w, http.StatusInternalServerError, "Container deletion failed", err.Error())
		return
	}
	deleted, err := h.Store.Delete(r.Context(), threeTierAppId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Delete failed", err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "Not found", "3-tier app not found")
		return
	}
	if h.Status != nil {
		h.Status.PublishDeleted(r.Context(), threeTierAppId)
	}
	w.WriteHeader(http.StatusNoContent)
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

func writeError(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1alpha1.Error{
		Type:   "about:blank",
		Title:  title,
		Status: ptr(int32(status)),
		Detail: &detail,
	})
}

func ptr[T any](v T) *T { return &v }
