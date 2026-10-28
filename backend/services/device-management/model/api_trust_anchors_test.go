// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/x509"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// anchor creates a trust anchor from the CA, enabled.
func (f *provFixture) anchor(token string, ca *pkiNode, mutate ...func(*DeviceTrustAnchorCreateRequest)) *DeviceTrustAnchor {
	f.t.Helper()
	req := &DeviceTrustAnchorCreateRequest{Token: token, CertificatePem: ca.pem()}
	for _, m := range mutate {
		m(req)
	}
	a, err := f.api.CreateDeviceTrustAnchor(f.ctx, req)
	require.NoError(f.t, err)
	return a
}

func (f *provFixture) anchorsCount() int64 {
	f.t.Helper()
	var n int64
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Count(&n).Error)
	return n
}

func requireInvalid(t *testing.T, err error, what string) {
	t.Helper()
	require.Error(t, err, what)
	require.True(t, isInvalidRefusal(err), "%s: want an INVALID_VALUE refusal, got %v", what, err)
}

func TestCreateTrustAnchorStoresWhatTheCertificateSays(t *testing.T) {
	f := newProvFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca, func(r *DeviceTrustAnchorCreateRequest) { r.Name = strp("Maker") })

	assert.Equal(t, x509trust.Fingerprint(ca.cert), a.FingerprintSha256)
	assert.Equal(t, "CN=Maker Root", a.Subject)
	assert.Equal(t, "CN=Maker Root", a.Issuer)
	assert.Equal(t, ca.pem(), a.CertificatePem)
	assert.True(t, a.Enabled, "an anchor is enabled unless said otherwise")
	assert.True(t, a.NotAfter.Equal(ca.cert.NotAfter.UTC()))
	cert, err := a.Certificate()
	require.NoError(t, err)
	assert.True(t, cert.Equal(ca.cert))

	got, err := f.api.DeviceTrustAnchorsByToken(f.ctx, []string{"maker"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	off := f.anchor("off", newRootCA(t, "Off"), func(r *DeviceTrustAnchorCreateRequest) { r.Enabled = boolp(false) })
	assert.False(t, off.Enabled)
}

// Each refusal below changes ONE thing about a request the first line of the test proves is
// accepted, so a refusal cannot be an accident of a bad fixture.
func TestCreateTrustAnchorRefusals(t *testing.T) {
	f := newProvFixture(t)
	good := newRootCA(t, "Good")
	f.anchor("control", good)
	before := f.anchorsCount()

	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	cases := map[string]string{
		"a device certificate, not a CA": good.leaf(t, "d").pem(),
		"two certificates":               newRootCA(t, "A").pem() + newRootCA(t, "B").pem(),
		"empty":                          "",
		"prose":                          "not a certificate",
		"a private key beside a CA":      newRootCA(t, "C").pem() + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"an expired CA": newPki(t, nil, pkiOpts{cn: "old", ca: true,
			notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour)}).pem(),
		"a CA with name constraints": newPki(t, nil, pkiOpts{cn: "nc", ca: true,
			mutate: func(c *x509.Certificate) { c.PermittedIPRanges = []*net.IPNet{ipnet} }}).pem(),
		"a CA that cannot sign certificates": newPki(t, nil, pkiOpts{cn: "nosign", ca: true,
			mutate: func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageCRLSign }}).pem(),
	}
	i := 0
	for name, pemText := range cases {
		i++
		_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{
			Token: fmt.Sprintf("bad-%d", i), CertificatePem: pemText})
		requireInvalid(t, err, name)
	}
	assert.Equal(t, before, f.anchorsCount(), "a refused anchor was stored")

	// The private key is never echoed.
	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{
		Token: "k", CertificatePem: cases["a private key beside a CA"]})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "AAAA")
}

func TestTrustAnchorLimitPerTenant(t *testing.T) {
	f := newProvFixture(t)
	for i := 0; i < MaxTrustAnchorsPerTenant; i++ {
		f.anchor(fmt.Sprintf("a-%02d", i), newRootCA(t, fmt.Sprintf("CA %d", i)))
	}
	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{
		Token: "one-too-many", CertificatePem: newRootCA(t, "extra").pem()})
	var lim *limit.Error
	require.ErrorAs(t, err, &lim)
	assert.Equal(t, MaxTrustAnchorsPerTenant, lim.Max)
	assert.Equal(t, int64(MaxTrustAnchorsPerTenant), f.anchorsCount())
}

