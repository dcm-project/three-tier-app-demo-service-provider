package containerclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

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

const (
	// k8sGenerateNameSuffixLen is "-" + 5 random chars added by apiserver.
	k8sGenerateNameSuffixLen = 6
	// k8sNameMaxLen is DNS label max length for Service names.
	k8sNameMaxLen = 63
	// metadata.name must leave room for GenerateName suffix on the k8s SP side.
	k8sContainerMetadataNameMaxLen = k8sNameMaxLen - k8sGenerateNameSuffixLen

	// Polling for Service name: create may return before the SP populates it.
	k8sServiceNamePollInterval = 500 * time.Millisecond
	k8sServiceNameMaxWait      = 3 * time.Minute
)

// newHTTPClient creates an HTTPClient targeting the k8s container SP at baseURL.
// Use exposure to select web tier ingress: OpenShift Route (with oroutes) or external Service.
func newHTTPClient(baseURL string, stackDBCfg config.StackDBCfg, exposure string, oroutes *openShiftRoutes) (*HTTPClient, error) {
	client, err := k8sclient.NewClientWithResponses(baseURL)
	if err != nil {
		return nil, err
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

// k8sProcessForWeb configures nginx to proxy to the app Service name, using the app
// tier's configured port (defaults to 8080).
func k8sProcessForWeb(appServiceHost string, appPort int) *k8sapi.ContainerProcess {
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
exec nginx -g 'daemon off;'`, appServiceHost, appPort)
	cmd := []string{"/bin/sh", "-c"}
	args := []string{script}
	return &k8sapi.ContainerProcess{Command: &cmd, Args: &args}
}

// k8sProcessEnvForApp points Spring Pet Clinic at the DB Service name (from GET after
// create; the create response may omit service info).
func (h *HTTPClient) k8sProcessEnvForApp(dbServiceHost string, db v1alpha1.DatabaseTierSpec) *k8sapi.ContainerProcess {
	c := h.StackDBCfg
	switch db.Engine {
	case "mysql":
		env := []k8sapi.ContainerEnvVar{
			{Name: "SPRING_DATASOURCE_URL", Value: fmt.Sprintf("jdbc:mysql://%s:3306/%s", dbServiceHost, c.DatabaseName)},
			{Name: "SPRING_DATASOURCE_USERNAME", Value: c.MysqlUser},
			{Name: "SPRING_DATASOURCE_PASSWORD", Value: c.Password},
		}
		return &k8sapi.ContainerProcess{Env: &env}
	default:
		env := []k8sapi.ContainerEnvVar{
			{Name: "SPRING_DATASOURCE_URL", Value: fmt.Sprintf("jdbc:postgresql://%s:5432/%s", dbServiceHost, c.DatabaseName)},
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

// k8sContainerServiceName returns the trimmed Service name, or "" if the
// container has no service, no name, or the value is blank (GET may return 200
// before the field is set).
func k8sContainerServiceName(c *k8sapi.Container) string {
	if c == nil || c.Service == nil || c.Service.Name == nil {
		return ""
	}
	return strings.TrimSpace(*c.Service.Name)
}

// waitForContainerServiceName polls GET /containers/{id} until the Service has a
// name or the wait budget is exceeded (create alone may not include service info).
func (h *HTTPClient) waitForContainerServiceName(ctx context.Context, tierName, containerID string) (string, error) {
	deadline := time.Now().Add(k8sServiceNameMaxWait)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resp, err := h.Client.GetContainerWithResponse(ctx, containerID)
		if err != nil {
			return "", fmt.Errorf("get %s container: %w", tierName, err)
		}
		switch resp.StatusCode() {
		case http.StatusOK:
			if resp.JSON200 != nil {
				if name := k8sContainerServiceName(resp.JSON200); name != "" {
					return name, nil
				}
			}
		case http.StatusNotFound:
			// Newly created resource may not be readable immediately.
		default:
			return "", fmt.Errorf("get %s container: unexpected status %d", tierName, resp.StatusCode())
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("get %s: service name not ready after %s", tierName, k8sServiceNameMaxWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(k8sServiceNamePollInterval):
		}
	}
}

func k8sContainerMetadataName(tierName, stackID string) string {
	// metadataName must include stackID, not just tierName ("db"/"app"/"web").
	// tierName is only the tier role; it is identical for every provisioned stack.
	// Using it alone would make Deployment/Service names collide when two catalog
	// instances (or any two stacks) run in the same namespace. stackID is the
	// per-stack id from SPRM (e.g. placement resource UUID), so tier+stackID stays unique.
	metadataName := tierName + "-" + stackID
	// k8s SP turns this into GenerateName (<metadataName>- + random suffix),
	// so keep a 6-char budget and avoid a trailing '-' after truncation.
	if len(metadataName) > k8sContainerMetadataNameMaxLen {
		metadataName = strings.TrimRight(metadataName[:k8sContainerMetadataNameMaxLen], "-")
	}
	return metadataName
}

// createK8sStackTier POSTs a single tier. It does not roll back on error; the caller
// must delete *ids (plus the new id is not appended until success).
func (h *HTTPClient) createK8sStackTier(ctx context.Context, ids *[]string, stackID, tierName, id, image string, ports []k8sapi.ContainerPort, process *k8sapi.ContainerProcess, res k8sapi.ContainerResources) error {
	metadataName := k8sContainerMetadataName(tierName, stackID)
	body := k8sapi.Container{
		Spec: k8sapi.ContainerSpec{
			ServiceType: k8sapi.ContainerSpecServiceTypeContainer,
			Metadata:    k8sapi.ContainerMetadata{Name: metadataName},
			Image:       k8sapi.ContainerImage{Reference: image},
			Resources:   res,
			Network:     &k8sapi.ContainerNetwork{Ports: &ports},
		},
	}
	if process != nil {
		body.Spec.Process = process
	}
	idParam := id
	resp, err := h.Client.CreateContainerWithResponse(ctx, &k8sapi.CreateContainerParams{Id: &idParam}, body)
	if err != nil {
		return fmt.Errorf("create %s: %w", tierName, err)
	}
	switch resp.StatusCode() {
	case http.StatusCreated:
		*ids = append(*ids, id)
		return nil
	case http.StatusConflict:
		return ErrConflict
	default:
		detail := k8sCreateContainerFailureDetail(resp)
		if detail != "" {
			return fmt.Errorf("create %s: unexpected status %d: %s", tierName, resp.StatusCode(), detail)
		}
		return fmt.Errorf("create %s: unexpected status %d", tierName, resp.StatusCode())
	}
}

// createStackContainers provisions db → app → web and returns container ids created so far.
// On any error, ids lists tiers successfully created in this call (for rollback by the caller).
func (h *HTTPClient) createStackContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) ([]string, error) {
	dbPort := 5432
	if spec.Database.Engine == "mysql" {
		dbPort = 3306
	}
	appPort := 8080
	if spec.App.HttpPort != nil {
		appPort = *spec.App.HttpPort
	}
	var webVis k8sapi.ContainerPortVisibility
	if h.webExposure == config.WebExposureOpenShift {
		webVis = k8sapi.Internal
	} else {
		webVis = k8sapi.External
	}
	dbRes := k8sapi.ContainerResources{
		Cpu:    k8sapi.ContainerCpu{Min: 2, Max: 4},
		Memory: k8sapi.ContainerMemory{Min: "1GB", Max: "2GB"},
	}
	appRes := k8sapi.ContainerResources{
		Cpu:    k8sapi.ContainerCpu{Min: 2, Max: 8},
		Memory: k8sapi.ContainerMemory{Min: "2GB", Max: "4GB"},
	}
	webRes := k8sapi.ContainerResources{
		Cpu:    k8sapi.ContainerCpu{Min: 1, Max: 4},
		Memory: k8sapi.ContainerMemory{Min: "512MB", Max: "1GB"},
	}
	ids := make([]string, 0, 3)

	// 1) DB — no cross-tier deps; service name is read via GET (create may omit service info).
	dbID := stackID + "-db"
	if err := h.createK8sStackTier(ctx, &ids, stackID, "db", dbID, dbImageFromSpec(spec.Database), tierPorts(spec.Database.Network, dbPort, k8sapi.Internal), h.k8sProcessEnvForDatabase(spec.Database), dbRes); err != nil {
		slog.ErrorContext(ctx, "create db container", "stack_id", stackID, "err", err)
		return ids, err
	}
	dbServiceName, err := h.waitForContainerServiceName(ctx, "db", dbID)
	if err != nil {
		slog.ErrorContext(ctx, "get db service name", "stack_id", stackID, "err", err)
		return ids, err
	}

	// 2) App — JDBC target is the DB Service name from GET.
	appID := stackID + "-app"
	if err = h.createK8sStackTier(ctx, &ids, stackID, "app", appID, spec.App.Image, tierPorts(spec.App.Network, 8080, k8sapi.Internal), h.k8sProcessEnvForApp(dbServiceName, spec.Database), appRes); err != nil {
		slog.ErrorContext(ctx, "create app container", "stack_id", stackID, "err", err)
		return ids, err
	}
	appServiceName, err := h.waitForContainerServiceName(ctx, "app", appID)
	if err != nil {
		slog.ErrorContext(ctx, "get app service name", "stack_id", stackID, "err", err)
		return ids, err
	}

	// 3) Web — nginx proxy target is the app Service name.
	webID := stackID + "-web"
	if err = h.createK8sStackTier(ctx, &ids, stackID, "web", webID, spec.Web.Image, tierPorts(spec.Web.Network, 80, webVis), k8sProcessForWeb(appServiceName, appPort), webRes); err != nil {
		slog.ErrorContext(ctx, "create web container", "stack_id", stackID, "err", err)
		return ids, err
	}
	return ids, nil
}

func (h *HTTPClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) error {
	ids, err := h.createStackContainers(ctx, stackID, spec)
	if err != nil {
		_ = deleteContainerIDs(ctx, h.Client, ids)
		return err
	}
	return nil
}

func (h *HTTPClient) DeleteContainers(ctx context.Context, stackID string) error {
	if h.openShiftRoutes != nil {
		if err := h.openShiftRoutes.deleteRoute(ctx, stackID); err != nil {
			slog.WarnContext(ctx, "delete openshift route", "stack_id", stackID, "err", err)
		}
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
	webID := stackID + "-web"
	resp, err := h.Client.GetContainerWithResponse(ctx, webID)
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil
	}
	svc := resp.JSON200.Service
	if h.webExposure == config.WebExposureOpenShift {
		webServiceName := k8sContainerServiceName(resp.JSON200)
		if webServiceName == "" {
			slog.WarnContext(ctx, "web container has no service name", "stack_id", stackID)
			return nil
		}
		u, err := h.openShiftRoutes.ensureWebRoute(ctx, stackID, webServiceName)
		if err != nil {
			slog.WarnContext(ctx, "ensure openshift web route", "stack_id", stackID, "err", err)
			return nil
		}
		return u
	}
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
