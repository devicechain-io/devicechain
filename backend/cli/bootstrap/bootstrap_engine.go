// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// BootstrapEngine is `dcctl bootstrap` as an installflow.Engine: the run the command
// settles its arguments into, executed as the stages installflow.BootstrapSteps names —
// the command's own preflight, escrow plan and cluster, then the pipeline's eleven
// steps one for one — in the order a bootstrap has always run them, reporting each
// through the run's event stream.
//
// It owns the stages and the facts; it draws nothing. What it says in words goes to the
// Console its runtime hands it; the preflight doctor, which lives in the command layer,
// is reached through the runtime's PreflightRunner; and the cluster work that only the
// command layer can do (it reads flags this package does not own) is the Target
// callback.
//
// 🔴 TWO VALUES NEVER TRAVEL AS EVENTS. The escrow passphrase reaches the escrow plan
// through the runtime's prompt broker, as a secret answer, when a person types it, and
// through the plan alone when a file or the environment supplied it. The generated
// superuser seed is offered to the presenter as a one-use installflow.Reveal and only
// when printSuperuserReport would have shown it; the summary carries where it is kept.
type BootstrapEngine struct {
	// Instance is the instance being built.
	Instance string
	// DryRun and AssumeYes are --dry-run and --yes, which the request must agree with.
	DryRun    bool
	AssumeYes bool
	// KubeContext and Cluster describe the target for the run's first event. A context
	// name is a label, not an identity.
	KubeContext string
	Cluster     string
	// SkipPreflight is --skip-preflight. Without it the engine REQUIRES a preflight
	// runner, for the reason InstallEngine does.
	SkipPreflight bool
	// Escrow is the escrow flags. The plan is resolved in the bootstrap.escrow stage,
	// after the preflight and before any cluster, as it always was.
	Escrow EscrowFlags
	// Target settles the cluster the instance is built on and returns the State the
	// pipeline runs from, given the escrow plan. It runs in the bootstrap.cluster stage,
	// with the run on its context, so what it prints goes to the run's console.
	Target func(ctx context.Context, con Console, plan EscrowPlan) (*State, error)
	// Pipeline is the steps. Every step name must have a bootstrap stage
	// (bootstrapStageOf); a step without one fails the run rather than running
	// unreported.
	Pipeline Pipeline
	// Finish runs after the pipeline, whatever it returned, and returns the run's error:
	// the command layer releases the claim and unwinds a local record that describes
	// nothing.
	Finish func(ctx context.Context, con Console, st *State, runErr error) error
	// PersonAtTerminal, when set, reports whether a person is at a terminal to be asked
	// for the escrow passphrase, in place of looking at the process's stdin. A
	// presenter that owns the terminal while the run is live (the terminal UI, which
	// puts the null device on stdin so no child can read it) sets it; nil keeps the
	// stdin check every plain run has always made. It never moves the file and the
	// environment from ahead of a person.
	PersonAtTerminal func() bool
}

// Console is where the command-layer callbacks of an engine print: the run's stdout,
// for the plain presenter the process's own.
type Console struct{ c console }

// Println prints a line, as fmt.Println.
func (c Console) Println(a ...any) { c.c.println(a...) }

// Printf prints, as fmt.Printf.
func (c Console) Printf(format string, a ...any) { c.c.printf(format, a...) }

// bootstrapStageOf is each pipeline step's stage, by the step's name. It is the one
// place the names the banners print are tied to the IDs the events carry, and
// TestEveryPipelineStepHasItsOwnStage holds it to NewDefaultPipeline.
var bootstrapStageOf = map[string]installflow.StepID{
	"Ensure local registry":           installflow.StepBootstrapRegistry,
	"Claim the cluster":               installflow.StepBootstrapClaim,
	"Refuse a rebuild":                installflow.StepBootstrapRefuseRebuild,
	"Check what other instances hold": installflow.StepBootstrapSingletons,
	"Declare the instance":            installflow.StepBootstrapDeclare,
	"Render configuration":            installflow.StepBootstrapRender,
	"Apply infrastructure":            installflow.StepBootstrapInfrastructure,
	"Install instance (Helm)":         installflow.StepBootstrapHelm,
	"Wait for readiness":              installflow.StepBootstrapReadiness,
	"Even out broker leadership":      installflow.StepBootstrapBrokerBalance,
	"Report access info":              installflow.StepBootstrapReport,
}

