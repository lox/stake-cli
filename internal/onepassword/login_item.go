package onepassword

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	onepasswordsdk "github.com/1password/onepassword-sdk-go"
)

const (
	integrationName    = "stake-cli"
	integrationVersion = "dev"
)

// AuthConfig controls how stake-cli authenticates to 1Password.
type AuthConfig struct {
	ServiceAccountToken string
	DesktopAccount      string
}

// LoginCredentials contains the credential values resolved from one 1Password item.
type LoginCredentials struct {
	ItemReference string
	Email         string
	Password      string
	MFACode       string
}

type secretsResolver interface {
	ResolveAll(ctx context.Context, secretReferences []string) (onepasswordsdk.ResolveAllResponse, error)
}

type itemsReader interface {
	Get(ctx context.Context, vaultID string, itemID string) (onepasswordsdk.Item, error)
	List(ctx context.Context, vaultID string, filters ...onepasswordsdk.ItemListFilter) ([]onepasswordsdk.ItemOverview, error)
}

type vaultsReader interface {
	List(ctx context.Context, params ...onepasswordsdk.VaultListParams) ([]onepasswordsdk.VaultOverview, error)
}

// LoadLoginCredentials resolves Stake login credentials from one 1Password item reference.
func LoadLoginCredentials(ctx context.Context, auth AuthConfig, itemReference string) (LoginCredentials, error) {
	itemReference, err := normalizeItemReference(itemReference)
	if err != nil {
		return LoginCredentials{}, err
	}

	client, err := newClient(ctx, auth)
	if err != nil {
		return LoginCredentials{}, err
	}

	credentials, err := loadLoginCredentials(ctx, client.Secrets(), client.Items(), client.Vaults(), itemReference)
	runtime.KeepAlive(client)
	if err != nil {
		return LoginCredentials{}, err
	}
	credentials.ItemReference = itemReference

	return credentials, nil
}

