// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// instanceGVR addresses the cluster-scoped Instance resource.
var instanceGVR = schema.GroupVersionResource{
	Group: "core.devicechain.io", Version: "v1beta1", Resource: "instances",
}

// InstanceSpecFrom builds the declaration this run would write.
//
// 🔴 IT TAKES THE RESOLVED STATE, NOT argv, and that is the whole point of doing
// it here. Every value below has already been settled — the image source in the
// command layer, the cluster binding by EnsureCluster, the area set by
// ResolveEnabledAreas — so what lands in the cluster is what this run ACTUALLY
// deployed, not what was asked for. A declaration built from flags would record
// an empty registry as an empty registry and leave the next reader to re-derive
// a default that may have changed between releases.
//
// Two fields are deliberately inverted from the flags they come from. --no-tls
// and --no-monitoring are conveniences on a command line, where the default is
// implicit; a declaration is read by someone asking what this instance IS, and
// making them invert a negative to learn that Prometheus is installed is a
// needless step in front of the answer.
func InstanceSpecFrom(st *State, binding ClusterBinding, provider string) dcv1beta1.InstanceSpec {
	// The ingress host is RESOLVED here, not copied. stepRenderConfig defaults it
	// into st.Values, and the declaration is written before that step runs — so
	// reading the raw field would record an omitted host on every default
	// bootstrap, which is precisely the "leave the next reader to re-derive a
	// default that may have changed between releases" this function exists to
	// avoid. One definition of the default, consulted twice.
	host := st.IngressHost
	if host == "" {
		host = DefaultIngressHost
	}

	spec := dcv1beta1.InstanceSpec{
		Provider:      provider,
		Cluster:       binding.Cluster,
		Managed:       binding.Managed,
		Profile:       st.Profile,
		HA:            st.HA,
		Compact:       st.Compact,
		Monitoring:    !st.NoMonitoring,
		CNPG:          !st.NoCNPG,
		GrafanaSSO:    grafanaSSOEnabled(st),
		Host:          host,
		TLS:           !st.NoTLS,
		ImageRegistry: st.ImageRegistry,
		ImageVersion:  st.ImageVersion,
		Restored:      st.Restore.Active(),
	}
	// The DELTA, not the expansion. st.EnabledAreas is the resolved union the chart
	// is told about; st.EnableAreas is what the operator asked for on top of the
	// profile. Recording the union would freeze a profile's membership into the
	// declaration, and a profile's membership is a property of the RELEASE — "full"
	// is contractually exhaustive, so it gains an area whenever the platform does.
	// A later dcctl would then deploy the old contents from this object and the new
	// ones from the equivalent command line.
	if len(st.EnableAreas) > 0 {
		spec.ExtraFunctionalAreas = append([]string(nil), st.EnableAreas...)
	}
	if spec.Restored {
		now := metav1.NewTime(time.Now().UTC())
		spec.RestoredAt = &now
	}
	return spec
}

// ValidateInstanceSpec judges a declaration on its own, before anything is
// written or acted on.
//
// The CRD carries the same rules as CEL, which is what stops a hand-edited CR.
// This is the check that runs on the SUPPORTED path, where dcctl is the only
// writer — so it is the one that has to be right, and the one worth testing.
func ValidateInstanceSpec(spec dcv1beta1.InstanceSpec) error {
	if spec.Provider == "" {
		return fmt.Errorf("the instance declaration names no provider, so nothing could later " +
			"tell which kind of cluster this instance lives in")
	}
	if spec.RestoredAt != nil && !spec.Restored {
		return fmt.Errorf("the instance declaration carries a restore timestamp and says the " +
			"instance was not restored")
	}
	if spec.Restored && spec.RestoredAt == nil {
		return fmt.Errorf("the instance declaration says this instance was restored and carries " +
			"no timestamp for it")
	}
	// 🔴 THE AREA SET IS JUDGED AGAINST THE CATALOG, not merely against itself.
	// This is the read-back check: a declaration is an INPUT to a later run, and an
	// unknown area or an unmet hard dependency in it is a bootstrap that fails
	// somewhere in the chart, minutes in, rather than here. ResolveEnabledAreas is
	// the same function the command layer runs over the same two values, so a
	// declaration that passes here is one dcctl can act on.
	if _, err := ResolveEnabledAreas(spec.Profile, spec.ExtraFunctionalAreas); err != nil {
		return fmt.Errorf("the instance declaration names an area set this build cannot "+
			"deploy: %w", err)
	}
	return nil
}

