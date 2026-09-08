// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// THE TYPED-FIELD VALIDATOR, SHARED BY TWO CONTRACTS.
//
// This file is the whole of what it means to check values against a
// []ParameterSpec: a declaration-time pass (is the contract itself coherent?) and
// an instance-time pass (does this document satisfy it?). It began as
// command_validation.go serving command payloads alone (ADR-043); the asset-type
// property schema (ADR-072) needs the identical two passes over the identical
// descriptor, so the passes moved here rather than being spelled a second time.
//
// 🔴 THE ONLY THING THAT VARIES BETWEEN THE TWO USERS IS THE NOUN IN THE ERROR
// MESSAGE, and it is carried as data rather than as a second copy of the code. An
// author who typed a bad bound on an asset type must be told about an "asset
// property", not a "command parameter" — but "parameter" is exactly what the
// command path must keep saying, because those strings are relayed verbatim to the
// tenant API client as a rejection reason and one of them is pinned character for
// character by command-delivery's enqueue_validator_test. specChecker holds the
// three words that differ; nothing else is parameterized, so the two contracts
// cannot drift apart one constraint at a time.

// specChecker validates a []ParameterSpec contract and the documents that fill it,
// naming the thing being validated in author-facing messages.
type specChecker struct {
	// decl names the field in DECLARATION-time messages, where the message must say
	// which KIND of contract is malformed ("command parameter %q has an empty name").
	decl string
	// noun and plural name the field in every other message, where the surrounding
	// context has already established which contract is meant ("unknown parameter
	// %q", "must not nest child parameters").
	noun   string
	plural string
}

var (
	// commandSpecs validates a CommandDefinition's parameter schema. Its wording is
	// load-bearing: see the note above.
	commandSpecs = specChecker{decl: "command parameter", noun: "parameter", plural: "parameters"}
	// assetPropertySpecs validates an AssetType's property schema (ADR-072).
	assetPropertySpecs = specChecker{decl: "asset property", noun: "property", plural: "properties"}
)

// validateSchema checks that a typed-field contract is well-formed at declaration
// time. It enforces, recursively per level: a non-empty, level-unique Name; a
// scalar field declares a known MetricDataType and its optional Default/Enum/bounds
// parse against that type; and an object field (Kind == OBJECT) declares no scalar
// constraints and nests a (recursively valid) child list. A nil or empty schema is
// valid — the contract simply declares no fields.
func (c specChecker) validateSchema(schema []ParameterSpec) error {
	return c.validateLevel(schema, "")
}

func (c specChecker) validateLevel(params []ParameterSpec, path string) error {
	seen := make(map[string]struct{}, len(params))
	for _, p := range params {
		where := paramPath(path, p.Name)
		if p.Name == "" {
			return fmt.Errorf("%s at %q has an empty name", c.decl, pathOrRoot(path))
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("%s %q is declared more than once", c.decl, where)
		}
		seen[p.Name] = struct{}{}

		switch p.Kind {
		case ParameterSpecObject:
			if p.DataType != "" || p.Unit != "" || p.Default != nil ||
				p.MinValue != nil || p.MaxValue != nil || len(p.Enum) > 0 {
				return fmt.Errorf("object %s %q must not declare scalar constraints", c.noun, where)
			}
			if err := c.validateLevel(p.Parameters, where); err != nil {
				return err
			}
		case ParameterSpecScalar, "":
			if len(p.Parameters) > 0 {
				return fmt.Errorf("scalar %s %q must not nest child %s", c.noun, where, c.plural)
			}
			if !p.DataType.Valid() {
				return fmt.Errorf("%s %q has invalid data type %q", c.decl, where, p.DataType)
			}
			if err := c.checkConstraints(p, where); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s %q has unknown kind %q", c.decl, where, p.Kind)
		}
	}
	return nil
}

