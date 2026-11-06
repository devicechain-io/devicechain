// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// CRL upload, replace and delete against a REAL PostgreSQL: the anti-rollback mark under the
// anchor and issuer locks, concurrent uploads, delete followed by re-upload, the foreign keys when
// an anchor that held a CRL is deleted, big CRL numbers, big lists, and a mixed stress for lock
// order. The SQLite fixture drops FOR UPDATE and has no foreign keys, so none of these is visible
// there. The interleavings are FORCED (a callback pauses one side while the test waits for the
// other to be provably blocked on a lock), not raced.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'CrlPostgres' -v
package model

import (
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func (f *pgCertFixture) replica() *Api {
	f.t.Helper()
	r := NewApi(newPostgresRdbManager(f.t))
	r.DeviceSecretKey = f.api.DeviceSecretKey
	r.SessionKicker = f.kicker
	return r
}

func (f *pgCertFixture) putVia(api *Api, crl []byte, chain ...*pkiNode) (*RevocationListPutResult, error) {
	var pemChain string
	for _, c := range chain {
		pemChain += c.pem()
	}
	return api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: "maker", Crl: crl, IssuerChainPem: pemChain})
}

func (f *pgCertFixture) activeLists() []DeviceRevocationList {
	f.t.Helper()
	var out []DeviceRevocationList
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Order("id").Find(&out).Error)
	return out
}

func (f *pgCertFixture) crlSerials() map[string]bool {
	f.t.Helper()
	var rows []DeviceRevokedCertificate
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("kind = ?", RevokedKindCRL).Find(&rows).Error)
	out := map[string]bool{}
	for _, r := range rows {
		out[*r.Serial] = true
	}
	return out
}

func hexSerial(n *big.Int) string { s, _ := x509trust.SerialHex(n); return s }

// holdEntries pauses the upload whose entries include serial, right after its entries are
// inserted: the anchor and the issuer rows are held FOR UPDATE and the entries are uncommitted.
func (f *pgCertFixture) holdEntries(serial *big.Int) (reached <-chan struct{}, release func()) {
	f.t.Helper()
	reachedCh, releaseCh := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	want := hexSerial(serial)
	name := fmt.Sprintf("test:hold-crl-%d", time.Now().UnixNano())
	require.NoError(f.t, f.mgr.Database.Callback().Create().After("gorm:create").Register(name, func(db *gorm.DB) {
		rows := insertedEntries(db)
		if db.Error != nil {
			return
		}
		for _, r := range rows {
			if r.Serial != nil && *r.Serial == want {
				once.Do(func() {
					close(reachedCh)
					<-releaseCh
				})
				return
			}
		}
	}))
	rel := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	f.t.Cleanup(func() {
		rel()
		_ = f.mgr.Database.Callback().Create().Remove(name)
	})
	return reachedCh, rel
}

// insertedEntries is the CRL entry rows the statement just created, whatever shape gorm handed them in.
func insertedEntries(db *gorm.DB) []crlEntryRow {
	rv := db.Statement.ReflectValue
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	var out []crlEntryRow
	for i := 0; i < rv.Len(); i++ {
		switch r := rv.Index(i).Interface().(type) {
		case crlEntryRow:
			out = append(out, r)
		case *crlEntryRow:
			out = append(out, *r)
		}
	}
	return out
}

func nextSerial(f *pgCertFixture, t *testing.T, cn string) *big.Int {
	return f.ca.leaf(t, cn).cert.SerialNumber
}

