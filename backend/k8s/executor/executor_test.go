// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func input() DecideInput {
	return DecideInput{
		Enrollment:   &api.ExecutorEnrollment{ObjectMeta: metav1.ObjectMeta{UID: "enrollment", Generation: 2}, Status: api.ExecutorEnrollmentStatus{ObservedGeneration: 2}},
		Verification: Verification{Ready: true},
		Operation:    &api.InstanceOperation{Spec: api.InstanceOperationSpec{Instance: "prod", InstanceUID: "target", EnrollmentUID: "enrollment", Action: api.ActionUpgrade, ProtocolVersion: 1}},
		Instance:     &api.Instance{ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "target"}},
		Registry:     Registry{},
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name         string
		change       func(*DecideInput)
		phase        api.OperationPhase
		reason, skip string
	}{
		{"preview", func(*DecideInput) {}, api.OperationRefused, "NotExecutableInThisBuild", ""},
		{"new protocol", func(i *DecideInput) { i.Operation.Spec.ProtocolVersion = 2 }, "", "", "protocol"},
		{"unknown action", func(i *DecideInput) { i.Operation.Spec.Action = "Future" }, "", "", "action"},
		{"no enrollment", func(i *DecideInput) { i.Enrollment = nil }, "", "", ""},
		{"unverified", func(i *DecideInput) { i.Verification.Ready = false }, "", "", ""},
		{"old observation", func(i *DecideInput) { i.Enrollment.Status.ObservedGeneration = 1 }, "", "", ""},
		{"stale enrollment", func(i *DecideInput) { i.Operation.Spec.EnrollmentUID = "old" }, api.OperationRefused, "StaleEnrollment", ""},
		{"missing target", func(i *DecideInput) { i.Instance = nil }, api.OperationRefused, "InstanceNotFound", ""},
		{"stale target", func(i *DecideInput) { i.Operation.Spec.InstanceUID = "old" }, api.OperationRefused, "StaleTarget", ""},
		{"bootstrap exists", func(i *DecideInput) { i.Operation.Spec.Action = api.ActionBootstrap }, api.OperationRefused, "InstanceExists", ""},
		{"bootstrap absent", func(i *DecideInput) { i.Operation.Spec.Action = api.ActionBootstrap; i.Instance = nil }, api.OperationRefused, "NotExecutableInThisBuild", ""},
		{"runner", func(i *DecideInput) {
			i.Registry = Registry{api.ActionUpgrade: func(context.Context, *api.InstanceOperation) error { return nil }}
		}, api.OperationRunning, "Executable", ""},
	}
	for _, terminal := range []api.OperationPhase{api.OperationRefused, api.OperationSucceeded, api.OperationFailed} {
		phase := terminal
		cases = append(cases, struct {
			name         string
			change       func(*DecideInput)
			phase        api.OperationPhase
			reason, skip string
		}{"terminal " + string(phase), func(i *DecideInput) { i.Operation.Status.Phase = phase }, "", "", ""})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := input()
			tt.change(&in)
			before := in.Operation.DeepCopy()
			got := Decide(in)
			if got.Phase != tt.phase || got.Reason != tt.reason || got.SkippedReason != tt.skip || got.Write != (tt.phase != "") {
				t.Fatalf("verdict=%+v", got)
			}
			if !reflect.DeepEqual(before, in.Operation) {
				t.Fatal("Decide mutated input")
			}
			if got.Reason == "NotExecutableInThisBuild" && (!strings.Contains(got.Message, "Preview") || !strings.Contains(got.Message, "release notes")) {
				t.Fatal(got.Message)
			}
		})
	}
	// Every recognised verb reaches the inert registry, including Install and Teardown.
	for _, action := range []api.OperationAction{api.ActionInstall, api.ActionBootstrap, api.ActionUpgrade, api.ActionTeardown} {
		in := input()
		in.Operation.Spec.Action = action
		if action == api.ActionBootstrap {
			in.Instance = nil
		}
		if got := Decide(in); got.Reason != "NotExecutableInThisBuild" {
			t.Fatalf("%s: %+v", action, got)
		}
	}
}

