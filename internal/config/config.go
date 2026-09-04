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
	// Services holds each configured service's base URL from the config
	// file, keyed by its camelCase service name (e.g. "gameRoom").
	Services map[string]string
	// FilePath is the resolved config file path, or "" when no home
	// directory is available.
	FilePath string
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

	// Optional: only asset commands need it, so absence is fine and validated
	// only when something is set.
	assetsWebURL := firstNonEmpty(s.FlagAssetsWebURL, s.Getenv("SWEETRPG_ASSETS_WEB_URL"), file.AssetsWebURL)
	if assetsWebURL != "" {
		if err := validateURL("assets web", assetsWebURL); err != nil {
			return nil, err
		}
	}

	output := file.Output
	if output == "" {
		output = DefaultOutputFormat
	}
	return &Config{
		AssetsWebURL: assetsWebURL,
		Output:       output,
		Services:     file.Services,
		FilePath:     filePath(s.HomeDir),
	}, nil
}

// ServiceURL resolves a named service's base URL by precedence: flagOverride
// (a command's own --api-url flag, if it has one) > the service's
// SWEETRPG_<SERVICE>_API_URL environment variable > the config file's
// services.<service> entry. service is given in kebab-case (e.g.
// "game-room"); the config file key is its camelCase form ("gameRoom").
// SWEETRPG_CATALOG_API_URL is preserved as the env var name for "catalog".
func (c *Config) ServiceURL(getenv func(string) string, flagOverride, service string) (string, error) {
	key := serviceConfigKey(service)
	apiURL := firstNonEmpty(flagOverride, getenv(serviceEnvVar(service)), c.Services[key])
	if apiURL == "" {
		return "", fmt.Errorf(
			"no %s API base URL configured: pass --api-url, export %s, or set services.%s in %s",
			service, serviceEnvVar(service), key, c.FilePath,
		)
	}
	if err := validateURL(service, apiURL); err != nil {
		return "", err
	}
	return apiURL, nil
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
	Services     map[string]string `yaml:"services"`
	AssetsWebURL string            `yaml:"assets-web-url"`
	Output       string            `yaml:"output"`
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
	fc.AssetsWebURL = strings.TrimSpace(fc.AssetsWebURL)
	fc.Output = strings.TrimSpace(fc.Output)
	for k, v := range fc.Services {
		fc.Services[k] = strings.TrimSpace(v)
	}
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
