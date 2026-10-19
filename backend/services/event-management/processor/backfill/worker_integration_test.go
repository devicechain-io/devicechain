// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package backfill

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// twoDevices seeds two devices of identical unfilled readings, so a job has two roster batches
// (the roster serves one device a page) and several chunk windows each.
func (r *rig) twoDevices(now time.Time) {
	r.t.Helper()
	msgs := sampleMessages(now)
	created := now.Add(-100 * 24 * time.Hour)
	r.seed("d-a", msgs, nil, nil)
	r.seed("d-b", msgs, nil, nil)
	r.roster.devices = []RosterEntry{device(1, "d-a", created, fptr(2)), device(2, "d-b", created, fptr(2))}
	r.roster.pageCap = 1
}

// counters is what a finished job reports, minus its identity and timestamps.
type counters struct {
	DevicesDone, Samples, Computed, Written, AlreadyPresent int64
	Skipped                                                 string
}

func countersOf(j model.CalculatedBackfill) counters {
	return counters{j.DevicesDone, j.Samples, j.Computed, j.Written, j.AlreadyPresent, string(j.Skipped)}
}

// control runs the same job uninterrupted for another tenant over the same data and returns what
// a correct run reports and stores. The data is seeded under that tenant first, so the two runs
// see identical readings.
func (r *rig) control(now time.Time) (counters, []row) {
	r.t.Helper()
	keep := r.tenant
	r.tenant = "control"
	defer func() { r.tenant = keep }()
	r.twoDevices(now)
	r.build()
	r.newJob("ctrl", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("ctrl")
	require.Equal(r.t, model.BackfillSucceeded, j.Status, "control failure: %v", j.Failure)
	require.NotZero(r.t, j.Written)
	return countersOf(j), withoutDevice(r.derived("d-a", "d-b"))
}

// A second job over the same range writes nothing: every pair is found present, and the row
// count does not move.
func TestIntegrationRerunWritesNothing(t *testing.T) {
	r := newRig(t, "rerun")
	now := time.Now().UTC()
	r.twoDevices(now)
	r.build()
	r.newJob("first", now.Add(-72*time.Hour), now)
	first := r.runUntilDone("first")
	require.Equal(t, model.BackfillSucceeded, first.Status)
	require.NotZero(t, first.Written)
	before := r.derived("d-a", "d-b")

	r.newJob("second", now.Add(-72*time.Hour), now)
	second := r.runUntilDone("second")
	require.Equal(t, model.BackfillSucceeded, second.Status)
	assert.Zero(t, second.Written, "nothing is missing the second time")
	assert.Equal(t, first.Written, second.AlreadyPresent, "every row the first run wrote is found")
	assert.Equal(t, second.Computed, second.AlreadyPresent)
	assert.Equal(t, before, r.derived("d-a", "d-b"))
}

// A worker that dies mid-job resumes where the last commit left it, and the job's counters end
// exactly where an uninterrupted run's do. Two ways to die: the batch's transaction fails
// before it commits (a crash mid-batch), and the pass is cancelled between batches (a stop).
// At every stop the rows in the database equal the job's written counter, so a batch is
// either wholly in or wholly out.
func TestIntegrationKilledJobResumesWithExactCounters(t *testing.T) {
	for _, how := range []string{"crash mid-batch", "stop between batches"} {
		t.Run(how, func(t *testing.T) {
			r := newRig(t, "kill"+map[string]string{"crash mid-batch": "a", "stop between batches": "b"}[how])
			now := time.Now().UTC()
			want, wantRows := r.control(now)

			r.tenant = "acme"
			r.twoDevices(now)
			w := r.build()
			r.newJob("j", now.Add(-72*time.Hour), now)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			killAt := 3
			if how == "crash mid-batch" {
				w.hooks.beforeCommit = func(context.Context, *gorm.DB) error {
					calls++
					if calls == killAt {
						return errors.New("killed mid-batch")
					}
					return nil
				}
			} else {
				w.hooks.afterCommit = func(context.Context) error {
					calls++
					if calls == killAt {
						cancel()
					}
					return nil
				}
			}
			err := w.RunOnce(ctx)
			if how == "crash mid-batch" {
				require.Error(t, err, "the first pass dies")
			}
			mid := r.job("j")
			assert.Equal(t, model.BackfillRunning, mid.Status)
			assert.Less(t, mid.Written, want.Written, "it died with work left")
			assert.EqualValues(t, mid.Written, int64(len(r.derived("d-a", "d-b"))),
				"the stored rows are exactly what the committed counters account for")

			w.hooks = hooks{}
			j := r.runUntilDone("j")
			require.Equal(t, model.BackfillSucceeded, j.Status)
			assert.Equal(t, want, countersOf(j), "counters are exact across the kill")
			assert.Equal(t, wantRows, withoutDevice(r.derived("d-a", "d-b")))
			assert.Greater(t, j.ClaimEpoch, int64(1), "the resume claimed again")
		})
	}
}

// A worker whose claim was taken over cannot commit: its batch rolls back and it stops, whether
// the new claim came while it held the lock or after the lock's session was terminated. Neither
// double-counts.
func TestIntegrationClaimEpochFencesAStaleWorker(t *testing.T) {
	t.Run("a newer claim lands while the first worker is mid-batch", func(t *testing.T) {
		r := newRig(t, "epoch")
		now := time.Now().UTC()
		want, wantRows := r.control(now)
		r.tenant = "acme"
		r.twoDevices(now)
		a := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)

		var skippedWhileHeld error
		taken := false
		a.hooks.beforeCommit = func(ctx context.Context, tx *gorm.DB) error {
			if taken {
				return nil
			}
			taken = true
			// A second worker finds the lock held and declines: one worker per instance.
			b2 := r.build2()
			skippedWhileHeld = b2.RunOnce(ctx)
			// Whatever took the claim (a failover replica that got the lock after the first
			// session died) takes it by this guarded update, on another connection.
			_, err := model.ClaimBackfill(r.tdb(), "j")
			return err
		}
		require.NoError(t, a.RunOnce(context.Background()), "a stale worker stops quietly")
		assert.ErrorIs(t, skippedWhileHeld, core.ErrPassSkipped)

		j := r.job("j")
		assert.EqualValues(t, 2, j.ClaimEpoch, "the other claim stands")
		assert.Zero(t, j.Written, "the stale worker's batch rolled back")
		assert.Empty(t, r.derived("d-a", "d-b"), "no row survived the rollback")

		a.hooks = hooks{}
		j = r.runUntilDone("j")
		assert.Equal(t, want, countersOf(j))
		assert.Equal(t, wantRows, withoutDevice(r.derived("d-a", "d-b")))
	})

	t.Run("the lock session is terminated mid-batch", func(t *testing.T) {
		r := newRig(t, "epochterm")
		now := time.Now().UTC()
		want, wantRows := r.control(now)
		r.tenant = "acme"
		r.twoDevices(now)
		a := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)

		killed := false
		a.hooks.beforeCommit = func(ctx context.Context, tx *gorm.DB) error {
			if killed {
				return nil
			}
			killed = true
			var pid int
			require.NoError(t, tx.Raw(`SELECT pg_backend_pid()`).Scan(&pid).Error)
			require.NoError(t, r.sys().Exec(`SELECT pg_terminate_backend(?)`, pid).Error)
			return nil
		}
		require.Error(t, a.RunOnce(context.Background()), "the work died with the lock's session")
		assert.EqualValues(t, 1, r.job("j").ClaimEpoch)
		assert.Zero(t, r.job("j").Written)

		// A second worker, a new process, takes over once the lock is free.
		b := r.build2()
		var j model.CalculatedBackfill
		for i := 0; i < 100; i++ {
			_ = b.RunOnce(context.Background())
			if j = r.job("j"); j.Status == model.BackfillSucceeded {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		require.Equal(t, model.BackfillSucceeded, j.Status)
		assert.Equal(t, want, countersOf(j), "nothing was counted twice")
		assert.Equal(t, wantRows, withoutDevice(r.derived("d-a", "d-b")))
	})
}

// build2 makes another worker over the same database, as a second replica would.
func (r *rig) build2() *Worker {
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	return NewWorker(ms, r.mgr, r.roster, r.cfg, NewMetrics(ms), core.NewNoOpLifecycleCallbacks())
}

// Cancelling a QUEUED job is immediate and the worker never runs it. Cancelling a RUNNING one
// is seen at the next commit, which rolls back; the batches committed before it stay.
func TestIntegrationCancel(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		r := newRig(t, "cancelq")
		now := time.Now().UTC()
		r.twoDevices(now)
		w := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)
		cancelled, err := model.CancelBackfill(r.tdb(), "j")
		require.NoError(t, err)
		assert.Equal(t, model.BackfillCancelled, cancelled.Status)
		require.NotNil(t, cancelled.FinishedAt)

		require.NoError(t, w.RunOnce(context.Background()))
		assert.Empty(t, r.derived("d-a", "d-b"))
		assert.Zero(t, r.job("j").ClaimEpoch, "never claimed")
		// The slot is free again.
		r.newJob("next", now.Add(-72*time.Hour), now)
	})

	t.Run("running", func(t *testing.T) {
		r := newRig(t, "cancelr")
		now := time.Now().UTC()
		r.twoDevices(now)
		w := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)
		calls := 0
		w.hooks.beforeCommit = func(ctx context.Context, tx *gorm.DB) error {
			calls++
			if calls == 3 {
				_, err := model.CancelBackfill(r.tdb(), "j")
				return err
			}
			return nil
		}
		require.NoError(t, w.RunOnce(context.Background()))
		j := r.job("j")
		assert.Equal(t, model.BackfillCancelled, j.Status)
		require.NotNil(t, j.FinishedAt)
		require.NotNil(t, j.CancelRequestedAt)
		assert.NotZero(t, j.Written, "what committed before the cancel stays")
		assert.EqualValues(t, j.Written, int64(len(r.derived("d-a", "d-b"))), "and the in-flight batch did not")
		// And the worker never picks it up again.
		w.hooks = hooks{}
		require.NoError(t, w.RunOnce(context.Background()))
		assert.Equal(t, j.Written, r.job("j").Written)
	})
}

