// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"net/http"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// extraTokenKey / extraTenantKey are the TokenInfo.Extra keys the tools read to
// forward the caller's identity downstream.
const (
	extraTokenKey  = "dc_token"
	extraTenantKey = "dc_tenant"
)

// NewTokenVerifier builds the OAuth 2.1 Resource Server token verifier (ADR-047).
// It validates the presented bearer as a DeviceChain access token against the
// live JWKS validator (RS256, signature + expiry + tenant), then enforces the
// audience binding: the token's `aud` MUST include this server's resource
// identifier. This is the anti-confused-deputy control — a token minted for
// another audience (or with no audience) is rejected, so the MCP server cannot be
// tricked into acting with a token that was never issued for it.
//
// The read-only scope requirement is enforced by the RequireBearerToken middleware
// (via its Scopes option) against the Scopes this verifier returns; the scope→
// authority cap itself was already applied at token-issue time in the AS, so the
// token's authorities are structurally read-only.
//
// validator is late-bound (the JWKS validator becomes available only once the
// readiness gate opens), so a nil validator fails closed.
func NewTokenVerifier(validator func() *coreauth.Validator, resourceID string) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		v := validator()
		if v == nil {
			return nil, fmt.Errorf("%w: token validator is not ready", sdkauth.ErrInvalidToken)
		}
		claims, err := v.Validate(token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sdkauth.ErrInvalidToken, err)
		}
		if !audienceIncludes(claims.Audience, resourceID) {
			return nil, fmt.Errorf("%w: token audience is not bound to this resource", sdkauth.ErrInvalidToken)
		}

		info := &sdkauth.TokenInfo{
			Scopes: coreauth.ParseScope(claims.Scope),
			UserID: claims.Email,
			Extra: map[string]any{
				extraTokenKey:  token,
				extraTenantKey: claims.Tenant,
			},
		}
		if claims.ExpiresAt != nil {
			info.Expiration = claims.ExpiresAt.Time
		}
		return info, nil
	}
}

// audienceIncludes reports whether the token audience list contains the resource id.
func audienceIncludes(aud []string, resourceID string) bool {
	for _, a := range aud {
		if a == resourceID {
			return true
		}
	}
	return false
}
