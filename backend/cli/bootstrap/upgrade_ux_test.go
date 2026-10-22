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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
		{"a build keeps its tag even pushed to the published registry", DefaultImageRegistry, "0.23.0", true, "0.23.0"},
		{"a non-semver tag is left alone", "", "main-abc123", false, "main-abc123"},
		{"latest is left alone", "", "latest", false, "latest"},
		{"a partial version is left alone", "", "0.23", false, "0.23"},
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

// rollingDeployment is an unfinished rollout at revision 2, with a ReplicaSet per
// revision so the wait can tell the current revision's pods from the previous one's.
func rollingDeployment() (*appsv1.Deployment, []runtime.Object) {
	d := deployment(2, 2, 1, 1, 1, 0)
	d.UID = types.UID("dep-uid")
	d.Annotations = map[string]string{revisionAnnotation: "2"}
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "op"}}
	ctrl := true
	rs := func(rev, hash string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: d.Name + "-" + hash, Namespace: d.Namespace,
			Labels:      map[string]string{"app": "op", appsv1.DefaultDeploymentUniqueLabelKey: hash},
			Annotations: map[string]string{revisionAnnotation: rev},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment",
				Name: d.Name, UID: d.UID, Controller: &ctrl}},
		}}
	}
	// The previous revision's ReplicaSet sorts FIRST, so picking the current one has to
	// be done by its revision rather than by list order.
	return d, []runtime.Object{d, rs("1", "a-old"), rs("2", "b-new")}
}

func podFor(d *appsv1.Deployment, hash, reason, message, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-" + hash, Namespace: d.Namespace, UID: types.UID("uid-" + hash),
			Labels: map[string]string{"app": "op", appsv1.DefaultDeploymentUniqueLabelKey: hash}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "manager", Image: image,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
		}}},
	}
}

// An image that cannot exist used to cost the whole five-minute wait and end in "did
// not finish" with no cause. It must now fail at once and name the image.
func TestWaitForRolloutFailsFastOnAnImageThatWillNeverPull(t *testing.T) {
	const img = "ghcr.io/devicechain-io/dc-k8s:0.23.0"
	for _, tc := range []struct{ reason, message string }{
		{"InvalidImageName", `couldn't parse image name "ghcr.io//x"`},
		{"ErrImagePull", `failed to resolve reference "` + img + `": ` + img + `: not found`},
		{"ImagePullBackOff", `Back-off pulling image "` + img + `": ErrImagePull: manifest unknown`},
		{"ErrImagePull", `failed to authorize: 401 Unauthorized`},
	} {
		t.Run(tc.reason+"/"+tc.message, func(t *testing.T) {
			d, objs := rollingDeployment()
			client := fake.NewSimpleClientset(append(objs, podFor(d, "b-new", tc.reason, tc.message, img))...)
			start := time.Now()
			err := waitForRollout(context.Background(), client, operatorTarget, time.Minute)
			if err == nil {
				t.Fatal("reported success over an unpullable image")
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("waited %s instead of failing fast", time.Since(start))
			}
			if !strings.Contains(err.Error(), img) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error does not name the image and reason: %v", err)
			}
		})
	}
}

// A pull failure that does not say the image is missing — a timeout on a slow link, a
// rate limit, a registry blip — is retried by the kubelet and may yet succeed, so it
// is waited for. The timeout still names it as the likely cause.
func TestWaitForRolloutWaitsOutATransientPullFailure(t *testing.T) {
	for _, tc := range []struct{ reason, message string }{
		{"ErrImagePull", "rpc error: code = Unknown desc = context deadline exceeded"},
		{"ErrImagePull", "toomanyrequests: You have reached your pull rate limit"},
		{"ImagePullBackOff", `Back-off pulling image "img"`},
	} {
		t.Run(tc.message, func(t *testing.T) {
			d, objs := rollingDeployment()
			client := fake.NewSimpleClientset(append(objs, podFor(d, "b-new", tc.reason, tc.message, "img"))...)
			err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "did not finish") {
				t.Fatalf("want the ordinary timeout, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("the timeout does not name the pull failure it saw: %v", err)
			}
		})
	}
}

// ...but not for ever: one that persists past the grace period is given up on.
func TestWaitForRolloutGivesUpOnAPullFailureThatPersists(t *testing.T) {
	orig := imagePullGrace
	t.Cleanup(func() { imagePullGrace = orig })
	imagePullGrace = 0
	d, objs := rollingDeployment()
	client := fake.NewSimpleClientset(append(objs, podFor(d, "b-new", "ImagePullBackOff", `Back-off pulling image "img"`, "img"))...)
	err := waitForRollout(context.Background(), client, operatorTarget, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "cannot be pulled") {
		t.Fatalf("want the pull failure, got %v", err)
	}
}

// A re-run that corrects a bad --version starts a new revision while the previous
// attempt's pod is still in ImagePullBackOff. That pod is not this rollout's.
func TestWaitForRolloutIgnoresAPreviousRevisionsPullFailure(t *testing.T) {
	d, objs := rollingDeployment()
	client := fake.NewSimpleClientset(append(objs,
		podFor(d, "a-old", "ErrImagePull", "x:0.23.0: not found", "x:0.23.0"),
		podFor(d, "b-new", "ContainerCreating", "", "x:v0.23.0"))...)
	err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("want the ordinary timeout, got %v", err)
	}
}

