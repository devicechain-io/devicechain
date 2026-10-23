// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package profilecheck confirms device-profile tokens against device-management, over a
// service token scoped to device:read and the caller's tenant.
package profilecheck

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-update-management/model"
)

// Querier is the slice of svcclient.Client this package uses.
type Querier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// Checker implements model.ProfileChecker.
type Checker struct {
	client Querier
	url    string
}

var _ Querier = (*svcclient.Client)(nil)

// New builds a checker that asks the device-management GraphQL endpoint at graphqlURL.
func New(client Querier, graphqlURL string) *Checker {
	return &Checker{client: client, url: graphqlURL}
}

const profilesByToken = `query($tokens: [String!]!) { deviceProfilesByToken(tokens: $tokens) { token } }`

// Missing returns the requested tokens that are not device profiles in the tenant. An
// error means the answer could not be obtained; it is never reported as "none missing".
func (c *Checker) Missing(ctx context.Context, tenant string, tokens []string) ([]string, error) {
	var out struct {
		DeviceProfilesByToken []struct {
			Token string `json:"token"`
		} `json:"deviceProfilesByToken"`
	}
	if err := c.client.Query(ctx, c.url, tenant, profilesByToken, map[string]any{"tokens": tokens}, &out); err != nil {
		return nil, fmt.Errorf("device-management profile lookup: %w", err)
	}
	found := make(map[string]bool, len(out.DeviceProfilesByToken))
	for _, p := range out.DeviceProfilesByToken {
		found[p.Token] = true
	}
	var missing []string
	for _, t := range tokens {
		if !found[t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

// deviceFacts reads the device and, in the same request, its scope anchors (ADR-081) — the
// stamp an assignment is written with. One request, so the two describe the device as one
// read saw it.
const deviceFacts = `query($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) { token enabled deviceType { profile { token } } }
  deviceScopeAnchors(deviceTokens: $tokens) { deviceToken anchors }
}`

// DeviceOf implements model.DeviceLookup: the token of the device-profile a device resolves
// through (empty for a device whose type has none), whether an operator has disabled it, and
// found=false for a device the tenant does not have, and its scope anchors. An error means the
// answer could not be obtained; it is never reported as a device that does not exist or one
// that is enabled — nor as a device nobody owns, which an answer missing the anchors would be
// read as.
func (c *Checker) DeviceOf(ctx context.Context, tenant, deviceToken string) (facts model.DeviceFacts, found bool, err error) {
	var out struct {
		DevicesByToken []struct {
			Token      string `json:"token"`
			Enabled    *bool  `json:"enabled"`
			DeviceType struct {
				Profile *struct {
					Token string `json:"token"`
				} `json:"profile"`
			} `json:"deviceType"`
		} `json:"devicesByToken"`
		DeviceScopeAnchors *[]struct {
			DeviceToken string   `json:"deviceToken"`
			Anchors     []string `json:"anchors"`
		} `json:"deviceScopeAnchors"`
	}
	if err := c.client.Query(ctx, c.url, tenant, deviceFacts, map[string]any{"tokens": []string{deviceToken}}, &out); err != nil {
		return model.DeviceFacts{}, false, fmt.Errorf("device-management device lookup: %w", err)
	}
	for _, d := range out.DevicesByToken {
		if d.Token != deviceToken {
			continue
		}
		// `enabled` is non-null in the schema; an answer without it is not "enabled".
		if d.Enabled == nil {
			return model.DeviceFacts{}, false, errors.New("device-management device lookup: the answer carries no enabled flag")
		}
		facts.Disabled = !*d.Enabled
		if d.DeviceType.Profile != nil {
			facts.Profile = d.DeviceType.Profile.Token
		}
		if out.DeviceScopeAnchors == nil {
			return model.DeviceFacts{}, false, errors.New("device-management device lookup: the answer carries no anchors")
		}
		facts.Anchors = []string{}
		for _, a := range *out.DeviceScopeAnchors {
			if a.DeviceToken == deviceToken {
				facts.Anchors = append(facts.Anchors, a.Anchors...)
			}
		}
		return facts, true, nil
	}
	return model.DeviceFacts{}, false, nil
}

// resolveDeviceGroupTargets walks a group's device members by KEYSET cursor (the contract command
// delivery's batches rely on): offset paging would silently skip a member that stays in the group
// while an earlier one leaves.
const resolveDeviceGroupTargets = `query($groupToken: String!, $version: Int, $afterId: String, $limit: Int!) {
  resolveDeviceGroupTargets(groupToken: $groupToken, version: $version, afterId: $afterId, limit: $limit) {
    rejected
    code
    reason
    deviceTokens
    nextCursor
    resolvedVersion
  }
}`

// ResolveGroupTargets implements model.GroupResolver: one page of a group's device members.
//
// A REJECTION (no such group, not a device group, never published) is a decided fact carried on
// the page; a transport or availability failure is an error the caller fails closed on. `rejected`
// is decoded through a pointer because it is non-null in the schema: an answer without it is a
// broken response, and reading it as false would turn it into a resolved, EMPTY group.
func (c *Checker) ResolveGroupTargets(ctx context.Context, groupToken string, version *int32,
	afterCursor string, limit int) (*model.GroupTargetPage, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("device-management group lookup: no tenant in context")
	}
	var out struct {
		ResolveDeviceGroupTargets struct {
			Rejected        *bool    `json:"rejected"`
			Code            *string  `json:"code"`
			Reason          *string  `json:"reason"`
			DeviceTokens    []string `json:"deviceTokens"`
			NextCursor      *string  `json:"nextCursor"`
			ResolvedVersion *int32   `json:"resolvedVersion"`
		} `json:"resolveDeviceGroupTargets"`
	}
	vars := map[string]any{"groupToken": groupToken, "limit": limit, "version": nil, "afterId": nil}
	if version != nil {
		vars["version"] = *version
	}
	if afterCursor != "" {
		vars["afterId"] = afterCursor
	}
	if err := c.client.Query(ctx, c.url, tenant, resolveDeviceGroupTargets, vars, &out); err != nil {
		return nil, fmt.Errorf("device-management group lookup: %w", err)
	}
	page := out.ResolveDeviceGroupTargets
	if page.Rejected == nil {
		return nil, errors.New("device-management group lookup: the answer carries no rejected verdict")
	}
	resolved := &model.GroupTargetPage{Rejected: *page.Rejected, DeviceTokens: page.DeviceTokens,
		ResolvedVersion: page.ResolvedVersion}
	if page.Code != nil {
		resolved.Code = *page.Code
	}
	if page.Reason != nil {
		resolved.Reason = *page.Reason
	}
	if page.NextCursor != nil {
		resolved.NextCursor = *page.NextCursor
	}
	return resolved, nil
}
