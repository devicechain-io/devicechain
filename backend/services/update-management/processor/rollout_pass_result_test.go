// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
)

// A rollout pass whose steps fail must not read as a complete pass: AdvanceRollouts returns a nil
// error after individual step failures and reports them only in its tally.
func TestRolloutPassResultClassifiesStepFailures(t *testing.T) {
	cases := []struct {
		name    string
		res     model.AdvanceResult
		partial bool
		failed  bool
	}{
		{name: "nothing running", res: model.AdvanceResult{}},
		{name: "all stepped", res: model.AdvanceResult{Stepped: 3, Dispatched: 2}},
		{name: "one of three failed", res: model.AdvanceResult{Stepped: 3, Failed: 1}, partial: true},
		{name: "the only step failed", res: model.AdvanceResult{Stepped: 1, Failed: 1}, partial: true},
		{name: "every step failed", res: model.AdvanceResult{Stepped: 2, Failed: 2}, failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rolloutPassResult(tc.res)
			switch {
			case tc.partial:
				if !errors.Is(err, core.ErrPassPartial) {
					t.Fatalf("want a partial pass, got %v", err)
				}
			case tc.failed:
				if err == nil || errors.Is(err, core.ErrPassPartial) {
					t.Fatalf("want a failed pass, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("want a complete pass, got %v", err)
				}
			}
		})
	}
}
