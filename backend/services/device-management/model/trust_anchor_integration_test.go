// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Trust anchors, certificate credentials and their session kicks against a REAL PostgreSQL: the
// migration's indexes and foreign keys, the anchor row lock, the kick queue's upsert and
// row-value delete under a bigint origin, and the interleavings of a disable, a delete and a
// credential create.
//
// 🔴 WHY THESE CANNOT LIVE WITH api_trust_anchors_test.go. That file runs on in-memory SQLite,
// whose driver silently drops `FOR UPDATE`, has no foreign keys by default, and never builds the
// partial unique indexes the migration declares. With the locks, the constraints and the indexes
// deleted it stays green; each property below is one it cannot see.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'TrustAnchor|X509' -v
package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func (f *pgFixture) anchor(token string, ca *pkiNode) *DeviceTrustAnchor {
	f.t.Helper()
	a, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: token, CertificatePem: ca.pem()})
	require.NoError(f.t, err)
	return a
}

func (f *pgFixture) device(token string) {
	f.t.Helper()
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "dt-a"})
	require.NoError(f.t, err)
}

func (f *pgFixture) x509Credential(token, device string, ca *pkiNode, anchor, cn string) (*DeviceCredential, error) {
	return f.api.CreateDeviceCredential(f.ctx, x509Create(token, device, ca.leaf(f.t, cn).pem(), anchor))
}

func (f *pgFixture) pendingKicks() []PendingSessionKick {
	f.t.Helper()
	var rows []PendingSessionKick
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Order("device_token, origin").Find(&rows).Error)
	return rows
}

func isUniqueViolation(err error) bool { return conflict.Is(err) }

// The migration's indexes and foreign keys, exercised: the same CA twice is a conflict whether
// it comes under a new token or the same one, a token is free again once its anchor is deleted,
// and two devices cannot hold one pinned certificate.
func TestTrustAnchorIndexesAndPinUniquenessOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)

	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "again", CertificatePem: ca.pem()})
	require.True(t, isUniqueViolation(err), "the same CA under another token: %v", err)
	_, err = f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "maker", CertificatePem: newRootCA(t, "Other").pem()})
	require.True(t, isUniqueViolation(err), "another CA under the same token: %v", err)

	f.device("dev-1")
	f.device("dev-2")
	leaf := ca.leaf(t, "SN-1")
	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev-1", leaf.pem(), "maker"))
	require.NoError(t, err)
	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-2", "dev-2", leaf.pem(), "maker"))
	require.True(t, isUniqueViolation(err), "one certificate pinned by two devices: %v", err)

	// A tenant does not see another tenant's anchors, so the same CA is fine there.
	other := newPgFixture(t)
	other.anchor("maker", ca)

	ok, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "maker")
	require.ErrorIs(t, err, ErrEntityInUse)
	assert.False(t, ok)
	_, err = f.api.DeleteDeviceCredential(f.ctx, "c-1")
	require.NoError(t, err)
	ok, err = f.api.DeleteDeviceTrustAnchor(f.ctx, "maker")
	require.NoError(t, err)
	assert.True(t, ok)
	f.anchor("maker", ca) // the token and the fingerprint are free again
}

// The foreign keys are the backstop under the API's own counts: a delete that gets past them
// (here, straight SQL) is refused by the database, for a credential and for a profile.
func TestTrustAnchorForeignKeysRestrictOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	f.device("dev-1")
	_, err := f.x509Credential("c-1", "dev-1", ca, "maker", "SN-1")
	require.NoError(t, err)
	f.profile("pp-x", ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.TrustAnchorToken = strPtr("maker") })

	err = f.mgr.Database.Exec(`DELETE FROM "device-management".device_trust_anchors WHERE id = ?`, a.ID).Error
	require.Error(t, err, "the database let an anchor go while a credential and a profile rely on it")
	assert.Contains(t, err.Error(), "violates foreign key constraint")

	// Each reference holds it alone.
	_, err = f.api.DeleteDeviceCredential(f.ctx, "c-1")
	require.NoError(t, err)
	require.Error(t, f.mgr.Database.Exec(`DELETE FROM "device-management".device_trust_anchors WHERE id = ?`, a.ID).Error,
		"the profile alone must hold the anchor")
	_, err = f.api.DeleteProvisioningProfile(f.ctx, "pp-x")
	require.NoError(t, err)
	require.NoError(t, f.mgr.Database.Exec(`DELETE FROM "device-management".device_trust_anchors WHERE id = ?`, a.ID).Error)
}

// Disabling an anchor queues one kick per relying device under the anchor's own origin (a value
// far above any profile id, in a bigint), the queue upserts and clears with row-value predicates,
// and the debt survives until the responder confirms.
func TestDisablingAnAnchorQueuesAndClearsKicksOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	f.kicker.set(false)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	for i := 0; i < 3; i++ {
		dev := fmt.Sprintf("dev-%d", i)
		f.device(dev)
		_, err := f.x509Credential(fmt.Sprintf("c-%d", i), dev, ca, "maker", fmt.Sprintf("SN-%d", i))
		require.NoError(t, err)
	}
	// A profile's revoke owes a kick on the same device under a different origin.
	f.profile("pp-1", ProvisionAllowNew)
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		return f.api.queueSessionKicksOnTx(tx, f.tenant, 1, []string{"dev-0"})
	}))

	_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	rows := f.pendingKicks()
	require.Len(t, rows, 4, "three anchor debts and one profile debt")
	var anchorRows int
	for _, r := range rows {
		if r.Origin == anchorKickOrigin(a.ID) {
			anchorRows++
		}
	}
	assert.Equal(t, 3, anchorRows)

	// Repeating, still down: the same rows are refreshed, not duplicated.
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(false))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	assert.Len(t, f.pendingKicks(), 4)

	f.kicker.set(true)
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(false))
	require.NoError(t, err)
	rows = f.pendingKicks()
	// Ending a device's sessions satisfies every debt about them, so the profile's row for
	// dev-0 is settled by the same confirmation only if its scope selected it; this scope is the
	// anchor's origin, so it survives for the background pass.
	require.Len(t, rows, 1)
	assert.Equal(t, uint(1), rows[0].Origin)
	res, err := f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Tenants)
	assert.Empty(t, f.pendingKicks())
}

