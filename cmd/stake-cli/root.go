package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/log"

	"github.com/lox/stake-cli/internal/stakelogin"
	"github.com/lox/stake-cli/pkg/sessionstore"
	"github.com/lox/stake-cli/pkg/stake"
	"github.com/lox/stake-cli/pkg/types"
)

type cli struct {
	AuthStore string           `help:"Path to the stored Stake auth file" type:"path"`
	BaseURL   string           `help:"Base URL for the Stake API" default:"https://api2.prd.hellostake.com"`
	Timeout   time.Duration    `help:"HTTP timeout for requests" default:"30s"`
	Version   kong.VersionFlag `name:"version" help:"Print version information and quit"`

	Auth   authCmd   `cmd:"" help:"Manage stored Stake auth"`
	Status statusCmd `cmd:"" help:"Validate stored sessions and print live status for every account"`
	Users  usersCmd  `cmd:"" help:"List switchable Stake users for a stored account"`
	Trades tradesCmd `cmd:"" help:"Fetch normalized trades for a stored account"`
}

type runtime struct {
	ctx           context.Context
	stdin         io.Reader
	stdout        io.Writer
	logger        *log.Logger
	authStorePath string
	baseURL       string
	timeout       time.Duration
}

var runStakeLogin = stakelogin.Run
var cliInput io.Reader = os.Stdin
var cliExit = os.Exit

func execute(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	cli := cli{}
	parser, err := kong.New(
		&cli,
		kong.Name("stake-cli"),
		kong.Description("CLI client for Stake accounts backed by stored session tokens"),
		kong.Exit(cliExit),
		kong.UsageOnError(),
		kong.Vars{"version": version},
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		return fmt.Errorf("build CLI parser: %w", err)
	}

	parseCtx, err := parser.Parse(args)
	if err != nil {
		return err
	}

	runtime := &runtime{
		ctx:           ctx,
		stdin:         cliInput,
		stdout:        stdout,
		logger:        log.New(stderr),
		authStorePath: cli.AuthStore,
		baseURL:       cli.BaseURL,
		timeout:       cli.Timeout,
	}

	if err := parseCtx.Run(runtime); err != nil {
		return err
	}

	return nil
}

func writeOutput(w io.Writer, value interface{}) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

func (r *runtime) storedAccount(name string) (*sessionstore.Entry, error) {
	store, err := sessionstore.Load(r.authStorePath)
	if err != nil {
		return nil, err
	}
	entry, err := store.Get(name)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *runtime) stakeClient(name string, token string) *stake.Client {
	return stake.NewClient(stake.Config{
		BaseURL:      r.baseURL,
		Timeout:      r.timeout,
		SessionToken: token,
		OnSessionToken: func(refreshed string) {
			if err := sessionstore.Update(r.authStorePath, func(store *sessionstore.File) error {
				entry, err := store.Get(name)
				if err != nil {
					return err
				}
				entry.SessionToken = refreshed
				entry.UpdatedAt = time.Now().UTC()
				store.Upsert(*entry)
				return nil
			}); err != nil {
				r.logger.Warn("Persisting refreshed Stake session token failed", "account", name, "error", err)
			}
		},
	}, r.logger)
}

type authAccountsResponse struct {
	Accounts []sessionstore.View `json:"accounts"`
}

type authAccountResponse struct {
	Account sessionstore.View `json:"account"`
}

type authLoginResponse struct {
	Login    stakelogin.Result   `json:"login"`
	Account  sessionstore.View   `json:"account"`
	Accounts []sessionstore.View `json:"accounts,omitempty"`
}

type statusResponse struct {
	Accounts []accountStatusResponse `json:"accounts"`
}

type accountStatusResponse struct {
	Account     string      `json:"account"`
	OK          bool        `json:"ok"`
	ValidatedAt *time.Time  `json:"validated_at,omitempty"`
	Error       string      `json:"error,omitempty"`
	User        *stake.User `json:"user,omitempty"`
}

type usersResponse struct {
	Account       string               `json:"account"`
	ActiveUser    string               `json:"active_user,omitempty"`
	ActiveProduct string               `json:"active_product,omitempty"`
	MasterUserID  string               `json:"master_user_id,omitempty"`
	Users         []listedUserResponse `json:"users"`
}

type listedUserResponse struct {
	Alias            string                      `json:"alias,omitempty"`
	UserID           string                      `json:"user_id"`
	FirstName        string                      `json:"first_name,omitempty"`
	MiddleName       string                      `json:"middle_name,omitempty"`
	LastName         string                      `json:"last_name,omitempty"`
	AccountType      string                      `json:"account_type,omitempty"`
	AccountStatus    string                      `json:"account_status,omitempty"`
	StakeKycStatus   string                      `json:"stake_kyc_status,omitempty"`
	RegionIdentifier string                      `json:"region_identifier,omitempty"`
	EmailVerified    bool                        `json:"email_verified"`
	CreatedDate      int64                       `json:"created_date,omitempty"`
	WithdrawalAccess bool                        `json:"withdrawal_access"`
	Staff            bool                        `json:"staff"`
	Active           bool                        `json:"active"`
	Master           bool                        `json:"master"`
	Products         []listedUserProductResponse `json:"products,omitempty"`
}

