// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package backfill is the worker that fills a published profile version's calculated metrics
// into stored history (ADR-082, S8): it evaluates the version's plan over the stored
// measurement rows of the profile's devices and inserts the derived rows that are missing.
//
// 🔴 THIS PACKAGE MUST NEVER PUBLISH. A derived row it writes is history: detection, the live
// device state and live subscriptions must not see it, because they would treat a reading from
// last month as happening now. Nothing here can reach the broker, and deps_test.go keeps it
// that way: the package may not depend on the messaging layer, on nats.go, or on the event
// persistence processor (which does).
//
// 🔴 ONE WORKER PER INSTANCE, ON ONE CONNECTION. A pass takes an advisory lock on a pinned
// connection and runs every statement of every job — reads, batch transactions, rollup
// refreshes — on that same connection (rdb.TryAdvisoryLockSession). When the lock's session
// dies the work dies with it. The claim epoch on every commit is the guarantee behind that: a
// worker that somehow outlives its lock finds its next commit matches no row, rolls back, and
// stops.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// The worker's constants. Only the rate and the on/off switch are the operator's (Config);
// nothing has measured a reason to change the rest.
const (
	// lockName names the instance-wide advisory lock: one worker for the whole instance.
	lockName = "calc-backfill"
	// passInterval is the gap between passes.
	passInterval = 10 * time.Second
	// sliceDuration is how long one pass works on one job before moving to the next, so one
	// tenant's long job does not hold up another's.
	sliceDuration = 5 * time.Minute
	// deviceBatch is the devices read from the roster and processed together.
	deviceBatch = 100
	// maxRunnable bounds the active jobs one pass lists.
	maxRunnable = 50
	// statementTimeout bounds every read and write of a batch.
	statementTimeout = 60 * time.Second
	// refreshTimeout bounds one rollup refresh, which re-aggregates a whole chunk across every
	// tenant.
	refreshTimeout = 30 * time.Minute
	// refreshTries is how often a refresh that meets the aggregate policy's own refresh is
	// retried before the job fails naming the window.
	refreshTries = 10
	// dmDownLimit is how long a job tolerates device-management being unreachable before it
	// fails.
	dmDownLimit = time.Hour
	// consecutiveFailureLimit is how many passes in a row may fail on an error the worker has no
	// name for before the job fails with it, so a deterministic fault ends instead of looping.
	consecutiveFailureLimit = 10
	// rollupPolicyReach is how far back the aggregate's refresh policy reaches (its
	// start_offset), which a test pins to the live policy. Windows older than this, less the
	// margin, are never refreshed by it, so the job refreshes them itself.
	rollupPolicyReach = 30 * 24 * time.Hour
	// maxChunkRows bounds the rows one read page and one insert hold.
	maxChunkRows = 20000
)

// Config is what the operator and the lifecycle policy decide about the worker.
type Config struct {
	// RowsPerSecond is the most rows read plus written in a second, across every job.
	RowsPerSecond int
	// ChunkInterval is the interval of the measurement hypertable's chunks, for the retention
	// and rollup margins.
	ChunkInterval time.Duration
	// RetentionDays is the instance's retention window; 0 keeps everything.
	RetentionDays int
}

// hooks are the seams the tests use to interrupt a batch at a named point. They are nil in
// production.
type hooks struct {
	// beforeCommit runs inside a batch's transaction, after its rows are inserted and before
	// its progress is committed.
	beforeCommit func(ctx context.Context, tx *gorm.DB) error
	// afterCommit runs after a batch's transaction committed.
	afterCommit func(ctx context.Context) error
	// beforeInsert runs inside a batch's transaction with the derived rows a flush is about to
	// insert, after the read that found them missing: the window a live write can race.
	beforeInsert func(ctx context.Context, rows []*model.MeasurementEvent)
	// afterSkipCheck runs after a window passed its skip checks and before it is worked.
	afterSkipCheck func(ctx context.Context, db *gorm.DB, w chunkWindow)
	// refresh replaces the rollup refresh itself.
	refresh func(ctx context.Context, db *gorm.DB, ws, we time.Time) error
	// sleep replaces the wait between refresh retries.
	sleep func(ctx context.Context, d time.Duration) error
}

