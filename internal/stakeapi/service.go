package stakeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/internal/authstore"
	"github.com/lox/stake-cli/internal/config"
	"github.com/lox/stake-cli/pkg/stake"
	"github.com/lox/stake-cli/pkg/types"
)

// ErrAccountNotFound is returned when a requested Stake account is not managed by the server.
var ErrAccountNotFound = errors.New("stake account not found")

// Account describes one Stake account managed by the API server.
type Account struct {
	Name         string
	AccountID    string
	AccountType  string
	Username     string
	SessionToken string
}

// AccountStatus is the read-only status exposed by the API for one managed account.
type AccountStatus struct {
	Name                string      `json:"name"`
	AccountID           string      `json:"account_id,omitempty"`
	AccountType         string      `json:"account_type,omitempty"`
	Username            string      `json:"username,omitempty"`
	SessionValid        bool        `json:"session_valid"`
	LastCheckedAt       *time.Time  `json:"last_checked_at,omitempty"`
	LastValidationError string      `json:"last_validation_error,omitempty"`
	User                *stake.User `json:"user,omitempty"`
}

// Client is the Stake capability the service needs.
type Client interface {
	ValidateSession(ctx context.Context) (*stake.User, error)
	FetchTrades(ctx context.Context, account string) ([]*types.Trade, error)
	Proxy(ctx context.Context, method string, path string, body []byte, headers http.Header) (*stake.HTTPResponse, error)
}

// ClientFactory builds a Stake client for one configured account.
type ClientFactory func(account Account) Client

// TokenRefresher persists a newer session token for an account.
type TokenRefresher func(accountName string, token string) error

// LoadAccounts resolves Stake-enabled accounts from config and environment.
func LoadAccounts(cfg *config.Config, selected []string) ([]Account, []string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config is required")
	}

	selectedSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		selectedSet[name] = struct{}{}
	}

	candidates := make([]config.Account, 0)
	for _, account := range cfg.Accounts {
		if len(selectedSet) > 0 {
			if _, ok := selectedSet[account.Name]; !ok {
				continue
			}
		}
		if _, err := account.GetBrokerAccount("stake"); err == nil {
			candidates = append(candidates, account)
		}
	}

	if len(selectedSet) > 0 {
		for name := range selectedSet {
			found := false
			for _, candidate := range candidates {
				if candidate.Name == name {
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("selected account %q is not configured for Stake", name)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("no Stake accounts found in config")
	}

	allowGlobalToken := len(candidates) == 1
	accounts := make([]Account, 0, len(candidates))
	warnings := make([]string, 0)

	for _, candidate := range candidates {
		brokerCfg, _ := candidate.GetBrokerAccount("stake")
		token := resolveSessionToken(candidate.Name, brokerCfg, allowGlobalToken)
		if token == "" {
			if len(selectedSet) > 0 {
				return nil, nil, fmt.Errorf("no Stake session token found for selected account %q", candidate.Name)
			}
			warnings = append(warnings, fmt.Sprintf("skipping Stake account %q: no per-account session token configured", candidate.Name))
			continue
		}

		accounts = append(accounts, Account{
			Name:         candidate.Name,
			AccountID:    brokerCfg.AccountID,
			AccountType:  brokerCfg.AccountType,
			Username:     brokerCfg.Username,
			SessionToken: token,
		})
	}

	if len(accounts) == 0 {
		return nil, warnings, fmt.Errorf("no Stake accounts have session tokens configured")
	}

	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Name < accounts[j].Name
	})

	return accounts, warnings, nil
}

// LoadAccountsFromEntries resolves Stake accounts from the auth store.
func LoadAccountsFromEntries(entries []authstore.Entry, selected []string) ([]Account, error) {
	selectedSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		selectedSet[name] = struct{}{}
	}

	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		if len(selectedSet) > 0 {
			if _, ok := selectedSet[entry.Name]; !ok {
				continue
			}
		}
		if entry.SessionToken == "" {
			if len(selectedSet) > 0 {
				return nil, fmt.Errorf("stored account %q does not have a session token", entry.Name)
			}
			continue
		}

		accounts = append(accounts, Account{
			Name:         entry.Name,
			AccountID:    entry.Email,
			AccountType:  entry.AccountType,
			Username:     entry.Username,
			SessionToken: entry.SessionToken,
		})
	}

	if len(selectedSet) > 0 {
		for name := range selectedSet {
			found := false
			for _, account := range accounts {
				if account.Name == name {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("selected account %q is not stored", name)
			}
		}
	}

	if len(accounts) == 0 {
		return nil, fmt.Errorf("no stored Stake accounts found")
	}

	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Name < accounts[j].Name
	})

	return accounts, nil
}

func resolveSessionToken(accountName string, brokerCfg *config.BrokerAccount, allowGlobal bool) string {
	if token := os.Getenv(sessionTokenEnvKey(accountName)); token != "" {
		return token
	}

	if brokerCfg != nil && brokerCfg.SessionToken != "" {
		return brokerCfg.SessionToken
	}

	if allowGlobal {
		return os.Getenv("STAKE_SESSION_TOKEN")
	}

	return ""
}

func sessionTokenEnvKey(accountName string) string {
	return "STAKE_SESSION_TOKEN_" + strings.ToUpper(strings.ReplaceAll(accountName, "-", "_"))
}

// Service manages Stake clients and cached session status for multiple accounts.
type Service struct {
	logger          *log.Logger
	refreshInterval time.Duration
	accounts        map[string]*managedAccount
	ordered         []*managedAccount
}

