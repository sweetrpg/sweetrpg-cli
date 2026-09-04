// Package config resolves CLI configuration from flags, environment, and the
// config file. Only non-secret settings live in the file; tokens go to the OS
// keychain.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultOutputFormat is used when neither flag nor config file names one.
const DefaultOutputFormat = "human"

// FileName is the config file name relative to ~/.config.
const FileName = "cli.yaml"

// Config holds resolved settings for one command run.
type Config struct {
	AssetsWebURL string
	Output       string
	// BaseURL is the config file's baseURL - every service.<service> entry
	// in the config file is a path resolved against it (e.g. baseURL
	// "https://dev.sweetrpg.com" + services.catalog "/api/0/catalog").
	BaseURL string
	// Services holds each configured service's path override from the
	// config file, keyed by its camelCase service name (e.g. "gameRoom"),
	// resolved against BaseURL. An env var or flag override is always a
	// full URL instead - see ServiceURL.
	Services map[string]string
	// FilePath is the resolved config file path, or "" when no home
	// directory is available.
	FilePath string
	// AuthDomain, AuthClientID, and AuthAudience are the config file's
	// authTenant section, letting an operator repoint the Auth0 tenant
	// without a rebuild. See auth.ResolveConfig for the full precedence.
	AuthDomain   string
	AuthClientID string
	AuthAudience string
}

// Sources carries the raw inputs to resolution. Getenv and HomeDir are
// injectable so tests never touch process state.
type Sources struct {
	FlagAssetsWebURL string
	Getenv           func(string) string
	HomeDir          func() (string, error)
}

// Load resolves configuration by precedence: flag > environment > config file.
// Per-service base URLs are validated lazily via Config.ServiceURL, not here,
// since which services a given command run needs varies by command.
func Load(s Sources) (*Config, error) {
	file, err := loadFile(s.HomeDir)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Output:       file.Output,
		BaseURL:      file.BaseURL,
		Services:     file.Services,
		FilePath:     filePath(s.HomeDir),
		AuthDomain:   file.AuthTenant.Domain,
		AuthClientID: file.AuthTenant.ClientID,
		AuthAudience: file.AuthTenant.Audience,
	}
	if cfg.Output == "" {
		cfg.Output = DefaultOutputFormat
	}

	// Optional: only asset commands need it, so absence is fine and validated
	// only when something is set. "assets-web" isn't itself a platform API,
	// but resolves the same way (env/flag full URL, or a services.assetsWeb
	// path against baseURL) since it's a network base URL like the rest.
	assetsWebURL, err := cfg.serviceURL(s.Getenv, s.FlagAssetsWebURL, "assets-web", "SWEETRPG_ASSETS_WEB_URL")
	if err != nil && !errors.Is(err, errServiceUnconfigured) {
		return nil, err
	}
	cfg.AssetsWebURL = assetsWebURL

	return cfg, nil
}

// errServiceUnconfigured marks "nothing set this service" so Load can treat
// an absent (optional) assets-web URL as fine while still surfacing a real
// validation error (e.g. a malformed URL) to the caller.
var errServiceUnconfigured = errors.New("service not configured")

// ServiceURL resolves a named service's base URL by precedence: flagOverride
// (a command's own --api-url flag, if it has one) > the service's
// SWEETRPG_<SERVICE>_API_URL environment variable, both taken as a full URL
// > the config file's services.<service> entry, taken as a path and
// resolved against BaseURL. service is given in kebab-case (e.g.
// "game-room"); the config file key is its camelCase form ("gameRoom").
// SWEETRPG_CATALOG_API_URL is preserved as the env var name for "catalog".
func (c *Config) ServiceURL(getenv func(string) string, flagOverride, service string) (string, error) {
	apiURL, err := c.serviceURL(getenv, flagOverride, service, serviceEnvVar(service))
	if errors.Is(err, errServiceUnconfigured) {
		return "", fmt.Errorf(
			"no %s API base URL configured: pass --api-url, export %s, or set baseURL and services.%s in %s",
			service, serviceEnvVar(service), serviceConfigKey(service), c.FilePath,
		)
	}
	return apiURL, err
}

// serviceURL is the shared resolver behind ServiceURL and Load's assets-web
// handling: flagOverride/env are taken as full URLs, the config file entry
// as a path joined onto BaseURL. Returns errServiceUnconfigured (wrapped)
// when nothing at all is set, so callers can distinguish "unconfigured"
// from "configured but invalid".
func (c *Config) serviceURL(getenv func(string) string, flagOverride, service, envVar string) (string, error) {
	if full := firstNonEmpty(flagOverride, getenv(envVar)); full != "" {
		if err := validateURL(service, full); err != nil {
			return "", err
		}
		return full, nil
	}

	path := c.Services[serviceConfigKey(service)]
	if path == "" {
		return "", errServiceUnconfigured
	}
	if c.BaseURL == "" {
		return "", fmt.Errorf("services.%s is set but no baseURL is configured in %s", serviceConfigKey(service), c.FilePath)
	}
	full := strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
	if err := validateURL(service, full); err != nil {
		return "", err
	}
	return full, nil
}

// serviceConfigKey converts a kebab-case service name to its camelCase
// config file key, e.g. "game-room" -> "gameRoom".
func serviceConfigKey(service string) string {
	parts := strings.Split(service, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// serviceEnvVar converts a kebab-case service name to its environment
// variable name, e.g. "game-room" -> "SWEETRPG_GAME_ROOM_API_URL".
func serviceEnvVar(service string) string {
	return "SWEETRPG_" + strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_API_URL"
}

func validateURL(label, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid %s base URL %q: must be an http(s) URL with a host", label, raw)
	}
	return nil
}

type fileConfig struct {
	// BaseURL is the host every services.<service> path resolves against.
	BaseURL string `yaml:"baseURL"`
	// Services holds each service's path override, including "assetsWeb" -
	// not itself a platform API, but a path under baseURL like the rest.
	Services   map[string]string `yaml:"services"`
	Output     string            `yaml:"output"`
	AuthTenant authTenantFile    `yaml:"authTenant"`
}

// authTenantFile lets an operator repoint the Auth0 tenant without a
// rebuild; see auth.ResolveConfig for how it combines with env vars and the
// build-time defaults.
type authTenantFile struct {
	Domain   string `yaml:"domain"`
	ClientID string `yaml:"clientId"`
	Audience string `yaml:"audience"`
}

func loadFile(homeDir func() (string, error)) (*fileConfig, error) {
	path := filePath(homeDir)
	if path == "" {
		return &fileConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &fileConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	fc.BaseURL = strings.TrimSpace(fc.BaseURL)
	fc.Output = strings.TrimSpace(fc.Output)
	for k, v := range fc.Services {
		fc.Services[k] = strings.TrimSpace(v)
	}
	fc.AuthTenant.Domain = strings.TrimSpace(fc.AuthTenant.Domain)
	fc.AuthTenant.ClientID = strings.TrimSpace(fc.AuthTenant.ClientID)
	fc.AuthTenant.Audience = strings.TrimSpace(fc.AuthTenant.Audience)
	return &fc, nil
}

// filePath returns ~/.config/sweetrpg/cli.yaml, or "" when no home
// directory is available (the file layer is then simply absent).
func filePath(homeDir func() (string, error)) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "sweetrpg", FileName)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
