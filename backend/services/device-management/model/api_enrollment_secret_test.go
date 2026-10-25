// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 THE SECRETS A DEVICE IS ENROLLED WITH ARE NEVER STORED, ONLY THEIR KEYED DIGESTS.
//
// A provisioning profile's shared secret and a device claim's one-time secret used to sit in
// their rows as sent. These tests read the TABLE, not the model: a model that hid the
// plaintext from its readers while a column still held it would pass every model-level
// assertion.

// rawRow reads every column of the one row of table where token (or device id) matches, as
// text, so a test can ask whether any of them contains a secret.
func rawRow(t *testing.T, db *gorm.DB, query string, args ...any) map[string]any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, db.Raw(query, args...).Scan(&rows).Error)
	require.Len(t, rows, 1)
	return rows[0]
}

// requireNoSecretIn fails when any column of row holds secret, and returns the row's
// rendered columns for a message.
func requireNoSecretIn(t *testing.T, what string, row map[string]any, secret string) {
	t.Helper()
	for col, v := range row {
		assert.NotContains(t, fmt.Sprint(v), secret, "%s.%s holds the plaintext secret", what, col)
	}
}

func newProfileApi(t *testing.T) (*Api, string) {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{})...)
	_, err := api.CreateDeviceType(partialUpdateCtx(), &DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	return api, "pp-1"
}

func TestAProvisioningSecretIsStoredAsADigestNeverThePlaintext(t *testing.T) {
	api, token := newProfileApi(t)
	ctx := partialUpdateCtx()
	created, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "fleet-key",
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
	require.NoError(t, err)
	// The server generated it: 256 bits as base64url, 43 characters, and it is on the row
	// handed back and nowhere else.
	secret := created.IssuedSecret
	require.Len(t, secret, 43, "the generated secret is 32 bytes of base64url")

	row := rawRow(t, api.RDB.DB(ctx), `SELECT * FROM provisioning_profiles WHERE token = ?`, token)
	requireNoSecretIn(t, "provisioning_profiles", row, secret)
	assert.Nil(t, row["provision_secret"], "the retired plaintext column is written NULL")
	digest, _ := row["provision_secret_digest"].(string)
	assert.True(t, strings.HasPrefix(digest, "v1$"+testEnrollmentKeys.Provisioning.KeyId()+"$"), "digest %q", digest)

	// What a read of the profile returns carries no secret at all.
	rows0, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	require.Empty(t, requireOne(t, "profile", rows0, err).IssuedSecret, "a read returned the issued secret")

	// A rotation replaces the digest, and the old secret verifies nothing afterwards.
	rotated, err := api.RotateProvisioningProfileSecret(ctx, token)
	require.NoError(t, err)
	require.NotEqual(t, secret, rotated.IssuedSecret)
	row = rawRow(t, api.RDB.DB(ctx), `SELECT * FROM provisioning_profiles WHERE token = ?`, token)
	requireNoSecretIn(t, "provisioning_profiles", row, rotated.IssuedSecret)
	requireNoSecretIn(t, "provisioning_profiles", row, secret)
	rows, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	p := requireOne(t, "profile", rows, err)
	assert.Equal(t, rotated.IssuedSecret, provisionSecretStr(p.ProvisionSecretDigest, secret, rotated.IssuedSecret))
}

// An update that does not name the secret keeps the stored digest byte for byte: the edit
// that changed only the NAME must not rotate, blank or re-salt the secret a fleet presents.
func TestAnUpdateThatOmitsTheProvisioningSecretKeepsItsDigest(t *testing.T) {
	api, token := newProfileApi(t)
	ctx := partialUpdateCtx()
	created, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "fleet-key",
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
	require.NoError(t, err)
	before := rawRow(t, api.RDB.DB(ctx), `SELECT provision_secret_digest FROM provisioning_profiles WHERE token = ?`, token)

	_, err = api.UpdateProvisioningProfile(ctx, token, &ProvisioningProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)

	after := rawRow(t, api.RDB.DB(ctx), `SELECT provision_secret_digest FROM provisioning_profiles WHERE token = ?`, token)
	assert.Equal(t, before["provision_secret_digest"], after["provision_secret_digest"],
		"a name-only update changed the stored digest")
	rows, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	p := requireOne(t, "profile", rows, err)
	assert.Equal(t, created.IssuedSecret, provisionSecretStr(p.ProvisionSecretDigest, created.IssuedSecret))
}

