// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// errInstanceNotInCluster says this destroy removed a LOCAL RECORD and nothing else,
// because the instance it named had nothing in the cluster to remove.
//
// 🔴 A SUCCESS, CARRIED AS AN ERROR, FOR THE REASON errDestroyAborted IS. Three
// outcomes reach the callers of destroyInstanceOnly — it uninstalled, the operator
// declined, it found nothing to uninstall — and the one thing this file must not do is
// collapse two of them into one return value. That is the mistake the aborted case
// already records: a decline and a completed uninstall were both nil, so declining fell
// through to deleting the tfstate of an instance the operator had just said to leave
// alone. The callers below each say what this one means for them; nobody may read it as
// "the release was uninstalled".
var errInstanceNotInCluster = errors.New("instance has nothing in this cluster")

// destroyNeedsNoFurtherWork reports whether a non-nil result from destroyInstanceOnly is
// an OUTCOME rather than a failure: the operator declined, or there was nothing in the
// cluster to uninstall and the local record has already been removed.
//
// 🔴 ONE DEFINITION BECAUSE BOTH CALLERS MUST AGREE. destroyInstanceOnly is called from
// the --keep-cluster path and from the adopted-cluster branch of a full destroy, and the
// two read its result for different purposes — one exits, one decides whether to carry on
// deleting. A sentinel added to one list and not the other is how "the operator declined"
// once fell through to deleting the tfstate of the instance they had just spared.
func destroyNeedsNoFurtherWork(err error) bool {
	return errors.Is(err, errDestroyAborted) || errors.Is(err, errInstanceNotInCluster)
}

// uninstallOutcome routes a FAILED uninstall: the foreign-release refusal is the one
// that may still leave this command a job to do, and everything else is a failure.
//
// 🔴 SEPARATE FROM destroyInstanceOnly SO THE ROUTING CAN BE EXERCISED WITHOUT A CLUSTER,
// which is the same reason uninstallRefusalReason is separate from helmUninstall — and
// here it is load-bearing rather than tidy. Written inline, the branch that calls
// resolveForeignRelease sits behind a real Helm uninstall against a real cluster, so
// deleting it breaks nothing any test can see: the guard would still refuse, correctly,
// and the operator would still be stranded, which is the state this change exists to end.
//
// 🔴 AND THE MATCH IS ON TYPE, NOT ON WORDS. Recognising the refusal by its message would
// begin clearing local state the day the wording changed, or the day an unrelated failure
// happened to contain the same words.
func uninstallOutcome(err error, resolveForeign func() error) error {
	var foreign *foreignReleaseError
	if errors.As(err, &foreign) {
		return resolveForeign()
	}
	return fail("uninstalling release", err)
}

// resolveForeignRelease decides what to do when the uninstall refused because the
// release in this cluster belongs to somebody else.
//
// 🔴 THIS IS THE HALF OF THE FOREIGN-RELEASE GUARD THAT MAKES IT ACTIONABLE, AND
// WITHOUT IT THE GUARD IS A TRAP. The refusal (foreignReleaseError) is correct and it
// stops real data loss — but on its own it also strands the operator: destroyInstanceOnly
// returns before removeInstanceState, so ~/.devicechain/<instance> survives, every re-run
// of `dcctl destroy` meets the same refusal, and NO dcctl path clears the record.
// `dcctl instances list` then reports the instance as running. The way in is ordinary: a
// bootstrap writes its instance record BEFORE the pipeline (cmd/bootstrap.go), so any
// bootstrap into a cluster that already holds an instance leaves exactly this state, and
// the documented cleanup for it is `dcctl destroy`.
//
// The record may be cleared only when the instance demonstrably has NOTHING here. That
// is a positive finding, not a missing one: see instanceFootprint for what is asked, and
// keepRecordReason for the rule that anything unanswerable keeps the record.
func resolveForeignRelease(ctx context.Context, kubeContext string, opts DestroyOptions, refusal error) error {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		// Reached the cluster well enough to read the release a moment ago, and cannot
		// now. That is "we could not tell", so the refusal stands untouched.
		fmt.Println(color.YellowString(
			"  could not connect to the cluster to check whether %q has anything in it (%v),\n"+
				"  so its local record was KEPT.", opts.Instance, err))
		return fail("uninstalling release", refusal)
	}
	found, footprintErr := instanceFootprint(ctx, dyn, typed, opts.Instance)
	if keep := keepRecordReason(opts.Instance, found, footprintErr); keep != nil {
		fmt.Println(color.YellowString("  the local record for %q was KEPT: %v", opts.Instance, keep))
		return fail("uninstalling release", refusal)
	}

	// Closes the `doing` line the caller opened. Not "failed." — the uninstall was
	// refused and that refusal is the correct outcome; what follows is the rest of it.
	fmt.Println(color.YellowString("refused."))
	fmt.Println(color.YellowString(
		"The release in this cluster belongs to instance %q, so it was LEFT ALONE.\n"+
			"  Instance %q has nothing here — no declaration, no namespace, no Secrets it minted —\n"+
			"  so there is nothing to uninstall. Removing its local record only; the instance that IS\n"+
			"  installed here is untouched.",
		foreignOwner(refusal, "another instance"), opts.Instance))
	if err := removeInstanceState(opts); err != nil {
		return err
	}
	// 🔴 NOT "uninstalled". Nothing in the cluster changed, and a closing line claiming
	// otherwise over a no-op is the defect this whole command has been fixed for twice.
	fmt.Println(color.HiGreenString(
		"\nInstance %q was not installed in cluster %s; its local record has been removed.",
		opts.Instance, kubeContext))
	return errInstanceNotInCluster
}

