// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Provenance annotations. These are METADATA rather than status fields, and the
// choice is deliberate rather than stylistic.
//
// dcctl is the only writer of the spec; the operator is the only writer of the
// status. Putting "which dcctl last applied this" in the status would give that
// subresource two writers with no field-ownership split between them — a
// conflict this design has not settled and should not create in passing for a
// value that is pure provenance. An annotation has exactly one writer here and
// needs no arbitration.
const (
	// AnnotationLastAppliedBy records the dcctl version that last wrote the spec.
	AnnotationLastAppliedBy = "core.devicechain.io/last-applied-by"
	// AnnotationLastAppliedAt records when it did.
	AnnotationLastAppliedAt = "core.devicechain.io/last-applied-at"
)

// InstanceSpec is the DESIRED state of a DeviceChain instance: everything dcctl
// needs to reproduce this bootstrap from a different machine, and nothing else.
//
// 🔴 THE LIST IS CLOSED, AND IT IS TIGHTER THAN THE FILE IT REPLACES. dcctl used
// to record this in ~/.devicechain/<instance>/instance.json, under a rule
// already stated there: identifiers only — a name, a flag or a timestamp,
// nothing else. That file sits in a 0700 directory beside a private key, so its
// threat model is the worst available and the rule was still worth writing down.
//
// A CLUSTER-SCOPED CR IS DIFFERENT IN KIND. It is readable by anything holding
// cluster-wide get, and it turns up in `kubectl get -o yaml`, in GitOps diffs,
// in support bundles and in screenshots. So the rule tightens rather than
// relaxes, and two categories are named here so that nobody adds them later:
//
//   - NO VALUE THAT NAMES A FILESYSTEM PATH. --escrow-file,
//     --escrow-passphrase-file, --backup-credentials-file, --lwm2m-identities and
//     the --restore-* artifacts are reconnaissance on the operator's own machine,
//     and they are one-shot intents rather than properties of the instance.
//   - NO SECRET, AND NOTHING DERIVED FROM ONE. Not a password, not a hash, not a
//     seed, not a key. Credentials live in Secrets that dcctl writes; what a
//     consumer gets is a name.
//
// kubeContext is dropped for a third reason: it is a name in the WRITER's
// kubeconfig, meaningless on anyone else's machine, and circular — you need the
// context to reach the CR that would tell you the context. It stays local, as
// the one thing that legitimately remains machine-scoped.
//
// createdAt is dropped too, and not moved to the status: metadata.creationTimestamp
// is the same fact, maintained by the API server, and impossible to disagree with.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.profile) && size(self.profile) > 0 && has(self.enabledFunctionalAreas) && size(self.enabledFunctionalAreas) > 0)",message="set profile or enabledFunctionalAreas, not both: they are two ways of naming the same set and the chart treats them as mutually exclusive"
type InstanceSpec struct {
	// Provider names the infrastructure provider that resolved the cluster
	// ("local" today).
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="provider is immutable: it identifies the cluster this instance was bootstrapped into, and an instance cannot move between providers in place"
	Provider string `json:"provider"`

	// Cluster is the provider's name for the cluster holding this instance.
	//
	// 🔴 EMPTY IS A REAL VALUE AND IS HONEST. An instance bootstrapped into a
	// cluster dcctl did not create — an adopted --kube-context — has no
	// provider-side cluster name, and inventing one would make `dcctl destroy`
	// confident about a cluster nobody named.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cluster is immutable: it is half of the binding between this instance and the cluster it lives in, and rewriting it would point destroy at a different cluster"
	Cluster string `json:"cluster,omitempty"`

	// Managed reports whether the cluster itself is dcctl's to delete, as opposed
	// to one it was pointed at. `dcctl destroy` reads it: removing an instance from
	// a cluster somebody else owns must not remove the cluster.
	//
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="managed is immutable: it decides whether destroy may delete the cluster, and flipping it turns a scoped teardown into a cluster deletion"
	Managed bool `json:"managed"`

	// Profile names a curated set of functional areas to deploy (e.g. "default",
	// "full", "telemetry", "ingest-only") — ADR-022 decision 2. Mutually exclusive
	// with EnabledFunctionalAreas; an empty profile with no explicit set resolves
	// to "default": the standard system. "full" additionally ships the areas that
	// reach outside the instance (AI inference, outbound connectors, MCP).
	//
	// +optional
	Profile string `json:"profile,omitempty"`

	// EnabledFunctionalAreas is an explicit set of functional areas to deploy, as
	// an alternative to a named Profile (ADR-022 decision 2). The set is rejected
	// if it omits a required core area or an enabled area's hard dependency.
	//
	// +optional
	EnabledFunctionalAreas []string `json:"enabledFunctionalAreas,omitempty"`

	// HA applies the replicated messaging topology (ADR-020 A0).
	//
	// +optional
	HA bool `json:"ha,omitempty"`

	// Compact applies the small-footprint sizing preset.
	//
	// +optional
	Compact bool `json:"compact,omitempty"`

	// Monitoring reports whether the observability stack is part of this instance.
	// Stated positively rather than as the --no-monitoring flag it comes from: a
	// declaration reads as what the instance IS, and a reader should not have to
	// invert a negative to learn that Prometheus is installed.
	//
	// +optional
	Monitoring bool `json:"monitoring,omitempty"`

	// Host is the hostname the instance ingress is exposed on.
	//
	// +optional
	Host string `json:"host,omitempty"`

	// TLS reports whether the ingress serves HTTPS. Positive for the same reason
	// as Monitoring; it comes from --no-tls.
	//
	// +optional
	TLS bool `json:"tls,omitempty"`

	// ImageRegistry is where this instance's images are pulled from.
	//
	// +optional
	ImageRegistry string `json:"imageRegistry,omitempty"`

	// ImageVersion is the tag they are pulled at.
	//
	// +optional
	ImageVersion string `json:"imageVersion,omitempty"`

	// Restored reports that this instance's databases were recovered from an
	// archive rather than created empty.
	//
	// 🔴 A FACT, NEVER A PATH. That a restore happened is a property of the
	// instance and belongs to anyone reading it; WHERE the archive was is a
	// one-shot argument on one operator's command line. Recording the second here
	// would put a filesystem layout into a cluster-readable object to no one's
	// benefit.
	//
	// +optional
	Restored bool `json:"restored,omitempty"`

	// RestoredAt is when that happened.
	//
	// +optional
	RestoredAt *metav1.Time `json:"restoredAt,omitempty"`
}

