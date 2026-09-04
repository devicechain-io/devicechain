// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file implements asset-type versioning (ADR-072), mirroring device-profile
// versioning (api_profile_versions.go) and dynamic entity-group versioning
// (api_group_versions.go): AssetType.PropertySchema is the mutable DRAFT;
// PublishAssetType re-validates that draft and freezes it into the next immutable
// AssetTypeVersion, then points AssetType.ActiveVersion at it; RollbackAssetType
// re-points the active pointer at an earlier version (non-destructive; the draft is
// untouched). An asset is validated against the ACTIVE PUBLISHED version, never the
// draft — which is what makes publishing a decision rather than a ceremony.
//
// Like a group publish and unlike a profile publish, this emits NO cross-service
// fact: an asset's property contract is consumed only inside device-management.

// assetTypeByToken loads the single asset type addressed by token, returning
// gorm.ErrRecordNotFound when absent so the versioning entry points fail closed
// (mirrors deviceProfileByToken / entityGroupByToken).
func (api *Api) assetTypeByToken(ctx context.Context, token string) (*AssetType, error) {
	matches, err := api.AssetTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// PublishAssetType freezes an asset type's current draft property schema into a new
// immutable version — the next monotonic integer for that type — and points the
// type's ActiveVersion at it, so assets of the type are immediately validated
// against the just-published contract. label/description are optional annotations;
// publishedBy is the caller's identity. Concurrent publishes are safe: the unique
// (asset_type_id, version) index rejects a duplicate version number.
//
// A type with NO draft schema is refused: there is nothing to freeze, and minting
// an empty version would quietly change what its assets are allowed to carry (from
// "no contract, so no properties" to "a contract declaring none"). A draft that IS
// an empty array publishes happily — that is an author saying "assets of this type
// carry nothing", which is a different statement and a reachable one.
func (api *Api) PublishAssetType(ctx context.Context, token string,
	label, description *string, publishedBy string) (*AssetTypeVersion, error) {
	assetType, err := api.assetTypeByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if assetType.PropertySchema == nil {
		return nil, fmt.Errorf("asset type %q has no property schema to publish", token)
	}

	// Re-validate the draft one last time before freezing, so validated ≡ frozen: the
	// schema was already checked when it was written, but re-checking here means a
	// published version can never carry a contract this build would reject — the same
	// belt-and-braces PublishEntityGroup applies by re-compiling its selector.
	specs, err := decodeSpecsStrict(*assetType.PropertySchema)
	if err != nil {
		return nil, fmt.Errorf("asset type %q: %w", token, err)
	}
	if err := assetPropertySpecs.validateSchema(specs); err != nil {
		return nil, fmt.Errorf("asset type %q: %w", token, err)
	}

	var maxVersion int32
	if err := api.RDB.DB(ctx).Model(&AssetTypeVersion{}).
		Where("asset_type_id = ?", assetType.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return nil, err
	}

	version := &AssetTypeVersion{
		AssetTypeId:    assetType.ID,
		Version:        maxVersion + 1,
		Label:          rdb.NullStrOf(label),
		Description:    rdb.NullStrOf(description),
		PropertySchema: *assetType.PropertySchema,
		PublishedBy:    publishedBy,
	}
	// Insert the version and advance the active pointer atomically: a pointer update
	// that failed after the insert would leave an orphan version and assets validating
	// against the stale one, so wrap both.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		res := tx.Model(&AssetType{}).Where("id = ?", assetType.ID).
			Update("active_version", version.Version)
		if res.Error != nil {
			return res.Error
		}
		// The type was deleted between the load and here (its cascade already removed
		// the version rows): roll the whole publish back rather than commit a version
		// row no asset can ever resolve.
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: asset type %q", gorm.ErrRecordNotFound, token)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return version, nil
}

// RollbackAssetType re-points the type's active published version at an existing
// version, so its assets are validated against that earlier contract again. It is a
// non-destructive pointer flip: history is append-only and the mutable draft is
// untouched, so a bad publish is reverted instantly and can be rolled forward again.
// Returns gorm.ErrRecordNotFound if the type or the target version does not exist.
//
// 🔴 It does NOT re-validate assets already stored, and — since the gate in
// UpdateAsset runs only when the document or the type actually moves — those assets
// stay EDITABLE. Conformance is enforced when a property document is WRITTEN, against
// the version active at that moment; rolling back to a narrower contract can
// therefore leave stored documents that no longer satisfy it, nothing sweeps for
// them, and renaming one is not refused on their account.
//
// The last clause is the one that had to be earned rather than asserted. This comment
// previously said stored assets were not rechecked while UpdateAsset rechecked them
// on EVERY write, so a rename after a rollback failed with `unknown property "x"` —
// the comment was false on the very next write, and TestRenameAfterRollbackIsNotRefused
// is what now holds it true. That mattered beyond tidiness: once assets diverge, no
// version satisfies all of them, so an every-write gate leaves some set of them
// permanently uneditable whichever version the operator rolls to.
func (api *Api) RollbackAssetType(ctx context.Context, token string, version int32) (*AssetType, error) {
	assetType, err := api.assetTypeByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	var count int64
	if err := api.RDB.DB(ctx).Model(&AssetTypeVersion{}).
		Where("asset_type_id = ? AND version = ?", assetType.ID, version).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: asset type %q has no version %d", gorm.ErrRecordNotFound, token, version)
	}

	res := api.RDB.DB(ctx).Model(&AssetType{}).Where("id = ?", assetType.ID).
		Update("active_version", version)
	if res.Error != nil {
		return nil, res.Error
	}
	// The type was deleted between the existence check and here.
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: asset type %q", gorm.ErrRecordNotFound, token)
	}
	// Reload so the returned type carries the freshly-bumped updated_at.
	return api.assetTypeByToken(ctx, token)
}

