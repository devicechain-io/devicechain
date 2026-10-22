// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A GATEWAY is an ordinary device that fronts other devices: the edge agent at a site holds one
// credential and publishes for every local device under it. Which devices a gateway may speak
// for is decided by ONE column, devices.gateway_id on the child, written only here.
//
// Why a dedicated column rather than a relationship edge: the relationship graph is generic
// (tenant-defined types, many-to-many), so making it a security authority would make every
// relationship write a security-relevant write, and the check on the ingest path would need a
// graph read. This is single-valued (one atomic write moves a child), indexed (a gateway's
// children are one query), and read off the device row the resolver already holds, with no extra
// I/O. Only the two mutations below can grant the authority.

var (
	// ErrGatewayIsSelf refuses a device as its own gateway.
	ErrGatewayIsSelf = integrity.NewRefusal(integrity.ClassInvalid,
		"a device cannot be its own gateway")
	// ErrGatewayHasGateway refuses a gateway that is itself behind a gateway: gateways are one level deep.
	ErrGatewayHasGateway = integrity.NewRefusal(integrity.ClassInvalid,
		"that device is itself behind a gateway, and a gateway cannot have a gateway: clear its own gateway first")
	// ErrDeviceHasChildren refuses to put a device behind a gateway while it is a gateway: gateways are one level deep.
	ErrDeviceHasChildren = integrity.NewRefusal(integrity.ClassInvalid,
		"that device is the gateway of other devices, and a gateway cannot have a gateway: move or clear its children first")
	// ErrGatewayHasChildren refuses to delete a gateway that still has children, so no device is
	// left naming a gateway that is gone.
	ErrGatewayHasChildren = integrity.NewRefusal(integrity.ClassReference,
		"the device is the gateway of other devices and cannot be deleted: assign them to another gateway or clear their gateway first")
)

// gatewayHasChildren wraps ErrGatewayHasChildren with how many children the gateway has.
func gatewayHasChildren(n int64) error {
	return fmt.Errorf("%w (%d device(s))", ErrGatewayHasChildren, n)
}

// readLockedDevices reads the devices named by tokens on tx, locking the rows in id order so two
// concurrent gateway writes (an assignment and a delete, or two assignments touching the same
// pair) cannot interleave. The lock is a no-op on the SQLite test harness (see ReplaceDevice).
// Tokens that name nothing in the tenant are simply absent from the result.
func readLockedDevices(tx *gorm.DB, tokens ...string) (map[string]*Device, error) {
	var found []*Device
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token IN ?", tokens).Order("id ASC").Find(&found).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*Device, len(found))
	for _, d := range found {
		out[d.Token] = d
	}
	return out, nil
}

// checkGatewayPair applies the assignment rules to (child, gateway), both read on tx. A device
// of another tenant is not found (every read is tenant-scoped), so "same tenant" is the
// store's, not a check here.
func checkGatewayPair(tx *gorm.DB, child, gateway *Device) error {
	if child.ID == gateway.ID {
		return ErrGatewayIsSelf
	}
	// Depth is one: the gateway must not be behind a gateway...
	if gateway.GatewayId != nil {
		return ErrGatewayHasGateway
	}
	// ...and the child must not be a gateway.
	var children int64
	if err := tx.Model(&Device{}).Where("gateway_id = ?", child.ID).Count(&children).Error; err != nil {
		return err
	}
	if children > 0 {
		return ErrDeviceHasChildren
	}
	return nil
}

