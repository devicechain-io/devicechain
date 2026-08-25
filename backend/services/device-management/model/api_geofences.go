// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
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
// matches how UpdateDeviceProfile refuses the same move. A caller that genuinely wants a
// different token creates a second fence and deletes the first, which is the operation
// that also makes them confront the rules naming the old one.
func errGeoFenceTokenImmutable(token string, requested string) error {
	if requested == token {
		return nil
	}
	return fmt.Errorf("cannot rename geofence %q to %q: a fence's token is immutable, "+
		"because rules name fences by token and a rename would leave them naming nothing",
		token, requested)
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
	_, canonicalGeometry, err := validateGeoFenceGeometry(request.Geometry)
	if err != nil {
		return nil, err
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
		// The fences-per-tenant bound (see MaxGeoFencesPerTenant) is checked inside the
		// transaction so it reads the same state the insert lands in. It is still a
		// count-then-insert, so two simultaneous creates at the limit can both pass —
		// deliberately not serialized with a lock, because the bound exists to keep the
		// per-event containment cost computable, and being one over it for the moment
		// between two concurrent authoring calls does not threaten that.
		var n int64
		if err := tx.Model(&GeoFence{}).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxGeoFencesPerTenant {
			return fmt.Errorf("the tenant already has %d geofences; the limit is %d (every fence in scope "+
				"is tested per location event and the publish-time cost gate cannot see how many there are)",
				n, MaxGeoFencesPerTenant)
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		var err error
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	api.evictFenceSetVersion(ctx)
	api.emitMintedGeoFenceSet(ctx, minted)
	return created, nil
}

// UpdateGeoFence replaces a geofence and mints a new fence-set version. It mints even
// when only the name changed: "did this edit change what a rule computes" is a question
// this layer cannot answer cheaply or safely, and the failure modes are asymmetric — a
// spare version costs one row, while a missed one silently keeps events pointing at a
// snapshot that no longer describes the fences.
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
	_, canonicalGeometry, err := validateGeoFenceGeometry(request.Geometry)
	if err != nil {
		return nil, err
	}

	matches, err := api.GeoFencesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

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
		if err := tx.Save(updated).Error; err != nil {
			return err
		}
		var err error
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	api.evictFenceSetVersion(ctx)
	api.emitMintedGeoFenceSet(ctx, minted)
	return updated, nil
}

// DeleteGeoFence hard-deletes a geofence and mints a new fence-set version, following
// this area's uniform hard-delete semantics (see api_delete.go): a soft delete would
// keep the token occupying the per-tenant unique index, so it could never be reused.
// Returns false with no error when the token names nothing — and mints nothing in that
// case, since nothing changed.
func (api *Api) DeleteGeoFence(ctx context.Context, token string) (bool, error) {
	deleted := false
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
		minted, err = api.mintGeoFenceSetVersion(tx, time.Now())
		return err
	})
	if err != nil {
		return false, err
	}
	if deleted {
		api.evictFenceSetVersion(ctx)
		api.emitMintedGeoFenceSet(ctx, minted)
	}
	return deleted, nil
}

// emitMintedGeoFenceSet decodes a just-committed fence-set version's frozen snapshot and
// publishes its MANIFEST as a fact (ADR-078). It is the ONE place the three authoring paths
// funnel through, so a fourth mutation cannot mint a version and forget to announce it.
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

// hydrateGeoFenceSetSnapshot resolves a stored snapshot's content references into the
// evaluable fence set every caller outside this file works with.
//
// The archive read is ONE statement for the whole set, not one per fence: a version names
// at most MaxGeoFencesPerTenant fences, and distinct geometries are usually far fewer
// still because an edit changes one fence and leaves the rest addressing exactly the rows
// they already did.
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
func (api *Api) mintGeoFenceSetVersion(tx *gorm.DB, now time.Time) (*GeoFenceSetVersion, error) {
	latest := make([]GeoFenceSetVersion, 0, 1)
	if err := tx.Model(&GeoFenceSetVersion{}).
		Select("version").Order("version desc").Limit(1).Find(&latest).Error; err != nil {
		return nil, err
	}
	next := int32(1)
	if len(latest) == 1 {
		next = latest[0].Version + 1
	}

	fences := make([]GeoFence, 0)
	// Ordered by token so the snapshot bytes are a function of the fence set alone.
	// Unbounded by construction: MaxGeoFencesPerTenant bounds the row count.
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
	if err := archiveGeoFenceGeometries(tx, documents); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(&storedGeoFenceSetSnapshot{Version: next, Fences: refs})
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
