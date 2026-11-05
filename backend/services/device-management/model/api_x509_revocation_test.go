// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"database/sql"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// X.509 REVOCATION, THE STORE AND ITS READ. These drive the real entry points
// (RevokeDeviceCertificate, ResolveX509Revocation, ProvisionDevice, CreateDeviceCredential) over
// SQLite and assert what landed in the tables and what the connect check would decide. The locks
// and the races are the integration tests' (x509_revocation_integration_test.go).

type revFixture struct {
	*provFixture
	ca     *pkiNode
	anchor *DeviceTrustAnchor
}

func newRevFixture(t *testing.T) *revFixture {
	t.Helper()
	f := newProvFixture(t)
	ca, anchor := f.x509Setup()
	return &revFixture{provFixture: f, ca: ca, anchor: anchor}
}

func (f *revFixture) verify(leaf *pkiNode, inters ...*pkiNode) *x509trust.Verified {
	f.t.Helper()
	chain := []*x509.Certificate{leaf.cert}
	for _, i := range inters {
		chain = append(chain, i.cert)
	}
	v, err := x509trust.VerifyClientChain(f.ca.cert, chain, time.Now())
	require.NoError(f.t, err)
	return v
}

// freshAnchor re-reads the anchor row, as ResolveX509Connect does on a connect.
func (f *revFixture) freshAnchor() *DeviceTrustAnchor {
	f.t.Helper()
	a, err := f.api.trustAnchorByToken(f.ctx, f.anchor.Token)
	require.NoError(f.t, err)
	return a
}

// decide runs the whole check a connect runs: read the anchor, read the state, apply it.
func (f *revFixture) decide(v *x509trust.Verified) (*x509trust.Verified, error) {
	f.t.Helper()
	state, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), v.RevocationKeys(), time.Now())
	if err != nil {
		return nil, err
	}
	return v.ApplyRevocation(state)
}

func (f *revFixture) revoke(fp string) *DeviceRevokedCertificate {
	f.t.Helper()
	got, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: f.anchor.Token, FingerprintSha256: &fp})
	require.NoError(f.t, err)
	return got
}

func (f *revFixture) setMode(mode string) {
	f.t.Helper()
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Where("id = ?", f.anchor.ID).
		Update("revocation_mode", mode).Error)
}

// rowsCounter is the anchor's revocation_rows, and what is actually stored.
func (f *revFixture) rowsCounter() (counter, actual int64) {
	f.t.Helper()
	a := f.freshAnchor()
	counter = a.RevocationRows.Int64
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("trust_anchor_id = ?", a.ID).Count(&actual).Error)
	var lists int64
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationList{}).Where("trust_anchor_id = ?", a.ID).Count(&lists).Error)
	return counter, actual + lists
}

// queryCounter counts the statements read from the revocation tables while the returned stop func
// has not run.
func queryCounter(t *testing.T, db *gorm.DB) (n *atomic.Int64, stop func()) {
	t.Helper()
	n = new(atomic.Int64)
	name := fmt.Sprintf("test:count-revocation-queries-%d", time.Now().UnixNano())
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(d *gorm.DB) {
		if d.Statement.Table == "device_revoked_certificates" || d.Statement.Table == "device_revocation_lists" {
			n.Add(1)
		}
	}))
	return n, func() { require.NoError(t, db.Callback().Query().Remove(name)) }
}

// 🔑 A REVOKED CERTIFICATE IS REFUSED, AND THE STORE SAYS WHY. The control is the same chain before
// the revoke.
func TestManualRevocationRefusesTheCertificateAndOnlyIt(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	sibling := inter.leaf(t, "SN-2")
	v, sv := f.verify(leaf, inter), f.verify(sibling, inter)

	_, err := f.decide(v)
	require.NoError(t, err, "the control: nothing is revoked")

	got := f.revoke(x509trust.Fingerprint(leaf.cert))
	assert.Equal(t, RevokedKindManual, got.Kind)
	assert.Equal(t, string(x509trust.RevocationUnspecified), got.Reason)

	_, err = f.decide(v)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
	_, err = f.decide(sv)
	assert.NoError(t, err, "a sibling under the same intermediate is untouched")

	t.Run("an intermediate revoked refuses every leaf under it", func(t *testing.T) {
		f.revoke(x509trust.Fingerprint(inter.cert))
		_, err := f.decide(sv)
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
	})

	t.Run("unrevoke admits again", func(t *testing.T) {
		ok, err := f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, x509trust.Fingerprint(inter.cert))
		require.NoError(t, err)
		assert.True(t, ok)
		_, err = f.decide(sv)
		assert.NoError(t, err)
		ok, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, x509trust.Fingerprint(inter.cert))
		require.NoError(t, err)
		assert.False(t, ok, "there was nothing left to remove")
		_, err = f.decide(v)
		assert.Error(t, err, "the leaf entry stays")
	})
}

