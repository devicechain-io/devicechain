// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The revocation kick marker against a REAL PostgreSQL: the anchor row lock is released before the
// per-device kick rows are written, a registration racing a CA-level revoke is kicked or refused,
// and a marker nobody expanded (a crash between the revocation's commit and its expansion) is
// expanded by the background drain.
//
//	docker run -d --name dc-x509marker-pg -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-x509marker-pg 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'RevocationMarker' -v
package model

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// registerVia registers a leaf issued by inter (which chains to the fixture's CA), the way the
// callout does for a certificate presenting its intermediate.
func (f *pgCertFixture) registerVia(leaf, inter *pkiNode) (*ProvisionResult, error) {
	profiles, err := f.api.X509ProfileAnchors(f.ctx)
	if err != nil {
		return nil, err
	}
	verified, err := x509trust.VerifyClientChain(f.ca.cert, []*x509.Certificate{leaf.cert, inter.cert}, time.Now())
	if err != nil {
		return nil, err
	}
	return f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: profiles[0].Profile, Certificate: &X509Registration{
		Leaf: leaf.cert, NotAfter: verified.NotAfter, AnchorId: f.anchor.ID, AnchorFingerprint: f.anchor.FingerprintSha256, Verified: verified}})
}

func (f *pgCertFixture) markers() int64      { return f.count(&PendingRevocationKick{}) }
func (f *pgCertFixture) serialRows() int64   { return f.count(&PendingRevocationSerial{}) }
func (f *pgCertFixture) revocationRows() int { return len(f.revocationKickRows()) }

func (f *pgCertFixture) revocationKickRows() []PendingSessionKick {
	var out []PendingSessionKick
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("origin = ?", revocationKickOrigin(f.anchor.ID)).Find(&out).Error)
	return out
}

func (f *pgCertFixture) kickedRevoked() map[string]bool {
	f.kicker.mu.Lock()
	defer f.kicker.mu.Unlock()
	out := map[string]bool{}
	for _, c := range f.kicker.calls {
		if c.reason == messaging.SessionKickCertificateRevoked {
			for _, t := range c.tokens {
				out[t] = true
			}
		}
	}
	return out
}

// pgIntermediateFleet registers n devices through an intermediate and returns it with their tokens.
func pgIntermediateFleet(t *testing.T, f *pgCertFixture, n int) (*pkiNode, []string) {
	t.Helper()
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := f.registerVia(inter.leaf(t, fmt.Sprintf("SN-%d", i)), inter)
		require.NoError(t, err)
		tokens = append(tokens, res.DeviceToken)
	}
	return inter, tokens
}

// 🔑 THE ANCHOR LOCK IS RELEASED BEFORE THE PER-DEVICE ROWS ARE WRITTEN. With the expansion paused
// (it has not read a device yet) a certificate registration's lock on the anchor row, FOR SHARE, is
// granted at once, the marker is on record and no per-device row exists. A revoke that expanded
// inside the locked transaction would hold the row FOR UPDATE here and the probe would time out.
func TestRevocationMarkerReleasesTheAnchorLockBeforeTheExpansion(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	inter, tokens := pgIntermediateFleet(t, f, 30)

	type probe struct {
		err     error
		markers int64
		rows    int
		waited  time.Duration
	}
	probed := make(chan probe, 1)
	revocationExpandHook = func() error {
		revocationExpandHook = nil
		var p probe
		start := time.Now()
		p.err = f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '1500ms'").Error; err != nil {
				return err
			}
			return tx.Exec("SELECT id FROM device_trust_anchors WHERE id = ? FOR SHARE", f.anchor.ID).Error
		})
		p.waited = time.Since(start)
		p.markers, p.rows = f.markers(), f.revocationRows()
		probed <- p
		return nil
	}
	t.Cleanup(func() { revocationExpandHook = nil })

	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
	require.NoError(t, err)

	p := <-probed
	require.NoError(t, p.err, "the anchor row was still locked while the kick rows were being written (waited %s)", p.waited)
	assert.EqualValues(t, 1, p.markers, "the marker was committed with the entry")
	assert.Zero(t, p.rows, "no per-device row existed yet")
	assert.EqualValues(t, 0, f.markers(), "expanded and cleared")
	assert.Equal(t, len(tokens), f.revocationRows())
	for _, tok := range tokens {
		assert.True(t, f.kickedRevoked()[tok], "%s was asked", tok)
	}
}

