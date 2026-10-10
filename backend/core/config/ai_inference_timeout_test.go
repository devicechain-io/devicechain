// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"
)

// The caller must outwait the slowest inference ai-inference can be configured for, with
// room for the work ai-inference does before that deadline starts (a cross-service read
// bounded at 10s among it). Otherwise the caller cuts the callee off before the callee
// can say "timed out", and the author is told the service is unavailable.
func TestAiInferenceCallerOutwaitsTheCeiling(t *testing.T) {
	const preInferenceBound = 10 * time.Second
	if AiInferenceCallerTimeout <= AiInferenceMaxCallTimeout+preInferenceBound {
		t.Fatalf("caller timeout %v does not outwait the inference ceiling %v plus %v of pre-inference work",
			AiInferenceCallerTimeout, AiInferenceMaxCallTimeout, preInferenceBound)
	}
}
