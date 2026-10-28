// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withAnchor(token string) func(*ProvisioningProfileCreateRequest) {
	return func(r *ProvisioningProfileCreateRequest) { r.TrustAnchorToken = &token }
}

func TestCertificateProfileHasNoSecret(t *testing.T) {
	f := newProvFixture(t)
	a := f.anchor("maker", newRootCA(t, "Maker"))
	p := f.profile("pp-x", ProvisionAllowNew, withAnchor("maker"))

	require.NotNil(t, p.TrustAnchorId)
	assert.Equal(t, a.ID, *p.TrustAnchorId)
	assert.Equal(t, string(CredentialX509Certificate), p.CredentialType)
	assert.Empty(t, p.IssuedSecret, "no secret is shown for a profile that has none")
	assert.False(t, p.ProvisionSecretIssued)
	assert.False(t, p.ProvisionSecretDigest.Valid, "no secret is stored")

	// The control: a secret profile beside it has all three.
	s := f.profile("pp-s", ProvisionAllowNew)
	assert.Nil(t, s.TrustAnchorId)
	assert.NotEmpty(t, s.IssuedSecret)
	assert.True(t, s.ProvisionSecretIssued)
	assert.True(t, s.ProvisionSecretDigest.Valid)

	// The secret endpoint authenticates a profile by its key and secret. A certificate profile
	// answers to nothing there, whatever is sent.
	for _, secret := range []string{"", s.IssuedSecret, f.secrets["pp-s"], "anything-at-all-0123456789"} {
		_, err := f.api.AuthenticateProvisioningProfile(f.ctx, "key-pp-x", secret)
		require.Error(t, err, "a certificate profile authenticated with secret %q", secret)
	}
	_, err := f.api.AuthenticateProvisioningProfile(f.ctx, "key-pp-s", f.secrets["pp-s"])
	require.NoError(t, err, "the control profile must authenticate")

	rotated, err := f.api.RotateProvisioningProfileSecret(f.ctx, "pp-x")
	require.Nil(t, rotated)
	requireInvalid(t, err, "rotating a certificate profile")
	_, err = f.api.RotateProvisioningProfileSecret(f.ctx, "pp-s")
	require.NoError(t, err, "rotating the control profile")
}

func TestCreateCertificateProfileRefusals(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.anchor("shut", newRootCA(t, "Shut"), func(r *DeviceTrustAnchorCreateRequest) { r.Enabled = boolp(false) })
	f.profile("control", ProvisionAllowNew, withAnchor("maker"))

	req := func(token string, m func(*ProvisioningProfileCreateRequest)) error {
		r := &ProvisioningProfileCreateRequest{Token: token, ProvisionKey: "key-" + token,
			Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true, TrustAnchorToken: strp("maker")}
		m(r)
		_, err := f.api.CreateProvisioningProfile(f.ctx, r)
		return err
	}
	requireInvalid(t, req("p1", func(r *ProvisioningProfileCreateRequest) { r.CredentialType = strp("ACCESS_TOKEN") }), "a minted credential type")
	require.ErrorIs(t, req("p2", func(r *ProvisioningProfileCreateRequest) { r.TrustAnchorToken = strp("nope") }), gorm.ErrRecordNotFound)
	requireInvalid(t, req("p3", func(r *ProvisioningProfileCreateRequest) { r.TrustAnchorToken = strp("shut") }), "a disabled anchor")
	require.NoError(t, req("p4", func(r *ProvisioningProfileCreateRequest) { r.CredentialType = strp("X509_CERTIFICATE") }),
		"naming the credential type the profile has is fine")

	// The limit: eight certificate profiles per tenant (two exist), secret profiles uncounted.
	for i := 0; i < MaxX509ProfilesPerTenant-2; i++ {
		require.NoError(t, req(fmt.Sprintf("fill-%d", i), func(*ProvisioningProfileCreateRequest) {}))
	}
	var lim *limit.Error
	require.ErrorAs(t, req("over", func(*ProvisioningProfileCreateRequest) {}), &lim)
	assert.Equal(t, MaxX509ProfilesPerTenant, lim.Max)
	f.profile("secret-still-fine", ProvisionAllowNew)
}

