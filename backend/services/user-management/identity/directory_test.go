// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
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

// requireCallerGrants enforces the no-escalation invariant (review #3): the
// caller may only grant authorities they already hold; the "*" super-authority
// grants everything; no claims is unauthenticated.
func TestRequireCallerGrants(t *testing.T) {
	admin := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.AuthorityAll)}})
	assert.NoError(t, requireCallerGrants(admin, []string{string(auth.DeviceWrite), string(auth.AuthorityAll)}))

	scoped := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.DeviceRead)}})
	assert.NoError(t, requireCallerGrants(scoped, []string{string(auth.DeviceRead)}))
	// Cannot grant an authority not held — the escalation that finding #3 blocks.
	assert.ErrorIs(t, requireCallerGrants(scoped, []string{string(auth.AuthorityAll)}), auth.ErrForbidden)
	assert.ErrorIs(t, requireCallerGrants(scoped, []string{string(auth.DeviceWrite)}), auth.ErrForbidden)

	// No claims on the context → unauthenticated.
	assert.ErrorIs(t, requireCallerGrants(context.Background(), []string{string(auth.DeviceRead)}), auth.ErrUnauthenticated)
}
