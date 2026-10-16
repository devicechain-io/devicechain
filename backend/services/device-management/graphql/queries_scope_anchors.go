// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// DeviceScopeAnchorsResolver exposes one device's anchors.
type DeviceScopeAnchorsResolver struct {
	deviceToken string
	anchors     []string
}

func (r *DeviceScopeAnchorsResolver) DeviceToken() string { return r.deviceToken }

func (r *DeviceScopeAnchorsResolver) Anchors() []string { return r.anchors }

// DeviceScopeAnchors answers, for each named device that exists, the anchors another
// service stamps on a record it writes about that device (ADR-081): the stamp that decides
// which members limited to part of the tenant can see the record. command-delivery stamps a
// command with it at enqueue and update-management an assignment at assignment, both over a
// service token.
//
// 🔴 IT IS GATED ON Authorize, NOT AuthorizeScoped, SO A SCOPED MEMBER IS REFUSED IT — and
// that is the whole reason it is a query of its own rather than a field on Device. A device
// may be assigned to several customers at once, so a device's anchors can name a customer
// other than the caller's; a field on Device would hand an Acme member the token of every
// other customer sharing an Acme device the moment devices opted scoped callers in. The
// service tokens that call this are unscoped by construction (auth refuses a scope on one).
//
// Devices that do not exist are omitted, in request order otherwise. More than 1000 tokens
// is refused with LIMIT_EXCEEDED.
func (r *SchemaResolver) DeviceScopeAnchors(ctx context.Context, args struct {
	DeviceTokens []string
}) ([]*DeviceScopeAnchorsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	byDevice, err := r.GetApi(ctx).DeviceScopeAnchors(ctx, args.DeviceTokens)
	if err != nil {
		return nil, err
	}
	out := make([]*DeviceScopeAnchorsResolver, 0, len(byDevice))
	seen := make(map[string]struct{}, len(byDevice))
	for _, token := range args.DeviceTokens {
		anchors, ok := byDevice[token]
		if !ok {
			continue
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, &DeviceScopeAnchorsResolver{deviceToken: token, anchors: anchors})
	}
	return out, nil
}
