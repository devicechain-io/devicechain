// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The COMMAND-facing half of the typed-field contract (ADR-043). The two passes it
// runs — declaration-time and instance-time — live in spec_validation.go, which the
// asset-type property schema (ADR-072) runs too; this file is the command's own
// entry points, its stored-column decode, and its wording.

// ValidateParameterSchema checks that a command definition's parameter schema is
// well-formed at declaration time. See specChecker.validateSchema for what that
// means; a nil or empty schema is valid — the command simply takes no declared
// arguments.
func ValidateParameterSchema(schema []ParameterSpec) error {
	return commandSpecs.validateSchema(schema)
}

// ValidateCommandPayload checks a command payload against a command definition's
// parameter schema (ADR-043 enqueue validation) — the command-side mirror of
// ValidateMetricValue. Unlike measurement validation (lenient: an undeclared
// metric key passes), command validation is STRICT: a payload key not present in
// the schema is rejected, because a command is an actuation and a mis-keyed
// argument must never be silently delivered. A definition with a nil/empty schema
// accepts any well-formed JSON object (the backward-compatible free-form path).
//
// 🔴 The message shape is a CONTRACT, not an implementation detail: the enqueue
// gate relays it verbatim to the tenant API client as a rejection reason, and
// command-delivery's enqueue_validator_test pins `command "drive": required
// parameter "speed" is missing` character for character.
func ValidateCommandPayload(def *CommandDefinition, payload []byte) error {
	if def == nil {
		return fmt.Errorf("nil command definition")
	}
	schema, err := def.parameterSchema()
	if err != nil {
		return fmt.Errorf("command %q: %w", def.CommandKey, err)
	}
	if err := commandSpecs.validateDocument(schema, payload); err != nil {
		return fmt.Errorf("command %q: %w", def.CommandKey, err)
	}
	return nil
}

// parameterSchema decodes the stored JSONB schema into typed descriptors. A nil
// or empty column decodes to a nil slice (no declared parameters).
func (def *CommandDefinition) parameterSchema() ([]ParameterSpec, error) {
	if def.ParameterSchema == nil || len(bytes.TrimSpace(*def.ParameterSchema)) == 0 {
		return nil, nil
	}
	var schema []ParameterSpec
	if err := json.Unmarshal(*def.ParameterSchema, &schema); err != nil {
		return nil, fmt.Errorf("parameter schema is not valid JSON: %w", err)
	}
	return schema, nil
}

// decodeParameterSchemaStrict decodes a command parameter-schema document
// rejecting any unrecognized field. Named for its one caller's vocabulary; the
// decode itself is shared (decodeSpecsStrict).
func decodeParameterSchemaStrict(raw []byte) ([]ParameterSpec, error) {
	return decodeSpecsStrict(raw)
}
