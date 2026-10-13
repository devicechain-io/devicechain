// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// thingModelSchema compiles the vendored W3C Thing Model schema (testdata/wot/README.md
// says where it comes from). No network: the schema's own $id is registered as the
// resource it was read from.
func thingModelSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "wot", "tm-json-schema-v1.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	const id = "https://www.w3.org/2022/wot/tm-schema/v1.1"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateThingModel(t *testing.T, schema *jsonschema.Schema, out []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the model is not JSON: %v\n%s", err, out)
	}
	return schema.Validate(inst)
}

// seedWotProfile publishes profile "pump" with three metrics and two commands, one taking
// typed, nested parameters, and returns its profile and version 1.
func seedWotProfile(t *testing.T) (*DeviceProfile, *DeviceProfileVersion) {
	t.Helper()
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	name, desc := "Pump", "A circulation pump."
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "pump", Name: &name, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	metric := func(token, key, dataType string, unit, descriptor *string, min, max *float64, enum *string) {
		t.Helper()
		if _, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: token, DeviceProfileToken: "pump",
			MetricKey: key, DataType: dataType, Unit: unit, Descriptor: descriptor, MinValue: min, MaxValue: max, Enum: enum,
			Name: &key}); err != nil {
			t.Fatalf("metric %s: %v", key, err)
		}
	}
	str := func(s string) *string { return &s }
	f := func(v float64) *float64 { return &v }
	metric("m1", "temperature", "DOUBLE", str("Cel"), str("saref:Temperature"), f(-40), f(125), nil)
	// Allowed values in their documented form, a JSON array of strings: the model must
	// render them as integers, the property's type.
	metric("m2", "speed", "INT", str("1/min"), nil, nil, nil, str(`["0", "1500", "3000"]`))
	metric("m3", "running", "BOOLEAN", nil, nil, nil, nil, nil)

	command := func(token, key string, schema *string) {
		t.Helper()
		if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{Token: token, DeviceProfileToken: "pump",
			CommandKey: key, ParameterSchema: schema}); err != nil {
			t.Fatalf("command %s: %v", key, err)
		}
	}
	command("c1", "reboot", nil)
	command("c2", "setSpeed", str(`[
	  {"name":"rpm","dataType":"INT","unit":"1/min","required":true,"minValue":0,"maxValue":3000,"enum":["0","1500","3000"]},
	  {"name":"ramp","kind":"OBJECT","parameters":[{"name":"seconds","dataType":"DOUBLE","required":true},{"name":"mode","dataType":"STRING","enum":["soft","hard"]}]}
	]`))

	if _, err := api.PublishDeviceProfile(ctx, "pump", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"pump"})
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profile: %v", err)
	}
	return profiles[0], activeVersion(t, api, ctx, profiles[0])
}

func activeVersion(t *testing.T, api *Api, ctx context.Context, profile *DeviceProfile) *DeviceProfileVersion {
	t.Helper()
	version, err := api.ActiveDeviceProfileVersion(ctx, profile)
	if err != nil || version == nil {
		t.Fatalf("active version of %s: %v", profile.Token, err)
	}
	return version
}

// A published version renders as a Thing Model that the W3C TD 1.1 Thing Model schema
// accepts, with each metric a typed property and each command an action whose input is
// its typed parameters.
func TestAPublishedProfileVersionIsAValidThingModel(t *testing.T) {
	schema := thingModelSchema(t)
	profile, version := seedWotProfile(t)

	out, err := ThingModelOf(profile, version)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := validateThingModel(t, schema, out); err != nil {
		t.Fatalf("the model does not validate against the Thing Model schema: %v\n%s", err, out)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"@context":    "https://www.w3.org/2022/wot/td/v1.1",
		"@type":       "tm:ThingModel",
		"title":       "Pump",
		"description": "A circulation pump.",
		"version":     map[string]any{"model": "1"},
		"properties": map[string]any{
			"temperature": map[string]any{"@type": "saref:Temperature", "title": "temperature", "type": "number",
				"unit": "Cel", "minimum": -40.0, "maximum": 125.0, "readOnly": true, "observable": true},
			"speed": map[string]any{"title": "speed", "type": "integer", "unit": "1/min",
				"enum": []any{0.0, 1500.0, 3000.0}, "readOnly": true, "observable": true},
			"running": map[string]any{"title": "running", "type": "boolean", "readOnly": true, "observable": true},
		},
		"actions": map[string]any{
			"reboot": map[string]any{},
			"setSpeed": map[string]any{"input": map[string]any{
				"type":     "object",
				"required": []any{"rpm"},
				"properties": map[string]any{
					"rpm": map[string]any{"type": "integer", "unit": "1/min", "minimum": 0.0, "maximum": 3000.0,
						"enum": []any{0.0, 1500.0, 3000.0}},
					"ramp": map[string]any{"type": "object", "required": []any{"seconds"}, "properties": map[string]any{
						"seconds": map[string]any{"type": "number"},
						"mode":    map[string]any{"type": "string", "enum": []any{"soft", "hard"}},
					}},
				},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("model\n%s\nwant\n%s", gotJSON, wantJSON)
	}

	again, err := ThingModelOf(profile, version)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatal("rendering the same version twice gave different bytes")
	}
}

// The control: the schema is not a rubber stamp. The same document without its Thing
// Model type, or with a property type outside the WoT vocabulary, is refused, so a pass
// above is the schema's verdict and not a validator that accepts anything.
func TestTheThingModelSchemaRefusesWhatIsNotAThingModel(t *testing.T) {
	schema := thingModelSchema(t)
	profile, version := seedWotProfile(t)
	out, err := ThingModelOf(profile, version)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"no @type": func(d map[string]any) { delete(d, "@type") },
		// An instance version belongs to a Thing Description, never to a model.
		"an instance version": func(d map[string]any) { d["version"] = map[string]any{"instance": "1"} },
		"a property typed outside the vocabulary": func(d map[string]any) {
			d["properties"].(map[string]any)["running"].(map[string]any)["type"] = "bool"
		},
	} {
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		broken, _ := json.Marshal(doc)
		if validateThingModel(t, schema, broken) == nil {
			t.Errorf("%s: the schema accepted a document that is not a Thing Model", name)
		}
	}
}

// A version with no metrics and no commands is still a valid (empty) Thing Model, titled
// by the profile's token when it has no name.
func TestAnEmptyProfileVersionIsAValidThingModel(t *testing.T) {
	schema := thingModelSchema(t)
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "bare"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "bare", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	profiles, _ := api.DeviceProfilesByToken(ctx, []string{"bare"})
	out, err := api.DeviceProfileVersionThingModel(ctx, activeVersion(t, api, ctx, profiles[0]))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateThingModel(t, schema, out); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := `{"@context":"https://www.w3.org/2022/wot/td/v1.1","@type":"tm:ThingModel","title":"bare","version":{"model":"1"}}`
	if string(out) != want {
		t.Fatalf("model %s, want %s", out, want)
	}
}

// A metric whose allowed values are not a list of distinct values cannot be expressed, and
// is refused rather than rendered without its constraint.
func TestAnInexpressibleConstraintIsRefusedNotDropped(t *testing.T) {
	for _, enum := range []string{`{"a":1}`, `[1, 1]`, `"x"`} {
		raw := []byte(enum)
		if _, ok := wotEnum(raw); ok {
			t.Errorf("enum %s was accepted", enum)
		}
	}
	for _, enum := range []string{`[]`, `null`, `[1, "1"]`} {
		if _, ok := wotEnum([]byte(enum)); !ok {
			t.Errorf("enum %s was refused", enum)
		}
	}
}
