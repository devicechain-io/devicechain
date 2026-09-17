// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// `dcctl instances list` — what is on this machine, and where.
//
// 🔴 WHY "INSTANCES" AND NOT "CLUSTERS". An instance is what an operator NAMES; a cluster
// is a detail of whichever provider they happen to be on. The whole reason this command
// exists is that finding out what was running meant `kind get clusters` — reaching past
// dcctl for provider knowledge the CLI is supposed to hide. Naming the command after the
// provider's noun would have reproduced that in the CLI's own vocabulary. The cluster is
// shown as a COLUMN, which is where a detail belongs.
//
// 🔴 SECURITY: THIS COMMAND READS instance.json AND THE DESTROY MARKER, AND NOTHING ELSE.
// The marker is STATTED, not read, so no byte of the instance directory beyond the record
// reaches this process. That is a property to preserve rather than an accident of the
// current implementation. Their neighbour, terraform.tfstate, holds the database superuser
// password and the broker's TLS private key in cleartext — and THIS output is the kind of
// thing an operator pastes into an issue or has on screen while sharing. A display field
// sourced from the state file would be one path away from printing a private key.
//
// 🔑 BOTH OPENS HAPPEN INSIDE bootstrap.ListInstances RATHER THAN HERE, so the test that
// pins this claim — TestListInstancesReadsTheRecordAndTheDestroyMarkerAndNothingElse,
// which plants an unreadable terraform.tfstate — covers every one of them. A stat added
// in this file would sit outside it, and the claim would go on reading as tested.

var instancesCmd = &cobra.Command{
	Use:   "instances",
	Short: "Inspect the DeviceChain instances on this machine",
}

var instancesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the instances bootstrapped on this machine and the clusters they live in",
	Long: `Lists every instance dcctl has state for, the cluster it was bootstrapped into,
and whether that cluster is still there.

The cluster column is read from a record written at bootstrap. An instance created
before dcctl recorded that shows as "no record" — destroy still works on it, but it
falls back to guessing the cluster from the instance name, which is wrong for any
instance bootstrapped with --kube-context.

A row reading "cluster gone" is an instance whose cluster has been deleted out from
under it, leaving only local state. That is the normal end state of a validation rig
run, and clearing it is what ` + "`dcctl destroy`" + ` does.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstancesList(cmd.Context(), os.Stdout)
	},
	SilenceUsage: true,
}

// listingProbes are the two questions a row cannot answer from this machine: whether the
// cluster is still there, and what the instance's declaration in it says.
//
// Passed in rather than called directly so the vocabulary below can be exercised against
// every condition an operator can be in — a cluster that is gone, an API server that does
// not answer, a CRD that is not installed. Reaching for the provider and the cluster
// inline would leave the cell that matters most, "we could not tell", the one nothing can
// reach.
type listingProbes struct {
	clusterExists   func(ctx context.Context, p bootstrap.Provider, b bootstrap.ClusterBinding) (bool, error)
	readDeclaration func(ctx context.Context, kubeContext, instance string) (*dcv1beta1.Instance, error)
}

// liveProbes are what the command runs with. TestTheLiveProbesAreTheRealReads asserts
// they are real reads: every row in the vocabulary test supplies its own, so a build
// wired to a stub would pass all of them.
var liveProbes = listingProbes{
	clusterExists: func(ctx context.Context, p bootstrap.Provider, b bootstrap.ClusterBinding) (bool, error) {
		return p.ClusterExists(ctx, b)
	},
	readDeclaration: bootstrap.ReadInstanceCR,
}

// declarationReadTimeout bounds ONE row's declaration read.
//
// 🔴 PER ROW, NOT PER COMMAND, and that is why it is not derived from the caller's
// context. The rows are read serially, so a command-wide budget would be spent entirely
// by the first instance on an unreachable cluster and every row after it would report
// "could not check" about a cluster that was fine. A var so a test can shrink it; nothing
// else assigns to it.
var declarationReadTimeout = 5 * time.Second

// partWayDestroyed is the one cell two different pieces of evidence resolve to: the local
// destroy marker, and a declaration whose phase says the same thing. One sentence for one
// state — a row that said "marker present" would name the artifact rather than the
// situation, and an operator has to act on the situation.
const partWayDestroyed = "PART-WAY DESTROYED — re-run `dcctl destroy`"

// instanceStatus is the derived half of a row: the record says where the instance should
// be, and this asks what is actually there NOW. Deliberately not stored — a cached
// "running" is a claim that rots the moment somebody runs `kind delete`.
//
// 🔴 NO FAILURE MAY READ AS HEALTHY, AND THAT IS THE ONLY RULE THIS FUNCTION HAS. It was
// written the other way round: the word came from "does the CLUSTER exist", which is a
// question about the cluster and not about the instance, so every instance on a live
// cluster printed `running` — including one whose declaration read `Failed`, measured on
// a real cluster with no interrupt and no timing involved. Every arm below that cannot
// answer says so in words, and none of them falls through to a healthy cell.
//
// 🔑 THE ORDER IS EVIDENCE-FIRST, NEAREST FIRST. The destroy marker is on THIS machine,
// so it is answerable when nothing else is — including the case it exists for, a destroy
// that could not reach the cluster at all — and a row that asked the cluster first would
// go silent exactly then.
func instanceStatus(ctx context.Context, known bootstrap.KnownInstance, probes listingProbes) string {
	if known.Destroying {
		return partWayDestroyed
	}
	if known.DestroyingErr != nil {
		// Not "absent". This machine could not say whether a teardown is part-way
		// through, and the healthy cell would be a claim nobody made.
		return "could not check: " + known.DestroyingErr.Error()
	}
	if known.Err != nil {
		return "record unreadable: " + known.Err.Error()
	}
	if !known.HasRecord {
		return "no record — destroy will guess the cluster"
	}
	provider, err := bootstrap.Get(known.Record.Provider)
	if err != nil {
		return "unknown provider " + known.Record.Provider
	}
	exists, err := probes.clusterExists(ctx, provider, known.Record.Binding())
	switch {
	case err != nil:
		return "could not check: " + err.Error()
	case !exists:
		return "cluster gone — stale local state"
	}
	status := declarationStatus(ctx, known, probes)
	if !known.Record.Managed {
		// 🔑 A QUALIFIER, NOT A STATUS. This used to BE the cell, which is how
		// `running (adopted cluster — not named by dcctl)` came to be printed over an
		// instance whose declaration read Failed: a fact about the RECORD had taken the
		// place of the only fact about the instance.
		status += " (adopted cluster — not named by dcctl)"
	}
	return status
}

// declarationStatus turns what the cluster holds into the row's words.
//
// 🔴 THE PHASE IS INTENT, NOT OBSERVATION — what the last dcctl run was TRYING to do
// (see the annotation's own comment on the Instance type). Nothing here has looked at a
// pod, so `Ready` becomes "declared ready" and never "running": `running` is a claim
// about workloads that this command has no evidence for, and the day the operator writes
// Conditions is the day a row may start making it.
//
// 🔴 AND THE WORDING IS "started, not finished", NOT "did not finish". A bootstrap running
// right now in another terminal is a LIVE run, not a stalled one, and the cell has to be
// true of both — an operator told a healthy run "did not finish" destroys it.
func declarationStatus(ctx context.Context, known bootstrap.KnownInstance, probes listingProbes) string {
	// 🔴 BOUNDED, BECAUSE THE ROWS ARE READ SERIALLY. An API server that accepts the
	// connection and never answers would otherwise hang the whole table on its first row,
	// and a command that prints nothing cannot be told from one nobody ran.
	ctx, cancel := context.WithTimeout(ctx, declarationReadTimeout)
	defer cancel()

	inst, err := probes.readDeclaration(ctx, known.Record.KubeContext, known.Instance)
	switch {
	case err != nil:
		// 🔴 ONE CELL FOR THREE THINGS, AND THAT IS CORRECT. A CRD that is not installed,
		// an API server that refuses, and a read that ran out of time are all "we could
		// not tell" — ReadInstanceCR is careful never to return nil for any of them, for
		// the same reason this never prints a healthy word for them.
		return "could not check: " + err.Error()
	case inst == nil:
		return "cluster present, no declaration"
	case inst.DeletionTimestamp != nil:
		// Kubernetes deletion is one-way, so this object is going whatever its phase
		// says. Reporting the phase over it would describe an instance that is leaving.
		return "declaration marked for deletion"
	}

	switch phase := inst.Annotations[dcv1beta1.AnnotationPhase]; phase {
	case dcv1beta1.PhaseDestroying:
		return partWayDestroyed
	case dcv1beta1.PhaseBootstrapping:
		return "bootstrap started, not finished"
	case dcv1beta1.PhaseUpgrading:
		return "upgrade started, not finished"
	case dcv1beta1.PhaseFailed:
		return "last run failed"
	case dcv1beta1.PhaseReady:
		return "declared ready"
	case "":
		// An instance declared by a dcctl from before the annotation existed. Honest, and
		// distinct from every phase that IS recorded.
		return "declared, phase not recorded"
	default:
		// 🔴 PRINTED, NOT TRANSLATED. A newer dcctl wrote a phase this build has never
		// heard of; inventing a word for it would report a state this binary cannot
		// reason about as one it can, and the operator can at least search for the value.
		return fmt.Sprintf("declared, unknown phase %q", phase)
	}
}

func runInstancesList(ctx context.Context, out *os.File) error {
	known, err := bootstrap.ListInstances()
	if err != nil {
		return err
	}
	// 🔴 The empty case gets a SENTENCE, not an empty table. A header with no rows under
	// it reads identically to a command that failed to look, which is the exact ambiguity
	// this whole change exists to remove.
	//
	// It names instances/ rather than the tree above it, because those are different
	// claims and only the narrow one is true. An operator carrying an instance directory
	// from the pre-nesting layout — one sitting directly under the root — is told exactly
	// where this command looked, instead of being told the directory they can see in
	// front of them is empty.
	if len(known) == 0 {
		fmt.Fprintln(out, color.WhiteString("No DeviceChain instances on this machine (nothing under ~/.devicechain/instances)."))
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INSTANCE\tPROVIDER\tCLUSTER\tCONTEXT\tSTATUS")
	for _, k := range known {
		provider, cluster, kubeContext := "?", "?", "?"
		if k.HasRecord {
			provider = k.Record.Provider
			cluster = k.Record.Cluster
			if cluster == "" {
				cluster = "-"
			}
			kubeContext = k.Record.KubeContext
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.Instance, provider, cluster, kubeContext, instanceStatus(ctx, k, liveProbes))
	}
	return w.Flush()
}

func init() {
	instancesCmd.AddCommand(instancesListCmd)
	rootCmd.AddCommand(instancesCmd)
}
