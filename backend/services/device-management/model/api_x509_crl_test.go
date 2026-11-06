// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// CRL UPLOAD, REPLACE AND DELETE over SQLite: what lands in the tables, what the connect check
// then decides, and every rule the store (as opposed to the parser, which x509trust tests) owns.
// The locks and the races are the integration tests' (x509_crl_integration_test.go).

func serialOf(c *x509.Certificate) *big.Int { return c.SerialNumber }

// put uploads a CRL signed by signer under the fixture's anchor. chain is the issuer chain (the
// signer first); empty means the anchor signed it.
func (f *revFixture) put(crl []byte, chain ...*pkiNode) (*RevocationListPutResult, error) {
	f.t.Helper()
	var pemChain string
	for _, c := range chain {
		pemChain += c.pem()
	}
	return f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{
		AnchorToken: f.anchor.Token, Crl: crl, IssuerChainPem: pemChain})
}

func (f *revFixture) mustPut(crl []byte, chain ...*pkiNode) *RevocationListPutResult {
	f.t.Helper()
	res, err := f.put(crl, chain...)
	require.NoError(f.t, err)
	return res
}

func (f *revFixture) admits(v *x509trust.Verified) bool {
	f.t.Helper()
	_, err := f.decide(v)
	return err == nil
}

func (f *revFixture) listRows() (lists, entries, issuers int64) {
	f.t.Helper()
	db := f.api.RDB.DB(f.ctx)
	require.NoError(f.t, db.Unscoped().Model(&DeviceRevocationList{}).Count(&lists).Error)
	require.NoError(f.t, db.Model(&DeviceRevokedCertificate{}).Where("kind = ?", RevokedKindCRL).Count(&entries).Error)
	require.NoError(f.t, db.Model(&DeviceRevocationIssuer{}).Count(&issuers).Error)
	return
}

func requireRule(t *testing.T, err error, rule string) {
	t.Helper()
	require.Error(t, err)
	requireInvalid(t, err, "a refused CRL")
	assert.Contains(t, err.Error(), "revocation list refused: "+rule)
}

// 🔑 A STORED CRL REFUSES THE CERTIFICATES IT NAMES AND ONLY THEM; the control is the same chain
// before the upload.
func TestPutRevocationListRefusesTheListedCertificatesAndOnlyThose(t *testing.T) {
	f := newRevFixture(t)
	listed, sibling := f.ca.leaf(t, "SN-1"), f.ca.leaf(t, "SN-2")
	vl, vs := f.verify(listed), f.verify(sibling)
	require.True(t, f.admits(vl), "the control: nothing is listed yet")

	res := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(7), revoked: []crlRevoked{{serial: serialOf(listed.cert), reason: 1}}}))
	assert.Equal(t, 1, res.NewlyRevoked)
	assert.False(t, res.Replaced)
	assert.False(t, res.Unchanged)
	assert.Equal(t, "7", res.List.CrlNumber)
	assert.EqualValues(t, 1, res.List.EntryCount)
	assert.Equal(t, RevocationSourceUpload, res.List.Source)
	assert.Equal(t, partialUpdateTenant, res.List.TenantId)
	assert.Equal(t, f.anchor.ID, res.List.TrustAnchorId)

	_, err := f.decide(vl)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
	assert.True(t, f.admits(vs), "a certificate the list does not name is untouched")

	counter, actual := f.rowsCounter()
	assert.EqualValues(t, 2, actual, "one list and one entry")
	assert.EqualValues(t, 1, counter, "the counter counts lists and manual entries, not CRL entries")

	var entry DeviceRevokedCertificate
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("kind = ?", RevokedKindCRL).First(&entry).Error)
	assert.Equal(t, res.List.ID, *entry.RevocationListId)
	assert.Equal(t, string(x509trust.RevocationKeyCompromise), entry.Reason)
	assert.Equal(t, partialUpdateTenant, entry.TenantId)
	ser, _ := x509trust.SerialHex(listed.cert.SerialNumber)
	assert.Equal(t, ser, *entry.Serial)
}

// Replacing diffs against the previous list: kept entries are untouched, dropped ones un-revoke,
// new ones count as newly revoked, and a changed reason is not a new revocation.
func TestPutRevocationListReplaceDiffsAgainstThePreviousList(t *testing.T) {
	f := newRevFixture(t)
	a, b, c := f.ca.leaf(t, "SN-A"), f.ca.leaf(t, "SN-B"), f.ca.leaf(t, "SN-C")
	va, vb, vc := f.verify(a), f.verify(b), f.verify(c)

	first := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: []crlRevoked{
		{serial: serialOf(a.cert), reason: 1}, {serial: serialOf(b.cert), reason: 6}}}))
	assert.Equal(t, 2, first.NewlyRevoked)
	require.False(t, f.admits(va))
	require.False(t, f.admits(vb))
	require.True(t, f.admits(vc))
	var kept DeviceRevokedCertificate
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("kind = ?", RevokedKindCRL).Order("id").First(&kept).Error)

	// 2: A stays (same reason), B is released (certificateHold lifted), C is new, and the list's
	// own row is the same row.
	second := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: []crlRevoked{
		{serial: serialOf(a.cert), reason: 1}, {serial: serialOf(c.cert), reason: 5}}}))
	assert.True(t, second.Replaced)
	assert.Equal(t, 1, second.NewlyRevoked, "only C is new")
	assert.Equal(t, 1, second.Removed, "B was dropped")
	assert.Equal(t, first.List.ID, second.List.ID, "the issuer's list is replaced in place")
	assert.False(t, f.admits(va))
	assert.True(t, f.admits(vb), "a certificate the new list drops is admitted again")
	assert.False(t, f.admits(vc))
	var again DeviceRevokedCertificate
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("id = ?", kept.ID).First(&again).Error)
	assert.Equal(t, kept.Serial, again.Serial, "a kept entry is not rewritten")

	// 3: the same certificate with another reason is updated, not newly revoked.
	third := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(3), revoked: []crlRevoked{
		{serial: serialOf(a.cert), reason: 3}, {serial: serialOf(c.cert), reason: 5}}}))
	assert.Equal(t, 0, third.NewlyRevoked)
	var changed DeviceRevokedCertificate
	ser, _ := x509trust.SerialHex(a.cert.SerialNumber)
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("kind = ? AND serial = ?", RevokedKindCRL, ser).First(&changed).Error)
	assert.Equal(t, string(x509trust.RevocationAffiliationChanged), changed.Reason)

	lists, entries, issuers := f.listRows()
	assert.EqualValues(t, 1, lists)
	assert.EqualValues(t, 2, entries)
	assert.EqualValues(t, 1, issuers)
	counter, _ := f.rowsCounter()
	assert.EqualValues(t, 1, counter, "replacing never moves the counter")
}

