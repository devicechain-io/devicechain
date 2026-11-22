// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dcapply "github.com/devicechain-io/dc-k8s/apply"
	operatorconfig "github.com/devicechain-io/dc-k8s/config"
	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func executorTestImage() string {
	return "ghcr.io/devicechain-io/operator@sha256:" + strings.Repeat("a", 64)
}

func executorTestPlan(t *testing.T) ([]unstructured.Unstructured, *meta.DefaultRESTMapper, []*metav1.APIResourceList) {
	t.Helper()
	manifest, err := operatorconfig.RenderExecutor(executorTestImage(), operatorconfig.EnrollmentInput{Owner: "ops", ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := dcapply.Decode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
	resources := map[string]string{"Namespace": "namespaces", "ServiceAccount": "serviceaccounts", "Deployment": "deployments", "ClusterRole": "clusterroles", "ClusterRoleBinding": "clusterrolebindings", "Role": "roles", "RoleBinding": "rolebindings", "ExecutorEnrollment": "executorenrollments"}
	seen := map[string]bool{}
	lists := map[string]*metav1.APIResourceList{}
	var discoveryResources []*metav1.APIResourceList
	for _, obj := range objects {
		gvk := obj.GroupVersionKind()
		if seen[gvk.String()] {
			continue
		}
		seen[gvk.String()] = true
		resource := resources[obj.GetKind()]
		if resource == "" {
			t.Fatalf("unexpected rendered kind %s", obj.GetKind())
		}
		scope := meta.RESTScopeRoot
		if obj.GetNamespace() != "" {
			scope = meta.RESTScopeNamespace
		}
		gvr := gvk.GroupVersion().WithResource(resource)
		mapper.AddSpecific(gvk, gvr, gvr, scope)
		gv := gvk.GroupVersion().String()
		if lists[gv] == nil {
			lists[gv] = &metav1.APIResourceList{GroupVersion: gv}
			discoveryResources = append(discoveryResources, lists[gv])
		}
		lists[gv].APIResources = append(lists[gv].APIResources, metav1.APIResource{Name: resource, Kind: gvk.Kind, Namespaced: obj.GetNamespace() != ""})
	}
	return objects, mapper, discoveryResources
}

func executorReadyDeployment() *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: executorName, Namespace: executorNamespace, Generation: 3},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: executorTestImage()}}}}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
}

func executorReadyEnrollment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.devicechain.io/v1beta1", "kind": "ExecutorEnrollment",
		"metadata": map[string]interface{}{"name": "cluster", "generation": int64(2)},
		"status": map[string]interface{}{
			"observedGeneration": int64(2), "observedImage": executorTestImage(),
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "Enrolled"}},
		},
	}}
}

func TestExecutorReadinessRequiresCurrentImageAndRollout(t *testing.T) {
	for _, name := range []string{"ready", "stale generation", "wrong image", "old pods", "wrong deployment pin", "condition refusal"} {
		t.Run(name, func(t *testing.T) {
			d, e := executorReadyDeployment(), executorReadyEnrollment()
			switch name {
			case "stale generation":
				_ = unstructured.SetNestedField(e.Object, int64(1), "status", "observedGeneration")
			case "wrong image":
				_ = unstructured.SetNestedField(e.Object, "other", "status", "observedImage")
			case "old pods":
				d.Status.Replicas = 2
			case "wrong deployment pin":
				d.Spec.Template.Spec.Containers[0].Image = "operator:latest"
			case "condition refusal":
				_ = unstructured.SetNestedSlice(e.Object, []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "ImageMismatch", "message": "wrong pod pin"}}, "status", "conditions")
			}
			ready, reason := judgeExecutorReady(d, e, executorTestImage())
			if ready != (name == "ready") {
				t.Fatalf("ready=%v: %s", ready, reason)
			}
			if name == "condition refusal" && (!strings.Contains(reason, "ImageMismatch") || !strings.Contains(reason, "wrong pod pin")) {
				t.Fatal(reason)
			}
		})
	}
}