// Disable, delete and credential creation interleave without a deadlock the database has to
// break, and without a raw constraint error reaching the caller. The first case is FORCED: a
// disable is held between its row lock and its write while a credential create queues behind
// it on the anchor's foreign key.
func TestADisableHeldOpenDoesNotDeadlockACredentialCreateOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	f.device("dev-1")

	created := make(chan error, 1)
	holdBeforeUpdate(t, f, "device_trust_anchors", func(db *gorm.DB) {
		go func() {
			_, err := f.x509Credential("c-1", "dev-1", ca, "maker", "SN-1")
			created <- err
		}()
		// The create is provably blocked behind the disable's lock before the disable goes on.
		waitForBlockedBackends(t, db, 1)
	})
	_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(false))
	require.NoError(t, err)

	select {
	case err := <-created:
		require.NoError(t, err, "the create that queued behind the disable must complete")
	case <-time.After(30 * time.Second):
		t.Fatal("the credential create never finished: a deadlock the database did not break")
	}
	// The credential exists under the now-disabled anchor, where it can never authenticate.
	creds := f.count(&DeviceCredential{})
	assert.EqualValues(t, 1, creds)
	a, err := f.api.DeviceTrustAnchorsByToken(f.ctx, []string{"maker"})
	require.NoError(t, err)
	assert.False(t, a[0].Enabled)
}

func TestAnchorOperationsInterleaveWithoutDeadlockOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	ca := newRootCA(t, "Maker Root")
	f.anchor("maker", ca)
	const devices = 6
	for i := 0; i < devices; i++ {
		f.device(fmt.Sprintf("dev-%d", i))
	}
	stopFlap := make(chan struct{})
	go func() {
		for up := false; ; up = !up {
			select {
			case <-stopFlap:
				return
			case <-time.After(15 * time.Millisecond):
				f.kicker.set(up)
			}
		}
	}()
	defer close(stopFlap)

	var wg sync.WaitGroup
	var bad atomic.Int64
	var mu sync.Mutex
	var problems []string
	check := func(op string, err error) {
		if err == nil || errors.Is(err, ErrEntityInUse) || errors.Is(err, gorm.ErrRecordNotFound) ||
			errors.Is(err, ErrSessionsNotEnded) || conflict.Is(err) {
			return
		}
		if class, ok := integrity.Refused(err); ok && class == integrity.ClassInvalid {
			return // a disabled anchor, a device already holding three: refusals, not faults
		}
		bad.Add(1)
		mu.Lock()
		problems = append(problems, fmt.Sprintf("%s: %v", op, err))
		mu.Unlock()
	}
	deadline := time.Now().Add(6 * time.Second)
	worker := func(seed int) {
		defer wg.Done()
		for i := seed; time.Now().Before(deadline); i++ {
			switch i % 6 {
			case 0, 1:
				_, err := f.x509Credential(fmt.Sprintf("c-%d-%d", seed, i), fmt.Sprintf("dev-%d", i%devices), ca, "maker", fmt.Sprintf("SN-%d-%d", seed, i))
				check("create credential", err)
			case 2:
				_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(false))
				check("disable", err)
			case 3:
				_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", setEnabled(true))
				check("enable", err)
			case 4:
				_, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "maker")
				check("delete", err)
			case 5:
				_, err := f.api.DeleteDeviceCredential(f.ctx, fmt.Sprintf("c-%d-%d", seed, i-5))
				check("delete credential", err)
			}
		}
	}
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go worker(w)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(90 * time.Second):
		t.Fatal("the mixed operations never finished: a deadlock the database did not break")
	}
	assert.Zero(t, bad.Load(), "operations failed with a server error: %s", strings.Join(problems, "; "))
}

// A certificate profile registers through the same row locks the secret path uses: creating one
// while its anchor is deleted and re-created never leaves a profile pointing at nothing.
func TestCertificateProfileCreationRacesAnchorDeleteOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	f.anchor("maker", newRootCA(t, "Maker Root"))

	var wg sync.WaitGroup
	var profileErr, deleteErr error
	var deleted bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		f.profile2("pp-x", "maker", &profileErr)
	}()
	go func() {
		defer wg.Done()
		deleted, deleteErr = f.api.DeleteDeviceTrustAnchor(f.ctx, "maker")
	}()
	wg.Wait()

	switch {
	case profileErr == nil && deleted:
		t.Fatal("the anchor was deleted while a profile relies on it")
	case profileErr == nil:
		require.ErrorIs(t, deleteErr, ErrEntityInUse)
	default:
		// The delete won; the profile's create found no anchor (or lost the foreign key), and
		// either way left nothing behind.
		require.NoError(t, deleteErr)
		assert.EqualValues(t, 0, f.count(&ProvisioningProfile{}))
	}
}

func (f *pgFixture) profile2(token, anchor string, out *error) {
	_, *out = f.api.CreateProvisioningProfile(f.ctx, &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "key-" + token, Strategy: string(ProvisionAllowNew),
		DeviceTypeToken: "dt-a", Enabled: true, TrustAnchorToken: &anchor,
	})
}
