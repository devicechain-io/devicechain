// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"
)

// stepRefuseSecondInstance stops a bootstrap aimed at a cluster that is already holding
// a DIFFERENT instance.
//
// 🔴 ONE INSTANCE PER CLUSTER IS THE BOUNDARY, AND UNTIL NOW IT WAS AN ACCIDENT (ADR-080).
// Nothing in dcctl decided it. What actually stopped a second instance was
// writeOwnedSecret refusing to overwrite a Secret stamped with another instance's name
// — inside the infrastructure apply, two thirds of the way through the run, after the
// operator had been reinstalled at this run's version, the declaration written and the
// infrastructure namespace adopted into a second OpenTofu state. The operator met it as:
//
//	refusing to write Secret dc-system/dc-rdb-app-credentials: it belongs to instance "a", not "b"
//
// A true sentence about a Secret. Not the sentence "this cluster already holds an
// instance", which is the thing they needed to know, and not at a point where nothing
// had been touched.
//
// 🔑 THIS IS NOT stepRefuseRebuild, AND THE TWO DO NOT OVERLAP. That step refuses a
// re-run aimed at the SAME instance, keyed on that instance's own configuration
// document; a differently-named instance passes it cleanly, because there is no
// document under that name to find. This one asks the opposite question — is anything
// ELSE here — and the two answers are drawn from different artifacts on purpose.
func stepRefuseSecondInstance(ctx context.Context, st *State) error {
	// A dry run applies nothing, so there is nothing to protect the cluster from, and
	// rehearsing a bootstrap against a cluster that already holds one is a reasonable
	// thing to want to do. It still SAYS what a real run would do — a rehearsal that
	// hides the refusal is a rehearsal of a different run. Same shape, and for the same
	// reason, as stepRefuseRebuild's dry-run branch.
	if st.DryRun {
		held, err := readClusterInstances(ctx, st.KubeContext)
		if err != nil {
			// A dry run against a cluster that will not answer is not a refusal to
			// report; it is a question that could not be asked. Say so rather than
			// failing the rehearsal, which is the same softening the --ha capacity
			// check applies for the same reason: a dry run may legitimately target a
			// cluster that does not exist yet.
			wouldDo(fmt.Sprintf("check which instances this cluster already holds "+
				"(could not ask it: %v)", err))
			return nil
		}
		if reason := multiInstanceRefusalReason(st, held); reason != nil {
			wouldDo(fmt.Sprintf(
				"REFUSE: this cluster already holds %s, so a real run would stop at this step "+
					"rather than bootstrap %q into it",
				namedInstances(held.othersThan(st.Instance)), st.Instance))
		}
		return nil
	}

	doing("checking whether this cluster already holds an instance")
	held, err := readClusterInstances(ctx, st.KubeContext)
	if err != nil {
		return fail("checking which instances this cluster already holds", err)
	}
	if err := multiInstanceRefusalReason(st, held); err != nil {
		return err
	}
	done()
	return nil
}

// ErrSecondInstance is the refusal, as a type rather than a bare error.
//
// 🔴 THE COMMAND LAYER HAS TO RECOGNISE THIS ONE SPECIFICALLY, which is the whole
// reason it is a type. `dcctl bootstrap` writes ~/.devicechain/<instance>/instance.json
// BEFORE the pipeline runs and deliberately keeps it on any failure — a bootstrap that
// died half-way has still created a cluster, and a record that was never written is an
// orphan nothing can destroy. This refusal is the one failure where that reasoning does
// not hold, because it can only fire on a cluster this run did NOT create, so the
// record it wrote describes nothing. See PriorLocalState.
type ErrSecondInstance struct {
	// Holds are the instances the cluster already has, excluding Wanted.
	Holds []string
	// Wanted is the instance this run was asked to bootstrap.
	Wanted string
	// Provider is what resolved the cluster, so the message can print commands that
	// actually run.
	Provider string
	// Source names the artifact the answer was read from.
	Source string
}