// The very CRL already in force is a no-op: nothing is written, whatever the high-water mark.
func TestPutRevocationListIsIdempotentOnTheSameCrl(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	crl := f.ca.crl(t, crlSpec{number: big.NewInt(4), revoked: revokedSerials(serialOf(a.cert))})
	first := f.mustPut(crl)
	before := first.List.UpdatedAt

	second := f.mustPut(crl)
	assert.True(t, second.Unchanged)
	assert.Equal(t, 0, second.NewlyRevoked)
	assert.Equal(t, first.List.ID, second.List.ID)
	assert.True(t, second.List.UpdatedAt.Equal(before), "nothing was written")
	counter, _ := f.rowsCounter()
	assert.EqualValues(t, 1, counter)
}

// 🔑 ANTI-ROLLBACK: a list whose number is not above the issuer's highest accepted one is refused,
// and the refusal changes nothing.
func TestPutRevocationListRefusesARollback(t *testing.T) {
	f := newRevFixture(t)
	a, b := f.ca.leaf(t, "SN-A"), f.ca.leaf(t, "SN-B")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(10), revoked: revokedSerials(serialOf(a.cert))}))

	for _, tc := range []struct {
		name   string
		number int64
	}{{"older", 9}, {"equal number, different list", 10}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(tc.number), revoked: revokedSerials(serialOf(b.cert))}))
			requireRule(t, err, RuleCrlNumberNotNewer)
			assert.True(t, f.admits(f.verify(b)), "the refused list changed nothing")
			assert.False(t, f.admits(f.verify(a)), "the list in force is still in force")
		})
	}
	t.Run("a higher number is accepted", func(t *testing.T) {
		res, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(11), revoked: revokedSerials(serialOf(b.cert))}))
		require.NoError(t, err)
		assert.Equal(t, "11", res.List.CrlNumber)
	})
}

// 🔑 THE HIGH-WATER MARK SURVIVES DELETING THE LIST, so deleting and re-uploading an older CRL is
// still a rollback. The control is a newer one, which is accepted.
func TestDeleteThenReuploadAnOlderListIsStillARollback(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(5), revoked: revokedSerials(serialOf(a.cert))}))
	is := x509trust.IssuerIDOfCA(f.ca.cert)

	deleted, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, f.admits(f.verify(a)), "the deleted list no longer refuses")

	older := f.ca.crl(t, crlSpec{number: big.NewInt(4)})
	_, err = f.put(older)
	requireRule(t, err, RuleCrlNumberNotNewer)
	_, err = f.put(f.ca.crl(t, crlSpec{number: big.NewInt(5)}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	lists, _, issuers := f.listRows()
	assert.EqualValues(t, 1, lists, "only the soft-deleted list remains")
	assert.EqualValues(t, 1, issuers, "the issuer row (and its mark) outlives the list")

	res, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(6), revoked: revokedSerials(serialOf(a.cert))}))
	require.NoError(t, err, "the control: a newer list after a delete is accepted")
	assert.False(t, res.Replaced, "the deleted list is not resurrected")
	assert.False(t, f.admits(f.verify(a)))
	counter, _ := f.rowsCounter()
	assert.EqualValues(t, 1, counter, "delete took the counter to 0, the new list back to 1")
}

// 🔑 CARRY-OVER (a): A DELETED LIST'S ENTRIES ARE NOT ENFORCED. The connect check reads entries by
// issuer and serial without asking whether their list still exists, so delete must remove them.
func TestDeletingAListStopsEnforcingItsEntries(t *testing.T) {
	f := newRevFixture(t)
	a, b := f.ca.leaf(t, "SN-A"), f.ca.leaf(t, "SN-B")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert), serialOf(b.cert))}))
	require.False(t, f.admits(f.verify(a)))
	require.False(t, f.admits(f.verify(b)))
	fp := x509trust.Fingerprint(a.cert)
	f.revoke(fp) // a manual entry for A: it must survive the delete

	is := x509trust.IssuerIDOfCA(f.ca.cert)
	deleted, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)
	assert.True(t, deleted)

	assert.True(t, f.admits(f.verify(b)), "B was refused only by the deleted list")
	assert.False(t, f.admits(f.verify(a)), "A is still refused, by its own manual entry")
	_, entries, _ := f.listRows()
	assert.EqualValues(t, 0, entries, "the list's entries are gone, not merely orphaned")
	var manual int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("kind = ?", RevokedKindManual).Count(&manual).Error)
	assert.EqualValues(t, 1, manual, "a manual entry is not a list's")
	counter, _ := f.rowsCounter()
	assert.EqualValues(t, 1, counter)

	again, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)
	assert.False(t, again, "deleting a list that is not there is a no-op")
}

// The list is soft-deleted, so a belt on the read side: even entries that somehow outlived their
// list are not an excuse (the delete removes them in the same transaction as the list).
func TestDeleteRemovesTheEntriesInTheSameTransactionAsTheList(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	// Fail the list's delete after the entries' delete ran: the whole thing must roll back.
	name := fmt.Sprintf("test:fail-list-delete-%d", time.Now().UnixNano())
	db := f.api.RDB.DB(f.ctx)
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(name, func(d *gorm.DB) {
		if d.Statement.Table == "device_revocation_lists" {
			_ = d.AddError(fmt.Errorf("boom"))
		}
	}))
	defer func() { _ = db.Callback().Delete().Remove(name) }()
	_, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.Error(t, err)
	assert.False(t, f.admits(f.verify(a)), "a failed delete left the list enforced")
	_, entries, _ := f.listRows()
	assert.EqualValues(t, 1, entries, "and its entry")
}

