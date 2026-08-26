// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// validateGeoFenceToken enforces that a geofence token is present and
// token-grammar-safe (ADR-042). A rule names a fence by this token, so it ends up
// inside a compiled rule body and travels on a per-tenant subject; a free-form token
// must not ossify before that enforcement.
func validateGeoFenceToken(token string) error {
	if err := core.ValidateToken(token); err != nil {
		return fmt.Errorf("invalid geofence token %q: %w", token, err)
	}
	return nil
}

// errGeoFenceTokenImmutable refuses an update that would move a fence to a different
// token. It is the request-shape half of `updateGeoFence(token:, request:)`, which
// carries the token twice: once to say which fence, and once inside a request input
// shared with the create path.
//
// 🔴 A RENAME HAS NO SAFE OUTCOME, WHICH IS WHY IT IS REFUSED RATHER THAN CASCADED. A
// rule names a fence by its token, inside compiled CEL text that this service cannot
// see and event-processing cannot be asked to rewrite. So a rename leaves every
// `geo.inFence("old")` naming nothing: containment answers ErrUnknownFence, which the
// runtime turns into a SKIPPED sample rather than a `false` — deliberately, so the
// breakage lands on the eval-error counter instead of silently reading as "outside".
// That is the loudest a downstream service can be about it, and it is still only
// visible to someone already looking at the counter. The rename itself, meanwhile,
// succeeds with a 200 and mints a fence-set version that freezes the NEW token into the
// snapshot, so nothing downstream can even reconstruct what the rules used to name.
//
// The other two surfaces already assume immutability — the console renders the token as
// a disabled input when editing, and the concept docs say the token is fixed once
// created — so this closes a gap between the API and everything written about it, and
// matches how UpdateDeviceProfile refuses the same move.
//
// 🔴 THE RECIPE IS CREATE-THEN-DELETE, IN THAT ORDER, AND THE ORDER IS NOT STYLE. A caller
// who genuinely wants a different token creates the second fence FIRST and deletes the old
// one after, which is also the operation that makes them confront the rules naming the old
// token. Doing it the other way round is a data-loss trap for exactly the tenants this
// service grandfathers: the whole-set position budget refuses growth relative to what is
// STORED, so deleting first lowers the stored sum and the recreate is then refused on its
// way back up. The fence is gone and cannot be put back. Create-then-delete never crosses
// that line — the create is the growth, and it is checked while the old fence still exists.
//
// It does mean the create-then-delete recipe needs one spare fence slot under the tenant's
// geoFenceCeiling for the moment both exist. A tenant exactly at its fence ceiling has to
// have the ceiling raised to rename anything, which is a refusal with a route out rather
// than an operation that destroys a fence.
//
// 🔴 AND THE RECIPE DOES NOT WORK AT ALL FOR A FENCE GRANDFATHERED OVER THE PER-FENCE
// CEILING, IN EITHER ORDER. A create has no stored baseline to be measured against, so
// CreateGeoFence checks the incoming count against geoFencePositionCeiling unconditionally —
// the growth comparison that lets an oversized fence be EDITED has nothing to compare to when
// the fence is new. So a fence larger than its tenant's current ceiling can be edited and
// deleted but never re-created under any token. The failure is benign in order (the create
// refuses before anything is deleted, so nothing is lost), but the recipe above is narrower
// than it reads: renaming such a fence needs the ceiling raised, not a different order.
func errGeoFenceTokenImmutable(token string, requested string) error {
	if requested == token {
		return nil
	}
	return fmt.Errorf("cannot rename geofence %q to %q: a fence's token is immutable, "+
		"because rules name fences by token and a rename would leave them naming nothing",
		token, requested)
}

// geoFenceCapsAttempt is the result of TRYING to resolve the tenant's geofence caps, held
// so that each check can decide for itself whether it needs a number.
//
// 🔴 IT EXISTS BECAUSE "RESOLVE, AND FAIL THE MUTATION IF YOU CANNOT" IS THE WRONG SHAPE,
// AND THE RESOLVER'S OWN CONTRACT SAYS SO. Resolving up front and refusing on error would
// take down renames, description edits, metadata edits, SHRINKS and DELETES for every
// tenant on the instance whenever user-management is unreachable — including, precisely,
// the deletes a tenant over its budget needs in order to get back under it. None of those
// operations makes anything worse, and none of them needs a cap to decide that.
//
// So the resolve is attempted ONCE, before the transaction opens, and its error is carried
// rather than returned. Three checks consume it, each demanding a number only in the case
// where a cap could actually refuse:
//
//	per-fence ceiling   only when the incoming geometry has MORE positions than the
//	                    geometry it replaces (so a create always, an edit sometimes)
//	fence count         only on a create
//	whole-set budget    only when the new distinct-geometry sum EXCEEDS the stored one
//
// Delete asks for none of them, structurally, on every path. That is a property of the
// arrangement rather than a case anybody special-cased, which is why it cannot rot.
//
// 🔴 THE RESOLVE IS DELIBERATELY OUTSIDE THE TRANSACTION. It is a blocking cross-service
// call with a multi-second timeout; inside api.RDB.DB(ctx).Transaction it would hold a
// Postgres transaction open for the whole round trip, on every fence mutation, and a
// user-management stall would become a connection-pool exhaustion here.
type geoFenceCapsAttempt struct {
	caps governance.GeoFenceCaps
	err  error
}

// errGeoFenceCapsNotNeeded is what geoFenceCapsNotNeeded() carries. It is deliberately NOT
// a resolve failure: nothing tried and failed, nothing was unreachable. If it ever reaches a
// caller it means a check demanded a cap on a path proven not to need one, which is a bug in
// this file and not a condition an operator can fix.
var errGeoFenceCapsNotNeeded = errors.New("the geofence caps were not resolved for this " +
	"operation, because it cannot increase the tenant's geofence footprint")

// geoFenceCapsNotNeeded is the attempt a DELETE passes to the mint.
//
// 🔴 IT MAKES "A DELETE IS NEVER BLOCKED BY A CAP" STRUCTURAL RATHER THAN ARGUED. A delete
// removes a fence from the set, so the distinct-geometry sum after it is a subset of the sum
// before — the budget check's "is this worse than what is stored" is false on every delete,
// and the fence-count and per-fence checks are not on this path at all. Passing an attempt
// that CANNOT answer therefore costs nothing, and it buys the property outright: if a future
// edit made a delete demand a cap, the delete fails loudly with errGeoFenceCapsNotNeeded in
// a test, instead of quietly acquiring a dependency on user-management being reachable.
//
// The alternative — resolving here like the other two paths — reads harmless because the
// resolver caches per tenant, and is not: on a user-management outage the FIRST delete blocks
// for the full fetch timeout before proceeding. A tenant over its budget trying to get back
// under it is exactly who is deleting fences during an incident.
func geoFenceCapsNotNeeded() geoFenceCapsAttempt {
	return geoFenceCapsAttempt{err: errGeoFenceCapsNotNeeded}
}

// require returns the resolved caps, or an error naming what could not be checked. Called
// only at the point where a cap is about to refuse something; see geoFenceCapsAttempt.
func (a geoFenceCapsAttempt) require(what string) (governance.GeoFenceCaps, error) {
	if a.err != nil {
		// The wrap says only what is true of BOTH errors this can carry. It used to add "could
		// not be resolved from user-management… guessing would raise it", which is the resolve
		// path's story and is false of errGeoFenceCapsNotNeeded — where nothing was tried and
		// nothing was unreachable. Each error states its own cause; this adds the context the
		// caller has and the error does not, which is WHICH CHECK could not be made.
		return governance.GeoFenceCaps{}, fmt.Errorf("cannot check %s: %w", what, a.err)
	}
	return a.caps, nil
}

