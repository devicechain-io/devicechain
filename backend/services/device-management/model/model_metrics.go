// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

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

// String returns the underlying string value.
func (t MetricDataType) String() string {
	return string(t)
}

// MetricDefinition is a typed, unit-bearing metric declared on a Device Profile
// (the DeviceType entity, ADR-016). Measurement events reference it by Key within
// the device's profile; the platform can validate/normalize on ingest and expose
// unit + type through the API. Hanging the definition off the profile (not each
// event) keeps the hot path cheap and the fleet consistent.
type MetricDefinition struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeId uint
	DeviceType   *DeviceType
	MetricKey    string         // referenced by measurement events
	DataType     string         // one of MetricDataType
	Unit         sql.NullString // UCUM code, e.g. Cel, kW, m/s
	MinValue     sql.NullFloat64
	MaxValue     sql.NullFloat64
	Enum         *datatypes.JSON // optional allowed-values array
	Descriptor   sql.NullString  // optional WoT @type / capability tag
}

// Data required to create a metric definition.
type MetricDefinitionCreateRequest struct {
	Token           string
	DeviceTypeToken string
	MetricKey       string
	Name            *string
	Description     *string
	DataType        string
	Unit            *string
	MinValue        *float64
	MaxValue        *float64
	Enum            *string
	Descriptor      *string
	Metadata        *string
}

// Search criteria for locating metric definitions.
type MetricDefinitionSearchCriteria struct {
	rdb.Pagination
	DeviceType *string // device type token
	MetricKey  *string
}

// Results for metric definition search.
type MetricDefinitionSearchResults struct {
	Results    []MetricDefinition
	Pagination rdb.SearchResultsPagination
}
