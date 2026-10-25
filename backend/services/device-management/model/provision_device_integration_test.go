// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Device self-registration against a REAL PostgreSQL: the migration, the locks, the
// unique-violation retry and the races.
//
// 🔴 WHY THESE CANNOT LIVE WITH provision_device_test.go. That file runs on in-memory SQLite,
// whose driver silently DROPS `FOR UPDATE` and has no foreign keys by default, so every property
// below is invisible there: with the locks deleted it stays green. The interleavings here are
// FORCED, not raced: a test callback holds one registration open after it has written its
// credential, and the test waits until another backend is provably blocked on a lock before it
// lets go. A race that merely usually happens is a test that usually passes.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'SelfRegistration|Provision' -v
package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var itTenantSeq atomic.Int64

// pgFixture is one fresh tenant on the shared server. Every test makes its own, so tests that
// run in one process never see each other's rows.
type pgFixture struct {
	t       *testing.T
	mgr     *rdb.RdbManager
	api     *Api
	ctx     context.Context
	tenant  string
	secrets map[string]string
	keys    map[string]string
}

func newPgFixture(t *testing.T) *pgFixture {
	t.Helper()
	mgr := newPostgresRdbManager(t)
	api := NewApi(mgr)
	api.DeviceSecretKey = testSecretKey
	api.EnrollmentKeys = testEnrollmentKeys
	api.ProvisioningChecker, _ = provisioningChecker(t, testSecretKey, testEnrollmentKeys.Provisioning)
	tenant := fmt.Sprintf("it%d-%d", time.Now().UnixNano(), itTenantSeq.Add(1))
	f := &pgFixture{t: t, mgr: mgr, api: api, ctx: core.WithTenant(context.Background(), tenant), tenant: tenant,
		secrets: map[string]string{}, keys: map[string]string{}}
	for _, tok := range []string{"dt-a", "dt-b"} {
		_, err := api.CreateDeviceType(f.ctx, &DeviceTypeCreateRequest{Token: tok})
		require.NoError(t, err)
	}
	return f
}

func (f *pgFixture) profile(token string, strategy ProvisioningStrategy, mutate ...func(*ProvisioningProfileCreateRequest)) {
	f.t.Helper()
	req := &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "key-" + token, Strategy: string(strategy), DeviceTypeToken: "dt-a", Enabled: true,
	}
	for _, m := range mutate {
		m(req)
	}
	created, err := f.api.CreateProvisioningProfile(f.ctx, req)
	require.NoError(f.t, err)
	f.secrets[token], f.keys[token] = created.IssuedSecret, req.ProvisionKey
}

func (f *pgFixture) register(profile, external, mat string) (*ProvisionResult, error) {
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys[profile], f.secrets[profile])
	if err != nil {
		return nil, err
	}
	return f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: external, CredentialSecret: mat})
}

func (f *pgFixture) count(model any) int64 {
	f.t.Helper()
	var n int64
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(model).Count(&n).Error)
	return n
}