var bootstrapStages = stageProfile{
	name: "bootstrap",
	manifest: func() []installflow.ManifestStep {
		var m []installflow.ManifestStep
		for _, id := range installflow.BootstrapSteps() {
			m = append(m, installflow.ManifestStep{ID: id, Optional: id == installflow.StepBootstrapPreflight})
		}
		return m
	},
	readOnly: func(id installflow.StepID) bool {
		switch id {
		case installflow.StepBootstrapInputs, installflow.StepBootstrapPreflight, installflow.StepBootstrapEscrow,
			installflow.StepBootstrapRefuseRebuild, installflow.StepBootstrapSingletons,
			installflow.StepBootstrapReadiness, installflow.StepBootstrapReport:
			return true
		}
		return false
	},
	// Every stage that writes anything, here or in the cluster, from its first call: the
	// cluster stage writes the local instance record, the registry stage builds and
	// pushes images, the render stage writes the escrow artifact. None marks its own
	// writes, so none may start at none_observed.
	mutating: func(id installflow.StepID) bool {
		switch id {
		case installflow.StepBootstrapCluster, installflow.StepBootstrapRegistry, installflow.StepBootstrapClaim,
			installflow.StepBootstrapDeclare, installflow.StepBootstrapRender, installflow.StepBootstrapInfrastructure,
			installflow.StepBootstrapHelm, installflow.StepBootstrapBrokerBalance:
			return true
		}
		return false
	},
	budget:    func(installflow.StepID) int64 { return 0 },
	preflight: installflow.StepBootstrapPreflight,
}

// Run executes the bootstrap. Its error is the bootstrap's own, unchanged, so a presenter
// that prints it prints what dcctl always printed.
func (e *BootstrapEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	if req.DryRun != e.DryRun || req.AssumeYes != e.AssumeYes {
		return failedBeforeStart(rt, errors.New("bootstrap engine: the request and the bootstrap options disagree about --dry-run or --yes"))
	}
	if e.Target == nil {
		return failedBeforeStart(rt, errors.New("bootstrap engine: no target"))
	}
	r := newInstallRun(rt, e.DryRun)
	r.stages, r.progressOnly = &bootstrapStages, req.ProgressOnly
	// The kubeconfig's credentials, as the install engine registers them; a value too
	// short to redact is said once the run has started.
	var uncovered bytes.Buffer
	registerKubeconfigSecrets(rt.Events, &uncovered)
	if err := r.start(e.target()); err != nil {
		return failedBeforeStart(rt, err)
	}
	if uncovered.Len() > 0 {
		_, _ = r.warnings().Write(uncovered.Bytes())
	}
	// The stopping state, exactly as the install engine has it: announced the moment a
	// cancel is asked for, and the run closed only once the stage's work has returned.
	if ctx.Err() != nil {
		r.stopping(installflow.CancelReasonOf(ctx))
	}
	watch := context.AfterFunc(ctx, func() { r.stopping(installflow.CancelReasonOf(ctx)) })
	defer watch()
	rctx := withRun(ctx, r)

	// The command settled every argv-only check before the run began.
	r.enter(installflow.StepBootstrapInputs)

	if e.SkipPreflight {
		r.skip(installflow.StepBootstrapPreflight)
	} else {
		r.enter(installflow.StepBootstrapPreflight)
		if rt.Preflight == nil {
			return r.close(ctx, errors.New("bootstrap engine: a preflight was asked for and no preflight runner was given"))
		}
		res, err := rt.Preflight.RunPreflight(ctx, r.observe)
		if err != nil {
			return r.close(ctx, err)
		}
		if res.Fails > 0 {
			return r.close(ctx, ErrPreflightFailed(res.Fails))
		}
	}

	// Settled LAST among the up-front checks and before any cluster, as it always was:
	// this is the one stage that may stop and ask a person something.
	r.enter(installflow.StepBootstrapEscrow)
	flags := e.Escrow
	flags.con = r.con
	flags.ask = func(q escrowQuestion) (string, error) { return r.askPassphrase(ctx, q) }
	flags.atTerminal = e.PersonAtTerminal
	plan, err := ResolveEscrowPlan(e.Instance, flags)
	if err != nil {
		return r.close(ctx, err)
	}
	r.addSecrets(plan.Passphrase, plan.RestoredRootKey)

	r.enter(installflow.StepBootstrapCluster)
	st, err := e.Target(rctx, Console{c: r.con}, plan)
	if err != nil {
		return r.close(ctx, err)
	}
	if st.Install != nil {
		r.recordIs(installflow.RecordUnchanged)
	}

	runErr := e.Pipeline.Run(rctx, st)
	if runErr == nil && !st.DryRun && st.Credentials != nil &&
		(st.SuperuserSeed == superuserSeedMinted || st.SuperuserSeed == superuserSeedRecovered) {
		ref := superuserSecretRef(st.Instance)
		r.credentials = append(r.credentials, installflow.SecretRef{Namespace: ref.Namespace, Name: ref.Name, Key: ref.Key})
	}
	if e.Finish != nil {
		runErr = e.Finish(rctx, Console{c: r.con}, st, runErr)
	}
	if st.Claim != nil {
		r.claimReleased()
	}
	return r.close(ctx, runErr)
}

