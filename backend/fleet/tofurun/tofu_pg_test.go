// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package tofurun

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-fleet/internal/pgtest"
	"github.com/devicechain-io/dc-fleet/internal/tofutest"
)

const (
	run1 = "aaaaaaaa-0000-0000-0000-000000000001"
	run2 = "aaaaaaaa-0000-0000-0000-000000000002"
	run3 = "aaaaaaaa-0000-0000-0000-000000000003"
	run4 = "aaaaaaaa-0000-0000-0000-000000000004"
)

// recFence is a Fence that records what it was told and can refuse.
type recFence struct {
	mu        sync.Mutex
	refuse    error
	acquired  int
	started   int
	completed []Outcome
}

func (f *recFence) Acquire(context.Context, RunInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return f.refuse
	}
	f.acquired++
	return nil
}
func (f *recFence) Started(context.Context, RunInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return nil
}
func (f *recFence) Complete(_ context.Context, _ RunInfo, o Outcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, o)
	return nil
}

// classCreds hands out a credential per class carrying the fake cloud's token for that class, or
// whatever the test overrides it with.
type classCreds struct {
	override map[IdentityClass]Credential
}

func tokenCred(t *testing.T, class IdentityClass, token string) Credential {
	t.Helper()
	env := map[string]string{}
	if token != "" {
		env["FLEET_FAKE_CLOUD_TOKEN"] = token
	}
	c, err := NewCredential(class, "fleet-"+string(class)+"@example", env)
	require.NoError(t, err)
	return c
}

func (c classCreds) For(class IdentityClass) (Credential, error) {
	if o, ok := c.override[class]; ok {
		return o, nil
	}
	return Credential{}, errors.New("no credential for the class")
}

func defaultCreds(t *testing.T) classCreds {
	return classCreds{override: map[IdentityClass]Credential{
		ClassApply:   tokenCred(t, ClassApply, "apply-token"),
		ClassDestroy: tokenCred(t, ClassDestroy, "destroy-token"),
	}}
}

type env struct {
	t       *testing.T
	pg      *pgtest.Env
	conn    string
	work    string
	fence   *recFence
	runner  *Runner
	backend PGBackend
}

