// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
)

// Absent, the publish window is the serial default, through the real load path.
func TestDerivedPublishingDefaults(t *testing.T) {
	for _, doc := range []string{``, `{}`, `{"derivedPublishing":{}}`} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := cfg.DerivedPublishing.Config(); got != runtime.DefaultOutputConfig() {
			t.Fatalf("%q: %+v, want the defaults %+v", doc, got, runtime.DefaultOutputConfig())
		}
	}
	want := runtime.OutputConfig{MaxInFlight: 32, MaxBufferedBytes: 4 << 20}
	if runtime.DefaultOutputConfig() != want {
		t.Fatalf("the defaults moved: %+v; the documentation states %+v", runtime.DefaultOutputConfig(), want)
	}
}

// Both fields, set, reach the processor's configuration.
func TestDerivedPublishingValuesAreApplied(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	if err := core.LoadConfiguration([]byte(`{"derivedPublishing":{"maxInFlight":32,"maxBufferedBytes":1048576}}`), cfg); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.DerivedPublishing.Config(), (runtime.OutputConfig{MaxInFlight: 32, MaxBufferedBytes: 1 << 20}); got != want {
		t.Fatalf("%+v, want %+v", got, want)
	}
}

// Zero, negative, excessive, mistyped and unknown values are refused at load, each naming its
// key. Zero is refused rather than read as "unset": an absent key is the way to ask for the default.
func TestDerivedPublishingRefusals(t *testing.T) {
	for _, tc := range []struct{ doc, key string }{
		{`{"derivedPublishing":{"maxInFlight":0}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxInFlight":-1}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxInFlight":1025}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxInFlight":9223372036854775807}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxInFlight":2.5}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxInFlight":"wide"}}`, "maxInFlight"},
		{`{"derivedPublishing":{"maxBufferedBytes":0}}`, "maxBufferedBytes"},
		{`{"derivedPublishing":{"maxBufferedBytes":65535}}`, "maxBufferedBytes"},
		{`{"derivedPublishing":{"maxBufferedBytes":268435457}}`, "maxBufferedBytes"},
		{`{"derivedPublishing":{"window":8}}`, "window"},
	} {
		err := core.LoadConfiguration([]byte(tc.doc), &EventProcessingConfiguration{})
		if err == nil {
			t.Errorf("%s: accepted", tc.doc)
			continue
		}
		if !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: the refusal does not name %s: %v", tc.doc, tc.key, err)
		}
	}
	for _, doc := range []string{
		`{"derivedPublishing":{"maxInFlight":1,"maxBufferedBytes":65536}}`,
		`{"derivedPublishing":{"maxInFlight":1024,"maxBufferedBytes":268435456}}`,
	} {
		if err := core.LoadConfiguration([]byte(doc), &EventProcessingConfiguration{}); err != nil {
			t.Errorf("%s: refused a bound: %v", doc, err)
		}
	}
}