func (e *ErrSecondInstance) Error() string {
	here := namedInstances(e.Holds)
	// The provider is empty in internally assembled pipelines and in tests; printing a
	// command with a hole in it is worse than printing a placeholder that reads as one.
	provider := e.Provider
	if provider == "" {
		provider = "<provider>"
	}
	return fmt.Sprintf(
		"this cluster already holds %s, and dcctl installs ONE INSTANCE PER CLUSTER. You asked "+
			"it to bootstrap %q into the same cluster. That would install this run's operator "+
			"over the one already running, adopt the shared infrastructure into a second "+
			"OpenTofu state, and mint database, broker and root-key credentials over the ones "+
			"the instance that is here is authenticating with.\n\n"+
			"  To move the instance that is here onto a new version:\n"+
			"      dcctl upgrade %s %s --version <tag>\n"+
			"  To build %q in a cluster of its own:\n"+
			"      dcctl bootstrap %s %s                        (a new local cluster)\n"+
			"      dcctl bootstrap %s %s --kube-context <ctx>   (a cluster you already have)\n"+
			"  To replace what is here with %q:\n"+
			"      dcctl destroy %s %s, then bootstrap again\n\n"+
			"The last takes %s's data with it. Read from %s.",
		here, e.Wanted,
		provider, e.first(),
		e.Wanted, provider, e.Wanted, provider, e.Wanted,
		e.Wanted, provider, e.first(),
		e.first(), e.sourceOrUnknown())
}

// first is the instance a single-valued recipe names. A cluster holding more than one
// is not a state dcctl can produce, so the recipes name the first and the sentence above
// them has already listed them all.
func (e *ErrSecondInstance) first() string {
	if len(e.Holds) == 0 {
		return "<instance>"
	}
	return e.Holds[0]
}

func (e *ErrSecondInstance) sourceOrUnknown() string {
	if e.Source == "" {
		return "this cluster"
	}
	return e.Source
}

// multiInstanceRefusalReason returns the refusal, or nil when this run may proceed.
//
// 🔴 NAMED FOR ITS SUCCESSOR, DELIBERATELY. The one-instance-per-cluster boundary is a
// statement about what dcctl supports TODAY, not about what the platform can do — the
// services are multi-tenant and the chart is not the constraint. When per-instance
// releases and per-instance credentials land, this becomes a named switch to flip
// rather than an anonymous guard somebody has to work out the purpose of before
// deleting it. That is why the policy is a named function separate from the step, and
// why the name says what it decides rather than where it is called from.
//
// Separated from the step for the second reason rebuildRefusalReason is: the policy can
// then be exercised without a cluster, and a guard whose refusal has only ever been
// produced by hand on a kind cluster is a guard the next edit removes.
//
// 🔑 THE REBUILD CARVE-OUTS DO NOT NEED REPEATING HERE, AND THAT IS VERIFIED RATHER
// THAN ASSUMED. rebuildRefusalReason exempts a restore (st.Restore / st.Escrow) and
// --allow-legacy-db-removal. Every one of those is a re-run against the SAME instance —
// a restore recovers the instance it names, and the legacy-removal flag means "re-run
// this instance's apply with the old resources removed" — so all of them leave
// othersThan() empty and never reach the refusal below. There is deliberately no flag
// check in this function: adding one would make the exemption depend on a list that has
// to be kept in step with the other guard's, and the identity test already covers every
// member of it. TestTheRebuildCarveOutsNeverReachThisRefusal is what holds it.
func multiInstanceRefusalReason(st *State, held clusterInstances) error {
	others := held.othersThan(st.Instance)
	if len(others) == 0 {
		// 🔴 THE NEGATIVE CONTROL THIS GUARD LIVES OR DIES BY. A cluster holding
		// nothing and a cluster holding THIS instance are both allowed through, and the
		// second is the one that matters: a refusal that fired on every second run of
		// the same instance would pass its own test while making a half-built instance
		// unrepairable and `dcctl bootstrap` non-idempotent. What stops a same-instance
		// re-run over a LIVE instance is stepRefuseRebuild, which is a different
		// question with a different answer and its own carve-outs.
		return nil
	}
	return &ErrSecondInstance{
		Holds:    others,
		Wanted:   st.Instance,
		Provider: st.Provider,
		Source:   held.Source,
	}
}

// namedInstances renders the held ids as the subject of a sentence.
func namedInstances(ids []string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, fmt.Sprintf("%q", id))
	}
	switch len(quoted) {
	case 0:
		return "an instance"
	case 1:
		return "instance " + quoted[0]
	default:
		return "instances " + strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
	}
}
