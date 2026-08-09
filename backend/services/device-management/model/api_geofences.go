// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
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

// CreateGeoFence creates a geofence and mints a new tenant fence-set version in the
// SAME transaction (ADR-078). The two are one atomic fact: a fence that exists at a
// version the stamp never advanced to would be invisible to every event resolved after
// it was created, and a version minted without the fence would claim a snapshot the
// fence is not in.
func (api *Api) CreateGeoFence(ctx context.Context, request *GeoFenceCreateRequest) (*GeoFence, error) {
	if err := validateGeoFenceToken(request.Token); err != nil {
		return nil, err
	}
	if _, err := validateGeoFenceGeometry(request.Geometry); err != nil {
		return nil, err
	}

	created := &GeoFence{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		Geometry:       datatypes.JSON(request.Geometry),
	}
	var minted *GeoFenceSetVersion
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
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
func (api *Api) UpdateGeoFence(ctx context.Context, token string,
	request *GeoFenceCreateRequest) (*GeoFence, error) {
	if err := validateGeoFenceToken(request.Token); err != nil {
		return nil, err
	}
	if _, err := validateGeoFenceGeometry(request.Geometry); err != nil {
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
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.Geometry = datatypes.JSON(request.Geometry)

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
// publishes it as a fact (ADR-078). It is the ONE place the three authoring paths funnel
// through, so a fourth mutation cannot mint a version and forget to announce it.
//
// It reads the fences back out of the SNAPSHOT COLUMN rather than re-querying the live
// fences, and that is the whole point: the snapshot is what a replay will later resolve
// this version to, so the fact and the archive are the same bytes by construction. Re-
// reading the live table would look identical in every test and would silently publish a
// LATER fence set under this version's number the moment two edits interleave.
//
// A nil version (nothing was minted) or an undecodable snapshot publishes nothing: the
// projection then simply does not hold this version, which its own miss path reports as a
// loud unresolvable rather than as "no fences". Best-effort throughout — the authoring
// action has already committed and must not be failed by a wire problem.
func (api *Api) emitMintedGeoFenceSet(ctx context.Context, minted *GeoFenceSetVersion) {
	if api.GeoFenceSetPublisher == nil || minted == nil {
		return
	}
	snapshot, err := parseGeoFenceSetSnapshot(minted.Snapshot)
	if err != nil {
		log.Error().Err(err).Int32("version", minted.Version).
			Msg("Unable to decode a just-minted geofence set snapshot; publishing no fence-set fact")
		return
	}
	api.emitGeoFenceSet(ctx, &GeoFenceSetMintedEvent{
		Version:  minted.Version,
		Fences:   snapshot.Fences,
		MintedAt: minted.MintedAt,
	})
}

// Get geofences by id.
func (api *Api) GeoFencesById(ctx context.Context, ids []uint) ([]*GeoFence, error) {
	found := make([]*GeoFence, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
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
	if version <= 0 {
		return &GeoFenceSetSnapshot{Version: 0, Fences: []GeoFenceSnapshotRef{}}, nil
	}
	found := make([]GeoFenceSetVersion, 0, 1)
	if err := api.RDB.DB(ctx).Where("version = ?", version).Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return parseGeoFenceSetSnapshot(found[0].Snapshot)
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
	found := make([]GeoFenceSetVersion, 0, 1)
	if err := api.RDB.DB(ctx).Order("version desc").Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return &GeoFenceSetSnapshot{Version: 0, Fences: []GeoFenceSnapshotRef{}}, nil
	}
	snapshot, err := parseGeoFenceSetSnapshot(found[0].Snapshot)
	if err != nil {
		return nil, err
	}
	// The row's own Version is authoritative over the number embedded in the snapshot
	// document: they are written together and agree, but only one of them is the column
	// the ordering above selected on.
	snapshot.Version = found[0].Version
	return snapshot, nil
}

// parseGeoFenceSetSnapshot decodes a stored snapshot, normalizing a missing fence list
// to a non-nil empty slice so a version minted by the deletion of the last fence never
// nil-derefs downstream.
func parseGeoFenceSetSnapshot(raw datatypes.JSON) (*GeoFenceSetSnapshot, error) {
	snapshot := &GeoFenceSetSnapshot{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, snapshot); err != nil {
			return nil, fmt.Errorf("unable to parse geofence set snapshot: %w", err)
		}
	}
	if snapshot.Fences == nil {
		snapshot.Fences = []GeoFenceSnapshotRef{}
	}
	return snapshot, nil
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
	refs := make([]GeoFenceSnapshotRef, 0, len(fences))
	for i := range fences {
		refs = append(refs, GeoFenceSnapshotRef{
			Token:    fences[i].Token,
			Geometry: json.RawMessage(fences[i].Geometry),
		})
	}
	encoded, err := json.Marshal(&GeoFenceSetSnapshot{Version: next, Fences: refs})
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
