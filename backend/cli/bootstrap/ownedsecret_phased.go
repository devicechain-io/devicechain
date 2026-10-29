// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// Two-phase sync of a credentials Secret that dcctl writes outside the release (the Sparkplug broker
// passwords, the LwM2M pre-shared keys).
//
// 🔴 A SECRET SHRINKS ONLY AFTER THE RELEASE THAT STOPPED READING IT HAS SUCCEEDED. The running pods
// hold non-optional secretKeyRefs, and a ref is resolved again at every container start. Deleting the
// Secret, or dropping a key, BEFORE the release meant that a release that failed before replacing the
// pods left the old ones pointing at a key that no longer existed: their next restart (a node drain,
// an OOM kill) was a CreateContainerConfigError. So:
//
//   - BEFORE the release the sync is ADDITIVE (ownedSecret.Merge): the Secret is created, or keys are
//     added or updated, so every key the new release names exists. Nothing is removed, and a
//     withdrawal writes nothing. The Secret then holds the union of the old and the new keys, which
//     both generations of pods can resolve.
//   - AFTER a successful release a postReleaseStep removes what the release no longer references: the
//     keys outside the new set, or the whole Secret on a withdrawal. Only a Secret dcctl wrote is
//     touched, here as everywhere (foreignReason).
//
// A failed release therefore leaves the old pods working, and re-running the same command converges.
// A failed post step is not a failed run: the release has succeeded, so it is reported with the
// command that finishes it, and the same command run again does finish it.
//
// THE STAMP. The pods' stamp is derived from the Secret's resourceVersion (credentialsStamp), and
// removing a key moves the resourceVersion without changing anything a pod reads. Left alone, the
// next run that merely READS the Secret would stamp the new version, differ from the stamp the release
// carries, and roll the pods for nothing. So the prune records, in an annotation written in the same
// update, the revision the release was stamped with, and the stamp reads that annotation in preference
// to the resourceVersion. A later additive write that CHANGES a value drops the annotation, so a real
// rotation still rolls the pods; one that changes nothing leaves it, so a re-run rolls nothing.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// annotationStampRevision is the revision of a Secret that its pods' stamp was computed from, set by
// a prune so that removing an unreferenced key does not look like a change of what the pods read.
const annotationStampRevision = "devicechain.io/credentials-stamp-revision"

// stampRevision is what the credentials stamp is derived from: the revision the pods were last
// stamped with when a prune has recorded one, else the Secret's own resourceVersion.
func stampRevision(s *corev1.Secret) string {
	if rev := s.Annotations[annotationStampRevision]; rev != "" {
		return rev
	}
	return s.ResourceVersion
}

// secretValues is every key a Secret holds, as a client sees it: a read from an API server fills Data
// only, and a fake (or a Secret not yet submitted) may also carry StringData, which wins.
func secretValues(s *corev1.Secret) map[string]string {
	out := make(map[string]string, len(s.Data)+len(s.StringData))
	for k, v := range s.Data {
		out[k] = string(v)
	}
	for k, v := range s.StringData {
		out[k] = v
	}
	return out
}

// mergeOwnedSecretFields is applyOwnedSecretFields for an additive update: existing keys stay.
func mergeOwnedSecretFields(s *corev1.Secret, spec ownedSecret) {
	have := secretValues(s)
	changed := false
	for k, v := range spec.Data {
		if cur, ok := have[k]; !ok || cur != v {
			changed = true
		}
	}
	s.Type = spec.Type
	if changed {
		if s.StringData == nil {
			s.StringData = map[string]string{}
		}
		for k, v := range spec.Data {
			s.StringData[k] = v
		}
		// A value moved, so the pods must be stamped with the new version.
		delete(s.Annotations, annotationStampRevision)
	}
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	for k, v := range spec.Labels {
		s.Labels[k] = v
	}
}

