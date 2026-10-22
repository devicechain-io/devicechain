// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A release number written without the "v" asks the published registry for a tag that
// does not exist (published tags are v0.23.0). It is normalized there, and ONLY there:
// the upgrade drill pushes 0.24.0-drill.1 to a local registry with no v.
func TestResolveImageSourcePrefixesABareReleaseOnThePublishedRegistryOnly(t *testing.T) {
	for _, tc := range []struct {
		name, registry, version string
		build                   bool
		want                    string
	}{
		{"bare release, published", "", "0.23.0", false, "v0.23.0"},
		{"bare prerelease, published", "", "0.24.0-rc.1", false, "v0.24.0-rc.1"},
		{"already prefixed", "", "v0.23.0", false, "v0.23.0"},
		{"explicit published registry", DefaultImageRegistry, "0.23.0", false, "v0.23.0"},
		{"local registry keeps the tag it was given", "localhost:5000", "0.24.0-drill.1", false, "0.24.0-drill.1"},
		{"a build keeps the tag it was given", "", "0.23.0", true, "0.23.0"},
		{"a non-semver tag is left alone", "", "main-abc123", false, "main-abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveImageSource(tc.registry, tc.version, tc.build)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tc.want {
				t.Errorf("version = %q, want %q", got.Version, tc.want)
			}
		})
	}
}

func podFor(d *appsv1.Deployment, reason, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: d.Namespace, Labels: map[string]string{"app": "op"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "manager", Image: image,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "not found"}},
		}}},
	}
}

// An unpullable operator image used to cost the whole five-minute wait and end in
// "did not finish" with no cause. It must now fail at once and name the image.
func TestWaitForRolloutFailsFastOnImagePullBackOff(t *testing.T) {
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName"} {
		t.Run(reason, func(t *testing.T) {
			d := deployment(2, 2, 1, 1, 1, 0)
			d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "op"}}
			client := fake.NewSimpleClientset(d, podFor(d, reason, "ghcr.io/devicechain-io/dc-k8s:0.23.0"))
			start := time.Now()
			err := waitForRollout(context.Background(), client, operatorTarget, time.Minute)
			if err == nil {
				t.Fatal("reported success over an unpullable image")
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("waited %s instead of failing fast", time.Since(start))
			}
			if !strings.Contains(err.Error(), "ghcr.io/devicechain-io/dc-k8s:0.23.0") || !strings.Contains(err.Error(), reason) {
				t.Errorf("error does not name the image and reason: %v", err)
			}
		})
	}
}

// Counterweight: a pod waiting for an ordinary reason is still just waited for.
func TestWaitForRolloutDoesNotFailFastOnOtherWaitingReasons(t *testing.T) {
	d := deployment(2, 2, 1, 1, 1, 0)
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "op"}}
	client := fake.NewSimpleClientset(d, podFor(d, "ContainerCreating", "img"))
	err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("want the ordinary timeout, got %v", err)
	}
}

func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestReportInstallOnAClusterWithInstancesPrintsTheUpgradeNextStep(t *testing.T) {
	isolateHome(t)
	writeRecord(t, InstanceRecord{Instance: "alpha", Provider: "local", KubeContext: "prod-eu", CreatedAt: time.Now()})
	writeRecord(t, InstanceRecord{Instance: "other", Provider: "local", KubeContext: "elsewhere", CreatedAt: time.Now()})
	st := &State{KubeContext: "prod-eu", ImageRegistry: "localhost:5000",
		Binding: ClusterBinding{KubeContext: "prod-eu"}}
	out := captureOutput(t, func() { reportInstall(st, "local") })
	for _, want := range []string{
		"dcctl upgrade alpha --dry-run --kube-context prod-eu --registry localhost:5000",
		"dcctl upgrade alpha --kube-context prod-eu --registry localhost:5000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Next: build an instance") || strings.Contains(out, "other") {
		t.Errorf("wrong next step or an instance from another cluster:\n%s", out)
	}
}

func TestReportInstallOnAFreshClusterStillPointsAtBootstrap(t *testing.T) {
	isolateHome(t)
	writeRecord(t, InstanceRecord{Instance: "other", Provider: "local", KubeContext: "elsewhere", CreatedAt: time.Now()})
	st := &State{KubeContext: "kind-devicechain", Binding: ClusterBinding{KubeContext: "kind-devicechain", Managed: true}}
	out := captureOutput(t, func() { reportInstall(st, "local") })
	if !strings.Contains(out, "Next: build an instance") || strings.Contains(out, "dcctl upgrade") {
		t.Errorf("unexpected next step:\n%s", out)
	}
}
