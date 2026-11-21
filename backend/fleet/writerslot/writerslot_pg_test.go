// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package writerslot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-fleet/internal/pgtest"
	"github.com/devicechain-io/dc-fleet/schema"
)

const (
	clusterA = "11111111-1111-1111-1111-111111111111"
	clusterB = "22222222-2222-2222-2222-222222222222"
	run1     = "aaaaaaaa-0000-0000-0000-000000000001"
	run2     = "aaaaaaaa-0000-0000-0000-000000000002"
	run3     = "aaaaaaaa-0000-0000-0000-000000000003"
	reqID    = "44444444-4444-4444-4444-444444444444"
	nonceA   = "abcdefghijklmnopqrstuvwxyz"
	podA     = "pod-uid-a"
	podB     = "pod-uid-b"
)

type fx struct {
	t   *testing.T
	env *pgtest.Env
	db  *gorm.DB
	mgr *Manager
	ctx context.Context
}

// premiseSet is a Premises with a fixed answer.
type premiseSet map[string]bool

func (p premiseSet) ExpiryHolds(mode string) bool { return p[mode] }

// dropRolesAfter registers the removal of cluster-global roles a test creates. Roles outlive the
// test database, so without this a second test that creates fleet_provider or a run's role fails.
// It is registered BEFORE the database so that, cleanups running last-in-first-out, the roles are
// dropped AFTER the database (and the objects they own) is gone.
func dropRolesAfter(t *testing.T, roles ...string) {
	t.Helper()
	dsn := MustDSNForTest(t)
	t.Cleanup(func() {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			return
		}
		sqlDB, _ := db.DB()
		defer sqlDB.Close()
		for _, r := range roles {
			_ = db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = ?`, r).Error
			_ = db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, r)).Error
		}
	})
}

// MustDSNForTest is the harness DSN.
func MustDSNForTest(t *testing.T) string {
	t.Helper()
	return pgtest.MustDSN(t, os.Getenv)
}

func newFx(t *testing.T, backend string, cfg Config) *fx {
	t.Helper()
	r1, _ := StateRoleName(run1)
	r2, _ := StateRoleName(run2)
	dropRolesAfter(t, r1, r2, "fleet_provider")
	env := pgtest.OpenArea(t, "fleet")
	require.NoError(t, schema.NewWriterExclusionSchema().Migrate(env.Gorm))
	m, err := NewManager(env.Gorm, "manager-a", cfg)
	require.NoError(t, err)
	f := &fx{t: t, env: env, db: env.Gorm, mgr: m, ctx: context.Background()}
	require.NoError(t, m.Take(f.ctx, time.Hour))
	f.identity(clusterA, "dcf-a", nonceA, backend)
	f.observe(clusterA, true)
	return f
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.db.Exec(sql, args...).Error, sql)
}

func (f *fx) identity(uid, name, nonce, backend string) {
	f.t.Helper()
	f.exec(`INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
		ownership_nonce, state_backend, state_ref, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
		VALUES (?, ?, 'managed', 'gke', 'example-project', 'us-central1-a', ?, ?, ?, 'ref', 'retained', 'ACTIVE', now(), now(), now())`,
		uid, reqID, name, nonce, backend)
}

func (f *fx) observe(uid string, complete bool) {
	f.t.Helper()
	require.NoError(f.t, f.mgr.RecordObservation(f.ctx, uid, Observation{Complete: complete, ClusterStatus: "RUNNING"}))
}

func (f *fx) spec(runUID, kind string) RunSpec {
	return RunSpec{ClusterUID: clusterA, Kind: kind, RunUID: runUID, RequestID: reqID, CredentialMode: ModeKindJWKS, CloudIdentityRole: "apply",
		CloudPrincipal: "apply@example", KsaTokenTTL: time.Hour, StsTokenTTL: time.Hour, AccessTokenTTL: time.Hour}
}

func (f *fx) acquire(runUID, kind string) *Run {
	f.t.Helper()
	r, err := f.mgr.Acquire(f.ctx, f.spec(runUID, kind))
	require.NoError(f.t, err)
	return r
}

func (f *fx) slot(uid string) slotRow {
	f.t.Helper()
	var s slotRow
	require.NoError(f.t, f.db.Raw(`SELECT * FROM writer_slots WHERE cluster_uid = ?`, uid).Scan(&s).Error)
	return s
}

func (f *fx) state() string { return f.slot(clusterA).State }

func (f *fx) running(runUID, pod string) {
	f.t.Helper()
	require.NoError(f.t, f.mgr.Observe(f.ctx, runUID, "fleet", "job-"+runUID, pod, "node-1"))
}

func terminated(pod string) PodObservation {
	return PodObservation{PodUID: pod, Containers: []ContainerStatus{{Name: "runner", Terminated: true}}}
}

func (f *fx) transitions(uid string) int {
	var n int
	require.NoError(f.t, f.db.Raw(`SELECT count(*) FROM writer_slot_transitions WHERE cluster_uid = ?`, uid).Scan(&n).Error)
	return n
}

func refused(t *testing.T, err error, want error) {
	t.Helper()
	require.Error(t, err)
	require.True(t, errors.Is(err, want), "got %v, want %v", err, want)
}

// A clean lifecycle: acquire, observe, exit, release.
func TestFleetPGHappyPathAcquireToRelease(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	r := f.acquire(run1, KindCreate)
	require.Equal(t, int64(1), r.Epoch)
	require.Equal(t, StateHeld, f.state())

	// Intent precedes submission: first_submit_at is set in the transaction that takes the slot.
	var first *time.Time
	require.NoError(t, f.db.Raw(`SELECT first_submit_at FROM cloud_identities WHERE cluster_uid = ?`, clusterA).Scan(&first).Error)
	require.NotNil(t, first, "first_submit_at must be recorded when the slot is taken, before any Job exists")

	f.running(run1, podA)
	require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 3}}))
	require.Equal(t, StateHeld, f.state(), "the slot stays HELD after the exit until Release")
	require.NoError(t, f.mgr.Release(f.ctx, run1))
	require.Equal(t, StateFree, f.state())

	// A second run takes epoch 2.
	r2 := f.acquire(run2, KindChange)
	require.Equal(t, int64(2), r2.Epoch)
	require.GreaterOrEqual(t, f.transitions(clusterA), 3)
}

// Two connections acquire concurrently: exactly one wins.
func TestFleetPGConcurrentAcquireExactlyOneWins(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	// The slot row exists already. Without it the first Acquire creates it, and concurrent inserts of
	// one primary key wait for each other, which would serialise the race this test is about.
	f.exec(`INSERT INTO writer_slots (cluster_uid, epoch, state, updated_at) VALUES (?, 0, 'FREE', now())`, clusterA)
	const n = 8
	var wins, refusals atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.mgr.Acquire(f.ctx, f.spec(fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i), KindChange))
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrNotAdmitted):
				refusals.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int64(1), wins.Load(), "exactly one concurrent acquire may win")
	require.Equal(t, int64(n-1), refusals.Load())
}

// Two managers: a second holder cannot transition while the first's lease is live; after takeover
// the first's transactions fail in the database.
func TestFleetPGTwoManagersAreFencedByTheLease(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	require.NoError(t, f.mgr.Take(f.ctx, 1500*time.Millisecond)) // renew with a short lease

	b, err := NewManager(f.db, "manager-b", Config{})
	require.NoError(t, err)
	refused(t, b.Take(f.ctx, time.Hour), ErrLeaseHeld)
	// A second holder that never took the lease cannot transition at all.
	_, err = b.Acquire(f.ctx, f.spec(run1, KindCreate))
	refused(t, err, ErrLeaseLost)

	time.Sleep(1700 * time.Millisecond)
	require.NoError(t, b.Take(f.ctx, time.Hour))
	require.Greater(t, b.LeaseEpoch(), f.mgr.LeaseEpoch(), "a takeover increments the lease epoch")

	// The deposed manager fails in the database, not by convention.
	_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindCreate))
	refused(t, err, ErrLeaseLost)
	refused(t, f.mgr.RecordObservation(f.ctx, clusterA, Observation{Complete: true}), ErrLeaseLost)
	// And the new holder acts.
	_, err = b.Acquire(f.ctx, f.spec(run1, KindCreate))
	require.NoError(t, err)

	// An expired lease with nobody having taken it over is not "ours" either.
	c := newFx(t, BackendGCS, Config{})
	require.NoError(t, c.mgr.Take(c.ctx, time.Second))
	time.Sleep(1300 * time.Millisecond)
	_, err = c.mgr.Acquire(c.ctx, c.spec(run1, KindCreate))
	refused(t, err, ErrLeaseLost)
}

// The lease row is read FOR SHARE: a takeover cannot overtake a transaction that is mid-flight.
func TestFleetPGLeaseIsReadForShare(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	require.NoError(t, f.mgr.Take(f.ctx, 700*time.Millisecond))
	time.Sleep(800 * time.Millisecond) // expired, so a takeover is permitted

	// The deposed-to-be manager's transaction begins... but its lease has expired, so use a
	// fresh long lease for the in-flight transaction and test the share lock directly.
	require.NoError(t, f.mgr.Take(f.ctx, time.Hour))
	inFlight := make(chan struct{})
	release := make(chan struct{})
	f.mgr.afterLease = func() {
		select {
		case inFlight <- struct{}{}:
		default:
		}
		<-release
	}
	done := make(chan error, 1)
	go func() { done <- f.mgr.RecordObservation(f.ctx, clusterA, Observation{Complete: true}) }()
	<-inFlight

	// A takeover needs the row exclusively, which the in-flight FOR SHARE read prevents. Use a
	// session lock_timeout so the attempt fails fast instead of waiting for the release.
	probe := f.env.ConnectArea(t, "fleet")
	err := probe.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout = '300ms'`).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE manager_lease SET holder_id = 'usurper', lease_epoch = lease_epoch + 1 WHERE id = 1`).Error
	})
	require.Error(t, err, "a takeover must not overtake a transaction that holds the lease row FOR SHARE")
	close(release)
	require.NoError(t, <-done)
}

func TestFleetPGAcquireRefusals(t *testing.T) {
	t.Run("every non-FREE state refuses, including one the code does not know", func(t *testing.T) {
		for _, state := range []string{StateHeld, StateUncertain, StateExcluded, StateRecovering, StateQuarantined} {
			f := newFx(t, BackendGCS, Config{})
			f.acquire(run1, KindCreate)
			f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_state_v1`)
			f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_excluded_both_cuts_v1`)
			f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_uncertain_v1`)
			f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_quarantine_has_reason_v1`)
			f.exec(`UPDATE writer_slots SET state = ?`, state)
			_, err := f.mgr.Acquire(f.ctx, f.spec(run2, KindChange))
			refused(t, err, ErrNotAdmitted)
		}
		f := newFx(t, BackendGCS, Config{})
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_state_v1`)
		f.exec(`INSERT INTO writer_slots (cluster_uid, state, updated_at) VALUES (?, 'FUTURE_STATE', now())`, clusterA)
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrUnknownValue)
	})

	t.Run("DESTROY is refused always, in every state", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindDestroy))
		refused(t, err, ErrDestroyNotImplemented)
		// Even where every other rule would admit it, and without having touched the slot.
		require.Equal(t, 0, f.transitions(clusterA))
		f.exec(`UPDATE cloud_identities SET lifecycle = 'TEARING_DOWN'`)
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindDestroy))
		refused(t, err, ErrDestroyNotImplemented)
		var n int
		require.NoError(t, f.db.Raw(`SELECT count(*) FROM runs`).Scan(&n).Error)
		require.Zero(t, n, "a refused DESTROY must leave no run behind")
	})

	t.Run("unsettled operations, including a DONE one that carries an error", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		require.NoError(t, f.mgr.RecordOperation(f.ctx, clusterA, CloudOperation{Name: "op-1", Status: OpRunning, Source: SourceObserverList}))
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)

		require.NoError(t, f.mgr.RecordOperation(f.ctx, clusterA, CloudOperation{Name: "op-1", Status: OpDone, ErrorCode: "13", Source: SourceObserverList}))
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)
		require.Contains(t, err.Error(), "not settled", "a DONE operation with an error is not settled")

		// Only a recorded resolution settles it.
		require.NoError(t, f.mgr.ResolveOperation(f.ctx, clusterA, "op-1", ResolutionReconciled))
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		require.NoError(t, err)
	})

	t.Run("a stale, incomplete or missing observation", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{ObservationFreshness: time.Second})
		f.observe(clusterA, false)
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)

		f.observe(clusterA, true)
		time.Sleep(1200 * time.Millisecond)
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)

		f.exec(`DELETE FROM cluster_observations`)
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)

		f.observe(clusterA, true) // the counterweight: a fresh complete listing admits
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		require.NoError(t, err)
	})

	t.Run("observe-only admits DRIFT_CHECK and nothing else", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.exec(`INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
			VALUES (?, ?, 'observe_only', 'gke', 'p', 'l', 'adopted', 'disposable', 'ACTIVE', now(), now(), now())`, clusterB, reqID)
		f.observe(clusterB, true)
		for _, kind := range []string{KindCreate, KindChange, KindUpgrade, KindRecover} {
			s := f.spec(run1, kind)
			s.ClusterUID = clusterB
			_, err := f.mgr.Acquire(f.ctx, s)
			refused(t, err, ErrNotAdmitted)
		}
		s := f.spec(run2, KindDriftCheck)
		s.ClusterUID = clusterB
		_, err := f.mgr.Acquire(f.ctx, s)
		require.NoError(t, err)
	})

	t.Run("a tombstoned record", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.exec(`UPDATE cloud_identities SET tombstoned_at = now()`)
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)
	})

	t.Run("TEARING_DOWN admits only RECOVER, DESTROYED nothing", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.exec(`UPDATE cloud_identities SET lifecycle = 'TEARING_DOWN'`)
		for _, kind := range []string{KindCreate, KindChange, KindUpgrade, KindDriftCheck} {
			_, err := f.mgr.Acquire(f.ctx, f.spec(run1, kind))
			refused(t, err, ErrNotAdmitted)
		}
		// RECOVER is a lifecycle-permitted kind, but still needs an EXCLUDED slot.
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindRecover))
		refused(t, err, ErrNotAdmitted)
		require.Contains(t, err.Error(), "EXCLUDED")
		f.exec(`UPDATE cloud_identities SET lifecycle = 'DESTROYED'`)
		_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindRecover))
		refused(t, err, ErrNotAdmitted)
	})
}

