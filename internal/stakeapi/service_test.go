package stakeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/shopspring/decimal"

	"github.com/lox/stake-cli/internal/config"
	"github.com/lox/stake-cli/pkg/stake"
	"github.com/lox/stake-cli/pkg/types"
)

func TestLoadAccountsUsesPerAccountEnv(t *testing.T) {
	t.Setenv("STAKE_SESSION_TOKEN_PRIMARY", "token-primary")

	cfg := &config.Config{
		Accounts: []config.Account{
			{
				Name: "primary",
				Brokers: map[string]config.BrokerAccount{
					"stake": {AccountType: "individual"},
				},
			},
		},
	}

	accounts, warnings, err := LoadAccounts(cfg, nil)
	if err != nil {
		t.Fatalf("LoadAccounts returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if got := accounts[0].SessionToken; got != "token-primary" {
		t.Fatalf("expected env token, got %q", got)
	}
}

func TestLoadAccountsSkipsTokenlessAccountsWhenNotSelected(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{
				Name: "primary",
				Brokers: map[string]config.BrokerAccount{
					"stake": {SessionToken: "configured-token", AccountType: "individual"},
				},
			},
			{
				Name: "secondary",
				Brokers: map[string]config.BrokerAccount{
					"stake": {AccountType: "trust"},
				},
			},
		},
	}

	accounts, warnings, err := LoadAccounts(cfg, nil)
	if err != nil {
		t.Fatalf("LoadAccounts returned error: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 resolved account, got %d", len(accounts))
	}
	if accounts[0].Name != "primary" {
		t.Fatalf("expected primary account, got %q", accounts[0].Name)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
}

func TestLoadAccountsRequiresTokenForSelectedAccount(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{
				Name: "primary",
				Brokers: map[string]config.BrokerAccount{
					"stake": {AccountType: "individual"},
				},
			},
		},
	}

	_, _, err := LoadAccounts(cfg, []string{"primary"})
	if err == nil {
		t.Fatal("expected missing token error")
	}
	if got := err.Error(); got != "no Stake session token found for selected account \"primary\"" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestServiceValidateAndFetchTrades(t *testing.T) {
	service := NewService(
		[]Account{{Name: "primary", AccountType: "individual", SessionToken: "token"}},
		log.New(os.Stderr),
		time.Minute,
		func(_ Account) Client {
			return &fakeClient{
				user: &stake.User{Email: "account@example.test", AccountType: "individual"},
				trades: []*types.Trade{{
					Symbol:    "AAPL",
					Type:      types.TradeTypeBuy,
					Quantity:  decimal.NewFromInt(1),
					Price:     decimal.NewFromInt(100),
					Currency:  "USD",
					Date:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
					Brokerage: decimal.NewFromFloat(3),
				}},
			}
		},
		nil,
	)

	status, err := service.Validate(context.Background(), "primary")
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !status.SessionValid {
		t.Fatal("expected session to be valid")
	}
	if status.User == nil || status.User.Email != "account@example.test" {
		t.Fatalf("unexpected user: %+v", status.User)
	}

	trades, err := service.FetchTrades(context.Background(), "primary")
	if err != nil {
		t.Fatalf("FetchTrades returned error: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	if trades[0].Account != "primary" {
		t.Fatalf("expected trade account to be primary, got %q", trades[0].Account)
	}
	if trades[0].AccountType != "individual" {
		t.Fatalf("expected account type to be individual, got %q", trades[0].AccountType)
	}
}

func TestHandlerRoutes(t *testing.T) {
	service := NewService(
		[]Account{{Name: "primary", AccountType: "individual", SessionToken: "token"}},
		log.New(os.Stderr),
		time.Minute,
		func(_ Account) Client {
			return &fakeClient{
				user: &stake.User{Email: "account@example.test", AccountType: "individual"},
				trades: []*types.Trade{{
					Symbol:    "AAPL",
					Type:      types.TradeTypeBuy,
					Quantity:  decimal.NewFromInt(1),
					Price:     decimal.NewFromInt(100),
					Currency:  "USD",
					Date:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
					Brokerage: decimal.NewFromFloat(3),
				}},
			}
		},
		nil,
	)

	if _, err := service.Validate(context.Background(), "primary"); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	handler := NewHandler(service, log.New(os.Stderr))

	t.Run("health", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recorder.Code)
		}
		var response HealthResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if response.Status != "ok" {
			t.Fatalf("expected status ok, got %q", response.Status)
		}
	})

	t.Run("accounts", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/accounts", nil)

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recorder.Code)
		}
		var response AccountsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if len(response.Accounts) != 1 {
			t.Fatalf("expected 1 account, got %d", len(response.Accounts))
		}
	})

	t.Run("user", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/accounts/primary/user", nil)

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recorder.Code)
		}
		var response UserResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if response.Account != "primary" {
			t.Fatalf("expected primary account, got %q", response.Account)
		}
	})

	t.Run("trades", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/accounts/primary/trades", nil)

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recorder.Code)
		}
		var response TradesResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if response.Count != 1 {
			t.Fatalf("expected 1 trade, got %d", response.Count)
		}
	})

	t.Run("missing-account", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/accounts/missing", nil)

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", recorder.Code)
		}
	})
}

