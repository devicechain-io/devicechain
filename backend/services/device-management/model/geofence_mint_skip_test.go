// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

// A fence-set version exists to make a STAMP resolve to the right shapes. These tests pin
// the consequence: an authoring action that leaves the (token, geometry) list exactly what
// the current version already froze mints nothing, announces nothing and evicts nothing —
// while anything that genuinely changes the set still does all three.
//
// Every one of them carries the positive control in the same test, because "nothing
// happened" is the answer a broken mint path gives to every question. A test that only
// asserted the skip would pass just as happily against a mint path that had stopped
// working altogether.

// mintSkipHarness wires an api with both recorders and seeds one fence, returning the
// state each test measures against.
type mintSkipHarness struct {
	api   *Api
	ctx   context.Context
	facts *captureFenceSets
	// evictor is the package's shared CacheEvictor recorder. Its fenceSetEvicts counter is
	// what the skip is mostly there to move: an eviction is a tenant-wide ProfileScope drop
	// costing the resolve hot path a miss per device type.
	evictor *captureEvictor
}

func newMintSkipHarness(t *testing.T) *mintSkipHarness {
	t.Helper()
	h := &mintSkipHarness{
		api:     newFenceSetEmitTestApi(t),
		ctx:     core.WithTenant(context.Background(), "acme"),
		facts:   &captureFenceSets{},
		evictor: &captureEvictor{},
	}
	h.api.GeoFenceSetPublisher = h.facts
	h.api.CacheEvictor = h.evictor
	return h
}

// observe reads the three things an authoring action can move.
func (h *mintSkipHarness) observe(t *testing.T) (version int32, facts int, evictions int) {
	t.Helper()
	v, err := h.api.CurrentFenceSetVersion(h.ctx)
	if err != nil {
		t.Fatalf("current fence set version: %v", err)
	}
	return v, len(h.facts.events), h.evictor.fenceSetEvicts
}

// The first fence a tenant ever authors always mints: there is no current version to be
// equal to. Asserted on its own because the skip's comparison has to treat "no version
// yet" as a mint, and getting that wrong would leave a tenant with fences and no version —
// a state the console reads as truncated history.
func TestTheFirstFenceAlwaysMints(t *testing.T) {
	h := newMintSkipHarness(t)

	if v, facts, evictions := h.observe(t); v != 0 || facts != 0 || evictions != 0 {
		t.Fatalf("a tenant with no fences: version=%d facts=%d evictions=%d, want 0/0/0", v, facts, evictions)
	}
	if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	v, facts, evictions := h.observe(t)
	if v != 1 || facts != 1 || evictions != 1 {
		t.Fatalf("after the first fence: version=%d facts=%d evictions=%d, want 1/1/1", v, facts, evictions)
	}
}

// An edit that changes only the NAME and METADATA mints nothing. Neither appears in the
// stored snapshot or in the manifest built from it, so no stamp could resolve differently
// for having minted one.
//
// The control is the second half: the same fence, same api, given genuinely different
// geometry, still mints, announces and evicts. Without it this test passes against a mint
// path that has stopped minting for any reason at all.
func TestAnEditChangingOnlyNameAndMetadataMintsNothing(t *testing.T) {
	h := newMintSkipHarness(t)
	geometry := boxGeometry(0, 0, 1, 1)
	if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: geometry}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, factsBefore, evictionsBefore := h.observe(t)

	name := "North Yard"
	metadata := `{"owner":"logistics"}`
	if _, err := h.api.UpdateGeoFence(h.ctx, "yard", &GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(geometry), Name: dcgraphql.OptionalStringOf(name), Metadata: dcgraphql.OptionalStringOf(metadata)}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	after, factsAfter, evictionsAfter := h.observe(t)
	if after != before {
		t.Errorf("renaming a fence moved the fence-set version %d -> %d; a name is in neither the "+
			"snapshot nor the manifest, so no stamp resolves differently for it", before, after)
	}
	if factsAfter != factsBefore {
		t.Errorf("renaming a fence announced %d fact(s); a version that was not minted must not be "+
			"announced", factsAfter-factsBefore)
	}
	if evictionsAfter != evictionsBefore {
		t.Errorf("renaming a fence forced %d tenant-wide cache eviction(s) for a change no cached "+
			"value depended on", evictionsAfter-evictionsBefore)
	}

	// The control. A real geometry change still does all three.
	if _, err := h.api.UpdateGeoFence(h.ctx, "yard", geoFenceEdit(boxGeometry(10, 10, 11, 11))); err != nil {
		t.Fatalf("control update: %v", err)
	}
	controlVersion, controlFacts, controlEvictions := h.observe(t)
	if controlVersion != before+1 || controlFacts != factsBefore+1 || controlEvictions != evictionsBefore+1 {
		t.Fatalf("a genuine geometry change gave version=%d facts=%d evictions=%d, want %d/%d/%d — "+
			"the skip above proves nothing if the mint path is simply dead",
			controlVersion, controlFacts, controlEvictions, before+1, factsBefore+1, evictionsBefore+1)
	}
}