// Doubt is reversible; exclusion is not.
func TestFleetPGDoubtAndReaffirm(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.acquire(run1, KindCreate)
	f.running(run1, podA)

	require.NoError(t, f.mgr.Doubt(f.ctx, run1, "node NotReady"))
	require.Equal(t, StateUncertain, f.state())
	require.NoError(t, f.mgr.Doubt(f.ctx, run1, "again"), "doubting a doubted slot is a no-op")

	// Not on a different pod UID, nor on a pod that is not Running, nor a node that is not Ready.
	refused(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podB, Running: true, NodeReady: true}), ErrTransitionRefused)
	refused(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podA, Running: false, NodeReady: true}), ErrTransitionRefused)
	refused(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podA, Running: true, NodeReady: false}), ErrTransitionRefused)
	require.Equal(t, StateUncertain, f.state())

	// The same pod UID Running on a Ready node returns it to where it was.
	require.NoError(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podA, Running: true, NodeReady: true}))
	require.Equal(t, StateHeld, f.state())

	// Not after a refresh cut.
	require.NoError(t, f.mgr.Doubt(f.ctx, run1, "again"))
	require.NoError(t, f.mgr.RecordRefreshCut(f.ctx, run1))
	refused(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podA, Running: true, NodeReady: true}), ErrTransitionRefused)
	require.Equal(t, StateUncertain, f.state())
}

