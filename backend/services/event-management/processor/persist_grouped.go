// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// The grouped batch write.
//
// Written one message at a time, a batch of 64 single-reading events is 128 INSERTs, each a
// round trip while the transaction stays open. writeGrouped builds every message's rows in
// memory first, groups them by tenant, and then writes each table ONCE per tenant: one
// alternate-id probe (only when some message carries an alternate id), one events INSERT,
// one INSERT per payload table that has rows, and one event_anchors INSERT. Each INSERT is
// split by rdb.CreateChunked when it would bind too many parameters, and keeps the ON
// CONFLICT arbiter the per-message path uses, so what is stored is what writing the
// messages one at a time stores.
//
// What it keeps from the per-message path, and how:
//
//   - Tenant isolation: every statement of a group binds the context of the group's first
//     message, and every row of the group is stamped with that tenant before the insert,
//     so a row filed under the wrong group is refused by the tenant-scope callback
//     (rdb.ErrTenantMismatch) instead of being written under another tenant.
//   - The erasure fence: the first statement of each group reads it, once per tenant per
//     transaction, as before.
//   - The alternate-id skip: one probe per group, plus an in-batch rule that reproduces
//     what the per-message probe saw of the messages before it in the same transaction —
//     an events row. A later message with the same key (AltIdMatch) as an earlier one is
//     skipped only when the earlier one writes an events row; one with an empty payload
//     writes none and hides nothing.
//   - State changes (and any type this file does not group) are written one at a time
//     through writeEvent, after the groups, so a redelivered state change still skips its
//     anchors (EventPersistenceResults.Deduped). When one of them carries an alternate id,
//     the order between it and a grouped message with the same key would decide which is
//     stored, so such a batch is written one message at a time instead (writeEach).
//
// A failure is blamed on a message wherever one can be named (blame): a message whose rows
// cannot be built is refused before any SQL; the erasure fence refuses a tenant, not a
// message; a statement that carried one message's rows names that message. A statement
// that carried several messages' rows cannot name one, because the database does not say
// which row it refused, so persistBatch writes the same batch again in a new transaction
// one message at a time (writeEach), which names it exactly as the per-message path does.

// errGroupUnattributed marks a grouped statement's failure when the statement carried
// several messages' rows, so no single message can be blamed for it.
var errGroupUnattributed = errors.New("the database refused a statement carrying several events")

// groupedMessage is one message's share of its tenant's rows.
type groupedMessage struct {
	idx     int // index in the batch
	alt     *model.AltIdKey
	rows    model.EventRows
	anchors []*model.EventAnchor
}

// tenantGroup is one tenant's grouped messages in a batch, in batch order.
type tenantGroup struct {
	ctx     context.Context // the tenant's first message's context
	first   int             // the batch index of the tenant's first message
	members []groupedMessage
}

// sequentialMessage is a message written with writeEvent after the groups.
type sequentialMessage struct {
	idx    int
	pevent model.Event
}

// groupedType reports whether writeGrouped writes events of type t in its tenant's group.
func groupedType(t esmodel.EventType) bool {
	return t == esmodel.Location || t == esmodel.Measurement || t == esmodel.Alert
}

// writeEach writes batch on tx one message at a time, each statement bound to its own
// message's context — the per-message path's statements, in batch order. It returns the
// index of the message whose statement failed, with the error.
func (ep *EventPersistenceWorker) writeEach(tx *gorm.DB, batch []pendingEvent) (int, error) {
	for i := range batch {
		p := &batch[i]
		pevent, err := ep.baseEvent(p.ctx, *p.event)
		if err == nil {
			_, err = ep.writeEvent(p.ctx, tx, pevent, *p.event)
		}
		if err != nil {
			// Stop here: on Postgres the transaction is aborted, or rolled back to the
			// savepoint a split insert ran under, and carrying on would commit the
			// messages around a half-written one.
			return i, err
		}
	}
	return -1, nil
}