// 🔑 A REGISTRATION RACING A CA-LEVEL REVOKE IS KICKED OR REFUSED. One that holds the anchor FOR SHARE
// when the revoke arrives makes the revoke wait, commits first, and its device is in the expansion;
// one that arrives after the revoke committed (here: between the commit and the expansion) is refused.
func TestRevocationMarkerRegistrationRacingTheRevokeIsKickedOrRefused(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	inter := newPki(t, f.ca, pkiOpts{cn: "Line CA", ca: true})
	racing := inter.leaf(t, "SN-racing")
	reached, release := f.holdAfterDevice("SN-racing")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerVia(racing, inter); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its device")

	late := inter.leaf(t, "SN-late")
	var lateErr error
	revocationExpandHook = func() error {
		revocationExpandHook = nil
		_, lateErr = f.registerVia(late, inter)
		return nil
	}
	t.Cleanup(func() { revocationExpandHook = nil })

	revoked := make(chan error, 1)
	go func() {
		_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
			AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
		revoked <- err
	}()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case err := <-revoked:
		t.Fatalf("the revoke finished while a registration held the anchor: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	require.NoError(t, <-revoked)
	reg := <-regDone
	require.NoError(t, reg.err, "the registration that held the anchor first is admitted")

	assert.True(t, f.kickedRevoked()[reg.res.DeviceToken], "its device was kicked")
	require.Error(t, lateErr, "a registration after the revoke committed is refused")
	assert.EqualValues(t, 1, f.count(&Device{}), "the refused registration created no device")
}

// 🔑 A MARKER NOBODY EXPANDED IS EXPANDED BY THE BACKGROUND DRAIN. The revocation's expansion fails
// (the process died between the commit and the expansion): the entry stands, the marker is on record,
// the writer says the sessions were not confirmed, and the drain then writes the rows and asks.
func TestRevocationMarkerLeftByACrashIsExpandedByTheDrain(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	inter, tokens := pgIntermediateFleet(t, f, 12)

	revocationExpandHook = func() error { return errors.New("crash") }
	t.Cleanup(func() { revocationExpandHook = nil })
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.EqualValues(t, 1, f.markers(), "the marker survives")
	assert.Zero(t, f.revocationRows(), "no per-device row yet")
	assert.Empty(t, f.kickedRevoked())

	revocationExpandHook = nil
	_, _ = f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	assert.EqualValues(t, 0, f.markers(), "the drain expanded it")
	for _, tok := range tokens {
		assert.True(t, f.kickedRevoked()[tok], "%s was asked by the drain", tok)
	}

	// A repeat of the revoke finds the entry, no marker and nothing owed: settled.
	_, err = f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
	require.NoError(t, err)
}

// The CRL path: the serials are stashed before the anchor lock, the marker carries the generation,
// the expansion joins them to the links, and both tables are empty afterwards. A crash in between
// leaves the serials for the drain.
func TestRevocationMarkerCrlUploadExpandsOutsideTheLock(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	leaves := make([]*pkiNode, 3)
	tokens := make([]string, 3)
	for i := range leaves {
		leaves[i] = f.ca.leaf(t, fmt.Sprintf("SN-%d", i))
		res, err := f.registerLeaf(leaves[i])
		require.NoError(t, err)
		tokens[i] = res.DeviceToken
	}
	put := func(crl []byte) error {
		_, err := f.api.PutDeviceRevocationList(f.ctx, &RevocationListPutRequest{AnchorToken: "maker", Crl: crl})
		return err
	}

	revocationExpandHook = func() error { return errors.New("crash") }
	t.Cleanup(func() { revocationExpandHook = nil })
	err := put(f.ca.crl(t, crlSpec{number: big.NewInt(1), revoked: revokedSerials(serialOf(leaves[0].cert), serialOf(leaves[1].cert))}))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.EqualValues(t, 1, f.markers())
	assert.EqualValues(t, 2, f.serialRows())
	assert.Empty(t, f.kickedRevoked())

	revocationExpandHook = nil
	_, _ = f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	assert.EqualValues(t, 0, f.markers())
	assert.EqualValues(t, 0, f.serialRows())
	assert.Equal(t, map[string]bool{tokens[0]: true, tokens[1]: true}, f.kickedRevoked())
}

// TestRevocationMarkerLockHoldMeasurement measures how long the locked transaction of a CA-level
// revocation holds the anchor row at fleet size: the previous design (find every device, upsert a
// kick row each, under the lock) against the marker (one row), and the expansion that now runs
// outside it. Skipped unless DC_MARKER_MEASURE is set:
//
//	DC_MARKER_MEASURE=20000,100000 DC_IT_PGPORT=... go test -tags integration -count=1 ./model/ -run LockHoldMeasurement -v
func TestRevocationMarkerLockHoldMeasurement(t *testing.T) {
	sizes := os.Getenv("DC_MARKER_MEASURE")
	if sizes == "" {
		t.Skip("set DC_MARKER_MEASURE=20000,100000 to measure")
	}
	for _, field := range strings.Split(sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		require.NoError(t, err)
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			f := newPgCertFixture(t)
			require.NoError(t, f.api.RDB.DB(f.ctx).Exec(
				`INSERT INTO devices (tenant_id, token, name, device_type_id, created_at, updated_at)
				 SELECT ?, 'm-dev-' || g, 'm-dev-' || g, (SELECT id FROM device_types WHERE tenant_id = ? LIMIT 1), now(), now()
				 FROM generate_series(1, ?) g`, f.tenant, f.tenant, n).Error)
			require.NoError(t, f.api.RDB.DB(f.ctx).Exec(
				`INSERT INTO device_credentials (tenant_id, token, device_id, credential_type, trust_anchor_id, credential_id, enabled, created_at, updated_at)
				 SELECT ?, 'm-cred-' || d.id, d.id, 'X509_CERTIFICATE', ?, 'pin-' || d.id, true, now(), now()
				 FROM devices d WHERE d.tenant_id = ? AND d.token LIKE 'm-dev-%'`, f.tenant, f.anchor.ID, f.tenant).Error)

			lockHold := func(inLock func(tx *gorm.DB) error) time.Duration {
				var held time.Duration
				require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
					if _, err := lockTrustAnchorByToken(tx, "maker"); err != nil {
						return err
					}
					start := time.Now()
					if err := inLock(tx); err != nil {
						return err
					}
					held = time.Since(start)
					return nil
				}))
				return held
			}
			before := lockHold(func(tx *gorm.DB) error {
				tokens, err := anchorDeviceTokens(tx, f.tenant, f.anchor.ID)
				if err != nil {
					return err
				}
				return f.api.queueSessionKicksOnTx(tx, f.tenant, revocationKickOrigin(f.anchor.ID), tokens)
			})
			require.NoError(t, f.api.RDB.DB(f.ctx).Where("tenant_id = ?", f.tenant).Delete(&PendingSessionKick{}).Error)
			after := lockHold(func(tx *gorm.DB) error {
				return f.api.queueRevocationKicksOnTx(tx, f.tenant, f.anchor.ID,
					RevocationDelta{Fingerprints: []string{"ab" + strings.Repeat("0", 62)}, CA: true}, true)
			})
			start := time.Now()
			require.NoError(t, f.api.expandRevocationMarkers(f.ctx, f.tenant, f.anchor.ID))
			expand := time.Since(start)
			require.EqualValues(t, n, f.count(&PendingSessionKick{}))
			t.Logf("n=%d: anchor lock held %s before (kick rows under the lock), %s now (marker); expansion outside the lock %s",
				n, before, after, expand)
		})
	}
}

