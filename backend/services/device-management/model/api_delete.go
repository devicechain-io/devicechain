// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
)

// This file holds the registry delete API. Deletes are uniform across the device
// management entity families, so the shared mechanics live here rather than being
// copy-pasted across the per-area api_*.go files.
//
// Semantics (greenfield, pre-GA — see CLAUDE.md "decisive cutovers"):
//
//   - All deletes are HARD deletes (Unscoped). Registry entities are addressed by
//     a unique token; a soft delete would keep that token occupying the unique
//     index, so a deleted token could never be reused (the same footgun the iam
//     identity delete hit). Hard delete frees the token immediately.
//   - Tenant scoping still applies: the global gorm delete callback injects the
//     tenant predicate even under Unscoped (Unscoped only disables the soft-delete
//     scope), so a delete can never cross a tenant boundary.
//   - Referential integrity is fail-closed. An entity that is a relationship edge
//     endpoint (ADR-013) has its dangling edges cascade-removed in the same
//     transaction — this is the "unassign on delete" behavior, since an assignment
//     is itself an edge. An entity still referenced by a hard foreign key (a type
//     in use by its instances, a relationship type in use by edges) is refused
//     with ErrEntityInUse rather than orphaning the reference or surfacing a raw
//     database constraint error.
//
// Every Delete* returns whether a row was removed (false + nil error when the
// token names nothing, mirroring DeleteEntityAttribute).

// ErrEntityInUse is returned when a delete is refused because other rows still
// reference the entity. Callers (the GraphQL layer) surface it as a user error.
var ErrEntityInUse = errors.New("entity is still referenced and cannot be deleted")

