// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	if err := r.start(installTarget(e.Options)); err != nil {
		return failedBeforeStart(rt, err)
	}

	// The flags were settled by the command before the run began.
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

	cluster := e.Cluster
	if cluster == nil {
		cluster = Install
	}
	return r.close(ctx, cluster(withRun(ctx, r), e.Provider, e.Options))
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

// mutatingStage reports whether a stage may change the cluster or this machine.
// install.cluster is not one by default — adopting a context or reusing a kind
// cluster changes nothing — and says so itself when it creates one (mayChange).
func mutatingStage(id installflow.StepID) bool {
	switch id {
	case installflow.StepInstallClaim, installflow.StepInstallCredentials, installflow.StepInstallPrepare,
		installflow.StepInstallWriteIntent, installflow.StepInstallOperator, installflow.StepInstallPrerequisites,
		installflow.StepInstallDatabaseLogin, installflow.StepInstallRecord:
		return true
	}
	return false
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
	// observeTools is Runtime.ObserveTools: run OpenTofu's apply in -json mode and
	// report its resource actions. Off, the apply runs exactly as it always has.
	observeTools bool

	started  bool
	step     installflow.StepID // the active stage; "" between stages
	attempt  int                // the active stage's attempt, 1-based
	stepMuts bool               // the active stage may change something
	last     installflow.StepID // the last stage entered
	changed  bool               // a stage that may change something has been entered

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
		return errors.New("install engine: no event reporter")
	}
	if err := r.events.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: t, DryRun: r.dryRun, Manifest: installManifest()}}); err != nil {
		return fmt.Errorf("install engine: starting the run: %w", err)
	}
	r.started = true
	return nil
}

// enter closes the active stage as finished and starts id.
func (r *installRun) enter(id installflow.StepID) {
	if r == nil || !r.started {
		return
	}
	r.finishActive()
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepStarted{
		Message: installflow.MessageCode(id), BudgetMS: stageBudget(id)}})
	r.step, r.attempt, r.last, r.stepMuts = id, 1, id, mutatingStage(id)
	if r.stepMuts && !r.dryRun {
		r.changed = true
	}
}

// skip records a stage that does not apply to this run.
func (r *installRun) skip(id installflow.StepID) {
	if r == nil || !r.started {
		return
	}
	r.finishActive()
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepStarted{Message: installflow.MessageCode(id)}})
	r.emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepNotApplicable}})
}

// mayChange says the active stage is about to change something it does not change
// on every run: creating a kind cluster, as opposed to adopting one.
func (r *installRun) mayChange() {
	if r == nil || r.dryRun || r.step == "" {
		return
	}
	r.stepMuts, r.changed = true, true
}

func (r *installRun) finishActive() {
	if r.step == "" {
		return
	}
	outcome := installflow.StepSucceeded
	switch {
	case readOnlyStage(r.step):
		outcome = installflow.StepUnchanged
	case r.dryRun:
		outcome = installflow.StepPlanned
	}
	r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: installflow.StepFinished{Outcome: outcome}})
	r.step = ""
}

// observe forwards one preflight observation into the stream.
func (r *installRun) observe(o installflow.PreflightObservation) {
	r.emit(installflow.Event{Step: installflow.StepInstallPreflight, Attempt: 1, Payload: o})
}

// The facts the summary reports, set where they become true.
func (r *installRun) claimHeld() {
	if r != nil {
		r.claim = installflow.ClaimHeld
	}
}

func (r *installRun) claimReleased() {
	if r != nil && r.claim == installflow.ClaimHeld {
		r.claim = installflow.ClaimReleased
	}
}

func (r *installRun) recordIs(s installflow.RecordState) {
	if r != nil {
		r.record = s
	}
}

func (r *installRun) readinessIs(s installflow.Readiness) {
	if r != nil {
		r.readiness = s
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
	return err == nil && ans.Confirmed
}

// close ends the run with the install's own error and sends its summary.
//
// 🔴 SUCCESS IS THE INSTALL'S RETURN VALUE AND NOTHING ELSE. A stage reached, a
// record written or a full progress count is not success; only Install returning nil
// — after the relational store's wait, which is its last gate — is.
func (r *installRun) close(ctx context.Context, err error) (installflow.Summary, error) {
	s := installflow.Summary{
		InstallRecord: r.record, Readiness: r.readiness, Claim: r.claim,
		PartialState: installflow.EffectNoneObserved,
	}
	if r.changed {
		s.PartialState = installflow.EffectMayHaveChanged
	}
	if r.record == installflow.RecordInstalled {
		s.PartialState = installflow.EffectApplied
	}
	if err == nil {
		// Close what the install left open before judging the stream, so a refused
		// final event counts against the run like any other.
		r.finishActive()
		if r.dryRun && r.last != installflow.StepInstallSummary {
			r.skip(installflow.StepInstallSummary)
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
			code := installflow.ErrorCode(string(r.step) + ".failed")
			if cancelled {
				code = "run.cancelled"
			}
			effect := installflow.EffectNoneObserved
			if r.stepMuts {
				effect = installflow.EffectMayHaveChanged
			}
			r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: installflow.StepFailed{
				Code: code, Retry: installflow.RetryUnknown, Cancelled: cancelled, Effect: effect}})
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