type listedUserProductResponse struct {
	Type                        string `json:"type"`
	Status                      string `json:"status,omitempty"`
	ProductPartnerAccountNumber string `json:"product_partner_account_number,omitempty"`
	HINNumber                   string `json:"hin_number,omitempty"`
	OpenedDate                  int64  `json:"opened_date,omitempty"`
	LendingEnabled              bool   `json:"lending_enabled"`
	ExtendedHoursEnabled        bool   `json:"extended_hours_enabled"`
}

type tradesResponse struct {
	Account   string         `json:"account"`
	Count     int            `json:"count"`
	FetchedAt time.Time      `json:"fetched_at"`
	Trades    []*types.Trade `json:"trades"`
}

type authProbeResponse struct {
	Account       string      `json:"account"`
	StartedAt     time.Time   `json:"started_at"`
	EndedAt       time.Time   `json:"ended_at"`
	Interval      string      `json:"interval"`
	Attempts      int         `json:"attempts"`
	Successes     int         `json:"successes"`
	Rotations     int         `json:"rotations"`
	StoppedReason string      `json:"stopped_reason"`
	LastCheckedAt time.Time   `json:"last_checked_at,omitempty"`
	LastSuccessAt *time.Time  `json:"last_success_at,omitempty"`
	LastError     string      `json:"last_error,omitempty"`
	User          *stake.User `json:"user,omitempty"`
}

type authCmd struct {
	Add    authAddCmd    `cmd:"" help:"Add or replace a stored session token"`
	Login  authLoginCmd  `cmd:"" help:"Browser-first Stake login backed by Rod and Stealth"`
	List   authListCmd   `cmd:"" help:"List stored auth entries"`
	Probe  authProbeCmd  `cmd:"" help:"Repeatedly validate a stored session until it fails or you stop the command"`
	Remove authRemoveCmd `cmd:"" help:"Remove a stored auth entry"`
	Status authStatusCmd `cmd:"" hidden:"" help:"Validate stored sessions and print live status for every account"`
	Token  authTokenCmd  `cmd:"" help:"Print a stored session token"`
}

type authAddCmd struct {
	Name  string `arg:"" name:"name" help:"Local account name"`
	Token string `help:"Stake session token" required:""`
}

func (c *authAddCmd) Run(runtime *runtime) error {
	view, err := runtime.validateAndStoreAccount(c.Name, c.Token, nil)
	if err != nil {
		return err
	}

	return writeOutput(runtime.stdout, authAccountResponse{Account: view})
}

type authLoginCmd struct {
	Name           string        `arg:"" optional:"" name:"name" help:"Stored alias to target for the initial login"`
	Alias          string        `help:"Stored alias to target for the initial login" name:"alias" xor:"login-target"`
	UserID         string        `help:"Stake user_id to target for the initial login" name:"user-id" xor:"login-target"`
	LoginURL       string        `help:"Stake sign-in URL to open in the browser" default:"https://trading.hellostake.com/auth/login" name:"login-url"`
	BrowserTimeout time.Duration `help:"Maximum time allowed for browser startup and initial navigation" default:"2m" name:"browser-timeout"`
	Headless       bool          `help:"Run headless instead of opening a visible browser" name:"headless"`
	AutoClose      bool          `help:"Close the browser after preparing the login page instead of leaving it open for manual auth" name:"auto-close"`
	OPItem         string        `help:"1Password item reference used to autofill email, password, and MFA (op://vault/item)" name:"op-item"`
	OPAccount      string        `help:"1Password desktop account to use instead of OP_SERVICE_ACCOUNT_TOKEN" name:"op-account"`
}

type authLoginSelection struct {
	alias          string
	accountName    string
	expectedUserID string
	explicit       bool
}

func (c *authLoginCmd) Run(runtime *runtime) error {
	selection, onePassword, err := c.selection(runtime)
	if err != nil {
		return err
	}
	if selection.explicit {
		runtime.logger.Info("Starting Stake login", "account", selection.accountName, "expected_user_id", selection.expectedUserID)
	} else {
		runtime.logger.Info("Starting Stake discovery login", "account", selection.accountName)
	}

	result, err := runStakeLogin(runtime.ctx, c.loginConfig(runtime, selection.accountName, selection.expectedUserID, onePassword), runtime.logger)
	if err != nil {
		return err
	}
	if result.SessionToken == "" {
		return writeOutput(runtime.stdout, result)
	}

	views, view, err := c.syncLoginAccounts(runtime, selection, result, onePassword)
	if err != nil {
		return fmt.Errorf("validate captured session token: %w", err)
	}
	if !selection.explicit && view.Name != "" {
		result.Account = view.Name
	}

	return writeOutput(runtime.stdout, authLoginResponse{
		Login:    *result,
		Account:  view,
		Accounts: views,
	})
}

