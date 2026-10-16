// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"maps"
	"slices"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// enableAreaState is the State `dcctl enable-area local guide <areas>` builds before the
// cluster reads: the declaration of a Ready default-profile instance applied with the
// request, over the credentials a real instance's document carries (compactState's), so
// what it renders is what the chart would be handed.
func enableAreaState(t *testing.T, inst *dcv1beta1.Instance, areas ...string) (*State, error) {
	t.Helper()
	st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
	return st, applyUpgradeDeclaration(st, inst, UpgradeOptions{
		Options:     Options{Instance: "guide"},
		EnableAreas: areas,
	})
}

// readyGuide is guideDeclaration whose last run ended Ready.
func readyGuide() *dcv1beta1.Instance {
	inst := guideDeclaration()
	setPhase(inst, dcv1beta1.PhaseReady)
	return inst
}

// 🔴 THE FEATURE, BY VALUE, THROUGH THE CHART. An instance bootstrapped on the default
// profile is asked for update-management; the chart rendered from what the run
// composes must deploy it, alongside every area the profile already ran.
func TestEnablingAnAreaDeploysItAlongsideTheProfile(t *testing.T) {
	st, err := enableAreaState(t, readyGuide(), "update-management")
	if err != nil {
		t.Fatalf("settling the request: %v", err)
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	vals, _, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	rendered := renderedFunctionalAreas(manifest)
	if !slices.Contains(rendered, "update-management") {
		t.Errorf("the chart rendered %v: update-management was asked for and is not deployed", rendered)
	}
	for _, a := range []string{"user-management", "device-management", "event-management", "event-processing"} {
		if !slices.Contains(rendered, a) {
			t.Errorf("the chart rendered %v: %s, which the profile already ran, was dropped", rendered, a)
		}
	}
	if _, has := vals["profile"]; has {
		t.Errorf("the values set profile=%v beside an explicit area set; the chart refuses both", vals["profile"])
	}
}

// 🔴 THE AREA MUST OUTLIVE THE NEXT ORDINARY UPGRADE. The declaration is what every
// later run reads its area set from, so an enable-area that deployed the area without
// recording it would have it pruned by the next `dcctl upgrade` — Helm deletes what
// leaves the manifest. Written as the two-run sequence, as the version's own test is.
func TestAnEnabledAreaSurvivesTheNextUpgrade(t *testing.T) {
	dyn := declaring(t, atVersion(t, "ghcr.io/devicechain-io", "v1.2.0"))

	first := upgradingTo("ghcr.io/devicechain-io", "v1.2.0")
	first.EnableAreas = []string{"update-management"}
	first.AreaChange = &AreaChange{Requested: first.EnableAreas, Added: first.EnableAreas}
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", first); err != nil {
		t.Fatalf("recording the enabled area: %v", err)
	}

	inst := readBack(t, dyn, "prod")
	if got := inst.Spec.ExtraFunctionalAreas; !slices.Equal(got, []string{"update-management"}) {
		t.Fatalf("the declaration records extra areas %v, want [update-management]", got)
	}

	// Run two: an ordinary upgrade, no areas named.
	st := &State{Instance: "prod", Values: map[string]string{}}
	if err := applyUpgradeDeclaration(st, inst, UpgradeOptions{}); err != nil {
		t.Fatalf("settling what the declaration says: %v", err)
	}
	if !slices.Contains(st.EnabledAreas, "update-management") {
		t.Errorf("the next upgrade deploys %v: the area enable-area added would be pruned", st.EnabledAreas)
	}
}

// The counterweight to the write above: only enable-area writes the delta. A plain
// upgrade leaves it as it was, even from a State that failed to carry it across — so
// recording a version can never write an instance's extra areas away.
func TestAPlainUpgradeLeavesTheDeclaredAreasAlone(t *testing.T) {
	before := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
	before.Spec.ExtraFunctionalAreas = []string{"lwm2m-ingest"}
	dyn := declaring(t, before)

	st := upgradingTo("ghcr.io/devicechain-io", "v1.3.0") // EnableAreas deliberately empty
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Spec.ExtraFunctionalAreas; !slices.Equal(got, []string{"lwm2m-ingest"}) {
		t.Errorf("a plain upgrade left extra areas %v, want [lwm2m-ingest]", got)
	}
}

