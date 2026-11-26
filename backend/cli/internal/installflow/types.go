// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"fmt"
	"regexp"
)

// Schema is the version of the event protocol every Envelope carries.
const Schema = "dcctl.install.events/v1"

// Operation is the dcctl command a run belongs to. Only install emits today;
// the others are reserved so the vocabulary does not have to move later.
type Operation string

const (
	OperationInstall   Operation = "install"
	OperationBootstrap Operation = "bootstrap"
	OperationUpgrade   Operation = "upgrade"
	OperationDestroy   Operation = "destroy"
)

// Kind names the payload an Envelope carries. It is derived from the payload's
// concrete type, so an envelope can never claim one kind and carry another.
type Kind string

const (
	KindRunStarted      Kind = "run.started"
	KindStepStarted     Kind = "step.started"
	KindStepProgress    Kind = "step.progress"
	KindStepFinished    Kind = "step.finished"
	KindStepFailed      Kind = "step.failed"
	KindLogLine         Kind = "log.line"
	KindPromptRequested Kind = "prompt.requested"
	KindPromptResolved  Kind = "prompt.resolved"
	KindRunCancelling   Kind = "run.cancelling"
	KindRunSummary      Kind = "run.summary"
)

// isControl reports whether events of this kind are control events: bounded,
// never coalesced and never dropped to make room. Logs and progress are not.
func (k Kind) isControl() bool { return k != KindLogLine && k != KindStepProgress }

// StepID is a stable step identifier, e.g. install.cluster.
type StepID string

// The install stages, in the order the engine runs them today.
const (
	StepInstallInputs        StepID = "install.inputs"
	StepInstallPreflight     StepID = "install.preflight"
	StepInstallCluster       StepID = "install.cluster"
	StepInstallClaim         StepID = "install.claim"
	StepInstallInspect       StepID = "install.inspect"
	StepInstallCredentials   StepID = "install.credentials"
	StepInstallPrepare       StepID = "install.prepare"
	StepInstallWriteIntent   StepID = "install.write-intent"
	StepInstallOperator      StepID = "install.operator"
	StepInstallPrerequisites StepID = "install.prerequisites"
	StepInstallDatabaseLogin StepID = "install.database-login"
	StepInstallRecord        StepID = "install.record"
	StepInstallRDBReady      StepID = "install.rdb-ready"
	StepInstallSummary       StepID = "install.summary"
)

// InstallSteps returns the install stage IDs in execution order. It returns a
// fresh slice, so a caller cannot reorder the canonical list.
func InstallSteps() []StepID {
	return []StepID{
		StepInstallInputs, StepInstallPreflight, StepInstallCluster, StepInstallClaim,
		StepInstallInspect, StepInstallCredentials, StepInstallPrepare, StepInstallWriteIntent,
		StepInstallOperator, StepInstallPrerequisites, StepInstallDatabaseLogin,
		StepInstallRecord, StepInstallRDBReady, StepInstallSummary,
	}
}

// The bootstrap stages, in the order `dcctl bootstrap` runs them. The first four are
// the command's own work before the pipeline: the arguments, the preflight, the
// root-key escrow plan (the one stage that may ask for a secret) and the cluster the
// instance is built on. The other eleven are the pipeline's steps, one for one.
const (
	StepBootstrapInputs         StepID = "bootstrap.inputs"
	StepBootstrapPreflight      StepID = "bootstrap.preflight"
	StepBootstrapEscrow         StepID = "bootstrap.escrow"
	StepBootstrapCluster        StepID = "bootstrap.cluster"
	StepBootstrapRegistry       StepID = "bootstrap.registry"
	StepBootstrapClaim          StepID = "bootstrap.claim"
	StepBootstrapRefuseRebuild  StepID = "bootstrap.refuse-rebuild"
	StepBootstrapSingletons     StepID = "bootstrap.singletons"
	StepBootstrapDeclare        StepID = "bootstrap.declare"
	StepBootstrapRender         StepID = "bootstrap.render"
	StepBootstrapInfrastructure StepID = "bootstrap.infrastructure"
	StepBootstrapHelm           StepID = "bootstrap.helm"
	StepBootstrapReadiness      StepID = "bootstrap.readiness"
	StepBootstrapBrokerBalance  StepID = "bootstrap.broker-balance"
	StepBootstrapReport         StepID = "bootstrap.report"
)

