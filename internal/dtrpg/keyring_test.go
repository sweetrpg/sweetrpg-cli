package dtrpg

import (
	"errors"
	"testing"
)

func TestMemoryKeyStoreRoundTrip(t *testing.T) {
	var store KeyStore = &MemoryKeyStore{}

	if _, err := store.LoadKey(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty store LoadKey = %v, want ErrNoKey", err)
	}
	if err := store.SaveKey(""); err == nil {
		t.Fatal("SaveKey(\"\") should be refused")
	}
	if err := store.SaveKey("app-key-123"); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	got, err := store.LoadKey()
	if err != nil || got != "app-key-123" {
		t.Fatalf("LoadKey = %q, %v", got, err)
	}
	if err := store.DeleteKey(); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, err := store.LoadKey(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("after delete LoadKey = %v, want ErrNoKey", err)
	}
}

func TestMemoryKeyStoreDeleteIsIdempotent(t *testing.T) {
	store := &MemoryKeyStore{}
	if err := store.DeleteKey(); err != nil {
		t.Fatalf("first DeleteKey: %v", err)
	}
	if err := store.DeleteKey(); err != nil {
		t.Fatalf("second DeleteKey: %v", err)
	}
}