// A revocation does not depend on the credential or the device: it hangs off the anchor.
func TestManualRevocationOutlivesTheCredentialAndTheDevice(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	cred, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem(), "maker"))
	require.NoError(t, err)
	v := f.verify(leaf)
	f.revoke(x509trust.Fingerprint(leaf.cert))

	ok, err := f.api.DeleteDeviceCredential(f.ctx, cred.Token)
	require.NoError(t, err)
	require.True(t, ok)
	// The device goes too (a hard delete of the row stands for DeleteDevice, which needs the whole
	// relationship schema this fixture does not carry).
	require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&Device{}, cred.DeviceId).Error)
	_, err = f.decide(v)
	require.Error(t, err, "deleting the credential and the device must not un-revoke the certificate")
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
}

func TestRevokeIsIdempotentAndCountsItsRows(t *testing.T) {
	f := newRevFixture(t)
	a, b := f.ca.leaf(t, "SN-1"), f.ca.leaf(t, "SN-2")
	first := f.revoke(x509trust.Fingerprint(a.cert))
	again, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: f.anchor.Token, CertificatePem: strp(a.pem()), Reason: strp("KEY_COMPROMISE")})
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "revoking again returns the existing entry")
	assert.Equal(t, string(x509trust.RevocationUnspecified), again.Reason, "the first reason stands")
	c, actual := f.rowsCounter()
	assert.EqualValues(t, 1, c)
	assert.EqualValues(t, 1, actual)

	f.revoke(x509trust.Fingerprint(b.cert))
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, x509trust.Fingerprint(a.cert))
	require.NoError(t, err)
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, x509trust.Fingerprint(a.cert))
	require.NoError(t, err)
	c, actual = f.rowsCounter()
	assert.EqualValues(t, 1, c, "the counter follows the rows through a revoke, a repeat and two unrevokes")
	assert.EqualValues(t, 1, actual)
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, x509trust.Fingerprint(b.cert))
	require.NoError(t, err)
	c, actual = f.rowsCounter()
	assert.EqualValues(t, 0, c)
	assert.EqualValues(t, 0, actual)
}

func TestRevokeRefusesWhatItCannotMean(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	fp := x509trust.Fingerprint(leaf.cert)
	rev := func(req DeviceCertificateRevokeRequest) error {
		if req.AnchorToken == "" {
			req.AnchorToken = f.anchor.Token
		}
		_, err := f.api.RevokeDeviceCertificate(f.ctx, &req)
		return err
	}
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{}), "neither a certificate nor a fingerprint")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{CertificatePem: strp(leaf.pem()), FingerprintSha256: &fp}), "both")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{FingerprintSha256: strp("abc")}), "a short fingerprint")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{FingerprintSha256: strp(strings.Repeat("g", 64))}), "non-hex")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{CertificatePem: strp("not a certificate")}), "garbage PEM")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{CertificatePem: strp(leaf.pem() + f.ca.leaf(t, "SN-2").pem())}), "two certificates")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{FingerprintSha256: &fp, Reason: strp("BECAUSE")}), "an unknown reason")
	requireInvalid(t, rev(DeviceCertificateRevokeRequest{FingerprintSha256: strp(x509trust.Fingerprint(f.ca.cert))}),
		"the anchor's own certificate (disable the anchor instead)")
	err := rev(DeviceCertificateRevokeRequest{AnchorToken: "nope", FingerprintSha256: &fp})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "an unknown anchor")
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, "nope", fp)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, "short")
	requireInvalid(t, err, "unrevoke with a short fingerprint")
	c, _ := f.rowsCounter()
	assert.EqualValues(t, 0, c, "no refused revoke recorded anything")

	// The control: a good fingerprint with a reason is recorded, upper case folded.
	got, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: f.anchor.Token, FingerprintSha256: strp(strings.ToUpper(fp)), Reason: strp("KEY_COMPROMISE")})
	require.NoError(t, err)
	assert.Equal(t, fp, *got.Fingerprint)
	assert.Equal(t, "keyCompromise", got.Reason)
	assert.Equal(t, "KEY_COMPROMISE", RevocationReasonEnum(got.Reason))
}

func TestRevokeStopsAtThePerTenantCeiling(t *testing.T) {
	f := newRevFixture(t)
	for i := 0; i < 3; i++ {
		f.revoke(x509trust.Fingerprint(f.ca.leaf(t, fmt.Sprintf("SN-%d", i)).cert))
	}
	// The ceiling is a constant; fill the table to it directly rather than minting 100 000 certificates.
	rows := make([]DeviceRevokedCertificate, 0, MaxManualRevocationsPerTenant)
	for i := 3; i < MaxManualRevocationsPerTenant; i++ {
		fp := fmt.Sprintf("%064x", i)
		rows = append(rows, DeviceRevokedCertificate{TrustAnchorId: f.anchor.ID, Kind: RevokedKindManual,
			Fingerprint: &fp, Reason: "unspecified", RevokedAt: time.Now(), CreatedAt: time.Now()})
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).CreateInBatches(rows, 1000).Error)
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: f.anchor.Token, FingerprintSha256: strp(x509trust.Fingerprint(f.ca.leaf(t, "one-more").cert))})
	var lim *limit.Error
	require.ErrorAs(t, err, &lim, "the (ceiling+1)th revocation")
}

