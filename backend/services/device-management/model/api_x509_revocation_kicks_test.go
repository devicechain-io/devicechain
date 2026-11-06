// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// LIVE-SESSION KICKS OF A REVOCATION, at the model. The broker-level proof (a real session ended,
// the race window, the reconnect refused) is processor/callout_x509_revocation_kicks_broker_test.go.

type revKickFixture struct {
	*provFixture
	kicker *togglingKicker
}

func newRevKickFixture(t *testing.T) *revKickFixture {
	t.Helper()
	f := newProvFixture(t)
	k := &togglingKicker{up: true}
	f.api.SessionKicker = k
	f.api.RevocationResweepAfter = time.Hour // the re-sweep tests set their own; a short default makes a later call re-ask
	return &revKickFixture{provFixture: f, kicker: k}
}

func (f *revKickFixture) callsOf(reason string) [][]string {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	var out [][]string
	for _, c := range f.kicker.calls {
		if c.reason == reason {
			out = append(out, c.tokens)
		}
	}
	return out
}

func (f *revKickFixture) kicked() []string {
	var all []string
	for _, c := range f.callsOf(messaging.SessionKickCertificateRevoked) {
		all = append(all, c...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

func (f *revKickFixture) resetCalls() {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	f.kicker.calls = nil
}

func (f *revKickFixture) revokeFp(anchor string, fp string) (*DeviceRevokedCertificate, error) {
	return f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: anchor, FingerprintSha256: &fp})
}

// seam runs a revocation writer's three calls with a delta of its own: what a CRL upload does.
func (f *revKickFixture) seam(anchor string, d RevocationDelta) error {
	f.t.Helper()
	a, err := f.api.trustAnchorByToken(f.ctx, anchor)
	require.NoError(f.t, err)
	whole := f.api.prepareRevocationKicks(f.ctx, "acme", anchor)
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		_, err := f.api.queueRevocationKicksOnTx(tx, "acme", a.ID, d, whole)
		return err
	}))
	return f.api.endRevokedSessions(f.ctx, "acme", a.ID)
}

// pin creates a certificate credential for a new device under anchor, through the chain.
func (f *revKickFixture) pin(anchor, device string, leaf *pkiNode, inters ...*pkiNode) *DeviceCredential {
	f.t.Helper()
	f.device(device, device+"-ext", "dt-a", "")
	text := leaf.pem()
	for _, i := range inters {
		text += i.pem()
	}
	c, err := f.api.CreateDeviceCredential(f.ctx, x509Create(device+"-cred", device, text, anchor))
	require.NoError(f.t, err)
	return c
}

func (f *revKickFixture) rowsOfRevocation(anchorId uint) []PendingSessionKick {
	f.t.Helper()
	var rows []PendingSessionKick
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("origin = ?", revocationKickOrigin(anchorId)).Order("device_token").Find(&rows).Error)
	return rows
}

// 🔑 A LEAF REVOCATION ENDS THE SESSIONS OF THAT DEVICE AND NO OTHER. The reason is the new
// vocabulary word, the sibling under the same CA is untouched, and a device on another anchor too.
func TestLeafRevocationKicksOnlyThatDevice(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	otherCA := newRootCA(t, "Other Root")
	f.anchor("other", otherCA)
	victim, sibling := ca.leaf(t, "SN-V"), ca.leaf(t, "SN-S")
	f.pin("maker", "dev-victim", victim)
	f.pin("maker", "dev-sibling", sibling)
	f.pin("other", "dev-elsewhere", otherCA.leaf(t, "SN-E"))

	f.resetCalls()
	got, err := f.revokeFp("maker", x509trust.Fingerprint(victim.cert))
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, []string{"dev-victim"}, f.kicked(), "exactly the revoked certificate's device")
	require.Len(t, f.callsOf(messaging.SessionKickCertificateRevoked), 1)
	rows := f.rowsOfRevocation(a.ID)
	require.Len(t, rows, 1, "the debt stays until its re-sweep is confirmed")
	assert.Equal(t, "dev-victim", rows[0].DeviceToken)
	require.NotNil(t, rows[0].NotBefore, "the first sweep is confirmed, so the row now waits for its re-sweep")
}