// Before the controller has observed the new template there is no current revision to
// judge, and the pods it can see are the old template's.
func TestWaitForRolloutJudgesNoPodBeforeTheControllerObservesTheTemplate(t *testing.T) {
	d, objs := rollingDeployment()
	d.Generation = 3
	client := fake.NewSimpleClientset(append(objs, podFor(d, "b-new", "ErrImagePull", "not found", "img"))...)
	err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("want the ordinary timeout, got %v", err)
	}
}

// Counterweight: a pod waiting for an ordinary reason is still just waited for — even
// with no grace at all, so only the waiting reason can be what spares it.
func TestWaitForRolloutDoesNotFailFastOnOtherWaitingReasons(t *testing.T) {
	orig := imagePullGrace
	t.Cleanup(func() { imagePullGrace = orig })
	imagePullGrace = 0
	d, objs := rollingDeployment()
	client := fake.NewSimpleClientset(append(objs, podFor(d, "b-new", "ContainerCreating", "", "img"))...)
	err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("want the ordinary timeout, got %v", err)
	}
}

func TestReportInstallOnAClusterWithInstancesPrintsTheUpgradeNextStep(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "alpha", Provider: "local", KubeContext: "prod-eu", CreatedAt: time.Now()})
	writeRecord(t, InstanceRecord{Instance: "beta", Provider: "remote", KubeContext: "prod-eu", ClusterUID: "u1", CreatedAt: time.Now()})
	writeRecord(t, InstanceRecord{Instance: "rebuilt", Provider: "local", KubeContext: "prod-eu", ClusterUID: "u0", CreatedAt: time.Now()})
	writeRecord(t, InstanceRecord{Instance: "other", Provider: "local", KubeContext: "elsewhere", CreatedAt: time.Now()})
	st := &State{KubeContext: "prod-eu", ClusterUID: "u1", ImageRegistry: "localhost:5000", ImageVersion: "0.24.0-drill.1",
		Binding: ClusterBinding{KubeContext: "prod-eu"}}
	out := captureOutput(t, func() { reportInstall(st, "local") })
	for _, want := range []string{
		"2 instances (alpha, beta)",
		"dcctl upgrade local alpha --kube-context prod-eu --registry localhost:5000 --version 0.24.0-drill.1 --dry-run",
		"dcctl upgrade local alpha --kube-context prod-eu --registry localhost:5000 --version 0.24.0-drill.1\n",
		"dcctl upgrade remote beta --kube-context prod-eu --registry localhost:5000 --version 0.24.0-drill.1 --dry-run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Next: build an instance") || strings.Contains(out, "other") || strings.Contains(out, "rebuilt") {
		t.Errorf("wrong next step or an instance from another cluster:\n%s", out)
	}
}

// The published registry and version are named too: an upgrade takes what it is not
// told from the instance's declaration, so a flagless command would move nothing.
func TestReportInstallNamesThePublishedReleaseAndSkipsATeardown(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "alpha", Provider: "local", KubeContext: "kind-devicechain", CreatedAt: time.Now()})
	writeRecord(t, InstanceRecord{Instance: "going", Provider: "local", KubeContext: "kind-devicechain", CreatedAt: time.Now()})
	if err := writeDestroyMarker("going"); err != nil {
		t.Fatal(err)
	}
	st := &State{KubeContext: "kind-devicechain", ImageRegistry: DefaultImageRegistry, ImageVersion: "v0.23.0",
		Binding: ClusterBinding{KubeContext: "kind-devicechain", Managed: true}}
	out := captureOutput(t, func() { reportInstall(st, "local") })
	if want := "dcctl upgrade local alpha --registry " + DefaultImageRegistry + " --version v0.23.0 --dry-run"; !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if strings.Contains(out, "--kube-context") || strings.Contains(out, "going") {
		t.Errorf("a created cluster's context, or an instance being torn down, was named:\n%s", out)
	}
}

func TestReportInstallOnAFreshClusterStillPointsAtBootstrap(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "other", Provider: "local", KubeContext: "elsewhere", CreatedAt: time.Now()})
	st := &State{KubeContext: "kind-devicechain", Binding: ClusterBinding{KubeContext: "kind-devicechain", Managed: true}}
	out := captureOutput(t, func() { reportInstall(st, "local") })
	if !strings.Contains(out, "Next: build an instance") || strings.Contains(out, "dcctl upgrade") {
		t.Errorf("unexpected next step:\n%s", out)
	}
}

// A pull that recovers — pulling again, or started — starts the grace clock over if it
// fails again: the kubelet's retries succeeding in between are not one long failure.
func TestPullWatchRestartsTheClockForAPullThatRecovered(t *testing.T) {
	d, objs := rollingDeployment()
	pod := podFor(d, "b-new", "ErrImagePull", "i/o timeout", "img")
	client := fake.NewSimpleClientset(append(objs, pod)...)
	setReason := func(reason string) {
		pod.Status.ContainerStatuses[0].State.Waiting.Reason = reason
		if _, err := client.CoreV1().Pods(d.Namespace).UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	w := newPullWatch(90 * time.Second)
	t0 := time.Now()
	if err := w.check(context.Background(), client, d, t0); err != nil {
		t.Fatalf("a first transient failure was given up on: %v", err)
	}
	setReason("ContainerCreating")
	if err := w.check(context.Background(), client, d, t0.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	setReason("ErrImagePull")
	if err := w.check(context.Background(), client, d, t0.Add(100*time.Second)); err != nil {
		t.Fatalf("a failure 40s after recovering was counted from the first one: %v", err)
	}
	if err := w.check(context.Background(), client, d, t0.Add(200*time.Second)); err == nil {
		t.Fatal("a failure persisting past the grace period was not given up on")
	}
}