// Worker runs backfill jobs.
type Worker struct {
	*core.PeriodicTask

	rdb     *rdb.RdbManager
	roster  Roster
	cfg     Config
	metrics *Metrics
	pacer   *pacer
	now     func() time.Time
	hooks   hooks

	// dmDownSince and failures are the worker's memory across passes, per job token. They are
	// lost on a restart, which only restarts the clocks.
	dmDownSince map[string]time.Time
	failures    map[string]int
}

// NewWorker builds the worker. roster may be nil when device-management is not configured, in
// which case a job found is left alone and said so, never failed.
func NewWorker(ms *core.Microservice, rdbm *rdb.RdbManager, roster Roster, cfg Config, metrics *Metrics,
	callbacks core.LifecycleCallbacks) *Worker {
	w := &Worker{
		rdb: rdbm, roster: roster, cfg: cfg, metrics: metrics,
		pacer: newPacer(cfg.RowsPerSecond), now: time.Now,
		dmDownSince: map[string]time.Time{}, failures: map[string]int{},
	}
	w.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, lockName, passInterval, w.RunOnce, callbacks,
		core.WithPassMetrics(metrics.Pass))
	return w
}

// RunOnce makes one pass: under the instance lock, one slice of work for each active job.
func (w *Worker) RunOnce(ctx context.Context) error {
	ran, err := w.rdb.TryAdvisoryLockSession(ctx, rdb.AdvisoryLockKey(lockName), func(db *gorm.DB) error {
		return w.pass(ctx, db)
	})
	// `ran` before `err`: TryAdvisoryLockSession returns `true, fn()`, so a non-nil error is the
	// lock's only when the body never ran.
	if !ran {
		if err != nil {
			return fmt.Errorf("acquiring the backfill lock: %w", err)
		}
		return core.ErrPassSkipped
	}
	return err
}

func (w *Worker) pass(ctx context.Context, db *gorm.DB) error {
	// The session settings below outlive the pass on a pooled connection otherwise.
	defer func() {
		_ = db.WithContext(context.Background()).Exec(`RESET statement_timeout`).Error
	}()
	// 🔴 THE ONE CROSS-TENANT READ: the list of active jobs (and the age of the oldest) across
	// every tenant, from a table of a few rows. Nothing else runs under this context: each job
	// is then worked under its own tenant (runJob), so every read and write of a tenant's data
	// carries the tenant predicate and the erasure fence.
	sys := db.WithContext(core.WithSystemContext(ctx))
	if age, err := model.OldestActiveBackfillAge(sys, w.now()); err == nil {
		w.metrics.OldestActiveAge.Set(age.Seconds())
	}
	jobs, err := model.RunnableBackfills(sys, maxRunnable)
	if err != nil {
		return fmt.Errorf("listing the active backfill jobs: %w", err)
	}
	var failed int
	var lastErr error
	for i := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.runJob(ctx, db, &jobs[i]); err != nil {
			failed++
			lastErr = err
		}
	}
	return core.PassResult(len(jobs), failed, lastErr)
}

// runJob claims one job and works it for one slice.
func (w *Worker) runJob(ctx context.Context, db *gorm.DB, listed *model.CalculatedBackfill) error {
	tenant := listed.TenantId
	tctx := core.WithTenant(ctx, tenant)
	tdb := db.WithContext(tctx)

	// 🔴 A TENANT BEING ERASED IS NOT CLAIMED. Its job row cannot be updated once the fence
	// stands, so claiming would only fail; the purge sweep deletes the row. The local fence is
	// authoritative and cheap.
	if err := rdb.CheckTenantFence(tctx, tdb, tenant); err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			log.Info().Str("tenant", tenant).Str("job", listed.Token).
				Msg("Backfill job skipped: the tenant is being erased.")
			return nil
		}
		return fmt.Errorf("reading the erasure fence for %q: %w", tenant, err)
	}
	if w.roster == nil {
		log.Warn().Str("tenant", tenant).Str("job", listed.Token).
			Msg("Backfill job left waiting: device-management is not configured, so there is no roster to walk.")
		return nil
	}

	job, err := model.ClaimBackfill(tdb, listed.Token)
	if errors.Is(err, model.ErrBackfillLost) {
		return nil // cancelled or finished between the list and the claim
	}
	if err != nil {
		return fmt.Errorf("claiming backfill %q: %w", listed.Token, err)
	}
	run, err := w.newRun(tdb, job)
	if err != nil {
		// A frozen plan this build refuses cannot become runnable by waiting.
		return w.fail(tdb, job, err.Error())
	}
	logJob(log.Info(), job).Int64("epoch", job.ClaimEpoch).Msg("Backfill job claimed.")
	if err := w.timeout(tdb, statementTimeout); err != nil {
		return err
	}
	return w.work(ctx, run)
}

