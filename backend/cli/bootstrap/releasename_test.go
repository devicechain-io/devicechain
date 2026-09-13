// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// 🔴 THE NAMES ARE LITERALS, NOT helmReleaseNameFor's OWN OUTPUT. A fixture built from
// the function under test agrees with it by construction: change "dc-" to "x-" and a
// test asserting helmReleaseNameFor("a") == helmReleaseNameFor("a") still passes while
// every instance in the field becomes unfindable. What is pinned here is the actual
// string, because that string is the contract the upgrade rig, the operator's `helm
// list` and every release installed by this binary have to agree on.
func TestTheReleaseNameCarriesTheInstance(t *testing.T) {
	if got := helmReleaseNameFor("alpha"); got != "dc-alpha" {
		t.Fatalf("got %q, want %q — the release name is the one thing that tells two "+
			"instances' releases apart", got, "dc-alpha")
	}

	// 🔴 THE NEGATIVE CONTROL THIS WHOLE SLICE EXISTS FOR. A name that did not vary with
	// the instance is precisely the state before this change, and it would pass the
	// assertion above if the constant happened to be "dc-alpha".
	if helmReleaseNameFor("a") == helmReleaseNameFor("b") {
		t.Fatal("two instances produced the same release name, so the second would install " +
			"over the first rather than beside it")
	}

	// And neither may collide with the name every pre-v0.17.0 instance already holds:
	// the sweep in uninstallLegacyRelease treats that name as somebody ELSE's convention,
	// so an instance that landed on it would be swept by its own destroy twice.
	if helmReleaseNameFor("") == legacyHelmReleaseName {
		t.Fatalf("an empty instance name collides with the legacy release name %q",
			legacyHelmReleaseName)
	}
}

// 🔴 THE STATE MASK IS THE QUESTION BEING ASKED. action.List defaults to ListDeployed —
// "which releases are healthy" — and the reader asks "is anything of ours still here".
// A release whose install FAILED has objects in the cluster; read as absent, a second
// instance gets built on top of the wreckage of the first.
func TestTheReleaseListSeesEverythingThatStillHasResources(t *testing.T) {
	for name, state := range map[string]action.ListStates{
		"a failed install":      action.ListFailed,
		"a pending install":     action.ListPendingInstall,
		"a pending upgrade":     action.ListPendingUpgrade,
		"one being uninstalled": action.ListUninstalling,
		"a healthy release":     action.ListDeployed,
	} {
		if liveReleaseStates&state == 0 {
			t.Errorf("%s is not counted as holding the cluster, so a bootstrap would be "+
				"allowed to install a second instance beside it", name)
		}
	}
	// The one genuine absence, and the negative control for the line above: an
	// uninstalled record survives only with --keep-history, and what it describes is gone.
	if liveReleaseStates&action.ListUninstalled != 0 {
		t.Error("an uninstalled release is counted as holding the cluster, so a destroy " +
			"followed by a bootstrap would be refused on a cluster that is genuinely empty")
	}
}

// 🔴 RECOGNITION IS BY CHART, AND THE NEGATIVE CONTROL IS THE POINT OF THE TEST. This
// list feeds a REFUSAL, so claiming a release that is not ours refuses bootstraps into
// clusters holding somebody else's software. "dc-" is a prefix anybody may use.
func TestOnlyReleasesInstalledFromOurChartAreClaimed(t *testing.T) {
	rels := []*release.Release{
		{Name: "dc-alpha", Chart: chartNamed(helmChartName)},
		{Name: "dc", Chart: chartNamed(helmChartName)},
		{Name: "dc-nats", Chart: chartNamed("nats")},
		{Name: "ingress-nginx", Chart: chartNamed("ingress-nginx")},
		{Name: "dc-broken", Chart: &chart.Chart{}},
		{Name: "dc-chartless", Chart: nil},
		nil,
	}
	got := releaseNamesFromChart(rels)
	want := []string{"dc", "dc-alpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func chartNamed(name string) *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{Name: name, Version: "0.0.0", APIVersion: "v2"}}
}

// deviceChainRelease builds a stored release as this binary installs one: our chart,
// deployed, with the instance recorded in its values.
func deviceChainRelease(name, instance string) *release.Release {
	rel := &release.Release{
		Name:      name,
		Version:   1,
		Namespace: helmReleaseNamespace,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart:     chartNamed(helmChartName),
	}
	if instance != "" {
		rel.Config = map[string]interface{}{"instance": map[string]interface{}{"id": instance}}
	}
	return rel
}

// inMemoryHelm builds an action.Configuration over Helm's memory driver, holding the
// given releases. Real action.List, action.NewGetValues and action.NewUninstall run
// against it, so what is exercised is the library's behaviour rather than a model of it.
func inMemoryHelm(t *testing.T, rels ...*release.Release) (*action.Configuration, *storage.Storage) {
	t.Helper()
	store := storage.Init(driver.NewMemory())
	for _, rel := range rels {
		if err := store.Create(rel); err != nil {
			t.Fatalf("seeding release %q: %v", rel.Name, err)
		}
	}
	return &action.Configuration{
		Releases:     store,
		KubeClient:   &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...interface{}) {},
	}, store
}

