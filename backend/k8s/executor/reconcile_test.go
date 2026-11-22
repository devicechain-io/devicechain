// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A stale Ready condition is deliberately insufficient without a fresh Pod read.
func TestOperationReconcileNeverTrustsCachedReady(t *testing.T) {
	in := input()
	in.Enrollment.Name = "cluster"
	in.Operation.Name = "request"
	in.Enrollment.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.InstanceOperation{}, &api.ExecutorEnrollment{}).WithObjects(in.Operation, in.Enrollment, in.Instance).Build()
	r := &OperationReconciler{Client: c, Reader: c, Self: SelfIdentity{}}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: in.Operation.Name}})
	if err != nil {
		t.Fatal(err)
	}
	var op api.InstanceOperation
	if err := c.Get(context.Background(), client.ObjectKey{Name: in.Operation.Name}, &op); err != nil {
		t.Fatal(err)
	}
	if op.Status.Phase != "" {
		t.Fatalf("unverified write: %+v", op.Status)
	}
	if result.RequeueAfter <= 0 {
		t.Fatal("pending request never reverified")
	}
}
func TestTerminalOperationNeverRequeues(t *testing.T) {
	in := input()
	in.Operation.Name = "terminal"
	in.Operation.Status.Phase = api.OperationRefused
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.InstanceOperation{}).WithObjects(in.Operation).Build()
	r := &OperationReconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "terminal"}})
	if err != nil || result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("terminal retry: %+v %v", result, err)
	}
}