// A restarted manager observes first and does not exclude a healthy run.
func TestFleetPGRestartedManagerReaffirmsAndDoesNotExclude(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.acquire(run1, KindCreate)
	f.running(run1, podA)

	healthy := PodObservation{PodUID: podA, Running: true, NodeReady: true}
	require.NoError(t, f.mgr.Resume(f.ctx, run1, &healthy, nil))
	require.Equal(t, StateHeld, f.state(), "a healthy run is left alone")

	// The observation fails: doubt, never exclusion.
	require.NoError(t, f.mgr.Resume(f.ctx, run1, nil, errors.New("api server unreachable")))
	require.Equal(t, StateUncertain, f.state())
	require.Nil(t, f.slot(clusterA).CloudCutProof)

	// It then observes the same pod healthy again, and reaffirms.
	require.NoError(t, f.mgr.Resume(f.ctx, run1, &healthy, nil))
	require.Equal(t, StateHeld, f.state())

	// A different pod is doubt, not exclusion either.
	other := PodObservation{PodUID: podB, Running: true, NodeReady: true}
	require.NoError(t, f.mgr.Resume(f.ctx, run1, &other, nil))
	require.Equal(t, StateUncertain, f.state())
}

func TestFleetPGSecondPodQuarantines(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.acquire(run1, KindCreate)
	f.running(run1, podA)
	f.running(run1, podA) // idempotent
	err := f.mgr.Observe(f.ctx, run1, "fleet", "job-"+run1, podB, "node-2")
	refused(t, err, ErrQuarantined)
	s := f.slot(clusterA)
	require.Equal(t, StateQuarantined, s.State)
	require.Equal(t, QuarantineSecondPod, *s.QuarantineReason)
}

func TestFleetPGExitNeedsEveryContainerTerminated(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.acquire(run1, KindCreate)
	f.running(run1, podA)
	head := StateHead{Lineage: "l1", Serial: 1}
	// Absence, phase and an empty observation are not evidence.
	refused(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: PodObservation{}, Head: head}), ErrTransitionRefused)
	refused(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: PodObservation{PodUID: podA, Running: false}, Head: head}), ErrTransitionRefused)
	refused(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: PodObservation{PodUID: podA, Containers: []ContainerStatus{{Terminated: false}}}, Head: head}), ErrTransitionRefused)
	refused(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podB), Head: head}), ErrTransitionRefused)
	require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Head: head}))
}

