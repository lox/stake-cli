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
	"github.com/lox/stake-cli/internal/onepassword"
	"github.com/lox/stake-cli/pkg/stake"
)

// Config describes the browser-first Stake login inputs.
type Config struct {
	AccountName    string
	APIBaseURL     string
	ExpectedUserID string
	OnePassword    OnePasswordConfig
	LoginURL       string
	BrowserTimeout time.Duration
	ShowBrowser    bool
	KeepBrowser    bool
	PromptInput    io.Reader
}

// OnePasswordConfig controls how Stake login automation loads credentials.
type OnePasswordConfig struct {
	ItemReference       string
	DesktopAccount      string
	ServiceAccountToken string
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

type loginCredentials struct {
	Email    string
	Password string
	MFACode  string
}

var automaticCapturePollInterval = time.Second
var errSessionNotReady = errors.New("stake session is not ready yet")
var credentialEntryPollInterval = 500 * time.Millisecond
var credentialStepTimeout = 30 * time.Second
var automaticLoginRetryInterval = 5 * time.Second
var loadOnePasswordLoginCredentials = onepassword.LoadLoginCredentials

// Run launches a browser to the Stake sign-in page and captures a confirmed session token.
func Run(ctx context.Context, cfg Config, logger *log.Logger) (*Result, error) {
	if logger == nil {
		logger = log.New(io.Discard)
	}

	if err := cfg.normalizeAndValidate(); err != nil {
		return nil, err
	}

	credentials, err := configuredLoginCredentials(ctx, cfg)
	if err != nil {
		return nil, err
	}

	startedAt := time.Now().UTC()
	session, err := prepareBrowser(cfg)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()

	notes := []string{"Stealth browser launched and navigated to the Stake login page."}
	if credentials != nil {
		logger.Info("Populating Stake login form from 1Password", "account", cfg.AccountName, "item", cfg.OnePassword.ItemReference)

		automationNotes, err := enterLoginCredentials(ctx, session.page, credentials)
		if err != nil {
			return nil, err
		}
		notes = append(notes, automationNotes...)
	}

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

	automatedLogin := credentials != nil

	if cfg.KeepBrowser || automatedLogin {
		if automatedLogin {
			logger.Info("Waiting for Stake to expose a session after automated sign-in", "account", cfg.AccountName)
		} else {
			logger.Info("Leaving Stake login browser open for authentication", "account", cfg.AccountName)
		}

		var (
			captured *capturedSession
			waitErr  error
		)
		if automatedLogin || cfg.ExpectedUserID != "" {
			if cfg.ExpectedUserID != "" {
				logger.Info(
					"Waiting for Stake to expose a session for the stored account",
					"account", cfg.AccountName,
					"user_id", cfg.ExpectedUserID,
				)
			}
			var reenterLogin func(context.Context) error
			if automatedLogin {
				lastRetryAttempt := time.Time{}
				reenterLogin = func(ctx context.Context) error {
					if time.Since(lastRetryAttempt) < automaticLoginRetryInterval {
						return nil
					}

					currentURL, err := currentPageURL(ctx, session.page)
					if err != nil {
						return fmt.Errorf("inspect page before login retry: %w", err)
					}
					if !strings.Contains(strings.ToLower(currentURL), "/auth/login") {
						return nil
					}

					lastRetryAttempt = time.Now()
					logger.Info("Stake returned to the login page; retrying automated sign-in", "account", cfg.AccountName, "url", currentURL)
					_, err = enterLoginCredentials(ctx, session.page, credentials)
					return err
				}
			}
			captured, waitErr = waitForExpectedSession(ctx, logger, inspector, reenterLogin)
		} else {
			logger.Info("Press Enter when the correct Stake account is active in the browser", "account", cfg.AccountName)
			waitErr = waitForConfirmation(ctx, cfg.PromptInput)
			if waitErr == nil {
				captured, waitErr = captureConfirmedSession(ctx, inspector)
			}
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
				Notes:          append(notes, captured.notes...),
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
				Notes:          append(notes, "Browser was closed after cancellation."),
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
		Notes: append(notes,
			"Complete the Stake login manually in the opened browser window.",
			"Session-token capture runs only after manual confirmation; auto-close mode skips capture.",
		),
	}, nil
}

func (c *Config) normalizeAndValidate() error {
	c.AccountName = strings.TrimSpace(c.AccountName)
	c.APIBaseURL = strings.TrimSpace(c.APIBaseURL)
	c.ExpectedUserID = strings.TrimSpace(c.ExpectedUserID)
	c.LoginURL = strings.TrimSpace(c.LoginURL)
	c.OnePassword.ItemReference = strings.TrimSpace(c.OnePassword.ItemReference)
	c.OnePassword.DesktopAccount = strings.TrimSpace(c.OnePassword.DesktopAccount)
	c.OnePassword.ServiceAccountToken = strings.TrimSpace(c.OnePassword.ServiceAccountToken)

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
	if c.OnePassword.ItemReference != "" && c.OnePassword.DesktopAccount != "" && c.OnePassword.ServiceAccountToken != "" {
		return fmt.Errorf("1Password auth must use either a service account token or a desktop account, not both")
	}
	if c.KeepBrowser && c.ExpectedUserID == "" && c.PromptInput == nil && c.OnePassword.ItemReference == "" {
		return fmt.Errorf("login confirmation input is required")
	}

	return nil
}

func configuredLoginCredentials(ctx context.Context, cfg Config) (*loginCredentials, error) {
	if cfg.OnePassword.ItemReference == "" {
		return nil, nil
	}

	resolved, err := loadOnePasswordLoginCredentials(ctx, onepassword.AuthConfig{
		ServiceAccountToken: cfg.OnePassword.ServiceAccountToken,
		DesktopAccount:      cfg.OnePassword.DesktopAccount,
	}, cfg.OnePassword.ItemReference)
	if err != nil {
		return nil, fmt.Errorf("load 1Password login credentials: %w", err)
	}

	return &loginCredentials{
		Email:    resolved.Email,
		Password: resolved.Password,
		MFACode:  resolved.MFACode,
	}, nil
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
		if isStakeUnauthorized(err) {
			logger.Info(
				"Stake browser session token is present, but not ready for validation yet",
				"account", cfg.AccountName,
				"expected_user_id", cfg.ExpectedUserID,
				"error", err,
			)
			return nil, "", fmt.Errorf("%w: validate confirmed session token: %v", errSessionNotReady, err)
		}
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
		logger.Info(
			"Stake account switch completed, but the browser session token is not ready for validation yet",
			"account", cfg.AccountName,
			"expected_user_id", cfg.ExpectedUserID,
			"error", err,
		)
		return nil, "", fmt.Errorf("%w: validate switched session token: %v", errSessionNotReady, err)
	}
	if strings.TrimSpace(user.UserID) != cfg.ExpectedUserID {
		logger.Info(
			"Stake account switch completed, but the expected account is not active in browser storage yet",
			"account", cfg.AccountName,
			"current_user_id", strings.TrimSpace(user.UserID),
			"expected_user_id", cfg.ExpectedUserID,
		)
		return nil, "", fmt.Errorf("%w: confirmed session switched to user_id %q, expected %q", errSessionNotReady, strings.TrimSpace(user.UserID), cfg.ExpectedUserID)
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

func waitForExpectedSession(ctx context.Context, logger *log.Logger, inspector sessionInspector, reenterLogin func(context.Context) error) (*capturedSession, error) {
	ticker := time.NewTicker(automaticCapturePollInterval)
	defer ticker.Stop()

	for {
		captured, err := captureExpectedSessionIfReady(ctx, inspector)
		if err == nil && captured != nil {
			return captured, nil
		}
		if errors.Is(err, errSessionNotReady) || isTransientAutomationError(err) {
			logger.Debug("Stake session is not ready for automatic capture yet", "error", err)
			if reenterLogin != nil {
				if retryErr := reenterLogin(ctx); retryErr != nil {
					return nil, retryErr
				}
			}
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
			"Session token capture was triggered automatically after a browser session became available.",
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

func enterLoginCredentials(ctx context.Context, page *rod.Page, credentials *loginCredentials) ([]string, error) {
	if page == nil {
		return nil, fmt.Errorf("login page is required")
	}
	if credentials == nil {
		return nil, fmt.Errorf("login credentials are required")
	}

	if err := waitForCredentialStep(ctx, page, "email", enterEmailStepScript, credentials.Email); err != nil {
		return nil, err
	}
	if err := waitForCredentialStep(ctx, page, "password", enterPasswordStepScript, credentials.Password); err != nil {
		return nil, err
	}

	notes := []string{"Stake login email and password were populated from 1Password."}
	if credentials.MFACode != "" {
		entered, err := enterMFACodeIfPrompted(ctx, page, credentials.MFACode)
		if err != nil {
			return nil, err
		}
		if entered {
			notes = append(notes, "1Password one-time password was entered automatically when requested.")
		}
	}

	return notes, nil
}

func waitForCredentialStep(ctx context.Context, page *rod.Page, fieldName string, script string, value string) error {
	stepCtx, cancel := context.WithTimeout(ctx, credentialStepTimeout)
	defer cancel()

	for {
		token, err := currentSessionToken(stepCtx, page)
		if err != nil {
			if isTransientAutomationError(err) {
				if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
					return fmt.Errorf("wait for %s field: %w", fieldName, stepCtx.Err())
				}
				continue
			}
			return fmt.Errorf("inspect login state while waiting for %s field: %w", fieldName, err)
		}
		if token != "" {
			return nil
		}

		result, err := evalPageString(stepCtx, page, script, value)
		if err != nil {
			if isTransientAutomationError(err) {
				if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
					return fmt.Errorf("wait for %s field: %w", fieldName, stepCtx.Err())
				}
				continue
			}
			return fmt.Errorf("enter %s: %w", fieldName, err)
		}

		switch result {
		case "done", "skip":
			return nil
		case "wait":
		default:
			return fmt.Errorf("enter %s: unexpected automation result %q", fieldName, result)
		}

		if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
			return fmt.Errorf("wait for %s field: %w", fieldName, stepCtx.Err())
		}
	}
}

func enterMFACodeIfPrompted(ctx context.Context, page *rod.Page, code string) (bool, error) {
	stepCtx, cancel := context.WithTimeout(ctx, credentialStepTimeout)
	defer cancel()

	for {
		token, err := currentSessionToken(stepCtx, page)
		if err != nil {
			if isTransientAutomationError(err) {
				if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
					return false, fmt.Errorf("wait for MFA prompt: %w", stepCtx.Err())
				}
				continue
			}
			return false, fmt.Errorf("inspect login state while waiting for MFA prompt: %w", err)
		}
		if token != "" {
			return false, nil
		}

		result, err := evalPageString(stepCtx, page, enterMFACodeStepScript, code)
		if err != nil {
			if isTransientAutomationError(err) {
				if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
					return false, fmt.Errorf("wait for MFA prompt: %w", stepCtx.Err())
				}
				continue
			}
			return false, fmt.Errorf("enter MFA code: %w", err)
		}

		switch result {
		case "done":
			return true, nil
		case "wait":
		default:
			return false, fmt.Errorf("enter MFA code: unexpected automation result %q", result)
		}

		if err := sleepWithContext(stepCtx, credentialEntryPollInterval); err != nil {
			return false, fmt.Errorf("wait for MFA prompt: %w", stepCtx.Err())
		}
	}
}

func evalPageString(ctx context.Context, page *rod.Page, script string, args ...interface{}) (string, error) {
	value, err := page.Context(ctx).Eval(script, args...)
	if err != nil {
		return "", err
	}
	if value == nil || value.Value.Nil() {
		return "", nil
	}

	return strings.TrimSpace(value.Value.Str()), nil
}

func sleepWithContext(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransientAutomationError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "execution context was destroyed") ||
		strings.Contains(message, "cannot find context with specified id") ||
		strings.Contains(message, "cannot find object with given id") ||
		strings.Contains(message, "inspected target navigated or closed")
}

