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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
//+kubebuilder:rbac:groups=core.devicechain.io,resources=microserviceconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services;configmaps;namespaces,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives an Instance toward its desired state: a dedicated namespace,
// an instance ConfigMap, and one shared Deployment + Service per microservice
// in the configuration catalog. Each microservice runs as a single deployment
// serving every tenant in the instance.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &corev1beta1.Instance{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Handling deleted instance: %+v", req.NamespacedName))
			if err := r.handleInstanceDeleted(ctx, req); err != nil {
				log.Error(err, "Unable to handle instance delete")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Locate namespace same as instance id and create if not existing
	instanceid := instance.ObjectMeta.Name
	_, err = getNamespace(instanceid)
	if err != nil {
		log.Info(fmt.Sprintf("Instance namespace not found. Creating namespace '%s'", instanceid))
		_, err = createNamespace(instanceid)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Locate instance config map and create if not existing
	_, err = getInstanceConfigMap(instance)
	if err != nil {
		cmap, err := createInstanceConfigMap(instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Created instance config map '%s'", cmap.ObjectMeta.Name))
	}

	// Deploy one shared microservice (Deployment + Service) per catalog entry.
	if err := r.reconcileSharedMicroservices(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		Complete(r)
}

// Handle case where instance has been deleted. The namespace deletion cascades
// to the Deployments, Services, and ConfigMaps owned by the instance, so only
// the cluster-scoped instance ConfigMap reference needs explicit cleanup.
func (r *InstanceReconciler) handleInstanceDeleted(ctx context.Context, req ctrl.Request) error {
	return deleteInstanceConfigMap(ctx, req)
}

// Reconcile the set of shared microservice deployments for an instance against
// the microservice configuration catalog.
func (r *InstanceReconciler) reconcileSharedMicroservices(ctx context.Context, instance *corev1beta1.Instance) error {
	log := logf.FromContext(ctx)

	catalog := &corev1beta1.MicroserviceConfigurationList{}
	if err := r.List(ctx, catalog); err != nil {
		return err
	}
	log.Info(fmt.Sprintf("Reconciling %d shared microservices for instance '%s'", len(catalog.Items), instance.ObjectMeta.Name))

	for i := range catalog.Items {
		msc := &catalog.Items[i]
		if err := r.assureMicroserviceDeployment(ctx, instance, msc); err != nil {
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

// Create the Deployment and Service for a shared microservice if not present.
func (r *InstanceReconciler) assureMicroserviceDeployment(ctx context.Context, instance *corev1beta1.Instance,
	msc *corev1beta1.MicroserviceConfiguration) error {
	log := logf.FromContext(ctx)

	name := msc.Spec.FunctionalArea
	key := types.NamespacedName{Namespace: instance.ObjectMeta.Name, Name: name}

	err := r.Get(ctx, key, &appsv1.Deployment{})
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return err
	}

	if err := r.createDeploymentAndService(ctx, instance, msc); err != nil {
		return err
	}
	log.Info(fmt.Sprintf("Created shared microservice '%s' for instance '%s'", name, instance.ObjectMeta.Name))
	return nil
}

// Create a Deployment and Service for a shared microservice.
func (r *InstanceReconciler) createDeploymentAndService(ctx context.Context, instance *corev1beta1.Instance,
	msc *corev1beta1.MicroserviceConfiguration) error {
	name := msc.Spec.FunctionalArea
	labels := microserviceLabels(instance, msc)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.ObjectMeta.Name,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            name,
							Image:           msc.Spec.Image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Env: []v1.EnvVar{
								{Name: ENV_INSTANCE_ID, Value: instance.ObjectMeta.Name},
								{Name: ENV_MICROSERVICE_ID, Value: msc.ObjectMeta.Name},
								{Name: ENV_MS_FUNCTIONAL_AREA, Value: msc.Spec.FunctionalArea},
							},
							Ports: []v1.ContainerPort{
								{Name: "graphql", ContainerPort: PORT_GRAPHQL, Protocol: v1.ProtocolTCP},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "instance-config", MountPath: "/etc/dci-config"},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "instance-config",
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: getInstanceConfigMapName(instance.ObjectMeta.Name),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if err := r.Create(ctx, deploy); err != nil {
		return err
	}

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.ObjectMeta.Name,
			Labels:    labels,
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{Name: "graphql", Protocol: v1.ProtocolTCP, Port: PORT_GRAPHQL},
			},
			Selector: labels,
		},
	}
	return r.Create(ctx, service)
}

// Create a new namespace
func createNamespace(nsid string) (*v1.Namespace, error) {
	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsid}}

	// Attempt to create the namespace.
	err := corev1beta1.V1Client.Create(context.Background(), ns)
	if err != nil {
		return nil, err
	}
	return ns, nil
}

// Get namespace by id
func getNamespace(nsid string) (*v1.Namespace, error) {
	ns := &v1.Namespace{}
	err := corev1beta1.V1Client.Get(context.Background(), client.ObjectKey{
		Name: nsid,
	}, ns)
	if err != nil {
		return nil, err
	}
	return ns, nil
}

// Get name of instance config map
func getInstanceConfigMapName(iname string) string {
	return fmt.Sprintf("%s-%s-%s", "dci", iname, "config")
}

// Create a new instance config map
func createInstanceConfigMap(dci *corev1beta1.Instance) (*v1.ConfigMap, error) {
	ic, err := corev1beta1.GetInstanceConfiguration(dci.Spec.ConfigurationId)
	if err != nil {
		return nil, err
	}

	cmap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getInstanceConfigMapName(dci.ObjectMeta.Name),
			Namespace: dci.ObjectMeta.Name,
		},
		Data: map[string]string{
			INSTANCE_CONFIG_NAME: string(ic.Spec.Configuration.RawMessage),
		},
	}

	// Attempt to create the config map.
	err = corev1beta1.V1Client.Create(context.Background(), cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

// Get config map associated with instance.
func getInstanceConfigMap(dci *corev1beta1.Instance) (*v1.ConfigMap, error) {
	cmap := &v1.ConfigMap{}
	err := corev1beta1.V1Client.Get(context.Background(), client.ObjectKey{
		Name:      getInstanceConfigMapName(dci.ObjectMeta.Name),
		Namespace: dci.ObjectMeta.Name,
	}, cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

// Delete config map associated with instance.
func deleteInstanceConfigMap(ctx context.Context, req ctrl.Request) error {
	cmap := &v1.ConfigMap{}
	err := corev1beta1.V1Client.Get(ctx, client.ObjectKey{
		Name:      getInstanceConfigMapName(req.Name),
		Namespace: req.Name,
	}, cmap)
	if err != nil {
		return err
	}
	return corev1beta1.V1Client.Delete(ctx, cmap)
}
