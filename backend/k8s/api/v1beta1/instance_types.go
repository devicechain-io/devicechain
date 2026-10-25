// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Provenance annotations.
//
// These are METADATA rather than status fields, and the choice is a scheduling
// one rather than a principle. The claim protocol that lands next puts a holder
// and a heartbeat in the status, written by dcctl, alongside the conditions the
// operator writes — two writers on one subresource, which that design records as
// an open question. Provenance does not need to wait on the answer, and putting
// it in the status now would pre-commit to one.
const (
	// AnnotationLastAppliedBy records the dcctl version that last wrote the spec.
	AnnotationLastAppliedBy = "core.devicechain.io/last-applied-by"
	// AnnotationLastAppliedAt records when it did.
	AnnotationLastAppliedAt = "core.devicechain.io/last-applied-at"

	// AnnotationPhase records what the last dcctl run was TRYING to do:
	// Bootstrapping, Upgrading, Ready, Failed or Destroying.
	//
	// This is intent, not observation, which is why it is an annotation rather
	// than a status field — status belongs to the operator and describes what it
	// can see of the workloads. The two answer different questions and a reader
	// needs both: "someone is part-way through destroying this" is not something
	// the workloads can report, because the thing it warns about is that they are
	// about to stop existing.
	//
	// 🔴 dcctl destroy writes PhaseDestroying BEFORE it deletes anything. That
	// ordering is the whole value of the field: a destroy killed at any later
	// point leaves a CR that says Destroying rather than one that still says
	// Ready over an instance that is half gone.
	AnnotationPhase = "core.devicechain.io/phase"

	// AnnotationBootstrapUnfinished marks a declaration whose first bootstrap has not
	// yet ended successfully. dcctl bootstrap writes it when its run is BUILDING the
	// instance (no configuration document yet, or the record already there), and the
	// write that records PhaseReady — by either verb — removes it in the same patch.
	//
	// 🔴 ABSENCE MEANS LIVE. `dcctl bootstrap` lets a re-run through over an instance
	// whose configuration document exists only while this reads exactly
	// BootstrapUnfinished AND the phase is not PhaseReady, so a declaration written by
	// an earlier release, a hand-edited value, a missing declaration, or the record
	// beside Ready (which only a dcctl from before the record leaves) all keep the
	// instance protected. A phase write that moves a declaration AWAY from Ready drops
	// an inert record, so moving the phase can never revive one.
	AnnotationBootstrapUnfinished = "core.devicechain.io/bootstrap-unfinished"
)

// BootstrapUnfinished is the only value AnnotationBootstrapUnfinished takes.
const BootstrapUnfinished = "true"

// The values AnnotationPhase takes.
//
// 🔴 PhaseUpgrading IS NOT COSMETIC. Every write of the declaration used to stamp
// Bootstrapping, `dcctl upgrade` included — so an instance that had ever been
// upgraded read "a bootstrap is part-way through" for the rest of its life, and
// the annotation's own promise (what the LAST run was trying to do) was false for
// the one instance that had been touched most recently. A verb that moves an
// instance needs its own word, and a phase nobody can distinguish from another
// verb's is worse than no phase: it is read as knowledge.
const (
	PhaseBootstrapping = "Bootstrapping"
	PhaseUpgrading     = "Upgrading"
	PhaseReady         = "Ready"
	PhaseFailed        = "Failed"
	PhaseDestroying    = "Destroying"
)

// FinalizerInstance keeps a hand-deleted Instance readable until dcctl has
// destroyed what it declares.
//
// 🔴 It protects the immutability rules below, not just the object. Those rules
// are CEL transition rules, and a transition rule compares self against oldSelf —
// so a CR that has been deleted and recreated has no oldSelf and every one of
// them passes vacuously. Without this finalizer, `kubectl delete` followed by a
// fresh apply repoints `cluster` in two steps that each look legitimate, and
// destroy then runs against someone else's cluster. The finalizer makes the
// delete half of that sequence not complete.
//
// The cost is the one every finalizer has: an object whose remover is gone cannot
// be deleted. `dcctl instances release` exists for exactly that, and it is
// documented rather than left as folklore, because an undocumented finalizer is
// how a cluster acquires an object nobody can remove.
const FinalizerInstance = "core.devicechain.io/instance"