// hardDeleteByToken hard-deletes the single tenant-scoped row of model identified
// by token, returning whether a row was removed. model is a zero-value pointer of
// the target type (e.g. &DeviceGroup{}) used only to resolve the table.
func (api *Api) hardDeleteByToken(ctx context.Context, model interface{}, token string) (bool, error) {
	result := api.RDB.DB(ctx).Unscoped().Where("token = ?", token).Delete(model)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// countReferencing counts rows of refModel whose column equals id. The count is
// Unscoped so it includes soft-deleted children — matching what a database
// foreign key constraint sees — but remains tenant-scoped via the query callback.
func (api *Api) countReferencing(ctx context.Context, refModel interface{}, column string, id uint) (int64, error) {
	var n int64
	err := api.RDB.DB(ctx).Unscoped().Model(refModel).Where(column+" = ?", id).Count(&n).Error
	return n, err
}

// deleteEdgeEntity hard-deletes an edge-participating entity (one of the ADR-013
// entity-type registry types) by token, first removing every relationship edge in
// which it is the source or target so no dangling edge is left behind, and every
// EntityAttribute owned by it (ADR-012/044) so no attribute outlives its entity.
// An optional cascade removes further owned child rows (e.g. a device's
// credentials) in the same transaction. Returns false (no error) when the token
// names no entity.
func (api *Api) deleteEdgeEntity(ctx context.Context, etype entity.Type, model interface{},
	token string, cascade func(tx *gorm.DB, id uint) error) (bool, error) {
	id, err := api.ResolveEntityToken(ctx, string(etype), token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where(
			"(source_type = ? AND source_id = ?) OR (target_type = ? AND target_id = ?)",
			string(etype), id, string(etype), id).Delete(&EntityRelationship{}).Error; err != nil {
			return err
		}
		// Cascade the entity's attributes: they address their owner by
		// (entity_type, entity_id) with no DB foreign key, so nothing else removes
		// them (ADR-044 same-service RI gap).
		if err := tx.Unscoped().Where(
			"entity_type = ? AND entity_id = ?", string(etype), id).Delete(&EntityAttribute{}).Error; err != nil {
			return err
		}
		// Cascade the entity's alarms (ADR-041): like attributes they address their
		// originator by uniform (originator_type, originator_id) with no DB foreign
		// key, so an alarm must not outlive the entity it was raised on.
		if err := tx.Unscoped().Where(
			"originator_type = ? AND originator_id = ?", string(etype), id).Delete(&Alarm{}).Error; err != nil {
			return err
		}
		if cascade != nil {
			if err := cascade(tx, id); err != nil {
				return err
			}
		}
		return tx.Unscoped().Where("token = ?", token).Delete(model).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// deleteParentType deletes a "type" parent by token, refusing with ErrEntityInUse
// while any child row references it. byToken/idOf resolve the parent row id from
// the token; childModel + childFk identify the referencing instances.
func deleteParentType[T any](ctx context.Context, api *Api,
	byToken func(*Api, context.Context, []string) ([]*T, error),
	idOf func(*T) uint, parent interface{}, token string,
	childModel interface{}, childFk string) (bool, error) {
	matches, err := byToken(api, ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	n, err := api.countReferencing(ctx, childModel, childFk, idOf(matches[0]))
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, fmt.Errorf("%w: %d row(s) reference %q", ErrEntityInUse, n, token)
	}
	return api.hardDeleteByToken(ctx, parent, token)
}

// --- Devices (ADR-013) ---------------------------------------------------------

// DeleteDeviceType deletes a device type. Refused while any device references it;
// its owned metric definitions (ADR-016), command definitions (ADR-043), and alarm
// definitions (ADR-041) are cascade-removed.
func (api *Api) DeleteDeviceType(ctx context.Context, token string) (bool, error) {
	matches, err := api.DeviceTypesByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	dt := matches[0]
	n, err := api.countReferencing(ctx, &Device{}, "device_type_id", dt.ID)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, fmt.Errorf("%w: %d device(s) reference device type %q", ErrEntityInUse, n, token)
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("device_type_id = ?", dt.ID).Delete(&MetricDefinition{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("device_type_id = ?", dt.ID).Delete(&CommandDefinition{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("device_type_id = ?", dt.ID).Delete(&AlarmDefinition{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Where("token = ?", token).Delete(&DeviceType{}).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// DeleteDevice deletes a device, cascade-removing its credentials (ADR-014) and
// every relationship edge it participates in (its assignments, ADR-013).
func (api *Api) DeleteDevice(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeDevice, &Device{}, token,
		func(tx *gorm.DB, id uint) error {
			return tx.Unscoped().Where("device_id = ?", id).Delete(&DeviceCredential{}).Error
		})
}

// DeleteDeviceGroup deletes a device group and its relationship edges.
func (api *Api) DeleteDeviceGroup(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeDeviceGroup, &DeviceGroup{}, token, nil)
}

// DeleteDeviceCredential deletes a single device credential by token (ADR-014).
func (api *Api) DeleteDeviceCredential(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &DeviceCredential{}, token)
}

// --- Assets --------------------------------------------------------------------

// DeleteAssetType deletes an asset type. Refused while any asset references it.
func (api *Api) DeleteAssetType(ctx context.Context, token string) (bool, error) {
	return deleteParentType(ctx, api, (*Api).AssetTypesByToken,
		func(m *AssetType) uint { return m.ID }, &AssetType{}, token, &Asset{}, "asset_type_id")
}

// DeleteAsset deletes an asset and its relationship edges.
func (api *Api) DeleteAsset(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeAsset, &Asset{}, token, nil)
}

// DeleteAssetGroup deletes an asset group and its relationship edges.
func (api *Api) DeleteAssetGroup(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeAssetGroup, &AssetGroup{}, token, nil)
}

// --- Areas ---------------------------------------------------------------------

// DeleteAreaType deletes an area type. Refused while any area references it.
func (api *Api) DeleteAreaType(ctx context.Context, token string) (bool, error) {
	return deleteParentType(ctx, api, (*Api).AreaTypesByToken,
		func(m *AreaType) uint { return m.ID }, &AreaType{}, token, &Area{}, "area_type_id")
}

// DeleteArea deletes an area and its relationship edges.
func (api *Api) DeleteArea(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeArea, &Area{}, token, nil)
}

// DeleteAreaGroup deletes an area group and its relationship edges.
func (api *Api) DeleteAreaGroup(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeAreaGroup, &AreaGroup{}, token, nil)
}

// --- Customers -----------------------------------------------------------------

// DeleteCustomerType deletes a customer type. Refused while any customer references it.
func (api *Api) DeleteCustomerType(ctx context.Context, token string) (bool, error) {
	return deleteParentType(ctx, api, (*Api).CustomerTypesByToken,
		func(m *CustomerType) uint { return m.ID }, &CustomerType{}, token, &Customer{}, "customer_type_id")
}

// DeleteCustomer deletes a customer and its relationship edges.
func (api *Api) DeleteCustomer(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeCustomer, &Customer{}, token, nil)
}

// DeleteCustomerGroup deletes a customer group and its relationship edges.
func (api *Api) DeleteCustomerGroup(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeCustomerGroup, &CustomerGroup{}, token, nil)
}

// --- Relationships (ADR-013) ---------------------------------------------------

// DeleteEntityRelationshipType deletes a relationship type. Refused while any edge
// still uses it.
func (api *Api) DeleteEntityRelationshipType(ctx context.Context, token string) (bool, error) {
	return deleteParentType(ctx, api, (*Api).EntityRelationshipTypesByToken,
		func(m *EntityRelationshipType) uint { return m.ID }, &EntityRelationshipType{}, token,
		&EntityRelationship{}, "relationship_type_id")
}

// RemoveEntityRelationship removes a single relationship edge by token. This is
// the explicit "unassign" operation — an assignment is a relationship edge
// (ADR-013), so removing the edge unassigns the source from the target.
func (api *Api) RemoveEntityRelationship(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &EntityRelationship{}, token)
}

// --- Metrics & provisioning ----------------------------------------------------

// DeleteMetricDefinition deletes a single metric definition by token (ADR-016).
func (api *Api) DeleteMetricDefinition(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &MetricDefinition{}, token)
}

// DeleteCommandDefinition deletes a single command definition by token (ADR-043).
func (api *Api) DeleteCommandDefinition(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &CommandDefinition{}, token)
}

// DeleteAlarmDefinition deletes a single alarm definition by token (ADR-041).
func (api *Api) DeleteAlarmDefinition(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &AlarmDefinition{}, token)
}

// DeleteProvisioningProfile deletes a single provisioning profile by token (ADR-012).
func (api *Api) DeleteProvisioningProfile(ctx context.Context, token string) (bool, error) {
	return api.hardDeleteByToken(ctx, &ProvisioningProfile{}, token)
}
