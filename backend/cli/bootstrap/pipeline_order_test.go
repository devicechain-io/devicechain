// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stepIndex reports where each named step function sits in the default pipeline.
// Comparing function pointers rather than step NAMES is deliberate: a rename
// would otherwise silently turn every ordering assertion below into a lookup
// that finds nothing, and a test that cannot find what it is measuring reports
// the same "not out of order" as a correct pipeline.
func stepIndex(t *testing.T, want func(context.Context, *State) error) int {
	t.Helper()
	target := reflect.ValueOf(want).Pointer()
	for i, s := range NewDefaultPipeline().Steps {
		if reflect.ValueOf(s.Run).Pointer() == target {
			return i
		}
	}
	t.Fatalf("step not present in the default pipeline")
	return -1
}

// 🔴 THE THREE EDGES THAT MAKE THE ADR-080 ORDER CORRECT, each asserted for the
// reason it exists rather than as one "the list has not changed" snapshot. A
// snapshot test fails on every reordering equally, including the ones that are
// fine, so it teaches the next person to re-record the expected list rather than
// to ask which edge they broke.
func TestPipelineOrderInvariants(t *testing.T) {
	registry := stepIndex(t, stepLocalRegistry)
	core := stepIndex(t, stepInstallCore)
	render := stepIndex(t, stepRenderConfig)
	infra := stepIndex(t, stepInfraApply)

	// The operator Deployment names an image that, on the --build path, does not
	// exist until stepLocalRegistry has pushed it.
	if registry >= core {
		t.Errorf("stepLocalRegistry runs at %d and stepInstallCore at %d: a --build bootstrap "+
			"would install the operator against an image nothing has pushed yet", registry, core)
	}
	// The Instance CRD has to exist before anything declares an instance. Nothing
	// in dcctl writes an Instance CR YET — the claim step is a later slice — so
	// this edge is pinned ahead of the code that needs it, deliberately: it is
	// cheap now and it is the whole reason the operator install moved.
	if core >= render {
		t.Errorf("stepInstallCore runs at %d and stepRenderConfig at %d: the Instance CRD would "+
			"not exist when the instance is claimed", core, render)
	}
	// Unchanged from before ADR-080, and pinned separately in broker_record_test.go
	// with the incident it comes from. Repeated here so a future reorder that
	// satisfies the two edges above cannot quietly break this one.
	if render >= infra {
		t.Errorf("stepRenderConfig runs at %d and stepInfraApply at %d: the broker credentials "+
			"would reach the broker before anything recorded them", render, infra)
	}
}

