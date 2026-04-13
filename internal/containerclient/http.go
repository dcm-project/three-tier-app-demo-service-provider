package containerclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
	k8sclient "github.com/dcm-project/k8s-container-service-provider/pkg/client"
)

type HTTPClient struct {
	Client          *k8sclient.ClientWithResponses
	StackDBCfg      config.StackDBCfg
	webExposure     string
	openShiftRoutes *openShiftRoutes
}

// NewHTTPClient creates an HTTP client for the given base URL (kubernetes exposure: external web Service).
func NewHTTPClient(baseURL string, stackDBCfg config.StackDBCfg) (*HTTPClient, error) {
	return newHTTPClient(baseURL, stackDBCfg, config.WebExposureKubernetes, nil)
}

func newHTTPClient(baseURL string, stackDBCfg config.StackDBCfg, exposure string, oroutes *openShiftRoutes) (*HTTPClient, error) {
	client, err := k8sclient.NewClientWithResponses(baseURL)
	if err != nil {
		return nil, err
	}
	if exposure == "" {
		exposure = config.WebExposureKubernetes
	}
	return &HTTPClient{
		Client:          client,
		StackDBCfg:      stackDBCfg,
		webExposure:     exposure,
		openShiftRoutes: oroutes,
	}, nil
}

func tierPorts(net *v1alpha1.TierNetwork, defaultPort int, visibility k8sapi.ContainerPortVisibility) []k8sapi.ContainerPort {
	if net != nil && net.Ports != nil && len(*net.Ports) > 0 {
		out := make([]k8sapi.ContainerPort, len(*net.Ports))
		for i, p := range *net.Ports {
			out[i] = k8sapi.ContainerPort{ContainerPort: p.ContainerPort, Visibility: visibility}
		}
		return out
	}
	return []k8sapi.ContainerPort{{ContainerPort: defaultPort, Visibility: visibility}}
}

func (h *HTTPClient) k8sProcessEnvForDatabase(db v1alpha1.DatabaseTierSpec) *k8sapi.ContainerProcess {
	c := h.StackDBCfg
	switch db.Engine {
	case "mysql":
		env := []k8sapi.ContainerEnvVar{
			{Name: "MYSQL_ROOT_PASSWORD", Value: c.Password},
			{Name: "MYSQL_DATABASE", Value: c.DatabaseName},
		}
		return &k8sapi.ContainerProcess{Env: &env}
	default:
		env := []k8sapi.ContainerEnvVar{
			{Name: "POSTGRES_PASSWORD", Value: c.Password},
			{Name: "POSTGRES_DB", Value: c.DatabaseName},
		}
		return &k8sapi.ContainerProcess{Env: &env}
	}
}

// k8sProcessForWeb configures nginx to proxy to the app Service, using the app
// tier's configured port (defaults to 8080).
func k8sProcessForWeb(stackID string, appPort int) *k8sapi.ContainerProcess {
	appHost := stackID + "-app"
	script := fmt.Sprintf(`cat > /etc/nginx/conf.d/default.conf <<'EOF'
server {
  listen 80;
  location / {
    proxy_pass http://%s:%d;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
  }
}
EOF
exec nginx -g 'daemon off;'`, appHost, appPort)
	cmd := []string{"/bin/sh", "-c"}
	args := []string{script}
	return &k8sapi.ContainerProcess{Command: &cmd, Args: &args}
}

// k8sProcessEnvForApp points Spring Pet Clinic at the cluster DNS name for the DB Service
// (same name as the db container id: stackID-db).
func (h *HTTPClient) k8sProcessEnvForApp(stackID string, db v1alpha1.DatabaseTierSpec) *k8sapi.ContainerProcess {
	host := stackID + "-db"
	c := h.StackDBCfg
	switch db.Engine {
	case "mysql":
		env := []k8sapi.ContainerEnvVar{
			{Name: "SPRING_DATASOURCE_URL", Value: fmt.Sprintf("jdbc:mysql://%s:3306/%s", host, c.DatabaseName)},
			{Name: "SPRING_DATASOURCE_USERNAME", Value: c.MysqlUser},
			{Name: "SPRING_DATASOURCE_PASSWORD", Value: c.Password},
		}
		return &k8sapi.ContainerProcess{Env: &env}
	default:
		env := []k8sapi.ContainerEnvVar{
			{Name: "SPRING_DATASOURCE_URL", Value: fmt.Sprintf("jdbc:postgresql://%s:5432/%s", host, c.DatabaseName)},
			{Name: "SPRING_DATASOURCE_USERNAME", Value: c.PostgresUser},
			{Name: "SPRING_DATASOURCE_PASSWORD", Value: c.Password},
		}
		return &k8sapi.ContainerProcess{Env: &env}
	}
}