// During a rolling upgrade an old-version pod rotates the secret into the plaintext column
// and leaves the digest alone. An update from a new pod that does not name the secret must
// carry that rotation into the digest, not discard it (and so revive the rotated-away one).
func TestAnUpdateAdoptsASecretAnOldPodRotatedIntoThePlaintextColumn(t *testing.T) {
	api, token := newProfileApi(t)
	ctx := partialUpdateCtx()
	created, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "fleet-key",
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
	require.NoError(t, err)
	require.True(t, created.ProvisionSecretIssued)
	require.NoError(t, api.RDB.DB(ctx).Exec(
		`UPDATE provisioning_profiles SET provision_secret = ? WHERE token = ?`, "old-pod-rotated", token).Error)

	_, err = api.UpdateProvisioningProfile(ctx, token, &ProvisioningProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)

	rows, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	p := requireOne(t, "profile", rows, err)
	assert.Equal(t, "old-pod-rotated",
		provisionSecretStr(p.ProvisionSecretDigest, created.IssuedSecret, "old-pod-rotated"))
	// A secret a person chose is not one the server generated: the endpoint refuses the profile
	// until it is rotated.
	assert.False(t, p.ProvisionSecretIssued, "an adopted plaintext secret left the profile marked as server-issued")
	row := rawRow(t, api.RDB.DB(ctx), `SELECT * FROM provisioning_profiles WHERE token = ?`, token)
	assert.Nil(t, row["provision_secret"], "the save left the plaintext in place")
}

// No key, no secret: a create or a rotation that has to digest one is refused and nothing falls
// back to storing it.
func TestAProvisioningSecretIsRefusedWithoutAKey(t *testing.T) {
	api, token := newProfileApi(t)
	ctx := partialUpdateCtx()
	create := func() error {
		_, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
			Token: token, ProvisionKey: "fleet-key",
			Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
		return err
	}
	require.NoError(t, create())
	keys := api.EnrollmentKeys
	api.EnrollmentKeys = nil
	_, err := api.RotateProvisioningProfileSecret(ctx, token)
	assert.ErrorIs(t, err, errNoProvisioningSecretKey)
	require.NoError(t, api.RDB.DB(ctx).Exec(`DELETE FROM provisioning_profiles`).Error)
	assert.ErrorIs(t, create(), errNoProvisioningSecretKey)
	rows, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	require.NoError(t, err)
	assert.Empty(t, rows, "a refused create still wrote a row")
	api.EnrollmentKeys = keys
}

// ─── Device claims ──────────────────────────────────────────────────────────

// hierarchyCtx is the tenant context hierarchyTestApi's database is seeded under.
func hierarchyCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

func seedClaimable(t *testing.T) (*Api, string) {
	t.Helper()
	api, ctx := hierarchyTestApi(t)
	seedClaimableIn(t, api, ctx)
	return api, "dozer-01"
}

// seedClaimableIn gives ctx's tenant a device "dozer-01" and a customer "acme-mining".
func seedClaimableIn(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")
	customerType := &CustomerType{}
	customerType.Token = "operator"
	require.NoError(t, api.RDB.DB(ctx).Create(customerType).Error)
	_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: "acme-mining", CustomerTypeToken: "operator"})
	require.NoError(t, err)
}

func redeem(api *Api, secret string) error { return redeemIn(api, hierarchyCtx(), secret) }

func redeemIn(api *Api, ctx context.Context, secret string) error {
	_, err := api.ClaimDevice(ctx, &DeviceClaimRequest{DeviceToken: "dozer-01", ClaimSecret: secret,
		CustomerToken: "acme-mining"}, time.Now())
	return err
}

func claimRow(t *testing.T, api *Api) map[string]any {
	t.Helper()
	return rawRow(t, api.RDB.DB(hierarchyCtx()), `SELECT * FROM device_claims`)
}

