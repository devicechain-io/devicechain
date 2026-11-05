// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// X.509 revocation against a REAL PostgreSQL: the migration's constraints and its re-runnability,
// the anchor lock order, and the two interleavings of a revocation with a registration. The
// SQLite fixture drops FOR UPDATE / FOR SHARE and has no foreign keys, so none of these is
// visible there. The interleavings are FORCED (a callback pauses one side while the test waits for
// the other to be provably blocked on a lock), not raced.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'X509Revocation|Revocation.*Postgres' -v
package model

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func (f *pgCertFixture) revokeFingerprint(api *Api, fp string) error {
	_, err := api.RevokeDeviceCertificate(f.ctx, &DeviceCertificateRevokeRequest{AnchorToken: "maker", FingerprintSha256: &fp})
	return err
}

func (f *pgCertFixture) revokedRows() (counter, actual int64) {
	f.t.Helper()
	a, err := f.api.trustAnchorByToken(f.ctx, "maker")
	require.NoError(f.t, err)
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("trust_anchor_id = ?", a.ID).Count(&actual).Error)
	return a.RevocationRows.Int64, actual
}

// holdRevoke pauses the revocation of fingerprint right after its entry is inserted: the anchor is
// held FOR UPDATE and the entry is uncommitted.
func (f *pgCertFixture) holdRevoke(fingerprint string) (reached <-chan struct{}, release func()) {
	f.t.Helper()
	reachedCh, releaseCh := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	name := fmt.Sprintf("test:hold-revoke-%d", time.Now().UnixNano())
	require.NoError(f.t, f.mgr.Database.Callback().Create().After("gorm:create").Register(name, func(db *gorm.DB) {
		e, ok := db.Statement.Dest.(*DeviceRevokedCertificate)
		if !ok || db.Error != nil || e.Fingerprint == nil || *e.Fingerprint != fingerprint {
			return
		}
		once.Do(func() {
			close(reachedCh)
			<-releaseCh
		})
	}))
	rel := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	f.t.Cleanup(func() {
		rel()
		_ = f.mgr.Database.Callback().Create().Remove(name)
	})
	return reachedCh, rel
}

// ─── The migration ─────────────────────────────────────────────────────────

