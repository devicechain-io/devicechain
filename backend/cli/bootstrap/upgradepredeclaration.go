// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
)

// undeclaredInstanceRefusal settles WHICH refusal an operator gets when `dcctl upgrade`
// finds no declaration for the instance they named.
//
// 🔴 TWO SITUATIONS WORE ONE MESSAGE, AND ONLY ONE OF THEM WAS A TYPO. The refusal said
// "check the name, and check that <context> is the cluster you mean". That is the right
// advice for a name that was never installed anywhere. It is the wrong advice — and
// unfalsifiable advice, because the name and the cluster are both correct — for an
// instance that is running right here and was built by a release that recorded no
// declaration. Those operators need to be told the supported path, not sent to re-read
// their own command line.
//
// 🔑 NEVER RETURNS nil. The upgrade is refused either way; all this decides is what the
// operator is told to do next. A caller reading a nil here as "carry on" would act on a
// State that was never hydrated.
//
// 🔴 AND "COULD NOT ASK" IS ITS OWN ANSWER. A cluster that will not say what it holds
// must not be reported as a cluster holding nothing: that is the direction that tells an
// operator their instance does not exist while it is sitting there, and the direction
// that would send them to check a name that is perfectly correct.
func undeclaredInstanceRefusal(instance, provider, kubeContext string, held clusterInstances, readErr error) error {
	if readErr != nil {
		return fmt.Errorf(
			"there is no instance %q declared in this cluster, so there is nothing to upgrade. "+
				"Whether this cluster nonetheless HOLDS that instance — built by a release older "+
				"than the declaration this command reads — could not be established: %w. So this "+
				"refusal cannot say which of the two you are looking at: a name or a cluster to "+
				"check, or an instance that has to be destroyed and built again. `dcctl instances "+
				"list` shows what is declared where",
			instance, readErr)
	}

	if held.holds(instance) {
		return &ErrPreDeclarationInstance{
			Instance:    instance,
			Provider:    provider,
			KubeContext: kubeContext,
			Source:      held.Source,
		}
	}

	// 🔴 UNCHANGED, DELIBERATELY. Nothing in this cluster names the instance, so the
	// original reading is the correct one and its wording is the one operators have
	// already seen. The new refusal above is an addition to the vocabulary, not a
	// rewrite of it.
	return fmt.Errorf(
		"there is no instance %q declared in this cluster, so there is nothing to upgrade. "+
			"Check the name, and check that %q is the cluster you mean — `dcctl instances "+
			"list` shows what is declared where",
		instance, kubeContext)
}

// ErrPreDeclarationInstance is the refusal for an instance that IS here and predates the
// declaration `dcctl upgrade` needs.
//
// A type rather than a bare error for one reason: this is the refusal the upgrade drill
// asserts on, and the drill's claim is that the refusal is THIS one rather than merely
// that the command failed. Everything the message has to carry is a field, so a test can
// say which part is missing instead of diffing prose.
type ErrPreDeclarationInstance struct {
	// Instance is the instance that is here and cannot be upgraded onto this release.
	Instance string
	// Provider and KubeContext make the recipes below commands that actually run.
	Provider    string
	KubeContext string
	// Source names the artifact that said this instance is here, so the refusal can
	// report what it READ rather than only asserting its conclusion.
	Source string
}

func (e *ErrPreDeclarationInstance) Error() string {
	provider := e.Provider
	if provider == "" {
		// The same placeholder ErrSecondInstance uses, for the same reason: a command
		// printed with a hole in it is worse than one printed with something that reads
		// as a hole.
		provider = "<provider>"
	}
	// 🔑 EVERY SOURCE LABEL IS A PLURAL NOUN PHRASE — "the instance declarations in this
	// cluster", "the credentials dcctl minted in dc-system", "the DeviceChain Helm
	// releases in this cluster" — so the sentence is built around "named by %s" rather
	// than "%s names it", which did not agree with any of the three.
	source := e.Source
	if source == "" {
		source = "what is in this cluster"
	}
	return fmt.Sprintf(
		"instance %q IS in this cluster — named by %s — and it carries no declaration, so it "+
			"was built by a release older than the one that began recording them. `dcctl "+
			"upgrade` reads an instance's declaration to learn what that instance IS — its "+
			"profile, topology, exposure and functional areas — and there is nothing here to "+
			"read. This is not a mistyped name and not the wrong cluster.\n\n"+
			"THIS RELEASE DOES NOT UPGRADE ONTO ITS PREDECESSOR. The supported path is to "+
			"destroy the instance and build it again:\n\n"+
			"      dcctl destroy %s %s --kube-context %s\n"+
			"      dcctl bootstrap %s %s --kube-context %s\n\n"+
			"That takes %q's data with it, and there is no other route: nothing here writes a "+
			"declaration on behalf of an instance that never had one, because what that "+
			"instance was configured with was never recorded in a form this command can read. "+
			"Inventing one would deploy a guess over a live instance.",
		e.Instance, source,
		provider, e.Instance, e.KubeContext,
		provider, e.Instance, e.KubeContext,
		e.Instance)
}

// refuseUndeclaredInstance asks the cluster whether it holds the instance anyway, and
// turns the answer into the refusal it earns.
//
// 🔴 IT READS THROUGH readClusterInstances RATHER THAN ASKING THE CLUSTER ITSELF, and the
// choice between the two readers that already exist is not arbitrary (ADR-080).
//
//   - clusterInstances walks the Instance declarations, then the Secrets dcctl STAMPED
//     with an instance name, then the Helm release's own instance.id. Every one of those
//     is an attribution dcctl WROTE. It is also the only one of the two that reads the
//     Helm release — which for a pre-declaration instance is the artifact that names it,
//     since those releases wrote no declaration and stamped no Secrets.
//   - instanceFootprint (destroy_orphan.go) is per-instance and reads the declaration,
//     a NAMESPACE matching the instance's name, and the stamped Secrets. It is the right
//     reader for the question it answers — "may this local record be deleted?", where
//     anything at all disqualifying is the safe direction — but a bare namespace name is
//     something an operator could have created for any reason, and the refusal here ENDS
//     IN "destroy your instance". Advice that destructive has to rest on an attribution,
//     not on a coincidence of naming.
//
// A third reader is what is actually forbidden: two answers to "whose cluster is this"
// that can disagree would let one of them refuse while the other waves the same run past.
func refuseUndeclaredInstance(ctx context.Context, provider, kubeContext, instance string) error {
	held, err := readClusterInstances(ctx, kubeContext)
	return undeclaredInstanceRefusal(instance, provider, kubeContext, held, err)
}
