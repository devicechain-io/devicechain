// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ExecutorStatus(ctx context.Context, kubeContext string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	dyn, _, _, err := executorClients(kubeContext)
	if err != nil {
		return err
	}
	enrollment, err := dyn.Resource(enrollmentGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil && !executorResourceAbsent(err) {
		return fmt.Errorf("reading executor enrollment: %w", err)
	}
	fmt.Fprintln(out, executorPreview)
	if apierrors.IsNotFound(err) || executorResourceAbsent(err) {
		fmt.Fprintln(out, "Executor is not enrolled.")
	} else {
		view := map[string]interface{}{}
		for _, field := range []string{"spec", "status"} {
			value, found, err := unstructured.NestedFieldCopy(enrollment.Object, field)
			if err != nil {
				return err
			}
			if found {
				view[field] = value
			}
		}
		raw, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(raw))
	}
	operations, err := listExecutorResource(ctx, dyn, operationGVR)
	if err != nil && !executorResourceAbsent(err) {
		return fmt.Errorf("reading non-terminal InstanceOperations: %w", err)
	}
	pending := pendingExecutorOperations(operations, "")
	if len(pending) == 0 {
		fmt.Fprintln(out, "No non-terminal InstanceOperations.")
	}
	for _, operation := range pending {
		fmt.Fprintln(out, executorOperationSummary(operation, time.Now()))
	}
	return nil
}
