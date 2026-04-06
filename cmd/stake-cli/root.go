package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/stakelogin"
	"github.com/lox/stake-cli/pkg/stake"
	"github.com/lox/stake-cli/pkg/types"
)

type cli struct {
	AuthStore string        `help:"Path to the stored Stake auth file" type:"path"`
	BaseURL   string        `help:"Base URL for the Stake API" default:"https://api2.prd.hellostake.com"`
	Timeout   time.Duration `help:"HTTP timeout for requests" default:"30s"`

	Auth   authCmd   `cmd:"" help:"Manage stored Stake auth"`
	User   userCmd   `cmd:"" help:"Validate a stored session and print the live user payload"`
	Trades tradesCmd `cmd:"" help:"Fetch normalized trades for a stored account"`
}

type runtime struct {
	ctx           context.Context
	stdin         io.Reader
	stdout        io.Writer
	logger        *log.Logger
	authStorePath string
	baseURL       string
	timeout       time.Duration
}

var runStakeLogin = stakelogin.Run
var cliInput io.Reader = os.Stdin

func execute(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	cli := cli{}
	parser, err := kong.New(
		&cli,
		kong.Name("stake-cli"),
		kong.Description("CLI client for Stake accounts backed by stored session tokens"),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		return fmt.Errorf("build CLI parser: %w", err)
	}

	parseCtx, err := parser.Parse(args)
	if err != nil {
		return err
	}

	authStorePath, err := authstore.ResolvePath(cli.AuthStore)
	if err != nil {
		return err
	}

	runtime := &runtime{
		ctx:           ctx,
		stdin:         cliInput,
		stdout:        stdout,
		logger:        log.New(stderr),
		authStorePath: authStorePath,
		baseURL:       cli.BaseURL,
		timeout:       cli.Timeout,
	}

	if err := parseCtx.Run(runtime); err != nil {
		return err
	}

	return nil
}

func writeOutput(w io.Writer, value interface{}) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

func (r *runtime) storedAccount(name string) (*authstore.Entry, error) {
	store, err := authstore.Load(r.authStorePath)
	if err != nil {
		return nil, err
	}
	entry, err := store.Get(name)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *runtime) stakeClient(name string, token string) *stake.Client {
	return stake.NewClient(stake.Config{
		BaseURL:      r.baseURL,
		Timeout:      r.timeout,
		SessionToken: token,
		OnSessionToken: func(refreshed string) {
			if err := authstore.Update(r.authStorePath, func(store *authstore.File) error {
				entry, err := store.Get(name)
				if err != nil {
					return err
				}
				entry.SessionToken = refreshed
				entry.UpdatedAt = time.Now().UTC()
				store.Upsert(*entry)
				return nil
			}); err != nil {
				r.logger.Warn("Persisting refreshed Stake session token failed", "account", name, "error", err)
			}
		},
	}, r.logger)
}

type authAccountsResponse struct {
	Accounts []authstore.View `json:"accounts"`
}

type authAccountResponse struct {
	Account authstore.View `json:"account"`
}

type authLoginResponse struct {
	Login   stakelogin.Result `json:"login"`
	Account authstore.View    `json:"account"`
}

type userResponse struct {
	Account     string      `json:"account"`
	ValidatedAt *time.Time  `json:"validated_at,omitempty"`
	User        *stake.User `json:"user,omitempty"`
}

type tradesResponse struct {
	Account   string         `json:"account"`
	Count     int            `json:"count"`
	FetchedAt time.Time      `json:"fetched_at"`
	Trades    []*types.Trade `json:"trades"`
}

type authProbeResponse struct {
	Account       string      `json:"account"`
	StartedAt     time.Time   `json:"started_at"`
	EndedAt       time.Time   `json:"ended_at"`
	Interval      string      `json:"interval"`
	Attempts      int         `json:"attempts"`
	Successes     int         `json:"successes"`
	Rotations     int         `json:"rotations"`
	StoppedReason string      `json:"stopped_reason"`
	LastCheckedAt time.Time   `json:"last_checked_at,omitempty"`
	LastSuccessAt *time.Time  `json:"last_success_at,omitempty"`
	LastError     string      `json:"last_error,omitempty"`
	User          *stake.User `json:"user,omitempty"`
}

