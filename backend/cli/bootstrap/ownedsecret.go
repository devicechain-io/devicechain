// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Ownership marks every Secret dcctl mints.
//
// 🔴 THESE ARE ANNOTATIONS, NOT LABELS, AND THE DISTINCTION IS LOAD-BEARING. A label
// invites selection, and a selector over "everything dcctl owns" is one typo away
// from a bulk delete of exactly the objects that cannot be regenerated. Ownership is
// asked about ONE Secret at a time, by name, at the moment of writing it.
const (
	annotationManagedBy = "devicechain.io/managed-by"
	annotationOwnerName = "devicechain.io/instance"
	annotationOwnerUID  = "devicechain.io/instance-uid"
	annotationMintedAt  = "devicechain.io/minted-at"
	managedByDcctl      = "dcctl"
	mintedAtTimeFormat  = time.RFC3339
)

// ownedSecret is one Secret dcctl is the author of.
//
// StringData rather than Data throughout, and that is not a convenience. The LwM2M
// pre-shared keys are base64 STRINGS that the service base64-decodes itself, so
// writing their bytes under Data hands the service something that either fails to
// decode loudly or — if the raw bytes happen to be legal base64 characters — decodes
// to a DIFFERENT key with no error anywhere. One field for every value removes the
// question of which encoding a given key wanted.
type ownedSecret struct {
	Name      string
	Namespace string
	Type      corev1.SecretType
	// Labels a controller keys on. `cnpg.io/reload` is the example that matters:
	// without it CloudNativePG does not act on a credential change.
	Labels map[string]string
	// Annotations something OUTSIDE dcctl keys on, merged rather than replaced so
	// they cannot displace the ownership stamps below — or each other.
	// `helm.sh/resource-policy: keep` is the example that matters: without it Helm
	// deletes a Secret that has left the chart's manifest.
	Annotations map[string]string
	Data        map[string]string
}

// secretOwnership says who wrote a Secret, as far as its annotations admit.
type secretOwnership struct {
	managed  bool
	instance string
	uid      string
	mintedAt string
}

func readOwnership(s *corev1.Secret) secretOwnership {
	a := s.GetAnnotations()
	return secretOwnership{
		managed:  a[annotationManagedBy] == managedByDcctl,
		instance: a[annotationOwnerName],
		uid:      a[annotationOwnerUID],
		mintedAt: a[annotationMintedAt],
	}
}

// ErrForeignSecret is returned when a Secret exists that dcctl did not write.
type ErrForeignSecret struct {
	Name, Namespace, Reason string
}

func (e *ErrForeignSecret) Error() string {
	return fmt.Sprintf("refusing to write Secret %s/%s: %s", e.Namespace, e.Name, e.Reason)
}

