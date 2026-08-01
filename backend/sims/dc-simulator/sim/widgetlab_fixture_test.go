// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// ---- The golden fixtures -------------------------------------------------------
//
// Go authors the dashboard definitions and a TypeScript parser and React widgets
// consume them. Nothing in Go can check that contract, and nothing in TypeScript can
// run the builders — so the two sides meet at a committed artifact: Go writes what
// it builds, TypeScript reads it and holds it to the real parser, the real option
// schemas and the real widget registry.
//
// 🔴 THE FIXTURE IS THE PUBLISHED DOCUMENT, not a copy of it. A fixture assembled by
// a test while Provision publishes something built another way would give a green
// gate over a different document — the failure mode is not that the gate breaks, it
// is that it passes while the board a user opens is broken. So it is generated from
// Manifest().Dashboards, which is exactly what Provision hands dashboard-management,
// and a test asserts that identity rather than assuming it.
//
// They live outside the frontend packages because neither declares a `files` field
// or an .npmignore, so anything inside one publishes to npm with it.

var updateFixtures = flag.Bool("update-fixtures", false,
	"rewrite the widgetlab golden fixtures from the current builders")

const widgetlabFixtureDir = "../../../../frontend/testdata/widgetlab"

// widgetlabFixtureSeed is fixed so the fixture is reproducible. The boards bind
// device tokens, which are (manifest, seed) derived, so a fixture generated under a
// different seed would differ in every binding for no reason a reader could see.
const widgetlabFixtureSeed = 1

func widgetlabFixtures(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range NewWidgetlab(widgetlabFixtureSeed, Load{}).Manifest().Dashboards {
		out[d.Token] = d.Definition
	}
	if len(out) == 0 {
		t.Fatal("the scenario declares no dashboards, so the fixtures would be empty and every " +
			"check over them vacuous")
	}
	return out
}

func fixturePath(token string) string {
	return filepath.Join(widgetlabFixtureDir, token+".json")
}

// indent renders a definition for the committed file. Indented rather than the exact
// wire bytes because a reviewer has to be able to read a sixteen-widget board in a
// diff; the comparison below is over VALUES, so the formatting carries no meaning
// and a whitespace-only difference is not a failure.
func indent(t *testing.T, definition string) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(definition), &value); err != nil {
		t.Fatalf("built definition is not valid JSON: %v", err)
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("indent definition: %v", err)
	}
	return append(pretty, '\n')
}

// TestWidgetlabFixturesAreCurrent is the staleness gate. It fails when the committed
// fixture no longer matches what the builders produce, which is the only thing
// keeping the TypeScript side honest about a board that has since changed.
//
// Regenerate with:
//
//	go test ./sim/ -run TestWidgetlabFixturesAreCurrent -update-fixtures
func TestWidgetlabFixturesAreCurrent(t *testing.T) {
	for token, definition := range widgetlabFixtures(t) {
		path := fixturePath(token)
		want := indent(t, definition)

		if *updateFixtures {
			if err := os.MkdirAll(widgetlabFixtureDir, 0o755); err != nil {
				t.Fatalf("create fixture dir: %v", err)
			}
			removeOrphanFixtures(t)
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			t.Logf("wrote %s", path)
			continue
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v\nRegenerate with: go test ./sim/ -run "+
				"TestWidgetlabFixturesAreCurrent -update-fixtures", path, err)
		}
		// Compared as VALUES: the committed file is indented and the published
		// document is not, so bytes would differ for a reason that means nothing.
		if !sameJSON(string(got), definition) {
			t.Errorf("%s is stale — the builders now produce a different board.\n"+
				"Regenerate with: go test ./sim/ -run TestWidgetlabFixturesAreCurrent "+
				"-update-fixtures", path)
		}
	}
}

// committedFixtureTokens lists the fixture files actually on disk.
func committedFixtureTokens(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(widgetlabFixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", widgetlabFixtureDir, err)
	}
	var tokens []string
	for _, e := range entries {
		if name := e.Name(); filepath.Ext(name) == ".json" {
			tokens = append(tokens, name[:len(name)-len(".json")])
		}
	}
	return tokens
}

// removeOrphanFixtures deletes committed fixtures the manifest no longer declares.
// Without it, -update-fixtures only ever ADDS: renaming a dashboard writes the new
// file and leaves the old one next to it, and the TypeScript side — which reads by
// name — goes on parsing the fossil.
func removeOrphanFixtures(t *testing.T) {
	t.Helper()
	live := widgetlabFixtures(t)
	for _, token := range committedFixtureTokens(t) {
		if _, ok := live[token]; ok {
			continue
		}
		path := fixturePath(token)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove orphaned fixture %s: %v", path, err)
		}
		t.Logf("removed orphaned %s", path)
	}
}