// k8sCreateContainerFailureDetail extracts RFC 7807 detail or raw body from a non-success CreateContainer response.
func k8sCreateContainerFailureDetail(resp *k8sclient.CreateContainerResponse) string {
	if resp == nil {
		return ""
	}
	for _, e := range []*k8sapi.Error{
		resp.ApplicationproblemJSON400,
		resp.ApplicationproblemJSON401,
		resp.ApplicationproblemJSON403,
		resp.ApplicationproblemJSON409,
		resp.ApplicationproblemJSON500,
	} {
		if e == nil {
			continue
		}
		if e.Detail != nil && strings.TrimSpace(*e.Detail) != "" {
			return strings.TrimSpace(*e.Detail)
		}
		if strings.TrimSpace(e.Title) != "" {
			return strings.TrimSpace(e.Title)
		}
	}
	if len(resp.Body) > 0 {
		return strings.TrimSpace(string(resp.Body))
	}
	return ""
}

func (h *HTTPClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) error {
	dbPort := 5432
	if spec.Database.Engine == "mysql" {
		dbPort = 3306
	}
	appPort := 8080
	if spec.App.HttpPort != nil {
		appPort = *spec.App.HttpPort
	}
	webVis := k8sapi.External
	if h.webExposure == config.WebExposureOpenShift {
		webVis = k8sapi.Internal
	}
	// Fixed sizing (not user-configurable): Pet Clinic JVM + DB need headroom; nginx stays small.
	tiers := []struct {
		name    string
		id      string
		image   string
		ports   []k8sapi.ContainerPort
		process *k8sapi.ContainerProcess
		res     k8sapi.ContainerResources
	}{
		{
			name: "db", id: stackID + "-db", image: dbImageFromSpec(spec.Database),
			ports: tierPorts(spec.Database.Network, dbPort, k8sapi.Internal), process: h.k8sProcessEnvForDatabase(spec.Database),
			res: k8sapi.ContainerResources{
				Cpu:    k8sapi.ContainerCpu{Min: 2, Max: 4},
				Memory: k8sapi.ContainerMemory{Min: "1GB", Max: "2GB"},
			},
		},
		{
			name: "app", id: stackID + "-app", image: spec.App.Image,
			ports: tierPorts(spec.App.Network, 8080, k8sapi.Internal), process: h.k8sProcessEnvForApp(stackID, spec.Database),
			res: k8sapi.ContainerResources{
				Cpu:    k8sapi.ContainerCpu{Min: 2, Max: 8},
				Memory: k8sapi.ContainerMemory{Min: "2GB", Max: "4GB"},
			},
		},
		{
			name: "web", id: stackID + "-web", image: spec.Web.Image,
			ports: tierPorts(spec.Web.Network, 80, webVis), process: k8sProcessForWeb(stackID, appPort),
			res: k8sapi.ContainerResources{
				Cpu:    k8sapi.ContainerCpu{Min: 1, Max: 4},
				Memory: k8sapi.ContainerMemory{Min: "512MB", Max: "1GB"},
			},
		},
	}

	ids := make([]string, 0, len(tiers))
	for _, t := range tiers {
		ports := t.ports
		body := k8sapi.Container{
			Spec: k8sapi.ContainerSpec{
				ServiceType: k8sapi.ContainerSpecServiceTypeContainer,
				Metadata:    k8sapi.ContainerMetadata{Name: t.id},
				Image:       k8sapi.ContainerImage{Reference: t.image},
				Resources:   t.res,
				Network:     &k8sapi.ContainerNetwork{Ports: &ports},
			},
		}
		if t.process != nil {
			body.Spec.Process = t.process
		}
		idParam := t.id
		resp, err := h.Client.CreateContainerWithResponse(ctx, &k8sapi.CreateContainerParams{Id: &idParam}, body)
		if err != nil {
			// Roll back containers created so far.
			_ = deleteContainerIDs(ctx, h.Client, ids)
			return fmt.Errorf("create %s: %w", t.name, err)
		}
		switch resp.StatusCode() {
		case http.StatusCreated:
			ids = append(ids, t.id)
		case http.StatusConflict:
			_ = deleteContainerIDs(ctx, h.Client, ids)
			return ErrConflict
		default:
			_ = deleteContainerIDs(ctx, h.Client, ids)
			detail := k8sCreateContainerFailureDetail(resp)
			if detail != "" {
				return fmt.Errorf("create %s: unexpected status %d: %s", t.name, resp.StatusCode(), detail)
			}
			return fmt.Errorf("create %s: unexpected status %d", t.name, resp.StatusCode())
		}
	}
	return nil
}

