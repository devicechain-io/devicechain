// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// InstallEngine is `dcctl install` as an installflow.Engine: the run the command
// settles its flags into, executed as the stages installflow.InstallSteps names, in
// the order the install has always run them, reporting each through the run's
// event stream.
//
// It owns the stages and the facts; it draws nothing. What it says in words goes to
// the Console its runtime hands it, and the preflight doctor — which lives in the
// command layer — is reached through the runtime's PreflightRunner.
type InstallEngine struct {
	Provider Provider
	Options  InstallOptions
	// ResolveOptions reads and validates file-backed options after a successful
	// (or skipped) preflight. Options above already supplies the run's target,
	// --dry-run and --yes; the resolved values must agree with that request.
	ResolveOptions func() (InstallOptions, error)
	// SkipPreflight is --skip-preflight. Without it the engine REQUIRES a preflight
	// runner: a run that silently skipped the checks because nobody wired them would
	// be the fail-open this field exists to make impossible.
	SkipPreflight bool
	// Cluster runs the stages from install.cluster on. Nil runs Install. The command
	// holds it in a variable of its own so its characterisation goldens can stand a
	// recording fake in for the cluster work and still exercise everything above it.
	Cluster func(context.Context, Provider, InstallOptions) error
}

// ErrPreflightFailed is the refusal a failed preflight ends an install with.
func ErrPreflightFailed(fails int) error {
	return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", fails)
}

// Run executes the install. Its error is the install's own, unchanged, so a
// presenter that prints it prints what dcctl always printed.
func (e *InstallEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	if req.DryRun != e.Options.DryRun || req.AssumeYes != e.Options.AssumeYes {
		// Two answers to the same question: refuse rather than pick one.
		err := errors.New("install engine: the request and the install options disagree about --dry-run or --yes")
		return failedBeforeStart(rt, err)
	}
	if e.Provider == nil {
		return failedBeforeStart(rt, errors.New("install engine: no provider"))
	}
	r := newInstallRun(rt, e.Options.DryRun)
	// Registered before the first event; a value that could not be covered is said
	// once the run has started, so the warning is part of it.
	var uncovered bytes.Buffer
	registerSecrets(rt.Events, e.Options, &uncovered)
	registerKubeconfigSecrets(rt.Events, &uncovered)
	if err := r.start(installTarget(e.Options)); err != nil {
		return failedBeforeStart(rt, err)
	}
	if uncovered.Len() > 0 {
		_, _ = r.warnings().Write(uncovered.Bytes())
	}
	// 🔴 THE STOPPING STATE. A cancel is reported the moment it is requested, naming
	// the stage it found and whether a tool is still running — and nothing more: the
	// stage is left to stop on its own (an interrupted OpenTofu command gets its
	// graceful stop, a kind child its CommandContext kill), and only when it has
	// returned does close end it and send the summary. Reporting the run over before
	// that would let a presenter quit, and a re-run start, while the work was still
	// changing the cluster.
	//
	// A run cancelled before it began says so here, before its first stage, rather
	// than whenever the watcher's goroutine is scheduled.
	if ctx.Err() != nil {
		r.stopping(installflow.CancelReasonOf(ctx))
	}
	watch := context.AfterFunc(ctx, func() { r.stopping(installflow.CancelReasonOf(ctx)) })
	defer watch()

	// The command settled argv-only checks before the run began. File reads
	// retain their historical position after preflight.
	r.enter(installflow.StepInstallInputs)

	if e.SkipPreflight {
		r.skip(installflow.StepInstallPreflight)
	} else {
		r.enter(installflow.StepInstallPreflight)
		if rt.Preflight == nil {
			return r.close(ctx, errors.New("install engine: a preflight was asked for and no preflight runner was given"))
		}
		res, err := rt.Preflight.RunPreflight(ctx, r.observe)
		if err != nil {
			return r.close(ctx, err)
		}
		if res.Fails > 0 {
			return r.close(ctx, ErrPreflightFailed(res.Fails))
		}
	}

	opts := e.Options
	if e.ResolveOptions != nil {
		r.finishActive()
		if rt.Console.Flush != nil {
			rt.Console.Flush()
		}
		resolved, err := e.ResolveOptions()
		if err != nil {
			return r.close(ctx, err)
		}
		if req.DryRun != resolved.DryRun || req.AssumeYes != resolved.AssumeYes {
			return r.close(ctx, errors.New("install engine: the request and the install options disagree about --dry-run or --yes"))
		}
		// The files read here (an SMTP password, backup credentials) are new values.
		registerSecrets(rt.Events, resolved, r.con.stderr())
		opts = resolved
	}

	cluster := e.Cluster
	if cluster == nil {
		cluster = Install
	}
	return r.close(ctx, cluster(withRun(ctx, r), e.Provider, opts))
}