// compressChunksOlderThan enables compression on the measurement hypertable the way the
// lifecycle reconciler does and compresses every chunk wholly older than d.
func (r *rig) compressChunksOlderThan(d time.Duration) {
	r.t.Helper()
	sys := r.sys()
	require.NoError(r.t, sys.Exec(`ALTER TABLE "event-management".measurement_events SET (timescaledb.compress, `+
		`timescaledb.compress_segmentby = 'tenant_id, device_token', timescaledb.compress_orderby = 'occurred_time DESC')`).Error)
	var chunks []string
	require.NoError(r.t, sys.Raw(`SELECT c::text FROM show_chunks('"event-management".measurement_events', older_than => ?::timestamptz) c`,
		time.Now().Add(-d)).Scan(&chunks).Error)
	for _, c := range chunks {
		require.NoError(r.t, sys.Exec(`SELECT compress_chunk(?::regclass)`, c).Error)
	}
	require.NotEmpty(r.t, chunks, "there was nothing to compress, so the test proves nothing")
}

// A compressed chunk is skipped and counted, not written into. The same job fills the chunks
// that are not compressed.
func TestIntegrationCompressedWindowIsSkipped(t *testing.T) {
	r := newRig(t, "compressed")
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)
	for _, at := range []time.Time{old, recent} {
		r.seed("d-a", []message{{[]entry{{at, "voltage", "10"}, {at, "current", "2"}}}}, nil, nil)
	}
	r.compressChunksOlderThan(5 * 24 * time.Hour)
	r.roster.devices = []RosterEntry{device(1, "d-a", now.Add(-200*24*time.Hour), fptr(2))}
	r.build()
	r.newJob("j", now.Add(-12*24*time.Hour), now)
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)

	assert.EqualValues(t, 1, j.WindowsCompressed)
	rows := r.derived("d-a")
	require.NotEmpty(t, rows)
	for _, x := range rows {
		assert.True(t, x.At >= recent.Add(-time.Hour).Format(time.RFC3339Nano), "only the recent chunk was filled: %s", x.At)
	}
}