// InstanceSpec is the DESIRED state of a DeviceChain instance.
//
// WHAT BELONGS HERE, AND THE TEST THAT MATTERS MOST. Two questions have to be
// asked of every candidate field, and only one of them is about safety.
//
// The first is whether it is safe to publish. This object is cluster-scoped, so
// it appears in `kubectl get -o yaml`, in GitOps diffs, in support bundles and
// in screenshots — a wider audience than the 0700 directory the local instance
// record lives in. Nothing that names a filesystem path may appear here, and
// nothing derived from a secret: not a password, not a hash, not a seed, not a
// key. Credentials live in Secrets, and what a consumer gets is a name.
//
// The second question is the one a closed list makes easy to forget: is anything
// MISSING that a second operator needs? A declaration exists so that someone on
// another machine can re-run the bootstrap and converge to the same instance. A
// field that changes what gets deployed and is not recorded here does not make
// the object safer — it makes it wrong, silently, and the failure lands on
// whoever trusted it. This list was audited flag by flag against what
// `helmValues` and `infraVars` actually emit, and it grew when it was.
//
// The two rules pull in opposite directions and both are load-bearing. A field
// that is unsafe to publish and needed for convergence is a design problem to
// solve elsewhere, not a field to add quietly.
//
// 🔴 THE CLUSTER RULE CONTAINS NO STRING LITERAL, AND THAT IS NOT AN AESTHETIC
// CHOICE EITHER. The natural way to write it compares two ternaries that default
// to an empty string — and Go's doc-comment formatting rewrites a PAIR OF ASCII
// APOSTROPHES into a single U+201D right double quotation mark. That is gofmt,
// applied deterministically to every marker on this declaration, so the CEL
// expression the API server receives is one it refuses to parse and the CRD does
// not install at all.
//
// (This paragraph cannot show you the characters. Writing them here would make
// gofmt rewrite THIS sentence too, which is how the behaviour was finally found.)
//
// 🔴 The has()-only form is not merely a restatement: it is STRICTER in one
// quadrant. The literal form defaulted both sides to an empty string, so it read
// an absent cluster and an explicit empty one as equal; this form distinguishes
// them, and refuses a move between the two. That is the better answer — an
// instance either records a cluster or does not — but it is a behaviour change
// unreachable through dcctl (the field is omitempty) and reachable through
// kubectl, so it is pinned by a test rather than left to be rediscovered.
//
// This already happened once and was "fixed" by hand-editing the generated YAML,
// which left the marker and the artifact disagreeing until the next `make
// manifests`. Writing the rule in terms of `has()` alone removes the construct
// gofmt reaches for, so the fix cannot be undone by running a formatter.
//
// Three CEL rules live at the SPEC level rather than on the fields they govern,
// and that is not a style choice. A transition rule on a field is only evaluated
// when the field is present on BOTH sides, so `self == oldSelf` on an optional
// field silently permits absent→value and value→absent — which for `cluster`
// meant a two-edit repoint of the binding that `dcctl destroy` reads. The spec
// object is always present, so a rule written here always runs.
//
// +kubebuilder:validation:XValidation:rule="has(self.cluster) == has(oldSelf.cluster) && (!has(self.cluster) || self.cluster == oldSelf.cluster)",message="cluster is immutable: it is half of the binding between this instance and the cluster it lives in, and rewriting it (including by removing or adding it) would point destroy at a different cluster"
// +kubebuilder:validation:XValidation:rule="!(has(oldSelf.restored) && oldSelf.restored) || (has(self.restored) && self.restored)",message="restored cannot be unset: it records that this instance's databases came from an archive, which stays true. An ordinary re-run does not restore anything and must not erase the fact that an earlier one did"
// +kubebuilder:validation:XValidation:rule="!(has(self.restoredAt) && !(has(self.restored) && self.restored))",message="restoredAt is set on an instance that says it was not restored"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.objectStore) || has(self.objectStore)",message="objectStore cannot be removed: the logos and artifacts on it would be stranded, and the volume deleted with the declaration that named it"
type InstanceSpec struct {
	// Provider is the infrastructure provider that resolved this instance's
	// cluster. Immutable: an instance cannot move between providers in place.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="provider is immutable: it identifies the kind of cluster this instance was bootstrapped into"
	Provider string `json:"provider"`

	// Cluster is the provider's name for the cluster holding this instance. Empty
	// is a real value: an instance bootstrapped into a cluster dcctl did not create
	// has no provider-side cluster name, and inventing one would make destroy
	// confident about a cluster nobody named. Immutable, enforced at the spec level
	// so that removing it is refused too.
	//
	// +optional
	Cluster string `json:"cluster,omitempty"`

	// Managed reports whether the cluster itself is dcctl's to delete, as opposed
	// to one it was pointed at. Immutable, and required rather than optional: this
	// decides whether destroy may remove the CLUSTER, so an absent value must not
	// be able to mean anything.
	//
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="managed is immutable: it decides whether destroy may delete the cluster, and flipping it turns a scoped teardown into a cluster deletion"
	Managed bool `json:"managed"`

	// Profile names a curated set of functional areas ("default", "full",
	// "telemetry", "ingest-only"). Empty means the default profile.
	//
	// +optional
	Profile string `json:"profile,omitempty"`

	// ExtraFunctionalAreas are areas deployed on TOP of the profile.
	//
	// This records the operator's request rather than the set it expanded to, and
	// the difference matters on a re-run. A profile's membership is a property of
	// the release: "full" is contractually exhaustive, so it gains an area whenever
	// the platform does. Freezing the expansion would make a later dcctl deploy the
	// OLD contents of a profile from this declaration while deploying the new ones
	// from the equivalent command line — two paths to the same instance that no
	// longer agree. What was actually deployed is an observation, and belongs in
	// the status.
	//
	// +optional
	ExtraFunctionalAreas []string `json:"extraFunctionalAreas,omitempty"`

	// HA applies the replicated messaging topology.
	//
	// +optional
	HA bool `json:"ha,omitempty"`

	// Compact applies the small-footprint sizing preset.
	//
	// +optional
	Compact bool `json:"compact,omitempty"`

	// Monitoring reports whether the observability stack is part of this instance.
	//
	// Stated positively, like CNPG and TLS below, rather than as the --no-monitoring
	// flag it comes from. A command line has an implicit default and can afford a
	// negative; a declaration is read by someone asking what this instance IS, and
	// making them invert a negative to learn that Prometheus is installed puts a
	// step in front of the answer.
	//
	// +optional
	Monitoring bool `json:"monitoring,omitempty"`

	// CNPG reports whether this instance installs the CloudNativePG operator and
	// the backup plugin, as opposed to using ones the cluster already runs.
	//
	// It is recorded because leaving it out does not make the declaration safer, it
	// makes it wrong: Helm cannot adopt objects it did not create, so a second
	// operator who re-runs without knowing this fails the infrastructure apply with
	// an ownership error on a cluster that was fine.
	//
	// +optional
	CNPG bool `json:"cnpg,omitempty"`

	// GrafanaSSO reports whether Grafana's login is wired to DeviceChain SSO.
	//
	// Recorded for the same reason and with a sharper edge: this seeds an OAuth
	// client into the user-management config and turns the integration on in the
	// infrastructure apply, so a re-run without it does not merely skip the feature
	// — it removes both halves from a live instance.
	//
	// +optional
	GrafanaSSO bool `json:"grafanaSSO,omitempty"`

	// Host is the hostname the instance ingress is exposed on, resolved rather than
	// copied from the flag: an omitted host would leave the next reader to
	// re-derive a default that may have moved between releases.
	//
	// +optional
	Host string `json:"host,omitempty"`

	// TLS reports whether the ingress serves HTTPS. This describes the INGRESS and
	// nothing else — the broker's own TLS is configured separately and is on by
	// default regardless of this value.
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
	// archive rather than created empty. A fact about the instance, never a path:
	// that a restore happened belongs to anyone reading this, while WHERE the
	// archive was is one operator's command line.
	//
	// Write-once. An ordinary re-run restores nothing and must not erase it.
	//
	// +optional
	Restored bool `json:"restored,omitempty"`

	// RestoredAt is when the restore that produced this instance completed.
	//
	// +optional
	RestoredAt *metav1.Time `json:"restoredAt,omitempty"`

	// MQTTFailClosed records the operator's choice of the fail-closed MQTT QoS 1
	// acknowledgement (the instance configuration's infrastructure.nats.mqttFailClosed).
	// A pointer, because there are three answers and only two of them are a decision:
	// absent means the choice was never made, so the platform's default applies;
	// true and false are the operator's own words, and an
	// explicit false is kept as false by every later upgrade.
	//
	// Fixed configuration, so it is recorded here and not in the free-form tuning
	// overlay: `dcctl upgrade` reads it back and writes it into the instance
	// configuration document it composes, which is the only way a setting survives a
	// version bump now that dcctl owns that document.
	//
	// +optional
	MQTTFailClosed *bool `json:"mqttFailClosed,omitempty"`

	// TrustedProxies are the CIDR prefixes written to the instance configuration's
	// infrastructure.http.trustedProxies: the proxies whose X-Forwarded-For header the
	// services may believe, which for the self-registration endpoint is what makes a
	// per-source rate limit a limit on a DEVICE and not on the ingress controller. dcctl
	// derives them from the cluster's node pod ranges unless the operator named some; this
	// records what was rendered, so an upgrade that cannot read the nodes keeps it.
	//
	// +optional
	TrustedProxies []string `json:"trustedProxies,omitempty"`

	// TrustedProxiesExplicit says TrustedProxies is the operator's own list (dcctl's
	// --trusted-proxies), which an upgrade keeps as written, rather than a derivation it
	// refreshes from the cluster.
	//
	// +optional
	TrustedProxiesExplicit bool `json:"trustedProxiesExplicit,omitempty"`

	// ObjectStore is where the instance keeps opaque binaries that do not belong in
	// the database: tenant branding logos and update artifacts. Absent means the
	// instance has no object store, and the operations that need one are refused
	// with OBJECT_STORE_NOT_CONFIGURED rather than written somewhere ephemeral.
	//
	// Everything here is a SETTING or a NAME. An S3 credential is never a field:
	// the reference below points at a Secret, and the Secret holds the key.
	//
	// +optional
	ObjectStore *ObjectStoreSpec `json:"objectStore,omitempty"`
}

