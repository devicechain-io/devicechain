// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-microservice/core"
)

// Absent, the rule-health bounds are the accumulator's defaults, through the real load path.
func TestRuleHealthDefaults(t *testing.T) {
	for _, doc := range []string{``, `{}`, `{"ruleHealth":{}}`} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := cfg.RuleHealth.Config(); got != rulehealth.DefaultConfig() {
			t.Fatalf("%q: %+v, want the defaults %+v", doc, got, rulehealth.DefaultConfig())
		}
	}
	want := rulehealth.Config{FlushInterval: time.Second, MaxPendingKeys: 4096, MaxPendingBytes: 4 << 20,
		MaxBatchRows: 256, WriteTimeout: 2 * time.Second}
	if rulehealth.DefaultConfig() != want {
		t.Fatalf("the defaults moved: %+v; the documentation states %+v", rulehealth.DefaultConfig(), want)
	}
}

// Every field, set, reaches the accumulator's configuration.
func TestRuleHealthValuesAreApplied(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	doc := `{"ruleHealth":{"flushIntervalMillis":250,"maxPendingKeys":100,"maxPendingBytes":1048576,` +
		`"maxBatchRows":50,"writeTimeoutMillis":750}}`
	if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
		t.Fatal(err)
	}
	want := rulehealth.Config{FlushInterval: 250 * time.Millisecond, MaxPendingKeys: 100, MaxPendingBytes: 1 << 20,
		MaxBatchRows: 50, WriteTimeout: 750 * time.Millisecond}
	if got := cfg.RuleHealth.Config(); got != want {
		t.Fatalf("%+v, want %+v", got, want)
	}
}

// Zero, negative, excessive and unknown values are refused at load, each naming its key. Zero is
// refused rather than read as "unset": an absent key is the way to ask for the default.
func TestRuleHealthRefusals(t *testing.T) {
	for _, tc := range []struct{ doc, key string }{
		{`{"ruleHealth":{"flushIntervalMillis":0}}`, "flushIntervalMillis"},
		{`{"ruleHealth":{"flushIntervalMillis":-1}}`, "flushIntervalMillis"},
		{`{"ruleHealth":{"flushIntervalMillis":49}}`, "flushIntervalMillis"},
		{`{"ruleHealth":{"flushIntervalMillis":60001}}`, "flushIntervalMillis"},
		{`{"ruleHealth":{"flushIntervalMillis":9223372036854775807}}`, "flushIntervalMillis"},
		{`{"ruleHealth":{"maxPendingKeys":0}}`, "maxPendingKeys"},
		{`{"ruleHealth":{"maxPendingKeys":-5}}`, "maxPendingKeys"},
		{`{"ruleHealth":{"maxPendingKeys":1048577}}`, "maxPendingKeys"},
		{`{"ruleHealth":{"maxPendingBytes":0}}`, "maxPendingBytes"},
		{`{"ruleHealth":{"maxPendingBytes":65535}}`, "maxPendingBytes"},
		{`{"ruleHealth":{"maxPendingBytes":268435457}}`, "maxPendingBytes"},
		{`{"ruleHealth":{"maxBatchRows":0}}`, "maxBatchRows"},
		{`{"ruleHealth":{"maxBatchRows":4097}}`, "maxBatchRows"},
		{`{"ruleHealth":{"writeTimeoutMillis":0}}`, "writeTimeoutMillis"},
		{`{"ruleHealth":{"writeTimeoutMillis":99}}`, "writeTimeoutMillis"},
		{`{"ruleHealth":{"writeTimeoutMillis":60001}}`, "writeTimeoutMillis"},
		{`{"ruleHealth":{"flushIntervalMs":1000}}`, "flushIntervalMs"},
		{`{"ruleHealth":{"maxPendingKeys":"many"}}`, "maxPendingKeys"},
		{`{"ruleHealth":{"maxBatchRows":1.5}}`, "maxBatchRows"},
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
	// The bounds themselves are accepted.
	for _, doc := range []string{
		`{"ruleHealth":{"flushIntervalMillis":50,"maxPendingKeys":1,"maxPendingBytes":65536,"maxBatchRows":1,"writeTimeoutMillis":100}}`,
		`{"ruleHealth":{"flushIntervalMillis":60000,"maxPendingKeys":1048576,"maxPendingBytes":268435456,"maxBatchRows":4096,"writeTimeoutMillis":60000}}`,
	} {
		if err := core.LoadConfiguration([]byte(doc), &EventProcessingConfiguration{}); err != nil {
			t.Errorf("%s: refused a bound: %v", doc, err)
		}
	}
}
