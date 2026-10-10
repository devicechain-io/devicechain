// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Api struct {
	RDB *rdb.RdbManager

	// RollupReadsDisabled, when true, forces every bucketed measurement read onto the
	// raw hypertable instead of the measurement_rollups continuous aggregate (ADR-026
	// kill-switch, set from config in main). The zero value keeps the rollup path on.
	RollupReadsDisabled bool
}

// Create a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// Interface for event management API (used for mocking)
type EventManagementApi interface {
	CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error)
	CreateMeasurementEvent(ctx context.Context, request *MeasurementEventCreateRequest) (*MeasurementEvent, error)
	CreateAlertEvent(ctx context.Context, request *AlertEventCreateRequest) (*AlertEvent, error)

	// Batch creates persist all of a message's events of one type in multi-row
	// INSERTs (ADR-022 E5), as few as the driver's parameter limit allows — one for
	// any message short of thousands of rows (rdb.CreateChunked). They run on the
	// *gorm.DB they are handed so that a caller can supply a transaction-bound handle
	// (see PersistInTx); the tenant-scope create callback fires on every statement,
	// stamping the tenant onto every row.
	CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error)
	CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error)
	CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error)
	CreateStateChangeEvents(ctx context.Context, db *gorm.DB, requests []*StateChangeEventCreateRequest) ([]*StateChangeEvent, int64, error)

	// CreateEventRows writes one tenant's parent and payload rows, built for any number of
	// events, with one statement per table (see EventRows). The persistence writer uses it
	// to write a batch grouped by tenant; the batch creates above are its per-message path.
	CreateEventRows(ctx context.Context, db *gorm.DB, rows *EventRows) error

	// CreateEventAnchors persists an event's anchor set (ADR-013) on the given db
	// handle (a transaction), so the event is queryable by each of the device's
	// tracked-relationship dimensions.
	CreateEventAnchors(ctx context.Context, db *gorm.DB, anchors []*EventAnchor) error

	// DeleteAnchorsForEntity removes event_anchors rows referencing a deleted
	// entity (ADR-044): device deletes match device_token, other entities match
	// (anchor_type, anchor_token). `before` bounds the delete to events older than
	// it (guards token reuse); a zero value is unbounded (the sweep). Idempotent +
	// tenant-scoped. Returns rows removed.
	DeleteAnchorsForEntity(ctx context.Context, entityType string, entityToken string, before time.Time) (int64, error)

	// DistinctAnchorTenants returns every tenant with event_anchors (cross-tenant;
	// needs a system context). The two reads after it return ONE PAGE each of the
	// current tenant's distinct refs — targets by (type, token), sources by device
	// token — resumed by a keyset cursor, so neither loads a whole tenant at once.
	DistinctAnchorTenants(ctx context.Context) ([]string, error)
	DistinctAnchorTargetsAfter(ctx context.Context, afterType, afterToken string) ([]AnchorRef, error)
	DistinctAnchorDeviceTokensAfter(ctx context.Context, after string) ([]AnchorRef, error)

	// PersistInTx runs fn inside a single database transaction whose handle
	// carries the supplied context, so what fn writes — one message's events, or a
	// batch of messages' — commits all-or-nothing (ADR-022 E5).
	PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error

	// EventExistsByAltId reports whether a resolved event with the given
	// alternateId was already persisted for the tenant in context, backing
	// idempotent ingestion of a redelivered message.
	EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error)
	// EventsExistByAltId is EventExistsByAltId for many keys in one query, answering each
	// key in order.
	EventsExistByAltId(ctx context.Context, db *gorm.DB, keys []AltIdKey) ([]bool, error)

	// AnchorsForEvent returns one event's anchor set by its EVENT ID, backing the
	// Event.anchors field. Keyed on the identity because the natural key can name two
	// distinct events; occurred_time is carried only to prune hypertable chunks.
	AnchorsForEvent(ctx context.Context, eventId []byte, occurredTime time.Time) ([]EventAnchor, error)

	Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error)
	LocationEvents(ctx context.Context, criteria EventSearchCriteria) (*LocationEventSearchResults, error)
	MeasurementEvents(ctx context.Context, criteria EventSearchCriteria) (*MeasurementEventSearchResults, error)
	AlertEvents(ctx context.Context, criteria EventSearchCriteria) (*AlertEventSearchResults, error)
}