// Release needs every operation settled and the per-run state role dropped.
func TestFleetPGReleaseGuards(t *testing.T) {
	f := newFx(t, BackendPG, Config{})
	role, err := StateRoleName(run1)
	require.NoError(t, err)
	s := f.spec(run1, KindCreate)
	s.StateRoleName = role
	_, err = f.mgr.Acquire(f.ctx, s)
	require.NoError(t, err)
	f.running(run1, podA)
	require.NoError(t, f.mgr.CreateStateRole(f.ctx, run1, "postgres", "a-sufficiently-long-password"))
	require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Succeeded: true, Head: StateHead{Lineage: "l", Serial: 1}}))

	refused(t, f.mgr.Release(f.ctx, run1), ErrTransitionRefused) // the role still exists
	require.NoError(t, f.mgr.DropStateRole(f.ctx, run1))

	// An operation still running blocks the release; an errored DONE one quarantines.
	require.NoError(t, f.mgr.RecordOperation(f.ctx, clusterA, CloudOperation{Name: "op-x", Status: OpRunning, Source: SourceRunnerLog, RunUID: run1}))
	refused(t, f.mgr.Release(f.ctx, run1), ErrTransitionRefused)
	require.NoError(t, f.mgr.RecordOperation(f.ctx, clusterA, CloudOperation{Name: "op-x", Status: OpDone, ErrorCode: "9", Source: SourceRunnerLog, RunUID: run1}))
	refused(t, f.mgr.Release(f.ctx, run1), ErrQuarantined)
	require.Equal(t, QuarantineUnsettledOperation, *f.slot(clusterA).QuarantineReason)
}

// ---- proofs ---------------------------------------------------------------------------------

func (f *fx) uncertainRun(runUID, pod string, mutate func(*RunSpec)) {
	f.t.Helper()
	s := f.spec(runUID, KindCreate)
	if mutate != nil {
		mutate(&s)
	}
	_, err := f.mgr.Acquire(f.ctx, s)
	require.NoError(f.t, err)
	f.running(runUID, pod)
	require.NoError(f.t, f.mgr.Doubt(f.ctx, runUID, "lost"))
}

func TestFleetPGExclusionNeedsBothCuts(t *testing.T) {
	t.Run("TERMINATION: every container terminated, state cut PROCESS_TERMINATED", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.uncertainRun(run1, podA, nil)
		// Pod absence, an empty list, a still-running container, another pod: none is proof.
		for name, pod := range map[string]PodObservation{
			"absent":  {},
			"running": {PodUID: podA, Containers: []ContainerStatus{{Terminated: false}}},
			"other":   terminated(podB),
		} {
			err := f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: pod}, State: StateCut{Method: CutProcessTerminated}})
			refused(t, err, ErrProofRefused)
			require.Equal(t, StateUncertain, f.state(), name)
		}
		// The cloud cut alone is not enough: TERMINATION implies the process is dead, so the only
		// state cut that fits is PROCESS_TERMINATED.
		err := f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutCloudCredential}})
		refused(t, err, ErrProofRefused)
		require.NoError(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutProcessTerminated}}))
		s := f.slot(clusterA)
		require.Equal(t, StateExcluded, s.State)
		require.Equal(t, ProofTermination, *s.CloudCutProof)
		require.Equal(t, CutProcessTerminated, *s.StateCutMethod)
		require.NotNil(t, s.CloudCutAt)
		require.NotNil(t, s.StateCutAt)
	})

	t.Run("EXPIRY: refresh cut, the residual on the database clock, a measured credential mode", func(t *testing.T) {
		ttl := func(s *RunSpec) {
			s.KsaTokenTTL, s.StsTokenTTL, s.AccessTokenTTL = time.Second, time.Second, time.Second
		}
		cfg := Config{Premises: premiseSet{ModeKindJWKS: true}, ClockSkewMargin: 100 * time.Millisecond}
		f := newFx(t, BackendGCS, cfg)
		f.uncertainRun(run1, podA, ttl)
		proof := Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutCloudCredential}}

		// No refresh cut recorded.
		refused(t, f.mgr.Prove(f.ctx, run1, proof), ErrProofRefused)
		require.NoError(t, f.mgr.RecordRefreshCut(f.ctx, run1))
		// Before R = ksa + sts + access + skew on the database clock.
		err := f.mgr.Prove(f.ctx, run1, proof)
		refused(t, err, ErrProofRefused)
		require.Contains(t, err.Error(), "residual")
		// Dropping ONE of the three lifetimes would make 2.1 s enough: it must not be.
		f.exec(`UPDATE writer_slots SET refresh_cut_at = now() - interval '2200 milliseconds'`)
		refused(t, f.mgr.Prove(f.ctx, run1, proof), ErrProofRefused)
		// Past the full residual it is accepted.
		f.exec(`UPDATE writer_slots SET refresh_cut_at = now() - interval '3500 milliseconds'`)
		require.NoError(t, f.mgr.Prove(f.ctx, run1, proof))
		require.Equal(t, StateExcluded, f.state())
	})

	t.Run("EXPIRY is refused in a credential mode without a measured premise", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{Premises: premiseSet{ModeKindJWKS: true}, ClockSkewMargin: 10 * time.Millisecond})
		f.uncertainRun(run1, podA, func(s *RunSpec) {
			s.CredentialMode = ModeGKEWI
			s.KsaTokenTTL, s.StsTokenTTL, s.AccessTokenTTL = time.Second, time.Second, time.Second
		})
		require.NoError(t, f.mgr.RecordRefreshCut(f.ctx, run1))
		f.exec(`UPDATE writer_slots SET refresh_cut_at = now() - interval '1 hour'`)
		err := f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutCloudCredential}})
		refused(t, err, ErrProofRefused)
		require.Contains(t, err.Error(), ModeGKEWI)

		// The default premises (no argument at all) refuse everything.
		g := newFx(t, BackendGCS, Config{})
		g.uncertainRun(run1, podA, nil)
		require.NoError(t, g.mgr.RecordRefreshCut(g.ctx, run1))
		g.exec(`UPDATE writer_slots SET refresh_cut_at = now() - interval '1 day'`)
		refused(t, g.mgr.Prove(g.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutCloudCredential}}), ErrProofRefused)
	})

	t.Run("EXPIRY on a pg backend needs PG_ROLE_REVOKED, and a state cut the process cannot imply", func(t *testing.T) {
		cfg := Config{Premises: premiseSet{ModeKindJWKS: true}, ClockSkewMargin: 10 * time.Millisecond}
		f := newFx(t, BackendPG, cfg)
		role, _ := StateRoleName(run1)
		f.uncertainRun(run1, podA, func(s *RunSpec) {
			s.StateRoleName = role
			s.KsaTokenTTL, s.StsTokenTTL, s.AccessTokenTTL = time.Second, time.Second, time.Second
		})
		require.NoError(t, f.mgr.RecordRefreshCut(f.ctx, run1))
		f.exec(`UPDATE writer_slots SET refresh_cut_at = now() - interval '1 hour'`)
		// The cloud credential does not fence a Postgres state write.
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutCloudCredential}}), ErrProofRefused)
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutProcessTerminated}}), ErrProofRefused)
		// PG_ROLE_REVOKED but the role was never cut: it does not exist / can still log in.
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutPGRoleRevoked}}), ErrProofRefused)
		require.NoError(t, f.mgr.CreateStateRole(f.ctx, run1, "postgres", "a-sufficiently-long-password"))
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutPGRoleRevoked}}), ErrProofRefused) // can still log in
		require.NoError(t, f.mgr.RevokeStateRole(f.ctx, run1))
		require.NoError(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofExpiry}, State: StateCut{Method: CutPGRoleRevoked}}))
		require.Equal(t, StateExcluded, f.state())
	})

	t.Run("PG_ROLE_REVOKED on a gcs backend, and CLOUD_CREDENTIAL on a pg one, are refused", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.uncertainRun(run1, podA, nil)
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutPGRoleRevoked}}), ErrProofRefused)
	})

	t.Run("the database refuses an EXCLUDED slot holding one cut", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.uncertainRun(run1, podA, nil)
		err := f.db.Exec(`UPDATE writer_slots SET state = 'EXCLUDED', cloud_cut_proof = 'TERMINATION', cloud_cut_at = now(), state_cut_method = 'PROCESS_TERMINATED'`).Error
		require.Error(t, err)
		require.Contains(t, err.Error(), "ck_writer_slots_excluded_both_cuts_v1")
	})
}

