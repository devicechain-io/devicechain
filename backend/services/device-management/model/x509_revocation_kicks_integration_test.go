// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The live-session kick of a certificate revocation against a REAL PostgreSQL: the kick queue's
// upsert (which resets not_before), the generation-guarded re-sweep UPDATE under a row value and a
// bigint origin, the anchor-wide targeting subquery, and the 10^5-device queue write. SQLite
// refuses a statement that binds as many parameters as Postgres allows, so the fleet-sized write
// can only be measured here.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'RevocationKick' -v
//	  go test -tags integration -run xxx -bench QueueSessionKicks ./model/...
package model

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔑 THE WHOLE REVOCATION KICK ON POSTGRES: leaf targeting, the re-sweep, the requeue reset, and an
// anchor-wide kick over many devices through the one-statement subquery.
func TestRevocationKickPostgres(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = 300 * time.Millisecond
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	const n = 150
	var first *pkiNode
	for i := 0; i < n; i++ {
		dev := fmt.Sprintf("kdev-%03d", i)
		f.device(dev)
		leaf := inter.leaf(t, fmt.Sprintf("SN-%d", i))
		if i == 0 {
			first = leaf
		}
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create(dev+"-c", dev, leaf.pem()+inter.pem(), "maker"))
		require.NoError(t, err)
	}
	revoked := func() int {
		f.kicker.mu.Lock()
		defer f.kicker.mu.Unlock()
		c := 0
		for _, call := range f.kicker.calls {
			if call.reason == messaging.SessionKickCertificateRevoked {
				c += len(call.tokens)
			}
		}
		return c
	}
	rows := func() []PendingRevocationKick {
		var out []PendingRevocationKick
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("origin = ?", revocationKickOrigin(f.anchor.ID)).Find(&out).Error)
		return out
	}

	// A leaf: one device, its row waiting for the re-sweep.
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(first.cert))})
	require.NoError(t, err)
	assert.Equal(t, 1, revoked())
	require.Len(t, rows(), 1)
	require.NotNil(t, rows()[0].NotBefore)

	// The re-sweep runs when due and clears the row.
	require.Eventually(t, func() bool {
		// This tenant's pass only: a shared server holds other tests' debts.
		require.NoError(t, f.api.drainRevocationKicks(f.ctx, f.tenant, 0, 0))
		return len(rows()) == 0
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, 2, revoked(), "swept twice")

	// An intermediate: every device under the anchor, in one targeting statement; the requeue of
	// the first device's row (already cleared) and of the others is a plain insert.
	_, err = f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
	require.NoError(t, err)
	assert.Equal(t, 2+n, revoked(), "all %d devices were asked", n)
	require.Len(t, rows(), n)

	// Re-queued while waiting out the re-sweep: the first sweep is due again (not_before reset).
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		qerr := f.api.queueRevocationKicksOnTx(tx, f.tenant, f.anchor.ID,
			RevocationDelta{Fingerprints: []string{x509trust.Fingerprint(inter.cert)}}, true)
		return qerr
	}))
	require.NoError(t, f.expand(), "the marker is expanded outside the lock")
	for _, r := range rows() {
		assert.Nil(t, r.NotBefore, "a requeued debt's first sweep is due at once")
	}
}

// BenchmarkQueueSessionKicksOnTx is the whole queue write (dedupe, sort, chunked upsert) of a
// fleet-sized kick, up to the 10^5 devices the arc names. ns/op over n is flat when it is linear.
func BenchmarkQueueSessionKicksOnTx(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		tokens := make([]string, 0, n)
		for i := 0; i < n; i++ {
			tokens = append(tokens, fmt.Sprintf("device-%07d", i))
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			mgr := newPostgresRdbManager(b)
			api := NewApi(mgr)
			api.SessionKicker = &togglingKicker{up: true}
			tenant := fmt.Sprintf("bench%d", time.Now().UnixNano())
			ctx := core.WithTenant(context.Background(), tenant)
			// The rows are this benchmark's alone to leave behind: a shared server's background
			// drain would otherwise find a hundred thousand debts of a tenant that never existed.
			b.Cleanup(func() {
				_ = api.RDB.DB(ctx).Where("tenant_id = ?", tenant).Delete(&PendingSessionKick{}).Error
			})
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				require.NoError(b, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
					return api.queueRevocationKickRowsOnTx(tx, tenant, 1, tokens)
				}))
			}
		})
	}
}

