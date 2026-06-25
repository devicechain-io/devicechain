// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ValidAuthority accepts known authorities (including the super-authority) and
// rejects unknown strings, so a role definition cannot grant a typo.
func TestValidAuthority(t *testing.T) {
	for _, valid := range []Authority{AuthorityAll, UserWrite, DeviceRead, CommandWrite} {
		assert.True(t, ValidAuthority(string(valid)), "expected %q valid", valid)
	}
	for _, invalid := range []string{"", "device:admin", "bogus", "DEVICE:READ"} {
		assert.False(t, ValidAuthority(invalid), "expected %q invalid", invalid)
	}
}

// HasAuthority matches an exact authority, grants everything for the
// super-authority, and denies an unheld authority.
func TestClaimsHasAuthority(t *testing.T) {
	scoped := &Claims{Authorities: []string{string(DeviceRead), string(EventRead)}}
	assert.True(t, scoped.HasAuthority(DeviceRead))
	assert.True(t, scoped.HasAuthority(EventRead))
	assert.False(t, scoped.HasAuthority(DeviceWrite))

	admin := &Claims{Authorities: []string{string(AuthorityAll)}}
	assert.True(t, admin.HasAuthority(DeviceWrite))
	assert.True(t, admin.HasAuthority(UserWrite))

	none := &Claims{}
	assert.False(t, none.HasAuthority(DeviceRead))
}

// Authorize returns ErrUnauthenticated with no claims on the context,
// ErrForbidden when the authority is missing, and nil when granted.
func TestAuthorize(t *testing.T) {
	bare := context.Background()
	assert.ErrorIs(t, Authorize(bare, DeviceWrite), ErrUnauthenticated)

	scoped := WithClaims(context.Background(), &Claims{Authorities: []string{string(DeviceRead)}})
	assert.ErrorIs(t, Authorize(scoped, DeviceWrite), ErrForbidden)
	assert.NoError(t, Authorize(scoped, DeviceRead))

	admin := WithClaims(context.Background(), &Claims{Authorities: []string{string(AuthorityAll)}})
	assert.NoError(t, Authorize(admin, DeviceWrite))
}

// AuthorizeAny passes when any one authority is held, fails closed on an empty
// set, and distinguishes unauthenticated from forbidden.
func TestAuthorizeAny(t *testing.T) {
	scoped := WithClaims(context.Background(), &Claims{Authorities: []string{string(DeviceRead)}})
	assert.NoError(t, AuthorizeAny(scoped, DeviceWrite, DeviceRead))
	assert.ErrorIs(t, AuthorizeAny(scoped, DeviceWrite, CommandWrite), ErrForbidden)

	// Empty required set fails closed rather than authorizing.
	assert.ErrorIs(t, AuthorizeAny(scoped), ErrForbidden)

	assert.ErrorIs(t, AuthorizeAny(context.Background(), DeviceRead), ErrUnauthenticated)
}