// 🔑 A CA-LEVEL ENTRY ENDS EVERY DEVICE UNDER THE ANCHOR: links record the path a credential was
// CREATED through, not the paths its sessions use, so an intermediate revocation is not targeted by
// them. The control is the leaf case above (one device); a device on another anchor is untouched.
func TestIntermediateRevocationKicksTheWholeAnchor(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	otherCA := newRootCA(t, "Other Root")
	f.anchor("other", otherCA)
	inter1 := newPki(t, ca, pkiOpts{cn: "Line 1", ca: true})
	inter2 := newPki(t, ca, pkiOpts{cn: "Line 2", ca: true})
	f.pin("maker", "dev-1", inter1.leaf(t, "SN-1"), inter1)
	f.pin("maker", "dev-2", inter2.leaf(t, "SN-2"), inter2)
	f.pin("maker", "dev-direct", ca.leaf(t, "SN-3"))
	f.pin("other", "dev-elsewhere", otherCA.leaf(t, "SN-E"))
	f.resetCalls()

	_, err := f.revokeFp("maker", x509trust.Fingerprint(inter1.cert))
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-1", "dev-2", "dev-direct"}, f.kicked(),
		"every device with an enabled certificate credential under the anchor, and no other anchor's")
}

// An entry that matches no link at all (a certificate nobody registered, an intermediate never seen)
// ends nothing: the connect path refuses it.
func TestAnUnmatchedRevocationKicksNobody(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	f.pin("maker", "dev-1", ca.leaf(t, "SN-1"))
	f.resetCalls()

	_, err := f.revokeFp("maker", x509trust.Fingerprint(ca.leaf(t, "never-registered").cert))
	require.NoError(t, err)
	_, err = f.revokeFp("maker", x509trust.Fingerprint(newPki(t, ca, pkiOpts{cn: "Unseen CA", ca: true}).cert))
	require.NoError(t, err)
	assert.Empty(t, f.callsOf(messaging.SessionKickCertificateRevoked))
	assert.Empty(t, f.rowsOfRevocation(a.ID))
}

// 🔴 A DISABLED CREDENTIAL IS STILL A LEAF TARGET. Its own disable commits before its kick, and a
// kick that failed there is not durably owed, so the revocation of its certificate is the next
// chance to end a session it may still hold. The anchor-wide kick (a CA-level entry) is the policy
// "every device with a live credential" and leaves it out, as an anchor disable does.
func TestARevokedLeafOfADisabledCredentialIsStillKicked(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	inter := newPki(t, ca, pkiOpts{cn: "Line", ca: true})
	disabledLeaf := ca.leaf(t, "SN-D")
	cred := f.pin("maker", "dev-disabled", disabledLeaf)
	f.pin("maker", "dev-live", inter.leaf(t, "SN-L"), inter)
	var off DeviceCredentialUpdateRequest
	off.Enabled.Set, off.Enabled.Value = true, boolp(false)
	_, err := f.api.UpdateDeviceCredential(f.ctx, cred.Token, &off)
	require.NoError(t, err)
	f.resetCalls()

	_, err = f.revokeFp("maker", x509trust.Fingerprint(disabledLeaf.cert))
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-disabled"}, f.kicked(), "the leaf entry covers the disabled credential")

	f.resetCalls()
	_, err = f.revokeFp("maker", x509trust.Fingerprint(inter.cert))
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-live"}, f.kicked(), "the anchor-wide kick is for devices with a live credential")
}