// 🔴 THE ENTRIES NO-OP. An anchor with nothing recorded changes NOTHING for an existing device:
// no revocation query runs at all, and the decision is the unrevoked chain, unchanged.
func TestEntriesAnchorWithNothingRecordedRunsNoQueryAndChangesNothing(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	v := f.verify(leaf, inter)

	n, stop := queryCounter(t, f.api.RDB.Database)
	defer stop()
	state, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), v.RevocationKeys(), time.Now())
	require.NoError(t, err)
	assert.Zero(t, n.Load(), "a query ran for an ENTRIES anchor that records nothing")
	assert.Equal(t, x509trust.ModeEntries, state.Mode, "the default mode is ENTRIES")
	assert.Equal(t, DefaultRevocationStaleGrace, state.StaleGrace)
	assert.Empty(t, state.RevokedFingerprints)
	assert.Empty(t, state.RevokedSerials)

	got, err := v.ApplyRevocation(state)
	require.NoError(t, err)
	assert.Equal(t, v.Chain, got.Chain, "the surviving path is the path VerifyClientChain picked")
	assert.Equal(t, v.NotAfter, got.NotAfter)
	assert.Equal(t, v.Fingerprint, got.Fingerprint)

	t.Run("the control: one manual entry and the query runs", func(t *testing.T) {
		f.revoke(x509trust.Fingerprint(f.ca.leaf(t, "someone-else").cert))
		before := n.Load()
		_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), v.RevocationKeys(), time.Now())
		require.NoError(t, err)
		assert.Equal(t, before+1, n.Load(), "one read for the entries of an ENTRIES anchor, and no list read")
		_, err = f.decide(v)
		assert.NoError(t, err, "an unrelated entry changes nothing for this device")
	})
}

// REQUIRE_CURRENT is decided from the anchor ROW, so the absence of rows cannot default the mode:
// no list at all is unknown, and the lists are read only in this mode.
func TestRequireCurrentIsDecidedFromTheAnchorRowNotFromTheAbsenceOfRows(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	v := f.verify(leaf)
	f.setMode("REQUIRE_CURRENT")

	n, stop := queryCounter(t, f.api.RDB.Database)
	defer stop()
	_, err := f.decide(v)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err), "no list under REQUIRE_CURRENT")
	assert.EqualValues(t, 2, n.Load(), "one read for the entries and one for the lists, and no more")

	issuer := x509trust.IssuerIDOfCA(f.ca.cert)
	add := func(next time.Time) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("trust_anchor_id = ?", f.anchor.ID).Delete(&DeviceRevocationList{}).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceRevocationList{
			TrustAnchorId: f.anchor.ID, RevocationIssuerId: 1, IssuerKey: issuer.Key, IssuerName: issuer.Name,
			IssuerCertificatePem: f.ca.pem(), IssuerSubject: "CN=Maker Root", CrlNumber: "1",
			ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: next, EntryCount: 0, CrlSha256: strings.Repeat("a", 64),
			Source: RevocationSourceUpload}).Error)
	}
	add(time.Now().Add(time.Hour))
	_, err = f.decide(v)
	assert.NoError(t, err, "a current list admits")
	add(time.Now().Add(-DefaultRevocationStaleGrace - time.Minute))
	_, err = f.decide(v)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err), "a list past nextUpdate + grace")
	add(time.Now().Add(-time.Hour))
	_, err = f.decide(v)
	assert.NoError(t, err, "past nextUpdate but inside the grace")

	t.Run("a configured grace is the grace", func(t *testing.T) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Where("id = ?", f.anchor.ID).
			Update("revocation_stale_grace", 0).Error)
		_, err := f.decide(v)
		require.Error(t, err, "a zero grace and a list an hour stale")
		assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err))
	})
	t.Run("the same state under ENTRIES admits", func(t *testing.T) {
		f.setMode("ENTRIES")
		_, err := f.decide(v)
		assert.NoError(t, err)
	})
}

