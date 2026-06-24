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

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

const (
	INSTANCE_CONFIG_NAME = "instance"
)

// InstanceReconciler reconciles a Instance object
type InstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.devicechain.io,resources=instances/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Instance object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &corev1beta1.Instance{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Handling deleted tenant: %+v", req.NamespacedName))
			err := r.handleInstanceDeleted(ctx, req)
			if err != nil {
				log.Error(err, "Unable to handle tenant delete")
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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1beta1.Instance{}).
		Complete(r)
}

// Handle case where instance has been deleted.
func (r *InstanceReconciler) handleInstanceDeleted(ctx context.Context, req ctrl.Request) error {
	return deleteInstanceConfigMap(ctx, req)
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

	// Attempt to create the namespace.
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
	err = corev1beta1.V1Client.Delete(ctx, cmap)
	if err != nil {
		return err
	}
	return nil
}