// 🔑 THE RE-SWEEP. The first kick is confirmed and the row is NOT deleted: it waits NotBefore, and is
// then kicked again and only then deleted. Before it is due a background pass leaves it alone.
func TestARevocationKickIsSweptAgainAfterTheDelay(t *testing.T) {
	f := newRevKickFixture(t)
	f.api.RevocationResweepAfter = 600 * time.Millisecond
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	f.resetCalls()

	_, err := f.revokeFp("maker", x509trust.Fingerprint(leaf.cert))
	require.NoError(t, err)
	require.Len(t, f.callsOf(messaging.SessionKickCertificateRevoked), 1)

	_, err = f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	assert.Len(t, f.callsOf(messaging.SessionKickCertificateRevoked), 1, "not due yet: the pass must not ask the broker again")
	require.Len(t, f.rowsOfRevocation(a.ID), 1)

	require.Eventually(t, func() bool {
		_, derr := f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
		require.NoError(t, derr)
		return len(f.callsOf(messaging.SessionKickCertificateRevoked)) == 2
	}, 10*time.Second, 50*time.Millisecond, "the re-sweep never ran")
	assert.Empty(t, f.rowsOfRevocation(a.ID), "the re-sweep is confirmed, so the debt is cleared")
	_, err = f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	assert.Len(t, f.callsOf(messaging.SessionKickCertificateRevoked), 2, "and it is swept exactly twice")
}

// A debt queued AGAIN while its row waits for the re-sweep is a new withdrawal: its first sweep is
// due at once, not after the old delay.
func TestRequeuingARevocationKickMakesItsFirstSweepDueAgain(t *testing.T) {
	f := newRevKickFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	_, err := f.revokeFp("maker", x509trust.Fingerprint(leaf.cert))
	require.NoError(t, err)
	rows := f.rowsOfRevocation(a.ID)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].NotBefore, "waiting out an hour")
	f.resetCalls()

	require.NoError(t, f.seam("maker", RevocationDelta{Fingerprints: []string{x509trust.Fingerprint(leaf.cert)}}))
	assert.Len(t, f.callsOf(messaging.SessionKickCertificateRevoked), 1, "a new withdrawal is swept at once")
}

// 🔑 ErrSessionsNotEnded IS SURFACED, THE REVOCATION STANDS, AND REPEATING IT ASKS AGAIN.
func TestAnUnconfirmedRevocationKickSurfacesAndIsRetried(t *testing.T) {
	f := newRevKickFixture(t)
	f.kicker.set(false)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	fp := x509trust.Fingerprint(leaf.cert)

	got, err := f.revokeFp("maker", fp)
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.NotNil(t, got, "the revocation is saved and returned with the error")
	ended, other := SessionsEndedOf(err)
	assert.False(t, ended)
	assert.NoError(t, other)
	rows := f.rowsOfRevocation(a.ID)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].NotBefore, "unconfirmed: still the first sweep")

	// The control: the entry is stored regardless.
	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("fingerprint = ?", fp).Count(&n).Error)
	assert.EqualValues(t, 1, n)

	// Repeating it, with the responder still down, reports the failure again (the entry exists, so
	// this call writes nothing and must still not report success).
	_, err = f.revokeFp("maker", fp)
	require.ErrorIs(t, err, ErrSessionsNotEnded)

	f.kicker.set(true)
	_, err = f.revokeFp("maker", fp)
	require.NoError(t, err, "the repeat finds the debt and settles it")
	ended, _ = SessionsEndedOf(err)
	assert.True(t, ended)
	rows = f.rowsOfRevocation(a.ID)
	require.Len(t, rows, 1)
	assert.NotNil(t, rows[0].NotBefore)

	// Anything else is not "sessions not ended".
	ended, other = SessionsEndedOf(fmt.Errorf("boom"))
	assert.False(t, ended)
	assert.Error(t, other)
}

