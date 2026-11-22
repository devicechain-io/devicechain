// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
)

func TestFleetOperationFoundationDecision(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kinds        []string
		trigger      bool
		tag, version string
		witness      error
		red          bool
	}{
		{name: "inert"},
		{name: "kind without witness", kinds: []string{"fake"}, tag: "v1.8.0", witness: errors.New("missing file"), red: true},
		{name: "missing tag", kinds: []string{"fake"}, red: true},
		{name: "trigger missing tag", trigger: true, red: true},
		{name: "patch", trigger: true, tag: "v1.9.0", version: "v1.9.1", red: true},
		{name: "minor", kinds: []string{"fake"}, tag: "v1.9.0", version: "v1.10.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := checkFleetOperationFoundation(tc.kinds, tc.trigger, tc.version, func() (string, error) { called = true; return tc.tag, nil }, func(string) error { return tc.witness })
			if (err != nil) != tc.red {
				t.Fatalf("got %v, red=%v", err, tc.red)
			}
			if tc.name == "inert" && called {
				t.Fatal("inert foundation resolved a baseline")
			}
		})
	}
}
