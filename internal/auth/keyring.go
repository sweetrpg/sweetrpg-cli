package auth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// ServiceName is the OS-keychain service all CLI credentials live under.
const ServiceName = "sweetrpg-catalog-cli"

// currentAccountKey is the pointer record naming which subject's credentials
// are active. It keeps the design "account = authenticated subject" while
// letting commands find credentials without knowing the sub up front.
const currentAccountKey = "current"

// ErrNotLoggedIn means no usable stored credentials exist. Callers map it to
// exit code 3 with a pointer at `auth login`.
var ErrNotLoggedIn = errors.New("not logged in: run 'sweetrpg-catalog auth login'")

// Session is what persists between runs: who logged in and the refresh token
// that proves it. Access tokens are never persisted - they are short-lived
// and re-derived from the refresh token.
type Session struct {
	Account      string `json:"account"`
	Email        string `json:"email,omitempty"`
	RefreshToken string `json:"refresh_token"`
}

// Store abstracts credential persistence so commands stay testable and so a
// broken keychain degrades to session-only use instead of plaintext files.
type Store interface {
	Save(Session) error
	Load() (*Session, error)
	Delete() error
}

// KeyringStore persists sessions in the OS keychain via go-keyring.
type KeyringStore struct{}

var _ Store = KeyringStore{}

func (KeyringStore) Save(s Session) error {
	if s.Account == "" || s.RefreshToken == "" {
		return fmt.Errorf("refusing to store incomplete session")
	}
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := keyring.Set(ServiceName, currentAccountKey, s.Account); err != nil {
		return err
	}
	return keyring.Set(ServiceName, s.Account, string(blob))
}

func (KeyringStore) Load() (*Session, error) {
	account, err := keyring.Get(ServiceName, currentAccountKey)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	blob, err := keyring.Get(ServiceName, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		return nil, fmt.Errorf("stored credentials are unreadable: %w", err)
	}
	return &s, nil
}

func (KeyringStore) Delete() error {
	account, err := keyring.Get(ServiceName, currentAccountKey)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil // idempotent logout
		}
		return err
	}
	del := keyring.Delete(ServiceName, account)
	ptr := keyring.Delete(ServiceName, currentAccountKey)
	if del != nil && !errors.Is(del, keyring.ErrNotFound) {
		return del
	}
	if ptr != nil && !errors.Is(ptr, keyring.ErrNotFound) {
		return ptr
	}
	return nil
}

// MemoryStore is an in-process Store for tests and for holding a session that
// could not be persisted (the refuse-to-persist fallback keeps it in memory
// for the rest of the run only).
type MemoryStore struct {
	session *Session
}

var _ Store = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) Save(s Session) error {
	cp := s
	m.session = &cp
	return nil
}

func (m *MemoryStore) Load() (*Session, error) {
	if m.session == nil {
		return nil, ErrNotLoggedIn
	}
	cp := *m.session
	return &cp, nil
}

func (m *MemoryStore) Delete() error {
	m.session = nil
	return nil
}
