// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// DeviceState is the live current-state projection for one device (ADR-012):
// connectivity + activity timestamps, distinct from the
// append-only event history. One row per device.
type DeviceState struct {
	gorm.Model
	rdb.TenantScoped
	DeviceToken         string
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

// LatestMeasurement is the current (most-recent) value of one named measurement
// for one device — the O(1) "what is it right now?" projection beside the
// append-only measurement history in event-management. One row per
// (tenant, device, name). Numeric measurements only for v1;
// a non-numeric reading is skipped upstream. Location gets its own sibling
// projection later. Bounded by (devices × metrics-per-device), so it never grows
// with history.
type LatestMeasurement struct {
	gorm.Model
	rdb.TenantScoped
	DeviceToken string
	Name        string
	Value       sql.NullFloat64
	Classifier  *uint
	// Unit + DataType are denormalized from the bound metric definition (ADR-016),
	// mirroring the measurement_events history projection, so the last-known value is
	// self-describing without a cross-service hop into device-management. Both are
	// null for an undeclared (unbound) measurement. Unit is unbounded text to match
	// the source column; DataType is a closed enum.
	Unit         *string `gorm:"type:text"`
	DataType     *string `gorm:"type:varchar(32)"`
	OccurredTime time.Time
}

// AuditExempt: a high-volume derived telemetry projection, not a control-plane
// mutation (ADR-019) — same rationale as DeviceState.
func (LatestMeasurement) AuditExempt() bool { return true }

// LatestMeasurementInput is one (name, value, occurredAt) reading to upsert into
// the latest-value projection.
type LatestMeasurementInput struct {
	Name         string
	Value        sql.NullFloat64
	Classifier   *uint
	Unit         *string
	DataType     *string
	OccurredTime time.Time
}

// Search criteria for locating device states. Note: DeviceToken is filterable via
// the API but is not exposed in the GraphQL criteria; use deviceStatesByDeviceToken
// for token lookups.
type DeviceStateSearchCriteria struct {
	rdb.Pagination
	Active *bool
}

// Results for device state search.
type DeviceStateSearchResults struct {
	Results    []DeviceState
	Pagination rdb.SearchResultsPagination
}