func (c *authLoginCmd) selection(runtime *runtime) (authLoginSelection, stakelogin.OnePasswordConfig, error) {
	positionalAlias := strings.TrimSpace(c.Name)
	flagAlias := strings.TrimSpace(c.Alias)
	userID := strings.TrimSpace(c.UserID)
	if positionalAlias != "" && flagAlias != "" {
		return authLoginSelection{}, stakelogin.OnePasswordConfig{}, fmt.Errorf("<name> cannot be combined with --alias")
	}
	if positionalAlias != "" && userID != "" {
		return authLoginSelection{}, stakelogin.OnePasswordConfig{}, fmt.Errorf("<name> cannot be combined with --user-id")
	}

	alias := flagAlias
	if alias == "" {
		alias = positionalAlias
	}

	onePassword, err := c.onePasswordConfig(runtime, alias)
	if err != nil {
		return authLoginSelection{}, stakelogin.OnePasswordConfig{}, err
	}

	expectedUserID := userID
	if alias != "" && expectedUserID == "" {
		expectedUserID, err = runtime.expectedLoginUserID(alias)
		if err != nil {
			return authLoginSelection{}, stakelogin.OnePasswordConfig{}, err
		}
	}

	accountName := alias
	if accountName == "" {
		if userID != "" {
			accountName = "user-" + shortGeneratedUserIDSuffix(userID)
		} else {
			accountName = "discovery"
		}
	}

	return authLoginSelection{
		alias:          alias,
		accountName:    accountName,
		expectedUserID: expectedUserID,
		explicit:       alias != "" || userID != "",
	}, onePassword, nil
}

func (c *authLoginCmd) loginConfig(runtime *runtime, accountName string, expectedUserID string, onePassword stakelogin.OnePasswordConfig) stakelogin.Config {
	return stakelogin.Config{
		AccountName:    accountName,
		APIBaseURL:     runtime.baseURL,
		ExpectedUserID: expectedUserID,
		OnePassword:    onePassword,
		LoginURL:       c.LoginURL,
		BrowserTimeout: c.BrowserTimeout,
		ShowBrowser:    !c.Headless,
		KeepBrowser:    !c.AutoClose,
		PromptInput:    runtime.stdin,
	}
}

type usersCmd struct {
	Account string `arg:"" name:"account" help:"Stored account name"`
}

func (c *usersCmd) Run(runtime *runtime) error {
	entry, err := runtime.storedAccount(c.Account)
	if err != nil {
		return err
	}

	userList, err := runtime.stakeClient(c.Account, entry.SessionToken).ListUsers(runtime.ctx)
	if err != nil {
		return err
	}
	aliases, err := runtime.generatedAliases(userList)
	if err != nil {
		return err
	}

	response := usersResponse{
		Account:       c.Account,
		ActiveUser:    userList.ActiveUser,
		ActiveProduct: userList.ActiveProduct,
		MasterUserID:  userList.MasterUserID,
		Users:         make([]listedUserResponse, 0, len(userList.Users)),
	}
	for _, user := range userList.Users {
		products := make([]listedUserProductResponse, 0, len(user.Products))
		for _, product := range user.Products {
			products = append(products, listedUserProductResponse{
				Type:                        product.Type,
				Status:                      product.Status,
				ProductPartnerAccountNumber: product.ProductPartnerAccountNumber,
				HINNumber:                   product.HINNumber,
				OpenedDate:                  product.OpenedDate,
				LendingEnabled:              product.LendingEnabled,
				ExtendedHoursEnabled:        product.ExtendedHoursEnabled,
			})
		}

		response.Users = append(response.Users, listedUserResponse{
			Alias:            aliases[user.UserID],
			UserID:           user.UserID,
			FirstName:        user.FirstName,
			MiddleName:       user.MiddleName,
			LastName:         user.LastName,
			AccountType:      user.AccountType,
			AccountStatus:    user.AccountStatus,
			StakeKycStatus:   user.StakeKycStatus,
			RegionIdentifier: user.RegionIdentifier,
			EmailVerified:    user.EmailVerified,
			CreatedDate:      user.CreatedDate,
			WithdrawalAccess: user.WithdrawalAccess,
			Staff:            user.Staff,
			Active:           user.UserID == userList.ActiveUser,
			Master:           user.UserID == userList.MasterUserID,
			Products:         products,
		})
	}

	return writeOutput(runtime.stdout, response)
}