// checkConstraints validates that a scalar descriptor's own bounds, enum, and
// Default are internally consistent with its DataType, so an unsatisfiable field
// (e.g. an INT enum of ["abc"], or a default outside its own enum) is rejected at
// declaration rather than silently stranding every document that must fill it.
func (c specChecker) checkConstraints(p ParameterSpec, where string) error {
	if (p.MinValue != nil || p.MaxValue != nil) &&
		p.DataType != MetricDouble && p.DataType != MetricInt {
		return fmt.Errorf("%s %q declares numeric bounds on non-numeric type %s", c.decl, where, p.DataType)
	}
	if p.MinValue != nil && p.MaxValue != nil && *p.MinValue > *p.MaxValue {
		return fmt.Errorf("%s %q has minValue > maxValue", c.decl, where)
	}
	if len(p.Enum) > 0 {
		if p.DataType == MetricBoolean {
			return fmt.Errorf("%s %q declares an enum on BOOLEAN", c.decl, where)
		}
		for _, e := range p.Enum {
			if err := validateScalarLiteral(p, e); err != nil {
				return fmt.Errorf("%s %q enum value %q: %w", c.decl, where, e, err)
			}
		}
	}
	if p.Default != nil {
		if err := c.validateDefault(p, *p.Default, where); err != nil {
			return fmt.Errorf("%s %q default: %w", c.decl, where, err)
		}
	}
	return nil
}

// validateScalarLiteral checks that an author-declared string literal (an enum
// member) parses as the field's declared scalar type.
func validateScalarLiteral(p ParameterSpec, value string) error {
	switch p.DataType {
	case MetricDouble:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("%q is not a valid %s", value, p.DataType)
		}
	case MetricInt:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("%q is not a valid %s", value, p.DataType)
		}
	case MetricString:
		// Any string is a valid STRING literal.
	}
	return nil
}

// validateDefault checks a declared Default (its string form) against the field's
// scalar type, numeric bounds, and enum allow-list.
func (c specChecker) validateDefault(p ParameterSpec, value string, where string) error {
	switch p.DataType {
	case MetricDouble, MetricInt:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%q is not a valid %s", value, p.DataType)
		}
		if p.DataType == MetricInt && f != math.Trunc(f) {
			return fmt.Errorf("%q is not a valid %s", value, p.DataType)
		}
		if err := c.checkBounds(p, f, where); err != nil {
			return err
		}
		return c.checkEnumNumeric(p, f, where)
	case MetricBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%q is not a valid %s", value, p.DataType)
		}
		return nil
	case MetricString:
		return c.checkEnumString(p, value, where)
	default:
		return nil
	}
}

// validateDocument checks a value document against a typed-field contract. It is
// STRICT: a key the schema does not declare is rejected, because a mis-keyed field
// that is silently accepted is indistinguishable from one that was never sent. It
// enforces, per declared field: required presence; the value's JSON type matches
// the declared DataType; numeric bounds; the enum allow-list; and, for an object
// field, recursive validation of the nested object.
//
// A nil/empty schema accepts any well-formed JSON object. An absent document —
// empty, whitespace, or a JSON null — is validated as an empty object, so a
// contract of only optional fields is satisfied by sending nothing.
func (c specChecker) validateDocument(schema []ParameterSpec, document []byte) error {
	if len(schema) == 0 {
		return nil
	}
	if trimmed := bytes.TrimSpace(document); len(trimmed) == 0 || string(trimmed) == "null" {
		if req := firstRequired(schema); req != "" {
			return fmt.Errorf("required %s %q is missing", c.noun, req)
		}
		return nil
	}
	obj, err := decodeObject(document)
	if err != nil {
		return err
	}
	return c.validateDocumentLevel(schema, obj, "")
}

func (c specChecker) validateDocumentLevel(params []ParameterSpec, obj map[string]json.RawMessage, path string) error {
	declared := make(map[string]ParameterSpec, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	// Strict: reject any entry the schema does not declare.
	for key := range obj {
		if _, ok := declared[key]; !ok {
			return fmt.Errorf("unknown %s %q", c.noun, paramPath(path, key))
		}
	}
	for _, p := range params {
		where := paramPath(path, p.Name)
		raw, present := obj[p.Name]
		if !present || isJSONNull(raw) {
			if p.Required {
				return fmt.Errorf("required %s %q is missing", c.noun, where)
			}
			continue
		}
		if p.Kind == ParameterSpecObject {
			nested, err := decodeObject(raw)
			if err != nil {
				return fmt.Errorf("%s %q: %w", c.noun, where, err)
			}
			if err := c.validateDocumentLevel(p.Parameters, nested, where); err != nil {
				return err
			}
			continue
		}
		if err := c.validateScalarValue(p, raw, where); err != nil {
			return err
		}
	}
	return nil
}

// validateScalarValue checks a raw JSON value against a scalar descriptor: the
// JSON type matches the declared DataType, then numeric bounds and the enum.
func (c specChecker) validateScalarValue(p ParameterSpec, raw json.RawMessage, where string) error {
	switch p.DataType {
	case MetricDouble, MetricInt:
		f, err := jsonNumberValue(p.DataType, raw)
		if err != nil {
			return fmt.Errorf("%s %q: %s is not a valid %s", c.noun, where, raw, p.DataType)
		}
		if err := c.checkBounds(p, f, where); err != nil {
			return err
		}
		return c.checkEnumNumeric(p, f, where)
	case MetricBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("%s %q: %s is not a valid %s", c.noun, where, raw, p.DataType)
		}
		return nil
	case MetricString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("%s %q: %s is not a valid %s", c.noun, where, raw, p.DataType)
		}
		return c.checkEnumString(p, s, where)
	default:
		// An unknown declared type is a schema fault caught by validateSchema at
		// declaration; do not reject the document on its behalf.
		return nil
	}
}