// 🔑 THE LEGACY RECONCILER RUNS BEFORE THE TARGETING. A credential created before chain links existed
// has none; a CRL entry (a serial) finds it only once its depth-0 link is written. The writer's
// seam reconciles first, so the legacy credential's session is ended and a sibling's is not.
func TestLegacyCredentialsAreLinkedBeforeAKickIsTargeted(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	legacy, sibling := ca.leaf(t, "SN-L"), ca.leaf(t, "SN-S")
	lc := f.pin("maker", "dev-legacy", legacy)
	f.pin("maker", "dev-sibling", sibling)
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", lc.ID).Delete(&DeviceCredentialChainLink{}).Error)
	serial, ok := x509trust.SerialHex(legacy.cert.SerialNumber)
	require.True(t, ok)
	// The CRL's issuer identity: the name is the leaf's issuer name; a legacy link has no key, so
	// whatever key the CRL's issuer has matches it.
	issuer := x509trust.IssuerID{Key: "any-key", Name: x509trust.IssuerNameHash(legacy.cert)}
	f.resetCalls()

	require.NoError(t, f.seam("maker", RevocationDelta{Serials: []x509trust.IssuerSerial{{Issuer: issuer, Serial: serial}}}))
	assert.Equal(t, []string{"dev-legacy"}, f.kicked(),
		"a legacy link has no issuer key, so name + serial finds it; the sibling's serial is not on the list")
}

// A link that HAS an issuer key is matched on it: the same (name, serial) under another key is
// another CA's certificate, and no session of this one is ended for it.
func TestAKeyedLinkIsNotMatchedUnderAnotherIssuerKey(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	serial, _ := x509trust.SerialHex(leaf.cert.SerialNumber)
	v, err := x509trust.VerifyClientChain(ca.cert, []*x509.Certificate{leaf.cert}, time.Now())
	require.NoError(t, err)
	real := v.RevocationKeys().Serials[0].Issuer
	f.resetCalls()

	wrong := x509trust.IssuerID{Key: "ff" + real.Key[2:], Name: real.Name}
	require.NoError(t, f.seam("maker", RevocationDelta{Serials: []x509trust.IssuerSerial{{Issuer: wrong, Serial: serial}}}))
	assert.Empty(t, f.kicked(), "another issuer key")

	require.NoError(t, f.seam("maker", RevocationDelta{Serials: []x509trust.IssuerSerial{{Issuer: real, Serial: serial}}}))
	assert.Equal(t, []string{"dev-1"}, f.kicked(), "the control: the right identity ends it")
}

// 🔑 THE RECONCILER IS BOUNDED. With more unlinked credentials than the budget the call links the
// budget's worth, says the links are not whole, and the kick covers the WHOLE anchor (an over-kick,
// never a miss); the next calls carry on and, once everything is linked, target narrowly again.
func TestTheLegacyReconcilerIsBoundedAndAnIncompleteOneWidensTheKick(t *testing.T) {
	f := newRevKickFixture(t)
	old := legacyReconcileBudget
	legacyReconcileBudget = legacyLinkBatch // one batch
	t.Cleanup(func() { legacyReconcileBudget = old })
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	n := legacyLinkBatch + 3
	var creds []*DeviceCredential
	for i := 0; i < n; i++ {
		creds = append(creds, f.pin("maker", fmt.Sprintf("dev-%03d", i), ca.leaf(t, fmt.Sprintf("SN-%d", i))))
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("trust_anchor_id = ?", a.ID).Delete(&DeviceCredentialChainLink{}).Error)
	f.resetCalls()

	whole := f.api.prepareRevocationKicks(f.ctx, "acme", "maker")
	assert.False(t, whole, "the budget ran out before the credentials did")
	var linked int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Where("trust_anchor_id = ?", a.ID).Count(&linked).Error)
	assert.EqualValues(t, legacyLinkBatch, linked, "bounded: exactly one batch was linked, not all")

	// An entry that matches no link: with links not whole the kick covers the anchor.
	serial, _ := x509trust.SerialHex(ca.leaf(t, "nobody").cert.SerialNumber)
	nobody := RevocationDelta{Serials: []x509trust.IssuerSerial{{Issuer: x509trust.IssuerID{Key: "k", Name: "n"}, Serial: serial}}}
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		_, err := f.api.queueRevocationKicksOnTx(tx, "acme", a.ID, nobody, whole)
		return err
	}))
	require.NoError(t, f.api.endRevokedSessions(f.ctx, "acme", a.ID))
	assert.Len(t, f.kicked(), n, "an incomplete reconcile widens the kick to every device under the anchor")
	_ = creds

	// Carried on: the next call links the rest and reports whole; the same entry then kicks nobody.
	assert.True(t, f.api.prepareRevocationKicks(f.ctx, "acme", "maker"))
	f.resetCalls()
	require.NoError(t, f.seam("maker", nobody))
	assert.Empty(t, f.kicked(), "linked and whole: an entry that matches nothing ends nothing")
}