// BootstrapSteps returns the bootstrap stage IDs in execution order, as a fresh slice.
func BootstrapSteps() []StepID {
	return []StepID{
		StepBootstrapInputs, StepBootstrapPreflight, StepBootstrapEscrow, StepBootstrapCluster,
		StepBootstrapRegistry, StepBootstrapClaim, StepBootstrapRefuseRebuild, StepBootstrapSingletons,
		StepBootstrapDeclare, StepBootstrapRender, StepBootstrapInfrastructure, StepBootstrapHelm,
		StepBootstrapReadiness, StepBootstrapBrokerBalance, StepBootstrapReport,
	}
}

// MessageCode identifies a message for a presenter to render from its own
// catalogue. Codes, step IDs, metric IDs, sources and prompt IDs share one
// identifier grammar, checked at the hub, so free text cannot ride in them.
type MessageCode string

// QuestionCreateKindCluster asks whether to create the managed kind cluster an
// install found missing. Param "cluster" names it. --yes answers it.
const QuestionCreateKindCluster MessageCode = "install.create-kind-cluster"

// The root-key escrow's secret questions. Each is a PromptSecret; --yes answers none of
// them, and the answer never travels in an event.
const (
	// QuestionEscrowPassphrase asks for the passphrase a new escrow artifact is written
	// under. Param "instance" names the instance. QuestionEscrowPassphraseConfirm follows
	// it, because a mistyped passphrase on the write side is found out years later.
	QuestionEscrowPassphrase MessageCode = "bootstrap.escrow-passphrase"
	// QuestionEscrowPassphraseConfirm asks for the same passphrase again.
	QuestionEscrowPassphraseConfirm MessageCode = "bootstrap.escrow-passphrase-confirm"
	// QuestionEscrowRestorePassphrase asks for the passphrase of the artifact a root key
	// is restored from. Param "artifact" is its path. It is asked once: a wrong one fails
	// to open the artifact, which says so.
	QuestionEscrowRestorePassphrase MessageCode = "bootstrap.escrow-restore-passphrase"
)

// ErrorCode is a stable, safe identifier for a failure.
type ErrorCode string

// PromptID is the opaque identifier of one prompt.
type PromptID string

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func validIdentifier(field, v string) error {
	if !identifierPattern.MatchString(v) {
		return fmt.Errorf("%w: %s is not an identifier", ErrInvalidEvent, field)
	}
	return nil
}

// enum is implemented by every closed string vocabulary below, so the hub can
// check them all the same way.
type enum interface{ valid() bool }

func checkEnum(field string, e enum) error {
	if !e.valid() {
		return fmt.Errorf("%w: %s has an unknown value", ErrInvalidEvent, field)
	}
	return nil
}

func oneOf[T ~string](v T, set ...T) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// ProgressMode says whether a progress value has a denominator.
type ProgressMode string

const (
	ProgressIndeterminate ProgressMode = "indeterminate"
	ProgressMeasured      ProgressMode = "measured"
)

func (m ProgressMode) valid() bool { return oneOf(m, ProgressIndeterminate, ProgressMeasured) }

// Unit is what a progress value counts.
type Unit string

const (
	UnitChecks      Unit = "checks"
	UnitResources   Unit = "resources"
	UnitActions     Unit = "actions"
	UnitDeployments Unit = "deployments"
	UnitReplicas    Unit = "replicas"
)

func (u Unit) valid() bool {
	return oneOf(u, UnitChecks, UnitResources, UnitActions, UnitDeployments, UnitReplicas)
}

// StepOutcome is how a step attempt finished without failing.
type StepOutcome string

const (
	// StepSucceeded is never used for a simulated write.
	StepSucceeded     StepOutcome = "succeeded"
	StepUnchanged     StepOutcome = "unchanged"
	StepPlanned       StepOutcome = "planned"
	StepNotApplicable StepOutcome = "not_applicable"
)

func (o StepOutcome) valid() bool {
	return oneOf(o, StepSucceeded, StepUnchanged, StepPlanned, StepNotApplicable)
}

// RetryClass says whether running the step again could help.
type RetryClass string

