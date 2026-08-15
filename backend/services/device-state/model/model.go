// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Presence-source discriminator values (ADR-067 decision 3). A DeviceState is
// INFERRED until a presence-asserting transport (Sparkplug, real-MQTT LWT) first
// produces for it, after which it is ASSERTED. Any value that is not exactly
// ASSERTED is treated as inferred (fail-safe toward today's behavior).
const (
	PresenceSourceInferred = "INFERRED"
	PresenceSourceAsserted = "ASSERTED"
)

// MaxAssertedPageSize is the largest page AssertedDeviceStates will serve.
//
// It is a REFUSAL threshold, not a clamp: a caller asking for more is answered with an
// error rather than a quietly smaller page. Callers walk this query until a page comes
// back shorter than the one they asked for, so a silently reduced page would be read as
// the end of the set — and the walk would stop with most of the fleet unread, which for
// a presence reconciler means treating connected devices as absent.
//
// 1000 sits an order of magnitude below where a page could threaten the caller's read
// limit even with the longest legal device tokens, which leaves the bound about memory
// and round trips rather than about being just barely safe.
const MaxAssertedPageSize = 1000

// DeviceState is the live current-state projection for one device (ADR-012):
// connectivity + activity timestamps, distinct from the
// append-only event history. One row per device.
type DeviceState struct {
	gorm.Model
	rdb.TenantScoped
	DeviceToken string
	// ExternalId denormalizes the device's external id (ADR-049) from the resolved
	// event so the projection is queryable by transport-native identity (ADR-067 SP4b
	// failover reconciliation enumerates asserted-active devices by their Sparkplug
	// "{group}/{node}[/{device}]" external id). Empty when the device has no external id.
	ExternalId string `gorm:"type:text"`
	// Source denormalizes the event Source that last drove this device (e.g.
	// "sparkplug:{hostId}"). SP4b failover reconciliation scopes its asserted-active
	// enumeration to its OWN source, so two Sparkplug sources on one tenant (distinct
	// brokers) can never cross-disconnect each other's devices (ADR-067 SP4b). Empty for
	// a device that has never produced an event carrying a source.
	Source              string `gorm:"type:text"`
	Active              bool
	LastConnectTime     sql.NullTime
	LastDisconnectTime  sql.NullTime
	LastActivityTime    sql.NullTime
	InactivityAlarmTime sql.NullTime
	InactivityTimeout   int // seconds; per-device override of the default
	// PresenceSource discriminates authoritative from inferred presence (ADR-067
	// decision 3): INFERRED (default) derives Active from activity + the inactivity
	// sweep, unchanged; ASSERTED takes Active ONLY from a StateChange — a data event
	// advances LastActivityTime but never flips Active, and the sweep skips it (its
	// offline is a DEATH/LWT, not a data-silence timeout).
	PresenceSource string `gorm:"type:varchar(16);not null;default:INFERRED"`
	// SessionId + PresenceTime are the last-applied presence transition's ordering key
	// (ADR-067 decision 4): a StateChange is applied only when it is not older than
	// this by (SessionId, PresenceTime), DISCONNECTED winning an equal stamp. Zero /
	// NULL until the first StateChange; unused for INFERRED devices.
	SessionId    uint64 `gorm:"not null;default:0"`
	PresenceTime sql.NullTime
}

// DefaultOrder implements rdb.Sortable: newest projection row first, tiebroken on the
// row id.
//
// The tiebreak is id rather than a token because this table has no registry token —
// device_token names the DEVICE, and while the projection holds one row per device
// today, that is an invariant of the merge path rather than a declared unique
// constraint, so it is not the column to hang a total order on. id is both unique and
// monotonic, which makes it a truthful tiebreak for "newest" as well as a correct one.
//
// 🔴 This is the order for the PAGED search only (DeviceStates). AssertedDeviceStates
// walks a keyset cursor with its own `id ASC` and does not go through ListOf — its
// order IS its cursor, and it must stay ascending on id.
func (DeviceState) DefaultOrder() string {
	return "device_states.created_at DESC, device_states.id DESC"
}

// AuditExempt opts the device-state projection out of the audit journal
// (ADR-019): it is high-volume derived connectivity/activity state recomputed
// from the event stream, not a control-plane entity mutation.
func (DeviceState) AuditExempt() bool { return true }

// LatestMeasurement is the current (most-recent) value of one named measurement
// for one device — the O(1) "what is it right now?" projection beside the
// append-only measurement history in event-management. One row per
// (tenant, device, name). Numeric measurements only for v1;
// a non-numeric reading is skipped upstream. Position has its own sibling
// projection, LatestLocation. Bounded by (devices × metrics-per-device), so it
// never grows with history.
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

// LatestLocation is the current (most-recent) position of one device — the O(1)
// "where is it right now?" projection beside the append-only location history in
// event-management. One row per (tenant, device): a device has exactly one current
// position, so unlike LatestMeasurement there is no per-name dimension. Bounded by
// the device count, so it never grows with history.
//
// The coordinate contract is fixed platform-wide and is NOT restated per row: WGS84 /
// EPSG:4326 decimal degrees, elevation and accuracy in metres (elevation above the
// ELLIPSOID, not mean sea level), speed in metres per second, heading in degrees
// clockwise from true north in [0, 360). Values arrive already range-checked at decode.
//
// 🔴 The column widths are the ones event-management's LocationEvent uses, deliberately
// and not by coincidence: this projection and that history are two views of the same
// fix, so a fix that round-trips into the history must round-trip into here at the same
// precision. Widening or narrowing one without the other makes the "current" position
// disagree with the most recent historical one.
//
// Every coordinate is nullable. A fix is not required to carry elevation, accuracy,
// speed or heading — most do not — and NULL says "not reported", which zero does not.
type LatestLocation struct {
	gorm.Model
	rdb.TenantScoped
	DeviceToken string
	Latitude    sql.NullFloat64 `gorm:"type:decimal(10,8)"`
	Longitude   sql.NullFloat64 `gorm:"type:decimal(11,8)"`
	Elevation   sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Accuracy    sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Speed       sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Heading     sql.NullFloat64 `gorm:"type:decimal(7,4)"`
	// OccurredTime is the fix's own event time, and it is the projection's ORDERING
	// KEY, not decoration: the merge only advances a row when the incoming fix is
	// strictly newer, so a redelivered or out-of-order fix can never move a device
	// backwards to a position it has already left.
	OccurredTime time.Time
}

// AuditExempt: a high-volume derived position projection, not a control-plane
// mutation (ADR-019) — same rationale as DeviceState.
func (LatestLocation) AuditExempt() bool { return true }

// LatestLocationInput is one positional fix to upsert into the last-known-position
// projection. Every coordinate is a nullable float because a fix reports only what
// its sensor produced.
type LatestLocationInput struct {
	Latitude     sql.NullFloat64
	Longitude    sql.NullFloat64
	Elevation    sql.NullFloat64
	Accuracy     sql.NullFloat64
	Speed        sql.NullFloat64
	Heading      sql.NullFloat64
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
