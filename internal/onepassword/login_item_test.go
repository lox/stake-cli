package onepassword

import (
	"context"
	"testing"

	onepasswordsdk "github.com/1password/onepassword-sdk-go"
)

type fakeSecretsResolver struct {
	response onepasswordsdk.ResolveAllResponse
	err      error
	refs     []string
}

func (f *fakeSecretsResolver) ResolveAll(_ context.Context, secretReferences []string) (onepasswordsdk.ResolveAllResponse, error) {
	f.refs = append([]string(nil), secretReferences...)
	return f.response, f.err
}

type fakeItemsReader struct {
	itemsByVault map[string][]onepasswordsdk.ItemOverview
	itemByKey    map[string]onepasswordsdk.Item
	listErr      error
	getErr       error
}

func (f *fakeItemsReader) List(_ context.Context, vaultID string, _ ...onepasswordsdk.ItemListFilter) ([]onepasswordsdk.ItemOverview, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]onepasswordsdk.ItemOverview(nil), f.itemsByVault[vaultID]...), nil
}

func (f *fakeItemsReader) Get(_ context.Context, vaultID string, itemID string) (onepasswordsdk.Item, error) {
	if f.getErr != nil {
		return onepasswordsdk.Item{}, f.getErr
	}
	return f.itemByKey[vaultID+"/"+itemID], nil
}

type fakeVaultsReader struct {
	vaults []onepasswordsdk.VaultOverview
	err    error
}

func (f *fakeVaultsReader) List(_ context.Context, _ ...onepasswordsdk.VaultListParams) ([]onepasswordsdk.VaultOverview, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]onepasswordsdk.VaultOverview(nil), f.vaults...), nil
}

func TestLoadLoginCredentialsReturnsCredentialsAndOptionalTOTP(t *testing.T) {
	resolver := &fakeSecretsResolver{
		response: onepasswordsdk.ResolveAllResponse{
			IndividualResponses: map[string]onepasswordsdk.Response[onepasswordsdk.ResolvedReference, onepasswordsdk.ResolveReferenceError]{
				"op://stake/family-trust/username":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "lachlan@example.test"}},
				"op://stake/family-trust/password":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "super-secret"}},
				"op://stake/family-trust/TOTP_onetimepassword?attribute=totp": {Content: &onepasswordsdk.ResolvedReference{Secret: "123456"}},
			},
		},
	}

	credentials, err := loadLoginCredentials(context.Background(), resolver, &fakeItemsReader{}, &fakeVaultsReader{}, "op://stake/family-trust")
	if err != nil {
		t.Fatalf("loadLoginCredentials returned error: %v", err)
	}
	if credentials.Email != "lachlan@example.test" {
		t.Fatalf("unexpected email: %q", credentials.Email)
	}
	if credentials.Password != "super-secret" {
		t.Fatalf("unexpected password: %q", credentials.Password)
	}
	if credentials.MFACode != "123456" {
		t.Fatalf("unexpected MFA code: %q", credentials.MFACode)
	}
	if len(resolver.refs) != 4 {
		t.Fatalf("expected 4 secret references, got %d", len(resolver.refs))
	}
}

func TestLoadLoginCredentialsAllowsMissingTOTP(t *testing.T) {
	resolver := &fakeSecretsResolver{
		response: onepasswordsdk.ResolveAllResponse{
			IndividualResponses: map[string]onepasswordsdk.Response[onepasswordsdk.ResolvedReference, onepasswordsdk.ResolveReferenceError]{
				"op://stake/personal/username":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "lachlan@example.test"}},
				"op://stake/personal/password":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "super-secret"}},
				"op://stake/personal/TOTP_onetimepassword?attribute=totp": {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound}},
				"op://stake/personal/onetimepassword?attribute=totp":      {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound}},
			},
		},
	}

	credentials, err := loadLoginCredentials(context.Background(), resolver, &fakeItemsReader{}, &fakeVaultsReader{}, "op://stake/personal")
	if err != nil {
		t.Fatalf("loadLoginCredentials returned error: %v", err)
	}
	if credentials.MFACode != "" {
		t.Fatalf("expected empty MFA code, got %q", credentials.MFACode)
	}
}

func TestLoadLoginCredentialsReturnsRequiredFieldErrors(t *testing.T) {
	resolver := &fakeSecretsResolver{
		response: onepasswordsdk.ResolveAllResponse{
			IndividualResponses: map[string]onepasswordsdk.Response[onepasswordsdk.ResolvedReference, onepasswordsdk.ResolveReferenceError]{
				"op://stake/personal/username": {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound}},
				"op://stake/personal/password": {Content: &onepasswordsdk.ResolvedReference{Secret: "super-secret"}},
			},
		},
	}

	if _, err := loadLoginCredentials(context.Background(), resolver, &fakeItemsReader{}, &fakeVaultsReader{}, "op://stake/personal"); err == nil {
		t.Fatal("expected loadLoginCredentials to fail when username is missing")
	}
}

