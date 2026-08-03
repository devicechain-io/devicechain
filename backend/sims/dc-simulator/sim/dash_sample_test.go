// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ---- The /dash paste samples ---------------------------------------------------
//
// frontend/testdata/dash-samples holds binding manifests a person pastes into the
// standalone viewer alongside a committed board definition. They are the only
// artifact in the repo that hands someone a device token and says "paste this" —
// which makes them the one place a token can rot with no symptom but an empty board.
//
// 🔴 A STALE SAMPLE LOOKS EXACTLY LIKE A BROKEN VIEWER. Every widget renders as an
// empty frame, the parse succeeded, no error is reported anywhere, and the reasonable
// conclusion is that /dash does not work. That is the failure this file exists to make
// impossible, and it can only be checked HERE: the entities a sample names exist
// because a scenario's manifest expands to them, and nothing on the TypeScript side
// knows what a manifest expands to.
//
// The other half — that the samples survive the viewer's own paste path — is
// frontend/apps/dashboard/src/load.test.ts, which drives the real loadDashboard. Split
// this way because neither side can do the other's job, not for tidiness.
//
// 🔴 LOCALLY, RUN THESE WITH -count=1 WHILE CHANGING SAMPLES. The samples live outside
// this module, and `go test` caching notices a file whose CONTENTS changed but not a
// directory whose LISTING did — so adding, removing or renaming a sample can replay a
// cached "ok" from before the change. CI is unaffected (it starts with a cold cache),
// which is what makes this a trap rather than a bug: the green you get is local only.

const dashSampleDir = "../../../../frontend/testdata/dash-samples"

// dashSampleBoards lists the sample directories on disk. Each is named for the
// dashboard token whose definition its manifests bind, which is what pairs a sample
// with a board without a lookup table anyone has to maintain.
func dashSampleBoards(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(dashSampleDir)
	if err != nil {
		t.Fatalf("read sample dir %s: %v", dashSampleDir, err)
	}
	var boards []string
	for _, e := range entries {
		if e.IsDir() {
			boards = append(boards, e.Name())
		}
	}
	// Without this every check below is a loop over nothing, reported green — and the
	// samples going missing is precisely the state that would produce it.
	if len(boards) == 0 {
		t.Fatalf("no sample directories under %s, so this file checks nothing", dashSampleDir)
	}
	return boards
}

// dashSampleManifests reads one board's sample files as slot → binding maps.
func dashSampleManifests(t *testing.T, board string) map[string]map[string]dashboardSlotBinding {
	t.Helper()
	dir := filepath.Join(dashSampleDir, board)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]map[string]dashboardSlotBinding{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var manifest map[string]dashboardSlotBinding
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("%s is not a JSON object of slot → binding: %v", path, err)
		}
		out[e.Name()] = manifest
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no .json manifests, so it covers nothing", dir)
	}
	return out
}

// parseDefinition reads a published board back into the structs that built it, so the
// checks below can ask about slots and their scopes.
func parseDefinition(t *testing.T, token, definition string) dashboardDefinition {
	t.Helper()
	var def dashboardDefinition
	if err := json.Unmarshal([]byte(definition), &def); err != nil {
		t.Fatalf("dashboard %q does not round-trip through its own structs: %v", token, err)
	}
	return def
}

// TestDashSamplesNameBoardsThatStillExist is the staleness gate on the pairing itself.
// A directory that names no board is a sample for something renamed or deleted, and
// every token inside it is unchecked by the entity checks below — which walk boards,
// not directories, so they would pass in silence.
func TestDashSamplesNameBoardsThatStillExist(t *testing.T) {
	boards := simDashboardFixtures(t)
	for _, name := range dashSampleBoards(t) {
		if _, ok := boards[name]; !ok {
			t.Errorf("%s names dashboard %q, which no registered scenario declares",
				filepath.Join(dashSampleDir, name), name)
		}
	}
}

