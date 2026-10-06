package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
)

const (
	serviceType        = "three-tier-app-demo"
	httpTimeout        = 30 * time.Second
	defaultCost        = "low"
	defaultEnvironment = "dev"
	defaultHeartbeat   = 15 * time.Second
)

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

// SetHeartbeatInterval sets how often heartbeats are sent after registration.
func SetHeartbeatInterval(d time.Duration) Option {
	return func(r *Registrar) {
		r.heartbeatInterval = d
	}
}

// AgentRegistration is the control-plane /agents registration payload.
type AgentRegistration struct {
	Name         string   `json:"name"`
	Environment  string   `json:"environment"`
	Cost         string   `json:"cost"`
	TopicName    string   `json:"topic_name"`
	ServiceTypes []string `json:"service_types"`
}

type agentRegistrationResponse struct {
	AgentID string `json:"agent_id"`
}

type heartbeatRequest struct {
	ConsumerLag int64     `json:"consumer_lag"`
	Timestamp   time.Time `json:"timestamp"`
}

// Registrar handles registration with the DCM agent API.
type Registrar struct {
	cfg                *config.Config
	logger             *slog.Logger
	httpClient         *http.Client
	baseURL            *url.URL
	initialBackoff     time.Duration
	maxBackoff         time.Duration
	heartbeatInterval  time.Duration
	startOnce          sync.Once
	done               chan struct{}
}

// NewRegistrar creates a Registrar with the given configuration and options.
func NewRegistrar(cfg *config.Config, logger *slog.Logger, opts ...Option) (*Registrar, error) {
	u, err := url.Parse(cfg.DCM.RegistrationURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("creating DCM client: invalid registration URL %q", cfg.DCM.RegistrationURL)
	}

	r := &Registrar{
		cfg:               cfg,
		logger:            logger,
		httpClient:        &http.Client{Timeout: httpTimeout},
		baseURL:           u,
		initialBackoff:    1 * time.Second,
		maxBackoff:        60 * time.Second,
		heartbeatInterval: defaultHeartbeat,
		done:              make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// BuildPayload constructs the agent registration payload from configuration.
func BuildPayload(cfg *config.Config) AgentRegistration {
	env := cfg.Provider.Environment
	if env == "" {
		env = defaultEnvironment
	}
	cost := cfg.Provider.Cost
	if cost == "" {
		cost = defaultCost
	}
	topic := cfg.Provider.TopicName
	if topic == "" {
		topic = "dcm.agent." + cfg.Provider.Name
	}
	return AgentRegistration{
		Name:         cfg.Provider.Name,
		Environment:  env,
		Cost:         cost,
		TopicName:    topic,
		ServiceTypes: []string{serviceType},
	}
}

// Start begins the registration + heartbeat process in the background.
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

	var agentID string
	for {
		id, err := r.register(ctx, payload)
		if err == nil {
			r.logger.Info("registration successful", "agent_id", id, "name", payload.Name)
			agentID = id
			break
		}
		r.logger.Warn("registration failed, will retry", "error", err)

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

	r.heartbeatLoop(ctx, agentID)
}

func (r *Registrar) register(ctx context.Context, payload AgentRegistration) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal registration payload: %w", err)
	}

	endpoint := joinURL(r.baseURL, "agents")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sending registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending registration request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		if len(respBody) > 200 {
			respBody = respBody[:200]
		}
		return "", fmt.Errorf("registration returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result agentRegistrationResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode registration response: %w", err)
	}
	if result.AgentID == "" {
		return "", fmt.Errorf("registration response missing agent_id")
	}
	return result.AgentID, nil
}

func (r *Registrar) heartbeatLoop(ctx context.Context, agentID string) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	// Send an immediate heartbeat so last_heartbeat is set before the
	// control-plane health monitor's create_time-based stale cutoff.
	if err := r.heartbeat(ctx, agentID); err != nil {
		r.logger.Warn("heartbeat failed", "error", err, "agent_id", agentID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.heartbeat(ctx, agentID); err != nil {
				r.logger.Warn("heartbeat failed", "error", err, "agent_id", agentID)
			}
		}
	}
}

func (r *Registrar) heartbeat(ctx context.Context, agentID string) error {
	payload := heartbeatRequest{
		ConsumerLag: 0,
		Timestamp:   time.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal heartbeat payload: %w", err)
	}

	endpoint := joinURL(r.baseURL, "agents", agentID, "heartbeat")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sending heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending heartbeat request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat returned status %d", resp.StatusCode)
	}
	return nil
}

func joinURL(base *url.URL, parts ...string) string {
	u := *base
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	for _, p := range parts {
		path += "/" + strings.Trim(p, "/")
	}
	u.Path = path
	u.RawPath = ""
	return u.String()
}
