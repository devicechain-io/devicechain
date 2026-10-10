// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-ai-inference/config"
)

// The request execution deadline must sit above the worst-case draft, or it would end a
// request that is legitimately waiting on its provider.
func TestGraphQLExecTimeoutClearsTheWorstCaseDraft(t *testing.T) {
	prev := Configuration
	defer func() { Configuration = prev }()
	Configuration = &config.AiInferenceConfiguration{InferenceTimeoutMs: config.MaxInferenceTimeoutMs}

	worst := inferenceDraftCalls * time.Duration(config.MaxInferenceTimeoutMs) * time.Millisecond
	if got := graphQLExecTimeout(); got <= worst {
		t.Fatalf("exec timeout %v must exceed the worst-case draft %v", got, worst)
	}
}
