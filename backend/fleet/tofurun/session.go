// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors a session returns. Each is a refusal at one named rule and is testable with errors.Is.
var (
	// ErrWriterRefused wraps whatever the Fence refused with: no process was started.
	ErrWriterRefused = errors.New("tofurun: the writer fence refused the run")
	// ErrEncryptionRequired: state and plan encryption is required and no key configuration was given.
	ErrEncryptionRequired = errors.New("tofurun: state and plan encryption is required but no key configuration was given")
	// ErrModuleRefused: the root tree holds something a baked root must not.
	ErrModuleRefused = errors.New("tofurun: the root tree is not acceptable")
	// ErrBackendMismatch: after init the root's backend is not the one requested (a root with no
	// backend block would silently keep local state inside a Job pod).
	ErrBackendMismatch = errors.New("tofurun: the initialised backend is not the requested one")
	// ErrGateRefused: the plan gate refused the plan. The plan is not retained and cannot be applied.
	ErrGateRefused = errors.New("tofurun: the plan gate refused the plan")
	// ErrNoPlan: Apply with no retained plan (none made, a refused one, or one already applied).
	ErrNoPlan = errors.New("tofurun: there is no plan to apply")
	// ErrApprovalMismatch: the approval does not bind the retained plan's facts or state head.
	ErrApprovalMismatch = errors.New("tofurun: the approval does not match the plan")
	// ErrStalePlan: the state's lineage or serial moved since the plan was made.
	ErrStalePlan = errors.New("tofurun: the saved plan is stale: the state moved since it was made")
	// ErrReplanRequired: a fresh plan's facts differ from the approved ones.
	ErrReplanRequired = errors.New("tofurun: a fresh plan differs from the approved one: replan required")
	// ErrPlanTampered: the saved plan file is not the bytes that were planned.
	ErrPlanTampered = errors.New("tofurun: the saved plan file changed since it was planned")
	// ErrKindCannotApply: the run's kind never applies (a drift check only looks).
	ErrKindCannotApply = errors.New("tofurun: this run kind never applies")
	// ErrClosed: the session was closed.
	ErrClosed = errors.New("tofurun: the session is closed")
)

var runUIDRe = uuidRe

// Config is a Runner's fixed configuration: the same for every run of one image.
type Config struct {
	// Binary is the absolute path of the tofu binary; empty looks `tofu` up on PATH.
	Binary string
	// ExpectedVersion is the exact OpenTofu version; empty means PinnedVersion.
	ExpectedVersion string
	// WorkRoot is an existing directory (an emptyDir in a Job) under which each run gets a private,
	// 0700 directory that is removed when the session closes.
	WorkRoot string
	// PluginMirror is a directory of pre-baked providers. Empty means no provider can be
	// installed at all (the built-in provider still works): the runner never reaches a registry.
	PluginMirror string
	// Gate judges plans. The zero Gate refuses every change.
	Gate Gate
	// Fence is the writer exclusion. Required.
	Fence Fence
	// Credentials supplies the per-class cloud credential. Required.
	Credentials Credentials
	// Encryption is the TF_ENCRYPTION key configuration (a secret). Required unless
	// AllowPlaintextState is set, which only a test should do.
	Encryption string
	// AllowPlaintextState lets a run proceed with no Encryption. The roots' own `enforced = true`
	// is the second line; this is the first.
	AllowPlaintextState bool
	// LockTimeout is how long a command waits for the backend lock. Default 20s.
	LockTimeout time.Duration
	// RunnerImageDigest is recorded with the run.
	RunnerImageDigest string
}

// Runner opens sessions. It holds no per-run state.
type Runner struct {
	cfg Config
	bin string
}

