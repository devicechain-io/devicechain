// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The version rule on its own: the next minor is allowed, a skipped one is not. A downgrade and
// an unversioned build are not this check's business.
func TestSkippedMinorRule(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		refused  bool
	}{
		{"v1.8.0", "v1.8.0", false},      // a re-run
		{"v1.8.0", "v1.8.3", false},      // a patch
		{"v1.8.2", "v1.9.0", false},      // the next minor
		{"v1.8.2", "v1.9.0-rc.1", false}, // its release candidate
		{"v1.9.0-rc.2", "v1.9.0", false}, // rc to final
		{"v1.8.0", "v1.10.0", true},      // a skipped minor
		{"v1.8.0", "v1.10.0-rc.1", true}, // a skipped minor's candidate
		{"v1.7.4", "v1.10.0", true},      // two skipped
		{"v1.9.0", "v2.0.0", false},      // a major boundary
		{"v1.9.0", "v2.1.0", true},       // past the major's first minor
		{"v1.9.0", "v3.0.0", true},       // a skipped major
		{"v1.9.0", "v1.8.0", false},      // a downgrade: governed elsewhere
		{"v2.0.0", "v1.9.0", false},      // a major downgrade: governed elsewhere
		{"dev", "v1.10.0", false},        // an unversioned running build
		{"v1.8.0", "dev", false},         // an unversioned target
		{"0.0.1-local", "v1.10.0", true}, // a semver-shaped local tag is still a version
	} {
		err := checkSkippedMinor(tc.from, tc.to, false)
		if (err != nil) != tc.refused {
			t.Errorf("%s -> %s: refused=%v (%v), want refused=%v", tc.from, tc.to, err != nil, err, tc.refused)
		}
		if err != nil && (!strings.Contains(err.Error(), "--allow-skipped-minor") || !strings.Contains(err.Error(), tc.from)) {
			t.Errorf("%s -> %s: the refusal must name the running version and the override: %v", tc.from, tc.to, err)
		}
	}
}

// The override allows the move and says so.
func TestSkippedMinorOverride(t *testing.T) {
	if err := checkSkippedMinor("v1.8.0", "v1.10.0", true); err != nil {
		t.Fatalf("--allow-skipped-minor must allow the move: %v", err)
	}
}

// The refusal names the minor to upgrade to first.
func TestSkippedMinorRefusalNamesTheNextMinor(t *testing.T) {
	err := checkSkippedMinor("v1.8.2", "v1.10.0", false)
	if err == nil || !strings.Contains(err.Error(), "v1.9") {
		t.Fatalf("the refusal must say to upgrade to v1.9 first: %v", err)
	}
}

func skipAreaPod(name, area, image string, phase corev1.PodPhase, extra ...corev1.Container) *corev1.Pod {
	containers := append(append([]corev1.Container{}, extra...), corev1.Container{Name: area, Image: image})
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: InstanceNamespace("i"), Labels: map[string]string{areaLabel: area}},
		Spec:       corev1.PodSpec{Containers: containers},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// The version judged is the one the PODS run, never a Deployment template and never the
// declaration: after a rollout that stopped half-way the templates can all name v1.9 while an old
// ReplicaSet's pod still runs v1.8. The oldest running pod is the version the instance has
// completed; a sidecar listed first does not stand in for the area's own container.
func TestCompletedVersionIsTheOldestRunningAreaPod(t *testing.T) {
	sidecar := corev1.Container{Name: "proxy", Image: "example/proxy:dev"}
	typed := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "dc-event-sources", Namespace: InstanceNamespace("i"),
			Labels: map[string]string{areaLabel: "event-sources"}},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "event-sources", Image: "ghcr.io/devicechain-io/event-sources:v1.9.0"}}}}}},
		skipAreaPod("dm-new", "device-management", "ghcr.io/devicechain-io/device-management:v1.9.0", corev1.PodRunning, sidecar),
		skipAreaPod("es-old", "event-sources", "ghcr.io/devicechain-io/event-sources:v1.8.4", corev1.PodRunning, sidecar),
		skipAreaPod("es-done", "event-sources", "ghcr.io/devicechain-io/event-sources:v1.7.0", corev1.PodSucceeded),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nats-0", Namespace: InstanceNamespace("i")},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "nats", Image: "nats:2.10"}}}},
	)
	got, err := completedVersion(context.Background(), typed, "i")
	if err != nil || got != "v1.8.4" {
		t.Fatalf("completed version = %q, %v; want v1.8.4 (the oldest unfinished area pod's own container)", got, err)
	}
}

