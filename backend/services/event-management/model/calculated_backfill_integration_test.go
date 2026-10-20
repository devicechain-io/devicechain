// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// newBackfillManager is newPostgresManager with an empty job table: the instance database is
// named by the test, so a server that has run it before still holds its rows.
func newBackfillManager(t *testing.T, instance string) *rdb.RdbManager {
	t.Helper()
	mgr := newPostgresManager(t, instance)
	require.NoError(t, mgr.Database.WithContext(core.WithSystemContext(context.Background())).
		Exec(`TRUNCATE TABLE "event-management".calculated_backfills`).Error)
	return mgr
}

func newJobFor(tenant, token string) *CalculatedBackfill {
	now := time.Now().UTC()
	j := &CalculatedBackfill{Token: token, ProfileToken: "meter", ProfileVersionToken: "meter@1", ProfileVersion: 1,
		MetricKeys: json.RawMessage(`["power"]`), Plan: json.RawMessage(`{}`),
		RangeFrom: now.Add(-time.Hour), RangeTo: now}
	j.TenantId = tenant
	return j
}

// The migration is individually re-runnable, builds exactly the indexes it names, and leaves the
// table without a row.
func TestIntegrationBackfillMigrationIsRerunnable(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillmig")
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	m := NewCalculatedBackfillSchema()
	require.NoError(t, m.Migrate(sys), "a second pass over the finished schema")

	// A half-applied run: an index missing, as after a failure between statements.
	require.NoError(t, sys.Exec(`DROP INDEX "event-management".idx_calculated_backfills_status_updated`).Error)
	require.NoError(t, m.Migrate(sys), "replayed from the top over the half-built schema")

	var names []string
	require.NoError(t, sys.Raw(`SELECT indexname FROM pg_indexes WHERE schemaname = 'event-management' `+
		`AND tablename = 'calculated_backfills' ORDER BY indexname`).Scan(&names).Error)
	assert.Equal(t, []string{
		"calculated_backfills_pkey",
		"idx_calculated_backfills_status_updated",
		"idx_calculated_backfills_tenant_profile",
		"uix_calculated_backfills_tenant_active",
		"uix_calculated_backfills_tenant_token",
	}, names)

	var n int64
	require.NoError(t, sys.Model(&CalculatedBackfill{}).Count(&n).Error)
	assert.Zero(t, n, "the migration seeds nothing")

	var hyper int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.hypertables `+
		`WHERE hypertable_name = 'calculated_backfills'`).Scan(&hyper).Error)
	assert.Zero(t, hyper, "a plain table")
}

// The live model reads and writes only columns the migration built: a field added to the struct
// without a migration fails here, not on the first insert in production.
func TestIntegrationBackfillModelMatchesTheMigratedTable(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillcols")
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	var cols []string
	require.NoError(t, sys.Raw(`SELECT column_name FROM information_schema.columns `+
		`WHERE table_schema = 'event-management' AND table_name = 'calculated_backfills'`).Scan(&cols).Error)
	s, err := schema.Parse(&CalculatedBackfill{}, &sync.Map{}, mgr.Database.NamingStrategy)
	require.NoError(t, err)
	have := map[string]bool{}
	for _, c := range cols {
		have[c] = true
	}
	for _, f := range s.Fields {
		if f.DBName != "" {
			assert.True(t, have[f.DBName], "the model names column %q, which the migration does not build", f.DBName)
			delete(have, f.DBName)
		}
	}
	assert.Empty(t, have, "the migration builds columns the model does not read")
}

// One active job per tenant is the index's doing, a terminal job frees the slot in the same
// UPDATE, and tenants do not share a slot.
func TestIntegrationOneActiveBackfillPerTenant(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillone")
	ctx := core.WithTenant(context.Background(), "acme")
	db := mgr.Database.WithContext(ctx)

	require.NoError(t, CreateBackfill(ctx, db, newJobFor("acme", "first")))
	err := CreateBackfill(ctx, db, newJobFor("acme", "second"))
	var active *ErrBackfillActive
	require.True(t, errors.As(err, &active), "got %v", err)
	assert.Equal(t, "first", active.ActiveToken, "the conflict names the job holding the slot")

	other := core.WithTenant(context.Background(), "globex")
	require.NoError(t, CreateBackfill(other, mgr.Database.WithContext(other), newJobFor("globex", "first")),
		"another tenant has a slot of its own")

	// Running is still active; finished frees the slot.
	job, err := ClaimBackfill(db, "first")
	require.NoError(t, err)
	require.Error(t, CreateBackfill(ctx, db, newJobFor("acme", "third")))
	require.NoError(t, FinishBackfill(db, "first", job.ClaimEpoch, BackfillSucceeded, ""))
	require.NoError(t, CreateBackfill(ctx, db, newJobFor("acme", "third")))
}

func TestIntegrationBackfillStatusIsConstrained(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillchk")
	ctx := core.WithTenant(context.Background(), "acme")
	db := mgr.Database.WithContext(ctx)
	j := newJobFor("acme", "bad")
	require.NoError(t, CreateBackfill(ctx, db, j))
	err := db.Model(&CalculatedBackfill{}).Where("token = ?", "bad").Update("status", "PAUSED").Error
	assert.Error(t, err, "a status outside the five is refused by the database")
	err = db.Model(&CalculatedBackfill{}).Where("token = ?", "bad").Update("phase", "WARMUP").Error
	assert.Error(t, err)
}

// The guarded transitions: a claim bumps the epoch, and a write carrying any other epoch, or
// arriving after a cancel was requested, or after the job ended, matches nothing.
func TestIntegrationBackfillTransitionsAreGuarded(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfilltx")
	ctx := core.WithTenant(context.Background(), "acme")
	db := mgr.Database.WithContext(ctx)
	require.NoError(t, CreateBackfill(ctx, db, newJobFor("acme", "j")))

	// Nothing but a RUNNING job accepts progress.
	assert.ErrorIs(t, CommitBackfillProgress(db, "j", 1, BackfillDelta{Samples: 1}), ErrBackfillLost, "still QUEUED")

	job, err := ClaimBackfill(db, "j")
	require.NoError(t, err)
	assert.EqualValues(t, 1, job.ClaimEpoch)
	assert.Equal(t, BackfillRunning, job.Status)
	require.NotNil(t, job.StartedAt)
	started := *job.StartedAt

	next := int64(42)
	window := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	wp := &window
	require.NoError(t, CommitBackfillProgress(db, "j", 1, BackfillDelta{
		DevicesDone: 2, Samples: 10, Computed: 6, Written: 4, AlreadyPresent: 2, WindowsCompressed: 1,
		Skipped: map[string]int64{"missing_input": 4}, RosterAfterId: &next, CursorWindow: &wp}))
	require.NoError(t, CommitBackfillProgress(db, "j", 1, BackfillDelta{Samples: 5, Written: 1, Computed: 1}))
	var got CalculatedBackfill
	require.NoError(t, db.Where("token = ?", "j").Take(&got).Error)
	assert.EqualValues(t, 15, got.Samples, "counters add")
	assert.EqualValues(t, 5, got.Written)
	assert.EqualValues(t, 42, got.RosterAfterId)
	require.NotNil(t, got.CursorWindow)
	assert.True(t, got.CursorWindow.Equal(window))
	skips, err := got.SkippedCounts()
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"missing_input": 4}, skips, "a commit that names no skips leaves the map")

	// A reset is a nil window, not an absent one.
	var none *time.Time
	require.NoError(t, CommitBackfillProgress(db, "j", 1, BackfillDelta{CursorWindow: &none}))
	got = CalculatedBackfill{} // a scan into a set pointer would leave it set
	require.NoError(t, db.Where("token = ?", "j").Take(&got).Error)
	assert.Nil(t, got.CursorWindow)

	// A stale epoch writes nothing.
	assert.ErrorIs(t, CommitBackfillProgress(db, "j", 0, BackfillDelta{Samples: 1}), ErrBackfillLost)
	assert.ErrorIs(t, CommitBackfillProgress(db, "j", 2, BackfillDelta{Samples: 1}), ErrBackfillLost)
	assert.ErrorIs(t, FinishBackfill(db, "j", 2, BackfillSucceeded, ""), ErrBackfillLost)

	// Claiming again (a resume) bumps the epoch and keeps the first start.
	again, err := ClaimBackfill(db, "j")
	require.NoError(t, err)
	assert.EqualValues(t, 2, again.ClaimEpoch)
	assert.True(t, again.StartedAt.Equal(started))
	assert.ErrorIs(t, CommitBackfillProgress(db, "j", 1, BackfillDelta{Samples: 1}), ErrBackfillLost, "the first claim is stale")

	// A cancel request stops progress, and only a cancel request lets the job end CANCELLED.
	assert.ErrorIs(t, FinishBackfill(db, "j", 2, BackfillCancelled, ""), ErrBackfillLost, "nobody asked")
	cancelled, err := CancelBackfill(db, "j")
	require.NoError(t, err)
	assert.Equal(t, BackfillRunning, cancelled.Status, "a running job is asked, not stopped")
	require.NotNil(t, cancelled.CancelRequestedAt)
	assert.ErrorIs(t, CommitBackfillProgress(db, "j", 2, BackfillDelta{Samples: 1}), ErrBackfillLost)
	require.NoError(t, FinishBackfill(db, "j", 2, BackfillCancelled, ""))

	// Ended is ended.
	_, err = ClaimBackfill(db, "j")
	assert.ErrorIs(t, err, ErrBackfillLost)
	_, err = CancelBackfill(db, "j")
	assert.ErrorIs(t, err, ErrBackfillLost)
	require.NoError(t, db.Where("token = ?", "j").Take(&got).Error)
	assert.Equal(t, BackfillCancelled, got.Status)
	require.NotNil(t, got.FinishedAt)
}

func TestIntegrationBackfillFailureIsRecorded(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillfail")
	ctx := core.WithTenant(context.Background(), "acme")
	db := mgr.Database.WithContext(ctx)
	require.NoError(t, CreateBackfill(ctx, db, newJobFor("acme", "j")))
	job, err := ClaimBackfill(db, "j")
	require.NoError(t, err)
	require.NoError(t, FinishBackfill(db, "j", job.ClaimEpoch, BackfillFailed, "the roster is gone"))
	var got CalculatedBackfill
	require.NoError(t, db.Where("token = ?", "j").Take(&got).Error)
	require.NotNil(t, got.Failure)
	assert.Equal(t, "the roster is gone", *got.Failure)
}

// Jobs are tenant data: one tenant cannot claim, cancel or read another's, and the runnable scan
// is the one read that crosses tenants and needs the system context.
func TestIntegrationBackfillJobsAreTenantScoped(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillscope")
	a := core.WithTenant(context.Background(), "acme")
	g := core.WithTenant(context.Background(), "globex")
	require.NoError(t, CreateBackfill(a, mgr.Database.WithContext(a), newJobFor("acme", "j")))
	require.NoError(t, CreateBackfill(g, mgr.Database.WithContext(g), newJobFor("globex", "j")))

	_, err := ClaimBackfill(mgr.Database.WithContext(a), "j")
	require.NoError(t, err)
	var other CalculatedBackfill
	require.NoError(t, mgr.Database.WithContext(g).Where("token = ?", "j").Take(&other).Error)
	assert.Equal(t, BackfillQueued, other.Status, "the other tenant's job of the same token is untouched")

	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	jobs, err := RunnableBackfills(sys, 10)
	require.NoError(t, err)
	assert.Len(t, jobs, 2)
	age, err := OldestActiveBackfillAge(sys, time.Now())
	require.NoError(t, err)
	assert.Positive(t, int64(age))

	_, err = RunnableBackfills(mgr.Database.WithContext(context.Background()), 10)
	assert.Error(t, err, "no tenant and no system context is refused")
}

// A tenant purge classifies the job table as swept and leaves nothing of the tenant behind.
func TestIntegrationBackfillTableIsPurged(t *testing.T) {
	mgr := newBackfillManager(t, "itbackfillpurge")
	a := core.WithTenant(context.Background(), "acme")
	g := core.WithTenant(context.Background(), "globex")
	require.NoError(t, CreateBackfill(a, mgr.Database.WithContext(a), newJobFor("acme", "j")))
	require.NoError(t, CreateBackfill(g, mgr.Database.WithContext(g), newJobFor("globex", "j")))

	ctx := core.WithSystemContext(context.Background())
	plan, err := tenantpurge.Classify(ctx, mgr.Database)
	require.NoError(t, err)
	var found bool
	for _, e := range plan.Entries {
		if e.Table.Schema == "event-management" && e.Table.Name == "calculated_backfills" {
			found = true
			assert.Equal(t, tenantpurge.ClassDirect, e.Class, "swept by its tenant column")
		}
	}
	require.True(t, found, "the plan names the job table")

	epoch := time.Now().UTC()
	res, err := tenantpurge.Sweep(ctx, mgr.Database, plan, "acme", epoch, nil)
	require.NoError(t, err)
	assert.NotZero(t, res.Rows)
	resid, err := tenantpurge.Residue(ctx, mgr.Database, plan, "acme", epoch)
	require.NoError(t, err)
	assert.Zero(t, resid.Rows, "nothing of the tenant remains")

	var left int64
	require.NoError(t, mgr.Database.WithContext(core.WithSystemContext(context.Background())).
		Model(&CalculatedBackfill{}).Where("tenant_id = ?", "globex").Count(&left).Error)
	assert.EqualValues(t, 1, left, "the other tenant's job is not touched")
}