// New validates cfg and the tofu binary (it must be OpenTofu at the expected version).
func New(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.Fence == nil {
		return nil, ErrNoFence
	}
	if cfg.Credentials == nil {
		return nil, errors.New("tofurun: a credential source is required")
	}
	if cfg.ExpectedVersion == "" {
		cfg.ExpectedVersion = PinnedVersion
	}
	if cfg.LockTimeout <= 0 {
		cfg.LockTimeout = 20 * time.Second
	}
	if fi, err := os.Stat(cfg.WorkRoot); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("tofurun: the work root %q is not a directory", cfg.WorkRoot)
	}
	bin, err := ResolveTofu(cfg.Binary)
	if err != nil {
		return nil, err
	}
	if err := CheckVersion(ctx, bin, cfg.ExpectedVersion); err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, bin: bin}, nil
}

// OpenParams describes one run.
type OpenParams struct {
	ClusterUID string
	RunUID     string
	RequestID  string
	Kind       Kind
	// ModuleDir is the baked TREE holding the root. It is copied; the session never writes to it.
	ModuleDir string
	// RootPath is the root's directory inside ModuleDir (slash-separated, relative); empty means
	// the tree is the root. The whole tree is digested and copied, so a root's relative module
	// sources keep resolving, and tofu runs from RootPath.
	RootPath string
	Backend  Backend
	// Vars are root input variables, delivered as TF_VAR_ environment (never on a command line).
	Vars map[string]string
}

// checkClass is the identity-class rule: the credential offered must be of the class the kind
// requires. It is a variable only so a test can remove this protection and show the cloud still
// refuses; nothing else assigns it.
var checkClass = func(want IdentityClass, got Credential) error {
	if got.class != want {
		return fmt.Errorf("%w: a %s run needs a %s credential, was offered a %s one", ErrIdentityClass, want, want, got.class)
	}
	return nil
}

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Session is one run's tofu work: an isolated directory with an initialised backend.
type Session struct {
	r         *Runner
	bin       string
	info      RunInfo
	kind      Kind
	dir       string
	moduleDir string
	env       []string
	secrets   []string
	workspace string

	mu       sync.Mutex
	closed   bool
	failed   bool
	applied  bool
	plan     *retainedPlan
	lastHead StateHead
}

type retainedPlan struct {
	file     string
	checksum string
	facts    PlanFacts
	digest   string
	head     StateHead
}

// Info is the run as the fence was told of it.
func (s *Session) Info() RunInfo { return s.info }

// Open admits the run and prepares it. Every refusal that needs no process is decided before the
// fence is asked, and the fence is asked before the work directory exists, so a refused run leaves
// nothing behind.
func (r *Runner) Open(ctx context.Context, p OpenParams) (*Session, error) {
	if !runUIDRe.MatchString(p.RunUID) || !uuidRe.MatchString(p.ClusterUID) {
		return nil, errors.New("tofurun: the cluster and run uids must be uuids")
	}
	if strings.TrimSpace(p.RequestID) == "" {
		return nil, errors.New("tofurun: a request id is required")
	}
	class, err := ClassFor(p.Kind)
	if err != nil {
		return nil, err
	}
	if r.cfg.Encryption == "" && !r.cfg.AllowPlaintextState {
		return nil, ErrEncryptionRequired
	}
	if p.Backend == nil {
		return nil, errors.New("tofurun: a state backend is required")
	}
	cred, err := r.cfg.Credentials.For(class)
	if err != nil {
		return nil, fmt.Errorf("tofurun: credential for the %s class: %w", class, err)
	}
	if err := checkClass(class, cred); err != nil {
		return nil, err
	}
	for k := range p.Vars {
		if !varNameRe.MatchString(k) {
			return nil, fmt.Errorf("tofurun: %q is not a variable name", k)
		}
	}
	digest, lockHash, err := inspectTree(p.ModuleDir, p.RootPath)
	if err != nil {
		return nil, err
	}

	info := RunInfo{
		ClusterUID: strings.ToLower(p.ClusterUID), RunUID: strings.ToLower(p.RunUID), RequestID: p.RequestID,
		Kind: p.Kind, Class: class, Principal: cred.principal, Backend: p.Backend.Kind(),
		TofuVersion: r.cfg.ExpectedVersion, ProviderLockHash: lockHash, ModuleDigest: digest,
	}
	if err := r.cfg.Fence.Acquire(ctx, info); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWriterRefused, err)
	}

	s := &Session{r: r, bin: r.bin, info: info, kind: p.Kind}
	if err := s.prepare(ctx, p, cred); err != nil {
		s.failed = true
		return nil, errors.Join(err, s.Close(ctx))
	}
	return s, nil
}

