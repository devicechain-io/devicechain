// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// The geofence caps (ADR-023 governance, packaged per ADR-065 tier). THREE keys, because a
// tenant's geofence footprint has three independent costs and no one number bounds them all.
// Each constant below states the cost it bounds and the measurement the maximum was read off.
//
// 🔴 THE MAXIMA ARE NOT DECORATION. Every other tier ceiling in this package bounds only its
// REPRESENTATION (validatePositiveBurst stops at MaxInt32 because the value crosses a GraphQL
// Int), and validateHeldCommandCeiling explicitly refuses a semantic upper bound — a held
// backlog is tenant-local durable storage, so a tier granting a huge one spends only that
// tenant's disk. These three are different in kind: a fence set is compiled and retained in
// event-processing, a process EVERY tenant shares, and its manifest crosses a broker with a
// per-message ceiling. An operator who types an extra zero here does not overprovision one
// tenant, they degrade the instance. So each key carries a real maximum, and it is enforced at
// BOTH doors — the tier write and the per-tenant override — because the override door is not
// reached by ValidateTierConfig.
const (
	// DefaultGeoFencePositionCeiling is the platform default bound on ONE fence's total
	// position count across every ring. It is what a tenant gets when neither it nor its
	// tier declares a geoFencePositionCeiling.
	//
	// 512 sits about two orders of magnitude above real geofences — a site boundary, a yard,
	// a loading zone are tens of positions, a simplified administrative boundary is low
	// hundreds — and it is the number device-management shipped as a hard constant before
	// these caps became tier-driven, so a tenant that declares nothing is metered exactly
	// where it was.
	DefaultGeoFencePositionCeiling = 512

	// MaxGeoFencePositionCeiling is the largest per-fence position count any tier may grant.
	//
	// 🔴 READ OFF THE CACHE-FILL STALL, NOT OFF AUTHORING LATENCY, and those are different
	// consumers with different tolerances. Compiling one fence is O(V²) — core/geo's
	// LoopSelfIntersects scans every non-adjacent edge pair, because s2.Loop.Validate does
	// not detect crossings — and the binding consumer is event-processing filling its
	// geometry cache after an eviction, where the compile stalls that tenant's containment
	// behind a singleflight. Authoring pays the same cost, but a human absorbs it.
	//
	// Measured on the DETECT-side compile (event-processing's CompileGeometry, which is what
	// a fill actually runs), 2026-08-25:
	//
	//	  256 →   3.9ms      2048 →  206ms
	//	  512 →  14.1ms      4096 →  815ms
	//	 1024 →  63.5ms      8192 → 3.04s
	//
	// An exact quadratic, ~4× per doubling. The tolerance chosen is 100ms for one fill: a
	// fill already pays a network round-trip to fetch the geometry document before it
	// compiles anything, so a compile of the same order as the fetch it follows is
	// proportionate, and one several times larger is not. 1024 costs 63.5ms and fits; 2048
	// costs 206ms and does not. Re-measure before moving this — the table above replaced an
	// older one that had drifted from the code it described.
	MaxGeoFencePositionCeiling = 1024

	// DefaultGeoFenceCeiling is the platform default bound on how many fences one tenant may
	// hold — again the number device-management shipped as a hard constant, so a tenant that
	// declares nothing is metered where it was.
	DefaultGeoFenceCeiling = 100

	// MaxGeoFenceCeiling is the largest fence count any tier may grant.
	//
	// It is a WIRE bound, and it is the only one of the three that is. A fence-set manifest
	// carries one {token, hash} pair per fence and must fit a single broker message;
	// device-management's MaxGeoFenceSetManifestBytes() builds that worst case and measures
	// it rather than asserting a number, and it must loop THIS constant rather than the
	// default, because the default is what a tenant gets and this is what the platform must
	// survive.
	//
	// Measured 2026-08-25 at 215 bytes per entry (a 128-character token, a 64-character hash,
	// and JSON's punctuation): the break-even against the chart's default 1 MiB
	// streamMaxMsgSize is 4,876 fences. 4,000 is the round number below it, at 860,083 bytes
	// — 82% of the ceiling, leaving room for a third per-entry field of ~46 bytes before the
	// startup warning in device-management's publisher starts firing on a default deployment.
	// 🔴 Choosing the break-even itself would make that warning permanent noise on every
	// default install the day a tier granted the maximum.
	MaxGeoFenceCeiling = 4000

	// DefaultTenantGeometryPositions is the platform default bound on the total position count
	// across a tenant's WHOLE CURRENT fence set — the third cost, and the one the other two
	// cannot express. A ceiling on one fence bounds a compile; a ceiling on the count bounds a
	// manifest; neither bounds what the tenant's set costs to HOLD.
	//
	// It is the tenant's share of event-processing's geometry cache, which is the shared
	// resource this whole key exists for: 250,000 compiled vertices across every retained
	// entry, for every tenant the process serves. 50,000 is a fifth of it, so five tenants at
	// their full default budget are resident simultaneously — the property the cache's own
	// comment used to claim ("holds several tenants at that absolute ceiling") and that
	// nothing enforced. The alias in event-processing's cache makes it hold by construction.
	//
	// 🔴 IT IS DENOMINATED IN POSITIONS, NOT COMPILED VERTICES, and the two are deliberately
	// not reconciled in this codebase (a compiled ring drops its repeated closing position).
	// Positions ≥ compiled vertices, so a budget spent in positions is CONSERVATIVE against a
	// cache spent in compiled ones. Counting the other way would be a silent overdraft.
	DefaultTenantGeometryPositions = 50_000

	// MaxTenantGeometryPositions is the largest whole-fence-set budget any tier may grant.
	//
	// The property at stake is not "several tenants fit" — that one belongs to the default —
	// but the weaker and more structural one: NO SINGLE TENANT MAY HOLD MORE THAN HALF THE
	// SHARED CACHE. Above half, one tenant's fill can evict every other tenant's geometry, and
	// the cache stops being a shared resource and becomes that tenant's, with everyone else
	// paying a refetch and an O(V²) recompile on every event.
	//
	// So this is the cache bound halved, and event-processing derives the cache bound FROM it
	// (see DefaultMaxCachedVertices) rather than the other way round, which makes the relation
	// unrepresentable-to-drift instead of merely asserted. Raising a tenant's real capacity
	// past this means raising the cache, not this constant alone.
	MaxTenantGeometryPositions = 125_000
)