// ValidateInstanceSpecChange judges a declaration against the one already in the
// cluster.
//
// 🔴 THE IMMUTABLE FIELDS ARE THE BINDING, AND THE REASON IS DESTROY. provider,
// cluster and managed together answer "which cluster is this instance in, and is
// that cluster ours to delete". Rewriting any of them on a re-run does not move
// the instance — the instance stays exactly where it is — it moves the ANSWER,
// so a later `dcctl destroy` reads a binding that describes somewhere else.
// `managed` is the sharpest: flipping it false-to-true turns a scoped teardown
// into a cluster deletion.
//
// Everything else is expected to change. That is what a re-run is for.
func ValidateInstanceSpecChange(existing, desired dcv1beta1.InstanceSpec) error {
	for _, f := range []struct {
		name        string
		was, is     string
		consequence string
	}{
		{"provider", existing.Provider, desired.Provider,
			"an instance cannot move between providers in place"},
		{"cluster", existing.Cluster, desired.Cluster,
			"a later `dcctl destroy` would be pointed at a different cluster"},
		{"managed", fmt.Sprint(existing.Managed), fmt.Sprint(desired.Managed),
			"this decides whether destroy may delete the CLUSTER, not just the instance"},
	} {
		if f.was != f.is {
			return fmt.Errorf("this run would change the instance's %s from %q to %q, and that "+
				"binding is fixed at bootstrap: %s. If the instance really does live somewhere "+
				"else now, destroy it from the cluster that holds it and bootstrap it again",
				f.name, f.was, f.is, f.consequence)
		}
	}
	return ValidateInstanceSpec(desired)
}

// ReadInstanceCR returns the declaration in the cluster, or nil when there is
// none.
//
// 🔴 nil MEANS "THERE IS NO DECLARATION", AND NOTHING ELSE. A CRD that is not
// installed and an API server that will not answer are both "we could not tell",
// and both fail — the same rule DeployedInstanceConfig follows, for the same
// reason: reading "cannot tell" as "nothing there" is the direction that
// silently re-bootstraps over a live instance.
func ReadInstanceCR(ctx context.Context, kubeContext, id string) (*dcv1beta1.Instance, error) {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster to read the instance declaration: %w", err)
	}
	return readInstanceCR(ctx, dyn, id)
}

func readInstanceCR(ctx context.Context, dyn dynamic.Interface, id string) (*dcv1beta1.Instance, error) {
	obj, err := dyn.Resource(instanceGVR).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		if isInstanceNotFound(err, id) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the instance declaration %q: %w. Refusing to continue: "+
			"if a declaration IS there, treating this as a fresh install would re-bootstrap over "+
			"a live instance", id, err)
	}
	inst := &dcv1beta1.Instance{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, inst); err != nil {
		return nil, fmt.Errorf("decoding the instance declaration %q: %w", id, err)
	}
	return inst, nil
}

// isInstanceNotFound distinguishes "this instance is not declared" from "the
// Instance CRD is not installed", which the dynamic client reports IDENTICALLY.
//
// 🔴 THIS IS THE DISTINCTION THE FUNCTION ABOVE PROMISES AND ALMOST DID NOT MAKE.
// A Get through the dynamic client carries no RESTMapper, so a missing resource
// TYPE comes back from the API server as a plain 404 — apierrors.IsNotFound is
// true for both, and meta.IsNoMatchError never fires. Reading the first as "no
// declaration" is the direction that re-bootstraps over a live instance: an
// operator pointed at a cluster whose CRDs have not been installed yet would be
// told the instance is new.
//
// The two are separable by the Details the API server attaches: a missing OBJECT
// names the object and its group, a missing RESOURCE TYPE names neither.
func isInstanceNotFound(err error, id string) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	var se apierrors.APIStatus
	if !errors.As(err, &se) {
		return false
	}
	d := se.Status().Details
	return d != nil && d.Name == id && d.Group == instanceGVR.Group
}

// instanceToUnstructured converts the typed object for the dynamic client.
//
// The TYPE stays the source of truth even though the client is dynamic, which is
// what keeps the closed list closed: a field that is not on InstanceSpec cannot
// reach the cluster by being spelled correctly in a map literal.
func instanceToUnstructured(inst *dcv1beta1.Instance) (*unstructured.Unstructured, error) {
	inst.APIVersion = dcv1beta1.GroupVersion.String()
	inst.Kind = "Instance"
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(inst)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: raw}, nil
}