// geoFenceCaps attempts to resolve the caps for the tenant in ctx. It never fails the call;
// see geoFenceCapsAttempt for why the error is carried instead.
//
// With no resolver wired it answers the PLATFORM DEFAULTS and no error, which is what a
// unit test and an instance with no user-management coordinate both get — the same numbers
// this service enforced as hard constants before the caps became tier-driven. It is not a
// fallback for a resolver that FAILED: that error is carried, because a tier may sit below
// the defaults and serving them would raise the cap of the one tenant nobody could read.
func (api *Api) geoFenceCaps(ctx context.Context) geoFenceCapsAttempt {
	if api.GeoFenceCapsResolver == nil {
		return geoFenceCapsAttempt{caps: governance.DefaultGeoFenceCaps()}
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		// Unreachable through the GraphQL doors — the tenant-scope callback already
		// refuses any tenant-scoped query with no tenant in context — but carried as an
		// error rather than defaulted, so a future untenanted caller is refused at the
		// cap rather than metered at numbers no operator chose for it.
		return geoFenceCapsAttempt{err: fmt.Errorf("no tenant in context")}
	}
	caps, err := api.GeoFenceCapsResolver.Resolve(ctx, tenant)
	if err != nil {
		// The operator-facing reason lives HERE, on the only path where it is true, rather
		// than in require()'s wrap — which also carries errGeoFenceCapsNotNeeded, for which
		// "could not be resolved" would be a lie. It stays in an ERROR rather than moving to
		// a comment beside it because the audience is different: an operator who has just
		// been refused never reads this file, and needs to know that the refusal is an
		// outage rather than their cap, and why guessing was not an option.
		err = fmt.Errorf("this tenant's geofence caps could not be resolved from user-management, "+
			"and there is no safe number to assume (a tier may set a cap below the platform "+
			"default, so guessing would raise it): %w", err)
	}
	return geoFenceCapsAttempt{caps: caps, err: err}
}

// refuseGeoFenceCap counts a cap refusal and returns the error to hand back. Every refusal
// in this file goes through it, so a fourth cap cannot be added and forget the counter.
func (api *Api) refuseGeoFenceCap(cap string, err error) error {
	if api.GeoFenceCapRefusals != nil {
		api.GeoFenceCapRefusals.CountGeoFenceCapRefusal(cap)
	}
	return err
}

// CreateGeoFence creates a geofence and mints a new tenant fence-set version in the
// SAME transaction (ADR-078). The two are one atomic fact: a fence that exists at a
// version the stamp never advanced to would be invisible to every event resolved after
// it was created, and a version minted without the fence would claim a snapshot the
// fence is not in.
func (api *Api) CreateGeoFence(ctx context.Context, request *GeoFenceCreateRequest) (*GeoFence, error) {
	if err := validateGeoFenceToken(request.Token); err != nil {
		return nil, err
	}
	// The CANONICAL document is what gets stored, never request.Geometry. Storing the
	// authored text would put a document in the column that the size bound was never
	// applied to — see validateGeoFenceGeometry.
	canonicalGeometry, incoming, err := validateGeoFenceGeometry(request.Geometry)
	if err != nil {
		return nil, err
	}

	// Resolved BEFORE the transaction opens; see geoFenceCapsAttempt. A create is the one
	// path that needs a number unconditionally — it adds a fence, and every one of that
	// fence's positions is new — so the require below is not guarded by a "does this make
	// things worse" test the way the update and budget checks are, and an unresolvable
	// tenant is refused outright. That is the correct direction: a create is growth, and
	// growth is the one thing an unreadable cap must not wave through. The single resolve
	// here also serves the fence-count check inside the transaction and the budget check
	// inside the mint, so a create costs one round trip, not three.
	capsAttempt := api.geoFenceCaps(ctx)
	caps, err := capsAttempt.require("this geofence's position count")
	if err != nil {
		return nil, err
	}
	if incoming > caps.PositionCeiling {
		return nil, api.refuseGeoFenceCap(governance.GeoFencePositionCeilingField,
			fmt.Errorf("geofence has %d positions across its rings; this tenant's %s is %d (%s)",
				incoming, governance.GeoFencePositionCeilingField, caps.PositionCeiling,
				governance.GeoFencePositionCeilingBecause))
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &GeoFence{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		Geometry:       datatypes.JSON(canonicalGeometry),
	}
	var minted *GeoFenceSetVersion
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The fence-count bound is checked inside the transaction so it reads the same
		// state the insert lands in. It is a count-then-insert with no lock, and what that
		// costs is smaller than it looks: two concurrent creates both mint a fence-set
		// version in this same transaction, and the per-tenant unique index on
		// (tenant_id, version) makes the loser's WHOLE transaction abort. So the pair
		// cannot both land, and the count cannot be beaten by racing it.
		//
		// A lock is still not warranted, because that serialization is incidental rather
		// than promised — it comes from the mint, not from this check — and the bound could
		// tolerate being beaten anyway: what it protects is the SIZE OF THE ANNOUNCEMENT (a
		// manifest carries one entry per fence and must fit one broker message), and one
		// extra entry is 215 bytes against a ceiling with six figures of headroom. Recorded
		// this way round because the comment here used to assert the race as a tolerated
		// outcome, which stopped being reachable when the mint moved inside the transaction.
		//
		// 🔴 IT IS ONLY REACHED ON A CREATE, which is why this is the one check that never
		// needs a "would this make it worse" comparison: an update leaves the count exactly
		// as it was, and a delete lowers it. A tenant already over a lowered ceiling keeps
		// its fences and can still edit and delete them; it simply cannot add another.
		var n int64
		if err := tx.Model(&GeoFence{}).Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(caps.FenceCeiling) {
			return api.refuseGeoFenceCap(governance.GeoFenceCeilingField,
				fmt.Errorf("the tenant already has %d geofences; this tenant's %s is %d (%s)",
					n, governance.GeoFenceCeilingField, caps.FenceCeiling, governance.GeoFenceCeilingBecause))
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		var err error
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now(), capsAttempt)
		return err
	})
	if err != nil {
		return nil, err
	}
	api.announceMintedGeoFenceSet(ctx, minted)
	return created, nil
}