func (s *Session) prepare(ctx context.Context, p OpenParams, cred Credential) error {
	bc, err := p.Backend.Config(ctx, s.info)
	if err != nil {
		return err
	}
	if bc.Kind != s.info.Backend {
		return fmt.Errorf("%w: the backend changed kind", ErrBackendMismatch)
	}
	s.workspace = bc.Workspace
	if err := s.r.cfg.Fence.Started(ctx, s.info); err != nil {
		return fmt.Errorf("%w: %w", ErrWriterRefused, err)
	}
	dir, err := os.MkdirTemp(s.r.cfg.WorkRoot, "run-")
	if err != nil {
		return fmt.Errorf("tofurun: work directory: %w", err)
	}
	s.dir = dir
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("tofurun: work directory mode: %w", err)
	}
	treeDir := filepath.Join(dir, "module")
	rootPath, err := cleanRootPath(p.RootPath)
	if err != nil {
		return err
	}
	s.moduleDir = filepath.Join(treeDir, filepath.FromSlash(rootPath))
	home := filepath.Join(dir, "home")
	for _, d := range []string{treeDir, home, filepath.Join(dir, "data")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("tofurun: work directory: %w", err)
		}
	}
	if err := copyModule(p.ModuleDir, treeDir, s.info.ModuleDigest); err != nil {
		return err
	}
	cli := filepath.Join(dir, "tofurc")
	if err := os.WriteFile(cli, []byte(cliConfig(s.r.cfg.PluginMirror)), 0o600); err != nil {
		return fmt.Errorf("tofurun: cli config: %w", err)
	}

	env := minimalEnv()
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("HOME", home)
	set("USERPROFILE", home)
	set("TF_DATA_DIR", filepath.Join(dir, "data"))
	set("TF_CLI_CONFIG_FILE", cli)
	set("TF_IN_AUTOMATION", "1")
	set("TF_INPUT", "0")
	if s.r.cfg.Encryption != "" {
		set("TF_ENCRYPTION", s.r.cfg.Encryption)
		s.secrets = append(s.secrets, s.r.cfg.Encryption)
	}
	for _, k := range sortedKeys(bc.Env) {
		set(k, bc.Env[k])
		s.secrets = append(s.secrets, bc.Env[k])
	}
	for _, k := range sortedKeys(p.Vars) {
		set("TF_VAR_"+k, p.Vars[k])
	}
	for _, k := range sortedKeys(cred.env) {
		set(k, cred.env[k])
		s.secrets = append(s.secrets, cred.env[k])
	}
	s.env = env

	args := []string{"-input=false", "-no-color", "-reconfigure"}
	args = append(args, bc.Args...)
	if _, _, err := s.exec(ctx, "init", args, false); err != nil {
		return err
	}
	if err := s.checkBackend(bc.Kind); err != nil {
		return err
	}
	if bc.Workspace != "" {
		if _, _, err := s.exec(ctx, "workspace", []string{"select", "-or-create", bc.Workspace}, false); err != nil {
			return err
		}
	}
	return nil
}

