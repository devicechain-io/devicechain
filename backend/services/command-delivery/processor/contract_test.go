// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/wirecontract"
)

// The command envelopes are published to device authors as JSON Schema files in
// ../contract, served on the docs site under /schema/device/. These tests hold the Go
// structs and those files to one contract, so a renamed, added or removed field fails
// here rather than in the field.

func readContract(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "contract", name))
	if err != nil {
		t.Fatalf("read contract %s: %v", name, err)
	}
	return b
}

func TestEnvelopesMatchTheirPublishedSchemas(t *testing.T) {
	for _, tc := range []struct {
		file string
		v    any
	}{
		{"command-delivery.schema.json", deliveryEnvelope{}},
		{"command-response.schema.json", responseEnvelope{}},
	} {
		diff, err := wirecontract.Compare(readContract(t, tc.file), "", tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if len(diff) != 0 {
			t.Errorf("%s has drifted from %T:\n  %s", tc.file, tc.v, strings.Join(diff, "\n  "))
		}
	}
}

// schemaDoc is the part of a contract file these tests read beyond its members.
type schemaDoc struct {
	ID       string            `json:"$id"`
	Examples []json.RawMessage `json:"examples"`
}

func TestContractFilesAreIdentifiedByWhereTheyArePublished(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "contract"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		var doc schemaDoc
		if err := json.Unmarshal(readContract(t, e.Name()), &doc); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if want := "https://docs.devicechain.io/schema/device/" + e.Name(); doc.ID != want {
			t.Errorf("%s: $id is %q, want %q", e.Name(), doc.ID, want)
		}
	}
	// By name: a new file here is a new published contract, and it needs a struct test
	// above before it is one.
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "command-delivery.schema.json,command-response.schema.json"; got != want {
		t.Errorf("contract files are %s, want %s", got, want)
	}
}

// Every example a device author copies decodes into the struct the platform reads it
// with, member for member.
func TestResponseExamplesDecodeStrictly(t *testing.T) {
	var doc schemaDoc
	if err := json.Unmarshal(readContract(t, "command-response.schema.json"), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Examples) == 0 {
		t.Fatal("the response schema carries no examples")
	}
	for i, ex := range doc.Examples {
		dec := json.NewDecoder(bytes.NewReader(ex))
		dec.DisallowUnknownFields()
		var r responseEnvelope
		if err := dec.Decode(&r); err != nil {
			t.Errorf("example %d does not decode as a response: %v", i, err)
			continue
		}
		if r.CommandToken == "" || r.DispatchNonce == "" {
			t.Errorf("example %d omits a member the platform requires: %+v", i, r)
		}
	}
}

// The page and the schema both say a response payload is a STRING, and that an object
// there makes the whole message undecodable — which handleResponse discards. This pins the
// decode half of that claim to the struct.
func TestResponseObjectPayloadIsUndecodable(t *testing.T) {
	var r responseEnvelope
	err := json.Unmarshal([]byte(`{"commandToken":"c","dispatchNonce":"n","success":true,"payload":{"ok":true}}`), &r)
	if err == nil {
		t.Fatal("an object payload decoded; the published contract says it does not")
	}
}

// A delivery the platform actually builds carries exactly the members the schema
// declares, and every one it requires.
func TestDeliveryAsPublishedCarriesTheRequiredMembers(t *testing.T) {
	raw := json.RawMessage(`{"delaySeconds":5}`)
	for _, env := range []deliveryEnvelope{
		{Token: "t", DeviceToken: "d", Name: "reboot", DispatchNonce: "n", Payload: &raw},
		{Token: "t", DeviceToken: "d", Name: "reboot", DispatchNonce: "n"},
	} {
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		members, err := wirecontract.SchemaMembers(readContract(t, "command-delivery.schema.json"), "")
		if err != nil {
			t.Fatal(err)
		}
		for name, m := range members {
			if _, ok := got[name]; m.Required && !ok {
				t.Errorf("%s: required member %q is missing from %s", "delivery", name, b)
			}
		}
		for name := range got {
			if _, ok := members[name]; !ok {
				t.Errorf("delivery carries %q, which the schema does not declare", name)
			}
		}
	}
}
