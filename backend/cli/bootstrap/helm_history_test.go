// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func supersededRelease(version int) *release.Release {
	return &release.Release{
		Name:    "dc-history",
		Version: version,
		Info:    &release.Info{Status: release.StatusSuperseded},
		Config:  map[string]interface{}{"revision": version},
	}
}

// storeRevisions writes n revisions through a Storage configured with the given
// bound, and reports how many survive.
func storeRevisions(t *testing.T, maxHistory, n int) int {
	t.Helper()
	s := storage.Init(driver.NewMemory())
	s.MaxHistory = maxHistory
	for i := 1; i <= n; i++ {
		if err := s.Create(supersededRelease(i)); err != nil {
			t.Fatalf("creating revision %d: %v", i, err)
		}
	}
	h, err := s.History("dc-history")
	if err != nil {
		t.Fatalf("reading history: %v", err)
	}
	return len(h)
}

// 🔴 THIS PINS A PROPERTY OF HELM, NOT OF OUR CODE, AND THAT IS DELIBERATE. Two
// claims about the library are load-bearing for the bound and for what the
// operations document may promise about rotation, and both were read out of the
// source rather than observed:
//
//   - the SDK's zero value keeps EVERY revision, so a program that never assigns
//     MaxHistory is not taking a conservative default, it is opting out; and
//   - the prune trims history that already exists, rather than only capping growth
//     from the first bounded write — so one upgrade collapses a long history.
//
// A library upgrade that changed either would otherwise be invisible here until an
// instance had accumulated years of renders.
func TestHelmKeepsEveryRevisionUnlessBounded(t *testing.T) {
	if got := storeRevisions(t, 0, helmMaxHistory+5); got != helmMaxHistory+5 {
		t.Fatalf("the unbounded default dropped revisions: kept %d of %d — if Helm has "+
			"gained a default bound, the comment on helmMaxHistory is now wrong",
			got, helmMaxHistory+5)
	}
}

func TestABoundedStoreTrimsHistoryThatAlreadyExists(t *testing.T) {
	if got := storeRevisions(t, helmMaxHistory, helmMaxHistory+5); got != helmMaxHistory {
		t.Fatalf("bounded store kept %d revisions, want %d", got, helmMaxHistory)
	}
}

// The wiring half: the bound is worth nothing if the action the chart is actually
// re-run through does not carry it. Deleting the assignment in newHelmUpgrade leaves
// MaxHistory at zero, which this reads as the unlimited setting it is.
func TestTheChartUpgradeCarriesTheHistoryBound(t *testing.T) {
	upg := newHelmUpgrade(&action.Configuration{}, "dc-inst")
	if upg.MaxHistory != helmMaxHistory {
		t.Fatalf("chart upgrade runs with MaxHistory %d, want %d", upg.MaxHistory, helmMaxHistory)
	}
	if helmMaxHistory <= 0 {
		t.Fatal("helmMaxHistory must be positive; zero and below are how Helm spells 'keep everything'")
	}
}

// 🔴 THE TWO TESTS ABOVE PIN THE FIELD AND THE STORE, AND NOTHING PINNED THE LINK
// BETWEEN THEM. Helm copies the action's MaxHistory onto the storage inside
// RunWithContext (action/upgrade.go:170) — a line this repo had read but not
// observed, which is the exact gap the tests above exist to close. A Helm bump that
// moved or dropped that copy would leave both of them green and the bound inert.
//
// So this drives a real upgrade through newHelmUpgrade against an in-memory store
// and a fake cluster, and asserts on the history that survives. It covers the whole
// chain: the constant, the assignment in newHelmUpgrade, Helm's copy onto the
// storage, and the prune.
func TestAChartUpgradeActuallyTrimsTheStoredHistory(t *testing.T) {
	store := storage.Init(driver.NewMemory())
	over := helmMaxHistory + 4
	for i := 1; i <= over; i++ {
		rel := supersededRelease(i)
		rel.Namespace = "dc-inst"
		if i == over {
			rel.Info.Status = release.StatusDeployed
		}
		if err := store.Create(rel); err != nil {
			t.Fatalf("seeding revision %d: %v", i, err)
		}
	}
	if got, _ := store.History("dc-history"); len(got) != over {
		t.Fatalf("seeded %d revisions, store holds %d — the unbounded store is the premise here", over, len(got))
	}

	cfg := &action.Configuration{
		Releases:     store,
		KubeClient:   &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...interface{}) {},
	}

	upg := newHelmUpgrade(cfg, "dc-inst")
	upg.Wait = false // no cluster to wait on; the bound is what is under test
	if _, err := upg.RunWithContext(context.Background(), "dc-history", minimalChart(), map[string]interface{}{}); err != nil {
		t.Fatalf("upgrade failed before it could exercise the bound: %v", err)
	}

	h, err := store.History("dc-history")
	if err != nil {
		t.Fatalf("reading history: %v", err)
	}
	if len(h) != helmMaxHistory {
		t.Fatalf("after an upgrade the store holds %d revisions, want %d — the bound did not "+
			"reach the storage, so nothing is trimming this instance's history", len(h), helmMaxHistory)
	}
}

// minimalChart is the smallest chart an upgrade will accept: a name, a version, and
// one rendered object. Nothing about the chart is under test here.
func minimalChart() *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "dc", Version: "0.0.1"},
		Templates: []*chart.File{{
			Name: "templates/cm.yaml",
			Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dc-probe\n"),
		}},
	}
}