func TestResolveImageSource(t *testing.T) {
	// 🔴 BOTH ARMS RUN, and that took swapping the variable. DefaultImageVersion is
	// "dev" in every test binary (only goreleaser stamps it), so a subtest that
	// branches on IsUnpublishedImageVersion(DefaultImageVersion) executes one arm
	// and leaves the other as prose. The released build's behaviour is the arm that
	// never ran. upgrade_test.go swaps the variable for the same reason.
	t.Run("an unstamped build refuses the published path", func(t *testing.T) {
		defer func(orig string) { DefaultImageVersion = orig }(DefaultImageVersion)
		DefaultImageVersion = "dev"

		got, err := ResolveImageSource("", "", false)
		if err == nil {
			t.Fatalf("a dcctl with no pinned image version resolved to %+v instead of refusing", got)
		}
		if !strings.Contains(err.Error(), "--version") || !strings.Contains(err.Error(), "--build") {
			t.Errorf("the refusal does not name either way out of it: %v", err)
		}
	})

	// A BROKEN ldflags stamp leaves it empty rather than "dev", and
	// IsUnpublishedImageVersion does not catch "" — it is neither "dev" nor a dev
	// stamp. Unrefused, that reference has no tag at all, which Kubernetes reads
	// as :latest. Only the published path can reach it: --build defaults to the
	// literal "dev" and so is never empty, which the second arm pins.
	t.Run("an empty version is refused", func(t *testing.T) {
		defer func(orig string) { DefaultImageVersion = orig }(DefaultImageVersion)
		DefaultImageVersion = ""

		if got, err := ResolveImageSource("", "", false); err == nil {
			t.Errorf("an empty image version resolved to %+v instead of refusing", got)
		}
		got, err := ResolveImageSource("", "", true)
		if err != nil {
			t.Fatalf("--build does not read DefaultImageVersion and must be unaffected: %v", err)
		}
		if got.Version != "dev" {
			t.Errorf("--build resolved to version %q, so it is reading the broken stamp", got.Version)
		}
	})

	t.Run("a stamped build takes the published defaults", func(t *testing.T) {
		defer func(orig string) { DefaultImageVersion = orig }(DefaultImageVersion)
		DefaultImageVersion = "v0.17.0"

		got, err := ResolveImageSource("", "", false)
		if err != nil {
			t.Fatalf("a build with a pinned image version was refused: %v", err)
		}
		if got.Registry != DefaultImageRegistry || got.Version != "v0.17.0" {
			t.Fatalf("published defaults resolved to %+v", got)
		}
	})

	t.Run("an explicit published tag is accepted", func(t *testing.T) {
		got, err := ResolveImageSource("", "v0.17.0", false)
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		if got.Registry != DefaultImageRegistry || got.Version != "v0.17.0" {
			t.Fatalf("resolved to %+v", got)
		}
		if !strings.Contains(got.Label, "published") {
			t.Errorf("label %q does not say where the images come from", got.Label)
		}
	})

	t.Run("--build takes the local registry and is exempt from the tag check", func(t *testing.T) {
		got, err := ResolveImageSource("", "", true)
		if err != nil {
			t.Fatalf("--build was refused: %v", err)
		}
		if got.Registry != LocalRegistry || got.Version != "dev" {
			t.Fatalf("resolved to %+v", got)
		}
		if !strings.Contains(got.Label, "built from source") {
			t.Errorf("label %q does not say the images are built locally", got.Label)
		}
	})

	t.Run("explicit values pass through untouched", func(t *testing.T) {
		got, err := ResolveImageSource("registry.example/dc", "v1.2.3", false)
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		if got.Registry != "registry.example/dc" || got.Version != "v1.2.3" {
			t.Fatalf("resolved to %+v", got)
		}
	})

	// The command layer resolves this, then stepRenderConfig resolves it again for
	// the label. If the second call could move either value, the images installed by
	// the two steps ahead of render would differ from the ones the chart deploys.
	t.Run("idempotent", func(t *testing.T) {
		first, err := ResolveImageSource("", "", true)
		if err != nil {
			t.Fatal(err)
		}
		second, err := ResolveImageSource(first.Registry, first.Version, true)
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("re-resolving moved the answer: %+v then %+v", first, second)
		}
	})
}

// 🔴 THE FAIL-LOUD HALF. "/operator:" is a syntactically valid image reference
// that pulls nothing, so a step running before the image source is settled would
// not fail here — it would apply a Deployment and fail minutes later as an
// ImagePullBackOff naming an image nobody asked for.
//
// 🔴 DryRun IS SET ON EVERY FIXTURE, for the reason its sibling
// TestInfraApplyConsultsTheNodeGuard states: a test that proves a guard exists by
// watching the unguarded path fail must not let that path DO anything. Without
// it, deleting either guard made this test pass — but by server-side-applying
// "/operator:v0.17.0" into whatever the developer's current kube-context points
// at, and by starting a registry container, taking 30 seconds per subtest to do
// it. Both guards sit before the dry-run branch in their steps, so the assertion
// is unchanged and now costs nothing.
func TestStepsThatDeployImagesRefuseAnUnsettledSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   *State
		run  func(context.Context, *State) error
	}{
		{"install core, no registry", &State{ImageVersion: "v0.17.0", DryRun: true}, stepInstallCore},
		{"install core, no version", &State{ImageRegistry: "registry.example/dc", DryRun: true}, stepInstallCore},
		{"build path, no registry", &State{BuildImages: true, ImageVersion: "dev", DryRun: true}, stepLocalRegistry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t.Context(), tc.st)
			if err == nil {
				t.Fatal("the step accepted an unsettled image source")
			}
			if !strings.Contains(err.Error(), "ResolveImageSource") {
				t.Errorf("the refusal does not say what was skipped: %v", err)
			}
		})
	}
}

// deadKubeconfig points KUBECONFIG at a server nothing is listening on: the
// config builds, the API does not answer. That is the shape of "the cluster this
// dry run targets does not exist yet".
func deadKubeconfig(t *testing.T) {
	t.Helper()
	kubeconfig := filepath.Join(t.TempDir(), "config")
	const cfg = `apiVersion: v1
kind: Config
clusters:
- name: dead
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: dead
  context: {cluster: dead, user: dead}
current-context: dead
users:
- name: dead
  user: {token: x}
`
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
}