// 🔑 CARRY-OVER (b): AN ANCHOR THAT EVER HELD A CRL IS STILL DELETABLE.
func TestAnAnchorThatHeldACrlCanBeDeleted(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2)}))
	_, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(3), revoked: revokedSerials(serialOf(a.cert))}))
	lists, entries, issuers := f.listRows()
	require.EqualValues(t, 2, lists, "an active list and a soft-deleted one")
	require.EqualValues(t, 1, entries)
	require.EqualValues(t, 1, issuers)

	t.Run("a manual revocation still holds the anchor", func(t *testing.T) {
		fp := x509trust.Fingerprint(f.ca.leaf(t, "SN-M").cert)
		f.revoke(fp)
		_, err := f.api.DeleteDeviceTrustAnchor(f.ctx, f.anchor.Token)
		require.ErrorIs(t, err, ErrEntityInUse)
		ok, err := f.api.UnrevokeDeviceCertificate(f.ctx, f.anchor.Token, fp)
		require.NoError(t, err)
		require.True(t, ok)
		var n int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationIssuer{}).Count(&n).Error)
		require.EqualValues(t, 1, n, "the refused delete discarded nothing")
	})

	deleted, err := f.api.DeleteDeviceTrustAnchor(f.ctx, f.anchor.Token)
	require.NoError(t, err)
	assert.True(t, deleted)
	lists, entries, issuers = f.listRows()
	assert.EqualValues(t, 0, lists)
	assert.EqualValues(t, 0, entries)
	assert.EqualValues(t, 0, issuers)

	t.Run("a credential still holds an anchor with lists", func(t *testing.T) {
		g := newRevFixture(t)
		g.mustPut(g.ca.crl(t, crlSpec{number: big.NewInt(1)}))
		_, err := g.api.CreateDeviceCredential(g.ctx, x509Create("c-1", "dev", g.ca.leaf(t, "SN-1").pem(), "maker"))
		require.NoError(t, err)
		_, err = g.api.DeleteDeviceTrustAnchor(g.ctx, g.anchor.Token)
		require.ErrorIs(t, err, ErrEntityInUse)
		l, _, _ := g.listRows()
		assert.EqualValues(t, 1, l, "the refusal discarded nothing")
	})
}

// A CRL from a CA two levels below the anchor is accepted with its issuer chain and enforced; the
// anchor alone cannot validate it.
func TestPutRevocationListFromADeeperCAWithItsIssuerChain(t *testing.T) {
	f := newRevFixture(t)
	mid := newPki(t, f.ca, pkiOpts{cn: "Mid CA", ca: true})
	low := newPki(t, mid, pkiOpts{cn: "Low CA", ca: true})
	listed, sibling := low.leaf(t, "SN-1"), low.leaf(t, "SN-2")
	crl := low.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(listed.cert))})

	_, err := f.put(crl)
	require.Error(t, err, "without the chain the anchor cannot vouch for the signer")
	requireInvalid(t, err, "a CRL without its chain")

	res, err := f.put(crl, low, mid)
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewlyRevoked)
	assert.Equal(t, x509trust.IssuerIDOfCA(low.cert).Key, res.List.IssuerKey)
	assert.Equal(t, x509trust.IssuerIDOfCA(low.cert).Name, res.List.IssuerName)

	assert.False(t, f.admits(f.verify(listed, low, mid)))
	assert.True(t, f.admits(f.verify(sibling, low, mid)), "a sibling leaf of the same CA is untouched")
}

