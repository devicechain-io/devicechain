// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/wirecontract"
)

// The inbound device event and its payloads are published to device authors as JSON
// Schema files in ../contract, served on the docs site under /schema/device/. These tests
// hold the decoder's structs, its event-type switch and those files to one contract, so a
// renamed, added or removed field fails here rather than in the field.

func readContract(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "contract", name))
	if err != nil {
		t.Fatalf("read contract %s: %v", name, err)
	}
	return b
}

// payloadContracts maps each device-acceptable eventType to its payload schema and the
// structs the decoder reads it into (by JSON pointer into the schema).
var payloadContracts = map[string]struct {
	file    string
	structs map[string]any
}{
	model.Measurement.String(): {"measurement-payload.schema.json", map[string]any{
		"": model.UnresolvedMeasurementsPayload{}, "/$defs/entry": model.UnresolvedMeasurementsEntry{}}},
	model.Location.String(): {"location-payload.schema.json", map[string]any{
		"": model.UnresolvedLocationsPayload{}, "/$defs/entry": model.UnresolvedLocationEntry{}}},
	model.Alert.String(): {"alert-payload.schema.json", map[string]any{
		"": model.UnresolvedAlertsPayload{}, "/$defs/entry": model.UnresolvedAlertEntry{}}},
	model.NewRelationship.String(): {"new-relationship-payload.schema.json", map[string]any{
		"": model.UnresolvedNewRelationshipPayload{}}},
}

func TestStructsMatchTheirPublishedSchemas(t *testing.T) {
	check := func(file, pointer string, v any) {
		diff, err := wirecontract.Compare(readContract(t, file), pointer, v)
		if err != nil {
			t.Fatalf("%s#%s: %v", file, pointer, err)
		}
		if len(diff) != 0 {
			t.Errorf("%s#%s has drifted from %T:\n  %s", file, pointer, v, strings.Join(diff, "\n  "))
		}
	}
	check("device-event.schema.json", "", JsonEvent{})
	for _, pc := range payloadContracts {
		for pointer, v := range pc.structs {
			check(pc.file, pointer, v)
		}
	}
}

// envelopeSchema is the part of the envelope schema these tests read beyond its members.
type envelopeSchema struct {
	ID         string `json:"$id"`
	Properties struct {
		EventType struct {
			Enum []string `json:"enum"`
		} `json:"eventType"`
	} `json:"properties"`
	AllOf []struct {
		If struct {
			Properties struct {
				EventType struct {
					Const string `json:"const"`
				} `json:"eventType"`
			} `json:"properties"`
		} `json:"if"`
		Then struct {
			Properties struct {
				Payload struct {
					Ref string `json:"$ref"`
				} `json:"payload"`
			} `json:"properties"`
		} `json:"then"`
	} `json:"allOf"`
	Examples []json.RawMessage `json:"examples"`
}

func loadEnvelopeSchema(t *testing.T) envelopeSchema {
	t.Helper()
	var s envelopeSchema
	if err := json.Unmarshal(readContract(t, "device-event.schema.json"), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The published eventType enum is EXACTLY the set the decoder accepts from a device, and
// each one routes to the payload schema this file's map pairs with its structs.
func TestEventTypeEnumIsWhatTheDecoderAccepts(t *testing.T) {
	s := loadEnvelopeSchema(t)

	published := map[string]bool{}
	for _, name := range s.Properties.EventType.Enum {
		published[name] = true
	}
	routed := map[string]string{}
	for _, branch := range s.AllOf {
		routed[branch.If.Properties.EventType.Const] = branch.Then.Properties.Payload.Ref
	}

	for name := range model.EventTypesByName {
		_, err := decodeType(name, minimalPayload(name))
		accepted := err == nil
		if accepted != published[name] {
			t.Errorf("eventType %q: decoder accepts=%t, schema publishes=%t (err %v)", name, accepted, published[name], err)
		}
		if !published[name] {
			continue
		}
		pc, ok := payloadContracts[name]
		if !ok {
			t.Errorf("eventType %q is published with no payload contract in this test", name)
			continue
		}
		if routed[name] != pc.file {
			t.Errorf("eventType %q routes its payload to %q in the schema, want %q", name, routed[name], pc.file)
		}
	}
	if len(routed) != len(published) {
		t.Errorf("the schema routes %d eventTypes but publishes %d", len(routed), len(published))
	}
}

// minimalPayload is the smallest payload the decoder accepts for an event type.
func minimalPayload(eventType string) string {
	switch eventType {
	case model.Measurement.String():
		return `{"entries":[{"measurements":{"t":"1"}}]}`
	case model.Location.String():
		return `{"entries":[{"latitude":"1","longitude":"2"}]}`
	case model.Alert.String():
		return `{"entries":[{"type":"x"}]}`
	default:
		return `{"relationshipType":"r","targetType":"device","target":"d"}`
	}
}

func decodeType(eventType, payload string) (interface{}, error) {
	body := fmt.Sprintf(`{"device":"d","eventType":%q,"payload":%s}`, eventType, payload)
	_, built, err := NewJsonDecoder(nil).Decode([]byte(body), time.Time{})
	return built, err
}

// Every example a device author copies out of a published schema is accepted by the
// decoder: the envelope examples as they stand, each payload example inside an envelope of
// its type.
func TestPublishedExamplesDecode(t *testing.T) {
	s := loadEnvelopeSchema(t)
	if len(s.Examples) == 0 {
		t.Fatal("the envelope schema carries no examples")
	}
	for i, ex := range s.Examples {
		if _, _, err := NewJsonDecoder(nil).Decode(ex, time.Time{}); err != nil {
			t.Errorf("envelope example %d is refused: %v", i, err)
		}
	}
	for eventType, pc := range payloadContracts {
		var doc struct {
			Examples []json.RawMessage `json:"examples"`
		}
		if err := json.Unmarshal(readContract(t, pc.file), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Examples) == 0 {
			t.Errorf("%s carries no examples", pc.file)
		}
		for i, ex := range doc.Examples {
			if _, err := decodeType(eventType, string(ex)); err != nil {
				t.Errorf("%s example %d is refused: %v", pc.file, i, err)
			}
		}
	}
}

// NewRelationship is the one payload the decoder reads out of a map by literal key rather
// than through its struct's tags, so the struct comparison alone cannot see a key renamed
// in the decoder. This reads every schema property back out of the built payload.
func TestNewRelationshipKeysAreTheOnesTheDecoderReads(t *testing.T) {
	members, err := wirecontract.SchemaMembers(readContract(t, "new-relationship-payload.schema.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]string{}
	for name := range members {
		payload[name] = "value-of-" + name
	}
	raw, _ := json.Marshal(payload)
	built, err := decodeType(model.NewRelationship.String(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(built)
	var back map[string]string
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, payload) {
		t.Errorf("the decoder did not read every published key into its field:\n sent %v\n  got %v", payload, back)
	}
}

func TestContractFilesAreIdentifiedByWhereTheyArePublished(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "contract"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		var doc struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(readContract(t, e.Name()), &doc); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if want := "https://docs.devicechain.io/schema/device/" + e.Name(); doc.ID != want {
			t.Errorf("%s: $id is %q, want %q", e.Name(), doc.ID, want)
		}
	}
	// By name: a new file here is a new published contract, and it needs a struct test
	// above before it is one.
	want := []string{"device-event.schema.json"}
	for _, pc := range payloadContracts {
		want = append(want, pc.file)
	}
	sort.Strings(names)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("contract files are %v, want %v", names, want)
	}
}
