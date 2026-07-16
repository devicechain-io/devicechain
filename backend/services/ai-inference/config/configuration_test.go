// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

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

// TestValidateRejectsOutOfRange pins the fail-closed bounds.
func TestValidateRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AiInferenceConfiguration)
	}{
		{"zero timeout", func(c *AiInferenceConfiguration) {
			c.InferenceTimeoutMs = 0
			c.MaxPromptBytes = 1
			c.MaxOutputTokens = 1
		}},
		{"timeout over ceiling", func(c *AiInferenceConfiguration) {
			c.InferenceTimeoutMs = MaxInferenceTimeoutMs + 1
			c.MaxPromptBytes = 1
			c.MaxOutputTokens = 1
		}},
		{"negative timeout", func(c *AiInferenceConfiguration) {
			c.InferenceTimeoutMs = -1
			c.MaxPromptBytes = 1
			c.MaxOutputTokens = 1
		}},
		{"zero prompt cap", func(c *AiInferenceConfiguration) {
			c.InferenceTimeoutMs = 1
			c.MaxPromptBytes = 0
			c.MaxOutputTokens = 1
		}},
		{"zero output cap", func(c *AiInferenceConfiguration) {
			c.InferenceTimeoutMs = 1
			c.MaxPromptBytes = 1
			c.MaxOutputTokens = 0
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &AiInferenceConfiguration{}
			tc.mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected Validate to reject an out-of-range value")
			}
		})
	}
}
