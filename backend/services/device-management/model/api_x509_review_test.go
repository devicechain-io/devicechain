// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func moveCredential(f *provFixture, cred, device string) error {
	var req DeviceCredentialUpdateRequest
	req.DeviceToken.Set, req.DeviceToken.Value = true, &device
	_, err := f.api.UpdateDeviceCredential(f.ctx, cred, &req)
	return err
}

// The limit on a device certificate credentials is not a create-time check alone: a move onto
// the device is a way to add one.
func TestMovingACertificateCredentialCountsAgainstTheTargetDevice(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup() // device "dev"
	f.device("src", "src-ext", "dt-a", "")
	f.device("roomy", "roomy-ext", "dt-a", "")
	for i := 0; i < MaxX509CredentialsPerDevice; i++ {
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create(fmt.Sprintf("full-%d", i), "dev", ca.leaf(t, "SN").pem(), "maker"))
		require.NoError(t, err)
	}
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("mover", "src", ca.leaf(t, "SN").pem(), "maker"))
	require.NoError(t, err)

	err = moveCredential(f, "mover", "dev")
	requireInvalid(t, err, "a move onto a device holding the limit")
	got, err := f.api.DeviceCredentialsByToken(f.ctx, []string{"mover"})
	require.NoError(t, err)
	assert.Equal(t, "src", got[0].Device.Token, "a refused move moved the credential")

	require.NoError(t, moveCredential(f, "mover", "roomy"), "the control: a device with room")

	// A credential of another type moves onto a full device freely: the limit is about certificates.
	_, err = f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "tok", DeviceToken: "src", CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, moveCredential(f, "tok", "dev"))
}

// A device earmarked for a certificate profile has the token derived from its external id, so
// the external id is fixed. Everywhere else it can change.
func TestAnExternalIdCannotChangeUnderACertificateEarmark(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.profile("pp-x", ProvisionCheckPreProvisioned, withAnchor("maker"))
	f.profile("pp-s", ProvisionCheckPreProvisioned)
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "", ExternalId: strp("SN-1"), DeviceTypeToken: "dt-a", ProvisioningProfileToken: strp("pp-x")})
	require.NoError(t, err)
	f.device("plain", "plain-ext", "dt-a", "pp-s")
	f.device("bare", "bare-ext", "dt-a", "")
	derived := DeriveX509DeviceToken("SN-1")

	setExt := func(v *string) *DeviceUpdateRequest {
		var r DeviceUpdateRequest
		r.ExternalId.Set, r.ExternalId.Value = true, v
		return &r
	}
	_, err = f.api.UpdateDevice(f.ctx, derived, setExt(strp("SN-2")))
	require.ErrorIs(t, err, ErrInvalidEarmark)
	_, err = f.api.UpdateDevice(f.ctx, derived, setExt(nil))
	require.ErrorIs(t, err, ErrInvalidEarmark, "clearing it breaks the rule as much as changing it")
	got, err := f.api.DevicesByToken(f.ctx, []string{derived})
	require.NoError(t, err)
	assert.Equal(t, "SN-1", got[0].ExternalId.String)

	// Restating the same value, or editing anything else, is fine.
	_, err = f.api.UpdateDevice(f.ctx, derived, setExt(strp("SN-1")))
	require.NoError(t, err, "restating the external id")
	var rename DeviceUpdateRequest
	rename.Name.Set, rename.Name.Value = true, strp("renamed")
	_, err = f.api.UpdateDevice(f.ctx, derived, &rename)
	require.NoError(t, err, "an edit that leaves the external id alone")

	// The controls: a secret-profile earmark and no earmark leave the external id editable.
	_, err = f.api.UpdateDevice(f.ctx, "plain", setExt(strp("plain-2")))
	require.NoError(t, err)
	_, err = f.api.UpdateDevice(f.ctx, "bare", setExt(strp("bare-2")))
	require.NoError(t, err)
}

