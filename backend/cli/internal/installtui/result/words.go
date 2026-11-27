// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// stageNames is what the result and progress screens call each install and bootstrap stage.
var stageNames = map[installflow.StepID]string{
	installflow.StepInstallInputs:        "Resolve the request",
	installflow.StepInstallPreflight:     "Preflight checks",
	installflow.StepInstallCluster:       "Resolve the target cluster",
	installflow.StepInstallClaim:         "Claim the cluster",
	installflow.StepInstallInspect:       "Inspect the cluster",
	installflow.StepInstallCredentials:   "Resolve credentials",
	installflow.StepInstallPrepare:       "Prepare the cluster settings",
	installflow.StepInstallWriteIntent:   "Record the install intent",
	installflow.StepInstallOperator:      "Install the operator",
	installflow.StepInstallPrerequisites: "Apply the cluster prerequisites",
	installflow.StepInstallDatabaseLogin: "Open the database session",
	installflow.StepInstallRecord:        "Record the install",
	installflow.StepInstallRDBReady:      "Wait for the relational store",
	installflow.StepInstallSummary:       "Finalizing",

	installflow.StepBootstrapInputs:         "Resolve the request",
	installflow.StepBootstrapPreflight:      "Preflight checks",
	installflow.StepBootstrapEscrow:         "Settle the root-key escrow",
	installflow.StepBootstrapCluster:        "Resolve the cluster and images",
	installflow.StepBootstrapRegistry:       "Ensure the local registry",
	installflow.StepBootstrapClaim:          "Claim the cluster",
	installflow.StepBootstrapRefuseRebuild:  "Refuse a rebuild",
	installflow.StepBootstrapSingletons:     "Check what other instances hold",
	installflow.StepBootstrapDeclare:        "Declare the instance",
	installflow.StepBootstrapRender:         "Render the configuration",
	installflow.StepBootstrapInfrastructure: "Apply the infrastructure",
	installflow.StepBootstrapHelm:           "Install the instance (Helm)",
	installflow.StepBootstrapReadiness:      "Wait for readiness",
	installflow.StepBootstrapBrokerBalance:  "Even out broker leadership",
	installflow.StepBootstrapReport:         "Finalizing",
}

// stageName names a stage; one this table does not know still shows, by its ID.
func stageName(id installflow.StepID) string {
	if n, ok := stageNames[id]; ok {
		return n
	}
	return string(id)
}

// StageName is stageName for the progress screen's "Now" line (installtui), so
// both screens name a stage from this one table.
func StageName(id installflow.StepID) string { return stageName(id) }

// failedAt is the failed stage as a line, or "" when no stage is blamed.
func (in Input) failedAt() string {
	if in.Summary.FailedStep == "" {
		return ""
	}
	return stageName(in.Summary.FailedStep) + " (" + string(in.Summary.FailedStep) + ")"
}

// kind is which result screen a summary gets.
type kind uint8

const (
	kindFailed kind = iota
	kindStopped
	kindSucceeded
	kindPlanned
)

// kindOf is the screen for a verdict. Only a successful or planned verdict, with
// every control event delivered, is told as one; anything else is a failure.
func kindOf(s installflow.Summary) kind {
	switch {
	case s.DeliveryIncomplete:
		return kindFailed
	case s.Outcome == installflow.RunSucceeded:
		return kindSucceeded
	case s.Outcome == installflow.RunPlanned:
		return kindPlanned
	case s.Outcome == installflow.RunCancelled:
		return kindStopped
	}
	return kindFailed
}

// headline is the screen's title and the exit summary's first line.
func (in Input) headline() string {
	switch kindOf(in.Summary) {
	case kindSucceeded:
		return "Shared prerequisites installed"
	case kindPlanned:
		return "Dry run finished: nothing was changed"
	case kindStopped:
		return "Install stopped"
	}
	return "Install failed"
}

// outcome is the exit summary's verdict, after "dcctl install: ".
func (in Input) outcome() string {
	switch kindOf(in.Summary) {
	case kindSucceeded:
		return "shared prerequisites installed"
	case kindPlanned:
		return "dry run finished; nothing was changed"
	case kindStopped:
		return "stopped"
	}
	return "failed"
}

// recordedButNotReady is the run that recorded the install and then ran out of
// time waiting for the relational store: a failure, but not an empty cluster.
func (in Input) recordedButNotReady() bool {
	return in.Summary.InstallRecord == installflow.RecordInstalled &&
		in.Summary.FailedStep == installflow.StepInstallRDBReady
}

// changes says what the run applied. Only none_observed is told as "nothing".
func (in Input) changes() string {
	w := installflow.EffectWords(in.Summary.PartialState)
	if in.Summary.PartialState != installflow.EffectNoneObserved {
		w += " Nothing was rolled back."
	}
	return w
}

func claimWords(c installflow.ClaimState) string {
	switch c {
	case installflow.ClaimReleased:
		return "Released."
	case installflow.ClaimHeld:
		return "Not released: the claim on the cluster is still held."
	case installflow.ClaimNotClaimed:
		return "The cluster was not claimed."
	}
	return "Not known whether the claim was released."
}

func recordWords(r installflow.RecordState) string {
	switch r {
	case installflow.RecordInstalled:
		return "Recorded as installed."
	case installflow.RecordApplying:
		return "Marked as applying; not recorded as installed."
	case installflow.RecordUnchanged:
		return "Unchanged."
	case installflow.RecordNotRead:
		return "Not read."
	}
	return "Not known."
}

