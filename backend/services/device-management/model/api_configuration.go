// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the per-device configuration revisions and reported state. A device's
// configuration document is built from the declaration frozen into its profile's ACTIVE
// published version and the device's SHARED attributes (BuildConfigurationDocument).
// Revisions are minted eagerly, in the same transaction as a SHARED write or delete of a
// declared key, and only when the document's digest differs from the latest revision.
// A profile publish does not re-snapshot the fleet: the read API reports `Stale` instead.

// DeviceConfigurationRevisionSearchResults is a page of a device's revisions.
type DeviceConfigurationRevisionSearchResults struct {
	Results    []DeviceConfigurationRevision
	Pagination rdb.SearchResultsPagination
}

// configurationActor is the authenticated subject recorded on a minted revision: the
// caller's username, falling back to email. Never caller-supplied.
func configurationActor(ctx context.Context) string {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return ""
	}
	if claims.Username != "" {
		return claims.Username
	}
	return claims.Email
}

// activeConfigurationDeclaration resolves the declaration a device currently sees: the
// one frozen into its type's profile's ACTIVE published version. A device whose type has
// no profile, or whose profile was never published, declares nothing (nil, nil).
func activeConfigurationDeclaration(db *gorm.DB, deviceId uint) ([]ConfigurationKey, *uint, error) {
	var dev Device
	if err := db.Select("id", "device_type_id").Where("id = ?", deviceId).First(&dev).Error; err != nil {
		return nil, nil, err
	}
	var types []DeviceType
	if err := db.Select("id", "profile_id").Where("id = ?", dev.DeviceTypeId).Find(&types).Error; err != nil {
		return nil, nil, err
	}
	if len(types) == 0 || types[0].ProfileId == nil {
		return nil, nil, nil
	}
	var profiles []DeviceProfile
	if err := db.Select("id", "active_version").Where("id = ?", *types[0].ProfileId).Find(&profiles).Error; err != nil {
		return nil, nil, err
	}
	if len(profiles) == 0 || !profiles[0].ActiveVersion.Valid {
		return nil, nil, nil
	}
	var versions []DeviceProfileVersion
	if err := db.Where("device_profile_id = ? AND version = ?", profiles[0].ID,
		profiles[0].ActiveVersion.Int32).Find(&versions).Error; err != nil {
		return nil, nil, err
	}
	if len(versions) == 0 {
		// The active pointer names a missing version: an invariant breach. Fail loudly
		// rather than build an empty document a device would apply as "clear everything".
		return nil, nil, fmt.Errorf("device profile %d: active version %d is missing",
			profiles[0].ID, profiles[0].ActiveVersion.Int32)
	}
	snap, err := parseProfileSnapshot(versions[0].Snapshot)
	if err != nil {
		return nil, nil, err
	}
	id := versions[0].ID
	return snap.Configuration, &id, nil
}

// computeDeviceConfiguration builds the document a device's current declaration and
// SHARED attributes produce. Only SHARED rows are loaded.
func computeDeviceConfiguration(db *gorm.DB, deviceId uint) (*ConfigurationDocument, *uint, error) {
	declared, versionId, err := activeConfigurationDeclaration(db, deviceId)
	if err != nil {
		return nil, nil, err
	}
	attrs := make([]*EntityAttribute, 0)
	if err := db.Where("entity_type = ? AND entity_id = ? AND scope = ?",
		string(entity.TypeDevice), deviceId, string(AttributeScopeShared)).Find(&attrs).Error; err != nil {
		return nil, nil, err
	}
	doc, err := BuildConfigurationDocument(declared, attrs)
	if err != nil {
		return nil, nil, err
	}
	return doc, versionId, nil
}

// latestConfigurationRevision returns the device's newest revision, or nil.
func latestConfigurationRevision(db *gorm.DB, deviceId uint) (*DeviceConfigurationRevision, error) {
	var rows []DeviceConfigurationRevision
	if err := db.Where("device_id = ?", deviceId).Order("revision DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// lockDeviceConfiguration serializes revision minting for one device within its tenant.
// Postgres-only (pg_advisory_xact_lock releases at commit); under sqlite the single
// writer already serializes. Taken AFTER the attribute write and BEFORE the document is
// read: under READ COMMITTED each later statement takes a fresh snapshot, so a writer that
// waited here sees the previous holder's committed attribute and revision rows, and mints
// the next number over the combined document. The unique (device_id, revision) index is
// the backstop if this is ever bypassed.
func lockDeviceConfiguration(ctx context.Context, tx *gorm.DB, deviceId uint) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00device-configuration\x00%d", tenant, deviceId)
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(h.Sum64())).Error
}

// configurationScopeEligible reports whether an attribute write can change a device's
// configuration document: only a SHARED attribute of a device can.
func configurationScopeEligible(entityType, scope string) bool {
	return entity.Type(entityType) == entity.TypeDevice && AttributeScope(scope) == AttributeScopeShared
}

