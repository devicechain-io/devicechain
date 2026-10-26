// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/stretchr/testify/require"
)

// KindIntegration is the inbound integration's bearer token: Unthrottled, under a key of its
// own, bound to the tenant.

var integrationPrincipal = credential.Principal{Kind: credential.KindIntegration, ID: "acme:chirp-1", Tenant: "acme"}

func testIntegrationKey(t testing.TB) *credential.DeviceSecretKey {
	t.Helper()
	keys, err := credential.DeriveEnrollmentSecretKeys(testRootKey)
	require.NoError(t, err)
	return keys.Integration
}

func newIntegrationChecker(t *testing.T, store credential.Store) *credential.Checker {
	t.Helper()
	c, err := credential.NewChecker(store, map[credential.Kind]credential.Policy{
		credential.KindIntegration: {Unthrottled: true},
	}, credential.WithIntegrationSecretKey(testIntegrationKey(t)))
	require.NoError(t, err)
	return c
}

func integrationDigest(t testing.TB, tenant, secret string) func(context.Context) (string, error) {
	t.Helper()
	d, err := testIntegrationKey(t).Digest(tenant, secret)
	require.NoError(t, err)
	return plaintext(d)
}

func TestIntegrationKindWithoutItsKeyIsRefused(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIntegration: {Unthrottled: true}},
		credential.WithDeviceSecretKey(testDeviceKey(t)), credential.WithProvisioningSecretKey(testProvisioningKey(t)))
	require.ErrorIs(t, err, credential.ErrNoIntegrationSecretKey)
}

func TestIntegrationKindRefusesAThrottledPolicy(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIntegration: testPolicy},
		credential.WithIntegrationSecretKey(testIntegrationKey(t)))
	require.ErrorIs(t, err, credential.ErrMustBeUnthrottled)
}

func TestIntegrationTokenComparesItsDigest(t *testing.T) {
	store := credentialtest.NewStore()
	c := newIntegrationChecker(t, store)
	ctx := context.Background()
	require.NoError(t, c.Check(ctx, integrationPrincipal, "tok", integrationDigest(t, "acme", "tok")))
	for _, wrong := range []string{"toK", "tok-extra", "", " tok"} {
		require.ErrorIs(t, c.Check(ctx, integrationPrincipal, wrong, integrationDigest(t, "acme", "tok")),
			credential.ErrMismatch, "presented %q", wrong)
	}
	// Bound to the tenant: another tenant's principal never verifies this digest.
	other := integrationPrincipal
	other.Tenant = "globex"
	require.ErrorIs(t, c.Check(ctx, other, "tok", integrationDigest(t, "acme", "tok")), credential.ErrMismatch)
	require.Empty(t, store.Ops(), "an Unthrottled kind reads, charges and deletes nothing")
}

// An unknown integration pays the dummy compare, so the answer and the work do not say
// whether the integration exists.
func TestIntegrationUnknownIdPaysTheDummyCompare(t *testing.T) {
	c := newIntegrationChecker(t, credentialtest.NewStore())
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })
	require.ErrorIs(t, c.Check(context.Background(), integrationPrincipal, "anything", plaintext("")), credential.ErrMismatch)
	require.Len(t, seen, 1)
	require.Equal(t, credential.Dummy(c, credential.KindIntegration), seen[0])
	require.NoError(t, testIntegrationKey(t).Recognizes(string(seen[0])), "the dummy has the shape of a stored integration digest")
}

// The integration key is its own: a provisioning digest or a device digest of the same secret
// verifies nothing under KindIntegration, and the reverse.
func TestIntegrationDigestsDoNotCrossVerify(t *testing.T) {
	ctx := context.Background()
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIntegration:         {Unthrottled: true},
		credential.KindProvisioningProfile: {Unthrottled: true},
	}, credential.WithIntegrationSecretKey(testIntegrationKey(t)), credential.WithProvisioningSecretKey(testProvisioningKeyOf(t)))
	require.NoError(t, err)

	provisioning := credential.Principal{Kind: credential.KindProvisioningProfile, ID: "acme:chirp-1", Tenant: "acme"}
	pd, err := testProvisioningKeyOf(t).Digest("acme", "tok")
	require.NoError(t, err)

	require.ErrorIs(t, c.Check(ctx, integrationPrincipal, "tok", plaintext(pd)), credential.ErrMismatch,
		"a provisioning digest authenticated an integration")
	require.ErrorIs(t, c.Check(ctx, integrationPrincipal, "tok", digestOf(t, "tok")), credential.ErrMismatch,
		"a device digest authenticated an integration")
	id, err := testIntegrationKey(t).Digest("acme", "tok")
	require.NoError(t, err)
	require.ErrorIs(t, c.Check(ctx, provisioning, "tok", plaintext(id)), credential.ErrMismatch,
		"an integration digest authenticated a provisioning profile")
	// Controls: each verifies under its own kind.
	require.NoError(t, c.Check(ctx, integrationPrincipal, "tok", plaintext(id)))
	require.NoError(t, c.Check(ctx, provisioning, "tok", plaintext(pd)))
}

// The key BYTES are separate, not only the key ids: a digest made under another purpose's key,
// with its key id rewritten to the integration key's, still verifies nothing. Rewriting the id
// is the strongest forgery a holder of a database copy could try, so it is the one asserted.
func TestTheIntegrationKeyIsNotAnotherPurposesKey(t *testing.T) {
	keys, err := credential.DeriveEnrollmentSecretKeys(testRootKey)
	require.NoError(t, err)
	for name, other := range map[string]*credential.DeviceSecretKey{
		"provisioning": keys.Provisioning, "claim": keys.Claim, "device": testDeviceKey(t),
	} {
		d, err := other.Digest("acme", "tok")
		require.NoError(t, err)
		parts := strings.Split(d, "$")
		require.Len(t, parts, 4)
		parts[1] = keys.Integration.KeyId()
		require.ErrorIs(t, credential.VerifyDeviceSecret(keys.Integration, "acme", strings.Join(parts, "$"), "tok"),
			credential.ErrMismatch, "a %s digest with the integration key id verified under the integration key", name)
	}
	// The control: a digest the integration key made verifies.
	own, err := keys.Integration.Digest("acme", "tok")
	require.NoError(t, err)
	require.NoError(t, credential.VerifyDeviceSecret(keys.Integration, "acme", own, "tok"))
}

// testProvisioningKeyOf is the REAL derived provisioning key, which the integration key must
// differ from (testProvisioningKey in this package is a stand-in from the service account
// derivation).
func testProvisioningKeyOf(t testing.TB) *credential.DeviceSecretKey {
	t.Helper()
	keys, err := credential.DeriveEnrollmentSecretKeys(testRootKey)
	require.NoError(t, err)
	return keys.Provisioning
}
