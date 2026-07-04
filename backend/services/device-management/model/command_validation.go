// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// ValidateParameterSchema checks that a command definition's parameter schema is
// well-formed at declaration time (ADR-043). It enforces, recursively per level:
// a non-empty, level-unique parameter Name; a scalar parameter declares a known
// MetricDataType and its optional Default/Enum/bounds parse against that type; and
// an object parameter (Kind == OBJECT) declares no scalar constraints and nests a
// (recursively valid) child list. A nil or empty schema is valid — the command
// simply takes no declared arguments.
func ValidateParameterSchema(schema []CommandParameter) error {
	return validateParamLevel(schema, "")
}

func validateParamLevel(params []CommandParameter, path string) error {
	seen := make(map[string]struct{}, len(params))
	for _, p := range params {
		where := paramPath(path, p.Name)
		if p.Name == "" {
			return fmt.Errorf("command parameter at %q has an empty name", pathOrRoot(path))
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("command parameter %q is declared more than once", where)
		}
		seen[p.Name] = struct{}{}

		switch p.Kind {
		case CommandParamObject:
			if p.DataType != "" || p.Unit != "" || p.Default != nil ||
				p.MinValue != nil || p.MaxValue != nil || len(p.Enum) > 0 {
				return fmt.Errorf("object parameter %q must not declare scalar constraints", where)
			}
			if err := validateParamLevel(p.Parameters, where); err != nil {
				return err
			}
		case CommandParamScalar, "":
			if len(p.Parameters) > 0 {
				return fmt.Errorf("scalar parameter %q must not nest child parameters", where)
			}
			if !p.DataType.Valid() {
				return fmt.Errorf("command parameter %q has invalid data type %q", where, p.DataType)
			}
			if err := checkParamConstraints(p, where); err != nil {
				return err
			}
		default:
			return fmt.Errorf("command parameter %q has unknown kind %q", where, p.Kind)
		}
	}
	return nil
}

// checkParamConstraints validates that a scalar descriptor's own Default/bounds
// are internally consistent with its DataType.
func checkParamConstraints(p CommandParameter, where string) error {
	if (p.MinValue != nil || p.MaxValue != nil) &&
		p.DataType != MetricDouble && p.DataType != MetricInt {
		return fmt.Errorf("command parameter %q declares numeric bounds on non-numeric type %s", where, p.DataType)
	}
	if p.MinValue != nil && p.MaxValue != nil && *p.MinValue > *p.MaxValue {
		return fmt.Errorf("command parameter %q has minValue > maxValue", where)
	}
	if p.Default != nil {
		if err := validateScalar(p, *p.Default, where); err != nil {
			return fmt.Errorf("command parameter %q default: %w", where, err)
		}
	}
	return nil
}

// ValidateCommandPayload checks a command payload against a command definition's
// parameter schema (ADR-043 enqueue validation) — the command-side mirror of
// ValidateMetricValue. Unlike measurement validation (lenient: an undeclared
// metric key passes), command validation is STRICT: a payload key not present in
// the schema is rejected, because a command is an actuation and a mis-keyed
// argument must never be silently delivered. It enforces, per declared parameter:
// required presence; the value's JSON type matches the declared DataType; numeric
// bounds; the enum allow-list; and, for an object parameter, recursive validation
// of the nested object. A definition with a nil/empty schema accepts any
// well-formed JSON object (the backward-compatible free-form path).
func ValidateCommandPayload(def *CommandDefinition, payload []byte) error {
	schema, err := def.parameterSchema()
	if err != nil {
		return fmt.Errorf("command %q: %w", def.CommandKey, err)
	}
	// Backward-compatible free-form path: a definition that declares no parameters
	// accepts any payload as today (the JSON-validity check lives at the enqueue
	// boundary), so it is not decoded or constrained here.
	if len(schema) == 0 {
		return nil
	}
	// A payload is optional; validate an absent payload as an empty object so a
	// command that declares only optional parameters still enqueues.
	if len(bytes.TrimSpace(payload)) == 0 {
		if req := firstRequired(schema); req != "" {
			return fmt.Errorf("command %q: required parameter %q is missing", def.CommandKey, req)
		}
		return nil
	}
	obj, err := decodeObject(payload)
	if err != nil {
		return fmt.Errorf("command %q: %w", def.CommandKey, err)
	}
	if err := validatePayloadLevel(schema, obj, ""); err != nil {
		return fmt.Errorf("command %q: %w", def.CommandKey, err)
	}
	return nil
}

