// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/stretchr/testify/assert"
)

// outcomeFor reads the sentinels, so it must classify a WRAPPED sentinel the same as a
// bare one — the resolver wraps ErrUnavailable with context at most of its gates.
func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, outcomeServed},
		{"rate limited", inference.ErrRateLimited, outcomeRateLimited},
		{"consent required", inference.ErrConsentRequired, outcomeConsentRequired},
		{"unavailable", inference.ErrUnavailable, outcomeUnavailable},
		{"wrapped unavailable", fmt.Errorf("%w: no active provider is configured", inference.ErrUnavailable), outcomeUnavailable},
		{"wrapped rate limited", fmt.Errorf("ai-inference: %w", inference.ErrRateLimited), outcomeRateLimited},
		{"provider failure", errors.New("inference provider returned 500"), outcomeProviderError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, outcomeFor(tc.err))
		})
	}
}

// A LOCAL store failure must not read as a provider fault. The resolver wraps its
// active-provider read error in ErrUnavailable precisely so this classifies as
// unavailable: an operator seeing provider_error spike would go investigate their
// inference key and endpoint when their own database was the thing that blipped.
func TestOutcomeFor_LocalStoreFailureIsNotAProviderFault(t *testing.T) {
	storeErr := fmt.Errorf("%w: could not read the active provider: %v",
		inference.ErrUnavailable, errors.New("dial tcp 127.0.0.1:5432: connection refused"))
	assert.Equal(t, outcomeUnavailable, outcomeFor(storeErr))
	assert.NotEqual(t, outcomeProviderError, outcomeFor(storeErr))
}

// The recorders are nil-safe: a resolver built without a Microservice (unit tests, and
// the schema_test parse harness) must run unmeasured rather than panic.
func TestMetrics_NilIsSafe(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() {
		m.recordCall(outcomeServed)
		m.recordUsage(10, 20)
	})
	assert.Nil(t, NewMetrics(nil), "a nil Microservice yields nil metrics")
}