type authCmd struct {
	Add    authAddCmd    `cmd:"" help:"Add or replace a stored session token"`
	Login  authLoginCmd  `cmd:"" help:"Browser-first Stake login backed by Rod and Stealth"`
	List   authListCmd   `cmd:"" help:"List stored auth entries"`
	Probe  authProbeCmd  `cmd:"" help:"Repeatedly validate a stored session until it fails or you stop the command"`
	Remove authRemoveCmd `cmd:"" help:"Remove a stored auth entry"`
}

type authAddCmd struct {
	Name  string `arg:"" name:"name" help:"Local account name"`
	Token string `help:"Stake session token" required:""`
}

func (c *authAddCmd) Run(runtime *runtime) error {
	view, err := runtime.validateAndStoreAccount(c.Name, c.Token, nil)
	if err != nil {
		return err
	}

	return writeOutput(runtime.stdout, authAccountResponse{Account: view})
}

type authLoginCmd struct {
	Name           string        `arg:"" name:"name" help:"Local account name to eventually store the captured Stake session under"`
	LoginURL       string        `help:"Stake sign-in URL to open in the browser" default:"https://trading.hellostake.com/auth/login" name:"login-url"`
	BrowserTimeout time.Duration `help:"Maximum time allowed for browser startup and initial navigation" default:"2m" name:"browser-timeout"`
	Headless       bool          `help:"Run headless instead of opening a visible browser" name:"headless"`
	AutoClose      bool          `help:"Close the browser after preparing the login page instead of leaving it open for manual auth" name:"auto-close"`
	OPItem         string        `help:"1Password item reference used to autofill email, password, and MFA (op://vault/item)" name:"op-item"`
	OPAccount      string        `help:"1Password desktop account to use instead of OP_SERVICE_ACCOUNT_TOKEN" name:"op-account"`
}

func (c *authLoginCmd) Run(runtime *runtime) error {
	expectedUserID, err := runtime.expectedLoginUserID(c.Name)
	if err != nil {
		return err
	}
	onePassword, err := c.onePasswordConfig(runtime)
	if err != nil {
		return err
	}

	result, err := runStakeLogin(runtime.ctx, stakelogin.Config{
		AccountName:    c.Name,
		APIBaseURL:     runtime.baseURL,
		ExpectedUserID: expectedUserID,
		OnePassword:    onePassword,
		LoginURL:       c.LoginURL,
		BrowserTimeout: c.BrowserTimeout,
		ShowBrowser:    !c.Headless,
		KeepBrowser:    !c.AutoClose,
		PromptInput:    runtime.stdin,
	}, runtime.logger)
	if err != nil {
		return err
	}
	if result.SessionToken == "" {
		return writeOutput(runtime.stdout, result)
	}

	view, err := runtime.validateAndStoreAccount(c.Name, result.SessionToken, &onePassword)
	if err != nil {
		return fmt.Errorf("validate captured session token: %w", err)
	}

	return writeOutput(runtime.stdout, authLoginResponse{
		Login:   *result,
		Account: view,
	})
}

func (c *authLoginCmd) onePasswordConfig(runtime *runtime) (stakelogin.OnePasswordConfig, error) {
	itemReference := strings.TrimSpace(c.OPItem)
	desktopAccount := strings.TrimSpace(c.OPAccount)
	if itemReference == "" || desktopAccount == "" {
		entry, err := runtime.storedAccount(c.Name)
		if err != nil && !errors.Is(err, authstore.ErrAccountNotFound) {
			return stakelogin.OnePasswordConfig{}, err
		}
		if err == nil {
			if itemReference == "" {
				itemReference = strings.TrimSpace(entry.OPItem)
			}
			if desktopAccount == "" && strings.TrimSpace(c.OPItem) == "" {
				desktopAccount = strings.TrimSpace(entry.OPAccount)
			}
		}
	}
	if itemReference == "" {
		if desktopAccount != "" {
			return stakelogin.OnePasswordConfig{}, fmt.Errorf("--op-account requires --op-item")
		}
		return stakelogin.OnePasswordConfig{}, nil
	}

	config := stakelogin.OnePasswordConfig{
		ItemReference:  itemReference,
		DesktopAccount: desktopAccount,
	}
	if config.DesktopAccount == "" {
		config.ServiceAccountToken = strings.TrimSpace(os.Getenv("OP_SERVICE_ACCOUNT_TOKEN"))
	}

	return config, nil
}

