// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFailedEventsManager(t *testing.T, instance string) *rdb.RdbManager {
	t.Helper()
	mgr := newPostgresManager(t, instance)
	require.NoError(t, mgr.Database.WithContext(core.WithSystemContext(context.Background())).
		Exec(`TRUNCATE TABLE "event-management".failed_event_records`).Error)
	return mgr
}

// The migration builds a HYPERTABLE (the catalog says so, which a pg_dump cannot), is
// individually re-runnable, and a half-applied run replays from the top.
func TestIntegrationFailedEventsMigrationIsAHypertableAndRerunnable(t *testing.T) {
	mgr := newFailedEventsManager(t, "itfailedmig")
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	m := NewFailedEventRecordsSchema()
	require.NoError(t, m.Migrate(sys), "a second pass over the finished schema")

	// A half-applied run: an index missing, as after a failure between statements.
	require.NoError(t, sys.Exec(`DROP INDEX "event-management".idx_failed_event_records_device`).Error)
	require.NoError(t, m.Migrate(sys), "replayed from the top over the half-built schema")

	var hyper int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.hypertables `+
		`WHERE hypertable_schema = 'event-management' AND hypertable_name = 'failed_event_records'`).Scan(&hyper).Error)
	assert.EqualValues(t, 1, hyper, "failed_event_records is a hypertable, partitioned on occurred_time")

	var names []string
	require.NoError(t, sys.Raw(`SELECT indexname FROM pg_indexes WHERE schemaname = 'event-management' `+
		`AND tablename = 'failed_event_records' ORDER BY indexname`).Scan(&names).Error)
	assert.Contains(t, names, "uq_failed_event_records_seq")
	assert.Contains(t, names, "idx_failed_event_records_tenant_time")
	assert.Contains(t, names, "idx_failed_event_records_device")
	assert.NotContains(t, names, "idx_failed_event_records_device1", "a replay never leaves a duplicate index")
}

// The data-lifecycle reconciler governs the table on a 7-day window of its own, with the uniform
// window OFF, and the policy really drops what is older.
func TestIntegrationFailedEventsRetentionDropsOldRows(t *testing.T) {
	mgr := newFailedEventsManager(t, "itfailedret")
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 0, RetentionDays: 0, FailedEventRetentionDays: 7,
	}))

	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	var dropAfter string
	require.NoError(t, sys.Raw(`SELECT config->>'drop_after' FROM timescaledb_information.jobs `+
		`WHERE proc_name = 'policy_retention' AND hypertable_schema = 'event-management' `+
		`AND hypertable_name = 'failed_event_records'`).Scan(&dropAfter).Error)
	assert.Equal(t, "7 days", dropAfter)

	var others int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention' `+
		`AND hypertable_schema = 'event-management' AND hypertable_name <> 'failed_event_records'`).Scan(&others).Error)
	assert.Zero(t, others, "the uniform window is off, so no other table has a retention policy")

	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	for i, at := range []time.Time{old, now} {
		_, err := api.InsertFailedEventRecord(ctx, &FailedEventRecord{OccurredTime: at, RecordSeq: int64(i + 1),
			SourceArea: "device-management", Reason: 3, DeviceToken: "ghost"})
		require.NoError(t, err)
	}

	var jobID int64
	require.NoError(t, sys.Raw(`SELECT job_id FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention' `+
		`AND hypertable_name = 'failed_event_records'`).Scan(&jobID).Error)
	require.NoError(t, sys.Exec(`CALL run_job(?)`, jobID).Error)

	res, err := api.FailedEvents(ctx, FailedEventSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, res.Results, 1, "the 30-day-old row was dropped by the retention policy")
	assert.EqualValues(t, 2, res.Results[0].RecordSeq)
}

