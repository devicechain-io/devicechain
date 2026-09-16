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
	annotationManagedBy  = "devicechain.io/managed-by"
	annotationOwnerKind  = "devicechain.io/owner-kind"
	annotationOwnerName  = "devicechain.io/instance"
	annotationOwnerUID   = "devicechain.io/instance-uid"
	annotationClusterUID = "devicechain.io/cluster-uid"
	annotationMintedAt   = "devicechain.io/minted-at"
	managedByDcctl       = "dcctl"
	mintedAtTimeFormat   = time.RFC3339
)

// ownerKind says what a Secret belongs to: one instance, or the cluster every
// instance on it shares.
//
// 🔴 A KIND, NOT A CLUSTER UID WRITTEN INTO THE INSTANCE FIELDS. Every reader of these
// annotations — the second-instance guard, the destroy footprint, credential reuse,
// the upgrade read-back — treats `devicechain.io/instance` as naming an instance. A
// cluster-owned Secret stamped there with an invented or empty name is read by each of
// them as an instance the cluster holds, or as a stamp that cannot be attributed, and
// the second of those refuses every bootstrap. A separate kind is what lets each reader
// ask the question it actually means.
type ownerKind string

const (
	ownerInstance ownerKind = "instance"
	// ownerCluster is the kind for the credentials the CLUSTER prerequisites are built
	// from — the shared relational store, the object store, the backup destination and
	// the dashboard. Keyed on the kube-system namespace UID, for the reason a Secret is
	// keyed on a declaration UID rather than a name: a rebuilt cluster is a different
	// cluster, and must not inherit the last one's credentials as reuse.
	ownerCluster ownerKind = "cluster"
)

// secretOwner is who a Secret belongs to. Name is the instance name for an
// instance-owned Secret and empty for a cluster-owned one; UID is the instance
// declaration's UID or the cluster's.
type secretOwner struct {
	Kind ownerKind
	Name string
	UID  string
}

func instanceOwner(name, uid string) secretOwner {
	return secretOwner{Kind: ownerInstance, Name: name, UID: uid}
}

func clusterOwner(uid string) secretOwner {
	return secretOwner{Kind: ownerCluster, UID: uid}
}

func (o secretOwner) String() string {
	if o.Kind == ownerCluster {
		return fmt.Sprintf("this cluster (%s)", o.UID)
	}
	return fmt.Sprintf("instance %q", o.Name)
}

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
	// Scope is what the Secret belongs to. The zero value is an INSTANCE, so a spec
	// has to say "cluster" out loud to be shared.
	Scope ownerKind
}

// ownerFor resolves a Secret's scope against the run that is writing or reading it.
func ownerFor(scope ownerKind, st *State) secretOwner {
	if scope == ownerCluster {
		return clusterOwner(st.ClusterUID)
	}
	return instanceOwner(st.Instance, st.InstanceUID)
}

// secretOwnership says who wrote a Secret, as far as its annotations admit.
type secretOwnership struct {
	managed  bool
	owner    secretOwner
	mintedAt string
}

// readOwnership reads the stamp back.
//
// A Secret carrying no owner-kind is an INSTANCE's: that is every Secret dcctl wrote
// before the kind existed, and the instance annotations it does carry say so without
// ambiguity. An unrecognised kind is kept as written, so it compares unequal to every
// owner this build knows and is refused as foreign rather than guessed at.
func readOwnership(s *corev1.Secret) secretOwnership {
	a := s.GetAnnotations()
	own := secretOwnership{
		managed:  a[annotationManagedBy] == managedByDcctl,
		mintedAt: a[annotationMintedAt],
	}
	switch kind := ownerKind(a[annotationOwnerKind]); kind {
	case ownerCluster:
		own.owner = clusterOwner(a[annotationClusterUID])
	case "", ownerInstance:
		own.owner = instanceOwner(a[annotationOwnerName], a[annotationOwnerUID])
	default:
		own.owner = secretOwner{Kind: kind}
	}
	return own
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
	owner secretOwner,
	spec ownedSecret,
	now func() time.Time,
) error {
	if owner.UID == "" {
		// Not defensive padding: without a UID the staleness check below degrades to
		// a name comparison, and a name comparison is what lets a rebuild adopt the
		// previous generation's credentials. Refusing is the only honest answer.
		what := "the instance declaration has no UID"
		if owner.Kind == ownerCluster {
			what = "the cluster's identity is not known"
		}
		return fmt.Errorf("refusing to mint %s/%s: %s, so a Secret left by a previous "+
			"generation could not be told from this one's", spec.Namespace, spec.Name, what)
	}

	api := typed.CoreV1().Secrets(spec.Namespace)
	existing, err := api.Get(ctx, spec.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return createOwnedSecret(ctx, api, owner, spec, now)
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
	case own.owner.Kind != owner.Kind:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it belongs to %s, and this run writes it as %s's. A Secret does not change hands "+
				"between an instance and the cluster; rebuild whichever wrote it the other way",
			own.owner, owner)}
	case own.owner.Name != owner.Name:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it belongs to instance %q, not %q", own.owner.Name, owner.Name)}
	case own.owner.UID != owner.UID && owner.Kind == ownerCluster:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it was minted for a different cluster (%s; this one is %s). A cluster's identity "+
				"does not change while it lives, so this Secret was carried here from elsewhere "+
				"and is not this cluster's credential", own.owner.UID, owner.UID)}
	case own.owner.UID != owner.UID:
		return &ErrForeignSecret{spec.Namespace, spec.Name, fmt.Sprintf(
			"it was minted for a previous %s instance (declaration %s, this run is %s). A destroy "+
				"left it behind; run `dcctl destroy %s` before bootstrapping this name again, so "+
				"the rebuild does not inherit the old instance's credentials",
			owner.Name, own.owner.UID, owner.UID, owner.Name)}
	}

	// Ours. Keep the original minted-at — the value is when this credential came into
	// existence, and an update that rewrote it would report every re-run as a
	// rotation, which is the one question the stamp exists to answer.
	updated := existing.DeepCopy()
	applyOwnedSecretFields(updated, spec)
	setAnnotations(updated, owner, own.mintedAt)
	if _, err := api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating Secret %s/%s: %w", spec.Namespace, spec.Name, err)
	}
	return nil
}

func createOwnedSecret(
	ctx context.Context,
	api secretWriter,
	owner secretOwner,
	spec ownedSecret,
	now func() time.Time,
) error {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: spec.Namespace},
	}
	applyOwnedSecretFields(s, spec)
	setAnnotations(s, owner, now().UTC().Format(mintedAtTimeFormat))
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

func setAnnotations(s *corev1.Secret, owner secretOwner, mintedAt string) {
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[annotationManagedBy] = managedByDcctl
	s.Annotations[annotationOwnerKind] = string(owner.Kind)
	// Exactly one owner's fields, and the other's removed. A Secret carrying both an
	// instance name and a cluster UID would be read correctly by readOwnership and
	// WRONGLY by anything that looks at one annotation alone — which is how the
	// footprint check reads it.
	if owner.Kind == ownerCluster {
		s.Annotations[annotationClusterUID] = owner.UID
		delete(s.Annotations, annotationOwnerName)
		delete(s.Annotations, annotationOwnerUID)
	} else {
		s.Annotations[annotationOwnerName] = owner.Name
		s.Annotations[annotationOwnerUID] = owner.UID
		delete(s.Annotations, annotationClusterUID)
	}
	if mintedAt != "" {
		s.Annotations[annotationMintedAt] = mintedAt
	}
}