// Resubmitting a fence's EXISTING geometry byte-for-byte mints nothing. This is the shape a
// console save produces when the user opened the editor and saved without moving anything,
// and it is the one an earlier version of this suite performed by accident while believing
// it was advancing a version.
func TestResubmittingIdenticalGeometryMintsNothing(t *testing.T) {
	h := newMintSkipHarness(t)
	geometry := boxGeometry(2, 2, 3, 3)
	if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
		Token: "dock", Geometry: geometry}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, factsBefore, evictionsBefore := h.observe(t)

	for i := 0; i < 3; i++ {
		if _, err := h.api.UpdateGeoFence(h.ctx, "dock", geoFenceEdit(geometry)); err != nil {
			t.Fatalf("resubmit %d: %v", i, err)
		}
	}

	after, factsAfter, evictionsAfter := h.observe(t)
	if after != before || factsAfter != factsBefore || evictionsAfter != evictionsBefore {
		t.Fatalf("three identical resubmits moved version %d -> %d, facts %d -> %d, evictions %d -> %d; "+
			"all three should sit still", before, after, factsBefore, factsAfter, evictionsBefore, evictionsAfter)
	}

	// 🔴 THE ENGINE RETAINS FOUR VERSIONS PER TENANT. Three no-op saves used to mint three
	// versions, which is enough on its own to evict a fence set still being used by an event
	// in flight. The control confirms the path is alive.
	if _, err := h.api.UpdateGeoFence(h.ctx, "dock", geoFenceEdit(boxGeometry(4, 4, 5, 5))); err != nil {
		t.Fatalf("control update: %v", err)
	}
	// All three, not just the version: a mint path that still wrote rows but had stopped
	// announcing or evicting would satisfy a version-only control while leaving the engine
	// on a stale fence set.
	v, f, e := h.observe(t)
	if v != before+1 || f != factsBefore+1 || e != evictionsBefore+1 {
		t.Fatalf("a real move gave version=%d facts=%d evictions=%d, want %d/%d/%d",
			v, f, e, before+1, factsBefore+1, evictionsBefore+1)
	}
}

// 🔴 VERSION NUMBERS STAY DENSE ACROSS SKIPS. The console's history panel walks versions
// from the latest down to 1 one at a time, so a skip that ALLOCATED a number and discarded
// it would leave a hole the panel reads as truncated history. Skipping must mint nothing,
// not mint-and-throw-away.
func TestSkippedMintsLeaveNoHolesInTheVersionSequence(t *testing.T) {
	h := newMintSkipHarness(t)
	if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: boxGeometry(0, 0, 1, 1)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Interleave no-ops with real changes; only the real ones should consume a number.
	name := "renamed"
	steps := []struct {
		geometry string
		name     *string
		mints    bool
	}{
		{boxGeometry(0, 0, 1, 1), &name, false},
		{boxGeometry(6, 6, 7, 7), nil, true},
		{boxGeometry(6, 6, 7, 7), nil, false},
		{boxGeometry(8, 8, 9, 9), nil, true},
	}
	want := int32(1)
	for i, step := range steps {
		if _, err := h.api.UpdateGeoFence(h.ctx, "yard", &GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(step.geometry), Name: optionalStr(step.name)}); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if step.mints {
			want++
		}
		if v, _, _ := h.observe(t); v != want {
			t.Fatalf("after step %d the version is %d, want %d", i, v, want)
		}
	}

	// Every version from 1 to the latest resolves — which is what "no holes" means to the
	// caller that actually walks them.
	for v := int32(1); v <= want; v++ {
		if _, err := h.api.GeoFenceSetSnapshotAt(h.ctx, v); err != nil {
			t.Fatalf("version %d does not resolve, so the sequence 1..%d has a hole: %v", v, want, err)
		}
	}
}

// 🔴 A CURRENT SNAPSHOT THAT CANNOT BE DECODED MINTS; IT NEVER REFUSES. The comparison has
// to read the stored snapshot, which introduces a way for a corrupt row to reach the WRITE
// path for the first time. Propagating that error would block every fence mutation the
// tenant makes, permanently, with no route out through the API — so a decode failure is
// treated as "not equal".
//
// Note what that fixes and what it does not. Authoring recovers and the CURRENT set decodes,
// which is what this asserts. The corrupt row itself is untouched and stays lethal to a
// history walk, to a replay reaching it, and to any event already stamped with it — so what
// is asserted below is a HEAD repair, not a repair.
func TestAnUndecodableCurrentSnapshotMintsRatherThanRefusing(t *testing.T) {
	h := newMintSkipHarness(t)
	geometry := boxGeometry(0, 0, 1, 1)
	if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
		Token: "yard", Geometry: geometry}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _, _ := h.observe(t)

	if err := h.api.RDB.DB(h.ctx).Model(&GeoFenceSetVersion{}).
		Where("version = ?", before).
		Update("snapshot", datatypes.JSON([]byte(`{"fences":[{`))).Error; err != nil {
		t.Fatalf("corrupt the current snapshot: %v", err)
	}

	// The same rename that would be skipped against a readable snapshot must now succeed
	// AND mint, because an undecodable snapshot cannot be shown equal to anything.
	name := "North Yard"
	if _, err := h.api.UpdateGeoFence(h.ctx, "yard", &GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(geometry), Name: dcgraphql.OptionalStringOf(name)}); err != nil {
		t.Fatalf("a corrupt current snapshot must not block authoring: %v", err)
	}
	v, facts, evictions := h.observe(t)
	if v != before+1 {
		t.Fatalf("version after the corrupt-snapshot edit is %d, want %d", v, before+1)
	}
	// The repair has to REACH the engine, not merely reach the table. A version minted and
	// never announced leaves event-processing resolving against the fence set it already
	// held, which is the stale state the corrupt row was supposed to be recovered from.
	if facts != 2 || evictions != 2 {
		t.Fatalf("the repair mint announced %d fact(s) and forced %d eviction(s), want 2 and 2 "+
			"(one from the seed, one from the repair)", facts, evictions)
	}
	// The repair is real: the new current version decodes and names the fence.
	snapshot, err := h.api.CurrentGeoFenceSetSnapshot(h.ctx)
	if err != nil {
		t.Fatalf("the freshly minted version must decode: %v", err)
	}
	if len(snapshot.Fences) != 1 || snapshot.Fences[0].Token != "yard" {
		t.Fatalf("the repaired snapshot holds %v", snapshot.Fences)
	}
}

