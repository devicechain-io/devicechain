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

	// Batch creates persist all of a message's events of one type in a single
	// multi-row INSERT (ADR-022 E5). They run on the *gorm.DB they are handed so
	// that a caller can supply a transaction-bound handle (see PersistInTx); the
	// tenant-scope create callback fires on the batch destination, stamping the
	// tenant onto every row.
	CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error)
	CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error)
	CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error)
	CreateStateChangeEvents(ctx context.Context, db *gorm.DB, requests []*StateChangeEventCreateRequest) ([]*StateChangeEvent, int64, error)

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
	// needs a system context). DistinctAnchorRefs returns the current tenant's
	// distinct entity references. Both feed the reconciliation sweep (ADR-044).
	DistinctAnchorTenants(ctx context.Context) ([]string, error)
	DistinctAnchorRefs(ctx context.Context) ([]AnchorRef, error)

	// PersistInTx runs fn inside a single database transaction whose handle
	// carries the supplied (tenant-scoped) context, so a message's events are
	// committed all-or-nothing (ADR-022 E5).
	PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error

	// EventExistsByAltId reports whether a resolved event with the given
	// alternateId was already persisted for the tenant in context, backing
	// idempotent ingestion of a redelivered message.
	EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error)

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
// context so the tenant-scope GORM callbacks (which read the tenant from the
// statement context) still fire on every statement inside the transaction. The
// inserts performed by fn either all commit or all roll back, making a single
// message's events atomic (ADR-022 E5).
//
// This makes a message's writes all-or-nothing. Idempotency on the at-least-once
// consume path is layered on top via EventExistsByAltId: a redelivered resolved
// event carrying an alternateId is detected and skipped inside the transaction
// (PersistEvent), with the (tenant_id, alt_id, occurred_time) partial unique index
// as the race backstop. Events without an alternateId are still re-inserted on
// redelivery — supplying a stable alternateId is what opts an event into dedup.
func (api *Api) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

// EventExistsByAltId reports whether an event with the given alternateId already
// exists for the tenant in context at occurred (the dedup key components beyond
// tenant_id, which the global query callback applies). It backs idempotent
// ingestion: a redelivered resolved event is detected and skipped rather than
// double-persisted. db may be a transaction handle so the check and the inserts
// that follow share one transaction.
func (api *Api) EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).Model(&Event{}).
		Where("alt_id = ? AND occurred_time = ?", altId, occurred).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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
	// tenant_id is stamped onto each row by the tenant-scope create callback before
	// the insert, so the value is present when the conflict is evaluated.
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "event_id"}, {Name: "occurred_time"}},
		DoNothing: true,
	}).Create(distinct).Error
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

// Create a batch of location events in a single multi-row INSERT on the given
// db handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateLocationEvent; tenant scoping is applied by the global
// tenant-scope create callback, which stamps the tenant onto every slice entry.
func (api *Api) CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error) {
	if len(requests) == 0 {
		return []*LocationEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*LocationEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, errZeroEntryTime("location")
		}
		parents = append(parents, &request.Event)
		// The row's identity is derived from the frozen preimage in
		// model/payload_identity.go, which is the one definition of it — the live
		// measurement subscription derives the same identity for a reading it streams
		// rather than stores, and two copies of a content-addressed preimage is two
		// answers waiting to disagree.
		payloadId, cerr := DeriveLocationPayloadId(request)
		if cerr != nil {
			return nil, cerr
		}
		created = append(created, &LocationEvent{
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
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; the child rows relate to the base event by
	// event_id — no association / foreign key (ADR-026 amd, see events.go).
	//
	// ON CONFLICT on the row's own identity, for the same reason the parent has one: the
	// base-event key cannot cover payload rows, so a redelivery of an event carrying no
	// alternateId (every event lwm2m-ingest and sparkplug-ingest produce) used to leave one
	// envelope owning N copies of its own rows.
	result := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
		DoNothing: true,
	}).Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a batch of measurement events in a single multi-row INSERT on the given
// db handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateMeasurementEvent; tenant scoping is applied by the global
// tenant-scope create callback.
func (api *Api) CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error) {
	if len(requests) == 0 {
		return []*MeasurementEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*MeasurementEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, errZeroEntryTime("measurement")
		}
		parents = append(parents, &request.Event)
		payloadId, cerr := DeriveMeasurementPayloadId(request)
		if cerr != nil {
			return nil, cerr
		}
		created = append(created, &MeasurementEvent{
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
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; the child rows relate to the base event by
	// event_id — no association / foreign key (ADR-026 amd, see events.go).
	//
	// ON CONFLICT on the row's own identity, for the same reason the parent has one: the
	// base-event key cannot cover payload rows, so a redelivery of an event carrying no
	// alternateId (every event lwm2m-ingest and sparkplug-ingest produce) used to leave one
	// envelope owning N copies of its own rows.
	result := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
		DoNothing: true,
	}).Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a batch of alert events in a single multi-row INSERT on the given db
// handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateAlertEvent; tenant scoping is applied by the global
// tenant-scope create callback.
func (api *Api) CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error) {
	if len(requests) == 0 {
		return []*AlertEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*AlertEvent, 0, len(requests))
	for _, request := range requests {
		if request.EntryOccurredTime.IsZero() {
			return nil, errZeroEntryTime("alert")
		}
		parents = append(parents, &request.Event)
		payloadId, cerr := DeriveAlertPayloadId(request)
		if cerr != nil {
			return nil, cerr
		}
		created = append(created, &AlertEvent{
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
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; the child rows relate to the base event by
	// event_id — no association / foreign key (ADR-026 amd, see events.go).
	//
	// ON CONFLICT on the row's own identity, for the same reason the parent has one: the
	// base-event key cannot cover payload rows, so a redelivery of an event carrying no
	// alternateId (every event lwm2m-ingest and sparkplug-ingest produce) used to leave one
	// envelope owning N copies of its own rows.
	result := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
		DoNothing: true,
	}).Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a new state change event. Returns the created row.
func (api *Api) CreateStateChangeEvent(ctx context.Context, request *StateChangeEventCreateRequest) (*StateChangeEvent, error) {
	created, _, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a batch of state change events in a single multi-row INSERT on the given db
// handle (which may be a transaction). Unlike the other event tables the child rows
// are inserted ON CONFLICT DO NOTHING against the idempotency unique index
// (tenant_id, device_token, occurred_time, state, session_id): a StateChange carries
// no AltId, so the base-event dedup does not engage, and a persist-commit-then-crash-
// before-ack JetStream redelivery would otherwise write a duplicate presence row
// (phantom flapping). A birth+death at one instant differ by state and both survive;
// a late higher-session echo differs by session_id and is retained. The key omits
// reason by design — a producer MUST make each distinct transition distinct in
// (occurred_time, state, session_id); two rows colliding there are the same edge.
//
// Returns the rows and the RowsAffected count: 0 means the batch fully deduped (the
// caller then skips anchor persistence, which has no idempotency of its own).
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
	result := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "occurred_time"}, {Name: "state"}, {Name: "session_id"}},
		DoNothing: true,
	}).Create(&created)
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
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"}, {Name: "event_id"}, {Name: "occurred_time"},
			{Name: "anchor_type"}, {Name: "anchor_token"},
		},
		DoNothing: true,
	}).Create(anchors).Error
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
