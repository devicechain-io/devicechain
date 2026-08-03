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
// 🔴 IT COVERS EVERY REGISTERED SCENARIO, AND THAT IS THE POINT OF THE REGISTRY WALK.
// This gate was widgetlab-only, and the omission was invisible in exactly the way this
// repo keeps rediscovering: buildingpulse — the demo board — published through the same
// mutation with its option bags checked by nothing, while a comment elsewhere claimed
// "the sim's boards are gated". Naming the scenarios that have boards today would
// reproduce the hole the day a fourth one lands, so the set comes from sim.Registry and
// a scenario that declares dashboards is covered by existing.
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
	"rewrite the golden dashboard fixtures from the current builders")

const simDashboardFixtureDir = "../../../../frontend/testdata/sim-dashboards"

// fixtureSeed is fixed so the fixtures are reproducible. Boards bind device tokens,
// which are (manifest, seed) derived, so a fixture generated under a different seed
// would differ in every binding for no reason a reader could see.
const fixtureSeed = 1

// scenarioManifests builds every registered scenario at the fixture seed.
//
// Not every scenario declares dashboards — devicepulse declares none — so this returns
// them all and the callers below decide. A scenario is covered by being REGISTERED, so
// nothing here is a list anyone has to remember to extend.
func scenarioManifests(t *testing.T) map[string]SimManifest {
	t.Helper()
	out := map[string]SimManifest{}
	for id, newDriver := range Registry {
		m := newDriver(fixtureSeed, Load{}).Manifest()
		// Validated HERE rather than in one test, so it also covers the path that WRITES
		// the fixtures: -update-fixtures used to regenerate from manifests nothing had
		// checked, which is the one moment a bad board becomes the committed reference.
		if err := m.Validate(); err != nil {
			t.Fatalf("the %s manifest the fixtures come from does not validate: %v", id, err)
		}
		out[id] = m
	}
	if len(out) == 0 {
		t.Fatal("the registry is empty, so every check below would be vacuous")
	}
	return out
}

// simDashboard is a published board plus the scenario that publishes it. The owner is
// carried rather than looked up again because a board's DEFINITION is only half of what
// a checker needs — the entities it binds come from its scenario's manifest, and
// rediscovering which scenario that was is how the two drift apart.
type simDashboard struct {
	scenario   string
	manifest   SimManifest
	definition string
}

// simDashboardFixtures is every dashboard every registered scenario publishes, keyed by
// token. Tokens are asserted unique across scenarios: they name files in one directory,
// so a collision would silently leave one board's fixture holding another's document.
func simDashboardFixtures(t *testing.T) map[string]simDashboard {
	t.Helper()
	out := map[string]simDashboard{}
	for id, manifest := range scenarioManifests(t) {
		for _, d := range manifest.Dashboards {
			if prev, dup := out[d.Token]; dup {
				t.Fatalf("scenarios %q and %q both declare dashboard %q; the fixture files are "+
					"keyed by token, so one would overwrite the other", prev.scenario, id, d.Token)
			}
			out[d.Token] = simDashboard{scenario: id, manifest: manifest, definition: d.Definition}
		}
	}
	if len(out) == 0 {
		t.Fatal("no registered scenario declares a dashboard, so the fixtures would be empty " +
			"and every check over them vacuous")
	}
	return out
}

func fixturePath(token string) string {
	return filepath.Join(simDashboardFixtureDir, token+".json")
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

// TestSimDashboardFixturesAreCurrent is the staleness gate. It fails when the committed
// fixture no longer matches what the builders produce, which is the only thing
// keeping the TypeScript side honest about a board that has since changed.
//
// Regenerate with:
//
//	go test ./sim/ -run TestSimDashboardFixturesAreCurrent -update-fixtures
func TestSimDashboardFixturesAreCurrent(t *testing.T) {
	fixtures := simDashboardFixtures(t)
	if *updateFixtures {
		// Once per RUN, not once per board: both are whole-directory operations, and
		// removeOrphanFixtures rebuilds every manifest to decide what is orphaned.
		if err := os.MkdirAll(simDashboardFixtureDir, 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		removeOrphanFixtures(t)
	}

	for token, board := range fixtures {
		path := fixturePath(token)
		want := indent(t, board.definition)

		if *updateFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			t.Logf("wrote %s", path)
			continue
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v\nRegenerate with: go test ./sim/ -run "+
				"TestSimDashboardFixturesAreCurrent -update-fixtures", path, err)
		}
		// Compared as VALUES: the committed file is indented and the published
		// document is not, so bytes would differ for a reason that means nothing.
		if !sameJSON(string(got), board.definition) {
			t.Errorf("%s is stale — the builders now produce a different board.\n"+
				"Regenerate with: go test ./sim/ -run TestSimDashboardFixturesAreCurrent "+
				"-update-fixtures", path)
		}
	}
}

