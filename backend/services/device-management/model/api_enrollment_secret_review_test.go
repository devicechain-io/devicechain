// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func betaCtx() context.Context { return core.WithTenant(context.Background(), "beta") }

// 🔴 AN OLD-VERSION POD'S RE-OPEN MUST NOT LET THE SUPERSEDED SECRET REDEEM.
//
// During a rolling upgrade an old pod re-opens a claim by writing the new secret into
// claim_secret and leaving claim_secret_digest, the digest of the PREVIOUS secret, in place.
// ClaimDevice on a new pod must treat the plaintext as the later write.
func TestAnOldPodReopenDoesNotLetTheSupersededSecretRedeem(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "first-s3cret"})
	require.NoError(t, err)
	// What the old pod's Save of a re-opened claim does: new plaintext, status OPEN, the old
	// digest untouched.
	require.NoError(t, api.RDB.DB(ctx).Exec(
		`UPDATE device_claims SET claim_secret = ?, status = ?`, "second-s3cret", string(ClaimStatusOpen)).Error)

	assert.ErrorIs(t, redeem(api, "first-s3cret"), ErrClaimSecretMismatch, "the superseded secret redeemed")
	claim, err := api.DeviceClaimByDeviceToken(ctx, "dozer-01")
	require.NoError(t, err)
	assert.Equal(t, string(ClaimStatusOpen), claim.Status, "a refused redemption consumed the claim")

	require.NoError(t, redeem(api, "second-s3cret"))

	// Redeeming converted the row: the plaintext is gone and the digest is the second secret's.
	row := claimRow(t, api)
	requireNoSecretIn(t, "device_claims", row, "second-s3cret")
	assert.Nil(t, row["claim_secret"])
	digest, _ := row["claim_secret_digest"].(string)
	assert.NoError(t, credential.VerifyDeviceSecret(api.EnrollmentKeys.Claim, "acme", digest, "second-s3cret"))
	assert.Error(t, credential.VerifyDeviceSecret(api.EnrollmentKeys.Claim, "acme", digest, "first-s3cret"))
}

// A claim an old pod OPENED after the last start has a plaintext and no digest. It redeems on
// a new pod without waiting for a startup step, and redeeming leaves no plaintext behind.
func TestAClaimAnOldPodOpenedRedeemsWithoutAStartupStepAndIsConvertedByIt(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "temp"})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Exec(
		`UPDATE device_claims SET claim_secret = ?, claim_secret_digest = NULL`, "old-pod-opened").Error)

	assert.ErrorIs(t, redeem(api, "temp"), ErrClaimSecretMismatch)
	require.NoError(t, redeem(api, "old-pod-opened"))

	row := claimRow(t, api)
	requireNoSecretIn(t, "device_claims", row, "old-pod-opened")
	assert.Nil(t, row["claim_secret"])
	assert.NotNil(t, row["claim_secret_digest"])
}

// A blank plaintext an old pod saved back is not a secret and does not displace the digest.
func TestABlankPlaintextAnOldPodSavedDoesNotDisplaceTheClaimsDigest(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "kept-s3cret"})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Exec(`UPDATE device_claims SET claim_secret = ''`).Error)
	assert.ErrorIs(t, redeem(api, ""), ErrClaimSecretMismatch)
	require.NoError(t, redeem(api, "kept-s3cret"))
}

// 🔑 THE DIGEST IS BOUND TO THE ROW'S TENANT, AT EVERY CALL SITE.
//
// The primitive binds the tenant, but each call site chooses what to hand it, and a fixed or
// wrong tenant there would still verify in the one tenant the test happened to use.
func TestClaimAndProfileDigestsAreBoundToTheirOwnTenant(t *testing.T) {
	api, _ := seedClaimable(t)
	acme, beta := hierarchyCtx(), betaCtx()
	seedClaimableIn(t, api, beta)
	const secret = "the-same-printed-s3cret"
	for _, ctx := range []context.Context{acme, beta} {
		_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: secret})
		require.NoError(t, err)
	}
	// Each digest verifies in its own tenant, and in no other.
	digests := map[string]string{}
	for tenant, ctx := range map[string]context.Context{"acme": acme, "beta": beta} {
		row := rawRow(t, api.RDB.DB(ctx), `SELECT claim_secret_digest FROM device_claims WHERE tenant_id = ?`, tenant)
		digests[tenant], _ = row["claim_secret_digest"].(string)
		require.NoError(t, credential.VerifyDeviceSecret(api.EnrollmentKeys.Claim, tenant, digests[tenant], secret), tenant)
	}
	assert.Error(t, credential.VerifyDeviceSecret(api.EnrollmentKeys.Claim, "acme", digests["beta"], secret),
		"beta's claim digest verifies as acme's")
	assert.Error(t, credential.VerifyDeviceSecret(api.EnrollmentKeys.Claim, "beta", digests["acme"], secret),
		"acme's claim digest verifies as beta's")

	// Beta's digest copied into acme's row redeems nothing there; beta's own claim still redeems.
	require.NoError(t, api.RDB.DB(acme).Exec(
		`UPDATE device_claims SET claim_secret_digest = ? WHERE tenant_id = 'acme'`, digests["beta"]).Error)
	assert.ErrorIs(t, redeemIn(api, acme, secret), ErrClaimSecretMismatch, "a digest copied across tenants redeemed")
	require.NoError(t, redeemIn(api, beta, secret))

	// Profiles: the same secret in two tenants, each digest verifying only in its own.
	pa, _ := newProfileApi(t)
	for _, tenant := range []string{"acme", "beta"} {
		ctx := core.WithTenant(context.Background(), tenant)
		if tenant == "beta" {
			_, err := pa.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"})
			require.NoError(t, err)
		}
		_, err := pa.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
			Token: "pp-1", ProvisionKey: "key-" + tenant, ProvisionSecret: secret,
			Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
		require.NoError(t, err)
		row := rawRow(t, pa.RDB.DB(ctx), `SELECT provision_secret_digest FROM provisioning_profiles WHERE tenant_id = ?`, tenant)
		d, _ := row["provision_secret_digest"].(string)
		other := map[string]string{"acme": "beta", "beta": "acme"}[tenant]
		assert.NoError(t, credential.VerifyDeviceSecret(pa.EnrollmentKeys.Provisioning, tenant, d, secret), tenant)
		assert.Error(t, credential.VerifyDeviceSecret(pa.EnrollmentKeys.Provisioning, other, d, secret),
			"%s's profile digest verifies as %s's", tenant, other)
	}
}

