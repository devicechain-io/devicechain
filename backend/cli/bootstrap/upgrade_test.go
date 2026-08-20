// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	dck8s "github.com/devicechain-io/dc-k8s/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestResolveOperatorImageSourceRefusesAnUnpublishedVersion pins the refusal that
// keeps a failed upgrade from looking like a successful one.
//
// A locally built dcctl's DefaultImageVersion is "dev", which names no image in
// any registry. Applying it sets the Deployment's image to something unpullable:
// the apply succeeds, the rollout never completes, and the cluster stays on the
// old controller. Refusing before the apply is what turns that into an error an
// operator can read.
func TestResolveOperatorImageSourceRefusesAnUnpublishedVersion(t *testing.T) {
	for _, version := range []string{"dev", "0.0.1-dev.20260716T155833Z"} {
		if _, _, err := resolveOperatorImageSource(Options{ImageVersion: version}); err == nil {
			t.Fatalf("version %q was accepted; it names no published image", version)
		}
	}
}

// TestResolveOperatorImageSourceDefaultsToThePublishedRegistry checks the two
// halves settle independently: a caller may name a registry without a version, or
// a version without a registry, and the other half must fall back rather than
// being left empty (which would render "…//operator:" or "/operator:tag").
func TestResolveOperatorImageSourceDefaultsToThePublishedRegistry(t *testing.T) {
	tests := []struct {
		name             string
		opts             Options
		registry, verson string
	}{
		{"version only", Options{ImageVersion: "v1.3.0"}, DefaultImageRegistry, "v1.3.0"},
		{"both explicit", Options{ImageRegistry: "localhost:5000", ImageVersion: "my-build"}, "localhost:5000", "my-build"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, ver, err := resolveOperatorImageSource(tc.opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reg != tc.registry || ver != tc.verson {
				t.Fatalf("got %s:%s, want %s:%s", reg, ver, tc.registry, tc.verson)
			}
		})
	}
}

// TestResolveOperatorImageSourceAppliesTheBuildsPinnedVersion covers the default
// no caller passes: the version this dcctl was stamped with.
//
// DefaultImageVersion is swapped rather than read, because its value depends on
// how the binary under test was BUILT — "dev" for `go test`, a real tag under
// goreleaser. A test that read it would assert something different in CI than on
// a developer's machine, and the branch it is here to cover would go unmeasured
// in exactly one of them.
func TestResolveOperatorImageSourceAppliesTheBuildsPinnedVersion(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })

	DefaultImageVersion = "v1.2.3"
	reg, ver, err := resolveOperatorImageSource(Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg != DefaultImageRegistry || ver != "v1.2.3" {
		t.Fatalf("got %s:%s, want %s:v1.2.3", reg, ver, DefaultImageRegistry)
	}
}

// TestResolveOperatorImageSourceRefusesAnEmptyVersion covers the hole
// IsUnpublishedImageVersion does not: "" is neither "dev" nor a dev stamp, so it
// passes that predicate and renders "…/operator:" — a reference with no tag,
// which Kubernetes resolves as :latest. That is the worst of the three outcomes,
// because it PULLS: the controller would silently run whatever :latest points at
// rather than failing where someone would see it.
func TestResolveOperatorImageSourceRefusesAnEmptyVersion(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })

	DefaultImageVersion = ""
	if _, _, err := resolveOperatorImageSource(Options{}); err == nil {
		t.Fatal("an empty version was accepted; it renders an untagged image reference")
	}
}

