// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Metric data-type vocabulary (ADR-016). The minimum typing that makes a
// measurement self-describing.
type MetricDataType string

const (
	MetricDouble  MetricDataType = "DOUBLE"
	MetricInt     MetricDataType = "INT"
	MetricBoolean MetricDataType = "BOOLEAN"
	MetricString  MetricDataType = "STRING"
)

// Valid reports whether the type names one of the known metric data types.
func (t MetricDataType) Valid() bool {
	switch t {
	case MetricDouble, MetricInt, MetricBoolean, MetricString:
		return true
	default:
		return false
	}
}

// StorableAsMetric reports whether a measurement of this type can be stored in the
// numeric time-series column (ADR-016 amd). Metrics are numeric, aggregatable
// time-series: DOUBLE and INT store directly and BOOLEAN stores as 0/1 (a
// duty-cycle-aggregatable value). STRING is not storable — a string-valued signal
// can not be averaged/min/maxed, so it is device state (an attribute /
// latest-value), not a metric. (The full MetricDataType set is still valid for
// command parameters, ADR-043, which are not time-series.)
func (t MetricDataType) StorableAsMetric() bool {
	switch t {
	case MetricDouble, MetricInt, MetricBoolean:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t MetricDataType) String() string {
	return string(t)
}

// MetricDefinition is a typed, unit-bearing metric declared on a DeviceProfile
// (ADR-016/ADR-045). Measurement events reference it by Key within the device's
// profile (resolved device → type → profile); the platform can validate/normalize
// on ingest and expose unit + type through the API. Hanging the definition off the
// profile (not each event) keeps the hot path cheap and lets many types share one.
type MetricDefinition struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceProfileId uint
	DeviceProfile   *DeviceProfile
	MetricKey       string         // referenced by measurement events
	DataType        string         // one of MetricDataType
	Unit            sql.NullString // UCUM code, e.g. Cel, kW, m/s
	MinValue        sql.NullFloat64
	MaxValue        sql.NullFloat64
	Enum            *datatypes.JSON // optional allowed-values array
	Descriptor      sql.NullString  // optional WoT @type / capability tag
	// Expression is a calculated metric's formula (ADR-082): CEL over the other, MEASURED
	// metrics of the same reading, compiled by core/calcfield. NULL means a measured
	// metric — one devices report — which is every metric that predates the column.
	//
	// 🔴 WHETHER IT IS NULL IS FIXED AT CREATION. A measured metric cannot be given a
	// formula and a calculated one cannot have its formula cleared; only the formula of a
	// calculated metric can be edited. That keeps the definition id (a measurement's
	// classifier) a stable provenance marker: every row ever stored under a calculated
	// definition was computed, in every version of the profile.
	Expression sql.NullString
}

// Calculated reports whether the metric is computed from a formula rather than reported
// by devices.
func (m *MetricDefinition) Calculated() bool { return m.Expression.Valid }

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. A measurement event resolves its definition by MetricKey, not
// by position in a page, so this clause serves the authoring/list surfaces only.
func (MetricDefinition) DefaultOrder() string {
	return "metric_definitions.created_at DESC, metric_definitions.token ASC"
}

// Data required to create a metric definition.
type MetricDefinitionCreateRequest struct {
	Token              string
	DeviceProfileToken string
	MetricKey          string
	Name               *string
	Description        *string
	DataType           string
	Unit               *string
	MinValue           *float64
	MaxValue           *float64
	Enum               *string
	Descriptor         *string
	Metadata           *string
	// Expression makes the new metric CALCULATED: its value is computed from this formula
	// instead of reported by devices. Omitted (nil) makes it measured. Which one it is
	// cannot change after creation.
	Expression *string
}

// A PARTIAL update to a metric definition: omit a field to leave the stored value
// alone, send null to clear a nullable one, send a value to set it. There is no
// Token — the definition is identified by the mutation's `token` argument.
//
// 🔴 WHAT THE dataType VOCABULARY CHECK MEANS ONCE THE FIELD CAN BE ABSENT. The
// full-replace shape validated dataType on every update because every update carried
// one. Under three states an ABSENT dataType has nothing to validate, and running the
// check against the stored value would be a different rule wearing the same clothes:
// it would refuse to let a caller fix a metric's NAME because of a data type it did
// not send and cannot see. The invariant is instead maintained inductively — create
// validates, and every update that NAMES dataType validates — which is what makes
// "the stored value is a valid, storable metric type" true of every row without an
// edit to an unrelated field having to re-prove it.
//
// The deliberate consequence, stated because it is a behaviour change: a row that
// somehow holds an invalid data type can now have its other fields edited. That is
// the same shape as the geofence ceiling, which refuses GROWTH rather than existence
// — the alternative traps the row, since the only surface that could repair it is
// the one being refused.
type MetricDefinitionUpdateRequest struct {
	// DeviceProfileToken re-parents the definition. The FK is NOT NULL, so an
	// explicit null is refused rather than honoured (a metric with no profile is
	// unreachable by any device), and an unknown token refuses the whole update.
	DeviceProfileToken dcgraphql.OptionalString
	// MetricKey is what a measurement event names, and it must stay unique within
	// the profile the definition ends up in — so it is required and unclearable.
	MetricKey   dcgraphql.OptionalString
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// DataType is a NOT NULL vocabulary column: a null would store "", which is not
	// a metric type, so it is refused.
	DataType   dcgraphql.OptionalString
	Unit       dcgraphql.OptionalString
	MinValue   dcgraphql.OptionalFloat64
	MaxValue   dcgraphql.OptionalFloat64
	Enum       dcgraphql.OptionalString
	Descriptor dcgraphql.OptionalString
	Metadata   dcgraphql.OptionalString
	// Expression edits a CALCULATED metric's formula, and is refused on a measured one
	// (calculated-ness is fixed at creation, see MetricDefinition.Expression). On a
	// calculated metric a null or blank formula is refused like any required field —
	// clearing it would turn the metric into a measured one. On a measured metric an
	// explicit null asks for what it already is and is accepted.
	Expression dcgraphql.OptionalString
}

// Search criteria for locating metric definitions.
type MetricDefinitionSearchCriteria struct {
	rdb.Pagination
	DeviceProfile *string // device profile token
	MetricKey     *string
}

// Results for metric definition search.
type MetricDefinitionSearchResults struct {
	Results    []MetricDefinition
	Pagination rdb.SearchResultsPagination
}