// 🔴 FAIL CLOSED. Every reading problem is an error, never a pass.
func TestRevocationLookupFailsClosed(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	v := f.verify(leaf)
	keys := v.RevocationKeys()
	now := time.Now()
	f.revoke(x509trust.Fingerprint(f.ca.leaf(t, "other").cert)) // so the query runs under ENTRIES

	t.Run("the control", func(t *testing.T) {
		_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), keys, now)
		require.NoError(t, err)
	})
	t.Run("no tenant in the context", func(t *testing.T) {
		_, err := f.api.ResolveX509Revocation(context.Background(), f.freshAnchor(), keys, now)
		require.Error(t, err)
	})
	t.Run("no anchor", func(t *testing.T) {
		_, err := f.api.ResolveX509Revocation(f.ctx, nil, keys, now)
		require.Error(t, err)
		_, err = f.api.ResolveX509Revocation(f.ctx, &DeviceTrustAnchor{}, keys, now)
		require.Error(t, err, "an anchor row with no id")
	})
	t.Run("an anchor of another tenant", func(t *testing.T) {
		other := putest.TenantContext("someone-else")()
		_, err := f.api.ResolveX509Revocation(other, f.freshAnchor(), keys, now)
		require.Error(t, err, "the context's tenant does not own the anchor")
	})
	t.Run("an unknown stored mode", func(t *testing.T) {
		f.setMode("TRUST_ME")
		_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), keys, now)
		require.Error(t, err, "a mode this build does not know is refused, not defaulted")
		f.setMode("ENTRIES")
	})
	t.Run("a stored grace out of range", func(t *testing.T) {
		for _, secs := range []int64{-1, int64(MaxRevocationStaleGrace/time.Second) + 1} {
			require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Where("id = ?", f.anchor.ID).
				Update("revocation_stale_grace", secs).Error)
			_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), keys, now)
			require.Error(t, err, "grace %d", secs)
		}
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Where("id = ?", f.anchor.ID).
			Update("revocation_stale_grace", nil).Error)
	})
	t.Run("a negative row counter reads as having rows", func(t *testing.T) {
		a := f.freshAnchor()
		a.RevocationRows = sql.NullInt64{Int64: -1, Valid: true}
		assert.True(t, a.HasRevocationRows(), "the fast path that skips the query must not be reachable by a bad count")
	})
	t.Run("the table cannot be read", func(t *testing.T) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Migrator().RenameTable("device_revoked_certificates", "device_revoked_certificates_x"))
		_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), keys, now)
		require.Error(t, err, "a database error must refuse, never read as 'not revoked'")
		require.NoError(t, f.api.RDB.DB(f.ctx).Migrator().RenameTable("device_revoked_certificates_x", "device_revoked_certificates"))
		_, err = f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), keys, now)
		require.NoError(t, err, "the control: restored")
	})
	t.Run("more identities than a verified path can hold", func(t *testing.T) {
		big := x509trust.RevocationQuery{}
		for i := 0; i <= maxRevocationLookupKeys; i++ {
			big.Fingerprints = append(big.Fingerprints, fmt.Sprintf("%064x", i))
		}
		_, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), big, now)
		require.Error(t, err)
	})
}