func TestExecutorReadEscrowComparesAndZerosTheDeployedKey(t *testing.T) {
	fakeHome(t)
	key := bytes.Repeat([]byte{0x25}, escrow.RootKeySize)
	art, err := escrow.Wrap(key, "owner-passphrase", "prod", escrow.KDFParams{Alg: escrow.KDFArgon2id, Time: 1, Memory: 8, Threads: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := art.Encode()
	if err != nil {
		t.Fatal(err)
	}
	previousRead, previousKey := executorReadEscrow, executorReadRootKey
	t.Cleanup(func() { executorReadEscrow, executorReadRootKey = previousRead, previousKey })
	for _, name := range []string{"matches", "mismatch", "key error", "missing", "wrong instance"} {
		t.Run(name, func(t *testing.T) {
			var used []byte
			executorReadEscrow = func(path string) ([]byte, error) {
				if path != filepath.Join("owner-escrow", "prod-rootkey.escrow") {
					t.Fatalf("wrong existing path scheme: %s", path)
				}
				if name == "missing" {
					return nil, os.ErrNotExist
				}
				return raw, nil
			}
			executorReadRootKey = func(context.Context, string, string) ([]byte, error) {
				used = bytes.Clone(key)
				if name == "mismatch" {
					used[0]++
				}
				if name == "key error" {
					return used, errors.New("do not print root key")
				}
				return used, nil
			}
			e := readExecutorEscrow(t.Context(), "kind-c", "prod", "owner-escrow")
			if name == "wrong instance" {
				e.ArtifactInstance = "other"
			}
			if err := judgeExecutorEscrow([]executorEscrowEvidence{e}); (err == nil) != (name == "matches") {
				t.Fatalf("%s: %v", name, err)
			}
			for _, value := range used {
				if value != 0 {
					t.Fatal("deployed key buffer survived verification")
				}
			}
		})
	}
}

func stubExecutorClients(t *testing.T, dyn dynamic.Interface, typed *kubefake.Clientset, resources []*metav1.APIResourceList) {
	t.Helper()
	previousClients, previousOperator := executorClients, executorRequireOperator
	t.Cleanup(func() { executorClients, executorRequireOperator = previousClients, previousOperator })
	typed.Discovery().(*fakediscovery.FakeDiscovery).Resources = resources
	executorClients = func(string) (dynamic.Interface, discovery.DiscoveryInterface, kubernetes.Interface, error) {
		return dyn, typed.Discovery(), typed, nil
	}
	executorRequireOperator = func(context.Context, string, string, string) error { return nil }
}

func allowExecutorReviews(typed *kubefake.Clientset, allow func(*authorizationv1.SelfSubjectAccessReview) bool) {
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		request := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview).DeepCopy()
		request.Status.Allowed = allow(request)
		return true, request, nil
	})
}

