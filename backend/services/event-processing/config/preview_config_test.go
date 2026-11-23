// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// Absent, the instance-wide preview cap is the documented default, through the real load path.
func TestPreviewMaxConcurrentDefault(t *testing.T) {
	for _, doc := range []string{``, `{}`, `{"preview":{}}`} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := cfg.Preview.MaxConcurrentCount(); got != DefaultPreviewMaxConcurrent {
			t.Fatalf("%q: cap %d, want the default %d", doc, got, DefaultPreviewMaxConcurrent)
		}
	}
	if DefaultPreviewMaxConcurrent != 8 {
		t.Fatalf("the default moved to %d; the documentation states 8", DefaultPreviewMaxConcurrent)
	}
}

func TestPreviewMaxConcurrentApplied(t *testing.T) {
	for doc, want := range map[string]int{
		`{"preview":{"maxConcurrent":1}}`:   1,
		`{"preview":{"maxConcurrent":20}}`:  20,
		`{"preview":{"maxConcurrent":256}}`: 256,
	} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		if got := cfg.Preview.MaxConcurrentCount(); got != want {
			t.Fatalf("%s: cap %d, want %d", doc, got, want)
		}
	}
}

// Out of range or mistyped: refused at load, naming the key. Zero is refused, not read as unset.
func TestPreviewMaxConcurrentRefusals(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{"preview":{"maxConcurrent":0}}`, "preview.maxConcurrent"},
		{`{"preview":{"maxConcurrent":-1}}`, "preview.maxConcurrent"},
		{`{"preview":{"maxConcurrent":257}}`, "preview.maxConcurrent"},
		{`{"preview":{"maxConcurrent":"many"}}`, "maxConcurrent"},
		{`{"preview":{"cap":8}}`, "cap"},
	} {
		err := core.LoadConfiguration([]byte(tc.doc), &EventProcessingConfiguration{})
		if err == nil {
			t.Errorf("%s: accepted", tc.doc)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.doc, err, tc.want)
		}
	}
}