func TestVerifySelf(t *testing.T) {
	image := "registry/operator@sha256:" + strings.Repeat("a", 64)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "self", Namespace: "executor"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: image}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", ImageID: "containerd://different-platform-digest"}}}}
	cases := []struct {
		name     string
		image    string
		protocol int32
		identity SelfIdentity
		missing  bool
		reason   string
	}{
		{"spec match", image, 1, SelfIdentity{"self", "executor"}, false, "Enrolled"},
		{"spec mismatch", image + "b", 1, SelfIdentity{"self", "executor"}, false, "ImageMismatch"},
		{"future protocol", image, 2, SelfIdentity{"self", "executor"}, false, "ProtocolUnsupported"},
		{"missing env", image, 1, SelfIdentity{}, false, "NotObserved"},
		{"missing pod", image, 1, SelfIdentity{"self", "executor"}, true, "NotObserved"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(scheme)
			if !tt.missing {
				b = b.WithObjects(pod)
			}
			enrollment := &api.ExecutorEnrollment{Spec: api.ExecutorEnrollmentSpec{ExecutorImage: tt.image, ProtocolVersion: tt.protocol}, Status: api.ExecutorEnrollmentStatus{Capability: &api.ExecutorCapability{ProtocolVersion: 1, ExecutableActions: []api.OperationAction{api.ActionUpgrade}}}}
			got := VerifySelf(context.Background(), b.Build(), enrollment, tt.identity)
			if got.Reason != tt.reason || got.Ready != (tt.reason == "Enrolled") {
				t.Fatalf("%+v", got)
			}
			if !got.Ready && got.Capability != nil {
				t.Fatal("capability not cleared")
			}
			if got.Ready && (got.ObservedImage != image || got.ObservedImageID != pod.Status.ContainerStatuses[0].ImageID || got.Capability == nil || got.Capability.ProtocolVersion != 1 || got.Capability.ExecutableActions == nil || len(got.Capability.ExecutableActions) != 0) {
				t.Fatalf("%+v", got)
			}
		})
	}
	empty := pod.DeepCopy()
	empty.Status.ContainerStatuses = nil
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(empty).Build()
	if got := VerifySelf(context.Background(), c, &api.ExecutorEnrollment{Spec: api.ExecutorEnrollmentSpec{ExecutorImage: image, ProtocolVersion: 1}}, SelfIdentity{"self", "executor"}); got.Reason != "NotObserved" {
		t.Fatalf("empty runtime status: %+v", got)
	}
}

func TestExecutableActionsNeedARolledFoundation(t *testing.T) {
	if len(executable) == 0 {
		if got := ExecutableActions(); got == nil || len(got) != 0 {
			t.Fatalf("empty registry capability=%v", got)
		}
		return
	}
	if err := requireRolledFoundation(executable, os.Getenv("DC_BASELINE_TAG"), os.Getenv("DC_UPGRADE_TAG"), gitOutput); err != nil {
		t.Fatal(err)
	}
}

func TestRolledFoundationResolutionFailures(t *testing.T) {
	registry := Registry{api.ActionUpgrade: nil}
	for _, baseline := range []string{"", "v1.8.2"} {
		if err := requireRolledFoundation(registry, baseline, "v1.9.0", func(...string) ([]byte, error) { return nil, fmtError("no git") }); err == nil {
			t.Fatal("accepted unavailable baseline")
		}
	}
	if err := requireRolledFoundation(registry, "", "v1.0.0", func(...string) ([]byte, error) { return []byte("v1.0.0\nv1.1.0\nv0.9.0-rc.1"), nil }); err == nil {
		t.Fatal("accepted missing earlier stable tag")
	}
	if err := requireRolledFoundation(Registry{}, "", "", func(...string) ([]byte, error) { t.Fatal("empty registry consulted git"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
}

type fmtError string

func (e fmtError) Error() string { return string(e) }
