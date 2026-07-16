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

	// DefaultInferenceRequestsPerMinute / DefaultInferenceBurst are the PLATFORM
	// per-tenant rate ceiling for inference calls (ADR-056 §6 / ADR-023) — the
	// fail-safe every tenant is metered at when it declares no override. They are a
	// budget gate, not an authz gate: authorization to author a rule is not
	// authorization to spend the operator's provider key without bound.
	//
	// Sized against the shape of real authoring rather than a round number. One NL
	// draft costs up to nldraft's bounded repair loop (3 calls today), so 30/min is
	// ~10 drafts a minute sustained and a burst of 15 is ~5 back-to-back — comfortably
	// above a human describing rules and clicking Draft, and orders of magnitude below
	// a script looping the door, which is the case this exists to shed.
	DefaultInferenceRequestsPerMinute = 30
	DefaultInferenceBurst             = 15
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

	// InferenceRequestsPerMinute / InferenceBurst are the PLATFORM per-tenant rate
	// ceiling for inference calls (ADR-056 §6 / ADR-023), applied to every tenant that
	// declares no override on its control-plane row. Unset (0) defaults; a non-positive
	// value is rejected — there is no "unlimited" setting, by design.
	InferenceRequestsPerMinute float64
	InferenceBurst             int
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
	if c.InferenceRequestsPerMinute == 0 {
		c.InferenceRequestsPerMinute = DefaultInferenceRequestsPerMinute
	}
	if c.InferenceBurst == 0 {
		c.InferenceBurst = DefaultInferenceBurst
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
	// Never unlimited: a non-positive ceiling is rejected at startup rather than
	// coerced, so an operator who meant to widen the limit cannot accidentally remove
	// it (0 would otherwise mean "meter nothing" to the caller's eye and "admit
	// nothing" to the token bucket).
	if c.InferenceRequestsPerMinute <= 0 {
		return fmt.Errorf("inferenceRequestsPerMinute must be positive, got %v", c.InferenceRequestsPerMinute)
	}
	if c.InferenceBurst <= 0 {
		return fmt.Errorf("inferenceBurst must be positive, got %d", c.InferenceBurst)
	}
	return nil
}
