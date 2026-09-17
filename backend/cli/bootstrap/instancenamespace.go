// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// instanceNamespaceLabel is what says a namespace is an instance's. The chart writes it
// on everything it renders, the namespace included (devicechain.instanceLabels in
// templates/_helpers.tpl), and ensureNamespaceForRelease writes it on the namespace it
// creates ahead of the install — so every namespace either of them made for an instance
// carries it, which is what makes "absent" a safe reading of "not ours".
//
// 🔴 IT IS ALSO THE ESCAPE HATCH, AND THAT IS WHY IT IS A LABEL. There is no dcctl flow
// that pre-creates an instance namespace, so refusing one that exists costs nothing an
// operator was doing; the one thing they may legitimately want is to point an instance at
// a namespace they made themselves, and labelling it by hand is how they say so. Contrast
// the ownership keys in ownedsecret.go, which are annotations precisely so that nothing
// can ever SELECT on them.
const instanceNamespaceLabel = "devicechain.io/instance"

// ErrNamespaceUnavailable is the refusal of an instance that cannot own the namespace it
// is named after: one that already exists and is not this instance's.
//
// Typed for exactly the reason ErrHostTaken is: it is raised by stepCheckClusterSingletons,
// which TestTheSingletonStepRunsBeforeAnythingIsWritten holds ahead of the operator
// install and the Instance declaration, so this run has written nothing anywhere and the
// local record it wrote before the pipeline started describes an instance that was never
// built. The command layer takes that record back.
//
// 🔴 THE SEAM RAISES THE SAME REFUSAL UNTYPED, AND THAT IS DELIBERATE.
// ensureNamespaceForRelease reaches the same verdict from inside the infrastructure
// apply, where the operator, the declaration and the cluster lock are already written and
// the record is the only thing that can name them. Typing its refusal too would unwind the
// record of a half-built instance — the orphan that record exists to prevent.
type ErrNamespaceUnavailable struct{ Err error }

func (e *ErrNamespaceUnavailable) Error() string { return e.Err.Error() }
func (e *ErrNamespaceUnavailable) Unwrap() error { return e.Err }

// refuseANamespaceThisInstanceDoesNotOwn is the verdict on a namespace that already
// exists, and it is the same verdict wherever it is reached: the precheck takes it before
// the run writes anything, the seam takes it again immediately before the first write into
// the namespace itself.
//
// 🔴 AN INSTANCE OWNS ITS NAMESPACE, AND WHAT RIDES ON THAT IS THE ROOT KEY. dcctl writes
// the instance configuration Secret — the secret-store root key — plus the broker's TLS
// keypair and every database credential into the namespace named after the instance, and
// `dcctl destroy` deletes that namespace wholesale. Both halves are wrong against a
// namespace somebody else is using: the write scatters unrecoverable secrets into it, and
// the delete would take their work with it. Measured on a real cluster before this
// existed: a bootstrap named `monitoring` wrote the root key, a TLS private key and four
// database credentials into the monitoring namespace and was then refused by Helm, leaving
// destroy correctly refusing to clean up after it.
//
// existing is nil when the namespace is not there, which is the ordinary case and the
// only one that needs nothing said about it.
func refuseANamespaceThisInstanceDoesNotOwn(instance string, existing *corev1.Namespace) error {
	if existing == nil {
		return nil
	}
	// 🔑 A NAMESPACE ON ITS WAY OUT IS NOT A NAMESPACE, AND IT IS NOT SOMEBODY ELSE'S
	// EITHER. Kubernetes refuses new content in a terminating namespace, so a write would
	// fail with a sentence about "new content" that reads as a defect rather than as a
	// destroy that has not finished — and its labels are on their way out with it, so
	// judging ownership from them here would answer "foreign" about a namespace that is
	// about to stop existing. Ahead of the ownership branch for that reason.
	if existing.DeletionTimestamp != nil {
		return fmt.Errorf("namespace %q is still being deleted, so this instance cannot be "+
			"built into it yet: a previous `dcctl destroy` has not finished. Wait for the "+
			"namespace to go and run this again", instance)
	}
	if existing.Labels[instanceNamespaceLabel] == instance {
		return nil
	}
	return fmt.Errorf("namespace %q already exists and is not this instance's, so this instance "+
		"cannot be built into it: an instance OWNS the namespace it is named after — dcctl writes "+
		"its secret-store root key, its broker TLS keypair and every one of its database "+
		"credentials there, and `dcctl destroy %s` deletes the whole namespace. Refusing now, "+
		"before any of that is written.\n"+
		"  Build the instance under a name of its own, or — if that namespace really is meant to "+
		"be this instance's — say so and run this again:\n"+
		"    kubectl label namespace %s %s=%s",
		instance, instance, instance, instanceNamespaceLabel, instance)
}

// lookupNamespace returns the namespace, or nil if it is not there. A read failure is an
// error rather than a nil: "could not tell" is not "free".
func lookupNamespace(ctx context.Context, typed kubernetes.Interface, name string) (*corev1.Namespace, error) {
	ns, err := typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		return ns, nil
	case apierrors.IsNotFound(err):
		return nil, nil
	default:
		return nil, err
	}
}

// namespacePrecheckClient is the precheck's one cluster call. Indirected like the step's
// other cluster reads so the decision — including the property that it writes nothing —
// can be exercised without a cluster.
var namespacePrecheckClient = func(kubeContext string) (kubernetes.Interface, error) {
	_, _, typed, err := kubeClients(kubeContext)
	return typed, err
}

// precheckInstanceNamespace settles, before this run has written anything anywhere,
// whether this instance may own the namespace it is named after.
//
// 🔴 THE SEAM ON ITS OWN IS FOUR STEPS TOO LATE. ensureNamespaceForRelease is what stands
// between a foreign namespace and the instance configuration Secret, and it holds — but it
// runs inside the infrastructure apply, by which time the operator is installed, the
// Instance declaration exists, the cluster lock is held and the local record points at all
// of it. Taking the same decision here turns a half-built instance into a refusal that
// leaves nothing behind.
//
// 🔑 IT ANSWERS ONLY ABOUT A NAMESPACE THAT EXISTS, and that bound is worth stating because
// it is not the whole of the problem. A name the cluster will want LATER — `monitoring` on
// a `--compact` cluster that installed no monitoring stack — is free right now, so nothing
// here objects, and the collision surfaces only when somebody installs that component by
// hand afterwards. Closing that is a different mechanism (prefixing an instance's namespace
// so the two sets cannot meet) rather than a longer list of names to refuse.
//
// The refusals come back typed; a failure to READ does not, because a cluster that will
// not say what it holds is a failed run rather than a refused one.
func precheckInstanceNamespace(ctx context.Context, st *State) error {
	namespace := instanceNamespace(st.Instance)
	typed, err := namespacePrecheckClient(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to see whether namespace %q is this instance's: %w",
			namespace, err)
	}
	existing, err := lookupNamespace(ctx, typed, namespace)
	if err != nil {
		return fmt.Errorf("reading namespace %q to see whether this instance may be built into it: %w",
			namespace, err)
	}
	if err := refuseANamespaceThisInstanceDoesNotOwn(st.Instance, existing); err != nil {
		return &ErrNamespaceUnavailable{Err: err}
	}
	return nil
}
