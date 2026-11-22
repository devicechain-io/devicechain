// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestExecutorEnrollmentFlags(t *testing.T) {
	image := "ghcr.io/devicechain-io/operator@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		owner, image string
		valid        bool
	}{
		{"owner", image, true}, {"", image, false}, {"Owner", image, false},
		{strings.Repeat("x", 64), image, false}, {"-owner", image, false},
		{"owner", "operator:latest", false}, {"owner", "operator@sha256:abc", false},
		{"owner", "operator@sha256:" + strings.Repeat("A", 64), false},
	} {
		if err := ValidateExecutorEnrollment(tc.owner, tc.image); (err == nil) != tc.valid {
			t.Errorf("owner=%q image=%q: %v, valid=%v", tc.owner, tc.image, err, tc.valid)
		}
	}
}

func TestExecutorEscrowJudge(t *testing.T) {
	good := executorEscrowEvidence{Instance: "prod", ArtifactInstance: "prod", ArtifactPresent: true, Matches: true}
	for _, name := range []string{"missing", "wrong instance", "mismatch", "unreadable artifact", "unreadable key", "good", "zero instances"} {
		t.Run(name, func(t *testing.T) {
			e := good
			switch name {
			case "missing":
				e.ArtifactPresent = false
			case "wrong instance":
				e.ArtifactInstance = "other"
			case "mismatch":
				e.Matches = false
			case "unreadable artifact":
				e.ArtifactErr = errors.New("unreadable")
			case "unreadable key":
				e.KeyErr = errors.New("unreadable")
			}
			in := []executorEscrowEvidence{e}
			if name == "zero instances" {
				in = nil
			}
			err := judgeExecutorEscrow(in)
			valid := name == "good" || name == "zero instances"
			if (err == nil) != valid {
				t.Fatalf("%v, valid=%v", err, valid)
			}
			if !valid && (!strings.Contains(err.Error(), "prod") || !strings.Contains(err.Error(), "dcctl bootstrap")) {
				t.Fatalf("refusal omits the instance or remedy: %v", err)
			}
		})
	}
	a, b := good, good
	a.Instance, b.Instance = "a", "b"
	a.ArtifactPresent, b.Matches = false, false
	if err := judgeExecutorEscrow([]executorEscrowEvidence{a, b}); err == nil || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") {
		t.Fatalf("must report every failing instance: %v", err)
	}
}

func TestExecutorAccessJudge(t *testing.T) {
	for _, tc := range []struct {
		name          string
		direct, rules []bool
		allowed       bool
	}{
		{"escalate or bind", []bool{true}, []bool{false, false}, true},
		{"already holds every rule", []bool{false}, []bool{true, true}, true},
		{"one rule missing", []bool{false}, []bool{true, false}, false},
		{"neither path", []bool{false}, []bool{false, false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := executorAccessCheck{Description: "role", Alternatives: [][]executorAccessResult{{}, {}}}
			for i, path := range [][]bool{tc.direct, tc.rules} {
				for _, allowed := range path {
					check.Alternatives[i] = append(check.Alternatives[i], executorAccessResult{Permission: "rule", Allowed: allowed})
				}
			}
			if err := judgeExecutorAccess([]executorAccessCheck{check}); (err == nil) != tc.allowed {
				t.Fatalf("%v", err)
			}
		})
	}
	checks := []executorAccessCheck{
		{Description: "patch deployment", Alternatives: [][]executorAccessResult{{{Permission: "patch deployments", Allowed: false}}}},
		{Description: "watch deployment", Alternatives: [][]executorAccessResult{{{Permission: "watch deployments", Allowed: false}}}},
	}
	err := judgeExecutorAccess(checks)
	if err == nil || !strings.Contains(err.Error(), "patch deployment") || !strings.Contains(err.Error(), "watch deployment") || !strings.Contains(err.Error(), "pre-check") || !strings.Contains(err.Error(), "not a guarantee") {
		t.Fatalf("must name every missing permission and advisory scope: %v", err)
	}
	checks[0].Alternatives[0][0] = executorAccessResult{Permission: "patch deployments", Allowed: true, Err: errors.New("unreadable")}
	if judgeExecutorAccess(checks[:1]) == nil {
		t.Fatal("an unreadable SSAR passed")
	}
}

