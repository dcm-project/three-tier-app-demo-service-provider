package containerclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
)

// ErrConflict is returned when CreateContainers is called for a stack that already exists.
var ErrConflict = errors.New("container already exists")

// ErrNotFound is returned when DeleteContainers is called for a stack that was not created.
var ErrNotFound = errors.New("container not found")

// New returns a ContainerClient based on the application config.
// Selection order: DEV_CONTAINER_BACKEND=podman → PodmanClient,
// CONTAINER_SP_URL set → HTTPClient, otherwise MockClient.
func New(cfg config.Config, logger *slog.Logger) (ContainerClient, error) {
	switch cfg.DevContainerBackend {
	case "podman":
		logger.Info("using Podman backend")
		return &PodmanClient{
			StackDBCfg:  cfg.StackDB,
			WebHostPort: cfg.PodmanWebHostPort,
		}, nil
	case "":
		if cfg.ContainerSPURL != "" {
			c, err := NewHTTPClient(cfg.ContainerSPURL, cfg.StackDB)
			if err != nil {
				return nil, fmt.Errorf("creating container SP HTTP client: %w", err)
			}
			logger.Info("using k8s container SP", "url", cfg.ContainerSPURL)
			return c, nil
		}
		logger.Info("using mock backend")
		return &MockClient{}, nil
	default:
		return nil, fmt.Errorf("unknown DEV_CONTAINER_BACKEND %q (valid: podman, or empty for mock/k8s SP)", cfg.DevContainerBackend)
	}
}

// ContainerClient creates and deletes containers via a container SP or Podman.
// When CONTAINER_SP_URL is empty, use MockClient or PodmanClient per DEV_CONTAINER_BACKEND.
type ContainerClient interface {
	// CreateContainers creates DB, app, and web containers in sequence.
	CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) error
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
