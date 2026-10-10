// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package wirecontract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const schema = `{
  "type": "object",
  "properties": {
    "token":   {"type": "string"},
    "count":   {"type": "integer"},
    "ok":      {"type": "boolean"},
    "at":      {"type": "string", "format": "date-time"},
    "body":    {},
    "entries": {"type": "array", "items": {"$ref": "#/$defs/entry"}}
  },
  "required": ["token", "ok"],
  "$defs": {
    "entry": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}
  }
}`

type entry struct {
	Name string `json:"name"`
}

type matching struct {
	Token   string           `json:"token"`
	Count   *uint32          `json:"count,omitempty"`
	OK      bool             `json:"ok"`
	At      *time.Time       `json:"at,omitempty"`
	Body    *json.RawMessage `json:"body,omitempty"`
	Entries []entry          `json:"entries,omitempty"`
	hidden  int
}

func mustCompare(t *testing.T, pointer string, v any) []string {
	t.Helper()
	diff, err := Compare([]byte(schema), pointer, v)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return diff
}

func TestMatchingStructHasNoDiff(t *testing.T) {
	if diff := mustCompare(t, "", matching{}); len(diff) != 0 {
		t.Fatalf("a struct that matches its schema reported drift:\n%s", strings.Join(diff, "\n"))
	}
	if diff := mustCompare(t, "/$defs/entry", entry{}); len(diff) != 0 {
		t.Fatalf("a $defs entry that matches reported drift:\n%s", strings.Join(diff, "\n"))
	}
}

// The negative controls. Each is one kind of drift the comparison exists to catch; a
// comparison that reported none of them would be decoration.

type renamed struct {
	Token   string           `json:"tokenId"` // renamed on the struct only
	Count   *uint32          `json:"count,omitempty"`
	OK      bool             `json:"ok"`
	At      *time.Time       `json:"at,omitempty"`
	Body    *json.RawMessage `json:"body,omitempty"`
	Entries []entry          `json:"entries,omitempty"`
}

type extra struct {
	matching
	Added string `json:"added,omitempty"`
}

type requiredness struct {
	Token   string           `json:"token,omitempty"` // optional on the struct, required in the schema
	Count   *uint32          `json:"count,omitempty"`
	OK      bool             `json:"ok"`
	At      *time.Time       `json:"at,omitempty"`
	Body    *json.RawMessage `json:"body,omitempty"`
	Entries []entry          `json:"entries,omitempty"`
}

type retyped struct {
	Token   string           `json:"token"`
	Count   *string          `json:"count,omitempty"` // a string on the struct, an integer in the schema
	OK      bool             `json:"ok"`
	At      *time.Time       `json:"at,omitempty"`
	Body    *json.RawMessage `json:"body,omitempty"`
	Entries []entry          `json:"entries,omitempty"`
}

func TestDriftIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want []string
	}{
		{"renamed field", renamed{}, []string{`"token" is in the schema but not on the struct`, `"tokenId" is on the struct but not in the schema`}},
		{"required flipped", requiredness{}, []string{`"token" is required=false on the struct`}},
		{"type changed", retyped{}, []string{`"count" is JSON type "string" on the struct but "integer" in the schema`}},
	} {
		diff := strings.Join(mustCompare(t, "", tc.v), "\n")
		for _, w := range tc.want {
			if !strings.Contains(diff, w) {
				t.Errorf("%s: want a line containing %q, got:\n%s", tc.name, w, diff)
			}
		}
	}
}

func TestEmbeddedStructIsRefusedNotFlattened(t *testing.T) {
	// encoding/json flattens an untagged embedded struct; this package does not model that
	// and must say so rather than report a struct with one unexplained member.
	if _, err := Compare([]byte(schema), "", extra{}); err == nil {
		t.Fatal("an embedded struct was accepted; its promoted fields would have been miscounted")
	}
}

type untagged struct {
	Token string
}

func TestUntaggedFieldIsAnError(t *testing.T) {
	_, err := Compare([]byte(schema), "", untagged{})
	if err == nil || !strings.Contains(err.Error(), "has no json tag") {
		t.Fatalf("want an untagged-field error, got %v", err)
	}
}

func TestSchemaRequiringAnUndeclaredMemberIsAnError(t *testing.T) {
	_, err := SchemaMembers([]byte(`{"type":"object","properties":{"a":{}},"required":["b"]}`), "")
	if err == nil {
		t.Fatal("a schema requiring a member it does not declare was accepted")
	}
}

func TestPointerThatResolvesNowhereIsAnError(t *testing.T) {
	if _, err := SchemaMembers([]byte(schema), "/$defs/missing"); err == nil {
		t.Fatal("a pointer to nothing was accepted")
	}
}