func isStakeUnauthorized(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(strings.ToLower(err.Error()), "api error 401")
}

const enterEmailStepScript = `email => {
	const isVisible = (element) => {
		if (!element || !(element instanceof HTMLElement)) {
			return false
		}
		const style = window.getComputedStyle(element)
		const rect = element.getBoundingClientRect()
		return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0 && !element.hasAttribute("disabled") && !element.hasAttribute("readonly")
	}
	const queryVisible = (selectors) => {
		for (const selector of selectors) {
			for (const element of document.querySelectorAll(selector)) {
				if (isVisible(element)) {
					return element
				}
			}
		}
		return null
	}
	const setValue = (element, value) => {
		element.focus()
		const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
		const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set
		if (setter) {
			setter.call(element, value)
		} else {
			element.value = value
		}
		element.dispatchEvent(new Event("input", { bubbles: true }))
		element.dispatchEvent(new Event("change", { bubbles: true }))
		element.blur()
	}
	const submitForm = (element) => {
		const clickSubmitter = (submitter) => {
			if (!submitter || !isVisible(submitter) || submitter.disabled) {
				return false
			}
			submitter.click()
			return true
		}
		const findVisibleSubmitter = (root) => {
			if (!root) {
				return null
			}
			const selectors = [
				'button[type="submit"]',
				'input[type="submit"]',
				'button',
				'[role="button"]'
			]
			for (const selector of selectors) {
				for (const candidate of root.querySelectorAll(selector)) {
					const text = (candidate.innerText || candidate.value || "").trim().toLowerCase()
					if (isVisible(candidate) && (!text || /log in|login|sign in|continue|next|submit/.test(text))) {
						return candidate
					}
				}
			}
			return null
		}
		const form = element && element.form
		if (form) {
			const submitter = findVisibleSubmitter(form)
			if (clickSubmitter(submitter)) {
				return true
			}
			if (typeof form.requestSubmit === "function") {
				form.requestSubmit()
				return true
			}
			form.submit()
			return true
		}
		const container = element && (element.closest('form, [role="form"], mat-card, .mat-mdc-card, .mat-card, .cdk-overlay-pane, body') || document.body)
		if (clickSubmitter(findVisibleSubmitter(container)) || clickSubmitter(findVisibleSubmitter(document))) {
			return true
		}
		element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true }))
		element.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", code: "Enter", bubbles: true }))
		return false
	}
	const emailInput = queryVisible([
		'input[type="email"]',
		'input[name="email"]',
		'input[name="username"]',
		'input[id*="email" i]',
		'input[id*="user" i]',
		'input[autocomplete="email"]',
		'input[autocomplete="username"]'
	])
	const passwordInput = queryVisible([
		'input[type="password"]',
		'input[name="password"]',
		'input[id*="password" i]',
		'input[autocomplete="current-password"]',
		'input[autocomplete="new-password"]'
	])
	if (!emailInput) {
		if (passwordInput) {
			return "skip"
		}
		return "wait"
	}
	setValue(emailInput, email)
	if (!passwordInput) {
		submitForm(emailInput)
	}
	return "done"
}`