// UpdateGeoFence replaces a geofence, and mints a new fence-set version only when the
// replacement changed the fence SET — that is, its geometry. An edit touching only the
// name, the description or the metadata mints nothing, publishes nothing, evicts nothing.
//
// This reverses a decision previously recorded here, and it is worth saying why rather
// than quietly dropping the old sentence. That decision held the failure modes asymmetric
// — a spare version costs one row, a MISSED one leaves events pointing at a snapshot that
// no longer describes the fences — and concluded the question could not be answered here
// cheaply or safely. Both halves have since been shown wrong. The question is answered
// exactly by comparing the stored reference list, which makes a missed mint unreachable
// rather than merely unlikely; and a spare version costs a slot in the engine's bounded
// per-tenant retention plus a tenant-wide cache eviction, not one row. See
// mintGeoFenceSetVersion.
//
// A fence's TOKEN is immutable, and this is where that is enforced — see
// errGeoFenceTokenImmutable.
func (api *Api) UpdateGeoFence(ctx context.Context, token string,
	request *GeoFenceCreateRequest) (*GeoFence, error) {
	if err := validateGeoFenceToken(request.Token); err != nil {
		return nil, err
	}
	if err := errGeoFenceTokenImmutable(token, request.Token); err != nil {
		return nil, err
	}
	// 🔴 THE ROW IS LOADED BEFORE THE GEOMETRY IS CHECKED AGAINST THE TENANT'S CEILING,
	// AND THAT ORDER IS THE WHOLE OF THE GRANDFATHERING RULE. What the ceiling refuses is
	// GROWTH, not size: a tenant whose tier was lowered — or who is over the default
	// because this service used to allow 512 positions unconditionally — must still be
	// able to rename its fences, edit their descriptions, and REPLACE a fence with a
	// smaller one. Deciding that needs the stored position count, so the load moves ahead
	// of the check. It also means an unresolvable cap does not block any of those, since
	// the require below is never reached for them.
	matches, err := api.GeoFencesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	canonicalGeometry, incoming, err := validateGeoFenceGeometry(request.Geometry)
	if err != nil {
		return nil, err
	}

	// 🔴 AN UPDATE RESOLVES UNCONDITIONALLY, UNLIKE A DELETE, AND THE ASYMMETRY IS REAL
	// RATHER THAN AN OVERSIGHT. A delete cannot increase any of the three footprints, so it
	// passes geoFenceCapsNotNeeded() and never touches user-management. An update can: even
	// one that makes a fence SMALLER can raise the tenant's whole-set total, because that
	// total is over DISTINCT geometry and editing one of several identically-drawn fences
	// un-deduplicates it (see the budget check in mintGeoFenceSetVersion). So there is no
	// test available here that proves this edit needs no cap.
	//
	// 🔴 WHAT THAT COSTS, STATED — AND IT IS WORSE THAN "THE FIRST ONE STALLS". During a
	// user-management outage an update blocks for the fetch timeout and then SUCCEEDS if it
	// is not growth: a stall, not a refusal, since the resolve's error is carried rather
	// than returned. But the stall REPEATS. A failed refresh does not restamp the cache
	// entry's fetchedAt, so once the entry is older than the resolver's TTL every later
	// mutation finds it stale, re-enters the fetch, and pays the timeout again — for the
	// whole incident, not once. Only an entry still INSIDE the TTL is served without
	// blocking. An operator sizing an outage's blast radius needs the repeating version.
	capsAttempt := api.geoFenceCaps(ctx)
	updated := matches[0]
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON
	updated.Geometry = datatypes.JSON(canonicalGeometry)

	var minted *GeoFenceSetVersion
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 THE GRANDFATHERING BASELINE IS RE-READ HERE, UNDER A ROW LOCK, AND READING IT
		// OUTSIDE THE TRANSACTION WAS A REAL HOLE. The rule is "an edit may not INCREASE a
		// fence's position count past the tenant's ceiling", which is a comparison against
		// what is stored — so it has to be the state this write commits over, not a copy
		// read before the transaction opened. Two concurrent updates to one fence defeated
		// the outside read: a fence grandfathered at 1024 against a ceiling since lowered to
		// 512, one call shrinking it to 8 and another replacing it with a different 1024,
		// both loading 1024 first. The second sees 1024 > 1024 as false, skips the check
		// entirely, then blocks on the row lock and applies on top of the shrink — 8 becomes
		// 1024 with no ceiling check, an outcome neither serial order allows.
		//
		// The lock is what makes the re-read sufficient: under READ COMMITTED a plain
		// re-read inside the transaction could still be overtaken between the read and the
		// Save. It costs one row lock on an authoring path, which is the cheapest place in
		// this service to spend one.
		//
		// 🔴 NO TEST COVERS THIS, AND THAT IS A PROPERTY OF THE TEST STACK RATHER THAN AN
		// OVERSIGHT — SO DO NOT READ A GREEN SUITE AS EVIDENCE FOR IT. The unit tests run on
		// sqlite, which has neither the row locking nor the concurrency this defends against,
		// and the live models cannot declare the per-tenant unique index the argument above
		// leans on (see archiveGeoFenceGeometries). In a SEQUENTIAL test the pre-transaction
		// read and this one return the same bytes, so the fix has no observable behaviour to
		// assert. A mutation putting the read back outside the transaction IS killed, but
		// incidentally — sqlite refuses a non-transaction read while the transaction is open,
		// which is a fact about the plumbing and not about the race. Believing that kill would
		// be exactly the mistake of measuring the logic around a thing instead of the thing.
		//
		// What would actually cover it is two concurrent updates against real PostgreSQL, in
		// the integration rig rather than here.
		//
		// The budget check in the mint needs none of this — it reads oldSum inside tx and
		// the per-tenant unique index on (tenant, version) forces a concurrent mint to
		// conflict and roll back. The per-fence ceiling had no such serialization.
		locked := make([]GeoFence, 0, 1)
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token = ?", token).Limit(1).Find(&locked).Error; err != nil {
			return err
		}
		if len(locked) == 0 {
			return gorm.ErrRecordNotFound
		}
		// 🔴 AN UNCOUNTABLE STORED DOCUMENT IS TREATED AS ZERO, WHICH MAKES THIS EDIT LOOK
		// LIKE GROWTH AND DEMANDS THE CAP. Unreachable — every write to this column is
		// canonicalized by validateGeoFenceGeometry first — but the polarity is a choice, and
		// it is the opposite of the one mintGeoFenceSetVersion makes for an undecodable
		// SNAPSHOT. The difference is whether refusing leaves a route out. There, refusing
		// would block every mutation the tenant can make, permanently, with no way to repair
		// the row through the API. Here the route out is this same call: an edit that does not
		// increase the count is always allowed, so REPLACING the corrupt document with a
		// smaller one succeeds. Deleting is a route out too but a worse one to advertise — for
		// a tenant over its budget, delete-then-recreate forfeits the headroom and the fence
		// cannot be put back (see errGeoFenceTokenImmutable).
		stored := 0
		if n, err := geoFencePositionsIn([]byte(locked[0].Geometry)); err == nil {
			stored = n
		} else {
			log.Warn().Err(err).Str("token", token).
				Msg("Unable to count the positions in a stored geofence geometry; metering this edit as if the fence were new. An edit that does not increase the position count is still accepted, so replacing it with a smaller geometry will succeed.")
		}
		if incoming > stored {
			caps, err := capsAttempt.require("this geofence's position count")
			if err != nil {
				return err
			}
			if incoming > caps.PositionCeiling {
				return api.refuseGeoFenceCap(governance.GeoFencePositionCeilingField,
					fmt.Errorf("geofence would have %d positions across its rings, up from %d; this tenant's "+
						"%s is %d (%s). An edit that does not increase the position count is always allowed",
						incoming, stored, governance.GeoFencePositionCeilingField, caps.PositionCeiling,
						governance.GeoFencePositionCeilingBecause))
			}
		}
		if err := tx.Save(updated).Error; err != nil {
			return err
		}
		var err error
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now(), capsAttempt)
		return err
	})
	if err != nil {
		return nil, err
	}
	api.announceMintedGeoFenceSet(ctx, minted)
	return updated, nil
}

// DeleteGeoFence hard-deletes a geofence and mints a new fence-set version, following
// this area's uniform hard-delete semantics (see api_delete.go): a soft delete would
// keep the token occupying the per-tenant unique index, so it could never be reused.
// Returns false with no error when the token names nothing — and mints nothing in that
// case, since nothing changed.
func (api *Api) DeleteGeoFence(ctx context.Context, token string) (bool, error) {
	deleted := false
	// No cap is resolved on this path, on purpose and not as an optimization — see
	// geoFenceCapsNotNeeded.
	capsAttempt := geoFenceCapsNotNeeded()
	var minted *GeoFenceSetVersion
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Unscoped().Where("token = ?", token).Delete(&GeoFence{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		deleted = true
		var err error
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now(), capsAttempt)
		return err
	})
	if err != nil {
		return false, err
	}
	api.announceMintedGeoFenceSet(ctx, minted)
	return deleted, nil
}

