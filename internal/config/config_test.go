package config_test

import (
	"testing"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
)

func TestConfigValidateWebExposure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{"kubernetes", config.Config{WebExposure: config.WebExposureKubernetes}, false},
		{"openshift", config.Config{WebExposure: config.WebExposureOpenShift}, false},
		{"invalid", config.Config{WebExposure: "bogus"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPrepareNormalizesDefaults(t *testing.T) {
	t.Parallel()
	cfg := config.Config{}
	if err := config.Prepare(&cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if cfg.WebExposure != config.WebExposureOpenShift {
		t.Fatalf("WebExposure = %q, want openshift default", cfg.WebExposure)
	}
	if cfg.Kubernetes.Namespace != "default" {
		t.Fatalf("Kubernetes.Namespace = %q, want default", cfg.Kubernetes.Namespace)
	}
}

func TestPrepareTrimsWebExposureWhitespace(t *testing.T) {
	t.Parallel()
	cfg := config.Config{WebExposure: "  openshift  "}
	if err := config.Prepare(&cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if cfg.WebExposure != config.WebExposureOpenShift {
		t.Fatalf("WebExposure = %q, want trimmed openshift", cfg.WebExposure)
	}
}

func TestPrepareInvalidWebExposure(t *testing.T) {
	t.Parallel()
	cfg := config.Config{WebExposure: "not-a-mode"}
	if err := config.Prepare(&cfg); err == nil {
		t.Fatal("expected error for invalid SP_WEB_EXPOSURE")
	}
}
