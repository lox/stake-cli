package sessionstore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	updatedAt := time.Date(2026, 4, 5, 1, 2, 3, 0, time.UTC)

	store := &File{}
	store.Upsert(Entry{
		Name:         "primary",
		SessionToken: "token-1",
		UserID:       "user-123",
		OPItem:       "op://Private/stake.com",
		OPAccount:    "my.1password.com",
		Email:        "account@example.test",
		Username:     "sample-user",
		AccountType:  "individual",
		UpdatedAt:    updatedAt,
	})

	if err := Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	entry, err := loaded.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "token-1" {
		t.Fatalf("expected token-1, got %q", entry.SessionToken)
	}
	if entry.Email != "account@example.test" {
		t.Fatalf("expected email to round-trip, got %q", entry.Email)
	}
	if entry.OPItem != "op://Private/stake.com" {
		t.Fatalf("expected op item to round-trip, got %q", entry.OPItem)
	}
	if entry.OPAccount != "my.1password.com" {
		t.Fatalf("expected op account to round-trip, got %q", entry.OPAccount)
	}
	if !entry.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("expected updated_at %s, got %s", updatedAt, entry.UpdatedAt)
	}
}

func TestUpdateReplacesExistingAccount(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")

	if err := Update(storePath, func(store *File) error {
		store.Upsert(Entry{Name: "primary", SessionToken: "token-1"})
		store.Upsert(Entry{Name: "primary", SessionToken: "token-2", Email: "account@example.test"})
		return nil
	}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	loaded, err := Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	entry, err := loaded.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "token-2" {
		t.Fatalf("expected updated token, got %q", entry.SessionToken)
	}
	if entry.Email != "account@example.test" {
		t.Fatalf("expected updated email, got %q", entry.Email)
	}
}