// parameterSchema decodes the stored JSONB schema into typed descriptors. A nil
// or empty column decodes to a nil slice (no declared parameters).
func (def *CommandDefinition) parameterSchema() ([]CommandParameter, error) {
	if def.ParameterSchema == nil || len(bytes.TrimSpace(*def.ParameterSchema)) == 0 {
		return nil, nil
	}
	var schema []CommandParameter
	if err := json.Unmarshal(*def.ParameterSchema, &schema); err != nil {
		return nil, fmt.Errorf("parameter schema is not valid JSON: %w", err)
	}
	return schema, nil
}

func validatePayloadLevel(params []CommandParameter, obj map[string]json.RawMessage, path string) error {
	declared := make(map[string]CommandParameter, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	// Strict: reject any argument the schema does not declare.
	for key := range obj {
		if _, ok := declared[key]; !ok {
			return fmt.Errorf("unknown parameter %q", paramPath(path, key))
		}
	}
	for _, p := range params {
		where := paramPath(path, p.Name)
		raw, present := obj[p.Name]
		if !present || isJSONNull(raw) {
			if p.Required {
				return fmt.Errorf("required parameter %q is missing", where)
			}
			continue
		}
		if p.Kind == CommandParamObject {
			nested, err := decodeObject(raw)
			if err != nil {
				return fmt.Errorf("parameter %q: %w", where, err)
			}
			if err := validatePayloadLevel(p.Parameters, nested, where); err != nil {
				return err
			}
			continue
		}
		if err := validateScalarValue(p, raw, where); err != nil {
			return err
		}
	}
	return nil
}

// validateScalarValue checks a raw JSON value against a scalar descriptor: the
// JSON type matches the declared DataType, then numeric bounds and enum.
func validateScalarValue(p CommandParameter, raw json.RawMessage, where string) error {
	switch p.DataType {
	case MetricDouble, MetricInt:
		var num json.Number
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&num); err != nil {
			return fmt.Errorf("parameter %q: %s is not a valid %s", where, raw, p.DataType)
		}
		if p.DataType == MetricInt {
			if _, err := strconv.ParseInt(num.String(), 10, 64); err != nil {
				return fmt.Errorf("parameter %q: %s is not a valid %s", where, raw, p.DataType)
			}
		}
		f, err := num.Float64()
		if err != nil {
			return fmt.Errorf("parameter %q: %s is not a valid %s", where, raw, p.DataType)
		}
		if err := checkParamBounds(p, f, where); err != nil {
			return err
		}
		return checkParamEnum(p, num.String(), where)
	case MetricBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("parameter %q: %s is not a valid %s", where, raw, p.DataType)
		}
		return nil
	case MetricString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("parameter %q: %s is not a valid %s", where, raw, p.DataType)
		}
		return checkParamEnum(p, s, where)
	default:
		// An unknown declared type is a schema fault caught by ValidateParameterSchema
		// at declaration; do not reject the payload on its behalf.
		return nil
	}
}

// validateScalar checks a declared Default (a string form) against a scalar
// descriptor, reusing ValidateMetricValue's value-shaped semantics.
func validateScalar(p CommandParameter, value string, where string) error {
	def := &MetricDefinition{
		MetricKey: where,
		DataType:  p.DataType.String(),
	}
	if p.MinValue != nil {
		def.MinValue.Valid, def.MinValue.Float64 = true, *p.MinValue
	}
	if p.MaxValue != nil {
		def.MaxValue.Valid, def.MaxValue.Float64 = true, *p.MaxValue
	}
	return ValidateMetricValue(def, value)
}

func checkParamBounds(p CommandParameter, v float64, where string) error {
	if p.MinValue != nil && v < *p.MinValue {
		return fmt.Errorf("parameter %q: %v is below the minimum %v", where, v, *p.MinValue)
	}
	if p.MaxValue != nil && v > *p.MaxValue {
		return fmt.Errorf("parameter %q: %v is above the maximum %v", where, v, *p.MaxValue)
	}
	return nil
}

func checkParamEnum(p CommandParameter, value string, where string) error {
	if len(p.Enum) == 0 {
		return nil
	}
	for _, a := range p.Enum {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("parameter %q: %q is not one of the allowed values %v", where, value, p.Enum)
}

// firstRequired returns the path of the first required parameter in the schema,
// or "" when none are required (used to reject an empty payload fast).
func firstRequired(params []CommandParameter) string {
	for _, p := range params {
		if p.Required {
			return p.Name
		}
	}
	return ""
}

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("payload is not a JSON object: %w", err)
	}
	if obj == nil {
		return nil, fmt.Errorf("payload is not a JSON object")
	}
	return obj, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func paramPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func pathOrRoot(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}