func newEnv(t *testing.T, mutate func(*Config)) *env {
	t.Helper()
	pg := pgtest.Open(t)
	tofutest.PrepareStateSchema(t, pg.Gorm, "tf_state", "postgres")
	conn := pg.DSN
	e := &env{t: t, pg: pg, conn: conn, work: t.TempDir(), fence: &recFence{}}
	e.backend = PGBackend{ConnStr: conn, Schema: "tf_state"}
	cfg := Config{
		Binary: tofutest.Tofu(t), ExpectedVersion: detectVersion(t, tofutest.Tofu(t)), WorkRoot: e.work,
		Gate: Gate{AllowedTypes: map[string]bool{"terraform_data": true}}, Fence: e.fence, Credentials: defaultCreds(t),
		Encryption: tofutest.Encryption,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := New(context.Background(), cfg)
	require.NoError(t, err)
	e.runner = r
	return e
}

// detectVersion reads the installed binary's own version, so the tests run against whatever
// OpenTofu the maintainer has while the library's default stays the pinned one.
func detectVersion(t *testing.T, bin string) string {
	t.Helper()
	for _, v := range []string{"1.10.10", "1.10.9", "1.10.8", "1.10.7", "1.10.6", "1.10.5", "1.10.4", "1.10.3", "1.10.2", "1.10.1", "1.10.0", "1.11.0", "1.11.1", "1.11.2", "1.11.3", "1.11.4", "1.11.5"} {
		if CheckVersion(context.Background(), bin, v) == nil {
			return v
		}
	}
	t.Fatalf("the OpenTofu named by DC_FLEET_TOFU is not a version this test knows; add it")
	return ""
}

func (e *env) params(run string, kind Kind, vars map[string]string) OpenParams {
	return OpenParams{
		ClusterUID: clusterUID, RunUID: run, RequestID: "req-" + run, Kind: kind,
		ModuleDir: tofutest.Root(e.t, "fakecloud"), Backend: e.backend, Vars: vars,
	}
}

func (e *env) open(run string, kind Kind, vars map[string]string) *Session {
	e.t.Helper()
	s, err := e.runner.Open(context.Background(), e.params(run, kind, vars))
	require.NoError(e.t, err)
	return s
}

// apply runs a full plan-approve-apply of kind and closes the session.
func (e *env) apply(run string, kind Kind, vars map[string]string) *ApplyResult {
	e.t.Helper()
	s := e.open(run, kind, vars)
	defer func() { require.NoError(e.t, s.Close(context.Background())) }()
	pr, err := s.Plan(context.Background())
	require.NoError(e.t, err)
	res, err := s.Apply(context.Background(), Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.NoError(e.t, err)
	return res
}

func (e *env) stateRows() (rows int, plaintext bool) {
	e.t.Helper()
	var data []string
	require.NoError(e.t, e.pg.Gorm.Raw(`SELECT data FROM tf_state.states`).Scan(&data).Error)
	for _, d := range data {
		// tofu keeps serial and lineage readable in an encrypted state, by design; the resources are
		// what must not be.
		if strings.Contains(d, "terraform_data") || !strings.Contains(d, "encrypted_data") {
			plaintext = true
		}
	}
	return len(data), plaintext
}

func TestFleetPGTofuHappyCreateThenChangeStaysEncryptedAndTidy(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	s := e.open(run1, KindCreate, nil)
	pr, err := s.Plan(ctx)
	require.NoError(t, err)
	require.True(t, pr.HasChanges)
	require.Equal(t, 1, pr.Facts.ChangeCount)
	require.Equal(t, "terraform_data.cluster", pr.Facts.Facts[0].Address)
	require.Equal(t, []string{"create"}, pr.Facts.Facts[0].Actions)
	require.True(t, pr.Verdict.Allowed)
	first := pr.Head

	res, err := s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.NoError(t, err)
	require.Greater(t, res.Head.Serial, first.Serial, "an apply advances the state serial")
	require.NotEmpty(t, res.Head.Lineage)
	require.NoError(t, s.Close(ctx))
	require.Len(t, e.fence.completed, 1)
	require.True(t, e.fence.completed[0].Succeeded)
	require.Equal(t, res.Head, e.fence.completed[0].Head, "the fence is told the head the run left")

	rows, plaintext := e.stateRows()
	require.Equal(t, 1, rows)
	require.False(t, plaintext, "the state in Postgres must be ciphertext")
	entries, err := os.ReadDir(e.work)
	require.NoError(t, err)
	require.Empty(t, entries, "the work directory is removed at close")

	// A CHANGE that only updates in place applies; its plan is an update, safe in a change.
	res2 := e.apply(run2, KindChange, map[string]string{"gen": "2"})
	require.Greater(t, res2.Head.Serial, res.Head.Serial)
	require.Equal(t, res.Head.Lineage, res2.Head.Lineage)
}

func TestFleetPGTofuStalePlanIsRefused(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.apply(run1, KindCreate, nil)

	// Control first: with nothing moving the state between plan and apply, the same flow applies.
	c := e.open(run2, KindChange, map[string]string{"gen": "2"})
	cp, err := c.Plan(ctx)
	require.NoError(t, err)
	_, err = c.Apply(ctx, Approval{FactsDigest: cp.Digest, Head: cp.Head})
	require.NoError(t, err, "control: a plan whose state did not move applies")
	require.NoError(t, c.Close(ctx))

	// A is planned, then another writer moves the state, then A is applied.
	a := e.open(run3, KindChange, map[string]string{"gen": "3"})
	ap, err := a.Plan(ctx)
	require.NoError(t, err)
	b := e.open(run4, KindChange, map[string]string{"gen": "4"})
	bp, err := b.Plan(ctx)
	require.NoError(t, err)
	bres, err := b.Apply(ctx, Approval{FactsDigest: bp.Digest, Head: bp.Head})
	require.NoError(t, err)
	require.NoError(t, b.Close(ctx))

	_, err = a.Apply(ctx, Approval{FactsDigest: ap.Digest, Head: ap.Head})
	require.ErrorIs(t, err, ErrStalePlan)

	// The refusal is the runner's own, before tofu: the state is exactly what B left.
	head, err := a.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, bres.Head, head, "the stale plan did not run")
	require.False(t, a.applied)

	// And it is not the only layer: tofu itself refuses the same saved plan.
	_, _, terr := a.exec(ctx, "apply", []string{"-input=false", "-no-color", ap2file(a)}, false)
	require.Error(t, terr, "defence in depth: tofu's own staleness check refuses it too")
	require.NoError(t, a.Close(ctx))
	require.False(t, e.fence.completed[len(e.fence.completed)-1].Succeeded, "a refused apply is a failed run")
}

// ap2file is the saved plan's path (the refusal retained it for inspection).
func ap2file(s *Session) string { return s.plan.file }

func TestFleetPGTofuApprovalAndSingleUse(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	t.Run("a digest or head the plan does not carry is refused, and nothing applies", func(t *testing.T) {
		s := e.open(run1, KindCreate, nil)
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		_, err = s.Apply(ctx, Approval{FactsDigest: "deadbeef", Head: pr.Head})
		require.ErrorIs(t, err, ErrApprovalMismatch)
		_, err = s.Apply(ctx, Approval{FactsDigest: "", Head: pr.Head})
		require.ErrorIs(t, err, ErrApprovalMismatch)
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: StateHead{Lineage: pr.Head.Lineage, Serial: pr.Head.Serial + 1}})
		require.ErrorIs(t, err, ErrApprovalMismatch)
		head, err := s.Head(ctx)
		require.NoError(t, err)
		require.Equal(t, pr.Head, head, "nothing was written")
		require.False(t, s.applied)
	})

	t.Run("a saved plan applies once", func(t *testing.T) {
		s := e.open(run2, KindCreate, nil)
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		ap := Approval{FactsDigest: pr.Digest, Head: pr.Head}
		_, err = s.Apply(ctx, ap)
		require.NoError(t, err)
		_, err = s.Apply(ctx, ap)
		require.ErrorIs(t, err, ErrNoPlan, "the same approval cannot apply the same plan twice")
	})

	t.Run("an apply with no plan is refused", func(t *testing.T) {
		s := e.open(run3, KindChange, nil)
		defer s.Close(ctx)
		_, err := s.Apply(ctx, Approval{FactsDigest: "x"})
		require.ErrorIs(t, err, ErrNoPlan)
	})

	t.Run("a tampered plan file is refused", func(t *testing.T) {
		s := e.open(run4, KindChange, map[string]string{"gen": "9"})
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(s.plan.file, []byte("not the plan that was approved"), 0o600))
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.ErrorIs(t, err, ErrPlanTampered)
	})
}