// PersistInTx opens one transaction whose handle is bound to the supplied
// context. The inserts performed by fn either all commit or all roll back, making
// a message's events atomic (ADR-022 E5) — or a whole batch of messages', when the
// persistence writer commits several in one transaction. Each statement fn makes
// binds the context of a message of the statement's own tenant (db.WithContext) — one
// message's, or, for a statement the writer shares between several messages, the first of
// that tenant's — so the tenant-scope and erasure fence callbacks, which read the tenant
// from the statement, see that tenant however many tenants the transaction carries.
//
// Idempotency on the at-least-once consume path does not depend on the
// transaction: every event id is derived from the event's content and every insert
// carries an ON CONFLICT DO NOTHING arbiter, so a redelivery — with or without an
// alternateId — or a batch written again after a rollback adds nothing twice.
// EventExistsByAltId is a shortcut that skips a known redelivery before its
// inserts, not that guard. It does guard one thing the arbiters cannot: an event
// re-sent with the same alternate id and instant but different content has a
// different event id, so the events insert's arbiter does not absorb it, and
// idx_events_tenant_alt_id would refuse it (23505) if the probe had not skipped it.
func (api *Api) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

// AltIdKey is an event's alternate-id dedup key beyond its tenant: its alternate id and
// the instant it occurred.
type AltIdKey struct {
	AltId        string
	OccurredTime time.Time
}

// AltIdMatch is an AltIdKey as the event store compares it. occurred_time is stored at
// microsecond resolution, and the PostgreSQL driver binds a time.Time as
// Unix()*1e6 + Nanosecond()/1e3, which is exactly time.Time.UnixMicro, so two keys with
// equal AltIdMatch are the same key to the server. It is comparable, so a caller can key a
// map on it; it is the one definition of "the same alternate id" the persistence writer
// uses, here and in its in-batch rule.
type AltIdMatch struct {
	altId string
	us    int64
}

// Match is k as the event store compares it.
func (k AltIdKey) Match() AltIdMatch {
	return AltIdMatch{altId: k.AltId, us: k.OccurredTime.UnixMicro()}
}

// EventsExistByAltId reports, for each key in order, whether an event with that alternate
// id at that instant is already stored for the tenant in ctx (tenant_id is applied by the
// global query callback). It backs idempotent ingestion: a redelivered resolved event is
// detected and skipped rather than double-persisted. db may be a transaction handle, so the
// check and the inserts that follow share one transaction.
//
// It is ONE query whatever the number of keys: the (alt_id, occurred_time) pairs are
// matched in SQL, so the server compares each instant exactly as it compares a single
// key's, and the pairs are bounded by the keys' earliest and latest instants so a
// hypertable read touches only the chunks that cover them. The rows found are matched back
// to the keys at the store's resolution (AltIdMatch).
func (api *Api) EventsExistByAltId(ctx context.Context, db *gorm.DB, keys []AltIdKey) ([]bool, error) {
	out := make([]bool, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	pairs := make([][]any, 0, len(keys))
	lo, hi := keys[0].OccurredTime, keys[0].OccurredTime
	for _, k := range keys {
		pairs = append(pairs, []any{k.AltId, k.OccurredTime})
		if k.OccurredTime.Before(lo) {
			lo = k.OccurredTime
		}
		if k.OccurredTime.After(hi) {
			hi = k.OccurredTime
		}
	}
	var found []struct {
		AltId        string
		OccurredTime time.Time
	}
	if err := db.WithContext(ctx).Model(&Event{}).Select("alt_id", "occurred_time").
		Where("(alt_id, occurred_time) IN ? AND occurred_time >= ? AND occurred_time <= ?", pairs, lo, hi).
		Find(&found).Error; err != nil {
		return nil, err
	}
	stored := make(map[AltIdMatch]struct{}, len(found))
	for _, f := range found {
		stored[AltIdKey{AltId: f.AltId, OccurredTime: f.OccurredTime}.Match()] = struct{}{}
	}
	for i, k := range keys {
		_, out[i] = stored[k.Match()]
	}
	return out, nil
}

// EventExistsByAltId is EventsExistByAltId for one key.
func (api *Api) EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error) {
	found, err := api.EventsExistByAltId(ctx, db, []AltIdKey{{AltId: altId, OccurredTime: occurred}})
	if err != nil {
		return false, err
	}
	return found[0], nil
}