func TestX509RevocationSchemaOnPostgres(t *testing.T) {
	f := newPgCertFixture(t)
	sys := f.mgr.Database.WithContext(core.WithSystemContext(context.Background()))

	t.Run("the new columns are nullable with no default", func(t *testing.T) {
		var cols []struct{ Table, Column, Nullable, Default string }
		require.NoError(t, sys.Raw(`SELECT table_name AS "table", column_name AS "column", is_nullable AS nullable,
			coalesce(column_default, '') AS "default" FROM information_schema.columns
			WHERE table_schema = 'device-management' AND column_name IN
			('revocation_mode', 'revocation_stale_grace', 'revocation_rows', 'not_before')
			AND NOT (table_name = 'device_trust_anchors' AND column_name = 'not_before') ORDER BY column_name`).Scan(&cols).Error)
		require.Len(t, cols, 4)
		for _, c := range cols {
			assert.Equal(t, "YES", c.Nullable, c.Column)
			assert.Empty(t, c.Default, "%s: a default would put a value in the catalog's attmissingval", c.Column)
		}
	})

	t.Run("no constraint is left NOT VALID", func(t *testing.T) {
		var n int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_namespace ns ON ns.oid = t.relnamespace WHERE ns.nspname = 'device-management' AND NOT c.convalidated`).Scan(&n).Error)
		assert.Zero(t, n)
	})

	t.Run("the CHECK keeps a MANUAL and a CRL row apart", func(t *testing.T) {
		a, err := f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		fp := strings.Repeat("a", 64)
		serial, key, name := "1f", strings.Repeat("b", 64), strings.Repeat("c", 64)
		bad := []DeviceRevokedCertificate{
			{Kind: RevokedKindManual, Fingerprint: nil},                  // no fingerprint
			{Kind: RevokedKindManual, Fingerprint: &fp, Serial: &serial}, // a serial on a manual row
			{Kind: RevokedKindCRL, Fingerprint: &fp, IssuerKey: &key, IssuerName: &name, Serial: &serial},
			{Kind: RevokedKindCRL, IssuerKey: &key, IssuerName: &name, Serial: &serial}, // no list
			{Kind: "OTHER", Fingerprint: &fp},
		}
		for i, row := range bad {
			row.TrustAnchorId, row.Reason, row.RevokedAt = a.ID, "unspecified", time.Now()
			require.Error(t, f.api.RDB.DB(f.ctx).Create(&row).Error, "row %d should be refused by the CHECK", i)
		}
		good := DeviceRevokedCertificate{TrustAnchorId: a.ID, Kind: RevokedKindManual, Fingerprint: &fp, Reason: "unspecified", RevokedAt: time.Now()}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&good).Error, "the control")
		require.NoError(t, f.api.RDB.DB(f.ctx).Delete(&good).Error)
	})

	t.Run("an anchor with revocation data cannot be deleted from under it", func(t *testing.T) {
		a, err := f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		require.NoError(t, f.revokeFingerprint(f.api, x509trust.Fingerprint(f.ca.leaf(t, "x").cert)))
		raw := f.api.RDB.DB(f.ctx).Unscoped().Where("id = ?", a.ID).Delete(&DeviceTrustAnchor{}).Error
		require.Error(t, raw, "ON DELETE RESTRICT: the foreign key must refuse")
		_, err = f.api.UnrevokeDeviceCertificate(f.ctx, "maker", x509trust.Fingerprint(f.ca.leaf(t, "x").cert))
		require.NoError(t, err)
	})

	t.Run("the issuer identity is unique and a list cascades its entries", func(t *testing.T) {
		a, err := f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		issuer := DeviceRevocationIssuer{TrustAnchorId: a.ID, IssuerKey: strings.Repeat("1", 64), IssuerName: strings.Repeat("2", 64), HighWaterCrlNumber: "5"}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&issuer).Error)
		dup := DeviceRevocationIssuer{TrustAnchorId: a.ID, IssuerKey: issuer.IssuerKey, IssuerName: issuer.IssuerName, HighWaterCrlNumber: "6"}
		require.Error(t, f.api.RDB.DB(f.ctx).Create(&dup).Error, "one issuer row per identity")
		sameKeyOtherName := DeviceRevocationIssuer{TrustAnchorId: a.ID, IssuerKey: issuer.IssuerKey, IssuerName: strings.Repeat("3", 64), HighWaterCrlNumber: "1"}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&sameKeyOtherName).Error, "a key reused under another name is another issuer")

		list := DeviceRevocationList{TrustAnchorId: a.ID, RevocationIssuerId: issuer.ID, IssuerKey: issuer.IssuerKey, IssuerName: issuer.IssuerName,
			IssuerCertificatePem: "pem", IssuerSubject: "CN=x", CrlNumber: "123456789012345678901234567890", ThisUpdate: time.Now(),
			NextUpdate: time.Now().Add(time.Hour), EntryCount: 1, CrlSha256: strings.Repeat("d", 64), Source: RevocationSourceUpload}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&list).Error, "a 30-digit CRL number fits numeric(50,0)")
		list2 := list
		list2.ID = 0
		require.Error(t, f.api.RDB.DB(f.ctx).Create(&list2).Error, "one current list per issuer identity")

		serial, key, name := "1f", issuer.IssuerKey, issuer.IssuerName
		entry := DeviceRevokedCertificate{TrustAnchorId: a.ID, Kind: RevokedKindCRL, RevocationListId: &list.ID,
			IssuerKey: &key, IssuerName: &name, Serial: &serial, Reason: "keyCompromise", RevokedAt: time.Now()}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&entry).Error)
		require.Error(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&issuer).Error, "an issuer with a list is restricted")
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&list).Error)
		var left int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceRevokedCertificate{}).Where("kind = ?", RevokedKindCRL).Count(&left).Error)
		assert.Zero(t, left, "the list's entries cascade with it")
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&issuer).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&sameKeyOtherName).Error)
	})

	t.Run("a credential's links cascade with it and the read finds CRL entries by issuer and serial", func(t *testing.T) {
		leaf := f.ca.leaf(t, "SN-LINK")
		res, err := f.registerLeaf(leaf)
		require.NoError(t, err)
		var cred DeviceCredential
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("credential_id = ?", x509trust.Fingerprint(leaf.cert)).First(&cred).Error)
		var links int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Where("credential_id = ?", cred.ID).Count(&links).Error)
		assert.EqualValues(t, 1, links)

		// A CRL entry for exactly this leaf, as a stored list would carry it, is found on the
		// partial index by (issuer key, issuer name, serial) and refuses the chain.
		a, err := f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		issuer := x509trust.IssuerIDOfCA(f.ca.cert)
		serial, _ := x509trust.SerialHex(leaf.cert.SerialNumber)
		di := DeviceRevocationIssuer{TrustAnchorId: a.ID, IssuerKey: issuer.Key, IssuerName: issuer.Name, HighWaterCrlNumber: "1"}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&di).Error)
		dl := DeviceRevocationList{TrustAnchorId: a.ID, RevocationIssuerId: di.ID, IssuerKey: issuer.Key, IssuerName: issuer.Name,
			IssuerCertificatePem: f.ca.pem(), IssuerSubject: "CN=Maker Root", CrlNumber: "1", ThisUpdate: time.Now().Add(-time.Hour),
			NextUpdate: time.Now().Add(time.Hour), EntryCount: 1, CrlSha256: strings.Repeat("e", 64), Source: RevocationSourceUpload}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&dl).Error)
		entry := DeviceRevokedCertificate{TrustAnchorId: a.ID, Kind: RevokedKindCRL, RevocationListId: &dl.ID,
			IssuerKey: &issuer.Key, IssuerName: &issuer.Name, Serial: &serial, Reason: "keyCompromise", RevokedAt: time.Now()}
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&entry).Error)
		require.NoError(t, bumpRevocationRows(f.api.RDB.DB(f.ctx), a.ID, 2))

		verified, err := x509trust.VerifyClientChain(f.ca.cert, []*x509.Certificate{leaf.cert}, time.Now())
		require.NoError(t, err)
		a, err = f.api.trustAnchorByToken(f.ctx, "maker")
		require.NoError(t, err)
		state, err := f.api.ResolveX509Revocation(f.ctx, a, verified.RevocationKeys(), time.Now())
		require.NoError(t, err)
		_, err = verified.ApplyRevocation(state)
		require.Error(t, err)
		assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err), "a CRL entry on Postgres, found by issuer and serial")
		other := f.ca.leaf(t, "SN-OTHER")
		ov, err := x509trust.VerifyClientChain(f.ca.cert, []*x509.Certificate{other.cert}, time.Now())
		require.NoError(t, err)
		state, err = f.api.ResolveX509Revocation(f.ctx, a, ov.RevocationKeys(), time.Now())
		require.NoError(t, err)
		_, err = ov.ApplyRevocation(state)
		assert.NoError(t, err, "the control: another serial under the same issuer is not listed")
		_ = res

		// The credential goes; its links go with it (ON DELETE CASCADE).
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(&cred).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredentialChainLink{}).Where("credential_id = ?", cred.ID).Count(&links).Error)
		assert.Zero(t, links)
	})

	t.Run("every statement is re-runnable, from the top and from a half-applied state", func(t *testing.T) {
		replay := func() error {
			return f.mgr.Database.Transaction(func(tx *gorm.DB) error {
				return schema.NewX509RevocationSchema().Migrate(tx.WithContext(core.WithSystemContext(context.Background())))
			})
		}
		for i := 0; i < 2; i++ {
			require.NoError(t, replay(), "replay %d", i+1)
		}
		// A failure partway leaves a prefix. Take pieces away in the order the migration builds
		// them, and prove the replay restores each.
		for _, drop := range []string{
			`ALTER TABLE "device-management".device_revoked_certificates DROP CONSTRAINT ck_device_revoked_certificates_kind`,
			`ALTER TABLE "device-management".device_revoked_certificates DROP CONSTRAINT fk_device_revoked_certificates_list`,
			`ALTER TABLE "device-management".device_credential_chain_links DROP CONSTRAINT fk_device_credential_chain_links_credential`,
			`DROP INDEX "device-management".ix_device_revoked_certificates_serial`,
			`DROP INDEX "device-management".uix_device_revocation_issuers_identity`,
			`DROP INDEX "device-management".uix_device_revocation_lists_identity`,
			`ALTER TABLE "device-management".device_trust_anchors DROP COLUMN revocation_rows`,
			`ALTER TABLE "device-management".pending_session_kicks DROP COLUMN not_before`,
		} {
			require.NoError(t, sys.Exec(drop).Error, drop)
			require.NoError(t, replay(), "replay after: %s", drop)
		}
		var present int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_constraint WHERE conname IN
			('ck_device_revoked_certificates_kind', 'fk_device_revoked_certificates_list', 'fk_device_credential_chain_links_credential')`).Scan(&present).Error)
		assert.EqualValues(t, 3, present)
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'device-management' AND indexname IN
			('ix_device_revoked_certificates_serial', 'uix_device_revocation_issuers_identity', 'uix_device_revocation_lists_identity')`).Scan(&present).Error)
		assert.EqualValues(t, 3, present)
		require.NoError(t, sys.Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'device-management'
			AND ((table_name = 'device_trust_anchors' AND column_name = 'revocation_rows')
			OR (table_name = 'pending_session_kicks' AND column_name = 'not_before'))`).Scan(&present).Error)
		assert.EqualValues(t, 2, present)
	})
}

