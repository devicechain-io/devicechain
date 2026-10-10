// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-ai-inference/config"
)

// The request execution deadline must sit above the provider call, or it would end a
// request that is legitimately waiting on its provider.
func TestGraphQLExecTimeoutClearsTheWorstCaseDraft(t *testing.T) {
	prev := Configuration
	defer func() { Configuration = prev }()
	Configuration = &config.AiInferenceConfiguration{InferenceTimeoutMs: config.MaxInferenceTimeoutMs}

	worst := time.Duration(config.MaxInferenceTimeoutMs) * time.Millisecond
	got := graphQLExecTimeout()
	if got <= worst {
		t.Fatalf("exec timeout %v must exceed one provider call %v", got, worst)
	}
	if got > worst+time.Minute {
		t.Fatalf("exec timeout %v is far above one provider call %v; one request makes one call", got, worst)
	}
}
