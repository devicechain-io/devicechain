// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// Device ownership (ADR-081): which customers a device belongs to, as the scope of a
// member limited to some customers reads it. Ownership is an edge of a TRACKED
// relationship type from the device (decision 5); its scope form is the anchor
// "<target type>:<target token>", the same string the event pipeline stamps on events and
// a scope names. This file holds the two places that turn the graph into that form — the
// alarm stamp at raise, and the ownership-changed fact — so neither can drift from the
// other.

// DeviceOwnershipChangedEvent is the device ownership-changed fact: a device's COMPLETE
// current anchor set, read after the write that may have changed it. A consumer
// overwrites what it holds with it; an empty Anchors means the device is owned by nobody.
// The tenant travels on the per-tenant subject.
type DeviceOwnershipChangedEvent struct {
	DeviceToken string
	Anchors     []string
	ChangedTime time.Time
}

// DeviceOwnershipPublisher publishes ownership-changed facts. Fire-and-forget, like the
// other fact publishers: a failure is the publisher's to log and count, never the write's.
type DeviceOwnershipPublisher interface {
	PublishDeviceOwnershipChanged(ctx context.Context, event *DeviceOwnershipChangedEvent)
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
func (api *Api) deviceScopeAnchors(ctx context.Context, deviceId uint) (rdb.Anchors, error) {
	found, err := api.TrackedRelationshipsForDevice(ctx, deviceId)
	if err != nil {
		return nil, err
	}
	return anchorSetOf(found.Results), nil
}

// ownershipReadContext is the context the ownership fact's reads run under: the tenant of
// the write and NOTHING else of the caller's. The fact is the service's own statement of
// state, so it must not be bounded by — or attributed to — whoever made the write (a
// confused deputy in either direction), and it is published after the write has
// committed, so it must not inherit a request deadline that is about to fire.
func ownershipReadContext(ctx context.Context) (context.Context, bool) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, false
	}
	return core.WithTenant(context.Background(), tenant), true
}

// emitOwnershipChanged publishes, for each given device, its complete current anchor set.
// It is called post-commit by the funnel every edge writer already goes through
// (evictRelationshipSources, evictEntityDelete), which is what makes "every writer"
// true by construction rather than by a list: a write that changes a device's tracked set
// must evict the device's cached set or ingest stamps stale anchors, so a writer that
// forgot this fact would already be a stale-anchor bug. Over-publishing is harmless — the
// fact is a statement of state, not a delta.
//
// A device that no longer exists (a delete raced the read) is skipped: the entity-deleted
// fact is its last word. A read failure is logged and drops the facts of that batch; the
// write stands, and the device's next resolved event carries its anchors anyway.
func (api *Api) emitOwnershipChanged(ctx context.Context, deviceIds []uint) {
	if api.DeviceOwnershipPublisher == nil || len(deviceIds) == 0 {
		return
	}
	svc, ok := ownershipReadContext(ctx)
	if !ok {
		log.Warn().Int("devices", len(deviceIds)).
			Msg("Unable to publish device ownership changes: no tenant on the write's context")
		return
	}
	ids := make([]uint, 0, len(deviceIds))
	for _, id := range deviceIds {
		if id != 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	now := time.Now().UTC()
	for start := 0; start < len(ids); start += rdb.MaxLookupKeys {
		chunk := ids[start:min(start+rdb.MaxLookupKeys, len(ids))]
		facts, err := api.ownershipFacts(svc, chunk, now)
		if err != nil {
			log.Warn().Err(err).Int("devices", len(chunk)).
				Msg("Unable to read device ownership after a relationship change; those devices' ownership facts were not published")
			continue
		}
		for _, f := range facts {
			api.DeviceOwnershipPublisher.PublishDeviceOwnershipChanged(svc, f)
		}
	}
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
	facts := make([]*DeviceOwnershipChangedEvent, 0, len(devices))
	for _, d := range devices {
		facts = append(facts, &DeviceOwnershipChangedEvent{
			DeviceToken: d.Token,
			Anchors:     []string(anchorSetOf(bySource[d.ID])),
			ChangedTime: now,
		})
	}
	return facts, nil
}
