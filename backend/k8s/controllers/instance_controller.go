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
// 🔴 NOTHING REGISTERED HERE MAY WATCH A CRD THE BOOTSTRAP INSTALLS LATER, and
// that constraint is newer than it looks. The operator used to be installed
// after the infrastructure apply, so CloudNativePG's CRDs were already present
// by the time this ran; ADR-080 moves the operator to the FRONT of the pipeline
// so the Instance CRD exists before anything declares an instance, which means
// this now runs at a moment when CNPG, cert-manager and the monitoring stack do
// not exist yet.
//
// A Watches/Owns source registered against a missing CRD does not degrade — the
// informer never syncs, the manager's cache-sync deadline expires, and the whole
// controller exits. Observed state for a resource that may be absent is read by
// an unstructured Get on a requeue instead, where "the CRD is not installed"
// (meta.IsNoMatchError) and "the object is not there" are two different answers
// and neither is health. restore.go:273-282 in the CLI is the worked example.
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		Complete(r)
}