// TestDashSamplesOnlyCoverFixedTopologyScenarios keeps a sample from binding tokens
// that are only right at one device count.
//
// A resizable scenario's populations are rendered from the operator's --devices, so
// `bp-therm-004` names a real thermostat at the default and nothing at all when the
// same scenario is run smaller. The sample would be correct here, correct in CI, and
// an empty board for the person who ran it their way. FixedTopology scenarios refuse a
// resize outright, so their tokens are the same everywhere — which is the only ground
// on which a checked-in token can be promised.
func TestDashSamplesOnlyCoverFixedTopologyScenarios(t *testing.T) {
	boards := simDashboardFixtures(t)
	for _, name := range dashSampleBoards(t) {
		board, ok := boards[name]
		if !ok {
			continue // reported by TestDashSamplesNameBoardsThatStillExist
		}
		if !board.manifest.FixedTopology {
			t.Errorf("%s samples a board owned by %q, whose device set is resized by --devices; "+
				"its tokens cannot be promised to exist, so it must not carry a paste sample",
				filepath.Join(dashSampleDir, name), board.scenario)
		}
	}
}

// TestDashSampleBindingsNameRealEntities is the check the whole file is for: every
// token a sample hands someone to paste is an entity its scenario actually bootstraps.
func TestDashSampleBindingsNameRealEntities(t *testing.T) {
	boards := simDashboardFixtures(t)
	for _, name := range dashSampleBoards(t) {
		board, ok := boards[name]
		if !ok {
			continue
		}
		known := knownEntities(board.manifest)
		for file, manifest := range dashSampleManifests(t, name) {
			where := filepath.Join(dashSampleDir, name, file)
			if len(manifest) == 0 {
				t.Errorf("%s binds no slots at all, so pasting it does nothing a reader could "+
					"tell from pasting nothing", where)
			}
			for slot, binding := range manifest {
				if err := known.check(binding); err != nil {
					t.Errorf("%s binds slot %q to %v", where, slot, err)
				}
			}
		}
	}
}

// TestDashSampleScopedSlotsStayInsideTheirParent is the subtle one, and the reason a
// sample cannot just be a plausible-looking pair of tokens.
//
// A scoped slot resolves relative to its parent anchor: the cascade keeps a device
// binding only while that device is still a member of the parent's area. So a manifest
// that re-points the parent to another area and leaves the child on a device belonging
// to the old one parses cleanly, names two entities that both exist, and still renders
// the child's widgets empty. The pair has to be checked, not each token separately.
func TestDashSampleScopedSlotsStayInsideTheirParent(t *testing.T) {
	boards := simDashboardFixtures(t)
	checked := 0
	for _, name := range dashSampleBoards(t) {
		board, ok := boards[name]
		if !ok {
			continue
		}
		def := parseDefinition(t, name, board.definition)
		devices := map[string]DeviceInstance{}
		for _, d := range board.manifest.Expand(fixtureSeed) {
			devices[d.Token] = d
		}

		for file, manifest := range dashSampleManifests(t, name) {
			where := filepath.Join(dashSampleDir, name, file)
			for slot, spec := range def.Slots {
				if spec.Scope == nil || spec.Scope.Parent == "" {
					continue
				}
				// A slot the sample does not name still resolves — to the board's own
				// default — so the pair to check is the EFFECTIVE binding on both ends,
				// which is what makes a sample that rebinds only the parent a real case
				// rather than one that slips through.
				child, childOK := effectiveBinding(manifest, slot, spec)
				parentName := spec.Scope.Parent
				parent, parentOK := effectiveBinding(manifest, parentName, def.Slots[parentName])
				if !childOK || !parentOK {
					continue // an unbound end resolves at mount; there is no pair yet
				}
				if child.Kind != "device" || parent.Kind != "anchor" || parent.Anchor == nil {
					continue
				}
				if parent.Anchor.TargetType != "area" {
					continue // membership below is area assignment; other anchors resolve elsewhere
				}
				checked++
				device, exists := devices[child.DeviceToken]
				if !exists {
					continue // reported by TestDashSampleBindingsNameRealEntities
				}
				if !assignedTo(device, "area", parent.Anchor.TargetToken) {
					t.Errorf("%s binds %q to device %q, which is not in area %q that slot %q binds — "+
						"the cascade drops the device and every widget on it renders empty",
						where, slot, child.DeviceToken, parent.Anchor.TargetToken, spec.Scope.Parent)
				}
			}
		}
	}
	// The checks above are all inside a `continue`-heavy walk, so a definition that
	// stopped declaring scoped slots — or a sample set that stopped covering one —
	// would leave this test green having compared nothing.
	if checked == 0 {
		t.Error("no sample resolved a scoped slot against a parent anchor, so this test " +
			"asserted nothing")
	}
}