// 🔑 TWO CONCURRENT UPLOADS N+1 AND N+2 END WITH N+2 STORED, WHICHEVER COMMITS FIRST. Both
// interleavings are forced.
func TestCrlPostgresConcurrentUploadsEndWithTheHigherNumber(t *testing.T) {
	for _, order := range []string{"N+2 commits first", "N+1 commits first"} {
		t.Run(order, func(t *testing.T) {
			f := newPgCertFixture(t)
			a, b := f.api, f.replica()
			base, one, two := nextSerial(f, t, "SN-base"), nextSerial(f, t, "SN-1"), nextSerial(f, t, "SN-2")
			_, err := f.putVia(a, f.ca.crl(t, crlSpec{number: big.NewInt(10), revoked: revokedSerials(base)}))
			require.NoError(t, err)

			crlOne := f.ca.crl(t, crlSpec{number: big.NewInt(11), revoked: revokedSerials(base, one)})
			crlTwo := f.ca.crl(t, crlSpec{number: big.NewInt(12), revoked: revokedSerials(base, two)})
			held, other, heldCrl, otherCrl := two, one, crlTwo, crlOne
			heldApi, otherApi := a, b
			if order == "N+1 commits first" {
				held, other, heldCrl, otherCrl = one, two, crlOne, crlTwo
			}
			_ = other
			reached, release := f.holdEntries(held)

			type outcome struct {
				res *RevocationListPutResult
				err error
			}
			heldDone, otherDone := make(chan outcome, 1), make(chan outcome, 1)
			go func() {
				r, e := f.putVia(heldApi, heldCrl)
				heldDone <- outcome{r, e}
			}()
			select {
			case <-reached:
			case <-time.After(30 * time.Second):
				t.Fatal("the held upload never reached its entry insert")
			}
			go func() {
				r, e := f.putVia(otherApi, otherCrl)
				otherDone <- outcome{r, e}
			}()
			waitForBlockedBackends(t, f.mgr.Database, 1)
			select {
			case <-otherDone:
				t.Fatal("the second upload finished while the first held the anchor lock")
			case <-time.After(500 * time.Millisecond):
			}
			release()
			h, o := <-heldDone, <-otherDone
			require.NoError(t, h.err)

			lists := f.activeLists()
			require.Len(t, lists, 1)
			if order == "N+2 commits first" {
				requireRule(t, o.err, RuleCrlNumberNotNewer)
				assert.Equal(t, "12", lists[0].CrlNumber)
				assert.Equal(t, map[string]bool{hexSerial(base): true, hexSerial(two): true}, f.crlSerials(), "the refused N+1 left nothing behind")
			} else {
				require.NoError(t, o.err)
				assert.Equal(t, "12", lists[0].CrlNumber)
				assert.Equal(t, map[string]bool{hexSerial(base): true, hexSerial(two): true}, f.crlSerials(),
					"N+2 replaced N+1: its entries only")
			}
			var hw string
			require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationIssuer{}).Select("high_water_crl_number").Scan(&hw).Error)
			assert.Equal(t, "12", hw)
			counter, _ := f.revokedRows()
			assert.EqualValues(t, 1, counter)
		})
	}
}

// The first-ever uploads for an issuer race on a missing issuer row: the anchor lock serialises
// them, and the unique indexes never fire.
func TestCrlPostgresFirstUploadsRaceOnAMissingIssuerRow(t *testing.T) {
	for round := 0; round < 8; round++ {
		f := newPgCertFixture(t)
		replicas := []*Api{f.api, f.replica(), f.replica()}
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i := range replicas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = f.putVia(replicas[i], f.ca.crl(t, crlSpec{number: big.NewInt(int64(i + 1)), revoked: revokedSerials(big.NewInt(int64(100 + i)))}))
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				assert.Contains(t, err.Error(), RuleCrlNumberNotNewer, "the only acceptable refusal is a rollback")
				assert.NotContains(t, err.Error(), "SQLSTATE")
			}
		}
		require.NoError(t, errs[2], "the highest number is never the one refused")
		lists := f.activeLists()
		require.Len(t, lists, 1, "round %d: one list per issuer", round)
		assert.Equal(t, "3", lists[0].CrlNumber)
		var issuers int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationIssuer{}).Count(&issuers).Error)
		assert.EqualValues(t, 1, issuers)
		assert.Equal(t, map[string]bool{hexSerial(big.NewInt(102)): true}, f.crlSerials())
	}
}