func TestUpdateTrustAnchorIsPartial(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("a", newRootCA(t, "A"), func(r *DeviceTrustAnchorCreateRequest) { r.Name = strp("first") })

	var req DeviceTrustAnchorUpdateRequest
	req.Description.Set, req.Description.Value = true, strp("about")
	got, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "a", &req)
	require.NoError(t, err)
	assert.Equal(t, "first", got.Name.String, "an omitted field changed")
	assert.Equal(t, "about", got.Description.String)
	assert.True(t, got.Enabled, "an omitted enabled changed")

	var null DeviceTrustAnchorUpdateRequest
	null.Enabled.Set = true // sent as null
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", &null)
	require.Error(t, err, "enabled is NOT NULL; null must be refused")

	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "missing", &req)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func setEnabled(on bool) *DeviceTrustAnchorUpdateRequest {
	var req DeviceTrustAnchorUpdateRequest
	req.Enabled.Set, req.Enabled.Value = true, &on
	return &req
}

// x509Fleet is an anchor with devices holding certificate credentials issued under it.
type x509Fleet struct {
	ca      *pkiNode
	anchor  *DeviceTrustAnchor
	devices []string
}

func (f *provFixture) x509Fleet(anchorToken string, n int) x509Fleet {
	f.t.Helper()
	ca := newRootCA(f.t, "Fleet "+anchorToken)
	fl := x509Fleet{ca: ca, anchor: f.anchor(anchorToken, ca)}
	for i := 0; i < n; i++ {
		dev := fmt.Sprintf("%s-dev-%d", anchorToken, i)
		f.device(dev, dev+"-ext", "dt-a", "")
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create(dev+"-cred", dev, ca.leaf(f.t, dev).pem(), anchorToken))
		require.NoError(f.t, err)
		fl.devices = append(fl.devices, dev)
	}
	return fl
}

func TestDisablingAnAnchorEndsTheSessionsThatRelyOnIt(t *testing.T) {
	f := newProvFixture(t)
	kicker := &togglingKicker{up: true}
	f.api.SessionKicker = kicker
	a := f.x509Fleet("a", 3)
	b := f.x509Fleet("b", 2)
	// A disabled credential under anchor a holds no session worth ending.
	f.device("quiet", "quiet-ext", "dt-a", "")
	quiet, err := f.api.CreateDeviceCredential(f.ctx, x509Create("quiet-cred", "quiet", a.ca.leaf(t, "quiet").pem(), "a"))
	require.NoError(t, err)
	var off DeviceCredentialUpdateRequest
	off.Enabled.Set, off.Enabled.Value = true, boolp(false)
	_, err = f.api.UpdateDeviceCredential(f.ctx, quiet.Token, &off)
	require.NoError(t, err)

	// Control: enabling an enabled anchor, or touching only its name, kicks nobody. (The
	// credential disabled above kicked its own device; that is not what is measured here.)
	kicker.calls = nil
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(true))
	require.NoError(t, err)
	assert.Empty(t, kicker.calls, "no withdrawal, no kick")

	got, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(false))
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	require.Len(t, kicker.calls, 1)
	assert.ElementsMatch(t, a.devices, kicker.calls[0].tokens, "exactly the devices that rely on anchor a")
	assert.NotContains(t, kicker.calls[0].tokens, "quiet")
	for _, d := range b.devices {
		assert.NotContains(t, kicker.calls[0].tokens, d, "another anchor's devices were kicked")
	}
	assert.Empty(t, f.pendingKicks(), "a confirmed kick leaves nothing owed")
}

func TestAnAnchorKickThatCouldNotBeConfirmedIsOwedAndRetried(t *testing.T) {
	f := newProvFixture(t)
	kicker := &togglingKicker{}
	f.api.SessionKicker = kicker
	a := f.x509Fleet("a", 2)

	got, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.NotNil(t, got)
	assert.False(t, got.Enabled, "the disable is saved even though the kick was not confirmed")
	rows := f.pendingKicks()
	require.Len(t, rows, len(a.devices))
	for _, r := range rows {
		assert.Equal(t, anchorKickOrigin(got.ID), r.Origin)
	}

	// A repeat of the same disable changes nothing and still reports the debt.
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.Len(t, f.pendingKicks(), len(a.devices))

	// The responder returns: the repeat confirms and clears the debt.
	kicker.set(true)
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "a", setEnabled(false))
	require.NoError(t, err)
	assert.Empty(t, f.pendingKicks())

	// Or the background pass does, without anyone repeating the operation.
	kicker.set(false)
	f.x509Fleet("c", 1)
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "c", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	kicker.set(true)
	res, err := f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Tenants)
	assert.Empty(t, f.pendingKicks())
}

