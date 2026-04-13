package containerclient_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
)

func TestNew_RejectsInvalidWebExposure(t *testing.T) {
	t.Parallel()
	_, err := containerclient.New(config.Config{
		WebExposure: "bogus",
	}, slog.Default())
	if err == nil {
		t.Fatal("expected error from config validation")
	}
	if !strings.Contains(err.Error(), "SP_WEB_EXPOSURE") {
		t.Fatalf("error should mention SP_WEB_EXPOSURE: %v", err)
	}
}