// The caller-level proof: the secret goes in through InitiateDeviceClaim, comes out only as
// a digest, and ClaimDevice (the entry point an owner redeems through) honours it.
func TestAClaimSecretIsStoredAsADigestAndRedeemsOnlyWithTheRightSecret(t *testing.T) {
	const secret = "plaintext-claim-s3cret"
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: secret})
	require.NoError(t, err)

	row := claimRow(t, api)
	requireNoSecretIn(t, "device_claims", row, secret)
	assert.Nil(t, row["claim_secret"], "the retired plaintext column is written NULL")
	digest, _ := row["claim_secret_digest"].(string)
	assert.True(t, strings.HasPrefix(digest, "v1$"+testEnrollmentKeys.Claim.KeyId()+"$"), "digest %q", digest)

	// Negative controls, each one input away from the redemption below.
	for _, wrong := range []string{"wrong", secret + " ", strings.ToUpper(secret), ""} {
		err := redeem(api, wrong)
		assert.ErrorIs(t, err, ErrClaimSecretMismatch, "presented %q", wrong)
	}
	claim, err := api.DeviceClaimByDeviceToken(ctx, "dozer-01")
	require.NoError(t, err)
	assert.Equal(t, string(ClaimStatusOpen), claim.Status, "a wrong secret consumed the claim")

	require.NoError(t, redeem(api, secret))
	claim, err = api.DeviceClaimByDeviceToken(ctx, "dozer-01")
	require.NoError(t, err)
	assert.Equal(t, string(ClaimStatusClaimed), claim.Status)
	// One-time: the same secret does not redeem twice.
	assert.ErrorIs(t, redeem(api, secret), ErrClaimNotOpen)
}

// Re-opening a claimed device with a new secret (a resale) replaces the digest: the old
// secret opens nothing and the new one does, and the table still never holds either.
func TestReopeningAClaimWithANewSecretReplacesTheDigest(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "first-s3cret"})
	require.NoError(t, err)
	require.NoError(t, redeem(api, "first-s3cret"))

	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "second-s3cret"})
	require.NoError(t, err)
	row := claimRow(t, api)
	requireNoSecretIn(t, "device_claims", row, "first-s3cret")
	requireNoSecretIn(t, "device_claims", row, "second-s3cret")

	assert.ErrorIs(t, redeem(api, "first-s3cret"), ErrClaimSecretMismatch, "the superseded secret still redeems")
	require.NoError(t, redeem(api, "second-s3cret"))
}

// No key, no secret, and the old width limit still holds.
func TestAClaimSecretIsRefusedWithoutAKeyOrOverTheLimit(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	initiate := func(secret string) error {
		_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: secret})
		return err
	}
	assert.Error(t, initiate(strings.Repeat("x", 257)))
	api.EnrollmentKeys = nil
	assert.ErrorIs(t, initiate("s3cret"), errNoClaimSecretKey)
	_, err := api.DeviceClaimByDeviceToken(ctx, "dozer-01")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "a refused initiate still wrote a claim")
}

// 🔑 AN UNREDEEMED CLAIM SURVIVES THE UPGRADE. A claim opened before secrets were digested
// holds its plaintext in claim_secret and no digest. The startup step digests it in place,
// and the owner then redeems it with the secret they were given. (A claim an old pod opens
// AFTER the last start is redeemable without the step: see
// TestAClaimAnOldPodOpenedRedeemsWithoutAStartupStepAndIsConvertedByIt.)
func TestAnOpenClaimFromBeforeTheUpgradeStillRedeemsOnceTheStartupStepHasRun(t *testing.T) {
	api, _ := seedClaimable(t)
	ctx := hierarchyCtx()
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "temp"})
	require.NoError(t, err)
	// The row as the old code left it.
	require.NoError(t, api.RDB.DB(ctx).Exec(
		`UPDATE device_claims SET claim_secret = ?, claim_secret_digest = NULL`, "legacy-printed-s3cret").Error)
	n, err := schema.DigestPlaintextClaimSecrets(ctx, api.RDB.DB(ctx), api.EnrollmentKeys.Claim.Digest)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	row := claimRow(t, api)
	requireNoSecretIn(t, "device_claims", row, "legacy-printed-s3cret")
	assert.Nil(t, row["claim_secret"])
	require.NoError(t, redeem(api, "legacy-printed-s3cret"))

	// Idempotent: a second start finds nothing.
	n, err = schema.DigestPlaintextClaimSecrets(ctx, api.RDB.DB(ctx), api.EnrollmentKeys.Claim.Digest)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
