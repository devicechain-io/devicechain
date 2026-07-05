// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// CommandParamKind distinguishes a scalar parameter (a single typed value) from
// an object parameter (a nested group of parameters). SCALAR is the default when
// the kind is empty, so a plain typed descriptor need not set it.
type CommandParamKind string

const (
	CommandParamScalar CommandParamKind = "SCALAR"
	CommandParamObject CommandParamKind = "OBJECT"
)

// CommandParameter is one descriptor in a CommandDefinition's parameter schema
// (ADR-043). It is the typed contract the console renders as a form field and the
// delivery path validates an issued payload against. A scalar parameter carries a
// DataType (reusing the ADR-016 MetricDataType vocabulary) plus optional
// unit/bounds/enum/default; an object parameter (Kind == OBJECT) nests a child
// Parameters list, letting a command express structured arguments. The schema is
// an ordered slice so the console renders parameters in declaration order.
type CommandParameter struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Kind        CommandParamKind   `json:"kind,omitempty"`     // SCALAR (default) | OBJECT
	DataType    MetricDataType     `json:"dataType,omitempty"` // scalar only; ADR-016 vocabulary
	Unit        string             `json:"unit,omitempty"`     // scalar only; UCUM code, metadata
	Required    bool               `json:"required,omitempty"`
	Default     *string            `json:"default,omitempty"`    // scalar only; console hint, not injected
	MinValue    *float64           `json:"minValue,omitempty"`   // scalar numeric only
	MaxValue    *float64           `json:"maxValue,omitempty"`   // scalar numeric only
	Enum        []string           `json:"enum,omitempty"`       // scalar only; allowed values
	Parameters  []CommandParameter `json:"parameters,omitempty"` // object only; nested descriptors
}

// CommandDefinition is a typed command declared on a DeviceProfile (ADR-043/
// ADR-045). It gives the command vocabulary structure: each
// definition names a CommandKey the profile's devices accept and carries an
// ordered parameter schema describing that command's arguments. The console reads
// the schema to render a command form; the delivery path validates an issued
// command's payload against it. Hanging the definition off the profile (mirroring
// MetricDefinition, ADR-016) keeps the fleet consistent and makes the command
// model typed rather than the opaque Name+JSON blob it was.
//
// The parameter schema is stored as a JSONB document (an ordered
// []CommandParameter), not decomposed into child rows: it is a nested,
// order-bearing contract read and validated as a whole and never queried by inner
// field, so a relational decomposition (self-referential rows + an ordering
// column, reassembled on every read) would buy nothing. The command *vocabulary* —
// which keys a profile accepts — is relational (one row per key); only each
// command's argument shape is documentary. This is the JSON-Schema / OpenAPI
// parameter-modeling choice, not the opaque-command-blob the ADR removes.
type CommandDefinition struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceProfileId uint
	DeviceProfile   *DeviceProfile
	CommandKey      string          // the command the profile's devices accept
	ParameterSchema *datatypes.JSON // ordered []CommandParameter (JSONB); nil = no declared params
}

// Data required to create a command definition. ParameterSchema is the JSON
// encoding of an ordered []CommandParameter; it is validated for well-formedness
// on create/update (see ValidateParameterSchema). A nil or empty schema declares
// a command that takes no structured arguments.
type CommandDefinitionCreateRequest struct {
	Token              string
	DeviceProfileToken string
	CommandKey         string
	Name               *string
	Description        *string
	ParameterSchema    *string
	Metadata           *string
}

// Search criteria for locating command definitions.
type CommandDefinitionSearchCriteria struct {
	rdb.Pagination
	DeviceProfile *string // device profile token
	CommandKey    *string
}

// Results for command definition search.
type CommandDefinitionSearchResults struct {
	Results    []CommandDefinition
	Pagination rdb.SearchResultsPagination
}
