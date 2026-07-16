// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

// Defaults for the ai-inference service (ADR-056 §4). The service is a synchronous,
// fail-closed proxy to an inference provider (Claude at GA); these bound a single
// inference call so a slow provider cannot pin a request forever and an oversized
// prompt or response is rejected rather than buffered unboundedly. The provider call
// itself lands in slice 0c; the bounds are defined here so the config surface is
// stable from the start.
const (
	// DefaultInferenceTimeoutMs bounds a single outbound inference call. An unbounded
	// wait on a hung provider would pin the request goroutine indefinitely.
	DefaultInferenceTimeoutMs = 60_000
	// DefaultMaxPromptBytes caps an inbound prompt (+ its schema context). An
	// inference prompt is a bounded authoring request, not a blob; the cap bounds the
	// cost of a single call and keeps a caller from shipping an oversized payload.
	DefaultMaxPromptBytes = 128 << 10
	// DefaultMaxOutputTokens caps the provider's generated output for a single call —
	// a rule candidate is small, so a tight ceiling bounds cost and latency.
	DefaultMaxOutputTokens = 2_048

	// MaxInferenceTimeoutMs caps the configurable per-call timeout at startup so an
	// operator cannot set an unbounded wait.
	MaxInferenceTimeoutMs = 120_000
)

// AiInferenceConfiguration is the typed, fail-closed configuration for the
// ai-inference service (ADR-056). It is loaded via core.LoadConfiguration (unknown
// keys rejected).
type AiInferenceConfiguration struct {
	// RdbConfiguration is the per-service datastore configuration. The service keeps
	// its own envelope-encrypted secret store (ADR-059) in this database — each
	// service seals its secrets with the same instance KEK, so the crypto is uniform
	// without a shared datastore.
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// InferenceTimeoutMs bounds a single provider call. Unset (0) defaults to
	// DefaultInferenceTimeoutMs; a negative value or one above MaxInferenceTimeoutMs
	// is rejected.
	InferenceTimeoutMs int

	// MaxPromptBytes caps an inbound prompt. Unset (0) defaults to
	// DefaultMaxPromptBytes; a non-positive value is rejected.
	MaxPromptBytes int

	// MaxOutputTokens caps a provider's generated output. Unset (0) defaults to
	// DefaultMaxOutputTokens; a non-positive value is rejected.
	MaxOutputTokens int
}

// NewAiInferenceConfiguration creates the default configuration.
func NewAiInferenceConfiguration() *AiInferenceConfiguration {
	cfg := &AiInferenceConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset (0) fields with the platform defaults (ADR-022 decision 1).
func (c *AiInferenceConfiguration) ApplyDefaults() {
	if c.InferenceTimeoutMs == 0 {
		c.InferenceTimeoutMs = DefaultInferenceTimeoutMs
	}
	if c.MaxPromptBytes == 0 {
		c.MaxPromptBytes = DefaultMaxPromptBytes
	}
	if c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
}

// Validate rejects out-of-range tunables fail-closed (ADR-022 decision 1).
func (c *AiInferenceConfiguration) Validate() error {
	if c.InferenceTimeoutMs <= 0 || c.InferenceTimeoutMs > MaxInferenceTimeoutMs {
		return fmt.Errorf("inferenceTimeoutMs must be in (0, %d], got %d", MaxInferenceTimeoutMs, c.InferenceTimeoutMs)
	}
	if c.MaxPromptBytes <= 0 {
		return fmt.Errorf("maxPromptBytes must be positive, got %d", c.MaxPromptBytes)
	}
	if c.MaxOutputTokens <= 0 {
		return fmt.Errorf("maxOutputTokens must be positive, got %d", c.MaxOutputTokens)
	}
	return nil
}