// The reader has to see BOTH conventions: an instance installed by this release and one
// installed before the rename, since a pre-v0.17.0 instance left no declaration and
// stamped no Secrets, making its release the only artifact that names it.
func TestTheReaderFindsBothTheRenamedAndTheLegacyRelease(t *testing.T) {
	for name, tc := range map[string]struct {
		rels []*release.Release
		want string
	}{
		"a release installed by this version":   {[]*release.Release{deviceChainRelease("dc-alpha", "alpha")}, "alpha"},
		"a release installed before the rename": {[]*release.Release{deviceChainRelease("dc", "legacy")}, "legacy"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := inMemoryHelm(t, tc.rels...)
			ids, err := deviceChainReleases(cfg)
			if err != nil {
				t.Fatalf("reading the cluster's releases: %v", err)
			}
			if len(ids) != 1 || ids[0] != tc.want {
				t.Fatalf("got %v, want [%s] — a cluster that reads as empty is one a second "+
					"instance gets installed into", ids, tc.want)
			}
		})
	}

	// The negative control for the two above: a cluster holding nothing of ours reads as
	// empty, or the boundary would refuse the first bootstrap of every cluster.
	cfg, _ := inMemoryHelm(t, &release.Release{
		Name: "ingress-nginx", Version: 1, Namespace: helmReleaseNamespace,
		Info: &release.Info{Status: release.StatusDeployed}, Chart: chartNamed("ingress-nginx"),
	})
	ids, err := deviceChainReleases(cfg)
	if err != nil || len(ids) != 0 {
		t.Fatalf("a cluster holding only somebody else's release answered %v (err %v)", ids, err)
	}
}

// 🔴 "THERE IS A RELEASE AND I CANNOT TELL WHOSE" IS AN ERROR, NEVER AN ABSENCE.
// Collapsed into an empty list it reads as an empty cluster at every call site — which is
// the direction that writes a second instance over the first.
func TestAReleaseThatNamesNoInstanceIsRefusedRatherThanIgnored(t *testing.T) {
	cfg, _ := inMemoryHelm(t, deviceChainRelease("dc-mystery", ""))
	ids, err := deviceChainReleases(cfg)
	if err == nil {
		t.Fatalf("an unattributable release answered %v with no error, so this cluster is "+
			"indistinguishable from an empty one", ids)
	}
}

// uninstalls drives the whole destroy-side path through the seam, exactly as
// destroyInstanceOnly does, and reports which releases survive.
func uninstalls(t *testing.T, instance string, rels ...*release.Release) ([]string, error) {
	t.Helper()
	cfg, store := inMemoryHelm(t, rels...)
	restore := helmActionConfigFor
	t.Cleanup(func() { helmActionConfigFor = restore })
	helmActionConfigFor = func(string) (*action.Configuration, error) { return cfg, nil }

	err := helmUninstall(context.Background(), "kind-test", instance)

	live, listErr := store.ListDeployed()
	if listErr != nil {
		t.Fatalf("listing what survived: %v", listErr)
	}
	var names []string
	for _, rel := range live {
		names = append(names, rel.Name)
	}
	return names, err
}

// 🔴 THE SWEEP IS A CALL, AND THE CALL IS WHAT THIS PINS. uninstallLegacyRelease is a
// correct function whether or not anything invokes it, and its call site sits behind a
// live Helm connection — so a change that dropped it would break no test while `dcctl
// destroy` quietly stopped removing pre-v0.17.0 releases and reported success.
func TestDestroyRemovesTheReleaseUnderBothNames(t *testing.T) {
	t.Run("the release this version installs", func(t *testing.T) {
		survivors, err := uninstalls(t, "alpha", deviceChainRelease("dc-alpha", "alpha"))
		if err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		if len(survivors) != 0 {
			t.Fatalf("%v survived the destroy", survivors)
		}
	})

	t.Run("a release installed before the rename", func(t *testing.T) {
		survivors, err := uninstalls(t, "alpha", deviceChainRelease("dc", "alpha"))
		if err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		if len(survivors) != 0 {
			t.Fatalf("the pre-rename release %v survived, so `dcctl destroy` reports success "+
				"having left it behind — and destroy-then-bootstrap is the only remedy this "+
				"release offers a pre-v0.17.0 instance", survivors)
		}
	})
}