// 🔴 THE SET OF FIXTURES MUST EQUAL THE SET OF DASHBOARDS, both directions.
//
// This is the failure that leaves the whole gate green over a document nobody
// publishes. Rename a dashboard token and -update-fixtures writes the new file while
// the old one stays: the TypeScript test reads by name, so it goes on parsing the
// fossil forever — every assertion passing, about a board that no longer exists —
// while the real board is checked by nothing. Add a dashboard and it gets a fixture
// but no TypeScript coverage at all.
//
// Asserting equality is what makes the fixture directory a description of the
// manifest rather than an accumulation of everything it has ever been called.
func TestWidgetlabFixtureFilesMatchTheDashboardsExactly(t *testing.T) {
	manifest := NewWidgetlab(widgetlabFixtureSeed, Load{}).Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the manifest the fixtures come from does not validate: %v", err)
	}

	declared := map[string]bool{}
	for _, d := range manifest.Dashboards {
		declared[d.Token] = true
	}
	committed := map[string]bool{}
	for _, token := range committedFixtureTokens(t) {
		committed[token] = true
	}
	if len(declared) == 0 || len(committed) == 0 {
		t.Fatalf("%d dashboards and %d fixtures; with either empty this asserts nothing",
			len(declared), len(committed))
	}

	for token := range declared {
		if !committed[token] {
			t.Errorf("dashboard %q has no fixture, so nothing on the TypeScript side ever "+
				"sees it", token)
		}
	}
	for token := range committed {
		if !declared[token] {
			t.Errorf("%s is a fixture for a dashboard the manifest no longer declares; the "+
				"TypeScript gate may still be reading it and passing.\nRegenerate with: "+
				"go test ./sim/ -run TestWidgetlabFixturesAreCurrent -update-fixtures",
				fixturePath(token))
		}
	}
}

// Every measurement a board selects must be one the profile actually declares.
//
// This is external truth the fixture cannot supply: a selector naming a metric no
// device reports leaves the widget bound to a real device, on a real slot, showing
// nothing — and the fixture would round-trip it perfectly. The TypeScript side can
// see that a datasource survived; only this side knows what the devices report.
func TestWidgetlabBoardsSelectOnlyDeclaredMetrics(t *testing.T) {
	manifest := NewWidgetlab(widgetlabFixtureSeed, Load{}).Manifest()
	declared := map[string]bool{}
	for _, p := range manifest.Profiles {
		for _, m := range p.Metrics {
			declared[m.Key] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("the profile declares no metrics, so this asserts nothing")
	}

	checked := 0
	for _, d := range manifest.Dashboards {
		var board struct {
			Widgets []struct {
				Id         string `json:"id"`
				Type       string `json:"type"`
				Datasource *struct {
					Measurements []string `json:"measurements"`
				} `json:"datasource"`
			} `json:"widgets"`
		}
		if err := json.Unmarshal([]byte(d.Definition), &board); err != nil {
			t.Fatalf("dashboard %q is not decodable: %v", d.Token, err)
		}
		for _, w := range board.Widgets {
			if w.Datasource == nil {
				continue
			}
			for _, name := range w.Datasource.Measurements {
				checked++
				if !declared[name] {
					t.Errorf("%s/%s selects measurement %q, which no profile declares — the "+
						"widget binds a real device and shows nothing", d.Token, w.Id, name)
				}
			}
			// An EMPTY selector means every measurement. That is deliberate on a
			// table (its whole view is "the latest of everything") and a widening
			// bug on a widget that draws or displays specific ones: a chart authored
			// for two series would silently draw all four, and a card or gauge would
			// subscribe to metrics it never shows.
			//
			// The alarm and control widgets carry a datasource as SCOPE, not as a
			// value selector, so an empty list is correct for them too.
			switch w.Type {
			case "timeseries-chart", "latest-card", "gauge":
				if len(w.Datasource.Measurements) == 0 {
					t.Errorf("%s/%s selects no measurement, so it subscribes to every metric "+
						"the device reports rather than the ones it was authored for",
						d.Token, w.Id)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no board selects any measurement at all, so this test asserted nothing")
	}

	// The gallery's chart is sized for the two series it draws: useMeasurementStream
	// trims its buffer across ALL of a widget's measurements, so a third would cut
	// each series to two thirds of a sweep period.
	var gallery struct {
		Widgets []struct {
			Id         string `json:"id"`
			Datasource *struct {
				Measurements []string `json:"measurements"`
			} `json:"datasource"`
		} `json:"widgets"`
	}
	for _, d := range manifest.Dashboards {
		if d.Token != WidgetlabGalleryDashboardToken {
			continue
		}
		if err := json.Unmarshal([]byte(d.Definition), &gallery); err != nil {
			t.Fatalf("gallery is not decodable: %v", err)
		}
	}
	found := false
	for _, w := range gallery.Widgets {
		if w.Id != "wl-chart" || w.Datasource == nil {
			continue
		}
		found = true
		if n := len(w.Datasource.Measurements); n != 2 {
			t.Errorf("the gallery chart draws %d series; its window is sized for 2, so each "+
				"series would retain %d of a sweep period", n, 2/max(n, 1))
		}
	}
	if !found {
		t.Error("the gallery has no wl-chart widget, so the window-sizing check asserted nothing")
	}
}

// A fixture that is not reproducible is not a fixture: a regeneration that changed
// every device binding for no reason would bury a real change in noise.
func TestWidgetlabFixturesAreReproducible(t *testing.T) {
	first := widgetlabFixtures(t)
	second := widgetlabFixtures(t)
	if len(first) != len(second) {
		t.Fatalf("two builds produced %d and %d dashboards", len(first), len(second))
	}
	for token, definition := range first {
		if second[token] != definition {
			t.Errorf("dashboard %q differs between two builds at the same seed", token)
		}
	}
}
