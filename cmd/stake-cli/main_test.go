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
	goruntime "runtime"
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

func TestExecuteStatusCommandUsesStoredAuthForAllAccounts(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "primary", SessionToken: "stored-token"})
	store.Upsert(sessionstore.Entry{Name: "secondary", SessionToken: "broken-token"})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}

		switch got := r.Header.Get("Stake-Session-Token"); got {
		case "stored-token":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
				t.Fatalf("encoding response: %v", err)
			}
		case "broken-token":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("expired"))
		default:
			t.Fatalf("unexpected token header %q", got)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response statusResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if len(response.Accounts) != 2 {
		t.Fatalf("expected 2 account statuses, got %d", len(response.Accounts))
	}
	if response.Accounts[0].Account != "primary" || !response.Accounts[0].OK {
		t.Fatalf("expected primary to be ok, got %+v", response.Accounts[0])
	}
	if response.Accounts[0].User == nil || response.Accounts[0].User.Email != "account@example.test" {
		t.Fatalf("expected primary live user payload, got %+v", response.Accounts[0].User)
	}
	if response.Accounts[1].Account != "secondary" || response.Accounts[1].OK {
		t.Fatalf("expected secondary to fail, got %+v", response.Accounts[1])
	}
	if !strings.Contains(response.Accounts[1].Error, "API error 401") {
		t.Fatalf("expected secondary error to mention 401, got %q", response.Accounts[1].Error)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 validation requests, got %d", requestCount)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Contains(data, []byte("account@example.test")) {
		t.Fatalf("expected updated store metadata in %s", string(data))
	}
}

func TestExecuteAuthStatusCompatibilityCommandUsesStoredAuthForAllAccounts(t *testing.T) {
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
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "auth", "status"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response statusResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("expected 1 account status, got %d", len(response.Accounts))
	}
	if response.Accounts[0].Account != "primary" || !response.Accounts[0].OK {
		t.Fatalf("expected primary to be ok, got %+v", response.Accounts[0])
	}
}

