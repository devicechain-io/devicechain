// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// InstanceReconciler reconciles a Instance object.
//
// Static workload rendering (namespace, ConfigMaps, Deployments, Services per
// functional area) has moved to the Helm chart at deploy/helm/devicechain
// (ADR-022 decision 4); the operator no longer imperatively stamps it. The one
// genuine control-loop responsibility that remains on the Instance is OBSERVED
// STATUS: on every reconcile it reads the chart-rendered Deployments and writes
// what it saw (conditions, per-area replicas and images, the running version) to
// Instance.status. It is the ONLY writer of that status — dcctl asserts intent in
// annotations and claims through a Lease, and never writes status.
//
// 🔴 CONFIG HOT-RELOAD IS NOT ON THAT LIST, AND IT USED TO BE. It was removed
// because it describes something this platform deliberately does not do, and
// naming it as a follow-up told readers to wait for it — the claim reached the
// published docs that way. Configuration lives in two planes and neither one
// reloads in place:
//
//   - The NON-DATABASE config — infrastructure coordinates and credentials — is
//     a mounted file, read ONCE at startup (core.LoadInstanceConfiguration, which
//     says so itself: "hence Load rather than Reload"). A change is adopted by
//     CYCLING THE POD, via the checksum annotations the chart stamps on each
//     per-area Deployment's pod template. Nothing in any service watches, polls
//     or re-reads that file. That is the whole mechanism.
//   - Everything dynamic — per-tenant ceilings, shed priority, DETECT rules,
//     connectors, dashboards, notification policy — is DATABASE-backed and
//     already changes live, with no restart and no operator involvement.
//
// So there is no third thing left for this loop to reload. It could not do it
// anyway: InstanceSpec is cluster-scoped, therefore public, and by its own rule
// holds nothing derived from a secret, so it carries no configuration to
// reconcile from — and the RBAC below covers instances only, not the config
// Secret or ConfigMap.
type InstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Now is the clock; nil means time.Now. Tests set it to move time without waiting.
	Now func() time.Time
}

func (r *InstanceReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch

// Reconcile observes an Instance and writes its OBSERVED status.
//
// Nothing here changes a workload: they are rendered by Helm, and deleting the
// Instance is ordinary resource lifecycle. The status is recomputed from scratch
// on every pass, from the Deployments in the instance's namespace, so it is
// correct after a missed event and needs no memory of the previous pass. The
// write is skipped when nothing moved, which is what stops the status update's own
// watch event from becoming a loop.
//
// 🔴 THE dcctl ANNOTATIONS ARE NOT READ HERE, WITH ONE EXCEPTION. Status is what the
// operator saw; the phase annotation is what a dcctl run intended. Folding one into the
// other would make the operator pretend to know something only the CLI does.
//
// The exception is the published broker-certificate expiry dates
// (AnnotationBrokerCertificateNotAfter, AnnotationBrokerCANotAfter). The operator has no
// access to Secrets, so the step that mints or renews the certificate publishes a
// non-secret date and the operator compares it with its OWN clock; the condition it
// writes is therefore an observation about time, not a copy of an assertion. Nothing
// else is read from the annotations, the operator remains the only status writer, and
// absent or unreadable dates are reported as Unknown rather than trusted.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &corev1beta1.Instance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(corev1beta1.InstanceNamespace(instance.Name)),
		client.MatchingLabels{instanceLabel: instance.Name}); err != nil {
		return ctrl.Result{}, err
	}

	now := r.now()
	desired := computeStatus(instance, deployments.Items, now)
	// 🔴 TIME IS AN INPUT NO EVENT ANNOUNCES. A certificate crosses its warning window
	// and then its expiry with nothing in the cluster changing, so every pass schedules
	// its own next look (a published expiry is re-read at least hourly).
	_, recheck := certificateVerdict(instance, now)
	result := ctrl.Result{RequeueAfter: recheck}
	if apiequality.Semantic.DeepEqual(instance.Status, desired) {
		return result, nil
	}
	// A merge patch from the object as read, with no resourceVersion precondition.
	// Status has one writer, but not one process: the operator runs without leader
	// election, so two replicas during a rollout of the operator itself can both
	// patch. They compute the same status from the same Deployments and the patch
	// replaces whole fields, so the last write wins harmlessly. A spec or annotation
	// write by dcctl between the read and here must not fail the status write.
	patch := client.MergeFrom(instance.DeepCopy())
	instance.Status = desired
	if err := r.Status().Patch(ctx, instance, patch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Updated instance status", "instance", instance.Name)
	return result, nil
}

// deploymentToInstance maps a Deployment event to the Instance it belongs to, so
// the status follows a rollout rather than waiting for the CR to change.
func deploymentToInstance(_ context.Context, obj client.Object) []reconcile.Request {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil
	}
	if id := instanceForDeployment(d); id != "" {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: id}}}
	}
	return nil
}

// SetupWithManager wires the controller to reconcile on Instance changes.
//
// 🔴 NOTHING REGISTERED HERE MAY WATCH A CRD THAT IS OPTIONAL ON THE CLUSTER, and
// the constraint has already changed shape once. The operator used to be
// installed after the infrastructure apply, so CloudNativePG's CRDs were always
// present by the time this ran; ADR-080 moved the operator to the FRONT of the
// bootstrap so the Instance CRD exists before anything declares an instance, and
// for a while that meant CNPG, cert-manager and the monitoring stack did not exist
// yet when this ran. Today `dcctl install` puts all three in place before any
// bootstrap can start — but a cluster installed --no-cnpg or --no-monitoring
// legitimately never gets them, so a CRD this controller cannot count on is still
// a CRD it must not watch.
//
// A Watches/Owns source registered against a missing CRD does not fail fast and
// does not recover on its own either. controller-runtime retries GetInformer
// every 10s, but the controller wraps that wait in CacheSyncTimeout — 2 minutes
// by default, and this manager sets no override — and when it expires the
// manager returns the error and the process exits. A CRD that lands inside the
// window is picked up; anything slower is a crash-loop until it appears.
//
// Observed state for a resource that MAY be absent is read by an unstructured Get
// on a requeue instead, where the manager's mapper-backed client answers
// meta.IsNoMatchError for a missing CRD and IsNotFound for a missing object.
// Neither is health, and they are different answers: "CloudNativePG is not
// installed" and "the database has not been created yet" call for different
// reports. (clusterArchivePath in the CLI's restore.go is NOT the model — it
// deliberately collapses both into "not there", and it reads through the dynamic
// client, which has no RESTMapper, so a missing resource type comes back as a
// plain 404 and NoMatch never arises there at all.)
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		// Deployments are a core API and always present, so watching them is safe where
		// watching an optional CRD is not (above). Only an instance's own functional-area
		// workloads can enqueue anything.
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(deploymentToInstance),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				d, ok := o.(*appsv1.Deployment)
				return ok && instanceForDeployment(d) != ""
			}))).
		Complete(r)
}