// A chunk that is compressed AFTER the skip check, while the batch is being worked, can fail the
// insert with Timescale's decompression limit (configuration_limit_exceeded). The window is then
// rolled back whole, counted compressed, and the job carries on.
//
// The limit is injected, not provoked: on the Timescale this runs against (2.30.2) an insert of
// a NEW key into a compressed chunk did not decompress anything even with
// timescaledb.max_tuples_decompressed_per_dml_transaction set to 1, so the real error cannot be
// forced from here. What this pins is the worker's side of the contract: the error's code is
// recognised, the batch is rolled back with nothing written, the window is counted, and the
// cursor moves past it.
func TestIntegrationCompressionBetweenCheckAndInsertIsMapped(t *testing.T) {
	r := newRig(t, "midcompress")
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)
	for _, at := range []time.Time{old, recent} {
		r.seed("d-a", []message{{[]entry{{at, "voltage", "10"}, {at, "current", "2"}}}}, nil, nil)
	}
	r.roster.devices = []RosterEntry{device(1, "d-a", now.Add(-200*24*time.Hour), fptr(2))}
	w := r.build()
	r.newJob("j", now.Add(-12*24*time.Hour), now)
	compressed, fail := false, false
	w.hooks.afterSkipCheck = func(ctx context.Context, db *gorm.DB, win chunkWindow) {
		if compressed || !win.End.Before(now.Add(-5*24*time.Hour)) {
			return
		}
		compressed, fail = true, true
		r.compressChunksOlderThan(5 * 24 * time.Hour)
	}
	w.hooks.beforeCommit = func(context.Context, *gorm.DB) error {
		if fail {
			fail = false
			return &pgconn.PgError{Code: "53400", Message: "tuple decompression limit exceeded by operation"}
		}
		return nil
	}
	// Every pass succeeds: the mapped error is a skipped window, not a failed pass that the
	// next pass happens to get past because the chunk is compressed by then.
	for i := 0; i < 50; i++ {
		require.NoError(t, w.RunOnce(context.Background()), "pass %d", i)
		if s := r.job("j").Status; s != model.BackfillQueued && s != model.BackfillRunning {
			break
		}
	}
	j := r.job("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
	require.True(t, compressed)
	assert.EqualValues(t, 1, j.WindowsCompressed, "the window that compressed under it is counted compressed")
	assert.EqualValues(t, int64(len(r.derived("d-a"))), j.Written)
	for _, x := range r.derived("d-a") {
		assert.True(t, x.At >= recent.Add(-time.Hour).Format(time.RFC3339Nano), "the old chunk was not written: %s", x.At)
	}
}