// The queue is keyed (tenant, device, origin). A profile's revoke, a device reset and an anchor's
// disable are three different debts, so their origins must be three different numbers.
func TestKickOriginsDoNotCollide(t *testing.T) {
	assert.NotEqual(t, kickOriginReset, anchorKickOrigin(1))
	for _, profileID := range []uint{1, 2, 1000, 1 << 20, 1 << 32} {
		for _, anchorID := range []uint{1, 2, 1000, 1 << 20} {
			assert.NotEqual(t, profileID, anchorKickOrigin(anchorID), "profile %d vs anchor %d", profileID, anchorID)
		}
	}

	// And it holds in the queue: a revoke's debt and an anchor's debt for the SAME device, with
	// the same numeric id, are two rows, and settling one leaves the other.
	f := newProvFixture(t)
	f.api.SessionKicker = &togglingKicker{}
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		if err := f.api.queueSessionKicksOnTx(tx, "acme", 1, []string{"dev"}); err != nil {
			return err
		}
		return f.api.queueSessionKicksOnTx(tx, "acme", anchorKickOrigin(1), []string{"dev"})
	}))
	assert.Len(t, f.pendingKicks(), 2)
}

func TestDeleteTrustAnchorIsRefusedWhileReliedOn(t *testing.T) {
	f := newProvFixture(t)
	fl := f.x509Fleet("a", 1)

	_, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "a")
	require.ErrorIs(t, err, ErrEntityInUse, "a credential relies on the anchor")
	assert.Equal(t, int64(1), f.anchorsCount())

	// Soft-deleted credentials still count: the foreign key does, and a refusal that disagrees
	// with it would turn into a raw constraint error.
	cred := f.credentialsOf(fl.devices[0])[0]
	require.NoError(t, f.api.RDB.DB(f.ctx).Delete(&DeviceCredential{}, cred.ID).Error)
	_, err = f.api.DeleteDeviceTrustAnchor(f.ctx, "a")
	require.ErrorIs(t, err, ErrEntityInUse, "a soft-deleted credential still holds the foreign key")
	require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&DeviceCredential{}, cred.ID).Error)

	// A provisioning profile relies on it too.
	f.profile("x509-pp", ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.TrustAnchorToken = strp("a") })
	_, err = f.api.DeleteDeviceTrustAnchor(f.ctx, "a")
	require.ErrorIs(t, err, ErrEntityInUse, "a profile relies on the anchor")
	_, err = f.api.DeleteProvisioningProfile(f.ctx, "x509-pp")
	require.NoError(t, err)

	ok, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "a")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Zero(t, f.anchorsCount())

	ok, err = f.api.DeleteDeviceTrustAnchor(f.ctx, "a")
	require.NoError(t, err)
	assert.False(t, ok, "deleting what is not there is not an error")
}

func TestSearchTrustAnchorsFiltersByEnabled(t *testing.T) {
	f := newProvFixture(t)
	f.anchor("on", newRootCA(t, "On"))
	f.anchor("off", newRootCA(t, "Off"), func(r *DeviceTrustAnchorCreateRequest) { r.Enabled = boolp(false) })

	all, err := f.api.DeviceTrustAnchors(f.ctx, DeviceTrustAnchorSearchCriteria{})
	require.NoError(t, err)
	assert.Len(t, all.Results, 2)
	on, err := f.api.DeviceTrustAnchors(f.ctx, DeviceTrustAnchorSearchCriteria{Enabled: boolp(true)})
	require.NoError(t, err)
	require.Len(t, on.Results, 1)
	assert.Equal(t, "on", on.Results[0].Token)

	n, err := f.api.TrustAnchorCredentialCount(f.ctx, on.Results[0].ID)
	require.NoError(t, err)
	assert.Zero(t, n)
}

var _ = rdb.TokenReference{}
