package stakelogin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/stealth"
	"github.com/lox/stake-cli/pkg/stake"
)

// Config describes the browser-first Stake login inputs.
type Config struct {
	AccountName    string
	APIBaseURL     string
	ExpectedUserID string
	LoginURL       string
	BrowserTimeout time.Duration
	ShowBrowser    bool
	KeepBrowser    bool
	PromptInput    io.Reader
}

// Result captures the outcome of a browser-first login attempt.
type Result struct {
	Account        string    `json:"account"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	LoginURL       string    `json:"login_url"`
	CurrentURL     string    `json:"current_url,omitempty"`
	BrowserVisible bool      `json:"browser_visible"`
	KeepBrowser    bool      `json:"keep_browser"`
	Status         string    `json:"status"`
	Notes          []string  `json:"notes,omitempty"`
	SessionToken   string    `json:"-"`
}

type browserSession struct {
	page    *rod.Page
	cleanup func()
}

type sessionInspector struct {
	currentURL   func(context.Context) (string, error)
	sessionToken func(context.Context) (string, error)
	align        func(context.Context, string) ([]string, string, error)
}

type capturedSession struct {
	currentURL   string
	sessionToken string
	notes        []string
}

var automaticCapturePollInterval = time.Second
var errSessionNotReady = errors.New("stake session is not ready yet")

// Run launches a browser to the Stake sign-in page and captures a confirmed session token.
func Run(ctx context.Context, cfg Config, logger *log.Logger) (*Result, error) {
	if logger == nil {
		logger = log.New(io.Discard)
	}

	if err := cfg.normalizeAndValidate(); err != nil {
		return nil, err
	}

	startedAt := time.Now().UTC()
	session, err := prepareBrowser(cfg)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()

	if cfg.KeepBrowser {
		logger.Info("Leaving Stake login browser open for authentication", "account", cfg.AccountName)

		inspector := sessionInspector{
			currentURL: func(ctx context.Context) (string, error) {
				return currentPageURL(ctx, session.page)
			},
			sessionToken: func(ctx context.Context) (string, error) {
				return currentSessionToken(ctx, session.page)
			},
			align: func(ctx context.Context, sessionToken string) ([]string, string, error) {
				return alignSessionTokenToExpectedUser(ctx, cfg, logger, sessionToken)
			},
		}

		var (
			captured *capturedSession
			waitErr  error
		)
		if cfg.ExpectedUserID == "" {
			logger.Info("Press Enter when the correct Stake account is active in the browser", "account", cfg.AccountName)
			waitErr = waitForConfirmation(ctx, cfg.PromptInput)
			if waitErr == nil {
				captured, waitErr = captureConfirmedSession(ctx, inspector)
			}
		} else {
			logger.Info(
				"Waiting for Stake to expose a session for the stored account",
				"account", cfg.AccountName,
				"user_id", cfg.ExpectedUserID,
			)
			captured, waitErr = waitForExpectedSession(ctx, logger, inspector)
		}

		endedAt := time.Now().UTC()
		if waitErr == nil {
			logger.Info("Detected Stake session token in browser storage", "account", cfg.AccountName, "url", captured.currentURL)
			return &Result{
				Account:        cfg.AccountName,
				StartedAt:      startedAt,
				EndedAt:        endedAt,
				LoginURL:       cfg.LoginURL,
				CurrentURL:     captured.currentURL,
				BrowserVisible: cfg.ShowBrowser,
				KeepBrowser:    cfg.KeepBrowser,
				Status:         "session_token_detected",
				SessionToken:   captured.sessionToken,
				Notes: append([]string{
					"Stealth browser launched and navigated to the Stake login page.",
				}, captured.notes...),
			}, nil
		}
		if errors.Is(waitErr, context.Canceled) {
			logger.Info("Stake login canceled; closing browser", "account", cfg.AccountName)
			return &Result{
				Account:        cfg.AccountName,
				StartedAt:      startedAt,
				EndedAt:        endedAt,
				LoginURL:       cfg.LoginURL,
				BrowserVisible: cfg.ShowBrowser,
				KeepBrowser:    cfg.KeepBrowser,
				Status:         "canceled",
				Notes: []string{
					"Stealth browser launched and navigated to the Stake login page.",
					"Browser was closed after cancellation.",
				},
			}, nil
		}

		return nil, waitErr
	}

	logger.Info("Prepared Stake login page shell", "account", cfg.AccountName, "url", cfg.LoginURL, "visible", cfg.ShowBrowser)

	return &Result{
		Account:        cfg.AccountName,
		StartedAt:      startedAt,
		EndedAt:        time.Now().UTC(),
		LoginURL:       cfg.LoginURL,
		BrowserVisible: cfg.ShowBrowser,
		KeepBrowser:    cfg.KeepBrowser,
		Status:         "manual_login_pending",
		Notes: []string{
			"Stealth browser launched and navigated to the Stake login page.",
			"Complete the Stake login manually in the opened browser window.",
			"Session-token capture runs only after manual confirmation; auto-close mode skips capture.",
		},
	}, nil
}

func (c *Config) normalizeAndValidate() error {
	c.AccountName = strings.TrimSpace(c.AccountName)
	c.APIBaseURL = strings.TrimSpace(c.APIBaseURL)
	c.ExpectedUserID = strings.TrimSpace(c.ExpectedUserID)
	c.LoginURL = strings.TrimSpace(c.LoginURL)

	if c.AccountName == "" {
		return fmt.Errorf("account name is required")
	}
	if c.LoginURL == "" {
		return fmt.Errorf("login URL is required")
	}
	if c.BrowserTimeout <= 0 {
		return fmt.Errorf("browser timeout must be greater than zero")
	}
	if !c.ShowBrowser && c.KeepBrowser {
		return fmt.Errorf("headless login cannot keep the browser open for manual authentication")
	}
	if c.KeepBrowser && c.ExpectedUserID == "" && c.PromptInput == nil {
		return fmt.Errorf("login confirmation input is required")
	}

	return nil
}

func alignSessionTokenToExpectedUser(ctx context.Context, cfg Config, logger *log.Logger, sessionToken string) ([]string, string, error) {
	sessionToken = normalizeSessionToken(sessionToken)
	if sessionToken == "" {
		return nil, "", fmt.Errorf("stake session token is required")
	}
	if cfg.ExpectedUserID == "" {
		return nil, sessionToken, nil
	}

	client := stake.NewClient(stake.Config{
		BaseURL:      cfg.APIBaseURL,
		Timeout:      cfg.BrowserTimeout,
		SessionToken: sessionToken,
	}, logger)

	user, err := client.ValidateSession(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("validate confirmed session token: %w", err)
	}
	if strings.TrimSpace(user.UserID) == cfg.ExpectedUserID {
		logger.Info("Confirmed Stake session already matches the expected account", "account", cfg.AccountName, "user_id", cfg.ExpectedUserID)
		return []string{"Confirmed browser session already matched the stored Stake user_id."}, client.SessionToken(), nil
	}

	logger.Info(
		"Confirmed Stake session belongs to a different account; attempting switch",
		"account", cfg.AccountName,
		"current_user_id", strings.TrimSpace(user.UserID),
		"expected_user_id", cfg.ExpectedUserID,
	)
	if err := client.SwitchUser(ctx, cfg.ExpectedUserID); err != nil {
		return nil, "", fmt.Errorf("switch confirmed session to expected user_id %q: %w", cfg.ExpectedUserID, err)
	}

	user, err = client.ValidateSession(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("validate switched session token: %w", err)
	}
	if strings.TrimSpace(user.UserID) != cfg.ExpectedUserID {
		return nil, "", fmt.Errorf("confirmed session switched to user_id %q, expected %q", strings.TrimSpace(user.UserID), cfg.ExpectedUserID)
	}

	logger.Info("Switched Stake session to the expected account", "account", cfg.AccountName, "user_id", cfg.ExpectedUserID)
	return []string{"Confirmed browser session was switched to the stored Stake user_id before capture."}, client.SessionToken(), nil
}

func captureConfirmedSession(ctx context.Context, inspector sessionInspector) (*capturedSession, error) {
	currentURL, err := inspector.currentURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect confirmed page: %w", err)
	}
	currentToken, err := inspector.sessionToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect confirmed login storage: %w", err)
	}
	if normalizeSessionToken(currentToken) == "" {
		return nil, fmt.Errorf("no sessionKey found in browser storage for %q", currentURL)
	}

	notes, sessionToken, err := inspector.align(ctx, currentToken)
	if err != nil {
		return nil, err
	}

	return &capturedSession{
		currentURL:   currentURL,
		sessionToken: sessionToken,
		notes: append([]string{
			"Session token capture was triggered by manual confirmation.",
			"A non-empty localStorage sessionKey was detected.",
		}, notes...),
	}, nil
}

func waitForExpectedSession(ctx context.Context, logger *log.Logger, inspector sessionInspector) (*capturedSession, error) {
	ticker := time.NewTicker(automaticCapturePollInterval)
	defer ticker.Stop()

	for {
		captured, err := captureExpectedSessionIfReady(ctx, inspector)
		if err == nil && captured != nil {
			return captured, nil
		}
		if errors.Is(err, errSessionNotReady) {
			logger.Debug("Stake session is not ready for automatic capture yet", "error", err)
		} else if err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func captureExpectedSessionIfReady(ctx context.Context, inspector sessionInspector) (*capturedSession, error) {
	currentToken, err := inspector.sessionToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect login storage: %w", err)
	}
	if normalizeSessionToken(currentToken) == "" {
		return nil, errSessionNotReady
	}

	notes, sessionToken, err := inspector.align(ctx, currentToken)
	if err != nil {
		return nil, err
	}

	currentURL, err := inspector.currentURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect confirmed page: %w", err)
	}

	return &capturedSession{
		currentURL:   currentURL,
		sessionToken: sessionToken,
		notes: append([]string{
			"Session token capture was triggered automatically after the stored Stake account became available.",
			"A non-empty localStorage sessionKey was detected.",
		}, notes...),
	}, nil
}

func prepareBrowser(cfg Config) (*browserSession, error) {
	controlURL, err := newLauncher(cfg).Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(controlURL).Timeout(cfg.BrowserTimeout)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	page, err := stealth.Page(browser)
	if err != nil {
		_ = browser.Close()
		return nil, fmt.Errorf("create stealth page: %w", err)
	}
	if err := page.Navigate(cfg.LoginURL); err != nil {
		_ = page.Close()
		_ = browser.Close()
		return nil, fmt.Errorf("navigate login page: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		_ = page.Close()
		_ = browser.Close()
		return nil, fmt.Errorf("wait for login page load: %w", err)
	}

	return &browserSession{
		page: page,
		cleanup: func() {
			_ = page.Close()
			_ = browser.Close()
		},
	}, nil
}

func newLauncher(cfg Config) *launcher.Launcher {
	l := launcher.New().Headless(!cfg.ShowBrowser)
	if cfg.KeepBrowser {
		l = l.Leakless(false)
	}
	return l
}

func waitForConfirmation(ctx context.Context, input io.Reader) error {
	if input == nil {
		return fmt.Errorf("login confirmation input is required")
	}

	resultCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(input)
		_, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			resultCh <- fmt.Errorf("read login confirmation: %w", err)
			return
		}
		if errors.Is(err, io.EOF) {
			resultCh <- fmt.Errorf("read login confirmation: unexpected end of input")
			return
		}
		resultCh <- nil
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-resultCh:
		return err
	}
}

func currentPageURL(ctx context.Context, page *rod.Page) (string, error) {
	if page == nil {
		return "", fmt.Errorf("login page is required")
	}

	info, err := page.Context(ctx).Info()
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", nil
	}
	return strings.TrimSpace(info.URL), nil
}

func currentSessionToken(ctx context.Context, page *rod.Page) (string, error) {
	if page == nil {
		return "", fmt.Errorf("login page is required")
	}

	value, err := page.Context(ctx).Eval(`() => {
		try {
			return localStorage.getItem("sessionKey") || ""
		} catch (_) {
			return ""
		}
	}`)
	if err != nil {
		return "", err
	}
	if value == nil || value.Value.Nil() {
		return "", nil
	}

	return normalizeSessionToken(value.Value.Str()), nil
}

func normalizeSessionToken(raw string) string {
	replacer := strings.NewReplacer("\"", "", "'", "")
	return strings.TrimSpace(replacer.Replace(raw))
}