// Why each maximum exists, in one sentence an operator who has just been refused can act on.
//
// They live HERE, beside the numbers, because a refusal has to travel to two different doors —
// the tier write and the per-tenant override — and those doors are in different packages, take
// different Go types, and are written by different edits. Sharing only the NUMBER would leave
// them free to explain it differently, which is what happened: the override door told an
// operator that every one of these caps "bounds a resource shared by every tenant on the
// instance", and that is true of the budget alone. The fence count bounds a broker message.
//
// Each names the COST, not the rule. "Must be at most 4000" tells an operator what to type; it
// does not tell them whether typing something smaller is the right answer or whether the
// platform constant is the thing that should move.
const (
	GeoFencePositionCeilingBecause = "compiling one fence is quadratic in its position count, and " +
		"above this the compile stalls containment for the tenant every time event-processing " +
		"refills its geometry cache"
	GeoFenceCeilingBecause = "a fence-set manifest carries one entry per fence and must fit inside " +
		"one broker message"
	GeoFencePositionBudgetBecause = "the DETECT geometry cache is shared by every tenant on the " +
		"instance, and above this one tenant's fence set can evict every other tenant's geometry"
)

// GeoFenceCaps is a tenant's effective geofence caps, all three resolved down the ADR-065
// cascade (tenant override → tier → platform default) and folded onto the platform defaults, so
// every field is a live bound. There is no value at any level meaning unlimited.
type GeoFenceCaps struct {
	// PositionCeiling bounds ONE fence's total position count across every ring.
	PositionCeiling int
	// FenceCeiling bounds how many fences the tenant may hold.
	FenceCeiling int
	// PositionBudget bounds the total position count across the tenant's whole current fence
	// set — the distinct geometry it holds, deduped by content address exactly as the archive
	// and the cache dedupe it.
	PositionBudget int
}