// 🔴 THE ASYMMETRY, AND IT IS THE WHOLE DESIGN. Under the instance-derived name a foreign
// owner is a CONTRADICTION and must stop the destroy. Under the legacy name it is the
// ordinary fact that this cluster belongs to somebody else, and refusing there would
// break the very destroy that clears a stale local record pointing at another instance's
// cluster.
func TestWhoseReleaseItIsDecidesDifferentlyUnderEachName(t *testing.T) {
	t.Run("a legacy release owned by another instance is left alone and REPORTED", func(t *testing.T) {
		survivors, err := uninstalls(t, "alpha", deviceChainRelease("dc", "bravo"))
		if len(survivors) != 1 || survivors[0] != "dc" {
			t.Fatalf("survivors %v — another instance's release was uninstalled by a destroy "+
				"that did not name it", survivors)
		}
		// 🔴 SURVIVING IS HALF THE CLAIM. Leaving the release alone and then closing with
		// `Instance "alpha" uninstalled` is the defect this command has been fixed for
		// twice (#862, #1065): the operator is told their destroy worked when the cluster
		// was never touched. The refusal is what routes destroyInstanceOnly into
		// resolveForeignRelease, which checks the footprint, clears the stale record and
		// says what actually happened.
		var foreign *foreignReleaseError
		if !errors.As(err, &foreign) {
			t.Fatalf("a destroy that found only another instance's release returned %v; "+
				"without the refusal the caller reports a successful uninstall over a no-op", err)
		}
		if foreign.Owner != "bravo" || foreign.Release != "dc" {
			t.Fatalf("the refusal names owner %q release %q; the operator needs both to know "+
				"what is actually here", foreign.Owner, foreign.Release)
		}
	})

	// 🔴 THE NEGATIVE CONTROL FOR THE REFUSAL ABOVE, AND IT IS WHAT KEEPS DESTROY
	// IDEMPOTENT. A cluster holding NOTHING must stay silent success — a re-run, or an
	// instance whose release was removed by hand, is not a foreign release.
	t.Run("an empty cluster is still success", func(t *testing.T) {
		if _, err := uninstalls(t, "alpha"); err != nil {
			t.Fatalf("destroying into an empty cluster failed: %v — a destroy re-run would "+
				"never be able to finish", err)
		}
	})

	t.Run("a renamed release whose values name another instance is refused", func(t *testing.T) {
		survivors, err := uninstalls(t, "alpha", deviceChainRelease("dc-alpha", "bravo"))
		var foreign *foreignReleaseError
		if !errors.As(err, &foreign) {
			t.Fatalf("a release whose name and instance.id disagree was not refused (%v)", err)
		}
		if len(survivors) != 1 {
			t.Fatalf("the contradictory release was uninstalled anyway; survivors %v", survivors)
		}
		if foreign.Release != "dc-alpha" {
			t.Fatalf("the refusal names release %q, so an operator cannot go and look at it",
				foreign.Release)
		}
	})
}

// 🔴 AN INSTANCE INSTALLED UNDER THE OLD NAME CANNOT BE MOVED ONTO THIS RELEASE, AND THE
// SENTENCE IT GETS MATTERS. Without this refusal the run takes the install branch — its
// release is absent under the new name — and Helm rejects it with a message about
// `meta.helm.sh/release-name` annotations, which is true, unactionable, and arrives after
// the declaration and the operator have already moved.
//
// 🔑 THE CARVE-OUTS ARE WHY THIS IS NOT DEAD CODE BEHIND stepRefuseRebuild. That step
// exempts a restore and --allow-legacy-db-removal, and both are re-runs against exactly
// the instance this case is about.
func TestAnInstanceUnderTheOldReleaseNameIsRefusedWithTheRecreateRecipe(t *testing.T) {
	cfg, _ := inMemoryHelm(t, deviceChainRelease("dc", "alpha"))
	var legacy *ErrLegacyNamedRelease
	if err := refuseLegacyNamedRelease(cfg, "alpha"); !errors.As(err, &legacy) {
		t.Fatalf("an instance installed as the pre-rename release was not refused (%v); the "+
			"run would reach Helm and fail on ownership metadata instead", err)
	}
	if !strings.Contains(legacy.Error(), "dcctl destroy") {
		t.Error("the refusal does not name the supported path, which is the only thing an " +
			"operator can act on")
	}

	// 🔴 THREE NEGATIVE CONTROLS, AND THE LAST IS THE ONE THAT WOULD HURT. A guard that
	// fired on a fresh cluster would refuse the first bootstrap of every instance.
	for name, tc := range map[string]struct {
		rels     []*release.Release
		instance string
	}{
		// 🔴 BOTH RELEASES, NOT JUST THE NEW ONE, AND THE DIFFERENCE IS THE WHOLE TEST.
		// A fixture holding only "dc-alpha" passes whether or not the early return for an
		// already-migrated instance exists, because the legacy lookup behind it then finds
		// nothing either — a control that cannot detect the thing it names moving. Cutting
		// that early return survived this test until the fixture held both.
		"an instance already on this release's naming": {
			[]*release.Release{
				deviceChainRelease("dc-alpha", "alpha"),
				deviceChainRelease("dc", "alpha"),
			}, "alpha"},
		"a pre-rename release belonging to somebody else": {
			[]*release.Release{deviceChainRelease("dc", "bravo")}, "alpha"},
		"a cluster with no DeviceChain release at all": {nil, "alpha"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := inMemoryHelm(t, tc.rels...)
			if err := refuseLegacyNamedRelease(cfg, tc.instance); err != nil {
				t.Fatalf("refused a run it should have let through: %v", err)
			}
		})
	}
}