// registerSecrets tells the hub the secret values an install carries, so that
// every text the run reports — tool output taken as log lines above all — is
// redacted of them.
//
// 🔴 THIS IS DEFENCE IN DEPTH, NOT THE DEFENCE. Nothing the engine emits is meant
// to hold these values in the first place; this is what still catches one when
// a tool echoes it. A value too short for a strict registration is registered
// redact-only, and one too short even for that is warned about on w.
func registerSecrets(events installflow.Reporter, opts InstallOptions, w io.Writer) {
	a := opts.AlertRouting
	values := []string{a.Webhook, a.WatchdogWebhook, a.SMTPPassword, a.Config}
	if b := opts.BackupDestination; b != nil {
		values = append(values, b.AccessKeyID, b.SecretAccessKey)
	}
	var creds []credential
	for _, v := range values {
		if v != "" {
			creds = append(creds, credential{value: v})
		}
	}
	warnUncovered(w, "install secret", registerCredentials(events, creds))
}

// failedBeforeStart closes a run that could not begin. The hub accepts a summary
// with no run.started, so the stream still ends with its one verdict.
func failedBeforeStart(rt installflow.Runtime, err error) (installflow.Summary, error) {
	s := installflow.Summary{
		Outcome: installflow.RunFailed, InstallRecord: installflow.RecordNotRead,
		Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimNotClaimed,
		PartialState: installflow.EffectNoneObserved,
	}
	if rt.Events != nil {
		_ = rt.Events.Finish(s)
	}
	return s, err
}

// installTarget is the run's safe description of where it is going. A context name
// is a label, not an identity, so no cluster UID is claimed here.
func installTarget(opts InstallOptions) installflow.Target {
	if opts.KubeContext != "" {
		return installflow.Target{Mode: "existing-context", Context: installflow.Text(opts.KubeContext)}
	}
	cluster := opts.Cluster
	if cluster == "" {
		cluster = DefaultClusterName
	}
	return installflow.Target{Mode: "managed-kind", Context: installflow.Text(kindContext(cluster))}
}

// installManifest is every stage an install may run, in order. The preflight is
// optional (--skip-preflight), and so is the closing report, which a dry run never
// reaches.
func installManifest() []installflow.ManifestStep {
	var m []installflow.ManifestStep
	for _, id := range installflow.InstallSteps() {
		m = append(m, installflow.ManifestStep{ID: id,
			Optional: id == installflow.StepInstallPreflight || id == installflow.StepInstallSummary})
	}
	return m
}

// readOnlyStage reports whether a stage only reads. It finishes unchanged, in a dry
// run or not: it really ran, and it changed nothing.
func readOnlyStage(id installflow.StepID) bool {
	switch id {
	case installflow.StepInstallInputs, installflow.StepInstallPreflight, installflow.StepInstallInspect:
		return true
	}
	return false
}

// mutatingStage reports whether a stage may change the cluster or this machine from
// its first call on, so an attempt of it starts at may_have_changed.
//
// Two stages are not on the list because they say for themselves when they start to
// change something, and before that they have changed nothing: install.cluster, where
// adopting a context or reusing a kind cluster changes nothing and creating one does
// (mayChange), and install.prerequisites, whose writes are all OpenTofu's — it reads
// and fences until a command that writes state starts (tofuExec marks it). A stage
// whose writes are not individually marked stays on this list: an unmarked write
// reported as none_observed is the one wrong answer an effect must never give.
func mutatingStage(id installflow.StepID) bool {
	switch id {
	case installflow.StepInstallClaim, installflow.StepInstallCredentials, installflow.StepInstallPrepare,
		installflow.StepInstallWriteIntent, installflow.StepInstallOperator,
		installflow.StepInstallDatabaseLogin, installflow.StepInstallRecord:
		return true
	}
	return false
}

// stageProfile is what one operation's stages are, for the run that moves through
// them: the manifest it starts with, which stages only read, which may change something
// from their first call, the waits that bound them, and the closing report a dry run
// skips. install and bootstrap each have one; the run's mechanics are shared.
type stageProfile struct {
	name     string
	manifest func() []installflow.ManifestStep
	readOnly func(installflow.StepID) bool
	mutating func(installflow.StepID) bool
	budget   func(installflow.StepID) int64
	// preflight is the stage preflight observations belong to.
	preflight installflow.StepID
	// closingReport is skipped (not_applicable) by a dry run that did not reach it.
	// Empty when a dry run reaches its report like any other stage.
	closingReport installflow.StepID
}

