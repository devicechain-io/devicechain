// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
)

// provisionedFieldsMatch decides whether the reconcile skips a no-op write: it
// compares only the config-managed fields (secret hash, redirect URIs, scopes) and
// deliberately ignores `enabled` (runtime-owned after creation).
func TestProvisionedFieldsMatch(t *testing.T) {
	sc := SeedOAuthClient{
		ClientId:     "grafana",
		RedirectURIs: []string{"https://dc.example.com/grafana/login/generic_oauth"},
		Scopes:       []string{"read-only"},
		SecretHash:   "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV0123456",
	}
	base := &iam.OAuthClient{
		RedirectURIs: []string{"https://dc.example.com/grafana/login/generic_oauth"},
		Scopes:       []string{"read-only"},
		SecretHash:   sc.SecretHash,
		Enabled:      true,
	}

	if !provisionedFieldsMatch(base, sc) {
		t.Error("identical config-managed fields should match")
	}

	// `enabled` is not a config-managed field: an admin-disabled client still matches,
	// so the reconcile does NOT re-enable it.
	disabled := *base
	disabled.Enabled = false
	if !provisionedFieldsMatch(&disabled, sc) {
		t.Error("a disabled client with matching provisioned fields must still match (enabled is runtime-owned)")
	}

	// Any drift in a managed field triggers a re-sync.
	for name, mutate := range map[string]func(*iam.OAuthClient){
		"secret rotated": func(c *iam.OAuthClient) {
			c.SecretHash = "$2a$10$DIFFERENThashDIFFERENThashDIFFERENThashDIFFERENThash00"
		},
		"redirect added":    func(c *iam.OAuthClient) { c.RedirectURIs = append(c.RedirectURIs, "https://evil/cb") },
		"redirect replaced": func(c *iam.OAuthClient) { c.RedirectURIs = []string{"https://other/cb"} },
		"scope changed":     func(c *iam.OAuthClient) { c.Scopes = []string{"write"} },
		"secret cleared":    func(c *iam.OAuthClient) { c.SecretHash = "" },
	} {
		drifted := *base
		drifted.RedirectURIs = append([]string(nil), base.RedirectURIs...)
		drifted.Scopes = append([]string(nil), base.Scopes...)
		mutate(&drifted)
		if provisionedFieldsMatch(&drifted, sc) {
			t.Errorf("%s: drifted client must NOT match (should trigger re-sync)", name)
		}
	}
}
