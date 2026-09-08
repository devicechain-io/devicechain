// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

// EVERY DOCUMENT THIS PACKAGE SENDS IS A STRING, AND NOTHING HERE READ ONE.
//
// The compiler does not read them, the unit tests drive the fake server rather than a
// schema, and the schema they must satisfy lives in another module — so until this
// file the only thing that ever read a `sim` document was a real cluster, at the far
// end of a manual gate.
//
// That is the same hole the loadtest package closed for itself
// (loadtest/documents_test.go), after v0.12.0 shipped an upgrade drill whose
// provisioning documents were invalid against every schema this repository has ever
// served — with a mutation harness scoring it 29/29, because a mutation harness
// measures the logic AROUND a string and never the string.
//
// 🔴 WHAT THIS FILE COVERS IS NARROWER THAN THAT ONE, DELIBERATELY AND FOR NOW: the
// three authoring UPDATE documents, which are the ones the partial-update conversion
// rewrote. loadtest's version scans the whole package and enforces its own coverage
// floor; doing the same here needs the variable plumbing that makes each document's
// real KEY SET the thing under test, which is a piece of work of its own. Named so
// the next person extending it knows what is and is not being asked.
//
// The SDL lives OUTSIDE this module and Go's test cache does not track it, so a plain
// `go test` can serve a stale PASS over an edited schema. CI runs every module's
// tests with `-count=1`, and so does the sweep in CLAUDE.md.

// servedSchema parses what device-management serves on its TENANT plane, with the
// same options the service parses it with — MaxDepth bites at VALIDATE time, so a
// validator without it is weaker than the server it stands in for.
func servedSchema(t *testing.T, area string) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "services", area, "graphql")
	var sdl strings.Builder
	for _, ext := range []string{"*.graphql", "*.gql"} {
		found, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			t.Fatalf("glob %s/%s: %v", dir, ext, err)
		}
		for _, f := range found {
			if strings.Contains(filepath.Base(f), "admin") {
				continue
			}
			b, rerr := os.ReadFile(f)
			if rerr != nil {
				t.Fatalf("read %s: %v", f, rerr)
			}
			sdl.Write(b)
			sdl.WriteString("\n")
		}
	}
	// A test that parsed nothing would validate everything, which is the shape of gate
	// this file exists to refuse.
	if sdl.Len() == 0 {
		t.Fatalf("no tenant-plane schema files found for area %q under %s", area, dir)
	}
	// A nil resolver is enough: validation reads the schema, never a resolver.
	schema, err := graphql.ParseSchema(sdl.String(), nil,
		graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse %s: %v", area, err)
	}
	return schema
}

// The three authoring updates, validated with the variables ensure* really sends.
//
// The variables are not decoration. They pin the KEY SET, which is the one thing the
// validator genuinely reads about them — this repo runs a patched graphql-go
// precisely because upstream DISCARDS an input-object entry the schema does not
// define when it arrives through a variable. Each request goes through
// asUpdateRequest for the same reason the real call site does, so what is validated
// is the map the server would receive rather than a hand-written approximation of it.
func TestTheAuthoringUpdateDocumentsValidateAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t, "device-management")
	cases := []struct {
		name string
		doc  string
		vars map[string]any
	}{
		{"updateMetricDefinition", mutationUpdateMetricDefinition, map[string]any{
			"token": "prof-temperature",
			"request": asUpdateRequest(map[string]any{
				"token":              "prof-temperature",
				"deviceProfileToken": "prof",
				"metricKey":          "temperature",
				"name":               "Temperature",
				"dataType":           "DOUBLE",
				"unit":               "Cel",
			}),
		}},
		{"updateCommandDefinition", mutationUpdateCommandDefinition, map[string]any{
			"token": "cmd-1",
			"request": asUpdateRequest(map[string]any{
				"token":              "cmd-1",
				"deviceProfileToken": "prof",
				"commandKey":         "setSetpoint",
				"name":               "Set setpoint",
				"parameterSchema":    `[{"name":"v","dataType":"DOUBLE"}]`,
			}),
		}},
		{"updateDetectionRule", mutationUpdateDetectionRule, map[string]any{
			"token": "rule-1",
			"request": asUpdateRequest(map[string]any{
				"token":              "rule-1",
				"deviceProfileToken": "prof",
				"name":               "Over temperature",
				"definition":         `{"type":"threshold"}`,
				"enabled":            true,
			}),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if errs := schema.ValidateWithVariables(c.doc, c.vars); len(errs) > 0 {
				t.Fatalf("%s does not validate against the served schema: %v", c.name, errs)
			}
		})
	}

	// 🔴 THE NEGATIVE CONTROL, and the three cases above are worth nothing without it.
	//
	// 🔴 IT ASSERTS THE MESSAGE, NOT MERELY THAT AN ERROR CAME BACK, and the first
	// version of this control did the latter — which made it the very fail-open it was
	// written to guard against. Swapping ValidateWithVariables for Validate leaves the
	// document rejected with `Variable "request" has invalid value null`: an error, so
	// `len(errs) != 0` is satisfied, produced by a validator that never looked at the
	// payload at all. The control's own comment claimed to catch that and did not.
	//
	// So it names what the rejection must be ABOUT. A payload token is the defect
	// asUpdateRequest exists to prevent, the unknown-input-field guard is what refuses
	// it, and its message names the offending field and the type that does not declare
	// it. Anything else — a null variable, a syntax error, a missing argument — fails
	// here rather than passing as a rejection that happens to be for another reason.
	t.Run("control/a payload token is refused, and refused FOR BEING A TOKEN", func(t *testing.T) {
		errs := schema.ValidateWithVariables(mutationUpdateMetricDefinition, map[string]any{
			"token":   "prof-temperature",
			"request": map[string]any{"token": "moved", "metricKey": "temperature"},
		})
		if len(errs) == 0 {
			t.Fatal("a payload token validated against a dedicated update input — the validator " +
				"is not reading what this test assumes, so the passes above mean nothing")
		}
		var joined strings.Builder
		for _, e := range errs {
			joined.WriteString(e.Error())
			joined.WriteString("; ")
		}
		for _, want := range []string{"token", "not defined by type"} {
			if !strings.Contains(joined.String(), want) {
				t.Errorf("the rejection does not mention %q, so the validator refused this "+
					"document for some reason other than the payload token — and a control that "+
					"accepts any rejection is not reading the payload either: %s",
					want, joined.String())
			}
		}
	})
}