// Quarantine leaves only through proof; nothing waives it.
func TestFleetPGQuarantineLeavesOnlyThroughProof(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.uncertainRun(run1, podA, nil)
	require.NoError(t, f.mgr.Quarantine(f.ctx, clusterA, QuarantineNoProof, Actor{}))
	require.Equal(t, StateQuarantined, f.state())

	// Every way out that is not proof is closed.
	_, err := f.mgr.Acquire(f.ctx, f.spec(run2, KindChange))
	refused(t, err, ErrNotAdmitted)
	refused(t, f.mgr.Reaffirm(f.ctx, run1, PodObservation{PodUID: podA, Running: true, NodeReady: true}), ErrTransitionRefused)
	refused(t, f.mgr.Release(f.ctx, run1), ErrTransitionRefused)
	require.NoError(t, f.mgr.RecordContainment(f.ctx, run1, Actor{ID: "operator-1", Issuer: "idp"}))
	require.Equal(t, StateQuarantined, f.state(), "containment is not a proof: the state does not move")
	require.NotNil(t, f.slot(clusterA).Containment)

	// The same proof checks apply.
	refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: PodObservation{}}, State: StateCut{Method: CutProcessTerminated}}), ErrProofRefused)
	require.NoError(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutProcessTerminated}, Actor: Actor{ID: "operator-1", Issuer: "idp"}}))
	require.Equal(t, StateExcluded, f.state())
	var by string
	require.NoError(t, f.db.Raw(`SELECT actor_id FROM writer_slot_transitions WHERE to_state = 'EXCLUDED'`).Scan(&by).Error)
	require.Equal(t, "operator-1", by, "an operator's transition is attributed to the operator's opaque id")
}

// ---- recovery -------------------------------------------------------------------------------

func (f *fx) excluded(runUID, pod string) {
	f.t.Helper()
	f.uncertainRun(runUID, pod, nil)
	require.NoError(f.t, f.mgr.Prove(f.ctx, runUID, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(pod)}, State: StateCut{Method: CutProcessTerminated}}))
}

func TestFleetPGRecovery(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	// RECOVER is admitted only from EXCLUDED.
	_, err := f.mgr.Acquire(f.ctx, f.spec(run2, KindRecover))
	refused(t, err, ErrNotAdmitted)

	f.excluded(run1, podA)
	// A normal run is not admitted from EXCLUDED either.
	_, err = f.mgr.Acquire(f.ctx, f.spec(run2, KindChange))
	refused(t, err, ErrNotAdmitted)

	r := f.acquire(run2, KindRecover)
	require.Equal(t, StateRecovering, f.state())
	require.Equal(t, int64(2), r.Epoch)
	s := f.slot(clusterA)
	require.Nil(t, s.CloudCutProof, "the excluded run's proof does not carry onto the recovery holder")

	// A lost recovery run goes through doubt like any other.
	f.running(run2, "recovery-pod")
	require.NoError(t, f.mgr.Doubt(f.ctx, run2, "recovery pod lost"))
	require.Equal(t, StateUncertain, f.state())
	require.NoError(t, f.mgr.Reaffirm(f.ctx, run2, PodObservation{PodUID: "recovery-pod", Running: true, NodeReady: true}))
	require.Equal(t, StateRecovering, f.state(), "reaffirmation returns a doubted recovery to RECOVERING")

	// A finishing recovery needs the lock resolved: a foreign holder quarantines, never unlocks.
	require.NoError(t, f.mgr.Exit(f.ctx, run2, ExitSpec{Pod: terminated("recovery-pod"), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 5}}))
	refused(t, f.mgr.FinishRecovery(f.ctx, run2, LockResolution("")), ErrUnknownValue)
	refused(t, f.mgr.FinishRecovery(f.ctx, run2, LockForeign), ErrQuarantined)
	require.Equal(t, QuarantineForeignLockHolder, *f.slot(clusterA).QuarantineReason)
}

func TestFleetPGRecoveryFinishesToFree(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.excluded(run1, podA)
	f.acquire(run2, KindRecover)
	f.running(run2, "recovery-pod")
	// A recovery run that recorded no state head cannot be judged consistent.
	require.NoError(t, f.mgr.Exit(f.ctx, run2, ExitSpec{Pod: terminated("recovery-pod"), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 5}}))
	require.NoError(t, f.mgr.FinishRecovery(f.ctx, run2, LockAbsent))
	require.Equal(t, StateFree, f.state())
	var holder *string
	require.NoError(t, f.db.Raw(`SELECT holder_run_uid FROM writer_slots`).Scan(&holder).Error)
	require.Nil(t, holder)
}

