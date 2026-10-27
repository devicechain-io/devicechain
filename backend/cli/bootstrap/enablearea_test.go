// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	t.Parallel()
	// The telemetry profile is the one that does not already run update-management.
	inst := readyGuide()
	inst.Spec.Profile = "telemetry"
	st, err := enableAreaState(t, inst, "update-management")
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
	for _, a := range []string{"user-management", "device-management", "event-management", "device-state"} {
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
			requested: []string{"outbound-connectors"},
			wantDelta: []string{"outbound-connectors"}, wantAdd: []string{"outbound-connectors"}},
		{name: "the existing delta is kept, first", profile: "default",
			delta: []string{"lwm2m-ingest"}, requested: []string{"mcp"},
			wantDelta: []string{"lwm2m-ingest", "mcp"}, wantAdd: []string{"mcp"}},
		{name: "an area the profile runs is not recorded", profile: "default",
			requested: []string{"device-management"}},
		{name: "an area already in the delta is not repeated", profile: "default",
			delta: []string{"mcp"}, requested: []string{"mcp"},
			wantDelta: []string{"mcp"}},
		{name: "only the new half of a mixed request is added", profile: "default",
			delta: []string{"mcp"}, requested: []string{"mcp", "outbound-connectors", "event-processing"},
			wantDelta: []string{"mcp", "outbound-connectors"}, wantAdd: []string{"outbound-connectors"}},
		{name: "update-management, in the default profile, is not recorded", profile: "default",
			requested: []string{"update-management"}},
		{name: "update-management is recorded where the profile lacks it", profile: "telemetry",
			requested: []string{"update-management"},
			wantDelta: []string{"update-management"}, wantAdd: []string{"update-management"}},
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
		inst.Spec.ExtraFunctionalAreas = []string{"outbound-connectors"}
		setPhase(inst, phase)
		return inst
	}
	for _, c := range []struct {
		name    string
		inst    *dcv1beta1.Instance
		areas   []string
		settled bool
	}{
		{"declared, Ready", declared(dcv1beta1.PhaseReady), []string{"outbound-connectors"}, true},
		{"in the profile, Ready", readyGuide(), []string{"device-management"}, true},
		{"declared, the last run Failed", declared(dcv1beta1.PhaseFailed), []string{"outbound-connectors"}, false},
		{"declared, an upgrade that never ended", declared(dcv1beta1.PhaseUpgrading), []string{"outbound-connectors"}, false},
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
	st, err := enableAreaState(t, inst, "outbound-connectors")
	if err != nil {
		t.Fatal(err)
	}
	if st.AreaChange == nil {
		t.Fatal("an enable-area request left no AreaChange on the State")
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
	bare.Spec.Profile = "telemetry" // the default profile already runs update-management
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
	withStore := readyGuide()
	withStore.Spec.Profile = "telemetry"
	st, err = enableAreaState(t, withStore, "update-management")
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

// 🔴 A CHART REFUSAL OF THE NEW AREA SET IS MADE BEFORE THE DECLARATION IS WRITTEN. An
// instance on a cluster dcctl was only pointed at, with a ReadWriteOnce filesystem store,
// mounts that store in user-management alone; update-management mounts it too, and the
// chart refuses two pods on one ReadWriteOnce claim. Found at the Helm step, that refusal
// comes after the declaration already records update-management — so the instance is left
// Failed with an area every later `dcctl upgrade` re-renders and is refused over, and there
// is no command that takes it back out. It has to be found here, while nothing is written.
func TestEnableAreaRefusesAnAreaSetTheObjectStoreCannotMount(t *testing.T) {
	pointedAt := func(accessMode string) *dcv1beta1.Instance {
		inst := readyGuide()
		inst.Spec.Profile = "telemetry" // the default profile already runs update-management
		inst.Spec.Managed = false
		inst.Spec.ObjectStore = &dcv1beta1.ObjectStoreSpec{
			Backend: dcv1beta1.ObjectStoreFilesystem, Size: "1Gi", AccessMode: accessMode,
		}
		return inst
	}

	precheck := func(inst *dcv1beta1.Instance, areas ...string) error {
		t.Helper()
		st, err := enableAreaState(t, inst, areas...)
		if err != nil {
			t.Fatalf("settling %v: %v", areas, err)
		}
		return precheckAreaChange(t.Context(), st)
	}

	err := precheck(pointedAt(""), "update-management")
	if err == nil {
		t.Fatal("update-management was let through onto a ReadWriteOnce store user-management already mounts")
	}
	if !strings.Contains(err.Error(), "ReadWriteOnce") || !strings.Contains(err.Error(), "update-management") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	// The controls: a store that can be shared takes the area, and an area that does not
	// mount the store is not judged by it.
	if err := precheck(pointedAt("ReadWriteMany"), "update-management"); err != nil {
		t.Errorf("a ReadWriteMany store was refused: %v", err)
	}
	if err := precheck(pointedAt(""), "mcp"); err != nil {
		t.Errorf("mcp, which mounts no store, was refused: %v", err)
	}
	// And dcctl's own cluster colocates the pods, so the same store takes the area there.
	colocated := readyGuide()
	colocated.Spec.Profile = "telemetry"
	if err := precheck(colocated, "update-management"); err != nil {
		t.Errorf("a colocated store on dcctl's own cluster was refused: %v", err)
	}
	// On the default profile the area is already part of the instance (the request adds
	// nothing), and it runs without the store rather than being refused: see updateStoreMountless.
	def := pointedAt("")
	def.Spec.Profile = "default"
	if err := precheck(def, "update-management"); err != nil {
		t.Errorf("the default profile's own update-management was refused on a ReadWriteOnce store: %v", err)
	}
}

// The check above is worth nothing unless Upgrade asks it before the declaration is
// written: after, its refusal is the permanent one it exists to prevent. Held by the
// source, as the teardown refusal's order is, because Upgrade itself needs a cluster.
func TestTheAreaPrecheckIsMadeBeforeTheDeclarationIsWritten(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var precheck, record token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name != "Upgrade" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			switch {
			case id.Name == "precheckAreaChange" && precheck == token.NoPos:
				precheck = call.Pos()
			case id.Name == "recordUpgradedVersion" && record == token.NoPos:
				record = call.Pos()
			}
		}
		return true
	})
	if record == token.NoPos {
		t.Fatal("Upgrade no longer calls recordUpgradedVersion; this test is looking at the wrong thing")
	}
	if precheck == token.NoPos {
		t.Fatal("Upgrade does not call precheckAreaChange: an area the object store cannot take is refused only after it is recorded")
	}
	if precheck > record {
		t.Errorf("Upgrade calls precheckAreaChange (%s) after recordUpgradedVersion (%s)",
			fset.Position(precheck), fset.Position(record))
	}
}