// A manual revoke finds a credential whose leaf could not be linked (it is marked, with the pin as
// its fingerprint), and another tenant's link rows are not matched even when forged to name the
// same fingerprint under the same anchor id.
func TestAManualRevokeFindsAMarkedCredentialAndStaysInItsTenant(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	c := f.pin("maker", "dev-1", leaf)
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", c.ID).Delete(&DeviceCredentialChainLink{}).Error)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", c.ID).Update("certificate_pem", "garbage").Error)

	other := putest.TenantContext("someone-else")()
	oca := newRootCA(t, "Other Root")
	_, err := f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	_, err = f.api.CreateDevice(other, &DeviceCreateRequest{Token: "odev", DeviceTypeToken: "dt-a"})
	require.NoError(t, err)
	oa, err := f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: "o", CertificatePem: oca.pem()})
	require.NoError(t, err)
	_, err = f.api.CreateDeviceCredential(other, x509Create("oc", "odev", oca.leaf(t, "OSN").pem(), "o"))
	require.NoError(t, err)
	mine, err := f.api.trustAnchorByToken(f.ctx, "maker")
	require.NoError(t, err)
	require.NoError(t, f.api.RDB.DB(other).Model(&DeviceCredentialChainLink{}).Where("trust_anchor_id = ?", oa.ID).
		Updates(map[string]any{"trust_anchor_id": mine.ID, "fingerprint": x509trust.Fingerprint(leaf.cert)}).Error)
	f.resetCalls()

	_, err = f.revokeFp("maker", x509trust.Fingerprint(leaf.cert))
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-1"}, f.kicked(), "the marked credential is found by its pin; the other tenant's device is not")
}

// 🔴 THE TARGETING NAMES THE TENANT AND THE ANCHOR ITSELF. The rdb tenant callback is not the only
// thing keeping another tenant's rows out: it is skipped for a system context, and a projection or a
// raw statement never had it. Run under a system context, with another tenant's rows forged to sit
// under THIS anchor id (a depth-1 link naming the revoked fingerprint, and a credential and device
// that would answer), the targeting must still name only this tenant's device and must not widen.
func TestKickTargetingIsolatesTenantsByItsOwnPredicates(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	fp := x509trust.Fingerprint(leaf.cert)

	other := putest.TenantContext("someone-else")()
	oca := newRootCA(t, "Other Root")
	_, err := f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	_, err = f.api.CreateDevice(other, &DeviceCreateRequest{Token: "odev", DeviceTypeToken: "dt-a"})
	require.NoError(t, err)
	_, err = f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: "o", CertificatePem: oca.pem()})
	require.NoError(t, err)
	oc, err := f.api.CreateDeviceCredential(other, x509Create("oc", "odev", oca.leaf(t, "OSN").pem(), "o"))
	require.NoError(t, err)
	require.NoError(t, f.api.RDB.DB(other).Model(&DeviceCredential{}).Where("id = ?", oc.ID).Update("trust_anchor_id", a.ID).Error)
	require.NoError(t, f.api.RDB.DB(other).Model(&DeviceCredentialChainLink{}).Where("credential_id = ?", oc.ID).
		Updates(map[string]any{"trust_anchor_id": a.ID, "fingerprint": fp}).Error)
	require.NoError(t, f.api.RDB.DB(other).Create(&DeviceCredentialChainLink{CredentialId: oc.ID, Depth: 1, TrustAnchorId: a.ID,
		Fingerprint: fp, IssuerName: x509trust.IssuerNameHash(leaf.cert), Serial: "ff"}).Error)

	sys := f.api.RDB.DB(core.WithSystemContext(context.Background()))
	var tokens []string
	var wide bool
	require.NoError(t, sys.Transaction(func(tx *gorm.DB) error {
		var terr error
		tokens, wide, terr = revocationKickTargets(tx, "acme", a.ID, RevocationDelta{Fingerprints: []string{fp}}, true)
		return terr
	}))
	assert.Equal(t, []string{"dev-1"}, tokens, "only this tenant's device")
	assert.False(t, wide, "another tenant's depth-1 link must not make this tenant's kick anchor-wide")
}

