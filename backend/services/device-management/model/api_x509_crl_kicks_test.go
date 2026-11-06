// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE CRL UPLOAD ENDS THE LIVE SESSIONS OF THE CERTIFICATES IT NEWLY REVOKES, through the same
// queue, reason and re-sweep as a manual revoke.

type crlKickFixture struct {
	*revFixture
	kicker *togglingKicker
}

func newCrlKickFixture(t *testing.T) *crlKickFixture {
	t.Helper()
	f := newRevFixture(t)
	k := &togglingKicker{up: true}
	f.api.SessionKicker = k
	f.api.RevocationResweepAfter = time.Hour // the re-sweep tests set their own; a short default makes a later call re-ask
	return &crlKickFixture{revFixture: f, kicker: k}
}

func (f *crlKickFixture) pin(device string, leaf *pkiNode, inters ...*pkiNode) *DeviceCredential {
	f.t.Helper()
	f.device(device, device+"-ext", "dt-a", "")
	text := leaf.pem()
	for _, i := range inters {
		text += i.pem()
	}
	c, err := f.api.CreateDeviceCredential(f.ctx, x509Create(device+"-cred", device, text, f.anchor.Token))
	require.NoError(f.t, err)
	return c
}

func (f *crlKickFixture) kicked() []string {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	var all []string
	for _, c := range f.kicker.calls {
		if c.reason == messaging.SessionKickCertificateRevoked {
			all = append(all, c.tokens...)
		}
	}
	slices.Sort(all)
	return slices.Compact(all)
}

func (f *crlKickFixture) reset() {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	f.kicker.calls = nil
}

// 🔑 A leaf on the CRL ends that device's session; the sibling's is untouched; a replacement CRL
// kicks only what it ADDS (the earlier entry is not asked about again).
func TestCrlUploadKicksOnlyTheNewlyRevokedLeaves(t *testing.T) {
	f := newCrlKickFixture(t)
	a, b, c := f.ca.leaf(t, "SN-A"), f.ca.leaf(t, "SN-B"), f.ca.leaf(t, "SN-C")
	f.pin("dev-a", a)
	f.pin("dev-b", b)
	f.pin("dev-c", c)
	f.reset()

	res := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(a.cert))}))
	assert.True(t, res.SessionsEnded)
	assert.Equal(t, []string{"dev-a"}, f.kicked())

	f.reset()
	res = f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(serialOf(a.cert), serialOf(b.cert))}))
	assert.Equal(t, 1, res.NewlyRevoked)
	assert.Equal(t, []string{"dev-b"}, f.kicked(), "only the newly revoked certificate's device")
}

// A CRL entry for an intermediate seen on a credential's chain is CA-level: every device under the
// anchor is asked about.
func TestCrlUploadOfAnIntermediateKicksTheWholeAnchor(t *testing.T) {
	f := newCrlKickFixture(t)
	inter := newPki(t, f.ca, pkiOpts{cn: "Line", ca: true})
	f.pin("dev-1", inter.leaf(t, "SN-1"), inter)
	f.pin("dev-2", f.ca.leaf(t, "SN-2"))
	f.reset()

	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(inter.cert))}))
	assert.Equal(t, []string{"dev-1", "dev-2"}, f.kicked())
}

// A legacy credential (no links) is linked first and then found by (issuer name, serial).
func TestCrlUploadKicksALegacyCredential(t *testing.T) {
	f := newCrlKickFixture(t)
	legacy, other := f.ca.leaf(t, "SN-L"), f.ca.leaf(t, "SN-O")
	lc := f.pin("dev-legacy", legacy)
	f.pin("dev-other", other)
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", lc.ID).Delete(&DeviceCredentialChainLink{}).Error)
	f.reset()

	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(legacy.cert))}))
	assert.Equal(t, []string{"dev-legacy"}, f.kicked())
}