// One list per issuer: a second issuer under the same anchor is a second list.
func TestPutRevocationListKeepsOneListPerIssuer(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	a, b := f.ca.leaf(t, "SN-A"), inter.leaf(t, "SN-B")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	f.mustPut(inter.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(b.cert))}), inter)
	lists, entries, issuers := f.listRows()
	assert.EqualValues(t, 2, lists)
	assert.EqualValues(t, 2, entries)
	assert.EqualValues(t, 2, issuers)
	assert.False(t, f.admits(f.verify(a)))
	assert.False(t, f.admits(f.verify(b, inter)))
	got, err := f.api.DeviceRevocationListsOf(f.ctx, f.anchor.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// A key reused under another name is another issuer, and one anchor does not hold both: the
// second is refused as a conflicting identity rather than guessed at.
func TestPutRevocationListRefusesAConflictingIssuerIdentity(t *testing.T) {
	f := newRevFixture(t)
	one := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	other := one.reissuedAs(t, f.ca, "Line CA (renamed)")
	f.mustPut(one.crl(t, crlSpec{number: big.NewInt(1)}), one)

	_, err := f.put(other.crl(t, crlSpec{number: big.NewInt(1)}), other)
	requireRule(t, err, RuleConflictingIssuer)

	// The converse: the same name under a different key.
	again := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	_, err = f.put(again.crl(t, crlSpec{number: big.NewInt(1)}), again)
	requireRule(t, err, RuleConflictingIssuer)

	lists, _, issuers := f.listRows()
	assert.EqualValues(t, 1, lists)
	assert.EqualValues(t, 1, issuers, "a refused upload recorded nothing")
}

// Refusals change nothing stored, and name the rule.
func TestPutRevocationListRefusalsChangeNothing(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	stranger := newRootCA(t, "Stranger")
	good := f.ca.crl(t, crlSpec{number: big.NewInt(2)})

	cases := []struct {
		name string
		crl  []byte
		rule string
	}{
		{"foreign signer", stranger.crl(t, crlSpec{number: big.NewInt(9)}), string(x509trust.RuleWrongIssuer)},
		{"garbage", []byte("not a crl"), string(x509trust.RuleMalformed)},
		{"truncated", good[:len(good)/2], string(x509trust.RuleMalformed)},
		{"issued in the future", f.ca.crl(t, crlSpec{number: big.NewInt(9),
			thisUpdate: time.Now().Add(48 * time.Hour), nextUpdate: time.Now().Add(72 * time.Hour)}),
			string(x509trust.RuleThisUpdateFuture)},
		{"two PEM blocks", append(f.ca.crlPEM(t, crlSpec{number: big.NewInt(9)}), f.ca.crlPEM(t, crlSpec{number: big.NewInt(10)})...),
			string(x509trust.RuleMalformed)},
		{"empty", nil, string(x509trust.RuleMalformed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.put(tc.crl)
			requireRule(t, err, tc.rule)
			assert.False(t, f.admits(f.verify(a)), "the list in force is untouched")
			lists, entries, issuers := f.listRows()
			assert.EqualValues(t, 1, lists)
			assert.EqualValues(t, 1, entries)
			assert.EqualValues(t, 1, issuers)
		})
	}
	t.Run("the control: a good CRL right after", func(t *testing.T) {
		f.mustPut(good)
	})
}

// 🔑 THE SIZE CAP COMES BEFORE ANY OTHER WORK: an over-long CRL is refused before the parser, the
// database or a write slot is touched.
func TestPutRevocationListSizeCapIsCheckedBeforeAnythingElse(t *testing.T) {
	f := newRevFixture(t)
	lim := DefaultRevocationLimits()
	lim.MaxCrlBytes = 64 << 10
	lim.MaxGraphqlCrlBytes = 64 << 10
	require.NoError(t, f.api.SetRevocationLimits(lim))
	n, stop := queryCounter(t, f.api.RDB.Database)
	defer stop()

	// Not even a CRL, and not even an existing anchor: the size is judged first.
	huge := make([]byte, 64<<10+1)
	_, err := f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: "no-such-anchor", Crl: huge})
	requireRule(t, err, string(x509trust.RuleTooLarge))
	assert.Zero(t, n.Load(), "no query ran")

	t.Run("a door's own bound can only narrow the configured one", func(t *testing.T) {
		small := f.ca.crl(t, crlSpec{})
		_, err := f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{
			AnchorToken: f.anchor.Token, Crl: small, MaxBytes: len(small) - 1})
		requireRule(t, err, string(x509trust.RuleTooLarge))
		_, err = f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{
			AnchorToken: f.anchor.Token, Crl: small, MaxBytes: 1 << 30})
		require.NoError(t, err, "a larger bound than the configured one does not widen it, and this list fits")
		_, err = f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{
			AnchorToken: f.anchor.Token, Crl: append(small, make([]byte, 64<<10)...), MaxBytes: 1 << 30})
		requireRule(t, err, string(x509trust.RuleTooLarge))
	})
}

func TestPutRevocationListEntryAndListBounds(t *testing.T) {
	f := newRevFixture(t)
	lim := DefaultRevocationLimits()
	lim.MaxCrlEntries = 2
	lim.MaxListsPerAnchor = 2
	require.NoError(t, f.api.SetRevocationLimits(lim))

	leaves := []*pkiNode{f.ca.leaf(t, "1"), f.ca.leaf(t, "2"), f.ca.leaf(t, "3")}
	_, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(
		serialOf(leaves[0].cert), serialOf(leaves[1].cert), serialOf(leaves[2].cert))}))
	requireRule(t, err, string(x509trust.RuleTooManyEntries))
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaves[0].cert), serialOf(leaves[1].cert))}))

	i1 := newPki(t, f.ca, pkiOpts{cn: "I1", ca: true})
	i2 := newPki(t, f.ca, pkiOpts{cn: "I2", ca: true})
	f.mustPut(i1.crl(t, crlSpec{}), i1)
	_, err = f.put(i2.crl(t, crlSpec{}), i2)
	var le *limit.Error
	require.ErrorAs(t, err, &le, "a third list is over the per-anchor bound")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2)}))
	lists, _, _ := f.listRows()
	assert.EqualValues(t, 2, lists, "replacing a list is not a new list")
}

func TestSetRevocationLimitsRefusesWhatIsNotABound(t *testing.T) {
	f := newRevFixture(t)
	for name, mutate := range map[string]func(*RevocationLimits){
		"zero bytes":          func(l *RevocationLimits) { l.MaxCrlBytes = 0 },
		"zero entries":        func(l *RevocationLimits) { l.MaxCrlEntries = 0 },
		"zero lists":          func(l *RevocationLimits) { l.MaxListsPerAnchor = 0 },
		"zero writes":         func(l *RevocationLimits) { l.MaxConcurrentWrites = 0 },
		"graphql over crl":    func(l *RevocationLimits) { l.MaxGraphqlCrlBytes = l.MaxCrlBytes + 1 },
		"negative grace":      func(l *RevocationLimits) { l.DefaultStaleGrace = -1 },
		"grace over the max":  func(l *RevocationLimits) { l.DefaultStaleGrace = MaxRevocationStaleGrace + time.Second },
		"zero manual ceiling": func(l *RevocationLimits) { l.MaxManualEntriesPerTenant = 0 },
	} {
		l := DefaultRevocationLimits()
		mutate(&l)
		assert.Error(t, f.api.SetRevocationLimits(l), name)
	}
	assert.Equal(t, DefaultRevocationLimits(), f.api.RevocationLimits(), "refusals installed nothing")
}

// The GraphQL text bound is the PEM expansion of the DER bound, and a CRL at the DER bound fits it.
func TestGraphqlCrlTextBoundFitsThePemOfTheDerBound(t *testing.T) {
	f := newRevFixture(t)
	der := f.api.RevocationLimits().MaxGraphqlCrlBytes
	pemLen := len(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: make([]byte, der)}))
	assert.GreaterOrEqual(t, f.api.GraphqlCrlTextBound(), pemLen)
	assert.Less(t, f.api.GraphqlCrlTextBound(), pemLen+1024, "and not much more")
}

