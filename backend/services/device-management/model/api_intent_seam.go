// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
	"gorm.io/gorm"
)

// The intent observation seam: five read-only questions the automation area asks about the
// fleet, answered here because device-management owns the data. They know nothing about intents
// and write nothing. The three that read the fleet (digest, page, intersect) are
// internal/intentseam's; the preview and the standing answer reuse the bulk-change planner and
// the bulk-change standing check, so what the area is told is what a person would be told.

// IntentSeamTarget is one frozen version of a dynamic device group.
type IntentSeamTarget = intentseam.Target

// ConfigurationKeyStateDigest answers what the members of a frozen group version hold for one
// configuration key, as one digest and counts.
func (api *Api) ConfigurationKeyStateDigest(ctx context.Context, t IntentSeamTarget, key string) (*intentseam.DigestAnswer, error) {
	return api.IntentSeam.Digest(ctx, api.RDB.DB(ctx), t, key)
}

// ConfigurationKeyState answers one keyset page of the same rows the digest covers.
func (api *Api) ConfigurationKeyState(ctx context.Context, t IntentSeamTarget, key string, afterID int64, limit int) (*intentseam.PageAnswer, error) {
	return api.IntentSeam.Page(ctx, api.RDB.DB(ctx), t, key, afterID, limit)
}

// GroupTargetsIntersect answers whether two frozen targets share a live device.
func (api *Api) GroupTargetsIntersect(ctx context.Context, a, b IntentSeamTarget) (*intentseam.IntersectAnswer, error) {
	return api.IntentSeam.Intersect(ctx, api.RDB.DB(ctx), a, b)
}

// PreviewIntentBulkChange plans the bulk change a set of drifted devices would get, with the
// code create runs (normalizeFleetRequest + planNormalized), and writes nothing. It inherits
// every refusal of the normalizer unchanged: an empty list is INVALID_REQUEST, a list over the
// planner's byte bound INPUT_TOO_LARGE, over its line bound OPERATION_TOO_LARGE. The caller is
// expected never to earn one; they stay reachable so a later loosening of the planner shows.
// It runs as a seam statement: inside the seam's limiter, read-only, under PreviewTimeout.
func (api *Api) PreviewIntentBulkChange(ctx context.Context, kind string, configSet *FleetConfigSetIntent, deviceTokens []string) (*FleetPlan, error) {
	n, err := normalizeFleetRequest(&FleetOperationCreateRequest{Kind: kind, ConfigSet: configSet, DeviceTokens: deviceTokens})
	if err != nil {
		return nil, err
	}
	var plan *FleetPlan
	err = api.IntentSeam.Run(ctx, api.RDB.DB(ctx), intentseam.OpPreview, func(tx *gorm.DB) (bool, error) {
		var perr error
		plan, perr = planNormalizedOn(tx, n)
		return false, perr
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// ErrStandingPrincipalRequired refuses a standing question about nobody.
var ErrStandingPrincipalRequired = errors.New("a principal is required")

// BulkChangeGranteeStanding answers whether principal could run a bulk change now: whether it
// still holds device:write and fleet:operate in this tenant, by the same checker and deadline a
// running bulk change's lease uses. A nil answer means there is no answer (no checker wired, an
// error, a timeout, or a reply that did not mention the principal), and must never be read as
// either yes or no.
func (api *Api) BulkChangeGranteeStanding(ctx context.Context, principal string) (*bool, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, ErrStandingPrincipalRequired
	}
	if api.PrincipalStanding == nil {
		return nil, nil
	}
	askCtx, cancel := context.WithTimeout(ctx, FleetStandingTimeout)
	defer cancel()
	answer, err := api.PrincipalStanding.PrincipalStanding(askCtx, []string{principal}, fleetStandingAuthorities)
	if err != nil || answer == nil {
		return nil, nil
	}
	qualifies, present := answer[principal]
	if !present {
		return nil, nil
	}
	return &qualifies, nil
}
