package sessionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrAccountNotFound is returned when the session store does not contain an account.
var ErrAccountNotFound = errors.New("stored account not found")

// Entry is one saved Stake account record.
type Entry struct {
	Name         string    `json:"name"`
	SessionToken string    `json:"session_token"`
	UserID       string    `json:"user_id,omitempty"`
	OPItem       string    `json:"op_item,omitempty"`
	OPAccount    string    `json:"op_account,omitempty"`
	Email        string    `json:"email,omitempty"`
	Username     string    `json:"username,omitempty"`
	AccountType  string    `json:"account_type,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// View is the safe account shape returned to CLI users.
type View struct {
	Name        string    `json:"name"`
	UserID      string    `json:"user_id,omitempty"`
	Email       string    `json:"email,omitempty"`
	Username    string    `json:"username,omitempty"`
	AccountType string    `json:"account_type,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// TokenView exposes one stored account including the active session token.
type TokenView struct {
	Name         string    `json:"name"`
	SessionToken string    `json:"session_token"`
	UserID       string    `json:"user_id,omitempty"`
	Email        string    `json:"email,omitempty"`
	Username     string    `json:"username,omitempty"`
	AccountType  string    `json:"account_type,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// File is the persisted session store document.
type File struct {
	Accounts []Entry `json:"accounts"`
}

// ResolvePath returns the configured auth-store path or the default XDG location.
func ResolvePath(path string) (string, error) {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return trimmed, nil
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}

	return filepath.Join(configDir, "stake-cli", "accounts.json"), nil
}

// Load reads the session store from disk. Missing stores return an empty document.
func Load(path string) (*File, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &File{}, nil
		}
		return nil, fmt.Errorf("read session store: %w", err)
	}

	var store File
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse session store: %w", err)
	}
	store.sortAccounts()

	return &store, nil
}

// Save writes the session store to disk.
func Save(path string, store *File) error {
	if store == nil {
		return fmt.Errorf("session store is required")
	}

	resolved, err := ResolvePath(path)
	if err != nil {
		return err
	}

	store.sortAccounts()
	for i := range store.Accounts {
		store.Accounts[i].normalize()
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session store: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(resolved)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp session store: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp session store: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp session store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp session store: %w", err)
	}
	if err := os.Rename(tmpName, resolved); err != nil {
		cleanup()
		return fmt.Errorf("replace session store: %w", err)
	}

	return nil
}

// Update loads, mutates, and saves the session store atomically.
func Update(path string, update func(store *File) error) error {
	store, err := Load(path)
	if err != nil {
		return err
	}
	if err := update(store); err != nil {
		return err
	}
	return Save(path, store)
}

// Get returns one stored account by name.
func (f *File) Get(name string) (*Entry, error) {
	for i := range f.Accounts {
		if f.Accounts[i].Name == name {
			entry := f.Accounts[i]
			return &entry, nil
		}
	}
	return nil, ErrAccountNotFound
}

// Upsert inserts or replaces one stored account.
func (f *File) Upsert(entry Entry) {
	entry.normalize()
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}

	for i := range f.Accounts {
		if f.Accounts[i].Name == entry.Name {
			f.Accounts[i] = entry
			f.sortAccounts()
			return
		}
	}

	f.Accounts = append(f.Accounts, entry)
	f.sortAccounts()
}

// Delete removes one stored account. It returns true when an account was removed.
func (f *File) Delete(name string) bool {
	for i := range f.Accounts {
		if f.Accounts[i].Name == name {
			f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// Views returns a redacted view of all stored accounts.
func (f *File) Views() []View {
	views := make([]View, 0, len(f.Accounts))
	for _, entry := range f.Accounts {
		views = append(views, entry.View())
	}
	return views
}

// View returns a redacted view of one stored account.
func (e Entry) View() View {
	return View{
		Name:        e.Name,
		UserID:      e.UserID,
		Email:       e.Email,
		Username:    e.Username,
		AccountType: e.AccountType,
		UpdatedAt:   e.UpdatedAt,
	}
}

// TokenView returns one stored account including its active session token.
func (e Entry) TokenView() TokenView {
	return TokenView{
		Name:         e.Name,
		SessionToken: e.SessionToken,
		UserID:       e.UserID,
		Email:        e.Email,
		Username:     e.Username,
		AccountType:  e.AccountType,
		UpdatedAt:    e.UpdatedAt,
	}
}

func (f *File) sortAccounts() {
	sort.Slice(f.Accounts, func(i, j int) bool {
		return f.Accounts[i].Name < f.Accounts[j].Name
	})
}

func (e *Entry) normalize() {
	e.Name = strings.TrimSpace(e.Name)
	e.SessionToken = strings.TrimSpace(e.SessionToken)
	e.UserID = strings.TrimSpace(e.UserID)
	e.OPItem = strings.TrimSpace(e.OPItem)
	e.OPAccount = strings.TrimSpace(e.OPAccount)
	e.Email = strings.TrimSpace(e.Email)
	e.Username = strings.TrimSpace(e.Username)
	e.AccountType = strings.TrimSpace(e.AccountType)
	if !e.UpdatedAt.IsZero() {
		e.UpdatedAt = e.UpdatedAt.UTC()
	}
}