type managedAccount struct {
	config Account
	client Client

	mu     sync.RWMutex
	status AccountStatus
}

// NewService constructs a multi-account Stake service.
func NewService(accounts []Account, logger *log.Logger, refreshInterval time.Duration, factory ClientFactory, tokenRefresher TokenRefresher) *Service {
	if logger == nil {
		logger = log.New(os.Stderr)
	}

	service := &Service{
		logger:          logger,
		refreshInterval: refreshInterval,
		accounts:        make(map[string]*managedAccount, len(accounts)),
		ordered:         make([]*managedAccount, 0, len(accounts)),
	}

	for _, account := range accounts {
		managed := &managedAccount{
			config: account,
			status: AccountStatus{
				Name:        account.Name,
				AccountID:   account.AccountID,
				AccountType: account.AccountType,
				Username:    account.Username,
			},
		}
		if factory != nil {
			managed.client = factory(account)
		} else {
			managed.client = stake.NewClient(stake.Config{
				SessionToken: account.SessionToken,
				OnSessionToken: func(token string) {
					managed.mu.Lock()
					managed.config.SessionToken = token
					managed.mu.Unlock()
					if tokenRefresher != nil {
						if err := tokenRefresher(account.Name, token); err != nil {
							logger.Warn("Persisting refreshed Stake session token failed", "account", account.Name, "error", err)
						}
					}
				},
			}, logger)
		}
		service.accounts[account.Name] = managed
		service.ordered = append(service.ordered, managed)
	}

	sort.Slice(service.ordered, func(i, j int) bool {
		return service.ordered[i].config.Name < service.ordered[j].config.Name
	})

	return service
}

// Start begins the background validation loop.
func (s *Service) Start(ctx context.Context) {
	go func() {
		s.RefreshAll(ctx)
		if s.refreshInterval <= 0 {
			<-ctx.Done()
			return
		}

		ticker := time.NewTicker(s.refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RefreshAll(ctx)
			}
		}
	}()
}

// RefreshAll validates every managed Stake session and updates cached status.
func (s *Service) RefreshAll(ctx context.Context) {
	for _, account := range s.ordered {
		if ctx.Err() != nil {
			return
		}

		validateCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := s.refreshAccount(validateCtx, account)
		cancel()
		if err != nil {
			s.logger.Warn("Stake session validation failed", "account", account.config.Name, "error", err)
		}
	}
}

// Accounts returns the cached status for every managed account.
func (s *Service) Accounts() []AccountStatus {
	statuses := make([]AccountStatus, 0, len(s.ordered))
	for _, account := range s.ordered {
		statuses = append(statuses, account.snapshot())
	}
	return statuses
}

// Account returns the cached status for one managed account.
func (s *Service) Account(name string) (AccountStatus, error) {
	account, err := s.lookup(name)
	if err != nil {
		return AccountStatus{}, err
	}
	return account.snapshot(), nil
}

// Validate forces a live session validation for one account and updates the cached status.
func (s *Service) Validate(ctx context.Context, name string) (AccountStatus, error) {
	account, err := s.lookup(name)
	if err != nil {
		return AccountStatus{}, err
	}
	return s.refreshAccount(ctx, account)
}

// FetchTrades fetches normalized trades for one account using the configured Stake session.
func (s *Service) FetchTrades(ctx context.Context, name string) ([]*types.Trade, error) {
	account, err := s.lookup(name)
	if err != nil {
		return nil, err
	}

	trades, err := account.client.FetchTrades(ctx, account.config.Name)
	if err != nil {
		return nil, fmt.Errorf("fetching trades: %w", err)
	}

	account.mu.RLock()
	accountType := account.config.AccountType
	account.mu.RUnlock()

	for _, trade := range trades {
		trade.Account = account.config.Name
		if trade.AccountType == "" {
			trade.AccountType = accountType
		}
	}

	return trades, nil
}

// Mirror proxies one request through the stored Stake session for an account.
func (s *Service) Mirror(ctx context.Context, name string, method string, path string, body []byte, headers http.Header) (*stake.HTTPResponse, error) {
	account, err := s.lookup(name)
	if err != nil {
		return nil, err
	}

	response, err := account.client.Proxy(ctx, method, path, body, headers)
	if err != nil {
		return nil, fmt.Errorf("proxying request: %w", err)
	}

	return response, nil
}

func (s *Service) lookup(name string) (*managedAccount, error) {
	account, ok := s.accounts[name]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (s *Service) refreshAccount(ctx context.Context, account *managedAccount) (AccountStatus, error) {
	user, err := account.client.ValidateSession(ctx)
	now := time.Now().UTC()

	account.mu.Lock()
	defer account.mu.Unlock()

	account.status.LastCheckedAt = &now
	if err != nil {
		account.status.SessionValid = false
		account.status.LastValidationError = err.Error()
		account.status.User = nil
		return account.status, fmt.Errorf("validating session: %w", err)
	}

	account.status.SessionValid = true
	account.status.LastValidationError = ""
	account.status.User = user
	if user.Email != "" {
		account.status.AccountID = user.Email
		account.config.AccountID = user.Email
	}
	if user.AccountType != "" {
		account.status.AccountType = user.AccountType
		account.config.AccountType = user.AccountType
	}
	if user.Username != "" {
		account.status.Username = user.Username
		account.config.Username = user.Username
	}

	return account.status, nil
}

func (a *managedAccount) snapshot() AccountStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	status := a.status
	if a.status.User != nil {
		userCopy := *a.status.User
		status.User = &userCopy
	}
	return status
}
