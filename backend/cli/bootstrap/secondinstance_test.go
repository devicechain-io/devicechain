// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Tests for the refusal of a SECOND, differently-named instance.
//
// 🔴 WHAT IT REPLACES IS AN ACCIDENT. Nothing in dcctl decided one instance per cluster.
// What actually stopped a second one was writeOwnedSecret declining to overwrite a Secret
// stamped with another instance's name — at step 7 of 12, after the operator had been
// reinstalled at this run's version and the infrastructure namespace adopted into a
// second OpenTofu state — and it said so as a sentence about a Secret.
//
// So no test below is satisfied by "the bootstrap failed". Each asserts either WHICH
// refusal fired and what it told the operator, or that a run which must still work was
// not touched.

// holding is a cluster that already holds these instances, as the reader would report it.
func holding(ids ...string) clusterInstances {
	return clusterInstances{IDs: ids, Source: "the instance declarations in this cluster"}
}

// stubClusterInstances points the step's seam at a fixed answer.
func stubClusterInstances(t *testing.T, held clusterInstances, err error) {
	t.Helper()
	orig := readClusterInstances
	t.Cleanup(func() { readClusterInstances = orig })
	readClusterInstances = func(context.Context, string) (clusterInstances, error) {
		return held, err
	}
}

func TestASecondDifferentlyNamedInstanceIsRefused(t *testing.T) {
	err := multiInstanceRefusalReason(aBootstrapOf("bravo"), holding("alpha"))
	if err == nil {
		t.Fatal("a bootstrap of \"bravo\" into a cluster already running \"alpha\" was allowed " +
			"to proceed; it would install its operator over the live one and mint credentials " +
			"over the ones \"alpha\" is authenticating with")
	}

	// 🔴 ASSERT THE REASON, NOT THAT THERE IS ONE. A guard can be killed by the wrong
	// test, and the four things below are the whole content of the message: without the
	// holder the operator cannot tell which instance is actually there, without their own
	// name they cannot tell whether they mistyped, without the rule they will assume a
	// bug, and without a recipe they have nowhere to go.
	for _, want := range []struct{ what, substr string }{
		{"the instance that is actually here", `"alpha"`},
		{"the instance that was asked for", `"bravo"`},
		{"the rule being enforced", "ONE INSTANCE PER CLUSTER"},
		{"how to move the instance that is here", "dcctl upgrade"},
		{"how to put this one somewhere else", "--kube-context"},
		{"how to replace what is here", "dcctl destroy"},
		{"what the answer was read from", "the instance declarations"},
	} {
		if !strings.Contains(err.Error(), want.substr) {
			t.Errorf("the refusal does not name %s (%q): %v", want.what, want.substr, err)
		}
	}
}

// 🔴 THE NEGATIVE CONTROL, AND IT IS THE POINT OF THE PAIR. A refusal that refused every
// second run would pass the test above while making `dcctl bootstrap` non-idempotent and
// a half-built instance unrepairable — and nobody would find out until an install died
// part-way on a real cluster. What stops a re-run over a LIVE instance is
// stepRefuseRebuild, a different question with its own answer.
func TestARerunOfTheInstanceThatIsAlreadyHereIsNotRefused(t *testing.T) {
	if err := multiInstanceRefusalReason(aBootstrapOf("alpha"), holding("alpha")); err != nil {
		t.Fatalf("a re-run of the instance this cluster holds was refused, which makes a "+
			"half-built instance permanently unrepairable: %v", err)
	}
}

// The other half of the control: every first bootstrap of every cluster goes through here.
func TestAVirginClusterIsNotRefused(t *testing.T) {
	if err := multiInstanceRefusalReason(aBootstrapOf("alpha"), clusterInstances{}); err != nil {
		t.Fatalf("the first bootstrap of an empty cluster was refused: %v", err)
	}
}

