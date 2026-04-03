package containerclient

import (
	"context"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
)

// ContainerClient creates and deletes containers via a container SP or Podman.
// When CONTAINER_SP_URL is empty, use MockClient or PodmanClient per DEV_CONTAINER_BACKEND.
type ContainerClient interface {
	// CreateContainers creates DB, app, and web containers in sequence.
	// Returns container IDs (or names) for the three tiers.
	CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) (dbID, appID, webID string, err error)
	// DeleteContainers deletes the three containers for the given stack.
	// The client derives container names/IDs from stackID.
	DeleteContainers(ctx context.Context, stackID string) error
	// GetStatus returns the aggregated status (worst among the 3 containers).
	// Podman and k8s HTTP clients query the runtime or k8s-container SP directly.
	// ok is false on transport errors (e.g. k8s SP unreachable); caller may retry.
	GetStatus(ctx context.Context, stackID string) (status v1alpha1.ThreeTierAppStatus, ok bool)
	// GetWebEndpoint returns the public URL of the web tier when the underlying
	// platform assigns an external IP (e.g. OpenShift LoadBalancer). Returns nil
	// when no external IP is available; callers should fall back to port-forward.
	GetWebEndpoint(ctx context.Context, stackID string) *string
}