func TestExecutorPreflightAcceptsBothRBACPathsAndChecksWaitReads(t *testing.T) {
	objects, mapper, _ := executorTestPlan(t)
	for _, path := range []string{"direct", "existing rules", "denied"} {
		t.Run(path, func(t *testing.T) {
			typed := kubefake.NewSimpleClientset()
			seen := map[string]bool{}
			allowExecutorReviews(typed, func(r *authorizationv1.SelfSubjectAccessReview) bool {
				a := r.Spec.ResourceAttributes
				seen[a.Verb+" "+a.Resource+"/"+a.Name] = true
				if path == "denied" {
					return a.Verb != "watch" && !(a.Verb == "patch" && a.Resource == "deployments")
				}
				if a.Verb == "escalate" || a.Verb == "bind" {
					return path == "direct"
				}
				return path == "existing rules" || a.Verb == "create" || a.Verb == "patch" || (a.Resource == "deployments" && a.Verb == "get") || (a.Resource == "executorenrollments" && a.Verb == "get")
			})
			err := preflightExecutorAccess(t.Context(), typed, objects, mapper)
			if (err == nil) != (path != "denied") {
				t.Fatalf("%s: %v", path, err)
			}
			for _, check := range []string{"escalate clusterroles/dc-executor", "escalate roles/dc-executor", "bind clusterroles/dc-executor", "bind roles/dc-executor", "get deployments/dc-executor", "get executorenrollments/cluster"} {
				if !seen[check] {
					t.Errorf("missing SSAR %s", check)
				}
			}
			if path == "denied" && !strings.Contains(err.Error(), "patch deployments") {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutorEnrollAppliesRenderedObjectsWithoutCredentials(t *testing.T) {
	objects, _, resources := executorTestPlan(t)
	existing := executorReadyEnrollment()
	existing.Object["spec"] = map[string]interface{}{"owner": "ops", "escrowDelivery": "OwnerOffline", "executorImage": "previous-image"}
	dyn := executorTestDynamic(ownedExecutorNamespace(), existing)
	typed := kubefake.NewSimpleClientset(executorReadyDeployment())
	stubExecutorClients(t, dyn, typed, resources)
	allowExecutorReviews(typed, executorApplyWaitPermission)
	applied := 0
	dyn.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patch := a.(k8stesting.PatchAction)
		if patch.GetPatchType() != types.ApplyPatchType {
			t.Fatal("not server-side apply")
		}
		options := a.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		if options.FieldManager != "dcctl-executor" || options.Force == nil || *options.Force {
			t.Fatalf("wrong ownership policy: %+v", options)
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(patch.GetPatch()); err != nil {
			t.Fatal(err)
		}
		if obj.GetKind() == "Secret" || strings.Contains(string(patch.GetPatch()), "kubeconfig") {
			t.Fatal("enrollment applied credentials")
		}
		if obj.GetKind() == "ExecutorEnrollment" {
			_ = unstructured.SetNestedMap(obj.Object, executorReadyEnrollment().Object["status"].(map[string]interface{}), "status")
			obj.SetGeneration(2)
		}
		if err := dyn.Tracker().Create(a.GetResource(), obj, a.GetNamespace()); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				t.Fatal(err)
			}
			if err := dyn.Tracker().Update(a.GetResource(), obj, a.GetNamespace()); err != nil {
				t.Fatal(err)
			}
		}
		applied++
		return true, obj, nil
	})
	var out bytes.Buffer
	if err := EnrollExecutor(t.Context(), ExecutorEnrollOptions{Owner: "ops", Image: executorTestImage()}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if applied != len(objects) {
		t.Fatalf("applied %d, rendered %d", applied, len(objects))
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" || action.GetResource().Resource == "secrets" {
			t.Fatalf("unexpected write/read: %v", action)
		}
	}
}

func executorApplyWaitPermission(r *authorizationv1.SelfSubjectAccessReview) bool {
	a := r.Spec.ResourceAttributes
	return a != nil && (a.Verb == "create" || a.Verb == "patch" || a.Verb == "escalate" || a.Verb == "bind" ||
		(a.Verb == "get" && ((a.Group == "apps" && a.Resource == "deployments") || (a.Group == enrollmentGVR.Group && a.Resource == enrollmentGVR.Resource))))
}

func TestExecutorPreflightRequiresEachApplyAndWaitPermission(t *testing.T) {
	objects, mapper, _ := executorTestPlan(t)
	var required []executorReview
	for _, obj := range objects {
		mapping, err := mapper.RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GroupVersionKind().Version)
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range []string{"create", "patch"} {
			name := obj.GetName()
			if verb == "create" {
				name = ""
			}
			required = append(required, executorResourceReview(verb, mapping.Resource.Group, mapping.Resource.Resource, obj.GetNamespace(), name))
		}
	}
	required = append(required, executorResourceReview("get", "apps", "deployments", executorNamespace, executorName), executorResourceReview("get", enrollmentGVR.Group, enrollmentGVR.Resource, "", "cluster"))
	for _, missing := range required {
		t.Run(missing.description(), func(t *testing.T) {
			typed := kubefake.NewSimpleClientset()
			allowExecutorReviews(typed, func(r *authorizationv1.SelfSubjectAccessReview) bool {
				return executorApplyWaitPermission(r) && *r.Spec.ResourceAttributes != *missing.Resource
			})
			if err := preflightExecutorAccess(t.Context(), typed, objects, mapper); err == nil || !strings.Contains(err.Error(), missing.description()) {
				t.Fatalf("missing %s accepted: %v", missing.description(), err)
			}
		})
	}
}

func TestExecutorEnrollmentRefusalsPrecedeEveryApply(t *testing.T) {
	_, _, resources := executorTestPlan(t)
	for _, name := range []string{"bad image", "missing escrow", "privilege denial", "dry run"} {
		t.Run(name, func(t *testing.T) {
			dyn := executorTestDynamic()
			if name == "missing escrow" {
				u := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "core.devicechain.io/v1beta1", "kind": "Instance", "metadata": map[string]interface{}{"name": "prod"}}}
				if err := dyn.Tracker().Create(instanceGVR, u, ""); err != nil {
					t.Fatal(err)
				}
			}
			typed := kubefake.NewSimpleClientset()
			stubExecutorClients(t, dyn, typed, resources)
			allowExecutorReviews(typed, func(*authorizationv1.SelfSubjectAccessReview) bool { return name != "privilege denial" })
			previousRead := executorReadEscrow
			t.Cleanup(func() { executorReadEscrow = previousRead })
			executorReadEscrow = func(string) ([]byte, error) { return nil, os.ErrNotExist }
			opts := ExecutorEnrollOptions{Owner: "ops", Image: executorTestImage(), DryRun: name == "dry run"}
			operatorCalls := 0
			executorRequireOperator = func(context.Context, string, string, string) error { operatorCalls++; return nil }
			if name == "bad image" {
				opts.Image = "operator:latest"
			}
			err := EnrollExecutor(t.Context(), opts, io.Discard)
			if (err == nil) != (name == "dry run") {
				t.Fatalf("%s: %v", name, err)
			}
			if name == "bad image" && operatorCalls != 0 {
				t.Fatal("invalid flags contacted cluster")
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() != "list" && a.GetVerb() != "get" {
					t.Fatalf("pre-write refusal performed %s", a.GetVerb())
				}
			}
		})
	}
}

