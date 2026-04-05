package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/stakeapi"
	"github.com/lox/stake-cli/internal/stakelogin"
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

func TestExecuteUserCommandUsesProxyWhenConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/v1/accounts/primary/user" {
			t.Fatalf("expected proxy user path, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stakeapi.UserResponse{
			Account: "primary",
			User: &stake.User{
				UserID:      "user-123",
				Email:       "account@example.test",
				Username:    "sample-user",
				AccountType: "individual",
			},
		}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--proxy", server.URL, "user", "primary"}, &stdout, &bytes.Buffer{}); err != nil {
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
		t.Fatalf("expected proxied user payload, got %+v", response.User)
	}
}

func TestExecuteAuthProbeCommandReportsRotationAndFailure(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &authstore.File{}
	store.Upsert(authstore.Entry{Name: "primary", SessionToken: "initial-token"})
	if err := authstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++

		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}

		switch attempt {
		case 1:
			if got := r.Header.Get("Stake-Session-Token"); got != "initial-token" {
				t.Fatalf("expected initial token header, got %q", got)
			}
			w.Header().Set("Stake-Session-Token", "rotated-token")
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
				t.Fatalf("encoding response: %v", err)
			}
		case 2:
			if got := r.Header.Get("Stake-Session-Token"); got != "rotated-token" {
				t.Fatalf("expected rotated token header, got %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("expired"))
		default:
			t.Fatalf("unexpected extra probe attempt %d", attempt)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "auth", "probe", "primary", "--interval", "1ms"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response authProbeResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account != "primary" {
		t.Fatalf("expected primary account, got %q", response.Account)
	}
	if response.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", response.Attempts)
	}
	if response.Successes != 1 {
		t.Fatalf("expected 1 success, got %d", response.Successes)
	}
	if response.Rotations != 1 {
		t.Fatalf("expected 1 rotation, got %d", response.Rotations)
	}
	if response.StoppedReason != "validation_failed" {
		t.Fatalf("expected validation_failed stop reason, got %q", response.StoppedReason)
	}
	if !strings.Contains(response.LastError, "API error 401") {
		t.Fatalf("expected 401 failure in last error, got %q", response.LastError)
	}
	if response.LastSuccessAt == nil {
		t.Fatal("expected last success timestamp")
	}
	if response.User == nil || response.User.Email != "account@example.test" {
		t.Fatalf("expected last successful user payload, got %+v", response.User)
	}

	updatedStore, err := authstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := updatedStore.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "rotated-token" {
		t.Fatalf("expected rotated token to be stored, got %q", entry.SessionToken)
	}
	if entry.Email != "account@example.test" {
		t.Fatalf("expected probed email to be stored, got %q", entry.Email)
	}
}

func TestExecuteAuthLoginCommandDelegatesToScaffold(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &authstore.File{}
	store.Upsert(authstore.Entry{Name: "personal", SessionToken: "previous-token", UserID: "stored-user-id"})
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
		if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
			t.Fatalf("expected captured token header, got %q", got)
		}

		w.Header().Set("Stake-Session-Token", "rotated-login-token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	var capturedConfig stakelogin.Config
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		capturedConfig = cfg
		return &stakelogin.Result{
			Account:        cfg.AccountName,
			LoginURL:       cfg.LoginURL,
			CurrentURL:     "https://trading.hellostake.com/platform/dashboard",
			BrowserVisible: cfg.ShowBrowser,
			KeepBrowser:    cfg.KeepBrowser,
			Status:         "session_token_detected",
			SessionToken:   "captured-token",
		}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", storePath,
		"--base-url", server.URL,
		"auth", "login", "personal",
		"--login-url", "https://example.test/sign-in",
		"--browser-timeout", "45s",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.AccountName != "personal" {
		t.Fatalf("expected account name personal, got %q", capturedConfig.AccountName)
	}
	if capturedConfig.APIBaseURL != server.URL {
		t.Fatalf("unexpected API base url: %q", capturedConfig.APIBaseURL)
	}
	if capturedConfig.ExpectedUserID != "stored-user-id" {
		t.Fatalf("expected stored user id to be forwarded, got %q", capturedConfig.ExpectedUserID)
	}
	if capturedConfig.LoginURL != "https://example.test/sign-in" {
		t.Fatalf("unexpected login url: %q", capturedConfig.LoginURL)
	}
	if capturedConfig.BrowserTimeout != 45*time.Second {
		t.Fatalf("unexpected browser timeout: %s", capturedConfig.BrowserTimeout)
	}
	if !capturedConfig.ShowBrowser {
		t.Fatal("expected visible browser by default")
	}
	if !capturedConfig.KeepBrowser {
		t.Fatal("expected browser to stay open by default")
	}

	var response authLoginResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Login.Account != "personal" {
		t.Fatalf("expected personal account in login response, got %q", response.Login.Account)
	}
	if response.Login.Status != "session_token_detected" {
		t.Fatalf("expected session_token_detected status, got %q", response.Login.Status)
	}
	if response.Account.Name != "personal" {
		t.Fatalf("expected stored account named personal, got %q", response.Account.Name)
	}
	if response.Account.Email != "account@example.test" {
		t.Fatalf("expected stored email account@example.test, got %q", response.Account.Email)
	}

	store, err := authstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := store.Get("personal")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.SessionToken != "rotated-login-token" {
		t.Fatalf("expected rotated login token to be stored, got %q", entry.SessionToken)
	}
	if entry.Email != "account@example.test" {
		t.Fatalf("expected stored email account@example.test, got %q", entry.Email)
	}
}