func TestLoadLoginCredentialsReturnsUnexpectedTOTPErrors(t *testing.T) {
	resolver := &fakeSecretsResolver{
		response: onepasswordsdk.ResolveAllResponse{
			IndividualResponses: map[string]onepasswordsdk.Response[onepasswordsdk.ResolvedReference, onepasswordsdk.ResolveReferenceError]{
				"op://stake/personal/username":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "lachlan@example.test"}},
				"op://stake/personal/password":                            {Content: &onepasswordsdk.ResolvedReference{Secret: "super-secret"}},
				"op://stake/personal/TOTP_onetimepassword?attribute=totp": {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantUnableToGenerateTOTPCode}},
			},
		},
	}

	if _, err := loadLoginCredentials(context.Background(), resolver, &fakeItemsReader{}, &fakeVaultsReader{}, "op://stake/personal"); err == nil {
		t.Fatal("expected loadLoginCredentials to fail when TOTP generation fails")
	}
}

func TestLoadLoginCredentialsDiscoversDynamicTOTPField(t *testing.T) {
	otpDetails := onepasswordsdk.NewItemFieldDetailsTypeVariantOTP(&onepasswordsdk.OTPFieldDetails{})
	resolver := &fakeSecretsResolver{
		response: onepasswordsdk.ResolveAllResponse{
			IndividualResponses: map[string]onepasswordsdk.Response[onepasswordsdk.ResolvedReference, onepasswordsdk.ResolveReferenceError]{
				"op://Personal/stake.com/username":                              {Content: &onepasswordsdk.ResolvedReference{Secret: "lachlan@example.test"}},
				"op://Personal/stake.com/password":                              {Content: &onepasswordsdk.ResolvedReference{Secret: "super-secret"}},
				"op://Personal/stake.com/TOTP_onetimepassword?attribute=totp":   {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound}},
				"op://Personal/stake.com/onetimepassword?attribute=totp":        {Error: &onepasswordsdk.ResolveReferenceError{Type: onepasswordsdk.ResolveReferenceErrorTypeVariantFieldNotFound}},
				"op://Personal/stake.com/Section_ABC/TOTP_FIELD?attribute=totp": {Content: &onepasswordsdk.ResolvedReference{Secret: "123456"}},
			},
		},
	}
	items := &fakeItemsReader{
		itemsByVault: map[string][]onepasswordsdk.ItemOverview{
			"vault-1": {{ID: "item-1", Title: "stake.com", VaultID: "vault-1"}},
		},
		itemByKey: map[string]onepasswordsdk.Item{
			"vault-1/item-1": {
				ID:      "item-1",
				Title:   "stake.com",
				VaultID: "vault-1",
				Fields: []onepasswordsdk.ItemField{{
					ID:        "TOTP_FIELD",
					SectionID: ptrTo("Section_ABC"),
					Details:   &otpDetails,
				}},
			},
		},
	}
	vaults := &fakeVaultsReader{vaults: []onepasswordsdk.VaultOverview{{ID: "vault-1", Title: "Personal"}}}

	credentials, err := loadLoginCredentials(context.Background(), resolver, items, vaults, "op://Private/item-1")
	if err != nil {
		t.Fatalf("loadLoginCredentials returned error: %v", err)
	}
	if credentials.MFACode != "123456" {
		t.Fatalf("unexpected MFA code: %q", credentials.MFACode)
	}
	if len(resolver.refs) != 1 || resolver.refs[0] != "op://Personal/stake.com/Section_ABC/TOTP_FIELD?attribute=totp" {
		t.Fatalf("expected dynamic TOTP lookup, got %#v", resolver.refs)
	}
}

func TestNormalizeItemReference(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid", input: "op://stake/personal", want: "op://stake/personal"},
		{name: "trim trailing slash", input: " op://stake/personal/ ", want: "op://stake/personal"},
		{name: "missing prefix", input: "stake/personal", wantErr: true},
		{name: "field ref", input: "op://stake/personal/password", wantErr: true},
		{name: "query ref", input: "op://stake/personal?attribute=totp", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeItemReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected normalizeItemReference error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeItemReference returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeItemReference(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func ptrTo(value string) *string {
	return &value
}