func TestUpdateCertificateProfileAnchor(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("a", newRootCA(t, "A"))
	b := f.anchor("b", newRootCA(t, "B"))
	f.anchor("shut", newRootCA(t, "Shut"), func(r *DeviceTrustAnchorCreateRequest) { r.Enabled = boolp(false) })
	f.profile("pp-x", ProvisionAllowNew, withAnchor("a"))
	f.profile("pp-s", ProvisionAllowNew)

	set := func(v *string) *ProvisioningProfileUpdateRequest {
		var r ProvisioningProfileUpdateRequest
		r.TrustAnchorToken.Set, r.TrustAnchorToken.Value = true, v
		return &r
	}
	got, err := f.api.UpdateProvisioningProfile(f.ctx, "pp-x", set(strp("b")))
	require.NoError(t, err)
	assert.Equal(t, b.ID, *got.TrustAnchorId)

	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-x", set(nil))
	requireInvalid(t, err, "clearing the anchor")
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-x", set(strp("shut")))
	requireInvalid(t, err, "a disabled anchor")
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-x", set(strp("nope")))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-s", set(strp("a")))
	requireInvalid(t, err, "adding an anchor to a secret profile")

	still, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-s"})
	require.NoError(t, err)
	assert.Nil(t, still[0].TrustAnchorId, "a refused update changed the profile")
}

// ─── the earmark rule ──────────────────────────────────────────────────────

func TestADeviceEarmarkedForACertificateProfileHasTheDerivedToken(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.profile("pp-x", ProvisionCheckPreProvisioned, withAnchor("maker"))
	f.profile("pp-s", ProvisionCheckPreProvisioned)
	derived := core.DeriveDeviceToken("SN-0001", "pv-")
	assert.Equal(t, derived, DeriveX509DeviceToken("SN-0001"))

	create := func(token string, external *string, profile string) (*Device, error) {
		return f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: token, ExternalId: external,
			DeviceTypeToken: "dt-a", ProvisioningProfileToken: &profile})
	}

	// Omitted: the derived token is taken.
	d, err := create("", strp("SN-0001"), "pp-x")
	require.NoError(t, err)
	assert.Equal(t, derived, d.Token)
	require.NotNil(t, d.ExpectedProvisioningProfileId)

	// Stated and equal: fine.
	d, err = create(DeriveX509DeviceToken("SN-0002"), strp("SN-0002"), "pp-x")
	require.NoError(t, err)
	assert.Equal(t, DeriveX509DeviceToken("SN-0002"), d.Token)

	before := f.counts().devices
	_, err = create("my-own-token", strp("SN-0003"), "pp-x")
	require.ErrorIs(t, err, ErrInvalidEarmark, "a token other than the derived one")
	assert.Contains(t, err.Error(), DeriveX509DeviceToken("SN-0003"), "the refusal says what the token must be")
	_, err = create("", nil, "pp-x")
	require.ErrorIs(t, err, ErrInvalidEarmark, "no external id to derive from")
	_, err = create("", strp("  "), "pp-x")
	require.ErrorIs(t, err, ErrInvalidEarmark, "a blank external id")
	assert.Equal(t, before, f.counts().devices, "a refused create stored a device")

	// The control: a secret profile imposes nothing on the token.
	d, err = create("my-own-token", strp("SN-0003"), "pp-s")
	require.NoError(t, err)
	assert.Equal(t, "my-own-token", d.Token)
}

func TestBulkCreateAppliesTheCertificateEarmarkRule(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.profile("pp-x", ProvisionCheckPreProvisioned, withAnchor("maker"))
	mk := func(token, ext string) *DeviceCreateRequest {
		return &DeviceCreateRequest{Token: token, ExternalId: strp(ext), DeviceTypeToken: "dt-a", ProvisioningProfileToken: strp("pp-x")}
	}

	got, err := f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{mk("", "B-1"), mk(DeriveX509DeviceToken("B-2"), "B-2")})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, DeriveX509DeviceToken("B-1"), got[0].Token)
	assert.Equal(t, DeriveX509DeviceToken("B-2"), got[1].Token)

	before := f.counts().devices
	_, err = f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{mk("", "B-3"), mk("wrong", "B-4")})
	require.ErrorIs(t, err, ErrInvalidEarmark)
	assert.Equal(t, before, f.counts().devices, "the whole batch is refused")
}

func TestImportAppliesTheCertificateEarmarkRule(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.profile("pp-x", ProvisionCheckPreProvisioned, withAnchor("maker"))
	row := func(token, ext string) *DeviceImportRow {
		return &DeviceImportRow{Token: token, ExternalId: strp(ext), DeviceTypeToken: "dt-a", ProvisioningProfileToken: strp("pp-x")}
	}
	res, err := f.api.ImportDevices(f.ctx, []*DeviceImportRow{
		row(DeriveX509DeviceToken("I-1"), "I-1"), // right
		row("not-derived", "I-2"),                // wrong token
		row(DeriveX509DeviceToken("I-3"), "I-4"), // another device's token
	}, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, []string{DeviceImportCreated, DeviceImportInvalid, DeviceImportInvalid}, outcomes(res))
	assert.Equal(t, "INVALID_VALUE", *res.Rows[1].Code)
	assert.Contains(t, *res.Rows[1].Message, DeriveX509DeviceToken("I-2"))
}
