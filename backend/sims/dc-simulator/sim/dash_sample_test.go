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
// 🔴 RUN THESE WITH -count=1 WHILE CHANGING SAMPLES — INCLUDING WHEN ONLY EDITING ONE.
// `go test` does not track files outside the module at all, and everything this file
// reads is outside it, so a cached pass replays over a broken sample. CI passes
// -count=1 for this reason (the go job says why); locally it is discipline.
//
// The trap when checking that for yourself: two runs with different -run or -v
// arguments are different cache entries, and a FAILING result is never cached — so a
// mutation sequence that varies either one never tests the cache at all. That is how
// the first version of this comment came to state the opposite, confidently.

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
		if e.IsDir() {
			continue
		}
		// Both walkers — this one and the TypeScript half — select on the .json
		// extension, so a sample saved as .JSON or .txt is a file someone can still find
		// and paste while every check here skips it in silence. There is nothing else a
		// sample directory legitimately holds.
		if filepath.Ext(e.Name()) != ".json" {
			t.Errorf("%s is not a .json file; both sample gates select on that extension, so "+
				"it would be pastable and checked by nothing", filepath.Join(dir, e.Name()))
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
// A scoped slot resolves relative to its parent anchor. Under the 'manual' strategy —
// resolveContextBindings in frontend/packages/dashboards/src/context.ts — the cascade
// keeps the child's binding only while that device is still a member of the parent's
// area, and otherwise leaves the slot UNBOUND. So a manifest that re-points the parent
// to another area and leaves the child on a device belonging to the old one parses
// cleanly, names two entities that both exist, and still renders the child's widgets
// empty. The pair has to be checked, not each token separately.
//
// 🔴 THE STRATEGY DECIDES WHETHER THE SAMPLE MEANS ANYTHING, so it is read rather than
// assumed. Under 'first' the cascade ignores the child's binding outright and derives
// the parent's first member instead — a sample naming a device there is a line that
// reads as configuration and changes nothing on screen, which is worse than a wrong
// token because nothing ever contradicts it. Those are refused below rather than
// membership-checked, because membership is not the question for them.
//
// Nothing here uses `continue` for a shape it does not expect. An earlier version
// skipped a child bound to an anchor (or a parent bound to a device) as uninteresting,
// which made the type confusion — the likeliest copy-paste slip in a manifest, since
// the two stanzas sit one above the other — invisible to this file. It was caught only
// by the aggregate "asserted nothing" guard at the bottom, and one VALID sample
// alongside a broken one was enough to satisfy that and turn the whole gate green.
func TestDashSampleScopedSlotsStayInsideTheirParent(t *testing.T) {
	boards := simDashboardFixtures(t)
	resolved := 0
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
				parentName := spec.Scope.Parent

				// A binding the cascade discards is not a binding. Refused rather than
				// checked, and refused whether or not the token is any good.
				if spec.Scope.Strategy == "first" {
					resolved++
					if _, named := manifest[slot]; named {
						t.Errorf("%s binds %q, but that slot is scoped 'first' — the cascade "+
							"derives it from %q's first member and ignores what a manifest says, "+
							"so this line changes nothing a viewer can see",
							where, slot, parentName)
					}
					continue
				}

				// A slot the sample does not name still resolves — to the board's own
				// default — so the pair to check is the EFFECTIVE binding on both ends,
				// which is what makes a sample that rebinds only the parent a real case
				// rather than one that slips through.
				child, childOK := effectiveBinding(manifest, slot, spec)
				parent, parentOK := effectiveBinding(manifest, parentName, def.Slots[parentName])
				if !childOK {
					// Not counted: this branch asserts nothing, and a guard that counted
					// it would be satisfied by a corpus in which every scoped slot was
					// unbound — the cannot-fail shape the guard exists to prevent.
					// Neither the sample nor the board binds it. It renders as an empty
					// placeholder until someone picks in the viewer — a board-authoring
					// matter, not something a sample can fix, so there is no pair here.
					continue
				}
				resolved++
				if !parentOK {
					t.Errorf("%s leaves %q — the parent of scoped slot %q — bound to nothing; "+
						"the cascade cannot derive a child from an unbound parent, so %q renders "+
						"empty", where, parentName, slot, slot)
					continue
				}
				if child.Kind != "device" {
					t.Errorf("%s binds scoped slot %q with kind %q, but a scoped slot resolves to "+
						"a DEVICE; the cascade drops a binding of any other kind and the slot "+
						"renders empty", where, slot, child.Kind)
					continue
				}
				if parent.Kind != "anchor" || parent.Anchor == nil {
					t.Errorf("%s binds %q — an anchor slot, and the parent of %q — with kind %q; "+
						"the cascade derives a child only from an anchor parent",
						where, parentName, slot, parent.Kind)
					continue
				}
				if parent.Anchor.TargetType != "area" {
					// check() already refuses every target type the sim never assigns to,
					// so reaching here means a type it does assign but membership is not
					// area assignment. There is none today; erroring keeps it from being
					// waved through the day there is.
					t.Errorf("%s binds %q to a %s anchor; scoped-slot membership here is area "+
						"assignment, so this pair is unchecked",
						where, parentName, parent.Anchor.TargetType)
					continue
				}
				device, exists := devices[child.DeviceToken]
				if !exists {
					continue // reported by TestDashSampleBindingsNameRealEntities
				}
				area, err := deviceAreaToken(device)
				if err != nil || area != parent.Anchor.TargetToken {
					t.Errorf("%s binds %q to device %q, which is not in area %q that slot %q binds — "+
						"the cascade drops the device and every widget on it renders empty",
						where, slot, child.DeviceToken, parent.Anchor.TargetToken, parentName)
				}
			}
		}
	}
	// A board that stopped declaring scoped slots — or a sample set that stopped covering
	// one — would leave this test green having examined nothing. Counted at the top of the
	// walk rather than at the membership check, because refusing a 'first'-scoped binding
	// is real work too: a corpus made entirely of those has nothing left to compare, and
	// failing it would be a complaint about the boards rather than about the samples.
	if resolved == 0 {
		t.Error("no sample resolved a scoped slot at all, so this test asserted nothing")
	}
}