// 🔴 TENANT AND ANCHOR ISOLATION OF THE READ, EXPLICIT. Each case plants a row the read must not
// see and asserts the answer, with the control (the same row on the right anchor) refusing.
func TestRevocationReadIsIsolatedByTenantAndByAnchor(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	v := f.verify(leaf)
	fp := x509trust.Fingerprint(leaf.cert)
	f.revoke(x509trust.Fingerprint(f.ca.leaf(t, "other").cert)) // the query must run under ENTRIES

	t.Run("another tenant's anchor of the same CA", func(t *testing.T) {
		other := putest.TenantContext("someone-else")()
		oa, err := f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: "maker", CertificatePem: f.ca.pem()})
		require.NoError(t, err)
		_, err = f.api.RevokeDeviceCertificate(other, &DeviceCertificateRevokeRequest{AnchorToken: "maker", FingerprintSha256: &fp})
		require.NoError(t, err)
		_, err = f.decide(v)
		assert.NoError(t, err, "tenant B revoking the certificate under its own anchor does not refuse tenant A's connect")
		oa, err = f.api.trustAnchorByToken(other, "maker")
		require.NoError(t, err)
		state, err := f.api.ResolveX509Revocation(other, oa, v.RevocationKeys(), time.Now())
		require.NoError(t, err)
		assert.Contains(t, state.RevokedFingerprints, fp, "the control: B's own read sees B's entry")
	})
	t.Run("a second anchor of the same tenant", func(t *testing.T) {
		ca2 := newRootCA(t, "Second Root")
		_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "second", CertificatePem: ca2.pem()})
		require.NoError(t, err)
		_, err = f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: "second", FingerprintSha256: &fp})
		require.NoError(t, err)
		_, err = f.decide(v)
		assert.NoError(t, err, "an entry under another anchor does not apply under this one")
	})
	t.Run("a current list for the same issuer under another anchor is not coverage here", func(t *testing.T) {
		// REQUIRE_CURRENT on this anchor; the only list for the issuer belongs to a DIFFERENT
		// anchor of the same tenant (the same CA trusted twice). It must not make this anchor current.
		f.setMode("REQUIRE_CURRENT")
		defer f.setMode("ENTRIES")
		issuer := x509trust.IssuerIDOfCA(f.ca.cert)
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceRevocationList{
			TrustAnchorId: f.anchor.ID + 5000, RevocationIssuerId: 1, IssuerKey: issuer.Key, IssuerName: issuer.Name,
			IssuerCertificatePem: f.ca.pem(), IssuerSubject: "CN=Maker Root", CrlNumber: "1",
			ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(time.Hour), EntryCount: 0,
			CrlSha256: strings.Repeat("b", 64), Source: RevocationSourceUpload}).Error)
		_, err := f.decide(v)
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err),
			"another anchor's list was taken for this anchor's coverage: %v", err)
	})
	t.Run("the same certificate revoked under two anchors is two entries, and each is removed alone", func(t *testing.T) {
		ca2 := newRootCA(t, "Third Root")
		_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "third", CertificatePem: ca2.pem()})
		require.NoError(t, err)
		shared := x509trust.Fingerprint(f.ca.leaf(t, "SN-SHARED").cert)
		a, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: "maker", FingerprintSha256: &shared})
		require.NoError(t, err)
		b, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: "third", FingerprintSha256: &shared})
		require.NoError(t, err)
		assert.NotEqual(t, a.ID, b.ID, "revoking under the second anchor returned the first anchor's entry")
		assert.NotEqual(t, a.TrustAnchorId, b.TrustAnchorId)
		ok, err := f.api.UnrevokeDeviceCertificate(f.ctx, "third", shared)
		require.NoError(t, err)
		assert.True(t, ok)
		res, err := f.api.DeviceRevokedCertificates(f.ctx, "maker", DeviceRevokedCertificateSearchCriteria{})
		require.NoError(t, err)
		found := false
		for _, r := range res.Results {
			found = found || (r.Fingerprint != nil && *r.Fingerprint == shared)
		}
		assert.True(t, found, "unrevoking under one anchor removed the other anchor's entry")
	})
	t.Run("a row stamped with another tenant under this anchor is not read", func(t *testing.T) {
		sys := core.WithSystemContext(f.ctx)
		stray := fmt.Sprintf("%064x", 7)
		require.NoError(t, f.api.RDB.DB(sys).Create(&DeviceRevokedCertificate{TenantId: "someone-else", TrustAnchorId: f.anchor.ID,
			Kind: RevokedKindManual, Fingerprint: &stray, Reason: "unspecified", RevokedAt: time.Now(), CreatedAt: time.Now()}).Error)
		// Under a system context the rdb callback adds no predicate, so only the explicit
		// predicates keep this row out.
		strayKeys := x509trust.RevocationQuery{Fingerprints: []string{stray}}
		state, err := f.api.ResolveX509Revocation(sys, f.freshAnchor(), strayKeys, time.Now())
		require.NoError(t, err)
		assert.NotContains(t, state.RevokedFingerprints, stray, "another tenant's row was read")
	})
	t.Run("a row of another anchor of this tenant is not read", func(t *testing.T) {
		sys := core.WithSystemContext(f.ctx)
		stray := fmt.Sprintf("%064x", 8)
		require.NoError(t, f.api.RDB.DB(sys).Create(&DeviceRevokedCertificate{TenantId: partialUpdateTenant, TrustAnchorId: f.anchor.ID + 1000,
			Kind: RevokedKindManual, Fingerprint: &stray, Reason: "unspecified", RevokedAt: time.Now(), CreatedAt: time.Now()}).Error)
		state, err := f.api.ResolveX509Revocation(sys, f.freshAnchor(), x509trust.RevocationQuery{Fingerprints: []string{stray}}, time.Now())
		require.NoError(t, err)
		assert.NotContains(t, state.RevokedFingerprints, stray, "a row of another anchor was read")
	})
	t.Run("the search lists only this anchor's entries", func(t *testing.T) {
		res, err := f.api.DeviceRevokedCertificates(f.ctx, f.anchor.Token, DeviceRevokedCertificateSearchCriteria{})
		require.NoError(t, err)
		require.Len(t, res.Results, 2, "the two entries recorded under this anchor")
		for _, r := range res.Results {
			assert.Equal(t, f.anchor.ID, r.TrustAnchorId)
		}
		_, err = f.api.DeviceRevokedCertificates(f.ctx, "nope", DeviceRevokedCertificateSearchCriteria{})
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		bad := "CRLISH"
		_, err = f.api.DeviceRevokedCertificates(f.ctx, f.anchor.Token, DeviceRevokedCertificateSearchCriteria{Kind: &bad})
		requireInvalid(t, err, "an unknown kind")
	})
}

// A stored row the read cannot interpret is an error: never skipped into a pass.
func TestRevocationReadRefusesAMalformedStoredRow(t *testing.T) {
	f := newRevFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	v := f.verify(leaf)
	fp := x509trust.Fingerprint(leaf.cert)
	sys := core.WithSystemContext(f.ctx)
	require.NoError(t, f.api.RDB.DB(sys).Create(&DeviceRevokedCertificate{TenantId: partialUpdateTenant, TrustAnchorId: f.anchor.ID,
		Kind: "WEIRD", Fingerprint: &fp, Reason: "unspecified", RevokedAt: time.Now(), CreatedAt: time.Now()}).Error)
	require.NoError(t, bumpRevocationRows(f.api.RDB.DB(f.ctx), f.anchor.ID, 1))
	// The kind column is the guard here: the read only selects MANUAL and CRL, so a WEIRD row is
	// invisible to it, which is the safe reading; a MANUAL row with no fingerprint cannot match.
	_, err := f.decide(v)
	assert.NoError(t, err)
	require.NoError(t, f.api.RDB.DB(sys).Exec(`UPDATE device_revoked_certificates SET kind = 'CRL' WHERE kind = 'WEIRD'`).Error)
	_, err = f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), x509trust.RevocationQuery{
		Serials: []x509trust.IssuerSerial{{Issuer: x509trust.IssuerID{Key: "k", Name: "n"}, Serial: "1"}}}, time.Now())
	assert.NoError(t, err, "a CRL row that does not match the asked serial is simply not selected")
}