// 🔴 THE CARVE-OUTS THE REBUILD REFUSAL MAKES MUST SURVIVE, AND THIS VERIFIES THE CLAIM
// THAT THEY DO RATHER THAN LEAVING IT INFERRED. rebuildRefusalReason exempts a restore
// and --allow-legacy-db-removal. Every one of those is a re-run against the SAME instance
// by construction, so none of them can reach this refusal — which is why there is no flag
// check in multiInstanceRefusalReason at all. A copy of the carve-out list here would be
// a second list to keep in step with the first.
func TestTheRebuildCarveOutsNeverReachThisRefusal(t *testing.T) {
	carveOuts := map[string]func(*State){
		"a database restore":        func(st *State) { st.Restore = RestorePlan{RdbFrom: "dc-rdb-20260101"} },
		"a root-key restore":        func(st *State) { st.Escrow = EscrowPlan{RestoredRootKey: "k", RestoredFrom: "a.escrow"} },
		"--allow-legacy-db-removal": func(st *State) { st.AllowLegacyDbRemoval = true },
	}
	for name, apply := range carveOuts {
		t.Run(name+" against the instance that is here", func(t *testing.T) {
			st := aBootstrapOf("alpha")
			apply(st)
			if err := multiInstanceRefusalReason(st, holding("alpha")); err != nil {
				t.Errorf("a documented same-instance path was refused as a second instance: %v", err)
			}
		})
		// 🔴 AND THE COUNTERWEIGHT. These flags exempt a re-run of the instance that is
		// here; they must not exempt a DIFFERENT one, or `--allow-legacy-db-removal`
		// becomes an undocumented way past the boundary.
		t.Run(name+" against a different instance is still refused", func(t *testing.T) {
			st := aBootstrapOf("bravo")
			apply(st)
			if err := multiInstanceRefusalReason(st, holding("alpha")); err == nil {
				t.Error("a flag whose whole meaning is \"re-run THIS instance\" let a second " +
					"instance into a cluster that already holds one")
			}
		})
	}
}

// 🔴 THE REFUSAL AN OPERATOR SEES MUST NOT DEPEND ON THEIR FLAGS. There is an earlier,
// flag-dependent refusal in this pipeline: checkJetStreamVolumeIsUpgradable reads the
// EXISTING instance's NATS StatefulSet and refuses when this run would provision a
// different volume size — which is what `--compact` does. Its message is an upgrade
// recipe and mentions no instance at all, so an operator meeting it while bootstrapping a
// second instance is told to delete the StatefulSet of the instance they do not know is
// there. The ordering test below is what keeps this one unreachable; this is the half
// that says the answer itself does not move.
func TestTheRefusalIsTheSameWhateverFlagsTheRunCarries(t *testing.T) {
	flags := map[string]func(*State){
		"no flags":        func(*State) {},
		"--compact":       func(st *State) { st.Compact = true },
		"--ha":            func(st *State) { st.HA = true },
		"--no-monitoring": func(st *State) { st.NoMonitoring = true },
		"--no-cnpg":       func(st *State) { st.NoCNPG = true },
		"--build":         func(st *State) { st.BuildImages = true },
		"--no-tls":        func(st *State) { st.NoTLS = true },
	}
	for name, apply := range flags {
		t.Run(name, func(t *testing.T) {
			st := aBootstrapOf("bravo")
			apply(st)
			err := multiInstanceRefusalReason(st, holding("alpha"))
			if err == nil {
				t.Fatalf("a second instance was allowed in when the run carried %s", name)
			}
			if !strings.Contains(err.Error(), "ONE INSTANCE PER CLUSTER") {
				t.Errorf("%s changed which refusal the operator meets: %v", name, err)
			}
		})
	}
}

// 🔴 AND THE ORDERING THAT MAKES THAT TRUE. The flag-dependent check above lives inside
// stepInfraApply; this step has to come first, or `--compact` decides which message the
// operator reads.
func TestTheSecondInstanceRefusalRunsAfterTheLockAndBeforeAnythingIsApplied(t *testing.T) {
	claim := stepIndex(t, stepClaimCluster)
	refuse := stepIndex(t, stepRefuseSecondInstance)
	core := stepIndex(t, stepInstallCore)
	infra := stepIndex(t, stepInfraApply)

	if refuse <= claim {
		t.Errorf("the second-instance check runs at %d and the lock is taken at %d: a "+
			"concurrent bootstrap could make the answer stale between reading it and acting "+
			"on it", refuse, claim)
	}
	if refuse >= core {
		t.Errorf("the second-instance check runs at %d and the operator install at %d: a "+
			"refused bootstrap would already have written its own operator version over a "+
			"live instance", refuse, core)
	}
	if refuse >= infra {
		t.Errorf("the second-instance check runs at %d and the infrastructure apply at %d, "+
			"which carries checkJetStreamVolumeIsUpgradable: with --compact the operator "+
			"would meet a JetStream upgrade recipe that never mentions instances", refuse, infra)
	}
}

// A dry run applies nothing, so it is not refused — but it must SAY that a real run would
// be, or the rehearsal is of a different run than the one it rehearses.
func TestADryRunReportsTheRefusalWithoutActingOnIt(t *testing.T) {
	stubClusterInstances(t, holding("alpha"), nil)

	st := aBootstrapOf("bravo")
	st.DryRun = true
	if err := stepRefuseSecondInstance(t.Context(), st); err != nil {
		t.Fatalf("a rehearsal that applies nothing was refused: %v", err)
	}
}