// An unconfirmed kick rides with the stored list as ErrSessionsNotEnded, and a repeat of the SAME
// upload (a no-op for the list) asks again and reports settled once the responder is back.
func TestCrlUploadSurfacesAnUnconfirmedKickAndARepeatRetriesIt(t *testing.T) {
	f := newCrlKickFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	f.pin("dev-1", leaf)
	f.kicker.set(false)
	crl := f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaf.cert))})

	res, err := f.put(crl)
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.NotNil(t, res, "the list is stored and returned with the error")
	assert.False(t, res.SessionsEnded)
	lists, entries, _ := f.listRows()
	assert.EqualValues(t, 1, lists)
	assert.EqualValues(t, 1, entries)

	f.kicker.set(true)
	res, err = f.put(crl)
	require.NoError(t, err)
	assert.True(t, res.Unchanged)
	assert.True(t, res.SessionsEnded)
	assert.Equal(t, []string{"dev-1"}, f.kicked())
}

// An upload that revokes nothing new queues nothing.
func TestCrlUploadWithNothingNewKicksNobody(t *testing.T) {
	f := newCrlKickFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	f.pin("dev-1", leaf)
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(big.NewInt(424242))}))
	assert.Empty(t, f.kicked(), "the listed serial matches no credential")
	f.reset()
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(big.NewInt(424242))}))
	assert.Empty(t, f.kicked())
}

// The CRL join matches a link that HAS an issuer key on it: the same (name, serial) under another
// key is another CA's certificate.
func TestCrlKickJoinComparesTheIssuerKeyOfAKeyedLink(t *testing.T) {
	f := newCrlKickFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	f.pin("dev-1", leaf)
	v := f.verify(leaf)
	real := v.RevocationKeys().Serials[0].Issuer
	serial, _ := x509trust.SerialHex(serialOf(leaf.cert))
	queue := func(key string) int {
		var n int
		require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
			require.NoError(t, tx.Exec("DROP TABLE IF EXISTS temp."+crlNewTable).Error)
			require.NoError(t, tx.Exec("CREATE TEMP TABLE "+crlNewTable+" (serial varchar(64))").Error)
			require.NoError(t, tx.Exec("INSERT INTO "+crlNewTable+" (serial) VALUES (?)", serial).Error)
			var err error
			n, err = f.api.queueCrlRevocationKicksOnTx(tx, partialUpdateTenant, f.anchor.ID,
				x509trust.IssuerID{Key: key, Name: real.Name}, true)
			require.NoError(t, err)
			return dropCrlNew(tx)
		}))
		return n
	}
	assert.Equal(t, 0, queue("ff"+real.Key[2:]), "another issuer key")
	assert.Equal(t, 1, queue(real.Key), "the control")
}

// 🔑 THE CRL TWIN OF THE BOUNDED RECONCILER. With more unlinked legacy credentials than the budget,
// an upload that names ONE leaf asks about the WHOLE anchor (links not whole: an over-kick, never a
// miss); the next upload finishes the reconciliation and is narrow again.
func TestCrlUploadWithIncompleteLinksWidensTheKickToTheAnchor(t *testing.T) {
	f := newCrlKickFixture(t)
	old := legacyReconcileBudget
	legacyReconcileBudget = legacyLinkBatch
	t.Cleanup(func() { legacyReconcileBudget = old })
	n := legacyLinkBatch + 3
	leaves := make([]*pkiNode, n)
	for i := 0; i < n; i++ {
		leaves[i] = f.ca.leaf(t, fmt.Sprintf("SN-%d", i))
		f.pin(fmt.Sprintf("dev-%03d", i), leaves[i])
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("trust_anchor_id = ?", f.anchor.ID).Delete(&DeviceCredentialChainLink{}).Error)
	f.reset()

	res := f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaves[0].cert))}))
	assert.True(t, res.SessionsEnded)
	assert.Len(t, f.kicked(), n, "links not whole: every device under the anchor is asked about")

	f.reset()
	f.mustPut(f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(serialOf(leaves[0].cert), serialOf(leaves[1].cert))}))
	assert.Equal(t, []string{"dev-001"}, f.kicked(), "the second upload completes the links and is narrow again")
}
