// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/stretchr/testify/require"
)

// The two self-registration kinds are Unthrottled, and the checker is built with the keys
// they compare under.

var (
	provisioningProfile = credential.Principal{Kind: credential.KindProvisioningProfile, ID: "acme:factory-a", Tenant: "acme"}
	provisioned         = credential.Principal{Kind: credential.KindProvisionedCredential, ID: "acme:dev-1", Tenant: "acme"}
	unthrottled         = credential.Policy{Unthrottled: true}
)

// testProvisioningKey is a key distinct from the device key. The enrollment key derivation
// lives with the model that stores the digests; the Checker only needs a *DeviceSecretKey.
func testProvisioningKey(t testing.TB) *credential.DeviceSecretKey {
	t.Helper()
	k, err := credential.DeriveServiceAccountSecretKey(testRootKey)
	require.NoError(t, err)
	return k
}

func newProvisioningChecker(t *testing.T, store credential.Store) *credential.Checker {
	t.Helper()
	c, err := credential.NewChecker(store, map[credential.Kind]credential.Policy{
		credential.KindProvisioningProfile:   unthrottled,
		credential.KindProvisionedCredential: unthrottled,
		credential.KindDeviceCredential:      testPolicy,
	}, credential.WithClock(newClock().Now),
		credential.WithDeviceSecretKey(testDeviceKey(t)),
		credential.WithProvisioningSecretKey(testProvisioningKey(t)))
	require.NoError(t, err)
	return c
}

func provisioningDigest(t testing.TB, secret string) func(context.Context) (string, error) {
	t.Helper()
	d, err := testProvisioningKey(t).Digest("acme", secret)
	require.NoError(t, err)
	return plaintext(d)
}

func tokenDigest(t testing.TB, tenant, token string) func(context.Context) (string, error) {
	t.Helper()
	d, err := testDeviceKey(t).AccessTokenDigest(tenant, token)
	require.NoError(t, err)
	return plaintext(d)
}

// A provisioning kind without its key is refused at construction.
func TestProvisioningKindWithoutItsKeyIsRefused(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindProvisioningProfile: unthrottled},
		credential.WithDeviceSecretKey(testDeviceKey(t)))
	require.ErrorIs(t, err, credential.ErrNoProvisioningSecretKey)

	// The replay kind compares under the DEVICE key, so that is the key it needs.
	_, err = credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindProvisionedCredential: unthrottled},
		credential.WithProvisioningSecretKey(testProvisioningKey(t)))
	require.ErrorIs(t, err, credential.ErrNoDeviceSecretKey)
}

// Neither kind may be throttled: a per-key backoff on a secret nobody can guess only hands a
// stranger a lockout, and a replay mismatch must write no attempt record.
func TestProvisioningKindsRefuseAThrottledPolicy(t *testing.T) {
	for _, k := range []credential.Kind{credential.KindProvisioningProfile, credential.KindProvisionedCredential} {
		_, err := credential.NewChecker(credentialtest.NewStore(),
			map[credential.Kind]credential.Policy{k: testPolicy},
			credential.WithDeviceSecretKey(testDeviceKey(t)),
			credential.WithProvisioningSecretKey(testProvisioningKey(t)))
		require.ErrorIs(t, err, credential.ErrMustBeUnthrottled, "kind %s", k)
	}
}

func TestProvisioningProfileComparesItsDigest(t *testing.T) {
	store := credentialtest.NewStore()
	c := newProvisioningChecker(t, store)
	require.NoError(t, c.Check(context.Background(), provisioningProfile, "s3cret", provisioningDigest(t, "s3cret")))
	for _, wrong := range []string{"s3creT", "s3cret-extra", "", " s3cret"} {
		require.ErrorIs(t, c.Check(context.Background(), provisioningProfile, wrong, provisioningDigest(t, "s3cret")),
			credential.ErrMismatch, "presented %q", wrong)
	}
	// A different tenant's principal never verifies the digest: it is tenant-bound.
	other := provisioningProfile
	other.Tenant = "globex"
	require.ErrorIs(t, c.Check(context.Background(), other, "s3cret", provisioningDigest(t, "s3cret")), credential.ErrMismatch)
	require.Empty(t, store.Ops(), "an Unthrottled kind reads, charges and deletes nothing")
}

// An unknown key pays the dummy compare, a digest under the provisioning key.
func TestProvisioningUnknownKeyPaysTheDummyCompare(t *testing.T) {
	c := newProvisioningChecker(t, credentialtest.NewStore())
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })
	require.ErrorIs(t, c.Check(context.Background(), provisioningProfile, "anything", plaintext("")), credential.ErrMismatch)
	require.Len(t, seen, 1)
	require.Equal(t, credential.Dummy(c, credential.KindProvisioningProfile), seen[0])
	require.NoError(t, testProvisioningKey(t).Recognizes(string(seen[0])), "the dummy has the shape of a stored provisioning digest")
	require.Error(t, testDeviceKey(t).Recognizes(string(seen[0])), "and is not a device-key digest")
}