// ─── The two interleavings of a revocation and a registration (§4.7a) ──────

// 🔑 (a) THE REVOCATION COMMITS FIRST. The revoke holds the anchor FOR UPDATE with its entry
// uncommitted; the registration's lock on the anchor queues behind it; when the revoke commits the
// registration's in-transaction recheck reads the entry and refuses with NOTHING written.
func TestX509RevocationCommittingFirstRefusesTheRegistrationWithNothingWritten(t *testing.T) {
	f := newPgCertFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	twin := f.ca.leaf(t, "SN-2")
	fp := x509trust.Fingerprint(leaf.cert)
	reached, release := f.holdRevoke(fp)

	revokeDone := make(chan error, 1)
	go func() { revokeDone <- f.revokeFingerprint(f.api, fp) }()
	awaitSignal(t, reached, "the revoke to insert its entry")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case r := <-regDone:
		t.Fatalf("the registration finished while the revocation held the anchor: %+v", r)
	case <-time.After(750 * time.Millisecond):
	}

	release()
	require.NoError(t, <-revokeDone)
	reg := <-regDone
	require.Error(t, reg.err, "a certificate revoked before the registration committed was registered")
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(reg.err), "%v", reg.err)
	assert.EqualValues(t, 0, f.count(&Device{}), "the refused registration created a device")
	assert.EqualValues(t, 0, f.count(&DeviceCredential{}))
	assert.EqualValues(t, 0, f.count(&DeviceCredentialChainLink{}))

	res, err := f.registerLeaf(twin)
	require.NoError(t, err, "the control: an unrevoked twin registers")
	assert.Equal(t, ProvisionCreated, res.Outcome)
}