// 🔴 EVERY OTHER TEST IN THIS FILE HOLDS EXACTLY ONE FENCE, WHICH LEAVES THE COMPARISON
// EXERCISED ONLY ON LISTS OF LENGTH ONE. On a one-element list, comparing just the first
// element IS comparing the whole list — so a comparison narrowed to a subset, or one that
// checked the multiset of hashes rather than which fence holds which, passes all of them
// while silently skipping mints in production. That is the missed mint this change claims
// to have made unreachable, so it needs a case with more than one fence.
//
// Three steps, each pinning a different way the pairing can change while something about
// the set stays constant:
//
//   - SWAP the two fences' geometries, one update at a time. Note this is NOT a
//     multiset-preserving change: the API moves one fence per call, so each half of the swap
//     changes the set of hashes as well as the pairing. Both halves must mint.
//   - Give one fence the geometry the OTHER already has. The hash is already present in
//     the snapshot and in the archive, so a comparison keyed on "are these hashes known"
//     would skip. Must mint.
//   - Rename one of them. Nothing in the pairing moved. Must skip — which also shows the
//     skip still works once the list is longer than one.
func TestTheComparisonIsOverThePairingNotTheSetOfShapes(t *testing.T) {
	h := newMintSkipHarness(t)
	north := boxGeometry(0, 0, 1, 1)
	south := boxGeometry(20, 20, 21, 21)
	for _, seed := range []struct{ token, geometry string }{{"north", north}, {"south", south}} {
		if _, err := h.api.CreateGeoFence(h.ctx, &GeoFenceCreateRequest{
			Token: seed.token, Geometry: seed.geometry}); err != nil {
			t.Fatalf("seed %s: %v", seed.token, err)
		}
	}
	version, facts, evictions := h.observe(t)

	// Step 1 — swap. Same hashes, different owners.
	if _, err := h.api.UpdateGeoFence(h.ctx, "north", geoFenceEdit(south)); err != nil {
		t.Fatalf("swap north: %v", err)
	}
	if _, err := h.api.UpdateGeoFence(h.ctx, "south", geoFenceEdit(north)); err != nil {
		t.Fatalf("swap south: %v", err)
	}
	afterSwap, factsAfterSwap, evictionsAfterSwap := h.observe(t)
	if afterSwap != version+2 {
		t.Fatalf("swapping two fences' geometries moved the version %d -> %d, want %d: the set of "+
			"shapes is unchanged but which fence has which is not, and containment answers "+
			"differently for both", version, afterSwap, version+2)
	}
	if factsAfterSwap != facts+2 || evictionsAfterSwap != evictions+2 {
		t.Fatalf("the swap announced %d fact(s) and forced %d eviction(s), want 2 and 2",
			factsAfterSwap-facts, evictionsAfterSwap-evictions)
	}

	// Step 2 — give north the shape south already holds. Every hash in the resulting set was
	// already in the previous one; only north's ref changed.
	if _, err := h.api.UpdateGeoFence(h.ctx, "north", geoFenceEdit(north)); err != nil {
		t.Fatalf("converge north: %v", err)
	}
	afterConverge, _, _ := h.observe(t)
	if afterConverge != afterSwap+1 {
		t.Fatalf("giving one fence a shape already present in the set left the version at %d, "+
			"want %d: the hash was already archived, but this fence's pairing changed",
			afterConverge, afterSwap+1)
	}

	// Step 3 — the skip still holds with two fences in the list.
	name := "North Yard"
	if _, err := h.api.UpdateGeoFence(h.ctx, "north", &GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(north), Name: dcgraphql.OptionalStringOf(name)}); err != nil {
		t.Fatalf("rename north: %v", err)
	}
	if v, _, _ := h.observe(t); v != afterConverge {
		t.Fatalf("renaming one of two fences moved the version %d -> %d", afterConverge, v)
	}
}