func (c *authLoginCmd) onePasswordConfig(runtime *runtime, alias string) (stakelogin.OnePasswordConfig, error) {
	itemReference := strings.TrimSpace(c.OPItem)
	desktopAccount := strings.TrimSpace(c.OPAccount)
	alias = strings.TrimSpace(alias)
	if alias != "" && (itemReference == "" || desktopAccount == "") {
		entry, err := runtime.storedAccount(alias)
		if err != nil && !errors.Is(err, sessionstore.ErrAccountNotFound) {
			return stakelogin.OnePasswordConfig{}, err
		}
		if err == nil {
			if itemReference == "" {
				itemReference = strings.TrimSpace(entry.OPItem)
			}
			if desktopAccount == "" && strings.TrimSpace(c.OPItem) == "" {
				desktopAccount = strings.TrimSpace(entry.OPAccount)
			}
		}
	}
	if itemReference == "" {
		if desktopAccount != "" {
			return stakelogin.OnePasswordConfig{}, fmt.Errorf("--op-account requires --op-item")
		}
		return stakelogin.OnePasswordConfig{}, nil
	}

	config := stakelogin.OnePasswordConfig{
		ItemReference:  itemReference,
		DesktopAccount: desktopAccount,
	}
	if config.DesktopAccount == "" {
		config.ServiceAccountToken = strings.TrimSpace(os.Getenv("OP_SERVICE_ACCOUNT_TOKEN"))
	}

	return config, nil
}

func (r *runtime) expectedLoginUserID(name string) (string, error) {
	entry, err := r.storedAccount(name)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAccountNotFound) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(entry.UserID), nil
}

func (c *authLoginCmd) syncLoginAccounts(runtime *runtime, selection authLoginSelection, initial *stakelogin.Result, onePassword stakelogin.OnePasswordConfig) ([]sessionstore.View, sessionstore.View, error) {
	if initial == nil {
		return nil, sessionstore.View{}, fmt.Errorf("login result is required")
	}

	client := stake.NewClient(stake.Config{
		BaseURL:      runtime.baseURL,
		Timeout:      runtime.timeout,
		SessionToken: initial.SessionToken,
	}, runtime.logger)

	userList, err := client.ListUsers(runtime.ctx)
	if err != nil || userList == nil || len(userList.Users) == 0 {
		if err != nil {
			runtime.logger.Warn("Discovering related Stake users failed; storing fallback account only", "account", selection.accountName, "error", err)
		}
		view, err := runtime.storeLoginFallbackAccount(selection.alias, selection.expectedUserID, client.SessionToken(), &onePassword)
		if err != nil {
			return nil, sessionstore.View{}, err
		}
		return []sessionstore.View{view}, view, nil
	}

	aliases, err := runtime.generatedAliases(userList)
	if err != nil {
		return nil, sessionstore.View{}, err
	}
	runtime.logger.Info("Discovered linked Stake accounts", "count", len(userList.Users), "active_user_id", strings.TrimSpace(userList.ActiveUser))

	users := append([]stake.ListedUser(nil), userList.Users...)
	sort.Slice(users, func(i, j int) bool {
		return aliases[users[i].UserID] < aliases[users[j].UserID]
	})

	currentUserID := strings.TrimSpace(userList.ActiveUser)
	viewsByUserID := make(map[string]sessionstore.View, len(users))
	if currentUserID != "" {
		if activeAlias, ok := aliases[currentUserID]; ok {
			view, err := runtime.validateAndStoreExpectedAccount(activeAlias, currentUserID, client.SessionToken(), &onePassword)
			if err != nil {
				return nil, sessionstore.View{}, err
			}
			viewsByUserID[currentUserID] = view
		}
	}

	for _, listedUser := range users {
		if listedUser.UserID == currentUserID {
			continue
		}
		runtime.logger.Info("Capturing Stake session for discovered alias", "alias", aliases[listedUser.UserID], "user_id", listedUser.UserID)

		result, err := runStakeLogin(runtime.ctx, c.loginConfig(runtime, aliases[listedUser.UserID], listedUser.UserID, onePassword), runtime.logger)
		if err != nil {
			return nil, sessionstore.View{}, err
		}
		if strings.TrimSpace(result.SessionToken) == "" {
			return nil, sessionstore.View{}, fmt.Errorf("login for %q did not return a session token", aliases[listedUser.UserID])
		}

		view, err := runtime.validateAndStoreExpectedAccount(aliases[listedUser.UserID], listedUser.UserID, result.SessionToken, &onePassword)
		if err != nil {
			return nil, sessionstore.View{}, err
		}
		viewsByUserID[listedUser.UserID] = view
	}

	views := make([]sessionstore.View, 0, len(users))
	for _, listedUser := range users {
		if view, ok := viewsByUserID[listedUser.UserID]; ok {
			views = append(views, view)
		}
	}

	activeView := viewsByUserID[currentUserID]
	if activeView.Name == "" && len(views) > 0 {
		activeView = views[0]
	}

	return views, activeView, nil
}