func TestFleetPGTofuFreshPlanThatDiffersRequiresReplan(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	// Control: a fresh plan of unchanged inputs reduces to the same facts and applies.
	c := e.open(run1, KindCreate, nil)
	cp, err := c.Plan(ctx)
	require.NoError(t, err)
	_, err = c.Apply(ctx, Approval{FactsDigest: cp.Digest, Head: cp.Head})
	require.NoError(t, err)
	require.NoError(t, c.Close(ctx))

	s := e.open(run2, KindChange, map[string]string{"gen": "2"})
	defer s.Close(ctx)
	pr, err := s.Plan(ctx)
	require.NoError(t, err)
	// An input moves between the approved plan and the apply: the re-plan has one more resource.
	s.env = append(s.env, "TF_VAR_extra=1")
	_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.ErrorIs(t, err, ErrReplanRequired)
	require.False(t, s.applied)
}

func TestFleetPGTofuGateRefusalRetainsNothing(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.apply(run1, KindCreate, nil)

	t.Run("a replacement in a CHANGE run is refused and cannot be applied", func(t *testing.T) {
		s := e.open(run2, KindChange, map[string]string{"rev": "2"})
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.ErrorIs(t, err, ErrGateRefused)
		require.NotNil(t, pr, "the refused facts are returned so a person can read why")
		require.Equal(t, []string{"delete", "create"}, pr.Facts.Facts[0].Actions)
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.ErrorIs(t, err, ErrNoPlan)
	})

	t.Run("control: the same replacement in a DESTROY-class gate is not what destroy does, and an update passes a change", func(t *testing.T) {
		s := e.open(run3, KindChange, map[string]string{"gen": "5"})
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		require.Equal(t, []string{"update"}, pr.Facts.Facts[0].Actions)
	})

	t.Run("an empty gate refuses every change", func(t *testing.T) {
		e2 := newEnv(t, func(c *Config) { c.Gate = Gate{} })
		s := e2.open(run1, KindCreate, nil)
		defer s.Close(ctx)
		_, err := s.Plan(ctx)
		require.ErrorIs(t, err, ErrGateRefused)
	})

	t.Run("a drift check plans refresh-only and never applies", func(t *testing.T) {
		s := e.open(run4, KindDriftCheck, nil)
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		require.False(t, pr.HasChanges)
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.ErrorIs(t, err, ErrKindCannotApply)
	})
}