// A pod whose area container cannot be found refuses rather than reading another container.
func TestCompletedVersionNeedsTheAreaContainer(t *testing.T) {
	p := skipAreaPod("dm", "device-management", "ghcr.io/devicechain-io/device-management:v1.8.0", corev1.PodRunning)
	p.Spec.Containers[0].Name = "renamed"
	if _, err := completedVersion(context.Background(), fake.NewSimpleClientset(p), "i"); err == nil {
		t.Fatal("a pod without its area's container must make the running version unreadable")
	}
}

// Nothing to read is not "nothing to refuse": an instance whose running version cannot be read
// is refused, naming the override.
func TestCompletedVersionUnreadableRefuses(t *testing.T) {
	st := &State{Instance: "i", ImageVersion: "v1.10.0"}
	err := precheckSkippedMinorWith(context.Background(), st, false, func(context.Context) (string, error) {
		return "", errors.New("no area deployment runs a pod")
	})
	if err == nil || !strings.Contains(err.Error(), "--allow-skipped-minor") {
		t.Fatalf("an unreadable running version must refuse and name the override: %v", err)
	}
	if err := precheckSkippedMinorWith(context.Background(), st, true, func(context.Context) (string, error) {
		return "", errors.New("unreadable")
	}); err != nil {
		t.Fatalf("the override must also cover an unreadable version: %v", err)
	}
}

// A resumable upgrade: the declaration already names the target but the rollout stopped half-way.
// It is judged from what runs, so finishing v1.8 -> v1.9 is allowed and v1.8 -> v1.10 is not,
// whatever the declaration says.
func TestSkippedMinorJudgesRunningNotDeclared(t *testing.T) {
	running := func(context.Context) (string, error) { return "v1.8.1", nil }
	st := &State{Instance: "i", FromImageVersion: "v1.9.0", ImageVersion: "v1.9.0"}
	if err := precheckSkippedMinorWith(context.Background(), st, false, running); err != nil {
		t.Fatalf("resuming v1.8 -> v1.9 must be allowed: %v", err)
	}
	st = &State{Instance: "i", FromImageVersion: "v1.9.0", ImageVersion: "v1.10.0"}
	if err := precheckSkippedMinorWith(context.Background(), st, false, running); err == nil {
		t.Fatal("a declaration of v1.9 over workloads still on v1.8 must not license v1.10")
	}
}

// A listing failure refuses rather than reading as "nothing runs".
func TestCompletedVersionListFailure(t *testing.T) {
	typed := fake.NewSimpleClientset()
	if _, err := completedVersion(context.Background(), typed, "i"); err == nil {
		t.Fatal("an instance with no running area pod has no readable running version")
	}
}

// Upgrade itself is driven by no unit test (it needs a cluster), so the call is pinned here: the
// refusal must come before the declaration is written, and it must be passed the override.
func TestSkippedMinorIsCheckedBeforeTheDeclarationIsWritten(t *testing.T) {
	raw, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	check := strings.Index(src, "precheckSkippedMinor(ctx, typed, st, opts.AllowSkippedMinor)")
	write := strings.Index(src, "recordUpgradedVersion(ctx, dyn, opts.Instance, st)")
	if check < 0 || write < 0 || check > write {
		t.Fatalf("Upgrade must call precheckSkippedMinor (at %d) before recordUpgradedVersion (at %d)", check, write)
	}
}
