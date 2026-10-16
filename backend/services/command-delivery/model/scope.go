// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// RejectDeviceNotFound is the enqueue gate's DEVICE_NOT_FOUND, spelled here for the one
// rejection this service writes in the gate's place: a device outside a scoped member's
// scope (ADR-081). It is deliberately the owner's code and the owner's words ("device %q
// does not exist"), not a code of its own — a distinct answer for "exists, not yours"
// would tell a customer which tokens name another customer's equipment. Every other
// DEVICE_NOT_FOUND is relayed from device-management unchanged, as RejectionCode's doc says.
const RejectDeviceNotFound RejectionCode = "DEVICE_NOT_FOUND"

// Members limited to part of the tenant (ADR-081) reach this service through three doors:
// they read commands (the scope predicate on Command, see Command.ScopedBy), they read a
// device's hold, and they enqueue. Everything else — batches, cancel, the machine-only
// lifecycle writes — refuses them at the resolver, before any of this runs.

// DeviceAnchorLookup reads devices' scope anchors from device-management, the owner of the
// relationships that make them (ADR-081): a device that does not exist is absent from the
// map, a device nothing owns maps to an empty list. Dependency-inverted like
// CommandEnqueueValidator; verify implements it.
type DeviceAnchorLookup interface {
	DeviceScopeAnchors(ctx context.Context, deviceTokens []string) (map[string][]string, error)
}

// requireOwnedDevice answers whether a scoped member owns the device, as a refusal: nil when
// the caller is unscoped or owns it, gorm.ErrRecordNotFound (NOT_FOUND) when the device is
// outside the scope or does not exist — the two are one answer, so a member cannot learn
// which tokens name someone else's equipment — and the scope refusal when nothing can say.
//
// A lookup that FAILS is an availability failure, answered with the same sanitized words
// as every other unreachable gate on this service: the detail is logged, not returned,
// because svcclient's error names the in-cluster endpoint it could not reach, and a
// member's hold query is no place to learn the tenant's topology.
func (api *Api) requireOwnedDevice(ctx context.Context, deviceToken string) error {
	scope, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return nil
	}
	if api.DeviceAnchors == nil {
		return auth.ErrScopeUnsupported
	}
	found, err := api.DeviceAnchors.DeviceScopeAnchors(ctx, []string{deviceToken})
	if err != nil {
		log.Error().Err(err).Str("deviceToken", deviceToken).Msg("Device anchor lookup failed; refusing scoped read.")
		return fmt.Errorf("cannot decide the device's ownership: validation is unavailable")
	}
	if anchors, ok := found[deviceToken]; !ok || !inScope(scope, anchors) {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// inScope reports whether any of a device's anchors is in the scope. An unscoped caller is
// in scope everywhere; a scoped one with no anchor in common with the device is not, and
// neither is a device that has no anchors (nil or empty): absence never means "all".
func inScope(scope auth.Scope, anchors []string) bool {
	if !scope.IsScoped() {
		return true
	}
	mine := make(map[string]struct{}, len(scope.Anchors()))
	for _, a := range scope.Anchors() {
		mine[a] = struct{}{}
	}
	for _, a := range anchors {
		if _, ok := mine[a]; ok {
			return true
		}
	}
	return false
}

// tenantWideReadContext returns ctx with the caller's claims removed and its tenant kept,
// for a read the platform makes about the WHOLE tenant while serving a scoped member: the
// held-command ceiling (a tenant bound, which a count filtered to the member's own commands
// would let them walk past), and the hold of a device the member has already been shown to
// own. Under it the tenant predicate still applies and the scope predicate does not.
//
// 🔴 USE IT FOR A READ WHOSE ROWS NEVER REACH THE MEMBER, AND ONLY AFTER THE MEMBER'S RIGHT
// TO THE ANSWER IS DECIDED. It is the storage half of what svcclient.OnBehalfOfScopedCaller
// is for a service call: a deliberate, local decision that one answer may be computed above
// the member's scope. It is never used for a write — a write under it would be recorded in
// the audit journal with no actor — and never handed onward: derive it at the read.
func tenantWideReadContext(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, nil)
}

// isCodedRefusal reports whether err is a decided refusal — a coded rejection or an
// authorization refusal — rather than a failed statement, so a scoped insert that failed
// for one of those is not re-read as a token collision.
func isCodedRefusal(err error) bool {
	var rejection *EnqueueRejected
	return errors.As(err, &rejection) || errors.Is(err, auth.ErrForbidden)
}

// tokenTaken reports whether any live command in the tenant holds token, whoever stamped
// it. It is how a scoped member's failed insert is told apart from a token collision with a
// command the member cannot see: the member learns only that the token is taken
// (TOKEN_IN_USE, the answer every caller already gets for a token a batch holds), never
// anything about the command holding it.
func (api *Api) tokenTaken(ctx context.Context, token string) (bool, error) {
	var n int64
	if err := api.RDB.DB(tenantWideReadContext(ctx)).Model(&Command{}).
		Where("token = ?", token).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// batchStamps reads the scope anchors of a batch's admitted devices, in chunks the owner's
// lookup bound admits. With no lookup wired there is nothing to read and every command is
// written unstamped (NULL), exactly as with no enqueue gate; a lookup that fails fails the
// batch, with the sanitized message every other unreachable gate on this path gives.
func (api *Api) batchStamps(ctx context.Context, tokens []string) (map[string][]string, error) {
	stamps := make(map[string][]string, len(tokens))
	if api.DeviceAnchors == nil {
		return stamps, nil
	}
	for start := 0; start < len(tokens); start += rdb.MaxLookupKeys {
		end := min(start+rdb.MaxLookupKeys, len(tokens))
		found, err := api.DeviceAnchors.DeviceScopeAnchors(ctx, tokens[start:end])
		if err != nil {
			log.Error().Err(err).Msg("Device anchor lookup failed; refusing batch.")
			return nil, fmt.Errorf("cannot enqueue command batch: validation is unavailable")
		}
		for device, anchors := range found {
			stamps[device] = anchors
		}
	}
	return stamps, nil
}