// With no kicker wired (an instance with no MQTT auth callout) a revocation reads and writes no
// kick rows and never reports ErrSessionsNotEnded.
func TestRevocationWithNoKickerQueuesNothing(t *testing.T) {
	f := newRevKickFixture(t)
	f.api.SessionKicker = nil
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	_, err := f.revokeFp("maker", x509trust.Fingerprint(leaf.cert))
	require.NoError(t, err)
	assert.Empty(t, f.pendingKicks())
}

func TestValidateRevocationResweepAfter(t *testing.T) {
	assert.NoError(t, ValidateRevocationResweepAfter(DefaultRevocationResweepAfter, 3*time.Second))
	assert.Error(t, ValidateRevocationResweepAfter(8*time.Second, 3*time.Second), "must exceed lookup timeout + broker timeout")
	assert.NoError(t, ValidateRevocationResweepAfter(8*time.Second+time.Nanosecond, 3*time.Second))
	assert.Error(t, ValidateRevocationResweepAfter(MaxRevocationResweepAfter+time.Second, 3*time.Second))
	assert.Equal(t, DefaultRevocationResweepAfter, (&Api{}).revocationResweepAfter())
}

func TestNewlyRevokedSerialsIsTheDiff(t *testing.T) {
	is := x509trust.IssuerID{Key: "k", Name: "n"}
	got := NewlyRevokedSerials(is, []string{"01", "02"}, []string{"02", "03", "04"})
	assert.Equal(t, []x509trust.IssuerSerial{{Issuer: is, Serial: "03"}, {Issuer: is, Serial: "04"}}, got)
	assert.Empty(t, NewlyRevokedSerials(is, []string{"01"}, []string{"01"}))
}

// A dead responder is asked a few times, not once per chunk of a fleet-sized debt.
func TestAFleetSizedKickGivesUpAfterConsecutiveFailures(t *testing.T) {
	f := newRevKickFixture(t)
	f.kicker.set(false)
	var tokens []string
	for i := 0; i < 10*messaging.MaxSessionKickTokens; i++ {
		tokens = append(tokens, fmt.Sprintf("dev-%05d", i))
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueSessionKicksOnTx(tx, "acme", revocationKickOrigin(1), tokens)
	}))
	err := f.api.drainSessionKicks(f.ctx, "acme", 0, func(db *gorm.DB) *gorm.DB { return db })
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.Len(t, f.kicker.calls, maxConsecutiveKickFailures, "ten chunks were owed; the pass stopped asking after three failures")
	assert.Len(t, f.pendingKicks(), len(tokens), "nothing was lost")
}

