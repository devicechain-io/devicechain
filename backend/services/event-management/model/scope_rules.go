// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm/schema"
)

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
// subqueries do NOT read this model: they read eventAnchorKey, because this rule hides the
// shared area row a filter has to find.
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

// eventAnchorKey is the event_anchors table as the ANCHOR-FILTER subqueries read it
// (anchorKeySubquery, anchorEventKeySubquery). It is not a second table and is never
// migrated or written: it exists to carry a different scope rule from EventAnchor.
//
// Why two rules over one table. EventAnchor's rule (OwnAnchor) is right for LISTING an
// event's anchors to a scoped caller: it shows only the caller's own, so a co-owner or the
// shared area is never disclosed. Used as the FILTER it is wrong: asking for events on
// area:floor-shared searches for the row (area, floor-shared), which OwnAnchor hides from
// every customer-scoped caller, so the answer was empty with no error.
//
// 🔴 THE RULE HERE IS THE TENANT'S PREDICATE ALONE, ADMITTED ONLY INSIDE A SUBQUERY (KeyFilterOnly), AND THAT IS SAFE ONLY
// BECAUSE OF WHAT THE SUBQUERY IS FOR. It selects event KEYS and nothing else: nothing it
// returns is shown to the caller, and the OUTER read (events, measurement_events,
// location_events, alert_events) keeps its own AnchorTable rule, which admits only events
// carrying one of the caller's scope anchors. The filter is therefore always
// "events on this area/asset" INTERSECTED with "events this caller owns", enforced where
// the rows are read; every count and total is that intersection too. Do not read
// the subquery's rows anywhere else, and the model is unexported and its rule refuses any scoped
// statement that is not a subquery, so a direct read fails closed.
//
// The spec's first design joined the subquery to itself on the event key, to require an
// in-scope anchor on the filter side as well. It was built and measured, and dropped: it is
// redundant with the outer rule (a mutant that removed it changed no result) and, under the
// generic plans the pgx statement cache settles on, Postgres planned it as a nested loop
// over a materialized intersection, quadratic in the caller's events (a customer with 270k
// events on a 5,000-device area did not finish in 40 s; without the join the same read
// takes under a second). See the PR for the numbers.
//
// TableName takes the namer so gorm applies the functional-area prefix exactly as it does
// to the EventAnchor this mirrors (an explicit string would bypass the prefix).
type eventAnchorKey struct {
	rdb.TenantScoped
	EventId      []byte
	OccurredTime time.Time
	AnchorType   string
	AnchorToken  string
}

// TableName is event_anchors, derived through the namer like EventAnchor's own.
func (eventAnchorKey) TableName(namer schema.Namer) string { return namer.TableName("EventAnchor") }

// ScopedBy is the tenant's predicate alone; see the type's comment for why that is safe.
func (eventAnchorKey) ScopedBy() rdb.ScopeRule { return rdb.KeyFilterOnly() }

// checkAnchorFilter decides, from the caller's claims alone, whether a scoped caller may
// filter by the requested anchor. An unscoped caller, and a request with no anchor, pass.
//
//   - area and asset are free: the filter row may be a shared place, and the outer read's
//     own rule already limits the rows to the caller's devices' events.
//   - customer and group must be IN the caller's scope. Otherwise filtering by
//     customer:globex would reveal which of Acme's events Globex co-owns. A customer that
//     exists and one that does not are refused alike (ErrForbidden), because the decision
//     never touches the data.
//   - any other kind has no scoped semantics here and is refused (ErrScopeUnsupported),
//     never answered empty. A kind added to the entity vocabulary later is refused until
//     it is classified in this switch.
func checkAnchorFilter(ctx context.Context, anchorType, anchorToken *string) error {
	if anchorType == nil || anchorToken == nil {
		return nil
	}
	scope, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return nil
	}
	switch entity.Type(*anchorType) {
	case entity.TypeArea, entity.TypeAsset:
		return nil
	case entity.TypeCustomer, entity.TypeGroup:
		for _, a := range scope.Anchors() {
			if a == *anchorType+":"+*anchorToken {
				return nil
			}
		}
		return auth.ErrForbidden
	default:
		return auth.ErrScopeUnsupported
	}
}
