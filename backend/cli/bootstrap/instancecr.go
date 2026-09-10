// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	spec := dcv1beta1.InstanceSpec{
		Provider:      provider,
		Cluster:       binding.Cluster,
		Managed:       binding.Managed,
		Profile:       st.Profile,
		HA:            st.HA,
		Compact:       st.Compact,
		Monitoring:    !st.NoMonitoring,
		Host:          st.IngressHost,
		TLS:           !st.NoTLS,
		ImageRegistry: st.ImageRegistry,
		ImageVersion:  st.ImageVersion,
		Restored:      st.Restore.Active(),
	}
	// The chart treats the two as mutually exclusive, so the declaration must too:
	// an explicit set REPLACES the profile rather than adding to it.
	if len(st.EnabledAreas) > 0 {
		spec.Profile = ""
		spec.EnabledFunctionalAreas = append([]string(nil), st.EnabledAreas...)
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
	if spec.Profile != "" && len(spec.EnabledFunctionalAreas) > 0 {
		return fmt.Errorf("the instance declaration carries both a profile (%q) and an explicit "+
			"area set (%v). They are two ways of naming the same thing and the chart treats them as "+
			"mutually exclusive, so recording both would leave the next run to pick one",
			spec.Profile, spec.EnabledFunctionalAreas)
	}
	if spec.RestoredAt != nil && !spec.Restored {
		return fmt.Errorf("the instance declaration carries a restore timestamp and says the " +
			"instance was not restored")
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
		if apierrors.IsNotFound(err) {
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

	inst := &dcv1beta1.Instance{}
	if existing != nil {
		if err := ValidateInstanceSpecChange(existing.Spec, spec); err != nil {
			return err
		}
		inst = existing.DeepCopy()
	}
	inst.Name = id
	inst.Spec = spec
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[dcv1beta1.AnnotationLastAppliedBy] = dcctlVersion
	inst.Annotations[dcv1beta1.AnnotationLastAppliedAt] = time.Now().UTC().Format(time.RFC3339)

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
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating the instance declaration %q: %w", id, err)
	}
	return nil
}