// A debt too large to settle inside the writer's own call is not reported as settled: the call asks
// the broker about the first rows it may, and says ErrSessionsNotEnded for the rest, which the
// background drain carries on until the queue is empty.
func TestAFleetSizedRevocationKickIsNotReportedSettledUntilItIs(t *testing.T) {
	f := newRevKickFixture(t)
	old := revocationInlineKickRows
	revocationInlineKickRows = 2
	t.Cleanup(func() { revocationInlineKickRows = old })
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	for i := 0; i < 5; i++ {
		f.pin("maker", fmt.Sprintf("dev-%d", i), ca.leaf(t, fmt.Sprintf("SN-%d", i)))
	}
	inter := newPki(t, ca, pkiOpts{cn: "Line", ca: true})
	f.pin("maker", "dev-inter", inter.leaf(t, "SN-I"), inter)
	f.resetCalls()

	_, err := f.revokeFp("maker", x509trust.Fingerprint(inter.cert))
	require.ErrorIs(t, err, ErrSessionsNotEnded, "6 devices are owed and one call asks about 2")
	assert.Len(t, f.kicked(), 2)

	// The background drain finishes it (pages of the queue, as the periodic task runs it).
	for i := 0; i < 3; i++ {
		_, derr := f.api.DrainSessionKicks(f.ctx, 2)
		require.NoError(t, derr)
	}
	assert.Len(t, f.kicked(), 6, "every owed device was asked in the end")
	for _, r := range f.rowsOfRevocation(a.ID) {
		assert.NotNil(t, r.NotBefore, "each settled its first sweep")
	}
	_, err = f.revokeFp("maker", x509trust.Fingerprint(inter.cert))
	require.NoError(t, err, "nothing left in its first sweep: the repeat reports settled")
}

// 🔴 A CAPPED RE-SWEEP IS NOT REPORTED SETTLED. Repeating a revoke whose re-sweeps came due drains at
// most the inline limit; the rest of the due re-sweeps are still owed, and the call says so.
func TestARepeatedRevokeReportsDueReSweepsItDidNotReach(t *testing.T) {
	f := newRevKickFixture(t)
	f.api.RevocationResweepAfter = 20 * time.Millisecond
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	for i := 0; i < 4; i++ {
		f.pin("maker", fmt.Sprintf("dev-%d", i), ca.leaf(t, fmt.Sprintf("SN-%d", i)))
	}
	inter := newPki(t, ca, pkiOpts{cn: "Line", ca: true})
	f.pin("maker", "dev-inter", inter.leaf(t, "SN-I"), inter)
	fp := x509trust.Fingerprint(inter.cert)
	_, err := f.revokeFp("maker", fp)
	require.NoError(t, err, "five devices, one pass")
	time.Sleep(60 * time.Millisecond) // every re-sweep is due now
	old := revocationInlineKickRows
	revocationInlineKickRows = 2
	t.Cleanup(func() { revocationInlineKickRows = old })
	f.resetCalls()

	_, err = f.revokeFp("maker", fp)
	require.ErrorIs(t, err, ErrSessionsNotEnded, "five re-sweeps are due and one call asks about two")
	assert.Len(t, f.kicked(), 2)
	assert.Len(t, f.rowsOfRevocation(a.ID), 3, "the two re-swept rows are cleared; the three not reached are still owed")
}

// 🔴 A SWEEP IS NOT HELD FOR EVER BY A SLOW RESPONDER. When the time allowed runs out the rest of the
// debt stays pending and the call says so.
func TestASlowResponderDoesNotHoldADrainBeyondItsBudget(t *testing.T) {
	f := newRevKickFixture(t)
	old := sessionKickDrainBudget
	sessionKickDrainBudget = 30 * time.Millisecond
	t.Cleanup(func() { sessionKickDrainBudget = old })
	slow := &slowKicker{delay: 40 * time.Millisecond}
	f.api.SessionKicker = slow
	var tokens []string
	for i := 0; i < 6*messaging.MaxSessionKickTokens; i++ {
		tokens = append(tokens, fmt.Sprintf("dev-%05d", i))
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueSessionKicksOnTx(tx, "acme", revocationKickOrigin(1), tokens)
	}))
	err := f.api.drainSessionKicks(f.ctx, "acme", 0, func(db *gorm.DB) *gorm.DB { return db })
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.Less(t, int(slow.calls.Load()), 6, "it stopped asking when the budget ran out")
	assert.Greater(t, len(f.pendingKicks()), 0, "and left the rest pending")
}