func TestExecutorPartialApplyReportsEveryObject(t *testing.T) {
	objects, mapper, _ := executorTestPlan(t)
	dyn := executorTestDynamic()
	calls := 0
	cause := errors.New("apply conflict")
	dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 3 {
			return true, nil, cause
		}
		return true, &unstructured.Unstructured{}, nil
	})
	var out bytes.Buffer
	err := applyExecutorObjects(t.Context(), dyn, mapper, objects, &out)
	if !errors.Is(err, cause) || calls != 3 {
		t.Fatalf("calls=%d: %v", calls, err)
	}
	for i, obj := range objects {
		state := "not applied "
		if i < 2 {
			state = "applied "
		}
		if i == 2 {
			state = "failed "
		}
		if !strings.Contains(out.String(), state+executorObjectName(obj)) {
			t.Errorf("missing result for %s: %s", obj.GetName(), out.String())
		}
	}
}

func TestUpgradeCallsPendingOperationRefusalBeforeReadingDeclaration(t *testing.T) {
	stubOperatorCheck(t, nil)
	marker := errors.New("pending-operation refusal was called")
	previous := refusePendingOperationFor
	t.Cleanup(func() { refusePendingOperationFor = previous })
	calls := 0
	refusePendingOperationFor = func(_ context.Context, kubeContext, instance, verb string) error {
		calls++
		if kubeContext != "kind-c" || instance != "prod" || verb != "an upgrade" {
			t.Fatalf("wrong lookup: %s %s %s", kubeContext, instance, verb)
		}
		return marker
	}
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	_, err = hydrateUpgradeState(t.Context(), nil, provider, ClusterBinding{KubeContext: "kind-c"}, UpgradeOptions{Options: Options{Instance: "prod"}})
	if !errors.Is(err, marker) || calls != 1 {
		t.Fatalf("calls=%d, error=%v", calls, err)
	}
}

func TestDestroyWarnsAndContinuesForPendingOperations(t *testing.T) {
	rig := newTeardownRig(t, nil, []string{"acme"}, "acme")
	previous := pendingOperationForDestroy
	t.Cleanup(func() { pendingOperationForDestroy = previous })
	calls := 0
	pendingOperationForDestroy = func(_ context.Context, kubeContext, instance, verb string) error {
		calls++
		if kubeContext != "kind-c" || instance != "acme" || verb != "a destroy" {
			t.Fatalf("wrong lookup: %s %s %s", kubeContext, instance, verb)
		}
		return errors.New("request: action Teardown, age 1h; detection, not a lock")
	}
	out, err := rig.destroy(t, true)
	if err != nil || calls != 1 || !strings.Contains(out, "Continuing destroy") || !strings.Contains(out, "request:") {
		t.Fatalf("calls=%d, err=%v\n%s", calls, err, out)
	}
}

