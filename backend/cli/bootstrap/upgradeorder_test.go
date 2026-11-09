// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/client-go/kubernetes/fake"
)

const orderInstall = "dcctl install local"

// 🔴 THE HOLE: `dcctl upgrade` before `dcctl install` was let through whenever the release left
// the operator's definitions alone, because the identity check digests the definitions only.
// The install record is the cluster's witness of which dcctl last finished the install.
func TestUpgradeIsRefusedWhenTheClusterInstallIsOlderThanThisDcctl(t *testing.T) {
	for _, tc := range []struct {
		name    string
		w       installWitness
		dcctl   string
		refused bool
		names   string // a fragment the refusal must carry
	}{
		{"install by the previous release", installWitness{Found: true, DcctlVersion: "v1.2.0"}, "v1.3.0", true, "v1.2.0"},
		{"install by an older patch", installWitness{Found: true, DcctlVersion: "v1.3.0"}, "v1.3.1", true, "v1.3.0"},
		{"install by a release candidate of the same version", installWitness{Found: true, DcctlVersion: "v1.3.0-rc.1"}, "v1.3.0", true, "v1.3.0-rc.1"},
		{"install by an older candidate", installWitness{Found: true, DcctlVersion: "v1.4.0-rc.2"}, "v1.4.0-rc.10", true, "rc.2"},
		{"no install record", installWitness{}, "v1.3.0", true, "no install record"},
		{"an install that never finished", installWitness{Found: true, Applying: true}, "v1.3.0", true, "did not finish"},
		{"a record that does not say who installed", installWitness{Found: true}, "v1.3.0", true, "does not say"},
		{"a record naming a build that is no release", installWitness{Found: true, DcctlVersion: "dev"}, "v1.3.0", true, "does not say"},
		{"an unfinished re-install over an older install", installWitness{Found: true, Applying: true, DcctlVersion: "v1.2.0"}, "v1.3.0", true, "v1.2.0"},

		// The counterweights: a check that refused these would break every upgrade there is.
		{"a re-run at the same version", installWitness{Found: true, DcctlVersion: "v1.3.0"}, "v1.3.0", false, ""},
		{"the bare form of the same version", installWitness{Found: true, DcctlVersion: "1.3.0"}, "v1.3.0", false, ""},
		{"install by a newer dcctl (going back is the older dcctl's documented path)", installWitness{Found: true, DcctlVersion: "v1.4.0"}, "v1.3.0", false, ""},
		{"a candidate over a final release", installWitness{Found: true, DcctlVersion: "v1.3.0"}, "v1.3.0-rc.1", false, ""},
		{"a source build is not compared", installWitness{}, "dev", false, ""},
		{"a stamped source build is not compared", installWitness{Found: true, DcctlVersion: "v1.2.0"}, "v1.3.0-dev.20261109", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := judgeInstallOrder(tc.w, tc.dcctl, orderInstall)
			if !tc.refused {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("an upgrade by %s over %+v was let through", tc.dcctl, tc.w)
			}
			for _, want := range []string{orderInstall, tc.names} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
		})
	}
}

// The seam reads a real record: a completed install by v1.2.0 is the witness, an applying
// re-install keeps the last completed one, and both are refused for a v1.3.0 dcctl.
func TestTheInstallOrderIsReadFromTheClustersInstallRecord(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	put := func(t *testing.T, version string) *fake.Clientset {
		t.Helper()
		typed := fake.NewClientset()
		if err := putInstallRecord(ctx, typed, InstallRecord{
			Schema: installRecordSchema, Phase: installPhaseInstalled, DcctlVersion: version, UpdatedAt: now(),
		}); err != nil {
			t.Fatal(err)
		}
		return typed
	}

	if err := requireInstallOrderFor(ctx, put(t, "v1.2.0"), "v1.3.0", orderInstall); err == nil ||
		!strings.Contains(err.Error(), orderInstall) {
		t.Fatalf("an install by v1.2.0 was accepted for a v1.3.0 upgrade: %v", err)
	}
	if err := requireInstallOrderFor(ctx, put(t, "v1.3.0"), "v1.3.0", orderInstall); err != nil {
		t.Fatalf("a re-run at the installed version was refused: %v", err)
	}
	if err := requireInstallOrderFor(ctx, fake.NewClientset(), "v1.3.0", orderInstall); err == nil {
		t.Fatal("a cluster with no install record was accepted")
	}

	// A re-install that started and did not finish keeps the last completed install's version.
	typed := put(t, "v1.2.0")
	if err := markInstallApplying(ctx, typed, "uid", "v1.3.0", now); err != nil {
		t.Fatal(err)
	}
	w, err := readInstallWitness(ctx, typed)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Applying {
		t.Errorf("an applying record was read as finished: %+v", w)
	}
}

