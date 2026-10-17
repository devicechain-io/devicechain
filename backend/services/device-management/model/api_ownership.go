// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Device ownership (ADR-081): which customers a device belongs to, as the scope of a
// member limited to some customers reads it. Ownership is an edge of a TRACKED
// relationship type from the device (decision 5); its scope form is the anchor
// "<target type>:<target token>", the same string the event pipeline stamps on events and
// a scope names. This file holds the two places that turn the graph into that form — the
// alarm stamp at raise, and the ownership-changed fact — so neither can drift from the
// other.

// DeviceOwnershipChangedEvent is the device ownership-changed fact: a device's COMPLETE
// current anchor set — its tracked relationships and the access scopes it is in — read after
// the write that may have changed it. A consumer
// overwrites what it holds with it; an empty Anchors means the device is owned by nobody.
// The tenant travels on the per-tenant subject.
type DeviceOwnershipChangedEvent struct {
	DeviceToken string
	Anchors     []string
	ChangedTime time.Time

	// deviceId is the device's row id, which the drain matches the fact to its queued row
	// by. It is not part of the fact.
	deviceId uint
}

// DeviceOwnershipPublisher publishes ownership-changed facts for the ownership drain
// (ownership_outbox.go). Unlike the other fact publishers it REPORTS a failure: the drain
// deletes a queued fact only once it has been published, so a failure it was not told of
// would be a fact lost.
type DeviceOwnershipPublisher interface {
	PublishDeviceOwnershipChanged(ctx context.Context, event *DeviceOwnershipChangedEvent) error
}

// scopeAnchorOf renders a tracked edge as a scope anchor, or "" for an edge that cannot
// be one (a row predating the target-token denormalization, see deviceAnchors).
func scopeAnchorOf(r *EntityRelationship) string {
	if r.TargetType == "" || r.TargetToken == "" {
		return ""
	}
	return r.TargetType + ":" + r.TargetToken
}

// anchorSetOf folds a device's tracked edges into a sorted, de-duplicated anchor set.
// Never nil, so a device with no tracked edge stamps '{}' rather than NULL — the two
// read the same to a scope, but an empty set says "checked, owned by nobody".
func anchorSetOf(edges []EntityRelationship) rdb.Anchors {
	out := rdb.Anchors{}
	for i := range edges {
		if a := scopeAnchorOf(&edges[i]); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// deviceScopeAnchors returns the device's current anchors: one per tracked relationship.
// It reads through the plain Api, never a cache, so a stamp is never older than the write
// it follows.
//
// The access scopes the device is in are part of the set (ADR-081): "group:<token>" for
// each, read from the group read-model in the same way.
func (api *Api) deviceScopeAnchors(ctx context.Context, deviceId uint) (rdb.Anchors, error) {
	found, err := api.TrackedRelationshipsForDevice(ctx, deviceId)
	if err != nil {
		return nil, err
	}
	groups, err := accessGroupAnchors(api.RDB.DB(ctx), []uint{deviceId})
	if err != nil {
		return nil, err
	}
	return withGroupAnchors(anchorSetOf(found.Results), groups[deviceId]), nil
}

// ownershipFacts reads the current anchor sets of a batch of devices in two statements.
func (api *Api) ownershipFacts(ctx context.Context, ids []uint, now time.Time) ([]*DeviceOwnershipChangedEvent, error) {
	type deviceRow struct {
		ID    uint
		Token string
	}
	devices := make([]deviceRow, 0, len(ids))
	if err := api.RDB.DB(ctx).Model(&Device{}).Select("id", "token").
		Where("id IN ?", ids).Order("id").Scan(&devices).Error; err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	edges := make([]EntityRelationship, 0)
	if err := api.RDB.DB(ctx).Model(&EntityRelationship{}).
		Where("source_type = ? AND source_id IN ?", string(entity.TypeDevice), ids).
		Where("relationship_type_id IN (?)",
			api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", true)).
		Find(&edges).Error; err != nil {
		return nil, err
	}
	bySource := make(map[uint][]EntityRelationship, len(devices))
	for _, e := range edges {
		bySource[e.SourceId] = append(bySource[e.SourceId], e)
	}
	groups, err := accessGroupAnchors(api.RDB.DB(ctx), ids)
	if err != nil {
		return nil, err
	}
	facts := make([]*DeviceOwnershipChangedEvent, 0, len(devices))
	for _, d := range devices {
		facts = append(facts, &DeviceOwnershipChangedEvent{
			DeviceToken: d.Token,
			Anchors:     []string(withGroupAnchors(anchorSetOf(bySource[d.ID]), groups[d.ID])),
			ChangedTime: now,
			deviceId:    d.ID,
		})
	}
	return facts, nil
}