const (
	RetryTransient RetryClass = "transient"
	RetryPermanent RetryClass = "permanent"
	RetryUnknown   RetryClass = "unknown"
)

func (r RetryClass) valid() bool { return oneOf(r, RetryTransient, RetryPermanent, RetryUnknown) }

// EffectCertainty is what is known about changes a failed step made.
type EffectCertainty string

const (
	EffectNoneObserved   EffectCertainty = "none_observed"
	EffectMayHaveChanged EffectCertainty = "may_have_changed"
	EffectApplied        EffectCertainty = "applied"
)

func (e EffectCertainty) valid() bool {
	return oneOf(e, EffectNoneObserved, EffectMayHaveChanged, EffectApplied)
}

// LogLevel is the severity of a log line.
type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

func (l LogLevel) valid() bool { return oneOf(l, LogDebug, LogInfo, LogWarn, LogError) }

// PromptKind is the shape of answer a prompt wants.
type PromptKind string

const (
	PromptConfirm PromptKind = "confirm"
	PromptChoice  PromptKind = "choice"
	PromptSecret  PromptKind = "secret"
)

func (k PromptKind) valid() bool { return oneOf(k, PromptConfirm, PromptChoice, PromptSecret) }

// PromptDisposition is how a prompt was resolved. It is all a resolution
// event says: never the answer, and never the answer's length.
type PromptDisposition string

const (
	PromptConfirmed     PromptDisposition = "confirmed"
	PromptDeclined      PromptDisposition = "declined"
	PromptChosen        PromptDisposition = "chosen"
	PromptProvided      PromptDisposition = "provided"
	PromptAutoConfirmed PromptDisposition = "auto_confirmed"
	PromptCancelled     PromptDisposition = "cancelled"
	PromptUnavailable   PromptDisposition = "unavailable"
)

func (d PromptDisposition) valid() bool {
	return oneOf(d, PromptConfirmed, PromptDeclined, PromptChosen, PromptProvided,
		PromptAutoConfirmed, PromptCancelled, PromptUnavailable)
}

// CancelReason is why a run is stopping early.
type CancelReason string

const (
	CancelSignal          CancelReason = "signal"
	CancelUser            CancelReason = "user"
	CancelDeadline        CancelReason = "deadline"
	CancelDeliveryFailure CancelReason = "delivery_failure"
)

func (r CancelReason) valid() bool {
	return oneOf(r, CancelSignal, CancelUser, CancelDeadline, CancelDeliveryFailure)
}

// RunOutcome is the verdict of a whole run.
type RunOutcome string

const (
	RunSucceeded RunOutcome = "succeeded"
	RunPlanned   RunOutcome = "planned"
	RunFailed    RunOutcome = "failed"
	RunCancelled RunOutcome = "cancelled"
)

func (o RunOutcome) valid() bool { return oneOf(o, RunSucceeded, RunPlanned, RunFailed, RunCancelled) }

// RecordState is what the run knows about the cluster's install record.
type RecordState string

const (
	RecordNotRead   RecordState = "not_read"
	RecordUnchanged RecordState = "unchanged"
	RecordApplying  RecordState = "applying"
	RecordInstalled RecordState = "installed"
	RecordUnknown   RecordState = "unknown"
)

func (s RecordState) valid() bool {
	return oneOf(s, RecordNotRead, RecordUnchanged, RecordApplying, RecordInstalled, RecordUnknown)
}

// Readiness is what the run observed about the installed workloads.
type Readiness string

const (
	ReadinessReady      Readiness = "ready"
	ReadinessNotReady   Readiness = "not_ready"
	ReadinessNotChecked Readiness = "not_checked"
	ReadinessUnknown    Readiness = "unknown"
)

func (r Readiness) valid() bool {
	return oneOf(r, ReadinessReady, ReadinessNotReady, ReadinessNotChecked, ReadinessUnknown)
}

// ClaimState is what happened to the run's claim on the cluster.
type ClaimState string

const (
	ClaimReleased   ClaimState = "released"
	ClaimHeld       ClaimState = "held"
	ClaimNotClaimed ClaimState = "not_claimed"
	ClaimUnknown    ClaimState = "unknown"
)

func (c ClaimState) valid() bool {
	return oneOf(c, ClaimReleased, ClaimHeld, ClaimNotClaimed, ClaimUnknown)
}