func (r *runtime) generatedAliases(userList *stake.UserList) (map[string]string, error) {
	if userList == nil {
		return nil, fmt.Errorf("stake user list is required")
	}

	store, err := sessionstore.Load(r.authStorePath)
	if err != nil {
		return nil, err
	}

	discoveredUserIDs := make(map[string]struct{}, len(userList.Users))
	users := append([]stake.ListedUser(nil), userList.Users...)
	for _, user := range users {
		discoveredUserIDs[strings.TrimSpace(user.UserID)] = struct{}{}
	}

	sort.Slice(users, func(i, j int) bool {
		leftPriority := generatedAliasPriority(users[i], userList.MasterUserID)
		rightPriority := generatedAliasPriority(users[j], userList.MasterUserID)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return generatedDisplayName(users[i]) < generatedDisplayName(users[j])
	})

	aliases := make(map[string]string, len(users))
	usedAliases := make(map[string]string, len(users))
	for _, user := range users {
		candidates := generatedAliasCandidates(user, userList.MasterUserID)
		if alias := preferredStoredAlias(strings.TrimSpace(user.UserID), candidates, store, usedAliases); alias != "" {
			aliases[user.UserID] = alias
			usedAliases[alias] = strings.TrimSpace(user.UserID)
		}
	}
	for {
		progressed := false
		for _, user := range users {
			if _, ok := aliases[user.UserID]; ok {
				continue
			}

			alias := pickGeneratedAlias(generatedAliasCandidates(user, userList.MasterUserID), strings.TrimSpace(user.UserID), store, discoveredUserIDs, aliases, usedAliases)
			if alias == "" {
				continue
			}

			aliases[user.UserID] = alias
			usedAliases[alias] = strings.TrimSpace(user.UserID)
			progressed = true
		}
		if !progressed {
			break
		}
	}
	for _, user := range users {
		if _, ok := aliases[user.UserID]; ok {
			continue
		}

		alias := pickGeneratedFallbackAlias(generatedAliasCandidates(user, userList.MasterUserID), strings.TrimSpace(user.UserID), store, discoveredUserIDs, aliases, usedAliases)
		aliases[user.UserID] = alias
		usedAliases[alias] = strings.TrimSpace(user.UserID)
	}

	return aliases, nil
}

func generatedAliasPriority(user stake.ListedUser, masterUserID string) int {
	if strings.TrimSpace(user.UserID) == strings.TrimSpace(masterUserID) && strings.EqualFold(strings.TrimSpace(user.AccountType), "INDIVIDUAL") {
		return 0
	}
	if isGeneratedSMSFUser(user) {
		return 1
	}
	return 2
}

func generatedAliasCandidates(user stake.ListedUser, masterUserID string) []string {
	displayName := generatedDisplayName(user)
	fullAlias := slugifyAlias(displayName)
	if fullAlias == "" {
		fullAlias = slugifyAlias(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(user.AccountType)), "_", " "))
	}

	candidates := make([]string, 0, 4)
	if strings.TrimSpace(user.UserID) == strings.TrimSpace(masterUserID) && strings.EqualFold(strings.TrimSpace(user.AccountType), "INDIVIDUAL") {
		candidates = append(candidates, "personal")
	}
	if isGeneratedSMSFUser(user) {
		candidates = append(candidates, "smsf")
	}
	if isGeneratedTrustUser(user) {
		candidates = append(candidates, shortTrustAlias(displayName))
	}
	if fullAlias != "" {
		candidates = append(candidates, fullAlias)
	}
	if len(candidates) == 0 {
		candidates = append(candidates, "account")
	}

	return uniqueGeneratedAliases(candidates...)
}

func preferredStoredAlias(userID string, candidates []string, store *sessionstore.File, usedAliases map[string]string) string {
	if store == nil {
		return ""
	}

	for _, candidate := range candidates {
		if usedUserID, ok := usedAliases[strings.TrimSpace(candidate)]; ok && usedUserID != userID {
			continue
		}

		entry, err := store.Get(candidate)
		if err == nil && strings.TrimSpace(entry.UserID) == userID {
			return candidate
		}
	}

	return ""
}

func pickGeneratedAlias(candidates []string, userID string, store *sessionstore.File, discoveredUserIDs map[string]struct{}, assignedAliases map[string]string, usedAliases map[string]string) string {
	for _, candidate := range candidates {
		available, shouldWait := generatedAliasAvailability(candidate, userID, store, discoveredUserIDs, assignedAliases, usedAliases)
		if available {
			return candidate
		}
		if shouldWait {
			return ""
		}
	}

	return ""
}

