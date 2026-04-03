package containerclient

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
)

// PodmanClient creates real containers via Podman for local testing.
// Requires: podman installed and running.
// StackDB supplies credentials (from config / .env); zero value is not used from main.
// WebHostPort, when non-empty (e.g. "8081"), publishes the web tier container to that host
// port so the nginx proxy is reachable from the host. Leave empty to not publish.
type PodmanClient struct {
	StackDB     config.StackDBConfig
	WebHostPort string
}

func containerNames(stackID string) (db, app, web string) {
	return stackID + "-db", stackID + "-app", stackID + "-web"
}

func networkName(stackID string) string {
	return stackID + "-net"
}

// dbImageFromSpec maps engine+version to OCI image (e.g. postgres+16 -> docker.io/library/postgres:16).
func dbImageFromSpec(db v1alpha1.DatabaseTierSpec) string {
	switch db.Engine {
	case "postgres":
		return "docker.io/library/postgres:" + db.Version
	case "mysql":
		return "docker.io/library/mysql:" + db.Version
	default:
		return "docker.io/library/postgres:17"
	}
}

func (p *PodmanClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) (dbID, appID, webID string, err error) {
	dbName, appName, webName := containerNames(stackID)
	netName := networkName(stackID)
	slog.Info("[podman] creating stack", "stack", stackID, "network", netName)

	// --ignore: succeed even if network already exists (ygalblum).
	if out, err := exec.CommandContext(ctx, "podman", "network", "create", "--ignore", netName).CombinedOutput(); err != nil {
		return "", "", "", fmt.Errorf("create network: %w: %s", err, string(out))
	}

	// 1. DB container (Postgres or MySQL)
	c := p.StackDB
	var dbEnv []string
	switch spec.Database.Engine {
	case "mysql":
		dbEnv = []string{
			"MYSQL_ROOT_PASSWORD=" + c.Password,
			"MYSQL_DATABASE=" + c.DatabaseName,
		}
	default:
		dbEnv = []string{
			"POSTGRES_PASSWORD=" + c.Password,
			"POSTGRES_DB=" + c.DatabaseName,
		}
	}
	args := []string{"run", "-d", "--name", dbName, "--network", netName}
	for _, e := range dbEnv {
		args = append(args, "-e", e)
	}
	args = append(args, dbImageFromSpec(spec.Database))
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return "", "", "", fmt.Errorf("create db container: %w: %s", err, string(out))
	}

	// Give the DB a moment to initialise.
	select {
	case <-ctx.Done():
		podmanRollback(dbName)
		return "", "", "", ctx.Err()
	case <-time.After(3 * time.Second):
	}

	// 2. App container (Spring Pet Clinic)
	var appEnv []string
	switch spec.Database.Engine {
	case "mysql":
		appEnv = []string{
			"SPRING_DATASOURCE_URL=jdbc:mysql://" + dbName + ":3306/" + c.DatabaseName,
			"SPRING_DATASOURCE_USERNAME=" + c.MysqlUser,
			"SPRING_DATASOURCE_PASSWORD=" + c.Password,
		}
	default:
		appEnv = []string{
			"SPRING_DATASOURCE_URL=jdbc:postgresql://" + dbName + ":5432/" + c.DatabaseName,
			"SPRING_DATASOURCE_USERNAME=" + c.PostgresUser,
			"SPRING_DATASOURCE_PASSWORD=" + c.Password,
		}
	}
	args = []string{"run", "-d", "--name", appName, "--network", netName}
	for _, e := range appEnv {
		args = append(args, "-e", e)
	}
	args = append(args, spec.App.Image)
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		podmanRollback(dbName)
		return "", "", "", fmt.Errorf("create app container: %w: %s", err, string(out))
	}

	// 3. Web container (nginx) – proxy to app container.
	nginxConf := fmt.Sprintf(
		"server { listen 80; location / { proxy_pass http://%s:8080; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; } }\n",
		appName,
	)
	confDir := filepath.Join(os.TempDir(), "3tier-nginx", stackID)
	if err := os.MkdirAll(confDir, 0755); err != nil {
		podmanRollback(appName, dbName)
		return "", "", "", fmt.Errorf("create nginx config dir: %w", err)
	}
	confPath := filepath.Join(confDir, "default.conf")
	if err := os.WriteFile(confPath, []byte(nginxConf), 0644); err != nil {
		podmanRollback(appName, dbName)
		return "", "", "", fmt.Errorf("write nginx config: %w", err)
	}

	// Build podman run args; publish host port only when WebHostPort is set.
	// Avoid hardcoding 9080 which conflicts with the api-gateway workspace
	// service (ygalblum). Default: no host publish.
	args = []string{"run", "-d", "--name", webName, "--network", netName}
	if p.WebHostPort != "" {
		args = append(args, "-p", p.WebHostPort+":80")
	}
	args = append(args, "-v", confPath+":/etc/nginx/conf.d/default.conf:ro,z", spec.Web.Image)
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		podmanRollback(appName, dbName)
		return "", "", "", fmt.Errorf("create web container: %w: %s", err, string(out))
	}

	slog.Info("[podman] created containers", "db", dbName, "app", appName, "web", webName)
	return dbName, appName, webName, nil
}

// GetWebEndpoint returns http://localhost:{WebHostPort} when a host port is configured,
// otherwise nil (Podman containers are not reachable from outside the host by default).
func (p *PodmanClient) GetWebEndpoint(_ context.Context, _ string) *string {
	if p.WebHostPort == "" {
		return nil
	}
	url := "http://localhost:" + p.WebHostPort
	return &url
}

// GetStatus returns the worst status among the 3 containers (FAILED > PENDING > RUNNING).
func (p *PodmanClient) GetStatus(ctx context.Context, stackID string) (v1alpha1.ThreeTierAppStatus, bool) {
	dbName, appName, webName := containerNames(stackID)
	var states []string
	for _, name := range []string{dbName, appName, webName} {
		out, err := exec.CommandContext(ctx, "podman", "inspect", "-f", "{{.State.Status}}", name).CombinedOutput()
		if err != nil {
			return v1alpha1.FAILED, true
		}
		states = append(states, string(out))
	}
	return WorstStatusFromPodmanStates(states)
}

func (p *PodmanClient) DeleteContainers(ctx context.Context, stackID string) error {
	dbName, appName, webName := containerNames(stackID)
	netName := networkName(stackID)
	for _, name := range []string{webName, appName, dbName} {
		_ = exec.CommandContext(ctx, "podman", "rm", "-f", name).Run()
	}
	_ = exec.CommandContext(ctx, "podman", "network", "rm", netName).Run()
	_ = os.RemoveAll(filepath.Join(os.TempDir(), "3tier-nginx", stackID))
	return nil
}

// podmanRollback removes containers by name using a background context so it
// runs even when the original context is cancelled (ygalblum).
func podmanRollback(names ...string) {
	for _, name := range names {
		_ = exec.CommandContext(context.Background(), "podman", "rm", "-f", name).Run()
	}
}