// work runs the job for one slice and maps what stops it.
func (w *Worker) work(ctx context.Context, run *jobRun) error {
	deadline := w.now().Add(sliceDuration)
	token := run.job.Token
	for ctx.Err() == nil && w.now().Before(deadline) {
		var done bool
		var err error
		switch run.job.Phase {
		case model.BackfillPhaseRefresh:
			done, err = run.refreshNext(ctx)
		default:
			done, err = run.fillBatch(ctx)
		}
		switch {
		case err == nil && done:
			delete(w.failures, token)
			return w.finish(run, model.BackfillSucceeded, "")
		case err == nil:
			delete(w.failures, token)
			delete(w.dmDownSince, token)
			continue
		case errors.Is(err, model.ErrBackfillLost):
			return w.lost(run)
		case errors.Is(err, rdb.ErrTenantPurged):
			logJob(log.Info(), run.job).Msg("Backfill job stopped: the tenant is being erased. Nothing was written.")
			return nil
		case errors.Is(err, context.Canceled) || ctx.Err() != nil:
			return ctx.Err()
		}
		return w.handle(run, err)
	}
	logJob(log.Info(), run.job).Str("phase", run.job.Phase).Msg("Backfill slice ended.")
	return nil
}

// handle decides what an error that stopped the job means: a failure of the job itself, or a
// condition to retry on the next pass.
func (w *Worker) handle(run *jobRun, err error) error {
	token := run.job.Token
	var re *rosterError
	if errors.As(err, &re) {
		since, seen := w.dmDownSince[token]
		if !seen {
			since = w.now()
			w.dmDownSince[token] = since
		}
		if w.now().Sub(since) >= dmDownLimit {
			return w.finish(run, model.BackfillFailed, fmt.Sprintf(
				"device-management has been unreachable for %s: %v", dmDownLimit, re.err))
		}
		return err // RUNNING, and retried next pass
	}
	var fe *failJob
	if errors.As(err, &fe) {
		return w.finish(run, model.BackfillFailed, fe.Error())
	}
	w.failures[token]++
	if w.failures[token] >= consecutiveFailureLimit {
		return w.finish(run, model.BackfillFailed, fmt.Sprintf(
			"failed on %d passes in a row, last: %v", consecutiveFailureLimit, err))
	}
	return err
}

// failJob is an error that ends the job: a retry would meet it again.
type failJob struct{ msg string }

func (e *failJob) Error() string { return e.msg }

// finish ends the job in status. A guarded write that matches nothing means the job is no
// longer this worker's.
func (w *Worker) finish(run *jobRun, status, failure string) error {
	delete(w.failures, run.job.Token)
	delete(w.dmDownSince, run.job.Token)
	err := model.FinishBackfill(run.db, run.job.Token, run.job.ClaimEpoch, status, failure)
	if errors.Is(err, model.ErrBackfillLost) {
		return w.lost(run)
	}
	if err != nil {
		return fmt.Errorf("finishing backfill %q as %s: %w", run.job.Token, status, err)
	}
	w.metrics.JobsFinished.WithLabelValues(status).Inc()
	ev := log.Info()
	if status == model.BackfillFailed {
		ev = log.Error()
	}
	// A FAILED job is recorded on the job row and counted; the pass itself did its work.
	logJob(ev, run.job).Str("status", status).Str("failure", failure).Msg("Backfill job finished.")
	return nil
}