func (r *runtime) expectedLoginUserID(name string) (string, error) {
	entry, err := r.storedAccount(name)
	if err != nil {
		if errors.Is(err, authstore.ErrAccountNotFound) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(entry.UserID), nil
}

func (r *runtime) validateAndStoreAccount(name string, token string, onePassword *stakelogin.OnePasswordConfig) (authstore.View, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return authstore.View{}, fmt.Errorf("stake session token is required")
	}

	client := stake.NewClient(stake.Config{
		BaseURL:      r.baseURL,
		Timeout:      r.timeout,
		SessionToken: token,
	}, r.logger)

	user, err := client.ValidateSession(r.ctx)
	if err != nil {
		return authstore.View{}, err
	}

	entry := authstore.Entry{
		Name:         name,
		SessionToken: client.SessionToken(),
		UserID:       user.UserID,
		Email:        user.Email,
		Username:     user.Username,
		AccountType:  user.AccountType,
		UpdatedAt:    time.Now().UTC(),
	}
	if onePassword != nil {
		entry.OPItem = strings.TrimSpace(onePassword.ItemReference)
		entry.OPAccount = strings.TrimSpace(onePassword.DesktopAccount)
	}
	if err := authstore.Update(r.authStorePath, func(store *authstore.File) error {
		stored, err := store.Get(name)
		if err != nil && !errors.Is(err, authstore.ErrAccountNotFound) {
			return err
		}
		if err == nil {
			if entry.OPItem == "" {
				entry.OPItem = stored.OPItem
			}
			if entry.OPAccount == "" {
				entry.OPAccount = stored.OPAccount
			}
		}
		store.Upsert(entry)
		return nil
	}); err != nil {
		return authstore.View{}, err
	}

	return entry.View(), nil
}

type authListCmd struct{}

func (c *authListCmd) Run(runtime *runtime) error {
	store, err := authstore.Load(runtime.authStorePath)
	if err != nil {
		return err
	}
	return writeOutput(runtime.stdout, authAccountsResponse{Accounts: store.Views()})
}

type authRemoveCmd struct {
	Name string `arg:"" name:"name" help:"Local account name"`
}

func (c *authRemoveCmd) Run(runtime *runtime) error {
	return authstore.Update(runtime.authStorePath, func(store *authstore.File) error {
		if !store.Delete(c.Name) {
			return authstore.ErrAccountNotFound
		}
		return nil
	})
}

type authProbeCmd struct {
	Name        string        `arg:"" name:"name" help:"Stored account name"`
	Interval    time.Duration `help:"Wait between validation attempts" default:"30s"`
	MaxAttempts int           `help:"Stop after this many validation attempts; zero runs until failure or interruption" name:"max-attempts"`
}

