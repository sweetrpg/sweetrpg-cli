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
const FileName = "catalog-cli.yaml"

// Config holds resolved settings for one command run.
type Config struct {
	APIURL string
	Output string
}

// Sources carries the raw inputs to resolution. Getenv and HomeDir are
// injectable so tests never touch process state.
type Sources struct {
	FlagAPIURL string
	Getenv     func(string) string
	HomeDir    func() (string, error)
}

// Load resolves configuration by precedence: flag > environment > config file.
// A missing base URL yields an error naming every source so users can fix it
// without reading docs.
func Load(s Sources) (*Config, error) {
	file, err := loadFile(s.HomeDir)
	if err != nil {
		return nil, err
	}

	apiURL := firstNonEmpty(s.FlagAPIURL, s.Getenv("SWEETRPG_CATALOG_API_URL"), file.APIURL)
	if apiURL == "" {
		return nil, fmt.Errorf(
			"no catalog API base URL configured: pass --api-url, export SWEETRPG_CATALOG_API_URL, or set api-url in %s",
			filePath(s.HomeDir),
		)
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid catalog API base URL %q: must be an http(s) URL with a host", apiURL)
	}

	output := file.Output
	if output == "" {
		output = DefaultOutputFormat
	}
	return &Config{APIURL: apiURL, Output: output}, nil
}

type fileConfig struct {
	APIURL string `yaml:"api-url"`
	Output string `yaml:"output"`
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
	fc.APIURL = strings.TrimSpace(fc.APIURL)
	fc.Output = strings.TrimSpace(fc.Output)
	return &fc, nil
}

// filePath returns ~/.config/sweetrpg/catalog-cli.yaml, or "" when no home
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
