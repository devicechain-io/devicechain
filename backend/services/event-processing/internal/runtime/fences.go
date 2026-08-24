// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"sort"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
)

// MaxRetainedFenceSetVersions is how many fence-set versions the view keeps PER TENANT: the
// current one plus MaxRetainedFenceSetVersions-1 superseded ones.
//
// 🔴 RETAINING SUPERSEDED VERSIONS IS THE OBLIGATION, NOT AN OPTIMIZATION. An event stamped with
// version 7 must still be evaluable after version 9 exists, because the stamp is the whole
// determinism mechanism (ADR-078): dropping a superseded version turns "the fences that were live
// when this happened" into "the fences that are live now", which is precisely the answer the
// stamp was introduced to stop giving. A view holding only the current version would be
// indistinguishable from having no stamp at all on every event that is even slightly late.
//
// WHY A COUNT AND NOT A DURATION. Versions are minted by AUTHORING ACTIONS, not by the clock. A
// time-based bound ("keep an hour") retains unboundedly many versions during a burst of edits —
// exactly when memory matters — and none at all through a quiet week, when a single week-old
// in-flight event still needs one. A count bounds the quantity that actually costs something.
//
// WHY 4. Live evaluation needs the CURRENT version: an event is stamped at resolve time with the
// version that was current then, so the only events carrying a superseded one are those still in
// flight between device-management's resolve and this engine's fan-out, plus whatever the
// engine's allowed lateness admits — seconds to minutes, during which an operator would have to
// make FOUR separate fence edits to outrun the cache. The worst-case cost is bounded by the
// authoring limits (100 fences × 512 positions per tenant): 4 versions × 100 × 512 vertices at
// 24 bytes per unit vector is ~5 MB per tenant at the absolute authoring ceiling, and kilobytes
// for a realistic tenant with a handful of fences of a few dozen vertices each.
//
// WHY IT CAN BE THIS SMALL. Eviction here truncates no history: the frozen snapshots are durable
// in device-management, so anything that reaches further back than the live cache — a replay, an
// authoring preview over last week — resolves through a FenceSetSource instead (see
// LoadingFenceSets), which is not on the single-writer loop and may block. This view is a live
// cache, not the archive. A live miss is reported as an error (inFence answers ErrNoFenceSet),
// never as "no fences", so the bound is visible when it bites rather than silently wrong.
const MaxRetainedFenceSetVersions = 4

// FenceSetSource resolves the FROZEN fence set of one (tenant, fence-set version) — the fences
// that were live at the instant an event stamped with that version was resolved. It is the seam
// onto device-management's GeoFenceSetSnapshotAt (ADR-078).
//
// 🔴 IT IS A BLOCKING READ AND MUST NEVER BE CALLED FROM THE SINGLE-WRITER LOOP. The live fan-out
// reads FenceSetView, which is pure memory; a source read belongs to startup warming, to an
// off-loop prefetch, or to an authoring-time job like preview.
type FenceSetSource interface {
	FenceSetAt(ctx context.Context, tenant string, version int32) (*geofence.FenceSet, error)
}

// CurrentFenceSetSource resolves a tenant's CURRENT frozen fence set — the version a
// location event resolved right now would be stamped with, and the fences that version froze.
//
// It is a SEPARATE interface from FenceSetSource, not a second method on it, because the two
// have different callers and only one of them is allowed to exist. FenceSetSource answers
// "what was true at version N", which is the question a preview or a replay asks and the only
// question that is deterministic. This one answers "what is true now", which is a question the
// evaluation path must NEVER ask — it is the exact mistake the version stamp exists to prevent.
// Its legitimate callers both SEED a live cache rather than evaluate anything — the startup
// reconcile, and the published-rule fact handler when a tenant's first fence rule arrives (a
// tenant that had no fence rule at startup is not in the reconcile's list, and nothing else would
// ever fill its view). A type that cannot be reached from the fan-out keeps the distinction
// enforced by the compiler rather than by a comment.
//
// 🔴 IT IS A BLOCKING READ AND MUST NEVER BE CALLED FROM THE SINGLE-WRITER LOOP, for the same
// reason FenceSetSource must not be.
type CurrentFenceSetSource interface {
	CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error)
}