type fakeClient struct {
	user          *stake.User
	trades        []*types.Trade
	err           error
	proxyResponse *stake.HTTPResponse
	proxyMethod   string
	proxyPath     string
	proxyBody     []byte
}

func (f *fakeClient) ValidateSession(context.Context) (*stake.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

func (f *fakeClient) FetchTrades(context.Context, string) ([]*types.Trade, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.trades, nil
}

func (f *fakeClient) Proxy(_ context.Context, method string, path string, body []byte, _ http.Header) (*stake.HTTPResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.proxyMethod = method
	f.proxyPath = path
	f.proxyBody = append([]byte(nil), body...)
	if f.proxyResponse == nil {
		return &stake.HTTPResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte("ok")}, nil
	}
	return f.proxyResponse, nil
}

func TestHandlerReturnsBadGatewayOnUpstreamError(t *testing.T) {
	service := NewService(
		[]Account{{Name: "primary", AccountType: "individual", SessionToken: "token"}},
		log.New(os.Stderr),
		time.Minute,
		func(_ Account) Client {
			return &fakeClient{err: errors.New("upstream failed")}
		},
		nil,
	)

	handler := NewHandler(service, log.New(os.Stderr))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/accounts/primary/user", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", recorder.Code)
	}
}

func TestHandlerMirrorRoute(t *testing.T) {
	client := &fakeClient{
		proxyResponse: &stake.HTTPResponse{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "Stake-Session-Token": []string{"rotated-token"}},
			Body:       []byte(`{"ok":true}`),
		},
	}
	service := NewService(
		[]Account{{Name: "primary", SessionToken: "token"}},
		log.New(os.Stderr),
		time.Minute,
		func(_ Account) Client { return client },
		nil,
	)

	handler := NewHandler(service, log.New(os.Stderr))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/accounts/primary/mirror/api/user?include=positions", strings.NewReader(`{"a":1}`))
	request.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", recorder.Code)
	}
	if client.proxyMethod != http.MethodPost {
		t.Fatalf("expected proxy method POST, got %s", client.proxyMethod)
	}
	if client.proxyPath != "/api/user?include=positions" {
		t.Fatalf("expected proxy path /api/user?include=positions, got %s", client.proxyPath)
	}
	if string(client.proxyBody) != `{"a":1}` {
		t.Fatalf("unexpected proxy body: %s", string(client.proxyBody))
	}
	if got := recorder.Header().Get("Stake-Session-Token"); got != "" {
		t.Fatalf("expected mirrored token header to be stripped, got %q", got)
	}
}
