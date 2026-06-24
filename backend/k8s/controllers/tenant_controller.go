/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package controllers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// TenantReconciler reconciles a Tenant object.
//
// Under the shared-microservice model (ADR-001) a Tenant does NOT get its own
// set of pods. The controller maintains the tenant's ConfigMap; the shared
// microservice deployments owned by the Instance discover tenants at runtime
// and scope work by NATS subject + TimescaleDB partition. The ConfigMap is
// owned by the Tenant, so Kubernetes garbage-collects it on tenant deletion.
type TenantReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenants,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenants/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenants/finalizers,verbs=update
func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tenant := &v1beta1.Tenant{}
	if err := r.Get(ctx, req.NamespacedName, tenant); err != nil {
		// The tenant ConfigMap is owned by the Tenant and garbage-collected on
		// deletion; nothing to do here.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cmap := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      getTenantConfigMapName(tenant.ObjectMeta.Name),
		Namespace: tenant.ObjectMeta.Namespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cmap, func() error {
		return controllerutil.SetControllerReference(tenant, cmap, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, err
	}

	log.Info(fmt.Sprintf("Reconciled tenant '%s'", tenant.ObjectMeta.Name))
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Tenant{}).
		Owns(&v1.ConfigMap{}).
		Complete(r)
}

// Get name of tenant config map
func getTenantConfigMapName(tid string) string {
	return fmt.Sprintf("%s-%s-%s", "dct", tid, "config")
}
