// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestConfigSchemaValidation(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(config.InstanceJSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://devicechain.invalid/instance-config.schema.json"
	if err := compiler.AddResource(location, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := json.Marshal(config.NewDefaultInstanceConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		json  string
		valid bool
	}{
		{"default document with Go field names", string(defaults), true},
		{"omitted fields", `{}`, true},
		{"Helm casing", `{"infrastructure":{"nats":{"hostname":"nats","port":4222,"mqttFailClosed":false}}}`, true},
		{"nullable values", `{"infrastructure":{"tracing":{"samplingRatio":null},"nats":{"fetch":{"ahead":null},"hostname":null,"shardGroups":null},"http":{"trustedProxies":null}}}`, true},
		{"Unicode decoder casing", `{"infraſtructure":{"ſecrets":{"rootKey":"example"}}}`, true},
		{"retired key", `{"Infrastructure":{"Metrics":{"HTTPPORT":{"any":"ignored"}}}}`, true},
		{"typed map", `{"infrastructure":{"nats":{"auth":{"areaPasswords":{"event-processing":"password"}}}}}`, true},
		{"driver-owned map", `{"persistence":{"rdb":{"configuration":{"driverKey":{"arbitrary":[1,true,null]}}}}}`, true},
		{"integer map keys", `{"infrastructure":{"nats":{"shardGroups":{"count":2,"placementTags":{"0":["zone-a"],"1":["zone-b"]},"seed":{"weights":[32,32]}}}}}`, true},
		{"unknown root key", `{"infrastructrue":{}}`, false},
		{"unknown nested key", `{"infrastructure":{"nats":{"hostnmae":"nats"}}}`, false},
		{"unknown key inside pointer", `{"infrastructure":{"nats":{"shardGroups":{"seed":{"wieghts":[32,32]}}}}}`, false},
		{"wrong scalar type", `{"infrastructure":{"nats":{"port":"4222"}}}`, false},
		{"negative unsigned integer", `{"infrastructure":{"nats":{"port":-1}}}`, false},
		{"overflow unsigned integer", `{"infrastructure":{"nats":{"port":4294967296}}}`, false},
		{"fractional integer", `{"infrastructure":{"nats":{"port":4222.5}}}`, false},
		{"wrong pointer type", `{"infrastructure":{"nats":{"fetch":{"ahead":"false"}}}}`, false},
		{"wrong slice element", `{"infrastructure":{"http":{"trustedProxies":[3]}}}`, false},
		{"wrong map value", `{"infrastructure":{"nats":{"auth":{"areaPasswords":{"event-processing":3}}}}}`, false},
		{"wrong integer map key", `{"infrastructure":{"nats":{"shardGroups":{"placementTags":{"zone-a":["tag"]}}}}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(tc.json))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("schema validation valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
			// Independent counterweight: these structural examples must agree
			// with the decoder, without invoking semantic startup validation.
			decoder := json.NewDecoder(strings.NewReader(tc.json))
			decoder.DisallowUnknownFields()
			var into config.InstanceConfiguration
			if tc.name != "retired key" {
				if err := decoder.Decode(&into); (err == nil) != tc.valid {
					t.Fatalf("strict decoder valid=%v, want %v: %v", err == nil, tc.valid, err)
				}
			}
		})
	}
}

type schemaFailWriter struct{}

func (schemaFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestConfigSchemaReportsOutputFailure(t *testing.T) {
	configSchemaCmd.SetOut(schemaFailWriter{})
	t.Cleanup(func() { configSchemaCmd.SetOut(nil) })
	if err := configSchemaCmd.RunE(configSchemaCmd, nil); err == nil {
		t.Fatal("command ignored an output failure")
	}
}