// 🔴 THE VERB HOLDS THE IMAGES, NOT ITS CALLER. An image source supplied beside the areas
// would become the version the release refusal compares, and the run a version move.
func TestEnableAreaRefusesAnImageSourceBesideTheAreas(t *testing.T) {
	for _, o := range []Options{
		{Instance: "guide", ImageVersion: "v0.22.0"},
		{Instance: "guide", ImageRegistry: "registry.example.com/dc"},
	} {
		st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
		err := applyUpgradeDeclaration(st, readyGuide(), UpgradeOptions{Options: o, EnableAreas: []string{"mcp"}})
		if err == nil || !strings.Contains(err.Error(), "dcctl upgrade") {
			t.Errorf("enable-area with %+v: err = %v, want a refusal naming `dcctl upgrade`", o, err)
		}
	}
	// The counterweight: a plain upgrade still takes them.
	st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
	if err := applyUpgradeDeclaration(st, readyGuide(), UpgradeOptions{
		Options: Options{Instance: "guide", ImageVersion: "v0.22.0"}}); err != nil || st.ImageVersion != "v0.22.0" {
		t.Errorf("a plain upgrade to v0.22.0: err = %v, version %q", err, st.ImageVersion)
	}
}

// An instance built with --build runs "dev", which no registry publishes. enable-area
// builds no image, so it is refused — in enable-area's terms, not with the upgrade's
// advice to pass flags this verb does not have.
func TestEnableAreaOverSourceBuiltImagesSaysWhatToDo(t *testing.T) {
	inst := readyGuide()
	inst.Spec.ImageRegistry, inst.Spec.ImageVersion = LocalRegistry, "dev"
	_, err := enableAreaState(t, inst, "mcp")
	if err == nil {
		t.Fatal("enable-area over images tagged dev was let through")
	}
	if !strings.Contains(err.Error(), "builds no image") || !strings.Contains(err.Error(), "dcctl upgrade local guide") {
		t.Errorf("the refusal does not say what enable-area cannot do and what to run: %v", err)
	}
}

// A dcctl with no image version at all is a broken stamp, not a source build: the release
// refusal must not read it as "no release, let it through".
func TestEnableAreaRefusesADcctlWithNoImageVersion(t *testing.T) {
	saved := DefaultImageVersion
	t.Cleanup(func() { DefaultImageVersion = saved })
	DefaultImageVersion = ""
	if _, err := enableAreaState(t, readyGuide(), "mcp"); err == nil || !strings.Contains(err.Error(), "no image version") {
		t.Errorf("an unstamped dcctl: err = %v, want a refusal", err)
	}
}

func TestAddingLwm2mDayTwoIsSaid(t *testing.T) {
	st, err := enableAreaState(t, readyGuide(), "lwm2m-ingest")
	if err != nil {
		t.Fatal(err)
	}
	notes := areaChangeNotes(st)
	if len(notes) != 1 || !strings.Contains(notes[0], "--lwm2m-identities") {
		t.Errorf("notes = %q, want one about the pre-shared keys", notes)
	}
}

