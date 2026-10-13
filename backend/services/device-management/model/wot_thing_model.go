// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

// The W3C Web of Things (WoT) projection of a published device profile version.
//
// 🔑 A PROFILE IS A THING MODEL, NOT A THING DESCRIPTION. The WoT Thing Description 1.1
// Recommendation defines two documents. A Thing Description describes ONE Thing and must
// say how to reach it: every property and action carries `forms` (protocol endpoints) and
// the document carries `security` and `securityDefinitions`. A Thing Model (TD 1.1 §10)
// describes a CLASS of Things — their properties, actions and data schemas — and leaves
// the per-instance binding out. A profile version is exactly the latter: it says what every
// device of the profile reports and accepts, and names no device and no endpoint. Emitting
// a Thing Description from it would mean inventing forms and security for a device that
// does not exist, so the export is a Thing Model (`"@type": "tm:ThingModel"`), validated
// against the Thing Model JSON Schema published with TD 1.1.
//
// It is a read-only projection of the version's frozen snapshot: nothing is stored, and a
// version's model never changes because the snapshot never does (only the profile's
// title and description are read from the profile itself).

// wotContext is the TD 1.1 JSON-LD context URI.
const wotContext = "https://www.w3.org/2022/wot/td/v1.1"

// wotThingModelType marks the document as a Thing Model.
const wotThingModelType = "tm:ThingModel"

// wotDataType maps a metric or parameter data type to its WoT data schema type. Every
// member of the vocabulary maps; an unknown one is an error rather than a guess.
func wotDataType(t MetricDataType) (string, error) {
	switch t {
	case MetricDouble:
		return "number", nil
	case MetricInt:
		return "integer", nil
	case MetricBoolean:
		return "boolean", nil
	case MetricString:
		return "string", nil
	default:
		return "", fmt.Errorf("data type %q has no Web of Things equivalent", t)
	}
}

// ThingModelOf renders a published profile version as a WoT Thing Model (JSON-LD):
//
//   - each metric definition becomes a read-only, observable property keyed by its metric
//     key, typed from its data type, with its unit, bounds and allowed values, and with its
//     Descriptor (the semantic tag reserved for this) as the property's `@type`;
//   - each command definition becomes an action keyed by its command key, whose `input` is
//     an object schema built from the command's typed parameters (none when it declares
//     none).
//
// The output is deterministic: keys are emitted sorted, so the same version always renders
// the same bytes.
func ThingModelOf(profile *DeviceProfile, version *DeviceProfileVersion) ([]byte, error) {
	if profile == nil || version == nil {
		return nil, fmt.Errorf("a Thing Model needs a profile and one of its published versions")
	}
	snap, err := ParseProfileSnapshot(version.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("profile %q version %d: %w", profile.Token, version.Version, err)
	}

	title := profile.Token
	if profile.Name.Valid && profile.Name.String != "" {
		title = profile.Name.String
	}
	doc := map[string]any{
		"@context": wotContext,
		"@type":    wotThingModelType,
		"title":    title,
		"version":  map[string]any{"model": strconv.Itoa(int(version.Version))},
	}
	if profile.Description.Valid && profile.Description.String != "" {
		doc["description"] = profile.Description.String
	}

	properties := make(map[string]any, len(snap.Metrics))
	for _, m := range snap.Metrics {
		if m == nil {
			continue
		}
		prop, err := wotProperty(m)
		if err != nil {
			return nil, fmt.Errorf("metric %q: %w", m.MetricKey, err)
		}
		properties[m.MetricKey] = prop
	}
	if len(properties) > 0 {
		doc["properties"] = properties
	}

	actions := make(map[string]any, len(snap.Commands))
	for _, c := range snap.Commands {
		if c == nil {
			continue
		}
		action, err := wotAction(c)
		if err != nil {
			return nil, fmt.Errorf("command %q: %w", c.CommandKey, err)
		}
		actions[c.CommandKey] = action
	}
	if len(actions) > 0 {
		doc["actions"] = actions
	}
	return json.Marshal(doc)
}

// DeviceProfileVersionThingModel renders a published version as a Thing Model, reading the
// profile it belongs to (in the caller's tenant) for its title and description.
func (api *Api) DeviceProfileVersionThingModel(ctx context.Context, version *DeviceProfileVersion) ([]byte, error) {
	profiles, err := api.DeviceProfilesById(ctx, []uint{version.DeviceProfileId})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profile of version %d: %w", version.Version, gorm.ErrRecordNotFound)
	}
	return ThingModelOf(profiles[0], version)
}

