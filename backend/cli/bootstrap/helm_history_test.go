// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func supersededRelease(version int) *release.Release {
	return &release.Release{
		Name:    helmReleaseName,
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
	h, err := s.History(helmReleaseName)
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