func TestExecuteUsersCommandUsesStoredAuth(t *testing.T) {
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
		if r.URL.Path != "/api/user/product/config" {
			t.Fatalf("expected /api/user/product/config path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Stake-Session-Token"); got != "stored-token" {
			t.Fatalf("expected stored token header, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.UserList{
			ActiveUser:    "user-2",
			ActiveProduct: "AU_TRADING",
			MasterUserID:  "user-1",
			Users: []stake.ListedUser{
				{UserID: "user-1", FirstName: "Lachlan", LastName: "Donald", AccountType: "INDIVIDUAL", Products: []stake.ListedUserProduct{{Type: "AU_TRADING", Status: "OPEN"}}},
				{UserID: "user-2", FirstName: "Donald Family Trust", AccountType: "DISCRETIONARY_TRUST", Products: []stake.ListedUserProduct{{Type: "US_TRADING", Status: "OPEN"}}},
			},
		}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "users", "primary"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response usersResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account != "primary" {
		t.Fatalf("expected primary account, got %q", response.Account)
	}
	if response.ActiveUser != "user-2" {
		t.Fatalf("expected active user user-2, got %q", response.ActiveUser)
	}
	if response.MasterUserID != "user-1" {
		t.Fatalf("expected master user user-1, got %q", response.MasterUserID)
	}
	if len(response.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(response.Users))
	}
	if !response.Users[0].Master {
		t.Fatal("expected first user to be marked as master")
	}
	if response.Users[0].Alias != "personal" {
		t.Fatalf("expected first user alias personal, got %q", response.Users[0].Alias)
	}
	if !response.Users[1].Active {
		t.Fatal("expected second user to be marked as active")
	}
	if response.Users[1].Alias != "family-trust" {
		t.Fatalf("expected second user alias family-trust, got %q", response.Users[1].Alias)
	}
	if len(response.Users[1].Products) != 1 || response.Users[1].Products[0].Type != "US_TRADING" {
		t.Fatalf("expected second user US trading product, got %+v", response.Users[1].Products)
	}
}

func TestGeneratedAliasesUsesShortStableNames(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	runtime := &runtime{authStorePath: storePath}

	aliases, err := runtime.generatedAliases(&stake.UserList{
		MasterUserID: "user-personal",
		Users: []stake.ListedUser{
			{UserID: "user-smsf", FirstName: "Donald SMSF", AccountType: "SMSF_TRUST", StakeSMSFCustomer: true},
			{UserID: "user-trust", FirstName: "The Trustee for the DONALD FAMILY TRUST", AccountType: "DISCRETIONARY_TRUST"},
			{UserID: "user-personal", FirstName: "Lachlan", LastName: "Donald", AccountType: "INDIVIDUAL"},
		},
	})
	if err != nil {
		t.Fatalf("generatedAliases returned error: %v", err)
	}
	if aliases["user-personal"] != "personal" {
		t.Fatalf("expected personal alias, got %q", aliases["user-personal"])
	}
	if aliases["user-smsf"] != "smsf" {
		t.Fatalf("expected smsf alias, got %q", aliases["user-smsf"])
	}
	if aliases["user-trust"] != "family-trust" {
		t.Fatalf("expected family-trust alias, got %q", aliases["user-trust"])
	}
}

func TestGeneratedAliasesPreservesExistingAliasOwnershipDuringCollision(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "family-trust", UserID: "user-existing", SessionToken: "stored-token"})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	runtime := &runtime{authStorePath: storePath}
	aliases, err := runtime.generatedAliases(&stake.UserList{
		Users: []stake.ListedUser{
			{UserID: "user-collision", FirstName: "Donald Family Trust", AccountType: "DISCRETIONARY_TRUST"},
			{UserID: "user-existing", FirstName: "The Trustee for the DONALD FAMILY TRUST", AccountType: "DISCRETIONARY_TRUST"},
		},
	})
	if err != nil {
		t.Fatalf("generatedAliases returned error: %v", err)
	}

	if aliases["user-existing"] != "family-trust" {
		t.Fatalf("expected existing user to keep family-trust, got %q", aliases["user-existing"])
	}
	if aliases["user-collision"] == "family-trust" {
		t.Fatalf("expected colliding user to get a different alias, got %q", aliases["user-collision"])
	}
	if aliases["user-collision"] != "donald-family-trust" {
		t.Fatalf("expected colliding user alias donald-family-trust, got %q", aliases["user-collision"])
	}
}

func TestExecuteAuthListCommandUsesLegacyMacOSStoreByDefault(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("legacy macOS path only applies on darwin")
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	legacyPath := filepath.Join(homeDir, "Library", "Application Support", "stake-cli", "accounts.json")
	legacyStore := &sessionstore.File{}
	legacyStore.Upsert(sessionstore.Entry{Name: "legacy", SessionToken: "legacy-token"})
	if err := sessionstore.Save(legacyPath, legacyStore); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"auth", "list"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response authAccountsResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(response.Accounts))
	}
	if response.Accounts[0].Name != "legacy" {
		t.Fatalf("expected legacy account, got %q", response.Accounts[0].Name)
	}
}