// The object-store backends a declaration can name. An instance with no store has no
// ObjectStore block at all, so there is deliberately no "none" here: a third spelling
// of absence is one more thing a reader has to know means the same.
const (
	ObjectStoreFilesystem = "filesystem"
	ObjectStoreS3         = "s3"
)

// ObjectStoreSpec selects the instance's object store and carries its non-secret
// settings.
//
// +kubebuilder:validation:XValidation:rule="self.backend != \"s3\" || has(self.s3)",message="objectStore.s3 is required when backend is s3"
// +kubebuilder:validation:XValidation:rule="self.backend == \"s3\" || !has(self.s3)",message="objectStore.s3 is only valid when backend is s3"
// +kubebuilder:validation:XValidation:rule="self.backend == \"filesystem\" || (!has(self.size) && !has(self.storageClass) && !has(self.accessMode))",message="size, storageClass and accessMode describe the filesystem volume and are only valid when backend is filesystem"
// +kubebuilder:validation:XValidation:rule="self.backend != \"filesystem\" || has(self.size)",message="size is required when backend is filesystem"
// +kubebuilder:validation:XValidation:rule="self.backend == oldSelf.backend",message="objectStore.backend is immutable: nothing moves the objects already stored, so a different backend would leave them behind"
type ObjectStoreSpec struct {
	// Backend is where objects live: a volume the chart creates (filesystem) or an
	// S3-compatible bucket (s3).
	//
	// +kubebuilder:validation:Enum=filesystem;s3
	Backend string `json:"backend"`

	// Size is the requested size of the filesystem volume, as a Kubernetes quantity.
	//
	// +optional
	Size string `json:"size,omitempty"`

	// StorageClass is the filesystem volume's StorageClass. Empty selects the
	// cluster default.
	//
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessMode is the filesystem volume's access mode: ReadWriteOnce, or
	// ReadWriteMany when more than one pod must share the store.
	//
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteMany
	// +optional
	AccessMode string `json:"accessMode,omitempty"`

	// S3 is the bucket and its coordinates.
	//
	// +optional
	S3 *S3ObjectStoreSpec `json:"s3,omitempty"`
}

