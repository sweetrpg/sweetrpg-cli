package dtrpg

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// ErrNoKey means no DriveThruRPG application key is stored.
var ErrNoKey = errors.New("no DriveThruRPG key stored: run 'sweetrpg catalog import dtrpg login'")

// KeyStore persists the DriveThruRPG application key. The seam keeps commands
// testable and lets a broken keychain surface as a clear refusal instead of a
// plaintext fallback.
type KeyStore interface {
	SaveKey(key string) error
	LoadKey() (string, error)
	DeleteKey() error
}

// KeyringStore persists the key in the OS keychain via go-keyring. The zero
// value uses KeychainAccount (the catalog import's slot); set Account to use
// a different slot, e.g. GameRoomKeychainAccount.
type KeyringStore struct {
	Account string
}

var _ KeyStore = KeyringStore{}

func (k KeyringStore) account() string {
	if k.Account != "" {
		return k.Account
	}
	return KeychainAccount
}

func (k KeyringStore) SaveKey(key string) error {
	if key == "" {
		return errors.New("refusing to store an empty DriveThruRPG key")
	}
	return keyring.Set(KeychainService, k.account(), key)
}

func (k KeyringStore) LoadKey() (string, error) {
	key, err := keyring.Get(KeychainService, k.account())
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoKey
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (k KeyringStore) DeleteKey() error {
	err := keyring.Delete(KeychainService, k.account())
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
