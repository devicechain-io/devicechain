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

// The page and the schema say a response payload is ANY JSON value: a string is kept as its
// text, every other value as its own JSON text, and absent or null is no payload. This pins
// the decode half of that claim to the struct, and the schema half to the same file the docs
// site serves: the payload member declares no type, and an object example is published.
func TestResponsePayloadMayBeAnyJSONValue(t *testing.T) {
	str := func(s string) *string { return &s }
	for name, tc := range map[string]struct {
		payload string
		want    *string
	}{
		"object": {`{"ok":true,"n":[1,2]}`, str(`{"ok":true,"n":[1,2]}`)},
		"array":  {`[1,"two",null]`, str(`[1,"two",null]`)},
		"number": {`42.5`, str(`42.5`)},
		"bool":   {`false`, str(`false`)},
		"string": {`"rebooting in 5s"`, str("rebooting in 5s")},
		"null":   {`null`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var r responseEnvelope
			body := `{"commandToken":"c","dispatchNonce":"n","success":true,"payload":` + tc.payload + `}`
			if err := json.Unmarshal([]byte(body), &r); err != nil {
				t.Fatalf("a %s payload did not decode; the published contract says any JSON value does: %v", name, err)
			}
			got := responsePayloadText(r.Payload)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("payload = %q, want none", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("payload = %v, want %q", got, *tc.want)
			}
		})
	}

	// The schema says the same: payload is not required and declares no JSON type.
	var doc struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(readContract(t, "command-response.schema.json"), &doc); err != nil {
		t.Fatal(err)
	}
	payload, ok := doc.Properties["payload"]
	if !ok {
		t.Fatal("the response schema declares no payload member")
	}
	if typ, typed := payload["type"]; typed {
		t.Errorf("the response schema types payload as %v; it admits any JSON value", typ)
	}
}

// The published examples include a non-string payload, so a device author who copies the
// object form is copying something the decoder is known to accept.
func TestResponseExamplesIncludeAStructuredPayload(t *testing.T) {
	var doc schemaDoc
	if err := json.Unmarshal(readContract(t, "command-response.schema.json"), &doc); err != nil {
		t.Fatal(err)
	}
	for _, ex := range doc.Examples {
		var r responseEnvelope
		if err := json.Unmarshal(ex, &r); err != nil {
			t.Fatal(err)
		}
		if r.Payload != nil && bytes.HasPrefix(bytes.TrimSpace(*r.Payload), []byte("{")) {
			return
		}
	}
	t.Error("no published response example carries an object payload")
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