func TestExecuteStatusCommandPreservesConcurrentStoreUpdates(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{
		Name:         "primary",
		SessionToken: "stored-token",
		OPItem:       "op://Private/old",
		OPAccount:    "old.1password.com",
	})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := sessionstore.Update(storePath, func(store *sessionstore.File) error {
			entry, err := store.Get("primary")
			if err != nil {
				return err
			}
			entry.OPItem = "op://Private/new"
			entry.OPAccount = "new.1password.com"
			store.Upsert(*entry)
			return nil
		}); err != nil {
			t.Fatalf("Update returned error: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "status"}, io.Discard, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	updatedStore, err := sessionstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := updatedStore.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.OPItem != "op://Private/new" {
		t.Fatalf("expected concurrent OP item update to persist, got %q", entry.OPItem)
	}
	if entry.OPAccount != "new.1password.com" {
		t.Fatalf("expected concurrent OP account update to persist, got %q", entry.OPAccount)
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
		if r.URL.Path == "/api/user/product/config" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
			t.Fatalf("expected captured token header, got %q", got)
		}

		w.Header().Set("Stake-Session-Token", "rotated-login-token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "stored-user-id", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
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

func TestExecuteAuthLoginCommandSupportsDiscoveryWithoutSeedAlias(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path == "/api/user/product/config" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
			t.Fatalf("expected captured token header, got %q", got)
		}

		w.Header().Set("Stake-Session-Token", "rotated-login-token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", FirstName: "Lachlan", LastName: "Donald", Email: "account@example.test", Username: "sample-user", AccountType: "INDIVIDUAL"}); err != nil {
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
		"auth", "login",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.AccountName != "discovery" {
		t.Fatalf("expected discovery account name, got %q", capturedConfig.AccountName)
	}
	if capturedConfig.ExpectedUserID != "" {
		t.Fatalf("expected empty expected user id, got %q", capturedConfig.ExpectedUserID)
	}

	var response authLoginResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account.Name != "personal" {
		t.Fatalf("expected inferred personal account, got %q", response.Account.Name)
	}
}

func TestExecuteAuthLoginCommandSupportsAliasFlag(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "personal", UserID: "stored-user-id", OPItem: "op://Private/stake.com", OPAccount: "my.1password.com"})
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
		return &stakelogin.Result{Account: cfg.AccountName, Status: "manual_login_pending"}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"--auth-store", storePath, "auth", "login", "--alias", "personal"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.AccountName != "personal" {
		t.Fatalf("expected alias account name personal, got %q", capturedConfig.AccountName)
	}
	if capturedConfig.ExpectedUserID != "stored-user-id" {
		t.Fatalf("expected stored user id to be forwarded, got %q", capturedConfig.ExpectedUserID)
	}
	if capturedConfig.OnePassword.ItemReference != "op://Private/stake.com" {
		t.Fatalf("expected stored 1Password item, got %q", capturedConfig.OnePassword.ItemReference)
	}
	if capturedConfig.OnePassword.DesktopAccount != "my.1password.com" {
		t.Fatalf("expected stored 1Password desktop account, got %q", capturedConfig.OnePassword.DesktopAccount)
	}
}

func TestExecuteAuthLoginCommandSupportsUserIDFlag(t *testing.T) {
	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	var capturedConfig stakelogin.Config
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = logger
		capturedConfig = cfg
		return &stakelogin.Result{Account: cfg.AccountName, Status: "manual_login_pending"}, nil
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{"auth", "login", "--user-id", "12345678-1234-1234-1234-123456789abc"}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	if capturedConfig.AccountName != "user-12345678" {
		t.Fatalf("expected synthesized user account name, got %q", capturedConfig.AccountName)
	}
	if capturedConfig.ExpectedUserID != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("expected explicit user id to be forwarded, got %q", capturedConfig.ExpectedUserID)
	}
}

