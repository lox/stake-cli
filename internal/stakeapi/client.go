package stakeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// HTTPClient is a thin client for the local stake-api-server.
type HTTPClient struct {
	baseURL string
	client  *http.Client
}

// NewHTTPClient constructs a client for the read-only stake-api-server API.
func NewHTTPClient(baseURL string, client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &HTTPClient{
		baseURL: baseURL,
		client:  client,
	}
}

// Health fetches daemon health and session validation status.
func (c *HTTPClient) Health(ctx context.Context) (*HealthResponse, error) {
	var response HealthResponse
	if err := c.get(ctx, "/healthz", &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Accounts fetches the configured account list and cached status.
func (c *HTTPClient) Accounts(ctx context.Context) (*AccountsResponse, error) {
	var response AccountsResponse
	if err := c.get(ctx, "/v1/accounts", &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Account fetches one cached account status.
func (c *HTTPClient) Account(ctx context.Context, account string) (*AccountResponse, error) {
	var response AccountResponse
	if err := c.get(ctx, accountPath(account), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// User validates an account session and returns the live user payload.
func (c *HTTPClient) User(ctx context.Context, account string) (*UserResponse, error) {
	var response UserResponse
	if err := c.get(ctx, accountPath(account)+"/user", &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Trades fetches normalized trades for one account.
func (c *HTTPClient) Trades(ctx context.Context, account string) (*TradesResponse, error) {
	var response TradesResponse
	if err := c.get(ctx, accountPath(account)+"/trades", &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, target interface{}) error {
	requestURL, err := joinURL(c.baseURL, path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("build GET %s request: %w", path, err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GET %s: %w", path, responseError(resp))
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}

	return nil
}

func joinURL(baseURL string, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL %q: %w", baseURL, err)
	}

	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse API path %q: %w", path, err)
	}

	return base.ResolveReference(ref).String(), nil
}

func responseError(resp *http.Response) error {
	var apiError ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiError); err == nil && apiError.Error != "" {
		if resp.StatusCode == http.StatusNotFound {
			return ErrAccountNotFound
		}
		return errors.New(apiError.Error)
	}

	if resp.StatusCode == http.StatusNotFound {
		return ErrAccountNotFound
	}

	return fmt.Errorf("unexpected status %s", resp.Status)
}

func accountPath(account string) string {
	return "/v1/accounts/" + url.PathEscape(account)
}
