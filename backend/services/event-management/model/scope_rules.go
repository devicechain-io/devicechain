// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "github.com/devicechain-io/dc-microservice/rdb"

// Scope rules for the event store (ADR-081). A member whose access is limited to some
// customers ("customer:acme") reads an event only when the event was RECORDED while the
// device belonged to one of them — the event_anchors row the persistence worker wrote at
// the time, never the device's current owner. That is the whole of the ownership-period
// rule: anchors are stamped at write and never rewritten, so a reassigned device's history
// stays with the customer it belonged to when it happened.
//
// Every rule below is enforced by core/rdb's scope callback on a gorm read whose context
// carries a scoped caller's claims. A model with no rule is refused to such a caller; that
// is the state MeasurementRollup and StateChangeEvent are left in, on purpose (see the
// notes at the end).

// eventAnchorSet is the event_anchors set table, joined on the anchor's event_id. The
// payload tables join on event_id ALONE: a payload row carries its reading's own instant,
// which is not the anchor's (see anchorKeySubquery).
func eventAnchorSet(keys ...rdb.KeyPair) rdb.AnchorSet {
	return rdb.AnchorSet{
		Model:       &EventAnchor{},
		Keys:        keys,
		KindColumn:  "anchor_type",
		TokenColumn: "anchor_token",
	}
}

var anchorByEventId = rdb.KeyPair{Set: "event_id", Row: "event_id"}

// ScopedBy admits a scoped caller to the base event table through the event's anchors,
// joined on the event's full key (event_id, occurred_time). An anchor row is written with
// its event's own instant (anchorEventKeySubquery relies on the same fact), so the pair is
// exact, and it is the leading columns of uq_event_anchors_idem after the tenant.
func (Event) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorTable(eventAnchorSet(anchorByEventId,
		rdb.KeyPair{Set: "occurred_time", Row: "occurred_time"}))
}

// ScopedBy admits a scoped caller to a location reading through its event's anchors.
func (LocationEvent) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorTable(eventAnchorSet(anchorByEventId))
}

// ScopedBy admits a scoped caller to a measurement through its event's anchors.
func (MeasurementEvent) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorTable(eventAnchorSet(anchorByEventId))
}

// ScopedBy admits a scoped caller to a device alert through its event's anchors.
func (AlertEvent) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorTable(eventAnchorSet(anchorByEventId))
}

// ScopedBy limits a scoped caller to the anchor rows naming an anchor in its scope. An
// event of a device co-owned by two customers carries both anchors; a member of one must
// not learn the other exists, so Event.anchors lists only its own. The anchor-filter
// subqueries the reads build over this table (anchorKeySubquery, anchorEventKeySubquery)
// are filtered the same way, so filtering by an anchor outside the scope matches nothing.
func (EventAnchor) ScopedBy() rdb.ScopeRule {
	return rdb.OwnAnchor("anchor_type", "anchor_token")
}

// StateChangeEvent declares NO scope rule. No resolver reads it, so a rule here
// would be a guard nothing exercises; a scoped read that reaches it is refused, and the
// resolver that first reads it decides its rule (an event_id-only AnchorTable, like the
// payload tables above) along with the test that proves it.
//
// MeasurementRollup declares NO scope rule, deliberately. The continuous aggregate sums
// across every event of a device and carries no anchor dimension, so no predicate on it
// can say which customer a bucket belongs to: a device reassigned mid-bucket has both
// customers' readings in one row. BucketedMeasurements therefore sends a scoped caller to
// the raw path (useMeasurementRollup), and the scope callback refuses any scoped read that
// reaches the rollup anyway.
