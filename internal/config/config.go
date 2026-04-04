package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the stake-cli configuration.
type Config struct {
	Accounts []Account `yaml:"accounts"`
}

// Account represents one configured account entry.
type Account struct {
	Name    string                   `yaml:"name"`
	Brokers map[string]BrokerAccount `yaml:"brokers"`
}

// BrokerAccount represents the Stake-specific account fields used by the server.
type BrokerAccount struct {
	AccountID    string                 `yaml:"account_id"`
	AccountType  string                 `yaml:"account_type"`
	Username     string                 `yaml:"username,omitempty"`
	ReportsDir   string                 `yaml:"reports_dir,omitempty"`
	SessionToken string                 `yaml:"session_token,omitempty"`
	Config       map[string]interface{} `yaml:",inline"`
}

// Load reads configuration from file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// GetAccount returns the account configuration by name
func (c *Config) GetAccount(name string) (*Account, error) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i], nil
		}
	}
	return nil, fmt.Errorf("account not found: %s", name)
}

// GetBrokerAccount returns broker-specific account details
func (a *Account) GetBrokerAccount(broker string) (*BrokerAccount, error) {
	if ba, ok := a.Brokers[broker]; ok {
		return &ba, nil
	}
	return nil, fmt.Errorf("broker not configured for account %s: %s", a.Name, broker)
}
