package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/stakeapi"
	"github.com/lox/stake-cli/pkg/stake"
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
	stdout        io.Writer
	logger        *log.Logger
	authStorePath string
	baseURL       string
	timeout       time.Duration
}

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

type authCmd struct {
	Add    authAddCmd    `cmd:"" help:"Add or replace a stored session token"`
	List   authListCmd   `cmd:"" help:"List stored auth entries"`
	Remove authRemoveCmd `cmd:"" help:"Remove a stored auth entry"`
}

type authAddCmd struct {
	Name  string `arg:"" name:"name" help:"Local account name"`
	Token string `help:"Stake session token" required:""`
}

func (c *authAddCmd) Run(runtime *runtime) error {
	token := c.Token
	client := stake.NewClient(stake.Config{
		BaseURL:      runtime.baseURL,
		Timeout:      runtime.timeout,
		SessionToken: token,
		OnSessionToken: func(refreshed string) {
			token = refreshed
		},
	}, runtime.logger)

	user, err := client.ValidateSession(runtime.ctx)
	if err != nil {
		return err
	}

	entry := authstore.Entry{
		Name:         c.Name,
		SessionToken: token,
		UserID:       user.UserID,
		Email:        user.Email,
		Username:     user.Username,
		AccountType:  user.AccountType,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := authstore.Update(runtime.authStorePath, func(store *authstore.File) error {
		store.Upsert(entry)
		return nil
	}); err != nil {
		return err
	}

	return writeOutput(runtime.stdout, authAccountResponse{Account: entry.View()})
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

	return writeOutput(runtime.stdout, stakeapi.UserResponse{
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

	return writeOutput(runtime.stdout, stakeapi.TradesResponse{
		Account:   c.Account,
		Count:     len(trades),
		FetchedAt: fetchedAt,
		Trades:    trades,
	})
}
