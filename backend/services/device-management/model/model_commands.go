// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

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
// []ParameterSpec), not decomposed into child rows: it is a nested,
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
	ParameterSchema *datatypes.JSON // ordered []ParameterSpec (JSONB); nil = no declared params
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. This orders the paged command-definition LIST; it says
// nothing about parameter order, which is the ordered JSONB document's own business
// and is never re-derived from a query.
func (CommandDefinition) DefaultOrder() string {
	return "command_definitions.created_at DESC, command_definitions.token ASC"
}

// Data required to create a command definition. ParameterSchema is the JSON
// encoding of an ordered []ParameterSpec; it is validated for well-formedness
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

// A PARTIAL update to a command definition: omit a field to leave the stored value
// alone, send null to clear a nullable one, send a value to set it. There is no
// Token — the definition is identified by the mutation's `token` argument.
type CommandDefinitionUpdateRequest struct {
	// DeviceProfileToken re-parents the definition; the FK is NOT NULL, so a null is
	// refused and an unknown token refuses the whole update. The command key's
	// uniqueness is then checked against the profile the definition ends up IN, not
	// the one it is moving away from.
	DeviceProfileToken dcgraphql.OptionalString
	// CommandKey is the vocabulary entry the enqueue gate resolves; required, and
	// grammar-checked whenever the caller names it.
	CommandKey  dcgraphql.OptionalString
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// ParameterSchema is nullable — a command that takes no structured arguments is a
	// real state — so an explicit null clears it back to that.
	ParameterSchema dcgraphql.OptionalString
	Metadata        dcgraphql.OptionalString
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
