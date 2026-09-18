// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// InstanceReconciler reconciles a Instance object.
//
// Static workload rendering (namespace, ConfigMaps, Deployments, Services per
// functional area) has moved to the Helm chart at deploy/helm/devicechain
// (ADR-022 decision 4); the operator no longer imperatively stamps it. The
// genuine control-loop responsibilities that remain on the Instance — readiness
// status aggregation across the chart-rendered Deployments and config hot-reload
// — are tracked as follow-ups; this reconciler is the scaffold they land on.
type InstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/finalizers,verbs=update

// Reconcile observes an Instance. Workloads are rendered by Helm, so there is no
// child stamping here; deleting the Instance is handled by ordinary resource
// lifecycle. Status aggregation and hot-reload land on this loop later.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &corev1beta1.Instance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info(fmt.Sprintf("Observed instance '%s'", instance.ObjectMeta.Name))
	return ctrl.Result{}, nil
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
		Complete(r)
}
