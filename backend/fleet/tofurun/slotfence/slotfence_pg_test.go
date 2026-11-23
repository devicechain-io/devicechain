// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package slotfence

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-fleet/internal/pgtest"
	"github.com/devicechain-io/dc-fleet/internal/tofutest"
	"github.com/devicechain-io/dc-fleet/schema"
	"github.com/devicechain-io/dc-fleet/tofurun"
	"github.com/devicechain-io/dc-fleet/writerslot"
)

const (
	clusterA = "11111111-1111-1111-1111-111111111111"
	reqID    = "44444444-4444-4444-4444-444444444444"
	nonce    = "abcdefghijklmnopqrstuvwxyz"
	runPW    = "a-sufficiently-long-password"
)

func runUID(n int) string {
	return "bbbbbbbb-0000-0000-0000-" + strings.Repeat("0", 11) + string(rune('0'+n))
}

// pods stands in for the Kubernetes half: every run has a Job and one pod, and its containers have
// terminated by the time the runner reports its exit.
type pods struct{}

func (pods) Running(_ context.Context, run string) (string, string, string, string, error) {
	return "fleet", "job-" + run, "pod-" + run, "node-1", nil
}

func (pods) Exited(_ context.Context, _, pod string) (writerslot.PodObservation, error) {
	return writerslot.PodObservation{PodUID: pod, Containers: []writerslot.ContainerStatus{{Name: "runner", Terminated: true}}}, nil
}

// roleBackend is the pg backend as the manager provisions it: the per-run login role is created
// only once the slot admitted the run, a member of the provider role that owns the state schema.
type roleBackend struct {
	t   testing.TB
	mgr *writerslot.Manager
	env *pgtest.Env
}

func (roleBackend) Kind() tofurun.BackendKind { return tofurun.BackendPG }

func (b roleBackend) Config(ctx context.Context, run tofurun.RunInfo) (tofurun.BackendConfig, error) {
	if err := b.mgr.CreateStateRole(ctx, run.RunUID, "fleet_provider", runPW); err != nil {
		return tofurun.BackendConfig{}, err
	}
	role, err := writerslot.StateRoleName(run.RunUID)
	if err != nil {
		return tofurun.BackendConfig{}, err
	}
	return tofurun.PGBackend{ConnStr: tofutest.ConnString(b.t, b.env.DSN, role, runPW), Schema: "tf_state"}.Config(ctx, run)
}

type fx struct {
	t      *testing.T
	env    *pgtest.Env
	mgr    *writerslot.Manager
	fence  *Fence
	runner *tofurun.Runner
	work   string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	// The role names are per-run and cluster-global: remove them after the test database is gone.
	var names []string
	for i := 1; i <= 8; i++ {
		n, _ := writerslot.StateRoleName(runUID(i))
		names = append(names, n)
	}
	tofutest.DropRolesAfter(t, pgtest.MustDSN(t, os.Getenv), append(names, "fleet_provider")...)

	env := pgtest.OpenArea(t, "fleet")
	require.NoError(t, schema.NewWriterExclusionSchema().Migrate(env.Gorm))
	mgr, err := writerslot.NewManager(env.Gorm, "manager-a", writerslot.Config{})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.Take(ctx, time.Hour))

	require.NoError(t, env.Gorm.Exec(`CREATE ROLE fleet_provider NOLOGIN`).Error)
	tofutest.PrepareStateSchema(t, env.Gorm, "tf_state", "fleet_provider")
	require.NoError(t, env.Gorm.Exec(`INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
		ownership_nonce, state_backend, state_ref, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
		VALUES (?, ?, 'managed', 'gke', 'example-project', 'us-central1-a', 'dcf-a', ?, 'pg', 'ref', 'retained', 'ACTIVE', now(), now(), now())`,
		clusterA, reqID, nonce).Error)
	require.NoError(t, mgr.RecordObservation(ctx, clusterA, writerslot.Observation{Complete: true, ClusterStatus: "RUNNING"}))

	fence, err := New(mgr, Config{
		CredentialMode: writerslot.ModeKindJWKS, KsaTokenTTL: time.Hour, StsTokenTTL: time.Hour, AccessTokenTTL: time.Hour, Pods: pods{},
	})
	require.NoError(t, err)

	apply, err := tofurun.NewCredentialAllowing(tofurun.ClassApply, "fleet-apply@example", map[string]string{"FLEET_FAKE_CLOUD_TOKEN": "apply-token"}, "FLEET_FAKE_")
	require.NoError(t, err)
	destroy, err := tofurun.NewCredentialAllowing(tofurun.ClassDestroy, "fleet-destroy@example", map[string]string{"FLEET_FAKE_CLOUD_TOKEN": "destroy-token"}, "FLEET_FAKE_")
	require.NoError(t, err)

	work := t.TempDir()
	bin := tofutest.Tofu(t)
	runner, err := tofurun.New(ctx, tofurun.Config{
		Binary: bin, ExpectedVersion: version(t, bin), WorkRoot: work, Fence: fence, Encryption: tofutest.Encryption,
		Gate:        tofurun.Gate{AllowedTypes: map[string]bool{"terraform_data": true}},
		Credentials: creds{tofurun.ClassApply: apply, tofurun.ClassDestroy: destroy},
	})
	require.NoError(t, err)
	return &fx{t: t, env: env, mgr: mgr, fence: fence, runner: runner, work: work}
}