func TestExecuteAuthLoginCommandRejectsAliasAndUserIDTogether(t *testing.T) {
	err := execute(context.Background(), []string{"auth", "login", "--alias", "personal", "--user-id", "123"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected execute to fail when both --alias and --user-id are provided")
	}
	if !strings.Contains(err.Error(), "--alias") || !strings.Contains(err.Error(), "--user-id") {
		t.Fatalf("expected mutual exclusion error mentioning both flags, got %v", err)
	}
}

func TestExecuteAuthLoginCommandSyncsGeneratedAliases(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{Name: "family-trust", SessionToken: "stale-token"})
	store.Upsert(sessionstore.Entry{Name: "personal", SessionToken: "stale-token", UserID: "303b50f6-d7bf-4856-b2ef-2e11a958795f"})
	store.Upsert(sessionstore.Entry{Name: "smsf", SessionToken: "stale-token", UserID: "303b50f6-d7bf-4856-b2ef-2e11a958795f"})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch requestCount {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user/product/config" {
				t.Fatalf("expected GET /api/user/product/config, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
				t.Fatalf("expected captured token header, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.UserList{
				ActiveUser:   "303b50f6-d7bf-4856-b2ef-2e11a958795f",
				MasterUserID: "7b1932ce-2a8b-40f3-bc42-b88d23770622",
				Users: []stake.ListedUser{
					{UserID: "303b50f6-d7bf-4856-b2ef-2e11a958795f", FirstName: "The Trustee for the DONALD FAMILY TRUST", AccountType: "DISCRETIONARY_TRUST"},
					{UserID: "7b1932ce-2a8b-40f3-bc42-b88d23770622", FirstName: "Lachlan", LastName: "Donald", AccountType: "INDIVIDUAL"},
					{UserID: "945365ff-556c-4730-baef-9aad2161b647", FirstName: "Donald SMSF", AccountType: "SMSF_TRUST", StakeSMSFCustomer: true},
				},
			}); err != nil {
				t.Fatalf("encoding product config response: %v", err)
			}
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user" {
				t.Fatalf("expected GET /api/user, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
				t.Fatalf("expected captured token for active trust validation, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "303b50f6-d7bf-4856-b2ef-2e11a958795f", Email: "lachlan@ljd.cc", AccountType: "DISCRETIONARY_TRUST"}); err != nil {
				t.Fatalf("encoding trust user: %v", err)
			}
		case 3:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user" {
				t.Fatalf("expected GET /api/user, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "personal-token" {
				t.Fatalf("expected personal token for personal validation, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "7b1932ce-2a8b-40f3-bc42-b88d23770622", Email: "lachlan@ljd.cc", AccountType: "INDIVIDUAL"}); err != nil {
				t.Fatalf("encoding personal user: %v", err)
			}
		case 4:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user" {
				t.Fatalf("expected GET /api/user, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "smsf-token" {
				t.Fatalf("expected smsf token for smsf validation, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "945365ff-556c-4730-baef-9aad2161b647", Email: "lachlan@ljd.cc", AccountType: "SMSF_TRUST"}); err != nil {
				t.Fatalf("encoding smsf user: %v", err)
			}
		default:
			t.Fatalf("unexpected extra request %d", requestCount)
		}
	}))
	defer server.Close()

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	loginCalls := 0
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = cfg
		_ = logger
		loginCalls++
		switch loginCalls {
		case 1:
			if cfg.AccountName != "personal" {
				t.Fatalf("expected first login account personal, got %q", cfg.AccountName)
			}
			if cfg.ExpectedUserID != "303b50f6-d7bf-4856-b2ef-2e11a958795f" {
				t.Fatalf("expected first login expected user id to come from stored personal alias, got %q", cfg.ExpectedUserID)
			}
			return &stakelogin.Result{Account: cfg.AccountName, Status: "session_token_detected", SessionToken: "captured-token"}, nil
		case 2:
			if cfg.AccountName != "personal" {
				t.Fatalf("expected second login account personal, got %q", cfg.AccountName)
			}
			if cfg.ExpectedUserID != "7b1932ce-2a8b-40f3-bc42-b88d23770622" {
				t.Fatalf("expected personal alias to target individual user id, got %q", cfg.ExpectedUserID)
			}
			return &stakelogin.Result{Account: cfg.AccountName, Status: "session_token_detected", SessionToken: "personal-token"}, nil
		case 3:
			if cfg.AccountName != "smsf" {
				t.Fatalf("expected third login account smsf, got %q", cfg.AccountName)
			}
			if cfg.ExpectedUserID != "945365ff-556c-4730-baef-9aad2161b647" {
				t.Fatalf("expected smsf alias to target smsf user id, got %q", cfg.ExpectedUserID)
			}
			return &stakelogin.Result{Account: cfg.AccountName, Status: "session_token_detected", SessionToken: "smsf-token"}, nil
		default:
			t.Fatalf("unexpected extra login call %d", loginCalls)
			return nil, nil
		}
	}

	var stdout bytes.Buffer
	if err := execute(context.Background(), []string{
		"--auth-store", storePath,
		"--base-url", server.URL,
		"auth", "login", "personal",
		"--op-item", "op://Private/stake.com",
		"--op-account", "donald-family.1password.com",
	}, &stdout, io.Discard); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	var response authLoginResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if response.Account.Name != "family-trust" {
		t.Fatalf("expected active account family-trust, got %q", response.Account.Name)
	}
	if len(response.Accounts) != 3 {
		t.Fatalf("expected 3 synced accounts, got %d", len(response.Accounts))
	}
	if loginCalls != 3 {
		t.Fatalf("expected 3 login calls, got %d", loginCalls)
	}

	updatedStore, err := sessionstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	familyTrust, err := updatedStore.Get("family-trust")
	if err != nil {
		t.Fatalf("Get family-trust returned error: %v", err)
	}
	if familyTrust.UserID != "303b50f6-d7bf-4856-b2ef-2e11a958795f" {
		t.Fatalf("expected family-trust user id to be trust, got %q", familyTrust.UserID)
	}
	if familyTrust.SessionToken != "captured-token" {
		t.Fatalf("expected family-trust token to stay captured-token, got %q", familyTrust.SessionToken)
	}
	if familyTrust.OPAccount != "donald-family.1password.com" {
		t.Fatalf("expected family-trust op account to persist, got %q", familyTrust.OPAccount)
	}

	personal, err := updatedStore.Get("personal")
	if err != nil {
		t.Fatalf("Get personal returned error: %v", err)
	}
	if personal.UserID != "7b1932ce-2a8b-40f3-bc42-b88d23770622" {
		t.Fatalf("expected personal user id to be individual, got %q", personal.UserID)
	}
	if personal.SessionToken != "personal-token" {
		t.Fatalf("expected personal token to be refreshed, got %q", personal.SessionToken)
	}

	smsf, err := updatedStore.Get("smsf")
	if err != nil {
		t.Fatalf("Get smsf returned error: %v", err)
	}
	if smsf.UserID != "945365ff-556c-4730-baef-9aad2161b647" {
		t.Fatalf("expected smsf user id to be smsf, got %q", smsf.UserID)
	}
	if smsf.SessionToken != "smsf-token" {
		t.Fatalf("expected smsf token to be refreshed, got %q", smsf.SessionToken)
	}
}