func TestFleetPGStateHeadRegressionQuarantines(t *testing.T) {
	for name, head := range map[string]StateHead{
		"lower serial":     {Lineage: "l1", Serial: 4},
		"changed lineage":  {Lineage: "other", Serial: 9},
		"lineage and fall": {Lineage: "other", Serial: 1},
	} {
		f := newFx(t, BackendGCS, Config{})
		f.acquire(run1, KindCreate)
		f.running(run1, podA)
		require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 5}}), name)
		require.NoError(t, f.mgr.Release(f.ctx, run1), name)

		f.acquire(run2, KindChange)
		f.running(run2, podB)
		err := f.mgr.Exit(f.ctx, run2, ExitSpec{Pod: terminated(podB), Succeeded: true, Head: head})
		refused(t, err, ErrQuarantined)
		require.Equal(t, QuarantineStateHeadMismatch, *f.slot(clusterA).QuarantineReason, name)
	}

	// The counterweight: an equal or higher serial of the same lineage records cleanly.
	f := newFx(t, BackendGCS, Config{})
	f.acquire(run1, KindCreate)
	f.running(run1, podA)
	require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 5}}))
	require.NoError(t, f.mgr.Release(f.ctx, run1))
	f.acquire(run2, KindChange)
	f.running(run2, podB)
	require.NoError(t, f.mgr.Exit(f.ctx, run2, ExitSpec{Pod: terminated(podB), Succeeded: true, Head: StateHead{Lineage: "l1", Serial: 5}}))

	// Only a recorded operator reconciliation moves a head backwards, and it names the operator.
	require.NoError(t, f.mgr.Release(f.ctx, run2))
	refused2 := f.mgr.ReconcileStateHead(f.ctx, clusterA, StateHead{Lineage: "l2", Serial: 1}, Actor{}, "restore")
	require.Error(t, refused2, "an unattributed reconciliation is refused")
	require.NoError(t, f.mgr.ReconcileStateHead(f.ctx, clusterA, StateHead{Lineage: "l2", Serial: 1}, Actor{ID: "operator-1", Issuer: "idp"}, "restore from backup"))
}

// ---- values the code does not know ----------------------------------------------------------

func TestFleetPGUnknownValuesRefuseOnEveryReleasePath(t *testing.T) {
	dropChecks := func(f *fx) {
		f.exec(`ALTER TABLE runs DROP CONSTRAINT ck_runs_phase_v1`)
		f.exec(`ALTER TABLE runs DROP CONSTRAINT ck_runs_kind_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_cloud_cut_proof_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_state_cut_method_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_state_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_excluded_both_cuts_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_expiry_v1`)
		f.exec(`ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_termination_v1`)
		f.exec(`ALTER TABLE cloud_operations DROP CONSTRAINT ck_cloud_operations_status_v1`)
	}

	t.Run("an unknown run phase refuses Release", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.acquire(run1, KindCreate)
		f.running(run1, podA)
		require.NoError(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Succeeded: true, Head: StateHead{Lineage: "l", Serial: 1}}))
		dropChecks(f)
		f.exec(`UPDATE runs SET phase = 'FUTURE_PHASE'`)
		refused(t, f.mgr.Release(f.ctx, run1), ErrUnknownValue)
		require.Equal(t, StateHeld, f.state())
	})
	t.Run("an unknown run kind refuses Exit", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.acquire(run1, KindCreate)
		f.running(run1, podA)
		dropChecks(f)
		f.exec(`UPDATE runs SET kind = 'FUTURE_KIND'`)
		refused(t, f.mgr.Exit(f.ctx, run1, ExitSpec{Pod: terminated(podA), Head: StateHead{Lineage: "l", Serial: 1}}), ErrUnknownValue)
	})
	t.Run("an unknown proof or state-cut value in the slot refuses Prove", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.uncertainRun(run1, podA, nil)
		dropChecks(f)
		f.exec(`UPDATE writer_slots SET cloud_cut_proof = 'WAIVER'`)
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutProcessTerminated}}), ErrUnknownValue)
		f.exec(`UPDATE writer_slots SET cloud_cut_proof = NULL, state_cut_method = 'FUTURE'`)
		refused(t, f.mgr.Prove(f.ctx, run1, Proof{Cloud: CloudCut{Proof: ProofTermination, Pod: terminated(podA)}, State: StateCut{Method: CutProcessTerminated}}), ErrUnknownValue)
	})
	t.Run("an unknown slot state refuses Doubt, Release and Quarantine", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.acquire(run1, KindCreate)
		dropChecks(f)
		f.exec(`UPDATE writer_slots SET state = 'FUTURE_STATE'`)
		refused(t, f.mgr.Doubt(f.ctx, run1, "x"), ErrUnknownValue)
		refused(t, f.mgr.Release(f.ctx, run1), ErrUnknownValue)
		refused(t, f.mgr.Quarantine(f.ctx, clusterA, QuarantineNoProof, Actor{}), ErrUnknownValue)
	})
	t.Run("an unknown operation status or resolution is not treated as settled", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		dropChecks(f)
		f.exec(`ALTER TABLE cloud_operations DROP CONSTRAINT ck_cloud_operations_resolution_v1`)
		f.exec(`INSERT INTO cloud_operations (cluster_uid, operation_name, status, source, first_seen_at, last_seen_at) VALUES (?, 'op', 'FUTURE_STATUS', 'RUNNER_LOG', now(), now())`, clusterA)
		_, err := f.mgr.Acquire(f.ctx, f.spec(run1, KindChange))
		refused(t, err, ErrNotAdmitted)
		f.exec(`UPDATE cloud_operations SET status = 'DONE', error_code = '1', resolution = 'FUTURE_RESOLUTION'`)
		// A resolution is a recorded fact; an unknown one still reads as "has a resolution" in SQL,
		// so the manager must not be the one to widen what counts. It stays in the vocabulary check.
		require.Error(t, f.mgr.ResolveOperation(f.ctx, clusterA, "op", "FUTURE_RESOLUTION"))
	})
}