// DefaultGeoFenceCaps returns the platform defaults — what a tenant whose tier declares
// nothing is metered at.
//
// 🔴 It is NOT a fallback for an unresolvable tenant, and GeoFenceCapsResolver deliberately
// does not use it as one. A tier may declare caps BELOW these defaults, so serving them to a
// tenant whose tier could not be read would hand that tenant a cap LARGER than the operator
// granted it — the fail-open ADR-023 forbids, arriving through the back door of a sensible
// -looking default. It exists for the enforcing service's nil-resolver path, where no tenant
// is in question at all.
func DefaultGeoFenceCaps() GeoFenceCaps {
	return GeoFenceCaps{
		PositionCeiling: DefaultGeoFencePositionCeiling,
		FenceCeiling:    DefaultGeoFenceCeiling,
		PositionBudget:  DefaultTenantGeometryPositions,
	}
}

// GeoFenceCapsResolver resolves a tenant's three geofence caps from user-management's
// tenantGovernance query, cached with the same 60s TTL as every other per-tenant setting.
//
// 🔴 IT BLOCKS, AND THAT IS THE ONE THING THAT MAKES IT DIFFERENT FROM EVERY OTHER RESOLVER IN
// THIS PACKAGE. tenantResolver never blocks: it serves the platform default on a cold miss and
// refreshes out of band, which is right for a hot path whose default is itself a real ceiling
// and whose mistakes are transient (a few extra messages admitted while the cache warms).
//
// Neither property holds here. These caps are enforced at AUTHORING — a low-rate GraphQL
// mutation, already inside a database transaction — and what a fail-open window produces is
// DURABLE: rows that mint a fence-set version, evaluate in DETECT, and are then protected by
// this feature's own grandfathering rule, which refuses to make anything worse. An operator
// lowering an abusive tenant's cap would have it defeated by any device-management restart,
// permanently. So the first resolve for a tenant blocks on the fetch and can FAIL.
//
// The degradation is staged rather than binary, because the two failures are not equally bad:
//
//   - a STALE entry is served when a refresh fails. It is a real cap the operator set, this
//     process was told it, and the alternative — refusing — takes fence authoring down
//     platform-wide on a blip, including the deletes a tenant uses to get back under a cap.
//   - a COLD miss returns an error. There is nothing to serve, and DefaultGeoFenceCaps() is
//     not an answer: a tier may sit below the defaults, so serving them would raise the cap of
//     exactly the tenant whose tier could not be read.
//
// What the caller does with that error is the caller's decision, and it is not the same at
// every site: refusing to GROW a fence set on an unresolvable cap is right, while refusing to
// SHRINK or DELETE one needs no cap at all and must not be blocked by this.
//
// Safe for concurrent use.
type GeoFenceCapsResolver struct {
	fetch func(ctx context.Context, tenant string) (GeoFenceCaps, error)
	ttl   time.Duration
	now   func() time.Time

	// group collapses a burst of concurrent first-resolves for one tenant into a single
	// fetch. Without it, N concurrent authoring calls during a user-management outage each
	// burn the full svcclient timeout independently — the resolver's non-blocking siblings
	// get this from their inflight set, which a blocking resolver cannot reuse because its
	// callers need the RESULT, not just the knowledge that someone is fetching it.
	group singleflight.Group

	mu    sync.RWMutex
	cache map[string]geoFenceCapsEntry
}

type geoFenceCapsEntry struct {
	caps      GeoFenceCaps
	fetchedAt time.Time
}

// NewGeoFenceCapsResolver wires a caps resolver to user-management at umURL over the
// service-token client.
//
// Unlike NewHeldCommandCeilingResolver it takes no default: there is no configured
// service-level number to fall back to, because a fall-back is precisely what this resolver
// refuses to do on a cold miss (see the type comment).
func NewGeoFenceCapsResolver(client *svcclient.Client, umURL string) *GeoFenceCapsResolver {
	fetch := func(ctx context.Context, tenant string) (GeoFenceCaps, error) {
		return fetchGeoFenceCaps(ctx, client, umURL, tenant)
	}
	return newGeoFenceCapsResolver(fetch)
}