// checkBackend reads the minimal part of tofu's own record of the initialised backend and requires
// the requested type. A root whose backend block is missing or local initialises "local".
func (s *Session) checkBackend(want BackendKind) error {
	b, err := os.ReadFile(filepath.Join(s.dir, "data", "terraform.tfstate"))
	if err != nil {
		return fmt.Errorf("%w: no backend was recorded by init", ErrBackendMismatch)
	}
	var rec struct {
		Backend struct {
			Type string `json:"type"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(b, &rec); err != nil || rec.Backend.Type != string(want) {
		return fmt.Errorf("%w: wanted %s", ErrBackendMismatch, want)
	}
	return nil
}

// Head reads the state's lineage and serial (and nothing else of it; the rest is dropped as parsed).
func (s *Session) Head(ctx context.Context) (StateHead, error) {
	out, _, err := s.exec(ctx, "state", []string{"pull"}, true)
	if err != nil {
		return StateHead{}, err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return StateHead{}, nil // no state yet
	}
	var st struct {
		Lineage string `json:"lineage"`
		Serial  int64  `json:"serial"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return StateHead{}, errors.New("tofurun: the state could not be read for its head")
	}
	return StateHead{Lineage: st.Lineage, Serial: st.Serial}, nil
}

func (s *Session) lockArg() string {
	return fmt.Sprintf("-lock-timeout=%ds", int(s.r.cfg.LockTimeout/time.Second))
}

// PlanResult is what a plan produced: facts and digests, never plan JSON.
type PlanResult struct {
	Facts   PlanFacts
	Digest  string // the facts digest an approval binds to
	Verdict Verdict
	// Head is the state head the plan was made against.
	Head StateHead
	// HasChanges is false for an all-no-op plan.
	HasChanges bool
}

func (s *Session) guard() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

func (s *Session) fail(err error) error {
	if err != nil {
		s.failed = true
	}
	return err
}

func (s *Session) planArgs(out string) []string {
	args := []string{"-input=false", "-no-color", "-detailed-exitcode", s.lockArg(), "-out=" + out}
	switch {
	case s.kind == KindDestroy:
		args = append(args, "-destroy")
	case s.kind.refreshOnly():
		args = append(args, "-refresh-only")
	}
	return args
}

// Plan makes a saved plan, reduces it to facts and judges it. A plan the gate refuses is returned
// with ErrGateRefused and is NOT retained: nothing can then be applied.
func (s *Session) Plan(ctx context.Context) (*PlanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return nil, err
	}
	s.plan = nil
	before, err := s.Head(ctx)
	if err != nil {
		return nil, s.fail(err)
	}
	file := filepath.Join(s.dir, "plan.tfplan")
	if _, _, err := s.exec(ctx, "plan", s.planArgs(file), false, 2); err != nil {
		return nil, s.fail(err)
	}
	facts, err := s.factsOf(ctx, file)
	if err != nil {
		return nil, s.fail(err)
	}
	after, err := s.Head(ctx)
	if err != nil {
		return nil, s.fail(err)
	}
	if after != before {
		return nil, s.fail(fmt.Errorf("%w: the state moved while it was being planned", ErrStalePlan))
	}
	sum, err := fileSum(file)
	if err != nil {
		return nil, s.fail(err)
	}
	res := &PlanResult{Facts: facts, Digest: facts.Digest(), Head: before, HasChanges: hasChanges(facts)}
	res.Verdict = s.r.cfg.Gate.Judge(s.kind, facts)
	if !res.Verdict.Allowed {
		_ = os.Remove(file)
		s.failed = true
		return res, fmt.Errorf("%w: %s", ErrGateRefused, strings.Join(res.Verdict.Reasons, "; "))
	}
	s.plan = &retainedPlan{file: file, checksum: sum, facts: facts, digest: res.Digest, head: before}
	return res, nil
}

func (s *Session) factsOf(ctx context.Context, planFile string) (PlanFacts, error) {
	doc, _, err := s.exec(ctx, "show", []string{"-json", "-no-color", planFile}, true)
	if err != nil {
		return PlanFacts{}, err
	}
	return Extract(doc), nil
}

func hasChanges(f PlanFacts) bool {
	for _, x := range f.Facts {
		if !isInert(x.Actions) {
			return true
		}
	}
	return false
}

// Approval is what the manager's go decision binds: the facts digest it approved and the state head
// it approved against. Both are required.
type Approval struct {
	FactsDigest string
	Head        StateHead
}

// ApplyResult is what an apply left behind.
type ApplyResult struct {
	Head StateHead
}

