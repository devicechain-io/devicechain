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

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// TenantReconciler reconciles a Tenant object
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
			err := r.handleTenantDeleted(ctx, req)
			if err != nil {
				log.Error(err, "Unable to handle tenant delete")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	log.Info(fmt.Sprintf("Handling added/updated tenant: %+v", req.NamespacedName))

	// Get list of tenantmicroservices indexed by microservice id
	tmsbymsid, err := getTenantMicroservicesByMicroserviceId(tenant)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Find microservices where no tenantmicroservice exists for the tenant
	missing, err := getMicroservicesWithNoTenantMicroservice(ctx, tenant, tmsbymsid)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Add tenant microservice for those that were missing.
	for _, ms := range missing {
		tms, err := handleMissingTenantMicroservice(tenant, ms)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Added missing tenant microservice (%s/%s)", tms.Spec.TenantId, tms.Spec.MicroserviceId))
	}

	// Create tenant config map if not found
	_, err = getTenantConfigMap(tenant.ObjectMeta.Name, tenant.ObjectMeta.Namespace)
	if err != nil {
		cmap, err := createTenantConfigMap(tenant.ObjectMeta.Name, tenant.ObjectMeta.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Created tenant config map '%s'", cmap.ObjectMeta.Name))
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Tenant{}).
		Complete(r)
}

// Handle case where there is no tenantmicroservice for a tenant/microservice combination.
func handleMissingTenantMicroservice(tenant *v1beta1.Tenant, ms v1beta1.Microservice) (*v1beta1.TenantMicroservice, error) {
	return v1beta1.CreateTenantMicroservice(v1beta1.TenantMicroserviceCreateRequest{
		InstanceId:     tenant.GetObjectMeta().GetNamespace(),
		TenantId:       tenant.ObjectMeta.Name,
		MicroserviceId: ms.ObjectMeta.Name})
}

// Get map of tenant microservices indexed by microservice id.
func getTenantMicroservicesByMicroserviceId(tenant *v1beta1.Tenant) (map[string]v1beta1.TenantMicroservice, error) {
	// List tenant microservices with the given tenant label
	tmslist, err := v1beta1.GetTenantMicroservicesForTenant(v1beta1.TenantMicroserviceByTenantRequest{
		InstanceId: tenant.ObjectMeta.Namespace,
		TenantId:   tenant.ObjectMeta.Name})
	if err != nil {
		return nil, err
	}

	// Index all tenant microservices by microservice id
	tmsbymsid := map[string]v1beta1.TenantMicroservice{}
	for _, tms := range tmslist.Items {
		tmsbymsid[tms.Spec.MicroserviceId] = tms
	}

	return tmsbymsid, nil
}

// Get list of microservices that do not have a tenantmicroservice for tenant.
func getMicroservicesWithNoTenantMicroservice(ctx context.Context, tenant *v1beta1.Tenant,
	tmsbymsid map[string]v1beta1.TenantMicroservice) ([]v1beta1.Microservice, error) {
	log := logf.FromContext(ctx)

	mslist, err := v1beta1.ListMicroservices(v1beta1.MicroserviceListRequest{
		InstanceId: tenant.ObjectMeta.Namespace})
	if err != nil {
		return nil, err
	}

	// Loop through microservices and look up tenantmicroservices by id to find missing items
	missing := make([]v1beta1.Microservice, 0)
	for _, ms := range mslist.Items {
		if _, present := tmsbymsid[ms.ObjectMeta.Name]; !present {
			missing = append(missing, ms)
		}
	}
	log.Info(fmt.Sprintf("Found %d microservices without tenant microservices", len(missing)))

	return missing, nil
}

// Delete tenant ingress resource.
func (r *TenantReconciler) deleteTenantIngress(ctx context.Context, req ctrl.Request) error {
	igname := generateIngressName(req.Namespace, req.Name)
	ingress := &netv1.Ingress{}
	if err := r.Get(ctx, igname, ingress); err == nil {
		r.Delete(context.Background(), ingress)
	}
	return nil
}

// Handle a deleted tenant
func (r *TenantReconciler) handleTenantDeleted(ctx context.Context, req ctrl.Request) error {
	log := logf.FromContext(ctx)

	// Find all existing tenant microservices for tenant
	matches, err := v1beta1.GetTenantMicroservicesForTenant(v1beta1.TenantMicroserviceByTenantRequest{
		InstanceId: req.NamespacedName.Namespace,
		TenantId:   req.NamespacedName.Name})
	if err != nil {
		return err
	}

	// Loop through tenant microservices and delete them
	log.Info(fmt.Sprintf("Found %d tenant microservices that will be deleted.", len(matches.Items)))
	for _, tms := range matches.Items {
		tms, err := v1beta1.DeleteTenantMicroservice(v1beta1.TenantMicroserviceDeleteRequest{
			InstanceId:           req.NamespacedName.Namespace,
			TenantMicroserviceId: tms.ObjectMeta.Name})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("Deleted tenant microservice '%s' due to tenant delete.", tms.ObjectMeta.Name))
	}

	// Delete config map associated with tenant
	cmap, err := deleteTenantConfigMap(req.Name, req.Namespace)
	if err != nil {
		return err
	}
	log.Info(fmt.Sprintf("Deleted tenant config map '%s'.", cmap.ObjectMeta.Name))

	// Delete tenant ingress.
	return r.deleteTenantIngress(ctx, req)
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

	// Attempt to create the namespace.
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