var installStages = stageProfile{
	name: "install", manifest: installManifest, readOnly: readOnlyStage, mutating: mutatingStage,
	budget: stageBudget, preflight: installflow.StepInstallPreflight,
	closingReport: installflow.StepInstallSummary,
}

// moreCertain orders effect certainty — none_observed, may_have_changed, applied —
// and returns the higher of a and b. An attempt's effect only ever moves up.
func moreCertain(a, b installflow.EffectCertainty) installflow.EffectCertainty {
	rank := func(e installflow.EffectCertainty) int {
		switch e {
		case installflow.EffectNoneObserved:
			return 0
		case installflow.EffectApplied:
			return 2
		}
		return 1 // may_have_changed, and anything unknown
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// installRun is one engine run of the install as the code below the engine sees it:
// its console, its prompts, and the stage it is in. Install moves it from stage to
// stage with enter; the engine closes it with the install's own error.
//
// Every method is safe on a nil receiver, because Install and the helpers it shares
// with bootstrap and upgrade also run with no engine at all.
type installRun struct {
	events  installflow.Reporter
	prompts installflow.PromptBroker
	con     console
	dryRun  bool
	// stages is what the operation's stages are: install's, or bootstrap's.
	stages *stageProfile
	// reveals is Runtime.Reveals, and progressOnly Request.ProgressOnly: where a
	// one-time disclosure may be offered, and whether this run may offer one at all.
	reveals      installflow.RevealSink
	progressOnly bool
	// credentials are the references the summary points at: where a value the run
	// settled is kept, never the value.
	credentials []installflow.SecretRef
	// observeTools is Runtime.ObserveTools: run OpenTofu's apply in -json mode and
	// report its resource actions. Off, the apply runs exactly as it always has.
	observeTools bool

	// outlive is Runtime.Outlive: what a tool the run stops waiting for is held
	// open on, so a re-run is not admitted while it may still be applying.
	outlive func() (ended func())

	// mu guards the stage state below. The run's own goroutine moves it; the cancel
	// watcher and a tool's goroutine read and mark it. Events about a stage are
	// emitted under it, so the stream says what the state was when it says it.
	mu      sync.Mutex
	started bool
	step    installflow.StepID // the active stage; "" between stages
	attempt int                // the active stage's attempt, 1-based
	// effect is what the active attempt has done, so far as the run has seen: it
	// starts at none_observed (may_have_changed for a mutatingStage) and only rises.
	effect installflow.EffectCertainty
	// abandoned: the active attempt stopped waiting for an interrupted tool.
	abandoned bool
	// unanswered: the active attempt asked a question nobody could answer.
	unanswered bool
	last       installflow.StepID // the last stage entered
	changed    bool               // some attempt may have changed something
	applied    bool               // some attempt certainly changed something
	// tools counts the tools running, abandoned ones included; toolStop is the
	// graceful stop the last one started was given.
	tools    int
	toolStop time.Duration

	// stopOnce makes run.cancelling one event, sent by whichever of the cancel
	// watcher and close gets there first — or by neither, once close has sealed it.
	stopOnce sync.Once

	record    installflow.RecordState
	claim     installflow.ClaimState
	readiness installflow.Readiness

	// emitMu guards emitErr: progress is emitted from the goroutine terraform-exec
	// drains a command's stdout on, while every other event comes from the run's own.
	emitMu  sync.Mutex
	emitErr error // the first event the hub refused
}

func newInstallRun(rt installflow.Runtime, dryRun bool) *installRun {
	return &installRun{
		events: rt.Events, prompts: rt.Prompts, con: consoleOf(rt.Console), dryRun: dryRun,
		record: installflow.RecordNotRead, claim: installflow.ClaimNotClaimed,
		readiness: installflow.ReadinessNotChecked, observeTools: rt.ObserveTools,
		outlive: rt.Outlive, effect: installflow.EffectNoneObserved,
		stages: &installStages, reveals: rt.Reveals,
	}
}

func (r *installRun) emit(ev installflow.Event) {
	if r == nil || r.events == nil {
		return
	}
	if err := r.events.Emit(ev); err != nil {
		r.emitMu.Lock()
		if r.emitErr == nil {
			r.emitErr = err
		}
		r.emitMu.Unlock()
	}
}

func (r *installRun) firstEmitErr() error {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	return r.emitErr
}

func (r *installRun) start(t installflow.Target) error {
	if r.events == nil {
		return fmt.Errorf("%s engine: no event reporter", r.stages.name)
	}
	if err := r.events.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: t, DryRun: r.dryRun, Manifest: r.stages.manifest()}}); err != nil {
		return fmt.Errorf("%s engine: starting the run: %w", r.stages.name, err)
	}
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	return nil
}

