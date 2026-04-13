package containerclient_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
)

func TestNew_OpenShiftRequiresRouteNamespace(t *testing.T) {
	t.Parallel()
	_, err := containerclient.New(config.Config{
		ContainerSPURL:          "http://127.0.0.1:9",
		WebExposure:             config.WebExposureOpenShift,
		OpenShiftRouteNamespace: "",
	}, slog.Default())
	if err == nil {
		t.Fatal("expected error when SP_OPENSHIFT_ROUTE_NAMESPACE is empty for openshift exposure")
	}
	if !strings.Contains(err.Error(), "SP_OPENSHIFT_ROUTE_NAMESPACE") {
		t.Fatalf("error should mention SP_OPENSHIFT_ROUTE_NAMESPACE: %v", err)
	}
}