// reconcileDeviceConfigurationOnTx runs inside the transaction of a SHARED attribute write
// or delete. When the key is declared by the device's active profile version it checks
// the resulting document — refusing a value the declared type cannot carry, and a
// document over MaxConfigurationDocumentBytes — and mints a new revision iff the digest
// differs from the latest one. A key that is not declared cannot change the document, so
// it costs one declaration lookup and nothing else.
//
// written is true for a set (the key must then appear in the document) and false for a
// delete.
func (api *Api) reconcileDeviceConfigurationOnTx(ctx context.Context, tx *gorm.DB, entityType string,
	deviceId uint, scope, attrKey string, written bool) error {
	if !configurationScopeEligible(entityType, scope) {
		return nil
	}
	declared, _, err := activeConfigurationDeclaration(tx, deviceId)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(declared, func(k ConfigurationKey) bool { return k.Key == attrKey }) {
		return nil
	}
	if err := lockDeviceConfiguration(ctx, tx, deviceId); err != nil {
		return err
	}
	doc, versionId, err := computeDeviceConfiguration(tx, deviceId)
	if err != nil {
		return err
	}
	if written && slices.Contains(doc.Invalid, attrKey) {
		return &ConfigurationRefusal{Code: CodeConfigurationValueType, Message: fmt.Sprintf(
			"attribute %q is declared as device configuration with a different value type, "+
				"or its value cannot be carried as that type", attrKey)}
	}
	if len(doc.Canonical) > MaxConfigurationDocumentBytes {
		if !written {
			// A delete only shrinks the document. One still over the cap can be over it
			// only because a later profile version declared more keys; refusing the delete
			// would make it impossible to bring the device back under. Mint nothing: no
			// revision over the cap ever exists, and the read API shows the device stale.
			return nil
		}
		return &ConfigurationRefusal{Code: CodeConfigurationTooLarge, Message: fmt.Sprintf(
			"the device's configuration document would be %d bytes, over the %d byte limit",
			len(doc.Canonical), MaxConfigurationDocumentBytes)}
	}
	_, err = mintConfigurationRevision(ctx, tx, deviceId, doc, versionId)
	return err
}

// mintConfigurationRevision appends a revision for doc unless the latest revision already
// carries its digest. It returns the revision now current. The caller holds the device's
// configuration lock.
func mintConfigurationRevision(ctx context.Context, tx *gorm.DB, deviceId uint,
	doc *ConfigurationDocument, versionId *uint) (*DeviceConfigurationRevision, error) {
	latest, err := latestConfigurationRevision(tx, deviceId)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Digest == doc.Digest {
		return latest, nil
	}
	next := int64(1)
	if latest != nil {
		next = latest.Revision + 1
	}
	rev := &DeviceConfigurationRevision{
		DeviceId:         deviceId,
		Revision:         next,
		ProfileVersionId: versionId,
		Document:         append([]byte(nil), doc.Canonical...),
		Digest:           doc.Digest,
		Actor:            configurationActor(ctx),
	}
	if err := tx.Create(rev).Error; err != nil {
		return nil, err
	}
	return rev, nil
}

// deviceIdByToken resolves a device token in the caller's tenant.
func (api *Api) deviceIdByToken(ctx context.Context, token string) (uint, error) {
	var rows []Device
	if err := api.RDB.DB(ctx).Select("id").Where("token = ?", token).Limit(1).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return rows[0].ID, nil
}

// DeviceConfigurationByToken reads a device's configuration: the latest revision, the
// last report, and the derived pending / stale flags. Nothing is minted by a read.
func (api *Api) DeviceConfigurationByToken(ctx context.Context, deviceToken string) (*DeviceConfiguration, error) {
	deviceId, err := api.deviceIdByToken(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	db := api.RDB.DB(ctx)
	latest, err := latestConfigurationRevision(db, deviceId)
	if err != nil {
		return nil, err
	}
	var states []DeviceConfigurationState
	if err := db.Where("device_id = ?", deviceId).Limit(1).Find(&states).Error; err != nil {
		return nil, err
	}
	var reported *DeviceConfigurationState
	if len(states) == 1 {
		reported = &states[0]
	}
	doc, _, err := computeDeviceConfiguration(db, deviceId)
	if err != nil {
		return nil, err
	}
	return &DeviceConfiguration{
		Desired:    latest,
		Reported:   reported,
		Pending:    IsPending(latest, reported),
		Stale:      isStale(latest, doc),
		Undeclared: doc.Undeclared,
		Invalid:    doc.Invalid,
	}, nil
}

// isStale: the computed document differs from the latest minted one. With nothing minted,
// an empty document is not stale (there is nothing to deliver) and any other one is.
func isStale(latest *DeviceConfigurationRevision, doc *ConfigurationDocument) bool {
	if latest == nil {
		return string(doc.Canonical) != "{}"
	}
	return latest.Digest != doc.Digest
}

// DeviceConfigurationRevisions pages a device's revisions, newest first. An unknown device
// token is gorm.ErrRecordNotFound.
func (api *Api) DeviceConfigurationRevisions(ctx context.Context, deviceToken string,
	pagination rdb.Pagination) (*DeviceConfigurationRevisionSearchResults, error) {
	deviceId, err := api.deviceIdByToken(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	results := make([]DeviceConfigurationRevision, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceConfigurationRevision{}, func(result *gorm.DB) *gorm.DB {
		return result.Where("device_id = ?", deviceId)
	}, pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DeviceConfigurationRevisionSearchResults{Results: results, Pagination: pag}, nil
}

// deleteDeviceConfigurationOnTx removes a device's revisions and reported state with it.
func deleteDeviceConfigurationOnTx(tx *gorm.DB, deviceId uint) error {
	if err := tx.Where("device_id = ?", deviceId).Delete(&DeviceConfigurationRevision{}).Error; err != nil {
		return err
	}
	return tx.Where("device_id = ?", deviceId).Delete(&DeviceConfigurationState{}).Error
}
