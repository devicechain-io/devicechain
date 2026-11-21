// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"
)

func TestInstanceSchemaCommitted(t *testing.T) {
	raw, err := os.ReadFile("instance-config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("instance document must publish a JSON Schema dialect")
	}
	if err := CheckInstanceJSONSchema(raw); err != nil {
		t.Fatal(err)
	}
	// Negative controls: the check must reject valid JSON with a changed field,
	// as well as formatting drift. It must not merely check that a file exists.
	changed := bytes.Replace(raw, []byte(`"hostname"`), []byte(`"unknownHostname"`), 1)
	if bytes.Equal(changed, raw) {
		t.Fatal("negative control did not change a schema property")
	}
	for _, corrupt := range [][]byte{changed, append(bytes.Clone(raw), ' ')} {
		if err := CheckInstanceJSONSchema(corrupt); err == nil {
			t.Fatal("schema drift negative control was accepted")
		}
	}
}

func TestInstanceSchemaDecodingRules(t *testing.T) {
	var schema struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	raw, err := GenerateInstanceJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Defs) < 20 {
		t.Fatal("instance schema lost its nested config types")
	}
	for name, def := range schema.Defs {
		if def["additionalProperties"] != false {
			t.Errorf("%s accepts unknown keys", name)
		}
	}
	metrics := schema.Defs["MetricsConfiguration"]["properties"].(map[string]any)
	retired := metrics["httpPort"].(map[string]any)
	if retired["deprecated"] != true || retired["type"] != nil {
		t.Fatal("retired values must be documented and ignored for every JSON type")
	}
	for _, key := range []string{"rootKey", "ROOTKEY", "rootKey"} {
		if ok, err := regexp.MatchString(foldedKeyPattern("rootKey"), key); err != nil || !ok {
			t.Errorf("decoder-compatible field spelling %q rejected: %v", key, err)
		}
	}
	if ok, _ := regexp.MatchString(foldedKeyPattern("rootKey"), "rootKeyTypo"); ok {
		t.Fatal("case-insensitive pattern must match whole property names")
	}
}

func TestInstanceSchemaRefusesUnsupportedShapes(t *testing.T) {
	b := schemaBuilder{defs: map[string]any{}}
	for _, typ := range []reflect.Type{
		reflect.TypeFor[json.RawMessage](), reflect.TypeFor[map[bool]string](),
		reflect.TypeFor[chan int](), reflect.TypeFor[struct{ Embedded string }](),
	} {
		if _, err := b.node(typ); err == nil {
			t.Errorf("generated a guessed schema for %s", typ)
		}
	}
}

func TestInstanceSchemaCannotBeModifiedByCaller(t *testing.T) {
	original := bytes.Clone(InstanceJSONSchema())
	copy := InstanceJSONSchema()
	copy[0] = '!'
	if !bytes.Equal(original, InstanceJSONSchema()) {
		t.Fatal("caller changed the build's embedded schema")
	}
}