// Key separation, both ways: a provisioning digest under the device kind fails, and a device
// digest under the provisioning kind fails.
func TestProvisioningAndDeviceDigestsDoNotCrossVerify(t *testing.T) {
	c := newProvisioningChecker(t, credentialtest.NewStore())
	ctx := context.Background()
	devicePrincipal := credential.Principal{Kind: credential.KindDeviceCredential, ID: "acme:dev-1", Tenant: "acme"}

	require.ErrorIs(t, c.Check(ctx, devicePrincipal, "s3cret", provisioningDigest(t, "s3cret")), credential.ErrMismatch,
		"a provisioning digest authenticated a device credential")
	require.ErrorIs(t, c.Check(ctx, provisioningProfile, "s3cret", digestOf(t, "s3cret")), credential.ErrMismatch,
		"a device digest authenticated a provisioning profile")
	// Controls: each verifies under its own kind.
	require.NoError(t, c.Check(ctx, devicePrincipal, "s3cret", digestOf(t, "s3cret")))
	require.NoError(t, c.Check(ctx, provisioningProfile, "s3cret", provisioningDigest(t, "s3cret")))
}

func TestProvisionedCredentialComparesTheAccessTokenDigest(t *testing.T) {
	c := newProvisioningChecker(t, credentialtest.NewStore())
	ctx := context.Background()
	require.NoError(t, c.Check(ctx, provisioned, "tok-abc", tokenDigest(t, "acme", "tok-abc")))
	require.ErrorIs(t, c.Check(ctx, provisioned, "tok-abd", tokenDigest(t, "acme", "tok-abc")), credential.ErrMismatch)
	require.ErrorIs(t, c.Check(ctx, provisioned, "", tokenDigest(t, "acme", "tok-abc")), credential.ErrMismatch)
	// A salted device-secret digest is not an access token digest.
	require.ErrorIs(t, c.Check(ctx, provisioned, "tok-abc", digestOf(t, "tok-abc")), credential.ErrMismatch)
	// Another tenant's principal does not verify this tenant's digest.
	other := provisioned
	other.Tenant = "globex"
	require.ErrorIs(t, c.Check(ctx, other, "tok-abc", tokenDigest(t, "acme", "tok-abc")), credential.ErrMismatch)
	// An unknown device pays the dummy compare.
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })
	require.ErrorIs(t, c.Check(ctx, provisioned, "tok-abc", plaintext("")), credential.ErrMismatch)
	require.Len(t, seen, 1)
	require.Equal(t, credential.Dummy(c, credential.KindProvisionedCredential), seen[0])
	require.NoError(t, testDeviceKey(t).RecognizesAccessToken(string(seen[0])))
}

// A KindProvisionedCredential mismatch writes NO attempt record, and charges nothing to
// KindDeviceCredential, whose username a device presents to the broker.
func TestProvisionedCredentialMismatchChargesNothing(t *testing.T) {
	store := credentialtest.NewStore()
	c := newProvisioningChecker(t, store)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		require.ErrorIs(t, c.Check(ctx, provisioned, "wrong", tokenDigest(t, "acme", "tok-abc")), credential.ErrMismatch)
	}
	require.Empty(t, store.Ops(), "a replay mismatch wrote or read an attempt record")

	// The same identifier under the device kind starts from a clean slate, in a record of
	// its own.
	devicePrincipal := credential.Principal{Kind: credential.KindDeviceCredential, ID: provisioned.ID, Tenant: "acme"}
	require.NoError(t, c.Check(ctx, devicePrincipal, "s3cret", digestOf(t, "s3cret")))
	require.NotEqual(t, credential.Key(devicePrincipal), credential.Key(provisioned),
		"the two kinds share an attempt key")
}

// The two kinds, declared next to KindDeviceCredential, leave its backoff exactly as it was.
func TestProvisioningKindsDoNotDisturbTheDeviceBackoff(t *testing.T) {
	c := newProvisioningChecker(t, credentialtest.NewStore())
	ctx := context.Background()
	devicePrincipal := credential.Principal{Kind: credential.KindDeviceCredential, ID: "acme:dev-9", Tenant: "acme"}
	for i := 0; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, devicePrincipal, "bad", digestOf(t, "s3cret")), credential.ErrMismatch)
	}
	var th *credential.ThrottledError
	require.ErrorAs(t, c.Check(ctx, devicePrincipal, "s3cret", digestOf(t, "s3cret")), &th,
		"the device kind still throttles after its free attempts")
}
