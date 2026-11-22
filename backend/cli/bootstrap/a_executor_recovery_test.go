// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestPendingRecoveryNamesEachDelete(t *testing.T) {
	a := executorTestOperation("request-a", "prod", "Pending")
	b := executorTestOperation("request-b", "prod", "Running")
	dyn := executorTestDynamic(&a, &b)
	err := RefuseForPendingOperation(t.Context(), dyn, "prod", "an upgrade")
	if err == nil {
		t.Fatal("pending requests passed")
	}
	for _, name := range []string{"request-a", "request-b"} {
		if !strings.Contains(err.Error(), "kubectl delete instanceoperation "+name) {
			t.Errorf("no recovery command for %s: %v", name, err)
		}
	}
}

func TestForceUnenrollWithoutEnrollmentClosesOnlyPending(t *testing.T) {
	var objects []runtime.Object
	for _, phase := range []string{"", "Pending", "Running", "Future", "Refused", "Succeeded", "Failed"} {
		op := executorTestOperation("request-"+strings.ToLower(phase)+"x", "prod", phase)
		objects = append(objects, &op)
	}
	canary := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]interface{}{"name": executorName, "uid": "canary"},
	}}
	objects = append(objects, canary)
	dyn := executorTestDynamic(objects...)
	stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
	var out bytes.Buffer
	if err := UnenrollExecutor(t.Context(), "", true, &out); err != nil {
		t.Fatal(err)
	}
	for i, original := range objects[:7] {
		op := original.(*unstructured.Unstructured)
		got, err := dyn.Resource(operationGVR).Get(t.Context(), op.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		reason, _, _ := unstructured.NestedString(got.Object, "status", "reason")
		if i < 4 {
			if phase != "Refused" || reason != "Unenrolled" {
				t.Errorf("unclosed request %s: %s/%s", op.GetName(), phase, reason)
			}
			conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
			if len(conditions) != 1 {
				t.Fatalf("missing forced decision condition: %+v", got.Object)
			}
			condition := conditions[0].(map[string]interface{})
			if condition["type"] != "Decided" || condition["status"] != "True" || condition["reason"] != "Unenrolled" {
				t.Errorf("forced condition: %+v", condition)
			}
		} else if operationNonTerminal(*got) || reason != "" {
			t.Errorf("terminal verdict changed: %+v", got.Object)
		}
	}
	if !strings.Contains(out.String(), "4 non-terminal InstanceOperations") {
		t.Errorf("missing closed-request count: %s", out.String())
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "delete" || (action.GetVerb() == "update" && (action.GetResource() != operationGVR || action.GetSubresource() != "status")) {
			t.Errorf("unrelated mutation while not enrolled: %v", action)
		}
	}
}

