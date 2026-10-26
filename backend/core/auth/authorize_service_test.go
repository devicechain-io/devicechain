// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// AuthorizeService admits only a service token that carries the authority. Every other
// caller, including a tenant access token holding the very same authority, is refused.
func TestAuthorizeService(t *testing.T) {
	with := func(c *Claims) context.Context { return WithClaims(context.Background(), c) }
	held := []string{string(DeviceRead)}

	assert.ErrorIs(t, AuthorizeService(context.Background(), DeviceRead), ErrUnauthenticated)

	// The paired positive.
	assert.NoError(t, AuthorizeService(with(&Claims{TokenType: TokenTypeService, Authorities: held}), DeviceRead))

	// The negative controls: each is refused although the authority is held where it can be.
	assert.ErrorIs(t, AuthorizeService(with(&Claims{TokenType: TokenTypeAccess, Authorities: held}), DeviceRead), ErrForbidden,
		"a tenant access token holding the authority")
	assert.ErrorIs(t, AuthorizeService(with(&Claims{TokenType: TokenTypeAccess, Authorities: []string{string(AuthorityAll)}}), DeviceRead), ErrForbidden,
		"a tenant administrator")
	assert.ErrorIs(t, AuthorizeService(with(&Claims{Authorities: held}), DeviceRead), ErrForbidden,
		"claims with no token type")
	assert.ErrorIs(t, AuthorizeService(with(&Claims{TokenType: TokenTypeService, Authorities: held}), DeviceWrite), ErrForbidden,
		"a service token without the authority")
	assert.ErrorIs(t, AuthorizeService(with(&Claims{TokenType: TokenTypeService, Authorities: held,
		ScopeAnchors: []string{"customer:acme"}}), DeviceRead), ErrScopeUnsupported, "a scoped caller")
}
