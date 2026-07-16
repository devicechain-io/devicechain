// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExternalKindFailsClosed pins the fail-closed direction of the external-routing
// classification (ADR-056 §6): a kind is external (consent required) unless it is
// affirmatively registered as in-boundary. The GA kind is external, and — critically —
// an UNCLASSIFIED kind defaults to external, so a future provider added to the
// resolver's build() switch cannot silently skip the tenant opt-in gate.
func TestExternalKindFailsClosed(t *testing.T) {
	assert.True(t, IsExternalProviderKind(string(AIProviderKindAnthropic)),
		"the GA hosted provider is external")
	assert.True(t, IsExternalProviderKind("some-future-unclassified-kind"),
		"an unclassified kind must default to external (consent required)")
	assert.True(t, IsExternalProviderKind(""),
		"the empty kind must default to external")
}
