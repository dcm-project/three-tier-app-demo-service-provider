// Package config loads service configuration from environment variables using
// github.com/caarlos0/env/v11 (per ygalblum review).
package config

import (
	"fmt"
	"strings"

	env "github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Allowed values for SP_WEB_EXPOSURE (validated in [Config.Validate]).
const (
	WebExposureKubernetes = "kubernetes"
	WebExposureOpenShift  = "openshift"
)

// Config holds all service configuration.
type Config struct {
	SVCAddress          string `env:"SVC_ADDRESS"          envDefault:":8080"`
	SVCLogLevel         string `env:"SVC_LOG_LEVEL"        envDefault:"info"`
	ContainerSPURL      string `env:"CONTAINER_SP_URL"`
	DevContainerBackend string `env:"DEV_CONTAINER_BACKEND"`
	// WebExposure selects how the web tier is published: "kubernetes" (LoadBalancer/NodePort via k8s SP)
	// or "openshift" (ClusterIP Service + OpenShift Route created by this SP). Requires kube access for Route.
	WebExposure string `env:"SP_WEB_EXPOSURE" envDefault:"kubernetes"`
	// OpenShiftRouteNamespace is the namespace where the k8s container SP creates Services (OpenShift Route exposure).
	// Defaults to "default", matching the k8s container SP's usual NAMESPACE.
	OpenShiftRouteNamespace string `env:"SP_OPENSHIFT_ROUTE_NAMESPACE" envDefault:"default"`
	// OpenShiftKubeconfig is an optional kubeconfig path for Route create/delete; empty uses default loading (e.g. KUBECONFIG).
	OpenShiftKubeconfig string `env:"SP_OPENSHIFT_KUBECONFIG" envDefault:""`
	// PodmanWebHostPort publishes the nginx web container to this host port
	// when using the Podman backend (e.g. "8081"). Empty = no host publish,
	// avoiding conflicts with other services (ygalblum).
	PodmanWebHostPort string         `env:"PODMAN_WEB_HOST_PORT"`
	StackDB           StackDBCfg     `envPrefix:"TIER_STACK_"`
	Provider          ProviderConfig `envPrefix:"SP_"`
	DCM               DCMConfig      `envPrefix:"DCM_"`
	NATS              NATSConfig     `envPrefix:"SP_NATS_"`
	Store             StoreConfig    `envPrefix:"DB_"`
}

// StackDBCfg holds credentials for the provisioned DB/app tiers.
type StackDBCfg struct {
	Password     string `env:"DB_PASSWORD"    envDefault:"petclinic"`
	DatabaseName string `env:"DB_NAME"        envDefault:"petclinic"`
	PostgresUser string `env:"POSTGRES_USER"  envDefault:"postgres"`
	MysqlUser    string `env:"MYSQL_USER"     envDefault:"root"`
}

// DCMConfig holds DCM registry connection settings.
type DCMConfig struct {
	RegistrationURL string `env:"REGISTRATION_URL"`
}

// NATSConfig holds NATS connection settings for status event publishing.
type NATSConfig struct {
	URL string `env:"URL"`
}

// ProviderConfig holds SP identity for self-registration.
type ProviderConfig struct {
	Name        string `env:"NAME"`
	DisplayName string `env:"DISPLAY_NAME"`
	Endpoint    string `env:"ENDPOINT"`
	Region      string `env:"REGION"`
	Zone        string `env:"ZONE"`
}

// StoreConfig holds the SP's own persistence settings.
// DB_TYPE selects the backend: "pgsql" (default) or "sqlite".
type StoreConfig struct {
	Type string `env:"TYPE" envDefault:"pgsql"`
	// Path is the SQLite database file path (used when TYPE=sqlite).
	Path string `env:"PATH" envDefault:"three-tier-sp.db"`
	Host string `env:"HOST" envDefault:"localhost"`
	Port string `env:"PORT" envDefault:"5432"`
	Name string `env:"NAME" envDefault:"three-tier-sp"`
	User string `env:"USER" envDefault:"admin"`
	Pass string `env:"PASS"`
}

// Load reads configuration from environment variables.
// If a .env file exists in the current working directory it is loaded first
// (missing file is OK). The file is loaded via godotenv before env parsing so
// values are visible to the caarlos0/env parser.
func Load() (Config, error) {
	_ = godotenv.Load()
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("loading config: %w", err)
	}
	cfg.WebExposure = strings.TrimSpace(cfg.WebExposure)
	if cfg.WebExposure == "" {
		cfg.WebExposure = WebExposureKubernetes
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration values that env parsing alone does not constrain.
func (c Config) Validate() error {
	switch c.WebExposure {
	case WebExposureKubernetes, WebExposureOpenShift:
		return nil
	default:
		return fmt.Errorf("invalid SP_WEB_EXPOSURE %q (valid: %q, %q)",
			c.WebExposure, WebExposureKubernetes, WebExposureOpenShift)
	}
}

// RegistrationEnabled returns true when all required registration fields are set.
func (c Config) RegistrationEnabled() bool {
	return c.DCM.RegistrationURL != "" && c.Provider.Name != "" && c.Provider.Endpoint != ""
}
