// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// GroupResolver resolves an entity group to the devices it collects, at device-management
// — the service that owns groups. It satisfies command-delivery's model.GroupTargetResolver.
type GroupResolver struct {
	client *svcclient.Client
	url    string
}

// NewGroupResolver binds a group resolver to a service client and device-management's
// GraphQL endpoint URL.
func NewGroupResolver(client *svcclient.Client, graphqlURL string) *GroupResolver {
	return &GroupResolver{client: client, url: graphqlURL}
}

// resolveDeviceGroupTargets walks a group's device members by KEYSET cursor. Offset paging
// would silently skip any member that stays in the group while an earlier one leaves, and
// a device skipped by a fleet write lands in no refusal list and is recorded nowhere.
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

// ResolveGroupTargets fetches one page of a group's device members.
//
// The two failure modes stay distinct, as everywhere else on this seam: a REJECTION (the
// group does not exist, collects something other than devices, was never published) is a
// decided fact carried on the page, while a transport or availability failure is a plain
// error the caller fails closed on. If both arrived as errors, an unusable group would be
// indistinguishable from device-management being down — and the caller would report a
// fixable authoring mistake as a platform outage.
//
// `rejected` is decoded through a pointer for the same reason the single validator's
// `allowed` is: the schema declares it non-null, so an absent value means a broken or
// intercepted response rather than "not rejected". Reading absence as false would treat a
// garbled response as a successfully resolved — and therefore EMPTY — group, which
// command-delivery would then record as a fleet write that legitimately reached nobody.
func (r *GroupResolver) ResolveGroupTargets(ctx context.Context, groupToken string,
	version *int32, afterCursor string, limit int) (*model.GroupTargetPage, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
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
	vars := map[string]any{
		"groupToken": groupToken,
		"limit":      limit,
	}
	if version != nil {
		vars["version"] = *version
	} else {
		vars["version"] = nil
	}
	// The first page carries no cursor. Sent as null rather than "" because the owner
	// reads a present-but-empty cursor as a malformed one.
	if afterCursor != "" {
		vars["afterId"] = afterCursor
	} else {
		vars["afterId"] = nil
	}
	if err := r.client.Query(ctx, r.url, tenant, resolveDeviceGroupTargets, vars, &out); err != nil {
		return nil, err
	}

	page := out.ResolveDeviceGroupTargets
	if page.Rejected == nil {
		return nil, fmt.Errorf("verify: device-management returned no group target verdict")
	}
	resolved := &model.GroupTargetPage{
		Rejected:        *page.Rejected,
		DeviceTokens:    page.DeviceTokens,
		ResolvedVersion: page.ResolvedVersion,
	}
	// The owner's own classification is relayed rather than re-derived: it owns the
	// reasons a group cannot be commanded, and a caller that can only read the prose
	// cannot tell "not a device group" from "never published" without parsing it.
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