// A dry run may legitimately target a cluster that does not exist yet, so being unable to
// ask is not a refusal to report. The real run below is the counterweight.
func TestADryRunAgainstAClusterItCannotReachStillPrintsAPlan(t *testing.T) {
	stubClusterInstances(t, clusterInstances{}, errors.New("the API server is not answering"))

	st := aBootstrapOf("bravo")
	st.DryRun = true
	if err := stepRefuseSecondInstance(t.Context(), st); err != nil {
		t.Fatalf("a dry run against an unreachable cluster failed instead of saying so: %v", err)
	}
}

// 🔴 "COULD NOT TELL" MUST NOT RESOLVE TO "GO AHEAD". A real run that cannot read the
// cluster stops: reading a failure to look as an empty cluster is the direction that
// writes a second instance over the first.
func TestARealRunStopsWhenItCannotTellWhatTheClusterHolds(t *testing.T) {
	stubClusterInstances(t, clusterInstances{}, errors.New("the API server is not answering"))

	if err := stepRefuseSecondInstance(t.Context(), aBootstrapOf("bravo")); err == nil {
		t.Fatal("a bootstrap proceeded past a cluster that would not say whether it already " +
			"held an instance")
	}
}

// The negative control for the step itself, not just the policy: with the seam answering
// honestly, a first bootstrap and a re-run both go through.
func TestTheStepLetsThroughTheRunsThatMustStillWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		held clusterInstances
		want string
	}{
		{"a virgin cluster", clusterInstances{}, "alpha"},
		{"a re-run of the instance that is here", holding("alpha"), "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubClusterInstances(t, tc.held, nil)
			if err := stepRefuseSecondInstance(t.Context(), aBootstrapOf(tc.want)); err != nil {
				t.Fatalf("a run that must still work was refused: %v", err)
			}
		})
	}
}

// 🔴 THE COMMAND LAYER HAS TO BE ABLE TO RECOGNISE THIS SPECIFIC REFUSAL, because it is
// the one failure after which the local record it wrote before the pipeline describes
// nothing. Pipeline.Run wraps every step error, so the test goes through the wrapping.
func TestTheRefusalSurvivesBeingWrappedByThePipeline(t *testing.T) {
	inner := multiInstanceRefusalReason(aBootstrapOf("bravo"), holding("alpha"))
	wrapped := fmt.Errorf("step %q: %w", "Refuse a second instance", inner)

	var refusal *ErrSecondInstance
	if !errors.As(wrapped, &refusal) {
		t.Fatal("the command layer cannot tell this refusal from any other failure, so the " +
			"phantom local record it leaves behind would never be cleared")
	}
	if refusal.Wanted != "bravo" || len(refusal.Holds) != 1 || refusal.Holds[0] != "alpha" {
		t.Fatalf("the refusal carries %+v", refusal)
	}
}

// The recipes in the message have to be commands somebody can run, which means the
// provider has to reach them. It is empty in internally assembled pipelines, and a
// command line with a hole in it reads as a typo rather than as a placeholder.
func TestTheRecipesNameTheProviderTheRunResolvedWith(t *testing.T) {
	st := aBootstrapOf("bravo")
	st.Provider = "local"
	err := multiInstanceRefusalReason(st, holding("alpha"))
	if !strings.Contains(err.Error(), "dcctl destroy local alpha") {
		t.Errorf("the refusal does not print a destroy that would run: %v", err)
	}
	if !strings.Contains(err.Error(), "dcctl upgrade local alpha") {
		t.Errorf("the refusal does not print an upgrade that would run: %v", err)
	}

	// An internally assembled pipeline resolves no provider, which is the case that used
	// to print `dcctl destroy  alpha` — a command line with a hole in it reads as a typo
	// rather than as a placeholder.
	bare := multiInstanceRefusalReason(&State{Instance: "bravo"}, holding("alpha"))
	if !strings.Contains(bare.Error(), "<provider>") {
		t.Errorf("with no provider resolved the recipes silently lose an argument: %v", bare)
	}
}

func TestNamedInstancesReadsAsASentence(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "an instance"},
		{[]string{"alpha"}, `instance "alpha"`},
		{[]string{"alpha", "bravo"}, `instances "alpha" and "bravo"`},
		{[]string{"alpha", "bravo", "charlie"}, `instances "alpha", "bravo" and "charlie"`},
	} {
		if got := namedInstances(tc.in); got != tc.want {
			t.Errorf("namedInstances(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