// effectiveBinding is a slot's binding under a sample: the sample's override if it
// names the slot, otherwise the board's own default.
//
// 🔴 This is the Go side of effectiveBindings in frontend/packages/dashboards/src/
// bindings.ts, and the coupling is unavoidable rather than chosen: the checks here run
// where the entities are known, which is Go, and that function cannot be called from
// here. If its merge rule changes, change this with it — otherwise this gate keeps
// checking a different pair than the viewer renders.
func effectiveBinding(manifest map[string]dashboardSlotBinding, name string, spec dashboardSlot) (dashboardSlotBinding, bool) {
	if b, ok := manifest[name]; ok {
		return b, true
	}
	if spec.DefaultBinding != nil {
		return *spec.DefaultBinding, true
	}
	return dashboardSlotBinding{}, false
}

// entityIndex is everything a scenario bootstraps that a binding may name.
type entityIndex struct {
	devices   map[string]bool
	areas     map[string]bool
	customers map[string]bool
}

func knownEntities(m SimManifest) entityIndex {
	idx := entityIndex{
		devices:   map[string]bool{},
		areas:     map[string]bool{},
		customers: map[string]bool{},
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
		// Only the target types the sim actually ASSIGNS devices to. Assets are
		// bootstrapped as entities and never assigned to (renderAssignments builds area
		// and customer assignments only), so an asset anchor names something real and
		// resolves to no members — an entity check on the token alone would wave it
		// through, which is the same shape as accepting a relationship nothing creates.
		var pool map[string]bool
		switch b.Anchor.TargetType {
		case "area":
			pool = idx.areas
		case "customer":
			pool = idx.customers
		default:
			return fmt.Errorf("anchor targetType %q, which is not an entity type a sim "+
				"assignment targets — the anchor would resolve to no members",
				b.Anchor.TargetType)
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