// wotProperty renders one metric definition. A metric is reported by the device, never
// written through the platform, so it is readOnly; it changes over time and is delivered
// as it changes, so it is observable.
func wotProperty(m *MetricDefinition) (map[string]any, error) {
	typ, err := wotDataType(MetricDataType(m.DataType))
	if err != nil {
		return nil, err
	}
	prop := map[string]any{"type": typ, "readOnly": true, "observable": true}
	if m.Descriptor.Valid && m.Descriptor.String != "" {
		prop["@type"] = m.Descriptor.String
	}
	if m.Name.Valid && m.Name.String != "" {
		prop["title"] = m.Name.String
	}
	if m.Description.Valid && m.Description.String != "" {
		prop["description"] = m.Description.String
	}
	if m.Unit.Valid && m.Unit.String != "" {
		prop["unit"] = m.Unit.String
	}
	if m.MinValue.Valid {
		prop["minimum"] = m.MinValue.Float64
	}
	if m.MaxValue.Valid {
		prop["maximum"] = m.MaxValue.Float64
	}
	if m.Enum != nil {
		values, ok := wotEnum(*m.Enum)
		if !ok {
			return nil, fmt.Errorf("its allowed values are not a list of distinct values, which a Thing Model cannot carry")
		}
		if values != nil {
			// Allowed values are documented as strings; a numeric or boolean metric's are
			// rendered as values of its type, so they can match what the device reports.
			for i, v := range values {
				if s, isText := v.(string); isText && typ != "string" {
					typed, err := wotTypedValue(typ, s)
					if err != nil {
						return nil, err
					}
					values[i] = typed
				}
			}
			if _, distinct := wotEnum(mustJSON(values)); !distinct {
				return nil, fmt.Errorf("its allowed values repeat once read as %s", typ)
			}
			prop["enum"] = values
		}
	}
	return prop, nil
}

// wotEnum reads a stored allowed-values document as a WoT enum: a non-empty array of
// distinct values. An empty array or a JSON null states no constraint and yields nil.
// Anything else cannot be expressed, and is reported rather than dropped, because a model
// that silently lost a constraint would tell a consumer the property accepts more than it
// does.
func wotEnum(raw []byte) ([]any, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	if len(values) == 0 {
		return nil, true
	}
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		key, err := json.Marshal(v)
		if err != nil || seen[string(key)] {
			return nil, false
		}
		seen[string(key)] = true
	}
	return values, true
}

// mustJSON encodes values decoded from JSON a moment ago, which cannot fail to encode.
func mustJSON(values []any) []byte {
	out, err := json.Marshal(values)
	if err != nil {
		panic("model: re-encoding decoded JSON values failed: " + err.Error())
	}
	return out
}

// wotAction renders one command definition. Its parameters become the action's input: an
// object schema whose properties are the parameters, in the order declared, and whose
// required list names the required ones.
func wotAction(c *CommandDefinition) (map[string]any, error) {
	action := map[string]any{}
	if c.Name.Valid && c.Name.String != "" {
		action["title"] = c.Name.String
	}
	if c.Description.Valid && c.Description.String != "" {
		action["description"] = c.Description.String
	}
	if c.ParameterSchema == nil || len(*c.ParameterSchema) == 0 {
		return action, nil
	}
	specs, err := decodeParameterSchemaStrict(*c.ParameterSchema)
	if err != nil {
		return nil, err
	}
	if len(specs) > 0 {
		input, err := wotObjectSchema(specs)
		if err != nil {
			return nil, err
		}
		action["input"] = input
	}
	return action, nil
}

// wotObjectSchema renders a list of parameters as an object data schema.
func wotObjectSchema(specs []ParameterSpec) (map[string]any, error) {
	properties := make(map[string]any, len(specs))
	required := make([]string, 0)
	for _, s := range specs {
		schema, err := wotParameterSchema(s)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", s.Name, err)
		}
		properties[s.Name] = schema
		if s.Required {
			required = append(required, s.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// wotTypedValue reads an allowed value, stored as text, as a value of the parameter's
// WoT type, so an integer parameter's enum lists integers rather than strings a consumer
// would compare against numbers and never match.
func wotTypedValue(typ, text string) (any, error) {
	switch typ {
	case "integer":
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("allowed value %q is not an integer", text)
		}
		return v, nil
	case "number":
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("allowed value %q is not a number", text)
		}
		return v, nil
	case "boolean":
		v, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("allowed value %q is not a boolean", text)
		}
		return v, nil
	default:
		return text, nil
	}
}

// wotParameterSchema renders one parameter: a nested object, or a scalar with its type,
// unit, bounds and allowed values. A parameter's default is an authoring hint stored as
// text, not a typed value, so it is not rendered as the schema's default.
func wotParameterSchema(s ParameterSpec) (map[string]any, error) {
	var schema map[string]any
	if s.Kind == ParameterSpecObject {
		nested, err := wotObjectSchema(s.Parameters)
		if err != nil {
			return nil, err
		}
		schema = nested
	} else {
		typ, err := wotDataType(s.DataType)
		if err != nil {
			return nil, err
		}
		schema = map[string]any{"type": typ}
		if s.Unit != "" {
			schema["unit"] = s.Unit
		}
		if s.MinValue != nil {
			schema["minimum"] = *s.MinValue
		}
		if s.MaxValue != nil {
			schema["maximum"] = *s.MaxValue
		}
		if len(s.Enum) > 0 {
			values := make([]any, len(s.Enum))
			for i, v := range s.Enum {
				typed, err := wotTypedValue(typ, v)
				if err != nil {
					return nil, err
				}
				values[i] = typed
			}
			schema["enum"] = values
		}
	}
	if s.Description != "" {
		schema["description"] = s.Description
	}
	return schema, nil
}