// 🔴 THE WIRING, NOT THE FUNCTION: hydrateUpgradeState must make the call, ahead of the
// declaration read, so the refusal precedes anything that could be written.
func TestHydratingAnUpgradeAsksWhichDcctlInstalledTheCluster(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	prev := requireOperatorFor
	t.Cleanup(func() { requireOperatorFor = prev })
	requireOperatorFor = func(context.Context, string, string, string) error { return nil } // identity unchanged
	// The declaration read would fail the test if the refusal did not come first.
	declared := false
	stubDeclarationCalls(t, &declared)

	binding := ClusterBinding{KubeContext: "kind-c", Cluster: "c"}
	opts := UpgradeOptions{Options: Options{Instance: "prod"}, DcctlVersion: "v1.3.0"}
	typed := fake.NewClientset()
	if err := putInstallRecord(t.Context(), typed, InstallRecord{
		Schema: installRecordSchema, Phase: installPhaseInstalled, DcctlVersion: "v1.2.0", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = hydrateUpgradeState(t.Context(), typed, provider, binding, opts)
	if err == nil || !strings.Contains(err.Error(), "dcctl install local") {
		t.Fatalf("an upgrade over an older cluster install was not refused with the install command: %v", err)
	}
	if declared {
		t.Error("the declaration was read before the install order was checked")
	}
}

func TestANewerDcctlIsNotPointedAtAnOlderRelease(t *testing.T) {
	for _, tc := range []struct {
		name, requested, own string
		refused              bool
	}{
		{"the previous release", "v1.2.0", "v1.3.0", true},
		{"an older patch", "v1.3.0", "v1.3.1", true},
		{"a candidate of this release from the final dcctl", "v1.3.0-rc.1", "v1.3.0", true},
		{"the bare form of an older release", "1.2.0", "v1.3.0", true},

		{"its own release", "v1.3.0", "v1.3.0", false},
		{"the bare form of its own release", "1.3.0", "v1.3.0", false},
		{"a newer release", "v1.4.0", "v1.3.0", false},
		{"no version asked for", "", "v1.3.0", false},
		{"a tag that is no release", "my-build", "v1.3.0", false},
		{"a source-built dcctl", "v1.2.0", "dev", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseOlderRelease(tc.requested, tc.own, "local", "prod")
			if !tc.refused {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("--version %s from a %s dcctl was let through", tc.requested, tc.own)
			}
			for _, want := range []string{"older release", tc.requested, tc.own} {
				if want == "older release" {
					want = "the dcctl of release"
				}
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
		})
	}
}

// The wiring: applyUpgradeDeclaration makes the call, only for an explicit --version, and
// never for the version a plain upgrade stays on.
func TestAnUpgradeToAnOlderReleaseIsRefusedWhereTheVersionIsSettled(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })
	DefaultImageVersion = "v1.3.0"

	apply := func(version string) error {
		st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
		return applyUpgradeDeclaration(st, readyGuide(), UpgradeOptions{
			Options: Options{Instance: "guide", ImageVersion: version},
		})
	}
	err := apply("v1.2.0")
	if err == nil || !strings.Contains(err.Error(), "dcctl of release v1.2.0") {
		t.Fatalf("--version v1.2.0 from a v1.3.0 dcctl was not refused with the way back: %v", err)
	}
	if err := apply("v1.3.0"); err != nil {
		t.Errorf("the dcctl's own release was refused: %v", err)
	}
	// The declaration runs v0.21.1, older than this dcctl: a plain upgrade stays where it is.
	if err := apply(""); err != nil {
		t.Errorf("a flagless upgrade was refused for the version the instance already runs: %v", err)
	}
}

// stubDeclarationCalls makes the declaration read record that it happened.
func stubDeclarationCalls(t *testing.T, called *bool) {
	t.Helper()
	orig := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = orig })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		*called = true
		return nil, nil
	}
}