func newClient(ctx context.Context, auth AuthConfig) (*onepasswordsdk.Client, error) {
	serviceAccountToken := strings.TrimSpace(auth.ServiceAccountToken)
	desktopAccount := strings.TrimSpace(auth.DesktopAccount)

	if serviceAccountToken == "" && desktopAccount == "" {
		return nil, fmt.Errorf("1Password auth requires OP_SERVICE_ACCOUNT_TOKEN or --op-account")
	}

	var (
		client *onepasswordsdk.Client
		err    error
	)
	if serviceAccountToken != "" {
		client, err = onepasswordsdk.NewClient(
			ctx,
			onepasswordsdk.WithServiceAccountToken(serviceAccountToken),
			onepasswordsdk.WithIntegrationInfo(integrationName, integrationVersion),
		)
	} else {
		client, err = onepasswordsdk.NewClient(
			ctx,
			onepasswordsdk.WithDesktopAppIntegration(desktopAccount),
			onepasswordsdk.WithIntegrationInfo(integrationName, integrationVersion),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize 1Password client: %w", err)
	}

	return client, nil
}

func loadLoginCredentials(ctx context.Context, resolver secretsResolver, items itemsReader, vaults vaultsReader, itemReference string) (LoginCredentials, error) {
	canonicalItemReference, item, err := canonicalizeItemReference(ctx, items, vaults, itemReference)
	if err != nil {
		canonicalItemReference = itemReference
	}

	refs := []string{
		canonicalItemReference + "/username",
		canonicalItemReference + "/password",
		canonicalItemReference + "/TOTP_onetimepassword?attribute=totp",
		canonicalItemReference + "/onetimepassword?attribute=totp",
	}

	resolved, err := resolver.ResolveAll(ctx, refs)
	if err != nil {
		return LoginCredentials{}, fmt.Errorf("resolve 1Password item fields: %w", err)
	}

	email, err := requiredSecret(resolved, refs[0], "username")
	if err != nil {
		return LoginCredentials{}, err
	}
	password, err := requiredSecret(resolved, refs[1], "password")
	if err != nil {
		return LoginCredentials{}, err
	}
	mfaCode, err := optionalTOTPSecret(resolved, refs[2:]...)
	if err != nil {
		return LoginCredentials{}, err
	}
	if mfaCode == "" && item != nil {
		mfaCode, err = resolveDynamicTOTPSecret(ctx, resolver, canonicalItemReference, *item)
		if err != nil {
			return LoginCredentials{}, err
		}
	}

	return LoginCredentials{
		Email:    email,
		Password: password,
		MFACode:  mfaCode,
	}, nil
}

func requiredSecret(resolved onepasswordsdk.ResolveAllResponse, ref string, fieldName string) (string, error) {
	response, ok := resolved.IndividualResponses[ref]
	if !ok {
		return "", fmt.Errorf("resolve 1Password %s: missing response", fieldName)
	}
	if response.Error != nil {
		return "", fmt.Errorf("resolve 1Password %s: %s", fieldName, response.Error.Type)
	}
	if response.Content == nil {
		return "", fmt.Errorf("resolve 1Password %s: missing secret value", fieldName)
	}

	secret := strings.TrimSpace(response.Content.Secret)
	if secret == "" {
		return "", fmt.Errorf("resolve 1Password %s: empty secret value", fieldName)
	}

	return secret, nil
}

func optionalTOTPSecret(resolved onepasswordsdk.ResolveAllResponse, refs ...string) (string, error) {
	for _, ref := range refs {
		response, ok := resolved.IndividualResponses[ref]
		if !ok || response.Content == nil && response.Error == nil {
			continue
		}
		if response.Error != nil {
			if response.Error.Type == onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound ||
				response.Error.Type == onepasswordsdk.ResolveReferenceErrorTypeVariantNoMatchingSections {
				continue
			}
			return "", fmt.Errorf("resolve 1Password one-time password: %s", response.Error.Type)
		}
		secret := strings.TrimSpace(response.Content.Secret)
		if secret != "" {
			return secret, nil
		}
	}

	return "", nil
}

func canonicalizeItemReference(ctx context.Context, items itemsReader, vaults vaultsReader, itemReference string) (string, *onepasswordsdk.Item, error) {
	vaultName, itemName, err := splitItemReference(itemReference)
	if err != nil {
		return "", nil, err
	}

	vault, err := findVault(ctx, vaults, vaultName)
	if err != nil {
		return "", nil, err
	}
	itemOverview, err := findItem(ctx, items, vault.ID, itemName)
	if err != nil {
		return "", nil, err
	}
	item, err := items.Get(ctx, vault.ID, itemOverview.ID)
	if err != nil {
		return "", nil, fmt.Errorf("load 1Password item metadata: %w", err)
	}

	canonicalItemReference := fmt.Sprintf("op://%s/%s", vault.Title, item.Title)
	return canonicalItemReference, &item, nil
}

func resolveDynamicTOTPSecret(ctx context.Context, resolver secretsResolver, itemReference string, item onepasswordsdk.Item) (string, error) {
	for _, field := range item.Fields {
		if field.Details == nil || field.Details.OTP() == nil {
			continue
		}

		ref := itemReference
		if field.SectionID != nil && strings.TrimSpace(*field.SectionID) != "" {
			ref += "/" + strings.TrimSpace(*field.SectionID)
		}
		ref += "/" + strings.TrimSpace(field.ID) + "?attribute=totp"

		resolved, err := resolver.ResolveAll(ctx, []string{ref})
		if err != nil {
			return "", fmt.Errorf("resolve 1Password one-time password: %w", err)
		}

		secret, err := optionalTOTPSecret(resolved, ref)
		if err != nil {
			return "", err
		}
		if secret != "" {
			return secret, nil
		}
	}

	return "", nil
}

func splitItemReference(itemReference string) (string, string, error) {
	parts := strings.Split(strings.TrimPrefix(itemReference, "op://"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("1Password item reference must use op://vault/item")
	}

	vaultName := strings.TrimSpace(parts[0])
	itemName := strings.TrimSpace(parts[1])
	if vaultName == "" || itemName == "" {
		return "", "", fmt.Errorf("1Password item reference must use op://vault/item")
	}

	return vaultName, itemName, nil
}

func findVault(ctx context.Context, vaults vaultsReader, want string) (onepasswordsdk.VaultOverview, error) {
	allVaults, err := vaults.List(ctx)
	if err != nil {
		return onepasswordsdk.VaultOverview{}, fmt.Errorf("list 1Password vaults: %w", err)
	}

	aliases := builtInVaultAliases(want)
	for _, vault := range allVaults {
		if strings.EqualFold(strings.TrimSpace(vault.ID), want) {
			return vault, nil
		}
		for _, alias := range aliases {
			if strings.EqualFold(strings.TrimSpace(vault.Title), alias) {
				return vault, nil
			}
		}
	}

	return onepasswordsdk.VaultOverview{}, fmt.Errorf("find 1Password vault %q", want)
}

func builtInVaultAliases(name string) []string {
	trimmed := strings.TrimSpace(name)
	aliases := []string{trimmed}
	switch strings.ToLower(trimmed) {
	case "private":
		aliases = append(aliases, "Personal")
	case "personal":
		aliases = append(aliases, "Private")
	}
	return aliases
}

func findItem(ctx context.Context, items itemsReader, vaultID string, want string) (onepasswordsdk.ItemOverview, error) {
	allItems, err := items.List(ctx, vaultID)
	if err != nil {
		return onepasswordsdk.ItemOverview{}, fmt.Errorf("list 1Password items: %w", err)
	}

	for _, item := range allItems {
		if strings.EqualFold(strings.TrimSpace(item.ID), want) || strings.EqualFold(strings.TrimSpace(item.Title), want) {
			return item, nil
		}
	}

	return onepasswordsdk.ItemOverview{}, fmt.Errorf("find 1Password item %q", want)
}

func normalizeItemReference(itemReference string) (string, error) {
	itemReference = strings.TrimSpace(itemReference)
	itemReference = strings.TrimSuffix(itemReference, "/")

	if itemReference == "" {
		return "", fmt.Errorf("1Password item reference is required")
	}
	if !strings.HasPrefix(itemReference, "op://") {
		return "", fmt.Errorf("1Password item reference must start with op://")
	}
	if strings.Contains(itemReference, "?") {
		return "", fmt.Errorf("1Password item reference must point to an item, not a field query")
	}

	parts := strings.Split(strings.TrimPrefix(itemReference, "op://"), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("1Password item reference must use op://vault/item")
	}

	return itemReference, nil
}
