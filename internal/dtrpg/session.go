package dtrpg

import (
	"context"
	"fmt"

	sdk "github.com/pilgrimagesoftware/dtrpg-sdk.go"
	"github.com/pilgrimagesoftware/dtrpg-sdk.go/auth"
	"github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
)

// Session is an authenticated DriveThruRPG library client. The JWT it holds is
// never persisted; it dies with the process, and each run re-exchanges the
// stored application key for a fresh one.
type Session struct {
	lib *library.Client
}

// NewSession exchanges appKey for a DriveThruRPG session and returns a library
// client bound to it. baseURL overrides the production API endpoint (tests
// point it at a fixture server); pass "" for production.
func NewSession(ctx context.Context, appKey, baseURL string) (*Session, error) {
	if appKey == "" {
		return nil, ErrNoKey
	}
	installUserAgentTransport()
	cfg := library.NewConfig(appKey)
	if baseURL != "" {
		cfg = library.NewConfigWithBaseURL(appKey, baseURL)
	}

	client := sdk.NewSdkWithConfig(cfg)
	token, err := auth.Authenticate(ctx, appKey, cfg)
	if err != nil {
		return nil, fmt.Errorf("DriveThruRPG login failed: %w", err)
	}
	if _, err := client.ApplyAuthResponse(token); err != nil {
		return nil, err
	}
	lib, err := client.LibraryClient()
	if err != nil {
		return nil, err
	}
	return &Session{lib: lib}, nil
}
