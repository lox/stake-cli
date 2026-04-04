package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/stakeapi"
	"github.com/lox/stake-cli/pkg/stake"
)

func TestExecuteAuthAddCommand(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Stake-Session-Token"); got != "initial-token" {
			t.Fatalf("expected initial token header, got %q", got)
		}

		w.Header().Set("Stake-Session-Token", "rotated-token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "auth", "add", "primary", "--token", "initial-token"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response authAccountResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account.Name != "primary" {
		t.Fatalf("expected primary account, got %q", response.Account.Name)
	}

	store, err := authstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := store.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "rotated-token" {
		t.Fatalf("expected rotated token to be stored, got %q", entry.SessionToken)
	}
	if entry.Email != "account@example.test" {
		t.Fatalf("expected email to be stored, got %q", entry.Email)
	}
}

func TestExecuteUserCommandUsesStoredAuth(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &authstore.File{}
	store.Upsert(authstore.Entry{Name: "primary", SessionToken: "stored-token"})
	if err := authstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Stake-Session-Token"); got != "stored-token" {
			t.Fatalf("expected stored token header, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "user", "primary"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response stakeapi.UserResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account != "primary" {
		t.Fatalf("expected primary account, got %q", response.Account)
	}
	if response.User == nil || response.User.Email != "account@example.test" {
		t.Fatalf("expected live user payload, got %+v", response.User)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Contains(data, []byte("account@example.test")) {
		t.Fatalf("expected updated store metadata in %s", string(data))
	}
}
