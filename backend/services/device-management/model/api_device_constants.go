// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// The per-device constants door (ADR-082, S6b): the numbers a profile's devices carry in their
// metadata under the names a draft formula reads as `device.name`, for the rule preview's
// recalculation (event-processing holds no device records). It reads the SAME metadata through
// the SAME reader the resolver uses (readConstants), so what the preview computes with is what
// the resolver would read now: the device's CURRENT constants, not those a stored value was
// computed with.
//
// A keyset page over device ids, like the reconcile doors it sits beside, and bounded the same
// way: a page size or key list outside its range is refused, never clamped.

const (
	// MaxDeviceConstantsPageSize bounds one page of devices scanned. A row is a token and at most
	// MaxProfileConstants numbers.
	MaxDeviceConstantsPageSize = 500
)

// DeviceConstant is one constant a device's metadata holds under a requested name. Number is
// nil when the metadata holds the name but not a finite number (a string, a boolean, null, an
// object, an out-of-range number): present and unusable, which is not the same as absent.
type DeviceConstant struct {
	Key    string
	Number *float64
}

// DeviceConstantsEntry is the constants one device holds among those requested. Devices holding
// none are not listed.
type DeviceConstantsEntry struct {
	DeviceId    uint
	DeviceToken string
	Constants   []DeviceConstant
}

// DeviceConstantsPage is one keyset page. NextCursor is the last device id the page scanned when
// it scanned a full page, and nil when the walk is complete.
type DeviceConstantsPage struct {
	Entries    []DeviceConstantsEntry
	NextCursor *uint64
}

// DeviceConstantsPage returns one page of the constants the devices of a profile (the devices
// whose type adopts it) hold in their metadata under keys.
func (api *Api) DeviceConstantsPage(ctx context.Context, profileToken string, keys []string, afterId uint64,
	limit int) (*DeviceConstantsPage, error) {
	if err := checkReconcilePageSize("device constants", limit, MaxDeviceConstantsPageSize); err != nil {
		return nil, err
	}
	if len(keys) == 0 || len(keys) > calcfield.MaxProfileConstants {
		return nil, &InvalidArgumentError{Msg: fmt.Sprintf("keys must name between 1 and %d constants, got %d",
			calcfield.MaxProfileConstants, len(keys))}
	}
	for _, k := range keys {
		if k == "" || len(k) > calcfield.MaxConstantKeyBytes {
			return nil, &InvalidArgumentError{Msg: fmt.Sprintf("a constant name must be 1 to %d bytes",
				calcfield.MaxConstantKeyBytes)}
		}
	}
	profile, err := api.deviceProfileByToken(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	type row struct {
		Id       uint
		Token    string
		Metadata []byte
	}
	rows := make([]row, 0, limit)
	q := api.RDB.DB(ctx).Model(&Device{}).
		Select("devices.id AS id, devices.token AS token, devices.metadata AS metadata").
		// The tenant equality makes the joined table tenant-safe by the query itself, not only
		// by the provenance of device_type_id (hack/check-raw-joins.sh).
		Joins("JOIN device_types ON device_types.id = devices.device_type_id AND device_types.tenant_id = devices.tenant_id "+
			"AND device_types.deleted_at IS NULL").
		Where("device_types.profile_id = ?", profile.ID)
	if afterId > 0 {
		q = q.Where("devices.id > ?", afterId)
	}
	if err := q.Order("devices.id").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	page := &DeviceConstantsPage{Entries: make([]DeviceConstantsEntry, 0, len(rows))}
	for _, r := range rows {
		if len(r.Metadata) == 0 {
			continue
		}
		held := readConstants(r.Metadata, keys)
		if len(held) == 0 {
			continue
		}
		entry := DeviceConstantsEntry{DeviceId: r.Id, DeviceToken: r.Token, Constants: make([]DeviceConstant, 0, len(held))}
		for _, c := range held {
			dc := DeviceConstant{Key: c.Key()}
			if v, ok := c.Number(); ok {
				dc.Number = &v
			}
			entry.Constants = append(entry.Constants, dc)
		}
		page.Entries = append(page.Entries, entry)
	}
	if len(rows) > 0 {
		page.NextCursor = fullPageCursor(len(rows), limit, rows[len(rows)-1].Id)
	}
	return page, nil
}