const enterPasswordStepScript = `password => {
	const isVisible = (element) => {
		if (!element || !(element instanceof HTMLElement)) {
			return false
		}
		const style = window.getComputedStyle(element)
		const rect = element.getBoundingClientRect()
		return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0 && !element.hasAttribute("disabled") && !element.hasAttribute("readonly")
	}
	const queryVisible = (selectors) => {
		for (const selector of selectors) {
			for (const element of document.querySelectorAll(selector)) {
				if (isVisible(element)) {
					return element
				}
			}
		}
		return null
	}
	const setValue = (element, value) => {
		element.focus()
		const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
		const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set
		if (setter) {
			setter.call(element, value)
		} else {
			element.value = value
		}
		element.dispatchEvent(new Event("input", { bubbles: true }))
		element.dispatchEvent(new Event("change", { bubbles: true }))
		element.blur()
	}
	const submitForm = (element) => {
		const clickSubmitter = (submitter) => {
			if (!submitter || !isVisible(submitter) || submitter.disabled) {
				return false
			}
			submitter.click()
			return true
		}
		const findVisibleSubmitter = (root) => {
			if (!root) {
				return null
			}
			const selectors = [
				'button[type="submit"]',
				'input[type="submit"]',
				'button',
				'[role="button"]'
			]
			for (const selector of selectors) {
				for (const candidate of root.querySelectorAll(selector)) {
					const text = (candidate.innerText || candidate.value || "").trim().toLowerCase()
					if (isVisible(candidate) && (!text || /log in|login|sign in|continue|next|submit/.test(text))) {
						return candidate
					}
				}
			}
			return null
		}
		const form = element && element.form
		if (form) {
			const submitter = findVisibleSubmitter(form)
			if (clickSubmitter(submitter)) {
				return true
			}
			if (typeof form.requestSubmit === "function") {
				form.requestSubmit()
				return true
			}
			form.submit()
			return true
		}
		const container = element && (element.closest('form, [role="form"], mat-card, .mat-mdc-card, .mat-card, .cdk-overlay-pane, body') || document.body)
		if (clickSubmitter(findVisibleSubmitter(container)) || clickSubmitter(findVisibleSubmitter(document))) {
			return true
		}
		element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true }))
		element.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", code: "Enter", bubbles: true }))
		return false
	}
	const passwordInput = queryVisible([
		'input[type="password"]',
		'input[name="password"]',
		'input[id*="password" i]',
		'input[autocomplete="current-password"]',
		'input[autocomplete="new-password"]'
	])
	if (!passwordInput) {
		return "wait"
	}
	setValue(passwordInput, password)
	submitForm(passwordInput)
	return "done"
}`