// canonicalPayloadEntry renders one payload row's distinguishing content as deterministic
// bytes for DerivePayloadId. json.Marshal is deterministic HERE because every field below
// belongs to a struct — Go only randomises MAP iteration, and these carry no maps. That is
// load-bearing rather than incidental: the map that does exist upstream
// (UnresolvedMeasurementsEntry.Measurements) has already been expanded into one request per
// entry by the time these rows are built, so its ordering can no longer reach the digest.
//
// 🔴 THAT REASONING IS TRUE HERE AND WAS FALSE ONE LAYER UP, WHICH IS WORTH KNOWING BEFORE
// TRUSTING IT AGAIN. The expansion argument holds for a payload ROW, which is one
// measurement. It did not hold for DeriveEventId, which marshals the WHOLE multi-entry
// payload: there the map's order survived, as slice order, straight into the event id, so
// one reading hashed to a different id on every resolution and a redelivery double-
// persisted the event. The hazard was identified at this layer, solved at this layer, and
// its sibling was left standing — for thirteen days, since both digests landed 2026-08-01
// and the sort landed 2026-08-14. Short, but the map-ranging loop it depended on was much
// older: what was new was a digest that consumed its output.
//
// It is closed now, at the producer; DeriveEventId's own comment holds that contract and is
// the one place to keep current. The rule to carry forward HERE is the local one: "no maps
// in this struct" is only ever a statement about ONE marshal call, and every other call that
// marshals the same data has to be checked on its own.
//
// occurred_time is included because a single message's entries may each carry their own,
// so it discriminates within one event rather than merely repeating the parent's key.
func canonicalPayloadEntry(v any) ([]byte, error) {
	return json.Marshal(v)
}

// upsertParentEvents inserts the parent `events` rows for a batch of child event
// requests (location/measurement/alert) before the children, so a reader resolving a
// payload row's parent by event_id always finds it. (There is no natural-key join left
// to preserve: a payload row carries the SAMPLE's instant while its parent carries the
// message's, so the two agree on occurred_time only for an unbatched event.) The rows
// are deduped on the event's own identity
// and inserted ON CONFLICT DO NOTHING: multiple measurements in one message share a
// single parent event, and a redelivered message re-presents the same key.
//
// The payload tables carry no DB foreign key into `events` — an FK referencing a
// hypertable blocks drop_chunks on the parent (ADR-026 amd) — so parent-first
// ordering is an app-layer invariant this function upholds, not a constraint the
// database enforces. (It also sidesteps GORM's implicit belongs-to upsert, which on
// a composite-primary-key hypertable emitted an `ON CONFLICT DO UPDATE` with no
// inference target — invalid SQL, SQLSTATE 42601.)
//
// The insert is one column-array statement (eventColumns), which binds one array per
// column, so no number of events can make it bind more parameters than the driver accepts.
func upsertParentEvents(ctx context.Context, db *gorm.DB, events []*Event) error {
	if len(events) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(events))
	distinct := make([]*Event, 0, len(events))
	for _, e := range events {
		// Keyed on the event's OWN identity. This used to dedupe on
		// (device_token, event_type, occurred_time), which silently collapsed two
		// DISTINCT events that shared that tuple into one — the in-memory half of the
		// same defect the primary key had.
		key := string(e.EventId)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		distinct = append(distinct, e)
	}
	// The conflict target is the full primary key including tenant_id: a device
	// token is unique only per tenant (ADR-042), so omitting tenant_id here would
	// let one tenant's parent event suppress another tenant's identical-key event.
	// tenant_id is bound from the context by the column-array insert, which also refuses
	// a row naming another tenant, so the value is present when the conflict is evaluated.
	//
	// ON CONFLICT infers the key by its column SET, so the target (eventColumns) need not
	// follow the key's (tenant_id, occurred_time, event_id) order. A pod still running the
	// previous release, whose list is the same set, infers the rebuilt key the same way,
	// which is what keeps a rolling upgrade writing (NewTimeLeadingKeysSchema).
	_, err := eventColumns.Insert(db.WithContext(ctx), distinct)
	return err
}