// committedFixtureTokens lists the fixture files actually on disk.
func committedFixtureTokens(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(simDashboardFixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", simDashboardFixtureDir, err)
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
	live := simDashboardFixtures(t)
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
func TestSimDashboardFixtureFilesMatchTheDashboardsExactly(t *testing.T) {
	// Declared tokens come from the same collector that writes the files, so this
	// compares disk against exactly what -update-fixtures would produce — and inherits
	// its cross-scenario token-collision check for free.
	declared := map[string]bool{}
	for token := range simDashboardFixtures(t) {
		declared[token] = true
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
				"go test ./sim/ -run TestSimDashboardFixturesAreCurrent -update-fixtures",
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
// 🔴 THE METRIC SET IS PER SCENARIO, AND POOLING IT WOULD BE THE BUG. Every scenario
// declares its own profiles; a union across the registry would let widgetlab's
// `temperature` excuse a buildingpulse board selecting a metric buildingpulse never
// reports — a check that reads as coverage and answers a question nobody asked.
func TestSimBoardsSelectOnlyDeclaredMetrics(t *testing.T) {
	checked := 0
	for id, manifest := range scenarioManifests(t) {
		if len(manifest.Dashboards) == 0 {
			continue
		}
		declared := map[string]bool{}
		for _, p := range manifest.Profiles {
			for _, m := range p.Metrics {
				declared[m.Key] = true
			}
		}
		if len(declared) == 0 {
			t.Fatalf("%s declares dashboards but no profile metrics, so its boards are checked "+
				"against an empty set and every selector passes", id)
		}
		checked += checkBoardMetrics(t, manifest, declared)
	}
	if checked == 0 {
		t.Error("no board selects any measurement at all, so this test asserted nothing")
	}
}

// checkBoardMetrics returns how many measurement selections it examined, so the caller
// can refuse a run in which nothing was checked at all.
func checkBoardMetrics(t *testing.T, manifest SimManifest, declared map[string]bool) int {
	checked := 0
	t.Helper()
	for _, d := range manifest.Dashboards {
		var board struct {
			Widgets []struct {
				Id         string         `json:"id"`
				Type       string         `json:"type"`
				Options    map[string]any `json:"options"`
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
				// 🔴 NOT merely skipped. Every check in this arc shared the shape
				// "no datasource, nothing to say", so a widget that lost its binding
				// entirely passed everything while a widget with a merely mangled
				// selector failed — the strictly worse case being the one that got
				// through. Which types must carry one is decided on the TypeScript
				// side, where WIDGET_BINDS_DATASOURCE is exhaustive over WidgetType;
				// what this side can say is that a widget SELECTING measurements is
				// the only kind whose selector is worth checking, and that a board
				// widget carrying neither a datasource nor a reason is worth naming.
				continue
			}
			for _, name := range w.Datasource.Measurements {
				checked++
				if !declared[name] {
					t.Errorf("%s/%s selects measurement %q, which no profile declares — the "+
						"widget binds a real device and shows nothing", d.Token, w.Id, name)
				}
			}
			// 🔴 options.measurement is what a single-value widget ACTUALLY displays:
			// primaryMeasurementName prefers it over the datasource. A typo there is a
			// perfectly legal string that no schema check can question, and the widget
			// renders an em dash — configured, bound, and showing nothing.
			if name, ok := w.Options["measurement"].(string); ok && !declared[name] {
				t.Errorf("%s/%s displays measurement %q, which no profile declares — it renders "+
					"an em dash on a board that is otherwise correctly bound", d.Token, w.Id, name)
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
	return checked
}

// The gallery's chart is sized for the two series it draws: useMeasurementStream
// trims its buffer across ALL of a widget's measurements, so a third would cut
// each series to two thirds of a sweep period.
//
// Stays widgetlab-specific on purpose: the number 2 is this board's design, not a
// property any scenario's chart has.
func TestWidgetlabGalleryChartDrawsTheSeriesItsWindowIsSizedFor(t *testing.T) {
	manifest := NewWidgetlab(fixtureSeed, Load{}).Manifest()
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
func TestSimDashboardFixturesAreReproducible(t *testing.T) {
	first := simDashboardFixtures(t)
	second := simDashboardFixtures(t)
	if len(first) != len(second) {
		t.Fatalf("two builds produced %d and %d dashboards", len(first), len(second))
	}
	for token, board := range first {
		if second[token].definition != board.definition {
			t.Errorf("dashboard %q differs between two builds at the same seed", token)
		}
	}
}
