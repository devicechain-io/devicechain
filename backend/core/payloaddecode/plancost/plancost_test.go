// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package plancost_test

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
)

// The price ingest charges and the price the plan is held to are one formula: a plan's CostFor
// equals its shape priced by the same sizes, for each combination of inputs it reads.
func TestAShapePricesAnUplinkAsItsPlanDoes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		composed              uint64
		parsesJSON, parsesDec bool
	}{
		{"frame only", 120, false, false},
		{"json", 120, true, false},
		{"decoded", 120, false, true},
		{"both", 120, true, true},
	} {
		s := plancost.Shape{Composed: tc.composed, ParsesJSON: tc.parsesJSON, ParsesDecoded: tc.parsesDec}
		want := tc.composed
		if tc.parsesJSON {
			want += 300
		}
		if tc.parsesDec {
			want += 70
		}
		if got := s.For(300, 70); got != want {
			t.Errorf("%s: got %d want %d", tc.name, got, want)
		}
	}
	if plancost.ParseCost(-1) != 0 || plancost.ParseCost(5) != payloaddecode.ParseCost(5) {
		t.Error("ParseCost differs between the two packages")
	}
}

// The caps are a contract between ingest (which refuses before it charges) and the decoder (which
// enforces them again): a change to any of them moves what an accepted uplink may carry on one
// side of a rolling upgrade and not the other. The decoder's constants are ALIASES of these, so
// comparing the two proves nothing; the values are pinned as literals, in both packages and in the
// codec, and a deliberate change edits them here with its release note.
func TestIngestCapsAreTheDecodersCaps(t *testing.T) {
	const frame, jsonBytes, depth = 4096, 64 << 10, 32
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"plancost.MaxFrameBytes", plancost.MaxFrameBytes, frame},
		{"plancost.MaxJSONBytes", plancost.MaxJSONBytes, jsonBytes},
		{"plancost.MaxJSONDepth", plancost.MaxJSONDepth, depth},
		{"payloaddecode.MaxFrameBytes", payloaddecode.MaxFrameBytes, frame},
		{"payloaddecode.MaxJSONBytes", payloaddecode.MaxJSONBytes, jsonBytes},
		{"payloaddecode.MaxJSONDepth", payloaddecode.MaxJSONDepth, depth},
		{"integrationcodec.MaxDepth", integrationcodec.MaxDepth, depth},
		{"integrationcodec.MaxDecodedBytes", integrationcodec.MaxDecodedBytes, jsonBytes},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d: the ingest and decoder caps are a wire contract", c.name, c.got, c.want)
		}
	}
}

// ParseCost is one unit a byte, and that rate is a wire contract: event-sources prices an uplink
// with it before the 202, and device-management's plan prices the same parse with the same
// function. A change to the rate must carry the old rate with it (an environment version, or a
// field on the plan), or a mixed-version rollout charges and meters differently.
func TestParseCostIsOneUnitAByte(t *testing.T) {
	for _, n := range []int{0, 1, 100, plancost.MaxJSONBytes} {
		if got := plancost.ParseCost(n); got != uint64(n) {
			t.Fatalf("ParseCost(%d) = %d, want %d", n, got, n)
		}
	}
}
