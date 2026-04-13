// Package apiserver provides the HTTP server for the 3-tier demo service provider.
package apiserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	v1alpha1 "github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

const (
	readinessProbeTimeout  = 5 * time.Second
	readinessProbeInterval = 50 * time.Millisecond
	shutdownTimeout        = 10 * time.Second
	healthPath             = "/api/v1alpha1/health"
)

// Server is the HTTP server for the 3-tier demo service provider.
type Server struct {
	logger  *slog.Logger
	srv     *http.Server
	onReady func(context.Context)
}

// New creates a new Server wrapping the given handler with recovery,
// request-logging, and OpenAPI request validation middleware.
func New(addr string, handler http.Handler, logger *slog.Logger) (*Server, error) {
	swagger, err := v1alpha1.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
	}

	validate := oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapimiddleware.Options{
		SilenceServersWarning: true,
		ErrorHandler: func(w http.ResponseWriter, message string, statusCode int) {
			http.Error(w, message, statusCode)
		},
	})

	mux := http.NewServeMux()
	mux.Handle("/", recoveryMiddleware(logger)(requestLoggingMiddleware(logger)(validate(handler))))
	return &Server{
		logger: logger,
		srv: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}, nil
}

// WithOnReady registers a callback invoked once the server is confirmed ready.
// The server probes its own health endpoint before calling fn.
func (s *Server) WithOnReady(fn func(context.Context)) *Server {
	s.onReady = fn
	return s
}

// Run starts serving on ln and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	s.logger.Info("server starting", "address", ln.Addr().String())

	serveCh := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveCh <- err
		}
		close(serveCh)
	}()

	if s.onReady != nil {
		if err := s.waitForReady(ctx, ln.Addr().String()); err != nil {
			s.logger.Error("readiness probe failed, skipping onReady callback", "error", err)
		} else {
			s.onReady(ctx)
		}
	}

	select {
	case <-ctx.Done():
	case err := <-serveCh:
		if err != nil {
			return fmt.Errorf("serving on %s: %w", ln.Addr(), err)
		}
	}

	s.logger.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down server: %w", err)
	}
	return nil
}

func (s *Server) waitForReady(ctx context.Context, addr string) error {
	url := fmt.Sprintf("http://%s%s", addr, healthPath)
	client := &http.Client{Timeout: 1 * time.Second}

	deadline := time.NewTimer(readinessProbeTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(readinessProbeInterval)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			return fmt.Errorf("creating readiness probe request: %w", err)
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("readiness probe timed out after %s", readinessProbeTimeout)
		case <-ticker.C:
		}
	}
}

func recoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(http.ErrAbortHandler)
					}
					logger.Error("panic recovered", "panic", rec, "stack", string(debug.Stack()))
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start).String(),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