type creds map[tofurun.IdentityClass]tofurun.Credential

func (c creds) For(class tofurun.IdentityClass) (tofurun.Credential, error) { return c[class], nil }

func version(t *testing.T, bin string) string {
	t.Helper()
	for _, v := range []string{"1.10.10", "1.10.9", "1.10.8", "1.10.7", "1.10.6", "1.10.5", "1.10.4", "1.10.3", "1.10.2", "1.10.1", "1.10.0"} {
		if tofurun.CheckVersion(context.Background(), bin, v) == nil {
			return v
		}
	}
	t.Fatalf("the OpenTofu named by DC_FLEET_TOFU is not a version this test knows")
	return ""
}

func (f *fx) open(n int, kind tofurun.Kind, vars map[string]string) (*tofurun.Session, error) {
	return f.runner.Open(context.Background(), tofurun.OpenParams{
		ClusterUID: clusterA, RunUID: runUID(n), RequestID: reqID, Kind: kind,
		ModuleDir: tofutest.Root(f.t, "fakecloud"), Backend: roleBackend{t: f.t, mgr: f.mgr, env: f.env}, Vars: vars,
	})
}

func (f *fx) slotState() string {
	var s string
	require.NoError(f.t, f.env.Gorm.Raw(`SELECT state FROM writer_slots WHERE cluster_uid = ?`, clusterA).Scan(&s).Error)
	return s
}

func (f *fx) roleExists(n int) bool {
	name, _ := writerslot.StateRoleName(runUID(n))
	var c int
	require.NoError(f.t, f.env.Gorm.Raw(`SELECT count(*) FROM pg_roles WHERE rolname = ?`, name).Scan(&c).Error)
	return c > 0
}