// ErrZeroEntryTime is the fail-closed rejection for a payload create request whose own
// instant was never set.
//
// 🔴 IT IS AN ERROR RATHER THAN A FALLBACK TO THE PARENT'S TIME, and that is deliberate.
// Falling back would restore, silently and one layer down, exactly the defect this field
// exists to remove: a batch of samples spanning a minute stored at a single instant. The
// value is always available (resolution sets one on every entry), so a zero here is a
// caller that forgot the field, and the loud failure is what makes that a test failure
// instead of a slow corruption of the history table.
//
// 🔴 IT IS A SENTINEL SO THE CALLER CAN CLASSIFY IT AS DETERMINISTIC. The same bytes
// reproduce it on every delivery — a zero instant cannot become non-zero on retry — so
// treating it as transient would burn the message's whole redelivery budget and then
// dead-letter it under the wrong reason. That matters more here than for a plain caller
// bug, because the zero instant is reachable from the wire: it is a valid RFC 3339 string
// ("0001-01-01T00:00:00Z") that happens to be Go's zero time. The device-facing decoder
// refuses it, which is where a device gets told; this is the layer that must not spin if
// one ever arrives by another route.
var ErrZeroEntryTime = errors.New("payload create request carries no entry occurred time")

func errZeroEntryTime(kind string) error {
	return fmt.Errorf("%w: a %s row needs the sample's own instant, which is never inherited "+
		"from the parent event", ErrZeroEntryTime, kind)
}

