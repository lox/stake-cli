package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// TradeType represents buy or sell
type TradeType string

const (
	TradeTypeBuy  TradeType = "BUY"
	TradeTypeSell TradeType = "SELL"
)

// Trade represents a normalized trade from any broker
type Trade struct {
	Symbol   string          `json:"symbol"`
	Date     time.Time       `json:"date"`
	Type     TradeType       `json:"type"`
	Quantity decimal.Decimal `json:"quantity"`
	Price    decimal.Decimal `json:"price"`
	Currency string          `json:"currency"`

	Brokerage decimal.Decimal `json:"brokerage"`
	OtherFees decimal.Decimal `json:"other_fees,omitempty"`

	BrokerID string `json:"broker_id"` // Original ID from broker
	Broker   string `json:"broker"`    // STAKE, IBKR, etc
	Market   string `json:"market"`    // ASX, NYSE, etc

	Account     string `json:"account"`      // Account name (primary, secondary, retirement)
	AccountType string `json:"account_type"` // individual, trust, smsf

	FxRate     decimal.Decimal `json:"fx_rate,omitempty"`
	LocalValue decimal.Decimal `json:"local_value,omitempty"`

	Notes string `json:"notes,omitempty"`
}

// TotalCost returns the total cost including brokerage
func (t *Trade) TotalCost() decimal.Decimal {
	subtotal := t.Quantity.Mul(t.Price)
	return subtotal.Add(t.Brokerage).Add(t.OtherFees)
}
