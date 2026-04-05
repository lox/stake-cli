package stake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/lox/stake-cli/pkg/types"
	"github.com/shopspring/decimal"
)

const DefaultBaseURL = "https://api2.prd.hellostake.com"

const defaultStakeWebClientVersion = "7.0.5"

// Client represents a Stake API client
type Client struct {
	httpClient *http.Client
	baseURL    string
	logger     *log.Logger

	mu             sync.RWMutex
	sessionToken   string
	onSessionToken func(string)
}

// Config holds Stake API configuration
type Config struct {
	BaseURL        string
	Timeout        time.Duration
	SessionToken   string
	OnSessionToken func(string)
}

// NewClient creates a new Stake API client
func NewClient(cfg Config, logger *log.Logger) *Client {
	baseURL := cfg.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		baseURL:        strings.TrimRight(baseURL, "/"),
		sessionToken:   cfg.SessionToken,
		onSessionToken: cfg.OnSessionToken,
		logger:         logger,
	}
}

// HTTPResponse is a raw HTTP response returned by the proxy helper.
type HTTPResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// User represents the Stake user profile
type User struct {
	UserID      string `json:"userId"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Email       string `json:"emailAddress"`
	Username    string `json:"username"`
	AccountType string `json:"accountType"`
}

// ValidateSession validates the session token by calling /api/user
func (c *Client) ValidateSession(ctx context.Context) (*User, error) {
	var user User
	if err := c.doGet(ctx, "/api/user", &user); err != nil {
		return nil, fmt.Errorf("validating session: %w", err)
	}
	return &user, nil
}

// SwitchUser switches the active Stake account for the current session.
func (c *Client) SwitchUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user ID is required")
	}

	body, err := json.Marshal(struct {
		UserID string `json:"userId"`
	}{UserID: userID})
	if err != nil {
		return fmt.Errorf("encode switch user payload: %w", err)
	}

	headers := http.Header{}
	headers.Set("X-Server-Select", "AUS")
	headers.Set("X-Stake-Client-Version", defaultStakeWebClientVersion)
	headers.Set("X-Stake-Platform", "WEB")

	if err := c.doRequest(ctx, http.MethodPut, "/api/user/switch", body, headers, nil); err != nil {
		return fmt.Errorf("switching user: %w", err)
	}

	return nil
}

// FetchTrades retrieves all trades from both ASX and NYSE exchanges
func (c *Client) FetchTrades(ctx context.Context, account string) ([]*types.Trade, error) {
	// Validate session first
	user, err := c.ValidateSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("session validation failed (token may be expired): %w", err)
	}
	c.logger.Info("Authenticated as", "user", user.Email, "account_type", user.AccountType)

	var allTrades []*types.Trade

	// Fetch ASX trades
	c.logger.Info("Fetching ASX trades")
	asxTrades, err := c.fetchASXTrades(ctx, account)
	if err != nil {
		c.logger.Warn("Failed to fetch ASX trades", "error", err)
	} else {
		c.logger.Info("Fetched ASX trades", "count", len(asxTrades))
		allTrades = append(allTrades, asxTrades...)
	}

	// Fetch NYSE trades
	c.logger.Info("Fetching US trades")
	nyseTrades, err := c.fetchNYSETrades(ctx, account)
	if err != nil {
		c.logger.Warn("Failed to fetch US trades", "error", err)
	} else {
		c.logger.Info("Fetched US trades", "count", len(nyseTrades))
		allTrades = append(allTrades, nyseTrades...)
	}

	// Sort all trades by date
	sort.Slice(allTrades, func(i, j int) bool {
		return allTrades[i].Date.Before(allTrades[j].Date)
	})

	for _, trade := range allTrades {
		trade.AccountType = user.AccountType
	}

	return allTrades, nil
}

// Proxy forwards an arbitrary authenticated request to the Stake API.
func (c *Client) Proxy(ctx context.Context, method string, path string, body []byte, headers http.Header) (*HTTPResponse, error) {
	resp, respBody, err := c.doRequestRaw(ctx, method, path, body, headers)
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       respBody,
	}, nil
}

// ASX API types

type asxTradeActivityResponse struct {
	Items      []asxTransaction `json:"items"`
	HasNext    bool             `json:"hasNext"`
	Page       int              `json:"page"`
	TotalItems int              `json:"totalItems"`
}

type asxTransaction struct {
	BrokerOrderID       *int     `json:"brokerOrderId"`
	InstrumentCode      string   `json:"instrumentCode"`
	Type                string   `json:"type"`
	Side                string   `json:"side"`
	LimitPrice          *float64 `json:"limitPrice"`
	AveragePrice        *float64 `json:"averagePrice"`
	CompletedTimestamp  *string  `json:"completedTimestamp"`
	PlacedTimestamp     *string  `json:"placedTimestamp"`
	OrderStatus         string   `json:"orderStatus"`
	OrderCompletionType *string  `json:"orderCompletionType"`
	Consideration       *float64 `json:"consideration"`
	EffectivePrice      *float64 `json:"effectivePrice"`
	Units               *float64 `json:"units"`
	UserBrokerageFees   *float64 `json:"userBrokerageFees"`
	ExecutionDate       *string  `json:"executionDate"`
	ContractNoteNumber  *string  `json:"contractNoteNumber"`
}

// fetchASXTrades fetches all ASX trade activity with pagination
func (c *Client) fetchASXTrades(ctx context.Context, account string) ([]*types.Trade, error) {
	var allTrades []*types.Trade
	page := 0
	pageSize := 100

	for {
		url := fmt.Sprintf("/api/asx/orders/tradeActivity?size=%d&page=%d&sort=insertedAt,asc", pageSize, page)

		var resp asxTradeActivityResponse
		if err := c.doGet(ctx, url, &resp); err != nil {
			return nil, fmt.Errorf("fetching ASX trade activity page %d: %w", page, err)
		}

		c.logger.Debug("ASX trade activity page", "page", page, "items", len(resp.Items), "total", resp.TotalItems)

		for _, txn := range resp.Items {
			trade, err := c.convertASXTransaction(txn, account)
			if err != nil {
				c.logger.Debug("Skipping ASX transaction", "error", err, "instrument", txn.InstrumentCode)
				continue
			}
			allTrades = append(allTrades, trade)
		}

		if !resp.HasNext || len(resp.Items) == 0 {
			break
		}
		page++
	}

	return allTrades, nil
}

// convertASXTransaction converts an ASX transaction to a Trade
func (c *Client) convertASXTransaction(txn asxTransaction, account string) (*types.Trade, error) {
	// Only include filled/completed trades
	if txn.OrderStatus != "CLOSED" {
		return nil, fmt.Errorf("order not closed: %s", txn.OrderStatus)
	}
	if txn.OrderCompletionType != nil && *txn.OrderCompletionType != "FILLED" {
		return nil, fmt.Errorf("order not filled: %s", *txn.OrderCompletionType)
	}

	// Parse symbol - ASX uses "CBA.XAU" format, strip the ".XAU" suffix
	symbol := txn.InstrumentCode
	if idx := strings.LastIndex(symbol, "."); idx > 0 {
		symbol = symbol[:idx]
	}

	// Parse trade type - ASX uses "BUY"/"SELL" strings
	var tradeType types.TradeType
	switch strings.ToUpper(txn.Side) {
	case "BUY":
		tradeType = types.TradeTypeBuy
	case "SELL":
		tradeType = types.TradeTypeSell
	default:
		return nil, fmt.Errorf("unknown side: %s", txn.Side)
	}

	// Parse quantity
	if txn.Units == nil || *txn.Units == 0 {
		return nil, fmt.Errorf("no units")
	}
	quantity := decimal.NewFromFloat(*txn.Units)

	// Parse price - prefer effectivePrice, fall back to averagePrice
	var priceFloat float64
	if txn.EffectivePrice != nil {
		priceFloat = *txn.EffectivePrice
	} else if txn.AveragePrice != nil {
		priceFloat = *txn.AveragePrice
	} else {
		return nil, fmt.Errorf("no price available")
	}
	price := decimal.NewFromFloat(priceFloat)

	// Parse date - prefer executionDate, fall back to completedTimestamp
	var tradeDate time.Time
	if txn.ExecutionDate != nil && *txn.ExecutionDate != "" {
		var err error
		tradeDate, err = time.Parse("2006-01-02", *txn.ExecutionDate)
		if err != nil {
			return nil, fmt.Errorf("parsing execution date %q: %w", *txn.ExecutionDate, err)
		}
	} else if txn.CompletedTimestamp != nil && *txn.CompletedTimestamp != "" {
		var err error
		tradeDate, err = parseTimestamp(*txn.CompletedTimestamp)
		if err != nil {
			return nil, fmt.Errorf("parsing completed timestamp: %w", err)
		}
	} else {
		return nil, fmt.Errorf("no date available")
	}

	// Parse brokerage fees
	brokerage := decimal.Zero
	if txn.UserBrokerageFees != nil {
		brokerage = decimal.NewFromFloat(*txn.UserBrokerageFees)
	}

	// Generate broker ID
	brokerID := ""
	if txn.BrokerOrderID != nil {
		brokerID = fmt.Sprintf("STAKE-ASX-%d", *txn.BrokerOrderID)
	} else if txn.ContractNoteNumber != nil {
		brokerID = fmt.Sprintf("STAKE-ASX-CN-%s", *txn.ContractNoteNumber)
	}

	return &types.Trade{
		Symbol:    symbol,
		Date:      tradeDate,
		Type:      tradeType,
		Quantity:  quantity,
		Price:     price,
		Currency:  "AUD",
		Brokerage: brokerage,
		Broker:    "STAKE",
		BrokerID:  brokerID,
		Market:    "ASX",
		Account:   account,
	}, nil
}

// NYSE API types

type nyseTransactionsRequest struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Limit     int     `json:"limit"`
	Offset    *string `json:"offset"`
	Direction string  `json:"direction"`
}

type nyseTransaction struct {
	AccountAmount  float64         `json:"accountAmount"`
	AccountBalance float64         `json:"accountBalance"`
	AccountType    string          `json:"accountType"`
	BrokerageFee   *float64        `json:"brokerageFee"`
	Comment        string          `json:"comment"`
	FinTranID      string          `json:"finTranID"`
	FinTranTypeID  string          `json:"finTranTypeID"`
	FeeSec         float64         `json:"feeSec"`
	FeeTaf         float64         `json:"feeTaf"`
	FeeBase        float64         `json:"feeBase"`
	FeeXtraShares  float64         `json:"feeXtraShares"`
	FeeExchange    float64         `json:"feeExchange"`
	FillQty        float64         `json:"fillQty"`
	FillPx         float64         `json:"fillPx"`
	TranAmount     float64         `json:"tranAmount"`
	TranSource     string          `json:"tranSource"`
	TranWhen       string          `json:"tranWhen"`
	Instrument     *nyseInstrument `json:"instrument"`
	OrderID        *string         `json:"orderID"`
	OrderNo        *string         `json:"orderNo"`
}

type nyseInstrument struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

// fetchNYSETrades fetches all NYSE/US trade transactions
func (c *Client) fetchNYSETrades(ctx context.Context, account string) ([]*types.Trade, error) {
	// Fetch a large window of transactions - go back to earliest possible date
	reqBody := nyseTransactionsRequest{
		From:      "2015-01-01T00:00:00Z",
		To:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Limit:     5000,
		Offset:    nil,
		Direction: "prev",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var transactions []nyseTransaction
	if err := c.doPost(ctx, "/api/users/accounts/accountTransactions", body, &transactions); err != nil {
		return nil, fmt.Errorf("fetching US transactions: %w", err)
	}

	c.logger.Debug("US transactions received", "count", len(transactions))
	return c.convertNYSETransactions(transactions, account), nil
}

func (c *Client) convertNYSETransactions(transactions []nyseTransaction, account string) []*types.Trade {
	var fillTransactions []nyseTransaction
	commissionByOrder := make(map[string]decimal.Decimal)
	lastFillIndexByOrder := make(map[string]int)

	for _, txn := range transactions {
		switch txn.FinTranTypeID {
		case "BUY", "SELL", "SPUR", "SSAL":
			fillIndex := len(fillTransactions)
			fillTransactions = append(fillTransactions, txn)
			if key := nyseOrderKey(txn); key != "" {
				lastFillIndexByOrder[key] = fillIndex
			}
		case "COMM":
			if key := nyseOrderKey(txn); key != "" {
				commissionByOrder[key] = commissionByOrder[key].Add(nyseFeeAmount(txn))
			}
		}
	}

	trades := make([]*types.Trade, 0, len(fillTransactions))
	for fillIndex, txn := range fillTransactions {
		trade, err := c.convertNYSETransaction(txn, account)
		if err != nil {
			c.logger.Debug("Skipping US transaction", "error", err, "type", txn.FinTranTypeID)
			continue
		}

		if key := nyseOrderKey(txn); key != "" && lastFillIndexByOrder[key] == fillIndex {
			trade.Brokerage = trade.Brokerage.Add(commissionByOrder[key])
		}

		trades = append(trades, trade)
	}

	return trades
}

// convertNYSETransaction converts a NYSE transaction to a Trade
func (c *Client) convertNYSETransaction(txn nyseTransaction, account string) (*types.Trade, error) {
	// Must have instrument
	if txn.Instrument == nil {
		return nil, fmt.Errorf("no instrument data")
	}

	symbol := txn.Instrument.Symbol
	if symbol == "" {
		return nil, fmt.Errorf("empty symbol")
	}

	// Stake sometimes records DBX signup/share-transfer bonus stock as a zero-price fill.
	// Ignore that synthetic grant so it doesn't fail validation or create a zero-cost buy.
	if symbol == "DBX" && txn.FillPx == 0 {
		return nil, fmt.Errorf("ignoring zero-price DBX bonus trade")
	}

	// Parse trade type - Stake uses SPUR (purchase) and SSAL (sale)
	var tradeType types.TradeType
	switch txn.FinTranTypeID {
	case "BUY", "SPUR":
		tradeType = types.TradeTypeBuy
	case "SELL", "SSAL":
		tradeType = types.TradeTypeSell
	default:
		return nil, fmt.Errorf("unsupported transaction type: %s", txn.FinTranTypeID)
	}

	// Parse quantity and price from fill fields
	if txn.FillQty == 0 {
		return nil, fmt.Errorf("zero fill quantity")
	}
	quantity := decimal.NewFromFloat(txn.FillQty).Abs()
	price := decimal.NewFromFloat(txn.FillPx).Abs()

	// Parse date
	tradeDate, err := parseTimestamp(txn.TranWhen)
	if err != nil {
		return nil, fmt.Errorf("parsing date %q: %w", txn.TranWhen, err)
	}

	brokerage := nyseFeeAmount(txn)

	// Generate broker ID - use finTranID which is unique per fill
	brokerID := fmt.Sprintf("STAKE-US-%s", txn.FinTranID)

	return &types.Trade{
		Symbol:    symbol,
		Date:      tradeDate,
		Type:      tradeType,
		Quantity:  quantity,
		Price:     price,
		Currency:  "USD",
		Brokerage: brokerage,
		Broker:    "STAKE",
		BrokerID:  brokerID,
		Market:    DetermineUSMarket(symbol),
		Account:   account,
	}, nil
}

func nyseOrderKey(txn nyseTransaction) string {
	if txn.OrderID != nil && *txn.OrderID != "" {
		return "id:" + *txn.OrderID
	}
	if txn.OrderNo != nil && *txn.OrderNo != "" {
		return "no:" + *txn.OrderNo
	}
	return ""
}

func nyseFeeAmount(txn nyseTransaction) decimal.Decimal {
	totalFees := txn.FeeSec + txn.FeeTaf + txn.FeeBase + txn.FeeXtraShares + txn.FeeExchange
	if txn.BrokerageFee != nil {
		totalFees += *txn.BrokerageFee
	}

	if totalFees == 0 && txn.FinTranTypeID == "COMM" && txn.TranAmount != 0 {
		totalFees = math.Abs(txn.TranAmount)
	}

	return decimal.NewFromFloat(totalFees).Abs()
}

// doGet performs an authenticated GET request
func (c *Client) doGet(ctx context.Context, path string, result interface{}) error {
	return c.doRequest(ctx, http.MethodGet, path, nil, nil, result)
}

// doPost performs an authenticated POST request
func (c *Client) doPost(ctx context.Context, path string, body []byte, result interface{}) error {
	return c.doRequest(ctx, http.MethodPost, path, body, nil, result)
}

// doRequest performs an authenticated request and decodes the response
func (c *Client) doRequest(ctx context.Context, method string, path string, body []byte, headers http.Header, result interface{}) error {
	resp, respBody, err := c.doRequestRaw(ctx, method, path, body, headers)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decoding response: %w (body: %s)", err, string(respBody))
		}
	}

	return nil
}

func (c *Client) doRequestRaw(ctx context.Context, method string, path string, body []byte, headers http.Header) (*http.Response, []byte, error) {
	requestURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("creating request: %w", err)
	}

	for key, values := range headers {
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Stake-Session-Token") {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Stake-Session-Token", c.SessionToken())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response: %w", err)
	}

	c.maybeUpdateSessionToken(resp.Header.Get("Stake-Session-Token"))

	return resp, respBody, nil
}

// SessionToken returns the client's current session token.
func (c *Client) SessionToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionToken
}

func (c *Client) maybeUpdateSessionToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	c.mu.Lock()
	if token == c.sessionToken {
		c.mu.Unlock()
		return
	}
	c.sessionToken = token
	callback := c.onSessionToken
	c.mu.Unlock()

	if callback != nil {
		callback(token)
	}
}

// parseTimestamp parses Stake's various timestamp formats
func parseTimestamp(s string) (time.Time, error) {
	// Try RFC3339 variants first (with Z or timezone offset)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	formats := []string{
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse timestamp: %s", s)
}