// postReleaseStep is the second phase of a sync: it runs only once the release has succeeded. The
// zero value does nothing.
type postReleaseStep struct {
	// Secret is "<namespace>/<name>", for the warning.
	Secret string
	// Cleanup is the command that finishes the step by hand, for the warning.
	Cleanup string
	run     func(ctx context.Context) error
}

// finishOwnedSecrets is helmInstall's tail, apart from the cluster so a test can hand it a release
// that failed. A failed release runs nothing: the Secrets keep the union of the old and new keys, so
// the old pods can still restart. After a successful one each step runs, and one that fails is
// WARNED about, with the command that finishes it, and never turned into a failure of a run whose
// release succeeded; re-running the same command runs it again.
func finishOwnedSecrets(ctx context.Context, releaseErr error, steps []postReleaseStep, warn func(string)) error {
	if releaseErr != nil {
		return releaseErr
	}
	for _, s := range steps {
		if s.run == nil {
			continue
		}
		if err := s.run(ctx); err != nil {
			warn(fmt.Sprintf("the release succeeded, but Secret %s still holds credentials the release no longer uses: %v. "+
				"Finish it with: %s", s.Secret, err, s.Cleanup))
		}
	}
	return nil
}

// pruneOwnedSecret removes from a Secret dcctl wrote every key outside keep, recording stampRev (the
// revision the release was stamped with) in the same update. A Secret that is gone, or already holds
// only keep, is left as found. A Secret dcctl did not write is refused.
func pruneOwnedSecret(ctx context.Context, typed kubernetes.Interface, owner secretOwner, ns, name string,
	keep map[string]string, stampRev string) error {
	api := typed.CoreV1().Secrets(ns)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading Secret %s/%s: %w", ns, name, err)
	}
	if reason := foreignReason(readOwnership(existing), owner); reason != "" {
		return &ErrForeignSecret{ns, name, reason}
	}
	stale := false
	for k := range secretValues(existing) {
		if _, ok := keep[k]; !ok {
			stale = true
		}
	}
	if !stale {
		return nil
	}
	updated := existing.DeepCopy()
	for k := range secretValues(existing) {
		if _, ok := keep[k]; !ok {
			delete(updated.Data, k)
			delete(updated.StringData, k)
		}
	}
	if stampRev != "" {
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		updated.Annotations[annotationStampRevision] = stampRev
	}
	if _, err := api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("removing unreferenced keys from Secret %s/%s: %w", ns, name, err)
	}
	return nil
}

// refuseForeignSecret is the part of a withdrawal that can be decided before the release: a Secret of
// this name that dcctl did not write is refused now, with nothing changed, rather than discovered
// after the release.
func refuseForeignSecret(ctx context.Context, typed kubernetes.Interface, owner secretOwner, ns, name string) error {
	existing, err := typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading Secret %s/%s: %w", ns, name, err)
	}
	if reason := foreignReason(readOwnership(existing), owner); reason != "" {
		return &ErrForeignSecret{ns, name, reason}
	}
	return nil
}

// withdrawPostStep deletes the Secret (if it is ours) once the release no longer names it.
func withdrawPostStep(typed kubernetes.Interface, owner secretOwner, ns, name, rerun string) postReleaseStep {
	return postReleaseStep{
		Secret:  ns + "/" + name,
		Cleanup: fmt.Sprintf("kubectl -n %s delete secret %s (or %s)", ns, name, rerun),
		run: func(ctx context.Context) error {
			return deleteOwnedSecretIfOurs(ctx, typed, owner, ns, name)
		},
	}
}

// prunePostStep removes the keys outside keep once the release has succeeded, recording stampRev.
func prunePostStep(typed kubernetes.Interface, owner secretOwner, ns, name string, keep map[string]string, stampRev, rerun string) postReleaseStep {
	return postReleaseStep{
		Secret:  ns + "/" + name,
		Cleanup: rerun + ", with the same file",
		run: func(ctx context.Context) error {
			return pruneOwnedSecret(ctx, typed, owner, ns, name, keep, stampRev)
		},
	}
}