func pickGeneratedFallbackAlias(candidates []string, userID string, store *sessionstore.File, discoveredUserIDs map[string]struct{}, assignedAliases map[string]string, usedAliases map[string]string) string {
	for _, candidate := range candidates {
		available, _ := generatedAliasAvailability(candidate, userID, store, discoveredUserIDs, assignedAliases, usedAliases)
		if available {
			return candidate
		}
	}

	base := "account"
	if len(candidates) > 0 && strings.TrimSpace(candidates[0]) != "" {
		base = strings.TrimSpace(candidates[0])
	}
	suffix := shortGeneratedUserIDSuffix(userID)
	available, _ := generatedAliasAvailability(base+"-"+suffix, userID, store, discoveredUserIDs, assignedAliases, usedAliases)
	if available {
		return base + "-" + suffix
	}

	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%s-%d", base, suffix, index)
		available, _ := generatedAliasAvailability(candidate, userID, store, discoveredUserIDs, assignedAliases, usedAliases)
		if available {
			return candidate
		}
	}
}

func generatedAliasAvailability(alias string, userID string, store *sessionstore.File, discoveredUserIDs map[string]struct{}, assignedAliases map[string]string, usedAliases map[string]string) (bool, bool) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return false, false
	}
	if usedUserID, ok := usedAliases[alias]; ok && usedUserID != userID {
		return false, false
	}
	if store == nil {
		return true, false
	}

	entry, err := store.Get(alias)
	if err != nil {
		return errors.Is(err, sessionstore.ErrAccountNotFound), false
	}
	existingUserID := strings.TrimSpace(entry.UserID)
	if existingUserID == "" || existingUserID == userID {
		return true, false
	}
	if assignedAlias, ok := assignedAliases[existingUserID]; ok && assignedAlias != alias {
		return true, false
	}
	_, discovered := discoveredUserIDs[existingUserID]
	return false, discovered
}

func generatedDisplayName(user stake.ListedUser) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{user.FirstName, user.MiddleName, user.LastName} {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func isGeneratedSMSFUser(user stake.ListedUser) bool {
	return user.StakeSMSFCustomer || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(user.AccountType)), "SMSF_")
}

func isGeneratedTrustUser(user stake.ListedUser) bool {
	accountType := strings.ToUpper(strings.TrimSpace(user.AccountType))
	return strings.Contains(accountType, "TRUST") && !isGeneratedSMSFUser(user)
}

func shortTrustAlias(displayName string) string {
	cleaned := strings.ToLower(strings.TrimSpace(displayName))
	cleaned = strings.TrimPrefix(cleaned, "the trustee for the ")
	cleaned = strings.TrimPrefix(cleaned, "the trustee for ")

	words := generatedAliasWords(cleaned)
	if len(words) >= 2 && words[len(words)-1] == "trust" {
		return strings.Join(words[len(words)-2:], "-")
	}
	return slugifyAlias(cleaned)
}

func slugifyAlias(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	previousHyphen := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			previousHyphen = false
		case !previousHyphen:
			builder.WriteByte('-')
			previousHyphen = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func generatedAliasWords(value string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func uniqueGeneratedAliases(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	aliases := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		aliases = append(aliases, trimmed)
	}
	return aliases
}

func shortGeneratedUserIDSuffix(userID string) string {
	compact := strings.ReplaceAll(strings.TrimSpace(userID), "-", "")
	if len(compact) > 8 {
		compact = compact[:8]
	}
	if compact == "" {
		return "user"
	}
	return compact
}

func inferredLoginAlias(user *stake.User) string {
	if user == nil {
		return "account"
	}

	accountType := strings.ToUpper(strings.TrimSpace(user.AccountType))
	if accountType == "INDIVIDUAL" {
		return "personal"
	}
	if strings.HasPrefix(accountType, "SMSF_") {
		return "smsf"
	}

	displayName := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(user.FirstName), strings.TrimSpace(user.LastName)}, " "))
	if strings.Contains(accountType, "TRUST") {
		if alias := shortTrustAlias(displayName); alias != "" {
			return alias
		}
	}
	if alias := slugifyAlias(displayName); alias != "" {
		return alias
	}
	if alias := slugifyAlias(strings.ReplaceAll(strings.ToLower(accountType), "_", " ")); alias != "" {
		return alias
	}
	return "account"
}

func (r *runtime) validateAndStoreAccount(name string, token string, onePassword *stakelogin.OnePasswordConfig) (sessionstore.View, error) {
	return r.validateAndStoreExpectedAccount(name, "", token, onePassword)
}

func (r *runtime) storeLoginFallbackAccount(preferredName string, expectedUserID string, token string, onePassword *stakelogin.OnePasswordConfig) (sessionstore.View, error) {
	client := stake.NewClient(stake.Config{
		BaseURL:      r.baseURL,
		Timeout:      r.timeout,
		SessionToken: strings.TrimSpace(token),
	}, r.logger)

	user, err := client.ValidateSession(r.ctx)
	if err != nil {
		return sessionstore.View{}, err
	}
	if expectedUserID = strings.TrimSpace(expectedUserID); expectedUserID != "" && strings.TrimSpace(user.UserID) != expectedUserID {
		return sessionstore.View{}, fmt.Errorf("validated user %q, expected user %q", user.UserID, expectedUserID)
	}

	name := strings.TrimSpace(preferredName)
	if name == "" {
		name = inferredLoginAlias(user)
	}

	return r.storeValidatedAccount(name, client.SessionToken(), user, onePassword)
}