// An enable-area with nothing new to record over a declaration that already says it
// writes nothing: no provenance bump for a re-apply.
func TestAnEnableAreaWithNothingNewWritesNothing(t *testing.T) {
	before := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
	before.Spec.ExtraFunctionalAreas = []string{"mcp"}
	dyn := declaring(t, before)
	at := readBack(t, dyn, "prod").Annotations[dcv1beta1.AnnotationLastAppliedAt]

	st := upgradingTo("ghcr.io/devicechain-io", "v1.2.0")
	st.EnableAreas = []string{"mcp"}
	st.AreaChange = &AreaChange{Requested: []string{"mcp"}}
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	after := readBack(t, dyn, "prod")
	if after.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseReady ||
		after.Annotations[dcv1beta1.AnnotationLastAppliedAt] != at {
		t.Errorf("a no-op enable-area rewrote the declaration: %v", after.Annotations)
	}
}

func TestPlanAreaChange(t *testing.T) {
	for _, c := range []struct {
		name               string
		profile            string
		delta, requested   []string
		wantDelta, wantAdd []string
		wantErr            string
	}{
		{name: "a new area is appended", profile: "default",
			requested: []string{"update-management"},
			wantDelta: []string{"update-management"}, wantAdd: []string{"update-management"}},
		{name: "the existing delta is kept, first", profile: "default",
			delta: []string{"lwm2m-ingest"}, requested: []string{"mcp"},
			wantDelta: []string{"lwm2m-ingest", "mcp"}, wantAdd: []string{"mcp"}},
		{name: "an area the profile runs is not recorded", profile: "default",
			requested: []string{"device-management"}},
		{name: "an area already in the delta is not repeated", profile: "default",
			delta: []string{"mcp"}, requested: []string{"mcp"},
			wantDelta: []string{"mcp"}},
		{name: "only the new half of a mixed request is added", profile: "default",
			delta: []string{"mcp"}, requested: []string{"mcp", "update-management", "event-processing"},
			wantDelta: []string{"mcp", "update-management"}, wantAdd: []string{"update-management"}},
		{name: "an unknown area is refused with the catalog", profile: "default",
			requested: []string{"update-managment"}, wantErr: "update-management"},
		{name: "an unmet hard dependency is refused", profile: "telemetry",
			requested: []string{"outbound-connectors"}, wantErr: `requires "event-processing"`},
		{name: "the dependency named in the same request is met", profile: "telemetry",
			requested: []string{"event-processing", "outbound-connectors"},
			wantDelta: []string{"event-processing", "outbound-connectors"},
			wantAdd:   []string{"event-processing", "outbound-connectors"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			delta, added, err := planAreaChange(c.profile, c.delta, c.requested)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(delta, c.wantDelta) {
				t.Errorf("delta = %v, want %v", delta, c.wantDelta)
			}
			if !slices.Equal(added, c.wantAdd) {
				t.Errorf("added = %v, want %v", added, c.wantAdd)
			}
		})
	}
}

// 🔴 "ALREADY DECLARED" IS ONLY "NOTHING TO DO" OVER A READY INSTANCE. The declaration
// is written before anything moves, so a run that died half-way leaves the area declared
// over an instance that never got it — and a re-run that read that as settled would
// leave it undeployed for good while saying it was enabled.
func TestNothingToDoOnlyWhenTheAreaIsDeclaredAndTheLastRunEndedReady(t *testing.T) {
	declared := func(phase string) *dcv1beta1.Instance {
		inst := guideDeclaration()
		inst.Spec.ExtraFunctionalAreas = []string{"update-management"}
		setPhase(inst, phase)
		return inst
	}
	for _, c := range []struct {
		name    string
		inst    *dcv1beta1.Instance
		areas   []string
		settled bool
	}{
		{"declared, Ready", declared(dcv1beta1.PhaseReady), []string{"update-management"}, true},
		{"in the profile, Ready", readyGuide(), []string{"device-management"}, true},
		{"declared, the last run Failed", declared(dcv1beta1.PhaseFailed), []string{"update-management"}, false},
		{"declared, an upgrade that never ended", declared(dcv1beta1.PhaseUpgrading), []string{"update-management"}, false},
		{"new to a Ready instance", readyGuide(), []string{"mcp"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := enableAreaState(t, c.inst, c.areas...)
			if err != nil {
				t.Fatal(err)
			}
			if st.AreaChange == nil {
				t.Fatal("an enable-area request left no AreaChange on the State")
			}
			if st.AreaChange.Settled != c.settled {
				t.Errorf("settled = %v, want %v", st.AreaChange.Settled, c.settled)
			}
		})
	}

	// And an unfinished first bootstrap is never settled, even reading Ready-adjacent.
	inst := declared(dcv1beta1.PhaseFailed)
	inst.Annotations[dcv1beta1.AnnotationBootstrapUnfinished] = dcv1beta1.BootstrapUnfinished
	st, err := enableAreaState(t, inst, "update-management")
	if err != nil {
		t.Fatal(err)
	}
	if st.AreaChange.Settled {
		t.Error("an instance whose first bootstrap never finished was read as having nothing to do")
	}
}