func TestFleetPGTofuIdentityClasses(t *testing.T) {
	ctx := context.Background()

	t.Run("a destroy-class run cannot use apply credentials: refused before anything exists", func(t *testing.T) {
		e := newEnv(t, func(c *Config) {
			// A misbehaving source that hands the apply credential for every class.
			a := tokenCred(t, ClassApply, "apply-token")
			c.Credentials = classCreds{override: map[IdentityClass]Credential{ClassApply: a, ClassDestroy: a}}
		})
		_, err := e.runner.Open(ctx, e.params(run1, KindDestroy, nil))
		require.ErrorIs(t, err, ErrIdentityClass)
		require.Zero(t, e.fence.acquired, "the fence was never asked: the refusal precedes admission")
		entries, rerr := os.ReadDir(e.work)
		require.NoError(t, rerr)
		require.Empty(t, entries, "no work directory, so no process")
	})

	t.Run("an apply-class kind offered the destroy credential is refused too", func(t *testing.T) {
		e := newEnv(t, func(c *Config) {
			d := tokenCred(t, ClassDestroy, "destroy-token")
			c.Credentials = classCreds{override: map[IdentityClass]Credential{ClassApply: d, ClassDestroy: d}}
		})
		for _, k := range []Kind{KindCreate, KindChange, KindUpgrade, KindDriftCheck, KindRecover} {
			_, err := e.runner.Open(ctx, e.params(run1, k, nil))
			require.ErrorIs(t, err, ErrIdentityClass, k)
		}
		require.Zero(t, e.fence.acquired)
	})

	// The same property at the boundary that does not trust the runner: with the runner's check
	// REMOVED, the fake cloud still refuses a destroy that holds the apply token.
	t.Run("with the runner's class check removed, the cloud refuses the destroy", func(t *testing.T) {
		a := tokenCred(t, ClassApply, "apply-token")
		e := newEnv(t, func(c *Config) {
			c.Credentials = classCreds{override: map[IdentityClass]Credential{ClassApply: a, ClassDestroy: a}}
		})
		// Build the state to destroy with the genuine apply identity.
		seed := newEnvOn(t, e, defaultCreds(t))
		seed.apply(run1, KindCreate, nil)

		saved := checkClass
		checkClass = func(IdentityClass, Credential) error { return nil }
		defer func() { checkClass = saved }()

		s := e.open(run2, KindDestroy, nil)
		pr, err := s.Plan(ctx)
		require.NoError(t, err, "a destroy plan is allowed: it is only deletes")
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.Error(t, err, "the fake cloud's destroy step refuses the apply token")
		require.NoError(t, s.Close(ctx))
		checkClass = saved

		// The resource is still in the state.
		d := newEnvOn(t, e, defaultCreds(t))
		v := d.open(run3, KindDriftCheck, nil)
		defer v.Close(ctx)
		listed, _, err := v.exec(ctx, "state", []string{"list"}, true)
		require.NoError(t, err)
		require.Contains(t, string(listed), "terraform_data.cluster", "the cluster survived the refused destroy")
	})

	t.Run("control: the destroy credential destroys", func(t *testing.T) {
		e := newEnv(t, nil)
		e.apply(run1, KindCreate, nil)
		s := e.open(run2, KindDestroy, nil)
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		require.Equal(t, []string{"delete"}, pr.Facts.Facts[0].Actions)
		res, err := s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.NoError(t, err)
		require.NotEmpty(t, res.Head.Lineage)
	})

	t.Run("the parent's environment does not reach tofu: a destroy with no token in its credential is refused by the cloud", func(t *testing.T) {
		e := newEnv(t, func(c *Config) {
			c.Credentials = classCreds{override: map[IdentityClass]Credential{
				ClassApply: tokenCred(t, ClassApply, "apply-token"), ClassDestroy: tokenCred(t, ClassDestroy, ""),
			}}
		})
		seed := newEnvOn(t, e, defaultCreds(t))
		seed.apply(run1, KindCreate, nil)

		// The parent process holds exactly what the destroy step wants. If the child inherited it, the destroy would pass.
		t.Setenv("FLEET_FAKE_CLOUD_TOKEN", "destroy-token")
		s := e.open(run2, KindDestroy, nil)
		defer s.Close(ctx)
		pr, err := s.Plan(ctx)
		require.NoError(t, err)
		_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
		require.Error(t, err, "the child was not handed the parent's token")
	})
}

// newEnvOn returns an env sharing e's database and state but running with other credentials.
func newEnvOn(t *testing.T, e *env, creds Credentials) *env {
	t.Helper()
	cfg := e.runner.cfg
	cfg.Credentials = creds
	cfg.Fence = &recFence{}
	r, err := New(context.Background(), cfg)
	require.NoError(t, err)
	return &env{t: t, pg: e.pg, conn: e.conn, work: e.work, fence: cfg.Fence.(*recFence), runner: r, backend: e.backend}
}