// The queries the API runs, against the real engine: an idempotent insert, the suppressed bump,
// the summary's aggregates and the device count.
func TestIntegrationFailedEventsStoreAndRead(t *testing.T) {
	mgr := newFailedEventsManager(t, "itfailedrw")
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	now := time.Now().UTC().Truncate(time.Microsecond)

	rec := &FailedEventRecord{OccurredTime: now, RecordSeq: 5, SourceArea: "device-management", Reason: 3, DeviceToken: "ghost", Error: "x"}
	ok, err := api.InsertFailedEventRecord(ctx, rec)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = api.InsertFailedEventRecord(ctx, &FailedEventRecord{OccurredTime: now, RecordSeq: 5, SourceArea: "device-management", Reason: 3, DeviceToken: "ghost"})
	require.NoError(t, err)
	assert.False(t, ok, "a redelivery collides on (tenant, seq, time)")

	require.NoError(t, api.BumpFailedEventSuppressed(ctx, FailedEventRef{RecordSeq: 5, OccurredTime: now}, 99))
	sum, err := api.FailedEventSummary(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, sum, 1)
	assert.EqualValues(t, 100, sum[0].Records)
	assert.EqualValues(t, 99, sum[0].Suppressed)
	require.Len(t, sum[0].Devices, 1)
	assert.EqualValues(t, 100, sum[0].Devices[0].Records)

	n, err := api.DeviceFailedEventCount(ctx, "ghost", now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 100, n)

	refs, err := api.RecentFailedEvents(ctx, 3, "ghost", now.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.EqualValues(t, 5, refs[0].RecordSeq)
	cnt, err := api.CountFailedEvents(ctx, 3, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, cnt)

	// Another tenant sees none of it.
	other := core.WithTenant(context.Background(), "globex")
	res, err := api.FailedEvents(other, FailedEventSearchCriteria{})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
}

// A tenant delete leaves ZERO rows: the purge reaches the table by its tenant column, with no
// step that names it, and the other tenant's rows are untouched.
func TestIntegrationATenantDeleteLeavesNoFailedEventRows(t *testing.T) {
	mgr := newFailedEventsManager(t, "itfailedpurge")
	api := NewApi(mgr)
	now := time.Now().UTC()
	for _, tenant := range []string{"acme", "globex"} {
		ctx := core.WithTenant(context.Background(), tenant)
		for i := 0; i < 3; i++ {
			_, err := api.InsertFailedEventRecord(ctx, &FailedEventRecord{OccurredTime: now.Add(-time.Duration(i) * time.Minute),
				RecordSeq: int64(i + 1), SourceArea: "device-management", Reason: 3, DeviceToken: "ghost"})
			require.NoError(t, err)
		}
	}

	sys := core.WithSystemContext(context.Background())
	plan, err := tenantpurge.Classify(sys, mgr.Database)
	require.NoError(t, err)
	var found bool
	for _, e := range plan.Entries {
		if e.Table.Schema == "event-management" && e.Table.Name == "failed_event_records" {
			found = true
			assert.Equal(t, tenantpurge.ClassDirect, e.Class, "swept by its tenant column, not by being named")
		}
	}
	require.True(t, found, "the plan names the failed-events index")

	epoch := time.Now().UTC()
	_, err = tenantpurge.Sweep(sys, mgr.Database, plan, "acme", epoch, nil)
	require.NoError(t, err)
	resid, err := tenantpurge.Residue(sys, mgr.Database, plan, "acme", epoch)
	require.NoError(t, err)
	assert.Zero(t, resid.Rows, "nothing of the tenant remains")

	var acme, globex int64
	db := mgr.Database.WithContext(sys)
	require.NoError(t, db.Model(&FailedEventRecord{}).Where("tenant_id = ?", "acme").Count(&acme).Error)
	require.NoError(t, db.Model(&FailedEventRecord{}).Where("tenant_id = ?", "globex").Count(&globex).Error)
	assert.Zero(t, acme)
	assert.EqualValues(t, 3, globex, "the other tenant's rows are not touched")
}
