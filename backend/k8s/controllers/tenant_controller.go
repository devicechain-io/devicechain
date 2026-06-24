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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// TenantReconciler reconciles a Tenant object.
//
// Under the shared-microservice model (ADR-001) a Tenant does NOT get its own
// set of pods. The controller maintains the tenant's ConfigMap and bootstrap
// status; the shared microservice deployments owned by the Instance discover
// tenants at runtime and scope work by NATS subject + TimescaleDB partition.
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
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Handling deleted tenant: %+v", req.NamespacedName))
			if err := r.handleTenantDeleted(ctx, req); err != nil {
				log.Error(err, "Unable to handle tenant delete")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	log.Info(fmt.Sprintf("Handling added/updated tenant: %+v", req.NamespacedName))

	// Create tenant config map if not found.
	if _, err := getTenantConfigMap(tenant.ObjectMeta.Name, tenant.ObjectMeta.Namespace); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		cmap, err := createTenantConfigMap(tenant.ObjectMeta.Name, tenant.ObjectMeta.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Created tenant config map '%s'", cmap.ObjectMeta.Name))
	}

	// Initialize bootstrap state. Actual dataset bootstrapping is performed by
	// the microservice runtime (follow-up: bootstrap-ordering work); the
	// operator only seeds the initial state here.
	if tenant.Status.BootstrapState == "" {
		tenant.Status.BootstrapState = v1beta1.TenantNotBootstrapped
		if err := r.Status().Update(ctx, tenant); err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Initialized bootstrap state for tenant '%s'", tenant.ObjectMeta.Name))
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Tenant{}).
		Complete(r)
}

// Handle a deleted tenant by removing its ConfigMap.
func (r *TenantReconciler) handleTenantDeleted(ctx context.Context, req ctrl.Request) error {
	log := logf.FromContext(ctx)

	cmap, err := deleteTenantConfigMap(req.Name, req.Namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	log.Info(fmt.Sprintf("Deleted tenant config map '%s'.", cmap.ObjectMeta.Name))
	return nil
}

// Get name of tenant config map
func getTenantConfigMapName(tid string) string {
	return fmt.Sprintf("%s-%s-%s", "dct", tid, "config")
}

// Create a new tenant config map
func createTenantConfigMap(tid string, ns string) (*v1.ConfigMap, error) {
	cmap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getTenantConfigMapName(tid),
			Namespace: ns,
		},
		Data: map[string]string{},
	}

	err := v1beta1.V1Client.Create(context.Background(), cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

// Get config map associated with tenant
func getTenantConfigMap(tid string, ns string) (*v1.ConfigMap, error) {
	cmap := &v1.ConfigMap{}
	err := v1beta1.V1Client.Get(context.Background(), client.ObjectKey{
		Name:      getTenantConfigMapName(tid),
		Namespace: ns,
	}, cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

// Delete config map associated with tenant
func deleteTenantConfigMap(tid string, ns string) (*v1.ConfigMap, error) {
	cmap, err := getTenantConfigMap(tid, ns)
	if err != nil {
		return nil, err
	}

	err = v1beta1.V1Client.Delete(context.Background(), cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}
