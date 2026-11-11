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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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
		{"install by a newer dcctl satisfies the install prerequisite", installWitness{Found: true, DcctlVersion: "v1.4.0"}, "v1.3.0", false, ""},
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
	if err := markInstallApplying(ctx, typed, "uid", "v1.3.0", now, nil); err != nil {
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
		name, requested, own, registry string
		refused                        bool
	}{
		{"the previous release", "v1.2.0", "v1.3.0", "", true},
		{"an older patch", "v1.3.0", "v1.3.1", "", true},
		{"a candidate of this release from the final dcctl", "v1.3.0-rc.1", "v1.3.0", "", true},
		{"the bare form of an older release", "1.2.0", "v1.3.0", "", true},

		{"its own release", "v1.3.0", "v1.3.0", "", false},
		{"the bare form of its own release", "1.3.0", "v1.3.0", "", false},
		{"a newer release", "v1.4.0", "v1.3.0", "", true},
		{"a newer patch", "v1.3.1", "v1.3.0", "", true},
		{"no version asked for", "", "v1.3.0", "", false},
		{"a tag that is no release", "my-build", "v1.3.0", "", false},
		{"a source-built dcctl", "v1.2.0", "dev", "", false},
		{"a drill tag on a registry of the operator's own", "0.24.0-drill.1", "v1.3.0", "localhost:5000", false},
		{"an older release tag on a registry of the operator's own", "v1.2.0", "v1.3.0", "localhost:5000", false},
		{"an older release on the published registry", "v1.2.0", "v1.3.0", DefaultImageRegistry, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseForeignRelease(tc.requested, tc.own, tc.registry)
			if !tc.refused {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("--version %s from a %s dcctl was let through", tc.requested, tc.own)
			}
			want, _ := parseRelease(tc.requested)
			own, _ := parseRelease(tc.own)
			if want.compare(own) < 0 {
				requireOrderMessage(t, err, "no downgrade", "versioning-policy#upgrade-paths")
			} else {
				requireOrderMessage(t, err, "Get the dcctl of release "+tc.requested)
			}
			for _, want := range []string{tc.requested, tc.own} {
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
	if err == nil || !strings.Contains(err.Error(), "no downgrade") || !strings.Contains(err.Error(), "only moves an instance to its own release") {
		t.Fatalf("--version v1.2.0 from a v1.3.0 dcctl was not refused with the forward-only policy: %v", err)
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

// 🔴 THE LOOP THIS PREVENTS: the install record names the dcctl that ran the install, never the
// --version it was given. An upgrade to a release newer than its dcctl used to answer "run dcctl
// install at it", and that install, run by the same dcctl, recorded the same old version and left
// the upgrade asking for it again. This walks the documented install-then-upgrade with a newer
// --version and holds that the remedy the message names is one that clears it.
func TestANewerVersionIsNotAnInstallThenUpgradeLoop(t *testing.T) {
	ctx := context.Background()
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })
	typed := fake.NewClientset()
	install := func(dcctl string) {
		t.Helper()
		// What `dcctl install` records: the running dcctl, whatever --version said.
		if err := putInstallRecord(ctx, typed, InstallRecord{
			Schema: installRecordSchema, Phase: installPhaseInstalled, DcctlVersion: dcctl, UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	const newer = "v1.4.0"

	// The v1.3.0 dcctl: install --version v1.4.0 is refused up front, so it never records a
	// version that cannot satisfy the upgrade, and the refusal names the newer dcctl.
	DefaultImageVersion = "v1.3.0"
	err := RefuseForeignRelease(newer, DefaultImageRegistry)
	if err == nil || !strings.Contains(err.Error(), "Get the dcctl of release "+newer) {
		t.Fatalf("install --version %s from a v1.3.0 dcctl was let through: %v", newer, err)
	}
	// And had the install been run without the flag, the upgrade still refuses to take the
	// newer release, naming the same remedy rather than `dcctl install` alone.
	install("v1.3.0")
	if err := requireInstallOrderFor(ctx, typed, "v1.3.0", orderInstall); err != nil {
		t.Fatalf("an install by the same dcctl was refused: %v", err)
	}
	err = refuseForeignRelease(newer, DefaultImageVersion, DefaultImageRegistry)
	if err == nil || !strings.Contains(err.Error(), "Get the dcctl of release "+newer) {
		t.Fatalf("upgrade --version %s from a v1.3.0 dcctl was let through: %v", newer, err)
	}

	// The remedy: the v1.4.0 dcctl installs, then upgrades, and nothing refuses.
	DefaultImageVersion = newer
	if err := RefuseForeignRelease(newer, DefaultImageRegistry); err != nil {
		t.Fatalf("install --version %s from the v1.4.0 dcctl was refused: %v", newer, err)
	}
	if err := requireInstallOrderFor(ctx, typed, newer, orderInstall); err == nil {
		t.Fatal("a v1.4.0 upgrade was allowed over an install by v1.3.0")
	}
	install(newer)
	if err := requireInstallOrderFor(ctx, typed, newer, orderInstall); err != nil {
		t.Fatalf("the upgrade was still refused after the newer dcctl installed: %v", err)
	}
	if err := refuseForeignRelease(newer, DefaultImageVersion, DefaultImageRegistry); err != nil {
		t.Fatalf("the newer dcctl was refused its own release: %v", err)
	}
}

// A flagless upgrade re-applies the instance's DECLARED version. One declared by a newer dcctl
// than this one is refused like an explicit --version, naming that release's dcctl; an older
// declaration is the ordinary pre-upgrade state.
func TestAFlaglessUpgradeOfADeclarationNewerThanThisDcctlIsRefused(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })
	DefaultImageVersion = "v1.3.0"

	apply := func(registry, version string) error {
		inst := readyGuide()
		inst.Spec.ImageRegistry, inst.Spec.ImageVersion = registry, version
		st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
		return applyUpgradeDeclaration(st, inst, UpgradeOptions{Options: Options{Instance: "guide"}})
	}
	err := apply(DefaultImageRegistry, "v1.4.0")
	if err == nil || !strings.Contains(err.Error(), "Get the dcctl of release v1.4.0") ||
		!strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("a declaration at v1.4.0 was re-applied by a v1.3.0 dcctl: %v", err)
	}
	if err := apply(DefaultImageRegistry, "v1.3.0"); err != nil {
		t.Errorf("a declaration at this dcctl's release was refused: %v", err)
	}
	if err := apply(DefaultImageRegistry, "v1.2.0"); err != nil {
		t.Errorf("a declaration older than this dcctl was refused: %v", err)
	}
	if err := apply("localhost:5000", "v1.4.0"); err != nil {
		t.Errorf("a tag on the operator's own registry was judged as a release: %v", err)
	}
}

// Every spelling of the published registry is the published registry; a different one is not.
func TestSpellingsOfThePublishedRegistryAreNotCustomRegistries(t *testing.T) {
	for _, tc := range []struct {
		registry string
		refused  bool
	}{
		{DefaultImageRegistry, true},
		{"ghcr.io:443/devicechain-io", true},
		{DefaultImageRegistry + "/", true},
		{"GHCR.IO/devicechain-io", true},
		{"https://ghcr.io/devicechain-io//", true},
		{"ghcr.io:5000/devicechain-io", false},
		{"ghcr.io/other", false},
		{"localhost:5000", false},
	} {
		err := refuseForeignRelease("v1.2.0", "v1.3.0", tc.registry)
		if tc.refused && err == nil {
			t.Errorf("registry %q bypassed the published-registry check", tc.registry)
		}
		if !tc.refused && err != nil {
			t.Errorf("registry %q was judged as the published one: %v", tc.registry, err)
		}
	}
}

func TestRefuseADowngrade(t *testing.T) {
	for _, tc := range []struct {
		name, declaredRegistry, declared, targetRegistry, target string
		refused                                                  bool
	}{
		{"older minor", "", "v1.4.0", "", "v1.3.0", true},
		{"older patch", DefaultImageRegistry, "v1.3.1", DefaultImageRegistry, "v1.3.0", true},
		{"older candidate", "", "v1.4.0-rc.2", "", "v1.4.0-rc.1", true},
		{"candidate before final", "", "v1.4.0", "", "v1.4.0-rc.2", true},
		{"equal", "", "v1.3.0", "", "v1.3.0", false},
		{"newer", "", "v1.2.0", "", "v1.3.0", false},
		{"custom declaration", "localhost:5000", "v1.4.0", "", "v1.3.0", false},
		{"custom target", "", "v1.4.0", "localhost:5000", "v1.3.0", false},
		{"published spelling", "ghcr.io:443/devicechain-io/", "v1.4.0", DefaultImageRegistry, "v1.3.0", true},
		{"dev declaration", "", "dev", "", "v1.3.0", false},
		{"local declaration", "", "local-abc", "", "v1.3.0", false},
		{"empty declaration", "", "", "", "v1.3.0", false},
		{"unparseable declaration", "", "garbage", "", "v1.3.0", false},
		{"unpublished semver declaration", "", "v1.4.0-dev.3", "", "v1.3.0", false},
		{"dev target", "", "v1.4.0", "", "dev", false},
		{"local target", "", "v1.4.0", "", "local-abc", false},
		{"empty target", "", "v1.4.0", "", "", false},
		{"unparseable target", "", "v1.4.0", "", "garbage", false},
		{"unpublished semver target", "", "v1.4.0", "", "v1.3.0-dev.3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseADowngrade("guide", tc.declaredRegistry, tc.declared, tc.targetRegistry, tc.target)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireOrderMessage(t, err, "guide", tc.declared, tc.target, "no downgrade", "roll forward", "restore from backup", "Nothing has been changed")
		})
	}
}

func requireOrderMessage(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected forward-only refusal")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("refusal missing %q: %v", fragment, err)
		}
	}
}