type slowKicker struct {
	delay time.Duration
	calls atomic.Int64
}

func (k *slowKicker) KickDeviceSessions(context.Context, string, string, []string) error {
	k.calls.Add(1)
	time.Sleep(k.delay)
	return nil
}

// 🔴 A DEBT QUEUED AGAIN WHILE ITS FIRST SWEEP IS IN FLIGHT KEEPS ITS FIRST SWEEP. The sweep that was
// confirmed answered for the generation it read; the row now holds a newer one, so the settle must
// not move it on to its re-sweep.
func TestARequeueDuringTheFirstSweepIsNotSettledByIt(t *testing.T) {
	f := newRevKickFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	leaf := ca.leaf(t, "SN-1")
	f.pin("maker", "dev-1", leaf)
	fp := x509trust.Fingerprint(leaf.cert)
	requeue := &hookKicker{}
	f.api.SessionKicker = requeue
	requeue.hook = func() {
		// While the broker is being asked: another revocation of the same anchor queues the device.
		require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
			_, err := f.api.queueRevocationKicksOnTx(tx, "acme", a.ID, RevocationDelta{Fingerprints: []string{fp}}, true)
			return err
		}))
	}
	var once atomic.Bool
	inner := requeue.hook
	requeue.hook = func() {
		if once.CompareAndSwap(false, true) {
			inner()
		}
	}

	_, err := f.revokeFp("maker", fp)
	require.ErrorIs(t, err, ErrSessionsNotEnded, "the newer debt has not been swept: the call must not say settled")
	rows := f.rowsOfRevocation(a.ID)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].NotBefore, "the newer debt's first sweep is still due")
}

type hookKicker struct{ hook func() }

func (k *hookKicker) KickDeviceSessions(context.Context, string, string, []string) error {
	if k.hook != nil {
		k.hook()
	}
	return nil
}

// ─── linear dedupe (acceptance 12) ──────────────────────────────────────────

func TestSortedUniqueTokens(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, sortedUniqueTokens([]string{"c", "", "a", "b", "a", "c", ""}))
	assert.Empty(t, sortedUniqueTokens([]string{"", ""}))
	in := []string{"b", "a"}
	_ = sortedUniqueTokens(in)
	assert.Equal(t, []string{"b", "a"}, in, "the caller's slice is not reordered")
}

// 🔴 A 100 000-DEVICE QUEUE IS LINEAR. The quadratic dedupe it replaces (a slices.Contains in a loop)
// is ~5e9 string comparisons at this size, minutes; the bound is generous enough for any machine and
// still orders of magnitude below it.
func TestTheKickQueueDedupeIsLinearAtAHundredThousandDevices(t *testing.T) {
	const n = 100_000
	in := make([]string, 0, 2*n)
	for i := 0; i < n; i++ {
		tok := fmt.Sprintf("device-%07d", (i*7919)%n)
		in = append(in, tok, tok)
	}
	start := time.Now()
	out := sortedUniqueTokens(in)
	elapsed := time.Since(start)
	require.Len(t, out, n)
	assert.True(t, slices.IsSorted(out))
	assert.Less(t, elapsed, 5*time.Second, "dedupe of %d tokens took %s: it is not linear", len(in), elapsed)
}

func BenchmarkSortedUniqueTokens(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		in := make([]string, 0, 2*n)
		for i := 0; i < n; i++ {
			tok := fmt.Sprintf("device-%07d", (i*7919)%n)
			in = append(in, tok, tok)
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = sortedUniqueTokens(in)
			}
		})
	}
}