func (r *runtime) validateAndStoreExpectedAccount(name string, expectedUserID string, token string, onePassword *stakelogin.OnePasswordConfig) (sessionstore.View, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return sessionstore.View{}, fmt.Errorf("account name is required")
	}

	expectedUserID = strings.TrimSpace(expectedUserID)
	token = strings.TrimSpace(token)
	if token == "" {
		return sessionstore.View{}, fmt.Errorf("stake session token is required")
	}

	client := stake.NewClient(stake.Config{
		BaseURL:      r.baseURL,
		Timeout:      r.timeout,
		SessionToken: token,
	}, r.logger)

	user, err := client.ValidateSession(r.ctx)
	if err != nil {
		return sessionstore.View{}, err
	}
	if expectedUserID != "" && strings.TrimSpace(user.UserID) != expectedUserID {
		return sessionstore.View{}, fmt.Errorf("validated user %q, expected user %q", user.UserID, expectedUserID)
	}

	return r.storeValidatedAccount(name, client.SessionToken(), user, onePassword)
}

func (r *runtime) storeValidatedAccount(name string, token string, user *stake.User, onePassword *stakelogin.OnePasswordConfig) (sessionstore.View, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return sessionstore.View{}, fmt.Errorf("account name is required")
	}
	if user == nil {
		return sessionstore.View{}, fmt.Errorf("stake user is required")
	}

	entry := sessionstore.Entry{
		Name:         name,
		SessionToken: strings.TrimSpace(token),
		UserID:       user.UserID,
		Email:        user.Email,
		Username:     user.Username,
		AccountType:  user.AccountType,
		UpdatedAt:    time.Now().UTC(),
	}
	if onePassword != nil {
		entry.OPItem = strings.TrimSpace(onePassword.ItemReference)
		entry.OPAccount = strings.TrimSpace(onePassword.DesktopAccount)
	}
	if err := sessionstore.Update(r.authStorePath, func(store *sessionstore.File) error {
		stored, err := store.Get(name)
		if err != nil && !errors.Is(err, sessionstore.ErrAccountNotFound) {
			return err
		}
		if err == nil {
			if entry.OPItem == "" {
				entry.OPItem = stored.OPItem
			}
			if entry.OPAccount == "" {
				entry.OPAccount = stored.OPAccount
			}
		}
		store.Upsert(entry)
		return nil
	}); err != nil {
		return sessionstore.View{}, err
	}

	return entry.View(), nil
}

type authListCmd struct{}

func (c *authListCmd) Run(runtime *runtime) error {
	store, err := sessionstore.Load(runtime.authStorePath)
	if err != nil {
		return err
	}
	return writeOutput(runtime.stdout, authAccountsResponse{Accounts: store.Views()})
}

type authTokenCmd struct {
	Name string `arg:"" name:"name" help:"Local account name"`
	JSON bool   `help:"Output structured JSON instead of the raw session token"`
}

func (c *authTokenCmd) Run(runtime *runtime) error {
	entry, err := runtime.storedAccount(c.Name)
	if err != nil {
		return err
	}

	if !c.JSON {
		_, err := fmt.Fprintln(runtime.stdout, entry.SessionToken)
		return err
	}

	return writeOutput(runtime.stdout, entry.TokenView())
}

type authRemoveCmd struct {
	Name string `arg:"" name:"name" help:"Local account name"`
}

func (c *authRemoveCmd) Run(runtime *runtime) error {
	return sessionstore.Update(runtime.authStorePath, func(store *sessionstore.File) error {
		if !store.Delete(c.Name) {
			return sessionstore.ErrAccountNotFound
		}
		return nil
	})
}

type authProbeCmd struct {
	Name        string        `arg:"" name:"name" help:"Stored account name"`
	Interval    time.Duration `help:"Wait between validation attempts" default:"30s"`
	MaxAttempts int           `help:"Stop after this many validation attempts; zero runs until failure or interruption" name:"max-attempts"`
}

