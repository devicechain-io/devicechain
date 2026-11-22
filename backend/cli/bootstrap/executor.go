// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	dcapply "github.com/devicechain-io/dc-k8s/apply"
	operatorconfig "github.com/devicechain-io/dc-k8s/config"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const executorNamespace = "dc-executor-system"
const executorName = "dc-executor"
const executorManagedByLabel = "app.kubernetes.io/managed-by"
const executorFieldManager = "dcctl-executor"
const executorPreview = "Preview: the executor is installed and verified but executes no operations in this release"

type ExecutorEnrollOptions struct {
	Owner, Image, KubeContext, EscrowDir, InstallCommand string
	DryRun                                               bool
}

// RenderExecutor owns the single image and owner validators. Validate before
// loading a kubeconfig, including for dry runs.
func ValidateExecutorEnrollment(owner, image string) error {
	_, err := operatorconfig.RenderExecutor(image, operatorconfig.EnrollmentInput{Owner: owner, ProtocolVersion: 1})
	return err
}

var executorRequireOperator = RequireOperator
var executorClients = func(kubeContext string) (dynamic.Interface, discovery.DiscoveryInterface, kubernetes.Interface, error) {
	return kubeClients(kubeContext)
}

func EnrollExecutor(ctx context.Context, opts ExecutorEnrollOptions, out io.Writer) error {
	if err := ValidateExecutorEnrollment(opts.Owner, opts.Image); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	installCommand := opts.InstallCommand
	if installCommand == "" {
		installCommand = "dcctl install <provider> --kube-context " + opts.KubeContext
	}
	if err := executorRequireOperator(ctx, opts.KubeContext, "an executor enrollment", installCommand); err != nil {
		return err
	}
	dyn, disco, typed, err := executorClients(opts.KubeContext)
	if err != nil {
		return err
	}
	if _, err := readOwnedExecutorNamespace(ctx, dyn); err != nil {
		return err
	}
	if err := requireExecutorEscrows(ctx, dyn, opts.KubeContext, opts.EscrowDir); err != nil {
		return err
	}
	manifest, err := operatorconfig.RenderExecutor(opts.Image, operatorconfig.EnrollmentInput{Owner: opts.Owner, ProtocolVersion: 1})
	if err != nil {
		return err
	}
	objects, err := dcapply.Decode(manifest)
	if err != nil {
		return err
	}
	if err := requireCompatibleExecutorEnrollment(ctx, dyn, objects); err != nil {
		return err
	}
	mapper, err := dcapply.NewApplyOptions(dyn, disco).ToRESTMapper()
	if err != nil {
		return fmt.Errorf("discovering executor resources: %w", err)
	}
	if err := preflightExecutorAccess(ctx, typed, objects, mapper); err != nil {
		return err
	}
	fmt.Fprintln(out, executorPreview)
	fmt.Fprintln(out, "Privilege pre-check passed; this is a pre-check, not a guarantee.")
	if opts.DryRun {
		for _, object := range objects {
			fmt.Fprintf(out, "[dry-run] would apply %s\n", executorObjectName(object))
		}
		return nil
	}
	if err := applyExecutorObjects(ctx, dyn, mapper, objects, out); err != nil {
		return err
	}
	if err := waitForExecutor(ctx, typed, dyn, opts.Image, 5*time.Minute); err != nil {
		return err
	}
	fmt.Fprintln(out, "Executor enrollment is Ready; capability executes no operations.")
	return nil
}

var executorNamespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// Both enrollment and removal check ownership before any cluster mutation.
func readOwnedExecutorNamespace(ctx context.Context, dyn dynamic.Interface) (*unstructured.Unstructured, error) {
	ns, err := dyn.Resource(executorNamespaceGVR).Get(ctx, executorNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refusing executor change: cannot read Namespace/%s: %w", executorNamespace, err)
	}
	if ns.GetLabels()[executorManagedByLabel] != executorFieldManager {
		return nil, fmt.Errorf("refusing executor change: Namespace/%s is not owned by dcctl (requires %s=%s); no resources were changed", executorNamespace, executorManagedByLabel, executorFieldManager)
	}
	return ns, nil
}

func requireCompatibleExecutorEnrollment(ctx context.Context, dyn dynamic.Interface, objects []unstructured.Unstructured) error {
	existing, err := dyn.Resource(enrollmentGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("refusing executor enrollment: cannot read ExecutorEnrollment/cluster: %w", err)
	}
	for _, desired := range objects {
		if desired.GetKind() != "ExecutorEnrollment" {
			continue
		}
		var differences []string
		for _, field := range []string{"owner", "escrowDelivery"} {
			old, _, oldErr := unstructured.NestedString(existing.Object, "spec", field)
			want, _, wantErr := unstructured.NestedString(desired.Object, "spec", field)
			if oldErr != nil || wantErr != nil || old != want {
				differences = append(differences, fmt.Sprintf("spec.%s existing=%q requested=%q", field, old, want))
			}
		}
		if len(differences) != 0 {
			return fmt.Errorf("refusing immutable ExecutorEnrollment/cluster change: %s; unenroll first with dcctl executor unenroll, then enroll again; no resources were changed", strings.Join(differences, "; "))
		}
		return nil
	}
	return fmt.Errorf("executor plan has no ExecutorEnrollment; refusing enrollment")
}