// Tenant isolation: another tenant's anchor of the same CA is its own, and an upload names only
// the caller's anchors.
func TestPutRevocationListIsIsolatedByTenant(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	other := core.WithTenant(context.Background(), "globex")
	_, err := f.api.CreateDeviceTrustAnchor(other, &DeviceTrustAnchorCreateRequest{Token: f.anchor.Token, CertificatePem: f.ca.pem()})
	require.NoError(t, err)

	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	assert.False(t, f.admits(f.verify(a)))

	ga, err := f.api.trustAnchorByToken(other, f.anchor.Token)
	require.NoError(t, err)
	state, err := f.api.ResolveX509Revocation(other, ga, f.verify(a).RevocationKeys(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, state.RevokedSerials, "tenant globex's anchor of the same CA is not affected")
	lists, err := f.api.DeviceRevocationListsOf(other, ga.ID)
	require.NoError(t, err)
	assert.Empty(t, lists)

	t.Run("an upload names only the caller's anchors", func(t *testing.T) {
		_, err := f.api.PutDeviceRevocationList(core.WithTenant(context.Background(), "initech"), &RevocationListPutRequest{
			AnchorToken: f.anchor.Token, Crl: f.ca.crl(t, crlSpec{number: big.NewInt(50)})})
		require.Error(t, err)
		_, err = f.api.PutDeviceRevocationList(context.Background(), &RevocationListPutRequest{
			AnchorToken: f.anchor.Token, Crl: f.ca.crl(t, crlSpec{number: big.NewInt(50)})})
		require.Error(t, err, "no tenant in context")
		_, err = f.api.DeleteDeviceRevocationList(core.WithTenant(context.Background(), "initech"), f.anchor.Token,
			x509trust.IssuerIDOfCA(f.ca.cert).Key, x509trust.IssuerIDOfCA(f.ca.cert).Name)
		require.Error(t, err)
		gl, _ := f.api.DeviceRevocationListsOf(f.ctx, f.anchor.ID)
		require.Len(t, gl, 1, "the owner's list is untouched")
	})
}

func TestPutRevocationListNeedsAnExistingAnchor(t *testing.T) {
	f := newRevFixture(t)
	_, err := f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: "ghost", Crl: f.ca.crl(t, crlSpec{})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	_, err = f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: " ", Crl: f.ca.crl(t, crlSpec{})})
	requireInvalid(t, err, "a blank token")
	_, err = f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, "nope", "nope")
	requireInvalid(t, err, "a malformed issuer id")
}

// ─── mode and grace ─────────────────────────────────────────────────────────

func (f *revFixture) update(req *DeviceTrustAnchorUpdateRequest) (*DeviceTrustAnchor, error) {
	return f.api.UpdateDeviceTrustAnchor(f.ctx, f.anchor.Token, req)
}

func modeOf(s string) OptionalRevocationMode { return OptionalRevocationMode{Set: true, Value: &s} }

// 🔑 THE GUARD: switching to REQUIRE_CURRENT is refused while no current list exists, and it
// stores nothing when refused. The control is the same switch with a list.
func TestRequireCurrentIsRefusedWhileThereIsNoCurrentList(t *testing.T) {
	f := newRevFixture(t)
	_, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	requireInvalid(t, err, "REQUIRE_CURRENT with no list")
	assert.Contains(t, err.Error(), "no current revocation list")
	m, err := f.freshAnchor().RevocationModeOf()
	require.NoError(t, err)
	assert.Equal(t, x509trust.ModeEntries, m, "the refused switch stored nothing")

	t.Run("a list past its nextUpdate and grace is not current", func(t *testing.T) {
		f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1),
			thisUpdate: time.Now().Add(-72 * time.Hour), nextUpdate: time.Now().Add(-48 * time.Hour)}))
		_, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
		requireInvalid(t, err, "REQUIRE_CURRENT with a stale list")
		_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT"),
			RevocationStaleGrace: dcgraphql.OptionalStringOf("72h")})
		require.NoError(t, err, "a grace that still covers the list makes it current, in the same request")
	})
}

func TestRequireCurrentSwitchWithACurrentList(t *testing.T) {
	f := newRevFixture(t)
	a := f.ca.leaf(t, "SN-A")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))

	got, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	require.NoError(t, err)
	m, _ := got.RevocationModeOf()
	assert.Equal(t, x509trust.ModeRequireCurrent, m)
	assert.False(t, f.admits(f.verify(a)))
	assert.True(t, f.admits(f.verify(f.ca.leaf(t, "SN-B"))), "a covered, unlisted certificate is admitted in REQUIRE_CURRENT")

	t.Run("deleting the last list leaves the anchor refusing (revocation_unknown)", func(t *testing.T) {
		is := x509trust.IssuerIDOfCA(f.ca.cert)
		_, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
		require.NoError(t, err)
		_, err = f.decide(f.verify(f.ca.leaf(t, "SN-C")))
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevocationUnknown, x509trust.ReasonOf(err))
	})

	t.Run("going back to ENTRIES is never guarded", func(t *testing.T) {
		_, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("ENTRIES")})
		require.NoError(t, err)
	})
}

// Every issuer on a chain a registered credential used must be covered, not just one.
func TestRequireCurrentNeedsEveryRegisteredIssuerCovered(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	leaf := inter.leaf(t, "SN-1")
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem()+inter.pem(), "maker"))
	require.NoError(t, err)

	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1)}))
	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	requireInvalid(t, err, "the intermediate has no list")
	assert.Contains(t, err.Error(), "issuer name hash")
	assert.Contains(t, err.Error(), x509trust.IssuerIDOfCA(inter.cert).Name[:12])

	f.mustPut(inter.crl(t, crlSpec{number: big.NewInt(1)}), inter)
	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	require.NoError(t, err, "with both issuers covered")

	t.Run("a legacy link with no issuer key is matched by name", func(t *testing.T) {
		g := newRevFixture(t)
		g.mustPut(g.ca.crl(t, crlSpec{number: big.NewInt(1)}))
		leaf := g.ca.leaf(t, "SN-L")
		cred, err := g.api.CreateDeviceCredential(g.ctx, x509Create("c-1", "dev", leaf.pem(), "maker"))
		require.NoError(t, err)
		require.NoError(t, g.api.RDB.DB(g.ctx).Model(&DeviceCredentialChainLink{}).
			Where("credential_id = ?", cred.ID).Update("issuer_key", nil).Error)
		_, err = g.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
		require.NoError(t, err)
	})

	t.Run("a disabled or deleted credential's chain is not a reason to refuse", func(t *testing.T) {
		g := newRevFixture(t)
		in := newPki(t, g.ca, pkiOpts{cn: "Gone CA", ca: true})
		cred, err := g.api.CreateDeviceCredential(g.ctx, x509Create("c-1", "dev", in.leaf(t, "SN-G").pem()+in.pem(), "maker"))
		require.NoError(t, err)
		g.mustPut(g.ca.crl(t, crlSpec{number: big.NewInt(1)}))
		_, err = g.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
		requireInvalid(t, err, "the live credential's intermediate has no list")
		require.NoError(t, g.api.RDB.DB(g.ctx).Model(&DeviceCredential{Model: cred.Model}).Update("enabled", false).Error)
		_, err = g.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
		require.NoError(t, err)
	})
}

