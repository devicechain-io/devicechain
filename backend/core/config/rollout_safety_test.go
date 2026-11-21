// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRolloutSafetyInstanceKeyIsStrictAndDefaultsOff(t *testing.T) {
	for _, doc := range []string{`{}`, `{"infrastructure":{"updateManagement":{"rolloutSafety":false}}}`, `{"infrastructure":{"updateManagement":{"rolloutSafety":true}}}`} {
		c := &InstanceConfiguration{}
		dec := json.NewDecoder(bytes.NewBufferString(doc))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c); err != nil {
			t.Fatalf("decode %s: %v", doc, err)
		}
		c.ApplyDefaults()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		infra := got["Infrastructure"].(map[string]any)
		updates, ok := infra["UpdateManagement"].(map[string]any)
		if !ok {
			t.Fatal("missing fixed instance updateManagement configuration")
		}
		if updates["RolloutSafety"] != (doc == `{"infrastructure":{"updateManagement":{"rolloutSafety":true}}}`) {
			t.Fatalf("wrong opt-in after defaults: %v", updates)
		}
	}
	dec := json.NewDecoder(bytes.NewBufferString(`{"infrastructure":{"updateManagement":{"rolloutSaftey":true}}}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&InstanceConfiguration{}); err == nil {
		t.Fatal("typo accepted")
	}
	dec = json.NewDecoder(bytes.NewBufferString(`{"infrastructure":{"updateManagement":{"rolloutSafety":"true"}}}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&InstanceConfiguration{}); err == nil {
		t.Fatal("non-boolean accepted")
	}
}