// Apply applies the retained saved plan, once, if and only if every check holds:
//
//  1. the kind applies at all;
//  2. the approval binds this plan's facts digest and state head;
//  3. the gate still allows the retained facts for this kind;
//  4. the plan file is the bytes that were planned;
//  5. the state's lineage and serial are what the plan was made against;
//  6. a fresh plan of the same inputs has the same facts digest.
//
// Any refusal leaves the plan retained for inspection but a failed session; none of them runs apply.
func (s *Session) Apply(ctx context.Context, ap Approval) (*ApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return nil, err
	}
	if !s.kind.applies() {
		return nil, s.fail(fmt.Errorf("%w: %s", ErrKindCannotApply, s.kind))
	}
	pl := s.plan
	if pl == nil || s.applied {
		return nil, s.fail(ErrNoPlan)
	}
	if ap.FactsDigest == "" || ap.FactsDigest != pl.digest {
		return nil, s.fail(fmt.Errorf("%w: facts digest", ErrApprovalMismatch))
	}
	if ap.Head != pl.head {
		return nil, s.fail(fmt.Errorf("%w: state head", ErrApprovalMismatch))
	}
	if v := s.r.cfg.Gate.Judge(s.kind, pl.facts); !v.Allowed {
		return nil, s.fail(fmt.Errorf("%w: %s", ErrGateRefused, strings.Join(v.Reasons, "; ")))
	}
	sum, err := fileSum(pl.file)
	if err != nil || sum != pl.checksum {
		return nil, s.fail(ErrPlanTampered)
	}
	now, err := s.Head(ctx)
	if err != nil {
		return nil, s.fail(err)
	}
	if now != pl.head {
		return nil, s.fail(ErrStalePlan)
	}
	// Revalidate: a fresh plan against the same state and inputs must reduce to the same facts.
	reval := filepath.Join(s.dir, "revalidate.tfplan")
	if _, _, err := s.exec(ctx, "plan", s.planArgs(reval), false, 2); err != nil {
		return nil, s.fail(err)
	}
	fresh, err := s.factsOf(ctx, reval)
	_ = os.Remove(reval)
	if err != nil {
		return nil, s.fail(err)
	}
	if fresh.Digest() != pl.digest {
		return nil, s.fail(ErrReplanRequired)
	}

	s.applied = true
	s.plan = nil // a saved plan is single use, whatever the outcome
	if _, _, err := s.exec(ctx, "apply", []string{"-input=false", "-no-color", s.lockArg(), pl.file}, false); err != nil {
		_ = os.Remove(pl.file)
		return nil, s.fail(err)
	}
	_ = os.Remove(pl.file)
	head, err := s.Head(ctx)
	if err != nil {
		return nil, s.fail(err)
	}
	return &ApplyResult{Head: head}, nil
}

// Close reports the outcome to the fence once, then removes the work directory. It is safe to call
// again. The fence is told even when the run's context is cancelled.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	out := Outcome{Succeeded: !s.failed, Applied: s.applied}
	if s.dir != "" && s.env != nil {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
		head, err := s.Head(hctx)
		cancel()
		if err != nil {
			out.Succeeded = false
		}
		out.Head = head
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	if err := s.r.cfg.Fence.Complete(cctx, s.info, out); err != nil {
		errs = append(errs, fmt.Errorf("tofurun: report the exit to the fence: %w", err))
	}
	cancel()
	if s.dir != "" {
		if err := os.RemoveAll(s.dir); err != nil {
			errs = append(errs, fmt.Errorf("tofurun: remove the work directory: %w", err))
		}
	}
	return errors.Join(errs...)
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func fileSum(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("tofurun: read %s: %w", filepath.Base(path), err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func cliConfig(mirror string) string {
	if mirror == "" {
		// No mirror: nothing may be installed. The built-in provider needs no installation.
		return "provider_installation {\n  direct {\n    exclude = [\"*/*/*\"]\n  }\n}\n"
	}
	return fmt.Sprintf("provider_installation {\n  filesystem_mirror {\n    path = %q\n  }\n  direct {\n    exclude = [\"*/*/*\"]\n  }\n}\n", filepath.ToSlash(mirror))
}