// Create a new location event.
func (api *Api) CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error) {
	created, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a new measurement event.
func (api *Api) CreateMeasurementEvent(ctx context.Context, request *MeasurementEventCreateRequest) (*MeasurementEvent, error) {
	created, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a new alert event.
func (api *Api) CreateAlertEvent(ctx context.Context, request *AlertEventCreateRequest) (*AlertEvent, error) {
	created, err := api.CreateAlertEvents(ctx, api.RDB.DB(ctx), []*AlertEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// payloadTarget (event_columns.go) is the arbiter every payload table shares: a row's own
// identity.
//
// ON CONFLICT on the row's own identity, for the same reason the parent has one: the
// base-event key cannot cover payload rows, so a redelivery of an event carrying no
// alternateId (every event lwm2m-ingest and sparkplug-ingest produce) used to leave one
// envelope owning N copies of its own rows.
//
// The key is (tenant_id, occurred_time, payload_id); ON CONFLICT infers it by the column
// SET, so the target's order is not the key's, and need not be (NewTimeLeadingKeysSchema).
//
// insertPayloadRows inserts payload rows ON CONFLICT on their identity, in one column-array
// statement. The caller upserts the parent events first; the payload rows relate to them
// by event_id, with no association or foreign key (ADR-026 amd, see events.go).
func insertPayloadRows[R any](ctx context.Context, db *gorm.DB, table *rdb.ColumnTable[R], rows []*R) error {
	_, err := table.Insert(db.WithContext(ctx), rows)
	return err
}

// BuildLocationRows maps requests to their parent events and location rows without
// writing anything. It is the one request-to-row mapping, shared by CreateLocationEvents
// and the persistence writer's grouped batch write. It refuses a request with no entry
// instant (ErrZeroEntryTime) and returns the error DeriveLocationPayloadId returns. The
// rows carry no tenant: the tenant-scope create callback stamps the context's, or refuses
// a row a caller stamped with another.
func BuildLocationRows(requests []*LocationEventCreateRequest) ([]*Event, []*LocationEvent, error) {
	parents := make([]*Event, 0, len(requests))
	rows := make([]*LocationEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, nil, errZeroEntryTime("location")
		}
		parents = append(parents, &request.Event)
		// The row's identity is derived from the frozen preimage in
		// model/payload_identity.go, which is the one definition of it — the live
		// measurement subscription derives the same identity for a reading it streams
		// rather than stores, and two copies of a content-addressed preimage is two
		// answers waiting to disagree.
		payloadId, err := DeriveLocationPayloadId(request)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, &LocationEvent{
			EventId:      request.EventId,
			PayloadId:    payloadId,
			DeviceToken:  request.DeviceToken,
			EventType:    request.EventType,
			OccurredTime: request.EntryOccurredTime,
			Latitude:     rdb.NullFloat64Of(request.Latitude),
			Longitude:    rdb.NullFloat64Of(request.Longitude),
			Elevation:    rdb.NullFloat64Of(request.Elevation),
			Accuracy:     rdb.NullFloat64Of(request.Accuracy),
			Speed:        rdb.NullFloat64Of(request.Speed),
			Heading:      rdb.NullFloat64Of(request.Heading),
		})
	}
	return parents, rows, nil
}

// BuildMeasurementRows is BuildLocationRows for measurement requests.
func BuildMeasurementRows(requests []*MeasurementEventCreateRequest) ([]*Event, []*MeasurementEvent, error) {
	parents := make([]*Event, 0, len(requests))
	rows := make([]*MeasurementEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, nil, errZeroEntryTime("measurement")
		}
		parents = append(parents, &request.Event)
		payloadId, err := DeriveMeasurementPayloadId(request)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, &MeasurementEvent{
			EventId:      request.EventId,
			PayloadId:    payloadId,
			DeviceToken:  request.DeviceToken,
			EventType:    request.EventType,
			OccurredTime: request.EntryOccurredTime,
			Name:         request.Name,
			Value:        rdb.NullFloat64Of(request.Value),
			Classifier:   request.Classifier,
			Unit:         request.Unit,
			DataType:     request.DataType,
		})
	}
	return parents, rows, nil
}

// BuildAlertRows is BuildLocationRows for alert requests.
func BuildAlertRows(requests []*AlertEventCreateRequest) ([]*Event, []*AlertEvent, error) {
	parents := make([]*Event, 0, len(requests))
	rows := make([]*AlertEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, nil, errZeroEntryTime("alert")
		}
		parents = append(parents, &request.Event)
		payloadId, err := DeriveAlertPayloadId(request)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, &AlertEvent{
			EventId:      request.EventId,
			PayloadId:    payloadId,
			DeviceToken:  request.DeviceToken,
			EventType:    request.EventType,
			OccurredTime: request.EntryOccurredTime,
			Type:         request.Type,
			Level:        request.Level,
			Message:      request.Message,
			Source:       request.Source,
		})
	}
	return parents, rows, nil
}

