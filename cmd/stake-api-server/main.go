package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/stakeapi"
)

var cli struct {
	AuthStore       string        `help:"Path to the stored Stake auth file" type:"path"`
	Listen          string        `help:"Listen address for the internal API" default:"127.0.0.1:8081"`
	Account         []string      `help:"Limit the server to specific Stake accounts" name:"account"`
	RefreshInterval time.Duration `help:"How often to validate stored Stake sessions in the background" default:"15m"`
	ShutdownTimeout time.Duration `help:"Graceful shutdown timeout" default:"10s"`
}

func main() {
	ctx := kong.Parse(&cli,
		kong.Name("stake-api-server"),
		kong.Description("Local Stake proxy and read-only REST API backed by stored session tokens"),
		kong.UsageOnError(),
	)

	if err := run(); err != nil {
		ctx.FatalIfErrorf(err)
	}
}

func run() error {
	logger := log.New(os.Stderr)

	storePath, err := authstore.ResolvePath(cli.AuthStore)
	if err != nil {
		return err
	}

	store, err := authstore.Load(storePath)
	if err != nil {
		return fmt.Errorf("loading auth store: %w", err)
	}

	accounts, err := stakeapi.LoadAccountsFromEntries(store.Accounts, cli.Account)
	if err != nil {
		return err
	}

	service := stakeapi.NewService(accounts, logger, cli.RefreshInterval, nil, func(accountName string, token string) error {
		return authstore.Update(storePath, func(store *authstore.File) error {
			entry, err := store.Get(accountName)
			if err != nil {
				return err
			}
			entry.SessionToken = token
			entry.UpdatedAt = time.Now().UTC()
			store.Upsert(*entry)
			return nil
		})
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service.Start(ctx)

	server := &http.Server{
		Addr:              cli.Listen,
		Handler:           stakeapi.NewHandler(service, logger),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("Starting stake-api-server", "listen", cli.Listen, "accounts", len(accounts), "refresh_interval", cli.RefreshInterval, "auth_store", storePath)

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cli.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down server: %w", err)
		}
		return nil
	case err := <-errCh:
		if err == nil {
			return nil
		}
		return fmt.Errorf("serving API: %w", err)
	}
}