// 🔑 DELETE FOLLOWED BY RE-UPLOAD, on the real database: the mark outlives the list, a newer list
// is accepted, and the entries of the deleted list are gone in between.
func TestCrlPostgresDeleteThenReuploadKeepsTheMark(t *testing.T) {
	f := newPgCertFixture(t)
	a := nextSerial(f, t, "SN-A")
	_, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(5), revoked: revokedSerials(a)}))
	require.NoError(t, err)
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	ok, err := f.api.DeleteDeviceRevocationList(f.ctx, "maker", is.Key, is.Name)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, f.crlSerials(), "the deleted list's entries are gone")

	_, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(4)}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	_, err = f.putVia(f.replica(), f.ca.crl(t, crlSpec{number: big.NewInt(5)}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	res, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(6), revoked: revokedSerials(a)}))
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewlyRevoked)

	t.Run("a delete and an older upload racing never leave the older list in force", func(t *testing.T) {
		for round := 0; round < 6; round++ {
			g := newPgCertFixture(t)
			_, err := g.putVia(g.api, g.ca.crl(t, crlSpec{number: big.NewInt(50)}))
			require.NoError(t, err)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = g.api.DeleteDeviceRevocationList(g.ctx, "maker", is.Key, is.Name) }()
			go func() { defer wg.Done(); _, _ = g.putVia(g.replica(), g.ca.crl(t, crlSpec{number: big.NewInt(40)})) }()
			wg.Wait()
			for _, l := range g.activeLists() {
				assert.NotEqual(t, "40", l.CrlNumber, "round %d: the rolled-back list is in force", round)
			}
			assert.Empty(t, g.crlSerials())
		}
	})
}

// CRL numbers are up to 20 octets; the mark and the comparison are exact.
func TestCrlPostgresBigCrlNumbers(t *testing.T) {
	f := newPgCertFixture(t)
	big1, _ := new(big.Int).SetString("123456789012345678901234567890123456789012345678", 10) // 48 digits, 159 bits
	big2 := new(big.Int).Add(big1, big.NewInt(1))
	_, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big2}))
	require.NoError(t, err)
	_, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: big1}))
	requireRule(t, err, RuleCrlNumberNotNewer)
	l := f.activeLists()
	require.Len(t, l, 1)
	assert.Equal(t, big2.String(), l[0].CrlNumber, "stored exactly, not rounded")
	_, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: new(big.Int).Add(big2, big.NewInt(1))}))
	require.NoError(t, err)
}

// 🔑 CARRY-OVER (b) against the real foreign keys: an anchor that held a CRL is deletable, in the
// right order, and leaves nothing behind.
func TestCrlPostgresAnAnchorThatHeldACrlCanBeDeleted(t *testing.T) {
	f := newPgCertFixture(t)
	_, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(big.NewInt(7), big.NewInt(8))}))
	require.NoError(t, err)
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	_, err = f.api.DeleteDeviceRevocationList(f.ctx, "maker", is.Key, is.Name)
	require.NoError(t, err)
	_, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(big.NewInt(7))}))
	require.NoError(t, err)

	// The fixture's registration profile relies on the anchor; remove it first, as an operator would.
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("trust_anchor_id IS NOT NULL").Unscoped().Delete(&ProvisioningProfile{}).Error)
	deleted, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "maker")
	require.NoError(t, err)
	assert.True(t, deleted)
	for _, m := range []any{&DeviceRevocationIssuer{}, &DeviceRevokedCertificate{}} {
		var n int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Model(m).Count(&n).Error)
		assert.Zero(t, n, "%T", m)
	}
	var lists int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Model(&DeviceRevocationList{}).Count(&lists).Error)
	assert.Zero(t, lists, "soft-deleted lists go with the anchor")
}

