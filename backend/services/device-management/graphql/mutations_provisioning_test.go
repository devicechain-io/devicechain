// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/assert"
)

// Every provisioning policy sentinel is collapsed to the single generic
// provisioningRejected error so a device-facing caller cannot tell them apart
// and enumerate valid provision keys or device tokens.
func TestSanitizeProvisioningError_CollapsesSentinels(t *testing.T) {
	for _, sentinel := range []error{
		model.ErrProvisioningKeyNotResolved,
		model.ErrProvisioningDisabled,
		model.ErrProvisioningExpired,
		model.ErrProvisioningSecretMismatch,
		model.ErrProvisioningStrategyInvalid,
		model.ErrProvisioningDeviceNotPreProvisioned,
	} {
		assert.Equal(t, provisioningRejected, sanitizeProvisioningError(sentinel),
			"sentinel %v should collapse to the generic error", sentinel)
		// A wrapped sentinel must collapse too (errors.Is matching).
		wrapped := fmt.Errorf("context: %w", sentinel)
		assert.Equal(t, provisioningRejected, sanitizeProvisioningError(wrapped))
	}
}

// An unexpected error (e.g. a datastore failure) is not an enumeration vector and
// passes through unchanged so it stays diagnosable.
func TestSanitizeProvisioningError_PassesThroughOther(t *testing.T) {
	other := errors.New("connection refused")
	assert.Equal(t, other, sanitizeProvisioningError(other))
}