// A shorter grace while in REQUIRE_CURRENT is guarded too; an unrelated update is not.
func TestShorteningTheGraceInRequireCurrentIsGuarded(t *testing.T) {
	f := newRevFixture(t)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1),
		thisUpdate: time.Now().Add(-48 * time.Hour), nextUpdate: time.Now().Add(-12 * time.Hour)}))
	_, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	require.NoError(t, err, "12h past nextUpdate is inside the 24h default grace")

	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationStaleGrace: dcgraphql.OptionalStringOf("1h")})
	requireInvalid(t, err, "a grace that would make the list stale")
	g := f.freshAnchor()
	assert.False(t, g.RevocationStaleGrace.Valid, "the refused change stored nothing")

	_, err = f.update(&DeviceTrustAnchorUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err, "an update that touches neither is not guarded")
	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationStaleGrace: dcgraphql.OptionalStringOf("48h")})
	require.NoError(t, err, "a longer one is fine")
}

func TestRevocationModeAndGraceInputs(t *testing.T) {
	f := newRevFixture(t)
	for name, req := range map[string]*DeviceTrustAnchorUpdateRequest{
		"null mode":            {RevocationMode: OptionalRevocationMode{Set: true}},
		"unknown mode":         {RevocationMode: modeOf("ALWAYS")},
		"lower-case mode":      {RevocationMode: modeOf("entries")},
		"unparsable grace":     {RevocationStaleGrace: dcgraphql.OptionalStringOf("a day")},
		"negative grace":       {RevocationStaleGrace: dcgraphql.OptionalStringOf("-1h")},
		"grace over 7 days":    {RevocationStaleGrace: dcgraphql.OptionalStringOf("169h")},
		"sub-second grace":     {RevocationStaleGrace: dcgraphql.OptionalStringOf("1500ms")},
		"blank grace":          {RevocationStaleGrace: dcgraphql.OptionalStringOf("  ")},
		"a refusal and a name": {RevocationMode: modeOf("ALWAYS"), Name: dcgraphql.OptionalStringOf("renamed")},
	} {
		_, err := f.update(req)
		requireInvalid(t, err, name)
	}
	a := f.freshAnchor()
	assert.False(t, a.RevocationMode.Valid)
	assert.False(t, a.RevocationStaleGrace.Valid)
	assert.False(t, a.Name.Valid, "a refused update changed no field")

	got, err := f.update(&DeviceTrustAnchorUpdateRequest{RevocationStaleGrace: dcgraphql.OptionalStringOf("0s")})
	require.NoError(t, err)
	g, err := got.RevocationStaleGraceOf(time.Hour)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), g, "a stored zero grace is a grace, not an absence")
	got, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationStaleGrace: dcgraphql.ClearedString()})
	require.NoError(t, err)
	g, _ = got.RevocationStaleGraceOf(time.Hour)
	assert.Equal(t, time.Hour, g, "null restores the instance default")
	got, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationStaleGrace: dcgraphql.OptionalStringOf("90m")})
	require.NoError(t, err)
	g, _ = got.RevocationStaleGraceOf(time.Hour)
	assert.Equal(t, 90*time.Minute, g)
}

func TestRevocationListStatus(t *testing.T) {
	now := time.Now()
	l := &DeviceRevocationList{ThisUpdate: now.Add(-100 * time.Hour), NextUpdate: now.Add(100 * time.Hour)}
	grace := 24 * time.Hour
	at := func(d time.Duration) string { return RevocationListStatus(l, grace, now.Add(d)) }
	assert.Equal(t, RevocationStatusFresh, at(0))
	assert.Equal(t, RevocationStatusFresh, at(49*time.Hour), "up to 75% of 200h (150h in)")
	assert.Equal(t, RevocationStatusExpiring, at(50*time.Hour))
	assert.Equal(t, RevocationStatusExpiring, at(100*time.Hour), "nextUpdate itself is inside the grace")
	assert.Equal(t, RevocationStatusExpiring, at(124*time.Hour), "the grace's last instant is still current")
	assert.Equal(t, RevocationStatusStale, at(124*time.Hour+time.Second))

	assert.Equal(t, RevocationStatusNone, RevocationAnchorStatus(nil, grace, now))
	fresh := DeviceRevocationList{ThisUpdate: now.Add(-time.Hour), NextUpdate: now.Add(100 * time.Hour)}
	stale := DeviceRevocationList{ThisUpdate: now.Add(-300 * time.Hour), NextUpdate: now.Add(-200 * time.Hour)}
	assert.Equal(t, RevocationStatusFresh, RevocationAnchorStatus([]DeviceRevocationList{fresh}, grace, now))
	assert.Equal(t, RevocationStatusStale, RevocationAnchorStatus([]DeviceRevocationList{fresh, stale}, grace, now), "the worst list decides")
}