// foreignOwner digs the owning instance out of the refusal for the message above, and
// falls back rather than asserting. The refusal is always a *foreignReleaseError on the
// path that reaches here — the caller matched on exactly that — so the fallback is for
// the next caller, not for today.
func foreignOwner(refusal error, fallback string) string {
	var foreign *foreignReleaseError
	if errors.As(refusal, &foreign) && foreign.Owner != "" {
		return foreign.Owner
	}
	return fallback
}

// keepRecordReason returns nil when the local record may be cleared, or the reason it
// must be KEPT.
//
// 🔴 "WE COULD NOT TELL" NEVER RESOLVES TO THE DESTRUCTIVE ANSWER — the rule
// reuseMintedCredential and clusterArchivePath are written to, and this is the same
// shape of decision: the branch that deletes is the one that cannot be undone, so it
// must be reached by a positive reading rather than by failing to look. An unreachable
// API server, a Get that errors, a list that is refused — all of them arrive here as an
// error and all of them keep the record.
//
// 🔴 AND THE NEGATIVE CONTROL IS THE POINT OF THE PAIR. A version that cleared the
// record whenever the uninstall was refused would pass any test built from the stranded
// case above, and would delete the tfstate and cluster binding of a REAL instance whose
// release is mis-attributed for any other reason. Anything found here is that instance.
func keepRecordReason(instance string, found []string, footprintErr error) error {
	if footprintErr != nil {
		return fmt.Errorf("whether it has anything in this cluster could not be established, "+
			"and a record is only removed on a positive answer: %w", footprintErr)
	}
	if len(found) > 0 {
		return fmt.Errorf("instance %q has %s in this cluster, so its record still describes "+
			"something real. Uninstall it from the cluster that holds it before removing the record",
			instance, strings.Join(found, " and "))
	}
	return nil
}

// instanceFootprint lists everything this cluster holds that belongs to instance, in the
// order a bootstrap writes it — earliest durable artifact first. An empty list with a nil
// error is the ONLY answer that means "nothing here".
//
// 🔴 THE DECLARATION IS FIRST BECAUSE OF WHERE IT SITS IN THE PIPELINE, AND THAT ORDERING
// IS WHAT MAKES THE WHOLE CHECK SAFE. The eleven bootstrap steps write, in order: the
// cluster claim (2), the core components (4), the DECLARATION (5), the rendered
// configuration and the instance namespace (6), the infrastructure and the Secrets dcctl
// mints (7), the Helm release (8). So a run that got far enough to apply OpenTofu — far
// enough for its local tfstate to describe real cluster objects — necessarily wrote its
// declaration first, and the declaration check keeps its record. The record can only be
// cleared for a run that died before step 5, which has written nothing anywhere.
//
// The cluster LOCK is deliberately not one of these. It is not durable state, this very
// command takes one before it gets here, and counting it would make every destroy of a
// stranded record refuse itself.
func instanceFootprint(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface, instance string) ([]string, error) {
	var found []string

	// readInstanceCR already fails rather than answering on anything it cannot read,
	// including an Instance CRD that is not installed — isInstanceNotFound separates a
	// missing OBJECT from a missing RESOURCE TYPE and only the first reads as absent.
	// Keeping the record on a cluster with no CRD is the right direction anyway: the
	// operator installs the CRD before the release (step 4 before step 8), so a cluster
	// holding a release has one, and a cluster that somehow does not is a cluster whose
	// answer we do not have.
	inst, err := readInstanceCR(ctx, dyn, instance)
	if err != nil {
		return nil, fmt.Errorf("reading the declaration for %q: %w", instance, err)
	}
	if inst != nil {
		found = append(found, fmt.Sprintf("a declaration (Instance %q)", instance))
	}

	// The namespace is matched by NAME ALONE, with no label check, and that is the
	// opposite of removeInstanceNamespace on purpose. That function is deciding what to
	// DELETE, so it deletes only what it can prove is ours; this one is deciding whether
	// anything is here, so a namespace carrying the instance's name is disqualifying
	// whoever labelled it.
	if _, err := typed.CoreV1().Namespaces().Get(ctx, instance, metav1.GetOptions{}); err == nil {
		found = append(found, fmt.Sprintf("namespace %q", instance))
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("reading namespace %q: %w", instance, err)
	}

	// The Secrets dcctl mints live in the SHARED infrastructure namespace under names
	// that carry no instance, so they are found by their ownership annotation. Matched on
	// the owner NAME without requiring the managed-by stamp, for the same reason as the
	// namespace: a Secret annotated as this instance's is evidence about this instance
	// however it got there.
	secrets, err := typed.CoreV1().Secrets(infraNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the Secrets in %s to see whether %q minted any: %w",
			infraNamespace, instance, err)
	}
	var owned []string
	for i := range secrets.Items {
		if readOwnership(&secrets.Items[i]).instance == instance {
			owned = append(owned, secrets.Items[i].Name)
		}
	}
	if len(owned) > 0 {
		found = append(found, fmt.Sprintf("Secrets it minted in %s (%s)",
			infraNamespace, strings.Join(owned, ", ")))
	}

	return found, nil
}