// announceMintedGeoFenceSet runs the post-commit consequences of a mint — or none of them,
// when nothing was minted. It is the ONE place the three authoring paths funnel through, so
// a fourth mutation cannot mint a version and forget half of what a mint owes.
//
// 🔴 IT EXISTS BECAUSE "half" WAS THE REAL RISK. emitMintedGeoFenceSet already declined a
// nil version on its own, so a per-call-site guard was redundant for the announcement and
// load-bearing only for the EVICTION — which is precisely the half a new mutation would
// forget, and precisely the half no contract stated. Writing the rule once removes the
// asymmetry rather than documenting it.
//
// The ordering is evict THEN publish, and it now has somewhere to be stated. Evicting first
// means the tenant's next resolve cannot read a cached version older than the set the
// engine is about to be told about; the reverse order leaves a window where the engine
// holds the new fences while this service is still stamping the old number at them.
//
// Post-commit on purpose, outside the mutation's transaction: both actions advertise a
// version, and advertising one that then rolled back is the failure neither is allowed to
// have. The cost is that a crash here loses them, which is what the reconcile sweep exists
// to repair.
func (api *Api) announceMintedGeoFenceSet(ctx context.Context, minted *GeoFenceSetVersion) {
	if minted == nil {
		return
	}
	api.evictFenceSetVersion(ctx)
	api.emitMintedGeoFenceSet(ctx, minted)
}

// emitMintedGeoFenceSet decodes a just-committed fence-set version's frozen snapshot and
// publishes its MANIFEST as a fact (ADR-078). announceMintedGeoFenceSet above is the single
// funnel every authoring path reaches it through; this function keeps its own nil check as
// defence rather than as the contract.
//
// It reads the fences back out of the SNAPSHOT COLUMN rather than re-querying the live
// fences, and that is the whole point: the snapshot is what a replay will later resolve
// this version to, so the fact and the archive are the same bytes by construction. Re-
// reading the live table would look identical in every test and would silently publish a
// LATER fence set under this version's number the moment two edits interleave.
//
// 🔴 IT NO LONGER HYDRATES, AND THE DELETED READ IS THE POINT OF THE WHOLE CHANGE. The
// stored snapshot ALREADY names geometry by content address; hydration existed only to
// expand those addresses into documents for a fact that had to carry them. A manifest
// carries the addresses, so announcing a fence edit now costs one row read instead of one
// row read plus an archive read of every geometry in the set — and the announcement's size
// stops depending on what the fences contain.
//
// A nil version (nothing was minted) or an undecodable snapshot publishes nothing: the
// projection then simply does not hold this version, which its own miss path reports as a
// loud unresolvable rather than as "no fences". Best-effort throughout — the authoring
// action has already committed and must not be failed by a wire problem.
func (api *Api) emitMintedGeoFenceSet(ctx context.Context, minted *GeoFenceSetVersion) {
	if api.GeoFenceSetPublisher == nil || minted == nil {
		return
	}
	stored, err := parseStoredGeoFenceSetSnapshot(minted.Snapshot)
	if err != nil {
		log.Error().Err(err).Int32("version", minted.Version).
			Msg("Unable to decode a just-minted geofence set snapshot; publishing no fence-set fact")
		return
	}
	api.emitGeoFenceSet(ctx, manifestFromStored(minted.Version, minted.MintedAt, stored))
}

// manifestFromStored projects a stored snapshot onto the manifest every seam hands out. The
// version and timestamp come from the ROW rather than from the document, because the row's
// columns are what any ordering or lookup selected on; the two are written together and
// agree, but only one of them is the thing that was queried.
//
// The fence slice is allocated unconditionally, so a manifest's Fences is never nil on any
// path — the same single-owner guarantee hydrateGeoFenceSetSnapshot provides for the
// hydrated form, and the reason parseStoredGeoFenceSetSnapshot does not normalize it.
func manifestFromStored(version int32, mintedAt time.Time, stored *storedGeoFenceSetSnapshot) *GeoFenceSetManifest {
	manifest := &GeoFenceSetManifest{
		Version:  version,
		MintedAt: mintedAt,
		Fences:   make([]GeoFenceManifestEntry, 0, len(stored.Fences)),
	}
	for _, ref := range stored.Fences {
		manifest.Fences = append(manifest.Fences, GeoFenceManifestEntry{Token: ref.Token, Hash: ref.Hash})
	}
	return manifest
}

// GeoFenceSetManifestAt returns the manifest of one fence-set version: which fences it held
// and the content address of each one's geometry.
//
// It is the read half of manifest delivery — how a holder of a version number learns what to
// resolve, whether it missed the fact, is replaying last week, or is starting cold. Unlike
// GeoFenceSetSnapshotAt it does NOT touch the geometry archive, so its cost and its response
// size are functions of the fence count alone.
//
// Version 0 (no fence set ever existed) yields an empty manifest rather than an error; an
// unknown POSITIVE version is gorm.ErrRecordNotFound, for the same reason GeoFenceSetSnapshotAt
// gives — a stamp naming a version that is not on record means the history was truncated, and
// answering "empty" would read as "no fences" rather than "cannot know". A NEGATIVE version is
// refused outright rather than folded into version 0's answer.
func (api *Api) GeoFenceSetManifestAt(ctx context.Context, version int32) (*GeoFenceSetManifest, error) {
	if version < 0 {
		return nil, errNegativeFenceSetVersion(version)
	}
	if version == 0 {
		return &GeoFenceSetManifest{Version: 0, Fences: []GeoFenceManifestEntry{}}, nil
	}
	row, stored, err := api.fenceSetVersionRow(ctx, version)
	if err != nil {
		return nil, err
	}
	return manifestFromStored(row.Version, row.MintedAt, stored), nil
}

