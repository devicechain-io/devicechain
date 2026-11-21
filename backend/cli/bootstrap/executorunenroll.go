// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

func prepareExecutorUnenroll(ctx context.Context, dyn dynamic.Interface, force bool) error {
	operations, err := listExecutorResource(ctx, dyn, operationGVR)
	if err != nil {
		if executorResourceAbsent(err) {
			return nil
		}
		return fmt.Errorf("refusing executor unenrollment: cannot read InstanceOperations: %w", err)
	}
	pending := pendingExecutorOperations(operations, "")
	if len(pending) != 0 && !force {
		var descriptions []string
		for _, op := range pending {
			descriptions = append(descriptions, executorOperationSummary(op, time.Now()))
		}
		return fmt.Errorf("refusing executor unenrollment while operations are non-terminal:\n  %s\nWait for a verdict, or use dcctl executor unenroll --force to mark them Refused/Unenrolled first. This is detection, not a lock", strings.Join(descriptions, "\n  "))
	}
	for _, op := range pending {
		if err := refuseOperationOnUnenroll(ctx, dyn, op.GetName()); err != nil {
			return err
		}
	}
	return nil
}

// Forced unenrollment is the one non-executor writer of operation status. An
// administrator closes outstanding requests before removing the writer that
// could decide them. UpdateStatus carries resourceVersion; on a conflict, reread
// and preserve any terminal verdict the executor has already written.
func refuseOperationOnUnenroll(ctx context.Context, dyn dynamic.Interface, name string) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		op, err := dyn.Resource(operationGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !operationNonTerminal(*op) {
			return nil
		}
		status := map[string]interface{}{
			"phase": "Refused", "reason": "Unenrolled",
			"message":            "A provider administrator forcibly unenrolled the executor; submit a new request after enrollment.",
			"observedGeneration": op.GetGeneration(),
			"decidedAt":          time.Now().UTC().Format(time.RFC3339),
		}
		if err := unstructured.SetNestedMap(op.Object, status, "status"); err != nil {
			return err
		}
		_, err = dyn.Resource(operationGVR).UpdateStatus(ctx, op, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("marking InstanceOperation/%s Refused/Unenrolled before removing executor: %w", name, err)
	}
	return nil
}

func UnenrollExecutor(ctx context.Context, kubeContext string, force bool, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	dyn, _, typed, err := executorClients(kubeContext)
	if err != nil {
		return err
	}
	ns, err := readOwnedExecutorNamespace(ctx, dyn)
	if err != nil {
		return err
	}
	_, enrollmentErr := dyn.Resource(enrollmentGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if enrollmentErr != nil && !executorResourceAbsent(enrollmentErr) {
		return fmt.Errorf("refusing executor unenrollment: cannot read ExecutorEnrollment/cluster: %w", enrollmentErr)
	}
	if ns == nil && executorResourceAbsent(enrollmentErr) {
		fmt.Fprintln(out, "Executor not enrolled; no resources were changed.")
		return nil
	}
	if ns != nil && ns.GetUID() == "" {
		return fmt.Errorf("refusing executor unenrollment: Namespace/%s has no UID; no resources were changed", executorNamespace)
	}
	if err := prepareExecutorUnenroll(ctx, dyn, force); err != nil {
		return err
	}
	targets := []struct {
		resource        schema.GroupVersionResource
		namespace, name string
	}{
		{enrollmentGVR, "", "cluster"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, executorNamespace, executorName},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, "", executorName},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, executorNamespace, executorName},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, "", executorName},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, executorNamespace, executorName},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, "", "dc-executor-requester"},
		{schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "", executorNamespace},
	}
	for _, target := range targets {
		options := metav1.DeleteOptions{}
		if target.resource == executorNamespaceGVR {
			if ns == nil {
				continue
			}
			uid := ns.GetUID()
			options.Preconditions = &metav1.Preconditions{UID: &uid}
		}
		var resource dynamic.ResourceInterface = dyn.Resource(target.resource)
		if target.namespace != "" {
			resource = dyn.Resource(target.resource).Namespace(target.namespace)
		}
		err := resource.Delete(ctx, target.name, options)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("executor removal stopped deleting %s/%s/%s: %w; earlier deletions have already completed", target.resource.Resource, target.namespace, target.name, err)
		}
		fmt.Fprintf(out, "removed %s/%s/%s\n", target.resource.Resource, target.namespace, target.name)
	}
	err = wait.PollUntilContextTimeout(ctx, time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := typed.CoreV1().Namespaces().Get(ctx, executorNamespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("waiting for executor namespace %s to disappear: %w", executorNamespace, err)
	}
	fmt.Fprintln(out, "Executor unenrolled; its namespace is gone. InstanceOperation history and resource definitions remain.")
	return nil
}
