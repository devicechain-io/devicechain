// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// The shed-letter budget is typed and fails closed: a negative, NaN or infinite value refuses to
// start the service, through the same strict load an operator's document goes through.
func TestShedLetterBudgetRejectsNegativeAndNonFinite(t *testing.T) {
	for _, doc := range []string{
		`{"shedLetterPerSecond":-1}`,
		`{"shedLetterBurst":-1}`,
		`{"shedLetterGlobalPerSecond":-0.5}`,
		`{"shedLetterGlobalBurst":-3}`,
	} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err == nil {
			t.Errorf("%s loaded; a negative budget must fail the load closed", doc)
		}
	}
	// JSON cannot spell NaN or Inf, so those reach Validate only from code; check them there.
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, cfg := range []EventProcessingConfiguration{{ShedLetterPerSecond: v}, {ShedLetterGlobalPerSecond: v}} {
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate accepted a rate of %v: %+v", v, cfg)
			}
		}
	}
}

// Unset keys take the documented defaults, and explicit ones are left alone.
func TestShedLetterBudgetDefaults(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	if err := core.LoadConfiguration([]byte(`{}`), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ShedLetterPerSecond != 1 || cfg.ShedLetterBurst != 60 ||
		cfg.ShedLetterGlobalPerSecond != 10 || cfg.ShedLetterGlobalBurst != 100 {
		t.Fatalf("defaults = %g/%d/%g/%d, want 1/60/10/100", cfg.ShedLetterPerSecond, cfg.ShedLetterBurst,
			cfg.ShedLetterGlobalPerSecond, cfg.ShedLetterGlobalBurst)
	}
	tuned := &EventProcessingConfiguration{}
	if err := core.LoadConfiguration([]byte(`{"shedLetterPerSecond":2.5,"shedLetterBurst":7,`+
		`"shedLetterGlobalPerSecond":4,"shedLetterGlobalBurst":9}`), tuned); err != nil {
		t.Fatal(err)
	}
	if tuned.ShedLetterPerSecond != 2.5 || tuned.ShedLetterBurst != 7 ||
		tuned.ShedLetterGlobalPerSecond != 4 || tuned.ShedLetterGlobalBurst != 9 {
		t.Fatalf("explicit values were overwritten: %+v", tuned)
	}
}