// The lookup is never cached: CachedApi embeds *Api and PROMOTES ResolveX509Revocation. A method
// declared on CachedApi would put a cache in front of a revocation.
func TestResolveX509RevocationIsPromotedNotOverridden(t *testing.T) {
	m, ok := reflect.TypeOf(&CachedApi{}).MethodByName("ResolveX509Revocation")
	require.True(t, ok, "CachedApi must carry ResolveX509Revocation (promoted from Api)")
	fn := runtime.FuncForPC(m.Func.Pointer())
	file, _ := fn.FileLine(fn.Entry())
	assert.Equal(t, "<autogenerated>", file, "CachedApi declares its own ResolveX509Revocation (%s): a cache in front of a revocation", file)

	d, ok := reflect.TypeOf(&CachedApi{}).MethodByName("AuthenticateDevice")
	require.True(t, ok)
	dfile, _ := runtime.FuncForPC(d.Func.Pointer()).FileLine(runtime.FuncForPC(d.Func.Pointer()).Entry())
	assert.NotEqual(t, "<autogenerated>", dfile, "the control must be able to tell a declared method from a promoted one")

	// And the interface the callout holds carries it, so nothing can route around Api.
	_, ok = reflect.TypeOf((*DeviceManagementApi)(nil)).Elem().MethodByName("ResolveX509Revocation")
	assert.True(t, ok)
}

// ─── chain links ────────────────────────────────────────────────────────────

func (f *revFixture) links(credentialId uint) []DeviceCredentialChainLink {
	f.t.Helper()
	var out []DeviceCredentialChainLink
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", credentialId).Order("depth").Find(&out).Error)
	return out
}

func TestCredentialCreateWritesTheChainLinksOfItsPath(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	cred, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem()+inter.pem(), "maker"))
	require.NoError(t, err)

	links := f.links(cred.ID)
	require.Len(t, links, 2, "the leaf and the intermediate; the anchor is not a link")
	assert.Equal(t, partialUpdateTenant, links[0].TenantId, "the tenant is stamped")
	assert.Equal(t, f.anchor.ID, links[0].TrustAnchorId)
	assert.EqualValues(t, 0, links[0].Depth)
	assert.Equal(t, x509trust.Fingerprint(leaf.cert), links[0].Fingerprint)
	serial, _ := x509trust.SerialHex(leaf.cert.SerialNumber)
	assert.Equal(t, serial, links[0].Serial)
	require.NotNil(t, links[0].IssuerKey)
	assert.Equal(t, x509trust.IssuerIDOfCA(inter.cert), x509trust.IssuerID{Key: *links[0].IssuerKey, Name: links[0].IssuerName})
	assert.EqualValues(t, 1, links[1].Depth)
	assert.Equal(t, x509trust.Fingerprint(inter.cert), links[1].Fingerprint)
	assert.Equal(t, x509trust.IssuerIDOfCA(f.ca.cert), x509trust.IssuerID{Key: *links[1].IssuerKey, Name: links[1].IssuerName})

	t.Run("a refused create writes none", func(t *testing.T) {
		before := int64(0)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Count(&before).Error)
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-bad", "dev", newRootCA(t, "Stranger").leaf(t, "x").pem(), "maker"))
		require.Error(t, err)
		var after int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Count(&after).Error)
		assert.Equal(t, before, after)
	})
}

func TestCertificateRegistrationWritesLinksFromTheSurvivingPath(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	reg := f.registration(leaf, inter)
	res, err := f.register509("pp-x", reg)
	require.NoError(t, err)
	require.Equal(t, ProvisionCreated, res.Outcome)

	var cred DeviceCredential
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", x509trust.Fingerprint(leaf.cert)).First(&cred).Error)
	var links []DeviceCredentialChainLink
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", cred.ID).Order("depth").Find(&links).Error)
	require.Len(t, links, 2)
	assert.Equal(t, x509trust.Fingerprint(leaf.cert), links[0].Fingerprint)
	assert.Equal(t, x509trust.Fingerprint(inter.cert), links[1].Fingerprint)
}

