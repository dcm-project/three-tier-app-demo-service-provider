// Package registration handles self-registration with DCM's environment agent.
//
// The control-plane /providers API was removed (control-plane#51). Standalone
// SPs register against environment-agent's POST /api/v1alpha1/providers, the
// same contract used by osac-service-provider. The agent then advertises
// three-tier-app-demo on its own POST /agents to the control plane.
package registration

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	v1alpha1 "github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	agentv1alpha1 "github.com/dcm-project/environment-agent/api/v1alpha1"
	agentclient "github.com/dcm-project/environment-agent/pkg/client"
)

const (
	serviceType   = "three-tier-app-demo"
	schemaVersion = "v1alpha1"
	httpTimeout   = 30 * time.Second
)

var endpointSuffix = mustPostPath()

func mustPostPath() string {
	p, err := v1alpha1.PostPath()
	if err != nil {
		panic(fmt.Sprintf("registration: resolving endpoint path from OpenAPI spec: %v", err))
	}
	return p
}

// Option configures a Registrar.
type Option func(*Registrar)

// SetInitialBackoff sets the initial retry backoff interval for retryable failures.
func SetInitialBackoff(d time.Duration) Option {
	return func(r *Registrar) {
		r.initialBackoff = d
	}
}

// SetMaxBackoff sets the maximum retry backoff interval for retryable failures.
func SetMaxBackoff(d time.Duration) Option {
	return func(r *Registrar) {
		r.maxBackoff = d
	}
}

// SetReRegistrationInterval sets how often a successful registration is renewed,
// and the retry cadence after a 409 (slot held by another provider).
func SetReRegistrationInterval(d time.Duration) Option {
	return func(r *Registrar) {
		r.reRegistrationInterval = d
	}
}

// WithHTTPClient overrides the HTTP client used by the generated
// environment-agent client. Intended for tests.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Registrar) {
		r.httpClient = c
	}
}

// Registrar registers this SP with environment-agent's provider API.
type Registrar struct {
	cfg                    *config.Config
	logger                 *slog.Logger
	client                 *agentclient.ClientWithResponses
	httpClient             *http.Client
	initialBackoff         time.Duration
	maxBackoff             time.Duration
	reRegistrationInterval time.Duration
	startOnce              sync.Once
	done                   chan struct{}
}

// NewRegistrar creates a Registrar targeting cfg.DCM.RegistrationURL
// (environment-agent base, e.g. http://agent:8080/api/v1alpha1).
func NewRegistrar(cfg *config.Config, logger *slog.Logger, opts ...Option) (*Registrar, error) {
	r := &Registrar{
		cfg:                    cfg,
		logger:                 logger,
		initialBackoff:         1 * time.Second,
		maxBackoff:             60 * time.Second,
		reRegistrationInterval: 60 * time.Second,
		httpClient:             &http.Client{Timeout: httpTimeout},
		done:                   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}

	client, err := agentclient.NewClientWithResponses(
		normalizeRegistrationURL(cfg.DCM.RegistrationURL),
		agentclient.WithHTTPClient(r.httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("creating environment-agent client: %w", err)
	}
	r.client = client
	return r, nil
}

// BuildPayload constructs the environment-agent provider registration payload.
func BuildPayload(cfg *config.Config) agentv1alpha1.Provider {
	p := agentv1alpha1.Provider{
		Name:          cfg.Provider.Name,
		ServiceType:   serviceType,
		Endpoint:      strings.TrimRight(cfg.Provider.Endpoint, "/") + endpointSuffix,
		SchemaVersion: schemaVersion,
	}
	if cfg.Provider.DisplayName != "" {
		displayName := cfg.Provider.DisplayName
		p.DisplayName = &displayName
	}
	if cfg.Provider.Region != "" || cfg.Provider.Zone != "" {
		meta := &agentv1alpha1.ProviderMetadata{}
		if cfg.Provider.Region != "" {
			region := cfg.Provider.Region
			meta.RegionCode = &region
		}
		if cfg.Provider.Zone != "" {
			zone := cfg.Provider.Zone
			meta.Zone = &zone
		}
		p.Metadata = meta
	}
	return p
}

// Start begins registration in the background. Multiple calls are safe.
func (r *Registrar) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		go func() {
			defer close(r.done)
			r.runLoop(ctx)
		}()
	})
}

// Done returns a channel that is closed when the registration goroutine has completed.
func (r *Registrar) Done() <-chan struct{} {
	return r.done
}

func (r *Registrar) runLoop(ctx context.Context) {
	backoff := r.initialBackoff

	for {
		statusCode, err := r.register(ctx)

		switch {
		case err == nil && (statusCode == http.StatusOK || statusCode == http.StatusCreated):
			r.logger.Info("registration successful", "name", r.cfg.Provider.Name, "status", statusCode)
			backoff = r.initialBackoff
			if !sleepOrDone(ctx, r.reRegistrationInterval) {
				return
			}
			continue

		case err == nil && statusCode == http.StatusConflict:
			r.logger.Warn("registration conflict: service type already served by another provider, will retry on re-registration cadence",
				"name", r.cfg.Provider.Name)
			if !sleepOrDone(ctx, r.reRegistrationInterval) {
				return
			}
			continue

		case err == nil && statusCode >= 400 && statusCode < 500:
			r.logger.Error("registration failed with non-retryable status, giving up",
				"name", r.cfg.Provider.Name, "status", statusCode)
			return

		case err == nil:
			r.logger.Warn("registration returned unexpected status, will retry",
				"name", r.cfg.Provider.Name, "status", statusCode)

		default:
			r.logger.Warn("registration request failed, will retry",
				"name", r.cfg.Provider.Name, "error", err)
		}

		if !sleepOrDone(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
}

func (r *Registrar) register(ctx context.Context) (int, error) {
	payload := BuildPayload(r.cfg)
	resp, err := r.client.CreateProviderWithResponse(ctx, nil, payload)
	if err != nil {
		return 0, fmt.Errorf("sending registration request: %w", err)
	}
	return resp.StatusCode(), nil
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// normalizeRegistrationURL ensures a trailing slash so oapi-codegen's
// relative "/providers" join keeps the /api/v1alpha1 prefix.
func normalizeRegistrationURL(raw string) string {
	if raw == "" {
		return raw
	}
	if strings.HasSuffix(raw, "/") {
		return raw
	}
	return raw + "/"
}