func TestFleetPGSlotFenceFullRunThroughTheSlot(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()

	s, err := f.open(1, tofurun.KindCreate, nil)
	require.NoError(t, err)
	require.Equal(t, writerslot.StateHeld, f.slotState(), "the slot is held from admission")
	require.True(t, f.roleExists(1), "the per-run state role exists while the run holds the slot")

	pr, err := s.Plan(ctx)
	require.NoError(t, err)
	res, err := s.Apply(ctx, tofurun.Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.NoError(t, err)
	require.NoError(t, s.Close(ctx))

	require.Equal(t, writerslot.StateFree, f.slotState(), "exit, role drop and release free the slot")
	require.False(t, f.roleExists(1), "the per-run role is gone")
	var head struct {
		Lineage string
		Serial  int64
	}
	require.NoError(t, f.env.Gorm.Raw(`SELECT lineage, serial FROM state_heads WHERE cluster_uid = ?`, clusterA).Scan(&head).Error)
	require.Equal(t, res.Head.Lineage, head.Lineage, "the slot recorded the head the run left")
	require.Equal(t, res.Head.Serial, head.Serial)
	entries, err := os.ReadDir(f.work)
	require.NoError(t, err)
	require.Empty(t, entries)

	// The next run is admitted: the exit really released.
	s2, err := f.open(2, tofurun.KindChange, map[string]string{"gen": "2"})
	require.NoError(t, err)
	require.NoError(t, s2.Close(ctx))
}

func TestFleetPGSlotFenceSecondWriterIsRefused(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()

	first, err := f.open(1, tofurun.KindCreate, nil)
	require.NoError(t, err)

	second, err := f.open(2, tofurun.KindChange, nil)
	require.Nil(t, second)
	require.ErrorIs(t, err, tofurun.ErrWriterRefused)
	require.ErrorIs(t, err, writerslot.ErrNotAdmitted, "the slot's own refusal is what refused it")
	require.False(t, f.roleExists(2), "a refused run was given no state role")
	entries, rerr := os.ReadDir(f.work)
	require.NoError(t, rerr)
	require.Len(t, entries, 1, "only the first run has a work directory: the second started nothing")
	require.Equal(t, writerslot.StateHeld, f.slotState())

	// Control: once the first run exits, the same request is admitted, so the refusal above was the
	// slot's and not a defect of the request.
	require.NoError(t, first.Close(ctx))
	again, err := f.open(2, tofurun.KindChange, nil)
	require.NoError(t, err)
	require.NoError(t, again.Close(ctx))
}

func TestFleetPGSlotFenceConcurrentOpensAdmitExactlyOne(t *testing.T) {
	f := newFx(t)
	const n = 6
	var wins, refused atomic.Int64
	var winner *tofurun.Session
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := f.open(i, tofurun.KindChange, nil)
			switch {
			case err == nil:
				wins.Add(1)
				mu.Lock()
				winner = s
				mu.Unlock()
			case isRefused(err):
				refused.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int64(1), wins.Load(), "exactly one concurrent run holds the writer slot")
	require.Equal(t, int64(n-1), refused.Load())
	require.NotNil(t, winner)
	require.NoError(t, winner.Close(context.Background()))
}

func isRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), tofurun.ErrWriterRefused.Error())
}

func TestFleetPGSlotFenceDestroyFailsLoudly(t *testing.T) {
	f := newFx(t)
	s, err := f.open(1, tofurun.KindDestroy, nil)
	require.Nil(t, s)
	require.ErrorIs(t, err, tofurun.ErrWriterRefused)
	require.ErrorIs(t, err, writerslot.ErrDestroyNotImplemented, "this build executes no destroy: a typed refusal, not a plausible success")
	var runs int
	require.NoError(t, f.env.Gorm.Raw(`SELECT count(*) FROM runs`).Scan(&runs).Error)
	require.Zero(t, runs, "the slot was not even touched")
	require.False(t, f.roleExists(1))
	entries, rerr := os.ReadDir(f.work)
	require.NoError(t, rerr)
	require.Empty(t, entries)

	// Control: the same request as a CHANGE is admitted, so the refusal above is about DESTROY.
	ok, err := f.open(1, tofurun.KindChange, nil)
	require.NoError(t, err)
	require.NoError(t, ok.Close(context.Background()))
}

func TestFleetPGSlotFenceAGateRefusedRunStillFreesTheSlot(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	s, err := f.open(1, tofurun.KindCreate, nil)
	require.NoError(t, err)
	pr, err := s.Plan(ctx)
	require.NoError(t, err)
	_, err = s.Apply(ctx, tofurun.Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.NoError(t, err)
	require.NoError(t, s.Close(ctx))

	// A replacement in a CHANGE run: the gate refuses, the run is a failed one, the slot is freed.
	r, err := f.open(2, tofurun.KindChange, map[string]string{"rev": "2"})
	require.NoError(t, err)
	_, err = r.Plan(ctx)
	require.ErrorIs(t, err, tofurun.ErrGateRefused)
	require.NoError(t, r.Close(ctx))
	require.Equal(t, writerslot.StateFree, f.slotState())
	var phase string
	require.NoError(t, f.env.Gorm.Raw(`SELECT phase FROM runs WHERE run_uid = ?`, runUID(2)).Scan(&phase).Error)
	require.Equal(t, writerslot.PhaseReconciled, phase)
}
