package registration

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	dcmv1alpha1 "github.com/dcm-project/service-provider-manager/api/v1alpha1/provider"
	dcmclient "github.com/dcm-project/service-provider-manager/pkg/client/provider"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
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

var ops = []string{"CREATE", "DELETE", "READ"}

// Option configures a Registrar.
type Option func(*Registrar)

// SetInitialBackoff sets the initial retry backoff interval.
func SetInitialBackoff(d time.Duration) Option {
	return func(r *Registrar) {
		r.initialBackoff = d
	}
}

// SetMaxBackoff sets the maximum retry backoff interval.
func SetMaxBackoff(d time.Duration) Option {
	return func(r *Registrar) {
		r.maxBackoff = d
	}
}

// Registrar handles registration with the DCM service provider registry.
type Registrar struct {
	cfg            *config.Config
	logger         *slog.Logger
	client         *dcmclient.ClientWithResponses
	initialBackoff time.Duration
	maxBackoff     time.Duration
	startOnce      sync.Once
	done           chan struct{}
}

// NewRegistrar creates a Registrar with the given configuration and options.
func NewRegistrar(cfg *config.Config, logger *slog.Logger, opts ...Option) (*Registrar, error) {
	u, err := url.Parse(cfg.DCM.RegistrationURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("creating DCM client: invalid registration URL %q", cfg.DCM.RegistrationURL)
	}

	r := &Registrar{
		cfg:            cfg,
		logger:         logger,
		initialBackoff: 1 * time.Second,
		maxBackoff:     60 * time.Second,
		done:           make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}

	httpClient := &http.Client{Timeout: httpTimeout}
	c, err := dcmclient.NewClientWithResponses(
		cfg.DCM.RegistrationURL,
		dcmclient.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("creating DCM client: %w", err)
	}
	r.client = c

	return r, nil
}

// BuildPayload constructs the registration payload from configuration.
func BuildPayload(cfg *config.Config) dcmv1alpha1.Provider {
	p := dcmv1alpha1.Provider{
		Name:          cfg.Provider.Name,
		ServiceType:   serviceType,
		Endpoint:      cfg.Provider.Endpoint + endpointSuffix,
		Operations:    &ops,
		SchemaVersion: schemaVersion,
	}

	if cfg.Provider.DisplayName != "" {
		displayName := cfg.Provider.DisplayName
		p.DisplayName = &displayName
	}

	if cfg.Provider.Region != "" || cfg.Provider.Zone != "" {
		meta := &dcmv1alpha1.ProviderMetadata{}
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

// Start begins the registration process in the background.
func (r *Registrar) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		go func() {
			defer close(r.done)
			r.run(ctx)
		}()
	})
}

// Done returns a channel that is closed when the registration goroutine has completed.
func (r *Registrar) Done() <-chan struct{} {
	return r.done
}

func (r *Registrar) run(ctx context.Context) {
	payload := BuildPayload(r.cfg)
	backoff := r.initialBackoff

	for {
		if err := r.register(ctx, payload); err == nil {
			r.logger.Info("registration successful")
			return
		} else {
			r.logger.Warn("registration failed, will retry", "error", err)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}

		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
}

func (r *Registrar) register(ctx context.Context, provider dcmv1alpha1.Provider) error {
	resp, err := r.client.CreateProviderWithResponse(ctx, nil, provider)
	if err != nil {
		return fmt.Errorf("sending registration request: %w", err)
	}

	sc := resp.StatusCode()
	if sc != http.StatusOK && sc != http.StatusCreated {
		body := resp.Body
		if len(body) > 200 {
			body = body[:200]
		}
		return fmt.Errorf("registration returned status %d: %s", sc, string(body))
	}

	return nil
}