// jsonNumberValue decodes a numeric JSON value as a float64. For INT it accepts
// any integral value — 10, 10.0, 1e2 — and rejects a fractional one, since JSON
// has no distinct integer type and a producer may legitimately emit "10.0".
func jsonNumberValue(dt MetricDataType, raw json.RawMessage) (float64, error) {
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		return 0, err
	}
	f, err := num.Float64()
	if err != nil {
		return 0, err
	}
	if dt == MetricInt && (math.IsInf(f, 0) || f != math.Trunc(f)) {
		return 0, fmt.Errorf("%s is not an integer", raw)
	}
	return f, nil
}

func (c specChecker) checkBounds(p ParameterSpec, v float64, where string) error {
	if p.MinValue != nil && v < *p.MinValue {
		return fmt.Errorf("%s %q: %v is below the minimum %v", c.noun, where, v, *p.MinValue)
	}
	if p.MaxValue != nil && v > *p.MaxValue {
		return fmt.Errorf("%s %q: %v is above the maximum %v", c.noun, where, v, *p.MaxValue)
	}
	return nil
}

// checkEnumString enforces a STRING enum allow-list by exact match.
func (c specChecker) checkEnumString(p ParameterSpec, value string, where string) error {
	if len(p.Enum) == 0 {
		return nil
	}
	for _, a := range p.Enum {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("%s %q: %q is not one of the allowed values %v", c.noun, where, value, p.Enum)
}

// checkEnumNumeric enforces a numeric enum allow-list by comparing values
// numerically, so a document value 1.50 satisfies an enum entry "1.5". Entries are
// validated as parseable numbers at declaration (checkConstraints).
func (c specChecker) checkEnumNumeric(p ParameterSpec, v float64, where string) error {
	if len(p.Enum) == 0 {
		return nil
	}
	for _, a := range p.Enum {
		if af, err := strconv.ParseFloat(a, 64); err == nil && af == v {
			return nil
		}
	}
	return fmt.Errorf("%s %q: %v is not one of the allowed values %v", c.noun, where, v, p.Enum)
}

// firstRequired returns the path of the first required field in the schema, or ""
// when none are required (used to reject an empty document fast).
func firstRequired(params []ParameterSpec) string {
	for _, p := range params {
		if p.Required {
			return p.Name
		}
	}
	return ""
}

// decodeObject decodes a JSON object. It uses Unmarshal (not a streaming Decoder)
// so trailing garbage after the closing brace is rejected rather than silently
// accepted into the stored document.
func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Deliberately NOT wrapped: since ADR-043's enqueue gate relays a rejection
		// reason verbatim to the tenant API client, wrapping would put Go type names
		// ("cannot unmarshal array into Go value of type map[string]json.RawMessage")
		// in a user-facing message. The only actionable fact is that the payload must
		// be an object.
		return nil, fmt.Errorf("payload is not a JSON object")
	}
	if obj == nil {
		return nil, fmt.Errorf("payload is not a JSON object")
	}
	return obj, nil
}

// decodeSpecsStrict decodes a typed-field contract rejecting any unrecognized
// field, so an author's typo'd constraint key (e.g. "maximum" for "maxValue") is
// caught at declaration rather than silently dropped. A nil/empty document decodes
// to a nil slice.
//
// The error says "parameter schema" for both users, because it is the ONE message
// here a caller wraps with its own context before it reaches an author.
func decodeSpecsStrict(raw []byte) ([]ParameterSpec, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var schema []ParameterSpec
	if err := dec.Decode(&schema); err != nil {
		return nil, fmt.Errorf("parameter schema is not valid JSON: %w", err)
	}
	return schema, nil
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
