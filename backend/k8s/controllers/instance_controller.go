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
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		Complete(r)
}
