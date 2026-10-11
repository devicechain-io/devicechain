// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package wirecontract

import (
	"encoding/json"
	"reflect"
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

// ---- Nested structure: items, additionalProperties and $ref are followed. ----

type renamedEntry struct {
	EntryName string `json:"entryName"`
}

type nestedRenamed struct {
	Token   string           `json:"token"`
	Count   *uint32          `json:"count,omitempty"`
	OK      bool             `json:"ok"`
	At      *time.Time       `json:"at,omitempty"`
	Body    *json.RawMessage `json:"body,omitempty"`
	Entries []renamedEntry   `json:"entries,omitempty"`
}

func TestNestedStructDriftIsReported(t *testing.T) {
	diff := strings.Join(mustCompare(t, "", nestedRenamed{}), "\n")
	if !strings.Contains(diff, `entries[]: "entryName" is on the struct but not in the schema`) {
		t.Fatalf("a field renamed inside an array element was not reported:\n%s", diff)
	}
}

func TestInlineItemsWithoutPropertiesIsNotVacuous(t *testing.T) {
	// The reviewer's case: entries.items replaced by a bare {"type":"object"}. That schema
	// says nothing about the element's members, so it cannot be passed as matching.
	inline := strings.Replace(schema, `"items": {"$ref": "#/$defs/entry"}`, `"items": {"type": "object"}`, 1)
	diff, err := Compare([]byte(inline), "", matching{})
	if err == nil && len(diff) == 0 {
		t.Fatal("an inline element schema with no properties passed against a struct element")
	}
}

func TestArrayWithoutItemsIsReported(t *testing.T) {
	bare := strings.Replace(schema, `, "items": {"$ref": "#/$defs/entry"}`, ``, 1)
	diff := strings.Join(mustCompareDoc(t, bare, matching{}), "\n")
	if !strings.Contains(diff, "declares no items") {
		t.Fatalf("an array schema with no items passed against a struct slice:\n%s", diff)
	}
}

const mapSchema = `{
  "type": "object",
  "properties": {"values": {"type": "object", "additionalProperties": {"type": "string"}}},
  "required": ["values"]
}`

type mapOfStrings struct {
	Values map[string]string `json:"values"`
}

type mapOfInts struct {
	Values map[string]int `json:"values"`
}

func TestMapValueTypesAreCompared(t *testing.T) {
	if diff := mustCompareDoc(t, mapSchema, mapOfStrings{}); len(diff) != 0 {
		t.Fatalf("matching map reported drift: %v", diff)
	}
	diff := strings.Join(mustCompareDoc(t, mapSchema, mapOfInts{}), "\n")
	if !strings.Contains(diff, `values{}: elements are JSON type "integer" on the struct but "string" in the schema`) {
		t.Fatalf("a map value type change was not reported:\n%s", diff)
	}
	noAP := strings.Replace(mapSchema, `, "additionalProperties": {"type": "string"}`, ``, 1)
	if diff := mustCompareDoc(t, noAP, mapOfStrings{}); len(diff) == 0 {
		t.Fatal("a typed map passed against a schema declaring no additionalProperties")
	}
}

// ---- Tag branches. ----

func TestDuplicateJSONNameIsAnError(t *testing.T) {
	// Built at run time: go vet refuses a literal struct that repeats a json tag, which is
	// exactly why this branch could otherwise go untested.
	dup := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(""), Tag: `json:"token"`},
		{Name: "B", Type: reflect.TypeOf(""), Tag: `json:"token,omitempty"`},
	})
	_, err := StructMembers(dup)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a duplicate-name error, got %v", err)
	}
}

type skipped struct {
	Token    string `json:"token"`
	Internal string `json:"-"`
}

func TestDashTaggedFieldIsNotOnTheWire(t *testing.T) {
	m, err := StructMembers(reflect.TypeOf(skipped{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf(`a json:"-" field was counted as a member: %v`, m)
	}
	if _, ok := m["Internal"]; ok {
		t.Fatal(`a json:"-" field appeared under its Go name`)
	}
}

func mustCompareDoc(t *testing.T, doc string, v any) []string {
	t.Helper()
	diff, err := Compare([]byte(doc), "", v)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return diff
}