func executorObjectName(obj unstructured.Unstructured) string {
	if obj.GetNamespace() != "" {
		return obj.GetKind() + "/" + obj.GetNamespace() + "/" + obj.GetName()
	}
	return obj.GetKind() + "/" + obj.GetName()
}

func applyExecutorObjects(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, objects []unstructured.Unstructured, out io.Writer) error {
	// Check the entire plan before its first write.
	for _, object := range objects {
		if object.GetKind() == "Secret" {
			return fmt.Errorf("executor overlay unexpectedly contains a Secret; refusing enrollment")
		}
	}
	force := false
	options := metav1.PatchOptions{FieldManager: "dcctl-executor", Force: &force}
	for i, object := range objects {
		if _, err := dcapply.ApplyUnstructuredWithOptions(ctx, dyn, mapper, object, true, options); err != nil {
			fmt.Fprintf(out, "failed %s: %v\n", executorObjectName(object), err)
			for _, remaining := range objects[i+1:] {
				fmt.Fprintf(out, "not applied %s\n", executorObjectName(remaining))
			}
			return fmt.Errorf("executor apply stopped at %s: %w; review the object-by-object results above. The privilege pre-check is not a guarantee", executorObjectName(object), err)
		}
		fmt.Fprintf(out, "applied %s\n", executorObjectName(object))
	}
	return nil
}

func judgeExecutorReady(deployment *appsv1.Deployment, enrollment *unstructured.Unstructured, image string) (bool, string) {
	var reasons []string
	if deployment == nil {
		reasons = append(reasons, "Deployment NotObserved")
	} else {
		pinned := false
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "manager" && container.Image == image {
				pinned = true
			}
		}
		wanted := int32(1)
		if deployment.Spec.Replicas != nil {
			wanted = *deployment.Spec.Replicas
		}
		if !pinned || wanted < 1 || deployment.Status.ObservedGeneration != deployment.Generation ||
			deployment.Status.UpdatedReplicas != wanted || deployment.Status.Replicas != wanted ||
			deployment.Status.ReadyReplicas != wanted || deployment.Status.AvailableReplicas != wanted {
			reasons = append(reasons, "Deployment RolloutInProgress")
			for _, condition := range deployment.Status.Conditions {
				if condition.Reason != "" {
					reasons = append(reasons, string(condition.Type)+"/"+condition.Reason+": "+condition.Message)
				}
			}
		}
	}
	if enrollment == nil {
		reasons = append(reasons, "ExecutorEnrollment NotObserved")
	} else {
		observed, _, _ := unstructured.NestedInt64(enrollment.Object, "status", "observedGeneration")
		observedImage, _, _ := unstructured.NestedString(enrollment.Object, "status", "observedImage")
		conditions, _, _ := unstructured.NestedSlice(enrollment.Object, "status", "conditions")
		ready := false
		for _, raw := range conditions {
			condition, ok := raw.(map[string]interface{})
			if !ok || condition["type"] != "Ready" {
				continue
			}
			ready = condition["status"] == "True"
			if !ready {
				reason, _ := condition["reason"].(string)
				message, _ := condition["message"].(string)
				reasons = append(reasons, "ExecutorEnrollment Ready/"+reason+": "+message)
			}
		}
		if !ready {
			reasons = append(reasons, "ExecutorEnrollment Ready is not True")
		}
		if observed != enrollment.GetGeneration() {
			reasons = append(reasons, "ExecutorEnrollment observation is stale")
		}
		if observedImage != image {
			reasons = append(reasons, "ExecutorEnrollment observedImage differs from the pinned image")
		}
	}
	return len(reasons) == 0, strings.Join(reasons, "; ")
}

func waitForExecutor(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, image string, timeout time.Duration) error {
	last := "NotObserved: executor has not been read"
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deployment, err := typed.AppsV1().Deployments(executorNamespace).Get(ctx, executorName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("reading executor Deployment: %w", err)
		}
		if err != nil {
			deployment = nil
		}
		enrollment, err := dyn.Resource(enrollmentGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("reading ExecutorEnrollment/cluster: %w", err)
		}
		if err != nil {
			enrollment = nil
		}
		ready, reason := judgeExecutorReady(deployment, enrollment, image)
		last = reason
		return ready, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for executor Deployment rollout and enrollment Ready: %w; last observation: %s", err, last)
	}
	return nil
}