// writeGrouped writes batch on tx with one statement per tenant per table, then each
// message it does not group on its own (see the file comment). It returns the index of the
// message to blame with the error, or -1 when none can be named.
func (ep *EventPersistenceWorker) writeGrouped(tx *gorm.DB, batch []pendingEvent) (int, error) {
	for i := range batch {
		if e := batch[i].event; !groupedType(e.EventType) && e.AltId != nil {
			return ep.writeEach(tx, batch)
		}
	}
	groups, seq, failedAt, err := ep.groupBatch(batch)
	if err != nil {
		return failedAt, err
	}
	for _, g := range groups {
		if failedAt, err := ep.writeGroup(tx, g); err != nil {
			return failedAt, err
		}
	}
	for _, s := range seq {
		p := &batch[s.idx]
		if _, err := ep.writeEvent(p.ctx, tx, s.pevent, *p.event); err != nil {
			return s.idx, err
		}
	}
	return -1, nil
}

// groupBatch builds every grouped message's rows, without any SQL, and files them by
// tenant in order of each tenant's first message. A message whose rows cannot be built is
// returned as the one to blame, before anything is written.
func (ep *EventPersistenceWorker) groupBatch(batch []pendingEvent) ([]*tenantGroup, []sequentialMessage, int, error) {
	var groups []*tenantGroup
	byTenant := map[string]*tenantGroup{}
	var seq []sequentialMessage
	for i := range batch {
		p := &batch[i]
		pevent, err := ep.baseEvent(p.ctx, *p.event)
		if err != nil {
			return nil, nil, i, err
		}
		if !groupedType(p.event.EventType) {
			seq = append(seq, sequentialMessage{idx: i, pevent: pevent})
			continue
		}
		m := groupedMessage{idx: i}
		if err := buildRows(pevent, *p.event, &m.rows); err != nil {
			return nil, nil, i, err
		}
		m.anchors = anchorRows(pevent.EventId, *p.event)
		// Every row names its tenant before it is grouped, so a row in the wrong group is
		// refused by the tenant-scope callback rather than stamped with the group's tenant.
		for _, r := range m.rows.Parents {
			r.TenantId = p.tenant
		}
		for _, r := range m.rows.Locations {
			r.TenantId = p.tenant
		}
		for _, r := range m.rows.Measurements {
			r.TenantId = p.tenant
		}
		for _, r := range m.rows.Alerts {
			r.TenantId = p.tenant
		}
		for _, r := range m.anchors {
			r.TenantId = p.tenant
		}
		if p.event.AltId != nil {
			m.alt = &model.AltIdKey{AltId: *p.event.AltId, OccurredTime: p.event.OccurredTime}
		}
		g, ok := byTenant[p.tenant]
		if !ok {
			g = &tenantGroup{ctx: p.ctx, first: i}
			byTenant[p.tenant] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, m)
	}
	return groups, seq, -1, nil
}

// buildRows builds a location, measurement or alert message's parent and payload rows into
// rows, refusing a payload that is not of the event's type exactly as writeEvent does.
func buildRows(pevent model.Event, event dmmodel.ResolvedEvent, rows *model.EventRows) error {
	var err error
	switch event.EventType {
	case esmodel.Location:
		payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload)
		if !ok {
			return errPayloadMismatch("location")
		}
		requests, rerr := locationRequests(pevent, *payload)
		if rerr != nil {
			return rerr
		}
		rows.Parents, rows.Locations, err = model.BuildLocationRows(requests)
	case esmodel.Measurement:
		payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
		if !ok {
			return errPayloadMismatch("measurement")
		}
		requests, rerr := measurementRequests(pevent, *payload)
		if rerr != nil {
			return rerr
		}
		rows.Parents, rows.Measurements, err = model.BuildMeasurementRows(requests)
	case esmodel.Alert:
		payload, ok := event.Payload.(*dmmodel.ResolvedAlertsPayload)
		if !ok {
			return errPayloadMismatch("alert")
		}
		rows.Parents, rows.Alerts, err = model.BuildAlertRows(alertRequests(pevent, *payload))
	default:
		return fmt.Errorf("unhandled event type in grouped persistence: %s", event.EventType.String())
	}
	return err
}

