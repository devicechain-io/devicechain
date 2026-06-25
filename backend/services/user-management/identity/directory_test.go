// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
)

// validateAuthorities accepts a set drawn from the known vocabulary (including
// the super-authority) and rejects any unknown authority, so a role definition
// cannot persist a typo that would silently grant nothing.
func TestValidateAuthorities(t *testing.T) {
	assert.NoError(t, validateAuthorities([]string{
		string(auth.DeviceRead), string(auth.DeviceWrite), string(auth.AuthorityAll),
	}))
	assert.NoError(t, validateAuthorities(nil))

	err := validateAuthorities([]string{string(auth.DeviceRead), "device:admin"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "device:admin")
}