// ---- lock order -----------------------------------------------------------------------------

// The Observer's write path and the slot's transitions run concurrently on the same cluster, many
// times, with deadlock detection armed (the server's own). Any 40P01 fails.
func TestFleetPGLockOrderHoldsUnderConcurrentObserverAndTransitions(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	f.exec(`SET deadlock_timeout = '200ms'`)
	deadline := time.Now().Add(6 * time.Second)
	var wg sync.WaitGroup
	var deadlocks atomic.Int64
	check := func(err error) {
		if err != nil && strings.Contains(err.Error(), "40P01") {
			deadlocks.Add(1)
		}
	}
	for i := 0; i < 3; i++ { // the Observer
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; time.Now().Before(deadline); n++ {
				check(f.mgr.RecordObservation(f.ctx, clusterA, Observation{Complete: true}))
				check(f.mgr.RecordOperation(f.ctx, clusterA, CloudOperation{Name: fmt.Sprintf("op-%d-%d", i, n%5), Status: OpDone, Source: SourceObserverList}))
			}
		}(i)
	}
	for i := 0; i < 3; i++ { // the slot
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; time.Now().Before(deadline); n++ {
				uid := fmt.Sprintf("cccccccc-%04d-0000-0000-%012d", i, n)
				_, err := f.mgr.Acquire(f.ctx, f.spec(uid, KindChange))
				check(err)
				if err == nil {
					check(f.mgr.Observe(f.ctx, uid, "fleet", "job-"+uid, "pod-"+uid, "n"))
					check(f.mgr.Exit(f.ctx, uid, ExitSpec{Pod: terminated("pod-" + uid), Succeeded: true, Head: StateHead{Lineage: "l", Serial: int64(n + 1)}}))
					check(f.mgr.Release(f.ctx, uid))
				}
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, deadlocks.Load(), "the lock order must hold under an opposing-path workload")
	var runs int
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM runs`).Scan(&runs).Error)
	require.Positive(t, runs, "the workload must have done work for the absence of deadlocks to mean anything")
}

// ---- the state cut is real ------------------------------------------------------------------

func (f *fx) asRole(role, password string) (*gorm.DB, error) {
	u, err := url.Parse(f.env.DSN)
	require.NoError(f.t, err)
	u.User = url.UserPassword(role, password)
	q := u.Query()
	q.Set("search_path", "fleet,public")
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, err
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	f.t.Cleanup(func() { _ = sqlDB.Close() })
	return db, db.Exec(`SELECT 1`).Error
}

func TestFleetPGStateCutIsReal(t *testing.T) {
	setup := func(t *testing.T) (*fx, string, *gorm.DB) {
		f := newFx(t, BackendPG, Config{})
		// The provider role owns the state schema; the per-run role is a member and owns nothing.
		f.exec(`CREATE ROLE fleet_provider NOLOGIN`)
		f.exec(`CREATE SCHEMA tf_x AUTHORIZATION fleet_provider`)
		f.exec(`CREATE TABLE tf_x.states (name text primary key, data text)`)
		f.exec(`ALTER TABLE tf_x.states OWNER TO fleet_provider`)
		f.exec(`GRANT USAGE ON SCHEMA fleet TO fleet_provider`)
		role, err := StateRoleName(run1)
		require.NoError(t, err)
		const pw = "a-sufficiently-long-password"
		require.NoError(t, f.mgr.CreateStateRole(f.ctx, run1, "fleet_provider", pw))
		db, err := f.asRole(role, pw)
		require.NoError(t, err, "the per-run role must be able to log in before the cut")
		require.NoError(t, db.Exec(`INSERT INTO tf_x.states (name, data) VALUES ('s', 'v1')`).Error)
		return f, pw, db
	}

	t.Run("after the cut a live session is terminated, cannot reconnect, cannot write", func(t *testing.T) {
		f, pw, db := setup(t)
		role, _ := StateRoleName(run1)
		require.NoError(t, f.mgr.RevokeStateRole(f.ctx, run1))

		// The live session is gone: its next statement fails.
		require.Error(t, db.Exec(`UPDATE tf_x.states SET data = 'overwritten' WHERE name = 's'`).Error)
		// It cannot reconnect.
		_, err := f.asRole(role, pw)
		require.Error(t, err)
		// And the state is untouched.
		var data string
		require.NoError(t, f.db.Raw(`SELECT data FROM tf_x.states WHERE name = 's'`).Scan(&data).Error)
		require.Equal(t, "v1", data)
		var left int
		require.NoError(t, f.db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE usename = ?`, role).Scan(&left).Error)
		require.Zero(t, left)
	})

	t.Run("control: without the cut the same session writes", func(t *testing.T) {
		f, _, db := setup(t)
		require.NoError(t, db.Exec(`UPDATE tf_x.states SET data = 'overwritten' WHERE name = 's'`).Error)
		var data string
		require.NoError(t, f.db.Raw(`SELECT data FROM tf_x.states WHERE name = 's'`).Scan(&data).Error)
		require.Equal(t, "overwritten", data, "the negative control: an uncut role CAN overwrite state, so the cut above is what stopped it")
	})

	t.Run("the role is dropped at release", func(t *testing.T) {
		f, _, _ := setup(t)
		require.NoError(t, f.mgr.RevokeStateRole(f.ctx, run1))
		require.NoError(t, f.mgr.DropStateRole(f.ctx, run1))
		var n int
		role, _ := StateRoleName(run1)
		require.NoError(t, f.db.Raw(`SELECT count(*) FROM pg_roles WHERE rolname = ?`, role).Scan(&n).Error)
		require.Zero(t, n)
	})
}

// ---- recovery import decisions --------------------------------------------------------------

