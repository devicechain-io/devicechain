// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"
)

var operationGVR = schema.GroupVersionResource{Group: "core.devicechain.io", Version: "v1beta1", Resource: "instanceoperations"}
var enrollmentGVR = schema.GroupVersionResource{Group: "core.devicechain.io", Version: "v1beta1", Resource: "executorenrollments"}

// These seams let upgrade and destroy exercise their real call paths without a cluster.
var refusePendingOperationFor = CheckPendingOperation
var pendingOperationForDestroy = CheckPendingOperation

func executorResourceAbsent(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}

func listExecutorResource(ctx context.Context, dyn dynamic.Interface, resource schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	var objects []unstructured.Unstructured
	options := metav1.ListOptions{}
	for {
		page, err := dyn.Resource(resource).List(ctx, options)
		if err != nil {
			return nil, err
		}
		objects = append(objects, page.Items...)
		if page.GetContinue() == "" {
			return objects, nil
		}
		options.Continue = page.GetContinue()
	}
}

func operationNonTerminal(op unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(op.Object, "status", "phase")
	switch phase {
	case "Refused", "Succeeded", "Failed":
		return false
	default:
		// Includes empty, Pending and Running; an unknown future phase is not a
		// terminal verdict this build can safely recognise.
		return true
	}
}

func pendingExecutorOperations(ops []unstructured.Unstructured, instance string) []unstructured.Unstructured {
	var pending []unstructured.Unstructured
	for _, op := range ops {
		target, _, _ := unstructured.NestedString(op.Object, "spec", "instance")
		if (instance == "" || target == instance) && operationNonTerminal(op) {
			pending = append(pending, op)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].GetName() < pending[j].GetName() })
	return pending
}

func executorOperationSummary(op unstructured.Unstructured, now time.Time) string {
	instance, _, _ := unstructured.NestedString(op.Object, "spec", "instance")
	action, _, _ := unstructured.NestedString(op.Object, "spec", "action")
	phase, _, _ := unstructured.NestedString(op.Object, "status", "phase")
	if phase == "" {
		phase = "Pending (no verdict)"
	}
	age := "unknown"
	if created := op.GetCreationTimestamp(); !created.IsZero() {
		age = duration.HumanDuration(now.Sub(created.Time))
	}
	return fmt.Sprintf("%s: instance %s, action %s, phase %s, age %s", op.GetName(), instance, action, phase, age)
}

// judgePendingOperations is deliberately independent of Kubernetes reads.
func judgePendingOperations(ops []unstructured.Unstructured, readErr error, instance, verb string, now time.Time) error {
	if readErr != nil {
		if executorResourceAbsent(readErr) {
			return nil
		}
		return fmt.Errorf("refusing %s for instance %q: cannot read InstanceOperations: %w. This is detection, not a lock; restore read access and retry", verb, instance, readErr)
	}
	pending := pendingExecutorOperations(ops, instance)
	if len(pending) == 0 {
		return nil
	}
	lines := make([]string, 0, len(pending))
	for _, op := range pending {
		lines = append(lines, executorOperationSummary(op, now)+"; to remove this request: kubectl delete instanceoperation "+op.GetName())
	}
	return fmt.Errorf("refusing %s for instance %q while InstanceOperations are non-terminal:\n  %s\nWait for the executor's verdict, or run dcctl executor unenroll --force. This is detection, not a lock", verb, instance, strings.Join(lines, "\n  "))
}

// RefuseForPendingOperation supplies AB6's released foundation: absent definitions
// mean no requests; an unreadable ledger never means an empty ledger.
func RefuseForPendingOperation(ctx context.Context, dyn dynamic.Interface, instance, verb string) error {
	ops, err := listExecutorResource(ctx, dyn, operationGVR)
	return judgePendingOperations(ops, err, instance, verb, time.Now())
}

func CheckPendingOperation(ctx context.Context, kubeContext, instance, verb string) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return judgePendingOperations(nil, err, instance, verb, time.Now())
	}
	return RefuseForPendingOperation(ctx, dyn, instance, verb)
}