func TestLegacyCredentialsGetADepthZeroLinkWithNoIssuerKey(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	cred, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem()+inter.pem(), "maker"))
	require.NoError(t, err)
	// A credential created before links existed has none.
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", cred.ID).Delete(&DeviceCredentialChainLink{}).Error)

	// A foreign tenant's legacy credential must be left alone by this tenant's reconciler.
	other := putest.TenantContext("someone-else")()
	oca := newRootCA(t, "Other Root")
	_, err = f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	_, err = f.api.CreateDevice(other, &DeviceCreateRequest{Token: "odev", DeviceTypeToken: "dt-a"})
	require.NoError(t, err)
	_, err = f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: "o", CertificatePem: oca.pem()})
	require.NoError(t, err)
	ocred, err := f.api.CreateDeviceCredential(other, x509Create("oc", "odev", oca.leaf(t, "OSN").pem(), "o"))
	require.NoError(t, err)
	require.NoError(t, f.api.RDB.DB(other).Where("credential_id = ?", ocred.ID).Delete(&DeviceCredentialChainLink{}).Error)

	n, err := f.api.ReconcileLegacyChainLinks(f.ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	links := f.links(cred.ID)
	require.Len(t, links, 1, "only the leaf can be derived from the stored certificate")
	assert.EqualValues(t, 0, links[0].Depth)
	assert.Nil(t, links[0].IssuerKey, "the issuer's key is not in the leaf")
	assert.Equal(t, x509trust.IssuerNameHash(leaf.cert), links[0].IssuerName)
	assert.Equal(t, x509trust.Fingerprint(leaf.cert), links[0].Fingerprint)
	var otherLinks int64
	require.NoError(t, f.api.RDB.DB(other).Model(&DeviceCredentialChainLink{}).Where("credential_id = ?", ocred.ID).Count(&otherLinks).Error)
	assert.Zero(t, otherLinks, "another tenant's credential was reconciled")

	n, err = f.api.ReconcileLegacyChainLinks(f.ctx, 0)
	require.NoError(t, err)
	assert.Zero(t, n, "idempotent: nothing left to do")
	require.Len(t, f.links(cred.ID), 1)

	t.Run("a credential whose leaf cannot be read does not stop the others or spin", func(t *testing.T) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", cred.ID).Delete(&DeviceCredentialChainLink{}).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).
			Update("certificate_pem", "garbage").Error)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := f.api.ReconcileLegacyChainLinks(f.ctx, 0)
			assert.NoError(t, err)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the reconciler spun on an unreadable leaf")
		}
		assert.Empty(t, f.links(cred.ID))
	})
	t.Run("no tenant in the context", func(t *testing.T) {
		_, err := f.api.ReconcileLegacyChainLinks(context.Background(), 0)
		require.Error(t, err)
	})
}

// ─── the recheck under the registration lock ────────────────────────────────

// 🔑 A REGISTRATION RE-RUNS THE REVOCATION CHECK INSIDE ITS TRANSACTION. The gate (the callout's)
// passed the chain; a revocation then committed; the registration is refused with NOTHING written.
func TestCertificateRegistrationRechecksRevocationUnderTheLock(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	twin := inter.leaf(t, "SN-2")
	reg := f.registration(leaf, inter) // verified, and passing the gate
	treg := f.registration(twin, inter)

	// The revocation commits after the gate and before the registration.
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(leaf.cert))})
	require.NoError(t, err)

	before := f.counts()
	_, err = f.register509("pp-x", reg)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err), "refused as revoked, not as a generic failure: %v", err)
	assert.Equal(t, before, f.counts(), "a refused registration wrote rows")
	var links int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Count(&links).Error)
	assert.Zero(t, links)

	res, err := f.register509("pp-x", treg)
	require.NoError(t, err, "the control: the unrevoked twin registers")
	assert.Equal(t, ProvisionCreated, res.Outcome)

	t.Run("an intermediate revoked refuses the registration too", func(t *testing.T) {
		_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
			AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
		require.NoError(t, err)
		before := f.counts()
		_, err = f.register509("pp-x", f.registration(inter.leaf(t, "SN-3"), inter))
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
		assert.Equal(t, before, f.counts())
	})
	t.Run("REQUIRE_CURRENT with no list refuses it as unknown", func(t *testing.T) {
		_, err := f.api.UnrevokeDeviceCertificate(f.ctx, "maker", x509trust.Fingerprint(inter.cert))
		require.NoError(t, err)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceTrustAnchor{}).Where("id = ?", f.anchor.ID).
			Update("revocation_mode", "REQUIRE_CURRENT").Error)
		before := f.counts()
		_, err = f.register509("pp-x", f.registration(inter.leaf(t, "SN-4"), inter))
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err))
		assert.Equal(t, before, f.counts())
	})
}

func TestCertificateRegistrationRefusesWhenTheRecheckCannotRead(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	leaf := f.ca.leaf(t, "SN-1")
	reg := f.registration(leaf)
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(f.ca.leaf(t, "other").cert))})
	require.NoError(t, err)
	require.NoError(t, f.api.RDB.DB(f.ctx).Migrator().RenameTable("device_revoked_certificates", "device_revoked_certificates_x"))
	before := f.counts()
	_, err = f.register509("pp-x", reg)
	require.Error(t, err, "a recheck that cannot read must refuse the registration")
	assert.Empty(t, x509trust.ReasonOf(err), "an infrastructure error is not a classified revocation")
	assert.Equal(t, before, f.counts())
	require.NoError(t, f.api.RDB.DB(f.ctx).Migrator().RenameTable("device_revoked_certificates_x", "device_revoked_certificates"))
	_, err = f.register509("pp-x", reg)
	require.NoError(t, err, "the control: restored, it registers")
}

// ─── deleting an anchor ─────────────────────────────────────────────────────

func TestDeletingAnAnchorThatHoldsRevocationsIsRefused(t *testing.T) {
	f := newRevFixture(t)
	fp := x509trust.Fingerprint(f.ca.leaf(t, "SN-1").cert)
	f.revoke(fp)
	_, err := f.api.DeleteDeviceTrustAnchor(f.ctx, f.anchor.Token)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEntityInUse)
	_, err = f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, fp)
	require.NoError(t, err)
	deleted, err := f.api.DeleteDeviceTrustAnchor(f.ctx, f.anchor.Token)
	require.NoError(t, err, "the control: with nothing recorded it deletes")
	assert.True(t, deleted)
}