// gatewayForNewDevice resolves and checks the gateway a device is being created behind, or
// returns nil when it is created behind none. The new device has no id yet and no children, so
// the only rules that can apply to it are the gateway's own: it must exist in the tenant and
// not itself be behind a gateway.
func (api *Api) gatewayForNewDevice(ctx context.Context, token string, gatewayToken *string) (*uint, error) {
	if gatewayToken == nil || *gatewayToken == "" {
		return nil, nil
	}
	if *gatewayToken == token {
		return nil, ErrGatewayIsSelf
	}
	matches, err := api.DevicesByToken(ctx, []string{*gatewayToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if matches[0].GatewayId != nil {
		return nil, ErrGatewayHasGateway
	}
	id := matches[0].ID
	return &id, nil
}

// AssignDeviceGateway puts the device behind a gateway: from now on that gateway's broker login
// may publish telemetry for the device on the gateway's own namespace, and no other login may
// speak for it. Assigning a device to the gateway it already has changes nothing.
//
// The authority this grants is the same as issuing the device a credential (a gateway can then
// make the device report anything), which is why the caller is held to the credential-managing
// permission by the resolver. Both devices must be in the caller's tenant.
//
// It takes effect on the NEXT event, not at the next reconnect: the device's cached entry is
// evicted on every replica, and the gateway's live session is not touched (its grant never
// named a child). The device's other writes (credentials, sessions) are left alone.
func (api *Api) AssignDeviceGateway(ctx context.Context, deviceToken, gatewayToken string) (*Device, error) {
	var child *Device
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if deviceToken == gatewayToken {
			return ErrGatewayIsSelf
		}
		devices, err := readLockedDevices(tx, deviceToken, gatewayToken)
		if err != nil {
			return err
		}
		c, g := devices[deviceToken], devices[gatewayToken]
		if c == nil || g == nil {
			return gorm.ErrRecordNotFound
		}
		if err := checkGatewayPair(tx, c, g); err != nil {
			return err
		}
		if c.GatewayId == nil || *c.GatewayId != g.ID {
			// Through the loaded row, so the audit journal names the device it changed.
			if err := tx.Model(c).Update("gateway_id", g.ID).Error; err != nil {
				return err
			}
			id := g.ID
			c.GatewayId = &id
		}
		child = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	api.evictGatewayAssignment(ctx, child)
	return api.deviceWithType(ctx, child)
}

// ClearDeviceGateway takes the device out from behind its gateway: the gateway's next publish
// for it is refused (NotGatewayChild). Clearing a device that has no gateway changes nothing.
func (api *Api) ClearDeviceGateway(ctx context.Context, deviceToken string) (*Device, error) {
	var child *Device
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		devices, err := readLockedDevices(tx, deviceToken)
		if err != nil {
			return err
		}
		c := devices[deviceToken]
		if c == nil {
			return gorm.ErrRecordNotFound
		}
		if c.GatewayId != nil {
			if err := tx.Model(c).Update("gateway_id", nil).Error; err != nil {
				return err
			}
			c.GatewayId = nil
		}
		child = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	api.evictGatewayAssignment(ctx, child)
	return api.deviceWithType(ctx, child)
}

// evictGatewayAssignment drops the child's cached entries on every replica, so the resolver
// reads the new assignment on the next event. The device-by-token entry is the one the
// gateway check reads; the credential entry holds the same row for a credentialed event.
func (api *Api) evictGatewayAssignment(ctx context.Context, child *Device) {
	api.evictDeviceCredentials(ctx, child.TenantId, child.ID)
	api.evictDeviceByToken(ctx, child.Token)
}

// deviceWithType returns the device with its type loaded, as every other device write does.
func (api *Api) deviceWithType(ctx context.Context, d *Device) (*Device, error) {
	matches, err := api.DevicesByToken(ctx, []string{d.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// DeviceGateway returns the gateway a device is behind, or nil when it is behind none.
func (api *Api) DeviceGateway(ctx context.Context, device *Device) (*Device, error) {
	if device == nil || device.GatewayId == nil {
		return nil, nil
	}
	var gateway Device
	err := api.RDB.DB(ctx).Preload("DeviceType").Where("id = ?", *device.GatewayId).First(&gateway).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gateway, nil
}

// DeviceChildren pages the devices behind a gateway. A token that names no device in the tenant
// is not found; a device that is nobody's gateway has an empty page.
func (api *Api) DeviceChildren(ctx context.Context, gatewayToken string, pagination rdb.Pagination) (*DeviceSearchResults, error) {
	gateways, err := api.DevicesByToken(ctx, []string{gatewayToken})
	if err != nil {
		return nil, err
	}
	if len(gateways) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return api.deviceChildrenOf(ctx, gateways[0].ID, pagination)
}

func (api *Api) deviceChildrenOf(ctx context.Context, gatewayId uint, pagination rdb.Pagination) (*DeviceSearchResults, error) {
	results := make([]Device, 0)
	db, pag := api.RDB.ListOf(ctx, &Device{}, func(result *gorm.DB) *gorm.DB {
		return result.Where("devices.gateway_id = ?", gatewayId).Preload("DeviceType")
	}, pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DeviceSearchResults{Results: results, Pagination: pag}, nil
}

// refuseDeleteOfGateway is DeleteDevice's gateway rule, run on the delete's own transaction: a
// gateway that still has children is not deleted. The row is locked first so an assignment that
// is committing at this moment is either seen by the count or blocked until the delete is done
// (and then finds no gateway).
func refuseDeleteOfGateway(tx *gorm.DB, id uint) error {
	var locked Device
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).First(&locked).Error; err != nil {
		return err
	}
	var children int64
	if err := tx.Unscoped().Model(&Device{}).Where("gateway_id = ?", id).Count(&children).Error; err != nil {
		return err
	}
	if children > 0 {
		return gatewayHasChildren(children)
	}
	return nil
}