// 🔑 (b) THE REGISTRATION COMMITS FIRST. It holds the anchor FOR SHARE with its credential
// written; the revoke queues behind it, and when it commits the credential and its links exist for
// the revoke to find. Both complete, no deadlock, and the very next check refuses the certificate.
func TestX509RevocationWaitsForARegistrationInFlightAndThenFindsItsCredential(t *testing.T) {
	f := newPgCertFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	fp := x509trust.Fingerprint(leaf.cert)
	reached, release := f.holdRegistration("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	revoked := make(chan error, 1)
	go func() { revoked <- f.revokeFingerprint(f.api, fp) }()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case err := <-revoked:
		t.Fatalf("the revocation finished while a registration held the anchor share lock (%v)", err)
	case <-time.After(750 * time.Millisecond):
	}

	release()
	reg := <-regDone
	require.NoError(t, reg.err)
	assert.Equal(t, ProvisionCreated, reg.res.Outcome)
	require.NoError(t, <-revoked)

	// By the time the revoke returns, the credential and its link are committed for it to find.
	assert.EqualValues(t, 1, f.count(&DeviceCredential{}))
	assert.EqualValues(t, 1, f.count(&DeviceCredentialChainLink{}))
	a, err := f.api.trustAnchorByToken(f.ctx, "maker")
	require.NoError(t, err)
	verified, err := x509trust.VerifyClientChain(f.ca.cert, []*x509.Certificate{leaf.cert}, time.Now())
	require.NoError(t, err)
	state, err := f.api.ResolveX509Revocation(f.ctx, a, verified.RevocationKeys(), time.Now())
	require.NoError(t, err)
	_, err = verified.ApplyRevocation(state)
	require.Error(t, err)
	assert.Equal(t, x509trust.ReasonRevoked, x509trust.ReasonOf(err))
}

