package dtrpg

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// ErrNoKey means no DriveThruRPG application key is stored.
var ErrNoKey = errors.New("no DriveThruRPG key stored: run 'sweetrpg dtrpg login'")

// KeyStore persists the DriveThruRPG application key. The seam keeps commands
// testable and lets a broken keychain surface as a clear refusal instead of a
// plaintext fallback.
type KeyStore interface {
	SaveKey(key string) error
	LoadKey() (string, error)
	DeleteKey() error
}

// KeyringStore persists the key in the OS keychain via go-keyring, under
// KeychainAccount - the one slot shared by every consumer of the
// DriveThruRPG login.
type KeyringStore struct{}

var _ KeyStore = KeyringStore{}

func (KeyringStore) SaveKey(key string) error {
	if key == "" {
		return errors.New("refusing to store an empty DriveThruRPG key")
	}
	return keyring.Set(KeychainService, KeychainAccount, key)
}

func (KeyringStore) LoadKey() (string, error) {
	key, err := keyring.Get(KeychainService, KeychainAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoKey
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (KeyringStore) DeleteKey() error {
	err := keyring.Delete(KeychainService, KeychainAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil // idempotent logout
	}
	return err
}

// MemoryKeyStore is an in-process KeyStore for tests.
type MemoryKeyStore struct {
	key string
	set bool
}

var _ KeyStore = (*MemoryKeyStore)(nil)

func (m *MemoryKeyStore) SaveKey(key string) error {
	if key == "" {
		return errors.New("refusing to store an empty DriveThruRPG key")
	}
	m.key, m.set = key, true
	return nil
}

func (m *MemoryKeyStore) LoadKey() (string, error) {
	if !m.set {
		return "", ErrNoKey
	}
	return m.key, nil
}

func (m *MemoryKeyStore) DeleteKey() error {
	m.key, m.set = "", false
	return nil
}