func TestFleetPGIdentityCheckDecisions(t *testing.T) {
	newRecovery := func(t *testing.T) *fx {
		f := newFx(t, BackendGCS, Config{})
		f.excluded(run1, podA)
		f.acquire(run2, KindRecover)
		return f
	}
	var first time.Time
	observed := func(f *fx) ObservedCluster {
		require.NoError(t, f.db.Raw(`SELECT first_submit_at FROM cloud_identities`).Scan(&first).Error)
		return ObservedCluster{Project: "example-project", Location: "us-central1-a", Name: "dcf-a", NonceLabel: nonceA, CreateTime: first.Add(time.Minute), ResourceID: "id-1"}
	}
	declared := []string{"database", "services"}

	t.Run("all elements match: IMPORTED, the id is recorded once", func(t *testing.T) {
		f := newRecovery(t)
		d, err := f.mgr.RecordClusterImport(f.ctx, run2, "google_container_cluster.this", observed(f), Actor{}, reqID)
		require.NoError(t, err)
		require.Equal(t, ClusterImportDecision{Decision: DecisionImported, Reason: ReasonVerified}, d)
		var id, via *string
		require.NoError(t, f.db.Raw(`SELECT cloud_resource_id FROM cloud_identities`).Scan(&id).Error)
		require.NoError(t, f.db.Raw(`SELECT recovered_via FROM cloud_identities`).Scan(&via).Error)
		require.Equal(t, "id-1", *id)
		require.Equal(t, "import", *via)

		// A second IMPORTED decision for the same address is refused by the database.
		_, err = f.mgr.RecordClusterImport(f.ctx, run2, "google_container_cluster.this", observed(f), Actor{}, reqID)
		require.Error(t, err)

		// A pool of the verified cluster, declared: imported. Other parents and strangers: refused.
		pd, err := f.mgr.RecordPoolImport(f.ctx, run2, `google_container_node_pool.this["database"]`, "database", "id-1", declared, Actor{}, reqID)
		require.NoError(t, err)
		require.Equal(t, DecisionImported, pd.Decision)
		pd, err = f.mgr.RecordPoolImport(f.ctx, run2, `google_container_node_pool.this["services"]`, "services", "id-OTHER", declared, Actor{}, reqID)
		require.NoError(t, err)
		require.Equal(t, ReasonPoolParentNotVerified, pd.Reason)
		pd, err = f.mgr.RecordPoolImport(f.ctx, run2, `google_container_node_pool.this["stranger"]`, "stranger", "id-1", declared, Actor{}, reqID)
		require.NoError(t, err)
		require.Equal(t, ReasonPoolNotDeclared, pd.Reason)
	})

	t.Run("each single-element mismatch is refused with its own reason, and recorded", func(t *testing.T) {
		for want, mutate := range map[string]func(*ObservedCluster){
			ReasonNameMismatch:     func(o *ObservedCluster) { o.Name = "dcf-b" },
			ReasonProjectMismatch:  func(o *ObservedCluster) { o.Project = "other" },
			ReasonLocationMismatch: func(o *ObservedCluster) { o.Location = "elsewhere" },
			ReasonNonceMismatch:    func(o *ObservedCluster) { o.NonceLabel = "wrongwrongwrongwrongwrong" },
		} {
			f := newRecovery(t)
			o := observed(f)
			mutate(&o)
			d, err := f.mgr.RecordClusterImport(f.ctx, run2, "google_container_cluster.this", o, Actor{}, reqID)
			require.NoError(t, err)
			require.Equal(t, ClusterImportDecision{Decision: DecisionRefused, Reason: want}, d)
			var n int
			require.NoError(t, f.db.Raw(`SELECT count(*) FROM import_records WHERE decision = 'REFUSED' AND reason = ?`, want).Scan(&n).Error)
			require.Equal(t, 1, n)
			var id *string
			require.NoError(t, f.db.Raw(`SELECT cloud_resource_id FROM cloud_identities`).Scan(&id).Error)
			require.Nil(t, id, "a refused import records no resource id")
		}
	})

	t.Run("a create time before the intent quarantines; so does a different id", func(t *testing.T) {
		f := newRecovery(t)
		o := observed(f)
		o.CreateTime = first.Add(-time.Hour)
		d, err := f.mgr.RecordClusterImport(f.ctx, run2, "google_container_cluster.this", o, Actor{}, reqID)
		refused(t, err, ErrQuarantined)
		require.Equal(t, ReasonCreateBeforeIntent, d.Reason)
		require.Equal(t, QuarantineCreateBeforeIntent, *f.slot(clusterA).QuarantineReason)

		g := newRecovery(t)
		_, err = g.mgr.RecordClusterImport(g.ctx, run2, "google_container_cluster.this", observed(g), Actor{}, reqID)
		require.NoError(t, err)
		g.exec(`UPDATE writer_slots SET state = 'RECOVERING'`)
		o2 := observed(g)
		o2.ResourceID = "id-2"
		d, err = g.mgr.RecordClusterImport(g.ctx, run2, "google_container_cluster.other", o2, Actor{}, reqID)
		refused(t, err, ErrQuarantined)
		require.Equal(t, ReasonIdentityMismatch, d.Reason)
		require.Equal(t, QuarantineIdentityMismatch, *g.slot(clusterA).QuarantineReason)
	})

	t.Run("an import is decided only in a RECOVER run, and executing one is not implemented", func(t *testing.T) {
		f := newFx(t, BackendGCS, Config{})
		f.acquire(run1, KindChange)
		_, err := f.mgr.RecordClusterImport(f.ctx, run1, "google_container_cluster.this", ObservedCluster{}, Actor{}, reqID)
		refused(t, err, ErrTransitionRefused)
		refused(t, f.mgr.ExecuteImport(f.ctx, run1, "google_container_cluster.this"), ErrImportNotImplemented)
	})
}

// A second Manager instance with the SAME holder id but an older lease epoch is a deposed manager
// too: the holder id alone does not identify the lease, the epoch does.
func TestFleetPGStaleEpochOfTheSameHolderFails(t *testing.T) {
	f := newFx(t, BackendGCS, Config{})
	stale, err := NewManager(f.db, "manager-a", Config{})
	require.NoError(t, err)
	require.NoError(t, stale.Take(f.ctx, 300*time.Millisecond))
	time.Sleep(450 * time.Millisecond) // the lease lapses
	require.NoError(t, f.mgr.Take(f.ctx, time.Hour))
	require.Greater(t, f.mgr.LeaseEpoch(), stale.LeaseEpoch(), "re-taking an expired lease must start a new epoch even for the same holder")

	_, err = stale.Acquire(f.ctx, f.spec(run1, KindCreate))
	refused(t, err, ErrLeaseLost)
	_, err = f.mgr.Acquire(f.ctx, f.spec(run1, KindCreate))
	require.NoError(t, err)
}