const enterMFACodeStepScript = `code => {
	const isVisible = (element) => {
		if (!element || !(element instanceof HTMLElement)) {
			return false
		}
		const style = window.getComputedStyle(element)
		const rect = element.getBoundingClientRect()
		return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0 && !element.hasAttribute("disabled") && !element.hasAttribute("readonly")
	}
	const queryVisible = (selectors) => {
		for (const selector of selectors) {
			for (const element of document.querySelectorAll(selector)) {
				if (isVisible(element)) {
					return element
				}
			}
		}
		return null
	}
	const setValue = (element, value) => {
		element.focus()
		const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
		const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set
		if (setter) {
			setter.call(element, value)
		} else {
			element.value = value
		}
		element.dispatchEvent(new Event("input", { bubbles: true }))
		element.dispatchEvent(new Event("change", { bubbles: true }))
		element.blur()
	}
	const submitForm = (element) => {
		const clickSubmitter = (submitter) => {
			if (!submitter || !isVisible(submitter) || submitter.disabled) {
				return false
			}
			submitter.click()
			return true
		}
		const findVisibleSubmitter = (root) => {
			if (!root) {
				return null
			}
			const selectors = [
				'button[type="submit"]',
				'input[type="submit"]',
				'button',
				'[role="button"]'
			]
			for (const selector of selectors) {
				for (const candidate of root.querySelectorAll(selector)) {
					const text = (candidate.innerText || candidate.value || "").trim().toLowerCase()
					if (isVisible(candidate) && (!text || /log in|login|sign in|continue|next|submit|verify/.test(text))) {
						return candidate
					}
				}
			}
			return null
		}
		const form = element && element.form
		if (form) {
			const submitter = findVisibleSubmitter(form)
			if (clickSubmitter(submitter)) {
				return true
			}
			if (typeof form.requestSubmit === "function") {
				form.requestSubmit()
				return true
			}
			form.submit()
			return true
		}
		const container = element && (element.closest('form, [role="form"], mat-card, .mat-mdc-card, .mat-card, .cdk-overlay-pane, body') || document.body)
		if (clickSubmitter(findVisibleSubmitter(container)) || clickSubmitter(findVisibleSubmitter(document))) {
			return true
		}
		element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true }))
		element.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", code: "Enter", bubbles: true }))
		return false
	}
	const mfaInput = queryVisible([
		'input[autocomplete="one-time-code"]',
		'input[name*="otp" i]',
		'input[name*="code" i]',
		'input[id*="otp" i]',
		'input[id*="code" i]',
		'input[inputmode="numeric"]'
	])
	if (!mfaInput) {
		return "wait"
	}
	setValue(mfaInput, code)
	submitForm(mfaInput)
	return "done"
}`

func normalizeSessionToken(raw string) string {
	replacer := strings.NewReplacer("\"", "", "'", "")
	return strings.TrimSpace(replacer.Replace(raw))
}