// CurrentGeoFenceSetManifest returns the manifest of the tenant's CURRENT fence-set version
// — the version a location event resolved right now would be stamped with, together with the
// fences that version froze.
//
// It exists for event-processing's startup reconcile and its periodic sweep, which have to
// re-seed a live containment cache that survives no restart. As with
// CurrentGeoFenceSetSnapshot, reading the version and then the manifest separately would be
// two statements about a moving target: a fence edit landing between them yields a fence set
// filed under a number that is not the one the caller was told is current. One read of one
// row cannot disagree with itself.
//
// A tenant that has never had a fence yields version 0 with an empty fence list, matching the
// stamp such a tenant's events carry.
func (api *Api) CurrentGeoFenceSetManifest(ctx context.Context) (*GeoFenceSetManifest, error) {
	row, stored, err := api.fenceSetVersionRow(ctx, 0)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A tenant that has never had a fence. Not an error here, unlike the version-addressed
		// door: "you have no fence set" is a true answer to "what is your current one".
		return &GeoFenceSetManifest{Version: 0, Fences: []GeoFenceManifestEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return manifestFromStored(row.Version, row.MintedAt, stored), nil
}

// GeoFenceGeometryDocuments returns the archived geometry documents stored under the given
// content addresses — the other half of manifest delivery, and the door a holder of a
// manifest resolves its entries through.
//
// 🔴 AN ADDRESS THE TENANT DOES NOT HOLD IS ABSENT FROM THE RESULT, NOT AN ERROR, AND THAT
// IS THE OPPOSITE OF WHAT hydrateGeoFenceSetSnapshot DOES WITH THE SAME MISS. The difference
// is who asked. A dangling reference INSIDE a stored snapshot is corruption — this service
// wrote both halves and one of them is missing — so hydration refuses rather than hand back
// a short fence set. Here the hashes are caller-supplied, so "which of these do you hold?"
// is an ordinary question and the set of answers is the answer. Incompleteness is the
// CALLER's to interpret, and the caller that matters interprets it per fence: a manifest
// entry whose body did not come back becomes a fence carrying an error, never a fence that
// silently is not there.
//
// The result is ordered by hash so a caller comparing two responses, or a test comparing a
// response to a fixture, is not comparing against whatever order the storage engine chose.
func (api *Api) GeoFenceGeometryDocuments(ctx context.Context, hashes []string) ([]GeoFenceGeometryDocument, error) {
	if len(hashes) > MaxGeoFenceGeometryHashesPerRequest {
		return nil, fmt.Errorf("cannot read %d geofence geometry documents in one request; the limit is %d",
			len(hashes), MaxGeoFenceGeometryHashesPerRequest)
	}
	documents := make([]GeoFenceGeometryDocument, 0, len(hashes))
	// As in archiveGeoFenceGeometries, this skips a round trip rather than guarding
	// correctness — gorm renders an empty slice as `hash in (NULL)`, which matches nothing.
	// It is stated because nobody can see that from the call site, and because the sibling
	// primary-key form behaves OPPOSITELY on the same input (see rdb.FindByIds).
	if len(hashes) == 0 {
		return documents, nil
	}
	blobs := make([]GeoFenceGeometryBlob, 0, len(hashes))
	if err := api.RDB.DB(ctx).Where("hash in ?", hashes).Order("hash").Find(&blobs).Error; err != nil {
		return nil, err
	}
	for i := range blobs {
		documents = append(documents, GeoFenceGeometryDocument{
			Hash: blobs[i].Hash,
			// Handed on verbatim. The address is the SHA-256 of exactly these bytes, and
			// the caller re-derives it to check what it received, so anything that re-
			// encodes the document here breaks every caller's verification at once.
			Document: json.RawMessage(blobs[i].Document),
		})
	}
	return documents, nil
}

// Get geofences by id.
func (api *Api) GeoFencesById(ctx context.Context, ids []uint) ([]*GeoFence, error) {
	return rdb.FindByIds[GeoFence](api.RDB.DB(ctx), ids)
}

// Get geofences by token.
func (api *Api) GeoFencesByToken(ctx context.Context, tokens []string) ([]*GeoFence, error) {
	found := make([]*GeoFence, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for geofences that meet criteria.
func (api *Api) GeoFences(ctx context.Context, criteria GeoFenceSearchCriteria) (*GeoFenceSearchResults, error) {
	results := make([]GeoFence, 0)
	db, pag := api.RDB.ListOf(ctx, &GeoFence{}, func(result *gorm.DB) *gorm.DB {
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &GeoFenceSearchResults{Results: results, Pagination: pag}, nil
}

// CurrentFenceSetVersion returns the tenant's active fence-set version, or 0 when the
// tenant has never had a fence.
//
// 🔴 0 IS NOT "NO FENCES". A tenant that created a fence and then deleted it sits at a
// non-zero version whose snapshot is empty, and that distinction is what keeps replay
// honest: an event stamped 0 was resolved before any fence existed, while an event
// stamped 7 was resolved against a fence set that is knowable even if it was empty.
//
// It is read on the resolve hot path through ProfileScopeByDeviceType, so it is served
// from that lookup's existing cache rather than from a cache of its own — see
// CachedApi.EvictFenceSetVersion for how a fence change invalidates it.
func (api *Api) CurrentFenceSetVersion(ctx context.Context) (int32, error) {
	found := make([]GeoFenceSetVersion, 0, 1)
	// Select only the version: the snapshot column is the large one and no caller of
	// this function wants it.
	err := api.RDB.DB(ctx).Model(&GeoFenceSetVersion{}).
		Select("version").Order("version desc").Limit(1).Find(&found).Error
	if err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, nil
	}
	return found[0].Version, nil
}

// fenceSetVersionRow reads ONE fence-set version row and decodes its stored snapshot — the
// shared prologue of all four version-addressed reads (the two manifest doors and the two
// snapshot doors).
//
// 🔴 IT EXISTS BECAUSE THE SUBTLETY BELOW WAS WRITTEN OUT FOUR TIMES. Each of those doors has to
// take its version and timestamp from the ROW rather than from the snapshot DOCUMENT: the two
// are written together and agree, but only the columns are what an ordering or a lookup selected
// on, and a future divergence would surface as a fence set filed under a number nobody queried.
// Four copies of that reasoning is four places for a later fix — a predicate, an index hint, a
// soft-delete clause — to land in one of.
//
// version above zero selects that exact version; zero selects the tenant's CURRENT one. A
// negative version is refused by the callers before they get here, because the two forms answer
// it differently and neither answer belongs in a shared helper.
func (api *Api) fenceSetVersionRow(ctx context.Context, version int32) (*GeoFenceSetVersion, *storedGeoFenceSetSnapshot, error) {
	found := make([]GeoFenceSetVersion, 0, 1)
	query := api.RDB.DB(ctx)
	if version > 0 {
		query = query.Where("version = ?", version)
	} else {
		query = query.Order("version desc")
	}
	if err := query.Limit(1).Find(&found).Error; err != nil {
		return nil, nil, err
	}
	if len(found) == 0 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	stored, err := parseStoredGeoFenceSetSnapshot(found[0].Snapshot)
	if err != nil {
		return nil, nil, err
	}
	return &found[0], stored, nil
}

// errNegativeFenceSetVersion refuses a version below zero.
//
// 🔴 IT EXISTS BECAUSE BOTH DOORS USED TO FOLD IT INTO THE KNOWN-EMPTY ANSWER, which is a
// fall-through answering a NUMBER under the wrong cause. Version 0 means something precise —
// the tenant had never created a fence when the event was resolved, which is knowledge — and
// -5 means the caller is confused or something upstream mangled a stamp. Returning knowledge
// for a question that could not be asked in good faith hides the mangling, and it hides it
// behind the one answer that reads as legitimate. Versions mint from 1, so nothing reachable
// through a real stamp lands here; that is the argument for it being cheap, not for it being
// unnecessary.
func errNegativeFenceSetVersion(version int32) error {
	return fmt.Errorf("fence-set version %d is negative; versions are minted from 1 and 0 means "+
		"the tenant had no fence set at all", version)
}

// GeoFenceSetSnapshotAt returns the frozen fence set of one fence-set version — the
// fences that were live when an event stamped with that version was resolved. This is
// what a replay or a rule preview evaluates against, instead of re-reading the live
// fences and silently answering a different question.
//
// Version 0 (no fence set ever existed) yields an empty snapshot rather than an error;
// an unknown non-zero version is gorm.ErrRecordNotFound, because a stamp naming a
// version that is not on record means the history was truncated and answering "empty"
// would look like "no fences" rather than "cannot know".
func (api *Api) GeoFenceSetSnapshotAt(ctx context.Context, version int32) (*GeoFenceSetSnapshot, error) {
	if version < 0 {
		return nil, errNegativeFenceSetVersion(version)
	}
	if version == 0 {
		return &GeoFenceSetSnapshot{Version: 0, Fences: []GeoFenceSnapshotRef{}}, nil
	}
	_, stored, err := api.fenceSetVersionRow(ctx, version)
	if err != nil {
		return nil, err
	}
	return hydrateGeoFenceSetSnapshot(api.RDB.DB(ctx), stored)
}

// CurrentGeoFenceSetSnapshot returns the frozen fence set of the tenant's CURRENT
// fence-set version — the version a location event resolved right now would be stamped
// with, together with the fences that version froze.
//
// It exists for event-processing's startup reconcile, which has to re-seed a live
// containment cache that survives no restart. That caller could instead read
// CurrentFenceSetVersion and then GeoFenceSetSnapshotAt, and the difference is not
// convenience: those are two statements about a moving target, so a fence edit landing
// between them yields a snapshot whose version is not the one the caller was told is
// current — a set filed under the wrong number, which is precisely the failure the
// version stamp exists to prevent. One read of one row cannot disagree with itself.
//
// A tenant that has never had a fence yields version 0 with an empty fence list, matching
// the stamp such a tenant's events carry (see CurrentFenceSetVersion for why 0 is
// knowledge rather than absence).
func (api *Api) CurrentGeoFenceSetSnapshot(ctx context.Context) (*GeoFenceSetSnapshot, error) {
	row, stored, err := api.fenceSetVersionRow(ctx, 0)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &GeoFenceSetSnapshot{Version: 0, Fences: []GeoFenceSnapshotRef{}}, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := hydrateGeoFenceSetSnapshot(api.RDB.DB(ctx), stored)
	if err != nil {
		return nil, err
	}
	// The row's own Version is authoritative over the number embedded in the snapshot
	// document: they are written together and agree, but only one of them is the column
	// the ordering above selected on.
	snapshot.Version = row.Version
	return snapshot, nil
}

// parseStoredGeoFenceSetSnapshot decodes a stored snapshot into the STORED form —
// content references, no geometry. Every caller that needs evaluable fences goes on
// through hydrateGeoFenceSetSnapshot; see storedGeoFenceSetSnapshot for why the two
// forms are separate types.
//
// It does NOT normalize a missing fence list to a non-nil empty slice, and the omission
// is deliberate rather than an oversight. hydrateGeoFenceSetSnapshot allocates the
// hydrated slice unconditionally, so the non-nil guarantee every caller depends on has
// exactly one owner. Normalizing here as well was dead the moment hydration existed —
// removing it left every test green, which is precisely what says nothing was relying
// on it.
func parseStoredGeoFenceSetSnapshot(raw datatypes.JSON) (*storedGeoFenceSetSnapshot, error) {
	snapshot := &storedGeoFenceSetSnapshot{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, snapshot); err != nil {
			return nil, fmt.Errorf("unable to parse geofence set snapshot: %w", err)
		}
	}
	return snapshot, nil
}

// archiveGeoFenceGeometries stores a mint's canonical geometry documents under their
// content addresses, writing only the ones the tenant does not already hold. The map is
// keyed by hash, so documents repeated within one fence set are already collapsed by the
// time it arrives.
//
// 🔴 THE ABSENCE CHECK IS AN EXPLICIT READ, NOT A CONFLICT CLAUSE, AND THE DIFFERENCE IS
// NOT STYLE. The (tenant_id, hash) unique index is declared by the migration and CANNOT
// be declared on the live model — tenant_id lives in the embedded rdb.TenantScoped, which
// cannot carry a priority-1 tag (the same constraint GeoFenceSetVersion.Version records).
// So the unit tests, which AutoMigrate the live models onto sqlite, build this table with
// NO unique index at all: an implementation that leaned on ON CONFLICT DO NOTHING would
// deduplicate correctly in production and silently duplicate every document under every
// test, which is the exact shape of a gate that cannot see what it is gating. Reading
// first makes the behaviour the same on both engines and testable on either.
//
// The index still earns its place as the CONSTRAINT — two concurrent mints can both find
// a hash absent — which is why the insert keeps DO NOTHING as its race backstop. A
// conflict there means the other transaction stored the identical bytes, since the
// address IS the content, so there is nothing to reconcile and nothing to fail.
func archiveGeoFenceGeometries(tx *gorm.DB, documents map[string][]byte) error {
	// A round trip skipped, not a correctness guard: gorm renders an empty slice as
	// `hash in (NULL)`, which matches nothing (verified against the pinned driver), so
	// removing this changes only whether the statement is issued. Stated because no test
	// can distinguish the two — nobody should write one that only appears to.
	if len(documents) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(documents))
	for hash := range documents {
		hashes = append(hashes, hash)
	}
	existing := make([]GeoFenceGeometryBlob, 0, len(hashes))
	if err := tx.Select("hash").Where("hash in ?", hashes).Find(&existing).Error; err != nil {
		return err
	}
	for i := range existing {
		delete(documents, existing[i].Hash)
	}
	if len(documents) == 0 {
		return nil
	}
	// Sorted so a mint's inserts are ordered by content address rather than by map
	// iteration, which keeps two runs over the same fence set from deadlocking against
	// each other on the unique index in opposite orders.
	missing := make([]string, 0, len(documents))
	for hash := range documents {
		missing = append(missing, hash)
	}
	sort.Strings(missing)
	blobs := make([]GeoFenceGeometryBlob, 0, len(missing))
	for _, hash := range missing {
		blobs = append(blobs, GeoFenceGeometryBlob{Hash: hash, Document: datatypes.JSON(documents[hash])})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&blobs).Error
}

// sameGeoFenceRefs reports whether two stored reference lists describe the same fences
// frozen at the same geometry. It is what decides that a mutation changed nothing any
// stamp resolves to; see mintGeoFenceSetVersion for why that is an exact question.
//
// slices.Equal rather than reflect.DeepEqual, and the difference is not stylistic.
// parseStoredGeoFenceSetSnapshot deliberately declines to normalize a missing fence list
// into an empty slice, so a nil list and an empty one describe the same fence set while
// DeepEqual calls them different. slices.Equal compares lengths first, so it calls them
// the same. The divergence is currently unreachable anyway — every stored snapshot is
// marshalled from a non-nil slice and round-trips as "fences":[] — but that is a property
// of the marshalling, asserted nowhere, and one `omitempty` away from being false.
//
// It compares the PAIRING rather than the multiset of hashes, which is the natural way to
// write it and needs no argument to justify. Worth recording that the multiset form would
// be equivalent TODAY, and why the equivalence is not something to lean on: every authoring
// action changes exactly one fence, so for a mutation to preserve the multiset of hashes the
// changed fence's new hash would have to equal its old one — i.e. nothing changed. That is a
// property of the AUTHORING API, not of the comparison, and it would stop holding the day a
// bulk edit lands. A mutation-tested multiset variant of this function survives the whole
// suite for exactly that reason; it is an equivalent mutant, not a coverage hole.
//
// Both sides are built by ordering on token, so comparing by position is total. A
// collation difference between the two writes could only reorder them, which reads as
// UNEQUAL and mints — the safe direction. The unsafe direction cannot be reached:
// element-wise equality of two ordered lists implies the sets are equal.
func sameGeoFenceRefs(a, b []storedGeoFenceRef) bool {
	return slices.Equal(a, b)
}

// hydrateGeoFenceSetSnapshot resolves a stored snapshot's content references into the
// evaluable fence set every caller outside this file works with.
//
// The archive read is ONE statement for the whole set, not one per fence: a version names
// at most the tenant's geoFenceCeiling fences (governance.MaxGeoFenceCeiling at the very
// most, whatever any tier granted), and distinct geometries are usually far fewer still
// because an edit changes one fence and leaves the rest addressing exactly the rows they
// already did.
//
// 🔴 A REFERENCE THE ARCHIVE CANNOT ANSWER IS AN ERROR, NEVER A DROPPED FENCE. Returning
// a short fence set would be indistinguishable downstream from a tenant who really has
// that many fences — containment would answer "outside" for a device that is inside, and
// report a healthy rule that never fires. This is the same reason the fence-set fact
// carries FencesOmitted as a field rather than as an empty list.
func hydrateGeoFenceSetSnapshot(tx *gorm.DB, stored *storedGeoFenceSetSnapshot) (*GeoFenceSetSnapshot, error) {
	hydrated := &GeoFenceSetSnapshot{
		Version: stored.Version,
		Fences:  make([]GeoFenceSnapshotRef, 0, len(stored.Fences)),
	}
	// As in archiveGeoFenceGeometries, this skips a round trip rather than guarding
	// correctness — an empty IN matches nothing. The allocation above is what actually
	// makes the returned fence list non-nil, on every path.
	if len(stored.Fences) == 0 {
		return hydrated, nil
	}
	// Deduplicated because a fence set may name the same geometry from several fences,
	// which is the common case after a bulk import. Correctness does not depend on it —
	// a repeated value in an IN clause selects the same rows — so this is a narrower
	// read, not a different answer.
	hashes := make([]string, 0, len(stored.Fences))
	seen := make(map[string]struct{}, len(stored.Fences))
	for _, ref := range stored.Fences {
		if _, dup := seen[ref.Hash]; dup {
			continue
		}
		seen[ref.Hash] = struct{}{}
		hashes = append(hashes, ref.Hash)
	}
	blobs := make([]GeoFenceGeometryBlob, 0, len(hashes))
	if err := tx.Where("hash in ?", hashes).Find(&blobs).Error; err != nil {
		return nil, err
	}
	documents := make(map[string]json.RawMessage, len(blobs))
	for i := range blobs {
		documents[blobs[i].Hash] = json.RawMessage(blobs[i].Document)
	}
	for _, ref := range stored.Fences {
		document, ok := documents[ref.Hash]
		if !ok {
			return nil, fmt.Errorf(
				"geofence set version %d names geometry %s for fence %q, which is not in the archive",
				stored.Version, ref.Hash, ref.Token)
		}
		hydrated.Fences = append(hydrated.Fences, GeoFenceSnapshotRef{Token: ref.Token, Geometry: document})
	}
	return hydrated, nil
}

// deviceTypeIdsForTenant returns every device type id of the tenant in context — the
// keys of the ProfileScope cache, which is where the fence-set version is held. It is
// the tenant-wide sibling of deviceTypeIdsForProfile and exists for the eviction
// fan-out only (see CachedApi.EvictFenceSetVersion), never on a read path.
func (api *Api) deviceTypeIdsForTenant(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := api.RDB.DB(ctx).Model(&DeviceType{}).Pluck("id", &ids).Error
	return ids, err
}

// mintGeoFenceSetVersion appends the next fence-set version for the tenant, freezing
// the fence set AS IT STANDS IN tx. It must therefore be called AFTER the mutation
// within the same transaction — calling it before would snapshot the pre-change set
// under the post-change version number, which is the one arrangement that looks correct
// in every test that only checks the number went up.
//
// 🔴 IT RETURNS (nil, nil) WHEN THE FENCE SET DID NOT CHANGE, and every caller must evict
// and publish only when it hands back a version. A mutation leaving the (token, geometry)
// reference list exactly what the current version already froze — renaming a fence,
// editing its description or its metadata — mints nothing, because a version exists to
// make a STAMP resolve to the right shapes and such an edit changes no shape any stamp
// resolves to.
//
// 🔴 THIS IS AN EXACT COMPARISON OF THE STORED REFERENCE LIST, NEVER A GUESS ABOUT WHETHER
// AN EDIT MATTERED, and the distinction is the entire licence for skipping. This function
// used to mint unconditionally on the grounds that "did this edit change what a rule
// computes" could not be answered cheaply or safely here. It can be answered exactly: the
// snapshot IS what rules compute against, and name, description and metadata appear in
// neither the snapshot nor the manifest built from it. The old reasoning was sound about a
// heuristic and this is not one — an equal list is the SAME list, not a list judged
// similar enough.
//
// The cost of a spare version was also under-priced, as "one row". It is not:
//   - it consumes one of the four fence-set versions the engine retains per tenant
//     (runtime.MaxRetainedFenceSetVersions), so four renames in a row evict a version
//     still in use by in-flight events and force a blocking archive fetch; and
//   - it drives evictFenceSetVersion, which is a tenant-wide ProfileScope cache eviction
//     costing the resolve hot path a miss per device type.
//
// Both were paid, repeatedly, for edits that changed nothing.
//
// 🔴 WHY THIS STOPS AT FENCES. Three sibling histories in this codebase publish versions
// the same way — device profiles, entity groups, and (in their own services) dashboards and
// connectors — and none of them guards against a no-op publish either. Do NOT carry this
// reasoning next door. All three differences cut the same way: those are an explicit Publish
// the user asked for rather than a side effect of editing something else; their version rows
// carry a LABEL, a description and a publisher, so two versions with identical content are
// genuinely different artifacts and "unchanged" is not even well defined; and their mutations
// return a non-nullable version, so there is nothing for a skip to hand back. The right
// answer there is a refusal carrying a reason, in the resolver, not this. For profiles it
// would also be actively harmful — api_profile_versions.go records that a dropped emit is
// recovered BY A LATER PUBLISH, so a content skip would delete a repair path that is still
// load-bearing there.
//
// 🔴 SKIPPING ALSO CLOSES AN ACCIDENTAL REPAIR CHANNEL, and that is worth stating because
// nothing else in the file records it. The evict and the publish run OUTSIDE the mint's
// transaction, so a process that dies between commit and publish leaves the engine on the
// previous fence set. Previously any later no-op edit re-published the current set and
// repaired that by accident. It no longer does, so the designed paths — JetStream
// redelivery, the startup reconcile, and the five-minute sweep — are now the ONLY repair
// for a lost fact, and the ProfileScope TTL the only one for a lost eviction. All three
// exist and are bounded; none of them used to be load-bearing alone.
//
// 🔴 VERSION NUMBERS STAY DENSE. Skipping mints NOTHING; it never allocates a number and
// discards it. The console's fence history panel walks versions from the latest down to 1
// one at a time, so a scheme that burned numbers would leave holes it reads as truncated
// history.
func (api *Api) mintGeoFenceSetVersion(tx *gorm.DB, now time.Time,
	capsAttempt geoFenceCapsAttempt) (*GeoFenceSetVersion, error) {
	// The version number and the snapshot to compare against come out of ONE row read,
	// inside tx. Reaching for fenceSetVersionRow instead would read through
	// api.RDB.DB(ctx) — OUTSIDE this transaction — comparing against a row a concurrent
	// mint can move independently of the number computed from it.
	latest := make([]GeoFenceSetVersion, 0, 1)
	if err := tx.Model(&GeoFenceSetVersion{}).
		Select("version", "snapshot").Order("version desc").Limit(1).Find(&latest).Error; err != nil {
		return nil, err
	}
	next := int32(1)
	if len(latest) == 1 {
		next = latest[0].Version + 1
	}

	fences := make([]GeoFence, 0)
	// Ordered by token so the snapshot bytes are a function of the fence set alone.
	// Deliberately unpaginated: the tenant's geoFenceCeiling bounds the row count, and the
	// mint needs the WHOLE set — a page of it would freeze a snapshot missing fences. That
	// ceiling is now a tier setting, so the real bound here is governance.MaxGeoFenceCeiling
	// (the most any tier may grant) rather than a constant in this package.
	if err := tx.Order("token asc").Find(&fences).Error; err != nil {
		return nil, err
	}
	// The snapshot records CONTENT ADDRESSES, and the documents they name are archived
	// here in the same transaction as the version row that references them. Order
	// matters and it is not stylistic: a version whose snapshot names a document the
	// archive does not hold is a version that cannot be hydrated, and hydration failure
	// is loud but unrecoverable. Writing the blobs first means the only way to observe a
	// dangling reference is a transaction that did not commit at all.
	refs := make([]storedGeoFenceRef, 0, len(fences))
	documents := make(map[string][]byte, len(fences))
	for i := range fences {
		// Hash what was READ, and archive those same bytes — never the authored text.
		// See GeoFenceGeometryHash for why the two differ and why the difference is
		// unbounded.
		document := []byte(fences[i].Geometry)
		hash := GeoFenceGeometryHash(document)
		refs = append(refs, storedGeoFenceRef{Token: fences[i].Token, Hash: hash})
		documents[hash] = document
	}
	// oldSum is what the CURRENT version's distinct geometry summed to. knowOldSum is false
	// when there is no comparable number — an undecodable snapshot, or one written before
	// PositionSum existed — and the budget then FAILS OPEN for exactly this one mint, which
	// writes the field and heals the tenant.
	//
	// A tenant with no version yet always mints: there is nothing to be equal to, and the
	// console reads a fence that exists as implying a version of at least 1. Its oldSum is a
	// genuine zero rather than an unknown — no version means no fences, so a first create
	// really is all-new geometry and the budget applies to it in full.
	oldSum, knowOldSum := 0, true
	if len(latest) == 1 {
		current, err := parseStoredGeoFenceSetSnapshot(latest[0].Snapshot)
		switch {
		case err != nil:
			// 🔴 AN UNDECODABLE CURRENT SNAPSHOT MINTS; IT NEVER REFUSES. Returning this
			// error would let one corrupt row block every fence mutation the tenant makes,
			// permanently, with no route out through the API.
			//
			// 🔴 BUT IT REPAIRS THE HEAD ONLY, AND CALLING IT "SELF-REPAIR" WITHOUT THAT
			// BOUND WOULD BE A LIE. Nothing is written over: this appends N+1 and leaves
			// the undecodable row at N exactly where it was. Authoring recovers, and so
			// does everything reading the CURRENT set — but the history walk still errors
			// at N, a replay preview reaching N still errors, and an event already stamped
			// N is unevaluatable for as long as it can be replayed. Those need the row
			// repaired by hand; there is no path here that reaches them.
			log.Error().Err(err).Int32("version", latest[0].Version).
				Msg("Unable to decode the current geofence set snapshot; minting a new version " +
					"rather than comparing against it")
			knowOldSum = false
		case sameGeoFenceRefs(current.Fences, refs):
			return nil, nil
		case current.PositionSum != nil:
			oldSum = *current.PositionSum
		default:
			knowOldSum = false
		}
	}

	// The whole-set position budget is spent on the DISTINCT geometry the tenant holds —
	// which is exactly what `documents` is, since it is keyed by content address. The archive
	// and event-processing's geometry cache both dedupe the same way, so two fences sharing a
	// shape cost one entry in all three places.
	//
	// 🔴 IT IS COUNTED HERE, FROM BYTES ALREADY IN HAND, AND NOT READ BACK OUT OF A COLUMN.
	// The obvious design stores a position count on GeoFenceGeometryBlob and sums it in SQL.
	// That archive is IMMUTABLE and content-addressed — a geometry change writes a NEW row —
	// so a column added by a migration is permanently NULL for every row an older pod wrote
	// during the rolling update that deployed it, SUM() skips NULLs in silence, and the budget
	// under-counts forever, fail-open, with nothing to notice it. Here the documents are in
	// memory: at most the fence ceiling of them, each bounded by MaxGeoFenceGeometryBytes,
	// parsed once per mint.
	//
	// 🔴 COUNTED AFTER THE SKIP ABOVE, NOT BEFORE IT. A mutation that leaves the fence set as
	// it was — a rename, a description edit, a metadata edit — returns at the skip and never
	// reaches this loop, which is both the common case and the one that must stay cheap.
	newSum, knowNewSum := 0, true
	for hash, document := range documents {
		n, err := geoFencePositionsIn(document)
		if err != nil {
			// 🔴 THE DAY A SECOND GEOMETRY KIND IS ACCEPTED, THIS BRANCH BECOMES A SILENT
			// EXEMPTION. geoFencePositionsIn errors on a kind it cannot count — correctly,
			// since a zero would read as "this fence is free" — but the handling below turns
			// that into a permanent per-mint stand-aside, so a tenant holding one fence of a
			// newly-supported kind would lose whole-set metering entirely, with only a log
			// line and no failing test. Whoever accepts the next kind owes it a position
			// count here BEFORE the validator accepts it, not after.
			// 🔴 FAIL OPEN, DO NOT RETURN. This document belongs to a fence that is NOT
			// being edited — `documents` is the whole post-mutation set — so returning the
			// error here would let one uncorrelated corrupt row block every later mutation,
			// including the DELETE of the corrupt fence itself. That is the same trap the
			// undecodable-snapshot path above refuses, arriving through a different door.
			// Unreachable in any case: every write to this column is canonicalized first.
			log.Error().Err(err).Str("hash", hash).
				Msg("Unable to count the positions in a stored geofence geometry; skipping this " +
					"tenant's geofence position budget check for this change rather than blocking it")
			knowNewSum = false
			break
		}
		newSum += n
	}

	// 🔴 THE BUDGET REFUSES GROWTH, NOT SIZE, AND THE SECOND CONDITION IS NOT BELT AND
	// BRACES. A plain `newSum > budget` refuses a tenant already over the budget from doing
	// anything about it: shrinking a fence still leaves them over, and DELETING one arrives
	// here too, because DeleteGeoFence hard-deletes and then mints inside one transaction.
	// Refusing a delete for being over budget is worse than having no budget.
	//
	// 🔴 A SHRINKING EDIT CAN STILL BE REFUSED, AND THAT IS HONEST RATHER THAN A BUG. The
	// sum is over DISTINCT geometry, so shrinking one of two fences that shared a shape
	// UN-DEDUPES it: 512 counted once becomes 512 + 400 counted twice. The tenant made a
	// fence smaller and the charge grew — because the cache footprint really did grow, which
	// is the thing being metered. The error says so rather than leaving the author to guess.
	//
	// 🔴 AND THE RATCHET IS DELIBERATE: for a tenant over budget, deleting a fence lowers
	// oldSum, so recreating that same fence is then refused. Grandfathered headroom is
	// forfeited by deleting, which is what "cannot grow" means when the starting point is
	// already too high. A high-water mark would remove the ratchet and would never decay,
	// which is worse. It is why the token-rename recipe is create-then-delete rather than the
	// reverse; see errGeoFenceTokenImmutable.
	if knowOldSum && knowNewSum && newSum > oldSum {
		caps, err := capsAttempt.require("the tenant's total geofence position count")
		if err != nil {
			return nil, err
		}
		if newSum > caps.PositionBudget {
			return nil, api.refuseGeoFenceCap(governance.GeoFencePositionBudgetField,
				fmt.Errorf("this change would bring the tenant's geofences to %d positions of distinct "+
					"geometry, up from %d; this tenant's %s is %d (%s). The count is over DISTINCT "+
					"geometry, so making one of several identical fences different can raise it even "+
					"when that fence got smaller. An edit that does not raise the total is always "+
					"allowed, including a delete",
					newSum, oldSum, governance.GeoFencePositionBudgetField, caps.PositionBudget,
					governance.GeoFencePositionBudgetBecause))
		}
	}

	// Nothing is archived on the skip path above, and nothing needs to be: an equal
	// reference list names identical hashes, and every one of those was archived by the
	// transaction that first wrote a version naming it. There is no missing blob for a
	// repeat write to heal — the only deletion that reaches this table is the whole-tenant
	// purge, which takes the versions with it.
	if err := archiveGeoFenceGeometries(tx, documents); err != nil {
		return nil, err
	}
	// PositionSum is written on every mint that could COUNT one, which is what makes the
	// absent-sum fail-open above self-healing: a tenant carried forward from a version that
	// predates the field is fully metered again from its next fence edit onwards.
	//
	// 🔴 AND IT IS LEFT ABSENT WHEN THE COUNT FAILED, RATHER THAN WRITTEN AS THE PARTIAL SUM.
	// A partial sum is a number that looks authoritative and is too small, which would meter
	// the tenant's next edit against a total lower than what they hold and refuse a change
	// that made nothing worse. Absent already means "cannot compare, stand aside" — the one
	// honest thing to record about a count that did not finish.
	stored := &storedGeoFenceSetSnapshot{Version: next, Fences: refs}
	if knowNewSum {
		stored.PositionSum = &newSum
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}

	version := &GeoFenceSetVersion{
		Version:  next,
		Snapshot: datatypes.JSON(encoded),
		MintedAt: now,
	}
	// A concurrent mint that already took `next` fails here on the per-tenant unique
	// index and rolls the whole mutation back, exactly as a concurrent profile publish
	// does. Losing the write is the correct outcome: the alternative is two different
	// fence sets sharing one version number, which no stamp could ever disambiguate.
	if err := tx.Create(version).Error; err != nil {
		return nil, err
	}
	return version, nil
}