// writeGroup writes one tenant's grouped messages: the alternate-id skip, then the parent
// and payload rows of every message not skipped, then their anchors. It returns the batch
// index of the message to blame with the error, or -1 (see blame).
func (ep *EventPersistenceWorker) writeGroup(tx *gorm.DB, g *tenantGroup) (int, error) {
	skipped := make([]bool, len(g.members))

	// The in-batch rule. Written one at a time, a message's probe sees an earlier message
	// of the same transaction only through that message's events row, so an earlier
	// message hides a later one with the same key only when it writes one. An earlier
	// message with an empty payload writes no events row and hides nothing.
	claimed := map[model.AltIdMatch]struct{}{}
	var keys []model.AltIdKey
	var keyed []int // member index of each key
	for j, m := range g.members {
		if m.alt == nil {
			continue
		}
		k := m.alt.Match()
		if _, ok := claimed[k]; ok {
			skipped[j] = true
			logAltIdSkip(m.alt.AltId)
			continue
		}
		if len(m.rows.Parents) > 0 {
			claimed[k] = struct{}{}
		}
		keys = append(keys, *m.alt)
		keyed = append(keyed, j)
	}

	// The probe: one query for every key the rule above did not settle.
	if len(keys) > 0 {
		found, err := ep.Api.EventsExistByAltId(g.ctx, tx, keys)
		if err != nil {
			return blame(g, g.indexes(keyed), err)
		}
		for n, exists := range found {
			if exists {
				skipped[keyed[n]] = true
				logAltIdSkip(keys[n].AltId)
			}
		}
	}

	var rows model.EventRows
	var anchors []*model.EventAnchor
	var rowWriters, anchorWriters []int
	for j, m := range g.members {
		if skipped[j] {
			continue
		}
		if len(m.rows.Parents) > 0 {
			rowWriters = append(rowWriters, m.idx)
			rows.Parents = append(rows.Parents, m.rows.Parents...)
			rows.Locations = append(rows.Locations, m.rows.Locations...)
			rows.Measurements = append(rows.Measurements, m.rows.Measurements...)
			rows.Alerts = append(rows.Alerts, m.rows.Alerts...)
		}
		if len(m.anchors) > 0 {
			anchorWriters = append(anchorWriters, m.idx)
			anchors = append(anchors, m.anchors...)
		}
	}
	if len(rowWriters) > 0 {
		if err := ep.Api.CreateEventRows(g.ctx, tx, &rows); err != nil {
			return blame(g, rowWriters, err)
		}
	}
	if len(anchorWriters) > 0 {
		if err := ep.Api.CreateEventAnchors(g.ctx, tx, anchors); err != nil {
			return blame(g, anchorWriters, err)
		}
	}
	return -1, nil
}

// indexes maps member indexes to batch indexes.
func (g *tenantGroup) indexes(members []int) []int {
	out := make([]int, 0, len(members))
	for _, j := range members {
		out = append(out, g.members[j].idx)
	}
	return out
}

func logAltIdSkip(altId string) {
	log.Info().Str("altId", altId).Msg("Skipping already-persisted event (idempotent redelivery)")
}

// blame names the message to set aside for a grouped statement's failure, given the batch
// indexes of the messages whose rows the statement carried.
//
//   - A lost connection blames nobody, as on the per-message path.
//   - The erasure fence refuses the TENANT, not a message, so the group's first message is
//     named and persistBatch sets aside every message of that tenant with it.
//   - A statement that carried one message's rows names that message.
//   - Otherwise the database refused a row it does not identify, and the error is marked
//     errGroupUnattributed for persistBatch to find the message by writing the batch again
//     one message at a time.
func blame(g *tenantGroup, carried []int, err error) (int, error) {
	switch {
	case rdb.IsConnectionFailure(err):
		return -1, err
	case errors.Is(err, rdb.ErrTenantPurged):
		return g.first, err
	case len(carried) == 1:
		return carried[0], err
	default:
		return -1, fmt.Errorf("%w: %w", errGroupUnattributed, err)
	}
}