func (c *authProbeCmd) Run(runtime *runtime) error {
	if c.Interval <= 0 {
		return fmt.Errorf("probe interval must be greater than zero")
	}
	if c.MaxAttempts < 0 {
		return fmt.Errorf("max attempts must be zero or greater")
	}

	entry, err := runtime.storedAccount(c.Name)
	if err != nil {
		return err
	}

	client := runtime.stakeClient(c.Name, entry.SessionToken)
	previousToken := client.SessionToken()
	report := authProbeResponse{
		Account:   c.Name,
		StartedAt: time.Now().UTC(),
		Interval:  c.Interval.String(),
	}
	if entry.UserID != "" || entry.Email != "" || entry.Username != "" || entry.AccountType != "" {
		report.User = &stake.User{
			UserID:      entry.UserID,
			Email:       entry.Email,
			Username:    entry.Username,
			AccountType: entry.AccountType,
		}
	}

	for {
		if runtime.ctx.Err() != nil {
			report.StoppedReason = "canceled"
			break
		}

		attempt := report.Attempts + 1
		report.Attempts = attempt
		report.LastCheckedAt = time.Now().UTC()

		user, err := client.ValidateSession(runtime.ctx)
		if err != nil {
			report.StoppedReason = "validation_failed"
			report.LastError = err.Error()
			runtime.logger.Warn("Stake session probe failed", "account", c.Name, "attempt", attempt, "error", err)
			break
		}

		validatedAt := time.Now().UTC()
		report.Successes++
		report.LastSuccessAt = &validatedAt
		report.User = user

		currentToken := client.SessionToken()
		rotated := currentToken != previousToken
		if rotated {
			report.Rotations++
			previousToken = currentToken
			runtime.logger.Info("Stake session token rotated during probe", "account", c.Name, "attempt", attempt)
		}

		if err := authstore.Update(runtime.authStorePath, func(store *authstore.File) error {
			updated, err := store.Get(c.Name)
			if err != nil {
				return err
			}
			updated.SessionToken = currentToken
			updated.UserID = user.UserID
			updated.Email = user.Email
			updated.Username = user.Username
			updated.AccountType = user.AccountType
			updated.UpdatedAt = validatedAt
			store.Upsert(*updated)
			return nil
		}); err != nil {
			return err
		}

		runtime.logger.Info("Stake session probe succeeded", "account", c.Name, "attempt", attempt, "rotated", rotated)

		if c.MaxAttempts > 0 && attempt >= c.MaxAttempts {
			report.StoppedReason = "max_attempts"
			break
		}

		timer := time.NewTimer(c.Interval)
		select {
		case <-runtime.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			report.StoppedReason = "canceled"
			goto done
		case <-timer.C:
		}
	}

done:
	report.EndedAt = time.Now().UTC()
	if report.StoppedReason == "" {
		report.StoppedReason = "completed"
	}

	return writeOutput(runtime.stdout, report)
}

type userCmd struct {
	Account string `arg:"" name:"account" help:"Stored account name"`
}

func (c *userCmd) Run(runtime *runtime) error {
	entry, err := runtime.storedAccount(c.Account)
	if err != nil {
		return err
	}

	client := runtime.stakeClient(c.Account, entry.SessionToken)
	user, err := client.ValidateSession(runtime.ctx)
	if err != nil {
		return err
	}
	validatedAt := time.Now().UTC()

	if err := authstore.Update(runtime.authStorePath, func(store *authstore.File) error {
		updated := *entry
		updated.SessionToken = client.SessionToken()
		updated.UserID = user.UserID
		updated.Email = user.Email
		updated.Username = user.Username
		updated.AccountType = user.AccountType
		updated.UpdatedAt = validatedAt
		store.Upsert(updated)
		return nil
	}); err != nil {
		return err
	}

	return writeOutput(runtime.stdout, userResponse{
		Account:     c.Account,
		ValidatedAt: &validatedAt,
		User:        user,
	})
}

type tradesCmd struct {
	Account string `arg:"" name:"account" help:"Stored account name"`
}

func (c *tradesCmd) Run(runtime *runtime) error {
	entry, err := runtime.storedAccount(c.Account)
	if err != nil {
		return err
	}

	client := runtime.stakeClient(c.Account, entry.SessionToken)
	trades, err := client.FetchTrades(runtime.ctx, c.Account)
	if err != nil {
		return err
	}
	fetchedAt := time.Now().UTC()

	if err := authstore.Update(runtime.authStorePath, func(store *authstore.File) error {
		updated := *entry
		updated.SessionToken = client.SessionToken()
		updated.UpdatedAt = fetchedAt
		store.Upsert(updated)
		return nil
	}); err != nil {
		return err
	}

	return writeOutput(runtime.stdout, tradesResponse{
		Account:   c.Account,
		Count:     len(trades),
		FetchedAt: fetchedAt,
		Trades:    trades,
	})
}
