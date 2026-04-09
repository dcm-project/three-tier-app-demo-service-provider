package store

import (
	"context"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
)

// AppStore persists 3-tier app records.
type AppStore interface {
	Create(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error)
	Get(ctx context.Context, id string) (v1alpha1.ThreeTierApp, bool)
	List(ctx context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool)
	// Update replaces a stored app record. Returns ErrNotFound when id is missing.
	Update(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error)
	Delete(ctx context.Context, id string) error
}
