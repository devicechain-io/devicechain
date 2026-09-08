// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// ParameterSpecKind distinguishes a scalar field (a single typed value) from an
// object field (a nested group of fields). SCALAR is the default when the kind is
// empty, so a plain typed descriptor need not set it.
type ParameterSpecKind string

const (
	ParameterSpecScalar ParameterSpecKind = "SCALAR"
	ParameterSpecObject ParameterSpecKind = "OBJECT"
)

// ParameterSpec is one descriptor in a typed-field contract: a name, a datatype
// from the ADR-016 MetricDataType vocabulary, and the optional constraints an
// author can put on the values that fill it. It was introduced for command
// parameter schemas (ADR-043) and is now the area's ONE typed-field vocabulary,
// serving two contracts:
//
//   - a CommandDefinition's ParameterSchema — the arguments a command accepts,
//     validated at enqueue (ValidateCommandPayload); and
//   - an AssetType's PropertySchema — the properties an asset of that type carries
//     (ADR-072), validated when an asset's property document is written
//     (ValidateAssetProperties).
//
// It is deliberately NOT named for either of them. The type was called
// CommandParameter while it had one user; giving the second user that name would
// have made every asset-side signature read as if it were about commands, and
// spelling a second, structurally identical descriptor for assets would have made
// the two contracts diverge one constraint at a time. MetricDataType had already
// crossed the same line — it is the metric vocabulary AND the command-parameter
// one — so this follows a precedent rather than setting one.
//
// A scalar field carries a DataType plus optional unit/bounds/enum/default; an
// object field (Kind == OBJECT) nests a child Parameters list, letting a contract
// express structured values. The list is ordered so an authoring UI renders fields
// in declaration order.
//
// 🔴 Default is an AUTHORING HINT, NOT AN INJECTED VALUE. Nothing anywhere fills a
// missing field in from it — a required field with a default is still missing if
// the value document omits it, and that is checked by
// TestAssetPropertiesRequiredIsNotSatisfiedByADefault. A materialized default would
// freeze one version's declaration into stored data, where a later publish that
// changed the default could no longer reach it.
//
// 🔴 The json tags are the DURABLE key names, not decoration. This struct is
// serialized into two places that outlive the process that wrote them — a
// command_definitions.parameter_schema column and a frozen asset_type_versions
// snapshot — so a renamed tag silently drops the field from every stored document.
// Rename a Go field freely; changing a tag is a data migration.
type ParameterSpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Kind        ParameterSpecKind `json:"kind,omitempty"`     // SCALAR (default) | OBJECT
	DataType    MetricDataType    `json:"dataType,omitempty"` // scalar only; ADR-016 vocabulary
	Unit        string            `json:"unit,omitempty"`     // scalar only; UCUM code, metadata
	Required    bool              `json:"required,omitempty"`
	Default     *string           `json:"default,omitempty"`    // scalar only; authoring hint, not injected
	MinValue    *float64          `json:"minValue,omitempty"`   // scalar numeric only
	MaxValue    *float64          `json:"maxValue,omitempty"`   // scalar numeric only
	Enum        []string          `json:"enum,omitempty"`       // scalar only; allowed values
	Parameters  []ParameterSpec   `json:"parameters,omitempty"` // object only; nested descriptors
}