// 🔑 A CREDENTIAL THAT LEAVES THE SET WHILE THE MARKER WAITS STILL OWES ITS DEVICE A KICK. With the
// expansion paused after the revocation committed, one device's only certificate credential is deleted
// and another's is moved to a third device, each with a responder that is down so the write's own inline
// kick fails. The expansion then no longer finds the first two devices through their credentials; the
// kicks queued by the delete and the move are all that name them. (Found by the second review.)
func TestRevocationMarkerCredentialDeletedOrMovedBeforeTheExpansionIsStillKicked(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	inter, tokens := pgIntermediateFleet(t, f, 4)
	credTokenOf := func(device string) string {
		var creds []DeviceCredential
		require.NoError(t, f.api.RDB.DB(f.ctx).Joins("JOIN devices ON devices.id = device_credentials.device_id").
			Where("devices.token = ? AND device_credentials.credential_type = ?", device, string(CredentialX509Certificate)).
			Find(&creds).Error)
		require.Len(t, creds, 1)
		return creds[0].Token
	}
	deleted, moved, target := tokens[0], tokens[1], tokens[2]
	deletedCred, movedCred := credTokenOf(deleted), credTokenOf(moved)

	var hookErr error
	revocationExpandHook = func() error {
		revocationExpandHook = nil
		f.kicker.set(false) // the writes' own inline kicks fail
		defer f.kicker.set(true)
		if _, err := f.api.DeleteDeviceCredential(f.ctx, deletedCred); err != nil && !errors.Is(err, ErrSessionsNotEnded) {
			hookErr = err
		}
		if _, err := f.api.UpdateDeviceCredential(f.ctx, movedCred,
			&DeviceCredentialUpdateRequest{DeviceToken: dcgraphql.OptionalStringOf(target)}); err != nil && !errors.Is(err, ErrSessionsNotEnded) {
			hookErr = err
		}
		return nil
	}
	t.Cleanup(func() { revocationExpandHook = nil })

	_, err := f.api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{
		AnchorToken: "maker", FingerprintSha256: strp(x509trust.Fingerprint(inter.cert))})
	require.NoError(t, hookErr)
	require.NoError(t, err)
	kicked := f.kickedRevoked()
	assert.True(t, kicked[deleted], "the device whose credential was deleted in the window")
	assert.True(t, kicked[moved], "the device whose credential was moved away in the window")
	assert.True(t, kicked[target], "the device it moved to")
}

// Concurrent expanders of one marker (the writer's call and the background drain, here eight at once)
// converge: no error, no deadlock, every device queued once, the marker gone.
func TestRevocationMarkerConcurrentExpandersConverge(t *testing.T) {
	f := newPgCertFixture(t)
	f.api.RevocationResweepAfter = time.Hour
	inter, tokens := pgIntermediateFleet(t, f, 40)
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueRevocationKicksOnTx(tx, f.tenant, f.anchor.ID,
			RevocationDelta{Fingerprints: []string{x509trust.Fingerprint(inter.cert)}, CA: true}, true)
	}))
	require.EqualValues(t, 1, f.markers())
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			errs <- f.api.expandRevocationMarkers(f.ctx, f.tenant, f.anchor.ID)
		}()
	}
	close(start)
	for i := 0; i < 8; i++ {
		require.NoError(t, <-errs)
	}
	assert.EqualValues(t, 0, f.markers())
	assert.Equal(t, len(tokens), f.revocationRows())
}