// FenceManifestResolver assembles an evaluable fence set from a manifest the caller ALREADY
// HOLDS — the fence-set fact, which names a version's fences and the content address of each
// one's geometry without carrying the geometry itself.
//
// It is a third interface rather than a method on either source above because it answers a
// different question from both. The two sources answer "go and find out what version N is",
// which costs a read of the archive; this one answers "here is what version N is, make it
// evaluable", which costs only the geometry the holder does not already have. That difference
// is the entire economy of manifest delivery: an edit to one fence of a hundred announces a
// hundred names and transfers ONE body, and routing the fact through a source would throw that
// away by re-reading the manifest that just arrived.
//
// Its implementation shares a compiled-geometry cache with the sources, which is what makes the
// two roads reinforce rather than duplicate each other — a body fetched for a fact is already
// held when the next reconcile sweep asks for the same version.
type FenceManifestResolver interface {
	ResolveManifest(ctx context.Context, tenant string, manifest *dmmodel.GeoFenceSetManifest) (*geofence.FenceSet, error)
}

// FenceSetView is the loop-owned in-memory projection of tenants' frozen geofence sets, keyed on
// (tenant, fence-set version) — the read side a location event's containment predicate resolves
// against. It is the geofence sibling of DeviceAttributeView and shares its concurrency contract:
// it runs ONLY on the processor's single-writer loop (and the startup reconcile that precedes
// it), the same goroutine that reads it on the hot path, so it needs no lock.
//
// It differs from every other projection in this package in one way, and the difference is the
// point: it is keyed on a VERSION as well as a tenant, and it deliberately retains SUPERSEDED
// versions. Every other view holds only the current state of the thing it mirrors, because every
// other view answers "what is true now". This one answers "what was true when that event
// happened", which is a different question and cannot be served by a current-state map.
type FenceSetView struct {
	byTenant map[string]*tenantFenceSets
	// retain is the per-tenant version bound; 0 means MaxRetainedFenceSetVersions.
	retain int
}

// tenantFenceSets is one tenant's retained versions, plus their order so eviction is a decision
// rather than a map-iteration accident.
type tenantFenceSets struct {
	byVersion map[int32]*geofence.FenceSet
	// versions is ascending, so the lowest (oldest) is evicted first.
	versions []int32
}

// NewFenceSetView builds an empty view retaining MaxRetainedFenceSetVersions versions per tenant.
func NewFenceSetView() *FenceSetView { return NewFenceSetViewRetaining(MaxRetainedFenceSetVersions) }

// NewFenceSetViewRetaining builds a view with an explicit retention bound. A non-positive bound
// falls back to the default rather than retaining nothing — a view that retains zero versions
// would answer ErrNoFenceSet for every event, which is a configuration mistake that must not be
// expressible.
func NewFenceSetViewRetaining(retain int) *FenceSetView {
	if retain <= 0 {
		retain = MaxRetainedFenceSetVersions
	}
	return &FenceSetView{byTenant: map[string]*tenantFenceSets{}, retain: retain}
}

// Put installs one tenant's frozen fence set for its version, evicting the OLDEST retained
// version when the tenant is over the bound. Re-putting a version already held replaces it in
// place and does not consume a retention slot (a snapshot of a given version is immutable, so a
// repeat is a redelivery, not a change).
func (v *FenceSetView) Put(tenant string, fs *geofence.FenceSet) {
	if tenant == "" || fs == nil {
		return
	}
	t := v.byTenant[tenant]
	if t == nil {
		t = &tenantFenceSets{byVersion: map[int32]*geofence.FenceSet{}}
		v.byTenant[tenant] = t
	}
	if _, held := t.byVersion[fs.Version()]; !held {
		t.versions = append(t.versions, fs.Version())
		sort.Slice(t.versions, func(i, j int) bool { return t.versions[i] < t.versions[j] })
	}
	t.byVersion[fs.Version()] = fs
	for len(t.versions) > v.retain {
		oldest := t.versions[0]
		t.versions = t.versions[1:]
		delete(t.byVersion, oldest)
	}
}

// For returns the frozen fence set of a (tenant, version), or nil when the view does not hold it.
//
// 🔴 VERSION 0 IS NOT A MISS. 0 is the stamp device-management writes on an event resolved when
// the tenant had never created a fence, so the answer is a KNOWN-EMPTY set — knowledge, not
// absence. A rule naming a fence then gets "no such fence", which is true, instead of "the fence
// set is unavailable", which would be a lie about a fact that is perfectly well known. It is
// answered without consulting the map, so it costs nothing and cannot be evicted.
func (v *FenceSetView) For(tenant string, version int32) *geofence.FenceSet {
	if version <= 0 {
		return geofence.EmptyFenceSet(0)
	}
	t := v.byTenant[tenant]
	if t == nil {
		return nil
	}
	return t.byVersion[version]
}

