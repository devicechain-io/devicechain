// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	dck8s "github.com/devicechain-io/dc-k8s/config"
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

// The namespaces `dcctl install` creates on a cluster that have no Go constant of their
// own, because dcctl never names them — it passes the OpenTofu defaults through and reads
// back what the apply reported.
//
// 🔴 THESE MIRROR deploy/opentofu/cluster/variables.tf, and a copy that drifts is worse
// than no copy: it reserves a name the install does not use while leaving free the one it
// does. Hand-copied rather than extracted from the embedded root because an extractor that
// stops matching returns "" — which reserves nothing, reports nothing, and looks exactly
// like a cluster with no such component. TestTheReservedClusterNamespacesMatchTheOpenTofuDefaults
// reads the embedded root and fails loudly on drift instead.
const (
	certManagerNamespace  = "cert-manager"  // variable "cert_manager_namespace"
	cnpgSystemNamespace   = "cnpg-system"   // variable "cnpg_namespace"
	ingressNginxNamespace = "ingress-nginx" // variable "ingress_nginx_namespace"
)

// ErrNamespaceUnavailable is the refusal of an instance that cannot own the namespace it
// is named after — a name `dcctl install` needs for something else, or a namespace that
// already exists and is not this instance's.
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

// namespacesTheClusterInstallOwns maps every namespace `dcctl install` can create to what
// it creates there, for the refusal to say.
//
// 🔴 SOURCED FROM THE CONSTANTS THE INSTALL ITSELF USES, never from a hand-written list of
// strings, so a namespace that moves moves here too. The operator's is not a constant at
// all — it is a kustomize setting read out of the rendered overlay, the same way
// ClaimClients reads it — and is passed in for that reason.
//
// 🔑 THIS IS NOT reservedInstanceNames AND MUST NOT BE FOLDED INTO IT. That list is the
// labels PostgreSQL already means something by; this one is namespaces. They answer
// different questions about the same string, and merging them recreates the two-lists-
// that-disagreed defect dcdir.go's single inventory exists to kill — a name would start
// being refused by whichever list was edited last.
func namespacesTheClusterInstallOwns(operatorNS string) map[string]string {
	owned := map[string]string{
		infraNamespace:        "the cluster's shared relational and object stores",
		monitoringNamespace:   "the monitoring stack",
		certManagerNamespace:  "cert-manager",
		cnpgSystemNamespace:   "the CloudNativePG operator and its backup plugin",
		ingressNginxNamespace: "the ingress controller",
	}
	if operatorNS != "" {
		owned[operatorNS] = "the DeviceChain operator, and it holds the cluster lock"
	}
	return owned
}

// namespacesKubernetesOwns are the namespaces that are on a cluster before dcctl ever
// sees it. An instance named after one of these could never have worked; refusing by name
// is only so the message says which one it is rather than "already exists".
var namespacesKubernetesOwns = map[string]string{
	"default":         "the namespace every client falls back to",
	"kube-system":     "where the control plane runs",
	"kube-public":     "the cluster's world-readable namespace",
	"kube-node-lease": "where the nodes write their heartbeats",
}

// kubeReservedPrefix is the prefix Kubernetes documents as reserved for its own
// namespaces. It is a convention rather than something the API server enforces, so a
// `kube-`-prefixed instance would in fact be created — into the space the next Kubernetes
// release is entitled to take.
const kubeReservedPrefix = "kube-"

// refuseAReservedNamespace stops an instance named after a namespace that is not an
// instance's to take.
//
// 🔴 THIS IS THE HALF THE SEAM STRUCTURALLY CANNOT DO, AND THE REASON IS TIMING. Every
// check ensureNamespaceForRelease makes is about a namespace that EXISTS; a `--compact`
// cluster installs no monitoring stack, so `monitoring` is a free name at bootstrap and an
// instance would be built into it with nothing objecting. The cost lands later and on
// somebody else: whoever installs the stack by hand afterwards finds it deleted the next
// time that instance is destroyed, because destroy deletes the namespace the instance owns.
func refuseAReservedNamespace(instance, operatorNS string) error {
	if what, ok := namespacesKubernetesOwns[instance]; ok {
		return fmt.Errorf("instance name %q is %s: it is Kubernetes' own namespace and not an "+
			"instance's to take. Build this instance under another name", instance, what)
	}
	if strings.HasPrefix(instance, kubeReservedPrefix) {
		return fmt.Errorf("instance name %q starts with %q, which Kubernetes reserves for its own "+
			"namespaces. Nothing stops the namespace being created, which is the problem: it would "+
			"sit in the space a later Kubernetes release is entitled to use. Build this instance "+
			"under another name", instance, kubeReservedPrefix)
	}
	if what, ok := namespacesTheClusterInstallOwns(operatorNS)[instance]; ok {
		return fmt.Errorf("instance name %q is the namespace `dcctl install` puts %s in, and an "+
			"instance owns the namespace it is named after — so building it here would make one "+
			"namespace answer to two owners, and `dcctl destroy %s` would delete that component "+
			"along with the instance. Build this instance under another name",
			instance, what, instance)
	}
	return nil
}

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

// renderedOperatorNamespace reads the operator's namespace out of the overlay, the same
// source ClaimClients and stepInstallCore use and for the same reason: it is a kustomize
// setting, and a constant here would be a second place to remember it. Rendered with no
// image because nothing is going to apply this.
func renderedOperatorNamespace() (string, error) {
	manifests, err := dck8s.RenderOperator("")
	if err != nil {
		return "", fmt.Errorf("rendering the operator overlay to find which namespace it takes: %w", err)
	}
	return operatorNamespace(manifests)
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
// 🔑 IT REFUSES A RESERVED NAME BEFORE IT TOUCHES THE CLUSTER, which is an ordering rather
// than an optimisation: that half of the answer does not depend on a cluster read, and a
// name `dcctl install` owns must be refused just as firmly on a cluster dcctl cannot
// currently reach as on one it can.
//
// The refusals come back typed; a failure to READ does not, because a cluster that will
// not say what it holds is a failed run rather than a refused one.
func precheckInstanceNamespace(ctx context.Context, st *State) error {
	operatorNS, err := renderedOperatorNamespace()
	if err != nil {
		return err
	}
	if err := refuseAReservedNamespace(st.Instance, operatorNS); err != nil {
		return &ErrNamespaceUnavailable{Err: err}
	}

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
