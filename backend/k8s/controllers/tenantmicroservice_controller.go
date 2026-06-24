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
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/devicechain-io/dc-k8s/api/v1beta1"
)

const (
	ENV_INSTANCE_ID        = "DC_INSTANCE_ID"
	ENV_TENANT_ID          = "DC_TENANT_ID"
	ENV_TENANT_NAME        = "DC_TENANT_NAME"
	ENV_MICROSERVICE_ID    = "DC_MICROSERVICE_ID"
	ENV_MICROSERVICE_NAME  = "DC_MICROSERVICE_NAME"
	ENV_MS_FUNCTIONAL_AREA = "DC_MS_FUNCTIONAL_AREA"
)

// TenantMicroserviceReconciler reconciles a TenantMicroservice object
type TenantMicroserviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenantmicroservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenantmicroservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=tenantmicroservices/finalizers,verbs=update
func (r *TenantMicroserviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tms := &v1beta1.TenantMicroservice{}
	err := r.Get(ctx, req.NamespacedName, tms)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Handling deleted tenant microservice: %+v", req.NamespacedName))
			if err := r.handleTenantMicroserviceDeleted(ctx, req); err != nil {
				log.Error(err, "Unable to handle tenant microservice delete")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle creating or updating a k8s Deployment for the tenant microservice
	log.Info(fmt.Sprintf("Handling added/updated tenant microservice: %+v", req.NamespacedName))
	err = r.createOrUpdateDeployment(ctx, tms)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create or update instance ingress based on changes.
	err = r.updateInstanceIngress(ctx, tms)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update tenant config map entry with configuration
	err = r.updateTenantConfigMap(ctx, tms)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantMicroserviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.TenantMicroservice{}).
		Complete(r)
}

// Get namespaced name for deployment
func getDeploymentName(tms *v1beta1.TenantMicroservice) types.NamespacedName {
	return types.NamespacedName{Namespace: tms.ObjectMeta.Namespace, Name: tms.ObjectMeta.Name}
}

// Create or update a k8s Deployment for the tenant microservice
func (r *TenantMicroserviceReconciler) createOrUpdateDeployment(ctx context.Context, tms *v1beta1.TenantMicroservice) error {
	log := logf.FromContext(ctx)

	// Attempt to look up existing deployment.
	dname := getDeploymentName(tms)
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, dname, deploy); err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Existing deployment not found for tenant microservice: %+v", dname))

			// Look up associated microservice.
			dct, err := v1beta1.GetTenant(v1beta1.TenantGetRequest{
				InstanceId: tms.ObjectMeta.Namespace,
				TenantId:   tms.Spec.TenantId,
			})
			if err != nil {
				return err
			}

			// Look up associated microservice.
			ms, err := v1beta1.GetMicroservice(v1beta1.MicroserviceGetRequest{
				InstanceId:     tms.ObjectMeta.Namespace,
				MicroserviceId: tms.Spec.MicroserviceId,
			})
			if err != nil {
				return err
			}

			// Create a new deployment.
			_, err = r.createDeploymentAndService(ctx, tms, dct, ms)
			return err
		} else {
			return err
		}
	}

	log.Info(fmt.Sprintf("Existing deployment found for tenant microservice: %+v", dname))

	return nil
}

// Create labels to target deployment
func createDeploymentLabels(tms *v1beta1.TenantMicroservice) map[string]string {
	return map[string]string{
		v1beta1.LABEL_TENANT:       tms.Spec.TenantId,
		v1beta1.LABEL_MICROSERVICE: tms.Spec.MicroserviceId,
	}
}

// Create a deployment based on tenant microservice details
func (r *TenantMicroserviceReconciler) createDeploymentAndService(ctx context.Context, tms *v1beta1.TenantMicroservice,
	dct *v1beta1.Tenant, ms *v1beta1.Microservice) (*appsv1.Deployment, error) {
	log := logf.FromContext(ctx)

	dci, err := v1beta1.GetInstance(v1beta1.InstanceGetRequest{Id: tms.ObjectMeta.Namespace})
	if err != nil {
		return nil, err
	}

	dname := getDeploymentName(tms)
	labels := createDeploymentLabels(tms)

	// Create deployment.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dname.Name,
			Namespace: dname.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            tms.Spec.MicroserviceId,
							Image:           ms.Spec.Image,
							ImagePullPolicy: ms.Spec.ImagePullPolicy,
							Env: []corev1.EnvVar{
								{
									Name:  ENV_INSTANCE_ID,
									Value: dct.ObjectMeta.Namespace,
								},
								{
									Name:  ENV_TENANT_ID,
									Value: dct.ObjectMeta.Name,
								},
								{
									Name:  ENV_TENANT_NAME,
									Value: dct.Spec.Name,
								},
								{
									Name:  ENV_MICROSERVICE_ID,
									Value: ms.ObjectMeta.Name,
								},
								{
									Name:  ENV_MICROSERVICE_NAME,
									Value: ms.Spec.Name,
								},
								{
									Name:  ENV_MS_FUNCTIONAL_AREA,
									Value: ms.Spec.FunctionalArea,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "instance-config",
									MountPath: "/etc/dci-config",
								}, {
									Name:      "tenant-config",
									MountPath: "/etc/dct-config",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "instance-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: getInstanceConfigMapName(dci.ObjectMeta.Name),
									},
								},
							},
						},
						{
							Name: "tenant-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: getTenantConfigMapName(tms.Spec.TenantId),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	err = r.Create(context.Background(), deploy)
	if err != nil {
		return nil, err
	}

	// Create service for accessing pods.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dname.Name,
			Namespace: dname.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     "graphql",
					Protocol: corev1.ProtocolTCP,
					Port:     8080,
				},
			},
			Selector: labels,
		},
	}
	err = r.Create(context.Background(), service)
	if err != nil {
		return nil, err
	}

	log.Info(fmt.Sprintf("Created k8s Deployment for tenant microservice: %+v", dname))
	return deploy, nil
}

