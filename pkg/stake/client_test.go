package stake

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/log"
)

func TestValidateSessionRefreshesSessionToken(t *testing.T) {
	seenTokens := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTokens = append(seenTokens, r.Header.Get("Stake-Session-Token"))
		if len(seenTokens) == 1 {
			w.Header().Set("Stake-Session-Token", "rotated-token")
		}
		if err := json.NewEncoder(w).Encode(User{Email: "account@example.test", Username: "sample-user", AccountType: "individual"}); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	refreshedToken := ""
	client := NewClient(Config{
		BaseURL:      server.URL,
		SessionToken: "initial-token",
		OnSessionToken: func(token string) {
			refreshedToken = token
		},
	}, log.New(io.Discard))

	if _, err := client.ValidateSession(context.Background()); err != nil {
		t.Fatalf("ValidateSession returned error: %v", err)
	}
	if _, err := client.ValidateSession(context.Background()); err != nil {
		t.Fatalf("second ValidateSession returned error: %v", err)
	}

	if len(seenTokens) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(seenTokens))
	}
	if seenTokens[0] != "initial-token" {
		t.Fatalf("expected first request to use initial token, got %q", seenTokens[0])
	}
	if seenTokens[1] != "rotated-token" {
		t.Fatalf("expected second request to use rotated token, got %q", seenTokens[1])
	}
	if refreshedToken != "rotated-token" {
		t.Fatalf("expected callback token rotated-token, got %q", refreshedToken)
	}
}

func TestConvertNYSETransactionsAssignsCommissionToFinalFill(t *testing.T) {
	client := NewClient(Config{}, log.New(io.Discard))

	orderID := "KC.85d7e69a-189a-479d-89bb-f8d822b5fbd1"
	orderNo := "KCFE030624"
	buyOrderID := "LB.30a64d22-d21e-4797-b944-28dc810b5517"
	buyOrderNo := "LBAJ146073"

	trades := client.convertNYSETransactions([]nyseTransaction{
		{
			FinTranID:     "KC.97b8729b-aec0-4362-8ae0-8b64e22cd5b1",
			FinTranTypeID: "SSAL",
			FillQty:       93,
			FillPx:        124.84,
			TranWhen:      "2023-03-09T14:30:54.771Z",
			Instrument:    &nyseInstrument{Symbol: "ABNB"},
			OrderID:       &orderID,
			OrderNo:       &orderNo,
		},
		{
			FinTranID:     "KC.24f60de6-0e56-4e3c-9ff0-f97190e569ae",
			FinTranTypeID: "SSAL",
			FillQty:       0.41575341,
			FillPx:        124.84,
			TranWhen:      "2023-03-09T14:30:54.777Z",
			Instrument:    &nyseInstrument{Symbol: "ABNB"},
			OrderID:       &orderID,
			OrderNo:       &orderNo,
		},
		{
			FinTranID:     "KC.8f406ca6-31da-4d92-9167-041e8513438b",
			FinTranTypeID: "COMM",
			FeeBase:       3,
			FeeSec:        0.09,
			FeeTaf:        0.01,
			TranAmount:    -3.1,
			TranWhen:      "2023-03-09T14:31:03.191Z",
			Instrument:    &nyseInstrument{Symbol: "ABNB"},
			OrderID:       &orderID,
			OrderNo:       &orderNo,
		},
		{
			FinTranID:     "LB.buy-fill",
			FinTranTypeID: "SPUR",
			FillQty:       10,
			FillPx:        75,
			TranWhen:      "2024-02-28T20:52:01.000Z",
			Instrument:    &nyseInstrument{Symbol: "XYZ"},
			OrderID:       &buyOrderID,
			OrderNo:       &buyOrderNo,
		},
		{
			FinTranID:     "LB.buy-comm",
			FinTranTypeID: "COMM",
			FeeBase:       3,
			TranAmount:    -3,
			TranWhen:      "2024-02-28T20:52:04.059Z",
			Instrument:    &nyseInstrument{Symbol: "XYZ"},
			OrderID:       &buyOrderID,
			OrderNo:       &buyOrderNo,
		},
	}, "personal")

	if len(trades) != 3 {
		t.Fatalf("expected 3 trades, got %d", len(trades))
	}

	if got := trades[0].Brokerage.String(); got != "0" {
		t.Fatalf("expected first partial fill brokerage 0, got %s", got)
	}

	if got := trades[1].Brokerage.String(); got != "3.1" {
		t.Fatalf("expected final fill brokerage 3.1, got %s", got)
	}

	if got := trades[2].Brokerage.String(); got != "3" {
		t.Fatalf("expected single-fill buy brokerage 3, got %s", got)
	}

	if got := trades[2].Market; got != "NYSE" {
		t.Fatalf("expected XYZ trade market NYSE, got %s", got)
	}
}

func TestConvertNYSETransactionsSkipsZeroPriceDBXBonusTrade(t *testing.T) {
	client := NewClient(Config{}, log.New(io.Discard))

	bonusOrderID := "LD.2bdb8401-9f64-4219-889a-37d128051227"
	bonusOrderNo := "LDGM153878"
	sellOrderID := "LE.fc2bf2c2-2ae3-428f-924c-c6c820b924bc"
	sellOrderNo := "LESW055615"

	trades := client.convertNYSETransactions([]nyseTransaction{
		{
			FinTranID:     "LD.6fb00137-8b85-4959-b211-a5c80ca9f5d9",
			FinTranTypeID: "SPUR",
			FillQty:       1,
			FillPx:        0,
			TranWhen:      "2024-04-26T12:25:38.104Z",
			Instrument:    &nyseInstrument{Symbol: "DBX"},
			OrderID:       &bonusOrderID,
			OrderNo:       &bonusOrderNo,
		},
		{
			FinTranID:     "LE.740eb71c-488e-4275-9169-4a9d1c4d74df",
			FinTranTypeID: "SSAL",
			FillQty:       1,
			FillPx:        23.87,
			TranWhen:      "2024-05-10T13:30:04.210Z",
			Instrument:    &nyseInstrument{Symbol: "DBX"},
			OrderID:       &sellOrderID,
			OrderNo:       &sellOrderNo,
		},
		{
			FinTranID:     "LE.e58a54c6-5c94-4db6-b0b2-0191ca58953a",
			FinTranTypeID: "COMM",
			FeeBase:       3,
			FeeSec:        0.01,
			FeeTaf:        0.01,
			TranAmount:    -3.02,
			TranWhen:      "2024-05-10T13:30:29.869Z",
			Instrument:    &nyseInstrument{Symbol: "DBX"},
			OrderID:       &sellOrderID,
			OrderNo:       &sellOrderNo,
		},
	}, "personal")

	if len(trades) != 1 {
		t.Fatalf("expected only the DBX sell trade to remain, got %d trades", len(trades))
	}

	if got := trades[0].Type; got != "SELL" {
		t.Fatalf("expected remaining DBX trade to be SELL, got %s", got)
	}

	if got := trades[0].Price.String(); got != "23.87" {
		t.Fatalf("expected DBX sell price 23.87, got %s", got)
	}

	if got := trades[0].Brokerage.String(); got != "3.02" {
		t.Fatalf("expected DBX sell brokerage 3.02, got %s", got)
	}
}