func TestUpgradeDeclarationRefusesMovingBack(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })
	for _, tc := range []struct {
		name, own, declaredRegistry, declared, targetRegistry, target string
		refused                                                       bool
	}{
		{"older minor", "v1.3.0", DefaultImageRegistry, "v1.4.0", "", "v1.3.0", true},
		{"older patch", "v1.3.0", DefaultImageRegistry, "v1.3.1", "", "v1.3.0", true},
		{"older candidate", "v1.4.0-rc.1", DefaultImageRegistry, "v1.4.0-rc.2", "", "v1.4.0-rc.1", true},
		{"source built dcctl still refuses", "dev", DefaultImageRegistry, "v1.4.0", "", "v1.3.0", true},
		{"unpublished dcctl still refuses", "v1.3.0-dev.3", DefaultImageRegistry, "v1.4.0", "", "v1.3.0", true},
		{"forward", "v1.3.0", DefaultImageRegistry, "v1.2.0", "", "v1.3.0", false},
		{"equal", "v1.3.0", DefaultImageRegistry, "v1.3.0", "", "v1.3.0", false},
		{"flagless", "v1.3.0", DefaultImageRegistry, "v1.2.0", "", "", false},
		{"custom declaration and target", "v1.3.0", "localhost:5000", "v1.4.0", "localhost:5000", "v1.3.0", false},
		{"custom declaration only", "v1.3.0", "localhost:5000", "v1.4.0", DefaultImageRegistry, "v1.3.0", false},
		{"custom target only", "v1.3.0", DefaultImageRegistry, "v1.4.0", "localhost:5000", "v1.3.0", false},
		{"dev declaration", "v1.3.0", DefaultImageRegistry, "dev", "", "v1.3.0", false},
		{"local declaration", "v1.3.0", DefaultImageRegistry, "local-abc", "", "v1.3.0", false},
		{"empty declaration", "v1.3.0", DefaultImageRegistry, "", "", "v1.3.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			DefaultImageVersion = tc.own
			inst := readyGuide()
			inst.Spec.ImageRegistry, inst.Spec.ImageVersion = tc.declaredRegistry, tc.declared
			st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
			err := applyUpgradeDeclaration(st, inst, UpgradeOptions{Options: Options{Instance: "guide", ImageRegistry: tc.targetRegistry, ImageVersion: tc.target}})
			if tc.refused {
				requireOrderMessage(t, err, tc.declared, "no downgrade")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRefuseAnOlderInstall(t *testing.T) {
	installed := func(v string) *InstallRecord { return &InstallRecord{Phase: installPhaseInstalled, DcctlVersion: v} }
	applying := func(current, last string) *InstallRecord {
		return &InstallRecord{Phase: installPhaseApplying, DcctlVersion: current, LastInstalled: &CompletedInstall{DcctlVersion: last}}
	}
	for _, tc := range []struct {
		name        string
		prev        *InstallRecord
		own, newest string
	}{
		{"newer installed", installed("v1.5.0"), "v1.4.0", "v1.5.0"},
		{"newer attempt", applying("v1.5.0", "v1.4.0"), "v1.4.0", "v1.5.0"},
		{"newer completed", applying("v1.4.0", "v1.5.0"), "v1.4.0", "v1.5.0"},
		{"newest of both", applying("v1.5.0", "v1.6.0"), "v1.4.0", "v1.6.0"},
		{"unknown attempt with newer completed", applying("garbage", "v1.5.0"), "v1.4.0", "v1.5.0"},
		{"older patch", installed("v1.4.1"), "v1.4.0", "v1.4.1"},
		{"older candidate", installed("v1.4.0-rc.2"), "v1.4.0-rc.1", "v1.4.0-rc.2"},
		{"equal", installed("v1.4.0"), "v1.4.0", ""},
		{"older recorded", installed("v1.3.0"), "v1.4.0", ""},
		{"no record", nil, "v1.4.0", ""},
		{"dev dcctl", installed("v1.5.0"), "dev", ""},
		{"unpublished dcctl", installed("v1.5.0"), "v1.4.0-dev.3", ""},
		{"local dcctl", installed("v1.5.0"), "local", ""},
		{"empty record version", installed(""), "v1.4.0", ""},
		{"garbage record version", installed("garbage"), "v1.4.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseAnOlderInstall(tc.prev, tc.own)
			if tc.newest != "" {
				requireOrderMessage(t, err, tc.newest, tc.own, "or newer", "no downgrade", "Nothing has been changed")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnectAndRefuseRejectsAnOlderInstallBeforeAnyWrite(t *testing.T) {
	for _, applying := range []bool{false, true} {
		name := "installed"
		if applying {
			name = "applying"
		}
		t.Run(name, func(t *testing.T) {
			typed := fake.NewClientset()
			rec := installed()
			rec.DcctlVersion = "v1.5.0"
			if applying {
				rec.Phase = installPhaseApplying
				rec.LastInstalled = &CompletedInstall{DcctlVersion: "v1.4.0"}
			}
			if err := putInstallRecord(t.Context(), typed, *rec); err != nil {
				t.Fatal(err)
			}
			typed.ClearActions()
			orig := installClients
			t.Cleanup(func() { installClients = orig })
			installClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return nil, typed, nil }
			withClusterInstances(t, nil, nil)
			st, settings := reinstallState()
			st.DcctlVersion = "v1.4.0"
			_, _, err := connectAndRefuse(t.Context(), st, settings, stateHere())
			requireOrderMessage(t, err, "v1.5.0", "or newer", "no downgrade", "Nothing has been changed")
			for _, action := range typed.Actions() {
				if verb := action.GetVerb(); verb != "get" && verb != "list" && verb != "watch" {
					t.Errorf("wrote before refusing: %s", verb)
				}
			}
		})
	}
}