// Create a batch of location events in one column-array INSERT per table (the parents,
// then the rows) on the given db handle (which may be a transaction). The per-row
// request->row mapping is BuildLocationRows; the insert stamps the context's tenant onto
// every slice entry, and refuses a row naming another (rdb.ColumnTable).
func (api *Api) CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error) {
	if len(requests) == 0 {
		return []*LocationEvent{}, nil
	}
	parents, created, err := BuildLocationRows(requests)
	if err != nil {
		return nil, err
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	if err := insertPayloadRows(ctx, db, locationColumns, created); err != nil {
		return nil, err
	}
	return created, nil
}

// Create a batch of measurement events in one column-array INSERT per table, as
// CreateLocationEvents. The per-row request->row mapping is BuildMeasurementRows.
func (api *Api) CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error) {
	if len(requests) == 0 {
		return []*MeasurementEvent{}, nil
	}
	parents, created, err := BuildMeasurementRows(requests)
	if err != nil {
		return nil, err
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	if err := insertPayloadRows(ctx, db, measurementColumns, created); err != nil {
		return nil, err
	}
	return created, nil
}

// Create a batch of alert events in one column-array INSERT per table, as
// CreateLocationEvents. The per-row request->row mapping is BuildAlertRows.
func (api *Api) CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error) {
	if len(requests) == 0 {
		return []*AlertEvent{}, nil
	}
	parents, created, err := BuildAlertRows(requests)
	if err != nil {
		return nil, err
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	if err := insertPayloadRows(ctx, db, alertColumns, created); err != nil {
		return nil, err
	}
	return created, nil
}

// EventRows is every parent and payload row a group of ONE tenant's events writes, built
// without touching the database (Build*Rows). A caller that builds it for several events
// must give every row that tenant's TenantId: the column-array insert then refuses
// a row filed under the wrong group (rdb.ErrTenantMismatch) instead of stamping it with the
// context's.
type EventRows struct {
	Parents      []*Event
	Locations    []*LocationEvent
	Measurements []*MeasurementEvent
	Alerts       []*AlertEvent
}

// CreateEventRows writes rows on db under the tenant in ctx: the parents first
// (deduplicated on their event id, as upsertParentEvents always has), then each payload
// table that has rows. It makes one column-array INSERT per table (rdb.ColumnTable),
// whatever the number of rows, and every statement keeps its ON CONFLICT arbiter.
func (api *Api) CreateEventRows(ctx context.Context, db *gorm.DB, rows *EventRows) error {
	if err := upsertParentEvents(ctx, db, rows.Parents); err != nil {
		return err
	}
	if len(rows.Locations) > 0 {
		if err := insertPayloadRows(ctx, db, locationColumns, rows.Locations); err != nil {
			return err
		}
	}
	if len(rows.Measurements) > 0 {
		if err := insertPayloadRows(ctx, db, measurementColumns, rows.Measurements); err != nil {
			return err
		}
	}
	if len(rows.Alerts) > 0 {
		if err := insertPayloadRows(ctx, db, alertColumns, rows.Alerts); err != nil {
			return err
		}
	}
	return nil
}

// Create a new state change event. Returns the created row.
func (api *Api) CreateStateChangeEvent(ctx context.Context, request *StateChangeEventCreateRequest) (*StateChangeEvent, error) {
	created, _, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a batch of state change events in multi-row INSERTs of at most
// rdb.RowsPerInsert rows each (rdb.CreateChunked), all or nothing on the given db handle
// (which may be a transaction). Unlike the other event tables the child rows
// are inserted ON CONFLICT DO NOTHING against the idempotency unique index
// (tenant_id, device_token, occurred_time, state, session_id): a StateChange carries
// no AltId, so the base-event dedup does not engage, and a persist-commit-then-crash-
// before-ack JetStream redelivery would otherwise write a duplicate presence row
// (phantom flapping). A birth+death at one instant differ by state and both survive;
// a late higher-session echo differs by session_id and is retained. The key omits
// reason by design — a producer MUST make each distinct transition distinct in
// (occurred_time, state, session_id); two rows colliding there are the same edge.
//
// Returns the rows and the RowsAffected count, summed over the statements: 0 means the
// batch fully deduped (the caller then skips anchor persistence, which has no idempotency
// of its own).
func (api *Api) CreateStateChangeEvents(ctx context.Context, db *gorm.DB, requests []*StateChangeEventCreateRequest) ([]*StateChangeEvent, int64, error) {
	if len(requests) == 0 {
		return []*StateChangeEvent{}, 0, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*StateChangeEvent, 0, len(requests))
	for _, request := range requests {
		parents = append(parents, &request.Event)
		created = append(created, &StateChangeEvent{
			EventId:      request.EventId,
			DeviceToken:  request.DeviceToken,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			State:        request.State,
			Reason:       request.Reason,
			SessionId:    request.SessionId,
		})
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, 0, err
	}
	result := rdb.CreateChunked(db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "occurred_time"}, {Name: "state"}, {Name: "session_id"}},
		DoNothing: true,
	}), &created)
	if result.Error != nil {
		return nil, 0, result.Error
	}
	return created, result.RowsAffected, nil
}

