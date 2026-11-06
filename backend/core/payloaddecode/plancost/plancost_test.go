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

// The caps ingest enforces are the caps the decoder enforces, and the codec's nesting limit is the
// one the decoder carries.
func TestIngestCapsAreTheDecodersCaps(t *testing.T) {
	if plancost.MaxFrameBytes != payloaddecode.MaxFrameBytes || plancost.MaxJSONBytes != payloaddecode.MaxJSONBytes ||
		plancost.MaxJSONDepth != payloaddecode.MaxJSONDepth {
		t.Fatal("ingest and decoder caps differ")
	}
	if integrationcodec.MaxDepth != plancost.MaxJSONDepth || integrationcodec.MaxDecodedBytes != plancost.MaxJSONBytes {
		t.Fatal("the codec's limits differ from the decoder's")
	}
}