func TestFleetPGTofuFenceRefusalStartsNothing(t *testing.T) {
	e := newEnv(t, nil)
	e.fence.refuse = errors.New("slot held by another run")
	_, err := e.runner.Open(context.Background(), e.params(run1, KindCreate, nil))
	require.ErrorIs(t, err, ErrWriterRefused)
	require.Zero(t, e.fence.started)
	entries, rerr := os.ReadDir(e.work)
	require.NoError(t, rerr)
	require.Empty(t, entries)
	var n int
	require.NoError(t, e.pg.Gorm.Raw(`SELECT count(*) FROM tf_state.states`).Scan(&n).Error)
	require.Zero(t, n, "a refused run touched no state")
}

func TestFleetPGTofuRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("no encryption key is refused", func(t *testing.T) {
		e := newEnv(t, func(c *Config) { c.Encryption = "" })
		_, err := e.runner.Open(ctx, e.params(run1, KindCreate, nil))
		require.ErrorIs(t, err, ErrEncryptionRequired)
		require.Zero(t, e.fence.acquired)
	})

	t.Run("the root's own enforced encryption refuses a key-less run even if the runner was told plaintext is fine", func(t *testing.T) {
		e := newEnv(t, func(c *Config) { c.Encryption = ""; c.AllowPlaintextState = true })
		_, err := e.runner.Open(ctx, e.params(run1, KindCreate, nil))
		require.Error(t, err, "init fails: the fixture root enforces encryption")
	})

	t.Run("a root with no backend block is refused after init", func(t *testing.T) {
		e := newEnv(t, nil)
		p := e.params(run1, KindCreate, nil)
		p.ModuleDir = tofutest.Root(t, "nobackend")
		p.Backend = bareBackend{}
		_, err := e.runner.Open(ctx, p)
		require.ErrorIs(t, err, ErrBackendMismatch)
		entries, rerr := os.ReadDir(e.work)
		require.NoError(t, rerr)
		require.Empty(t, entries, "a failed open cleans up")
		require.Len(t, e.fence.completed, 1, "and tells the fence the run ended")
		require.False(t, e.fence.completed[0].Succeeded)
	})

	t.Run("a wrong state password leaks neither the password nor the connection string", func(t *testing.T) {
		e := newEnv(t, nil)
		e.backend = PGBackend{ConnStr: tofutest.ConnString(t, e.conn, "postgres", "wrong-password-xyz"), Schema: "tf_state"}
		_, err := e.runner.Open(ctx, e.params(run1, KindCreate, nil))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "wrong-password-xyz")
		require.NotContains(t, err.Error(), e.backend.ConnStr)
		require.NotContains(t, err.Error(), tofutest.Passphrase)
	})

	t.Run("a root carrying state is refused", func(t *testing.T) {
		e := newEnv(t, nil)
		root := t.TempDir()
		require.NoError(t, copyModule(tofutest.Root(t, "fakecloud"), root, mustDigest(t, tofutest.Root(t, "fakecloud"))))
		require.NoError(t, os.WriteFile(root+"/terraform.tfstate", []byte("{}"), 0o600))
		p := e.params(run1, KindCreate, nil)
		p.ModuleDir = root
		_, err := e.runner.Open(ctx, p)
		require.ErrorIs(t, err, ErrModuleRefused)
		require.Zero(t, e.fence.acquired)
	})
}

func mustDigest(t *testing.T, dir string) string {
	t.Helper()
	d, _, err := inspectModule(dir)
	require.NoError(t, err)
	return d
}

// bareBackend names the pg kind but configures nothing, so init lands on whatever the root declares.
type bareBackend struct{}

func (bareBackend) Kind() BackendKind { return BackendPG }
func (bareBackend) Config(context.Context, RunInfo) (BackendConfig, error) {
	return BackendConfig{Kind: BackendPG}, nil
}

func TestFleetPGTofuRootPathKeepsRelativeModulesResolving(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	p := e.params(run1, KindCreate, nil)
	p.ModuleDir = tofutest.Tree(t, "reltree")
	p.RootPath = "roots/app"
	s, err := e.runner.Open(ctx, p)
	require.NoError(t, err)
	pr, err := s.Plan(ctx)
	require.NoError(t, err)
	require.Equal(t, "module.inner.terraform_data.x", pr.Facts.Facts[0].Address)
	_, err = s.Apply(ctx, Approval{FactsDigest: pr.Digest, Head: pr.Head})
	require.NoError(t, err)
	require.NoError(t, s.Close(ctx))

	// Control: the same root run as if the root were the tree cannot see its sibling module.
	q := e.params(run2, KindCreate, nil)
	q.ModuleDir = tofutest.Tree(t, "reltree") + "/roots/app"
	_, err = e.runner.Open(ctx, q)
	require.Error(t, err, "copying the root alone loses ../../modules/inner")
}