// enter closes the active stage as finished and starts id.
func (r *installRun) enter(id installflow.StepID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return
	}
	r.finishActiveLocked()
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepStarted{
		Message: installflow.MessageCode(id), BudgetMS: r.stages.budget(id)}})
	r.step, r.attempt, r.last = id, 1, id
	r.beginAttemptLocked()
}

// beginAttemptLocked resets what the active attempt is known to have done.
func (r *installRun) beginAttemptLocked() {
	r.effect, r.abandoned, r.unanswered = installflow.EffectNoneObserved, false, false
	if r.stages.mutating(r.step) {
		r.raiseLocked(installflow.EffectMayHaveChanged)
	}
}

// raiseLocked records that the active attempt has done at least e. A rehearsal
// changes nothing, whatever it reaches.
func (r *installRun) raiseLocked(e installflow.EffectCertainty) {
	if r.dryRun || r.step == "" {
		return
	}
	r.effect = moreCertain(r.effect, e)
	switch r.effect {
	case installflow.EffectApplied:
		r.applied, r.changed = true, true
	case installflow.EffectMayHaveChanged:
		r.changed = true
	}
}

func (r *installRun) raise(e installflow.EffectCertainty) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raiseLocked(e)
}

// skip records a stage that does not apply to this run.
func (r *installRun) skip(id installflow.StepID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skipLocked(id)
}

func (r *installRun) skipLocked(id installflow.StepID) {
	if !r.started {
		return
	}
	r.finishActiveLocked()
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepStarted{Message: installflow.MessageCode(id)}})
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepNotApplicable}})
}

// mayChange says the active stage is about to change something it does not change
// on every run: creating a kind cluster, as opposed to adopting one; starting an
// OpenTofu command that writes state.
func (r *installRun) mayChange() { r.raise(installflow.EffectMayHaveChanged) }

// changeApplied says the active stage's change has certainly been made: objects the
// API server accepted, an OpenTofu apply that returned success.
func (r *installRun) changeApplied() { r.raise(installflow.EffectApplied) }

// stopping is the stopping state's one announcement: the run has been asked to stop,
// this is the stage it found, and this is whether a tool is still running and how
// long that tool was given to stop. It changes nothing else — the stage goes on until
// its work returns, and close then ends it.
func (r *installRun) stopping(reason installflow.CancelReason) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		c := installflow.RunCancelling{Reason: reason, ActiveStep: r.step, WaitingForTool: r.tools > 0}
		if c.WaitingForTool {
			c.StopBudgetMS = r.toolStop.Milliseconds()
		}
		r.emit(installflow.Event{Payload: c})
	})
}

func (r *installRun) finishActive() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishActiveLocked()
}

func (r *installRun) finishActiveLocked() {
	if r.step == "" {
		return
	}
	outcome := installflow.StepSucceeded
	switch {
	case r.stages.readOnly(r.step):
		outcome = installflow.StepUnchanged
	case r.dryRun:
		outcome = installflow.StepPlanned
	}
	r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: installflow.StepFinished{Outcome: outcome}})
	r.step = ""
}

// observe forwards one preflight observation into the stream.
func (r *installRun) observe(o installflow.PreflightObservation) {
	r.emit(installflow.Event{Step: r.stages.preflight, Attempt: 1, Payload: o})
}

// The facts the summary reports, set where they become true.
func (r *installRun) claimHeld() {
	if r != nil {
		r.mu.Lock()
		r.claim = installflow.ClaimHeld
		r.mu.Unlock()
	}
}

func (r *installRun) claimReleased() {
	if r != nil {
		r.mu.Lock()
		if r.claim == installflow.ClaimHeld {
			r.claim = installflow.ClaimReleased
		}
		r.mu.Unlock()
	}
}

func (r *installRun) recordIs(s installflow.RecordState) {
	if r != nil {
		r.mu.Lock()
		r.record = s
		r.mu.Unlock()
	}
}

func (r *installRun) readinessIs(s installflow.Readiness) {
	if r != nil {
		r.mu.Lock()
		r.readiness = s
		r.mu.Unlock()
	}
}

