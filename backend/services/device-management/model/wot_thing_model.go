// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
// What the snapshot holds and the model deliberately leaves out:
//
//   - detection rules: they are what the platform does with a device's data, not part of
//     the device's interface, so a consumer of the model has no use for them;
//   - the location declaration and the configuration declaration: both describe what the
//     PLATFORM keeps about a device (where it is, which shared attributes it is sent), and
//     the model describes what a device reports and accepts;
//   - metadata on the profile, its metrics and its commands: free-form, with no WoT term;
//   - a parameter's default, for the reason wotParameterSchema gives.
//
// A constraint the model cannot carry faithfully is refused, never dropped or approximated:
// a model that lost one would tell a consumer a property accepts more than it does.
//
// The output is deterministic: keys are emitted sorted, so the same version, under the same
// profile name and description, always renders the same bytes.
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
	if (m.MinValue.Valid || m.MaxValue.Valid) && typ != "number" && typ != "integer" {
		return nil, fmt.Errorf("it has numeric bounds, which a %s property cannot carry", typ)
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
			// Allowed values are declared as text, and ingest enforces them only in that
			// form; a member that is not text is a constraint the platform does not apply,
			// so it is refused rather than advertised. A numeric or boolean metric's values
			// are rendered as values of its type, so they can match what the device reports.
			texts := make([]string, len(values))
			for i, v := range values {
				s, isText := v.(string)
				if !isText {
					return nil, fmt.Errorf("allowed value %s is not text, which is how allowed values are declared", mustText(v))
				}
				texts[i] = s
			}
			typed, err := wotTypedEnum(typ, texts)
			if err != nil {
				return nil, err
			}
			prop["enum"] = typed
		}
	}
	return prop, nil
}

// wotTypedEnum reads allowed values declared as text as values of the WoT type typ, and
// refuses a list whose members repeat once typed ("1" and "01" are one integer): a Thing
// Model's enum must hold distinct values.
func wotTypedEnum(typ string, texts []string) ([]any, error) {
	values := make([]any, len(texts))
	seen := make(map[string]bool, len(texts))
	for i, text := range texts {
		v, err := wotTypedValue(typ, text)
		if err != nil {
			return nil, err
		}
		key, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("allowed value %q cannot be written as %s: %w", text, typ, err)
		}
		if seen[string(key)] {
			return nil, fmt.Errorf("its allowed values repeat once read as %s (%s)", typ, key)
		}
		seen[string(key)] = true
		values[i] = v
	}
	return values, nil
}

// mustText renders a decoded JSON value for an error message.
func mustText(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(out)
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
		// ParseFloat accepts "NaN" and "Inf", which JSON has no way to write.
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
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
			values, err := wotTypedEnum(typ, s.Enum)
			if err != nil {
				return nil, err
			}
			schema["enum"] = values
		}
	}
	if s.Description != "" {
		schema["description"] = s.Description
	}
	return schema, nil
}
