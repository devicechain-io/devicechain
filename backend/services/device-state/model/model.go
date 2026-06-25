// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// DeviceState is the live current-state projection for one device (ADR-012 /
// ThingsBoard §2.8): connectivity + activity timestamps, distinct from the
// append-only event history. One row per device.
type DeviceState struct {
	gorm.Model
	rdb.TenantScoped
	DeviceId            uint
	Active              bool
	LastConnectTime     sql.NullTime
	LastDisconnectTime  sql.NullTime
	LastActivityTime    sql.NullTime
	InactivityAlarmTime sql.NullTime
	InactivityTimeout   int // seconds; per-device override of the default
}

// AuditExempt opts the device-state projection out of the audit journal
// (ADR-019): it is high-volume derived connectivity/activity state recomputed
// from the event stream, not a control-plane entity mutation.
func (DeviceState) AuditExempt() bool { return true }

// Search criteria for locating device states. Note: DeviceId is filterable via
// the API but is not exposed in the GraphQL criteria (graph-gophers can not bind
// an optional Int onto a Go *uint); use deviceStatesByDeviceId for id lookups.
type DeviceStateSearchCriteria struct {
	rdb.Pagination
	Active *bool
}

// Results for device state search.
type DeviceStateSearchResults struct {
	Results    []DeviceState
	Pagination rdb.SearchResultsPagination
}