func TestExecuteAuthLoginCommandFailsWhenSwitchedUserValidatesAsDifferentAccount(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch requestCount {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user/product/config" {
				t.Fatalf("expected GET /api/user/product/config, got %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.UserList{
				ActiveUser:   "user-trust",
				MasterUserID: "user-personal",
				Users: []stake.ListedUser{
					{UserID: "user-trust", FirstName: "The Trustee for the DONALD FAMILY TRUST", AccountType: "DISCRETIONARY_TRUST"},
					{UserID: "user-personal", FirstName: "Lachlan", LastName: "Donald", AccountType: "INDIVIDUAL"},
				},
			}); err != nil {
				t.Fatalf("encoding product config response: %v", err)
			}
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user" {
				t.Fatalf("expected GET /api/user, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
				t.Fatalf("expected captured token for trust validation, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-trust", Email: "lachlan@ljd.cc", AccountType: "DISCRETIONARY_TRUST"}); err != nil {
				t.Fatalf("encoding trust user: %v", err)
			}
		case 3:
			if r.Method != http.MethodGet || r.URL.Path != "/api/user" {
				t.Fatalf("expected GET /api/user, got %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "personal-token" {
				t.Fatalf("expected personal token for personal validation, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-trust", Email: "lachlan@ljd.cc", AccountType: "DISCRETIONARY_TRUST"}); err != nil {
				t.Fatalf("encoding mismatched user: %v", err)
			}
		default:
			t.Fatalf("unexpected extra request %d", requestCount)
		}
	}))
	defer server.Close()

	originalRunStakeLogin := runStakeLogin
	t.Cleanup(func() {
		runStakeLogin = originalRunStakeLogin
	})

	loginCalls := 0
	runStakeLogin = func(_ context.Context, cfg stakelogin.Config, logger *log.Logger) (*stakelogin.Result, error) {
		_ = cfg
		_ = logger
		loginCalls++
		switch loginCalls {
		case 1:
			return &stakelogin.Result{Account: cfg.AccountName, Status: "session_token_detected", SessionToken: "captured-token"}, nil
		case 2:
			if cfg.ExpectedUserID != "user-personal" {
				t.Fatalf("expected second login to target user-personal, got %q", cfg.ExpectedUserID)
			}
			return &stakelogin.Result{Account: cfg.AccountName, Status: "session_token_detected", SessionToken: "personal-token"}, nil
		default:
			t.Fatalf("unexpected extra login call %d", loginCalls)
			return nil, nil
		}
	}

	err := execute(context.Background(), []string{
		"--auth-store", storePath,
		"--base-url", server.URL,
		"auth", "login", "personal",
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected execute to fail when an alias token validates as the wrong user")
	}
	if !strings.Contains(err.Error(), "expected user") {
		t.Fatalf("expected expected-user mismatch error, got %v", err)
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
		if r.URL.Path == "/api/user/product/config" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
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

func TestExecuteAuthAddCommandRejectsBlankName(t *testing.T) {
	serverCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := execute(context.Background(), []string{
		"--auth-store", filepath.Join(t.TempDir(), "accounts.json"),
		"--base-url", server.URL,
		"auth", "add", "   ",
		"--token", "initial-token",
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected execute to fail for a blank account name")
	}
	if !strings.Contains(err.Error(), "account name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if serverCalled {
		t.Fatal("expected blank account name to fail before any API call")
	}
}

func TestExecuteTradesCommandPreservesConcurrentStoreUpdates(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "accounts.json")
	store := &sessionstore.File{}
	store.Upsert(sessionstore.Entry{
		Name:         "primary",
		SessionToken: "stored-token",
		OPItem:       "op://Private/old",
		OPAccount:    "old.1password.com",
	})
	if err := sessionstore.Save(storePath, store); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/user":
			if err := sessionstore.Update(storePath, func(store *sessionstore.File) error {
				entry, err := store.Get("primary")
				if err != nil {
					return err
				}
				entry.OPItem = "op://Private/new"
				entry.OPAccount = "new.1password.com"
				store.Upsert(*entry)
				return nil
			}); err != nil {
				t.Fatalf("Update returned error: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(stake.User{UserID: "user-123", Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
				t.Fatalf("encoding response: %v", err)
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/asx/orders/tradeActivity"):
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "hasNext": false, "page": 0, "totalItems": 0}); err != nil {
				t.Fatalf("encoding ASX response: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/users/accounts/accountTransactions":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode([]any{}); err != nil {
				t.Fatalf("encoding US response: %v", err)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := execute(context.Background(), []string{"--auth-store", storePath, "--base-url", server.URL, "trades", "primary"}, io.Discard, &bytes.Buffer{}); err != nil {
		t.Fatalf("execute returned error: %v", err)
	}

	updatedStore, err := sessionstore.Load(storePath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	entry, err := updatedStore.Get("primary")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.OPItem != "op://Private/new" {
		t.Fatalf("expected concurrent OP item update to persist, got %q", entry.OPItem)
	}
	if entry.OPAccount != "new.1password.com" {
		t.Fatalf("expected concurrent OP account update to persist, got %q", entry.OPAccount)
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