func TestForcedUnenrollmentPreservesAConcurrentTerminalVerdict(t *testing.T) {
	op := executorTestOperation("request", "prod", "Running")
	dyn := executorTestDynamic(&op)
	writes := 0
	dyn.PrependReactor("update", "instanceoperations", func(a k8stesting.Action) (bool, runtime.Object, error) {
		writes++
		terminal := op.DeepCopy()
		_ = unstructured.SetNestedField(terminal.Object, "Succeeded", "status", "phase")
		_ = unstructured.SetNestedField(terminal.Object, "ExecutorVerdict", "status", "reason")
		terminal.SetResourceVersion("8")
		if err := dyn.Tracker().Update(operationGVR, terminal, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(operationGVR.GroupResource(), "request", errors.New("resourceVersion changed"))
	})
	if err := prepareExecutorUnenroll(t.Context(), dyn, true); err != nil {
		t.Fatal(err)
	}
	got, err := dyn.Resource(operationGVR).Get(t.Context(), "request", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if writes != 1 || phase != "Succeeded" {
		t.Fatalf("writes=%d, phase=%s", writes, phase)
	}
}

func TestExecutorStatusIsSuccessfulWhenNotEnrolled(t *testing.T) {
	dyn := executorTestDynamic()
	stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
	var out bytes.Buffer
	if err := ExecutorStatus(t.Context(), "", &out); err != nil || !strings.Contains(out.String(), "not enrolled") {
		t.Fatalf("%v: %s", err, out.String())
	}
}

func TestExecutorUnenrollmentNeverDeletesResourceDefinitions(t *testing.T) {
	dyn := executorTestDynamic(ownedExecutorNamespace())
	stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
	if err := UnenrollExecutor(t.Context(), "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	var enrollmentDeleted, namespaceDeleted bool
	for _, a := range dyn.Actions() {
		if a.GetVerb() != "delete" {
			continue
		}
		switch a.GetResource().Resource {
		case "customresourcedefinitions", "instances", "instanceoperations", "secrets":
			t.Fatalf("deleted protected resource: %v", a)
		case "executorenrollments":
			enrollmentDeleted = true
		case "namespaces":
			namespaceDeleted = true
		}
	}
	if !enrollmentDeleted || !namespaceDeleted {
		t.Fatalf("enrollment=%v namespace=%v", enrollmentDeleted, namespaceDeleted)
	}
}

func ownedExecutorNamespace() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]interface{}{"name": executorNamespace, "uid": "owned-namespace", "labels": map[string]interface{}{"app.kubernetes.io/managed-by": "dcctl-executor"}},
	}}
}

func TestExecutorUnenrollOwnershipBeforeAnyWrite(t *testing.T) {
	for _, name := range []string{"foreign", "foreign forced", "absent", "owned"} {
		t.Run(name, func(t *testing.T) {
			ns := ownedExecutorNamespace()
			if strings.HasPrefix(name, "foreign") {
				ns.SetLabels(nil)
			}
			dyn := executorTestDynamic()
			if name != "absent" {
				if err := dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, ns, ""); err != nil {
					t.Fatal(err)
				}
			}
			if name == "foreign forced" {
				op := executorTestOperation("pending", "prod", "Pending")
				if err := dyn.Tracker().Create(operationGVR, &op, ""); err != nil {
					t.Fatal(err)
				}
			}
			stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
			var out bytes.Buffer
			err := UnenrollExecutor(t.Context(), "", name == "foreign forced", &out)
			if strings.HasPrefix(name, "foreign") {
				if err == nil || !strings.Contains(err.Error(), executorNamespace) {
					t.Errorf("foreign namespace accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if name == "absent" && !strings.Contains(out.String(), "not enrolled") {
				t.Errorf("missing not enrolled report: %s", out.String())
			}
			deletedNamespace := false
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "get" || a.GetVerb() == "list" {
					continue
				}
				if name != "owned" {
					t.Errorf("unexpected mutation: %v", a)
					continue
				}
				if a.GetVerb() == "delete" && a.GetResource().Resource == "namespaces" {
					deletedNamespace = true
					options := a.(k8stesting.DeleteAction).GetDeleteOptions()
					if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != ns.GetUID() {
						t.Errorf("namespace deletion lost UID: %+v", options)
					}
				}
			}
			if name == "owned" && !deletedNamespace {
				t.Error("owned namespace not removed")
			}
		})
	}
}

