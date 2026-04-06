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
	"github.com/lox/stake-cli/internal/stakelogin"
	"github.com/lox/stake-cli/pkg/sessionstore"
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

	store, err := sessionstore.Load(storePath)
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
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "primary", SessionToken: "stored-token"})
	if err := sessionstore.Save(storePath, store); err != nil {
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

	var response userResponse
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

func TestExecuteAuthProbeCommandReportsRotationAndFailure(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "primary", SessionToken: "initial-token"})
	if err := sessionstore.Save(storePath, store); err != nil {
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

	updatedStore, err := sessionstore.Load(storePath)
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
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "personal", SessionToken: "previous-token", UserID: "stored-user-id"})
	if err := sessionstore.Save(storePath, store); err != nil {
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

	store, err := sessionstore.Load(storePath)
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

func TestExecuteAuthLoginCommandPassesOnePasswordServiceAccountConfig(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "service-account-token")

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	var capturedConfig stakelogin.Config
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		capturedConfig = cfg
		return &stakelogin.Result{
			Account: cfg.AccountName,
			Status:  "manual_login_pending",
		}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", filepath.Join(t.TempDir(), "accounts.json"),
		"auth", "login", "personal",
		"--op-item", "op://stake/personal",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.OnePassword.ItemReference != "op://stake/personal" {
		t.Fatalf("unexpected 1Password item reference: %q", capturedConfig.OnePassword.ItemReference)
	}
	if capturedConfig.OnePassword.ServiceAccountToken != "service-account-token" {
		t.Fatalf("unexpected 1Password service account token: %q", capturedConfig.OnePassword.ServiceAccountToken)
	}
	if capturedConfig.OnePassword.DesktopAccount != "" {
		t.Fatalf("expected empty desktop account, got %q", capturedConfig.OnePassword.DesktopAccount)
	}
}

func TestExecuteAuthLoginCommandPassesOnePasswordDesktopAccountConfig(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "service-account-token")

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	var capturedConfig stakelogin.Config
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		capturedConfig = cfg
		return &stakelogin.Result{
			Account: cfg.AccountName,
			Status:  "manual_login_pending",
		}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", filepath.Join(t.TempDir(), "accounts.json"),
		"auth", "login", "personal",
		"--op-item", "op://stake/personal",
		"--op-account", "Personal",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.OnePassword.ItemReference != "op://stake/personal" {
		t.Fatalf("unexpected 1Password item reference: %q", capturedConfig.OnePassword.ItemReference)
	}
	if capturedConfig.OnePassword.DesktopAccount != "Personal" {
		t.Fatalf("unexpected 1Password desktop account: %q", capturedConfig.OnePassword.DesktopAccount)
	}
	if capturedConfig.OnePassword.ServiceAccountToken != "" {
		t.Fatalf("expected empty service account token, got %q", capturedConfig.OnePassword.ServiceAccountToken)
	}
}

func TestExecuteAuthLoginCommandUsesStoredOnePasswordConfig(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{
		Name:      "personal",
		UserID:    "stored-user-id",
		OPItem:    "op://Private/stake.com",
		OPAccount: "my.1password.com",
	})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	var capturedConfig stakelogin.Config
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		capturedConfig = cfg
		return &stakelogin.Result{
			Account: cfg.AccountName,
			Status:  "manual_login_pending",
		}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", storePath,
		"auth", "login", "personal",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.ExpectedUserID != "stored-user-id" {
		t.Fatalf("expected stored user id to be forwarded, got %q", capturedConfig.ExpectedUserID)
	}
	if capturedConfig.OnePassword.ItemReference != "op://Private/stake.com" {
		t.Fatalf("unexpected stored 1Password item reference: %q", capturedConfig.OnePassword.ItemReference)
	}
	if capturedConfig.OnePassword.DesktopAccount != "my.1password.com" {
		t.Fatalf("unexpected stored 1Password desktop account: %q", capturedConfig.OnePassword.DesktopAccount)
	}
}

func TestExecuteAuthLoginCommandStoresOnePasswordConfig(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")

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

	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		return &stakelogin.Result{
			Account:      cfg.AccountName,
			Status:       "session_token_detected",
			SessionToken: "captured-token",
		}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", storePath,
		"--base-url", server.URL,
		"auth", "login", "personal",
		"--op-item", "op://Private/stake.com",
		"--op-account", "my.1password.com",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	store, err := sessionstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := store.Get("personal")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.OPItem != "op://Private/stake.com" {
		t.Fatalf("expected stored op item, got %q", entry.OPItem)
	}
	if entry.OPAccount != "my.1password.com" {
		t.Fatalf("expected stored op account, got %q", entry.OPAccount)
	}
}

func TestExecuteAuthTokenCommandPrintsRawToken(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "primary", SessionToken: "stored-token"})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "auth", "token", "primary"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if got := stdout.String(); got != "stored-token\n" {
		t.Fatalf("expected raw token output, got %q", got)
	}
}

func TestExecuteAuthTokenCommandOutputsJSON(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	updatedAt := time.Date(2026, 4, 6, 3, 4, 5, 0, time.UTC)
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{
		Name:         "primary",
		SessionToken: "stored-token",
		UserID:       "user-123",
		Email:        "account@example.test",
		Username:     "sample-user",
		AccountType:  "individual",
		UpdatedAt:    updatedAt,
	})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "auth", "token", "primary", "--json"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response sessionstore.TokenView
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Name != "primary" {
		t.Fatalf("expected primary account, got %q", response.Name)
	}
	if response.SessionToken != "stored-token" {
		t.Fatalf("expected stored-token, got %q", response.SessionToken)
	}
	if response.Email != "account@example.test" {
		t.Fatalf("expected account@example.test, got %q", response.Email)
	}
	if !response.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("expected updated_at %s, got %s", updatedAt, response.UpdatedAt)
	}
}

func TestExecuteAuthLoginCommandRejectsDesktopAccountWithoutItem(t *testing.T) {
	err := execute(context.Background(), []string{
		"--auth-store", filepath.Join(t.TempDir(), "accounts.json"),
		"auth", "login", "personal",
		"--op-account", "Personal",
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected execute to fail without --op-item")
	}
	if !strings.Contains(err.Error(), "--op-account requires --op-item") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteVersionFlagPrintsVersion(t *testing.T) {
	originalVersion := version
	version = "v1.2.3"
	t.Cleanup(func() {
		version = originalVersion
	})

	originalCLIExit := cliExit
	var exitCode int
	cliExit = func(code int) {
		exitCode = code
	}
	t.Cleanup(func() {
		cliExit = originalCLIExit
	})

	var stdout bytes.Buffer
	err := execute(context.Background(), []string{"--version"}, &stdout, io.Discard)
	if err == nil {
		t.Fatal("expected execute to stop after printing version")
	}
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d", exitCode)
	}
	if got := stdout.String(); got != "v1.2.3\n" {
		t.Fatalf("expected version output, got %q", got)
	}
}