// InstanceStatus is the OBSERVED state, and it is the operator's to write.
//
// Nothing here is an input. If dcctl needs a value to reproduce a bootstrap it
// belongs in the spec, and if a value is only ever read by a human it belongs in
// a condition with a reason that says what was seen.
type InstanceStatus struct {
	// ObservedGeneration is the spec generation this status was computed from.
	// Without it a reader cannot tell a status that reflects the current spec from
	// one left over from the previous — and "stale but plausible" is the failure
	// mode a status exists to prevent.
	//
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report what the operator can see of the instance's workloads.
	//
	// 🔴 ABSENCE IS REPORTED WITH A REASON, NEVER AS HEALTH. A resource whose CRD
	// is not installed, one that has not been created yet, and one that is failing
	// are three different answers, and collapsing any of them into "not Ready"
	// throws away the only part a human can act on.
	//
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,shortName=dci
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
//+kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster`
//+kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profile`
//+kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.imageVersion`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Instance is the declaration of a DeviceChain instance.
//
// 🔴 CLUSTER-SCOPED, AND THAT IS LOAD-BEARING RATHER THAN INCIDENTAL. The
// instance namespace does not exist when this object is created — creating it is
// part of the bootstrap this object describes — so a namespaced CR would have
// nowhere to live at the one moment it is needed. Cluster scope also makes the
// instance id unique cluster-wide, which is what lets the object serve as the
// claim two operators race for rather than as a record each of them keeps.
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceList contains a list of Instance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
