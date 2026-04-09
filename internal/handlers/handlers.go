// Package handlers implements StrictServerInterface; HTTP input validation
// and response mapping only — all business logic lives in service.ThreeTierAppService.
package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/api/server"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/service"
)

// Handlers implements server.StrictServerInterface.
type Handlers struct {
	Svc *service.ThreeTierAppService
}

var _ server.StrictServerInterface = (*Handlers)(nil)

var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (h *Handlers) GetHealth(_ context.Context, _ server.GetHealthRequestObject) (server.GetHealthResponseObject, error) {
	path := "health"
	ht := "3-tier-demo-service-provider.dcm.io/health"
	return server.GetHealth200JSONResponse(v1alpha1.Health{
		Type:  &ht,
		State: "healthy",
		Path:  &path,
	}), nil
}

func (h *Handlers) ListThreeTierApps(ctx context.Context, req server.ListThreeTierAppsRequestObject) (server.ListThreeTierAppsResponseObject, error) {
	maxPageSize := int32(50)
	if req.Params.MaxPageSize != nil {
		maxPageSize = *req.Params.MaxPageSize
	}

	offset := 0
	if req.Params.PageToken != nil && *req.Params.PageToken != "" {
		if b, err := base64.StdEncoding.DecodeString(*req.Params.PageToken); err == nil {
			if n, err := strconv.Atoi(string(b)); err == nil && n >= 0 {
				offset = n
			}
		}
	}

	apps, hasMore := h.Svc.List(ctx, int(maxPageSize), offset)
	list := make([]v1alpha1.ThreeTierApp, len(apps))
	copy(list, apps)

	var nextToken *string
	if hasMore {
		t := base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset + int(maxPageSize))))
		nextToken = &t
	}
	return server.ListThreeTierApps200JSONResponse(v1alpha1.ThreeTierAppList{
		ThreeTierApps: &list,
		NextPageToken: nextToken,
	}), nil
}

func (h *Handlers) CreateThreeTierApp(ctx context.Context, req server.CreateThreeTierAppRequestObject) (server.CreateThreeTierAppResponseObject, error) {
	body := req.Body

	if !idPattern.MatchString(body.Metadata.Name) {
		return server.CreateThreeTierApp400ApplicationProblemPlusJSONResponse(
			errBody("Invalid name", "metadata.name must match "+idPattern.String()),
		), nil
	}
	id := body.Metadata.Name
	if req.Params.Id != nil && *req.Params.Id != "" {
		if !idPattern.MatchString(*req.Params.Id) {
			return server.CreateThreeTierApp400ApplicationProblemPlusJSONResponse(
				errBody("Invalid id", "id must match "+idPattern.String()),
			), nil
		}
		id = *req.Params.Id
	}

	app, err := h.Svc.Create(ctx, id, body.Spec)
	if err != nil {
		if errors.Is(err, service.ErrConflict) {
			return server.CreateThreeTierApp409ApplicationProblemPlusJSONResponse(
				errBody("Conflict", fmt.Sprintf("3-tier app %q already exists", id)),
			), nil
		}
		return server.CreateThreeTierApp500ApplicationProblemPlusJSONResponse(
			errBody("Create failed", err.Error()),
		), nil
	}
	return server.CreateThreeTierApp201JSONResponse(app), nil
}

func (h *Handlers) GetThreeTierApp(ctx context.Context, req server.GetThreeTierAppRequestObject) (server.GetThreeTierAppResponseObject, error) {
	app, err := h.Svc.Get(ctx, req.ThreeTierAppId)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			return server.GetThreeTierApp404ApplicationProblemPlusJSONResponse(
				errBody("Not found", "3-tier app not found"),
			), nil
		}
		return server.GetThreeTierApp500ApplicationProblemPlusJSONResponse(
			errBody("Get failed", err.Error()),
		), nil
	}
	return server.GetThreeTierApp200JSONResponse(app), nil
}

func (h *Handlers) DeleteThreeTierApp(ctx context.Context, req server.DeleteThreeTierAppRequestObject) (server.DeleteThreeTierAppResponseObject, error) {
	if err := h.Svc.Delete(ctx, req.ThreeTierAppId); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			return server.DeleteThreeTierApp404ApplicationProblemPlusJSONResponse(
				errBody("Not found", "3-tier app not found"),
			), nil
		}
		return server.DeleteThreeTierApp500ApplicationProblemPlusJSONResponse(
			errBody("Delete failed", err.Error()),
		), nil
	}
	return server.DeleteThreeTierApp204Response{}, nil
}

func errBody(title, detail string) v1alpha1.Error {
	return v1alpha1.Error{
		Type:   "about:blank",
		Title:  title,
		Detail: &detail,
	}
}