// A CRL upload ends the sessions of the certificates it newly revokes, on Postgres: the staged
// serials of the new entries are kept in a temp table (ON COMMIT DROP) and joined to the links after
// the anchor row is locked; a replacement kicks only what it adds.
func TestRevocationKickCrlUploadPostgres(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	leaves := make([]*pkiNode, 3)
	for i := range leaves {
		dev := fmt.Sprintf("cdev-%d", i)
		f.device(dev)
		leaves[i] = f.ca.leaf(t, fmt.Sprintf("SN-%d", i))
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create(dev+"-c", dev, leaves[i].pem(), "maker"))
		require.NoError(t, err)
	}
	asked := func() []string {
		f.kicker.mu.Lock()
		defer f.kicker.mu.Unlock()
		var out []string
		for _, c := range f.kicker.calls {
			if c.reason == messaging.SessionKickCertificateRevoked {
				out = append(out, c.tokens...)
			}
		}
		slices.Sort(out)
		return out
	}
	res, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaves[0].cert))}))
	require.NoError(t, err)
	assert.True(t, res.SessionsEnded)
	assert.Equal(t, []string{"cdev-0"}, asked())

	res, err = f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(2),
		revoked: revokedSerials(serialOf(leaves[0].cert), serialOf(leaves[2].cert))}))
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewlyRevoked)
	assert.Equal(t, []string{"cdev-0", "cdev-2"}, asked(), "the replacement asks only about what it adds")
}

// kickedByRevocation is every device token the kicker was asked about for a certificate revocation.
func (f *pgCertFixture) kickedByRevocation() []string {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	var out []string
	for _, c := range f.kicker.calls {
		if c.reason == messaging.SessionKickCertificateRevoked {
			out = append(out, c.tokens...)
		}
	}
	return out
}

// 🔴 THE LOCK ORDER OF THE KICK, PROVED BY AN INTERLEAVING. A JIT registration holds the anchor FOR
// SHARE with its credential written and uncommitted. A revocation of its certificate starts; it
// writes its entry and then waits for the anchor row FOR UPDATE. The registration commits, and ONLY
// THEN may the kick find its credential's links: a kick built before the anchor lock was won reads
// the links as they were (none) and misses a session that is about to exist. Either the
// registration is refused (the revocation committed first) or its device is asked about.
func TestRevocationKickManualRevokeAfterAnInFlightRegistrationFindsItsDevice(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	leaf := f.ca.leaf(t, "SN-1")
	reached, release := f.holdRegistration("SN-1")
	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	revoked := make(chan error, 1)
	go func() { revoked <- f.revokeFingerprint(f.api, x509trust.Fingerprint(leaf.cert)) }()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case err := <-revoked:
		t.Fatalf("the revocation finished while a registration held the anchor share lock (%v)", err)
	case <-time.After(750 * time.Millisecond):
	}
	release()
	reg := <-regDone
	require.NoError(t, <-revoked)
	if reg.err != nil {
		assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(reg.err), "refused only as revoked: %v", reg.err)
		return
	}
	assert.Contains(t, f.kickedByRevocation(), reg.res.DeviceToken,
		"the registration committed before the revocation did, so its session must be asked about")
}

// The same interleaving for a CRL upload: it stages and writes its entries (compatible with the
// registration's FOR SHARE), then waits for the anchor FOR UPDATE; the kick is queued only after
// that, against the committed links.
func TestRevocationKickCrlUploadAfterAnInFlightRegistrationFindsItsDevice(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	leaf := f.ca.leaf(t, "SN-1")
	reached, release := f.holdRegistration("SN-1")
	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	uploaded := make(chan error, 1)
	go func() {
		_, err := f.putVia(f.api, f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaf.cert))}))
		uploaded <- err
	}()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case err := <-uploaded:
		t.Fatalf("the upload finished while a registration held the anchor share lock (%v)", err)
	case <-time.After(750 * time.Millisecond):
	}
	release()
	reg := <-regDone
	require.NoError(t, <-uploaded)
	if reg.err != nil {
		assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(reg.err), "refused only as revoked: %v", reg.err)
		return
	}
	assert.Contains(t, f.kickedByRevocation(), reg.res.DeviceToken,
		"the registration committed before the upload did, so its session must be asked about")
}