// fail ends a job that never started running.
func (w *Worker) fail(tdb *gorm.DB, job *model.CalculatedBackfill, failure string) error {
	if err := model.FinishBackfill(tdb, job.Token, job.ClaimEpoch, model.BackfillFailed, failure); err != nil {
		if errors.Is(err, model.ErrBackfillLost) {
			return nil
		}
		return err
	}
	w.metrics.JobsFinished.WithLabelValues(model.BackfillFailed).Inc()
	logJob(log.Error(), job).Str("failure", failure).Msg("Backfill job failed before it ran.")
	return nil
}

// lost handles a guarded write that matched no row. If the cancel the job's owner asked for is
// what stopped it, this worker finishes the transition; otherwise the job is another worker's
// (or already over) and this one just stops.
func (w *Worker) lost(run *jobRun) error {
	var now model.CalculatedBackfill
	err := run.db.Where("token = ?", run.job.Token).Take(&now).Error
	if err == nil && now.Status == model.BackfillRunning && now.ClaimEpoch == run.job.ClaimEpoch &&
		now.CancelRequestedAt != nil {
		if err := model.FinishBackfill(run.db, run.job.Token, run.job.ClaimEpoch, model.BackfillCancelled, ""); err == nil {
			w.metrics.JobsFinished.WithLabelValues(model.BackfillCancelled).Inc()
			logJob(log.Info(), run.job).Msg("Backfill job cancelled. Rows already written stay.")
			return nil
		}
	}
	logJob(log.Info(), run.job).Msg("Backfill worker stopped: the job is no longer its own.")
	return nil
}

// timeout sets the session's statement timeout. It is session-wide on the pinned connection and
// is reset when the pass ends.
func (w *Worker) timeout(db *gorm.DB, d time.Duration) error {
	return db.Exec(`SELECT set_config('statement_timeout', ?, false)`, fmt.Sprintf("%d", d.Milliseconds())).Error
}

// jobRun is one claimed job being worked: the compiled plan, the job's counters as of the last
// commit, and the handle everything runs on.
type jobRun struct {
	w   *Worker
	db  *gorm.DB // the pinned session, carrying the job's tenant
	job *model.CalculatedBackfill

	doc      *model.CalculatedPlanDoc
	plan     *calcfield.Plan
	keys     []string        // the selected keys, in the order the job lists them
	selected map[string]bool // the same, as a set
	names    []string        // the measurement names a read selects: measured inputs and selected keys
	skipped  map[string]int64
	filter   []string // the device tokens the job is limited to, if any
	readRows int      // rows per read page and per insert
}

func (w *Worker) newRun(db *gorm.DB, job *model.CalculatedBackfill) (*jobRun, error) {
	doc, err := model.ParseCalculatedPlanDoc(job.Plan)
	if err != nil {
		return nil, err
	}
	plan, err := doc.Compile()
	if err != nil {
		return nil, fmt.Errorf("compiling the frozen plan: %w", err)
	}
	keys, err := decodeStrings(job.MetricKeys)
	if err != nil {
		return nil, fmt.Errorf("reading the job's metric keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, errors.New("the job selects no metric keys")
	}
	// The plan must still run every selected key and everything it reads: a later calcfield
	// that refuses one fails the job naming it, rather than filling a partial chain.
	if _, err := plan.Closure(keys); err != nil {
		return nil, fmt.Errorf("the frozen plan cannot run the selected metrics: %w", err)
	}
	skipped, err := job.SkippedCounts()
	if err != nil {
		return nil, err
	}
	r := &jobRun{w: w, db: db, job: job, doc: doc, plan: plan, keys: keys,
		selected: map[string]bool{}, skipped: skipped,
		readRows: max(1, min(maxChunkRows, w.cfg.RowsPerSecond/2))}
	seen := map[string]bool{}
	for _, k := range keys {
		r.selected[k] = true
	}
	for _, m := range doc.Measured {
		if !seen[m.Key] {
			seen[m.Key] = true
			r.names = append(r.names, m.Key)
		}
	}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			r.names = append(r.names, k)
		}
	}
	if len(job.DeviceFilter) > 0 && string(job.DeviceFilter) != "null" {
		if r.filter, err = decodeStrings(job.DeviceFilter); err != nil {
			return nil, fmt.Errorf("reading the job's device filter: %w", err)
		}
	}
	return r, nil
}
