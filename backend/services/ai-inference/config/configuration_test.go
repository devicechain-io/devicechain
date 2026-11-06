// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestApplyDefaultsFillsUnset checks that unset (0) tunables take the platform
// defaults and a defaulted config validates.
func TestApplyDefaultsFillsUnset(t *testing.T) {
	c := &AiInferenceConfiguration{}
	c.ApplyDefaults()
	if c.InferenceTimeoutMs != DefaultInferenceTimeoutMs {
		t.Fatalf("InferenceTimeoutMs = %d, want %d", c.InferenceTimeoutMs, DefaultInferenceTimeoutMs)
	}
	if c.MaxPromptBytes != DefaultMaxPromptBytes {
		t.Fatalf("MaxPromptBytes = %d, want %d", c.MaxPromptBytes, DefaultMaxPromptBytes)
	}
	if c.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("MaxOutputTokens = %d, want %d", c.MaxOutputTokens, DefaultMaxOutputTokens)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaulted config should validate: %v", err)
	}
}

// TestValidateRejectsOutOfRange pins the fail-closed bounds. Each case starts from a
// defaulted, valid configuration and breaks exactly ONE field, and the error must name
// that field, so a case can only pass through the guard it is named for.
func TestValidateRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		mutate func(*AiInferenceConfiguration)
	}{
		{"zero timeout", "inferenceTimeoutMs", func(c *AiInferenceConfiguration) { c.InferenceTimeoutMs = 0 }},
		{"timeout over ceiling", "inferenceTimeoutMs", func(c *AiInferenceConfiguration) { c.InferenceTimeoutMs = MaxInferenceTimeoutMs + 1 }},
		{"negative timeout", "inferenceTimeoutMs", func(c *AiInferenceConfiguration) { c.InferenceTimeoutMs = -1 }},
		{"zero prompt cap", "maxPromptBytes", func(c *AiInferenceConfiguration) { c.MaxPromptBytes = 0 }},
		{"zero output cap", "maxOutputTokens", func(c *AiInferenceConfiguration) { c.MaxOutputTokens = 0 }},
		{"zero rate", "inferenceRequestsPerMinute", func(c *AiInferenceConfiguration) { c.InferenceRequestsPerMinute = 0 }},
		{"zero burst", "inferenceBurst", func(c *AiInferenceConfiguration) { c.InferenceBurst = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &AiInferenceConfiguration{}
			c.ApplyDefaults()
			if err := c.Validate(); err != nil {
				t.Fatalf("the base fixture must itself be valid: %v", err)
			}
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected Validate to reject an out-of-range value")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("rejected by the wrong guard: %v (want one naming %s)", err, tc.field)
			}
		})
	}
}
