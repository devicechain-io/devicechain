// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"
)

// The caller must outwait the slowest inference ai-inference can be configured for PLUS
// the work it does, cold, before that deadline starts (three cross-service calls, each
// bounded at 10s). Otherwise the caller cuts the callee off before the callee can say
// "timed out", and the author is told the service is unavailable.
func TestAiInferenceCallerOutwaitsTheCeiling(t *testing.T) {
	const crossServiceCall = 10 * time.Second
	coldPreCall := 3 * crossServiceCall // mint + tenant limits + tenant facts
	if AiInferencePreCallBound < coldPreCall {
		t.Fatalf("pre-call bound %v is below the cold pre-call work %v", AiInferencePreCallBound, coldPreCall)
	}
	if AiInferenceCallerTimeout <= AiInferenceMaxCallTimeout+coldPreCall {
		t.Fatalf("caller timeout %v does not outwait the inference ceiling %v plus %v of pre-call work",
			AiInferenceCallerTimeout, AiInferenceMaxCallTimeout, coldPreCall)
	}
}

// The draft chain nests: the default inference deadline inside the draft budget, the
// draft budget inside the request edge. Out of order, the inner answer never reaches
// the author — the outer bound replaces it with a vaguer one.
func TestAiDraftChainNests(t *testing.T) {
	if !(AiInferenceDefaultCallTimeout < AiDraftBudget) {
		t.Fatalf("default inference timeout %v must be inside the draft budget %v",
			AiInferenceDefaultCallTimeout, AiDraftBudget)
	}
	if !(AiDraftBudget < RequestEdgeTimeout) {
		t.Fatalf("draft budget %v must be inside the request edge %v", AiDraftBudget, RequestEdgeTimeout)
	}
	if !(AiInferenceDefaultCallTimeout <= AiInferenceMaxCallTimeout) {
		t.Fatalf("default inference timeout %v exceeds its own ceiling %v",
			AiInferenceDefaultCallTimeout, AiInferenceMaxCallTimeout)
	}
}