// The un-revoke is journaled with the row it removed: its id and the certificate's fingerprint.
func TestUnrevokeJournalNamesTheRowAndTheFingerprint(t *testing.T) {
	f := newRevFixture(t)
	fp := x509trust.Fingerprint(f.ca.leaf(t, "SN-1").cert)
	entry := f.revoke(fp)
	ok, err := f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, fp)
	require.NoError(t, err)
	require.True(t, ok)

	var rows []rdb.AuditEvent
	require.NoError(t, f.api.RDB.DB(core.WithSystemContext(context.Background())).
		Where("table_name = ? AND operation = ?", "device_revoked_certificates", "delete").Find(&rows).Error)
	require.Len(t, rows, 1, "the un-revoke must be journaled")
	assert.Equal(t, fmt.Sprint(entry.ID), rows[0].EntityPK, "the journal must carry the removed row's id")
	assert.Equal(t, fp, rows[0].EntityLabel, "the journal must carry the fingerprint label")
}

// 🔴 A REVOCATION IS ENFORCED ONLY UNDER THE ANCHOR IT IS RECORDED FOR. Recording one under an
// anchor that does not enforce a certificate the tenant actually uses would "succeed" and strand
// the devices: it is refused, naming the anchor(s) that would enforce it.
func TestRevokeRefusesACertificateUsedOnlyUnderAnotherAnchor(t *testing.T) {
	f := newRevFixture(t) // anchor "maker" = f.ca (the root)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "sub", CertificatePem: inter.pem()})
	require.NoError(t, err)
	fpOf := func(n *pkiNode) string { return x509trust.Fingerprint(n.cert) }
	revokeUnder := func(anchor, fp string) error {
		_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: anchor, FingerprintSha256: &fp})
		return err
	}

	t.Run("a leaf pinned under the intermediate-anchored profile", func(t *testing.T) {
		leaf := inter.leaf(t, "SN-1")
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem(), "sub"))
		require.NoError(t, err)
		err = revokeUnder("maker", fpOf(leaf))
		requireInvalid(t, err, "revoking a leaf pinned under another anchor")
		assert.Contains(t, err.Error(), `"sub"`, "the refusal must name the anchor that would enforce it")
		c, _ := f.rowsCounter()
		assert.Zero(t, c, "a refused revoke recorded nothing")
		require.NoError(t, revokeUnder("sub", fpOf(leaf)), "the counterweight: under the anchor that enforces it")
	})
	t.Run("an intermediate on the recorded chain of a credential under another anchor", func(t *testing.T) {
		line2 := newPki(t, f.ca, pkiOpts{cn: "Line CA 2", ca: true})
		leaf := line2.leaf(t, "SN-2")
		f.device("dev2", "dev2-ext", "dt-a", "")
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-2", "dev2", leaf.pem()+line2.pem(), "maker"))
		require.NoError(t, err)
		other := newRootCA(t, "Other Root")
		_, err = f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "other", CertificatePem: other.pem()})
		require.NoError(t, err)
		err = revokeUnder("other", fpOf(line2))
		requireInvalid(t, err, "revoking an intermediate that appears only on another anchor's chains")
		assert.Contains(t, err.Error(), `"maker"`)
		require.NoError(t, revokeUnder("maker", fpOf(line2)), "the counterweight: under the anchor whose chains carry it")
	})
	t.Run("a pinned credential with no links (and no stored leaf to link) is still found", func(t *testing.T) {
		leaf := inter.leaf(t, "SN-BARE")
		f.device("dev3", "dev3-ext", "dt-a", "")
		cred, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-3", "dev3", leaf.pem(), "sub"))
		require.NoError(t, err)
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", cred.ID).Delete(&DeviceCredentialChainLink{}).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).Update("certificate_pem", nil).Error)
		err = revokeUnder("maker", fpOf(leaf))
		requireInvalid(t, err, "the pin itself, not only its links, names the anchor")
		assert.Contains(t, err.Error(), `"sub"`)
	})
	t.Run("a fingerprint the named anchor also enforces is revoked", func(t *testing.T) {
		shared := x509trust.Fingerprint(f.ca.leaf(t, "SN-BOTH").cert)
		maker, err := f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		sub, err := f.api.trustAnchorByToken(f.ctx, "sub")
		require.NoError(t, err)
		for i, a := range []*DeviceTrustAnchor{maker, sub} {
			require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceCredentialChainLink{CredentialId: uint(9000 + i), Depth: 1,
				TrustAnchorId: a.ID, Fingerprint: shared, IssuerName: "n", Serial: "1"}).Error)
		}
		require.NoError(t, revokeUnder("maker", shared), "it is on chains under the named anchor too, so the revocation is enforced where it is recorded")
	})
	t.Run("a fingerprint nothing uses is revoked under any anchor", func(t *testing.T) {
		require.NoError(t, revokeUnder("other", fpOf(f.ca.leaf(t, "SN-UNUSED"))))
	})
}