// confirmCreateCluster asks whether to create the missing kind cluster: through the
// run's prompt broker inside an engine run, and on the terminal as it always was
// outside one.
func (r *installRun) confirmCreateCluster(ctx context.Context, cluster string) bool {
	if r == nil || r.prompts == nil {
		return confirm(fmt.Sprintf("No kind cluster %q found. Create it now?", cluster))
	}
	ans, err := r.prompts.Ask(ctx, installflow.Prompt{
		Step: installflow.StepInstallCluster, Kind: installflow.PromptConfirm,
		Question:      installflow.QuestionCreateKindCluster,
		Params:        []installflow.Param{{Name: "cluster", Value: installflow.Text(cluster)}},
		YesResolvable: true,
	})
	if errors.Is(err, installflow.ErrPromptUnavailable) {
		r.mu.Lock()
		r.unanswered = true
		r.mu.Unlock()
	}
	return err == nil && ans.Confirmed
}

// close ends the run with the install's own error and sends its summary.
//
// 🔴 SUCCESS IS THE INSTALL'S RETURN VALUE AND NOTHING ELSE. A stage reached, a
// record written or a full progress count is not success; only Install returning nil
// — after the relational store's wait, which is its last gate — is.
//
// 🔴 AND IT IS REACHED ONLY ONCE THE WORK HAS SETTLED. A cancel does not end the run:
// it is announced (stopping) and the stage's own work is left to stop. close runs
// when that work has returned — the tool exited, or was given up on after its budget
// — so the summary can never say the run is over while a stage is still running.
func (r *installRun) close(ctx context.Context, err error) (installflow.Summary, error) {
	if ctx.Err() != nil {
		// Said before the stage it ends, if the watcher has not said it already.
		r.stopping(installflow.CancelReasonOf(ctx))
	} else {
		// Sealed: a cancel that lands after the verdict is not a stop of this run.
		r.stopOnce.Do(func() {})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := installflow.Summary{
		InstallRecord: r.record, Readiness: r.readiness, Claim: r.claim,
		PartialState: installflow.EffectNoneObserved,
		Credentials:  append([]installflow.SecretRef(nil), r.credentials...),
	}
	if r.changed {
		s.PartialState = installflow.EffectMayHaveChanged
	}
	if r.applied || r.record == installflow.RecordInstalled {
		s.PartialState = installflow.EffectApplied
	}
	if err == nil {
		// Close what the install left open before judging the stream, so a refused
		// final event counts against the run like any other.
		r.finishActiveLocked()
		if c := r.stages.closingReport; c != "" && r.dryRun && r.last != c {
			r.skipLocked(c)
		}
		if emitErr := r.firstEmitErr(); emitErr != nil {
			err = fmt.Errorf("reporting the install's progress: %w", emitErr)
		}
	}
	switch {
	case err != nil:
		cancelled := ctx.Err() != nil
		s.Outcome = installflow.RunFailed
		if cancelled {
			s.Outcome = installflow.RunCancelled
		}
		s.FailedStep = r.step // "" when the error came between stages, so none is blamed
		if r.step != "" {
			f := installflow.StepFailed{Code: installflow.ErrorCode(string(r.step) + ".failed"),
				Retry: installflow.RetryUnknown, Cancelled: cancelled, Effect: r.effect}
			switch {
			case r.abandoned:
				// Never none_observed: the command was not seen to finish.
				f.Code, f.Remedy = installflow.ErrorToolAbandoned, installflow.RemedyRerunSameCommand
				f.Effect = moreCertain(f.Effect, installflow.EffectMayHaveChanged)
			case cancelled:
				f.Code, f.Remedy = installflow.ErrorRunCancelled, installflow.RemedyRerunSameCommand
			case r.unanswered:
				// Asked before the change it was about, so the attempt's own effect
				// stands: none_observed unless something before the question wrote.
				f.Code, f.Remedy = installflow.ErrorPromptUnavailable, installflow.RemedyRerunWithYes
				f.Retry = installflow.RetryPermanent
			}
			s.PartialState = moreCertain(s.PartialState, f.Effect)
			r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: f})
			r.step = ""
		}
	case r.dryRun:
		s.Outcome = installflow.RunPlanned
	default:
		s.Outcome = installflow.RunSucceeded
	}
	if ferr := r.events.Finish(s); ferr != nil && err == nil {
		err = fmt.Errorf("reporting the install's outcome: %w", ferr)
		s.Outcome = installflow.RunFailed
	}
	return s, err
}

// stageBudget is the wait a stage is bounded by, where it has one, for a presenter
// to show. It bounds nothing itself.
func stageBudget(id installflow.StepID) int64 {
	if id == installflow.StepInstallRDBReady {
		return cnpgClusterReadyTimeout.Milliseconds()
	}
	return 0
}