// ForEvent resolves the fence set ONE resolved event must be evaluated against.
//
// 🔴 IT IS THE VERSION STAMPED ON THE EVENT, NEVER THE TENANT'S CURRENT ONE. This one line is the
// entire determinism mechanism of geofencing (ADR-078): the stamp was written into the immutable
// event at resolve time precisely so that evaluating it later — a replay, a late arrival, a
// preview of last week — asks about the fences that were live THEN. Reaching for the current
// version instead would look identical in every live test (the current version IS the stamped one
// for a fresh event) and would silently answer a different question for every event that is even
// slightly late. It is a named function rather than an inlined field read so that the invariant
// has somewhere to be stated, and somewhere to be broken on purpose.
func (v *FenceSetView) ForEvent(tenant string, ev *dmmodel.ResolvedEvent) *geofence.FenceSet {
	if ev == nil {
		return nil
	}
	return v.For(tenant, ev.FenceSetVersion)
}

// RetainedVersions reports the versions held for a tenant, ascending. It exists for the tests
// that pin the retention bound and for operator introspection; the hot path uses For.
func (v *FenceSetView) RetainedVersions(tenant string) []int32 {
	t := v.byTenant[tenant]
	if t == nil {
		return nil
	}
	out := make([]int32, len(t.versions))
	copy(out, t.versions)
	return out
}

// RemoveTenant drops every fence set belonging to one tenant and reports how many versions it
// removed, for the ADR-077 erasure. Authored fence geometry is the tenant's own configuration —
// the coordinates of its sites — so it goes with the tenant; the durable snapshots are swept by
// device-management's relational purge, and this is the in-memory copy, which nothing else would
// clear until a restart.
func (v *FenceSetView) RemoveTenant(tenant string) int {
	if tenant == "" {
		return 0
	}
	t := v.byTenant[tenant]
	if t == nil {
		return 0
	}
	n := len(t.versions)
	delete(v.byTenant, tenant)
	return n
}

// LoadingFenceSets resolves fence sets through a FenceSetSource, memoizing what it has already
// read. It is for the callers that MAY BLOCK — an authoring-time preview replaying history, a
// startup warm — and is deliberately NOT the type the fan-out holds: a source read on the
// single-writer loop would stall every tenant's event processing behind one database round trip.
//
// It memoizes rather than caching with a bound: a single preview reads a bounded window, so the
// versions it touches are bounded by that window's fence edits, and the instance is discarded
// with the job. It is not safe for concurrent use, which matches its one-job-at-a-time use.
type LoadingFenceSets struct {
	source FenceSetSource
	tenant string
	// byVersion memoizes resolved versions, INCLUDING the failures: a nil entry records "this
	// version could not be resolved" so a preview over ten thousand events stamped with a
	// missing version issues one failed read rather than ten thousand.
	byVersion map[int32]*geofence.FenceSet
}

// NewLoadingFenceSets builds a memoizing resolver for one tenant over a source. A nil source is
// legal and resolves everything to nil (ErrNoFenceSet at evaluation) — the honest behaviour when
// no source is wired, and distinguishable from a tenant with no fences.
func NewLoadingFenceSets(source FenceSetSource, tenant string) *LoadingFenceSets {
	return &LoadingFenceSets{source: source, tenant: tenant, byVersion: map[int32]*geofence.FenceSet{}}
}

// Resolve returns the frozen fence set for a version, reading through the source on first sight.
// Version 0 is the known-empty set (see FenceSetView.For). A read error resolves to nil, which
// the predicate reports as ErrNoFenceSet — an unresolvable fence set must never be presented as
// an empty one.
func (l *LoadingFenceSets) Resolve(ctx context.Context, version int32) *geofence.FenceSet {
	if version <= 0 {
		return geofence.EmptyFenceSet(0)
	}
	if fs, seen := l.byVersion[version]; seen {
		return fs
	}
	var fs *geofence.FenceSet
	if l.source != nil {
		loaded, err := l.source.FenceSetAt(ctx, l.tenant, version)
		if err == nil {
			fs = loaded
		}
	}
	l.byVersion[version] = fs
	return fs
}