// writeOwnedSecret creates or updates a Secret that dcctl is the author of, and
// refuses to touch one it is not.
//
// 🔴 THE REFUSAL IS THE POINT, AND IT FAILS CLOSED. The damaging path is not a write
// that errors; it is a write that succeeds over a credential something else is
// already authenticating with — a database that stops admitting its own services, a
// root key that makes every encrypted row unreadable. Neither announces itself, and
// neither is undoable. So anything this cannot positively establish as its own is
// refused rather than merged or patched.
//
// The minted-at stamp is written once, at creation, and carried forward on every
// update. It cannot be reconstructed later: an unstamped Secret cannot be asked when
// it was minted, which is why it is here rather than in the work that will read it.
//
// The owner UID is the CR's, not the instance NAME, because names are reused. An
// instance destroyed and rebuilt under the same name gets a new declaration with a
// new UID, so a Secret left behind by a destroy that died halfway is recognisable as
// belonging to a generation that is gone — rather than being adopted, which is how a
// rebuild inherits a dead instance's credentials and reports it as reuse.
func writeOwnedSecret(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, instanceUID string,
	spec ownedSecret,
	now func() time.Time,
) error {
	if instanceUID == "" {
		// Not defensive padding: without a UID the staleness check below degrades to
		// a name comparison, and a name comparison is what lets a rebuild adopt the
		// previous generation's credentials. Refusing is the only honest answer.
		return fmt.Errorf("refusing to mint %s/%s: the instance declaration has no UID, so a "+
			"Secret left by a previous generation could not be told from this one's",
			spec.Namespace, spec.Name)
	}

	api := typed.CoreV1().Secrets(spec.Namespace)
	existing, err := api.Get(ctx, spec.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return createOwnedSecret(ctx, api, instance, instanceUID, spec, now)
	case err != nil:
		return fmt.Errorf("reading Secret %s/%s: %w", spec.Namespace, spec.Name, err)
	}

	own := readOwnership(existing)
	switch {
	case !own.managed:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it carries no %s=%s annotation, so dcctl did not write it. Something else is the "+
				"author of this credential, and overwriting it would break whatever is using it",
			annotationManagedBy, managedByDcctl)}
	case own.instance != instance:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it belongs to instance %q, not %q", own.instance, instance)}
	case own.uid != instanceUID:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it was minted for a previous %s instance (declaration %s, this run is %s). A destroy "+
				"left it behind; run `dcctl destroy %s` before bootstrapping this name again, so "+
				"the rebuild does not inherit the old instance's credentials",
			instance, own.uid, instanceUID, instance)}
	}

	// Ours. Keep the original minted-at — the value is when this credential came into
	// existence, and an update that rewrote it would report every re-run as a
	// rotation, which is the one question the stamp exists to answer.
	updated := existing.DeepCopy()
	applyOwnedSecretFields(updated, spec)
	setAnnotations(updated, instance, instanceUID, own.mintedAt)
	if _, err := api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating Secret %s/%s: %w", spec.Namespace, spec.Name, err)
	}
	return nil
}

func createOwnedSecret(
	ctx context.Context,
	api secretWriter,
	instance, instanceUID string,
	spec ownedSecret,
	now func() time.Time,
) error {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: spec.Namespace},
	}
	applyOwnedSecretFields(s, spec)
	setAnnotations(s, instance, instanceUID, now().UTC().Format(mintedAtTimeFormat))
	if _, err := api.Create(ctx, s, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another writer won the race between the Get above and this Create. The
			// safe answer is the same as for any Secret we did not write: refuse, and
			// let the operator look at what appeared.
			return &ErrForeignSecret{spec.Namespace, spec.Name,
				"it appeared between reading and writing it, so another writer is active"}
		}
		return fmt.Errorf("creating Secret %s/%s: %w", spec.Namespace, spec.Name, err)
	}
	return nil
}

// secretWriter is the slice of the typed client this file needs, named so the create
// path can be exercised without standing up a whole clientset.
type secretWriter interface {
	Create(ctx context.Context, s *corev1.Secret, opts metav1.CreateOptions) (*corev1.Secret, error)
}

func applyOwnedSecretFields(s *corev1.Secret, spec ownedSecret) {
	s.Type = spec.Type
	// Replace rather than merge. A key that has left the spec has left the Secret;
	// leaving it behind means a rotation that drops a field still serves the old one
	// to anything that kept reading it.
	s.StringData = map[string]string{}
	for k, v := range spec.Data {
		s.StringData[k] = v
	}
	s.Data = nil
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	for k, v := range spec.Labels {
		s.Labels[k] = v
	}
	if len(spec.Annotations) > 0 && s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	for k, v := range spec.Annotations {
		s.Annotations[k] = v
	}
}

func setAnnotations(s *corev1.Secret, instance, uid, mintedAt string) {
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[annotationManagedBy] = managedByDcctl
	s.Annotations[annotationOwnerName] = instance
	s.Annotations[annotationOwnerUID] = uid
	if mintedAt != "" {
		s.Annotations[annotationMintedAt] = mintedAt
	}
}
