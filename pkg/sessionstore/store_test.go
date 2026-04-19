package sessionstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestResolvePath(t *testing.T) {
	t.Run("uses explicit path", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "custom-accounts.json")

		resolved, err := ResolvePath(explicit)
		if err != nil {
			t.Fatalf("ResolvePath returned error: %v", err)
		}
		if resolved != explicit {
			t.Fatalf("expected explicit path %q, got %q", explicit, resolved)
		}
	})

	t.Run("uses XDG_CONFIG_HOME when set", func(t *testing.T) {
		xdgConfigHome := filepath.Join(t.TempDir(), "xdg-config")
		t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)

		resolved, err := ResolvePath("")
		if err != nil {
			t.Fatalf("ResolvePath returned error: %v", err)
		}

		want := filepath.Join(xdgConfigHome, "stake-cli", "accounts.json")
		if resolved != want {
			t.Fatalf("expected XDG path %q, got %q", want, resolved)
		}
	})

	t.Run("falls back to ~/.config when XDG_CONFIG_HOME is unset", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("HOME", homeDir)
		t.Setenv("XDG_CONFIG_HOME", "")
		_ = os.Unsetenv("XDG_CONFIG_HOME")

		resolved, err := ResolvePath("")
		if err != nil {
			t.Fatalf("ResolvePath returned error: %v", err)
		}

		want := filepath.Join(homeDir, ".config", "stake-cli", "accounts.json")
		if resolved != want {
			t.Fatalf("expected default XDG path %q, got %q", want, resolved)
		}
	})
}

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

func TestLoadFallsBackToLegacyMacOSPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("legacy macOS path only applies on darwin")
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	legacyPath := filepath.Join(homeDir, "Library", "Application Support", "stake-cli", "accounts.json")
	legacyStore := &File{}
	legacyStore.Upsert(Entry{Name: "legacy", SessionToken: "legacy-token"})
	if err := Save(legacyPath, legacyStore); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	entry, err := loaded.Get("legacy")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "legacy-token" {
		t.Fatalf("expected legacy-token, got %q", entry.SessionToken)
	}
}

func TestLegacyResolvePathForOS(t *testing.T) {
	t.Run("supports macOS legacy config dir", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "Library", "Application Support")
		got := legacyResolvePathForOS("darwin", configDir)
		want := filepath.Join(configDir, "stake-cli", "accounts.json")
		if got != want {
			t.Fatalf("expected macOS legacy path %q, got %q", want, got)
		}
	})

	t.Run("supports windows legacy config dir", func(t *testing.T) {
		configDir := filepath.Join("C:/Users/alice/AppData/Roaming")
		got := legacyResolvePathForOS("windows", configDir)
		want := filepath.Join(configDir, "stake-cli", "accounts.json")
		if got != want {
			t.Fatalf("expected Windows legacy path %q, got %q", want, got)
		}
	})

	t.Run("ignores unsupported operating systems", func(t *testing.T) {
		got := legacyResolvePathForOS("linux", filepath.Join(t.TempDir(), ".config"))
		if got != "" {
			t.Fatalf("expected no legacy path for linux, got %q", got)
		}
	})
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

func TestGetAndDeleteNormalizeNames(t *testing.T) {
	store := &File{}
	store.Upsert(Entry{Name: " primary ", SessionToken: "token-1"})

	entry, err := store.Get(" primary ")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.Name != "primary" {
		t.Fatalf("expected normalized name, got %q", entry.Name)
	}

	if !store.Delete(" primary ") {
		t.Fatal("expected Delete to remove normalized name")
	}
	if len(store.Accounts) != 0 {
		t.Fatalf("expected store to be empty, got %d accounts", len(store.Accounts))
	}
}
