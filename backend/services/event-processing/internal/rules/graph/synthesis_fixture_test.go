// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// synthesisFixture is one pair from testdata/synthesis: a stored rule definition, and the canvas
// graph the console synthesizes from it when it opens that rule on the canvas.
type synthesisFixture struct {
	Definition json.RawMessage  `json:"definition"`
	Graph      CanvasDefinition `json:"graph"`
}

// TestConsoleSynthesisLowersBackToTheRule pins the half of the console canvas's open-time
// fidelity check that lives on this side of the wire.
//
// The console opens a rule the canvas did not author by synthesizing a graph from its definition
// (frontend canvas/roundtrip.ts), compiles that graph here, and allows a canvas Save only if the
// result still carries everything the stored definition says. If this lowering dropped or changed
// a field of a synthesized graph, every rule with that field would open locked for no reason — or,
// had the check been skipped, be silently rewritten on save. Each fixture's graph is asserted, on
// the console side, to be EXACTLY what the synthesis produces for its definition
// (roundtrip.test.ts reads this same directory); here it is asserted to lower back to that
// definition. Together the two halves pin the round trip end to end, including the paths most
// likely to diverge: a guard carried by a branch, an outbound action with headers, an alarm-key
// template, a duration spelled non-canonically, and a leaf-less connectivity rule.
func TestConsoleSynthesisLowersBackToTheRule(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "synthesis", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The control: a glob that matched nothing would pass every case below by having none.
	if len(paths) < 4 {
		t.Fatalf("found %d synthesis fixtures, want at least 4", len(paths))
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var fx synthesisFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			want, err := rules.Decode(fx.Definition)
			if err != nil {
				t.Fatalf("rules.Decode(definition): %v", err)
			}
			got := compileOne(t, fx.Graph)
			if !reflect.DeepEqual(got.Rule, want) {
				t.Fatalf("the synthesized graph does not lower back to its rule\n got: %+v\nwant: %+v", got.Rule, want)
			}
		})
	}
}