// fix is the recovery the screen points at. 🔴 IT IS ALWAYS A RE-RUN: there is
// no rollback, destroy, force-unlock or checkpoint skip to offer.
func (in Input) fix() string {
	remedy := installflow.MessageCode("")
	if in.Failure != nil {
		remedy = in.Failure.Remedy
	}
	switch {
	case remedy == installflow.RemedyRerunWithYes:
		return "Re-run the same command with --yes, which gives the confirmation it asked for."
	case in.recordedButNotReady():
		return "Install recorded; replicas still joining. Re-run the same command once the cluster " +
			"has room for them: it converges and waits again."
	case remedy == installflow.RemedyRerunSameCommand:
		return "Re-run the same command once dcctl has exited: an install converges, so it " +
			"finishes whatever this run left half done."
	}
	return "Fix the cause above, then re-run the same command: an install converges, so it " +
		"finishes whatever this run left half done."
}

// contextName is the kube context the run targeted, for DISPLAY only, or "".
func (in Input) contextName() string {
	if c := in.Target.Context.String(); c != "" {
		return c
	}
	if in.Facts.KubeContext != "" {
		return installflow.Text(in.Facts.KubeContext).String()
	}
	if in.managed() && in.Facts.Cluster != "" {
		return installflow.Text("kind-" + in.Facts.Cluster).String()
	}
	return ""
}

// commandContext is the kube context as an argument of a command to copy: the
// raw choice, a placeholder when there is none, or one naming the problem when
// the screen cannot draw it exactly.
func (in Input) commandContext() string {
	switch {
	case in.Facts.KubeContext != "":
		return drawableArg(in.Facts.KubeContext, "kube-context")
	case in.managed():
		return drawableArg("kind-"+in.commandCluster(), "kube-context")
	}
	return "<kube-context>"
}

// commandCluster is the managed kind cluster's raw name, the default when none
// was chosen.
func (in Input) commandCluster() string {
	if in.Facts.Cluster != "" {
		return in.Facts.Cluster
	}
	return defaultCluster
}

func (in Input) managed() bool {
	if in.Target.Mode != "" {
		return in.Target.Mode == "managed-kind"
	}
	return in.Facts.Managed
}

// target describes where the run went.
func (in Input) target() string {
	ctx := in.contextName()
	if ctx == "" {
		return "not resolved"
	}
	if in.managed() {
		return ctx + " (managed kind cluster)"
	}
	return ctx + " (existing context)"
}

const defaultCluster = "devicechain"

func (in Input) provider() string {
	if in.Facts.Provider != "" {
		return in.Facts.Provider
	}
	return "local"
}

// targetFlags selects the same cluster in another dcctl command, from the raw
// choices.
func (in Input) targetFlags() []string {
	if !in.managed() {
		return []string{"--kube-context", in.commandContext()}
	}
	if c := in.commandCluster(); c != defaultCluster {
		return []string{"--cluster", drawableArg(c, "cluster")}
	}
	return nil
}

// inspect is a read-only command that shows the state the failed stage left.
// "" when nothing useful can be named.
func (in Input) inspect() string {
	step := in.Summary.FailedStep
	switch {
	case step == "" || step == installflow.StepInstallInputs:
		return ""
	case step == installflow.StepInstallPreflight:
		return in.quote(append([]string{"dcctl", "preflight", in.provider()}, in.targetFlags()...))
	case step == installflow.StepInstallCluster && in.managed():
		return in.quote([]string{"kind", "get", "clusters"})
	}
	if in.contextName() == "" && in.Facts.KubeContext == "" && !in.managed() {
		return ""
	}
	return in.quote([]string{"kubectl", "--context", in.commandContext(), "get", "pods", "--all-namespaces"})
}

// nextStep is the success screen's next command: what the summary names, or
// building an instance on the cluster.
func (in Input) nextSteps() []string {
	if len(in.Summary.Next) > 0 {
		var out []string
		for _, n := range in.Summary.Next {
			argv := make([]string, len(n.Argv))
			for i, a := range n.Argv {
				argv[i] = a.String()
			}
			out = append(out, in.quote(argv))
		}
		return out
	}
	argv := append([]string{"dcctl", "bootstrap", in.provider(), "<instance>"}, in.targetFlags()...)
	if in.BootstrapUI {
		argv = append(argv, "--ui=tui")
	}
	return []string{in.quote(argv)}
}

// shape is the cluster's footprint and topology: two axes, never a profile.
func (f Facts) shape() string {
	s := "Standard footprint"
	if f.Compact {
		s = "Compact footprint"
	}
	if f.HA {
		return s + ", HA"
	}
	return s + ", not HA"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (f Facts) budget() string {
	if f.MaxConnections > 0 {
		return strconv.Itoa(f.MaxConnections) + " (each instance reserves its own share)"
	}
	return "the install's default (each instance reserves its own share)"
}

func (f Facts) backups() string {
	if f.Backups {
		return "configured"
	}
	return "not configured"
}

func credentialWords(r installflow.SecretRef) string {
	s := "Secret " + r.Namespace + "/" + r.Name
	if r.Key != "" {
		s += ", key " + r.Key
	}
	return s
}

var bareWord = regexp.MustCompile(`^[A-Za-z0-9_./:=+-]+$`)

// placeholder is a word the operator replaces before running the command
// (<instance>, <kube-context>): it stays bare and readable.
var placeholder = regexp.MustCompile(`^<[a-z-]+>$`)

// quote joins argv for the selected shell. A bare word stays bare; the
// placeholder <instance> stays readable.
func (in Input) quote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		switch {
		case bareWord.MatchString(a) || placeholder.MatchString(a):
			out[i] = a
		case in.Shell == PowerShell:
			out[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		default:
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}
