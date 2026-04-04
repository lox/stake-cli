package stake

// DetermineUSMarket returns the exchange for a US stock symbol.
// ShareSight requires the correct exchange to match instruments.
func DetermineUSMarket(symbol string) string {
	if nasdaqSymbols[symbol] {
		return "NASDAQ"
	}
	return "NYSE"
}

var nasdaqSymbols = map[string]bool{
	// Mega-cap tech
	"AAPL": true, "MSFT": true, "AMZN": true, "NVDA": true, "META": true,
	"TSLA": true, "GOOG": true, "GOOGL": true, "NFLX": true, "ADBE": true,

	// Semiconductors
	"INTC": true, "QCOM": true, "AMD": true, "MU": true,
	"ASML": true, "LRCX": true, "KLAC": true, "SNPS": true, "CDNS": true,

	// Software / SaaS
	"DOCU": true, "OKTA": true, "TEAM": true, "SPLK": true, "WDAY": true,
	"INTU": true, "ISRG": true, "FISV": true, "DBX": true, "PTON": true,
	"MTCH": true, "ZM": true, "ABNB": true, "SHOP": true, "CMPR": true,

	// Internet / Media
	"CSCO": true, "CMCSA": true, "EBAY": true, "BIIB": true,
	"SIRI": true, "VRSN": true, "ROKU": true, "DXCM": true,

	// Consumer / Retail
	"PEP": true, "COST": true, "SBUX": true, "MDLZ": true, "WBA": true,
	"FAST": true, "DLTR": true, "ORLY": true, "ULTA": true, "LULU": true,

	// Financial / Payment
	"PYPL": true, "ADP": true, "PAYX": true, "CTSH": true,

	// Travel / Hospitality
	"BKNG": true, "EXPE": true, "MAR": true, "UAL": true,

	// Biotech / Pharma
	"GILD": true, "ILMN": true, "ATVI": true,

	// Gaming / Entertainment
	"TTWO": true, "NTAP": true,

	// ETFs
	"QQQ": true, "TQQQ": true,

	// Other NASDAQ-listed
	"VRSK": true,
}
