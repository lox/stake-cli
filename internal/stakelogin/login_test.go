package stakelogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/lox/stake-cli/internal/onepassword"
)

func TestNewLauncherRespectsVisibilityAndLifetime(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Config
		wantHeadless bool
		wantLeakless bool
	}{
		{
			name: "manual browser stays open",
			cfg: Config{
				ShowBrowser: true,
				KeepBrowser: true,
			},
			wantHeadless: false,
			wantLeakless: false,
		},
		{
			name: "auto close visible browser uses leakless",
			cfg: Config{
				ShowBrowser: true,
				KeepBrowser: false,
			},
			wantHeadless: false,
			wantLeakless: true,
		},
		{
			name: "headless auto close remains headless and leakless",
			cfg: Config{
				ShowBrowser: false,
				KeepBrowser: false,
			},
			wantHeadless: true,
			wantLeakless: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLauncher(tt.cfg)

			if got := l.Has(flags.Headless); got != tt.wantHeadless {
				t.Fatalf("headless = %v, want %v", got, tt.wantHeadless)
			}
			if got := l.Has(flags.Leakless); got != tt.wantLeakless {
				t.Fatalf("leakless = %v, want %v", got, tt.wantLeakless)
			}
		})
	}
}

func TestWaitForConfirmationAcceptsEnter(t *testing.T) {
	if err := waitForConfirmation(context.Background(), strings.NewReader("\n")); err != nil {
		t.Fatalf("waitForConfirmation returned error: %v", err)
	}
}

func TestNormalizeSessionToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "plain-token", want: "plain-token"},
		{input: `'quoted-token'`, want: "quoted-token"},
		{input: "\"double-quoted-token\"", want: "double-quoted-token"},
		{input: ` 'mixed-token' `, want: "mixed-token"},
	}

	for _, tt := range tests {
		if got := normalizeSessionToken(tt.input); got != tt.want {
			t.Fatalf("normalizeSessionToken(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAlignSessionTokenToExpectedUserSwitchesAccount(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch requestCount {
		case 1:
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET request, got %s", r.Method)
			}
			if r.URL.Path != "/api/user" {
				t.Fatalf("expected /api/user path, got %s", r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "captured-token" {
				t.Fatalf("expected captured token header, got %q", got)
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{"userId": "default-user"}); err != nil {
				t.Fatalf("encoding initial user response: %v", err)
			}
		case 2:
			if r.Method != http.MethodPut {
				t.Fatalf("expected PUT request, got %s", r.Method)
			}
			if r.URL.Path != "/api/user/switch" {
				t.Fatalf("expected /api/user/switch path, got %s", r.URL.Path)
			}

			var payload struct {
				UserID string `json:"userId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decoding switch payload: %v", err)
			}
			if payload.UserID != "target-user" {
				t.Fatalf("expected target-user payload, got %q", payload.UserID)
			}

			w.Header().Set("Stake-Session-Token", "switched-token")
			w.WriteHeader(http.StatusNoContent)
		case 3:
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET request, got %s", r.Method)
			}
			if r.URL.Path != "/api/user" {
				t.Fatalf("expected /api/user path, got %s", r.URL.Path)
			}
			if got := r.Header.Get("Stake-Session-Token"); got != "switched-token" {
				t.Fatalf("expected switched token header, got %q", got)
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{"userId": "target-user"}); err != nil {
				t.Fatalf("encoding switched user response: %v", err)
			}
		default:
			t.Fatalf("unexpected extra request %d", requestCount)
		}
	}))
	defer server.Close()

	notes, sessionToken, err := alignSessionTokenToExpectedUser(context.Background(), Config{
		AccountName:    "family-trust",
		APIBaseURL:     server.URL,
		ExpectedUserID: "target-user",
		BrowserTimeout: 5 * time.Second,
	}, log.New(io.Discard), "captured-token")
	if err != nil {
		t.Fatalf("alignSessionTokenToExpectedUser returned error: %v", err)
	}
	if sessionToken != "switched-token" {
		t.Fatalf("expected switched-token, got %q", sessionToken)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "switched") {
		t.Fatalf("expected switch note, got %#v", notes)
	}
}

func TestAlignSessionTokenToExpectedUserWaitsForBrowserTokenAfterSwitch(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch requestCount {
		case 1:
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET request, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{"userId": "default-user"}); err != nil {
				t.Fatalf("encoding initial user response: %v", err)
			}
		case 2:
			if r.Method != http.MethodPut {
				t.Fatalf("expected PUT request, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case 3:
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET request, got %s", r.Method)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"HTTP 401 Unauthorized"}`))
		default:
			t.Fatalf("unexpected extra request %d", requestCount)
		}
	}))
	defer server.Close()

	_, _, err := alignSessionTokenToExpectedUser(context.Background(), Config{
		AccountName:    "personal",
		APIBaseURL:     server.URL,
		ExpectedUserID: "target-user",
		BrowserTimeout: 5 * time.Second,
	}, log.New(io.Discard), "captured-token")
	if !errors.Is(err, errSessionNotReady) {
		t.Fatalf("expected errSessionNotReady, got %v", err)
	}
	if !strings.Contains(err.Error(), "validate switched session token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAlignSessionTokenToExpectedUserWaitsWhenCurrentBrowserTokenIsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/user" {
			t.Fatalf("expected /api/user path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"HTTP 401 Unauthorized"}`))
	}))
	defer server.Close()

	_, _, err := alignSessionTokenToExpectedUser(context.Background(), Config{
		AccountName:    "family-trust",
		APIBaseURL:     server.URL,
		ExpectedUserID: "target-user",
		BrowserTimeout: 5 * time.Second,
	}, log.New(io.Discard), "captured-token")
	if !errors.Is(err, errSessionNotReady) {
		t.Fatalf("expected errSessionNotReady, got %v", err)
	}
	if !strings.Contains(err.Error(), "validate confirmed session token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForExpectedSessionCapturesAutomatically(t *testing.T) {
	originalInterval := automaticCapturePollInterval
	automaticCapturePollInterval = time.Millisecond
	t.Cleanup(func() {
		automaticCapturePollInterval = originalInterval
	})

	checks := 0
	captured, err := waitForExpectedSession(context.Background(), log.New(io.Discard), sessionInspector{
		currentURL: func(context.Context) (string, error) {
			return "https://trading.hellostake.com/platform/dashboard", nil
		},
		sessionToken: func(context.Context) (string, error) {
			checks++
			if checks < 2 {
				return "", nil
			}
			return "captured-token", nil
		},
		align: func(_ context.Context, sessionToken string) ([]string, string, error) {
			if sessionToken != "captured-token" {
				t.Fatalf("expected captured-token, got %q", sessionToken)
			}
			return []string{"Confirmed browser session was switched to the stored Stake user_id before capture."}, "switched-token", nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("waitForExpectedSession returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected automatic capture result")
	}
	if captured.currentURL != "https://trading.hellostake.com/platform/dashboard" {
		t.Fatalf("unexpected current url: %q", captured.currentURL)
	}
	if captured.sessionToken != "switched-token" {
		t.Fatalf("unexpected session token: %q", captured.sessionToken)
	}
	if len(captured.notes) < 2 || !strings.Contains(captured.notes[0], "triggered automatically") {
		t.Fatalf("expected automatic capture note, got %#v", captured.notes)
	}
}

func TestWaitForExpectedSessionReturnsTerminalError(t *testing.T) {
	originalInterval := automaticCapturePollInterval
	automaticCapturePollInterval = time.Millisecond
	t.Cleanup(func() {
		automaticCapturePollInterval = originalInterval
	})

	waited, err := waitForExpectedSession(context.Background(), log.New(io.Discard), sessionInspector{
		currentURL: func(context.Context) (string, error) {
			return "https://trading.hellostake.com/platform/dashboard", nil
		},
		sessionToken: func(context.Context) (string, error) {
			return "captured-token", nil
		},
		align: func(context.Context, string) ([]string, string, error) {
			return nil, "", errors.New("switch failed")
		},
	}, nil)
	if err == nil {
		t.Fatal("expected terminal automatic capture error")
	}
	if waited != nil {
		t.Fatalf("expected no captured session, got %#v", waited)
	}
	if !strings.Contains(err.Error(), "switch failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeAndValidateAllowsAutomaticCaptureWithoutPromptInput(t *testing.T) {
	cfg := Config{
		AccountName:    "family-trust",
		ExpectedUserID: "target-user",
		LoginURL:       "https://trading.hellostake.com/auth/login",
		BrowserTimeout: 5 * time.Second,
		ShowBrowser:    true,
		KeepBrowser:    true,
	}

	if err := cfg.normalizeAndValidate(); err != nil {
		t.Fatalf("normalizeAndValidate returned error: %v", err)
	}
}

func TestNormalizeAndValidateAllowsOnePasswordAutomationWithoutPromptInput(t *testing.T) {
	cfg := Config{
		AccountName:    "family-trust",
		LoginURL:       "https://trading.hellostake.com/auth/login",
		BrowserTimeout: 5 * time.Second,
		ShowBrowser:    true,
		KeepBrowser:    true,
		OnePassword: OnePasswordConfig{
			ItemReference: "op://stake/family-trust",
		},
	}

	if err := cfg.normalizeAndValidate(); err != nil {
		t.Fatalf("normalizeAndValidate returned error: %v", err)
	}
}

func TestConfiguredLoginCredentialsLoadsOnePasswordItem(t *testing.T) {
	originalLoader := loadOnePasswordLoginCredentials
	t.Cleanup(func() {
		loadOnePasswordLoginCredentials = originalLoader
	})

	loadOnePasswordLoginCredentials = func(ctx context.Context, auth onepassword.AuthConfig, itemReference string) (onepassword.LoginCredentials, error) {
		if ctx == nil {
			t.Fatal("expected context to be forwarded")
		}
		if itemReference != "op://stake/family-trust" {
			return onepassword.LoginCredentials{}, errors.New("unexpected item reference")
		}
		if auth.ServiceAccountToken != "service-account-token" {
			return onepassword.LoginCredentials{}, errors.New("unexpected service account token")
		}
		if auth.DesktopAccount != "Personal" {
			return onepassword.LoginCredentials{}, errors.New("unexpected desktop account")
		}
		return onepassword.LoginCredentials{
			Email:    "lachlan@example.test",
			Password: "super-secret",
			MFACode:  "123456",
		}, nil
	}

	credentials, err := configuredLoginCredentials(context.Background(), Config{
		OnePassword: OnePasswordConfig{
			ItemReference:       "op://stake/family-trust",
			ServiceAccountToken: "service-account-token",
			DesktopAccount:      "Personal",
		},
	})
	if err != nil {
		t.Fatalf("configuredLoginCredentials returned error: %v", err)
	}
	if credentials == nil {
		t.Fatal("expected login credentials")
	}
	if credentials.Email != "lachlan@example.test" {
		t.Fatalf("unexpected email: %q", credentials.Email)
	}
	if credentials.Password != "super-secret" {
		t.Fatalf("unexpected password: %q", credentials.Password)
	}
	if credentials.MFACode != "123456" {
		t.Fatalf("unexpected MFA code: %q", credentials.MFACode)
	}
}

func TestWaitForConfirmationReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer func() {
		_ = writer.Close()
	}()
	cancel()

	if err := waitForConfirmation(ctx, reader); err == nil {
		t.Fatal("expected canceled wait to return an error")
	}
}