// 🔴 A PLAIN UPGRADE ASKS THE SAME QUESTION BEFORE IT RECORDS ANYTHING. A new chart over an
// instance whose declaration already carries update-management, on a cluster dcctl was only
// pointed at with a ReadWriteOnce store, is refused by the chart at the Helm step: after
// recordUpgradedVersion has written a version that never happened.
func TestUpgradeRefusesAnAreaSetTheObjectStoreCannotMountBeforeRecording(t *testing.T) {
	inst := readyGuide()
	inst.Spec.Managed = false
	inst.Spec.ExtraFunctionalAreas = []string{"update-management"}
	inst.Spec.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "1Gi"}
	st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
	if err := applyUpgradeDeclaration(st, inst, UpgradeOptions{Options: Options{Instance: "guide"}}); err != nil {
		t.Fatal(err)
	}
	err := precheckUpgradeMount(t.Context(), st)
	if err == nil || !strings.Contains(err.Error(), "ReadWriteOnce") || !strings.Contains(err.Error(), "Nothing has been changed") {
		t.Fatalf("a plain upgrade onto an unmountable area set was let through: %v", err)
	}

	// The control: the same instance with a store that can be shared.
	inst.Spec.ObjectStore.AccessMode = "ReadWriteMany"
	st = &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
	if err := applyUpgradeDeclaration(st, inst, UpgradeOptions{Options: Options{Instance: "guide"}}); err != nil {
		t.Fatal(err)
	}
	if err := precheckUpgradeMount(t.Context(), st); err != nil {
		t.Errorf("a ReadWriteMany store was refused on a plain upgrade: %v", err)
	}
}

// Upgrade must make that check before the version is recorded, and must act on its answer.
// Held by the source, as above, because Upgrade itself needs a cluster: hydrateUpgradeState
// reads the operator, the declaration and the credentials back before the branch is reached.
//
// Order alone is not enough. A call whose error is dropped (`_ = precheckUpgradeMount(...)`),
// or one nested under a further condition (`if opts.DryRun {...}`), still comes first in the
// source and refuses nothing. So the call must be the init of an `if err != nil` that
// returns, standing directly in the plain-upgrade branch of `if st.AreaChange != nil`.
func TestTheUpgradeMountPrecheckPrecedesTheRecordedVersion(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var upgrade *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "Upgrade" {
			upgrade = fn
		}
	}
	if upgrade == nil {
		t.Fatal("upgrade.go declares no Upgrade; this test is looking at the wrong thing")
	}
	callsNamed := func(n ast.Node, name string) (pos []token.Pos) {
		ast.Inspect(n, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
					pos = append(pos, call.Pos())
				}
			}
			return true
		})
		return pos
	}
	records := callsNamed(upgrade.Body, "recordUpgradedVersion")
	if len(records) == 0 {
		t.Fatal("Upgrade no longer calls recordUpgradedVersion; this test is looking at the wrong thing")
	}

	// The branch: the first `if st.AreaChange != nil { ... } else { <plain upgrade> }` at the top level.
	var plain *ast.BlockStmt
	for _, stmt := range upgrade.Body.List {
		ifs, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		if c, ok := ifs.Cond.(*ast.BinaryExpr); ok && c.Op == token.NEQ {
			if sel, ok := c.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "AreaChange" && plain == nil {
				plain, _ = ifs.Else.(*ast.BlockStmt)
			}
		}
	}
	if plain == nil {
		t.Fatal("Upgrade has no plain-upgrade branch (the else of `if st.AreaChange != nil`); this test is looking at the wrong thing")
	}

	var guard *ast.IfStmt
	for _, stmt := range plain.List {
		ifs, ok := stmt.(*ast.IfStmt)
		if !ok || ifs.Init == nil || len(callsNamed(ifs.Init, "precheckUpgradeMount")) == 0 {
			continue
		}
		guard = ifs
	}
	if guard == nil {
		t.Fatalf("the plain-upgrade branch does not refuse on precheckUpgradeMount directly "+
			"(`if err := precheckUpgradeMount(ctx, st); err != nil { return err }`); calls found anywhere in Upgrade: %d",
			len(callsNamed(upgrade.Body, "precheckUpgradeMount")))
	}
	assign, ok := guard.Init.(*ast.AssignStmt)
	cond, cok := guard.Cond.(*ast.BinaryExpr)
	if !ok || len(assign.Lhs) != 1 || !cok || cond.Op != token.NEQ {
		t.Fatalf("the precheck at %s is not `if err := ...; err != nil`", fset.Position(guard.Pos()))
	}
	lhs, lok := assign.Lhs[0].(*ast.Ident)
	cx, xok := cond.X.(*ast.Ident)
	if !lok || !xok || lhs.Name != cx.Name {
		t.Fatalf("the precheck at %s tests something other than its own error", fset.Position(guard.Pos()))
	}
	returns := false
	for _, s := range guard.Body.List {
		if r, ok := s.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			if id, ok := r.Results[0].(*ast.Ident); ok && id.Name == lhs.Name {
				returns = true
			}
		}
	}
	if !returns {
		t.Fatalf("the precheck at %s does not return its refusal", fset.Position(guard.Pos()))
	}
	if guard.Pos() > records[0] {
		t.Errorf("Upgrade refuses on precheckUpgradeMount (%s) after recordUpgradedVersion (%s)",
			fset.Position(guard.Pos()), fset.Position(records[0]))
	}
}