// 🔑 EACH PURPOSE HAS ITS OWN KEY: a digest copied between tables, or from a credential row,
// in the SAME tenant verifies nothing.
func TestADigestCopiedBetweenPurposesVerifiesNothing(t *testing.T) {
	keys := testEnrollmentKeys
	assert.NotEqual(t, keys.Provisioning.KeyId(), keys.Claim.KeyId())
	assert.NotEqual(t, keys.Provisioning.KeyId(), testSecretKey.KeyId())
	assert.NotEqual(t, keys.Claim.KeyId(), testSecretKey.KeyId())

	// At the call site: a profile's digest copied into a claim redeems nothing.
	api, _ := seedClaimable(t)
	pa, _ := newProfileApi(t)
	ctx := hierarchyCtx()
	const secret = "shared-by-mistake-s3cret"
	_, err := pa.CreateProvisioningProfile(partialUpdateCtx(), &ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", ProvisionSecret: secret,
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
	require.NoError(t, err)
	profile := rawRow(t, pa.RDB.DB(partialUpdateCtx()), `SELECT provision_secret_digest FROM provisioning_profiles`)
	profileDigest, _ := profile["provision_secret_digest"].(string)

	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: secret})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Exec(`UPDATE device_claims SET claim_secret_digest = ?`, profileDigest).Error)
	assert.ErrorIs(t, redeem(api, secret), ErrClaimSecretMismatch, "a profile digest redeemed a claim")

	// And the reverse, and a device credential's, against the profile key.
	claimDigest, err := keys.Claim.Digest("acme", secret)
	require.NoError(t, err)
	credDigest, err := testSecretKey.Digest("acme", secret)
	require.NoError(t, err)
	assert.Error(t, credential.VerifyDeviceSecret(keys.Provisioning, "acme", claimDigest, secret))
	assert.Error(t, credential.VerifyDeviceSecret(keys.Provisioning, "acme", credDigest, secret))
	assert.Error(t, credential.VerifyDeviceSecret(keys.Claim, "acme", credDigest, secret))
}

// The write limit is 256 BYTES, inclusive: the column's old width.
func TestTheEnrollmentSecretLimitIs256BytesInclusive(t *testing.T) {
	exact, over := strings.Repeat("x", 256), strings.Repeat("x", 257)
	require.Equal(t, 256, credential.MaxEnrollmentSecretBytes)

	api, token := newProfileApi(t)
	ctx := partialUpdateCtx()
	create := func(tok, secret string) error {
		_, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
			Token: tok, ProvisionKey: "key-" + tok, ProvisionSecret: secret,
			Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
		return err
	}
	assert.NoError(t, create(token, exact), "exactly 256 bytes was refused")
	assert.Error(t, create("pp-2", over), "257 bytes was accepted")

	capi, _ := seedClaimable(t)
	cctx := hierarchyCtx()
	initiate := func(secret string) error {
		_, err := capi.InitiateDeviceClaim(cctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: secret})
		return err
	}
	assert.Error(t, initiate(over), "257 bytes was accepted")
	assert.NoError(t, initiate(exact), "exactly 256 bytes was refused")
	require.NoError(t, redeem(capi, exact))
}

// 🔴 THE REDEMPTION IS WRITTEN AGAINST THE SECRET IT WAS VERIFIED AGAINST.
//
// ClaimDevice verifies, then writes. A re-open that lands BETWEEN the two (another pod's, or an
// old pod's) must not let the superseded secret spend: the write's compare-and-set holds both
// secret columns to what was verified. The re-open is injected from a callback that fires
// just before the redemption's UPDATE, which is the only way to interleave deterministically.
func TestAReopenBetweenVerificationAndRedemptionSpendsNothing(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "first-s3cret"})
	require.NoError(t, err)

	db := api.RDB.Database
	fired := false
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:reopen-before-redeem", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "device_claims" {
			return
		}
		fired = true
		// An old pod's re-open, committed before this write. It runs on the redemption's own
		// transaction: the database under test is one SQLite connection, and a second would
		// wait on the first for ever.
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Exec(
			`UPDATE device_claims SET claim_secret = ?, status = ?`, "second-s3cret", string(ClaimStatusOpen)).Error)
	}))

	err = redeem(api, "first-s3cret")
	require.True(t, fired, "the interleaving never happened")
	assert.ErrorIs(t, err, ErrClaimNotOpen, "the superseded secret spent a redemption after a re-open")
	edges := int64(0)
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).Count(&edges).Error)
	assert.Zero(t, edges, "an ownership edge was written")

	// The injected re-open ran inside the refused redemption's transaction and was rolled back
	// with it; the row still holds the first secret, which now (correctly) redeems.
	require.NoError(t, redeem(api, "first-s3cret"))
}