// A window within two chunks of the retention horizon is skipped and counted; a window inside
// retention is filled.
func TestIntegrationRetentionWindowIsSkipped(t *testing.T) {
	r := newRig(t, "retention")
	now := time.Now().UTC()
	near := now.Add(-60 * time.Hour) // its chunk ends before now - 1 day
	recent := now.Add(-10 * time.Hour)
	for _, at := range []time.Time{near, recent} {
		r.seed("d-a", []message{{[]entry{{at, "voltage", "10"}, {at, "current", "2"}}}}, nil, nil)
	}
	r.roster.devices = []RosterEntry{device(1, "d-a", now.Add(-200*24*time.Hour), fptr(2))}
	r.cfg.RetentionDays = 3 // the horizon is now - 3d; with two 1-day chunks of margin: now - 1d
	r.build()
	r.newJob("j", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
	assert.EqualValues(t, 1, j.WindowsBeyondRetention)
	rows := r.derived("d-a")
	require.NotEmpty(t, rows)
	for _, x := range rows {
		assert.True(t, x.At >= near.Add(24*time.Hour).Format(time.RFC3339Nano), "a near-retention chunk was written: %s", x.At)
	}
}

// A reused token does not inherit an earlier device's readings: each device is filled only from
// its own creation.
func TestIntegrationCreatedAtClampsAReusedToken(t *testing.T) {
	r := newRig(t, "clamp")
	now := time.Now().UTC()
	before := now.Add(-26 * time.Hour)
	after := now.Add(-2 * time.Hour)
	for _, at := range []time.Time{before, after} {
		r.seed("reused", []message{{[]entry{{at, "voltage", "10"}, {at, "current", "2"}}}}, nil, nil)
	}
	// The device that holds the token now was created between the two readings.
	r.roster.devices = []RosterEntry{device(1, "reused", now.Add(-10*time.Hour), fptr(2))}
	r.build()
	r.newJob("j", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
	rows := r.derived("reused")
	require.NotEmpty(t, rows)
	for _, x := range rows {
		assert.Equal(t, after.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), x.At,
			"only the reading after the device's creation is filled")
	}
}

func (r *rig) plantFence(tenant string) {
	r.t.Helper()
	now := time.Now().UTC()
	require.NoError(r.t, r.sys().Create(&rdb.PurgedTenant{Token: tenant, Epoch: now, PlantedAt: now}).Error)
}

// A tenant being erased is not claimed, and a fence that stands mid-job stops it with nothing
// further written.
func TestIntegrationErasureFenceStopsTheJob(t *testing.T) {
	t.Run("at claim", func(t *testing.T) {
		r := newRig(t, "fenceclaim")
		now := time.Now().UTC()
		r.twoDevices(now)
		w := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)
		r.plantFence(r.tenant)
		require.NoError(t, w.RunOnce(context.Background()))
		j := r.job("j")
		assert.Equal(t, model.BackfillQueued, j.Status, "left alone for the purge sweep")
		assert.Zero(t, j.ClaimEpoch, "not even claimed")
		assert.Empty(t, r.derived("d-a", "d-b"))
	})

	t.Run("mid-job", func(t *testing.T) {
		r := newRig(t, "fencemid")
		now := time.Now().UTC()
		r.twoDevices(now)
		w := r.build()
		r.newJob("j", now.Add(-72*time.Hour), now)
		calls := 0
		w.hooks.afterCommit = func(context.Context) error {
			calls++
			if calls == 2 {
				r.plantFence(r.tenant)
			}
			return nil
		}
		require.NoError(t, w.RunOnce(context.Background()), "stopping for the fence is not a failure")
		j := r.job("j")
		assert.Equal(t, model.BackfillRunning, j.Status)
		stored := int64(len(r.derived("d-a", "d-b")))
		assert.Equal(t, j.Written, stored)
		assert.NotZero(t, stored)
		// Nothing more is written however many passes follow.
		w.hooks = hooks{}
		for i := 0; i < 3; i++ {
			require.NoError(t, w.RunOnce(context.Background()))
		}
		assert.Equal(t, stored, int64(len(r.derived("d-a", "d-b"))))
		assert.Equal(t, j.Written, r.job("j").Written)
	})
}

