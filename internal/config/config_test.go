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

const fileWithBoth = "services:\n  catalog: https://file.example.com\noutput: json\n"

func TestServiceURLPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		env       string
		fileBody  string
		wantURL   string
		wantError string
	}{
		{
			name:     "flag wins over env and file",
			flag:     "https://flag.example.com",
			env:      "https://env.example.com",
			fileBody: fileWithBoth,
			wantURL:  "https://flag.example.com",
		},
		{
			name:     "env wins over file",
			env:      "https://env.example.com",
			fileBody: fileWithBoth,
			wantURL:  "https://env.example.com",
		},
		{
			name:     "file provides URL",
			fileBody: fileWithBoth,
			wantURL:  "https://file.example.com",
		},
		{
			name:    "missing file is not an error when env set",
			env:     "https://env.example.com",
			wantURL: "https://env.example.com",
		},
		{
			name:      "nothing set names all three sources",
			wantError: "--api-url, export SWEETRPG_CATALOG_API_URL, or set services.catalog",
		},
		{
			name:      "blank values treated as unset",
			flag:      "   ",
			fileBody:  "services:\n  catalog: \"\"\n",
			wantError: "no catalog API base URL configured",
		},
		{
			name:      "invalid yaml is reported with path context",
			fileBody:  "services: [unclosed\n",
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

			cfg, err := Load(Sources{Getenv: getenv, HomeDir: home})
			if err != nil {
				if tt.wantError != "" && strings.Contains(err.Error(), tt.wantError) {
					return
				}
				t.Fatalf("unexpected Load error: %v", err)
			}

			gotURL, err := cfg.ServiceURL(getenv, tt.flag, "catalog")
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
			if gotURL != tt.wantURL {
				t.Errorf("ServiceURL = %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

func TestLoadOutputPrecedence(t *testing.T) {
	home := writeFile(t, fileWithBoth)
	cfg, err := Load(Sources{Getenv: func(string) string { return "" }, HomeDir: home})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Output != "json" {
		t.Errorf("Output = %q, want json", cfg.Output)
	}
}

func TestLoadWithoutHomeDirectoryStillResolvesFromEnv(t *testing.T) {
	getenv := func(string) string { return "https://env.example.com" }
	cfg, err := Load(Sources{Getenv: getenv, HomeDir: noHome})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotURL, err := cfg.ServiceURL(getenv, "", "catalog")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotURL != "https://env.example.com" {
		t.Errorf("ServiceURL = %q, want https://env.example.com", gotURL)
	}
}

func TestServiceURLUnsetErrorIncludesFilePath(t *testing.T) {
	home := writeFile(t, "")
	cfg, err := Load(Sources{Getenv: func(string) string { return "" }, HomeDir: home})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = cfg.ServiceURL(func(string) string { return "" }, "", "catalog")
	if err == nil {
		t.Fatal("want error for unset URL")
	}
	want := filepath.Join(".config", "sweetrpg", FileName)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}

func TestServiceURLForNonCatalogService(t *testing.T) {
	fileBody := "services:\n  gameRoom: https://gr.file.example.com\n"
	home := writeFile(t, fileBody)
	cfg, err := Load(Sources{Getenv: func(string) string { return "" }, HomeDir: home})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("resolves from file by camelCase key", func(t *testing.T) {
		gotURL, err := cfg.ServiceURL(func(string) string { return "" }, "", "game-room")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotURL != "https://gr.file.example.com" {
			t.Errorf("ServiceURL = %q, want https://gr.file.example.com", gotURL)
		}
	})

	t.Run("resolves from SWEETRPG_GAME_ROOM_API_URL env var", func(t *testing.T) {
		getenv := func(key string) string {
			if key == "SWEETRPG_GAME_ROOM_API_URL" {
				return "https://gr.env.example.com"
			}
			return ""
		}
		gotURL, err := cfg.ServiceURL(getenv, "", "game-room")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotURL != "https://gr.env.example.com" {
			t.Errorf("ServiceURL = %q, want https://gr.env.example.com", gotURL)
		}
	})

	t.Run("unconfigured service names all three sources", func(t *testing.T) {
		_, err := cfg.ServiceURL(func(string) string { return "" }, "", "users")
		if err == nil || !strings.Contains(err.Error(), "SWEETRPG_USERS_API_URL") {
			t.Fatalf("want error naming SWEETRPG_USERS_API_URL, got %v", err)
		}
		if !strings.Contains(err.Error(), "services.users") {
			t.Fatalf("want error naming services.users, got %v", err)
		}
	})
}

func TestAssetsWebURLResolution(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		env       string
		fileBody  string
		want      string
		wantError string
	}{
		{
			name:     "flag wins",
			flag:     "https://a-flag.example.com",
			env:      "https://a-env.example.com",
			fileBody: "assets-web-url: https://a-file.example.com\nservices:\n  catalog: https://api.example.com\n",
			want:     "https://a-flag.example.com",
		},
		{
			name:     "env wins over file",
			env:      "https://a-env.example.com",
			fileBody: "assets-web-url: https://a-file.example.com\nservices:\n  catalog: https://api.example.com\n",
			want:     "https://a-env.example.com",
		},
		{
			name:     "file provides value",
			fileBody: "assets-web-url:  https://a-file.example.com \nservices:\n  catalog: https://api.example.com\n",
			want:     "https://a-file.example.com",
		},
		{
			name:     "absent is allowed - only asset commands need it",
			fileBody: fileWithBoth,
			want:     "",
		},
		{
			name:      "invalid value rejected when set",
			env:       "not-a-url",
			wantError: "invalid assets web base URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := writeFile(t, tt.fileBody)
			getenv := func(key string) string {
				if key == "SWEETRPG_ASSETS_WEB_URL" {
					return tt.env
				}
				return ""
			}

			cfg, err := Load(Sources{
				FlagAssetsWebURL: tt.flag,
				Getenv:           getenv,
				HomeDir:          home,
			})

			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("want error containing %q, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.AssetsWebURL != tt.want {
				t.Errorf("AssetsWebURL = %q, want %q", cfg.AssetsWebURL, tt.want)
			}
		})
	}
}