// Handle a deleted tenant microservice
func (r *TenantMicroserviceReconciler) handleTenantMicroserviceDeleted(ctx context.Context, req ctrl.Request) error {
	log := logf.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deploy); err != nil {
		if !errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Unable to find deployment for tenant microservice: %+v", req.NamespacedName))
			return err
		}
	} else {
		if err := r.Delete(ctx, deploy); err != nil {
			log.Info(fmt.Sprintf("Unable to delete deployment for tenant microservice: %+v", req.NamespacedName))
			return err
		}
		log.Info(fmt.Sprintf("Deleted deployment for tenant microservice: %+v", req.NamespacedName))
	}

	service := &corev1.Service{}
	if err := r.Get(ctx, req.NamespacedName, service); err != nil {
		if !errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Unable to find service for tenant microservice: %+v", req.NamespacedName))
			return err
		}
	} else {
		if err := r.Delete(ctx, service); err != nil {
			log.Info(fmt.Sprintf("Unable to delete service for tenant microservice: %+v", req.NamespacedName))
			return err
		}
		log.Info(fmt.Sprintf("Deleted service for tenant microservice: %+v", req.NamespacedName))
	}
	return nil
}

// Update tenant configuration map with entry for tenant microservice
func (r *TenantMicroserviceReconciler) updateTenantConfigMap(ctx context.Context,
	tms *v1beta1.TenantMicroservice) error {

	// Get microservice information.
	ms, err := v1beta1.GetMicroservice(v1beta1.MicroserviceGetRequest{
		InstanceId:     tms.ObjectMeta.Namespace,
		MicroserviceId: tms.Spec.MicroserviceId,
	})
	if err != nil {
		return err
	}

	tcmap, err := getTenantConfigMap(tms.Spec.TenantId, tms.ObjectMeta.Namespace)
	if err != nil {
		return err
	}

	// Make sure map data is populated.
	if tcmap.Data == nil {
		tcmap.Data = make(map[string]string, 0)
	}

	// Update map index for functional area
	tcmap.Data[ms.Spec.FunctionalArea] = string(tms.Spec.Configuration.RawMessage)
	return r.Update(ctx, tcmap)
}

// Generate name used for instance ingress.
func generateIngressName(ns string, tenantid string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: ns,
		Name:      fmt.Sprintf("%s-%s-%s", ns, tenantid, "ingress"),
	}
}

// Generate the ingress path for a given tenant microservice.
func generateIngressPath(tms *v1beta1.TenantMicroservice) (*netv1.HTTPIngressPath, error) {
	ms, err := v1beta1.GetMicroservice(v1beta1.MicroserviceGetRequest{
		InstanceId:     tms.ObjectMeta.Namespace,
		MicroserviceId: tms.Spec.MicroserviceId,
	})
	if err != nil {
		return nil, err
	}

	pathtype := netv1.PathTypePrefix
	igpath := &netv1.HTTPIngressPath{
		Path:     fmt.Sprintf("/%s/%s/%s(/|$)(.*)", tms.ObjectMeta.Namespace, tms.Spec.TenantId, ms.Spec.FunctionalArea),
		PathType: &pathtype,
		Backend: netv1.IngressBackend{
			Service: &netv1.IngressServiceBackend{
				Name: tms.ObjectMeta.Name,
				Port: netv1.ServiceBackendPort{
					Number: 8080,
				},
			},
		},
	}
	return igpath, nil
}

// Generate ingress paths for all tenant microservices in instance namespace.
func (r *TenantMicroserviceReconciler) generateIngressPaths(tms *v1beta1.TenantMicroservice) ([]netv1.HTTPIngressPath, error) {
	tmslist := &v1beta1.TenantMicroserviceList{}
	err := r.List(context.Background(), tmslist, client.InNamespace(tms.ObjectMeta.Namespace),
		client.MatchingLabels{v1beta1.LABEL_TENANT: tms.Spec.TenantId})
	if err != nil {
		return nil, err
	}

	ipaths := make([]netv1.HTTPIngressPath, 0)
	for _, tms := range tmslist.Items {
		ipath, err := generateIngressPath(&tms)
		if err != nil {
			return nil, err
		}
		ipaths = append(ipaths, *ipath)
	}
	return ipaths, nil
}

// Generate ingress resource.
func (r *TenantMicroserviceReconciler) generateIngress(tms *v1beta1.TenantMicroservice,
	igname types.NamespacedName) (*netv1.Ingress, error) {
	ipaths, err := r.generateIngressPaths(tms)
	if err != nil {
		return nil, err
	}

	return &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      igname.Name,
			Namespace: igname.Namespace,
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":                "nginx",
				"nginx.ingress.kubernetes.io/rewrite-target": "/$2",
			},
		},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{
				{
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: ipaths,
						},
					},
				},
			},
		},
	}, nil
}

// Update ingress configuration based on tenant microservice changes.
func (r *TenantMicroserviceReconciler) updateInstanceIngress(ctx context.Context, tms *v1beta1.TenantMicroservice) error {
	igname := generateIngressName(tms.ObjectMeta.Namespace, tms.Spec.TenantId)
	updated, err := r.generateIngress(tms, igname)
	if err != nil {
		return err
	}

	ingress := &netv1.Ingress{}
	if err = r.Get(ctx, igname, ingress); err != nil {
		if errors.IsNotFound(err) {
			if err = r.Create(context.Background(), updated); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if err = r.Update(context.Background(), updated); err != nil {
			return err
		}
	}

	return nil
}