// A large list: batched insert, then a replacement that shares most of it. The diff touches only the
// difference.
func TestCrlPostgresALargeListIsMaterialisedAndDiffed(t *testing.T) {
	f := newPgCertFixture(t)
	const n = 30_000
	serials := make([]*big.Int, n)
	for i := range serials {
		serials[i] = big.NewInt(int64(1_000_000 + i))
	}
	started := time.Now()
	res, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serials...)}))
	require.NoError(t, err)
	t.Logf("upload of %d entries: %s", n, time.Since(started))
	assert.Equal(t, n, res.NewlyRevoked)
	assert.Len(t, f.crlSerials(), n)

	// Drop the first 10 000, keep 20 000, add 5 000 new ones.
	next := append([]*big.Int{}, serials[10_000:]...)
	for i := 0; i < 5_000; i++ {
		next = append(next, big.NewInt(int64(9_000_000+i)))
	}
	var inserts atomic.Int64
	name := fmt.Sprintf("test:count-inserts-%d", time.Now().UnixNano())
	require.NoError(t, f.mgr.Database.Callback().Create().After("gorm:create").Register(name, func(db *gorm.DB) {
		inserts.Add(int64(len(insertedEntries(db))))
	}))
	defer func() { _ = f.mgr.Database.Callback().Create().Remove(name) }()
	res, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(2), revoked: revokedSerials(next...)}))
	require.NoError(t, err)
	assert.Equal(t, 5_000, res.NewlyRevoked)
	assert.Equal(t, 10_000, res.Removed)
	assert.EqualValues(t, 5_000, inserts.Load(), "only the new entries were inserted")
	assert.Len(t, f.crlSerials(), 25_000)
	assert.EqualValues(t, 25_000, f.activeLists()[0].EntryCount)
}

// A mixed stress for lock order: uploads, deletes, manual revokes and unrevokes, mode changes and
// anchor updates from three replicas. No deadlock, no aborted transaction, and the counter equals
// what is stored.
func TestCrlPostgresMixedWritesNeitherDeadlockNorDriftTheCounter(t *testing.T) {
	f := newPgCertFixture(t)
	replicas := []*Api{f.api, f.replica(), f.replica()}
	is := x509trust.IssuerIDOfCA(f.ca.cert)
	var number atomic.Int64
	var bad atomic.Int64
	var problems sync.Map
	record := func(err error) {
		if err == nil {
			return
		}
		msg := err.Error()
		ok := strings.Contains(msg, "revocation list refused") || strings.Contains(msg, "REQUIRE_CURRENT") ||
			strings.Contains(msg, "limit exceeded")
		if !ok {
			bad.Add(1)
			problems.Store(msg, true)
		}
	}
	stop := time.Now().Add(6 * time.Second)
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			api := replicas[w%len(replicas)]
			for i := 0; time.Now().Before(stop); i++ {
				switch (w + i) % 5 {
				case 0, 1:
					n := number.Add(1)
					_, err := f.putVia(api, f.ca.crl(t, crlSpec{number: big.NewInt(n), revoked: revokedSerials(big.NewInt(n), big.NewInt(n+1000))}))
					record(err)
				case 2:
					_, err := api.DeleteDeviceRevocationList(f.ctx, "maker", is.Key, is.Name)
					record(err)
				case 3:
					fp := strings.Repeat(fmt.Sprintf("%x", (w*7+i)%15), 64)
					if i%2 == 0 {
						record(f.revokeFingerprint(api, fp))
					} else {
						_, err := api.UnrevokeDeviceCertificate(f.ctx, "maker", fp)
						record(err)
					}
				case 4:
					mode := []string{"ENTRIES", "REQUIRE_CURRENT"}[i%2]
					_, err := api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{RevocationMode: modeOf(mode)})
					record(err)
				}
			}
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(120 * time.Second):
		t.Fatal("the mixed writes never finished: a deadlock the database did not break")
	}
	problems.Range(func(k, _ any) bool { t.Logf("unexpected error: %v", k); return true })
	assert.Zero(t, bad.Load())

	a, err := f.api.trustAnchorByToken(f.ctx, "maker")
	require.NoError(t, err)
	var manual, lists int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("kind = ?", RevokedKindManual).Count(&manual).Error)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevocationList{}).Count(&lists).Error)
	assert.Equal(t, manual+lists, a.RevocationRows.Int64, "the counter equals the lists plus the manual entries")
	var orphans int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).
		Where("kind = ? AND revocation_list_id NOT IN (SELECT id FROM \"device-management\".device_revocation_lists WHERE deleted_at IS NULL)", RevokedKindCRL).
		Count(&orphans).Error)
	assert.Zero(t, orphans, "no entry outlives its list")
}