// AssetTypeVersions lists a type's published versions, newest first. Returns
// gorm.ErrRecordNotFound if the type does not exist.
func (api *Api) AssetTypeVersions(ctx context.Context, token string) ([]*AssetTypeVersion, error) {
	assetType, err := api.assetTypeByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	versions := make([]*AssetTypeVersion, 0)
	result := api.RDB.DB(ctx).Where("asset_type_id = ?", assetType.ID).
		Order("version DESC").Find(&versions)
	if result.Error != nil {
		return nil, result.Error
	}
	return versions, nil
}

// ActiveAssetTypeVersion loads the version an asset of this type is currently
// validated against, or nil when the type has never been published. The console
// reads it to render the property form, which is why it returns the version row
// rather than only its schema: an author filling a form needs to know WHICH
// contract they are filling.
//
// A pointer naming a version that does not exist is an error, not an empty result.
// The profile equivalent logs and resolves empty, because there the consequence of
// a dangling pointer is a device with no declared capabilities — inert. Here an
// empty resolve would mean "no schema", which is the branch that ACCEPTS anything,
// so the same leniency would turn a broken pointer into an open door.
func (api *Api) ActiveAssetTypeVersion(ctx context.Context, token string) (*AssetTypeVersion, error) {
	assetType, err := api.assetTypeByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !assetType.ActiveVersion.Valid {
		return nil, nil
	}
	return api.assetTypeVersionByNumber(ctx, assetType.ID, assetType.ActiveVersion.Int32)
}

// assetTypeVersionByNumber loads one frozen version of a type by number, wrapping a
// missing row with the identifying pair so a dangling ActiveVersion pointer reads as
// what it is rather than as a bare "record not found".
func (api *Api) assetTypeVersionByNumber(ctx context.Context, assetTypeId uint, version int32) (*AssetTypeVersion, error) {
	var v AssetTypeVersion
	err := api.RDB.DB(ctx).Where("asset_type_id = ? AND version = ?", assetTypeId, version).First(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: asset type %d has no version %d", gorm.ErrRecordNotFound, assetTypeId, version)
		}
		return nil, err
	}
	return &v, nil
}

// assetTypeVersionsFor loads the published versions of several asset types in one
// query, grouped by type id — the batched form the GraphQL resolver needs so a page
// of asset types does not issue a query per row.
func (api *Api) assetTypeVersionsFor(ctx context.Context, ids []uint) (map[uint][]*AssetTypeVersion, error) {
	grouped := make(map[uint][]*AssetTypeVersion, len(ids))
	if len(ids) == 0 {
		return grouped, nil
	}
	versions := make([]*AssetTypeVersion, 0)
	if err := api.RDB.DB(ctx).Where("asset_type_id in ?", ids).
		Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	for _, v := range versions {
		grouped[v.AssetTypeId] = append(grouped[v.AssetTypeId], v)
	}
	return grouped, nil
}

// deleteAssetTypeVersions removes a type's version history inside the caller's
// transaction. There is no database foreign key doing this — the four versioning
// implementations that came before all cascade by hand for the same reason — so a
// type delete that skipped it would strand rows no query can reach.
func deleteAssetTypeVersions(tx *gorm.DB, assetTypeId uint) error {
	return tx.Unscoped().Where("asset_type_id = ?", assetTypeId).Delete(&AssetTypeVersion{}).Error
}