// holdRegistration installs a callback that PAUSES the registration of externalId right after
// its credential row is written (so it holds the profile and device locks and has uncommitted
// rows), signalling `reached` and waiting for `release`. It pauses only the first such write.
func (f *pgFixture) holdRegistration(externalId string) (reached <-chan struct{}, release func()) {
	f.t.Helper()
	reachedCh, releaseCh := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	name := fmt.Sprintf("test:hold-registration-%s-%d", externalId, time.Now().UnixNano())
	wantToken := core.DeriveDeviceToken(externalId, provisionedCredentialTokenPrefix)
	require.NoError(f.t, f.mgr.Database.Callback().Create().After("gorm:create").Register(name, func(db *gorm.DB) {
		cred, ok := db.Statement.Dest.(*DeviceCredential)
		if !ok || db.Error != nil || cred.TenantId != f.tenant || cred.Token != wantToken {
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

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// notDone asserts that done has not fired within a grace period: the call is still blocked.
func notDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("%s finished while the registration still held its locks; it does not wait for them", what)
	case <-time.After(750 * time.Millisecond):
	}
}

// ─── The migration ─────────────────────────────────────────────────────────

func TestSelfRegistrationSchemaOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	sys := f.mgr.Database.WithContext(core.WithSystemContext(context.Background()))

	t.Run("the key is unique per tenant and the global constraint is gone", func(t *testing.T) {
		var constraints int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_constraint WHERE conname = 'uni_device-management_provisioning_profiles_provision_key'`).
			Scan(&constraints).Error)
		assert.Zero(t, constraints, "the global provision key constraint is still there")

		var def string
		require.NoError(t, sys.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = 'device-management'
			AND indexname = 'uix_provisioning_profiles_tenant_provision_key'`).Scan(&def).Error)
		assert.Contains(t, def, "UNIQUE")
		assert.Contains(t, def, "(tenant_id, provision_key)")
		assert.Contains(t, def, "deleted_at IS NULL")

		// Two tenants with the same key both succeed: one tenant can no longer learn that
		// another holds a key.
		other := core.WithTenant(context.Background(), f.tenant+"-b")
		_, err := f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt-a"})
		require.NoError(t, err)
		mk := func(ctx context.Context, token string) error {
			_, err := f.api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
				Token: token, ProvisionKey: "shared-key", Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true})
			return err
		}
		require.NoError(t, mk(f.ctx, "pp-1"))
		require.NoError(t, mk(other, "pp-1"), "a second tenant was refused a key another tenant holds")
		// A same-tenant duplicate is refused, as a conflict, naming the index.
		err = mk(f.ctx, "pp-2")
		require.Error(t, err)
		ce, ok := conflict.As(err)
		require.True(t, ok, "a duplicate key was refused with %v, not a conflict", err)
		assert.Equal(t, "uix_provisioning_profiles_tenant_provision_key", ce.Constraint)
	})

	t.Run("the new columns and their foreign keys", func(t *testing.T) {
		type col struct {
			Table, Column, Nullable, Default string
		}
		var cols []col
		require.NoError(t, sys.Raw(`SELECT table_name AS "table", column_name AS "column", is_nullable AS nullable,
			coalesce(column_default, '') AS "default" FROM information_schema.columns
			WHERE table_schema = 'device-management' AND column_name IN
			('max_devices', 'provision_secret_issued', 'expected_provisioning_profile_id', 'provisioning_profile_id')
			ORDER BY column_name`).Scan(&cols).Error)
		got := map[string]col{}
		for _, c := range cols {
			got[c.Column] = c
		}
		require.Len(t, got, 4)
		assert.Equal(t, "YES", got["max_devices"].Nullable)
		assert.Equal(t, "NO", got["provision_secret_issued"].Nullable)
		assert.Equal(t, "false", got["provision_secret_issued"].Default)
		assert.Equal(t, "YES", got["expected_provisioning_profile_id"].Nullable)
		assert.Equal(t, "YES", got["provisioning_profile_id"].Nullable)

		var rules []struct{ Name, OnDelete string }
		require.NoError(t, sys.Raw(`SELECT conname AS name, confdeltype::text AS on_delete FROM pg_constraint
			WHERE conname IN ('fk_devices_expected_provisioning_profile', 'fk_device_credentials_provisioning_profile')
			ORDER BY conname`).Scan(&rules).Error)
		require.Len(t, rules, 2)
		assert.Equal(t, "fk_device_credentials_provisioning_profile", rules[0].Name)
		assert.Equal(t, "r", rules[0].OnDelete, "the credential's provenance must RESTRICT")
		assert.Equal(t, "fk_devices_expected_provisioning_profile", rules[1].Name)
		assert.Equal(t, "n", rules[1].OnDelete, "the earmark must SET NULL")

		var indexes int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'device-management' AND indexname IN
			('idx_devices_expected_provisioning_profile', 'idx_device_credentials_provisioning_profile')`).Scan(&indexes).Error)
		assert.EqualValues(t, 2, indexes)
	})

	t.Run("every statement is re-runnable", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			err := f.mgr.Database.Transaction(func(tx *gorm.DB) error {
				return schema.NewSelfRegistrationSchema().Migrate(tx.WithContext(core.WithSystemContext(context.Background())))
			})
			require.NoError(t, err, "replay %d", i+1)
		}
	})

	t.Run("the foreign keys do what the model says", func(t *testing.T) {
		f.profile("pp-fk", ProvisionAllowNew)
		made, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "d-early", DeviceTypeToken: "dt-a",
			ProvisioningProfileToken: strPtr("pp-fk")})
		require.NoError(t, err)
		require.NotNil(t, made.ExpectedProvisioningProfileId)
		_, err = f.register("pp-fk", "SN-FK", material(1))
		require.NoError(t, err)

		// The database refuses to delete a profile that issued a credential, with a 23503 the
		// model answers as IN_USE, never as a server error.
		profileId := func() uint {
			rows, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-fk"})
			require.NoError(t, err)
			return rows[0].ID
		}()
		raw := f.api.RDB.DB(f.ctx).Unscoped().Where("id = ?", profileId).Delete(&ProvisioningProfile{}).Error
		require.Error(t, raw)
		assert.NotErrorIs(t, raw, ErrEntityInUse)
		mapped := inUseOn(raw, "pp-fk is referenced")
		assert.ErrorIs(t, mapped, ErrEntityInUse, "a foreign-key violation on a delete was not mapped to IN_USE: %v", raw)

		// After the credential is reset the delete goes through, and the early-earmarked device
		// falls back to no earmark instead of blocking it.
		_, err = f.api.ResetDeviceProvisioning(f.ctx, core.DeriveDeviceToken("SN-FK", "pv-"))
		require.NoError(t, err)
		deleted, err := f.api.DeleteProvisioningProfile(f.ctx, "pp-fk")
		require.NoError(t, err)
		require.True(t, deleted)
		rows, err := f.api.DevicesByToken(f.ctx, []string{"d-early", core.DeriveDeviceToken("SN-FK", "pv-")})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		for _, d := range rows {
			assert.Nil(t, d.ExpectedProvisioningProfileId, "%s kept an earmark for a deleted profile", d.Token)
		}
	})
}

// ─── Concurrency ───────────────────────────────────────────────────────────

type regResult struct {
	res *ProvisionResult
	err error
}

func runParallel(n int, fn func(i int) regResult) []regResult {
	out := make([]regResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

func noTransactionAborted(t *testing.T, results []regResult) {
	t.Helper()
	for _, r := range results {
		if r.err == nil {
			continue
		}
		msg := r.err.Error()
		assert.NotContains(t, msg, "current transaction is aborted", "a unique violation was resolved inside the transaction it aborted")
		assert.NotContains(t, msg, "25P02")
		assert.NotContains(t, msg, "deadlock", "registrations deadlocked")
		assert.NotContains(t, msg, "40P01")
	}
}

func tallyRegistrations(results []regResult) (byOutcome map[ProvisionOutcome]int, byCode map[string]int, other []error) {
	byOutcome, byCode = map[ProvisionOutcome]int{}, map[string]int{}
	for _, r := range results {
		var refusal *ProvisioningRefusal
		switch {
		case r.err == nil:
			byOutcome[r.res.Outcome]++
		case errors.As(r.err, &refusal):
			byCode[refusal.Code]++
		default:
			other = append(other, r.err)
		}
	}
	return
}

func TestProvisionTwentyParallelRegistrationsOfOneDeviceAdmitOne(t *testing.T) {
	t.Run("same material: one create and nineteen replays", func(t *testing.T) {
		f := newPgFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		results := runParallel(20, func(int) regResult {
			res, err := f.register("pp-1", "SN-1", material(1))
			return regResult{res, err}
		})
		noTransactionAborted(t, results)
		byOutcome, byCode, other := tallyRegistrations(results)
		assert.Empty(t, other)
		assert.Empty(t, byCode)
		assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 1, ProvisionReplayed: 19}, byOutcome)
		assert.EqualValues(t, 1, f.count(&Device{}))
		assert.EqualValues(t, 1, f.count(&DeviceCredential{}))
	})
	t.Run("different material: one winner and nineteen refusals", func(t *testing.T) {
		f := newPgFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		results := runParallel(20, func(i int) regResult {
			res, err := f.register("pp-1", "SN-1", material(i))
			return regResult{res, err}
		})
		noTransactionAborted(t, results)
		byOutcome, byCode, other := tallyRegistrations(results)
		assert.Empty(t, other)
		assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 1}, byOutcome)
		assert.Equal(t, map[string]int{ProvisioningCodeAlreadyProvisioned: 19}, byCode)
		assert.EqualValues(t, 1, f.count(&DeviceCredential{}), "more than one credential was issued to one device")
	})
}

func TestProvisionMaxDevicesIsExactUnderConcurrency(t *testing.T) {
	f := newPgFixture(t)
	five := int32(5)
	f.profile("pp-1", ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = &five })
	results := runParallel(20, func(i int) regResult {
		res, err := f.register("pp-1", fmt.Sprintf("SN-%d", i), material(i))
		return regResult{res, err}
	})
	noTransactionAborted(t, results)
	byOutcome, byCode, other := tallyRegistrations(results)
	assert.Empty(t, other)
	assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 5}, byOutcome, "the cap was not exact")
	assert.Equal(t, map[string]int{ProvisioningCodeLimitExceeded: 15}, byCode)
	assert.EqualValues(t, 5, f.count(&Device{}))
	assert.EqualValues(t, 5, f.count(&DeviceCredential{}))

	// An ADOPT at the cap is refused with the same 422.
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "d-adopt", ExternalId: strPtr("SN-ADOPT"),
		DeviceTypeToken: "dt-a", ProvisioningProfileToken: strPtr("pp-1")})
	require.NoError(t, err)
	_, err = f.register("pp-1", "SN-ADOPT", material(99))
	require.ErrorIs(t, err, ErrProvisioningLimitExceeded)
	assert.EqualValues(t, 5, f.count(&DeviceCredential{}))
}

func TestProvisionADerivedTokenCollisionOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: core.DeriveDeviceToken("SN-1", "pv-"), DeviceTypeToken: "dt-a"})
	require.NoError(t, err)
	_, err = f.register("pp-1", "SN-1", material(1))
	require.ErrorIs(t, err, ErrProvisioningTokenCollision)
	assert.NotContains(t, err.Error(), "aborted")
	assert.EqualValues(t, 1, f.count(&Device{}))
	assert.Zero(t, f.count(&DeviceCredential{}))
}

// Two profiles race for one external id. The loser's insert blocks on the winner's uncommitted
// row and then fails with a unique violation, which Postgres has already aborted its
// transaction over. It is retried in a FRESH transaction, where it re-reads the winner and is
// refused as a device that is not its own: the one answer that can only come from a re-read.
func TestProvisionAUniqueViolationIsRetriedInAFreshTransaction(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-a", ProvisionAllowNew)
	f.profile("pp-b", ProvisionAllowNew)

	reached, release := f.holdRegistration("SN-RACE")
	type out struct {
		res *ProvisionResult
		err error
	}
	a, b := make(chan out, 1), make(chan out, 1)
	go func() { r, e := f.register("pp-a", "SN-RACE", material(1)); a <- out{r, e} }()
	awaitSignal(t, reached, "profile A to write its device and credential")

	go func() { r, e := f.register("pp-b", "SN-RACE", material(2)); b <- out{r, e} }()
	waitForBlockedBackend(t, f.mgr.Database)
	select {
	case got := <-b:
		t.Fatalf("B finished (%v, %v) while A's device was uncommitted", got.res, got.err)
	default:
	}
	release()

	ra, rb := <-a, <-b
	require.NoError(t, ra.err)
	assert.Equal(t, ProvisionCreated, ra.res.Outcome)
	require.Error(t, rb.err)
	assert.NotContains(t, rb.err.Error(), "current transaction is aborted", "the violation was resolved inside the aborted transaction")
	assert.ErrorIs(t, rb.err, ErrProvisioningNotEarmarked, "B's retry must have re-read A's device: %v", rb.err)
	assert.EqualValues(t, 1, f.count(&Device{}))
	assert.EqualValues(t, 1, f.count(&DeviceCredential{}))
}

// ─── Lock order ────────────────────────────────────────────────────────────

// An in-flight registration has written its credential and not committed. A revoke of the same
// profile must WAIT for it (the profile row lock), and once it has, it must see and disable the
// credential. A revoke that did not lock the profile would return at once having disabled
// nothing, and the operator would be told the fleet was revoked while a credential issued a
// moment earlier stayed live.
func TestProvisionRevokeWaitsForARegistrationInFlight(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	reached, release := f.holdRegistration("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.register("pp-1", "SN-1", material(1)); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	revoked := make(chan struct{})
	var n int
	var revokeErr error
	go func() { n, revokeErr = f.api.RevokeProvisionedDevices(f.ctx, "pp-1"); close(revoked) }()
	waitForBlockedBackend(t, f.mgr.Database)
	notDone(t, revoked, "revokeProvisionedDevices")

	release()
	awaitSignal(t, revoked, "the revoke")
	require.NoError(t, revokeErr)
	reg := <-regDone
	require.NoError(t, reg.err)
	assert.Equal(t, ProvisionCreated, reg.res.Outcome)
	assert.Equal(t, 1, n, "the revoke did not see the credential the registration committed first")

	var enabled int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("enabled = ?", true).Count(&enabled).Error)
	assert.Zero(t, enabled, "a credential issued before the revoke stayed live")
}

// Deleting a profile counts its credentials under the profile lock: it waits for a registration
// in flight and then finds the credential it issued.
func TestProvisionDeleteProfileWaitsForARegistrationInFlight(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	reached, release := f.holdRegistration("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.register("pp-1", "SN-1", material(1)); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	deleted := make(chan struct{})
	var ok bool
	var delErr error
	go func() { ok, delErr = f.api.DeleteProvisioningProfile(f.ctx, "pp-1"); close(deleted) }()
	waitForBlockedBackend(t, f.mgr.Database)
	notDone(t, deleted, "deleteProvisioningProfile")

	release()
	awaitSignal(t, deleted, "the delete")
	require.NoError(t, (<-regDone).err)
	assert.False(t, ok)
	require.ErrorIs(t, delErr, ErrEntityInUse, "the delete did not see the credential the registration issued")
	// The readable refusal, not the foreign key's backstop: the count ran after the wait.
	assert.Contains(t, delErr.Error(), "credential(s) were issued by provisioning profile", "the delete fell through to the foreign key")
	rows, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-1"})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

// A reset of a device whose ADOPT is in flight waits for the device lock, finds the credential
// the adopt issued once it has, and removes it: the locked set it learned from its first,
// unlocked read (no profile) is not the set that applies, and it takes the profile first on its
// second pass rather than the device first.
func TestProvisionResetWaitsForAnAdoptInFlightAndThenRemovesItsCredential(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "dev-1", ExternalId: strPtr("SN-1"),
		DeviceTypeToken: "dt-a", ProvisioningProfileToken: strPtr("pp-1")})
	require.NoError(t, err)
	reached, release := f.holdRegistration("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.register("pp-1", "SN-1", material(1)); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the adopt to write its credential")

	reset := make(chan struct{})
	var removed int
	var resetErr error
	go func() { removed, resetErr = f.api.ResetDeviceProvisioning(f.ctx, "dev-1"); close(reset) }()
	waitForBlockedBackend(t, f.mgr.Database)
	notDone(t, reset, "resetDeviceProvisioning")

	release()
	awaitSignal(t, reset, "the reset")
	reg := <-regDone
	require.NoError(t, reg.err)
	assert.Equal(t, ProvisionAdopted, reg.res.Outcome)
	require.NoError(t, resetErr)
	assert.Equal(t, 1, removed, "the reset missed the credential the adopt committed first")
	assert.Zero(t, f.count(&DeviceCredential{}))
}

// The earmarked create takes the profile's share lock BEFORE inserting the device. Without it,
// a create holding an uncommitted device with an external id that a registration is about to
// insert waits (on the foreign key) for the profile lock the registration holds, and the
// registration waits for the create's unique-index entry: a deadlock Postgres breaks by killing
// one of them. Run enough rounds that the unlocked order would be hit.
func TestProvisionAnEarmarkedCreateAndARegistrationOfTheSameDeviceNeverDeadlock(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	const rounds = 40
	for i := 0; i < rounds; i++ {
		ext := fmt.Sprintf("SN-%d", i)
		var createErr, regErr error
		var reg *ProvisionResult
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, createErr = f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: fmt.Sprintf("d-%d", i), ExternalId: &ext,
				DeviceTypeToken: "dt-a", ProvisioningProfileToken: strPtr("pp-1")})
		}()
		go func() {
			defer wg.Done()
			<-start
			reg, regErr = f.register("pp-1", ext, material(i))
		}()
		close(start)
		wg.Wait()

		for _, err := range []error{createErr, regErr} {
			if err != nil {
				assert.NotContains(t, err.Error(), "deadlock", "round %d: %v", i, err)
				assert.NotContains(t, err.Error(), "40P01", "round %d", i)
			}
		}
		switch {
		case createErr == nil:
			require.NoError(t, regErr, "round %d: the create won, so the registration adopts", i)
			assert.Equal(t, ProvisionAdopted, reg.Outcome, "round %d", i)
		default:
			require.True(t, conflict.Is(createErr), "round %d: the create lost with %v", i, createErr)
			require.NoError(t, regErr, "round %d", i)
			assert.Equal(t, ProvisionCreated, reg.Outcome, "round %d", i)
		}
	}
}

// Every pair of paths that touches a profile and a device, run together over and over: none may
// deadlock, none may fail with a server error.
func TestProvisionMixedOperationsOnOneProfileNeverDeadlock(t *testing.T) {
	f := newPgFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	const devices = 12
	for i := 0; i < devices; i++ {
		ext := fmt.Sprintf("SN-%d", i)
		if i%2 == 0 {
			_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: fmt.Sprintf("d-%d", i), ExternalId: &ext,
				DeviceTypeToken: "dt-a", ProvisioningProfileToken: strPtr("pp-1")})
			require.NoError(t, err)
		}
	}
	// A device nothing below resets keeps one credential on the profile for good, so the delete
	// attempts are refused (IN_USE) instead of removing the profile out from under the test.
	_, err := f.register("pp-1", "SN-KEEPER", material(1000))
	require.NoError(t, err)
	var wg sync.WaitGroup
	var bad atomic.Int64
	var mu sync.Mutex
	var problems []string
	check := func(op string, err error) {
		if err == nil {
			return
		}
		var refusal *ProvisioningRefusal
		if errors.As(err, &refusal) || errors.Is(err, ErrEntityInUse) || errors.Is(err, gorm.ErrRecordNotFound) {
			return
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
			ext := fmt.Sprintf("SN-%d", i%devices)
			switch i % 5 {
			case 0, 1:
				_, err := f.register("pp-1", ext, material(i%devices))
				check("register", err)
			case 2:
				_, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
				check("revoke", err)
			case 3:
				tok := core.DeriveDeviceToken(ext, "pv-")
				if i%2 == 0 {
					tok = fmt.Sprintf("d-%d", i%devices)
				}
				_, err := f.api.ResetDeviceProvisioning(f.ctx, tok)
				check("reset", err)
			case 4:
				_, err := f.api.DeleteProvisioningProfile(f.ctx, "pp-1")
				check("delete", err)
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