// An import row earmarked for a certificate profile may omit its token: it is derived from the
// external id, as a single create and a bulk create derive it.
func TestImportDerivesAnOmittedCertificateToken(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("maker", newRootCA(t, "Maker"))
	f.profile("pp-x", ProvisionCheckPreProvisioned, withAnchor("maker"))
	f.profile("pp-s", ProvisionCheckPreProvisioned)
	row := func(token, ext, profile string) *DeviceImportRow {
		r := &DeviceImportRow{Token: token, DeviceTypeToken: "dt-a", ProvisioningProfileToken: strp(profile)}
		if ext != "" {
			r.ExternalId = strp(ext)
		}
		return r
	}
	rows := []*DeviceImportRow{
		row("", "I-1", "pp-x"),                           // omitted: derived
		row(DeriveX509DeviceToken("I-2"), "I-2", "pp-x"), // stated and right
		row("", "", "pp-x"),                              // nothing to derive from
		row("", "I-4", "pp-s"),                           // a secret profile derives nothing
	}
	res, err := f.api.ImportDevices(f.ctx, rows, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, []string{DeviceImportCreated, DeviceImportCreated, DeviceImportInvalid, DeviceImportInvalid}, outcomes(res))
	assert.Equal(t, DeriveX509DeviceToken("I-1"), res.Rows[0].Token, "the result names the derived token")
	assert.Equal(t, "", rows[0].Token, "the caller row was edited")
	got, err := f.api.DevicesByToken(f.ctx, []string{DeriveX509DeviceToken("I-1")})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "I-1", got[0].ExternalId.String)

	// A repeat of the same file is idempotent rather than a conflict.
	res, err = f.api.ImportDevices(f.ctx, rows[:2], DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, []string{DeviceImportExists, DeviceImportExists}, outcomes(res))
}

// Enabling an anchor again settles the kick its withdrawal owed: the devices have reconnected
// under the restored CA, and the kick would end their new sessions.
func TestEnablingAnAnchorClearsItsOwedKicks(t *testing.T) {
	f := newProvFixture(t)
	f.api.SessionKicker = &togglingKicker{}
	fl := f.x509Fleet("a", 2)
	// A debt of another origin for the same device must survive.
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueSessionKicksOnTx(tx, "acme", 1, []string{fl.devices[0]})
	}))

	_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Len(t, f.pendingKicks(), 3)

	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(true))
	require.NoError(t, err)
	rows := f.pendingKicks()
	require.Len(t, rows, 1, "the anchor's own debts are cleared")
	assert.Equal(t, uint(1), rows[0].Origin, "another origin's debt was cleared")

	// Enabling an anchor that was never disabled clears nothing.
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(true))
	require.NoError(t, err)
	assert.Len(t, f.pendingKicks(), 1)
}

// Another tenant anchor is not this tenant anchor: naming its token is NOT_FOUND for a
// credential and for a profile, whatever state it is in.
func TestAnotherTenantsAnchorIsNotFound(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	other := core.WithTenant(f.ctx, "other")
	_, err := f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: "theirs", CertificatePem: ca.pem()})
	require.NoError(t, err)

	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-x", "dev", ca.leaf(t, "SN").pem(), "theirs"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.api.CreateProvisioningProfile(f.ctx, &ProvisioningProfileCreateRequest{
		Token: "pp", ProvisionKey: "k", Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true,
		TrustAnchorToken: strp("theirs")})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "theirs", setEnabled(false))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// The control: the same request against this tenant own anchor of that name works.
	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-ok", "dev", ca.leaf(t, "SN").pem(), "maker"))
	require.NoError(t, err)
}

// A CA minted a few minutes ahead of this clock is an anchor, and its real notBefore is stored.
func TestAnAnchorInsideTheSkewIsUsable(t *testing.T) {
	f := newProvFixture(t)
	ahead := time.Now().Add(3 * time.Minute)
	ca := newPki(t, nil, pkiOpts{cn: "Ahead", ca: true, notBefore: ahead})
	a := f.anchor("ahead", ca)
	assert.WithinDuration(t, ahead, a.NotBefore, time.Second, "the stored notBefore is the certificate real one")
	f.device("dev", "dev-ext", "dt-a", "")
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", ca.leaf(t, "SN").pem(), "ahead"))
	require.NoError(t, err, "a chain to an anchor inside the skew verifies")

	_, err = f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{
		Token: "far", CertificatePem: newPki(t, nil, pkiOpts{cn: "Far", ca: true, notBefore: time.Now().Add(10 * time.Minute)}).pem()})
	requireInvalid(t, err, "an anchor past the skew")
}

// Only the TRANSITION to enabled settles the debt: an ordinary edit of an enabled anchor
// touches the queue not at all.
func TestOnlyReEnablingAnAnchorSettlesItsKicks(t *testing.T) {
	f := newProvFixture(t)
	f.api.SessionKicker = &togglingKicker{}
	a := f.anchor("a", newRootCA(t, "A"))
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueSessionKicksOnTx(tx, "acme", anchorKickOrigin(a.ID), []string{"some-device"})
	}))

	var rename DeviceTrustAnchorUpdateRequest
	rename.Name.Set, rename.Name.Value = true, strp("renamed")
	_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "a", &rename)
	require.NoError(t, err)
	assert.Len(t, f.pendingKicks(), 1, "an edit of an enabled anchor cleared a debt")
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(true))
	require.NoError(t, err)
	assert.Len(t, f.pendingKicks(), 1, "restating enabled cleared a debt")
}