func TestExecutorEnrollOwnershipBeforeEveryApply(t *testing.T) {
	_, _, resources := executorTestPlan(t)
	for _, name := range []string{"foreign namespace", "owner", "escrowDelivery", "enrollment read error", "namespace read error"} {
		t.Run(name, func(t *testing.T) {
			dyn := executorTestDynamic()
			switch name {
			case "foreign namespace":
				ns := ownedExecutorNamespace()
				ns.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "someone-else"})
				if err := dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, ns, ""); err != nil {
					t.Fatal(err)
				}
			case "owner", "escrowDelivery":
				e := executorReadyEnrollment()
				e.Object["spec"] = map[string]interface{}{"owner": "ops", "escrowDelivery": "OwnerOffline"}
				_ = unstructured.SetNestedField(e.Object, "other", "spec", name)
				if err := dyn.Tracker().Create(enrollmentGVR, e, ""); err != nil {
					t.Fatal(err)
				}
			case "enrollment read error", "namespace read error":
				resource := "executorenrollments"
				if name == "namespace read error" {
					resource = "namespaces"
				}
				dyn.PrependReactor("get", resource, func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			typed := kubefake.NewSimpleClientset()
			stubExecutorClients(t, dyn, typed, resources)
			allowExecutorReviews(typed, func(*authorizationv1.SelfSubjectAccessReview) bool { return true })
			dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("unexpected apply")
			})
			var out bytes.Buffer
			err := EnrollExecutor(t.Context(), ExecutorEnrollOptions{Owner: "ops", Image: executorTestImage()}, &out)
			if err == nil {
				t.Fatal("accepted unsafe enrollment")
			}
			if name == "owner" || name == "escrowDelivery" {
				want := "ops"
				if name == "escrowDelivery" {
					want = "OwnerOffline"
				}
				for _, text := range []string{name, "other", want, "unenroll first"} {
					if !strings.Contains(err.Error(), text) {
						t.Errorf("missing %q: %v", text, err)
					}
				}
			}
			if name == "foreign namespace" && !strings.Contains(err.Error(), executorNamespace) {
				t.Error(err)
			}
			if out.Len() != 0 {
				t.Errorf("preflight output before refusal: %s", out.String())
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() != "get" && a.GetVerb() != "list" {
					t.Errorf("refusal mutated cluster: %v", a)
				}
			}
		})
	}
}

func TestExecutorUnenrollReadFailuresAndMissingUID(t *testing.T) {
	for _, name := range []string{"namespace read", "enrollment read", "missing UID"} {
		t.Run(name, func(t *testing.T) {
			ns := ownedExecutorNamespace()
			if name == "missing UID" {
				ns.SetUID("")
			}
			dyn := executorTestDynamic(ns)
			if name != "missing UID" {
				resource := "namespaces"
				if name == "enrollment read" {
					resource = "executorenrollments"
				}
				dyn.PrependReactor("get", resource, func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
			if err := UnenrollExecutor(t.Context(), "", true, io.Discard); err == nil {
				t.Fatal("unsafe removal accepted")
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() != "get" {
					t.Errorf("refusal performed %s", a.GetVerb())
				}
			}
		})
	}
}

func TestExecutorEnrollmentPlanMustDeclareEnrollment(t *testing.T) {
	dyn := executorTestDynamic(executorReadyEnrollment())
	if err := requireCompatibleExecutorEnrollment(t.Context(), dyn, nil); err == nil {
		t.Fatal("plan without an enrollment accepted")
	}
}