// effectiveBinding is a slot's binding under a sample: the sample's override if it
// names the slot, otherwise the board's own default.
func effectiveBinding(manifest map[string]dashboardSlotBinding, name string, spec dashboardSlot) (dashboardSlotBinding, bool) {
	if b, ok := manifest[name]; ok {
		return b, true
	}
	if spec.DefaultBinding != nil {
		return *spec.DefaultBinding, true
	}
	return dashboardSlotBinding{}, false
}

func assignedTo(d DeviceInstance, targetType, targetToken string) bool {
	for _, a := range d.Assignments {
		if a.TargetType == targetType && a.TargetToken == targetToken {
			return true
		}
	}
	return false
}

// entityIndex is everything a scenario bootstraps that a binding may name.
type entityIndex struct {
	devices   map[string]bool
	areas     map[string]bool
	customers map[string]bool
	assets    map[string]bool
}

func knownEntities(m SimManifest) entityIndex {
	idx := entityIndex{
		devices:   map[string]bool{},
		areas:     map[string]bool{},
		customers: map[string]bool{},
		assets:    map[string]bool{},
	}
	for _, d := range m.Expand(fixtureSeed) {
		idx.devices[d.Token] = true
	}
	for _, a := range m.Areas {
		idx.areas[a.Token] = true
	}
	for _, c := range m.Customers {
		idx.customers[c.Token] = true
	}
	for _, a := range m.Assets {
		idx.assets[a.Token] = true
	}
	return idx
}

// check reports why a binding names nothing the scenario bootstraps, or nil.
func (idx entityIndex) check(b dashboardSlotBinding) error {
	switch b.Kind {
	case "device":
		if b.DeviceToken == "" {
			return fmt.Errorf("kind \"device\" with no deviceToken")
		}
		if !idx.devices[b.DeviceToken] {
			return fmt.Errorf("device %q, which the scenario does not bootstrap", b.DeviceToken)
		}
	case "anchor":
		if b.Anchor == nil {
			return fmt.Errorf("kind \"anchor\" with no anchor")
		}
		// The sim's only tracked relationship is the assignment one; an anchor on any
		// other relationship resolves to no members however real its target is.
		if b.Anchor.Relationship != assignmentRelationshipType {
			return fmt.Errorf("relationship %q, but the sim only creates %q relationships",
				b.Anchor.Relationship, assignmentRelationshipType)
		}
		var pool map[string]bool
		switch b.Anchor.TargetType {
		case "area":
			pool = idx.areas
		case "customer":
			pool = idx.customers
		case "asset":
			pool = idx.assets
		default:
			return fmt.Errorf("anchor targetType %q, which is not an entity type a sim "+
				"assignment targets", b.Anchor.TargetType)
		}
		if !pool[b.Anchor.TargetToken] {
			return fmt.Errorf("%s %q, which the scenario does not bootstrap",
				b.Anchor.TargetType, b.Anchor.TargetToken)
		}
	default:
		return fmt.Errorf("kind %q, which the binding parser drops", b.Kind)
	}
	return nil
}