// newGeoFenceCapsResolver builds a resolver over an arbitrary fetch — the seam the tests use,
// since svcclient.Client is concrete and nothing about a live fetch is otherwise injectable.
func newGeoFenceCapsResolver(fetch func(context.Context, string) (GeoFenceCaps, error)) *GeoFenceCapsResolver {
	return &GeoFenceCapsResolver{
		fetch: fetch,
		ttl:   defaultCacheTTL,
		now:   time.Now,
		cache: make(map[string]geoFenceCapsEntry),
	}
}

// Resolve returns the tenant's effective geofence caps, blocking on a fetch when the cache
// holds nothing fresh. A fresh entry is served without a network call; a stale one triggers a
// blocking refresh that falls back to the stale value if it fails; a cold miss returns the
// fetch's error.
//
// ctx bounds THIS caller's wait, not the shared fetch: the fetch runs on a detached context so
// one caller's cancellation cannot abort a round trip several callers are waiting on. A
// cancelled caller gets ctx.Err() and the fetch completes for whoever is left.
func (r *GeoFenceCapsResolver) Resolve(ctx context.Context, tenant string) (GeoFenceCaps, error) {
	if tenant == "" {
		// Refused rather than resolved to the defaults. An untenanted resolve has no cascade
		// to walk, so answering it at all would mean answering with a number no operator
		// chose, for a caller that has lost track of whose fences it is bounding.
		return GeoFenceCaps{}, fmt.Errorf("governance: cannot resolve geofence caps without a tenant")
	}

	if caps, ok := r.fresh(tenant); ok {
		return caps, nil
	}

	ch := r.group.DoChan(tenant, func() (any, error) {
		// Re-check inside the flight: a burst that queued behind one fetch must not each
		// fire another once it completes.
		if caps, ok := r.fresh(tenant); ok {
			return caps, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()

		caps, err := r.fetch(fetchCtx, tenant)
		if err != nil {
			// Serve the last-known value if this process ever had one. Not a fail-open to
			// the platform default — a value the operator set, which is bounded, and which
			// converges on the TTL as soon as user-management answers again.
			r.mu.RLock()
			stale, had := r.cache[tenant]
			r.mu.RUnlock()
			if had {
				return stale.caps, nil
			}
			return GeoFenceCaps{}, err
		}
		r.mu.Lock()
		r.cache[tenant] = geoFenceCapsEntry{caps: caps, fetchedAt: r.now()}
		r.mu.Unlock()
		return caps, nil
	})

	select {
	case <-ctx.Done():
		return GeoFenceCaps{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return GeoFenceCaps{}, res.Err
		}
		return res.Val.(GeoFenceCaps), nil
	}
}

// fresh returns the tenant's cached caps when the entry exists and is inside the TTL.
func (r *GeoFenceCapsResolver) fresh(tenant string) (GeoFenceCaps, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.cache[tenant]
	if !ok || r.now().Sub(e.fetchedAt) >= r.ttl {
		return GeoFenceCaps{}, false
	}
	return e.caps, true
}

// The three geofence caps' WIRE FIELD NAMES, declared here and nowhere else.
//
// Each of these strings is the same name in three places: the field user-management serves off
// tenantGovernance, the tier config key an operator sets, and the per-tenant override field an
// admin sends. They are the same name because an operator setting `geoFenceCeiling` on a tier
// and reading `geoFenceCeiling` back off a tenant is the whole legibility of the cascade — so
// they are declared ONCE, here, and user-management derives its tier keys from them rather than
// re-typing them. That is the pattern AllDimensions() already establishes for RateField and
// BurstField, for the same reason: the enforcing service's query and the authority's key must
// be the same string by construction, not by two people spelling it the same way.
const (
	GeoFencePositionCeilingField = "geoFencePositionCeiling"
	GeoFenceCeilingField         = "geoFenceCeiling"
	GeoFencePositionBudgetField  = "geoFencePositionBudget"
)

// geoFenceCapsQuery reads the three fields this resolver needs, BUILT from the constants above
// rather than written out — so a renamed field cannot leave the query selecting the old name.
//
// Named rather than inlined at the call for the reason spelled out on heldCommandCeilingQuery:
// svcclient.Client is concrete, so nothing about the fetch is otherwise reachable from a unit
// test, and a typo here would make every resolve on the instance error — which for a resolver
// that BLOCKS means fence authoring stops, not that a default is quietly served.
const geoFenceCapsQuery = "query { tenantGovernance { " +
	GeoFencePositionCeilingField + " " + GeoFenceCeilingField + " " + GeoFencePositionBudgetField + " } }"

// geoFenceCapsResponse is the wire shape of that query's data object. Named for the same
// reason as the query: a wrong json tag decodes to nil, which reads as "inherit the platform
// default" and would quietly ignore every cap an operator has set — the one failure this whole
// feature is built to prevent, arriving through a struct tag.
//
// 🔴 THE json TAGS MUST BE LITERALS — Go struct tags cannot reference a constant — so they are
// the one place the three names are written twice. TestGeoFenceCapsQueryAndResponseAgree builds
// its wire body FROM the constants and decodes through this type, which is what makes a tag that
// has drifted away from its constant fail rather than silently decode to nil.
type geoFenceCapsResponse struct {
	TenantGovernance struct {
		GeoFencePositionCeiling *int32 `json:"geoFencePositionCeiling"`
		GeoFenceCeiling         *int32 `json:"geoFenceCeiling"`
		GeoFencePositionBudget  *int32 `json:"geoFencePositionBudget"`
	} `json:"tenantGovernance"`
}

// fetchGeoFenceCaps reads the three scalars from tenantGovernance and folds each onto its
// platform default. Like the held-command ceiling and the shed priority these are standalone
// scalars, not rate+burst Dimensions, so they do not use the Dimension/governanceQuery
// machinery — a position count has no burst and no per-second unit.
func fetchGeoFenceCaps(ctx context.Context, client *svcclient.Client, umURL, tenant string) (GeoFenceCaps, error) {
	var out geoFenceCapsResponse
	if err := client.Query(ctx, umURL, tenant, geoFenceCapsQuery, nil, &out); err != nil {
		return GeoFenceCaps{}, err
	}
	g := out.TenantGovernance
	return GeoFenceCaps{
		PositionCeiling: resolveGeoFenceCap(g.GeoFencePositionCeiling, DefaultGeoFencePositionCeiling, MaxGeoFencePositionCeiling),
		FenceCeiling:    resolveGeoFenceCap(g.GeoFenceCeiling, DefaultGeoFenceCeiling, MaxGeoFenceCeiling),
		PositionBudget:  resolveGeoFenceCap(g.GeoFencePositionBudget, DefaultTenantGeometryPositions, MaxTenantGeometryPositions),
	}, nil
}

// resolveGeoFenceCap folds one wire cap onto its platform default. A null value (neither the
// tenant nor its tier declared one) or a non-positive one resolves to def; a value above max is
// CLAMPED to max rather than honoured. Split out pure so the rule is unit-testable without a
// live user-management.
//
// 🔴 THE CLAMP DOES NOT DEFEND AGAINST AN OUT-OF-BAND DATABASE WRITE, AND AN EARLIER VERSION OF
// THIS COMMENT SAID IT DID. That threat is already handled a level up and cannot reach here: an
// over-large value in a tenant column fails UsableGeoFence*, an over-large one in a tier's config
// blob fails the tier accessor's defensive read, and BOTH resolve to nil — inherit — so
// user-management serves null rather than a number the platform would not honour.
// TestTheServedCapsNeverExceedThePlatformMaximum pins exactly that. A clamp guarding a value that
// cannot arrive is not a defence, it is a comment.
//
// What it does defend against is VERSION SKEW. These maxima are compiled into both services, and
// during a rolling upgrade user-management runs ahead of device-management for as long as the
// rollout takes. A user-management that has learned a larger maximum will happily serve a cap
// this older reader was never built to honour, and the reader has no way to know the difference
// — the value is well-formed and the cascade that produced it was correct. Clamping to what THIS
// binary was built to allow is the only answer available to it.
//
// Clamping rather than erroring keeps the polarity right: the tenant is metered at the largest
// bound this binary will honour, never at none. That is the opposite choice from the write doors,
// which reject — deliberately, because a door has a human in front of it to be told, and a wire
// fold does not.
func resolveGeoFenceCap(v *int32, def, max int) int {
	if v == nil || *v <= 0 {
		return def
	}
	if int(*v) > max {
		return max
	}
	return int(*v)
}