// A dry run does not create a cluster, so --ha --dry-run against a machine with
// no cluster reached the node-capacity check, failed to connect, and died with a
// connection error instead of printing the plan.
func TestHaCapacityCheckIsBestEffortOnlyOnADryRun(t *testing.T) {
	deadKubeconfig(t)
	// Each call gets its OWN bounded context. client-go retries against an
	// unreachable endpoint for tens of seconds, so a single shared deadline would
	// be spent by the first call — and the second assertion would then be reading
	// an expired context rather than the branch it names.
	check := func(st *State) error {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		return checkHaNodeCapacity(ctx, st)
	}

	if err := check(&State{HA: true, KubeContext: "dead", DryRun: true}); err != nil {
		t.Errorf("a dry run against an unreachable cluster failed instead of warning: %v", err)
	}

	// The counterweight, and the reason this is not simply "skip it under dry-run":
	// a real run must still refuse to proceed when it cannot tell.
	err := check(&State{HA: true, KubeContext: "dead", DryRun: false})
	if err == nil {
		t.Fatal("a real --ha run proceeded without being able to see the cluster's nodes")
	}
	if !strings.Contains(err.Error(), "--ha topology") {
		t.Errorf("the refusal does not say what it could not verify: %v", err)
	}

	// And without --ha there is nothing to check on either path, so neither the
	// warning nor the failure may appear.
	if err := check(&State{HA: false, KubeContext: "dead"}); err != nil {
		t.Errorf("a run with no --ha consulted the cluster anyway: %v", err)
	}
}

// 🔴 THE OTHER HALF, AND IT HAD NO TEST AT ALL. The softening above applies to
// being unable to LOOK; what the cluster SAYS stays fatal on both paths. A
// reviewer moved the counting call inside the same dry-run softening — a
// one-token edit that passes an undersized cluster on `--ha --dry-run` — and it
// survived the entire package, because the only way to reach that line was
// against a real multi-node cluster. The listNodes seam is what makes the branch
// reachable; this is what holds it.
func TestTheClusterSayingNoIsFatalEvenOnADryRun(t *testing.T) {
	oneNode := []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "control-plane"}}}

	stub := func(t *testing.T, nodes []corev1.Node, err error) {
		t.Helper()
		orig := listNodes
		t.Cleanup(func() { listNodes = orig })
		listNodes = func(context.Context, string) ([]corev1.Node, error) { return nodes, err }
	}

	t.Run("an undersized cluster fails a dry run", func(t *testing.T) {
		stub(t, oneNode, nil)
		err := checkHaNodeCapacity(t.Context(), &State{HA: true, DryRun: true})
		if err == nil {
			t.Fatal("a dry run described an --ha install this cluster could never schedule")
		}
		if !strings.Contains(err.Error(), "--ha places") {
			t.Errorf("the refusal is not the counting rule's: %v", err)
		}
	})

	t.Run("an undersized cluster fails a real run", func(t *testing.T) {
		stub(t, oneNode, nil)
		if err := checkHaNodeCapacity(t.Context(), &State{HA: true}); err == nil {
			t.Fatal("an --ha bootstrap proceeded onto a cluster that cannot host it")
		}
	})

	// The counterweight, twice over: a cluster that CAN host it must pass on both
	// paths, or the guard fails bring-ups that would have worked.
	t.Run("a cluster that can host it passes", func(t *testing.T) {
		three := []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "w2"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "w3"}},
		}
		stub(t, three, nil)
		for _, dry := range []bool{false, true} {
			if err := checkHaNodeCapacity(t.Context(), &State{HA: true, DryRun: dry}); err != nil {
				t.Errorf("dry-run=%v: a three-node cluster was refused: %v", dry, err)
			}
		}
	})

	// And the seam must not have changed which errors are softened: a lister that
	// cannot answer is still fatal on a real run and a warning on a dry one.
	t.Run("a lister that cannot answer keeps the old asymmetry", func(t *testing.T) {
		stub(t, nil, errors.New("connecting to the cluster to verify it can host the --ha topology: nope"))
		if err := checkHaNodeCapacity(t.Context(), &State{HA: true, DryRun: true}); err != nil {
			t.Errorf("a dry run failed on an unreadable cluster: %v", err)
		}
		if err := checkHaNodeCapacity(t.Context(), &State{HA: true}); err == nil {
			t.Error("a real run proceeded without being able to see the cluster's nodes")
		}
	})
}