// CreateEventAnchors persists an event's anchor rows on the given db handle (a
// transaction). Anchors follow the same dedup policy as the events they index:
// an alternateId-bearing event is skipped before it reaches here on redelivery,
// and an event without one is re-persisted along with its anchors.
//
// The insert is an UPSERT, not the plain insert this comment used to describe: the
// body below conflicts on uq_event_anchors_idem, which was added precisely because a
// re-persisted event re-presented its whole anchor set. The "plain insert" claim
// outlived the change that falsified it — the same way the sibling claim in
// EventPersistenceResults.Deduped did.
//
// The insert is one column-array statement (anchorColumns), so no number of anchors can
// make it bind more parameters than the database driver accepts.
func (api *Api) CreateEventAnchors(ctx context.Context, db *gorm.DB, anchors []*EventAnchor) error {
	if len(anchors) == 0 {
		return nil
	}
	// An anchor set is idempotent on (event_id, anchor_type, anchor_token): one event is
	// anchored to a given target at most once, so the columns already ARE the identity and
	// no derived digest is needed here — unlike the payload tables, whose rows carry no
	// naturally unique column.
	//
	// 🔴 This closes the last leg of the same defect. persistEventAnchors is skipped only
	// when results.Deduped, which ONLY the state-change path ever sets, so for every other
	// event type a redelivery re-inserted the whole anchor set — and event_anchors carried
	// no unique index to stop it. The in-tree comment on that skip already warned "a plain
	// re-insert would duplicate the anchor set"; it was right, and it only guarded one of
	// the four paths that reach here.
	//
	// The key is (tenant_id, occurred_time, event_id, anchor_type, anchor_token); ON
	// CONFLICT infers it by the column SET, so anchorColumns' order need not match it.
	_, err := anchorColumns.Insert(db.WithContext(ctx), anchors)
	return err
}

// DeleteAnchorsForEntity removes event_anchors rows referencing a deleted entity
// (ADR-044 cross-service RI). A deleted device is the SOURCE of its anchors
// (matched by device_token); a deleted anchor target (customer / area / asset and
// their groups) is matched by (anchor_type, anchor_token). The entity is named by
// its stable per-tenant token, carried on the entity.deleted event. Idempotent —
// deleting already-absent rows is a no-op — and tenant-scoped via the fail-closed
// callback (the caller stamps the tenant from the event's subject). Returns rows
// removed.
//
// `before` bounds the cleanup to anchors of events that occurred strictly before it
// (ADR-044 decision-4 amendment, "token = stable identity"): a token freed on delete
// can be reused (ADR-042), so a redelivered/replayed deletion event must not wipe the
// anchors of a NEW device that later adopted the same token — those events are newer
// than the deletion. A zero `before` means unbounded: the reconciliation sweep passes
// it because the sweep only deletes when the token resolves to no entity at all, so
// every matching anchor is a true orphan.
func (api *Api) DeleteAnchorsForEntity(ctx context.Context, entityType string, entityToken string, before time.Time) (int64, error) {
	db := api.RDB.DB(ctx).Model(&EventAnchor{})
	if entityType == string(entity.TypeDevice) {
		// A device is always the anchor SOURCE (device_token), but it can ALSO be an
		// anchor target: a tracked device→device relationship (e.g. gateway→sensor)
		// records rows with (anchor_type="device", anchor_token=other). Clean both, or
		// a deleted device leaves dangling target rows on its trackers' events.
		db = db.Where("device_token = ? OR (anchor_type = ? AND anchor_token = ?)",
			entityToken, string(entity.TypeDevice), entityToken)
	} else {
		// Every other entity type is only ever an anchor target (never a source).
		db = db.Where("anchor_type = ? AND anchor_token = ?", entityType, entityToken)
	}
	if !before.IsZero() {
		db = db.Where("occurred_time < ?", before)
	}
	result := db.Delete(&EventAnchor{})
	return result.RowsAffected, result.Error
}