func (c *authProbeCmd) Run(runtime *runtime) error {
	if c.Interval <= 0 {
		return fmt.Errorf("probe interval must be greater than zero")
	}
	if c.MaxAttempts < 0 {
		return fmt.Errorf("max attempts must be zero or greater")
	}

	entry, err := runtime.storedAccount(c.Name)
	if err != nil {
		return err
	}

	client := runtime.stakeClient(c.Name, entry.SessionToken)
	previousToken := client.SessionToken()
	report := authProbeResponse{
		Account:   c.Name,
		StartedAt: time.Now().UTC(),
		Interval:  c.Interval.String(),
	}
	if entry.UserID != "" || entry.Email != "" || entry.Username != "" || entry.AccountType != "" {
		report.User = &stake.User{
			UserID:      entry.UserID,
			Email:       entry.Email,
			Username:    entry.Username,
			AccountType: entry.AccountType,
		}
	}

	for {
		if runtime.ctx.Err() != nil {
			report.StoppedReason = "canceled"
			break
		}

		attempt := report.Attempts + 1
		report.Attempts = attempt
		report.LastCheckedAt = time.Now().UTC()

		user, err := client.ValidateSession(runtime.ctx)
		if err != nil {
			report.StoppedReason = "validation_failed"
			report.LastError = err.Error()
			runtime.logger.Warn("Stake session probe failed", "account", c.Name, "attempt", attempt, "error", err)
			break
		}

		validatedAt := time.Now().UTC()
		report.Successes++
		report.LastSuccessAt = &validatedAt
		report.User = user

		currentToken := client.SessionToken()
		rotated := currentToken != previousToken
		if rotated {
			report.Rotations++
			previousToken = currentToken
			runtime.logger.Info("Stake session token rotated during probe", "account", c.Name, "attempt", attempt)
		}

		if err := sessionstore.Update(runtime.authStorePath, func(store *sessionstore.File) error {
			updated, err := store.Get(c.Name)
			if err != nil {
				return err
			}
			updated.SessionToken = currentToken
			updated.UserID = user.UserID
			updated.Email = user.Email
			updated.Username = user.Username
			updated.AccountType = user.AccountType
			updated.UpdatedAt = validatedAt
			store.Upsert(*updated)
			return nil
		}); err != nil {
			return err
		}

		runtime.logger.Info("Stake session probe succeeded", "account", c.Name, "attempt", attempt, "rotated", rotated)

		if c.MaxAttempts > 0 && attempt >= c.MaxAttempts {
			report.StoppedReason = "max_attempts"
			break
		}

		timer := time.NewTimer(c.Interval)
		select {
		case <-runtime.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			report.StoppedReason = "canceled"
			goto done
		case <-timer.C:
		}
	}

done:
	report.EndedAt = time.Now().UTC()
	if report.StoppedReason == "" {
		report.StoppedReason = "completed"
	}

	return writeOutput(runtime.stdout, report)
}

type statusCmd struct{}

func (c *statusCmd) Run(runtime *runtime) error {
	store, err := sessionstore.Load(runtime.authStorePath)
	if err != nil {
		return err
	}

	response := statusResponse{Accounts: make([]accountStatusResponse, 0, len(store.Accounts))}
	for _, stored := range store.Accounts {
		client := runtime.stakeClient(stored.Name, stored.SessionToken)
		user, err := client.ValidateSession(runtime.ctx)
		if err != nil {
			response.Accounts = append(response.Accounts, accountStatusResponse{
				Account: stored.Name,
				OK:      false,
				Error:   err.Error(),
			})
			continue
		}

		validatedAt := time.Now().UTC()
		if err := sessionstore.Update(runtime.authStorePath, func(store *sessionstore.File) error {
			updated, err := store.Get(stored.Name)
			if err != nil {
				return err
			}
			updated.SessionToken = client.SessionToken()
			updated.UserID = user.UserID
			updated.Email = user.Email
			updated.Username = user.Username
			updated.AccountType = user.AccountType
			updated.UpdatedAt = validatedAt
			store.Upsert(*updated)
			return nil
		}); err != nil {
			return err
		}

		response.Accounts = append(response.Accounts, accountStatusResponse{
			Account:     stored.Name,
			OK:          true,
			ValidatedAt: &validatedAt,
			User:        user,
		})
	}

	return writeOutput(runtime.stdout, response)
}

type authStatusCmd struct{}

func (c *authStatusCmd) Run(runtime *runtime) error {
	return (&statusCmd{}).Run(runtime)
}

type tradesCmd struct {
	Account string `arg:"" name:"account" help:"Stored account name"`
}

func (c *tradesCmd) Run(runtime *runtime) error {
	entry, err := runtime.storedAccount(c.Account)
	if err != nil {
		return err
	}

	client := runtime.stakeClient(c.Account, entry.SessionToken)
	trades, err := client.FetchTrades(runtime.ctx, c.Account)
	if err != nil {
		return err
	}
	fetchedAt := time.Now().UTC()

	if err := sessionstore.Update(runtime.authStorePath, func(store *sessionstore.File) error {
		updated, err := store.Get(c.Account)
		if err != nil {
			return err
		}
		updated.SessionToken = client.SessionToken()
		updated.UpdatedAt = fetchedAt
		store.Upsert(*updated)
		return nil
	}); err != nil {
		return err
	}

	return writeOutput(runtime.stdout, tradesResponse{
		Account:   c.Account,
		Count:     len(trades),
		FetchedAt: fetchedAt,
		Trades:    trades,
	})
}