// The configured default grace is the one the connect check uses for an anchor that sets none.
func TestTheConfiguredDefaultGraceReachesTheConnectCheck(t *testing.T) {
	f := newRevFixture(t)
	lim := DefaultRevocationLimits()
	lim.DefaultStaleGrace = 3 * time.Hour
	require.NoError(t, f.api.SetRevocationLimits(lim))
	state, err := f.api.ResolveX509Revocation(f.ctx, f.freshAnchor(), f.verify(f.ca.leaf(t, "x")).RevocationKeys(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 3*time.Hour, state.StaleGrace)
}

// The list row is journaled (the entries are not), and the journal names the row.
func TestPutAndDeleteAreJournaledByTheListRow(t *testing.T) {
	f := newRevFixture(t)
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&rdb.AuditEvent{}))
	a := f.ca.leaf(t, "SN-A")
	res := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(serialOf(a.cert))}))
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	_, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)

	var rows []rdb.AuditEvent
	require.NoError(t, f.api.RDB.DB(core.WithSystemContext(context.Background())).
		Where("table_name = ?", "device_revocation_lists").Order("id").Find(&rows).Error)
	var ops []string
	for _, r := range rows {
		ops = append(ops, r.Operation)
		assert.Equal(t, fmt.Sprint(res.List.ID), r.EntityPK, "the journal names the list row")
		assert.Equal(t, is.Key, r.EntityLabel, "and its issuer key")
	}
	assert.Equal(t, []string{"create", "update", "delete"}, ops)

	var entryRows int64
	require.NoError(t, f.api.RDB.DB(core.WithSystemContext(context.Background())).Model(&rdb.AuditEvent{}).
		Where("table_name = ?", "device_revoked_certificates").Count(&entryRows).Error)
	assert.Zero(t, entryRows, "CRL entries are derived rows and are not journaled")
}

// ─── one anchor's store is not another's ────────────────────────────────────

// A second anchor of the same tenant: its own CA, its own lists.
func (f *revFixture) secondAnchor(token string, ca *pkiNode) *DeviceTrustAnchor {
	f.t.Helper()
	return f.provFixture.anchor(token, ca)
}

func (f *revFixture) putTo(token string, crl []byte, chain ...*pkiNode) (*RevocationListPutResult, error) {
	f.t.Helper()
	var pemChain string
	for _, c := range chain {
		pemChain += c.pem()
	}
	return f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: token, Crl: crl, IssuerChainPem: pemChain})
}

// Another anchor's current list is not coverage for this one, and its issuers and lists do not count
// against this one's bounds or conflict with its identities.
func TestOneAnchorsListsAreNotAnotherAnchorsCoverageBoundsOrIdentities(t *testing.T) {
	f := newRevFixture(t)
	otherCA := newRootCA(t, "Other Root")
	f.secondAnchor("other", otherCA)
	_, err := f.putTo("other", otherCA.crl(t, crlSpec{number: big.NewInt(1)}))
	require.NoError(t, err)

	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	requireInvalid(t, err, "the other anchor's list is not this anchor's coverage")
	assert.Contains(t, err.Error(), "no current revocation list")

	t.Run("bounds are per anchor", func(t *testing.T) {
		lim := DefaultRevocationLimits()
		lim.MaxListsPerAnchor = 1
		require.NoError(t, f.api.SetRevocationLimits(lim))
		_, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(1)}))
		require.NoError(t, err, "the other anchor's list does not count against this anchor's bound")
	})

	third := newRootCA(t, "Third Root")
	f.secondAnchor("third", third)
	_, err = f.putTo("third", third.crl(t, crlSpec{number: big.NewInt(1)}))
	require.NoError(t, err)

	t.Run("deleting one anchor discards only its own store", func(t *testing.T) {
		_, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "third")
		require.NoError(t, err)
		lists, err := f.api.DeviceRevocationListsOf(f.ctx, f.anchor.ID)
		require.NoError(t, err)
		assert.Len(t, lists, 1, "the first anchor's list survived")
		other, err := f.api.trustAnchorByToken(f.ctx, "other")
		require.NoError(t, err)
		lists, err = f.api.DeviceRevocationListsOf(f.ctx, other.ID)
		require.NoError(t, err)
		assert.Len(t, lists, 1, "and the other anchor's")
		var issuers int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationIssuer{}).Count(&issuers).Error)
		assert.EqualValues(t, 2, issuers)
	})
}

// Deleting a list of one issuer leaves another issuer's list and entries alone.
func TestDeletingOneIssuersListLeavesTheOthersEnforced(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	a, b := f.ca.leaf(t, "SN-A"), inter.leaf(t, "SN-B")
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	f.mustPut(inter.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(b.cert))}), inter)
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	_, err := f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, is.Key, is.Name)
	require.NoError(t, err)
	assert.True(t, f.admits(f.verify(a)))
	assert.False(t, f.admits(f.verify(b, inter)), "the intermediate's list still refuses")
	lists, entries, _ := f.listRows()
	assert.EqualValues(t, 2, lists, "one active, one soft-deleted")
	assert.EqualValues(t, 1, entries)
}

// ─── review additions (mutation survivors) ──────────────────────────────────

// 🔑 THE MARK ADVANCES WITH EVERY ACCEPTED LIST, not only the first: after 5 then 7, a 6 is a rollback
// even while the list is in force, and after deleting it.
func TestTheHighWaterMarkFollowsEveryAcceptedList(t *testing.T) {
	f := newRevFixture(t)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(5)}))
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(7)}))
	_, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(6)}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	var is DeviceRevocationIssuer
	require.NoError(t, f.api.RDB.DB(f.ctx).First(&is).Error)
	assert.Equal(t, "7", is.HighWaterCrlNumber)

	id := x509trust.IssuerIDOfCA(f.ca.cert)
	_, err = f.api.DeleteDeviceRevocationList(f.ctx, f.anchor.Token, id.Key, id.Name)
	require.NoError(t, err)
	_, err = f.put(f.ca.crl(t, crlSpec{number: big.NewInt(6)}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(8)}))
}

