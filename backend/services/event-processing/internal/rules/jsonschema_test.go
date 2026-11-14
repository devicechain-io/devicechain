// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestJSONSchemaAuthoringContract(t *testing.T) {
	s, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	p := s["properties"].(map[string]any)
	for _, name := range []string{"window", "hold", "timeout", "gap"} {
		if p[name].(map[string]any)["type"] != "string" {
			t.Fatalf("%s must be a duration string", name)
		}
	}
	if !reflect.DeepEqual(s["required"], []string{"name", "type"}) {
		t.Fatal("leafless rules must be authorable", s["required"])
	}
	if len(p["type"].(map[string]any)["enum"].([]RuleType)) != 8 {
		t.Fatal("missing rule taxonomy")
	}
	actions := p["actions"].(map[string]any)
	if actions["maxItems"] != MaxActionsPerRule {
		t.Fatal("missing action bound")
	}
	a := actions["items"].(map[string]any)
	http := a["properties"].(map[string]any)["httpCall"].(map[string]any)
	if http["additionalProperties"] != false {
		t.Fatal("nested action must reject typos")
	}
	// Every serialized field of a real authored rule must be described.
	r := Rule{Name: "Hot", Type: TypeThreshold, When: Condition{Metric: "temperature", Op: OpGt, Threshold: new(float64)}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	for k := range obj {
		if _, ok := p[k]; !ok {
			t.Fatalf("missing field %s", k)
		}
	}
}
