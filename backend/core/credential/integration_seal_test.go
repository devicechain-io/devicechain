// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An integration's bearer token is sealed for the tenant AND the one integration it
// authenticates: it opens for exactly that pair, and never as a device's ACCESS_TOKEN.
func TestAnIntegrationTokenOpensOnlyForItsTenantAndIntegration(t *testing.T) {
	tr := testTransit(t, 3)
	sealed, err := tr.SealToken("acme", IntegrationSealKind("chirp-1"), "bearer-token-value")
	require.NoError(t, err)

	got, err := tr.OpenToken("acme", IntegrationSealKind("chirp-1"), sealed)
	require.NoError(t, err, "the paired positive")
	require.Equal(t, "bearer-token-value", got)

	for name, open := range map[string]func() (string, error){
		"another integration": func() (string, error) { return tr.OpenToken("acme", IntegrationSealKind("chirp-2"), sealed) },
		"another tenant":      func() (string, error) { return tr.OpenToken("globex", IntegrationSealKind("chirp-1"), sealed) },
		"a device access token": func() (string, error) {
			return tr.OpenToken("acme", "ACCESS_TOKEN", sealed)
		},
	} {
		_, err := open()
		require.ErrorIs(t, err, ErrSealedSecretInvalid, name)
	}

	// And the other direction: a device's sealed access token does not open as an integration's.
	device, err := tr.SealToken("acme", "ACCESS_TOKEN", "bearer-token-value")
	require.NoError(t, err)
	_, err = tr.OpenToken("acme", IntegrationSealKind("chirp-1"), device)
	require.ErrorIs(t, err, ErrSealedSecretInvalid)
}