// With no job the worker does nothing and says it did nothing; a second worker finds the lock
// held and declines. No broker exists in this test or anywhere in the package.
func TestIntegrationInertWithNoJobs(t *testing.T) {
	r := newRig(t, "inert")
	w := r.build()
	require.NoError(t, w.RunOnce(context.Background()))
	var n int64
	require.NoError(t, r.sys().Model(&model.MeasurementEvent{}).Count(&n).Error)
	assert.Zero(t, n)

	// Another holder of the instance lock (a second replica's worker) makes this one decline the
	// pass, and a queued job is left exactly as it was: not claimed, not touched.
	now := time.Now().UTC()
	r.twoDevices(now)
	r.newJob("j", now.Add(-72*time.Hour), now)
	sqldb, err := r.mgr.Database.DB()
	require.NoError(t, err)
	holder, err := sqldb.Conn(context.Background())
	require.NoError(t, err)
	defer holder.Close()
	var got bool
	require.NoError(t, holder.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)",
		rdb.AdvisoryLockKey(lockName)).Scan(&got))
	require.True(t, got)
	assert.ErrorIs(t, w.RunOnce(context.Background()), core.ErrPassSkipped)
	j := r.job("j")
	assert.Equal(t, model.BackfillQueued, j.Status)
	assert.Zero(t, j.ClaimEpoch)
	assert.Empty(t, r.derived("d-a", "d-b"))

	// Released, the next pass takes the job.
	_, err = holder.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", rdb.AdvisoryLockKey(lockName))
	require.NoError(t, err)
	require.NoError(t, w.RunOnce(context.Background()))
	assert.Equal(t, int64(1), r.job("j").ClaimEpoch)
}

// A roster that cannot be read leaves the job RUNNING and retried; after an hour of it the job
// fails, naming device-management.
func TestIntegrationRosterOutageFailsAfterAnHour(t *testing.T) {
	r := newRig(t, "dmdown")
	now := time.Now().UTC()
	r.twoDevices(now)
	r.roster.fail = errors.New("connection refused")
	w := r.build()
	clock := now
	w.now = func() time.Time { return clock }
	r.newJob("j", now.Add(-72*time.Hour), now)

	require.Error(t, w.RunOnce(context.Background()))
	assert.Equal(t, model.BackfillRunning, r.job("j").Status, "an outage is not a failure of the job")
	clock = clock.Add(30 * time.Minute)
	require.Error(t, w.RunOnce(context.Background()))
	assert.Equal(t, model.BackfillRunning, r.job("j").Status)
	clock = clock.Add(31 * time.Minute)
	require.NoError(t, w.RunOnce(context.Background()))
	j := r.job("j")
	assert.Equal(t, model.BackfillFailed, j.Status)
	require.NotNil(t, j.Failure)
	assert.Contains(t, *j.Failure, "device-management")

	// And an outage that ends first costs nothing.
	r2 := newRig(t, "dmblip")
	r2.twoDevices(now)
	r2.roster.fail = errors.New("connection refused")
	w2 := r2.build()
	r2.newJob("j", now.Add(-72*time.Hour), now)
	require.Error(t, w2.RunOnce(context.Background()))
	r2.roster.fail = nil
	require.Equal(t, model.BackfillSucceeded, r2.runUntilDone("j").Status)
}