// WriteInstanceCR records the declaration in the cluster: creating it when there
// is none, updating the spec when there is.
//
// 🔴 IT READS BEFORE IT WRITES, AND THE READ IS NOT AN OPTIMISATION. An update
// has to be judged against what is already there — see
// ValidateInstanceSpecChange — because the fields that must not move are exactly
// the ones a blind write would move silently.
//
// The claim semantics that make this safe under two operators (create-or-adopt
// with a compare-and-swap, and a heartbeat) are a separate concern and land with
// them. What is here is the declaration itself.
func WriteInstanceCR(ctx context.Context, kubeContext, id string, spec dcv1beta1.InstanceSpec, dcctlVersion string) error {
	if err := ValidateInstanceSpec(spec); err != nil {
		return err
	}
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to record the instance declaration: %w", err)
	}

	existing, err := readInstanceCR(ctx, dyn, id)
	if err != nil {
		return err
	}
	if existing != nil && existing.DeletionTimestamp != nil {
		// 🔴 A TERMINATING DECLARATION IS NOT ADOPTABLE, and saying so is more
		// honest than appearing to resume it. Kubernetes deletion is one-way:
		// once deletionTimestamp is set there is no API to clear it, so an
		// "adopt" here would write a spec into an object that is going to vanish
		// the moment its finalizer clears — a bootstrap that reports success over
		// a declaration with a delete already committed against it.
		return fmt.Errorf("instance %q is being destroyed (its declaration is marked for deletion); "+
			"finish it with `dcctl destroy %s`, or release the declaration without destroying "+
			"anything with `dcctl instances release %s`", id, id, id)
	}

	inst := &dcv1beta1.Instance{}
	if existing != nil {
		if err := ValidateInstanceSpecChange(existing.Spec, spec); err != nil {
			return err
		}
		inst = existing.DeepCopy()
	}
	inst.Name = id
	if existing != nil {
		// 🔴 THE RESTORE FACT IS CARRIED FORWARD, and this is the difference between
		// a declaration and a transcript of the last command line. A restore only
		// happens when the cluster is CREATED; every re-run after it is flagless, so
		// a wholesale spec replacement would write restored=false over an instance
		// whose databases genuinely did come from an archive — quietly, on a green
		// run. The API server refuses the write too; this is what stops dcctl
		// attempting it.
		if existing.Spec.Restored && !spec.Restored {
			spec.Restored = true
			spec.RestoredAt = existing.Spec.RestoredAt
		}
	}
	inst.Spec = spec
	applyProvenance(inst, dcctlVersion)
	setPhase(inst, dcv1beta1.PhaseBootstrapping)
	addFinalizer(inst)

	obj, err := instanceToUnstructured(inst)
	if err != nil {
		return err
	}
	if existing == nil {
		_, err = dyn.Resource(instanceGVR).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("recording the instance declaration %q: %w", id, err)
		}
		return nil
	}
	// The object carries the resourceVersion the read returned, which makes this
	// Update a compare-and-swap: a declaration that changed underneath us is
	// refused with a Conflict rather than overwritten.
	//
	// Its job is narrower than it was in the design's first draft. Mutual
	// exclusion between two dcctl runs is the claim Lease's (claim.go); what is
	// left for the precondition to catch is a HUMAN editing the CR during a run,
	// which the Lease knows nothing about. A conflict here refuses — it does not
	// re-read and retry, because a retry loop against a concurrent editor is how
	// the edit gets silently reverted.
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("the declaration for instance %q changed while this run was writing it; "+
				"something else is editing it — re-run once you know what", id)
		}
		return fmt.Errorf("updating the instance declaration %q: %w", id, err)
	}
	return nil
}

// setPhase records what this run is TRYING to do. See AnnotationPhase: it is
// intent, and it is deliberately not in status, which belongs to the operator.
func setPhase(inst *dcv1beta1.Instance, phase string) {
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[dcv1beta1.AnnotationPhase] = phase
}

// addFinalizer keeps a hand-deleted declaration readable until destroy has run.
// See FinalizerInstance for why the immutability rules depend on it.
func addFinalizer(inst *dcv1beta1.Instance) {
	for _, f := range inst.Finalizers {
		if f == dcv1beta1.FinalizerInstance {
			return
		}
	}
	inst.Finalizers = append(inst.Finalizers, dcv1beta1.FinalizerInstance)
}