// TestRenderedOperatorCarriesTheRequestedImage is the assertion that `dcctl
// upgrade` moves anything at all.
//
// The overlay ships a placeholder image (controller:latest) that kustomize
// rewrites. If that rewrite ever stopped happening, every upgrade would apply the
// placeholder: the command would report the version it was asked for, the apply
// would succeed, and the controller would be pinned to a tag that has nothing to
// do with the release. Nothing else in this package would notice.
func TestRenderedOperatorCarriesTheRequestedImage(t *testing.T) {
	const want = "ghcr.io/devicechain-io/operator:v9.9.9"

	manifests, err := dck8s.RenderOperator(want)
	if err != nil {
		t.Fatalf("rendering the operator overlay: %v", err)
	}
	refs, err := operatorDeployments(manifests)
	if err != nil {
		t.Fatalf("reading the rendered manifests: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("the operator overlay rendered no Deployment")
	}
	for _, r := range refs {
		if r.namespace == "" || r.name == "" {
			t.Fatalf("rendered Deployment is not fully named: %+v", r)
		}
	}
	if !strings.Contains(string(manifests), want) {
		t.Fatalf("the rendered stream does not carry %q — the image rewrite did not happen", want)
	}
}

// TestOperatorDeploymentsRefusesANamespacelessDeployment covers the branch that
// stops a controller being written into whatever namespace the caller's kube
// context happens to name.
func TestOperatorDeploymentsRefusesANamespacelessDeployment(t *testing.T) {
	const stream = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-k8s-controller-manager
`
	if _, err := operatorDeployments([]byte(stream)); err == nil {
		t.Fatal("a Deployment with no namespace was accepted")
	}
}

// deployment builds a Deployment in a given rollout state. `generation` and
// `observed` are separate so a controller that has not yet seen a new template
// can be expressed.
func deployment(generation, observed int64, desired, updated, replicas, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-k8s-controller-manager", Namespace: "dc-k8s-system", Generation: generation},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: observed,
			UpdatedReplicas:    updated,
			Replicas:           replicas,
			AvailableReplicas:  available,
		},
	}
}

var operatorTarget = []deploymentRef{{namespace: "dc-k8s-system", name: "dc-k8s-controller-manager"}}

// TestWaitForRolloutRejectsStatesTheNaiveCheckAccepts is the point of the whole
// helper, so each case is chosen to be one the OBVIOUS wait would pass.
//
// 🔴 EVERY CASE HERE SATISFIES `AvailableReplicas >= desired`. That is the check
// this helper exists not to be: it describes the OLD pods, it is true from before
// the apply, and a wait built on it returns instantly and always — reporting an
// upgrade that has not happened. A test whose failing states were obviously
// failing would not have measured that.
func TestWaitForRolloutRejectsStatesTheNaiveCheckAccepts(t *testing.T) {
	tests := []struct {
		name string
		dep  *appsv1.Deployment
	}{
		{
			// The controller has not observed the new template yet, so all three
			// counts below are still describing the OLD one and every one of them
			// looks complete. Only the generation comparison can see this.
			name: "the controller has not seen the new template",
			dep:  deployment(2, 1, 1, 1, 1, 1),
		},
		{
			// The old pod is up and the new one has not been created.
			name: "no replica has been recreated yet",
			dep:  deployment(2, 2, 1, 0, 1, 1),
		},
		{
			// The new pod is up and available; the old one has not gone away.
			name: "an old pod is still running",
			dep:  deployment(2, 2, 1, 1, 2, 1),
		},
		{
			// A scale-DOWN the controller has not finished: two pods on the new
			// template, one wanted. Every other condition holds — nothing old is
			// left and both new pods are available — so this state is reachable
			// ONLY through the replica-count comparison.
			//
			// 🔑 It is here because a mutation run found that condition doing no
			// work: the "no replica recreated yet" case above was being killed by
			// `Replicas == UpdatedReplicas` instead, so deleting the comparison
			// this case exists for broke no test at all.
			name: "a scale-down has not finished",
			dep:  deployment(2, 2, 1, 2, 2, 2),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Assert the premise rather than trusting it: if a case stopped
			// satisfying the naive check, it would still fail correctly here while
			// silently no longer testing anything.
			s, d := tc.dep.Status, *tc.dep.Spec.Replicas
			if s.AvailableReplicas < d {
				t.Fatalf("this case no longer satisfies the naive check (available %d < desired %d), so it does not measure what it claims", s.AvailableReplicas, d)
			}
			client := fake.NewSimpleClientset(tc.dep)
			err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond)
			if err == nil {
				t.Fatal("reported the rollout complete; the operator had not rolled over")
			}
		})
	}
}

// TestWaitForRolloutWaitsForAnUnavailableNewPod covers the fourth condition,
// which is kept out of the table above because it does NOT satisfy the naive
// check — it is the one failing state both readings agree on, and the table's own
// premise assertion refuses it, correctly.
//
// It is still worth its own case: this is where a crash-looping controller sits,
// and it is exactly the state an unpullable image produces. The recreate has
// happened and the old pod is gone, so three of the four conditions hold.
func TestWaitForRolloutWaitsForAnUnavailableNewPod(t *testing.T) {
	client := fake.NewSimpleClientset(deployment(2, 2, 1, 1, 1, 0))
	if err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond); err == nil {
		t.Fatal("reported the rollout complete while the new pod was not available")
	}
}

// TestWaitForRolloutAcceptsACompletedRollout is the counterweight. A wait that
// refuses everything would pass every case above and be useless.
func TestWaitForRolloutAcceptsACompletedRollout(t *testing.T) {
	client := fake.NewSimpleClientset(deployment(2, 2, 1, 1, 1, 1))
	if err := waitForRollout(context.Background(), client, operatorTarget, 50*time.Millisecond); err != nil {
		t.Fatalf("a completed rollout was refused: %v", err)
	}
}

// TestWaitForRolloutFailsOnAMissingDeployment: a target that is not there is not
// a rollout that finished. Without this branch the Get error would be dropped and
// an absent controller would read as an upgraded one.
func TestWaitForRolloutFailsOnAMissingDeployment(t *testing.T) {
	if err := waitForRollout(context.Background(), fake.NewSimpleClientset(), operatorTarget, 50*time.Millisecond); err == nil {
		t.Fatal("an absent Deployment was reported as a completed rollout")
	}
}

// TestCurrentOperatorImagesReportsWhatIsRunning covers the before/after line. An
// unreadable target is deliberately absent from the map rather than an error —
// this is reporting, and failing to read it must not fail an upgrade.
func TestCurrentOperatorImagesReportsWhatIsRunning(t *testing.T) {
	dep := deployment(1, 1, 1, 1, 1, 1)
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager", Image: "ghcr.io/devicechain-io/operator:v0.11.0"}}

	got := currentOperatorImages(context.Background(), fake.NewSimpleClientset(dep), operatorTarget)
	if got["dc-k8s-system/dc-k8s-controller-manager"] != "ghcr.io/devicechain-io/operator:v0.11.0" {
		t.Fatalf("got %q", got["dc-k8s-system/dc-k8s-controller-manager"])
	}
	if len(currentOperatorImages(context.Background(), fake.NewSimpleClientset(), operatorTarget)) != 0 {
		t.Fatal("an unreadable target should be absent, not reported")
	}
}