// A plain upgrade carries no AreaChange: the enable-area output and its nothing-to-do
// return are reachable only from the verb that asked for them.
func TestAPlainUpgradeIsNotAnAreaChange(t *testing.T) {
	st := &State{Instance: "guide", Provider: "local", Values: map[string]string{}}
	if err := applyUpgradeDeclaration(st, readyGuide(), UpgradeOptions{Options: Options{Instance: "guide"}}); err != nil {
		t.Fatal(err)
	}
	if st.AreaChange != nil {
		t.Errorf("a plain upgrade carries an AreaChange: %+v", st.AreaChange)
	}
}

// 🔴 ENABLING AN AREA MUST NOT MOVE THE VERSION. The run renders this dcctl's chart and
// applies this dcctl's infrastructure configuration, so over an instance on another
// release it would move both while leaving the images — a version change made by the
// command that promises not to make one.
func TestEnableAreaRefusesAnInstanceOnAnotherRelease(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })

	DefaultImageVersion = "v0.22.0" // guideDeclaration runs v0.21.1
	_, err := enableAreaState(t, readyGuide(), "update-management")
	if err == nil {
		t.Fatal("enable-area from a v0.22.0 dcctl over a v0.21.1 instance was let through")
	}
	for _, want := range []string{"v0.21.1", "v0.22.0", "dcctl upgrade local guide --version v0.22.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	DefaultImageVersion = "v0.21.1"
	if _, err := enableAreaState(t, readyGuide(), "update-management"); err != nil {
		t.Errorf("the matching release was refused: %v", err)
	}

	// A dcctl built from source names no release, and is what a developer uses on
	// images they built: it is let through.
	DefaultImageVersion = "dev"
	if _, err := enableAreaState(t, readyGuide(), "update-management"); err != nil {
		t.Errorf("a source-built dcctl was refused: %v", err)
	}
}

func TestCheckAreaRequest(t *testing.T) {
	got, err := CheckAreaRequest([]string{" mcp ", "update-management", "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"mcp", "update-management"}) {
		t.Errorf("normalized to %v, want [mcp update-management]", got)
	}
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, "at least one"},
		{[]string{"mcp", " "}, "empty"},
		{[]string{"frobnicate"}, `"frobnicate"`},
	} {
		_, err := CheckAreaRequest(c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("CheckAreaRequest(%q) = %v, want an error naming %s", c.in, err, c.want)
		}
	}
	// The refusal answers with the catalog, not a pointer elsewhere.
	_, err = CheckAreaRequest([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "update-management") || !strings.Contains(err.Error(), "ai-inference") {
		t.Errorf("an unknown area's refusal does not list the catalog: %v", err)
	}
}

// update-management added to an instance with no object store runs and refuses every
// artifact. The run says so, because nothing it does can add a store.
func TestAddingUpdatesWithoutAnObjectStoreIsSaid(t *testing.T) {
	bare := readyGuide()
	bare.Spec.ObjectStore = nil
	st, err := enableAreaState(t, bare, "update-management")
	if err != nil {
		t.Fatal(err)
	}
	notes := areaChangeNotes(st)
	if len(notes) != 1 || !strings.Contains(notes[0], "object store") {
		t.Errorf("notes = %q, want one about the missing object store", notes)
	}

	// The control: the same request over an instance with a store says nothing.
	st, err = enableAreaState(t, readyGuide(), "update-management")
	if err != nil {
		t.Fatal(err)
	}
	if notes := areaChangeNotes(st); len(notes) != 0 {
		t.Errorf("an instance with an object store was told it has none: %q", notes)
	}
	// And an area that does not need one says nothing either.
	st, err = enableAreaState(t, bare, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if notes := areaChangeNotes(st); len(notes) != 0 {
		t.Errorf("mcp was told about an object store: %q", notes)
	}
}