// S3ObjectStoreSpec is an S3-compatible bucket: AWS S3, or MinIO and the like.
type S3ObjectStoreSpec struct {
	// Bucket is the bucket objects are written to.
	//
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Region is the AWS region. Optional when Endpoint names an S3-compatible
	// service, which ignores it.
	//
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint overrides the AWS endpoint, for an S3-compatible service.
	//
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// UsePathStyle addresses the bucket in the URL path rather than the host name,
	// which MinIO and most S3-compatible servers require.
	//
	// +optional
	UsePathStyle bool `json:"usePathStyle,omitempty"`

	// CredentialsSecret names an existing Secret holding AWS_ACCESS_KEY_ID and
	// AWS_SECRET_ACCESS_KEY, which dcctl copies into the instance's namespace. Absent
	// means the services use the pod's own AWS identity (IRSA, an instance profile).
	//
	// +optional
	CredentialsSecret *SecretReference `json:"credentialsSecret,omitempty"`
}

// SecretReference names a Secret. It is a NAME: nothing the Secret holds appears
// in the declaration.
type SecretReference struct {
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// InstanceStatus is the OBSERVED state.
//
// Nothing here is an input: if dcctl needs a value to reproduce a bootstrap it
// belongs in the spec.
type InstanceStatus struct {
	// ObservedGeneration is the spec generation this status was computed from.
	// Without it a reader cannot tell a status that reflects the current spec from
	// one left over from the previous, and "stale but plausible" is the failure a
	// status exists to prevent.
	//
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report what the operator can see of the instance's workloads.
	//
	// Nothing writes these yet; the reconciler that will is a later slice. When it
	// does, absence must be reported with a reason rather than as health: a
	// resource whose CRD is not installed, one not created yet, and one that is
	// failing are three different answers, and collapsing any of them into "not
	// Ready" throws away the part a human can act on.
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
// The phase is a column because `kubectl get dci` is the one reader dcctl does not
// control, and an operator looking at a cluster mid-run is exactly who needs to see
// that a bootstrap, an upgrade or a destroy did not finish. An instance carrying no
// annotation renders blank, which is honest: nothing recorded what it was doing.
//
// 🔴 THE BACKSLASHES ARE REQUIRED AND MUST NOT BE TIDIED AWAY, AND THE TIDY VERSION
// FAILS SILENTLY. The API server evaluates this path itself — apiextensions-apiserver's
// tableconvertor, over client-go's jsonpath, with AllowMissingKeys(true) — and renders
// an EMPTY cell whenever the path errors or matches nothing. So the bracket spelling
// `['core.devicechain.io/phase']`, which does not resolve, produces a column that is
// blank for every instance rather than an error anybody sees. The backticks are what
// carry the backslashes through: controller-gen parses a backquoted marker argument as
// a raw string. Both halves are pinned by TestThePhaseColumnActuallyResolves, which
// drives the real evaluator the way the API server drives it.
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.metadata.annotations.core\.devicechain\.io/phase`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Instance is the declaration of a DeviceChain instance.
//
// Cluster-scoped, and that is load-bearing rather than incidental. The instance
// namespace does not exist when this object is created — creating it is part of
// the bootstrap this object describes — so a namespaced CR would have nowhere to
// live at the one moment it is needed. Cluster scope also makes the instance id
// unique cluster-wide, which is what lets the object serve as a claim two
// operators race for rather than as a record each of them keeps.
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is required. Without this an Instance carrying no spec at all is valid,
	// because the required fields INSIDE the spec are only checked once a spec
	// exists — so the object that declares nothing would pass every rule below.
	Spec   InstanceSpec   `json:"spec"`
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