// removeFinalizer drops it, reporting whether it was there.
func removeFinalizer(inst *dcv1beta1.Instance) bool {
	out := inst.Finalizers[:0]
	found := false
	for _, f := range inst.Finalizers {
		if f == dcv1beta1.FinalizerInstance {
			found = true
			continue
		}
		out = append(out, f)
	}
	inst.Finalizers = out
	return found
}

// applyProvenance stamps who wrote this declaration and when, leaving every other
// annotation alone.
//
// A named function rather than four inline lines so the set it writes can be
// asserted on: annotations are published with the object and sit outside the
// closed-list checks that cover the spec.
func applyProvenance(inst *dcv1beta1.Instance, dcctlVersion string) {
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[dcv1beta1.AnnotationLastAppliedBy] = dcctlVersion
	inst.Annotations[dcv1beta1.AnnotationLastAppliedAt] = time.Now().UTC().Format(time.RFC3339)
}

// SetInstancePhase records intent on an existing declaration, leaving the spec
// untouched.
//
// 🔴 Callers that are about to DELETE something must call this first, not after.
// The value of the phase is entirely in its ordering: written before the first
// destructive act, a process killed at any later point leaves a declaration that
// says Destroying, which is true. Written afterwards, the same kill leaves one
// that still says Ready over an instance that is half gone — which is the failure
// the field exists to prevent, reintroduced by call-site ordering.
//
// A declaration that is not there is not an error. Phase is a courtesy to the next
// reader, and failing a destroy because the thing it is destroying was never
// declared would be the tail wagging the dog.
func SetInstancePhase(ctx context.Context, kubeContext, id, phase string) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to record the instance phase: %w", err)
	}
	existing, err := readInstanceCR(ctx, dyn, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}

	// 🔴 A MERGE PATCH ON ONE ANNOTATION, NOT AN UPDATE OF THE WHOLE OBJECT, and
	// the difference is not stylistic. An Update carries the resourceVersion it
	// read, which makes it a compare-and-swap — correct for the spec, wrong here.
	// The operator writes status.conditions on this same object, and a status
	// write bumps resourceVersion like any other; a phase write that lost that
	// race would fail, and by the spec path's own rule a failure refuses. That
	// would let a routine status update abort a bootstrap, or worse, stop destroy
	// recording that it had started.
	//
	// Nothing contends for this annotation. dcctl is its only writer, so there is
	// no lost update to protect against, and a patch that touches one key leaves
	// every other field — including whatever the operator just wrote — alone.
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, dcv1beta1.AnnotationPhase, phase)
	if _, err := dyn.Resource(instanceGVR).Patch(ctx, id, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("recording phase %q on instance %q: %w", phase, id, err)
	}
	return nil
}

// ReleaseInstanceDeclaration removes the finalizer so the declaration can be
// deleted, and deletes it if a delete is already pending.
//
// 🔴 This is the escape hatch a finalizer obliges, and it is a real command rather
// than a documented `kubectl patch` because the alternative is a cluster carrying
// an object nobody can remove. It destroys NOTHING: the namespaces, databases and
// volumes the declaration describes are all still there afterwards, which is
// exactly why the caller has to say out loud that it is what they want.
func ReleaseInstanceDeclaration(ctx context.Context, kubeContext, id string) (released bool, err error) {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return false, fmt.Errorf("connecting to the cluster to release the instance declaration: %w", err)
	}
	existing, err := readInstanceCR(ctx, dyn, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, fmt.Errorf("instance %q is not declared on this cluster", id)
	}
	inst := existing.DeepCopy()
	if !removeFinalizer(inst) && inst.DeletionTimestamp == nil {
		return false, nil
	}
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		return false, err
	}
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("removing the finalizer from instance %q: %w", id, err)
	}
	// With the finalizer gone a pending delete completes on its own. When there is
	// no pending delete the caller asked to release a live declaration, so delete
	// it explicitly — otherwise the command would report success having done
	// nothing a reader can see.
	if inst.DeletionTimestamp == nil {
		if err := dyn.Resource(instanceGVR).Delete(ctx, id, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("deleting the declaration for instance %q: %w", id, err)
		}
	}
	return true, nil
}
