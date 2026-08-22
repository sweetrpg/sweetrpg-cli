package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noHome() (string, error) { return "", errors.New("no home") }

func writeFile(t *testing.T, content string) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		sub := filepath.Join(dir, ".config", "sweetrpg")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, error) { return dir, nil }
}

const fileWithBoth = "api-url: https://file.example.com\noutput: json\n"

func TestLoadPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		env       string
		fileBody  string
		wantURL   string
		wantOut   string
		wantError string
	}{
		{
			name:     "flag wins over env and file",
			flag:     "https://flag.example.com",
			env:      "https://env.example.com",
			fileBody: fileWithBoth,
			wantURL:  "https://flag.example.com",
			wantOut:  "json",
		},
		{
			name:     "env wins over file",
			env:      "https://env.example.com",
			fileBody: fileWithBoth,
			wantURL:  "https://env.example.com",
			wantOut:  "json",
		},
		{
			name:     "file provides URL",
			fileBody: fileWithBoth,
			wantURL:  "https://file.example.com",
			wantOut:  "json",
		},
		{
			name:    "missing file is not an error when env set",
			env:     "https://env.example.com",
			wantURL: "https://env.example.com",
			wantOut: DefaultOutputFormat,
		},
		{
			name:      "nothing set names all three sources",
			wantError: "--api-url, export SWEETRPG_CATALOG_API_URL, or set api-url",
		},
		{
			name:      "blank values treated as unset",
			flag:      "   ",
			fileBody:  "api-url: \"\"\n",
			wantError: "no catalog API base URL configured",
		},
		{
			name:      "invalid yaml is reported with path context",
			fileBody:  "api-url: [unclosed\n",
			wantError: "parsing config file",
		},
		{
			name:      "non-http scheme rejected",
			flag:      "ftp://weird.example.com",
			wantError: "must be an http(s) URL",
		},
		{
			name:      "url without host rejected",
			flag:      "not-a-url",
			wantError: "must be an http(s) URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := writeFile(t, tt.fileBody)
			getenv := func(string) string { return tt.env }

			cfg, err := Load(Sources{FlagAPIURL: tt.flag, Getenv: getenv, HomeDir: home})

			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("want error containing %q, got %q", tt.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.APIURL != tt.wantURL {
				t.Errorf("APIURL = %q, want %q", cfg.APIURL, tt.wantURL)
			}
			if cfg.Output != tt.wantOut {
				t.Errorf("Output = %q, want %q", cfg.Output, tt.wantOut)
			}
		})
	}
}

func TestLoadWithoutHomeDirectoryStillResolvesFromEnv(t *testing.T) {
	cfg, err := Load(Sources{
		Getenv:  func(string) string { return "https://env.example.com" },
		HomeDir: noHome,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIURL != "https://env.example.com" {
		t.Errorf("APIURL = %q, want https://env.example.com", cfg.APIURL)
	}
}

func TestLoadUnsetURLErrorIncludesFilePath(t *testing.T) {
	home := writeFile(t, "")
	_, err := Load(Sources{Getenv: func(string) string { return "" }, HomeDir: home})
	if err == nil {
		t.Fatal("want error for unset URL")
	}
	want := filepath.Join(".config", "sweetrpg", FileName)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}
