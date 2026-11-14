// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruleaction

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

func TestKindsConservativeScan(t *testing.T) {
	cases := []struct {
		name, raw    string
		want         []auth.Authority
		unknown, bad bool
	}{
		{"alarm", `{"actions":[{"type":"raiseAlarm"}]}`, nil, false, false},
		{"command", `{"actions":[{"type":"sendCommand"}]}`, []auth.Authority{auth.CommandWrite}, false, false},
		{"http", `{"actions":[{"type":"httpCall"}]}`, []auth.Authority{auth.ConnectorWrite}, false, false},
		{"publish", `{"actions":[{"type":"publish"}]}`, []auth.Authority{auth.ConnectorWrite}, false, false},
		{"duplicateActions", `{"actions":[{"type":"sendCommand"}],"actions":[{"type":"raiseAlarm"}]}`, []auth.Authority{auth.CommandWrite}, false, false},
		{"duplicateTypes", `{"actions":[{"type":"sendCommand","TYPE":"httpCall","type":"raiseAlarm"}]}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, false, false},
		{"mismatch", `{"actions":[{"type":"raiseAlarm","sendCommand":{}}]}`, []auth.Authority{auth.CommandWrite}, false, false},
		{"blocks", `{"actions":[{"type":"raiseAlarm","HTTPCall":{},"PUBLI\u017fH":{}}]}`, []auth.Authority{auth.ConnectorWrite}, false, false},
		{"nullBlocks", `{"actions":[{"type":"raiseAlarm","sendCommand":null,"httpCall":null,"publish":null}]}`, nil, false, false},
		{"unknown", `{"actions":[{"type":"future"}]}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, false},
		{"missingType", `{"actions":[{}]}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, false},
		{"invalidType", `{"actions":[{"type":{}}]}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, false},
		{"malformed", `{"actions":`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, true},
		{"trailing", `{} {}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, true},
		{"nonarray", `{"actions":{}}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, true},
		{"null", `{"actions":null}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, true},
		{"nonobjectAction", `{"actions":[null]}`, []auth.Authority{auth.CommandWrite, auth.ConnectorWrite}, true, true},
		{"noActions", `{}`, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Kinds([]byte(c.raw))
			if (err != nil) != c.bad || got.Unknown != c.unknown || !reflect.DeepEqual(Required(got), c.want) {
				t.Fatalf("Kinds = %+v, %v; required = %v, want %v", got, err, Required(got), c.want)
			}
		})
	}
}

func TestKindsMatchesEncodingJSONKeyFolding(t *testing.T) {
	for _, key := range []string{"actions", "Actions", "ACTIONS", "action\u017f", "ACTION\u017f", "act\u0131ons", "act\u212aons"} {
		raw := []byte(`{"` + key + `":[{"TyPe":"sendCommand"}]}`)
		var engine struct {
			Actions []struct {
				Type string `json:"type"`
			} `json:"actions"`
		}
		if err := json.Unmarshal(raw, &engine); err != nil {
			t.Fatal(err)
		}
		set, err := Kinds(raw)
		if err != nil {
			t.Fatal(err)
		}
		if set.Types["sendCommand"] != (len(engine.Actions) > 0) {
			t.Fatalf("key %q scan disagrees with encoding/json", key)
		}
	}
	// Kelvin sign has no equivalent in actions/type/block names, but pin the
	// folding equivalence itself so a future key containing k remains covered.
	var probe struct {
		K string `json:"k"`
	}
	if err := json.Unmarshal([]byte(`{"\u212a":"accepted"}`), &probe); err != nil || probe.K != "accepted" {
		t.Fatalf("Kelvin fold: %+v %v", probe, err)
	}
}

func TestKindsUnionsPastInvalidShapes(t *testing.T) {
	for _, raw := range []string{
		`{"actions":null,"ACTIONS":[{"type":"sendCommand"}]}`,
		`{"actions":[null,{"type":"sendCommand"}]}`,
	} {
		set, err := Kinds([]byte(raw))
		if err == nil || !set.Unknown || !set.Types["sendCommand"] {
			t.Fatalf("scan must retain every type and fail closed: %+v %v", set, err)
		}
	}
}

func TestKindsFoldedAndDuplicateBlocks(t *testing.T) {
	for _, raw := range []string{
		`{"action\u017f":[{"TYPE":"raiseAlarm","\u017fENDCOMMAND":{}}]}`,
		`{"actions":[{"type":"raiseAlarm","sendCommand":{},"sendCommand":null}]}`,
	} {
		set, err := Kinds([]byte(raw))
		if err != nil || set.Unknown || !reflect.DeepEqual(Required(set), []auth.Authority{auth.CommandWrite}) {
			t.Fatalf("must count every folded non-null block: %+v %v", set, err)
		}
	}
}