// 🔑 THE ANCHOR THE LIST WAS VERIFIED AGAINST IS THE ANCHOR IT IS STORED UNDER: if the row under the
// token changed (deleted and re-created as another CA) between the verification and the lock, the
// upload is refused and stores nothing.
func TestPutRefusesWhenTheAnchorChangedBetweenVerificationAndTheLock(t *testing.T) {
	f := newRevFixture(t)
	crl := f.ca.crl(t, crlSpec{number: big.NewInt(1)})
	db := f.api.RDB.DB(f.ctx)
	name := fmt.Sprintf("test:swap-anchor-%d", time.Now().UnixNano())
	var reads int
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(d *gorm.DB) {
		if d.Statement.Table != "device_trust_anchors" {
			return
		}
		reads++
		if reads == 1 { // the verification read has happened; swap the row before the locked read
			require.NoError(t, d.Session(&gorm.Session{NewDB: true}).Exec(
				"UPDATE device_trust_anchors SET fingerprint_sha256 = ? WHERE id = ?", strings.Repeat("e", 64), f.anchor.ID).Error)
		}
	}))
	defer func() { _ = db.Callback().Query().Remove(name) }()

	_, err := f.put(crl)
	requireInvalid(t, err, "an anchor swapped mid-upload")
	assert.Contains(t, err.Error(), "changed while the list was being checked")
	lists, entries, issuers := f.listRows()
	assert.Zero(t, lists+entries+issuers)
}

// Coverage is by the issuer's key AND name: a list from a CA that shares only the name does not
// cover the issuer a registered credential used.
func TestRequireCurrentCoverageNeedsTheKeyNotJustTheName(t *testing.T) {
	f := newRevFixture(t)
	real := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", real.leaf(t, "SN-1").pem()+real.pem(), "maker"))
	require.NoError(t, err)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1)}))
	imposter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true}) // same name, another key
	f.mustPut(imposter.crl(t, crlSpec{number: big.NewInt(1)}), imposter)

	_, err = f.update(&DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf("REQUIRE_CURRENT")})
	requireInvalid(t, err, "a same-name list does not cover the real issuer")
	_, err = f.put(real.crl(t, crlSpec{number: big.NewInt(1)}), real)
	requireRule(t, err, RuleConflictingIssuer) // and the real issuer cannot be added beside it: one identity per name
}

// Replacing one issuer's list reports only that list's own difference, whatever other lists the
// anchor holds.
func TestReplacingOneListCountsOnlyItsOwnDifference(t *testing.T) {
	f := newRevFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	a, b, c := f.ca.leaf(t, "A"), f.ca.leaf(t, "B"), inter.leaf(t, "C")
	f.mustPut(inter.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(c.cert))}), inter)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	res := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(serialOf(a.cert), serialOf(b.cert))}))
	assert.Equal(t, 1, res.NewlyRevoked)
	assert.Equal(t, 0, res.Removed, "the intermediate's entry is not this list's to remove")
	assert.False(t, f.admits(f.verify(c, inter)))
	res = f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(3), revoked: revokedSerials(serialOf(b.cert))}))
	assert.Equal(t, 0, res.NewlyRevoked)
	assert.Equal(t, 1, res.Removed)
}

// The manual-entry ceiling is the configured one.
func TestTheManualCeilingIsTheConfiguredOne(t *testing.T) {
	f := newRevFixture(t)
	lim := DefaultRevocationLimits()
	lim.MaxManualEntriesPerTenant = 2
	require.NoError(t, f.api.SetRevocationLimits(lim))
	f.revoke(x509trust.Fingerprint(f.ca.leaf(t, "1").cert))
	f.revoke(x509trust.Fingerprint(f.ca.leaf(t, "2").cert))
	fp := x509trust.Fingerprint(f.ca.leaf(t, "3").cert)
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: f.anchor.Token, FingerprintSha256: &fp})
	var le *limit.Error
	require.ErrorAs(t, err, &le)
	assert.Equal(t, 2, le.Max)
}

func TestAnchorStatusIsTheWorstWhateverTheOrder(t *testing.T) {
	now := time.Now()
	grace := 24 * time.Hour
	fresh := DeviceRevocationList{ThisUpdate: now.Add(-time.Hour), NextUpdate: now.Add(100 * time.Hour)}
	expiring := DeviceRevocationList{ThisUpdate: now.Add(-90 * time.Hour), NextUpdate: now.Add(10 * time.Hour)}
	stale := DeviceRevocationList{ThisUpdate: now.Add(-300 * time.Hour), NextUpdate: now.Add(-200 * time.Hour)}
	for _, order := range [][]DeviceRevocationList{{stale, fresh, expiring}, {fresh, stale, expiring}, {fresh, expiring, stale}, {expiring, fresh}} {
		want := RevocationStatusStale
		if len(order) == 2 {
			want = RevocationStatusExpiring
		}
		assert.Equal(t, want, RevocationAnchorStatus(order, grace, now))
	}
}

// ACCEPTED, AND PINNED: deleting an anchor discards its CRL-number high-water marks, so an anchor
// re-created from the same CA accepts a list the deleted anchor had already passed. (Deleting needs
// that no credential or profile relies on the anchor, and un-revoking its manual entries.)
func TestRecreatingADeletedAnchorForgetsItsHighWaterMark(t *testing.T) {
	f := newRevFixture(t)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(9)}))
	_, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(4)}))
	requireRule(t, err, RuleCrlNumberNotNewer)

	deleted, err := f.api.DeleteDeviceTrustAnchor(f.ctx, f.anchor.Token)
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: f.anchor.Token, CertificatePem: f.ca.pem()})
	require.NoError(t, err)

	res, err := f.put(f.ca.crl(t, crlSpec{number: big.NewInt(4)}))
	require.NoError(t, err, "the re-created anchor starts with no mark")
	assert.Equal(t, "4", res.List.CrlNumber)
}

// The defaults are sized against the pod: one upload at a time, a 2 MiB CRL (an upload peaks at roughly
// 20 to 30 times it in heap), 100 000 entries.
func TestTheDefaultLimitsAreTheOnesTheMemoryMeasurementWasMadeAt(t *testing.T) {
	d := DefaultRevocationLimits()
	assert.Equal(t, 1, d.MaxConcurrentWrites)
	assert.Equal(t, 2<<20, d.MaxCrlBytes)
	assert.Equal(t, 100_000, d.MaxCrlEntries)
	assert.Equal(t, 2<<20, d.MaxGraphqlCrlBytes)
}
