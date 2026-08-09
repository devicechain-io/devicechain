// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
)

// boxFence builds a frozen fence set of one axis-aligned lon/lat box.
func boxFence(version int32, token string, lonMin, latMin, lonMax, latMax float64) *geofence.FenceSet {
	geom, err := json.Marshal(map[string]any{
		"kind": geofence.KindPolygon2D,
		"geometry": map[string]any{
			"type": "Polygon",
			"coordinates": [][][2]float64{{
				{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return geofence.NewFenceSet(version, []geofence.SnapshotFence{{Token: token, Geometry: geom}})
}

func at(lon, lat float64) geofence.Position { return geofence.Position{Lat: lat, Lon: lon} }

// TestFenceSetViewRetainsSupersededVersions is the retention obligation itself: an event stamped
// with an OLD version must still be evaluable after a newer version exists. A view holding only
// the current version would answer every slightly-late event against fences it was never resolved
// against — which is exactly the behaviour the stamp exists to prevent.
func TestFenceSetViewRetainsSupersededVersions(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(7, "yard", 0, 0, 1, 1))     // the yard was here...
	v.Put("acme", boxFence(9, "yard", 10, 10, 11, 11)) // ...and then it was moved

	old := v.For("acme", 7)
	if old == nil {
		t.Fatal("version 7 was dropped when version 9 arrived; superseded versions must be retained")
	}
	in, err := old.Contains("yard", at(0.5, 0.5))
	if err != nil || !in {
		t.Fatalf("version 7 does not hold the old geometry: in=%v err=%v", in, err)
	}
	current := v.For("acme", 9)
	if current == nil {
		t.Fatal("the current version is missing")
	}
	if in, err := current.Contains("yard", at(0.5, 0.5)); err != nil || in {
		t.Fatalf("version 9 answered with the OLD geometry: in=%v err=%v", in, err)
	}
}

// TestFenceSetViewEvictsAtItsBound: retention is BOUNDED and the bound is the oldest-first one it
// claims. The check is on both ends — the oldest is gone AND the newest is present — because a
// view that dropped the wrong end would still hold the right number of versions.
func TestFenceSetViewEvictsAtItsBound(t *testing.T) {
	v := NewFenceSetViewRetaining(3)
	for ver := int32(1); ver <= 5; ver++ {
		v.Put("acme", boxFence(ver, "yard", 0, 0, 1, 1))
	}
	if got, want := v.RetainedVersions("acme"), []int32{3, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained %v, want %v (oldest evicted first, bound held)", got, want)
	}
	if v.For("acme", 2) != nil {
		t.Error("an evicted version is still resolvable")
	}
	if v.For("acme", 5) == nil {
		t.Error("the newest version was evicted")
	}
}

// TestFenceSetViewDefaultBound pins the shipped bound, so changing it is a deliberate edit to a
// documented number rather than a side effect.
func TestFenceSetViewDefaultBound(t *testing.T) {
	if MaxRetainedFenceSetVersions != 4 {
		t.Fatalf("MaxRetainedFenceSetVersions = %d, want 4", MaxRetainedFenceSetVersions)
	}
	v := NewFenceSetView()
	for ver := int32(1); ver <= 6; ver++ {
		v.Put("acme", boxFence(ver, "yard", 0, 0, 1, 1))
	}
	if got := len(v.RetainedVersions("acme")); got != MaxRetainedFenceSetVersions {
		t.Fatalf("retained %d versions, want %d", got, MaxRetainedFenceSetVersions)
	}
}

// TestFenceSetViewRePutDoesNotConsumeARetentionSlot: a snapshot of a given version is immutable, so
// a redelivered fact for a version already held is a repeat, not a change. Counting it would evict
// live versions on a redelivery storm.
func TestFenceSetViewRePutDoesNotConsumeARetentionSlot(t *testing.T) {
	v := NewFenceSetViewRetaining(2)
	v.Put("acme", boxFence(1, "yard", 0, 0, 1, 1))
	v.Put("acme", boxFence(2, "yard", 0, 0, 1, 1))
	for i := 0; i < 5; i++ {
		v.Put("acme", boxFence(2, "yard", 0, 0, 1, 1))
	}
	if got, want := v.RetainedVersions("acme"), []int32{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained %v, want %v", got, want)
	}
}

// TestFenceSetViewIsPerTenant: one tenant's fences are not reachable through another tenant's
// lookup, even at the same version number (version numbers are per-tenant and WILL collide).
func TestFenceSetViewIsPerTenant(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(3, "yard", 0, 0, 1, 1))
	v.Put("globex", boxFence(3, "depot", 50, 50, 51, 51))

	if v.For("acme", 3).Fence("depot") != nil {
		t.Error("globex's fence is visible in acme's fence set")
	}
	if v.For("globex", 3).Fence("yard") != nil {
		t.Error("acme's fence is visible in globex's fence set")
	}
	if v.For("thirdparty", 3) != nil {
		t.Error("a tenant with no fence sets resolved one")
	}
	// Positive control: each tenant does see its own, so the negatives above are not vacuous.
	if v.For("acme", 3).Fence("yard") == nil || v.For("globex", 3).Fence("depot") == nil {
		t.Fatal("a tenant cannot see its own fence; the isolation assertions prove nothing")
	}
}

// TestFenceSetViewVersionZeroIsKnownEmpty: 0 is what device-management stamps on an event resolved
// before the tenant ever created a fence. It is KNOWLEDGE (there were no fences), so it resolves
// to an empty set rather than a miss — a rule then gets "no such fence" instead of "the fence set
// is unavailable", and the retention bound can never evict it.
func TestFenceSetViewVersionZeroIsKnownEmpty(t *testing.T) {
	v := NewFenceSetView()
	fs := v.For("acme", 0)
	if fs == nil {
		t.Fatal("version 0 resolved to a miss; it is a known-empty set")
	}
	if fs.Len() != 0 {
		t.Fatalf("version 0 holds %d fences", fs.Len())
	}
	_, err := fs.Contains("yard", at(0, 0))
	if !errors.Is(err, geofence.ErrUnknownFence) {
		t.Errorf("version 0 answered %v, want ErrUnknownFence (the fence did not exist)", err)
	}
	if errors.Is(err, geofence.ErrNoFenceSet) {
		t.Error("version 0 reported the fence set as unavailable; it is known, and empty")
	}
}

// TestFenceSetViewRemoveTenant is the ADR-077 erasure: fence geometry is the tenant's own
// configuration (the coordinates of its sites), so it goes with the tenant, and nothing else
// clears it before a restart.
func TestFenceSetViewRemoveTenant(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(1, "yard", 0, 0, 1, 1))
	v.Put("acme", boxFence(2, "yard", 0, 0, 1, 1))
	v.Put("globex", boxFence(1, "depot", 0, 0, 1, 1))

	if n := v.RemoveTenant("acme"); n != 2 {
		t.Fatalf("RemoveTenant removed %d versions, want 2", n)
	}
	if v.For("acme", 1) != nil || v.For("acme", 2) != nil {
		t.Error("a purged tenant's fence sets are still resolvable")
	}
	if v.For("globex", 1) == nil {
		t.Error("the purge swept another tenant's fence set")
	}
	if n := v.RemoveTenant(""); n != 0 {
		t.Errorf("an empty tenant purged %d versions; it must sweep nothing", n)
	}
}

// stubSource is a FenceSetSource over a fixed (tenant, version) table, counting reads so the
// memoization can be asserted rather than assumed.
type stubSource struct {
	sets  map[string]map[int32]*geofence.FenceSet
	reads int
}

func (s *stubSource) FenceSetAt(_ context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	s.reads++
	byVersion, ok := s.sets[tenant]
	if !ok {
		return nil, errors.New("no such tenant")
	}
	fs, ok := byVersion[version]
	if !ok {
		return nil, errors.New("no such version")
	}
	return fs, nil
}

// TestLoadingFenceSetsResolvesThroughTheSourceAndMemoizes: the off-loop resolver reads each version
// once, including the versions it FAILS to read — a preview over ten thousand events stamped with a
// missing version must issue one failed read, not ten thousand.
func TestLoadingFenceSetsResolvesThroughTheSourceAndMemoizes(t *testing.T) {
	src := &stubSource{sets: map[string]map[int32]*geofence.FenceSet{
		"acme": {7: boxFence(7, "yard", 0, 0, 1, 1)},
	}}
	l := NewLoadingFenceSets(src, "acme")

	for i := 0; i < 3; i++ {
		fs := l.Resolve(context.Background(), 7)
		if fs == nil {
			t.Fatal("a resolvable version did not resolve")
		}
	}
	for i := 0; i < 3; i++ {
		if fs := l.Resolve(context.Background(), 8); fs != nil {
			t.Fatal("an unreadable version resolved to a fence set")
		}
	}
	if src.reads != 2 {
		t.Errorf("source was read %d times, want 2 (one per distinct version, failures included)", src.reads)
	}
	// Version 0 never reaches the source: it is known-empty by construction.
	if fs := l.Resolve(context.Background(), 0); fs == nil || fs.Len() != 0 {
		t.Fatalf("version 0 did not resolve to the known-empty set: %v", fs)
	}
	if src.reads != 2 {
		t.Errorf("version 0 caused a source read; it is known without one")
	}
}

// TestLoadingFenceSetsIsTenantScopedForLife: the resolver is bound to one tenant when it is built,
// so no version a replayed event carries can steer it to another tenant's fences.
func TestLoadingFenceSetsIsTenantScopedForLife(t *testing.T) {
	src := &stubSource{sets: map[string]map[int32]*geofence.FenceSet{
		"acme":   {7: boxFence(7, "yard", 0, 0, 1, 1)},
		"globex": {7: boxFence(7, "depot", 50, 50, 51, 51)},
	}}
	l := NewLoadingFenceSets(src, "acme")
	fs := l.Resolve(context.Background(), 7)
	if fs == nil {
		t.Fatal("acme's version 7 did not resolve")
	}
	if fs.Fence("depot") != nil {
		t.Error("globex's fence reached acme's resolver")
	}
	if fs.Fence("yard") == nil {
		t.Fatal("acme's own fence is missing; the isolation assertion proves nothing")
	}
}

// TestLoadingFenceSetsWithNoSourceIsHonest: with no source wired every version is unresolvable —
// nil, which the predicate reports as ErrNoFenceSet — rather than an empty set, which would read
// as "the tenant has no fences" and make every geofence rule quietly never fire.
func TestLoadingFenceSetsWithNoSourceIsHonest(t *testing.T) {
	l := NewLoadingFenceSets(nil, "acme")
	if fs := l.Resolve(context.Background(), 7); fs != nil {
		t.Error("an unwired resolver produced a fence set")
	}
	if fs := l.Resolve(context.Background(), 0); fs == nil || fs.Len() != 0 {
		t.Error("version 0 is knowable without a source and must still be the known-empty set")
	}
}