// target is the run's safe description of where it is going.
func (e *BootstrapEngine) target() installflow.Target {
	if e.KubeContext != "" {
		return installflow.Target{Mode: "existing-context", Context: installflow.Text(e.KubeContext)}
	}
	cluster := e.Cluster
	if cluster == "" {
		cluster = DefaultClusterName
	}
	return installflow.Target{Mode: "managed-kind", Context: installflow.Text(kindContext(cluster))}
}

// enterPipelineStep starts the stage a pipeline step reports as. Outside an engine run,
// or in a run whose stages are not bootstrap's, it does nothing.
func (r *installRun) enterPipelineStep(name string) error {
	if r == nil || r.stages != &bootstrapStages {
		return nil
	}
	id, ok := bootstrapStageOf[name]
	if !ok {
		return fmt.Errorf("bootstrap engine: step %q has no stage", name)
	}
	r.enter(id)
	return nil
}

// pipelineStepDone records what a step that returned nil has made true, and tells the
// hub the secret values it has settled so that nothing the run prints from here on can
// carry them.
func (r *installRun) pipelineStepDone(name string, st *State) {
	if r == nil || r.stages != &bootstrapStages {
		return
	}
	switch bootstrapStageOf[name] {
	case installflow.StepBootstrapClaim:
		if st.Claim != nil {
			r.claimHeld()
		}
	case installflow.StepBootstrapReadiness:
		if !st.DryRun {
			r.readinessIs(installflow.ReadinessReady)
		}
	}
	r.addSecrets(st.Values["secretsRootKey"], st.Escrow.Passphrase, st.Escrow.RestoredRootKey)
	if c := st.Credentials; c != nil {
		r.addSecrets(c.RDBPassword, c.RDBProvisionerPassword, c.RDBInstancePassword, c.TSDBPassword,
			c.ObjectStoreUser, c.ObjectStoreSecret, c.GrafanaAdminPassword, c.SuperuserPassword)
	}
}

// addSecrets registers values with the hub's redactor: defence in depth behind the rule
// that nothing the engine emits holds them. A value too short for a strict registration
// is registered redact-only; one too short even for that is warned about, never
// skipped silently.
func (r *installRun) addSecrets(values ...string) {
	if r == nil || r.events == nil {
		return
	}
	var creds []credential
	for _, v := range values {
		if v != "" {
			creds = append(creds, credential{value: v})
		}
	}
	warnUncovered(r.warnings(), "instance secret", registerCredentials(r.events, creds))
}

// askPassphrase asks a person for an escrow passphrase through the run's prompt broker:
// the secret channel. The answer comes back as an installflow.Secret, is copied into the
// plan, and is cleared. Whether it is empty, and whether a confirmation matches, are
// judged here, in the words the terminal prompt always used.
func (r *installRun) askPassphrase(ctx context.Context, q escrowQuestion) (string, error) {
	if r.prompts == nil {
		return promptPassphrase(q.purpose, q.confirm)
	}
	first, err := r.askSecret(ctx, q.code, q.params)
	if err != nil {
		if errors.Is(err, installflow.ErrInvalidAnswer) {
			return "", errEmptyPassphrase
		}
		return "", err
	}
	if !q.confirm {
		return first, nil
	}
	second, err := r.askSecret(ctx, installflow.QuestionEscrowPassphraseConfirm, nil)
	if err != nil {
		if errors.Is(err, installflow.ErrInvalidAnswer) {
			return "", errPassphraseMismatch
		}
		return "", err
	}
	if first != second {
		return "", errPassphraseMismatch
	}
	return first, nil
}

func (r *installRun) askSecret(ctx context.Context, code installflow.MessageCode, params []installflow.Param) (string, error) {
	ans, err := r.prompts.Ask(ctx, installflow.Prompt{
		Step: installflow.StepBootstrapEscrow, Kind: installflow.PromptSecret, Question: code, Params: params,
	})
	defer ans.Secret.Zero()
	if err != nil {
		return "", err
	}
	return string(ans.Secret.Expose()), nil
}

// offerReveal offers the presenter a one-time disclosure of value, and reports whether
// it did. It does not when the presenter takes none, or the run shows progress only.
func (r *installRun) offerReveal(o installflow.RevealOffer, value string) bool {
	if r == nil || r.reveals == nil || r.progressOnly {
		return false
	}
	o.Reveal = installflow.NewReveal([]byte(value))
	r.reveals.Offer(o)
	return true
}
