// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// DeviceScopeAnchors returns, for each named device that exists, the anchors a record about
// it is stamped with when another service writes one (ADR-081): one "<type>:<token>" per
// TRACKED relationship whose source is the device, the same set the event resolver
// denormalizes onto every event (deviceAnchors). A member whose access is limited to part of
// the tenant sees such a record when one of these anchors is in its scope, so this is the
// device's CURRENT ownership as the other services record it — commands at enqueue, update
// assignments at assignment.
//
// A device that does not exist is absent from the map; a device with no tracked
// relationship maps to an empty, non-nil list. The two are different answers — "no such
// device" and "a device nobody owns" — and a caller deciding ownership must not read the
// second as the first or the reverse.
//
// 🔑 THE RULE KEYS ON THE TRACKED FLAG, NOT ON A RELATIONSHIP TYPE TOKEN. `assigned` is
// only the default tracked type; any tracked relationship to a customer is ownership.
// Anchors naming other target types (an asset, an area) are emitted too, exactly as on an
// event: a scope names only customers and groups, so they can never match one, and leaving
// them in keeps this the one definition of a device's anchors rather than a second,
// narrower one that could drift from the events'.
//
// A relationship whose TargetToken is empty predates the ADR-044 denormalization; it is
// skipped, as the event resolver skips it, rather than emitted as an anchor no scope can
// name.
//
// The access scopes a device is in (ADR-081) follow its relationship anchors, as
// "group:<token>": a command enqueued to a device in "north" is visible to a member scoped to
// group:north, exactly as it is to the device's customer.
//
// More than rdb.MaxLookupKeys tokens is refused with LIMIT_EXCEEDED.
func (api *Api) DeviceScopeAnchors(ctx context.Context, tokens []string) (map[string][]string, error) {
	devices := make([]*Device, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &devices, "token", tokens); err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(devices))
	if len(devices) == 0 {
		return out, nil
	}
	tokenOf := make(map[uint]string, len(devices))
	ids := make([]uint, 0, len(devices))
	for _, d := range devices {
		tokenOf[d.ID] = d.Token
		ids = append(ids, d.ID)
		out[d.Token] = []string{}
	}
	rels := make([]EntityRelationship, 0)
	tracked := api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", true)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).
		Where("source_type = ?", string(entity.TypeDevice)).
		Where("relationship_type_id IN (?)", tracked).
		Order("entity_relationships.id ASC"), &rels, "source_id", ids); err != nil {
		return nil, err
	}
	groups, err := accessGroupAnchors(api.RDB.DB(ctx), ids)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rels))
	for _, r := range rels {
		device, ok := tokenOf[r.SourceId]
		if !ok || r.TargetToken == "" || r.TargetType == "" {
			continue
		}
		anchor := r.TargetType + ":" + r.TargetToken
		key := device + "\x00" + anchor
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out[device] = append(out[device], anchor)
	}
	for id, anchors := range groups {
		device := tokenOf[id]
		for _, anchor := range anchors {
			if !slices.Contains(out[device], anchor) {
				out[device] = append(out[device], anchor)
			}
		}
	}
	return out, nil
}