// ---- rollup refresh -----------------------------------------------------------------------

func (r *rig) materializedRows(name string) int64 {
	r.t.Helper()
	var mat string
	require.NoError(r.t, r.sys().Raw(`SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name) `+
		`FROM timescaledb_information.continuous_aggregates WHERE view_schema = 'event-management' `+
		`AND view_name = 'measurement_rollups'`).Scan(&mat).Error)
	var n int64
	require.NoError(r.t, r.sys().Raw(fmt.Sprintf(`SELECT count(*) FROM %s WHERE name = ? AND tenant_id = ?`, mat),
		name, r.tenant).Scan(&n).Error)
	return n
}

func (r *rig) oldHistory(now time.Time) {
	r.t.Helper()
	// Older than the policy's thirty days, so only this job's own refresh can materialize it.
	for _, d := range []time.Duration{45 * 24 * time.Hour, 44*24*time.Hour + 3*time.Hour} {
		at := now.Add(-d)
		r.seed("d-a", []message{{[]entry{{at, "voltage", "10"}, {at, "current", "2"}}}}, nil, nil)
	}
	r.roster.devices = []RosterEntry{device(1, "d-a", now.Add(-200*24*time.Hour), fptr(2))}
}

// The job refreshes the rollup, one chunk at a time, over history the aggregate's policy never
// reaches, so a chart of it shows the filled values once the job is done. The negative control
// is the same job with the refresh disabled, which leaves the rollup without them: the
// assertion reads the refresh, not something the insert does anyway.
func TestIntegrationRollupIsRefreshedForTouchedChunks(t *testing.T) {
	now := time.Now().UTC()
	t.Run("refreshed", func(t *testing.T) {
		r := newRig(t, "refpos")
		r.oldHistory(now)
		r.build()
		r.newJob("j", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		require.Zero(t, r.materializedRows("power"), "nothing is materialized before the job")
		j := r.runUntilDone("j")
		require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
		require.NotZero(t, j.Written)
		assert.NotZero(t, j.WindowsRefreshed)
		assert.NotZero(t, r.materializedRows("power"), "the derived rows are in the rollup's materialization")
		require.NotNil(t, j.RefreshCursor)
	})
	t.Run("negative control: refresh disabled", func(t *testing.T) {
		r := newRig(t, "refneg")
		r.oldHistory(now)
		w := r.build()
		w.hooks.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { return nil }
		r.newJob("j", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		j := r.runUntilDone("j")
		require.Equal(t, model.BackfillSucceeded, j.Status)
		require.NotZero(t, j.Written)
		assert.Zero(t, r.materializedRows("power"), "without the refresh the rollup has none of it")
	})
	t.Run("a job that wrote nothing refreshes nothing", func(t *testing.T) {
		r := newRig(t, "refnone")
		r.oldHistory(now)
		w := r.build()
		called := 0
		w.hooks.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { called++; return nil }
		r.newJob("a", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		require.Equal(t, model.BackfillSucceeded, r.runUntilDone("a").Status)
		first := called
		require.NotZero(t, first)
		r.newJob("b", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		j := r.runUntilDone("b")
		assert.Zero(t, j.Written)
		assert.Equal(t, first, called, "a re-run that writes nothing costs the instance no refresh")
	})
}

// A refresh that meets the aggregate policy's own refresh (55P03) is retried; the cursor does
// not move until a refresh succeeds; ten collisions in a row fail the job naming the window.
func TestIntegrationRefreshRetriesOnPolicyCollision(t *testing.T) {
	now := time.Now().UTC()
	collide := &pgconn.PgError{Code: "55P03", Message: "could not obtain lock"}
	t.Run("retried then refreshed", func(t *testing.T) {
		r := newRig(t, "refretry")
		r.oldHistory(now)
		w := r.build()
		w.hooks.sleep = func(context.Context, time.Duration) error { return nil }
		failures := 0
		w.hooks.refresh = func(ctx context.Context, db *gorm.DB, ws, we time.Time) error {
			if c := r.job("j").RefreshCursor; c != nil {
				assert.False(t, c.After(ws), "the cursor has not moved past a window that is not refreshed")
			}
			if failures < 2 {
				failures++
				return collide
			}
			return db.Exec(`CALL refresh_continuous_aggregate(?::regclass, ?::timestamptz, ?::timestamptz)`,
				rollupView, ws, we).Error
		}
		r.newJob("j", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		j := r.runUntilDone("j")
		require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
		assert.Equal(t, 2, failures)
		assert.NotZero(t, r.materializedRows("power"))
	})
	t.Run("gives up and names the window", func(t *testing.T) {
		r := newRig(t, "refgiveup")
		r.oldHistory(now)
		w := r.build()
		w.hooks.sleep = func(context.Context, time.Duration) error { return nil }
		tries := 0
		w.hooks.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { tries++; return collide }
		r.newJob("j", now.Add(-50*24*time.Hour), now.Add(-40*24*time.Hour))
		j := r.runUntilDone("j")
		assert.Equal(t, model.BackfillFailed, j.Status)
		assert.Equal(t, refreshTries, tries)
		require.NotNil(t, j.Failure)
		assert.Contains(t, *j.Failure, "rollup refresh")
		assert.Nil(t, j.RefreshCursor)
	})
}

// The thirty days the worker assumes the aggregate's policy covers are the policy's own.
func TestIntegrationPolicyReachMatchesTheLivePolicy(t *testing.T) {
	r := newRig(t, "policyreach")
	var startOffset string
	require.NoError(t, r.sys().Raw(`SELECT config->>'start_offset' FROM timescaledb_information.jobs `+
		`WHERE proc_name = 'policy_refresh_continuous_aggregate'`).Scan(&startOffset).Error)
	require.Equal(t, "30 days", startOffset, "the policy reaches back %s", startOffset)
	assert.Equal(t, 30*24*time.Hour, rollupPolicyReach)
}

// A message that carried one input twice at one instant with different values stored both rows,
// and storage cannot say which came first, which is the one live read. The sample is skipped
// and counted as ambiguous for each selected key, never filled with either value; the same
// repeat with EQUAL values is no ambiguity and fills. A clean sample beside them still fills.
func TestIntegrationAnInputRepeatedWithTwoValuesIsAmbiguous(t *testing.T) {
	r := newRig(t, "ambig")
	now := time.Now().UTC()
	t1 := now.Add(-2 * time.Hour).Truncate(time.Second)
	t2 := now.Add(-3 * time.Hour).Truncate(time.Second)
	t3 := now.Add(-4 * time.Hour).Truncate(time.Second)
	r.seed("d", []message{
		{[]entry{{t1, "voltage", "230"}, {t1, "voltage", "231"}, {t1, "current", "10"}}},
		{[]entry{{t2, "voltage", "100"}, {t2, "current", "2"}}},
	}, nil, nil)
	// Equal values: a second row exists only if something else about it differs, so seed the
	// repeat with a different classifier through a request of its own.
	ev := model.Event{EventId: []byte("equal-repeat-event-id-0000000000"), DeviceToken: "d",
		EventType: esmodel.Measurement, OccurredTime: t3, Source: "mqtt", ProcessedTime: now}
	var reqs []*model.MeasurementEventCreateRequest
	for _, x := range []struct {
		name, value string
		cls         uint64
	}{{"voltage", "50", 1}, {"voltage", "50", 99}, {"current", "4", 2}} {
		cls := x.cls
		req, err := model.MeasurementRequestFor(ev, t3, model.MeasurementReading{Name: x.name, Value: x.value, Classifier: &cls})
		require.NoError(t, err)
		reqs = append(reqs, req)
	}
	_, err := r.api.CreateMeasurementEvents(r.ctx(), r.tdb(), reqs)
	require.NoError(t, err)
	require.Equal(t, int64(8), r.countRows("d"), "both repeated readings are stored, in both samples")

	r.roster.devices = []RosterEntry{device(1, "d", now.Add(-100*24*time.Hour), fptr(2))}
	r.build()
	r.newJob("j", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)

	at := func(x time.Time) string { return x.UTC().Format(time.RFC3339Nano) }
	got := map[string][]string{}
	for _, x := range r.derived("d") {
		got[x.At] = append(got[x.At], x.Name)
	}
	assert.Empty(t, got[at(t1)], "neither value of the repeated input is used")
	assert.ElementsMatch(t, []string{"power", "kw", "flag"}, got[at(t2)], "a clean sample fills")
	assert.ElementsMatch(t, []string{"power", "kw", "flag"}, got[at(t3)], "an equal repeat is no ambiguity")
	skips, err := j.SkippedCounts()
	require.NoError(t, err)
	assert.Equal(t, int64(3), skips["ambiguous"], "one per selected key of the ambiguous sample: %v", skips)
	assert.Equal(t, int64(3), j.Samples)
	assert.Equal(t, j.Written+j.AlreadyPresent, j.Computed)
}

// A live write that lands between the fill's read and its insert holds the same canonical row,
// so the insert affects nothing. Those rows are counted present, not written: written is the
// rows THIS job stored, and computed still reconciles as written + alreadyPresent.
func TestIntegrationALiveWriteRacingTheInsertCountsAsPresent(t *testing.T) {
	r := newRig(t, "race")
	now := time.Now().UTC()
	r.twoDevices(now)
	w := r.build()
	r.newJob("j", now.Add(-72*time.Hour), now)
	var raced int64
	w.hooks.beforeInsert = func(ctx context.Context, rows []*model.MeasurementEvent) {
		if raced > 0 {
			return
		}
		// The first flush's rows are written by "live" on another connection, committed before
		// the fill's insert runs.
		n, err := model.InsertDerivedMeasurementRows(r.tdb(), rows)
		require.NoError(t, err)
		require.Equal(t, int64(len(rows)), n)
		raced = n
	}
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
	require.NotZero(t, raced)
	stored := int64(len(r.derived("d-a", "d-b")))
	assert.Equal(t, stored-raced, j.Written, "only what the job itself stored is written")
	assert.Equal(t, raced, j.AlreadyPresent, "the raced rows are present")
	assert.Equal(t, j.Written+j.AlreadyPresent, j.Computed)
}

// A derived row stored before the canonical derived identity carries a different payload id
// than the fill would compute, so the insert's conflict cannot catch it. The read's present
// check does: the sample already holds the key by name, it is counted present, and no second
// row is written beside the old one.
func TestIntegrationAnOldIdDerivedRowIsNotDuplicated(t *testing.T) {
	r := newRig(t, "oldid")
	now := time.Now().UTC()
	t1 := now.Add(-2 * time.Hour).Truncate(time.Second).Add(123456789)
	r.seed("d", []message{{[]entry{{t1, "voltage", "230.5"}, {t1, "current", "10.25"}}}}, nil, nil)
	// The old row: the same event, name and instant, under the value-keyed id a raw row has.
	var parent model.MeasurementEvent
	require.NoError(t, r.tdb().Where("device_token = ? AND name = ?", "d", "voltage").Take(&parent).Error)
	cls := uint64(5)
	old, err := model.MeasurementRequestFor(model.Event{EventId: parent.EventId, DeviceToken: "d",
		EventType: parent.EventType, OccurredTime: parent.OccurredTime}, t1,
		model.MeasurementReading{Name: "power", Value: "2362.625", Classifier: &cls})
	require.NoError(t, err)
	_, rows, err := model.BuildMeasurementRows([]*model.MeasurementEventCreateRequest{old})
	require.NoError(t, err)
	n, err := model.InsertDerivedMeasurementRows(r.tdb(), rows)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	r.roster.devices = []RosterEntry{device(1, "d", now.Add(-100*24*time.Hour), fptr(2))}
	r.build()
	r.newJob("j", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("j")
	require.Equal(t, model.BackfillSucceeded, j.Status, "%v", j.Failure)
	var powers int64
	require.NoError(t, r.tdb().Model(&model.MeasurementEvent{}).
		Where("device_token = ? AND name = ?", "d", "power").Count(&powers).Error)
	assert.Equal(t, int64(1), powers, "the old row is the only power row")
	assert.Equal(t, int64(1), j.AlreadyPresent, "power is present")
	assert.Equal(t, int64(2), j.Written, "kw and flag are filled")
	assert.Equal(t, j.Written+j.AlreadyPresent, j.Computed)
}
