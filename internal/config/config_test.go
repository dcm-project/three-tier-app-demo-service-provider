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
