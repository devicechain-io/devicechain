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

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

const (
	INSTANCE_CONFIG_NAME = "instance"

	// Environment variables passed to each shared microservice pod. Tenant
	// identity is intentionally absent: a single microservice pod serves all
	// tenants, discovering them at runtime via Tenant resources and scoping
	// work by NATS subject + TimescaleDB partition (ADR-001).
	ENV_INSTANCE_ID        = "DC_INSTANCE_ID"
	ENV_MICROSERVICE_ID    = "DC_MICROSERVICE_ID"
	ENV_MS_FUNCTIONAL_AREA = "DC_MS_FUNCTIONAL_AREA"

	// Default GraphQL port exposed by each microservice.
	PORT_GRAPHQL = 8080
)

// InstanceReconciler reconciles a Instance object
type InstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/finalizers,verbs=update
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instanceconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=microserviceconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services;configmaps;namespaces,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives an Instance toward its desired state: a dedicated namespace,
// an instance ConfigMap, and one shared Deployment + Service per microservice
// in the configuration catalog. Each microservice runs as a single deployment
// serving every tenant in the instance.
//
// All children are stamped with the Instance as controller-owner, so Kubernetes
// garbage-collects them when the Instance is deleted (no manual delete handling)
// and drift on any child re-triggers this reconcile via the Owns watches below.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &corev1beta1.Instance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		// On delete, owned children (namespace + everything in it) are
		// garbage-collected by Kubernetes; nothing to do here.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.assureNamespace(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.assureInstanceConfigMap(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileSharedMicroservices(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	log.Info(fmt.Sprintf("Reconciled instance '%s'", instance.ObjectMeta.Name))
	return ctrl.Result{}, nil
}

// SetupWithManager wires the controller to reconcile on Instance changes, on
// drift of any owned Deployment/Service/ConfigMap, and on catalog changes
// (a MicroserviceConfiguration edit re-enqueues every Instance).
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&v1.Service{}).
		Owns(&v1.ConfigMap{}).
		Watches(&corev1beta1.MicroserviceConfiguration{},
			handler.EnqueueRequestsFromMapFunc(r.mapCatalogToInstances)).
		Complete(r)
}

// mapCatalogToInstances enqueues every Instance when a catalog entry changes,
// so a new/edited/removed MicroserviceConfiguration propagates to all instances.
func (r *InstanceReconciler) mapCatalogToInstances(ctx context.Context, _ client.Object) []reconcile.Request {
	instances := &corev1beta1.InstanceList{}
	if err := r.List(ctx, instances); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(instances.Items))
	for i := range instances.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&instances.Items[i]),
		})
	}
	return reqs
}

// Ensure the instance namespace exists and is owned by the Instance, so that
// deleting the Instance cascades to the namespace and everything in it.
func (r *InstanceReconciler) assureNamespace(ctx context.Context, instance *corev1beta1.Instance) error {
	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instance.ObjectMeta.Name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		return controllerutil.SetControllerReference(instance, ns, r.Scheme)
	})
	return err
}

// Ensure the instance ConfigMap exists with the resolved instance configuration.
func (r *InstanceReconciler) assureInstanceConfigMap(ctx context.Context, instance *corev1beta1.Instance) error {
	ic := &corev1beta1.InstanceConfiguration{}
	if err := r.Get(ctx, client.ObjectKey{Name: instance.Spec.ConfigurationId}, ic); err != nil {
		return err
	}

	cmap := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      getInstanceConfigMapName(instance.ObjectMeta.Name),
		Namespace: instance.ObjectMeta.Name,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cmap, func() error {
		cmap.Data = map[string]string{INSTANCE_CONFIG_NAME: string(ic.Spec.Configuration.RawMessage)}
		return controllerutil.SetControllerReference(instance, cmap, r.Scheme)
	})
	return err
}

// Reconcile the set of shared microservice deployments for an instance against
// the microservice configuration catalog (cache-backed list).
func (r *InstanceReconciler) reconcileSharedMicroservices(ctx context.Context, instance *corev1beta1.Instance) error {
	catalog := &corev1beta1.MicroserviceConfigurationList{}
	if err := r.List(ctx, catalog); err != nil {
		return err
	}

	for i := range catalog.Items {
		msc := &catalog.Items[i]
		if err := r.assureMicroservice(ctx, instance, msc); err != nil {
			return err
		}
	}
	return nil
}

// Labels applied to a shared microservice deployment.
func microserviceLabels(instance *corev1beta1.Instance, msc *corev1beta1.MicroserviceConfiguration) map[string]string {
	return map[string]string{
		corev1beta1.LABEL_INSTANCE:     instance.ObjectMeta.Name,
		corev1beta1.LABEL_MICROSERVICE: msc.Spec.FunctionalArea,
	}
}

// Create or update the Deployment and Service for a shared microservice so the
// running workload converges to the catalog entry (image, env, ports).
func (r *InstanceReconciler) assureMicroservice(ctx context.Context, instance *corev1beta1.Instance,
	msc *corev1beta1.MicroserviceConfiguration) error {
	log := logf.FromContext(ctx)
	name := msc.Spec.FunctionalArea
	labels := microserviceLabels(instance, msc)

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.ObjectMeta.Name}}
	deployResult, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		// Selector is immutable post-create; the value is deterministic from
		// labels so re-setting it is a no-op on update.
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels
		deploy.Spec.Template.Spec.Containers = []v1.Container{{
			Name:            name,
			Image:           msc.Spec.Image,
			ImagePullPolicy: v1.PullIfNotPresent,
			Env: []v1.EnvVar{
				{Name: ENV_INSTANCE_ID, Value: instance.ObjectMeta.Name},
				{Name: ENV_MICROSERVICE_ID, Value: msc.ObjectMeta.Name},
				{Name: ENV_MS_FUNCTIONAL_AREA, Value: msc.Spec.FunctionalArea},
			},
			Ports: []v1.ContainerPort{{Name: "graphql", ContainerPort: PORT_GRAPHQL, Protocol: v1.ProtocolTCP}},
			VolumeMounts: []v1.VolumeMount{
				{Name: "instance-config", MountPath: "/etc/dci-config"},
			},
		}}
		deploy.Spec.Template.Spec.Volumes = []v1.Volume{{
			Name: "instance-config",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{Name: getInstanceConfigMapName(instance.ObjectMeta.Name)},
				},
			},
		}}
		return controllerutil.SetControllerReference(instance, deploy, r.Scheme)
	})
	if err != nil {
		return err
	}

	svc := &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.ObjectMeta.Name}}
	svcResult, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Ports = []v1.ServicePort{{Name: "graphql", Protocol: v1.ProtocolTCP, Port: PORT_GRAPHQL}}
		return controllerutil.SetControllerReference(instance, svc, r.Scheme)
	})
	if err != nil {
		return err
	}

	if deployResult != controllerutil.OperationResultNone || svcResult != controllerutil.OperationResultNone {
		log.Info(fmt.Sprintf("Reconciled shared microservice '%s' (deploy=%s, svc=%s)", name, deployResult, svcResult))
	}
	return nil
}

// Get name of instance config map
func getInstanceConfigMapName(iname string) string {
	return fmt.Sprintf("%s-%s-%s", "dci", iname, "config")
}
