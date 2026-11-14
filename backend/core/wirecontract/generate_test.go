// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package wirecontract

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenerateAuthoringShape(t *testing.T) {
	type auth struct {
		User string `json:"user"`
	}
	type config struct {
		Auth    *auth             `json:"auth,omitempty"`
		URLs    []string          `json:"urls"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	s, err := Generate(reflect.TypeFor[config](), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal(s)
	}
	if s["additionalProperties"] != false {
		t.Fatal("unknown config fields must be rejected")
	}
	if !reflect.DeepEqual(s["required"], []string{"urls"}) {
		t.Fatal(s["required"])
	}
	p := s["properties"].(map[string]any)
	a := p["auth"].(map[string]any)
	if a["additionalProperties"] != false || !reflect.DeepEqual(a["required"], []string{"user"}) {
		t.Fatal(a)
	}
	if p["urls"].(map[string]any)["items"].(map[string]any)["type"] != "string" {
		t.Fatal(p["urls"])
	}
	if p["headers"].(map[string]any)["additionalProperties"].(map[string]any)["type"] != "string" {
		t.Fatal(p["headers"])
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRefusesUnmodelledTypes(t *testing.T) {
	type recursive struct {
		Next *recursive `json:"next,omitempty"`
	}
	type untagged struct{ Name string }
	for _, typ := range []reflect.Type{reflect.TypeFor[recursive](), reflect.TypeFor[untagged](), reflect.TypeFor[json.RawMessage](), reflect.TypeFor[map[int]string](), reflect.TypeFor[chan int]()} {
		if _, err := Generate(typ, nil); err == nil {
			t.Errorf("published a guessed schema for %s", typ)
		}
	}
}

func TestGenerateEmbeddedConfig(t *testing.T) {
	type common struct {
		Region string `json:"region"`
	}
	type config struct {
		common
		Topic string `json:"topic"`
	}
	s, err := Generate(reflect.TypeFor[config](), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := s["properties"].(map[string]any)
	if len(p) != 2 || p["region"] == nil || p["topic"] == nil {
		t.Fatal("embedded fields must retain their JSON names", p)
	}
	if !reflect.DeepEqual(s["required"], []string{"region", "topic"}) {
		t.Fatal(s["required"])
	}
}
