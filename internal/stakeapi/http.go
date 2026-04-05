package stakeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/pkg/stake"
	"github.com/lox/stake-cli/pkg/types"
)

type apiService interface {
	Accounts() []AccountStatus
	Account(name string) (AccountStatus, error)
	Validate(ctx context.Context, name string) (AccountStatus, error)
	FetchTrades(ctx context.Context, name string) ([]*types.Trade, error)
	Proxy(ctx context.Context, name string, method string, path string, body []byte, headers http.Header) (*stake.HTTPResponse, error)
}

// NewHandler builds the local Stake proxy plus read-only account endpoints.
func NewHandler(service *Service, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.New(io.Discard)
	}

	handler := &httpHandler{
		service: service,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("GET /v1/accounts", handler.accounts)
	mux.HandleFunc("GET /v1/accounts/{account}", handler.account)
	mux.HandleFunc("GET /v1/accounts/{account}/user", handler.user)
	mux.HandleFunc("GET /v1/accounts/{account}/trades", handler.trades)
	mux.HandleFunc("/v1/accounts/{account}/mirror/{path...}", handler.legacyProxy)
	mux.HandleFunc("/", handler.proxy)
	return mux
}

type httpHandler struct {
	service apiService
	logger  *log.Logger
}

// HealthResponse describes daemon health and the last validation pass.
type HealthResponse struct {
	Status          string    `json:"status"`
	AccountCount    int       `json:"account_count"`
	HealthyAccounts int       `json:"healthy_accounts"`
	CheckedAccounts int       `json:"checked_accounts"`
	Timestamp       time.Time `json:"timestamp"`
}

// AccountsResponse lists every account managed by the daemon.
type AccountsResponse struct {
	Accounts []AccountStatus `json:"accounts"`
}

// AccountResponse wraps one account status payload.
type AccountResponse struct {
	Account AccountStatus `json:"account"`
}

// UserResponse contains the live user validation payload for an account.
type UserResponse struct {
	Account     string      `json:"account"`
	ValidatedAt *time.Time  `json:"validated_at,omitempty"`
	User        *stake.User `json:"user,omitempty"`
}

// TradesResponse contains normalized trade data for one account.
type TradesResponse struct {
	Account   string         `json:"account"`
	Count     int            `json:"count"`
	FetchedAt time.Time      `json:"fetched_at"`
	Trades    []*types.Trade `json:"trades"`
}

// ErrorResponse is returned when the API cannot fulfill a request.
type ErrorResponse struct {
	Error string `json:"error"`
}

func (h *httpHandler) health(w http.ResponseWriter, _ *http.Request) {
	accounts := h.service.Accounts()
	healthy := 0
	checked := 0
	for _, account := range accounts {
		if account.LastCheckedAt != nil {
			checked++
		}
		if account.SessionValid {
			healthy++
		}
	}

	status := "starting"
	if checked == len(accounts) {
		status = "ok"
		if healthy != len(accounts) {
			status = "degraded"
		}
	} else if checked > 0 {
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Status:          status,
		AccountCount:    len(accounts),
		HealthyAccounts: healthy,
		CheckedAccounts: checked,
		Timestamp:       time.Now().UTC(),
	})
}

func (h *httpHandler) accounts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, AccountsResponse{Accounts: h.service.Accounts()})
}

func (h *httpHandler) account(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.Account(r.PathValue("account"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AccountResponse{Account: status})
}

func (h *httpHandler) user(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.Validate(r.Context(), r.PathValue("account"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, UserResponse{
		Account:     status.Name,
		ValidatedAt: status.LastCheckedAt,
		User:        status.User,
	})
}

func (h *httpHandler) trades(w http.ResponseWriter, r *http.Request) {
	accountName := r.PathValue("account")
	trades, err := h.service.FetchTrades(r.Context(), accountName)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TradesResponse{
		Account:   accountName,
		Count:     len(trades),
		FetchedAt: time.Now().UTC(),
		Trades:    trades,
	})
}

func (h *httpHandler) proxy(w http.ResponseWriter, r *http.Request) {
	accountName := strings.TrimSpace(r.Header.Get("Stake-Session-Token"))
	if accountName == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Stake-Session-Token header must contain a stored account name"})
		return
	}

	h.proxyRequest(w, r, accountName, r.URL.Path)
}

func (h *httpHandler) legacyProxy(w http.ResponseWriter, r *http.Request) {
	proxyPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	h.proxyRequest(w, r, r.PathValue("account"), proxyPath)
}

func (h *httpHandler) proxyRequest(w http.ResponseWriter, r *http.Request, accountName string, proxyPath string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, fmt.Errorf("read proxy request body: %w", err))
		return
	}

	if r.URL.RawQuery != "" {
		proxyPath += "?" + r.URL.RawQuery
	}

	response, err := h.service.Proxy(r.Context(), accountName, r.Method, proxyPath, body, r.Header)
	if err != nil {
		h.writeError(w, err)
		return
	}

	copyHeaders(w.Header(), response.Header)
	w.Header().Del("Stake-Session-Token")
	w.WriteHeader(response.StatusCode)
	if _, err := w.Write(response.Body); err != nil {
		h.logger.Warn("Writing proxied Stake response failed", "error", err)
	}
}

func (h *httpHandler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, ErrAccountNotFound) {
		status = http.StatusNotFound
	}
	h.logger.Warn("Stake API request failed", "error", err)
	writeJSON(w, status, ErrorResponse{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