func TestUnenrollClusterRBACOwnershipAndUID(t *testing.T) {
	for _, scenario := range []string{"owned", "unlabelled", "foreign", "missing UID", "replacement", "read error"} {
		t.Run(scenario, func(t *testing.T) {
			objects := []runtime.Object{ownedExecutorNamespace()}
			for _, target := range []struct{ kind, name string }{{"ClusterRole", executorName}, {"ClusterRole", "dc-executor-requester"}, {"ClusterRoleBinding", executorName}} {
				obj := &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "rbac.authorization.k8s.io/v1", "kind": target.kind,
					"metadata": map[string]interface{}{"name": target.name, "uid": "original-" + target.name, "labels": map[string]interface{}{executorManagedByLabel: executorFieldManager}},
				}}
				if scenario == "unlabelled" {
					obj.SetLabels(nil)
				}
				if scenario == "foreign" {
					obj.SetLabels(map[string]string{executorManagedByLabel: "someone-else"})
				}
				if scenario == "missing UID" {
					obj.SetUID("")
				}
				objects = append(objects, obj)
			}
			dyn := executorTestDynamic(objects...)
			stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
			if scenario == "replacement" {
				dyn.PrependReactor("delete", "clusterrolebindings", func(a k8stesting.Action) (bool, runtime.Object, error) {
					options := a.(k8stesting.DeleteAction).GetDeleteOptions()
					if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "original-"+executorName {
						t.Error("replacement deletion lost original UID")
					}
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"}, executorName, errors.New("UID changed"))
				})
			}
			if scenario == "read error" {
				dyn.PrependReactor("get", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			var out bytes.Buffer
			err := UnenrollExecutor(t.Context(), "", true, &out)
			if (err != nil) != (scenario == "replacement" || scenario == "read error") {
				t.Fatalf("%s: %v", scenario, err)
			}
			deleted := 0
			for _, action := range dyn.Actions() {
				if action.GetVerb() != "delete" || (action.GetResource().Resource != "clusterroles" && action.GetResource().Resource != "clusterrolebindings") {
					continue
				}
				deleted++
				if scenario != "owned" && scenario != "replacement" {
					t.Errorf("unsafe RBAC deletion: %v", action)
				}
				options := action.(k8stesting.DeleteAction).GetDeleteOptions()
				name := action.(k8stesting.DeleteAction).GetName()
				if options.Preconditions == nil || options.Preconditions.UID == nil || string(*options.Preconditions.UID) != "original-"+name {
					t.Errorf("RBAC deletion lost UID: %+v", options)
				}
			}
			if scenario == "owned" && deleted != 3 {
				t.Errorf("deleted %d RBAC objects, want 3", deleted)
			}
			if scenario == "unlabelled" || scenario == "foreign" || scenario == "missing UID" {
				for _, target := range objects[1:] {
					obj := target.(*unstructured.Unstructured)
					if !strings.Contains(out.String(), "skipped "+obj.GetKind()+"/"+obj.GetName()) {
						t.Errorf("missing skipped-object report: %s", out.String())
					}
				}
			}
		})
	}
}

func TestUnenrolledWithoutForceLeavesPendingAlone(t *testing.T) {
	op := executorTestOperation("pending", "prod", "Pending")
	dyn := executorTestDynamic(&op)
	stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
	if err := UnenrollExecutor(t.Context(), "", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "get" {
			t.Errorf("unenrolled without force performed %s", action.GetVerb())
		}
	}
}

func TestUnenrolledForceReadAndWriteErrors(t *testing.T) {
	for _, verb := range []string{"list", "update", "absent definition"} {
		t.Run(verb, func(t *testing.T) {
			op := executorTestOperation("pending", "prod", "Running")
			dyn := executorTestDynamic(&op)
			if verb == "absent definition" {
				dyn.PrependReactor("list", "instanceoperations", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewNotFound(operationGVR.GroupResource(), "")
				})
			} else {
				dyn.PrependReactor(verb, "instanceoperations", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
			var out bytes.Buffer
			err := UnenrollExecutor(t.Context(), "", true, &out)
			if (err != nil) != (verb != "absent definition") {
				t.Fatalf("%s: %v", verb, err)
			}
			if verb == "absent definition" && !strings.Contains(out.String(), "0 non-terminal InstanceOperations") {
				t.Error(out.String())
			}
			for _, action := range dyn.Actions() {
				if action.GetVerb() == "delete" {
					t.Error("deleted after a closure error or absent definition")
				}
			}
		})
	}
}

func TestUnenrolledForceCountsConcurrentTerminalAsUnchanged(t *testing.T) {
	op := executorTestOperation("pending", "prod", "Running")
	dyn := executorTestDynamic(&op)
	dyn.PrependReactor("update", "instanceoperations", func(k8stesting.Action) (bool, runtime.Object, error) {
		terminal := op.DeepCopy()
		_ = unstructured.SetNestedField(terminal.Object, "Succeeded", "status", "phase")
		terminal.SetResourceVersion("8")
		if err := dyn.Tracker().Update(operationGVR, terminal, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(operationGVR.GroupResource(), op.GetName(), errors.New("concurrent verdict"))
	})
	stubExecutorClients(t, dyn, kubefake.NewSimpleClientset(), nil)
	var out bytes.Buffer
	if err := UnenrollExecutor(t.Context(), "", true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 non-terminal InstanceOperations") {
		t.Errorf("counted another writer's verdict: %s", out.String())
	}
}
