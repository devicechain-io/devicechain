// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// The console's reviewed alert presets (frontend rule-presets.ts) pre-fill the detection-rule form
// with these definitions. They are checked in as goldens so the two sides cannot drift: the
// console's test asserts each preset emits exactly this file, and this test asserts each file
// decodes strictly and compiles under the production limits, with the event-processing compiler —
// the same gate a publish runs. A preset that stopped compiling, or a compiler change that
// invalidated one, fails here rather than at an operator's publish.
func TestConsoleAlertPresetsCompile(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "presets", "*.json"))
	if err != nil || len(files) != 3 {
		t.Fatalf("want the 3 preset goldens, got %d (err=%v)", len(files), err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Decode(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			r.ID = "preset" // the gate assigns the rule's token as its id
			if _, err := Compile(r, Limits{}); err != nil {
				t.Fatalf("compile: %v", err)
			}
		})
	}
}