func (h *HTTPClient) DeleteContainers(ctx context.Context, stackID string) error {
	if h.openShiftRoutes != nil {
		_ = h.openShiftRoutes.deleteRoute(ctx, stackID)
	}
	return deleteContainerIDs(ctx, h.Client, []string{
		stackID + "-db", stackID + "-app", stackID + "-web",
	})
}

// deleteContainerIDs attempts to delete each container in ids, continuing on
// individual failures and returning all errors joined (ygalblum).
func deleteContainerIDs(ctx context.Context, client *k8sclient.ClientWithResponses, ids []string) error {
	var errs []error
	for _, id := range ids {
		resp, err := client.DeleteContainerWithResponse(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", id, err))
			continue
		}
		switch resp.StatusCode() {
		case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
			// not found is acceptable during rollback / concurrent deletes
		default:
			errs = append(errs, fmt.Errorf("delete %s: unexpected status %d", id, resp.StatusCode()))
		}
	}
	return errors.Join(errs...)
}

// GetWebEndpoint returns a browser URL for the web tier: OpenShift Route (SP_WEB_EXPOSURE=openshift)
// or LoadBalancer external IP (kubernetes). Returns nil when unavailable.
func (h *HTTPClient) GetWebEndpoint(ctx context.Context, stackID string) *string {
	if h.webExposure == config.WebExposureOpenShift {
		if h.openShiftRoutes == nil {
			return nil
		}
		u, err := h.openShiftRoutes.ensureWebRoute(ctx, stackID)
		if err != nil || u == nil {
			return nil
		}
		return u
	}
	webID := stackID + "-web"
	resp, err := h.Client.GetContainerWithResponse(ctx, webID)
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil
	}
	svc := resp.JSON200.Service
	if svc == nil || svc.ExternalIp == nil || *svc.ExternalIp == "" {
		return nil
	}
	port := 80
	if svc.Ports != nil && len(*svc.Ports) > 0 {
		port = (*svc.Ports)[0].Port
	}
	url := fmt.Sprintf("http://%s:%d", *svc.ExternalIp, port)
	return &url
}

// GetStatus queries the k8s container SP (GET /containers/{id}) for each tier and aggregates.
func (h *HTTPClient) GetStatus(ctx context.Context, stackID string) (v1alpha1.ThreeTierAppStatus, bool) {
	ids := []string{stackID + "-db", stackID + "-app", stackID + "-web"}
	statuses := make([]k8sapi.ContainerStatus, 0, 3)
	for _, id := range ids {
		resp, err := h.Client.GetContainerWithResponse(ctx, id)
		if err != nil {
			return "", false
		}
		switch resp.StatusCode() {
		case http.StatusOK:
			if resp.JSON200 == nil || resp.JSON200.Status == nil {
				statuses = append(statuses, k8sapi.PENDING)
				continue
			}
			statuses = append(statuses, *resp.JSON200.Status)
		case http.StatusNotFound:
			return v1alpha1.FAILED, true
		default:
			return "", false
		}
	}
	return AggregateK8sContainerStatuses(statuses)
}

// Ensure HTTPClient implements ContainerClient.
var _ ContainerClient = (*HTTPClient)(nil)