func executorTestOperation(name, instance, phase string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.devicechain.io/v1beta1", "kind": "InstanceOperation",
		"metadata": map[string]interface{}{"name": name, "resourceVersion": "7", "generation": int64(2)},
		"spec":     map[string]interface{}{"instance": instance, "action": "Upgrade"},
	}}
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-time.Hour)))
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

func executorTestDynamic(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		operationGVR: "InstanceOperationList", enrollmentGVR: "ExecutorEnrollmentList", instanceGVR: "InstanceList",
	}, objects...)
}

func TestPendingOperationJudge(t *testing.T) {
	for _, phase := range []string{"", "Pending", "Running", "Refused", "Succeeded", "Failed"} {
		t.Run("phase="+phase, func(t *testing.T) {
			op := executorTestOperation("request", "prod", phase)
			err := judgePendingOperations([]unstructured.Unstructured{op}, nil, "prod", "an upgrade", time.Now())
			want := phase == "" || phase == "Pending" || phase == "Running"
			if (err != nil) != want {
				t.Fatalf("phase=%q: %v", phase, err)
			}
			if want {
				for _, text := range []string{"request", "Upgrade", "age", "dcctl executor unenroll --force", "detection, not a lock"} {
					if !strings.Contains(err.Error(), text) {
						t.Errorf("missing %q: %v", text, err)
					}
				}
			}
		})
	}
	other := executorTestOperation("other", "other", "Running")
	if err := judgePendingOperations([]unstructured.Unstructured{other}, nil, "prod", "an upgrade", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{apierrors.NewNotFound(operationGVR.GroupResource(), ""), &meta.NoResourceMatchError{PartialResource: operationGVR}} {
		if got := judgePendingOperations(nil, err, "prod", "an upgrade", time.Now()); got != nil {
			t.Fatal(got)
		}
	}
	if judgePendingOperations(nil, errors.New("forbidden"), "prod", "an upgrade", time.Now()) == nil {
		t.Fatal("read failure passed")
	}
}

func TestPendingOperationDynamicReadFailsClosed(t *testing.T) {
	u := executorTestOperation("request", "prod", "Running")
	dyn := executorTestDynamic(&u)
	if RefuseForPendingOperation(t.Context(), dyn, "prod", "a bootstrap") == nil {
		t.Fatal("Running operation passed")
	}
	dyn.PrependReactor("list", "instanceoperations", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	if RefuseForPendingOperation(t.Context(), dyn, "prod", "a bootstrap") == nil {
		t.Fatal("read failure passed")
	}
}

func TestUnenrollRefusesPendingWithoutForce(t *testing.T) {
	u := executorTestOperation("request", "prod", "Pending")
	dyn := executorTestDynamic(&u)
	if err := prepareExecutorUnenroll(context.Background(), dyn, false); err == nil {
		t.Fatal("unenrollment with a Pending operation passed without --force")
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() != "list" {
			t.Fatalf("refusal wrote to cluster: %v", a)
		}
	}
	if err := prepareExecutorUnenroll(context.Background(), dyn, true); err != nil {
		t.Fatal(err)
	}
	got, err := dyn.Resource(operationGVR).Get(t.Context(), "request", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	reason, _, _ := unstructured.NestedString(got.Object, "status", "reason")
	if phase != "Refused" || reason != "Unenrolled" {
		t.Fatalf("forced verdict: %s/%s", phase, reason)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "update" {
			if a.GetSubresource() != "status" || a.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured).GetResourceVersion() != "7" {
				t.Fatalf("status update lost RV: %v", a)
			}
		}
	}
}