// A revoke queued behind a credential create (anchor SHARE then device) and an anchor disable
// (anchor UPDATE) all complete: the lock order anchor -> issuer -> entries never inverts.
func TestX509RevocationWritesDoNotDeadlockWithTheOtherAnchorWriters(t *testing.T) {
	f := newPgCertFixture(t)
	replica := NewApi(newPostgresRdbManager(t)) // a second replica on its own pool
	replica.DeviceSecretKey = f.api.DeviceSecretKey
	replica.SessionKicker = f.kicker

	const rounds = 12
	var wg sync.WaitGroup
	errs := make(chan error, rounds*4)
	for i := 0; i < rounds; i++ {
		leaf := f.ca.leaf(t, fmt.Sprintf("SN-%d", i))
		fp := x509trust.Fingerprint(leaf.cert)
		other := f.ca.leaf(t, fmt.Sprintf("SN-X-%d", i))
		wg.Add(4)
		go func() { defer wg.Done(); errs <- f.revokeFingerprint(f.api, fp) }()
		go func() {
			defer wg.Done()
			_, err := f.registerLeaf(other)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := replica.UnrevokeDeviceCertificate(f.ctx, "maker", fp)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{})
			errs <- err
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("revocation writes deadlocked with the other anchor writers")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			assert.NotContains(t, err.Error(), "deadlock", "a deadlock between the anchor writers")
			assert.NotContains(t, err.Error(), "40P01")
		}
	}
	counter, actual := f.revokedRows()
	assert.Equal(t, actual, counter, "the counter must equal the stored entries after concurrent revokes and unrevokes")
}

// Two replicas revoking the same certificate at once end with ONE entry and a counter of one.
func TestX509RevocationTwoReplicasRevokingOneCertificateStoreOneEntry(t *testing.T) {
	f := newPgCertFixture(t)
	replica := NewApi(newPostgresRdbManager(t))
	fp := x509trust.Fingerprint(f.ca.leaf(t, "SN-1").cert)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		api := f.api
		if i%2 == 1 {
			api = replica
		}
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.revokeFingerprint(api, fp) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	counter, actual := f.revokedRows()
	assert.EqualValues(t, 1, actual, "twenty concurrent revokes of one certificate stored more than one entry")
	assert.EqualValues(t, 1, counter)
}
