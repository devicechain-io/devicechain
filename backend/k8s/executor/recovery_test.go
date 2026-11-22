// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInstallDecidesWithoutInstance(t *testing.T) {
	for _, target := range []*api.Instance{nil, {ObjectMeta: metav1.ObjectMeta{UID: "unrelated"}}} {
		in := input()
		in.Operation.Spec.Action = api.ActionInstall
		in.Operation.Spec.Instance, in.Operation.Spec.InstanceUID = "", ""
		in.Instance = target
		if got := Decide(in); !got.Write || got.Reason != "NotExecutableInThisBuild" {
			t.Errorf("cluster Install: %+v", got)
		}
	}
}

type noInstanceReads struct {
	client.Client
	reads int
}

func (c *noInstanceReads) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*api.Instance); ok {
		c.reads++
		return fmtError("Instance lookup must not affect Install")
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestOperationTerminalConditionAndInstallLookup(t *testing.T) {
	for _, action := range []api.OperationAction{api.ActionInstall, api.ActionUpgrade, api.ActionBootstrap} {
		t.Run(string(action), func(t *testing.T) {
			in := input()
			in.Enrollment.Name, in.Operation.Name = "cluster", "request"
			in.Enrollment.Spec.ExecutorImage = "registry/operator@sha256:pin"
			in.Operation.Spec.Action = action
			in.Operation.Generation = 3
			if action == api.ActionInstall {
				in.Operation.Spec.Instance, in.Operation.Spec.InstanceUID = "", ""
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "self", Namespace: "executor"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: in.Enrollment.Spec.ExecutorImage}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", ImageID: "runtime"}}}}
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			objects := []client.Object{in.Operation, in.Enrollment, pod}
			if action == api.ActionUpgrade {
				objects = append(objects, in.Instance)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.InstanceOperation{}).WithObjects(objects...).Build()
			var used client.Client = c
			guard := &noInstanceReads{Client: c}
			if action == api.ActionInstall {
				used = guard
			}
			r := &OperationReconciler{Client: used, Reader: c, Self: SelfIdentity{"self", "executor"}}
			if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "request"}}); err != nil {
				t.Fatal(err)
			}
			if guard.reads != 0 {
				t.Errorf("Install read Instance %d times", guard.reads)
			}
			var got api.InstanceOperation
			if err := c.Get(t.Context(), client.ObjectKey{Name: "request"}, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Reason != "NotExecutableInThisBuild" {
				t.Fatalf("verdict: %+v", got.Status)
			}
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&got)
			if err != nil {
				t.Fatal(err)
			}
			conditions, _, _ := unstructured.NestedSlice(raw, "status", "conditions")
			if len(conditions) != 1 {
				t.Fatalf("wanted one Decided condition: %+v", raw["status"])
			}
			condition := conditions[0].(map[string]interface{})
			if condition["type"] != "Decided" || condition["status"] != "True" || condition["reason"] != got.Status.Reason || condition["observedGeneration"] != got.Generation || condition["lastTransitionTime"] == "" {
				t.Errorf("condition: %+v", condition)
			}
		})
	}
}
