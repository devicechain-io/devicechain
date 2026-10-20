// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity"
)

// The canonical preimage of each payload table's row identity, and of the base event's,
// in one place.
//
// 🔴 EACH PREIMAGE HAS EXACTLY ONE DEFINITION, WHICH IS THE POINT OF THIS FILE. They
// used to be written inline at the single site that built rows (model/api.go), which was
// correct while there was one site. There is now a second: the live measurement
// subscription names a streamed reading, and it has to name it with the SAME identity the
// persisted row will carry or one reading has two ids. A preimage copied to a second site
// is a preimage that eventually differs at one of them, and the divergence is invisible —
// both sides compute a well-formed digest, they simply stop agreeing, so a redelivery
// misses the idempotency index and inserts a duplicate row.
//
// 🔴 THE FIELD NAMES AND THEIR DECLARATION ORDER ARE FROZEN. json.Marshal writes the
// names into the preimage and emits fields in declaration order, so renaming or
// reordering one changes every stored row's payload_id. That is a data migration, not a
// refactor: the same reading redelivered across the deploy boundary hashes differently,
// misses the idempotency index, and inserts a duplicate.
//
// The structs stay anonymous and inline rather than becoming named types, deliberately.
// A named type is something an editor offers to rename and a linter offers to tidy; an
// anonymous struct under this comment is not. Each one is pinned by a test that hashes a
// LITERAL JSON document rather than re-deriving the document from these structs — a
// fixture built from the thing under test cannot notice that thing moving.

// DeriveLocationPayloadId computes the payload_id of one location row from its parent
// event and the stored fields of the fix.
//
// Every stored field takes part, including the three that arrived later. A fix is not
// identified by position alone: two readings at the same coordinates with different
// reported accuracy or heading are different readings, and leaving those out of the hash
// would let ON CONFLICT DO NOTHING silently swallow the second one.
//
// The VALUES are frozen as well as the names, and Occurred changed once: it used to be
// the envelope's time and is now the sample's own. An event published before that change
// and redelivered after it hashes differently and inserts a duplicate row. The window is
// one redelivery interval, it was taken knowingly pre-GA, and it is recorded here so the
// next value change is recognised for what it is.
func DeriveLocationPayloadId(request *LocationEventCreateRequest) ([]byte, error) {
	entry, err := canonicalPayloadEntry(struct {
		Lat, Lon, Elev           *float64
		Accuracy, Speed, Heading *float64
		Occurred                 time.Time
	}{request.Latitude, request.Longitude, request.Elevation,
		request.Accuracy, request.Speed, request.Heading, request.EntryOccurredTime})
	if err != nil {
		return nil, fmt.Errorf("canonicalizing a LocationEvent for its payload identity: %w", err)
	}
	return DerivePayloadId(request.EventId, entry), nil
}

// CanonicalEntryInstant is the one definition of a measurement row's instant as the
// system stores and identifies it: UTC, truncated to the microseconds a timestamptz holds.
//
// TRUNCATE, NEVER ROUND, AND NEVER LEAVE IT TO THE DRIVER. Live, a reading's instant is
// nanosecond-precise and may carry a zone offset; what is stored (and read back by anything
// that re-derives a row, such as a history backfill) is microseconds. A preimage hashed from
// the live value and one hashed from the stored value are then different strings for the
// same reading, so one sample could hold two derived rows. Both the derived payload id and
// the stored occurred_time go through this function, so the id a row has when it is built
// is the id recomputed from what was stored. The raw payload id deliberately does NOT: raw
// ids are never recomputed, and changing them would make a redelivered raw row insert again.
func CanonicalEntryInstant(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// DeriveMeasurementPayloadId computes the payload_id of one measurement row from its
// parent event and the reading's own content.
//
// This is the one with two callers — the persist path in model/api.go, and the live
// subscription, which builds a request for a reading it is about to stream rather than
// store — so it is the reason the other two moved here with it.
//
// A CALCULATED value (request.Derived, ADR-082) has a value-free preimage instead: the
// literal "derived", its name and the sample's instant, under the parent event — no Value,
// Classifier, Unit or DataType. A calculated value is a function of the sample and of the
// formula that computed it, and the formula can change between two copies of one reading (a
// profile published between a device's resend and its original). Keyed on its value, the
// second copy would store a second row; keyed on (event, name, instant), the first value
// written for a reading is the one kept and nothing can add another. The instant is the
// CanonicalEntryInstant (UTC, microseconds), so the id is reproducible from the stored row.
// A row stored before that change with a sub-microsecond or non-UTC instant holds the old
// id; redelivered after the deploy, it inserts once more. The raw branch below is
// untouched, and a raw row's id is exactly what it always was.
func DeriveMeasurementPayloadId(request *MeasurementEventCreateRequest) ([]byte, error) {
	if request.Derived {
		entry, err := canonicalPayloadEntry(struct {
			Kind     string
			Name     string
			Occurred time.Time
		}{"derived", request.Name, CanonicalEntryInstant(request.EntryOccurredTime)})
		if err != nil {
			return nil, fmt.Errorf("canonicalizing a derived MeasurementEvent for its payload identity: %w", err)
		}
		return DerivePayloadId(request.EventId, entry), nil
	}
	entry, err := canonicalPayloadEntry(struct {
		Name           string
		Value          *float64
		Classifier     *uint
		Unit, DataType *string
		Occurred       time.Time
	}{request.Name, request.Value, request.Classifier, request.Unit, request.DataType,
		request.EntryOccurredTime})
	if err != nil {
		return nil, fmt.Errorf("canonicalizing a MeasurementEvent for its payload identity: %w", err)
	}
	return DerivePayloadId(request.EventId, entry), nil
}

// DeriveAlertPayloadId computes the payload_id of one alert row from its parent event and
// the alert's own content. Message and Source are in the preimage rather than
// (event_id, type) alone, because a second SENSOR_FAULT carrying a different message is a
// different alert — see DerivePayloadId for why a natural column does not discriminate.
func DeriveAlertPayloadId(request *AlertEventCreateRequest) ([]byte, error) {
	entry, err := canonicalPayloadEntry(struct {
		Type     string
		Level    uint32
		Message  string
		Source   string
		Occurred time.Time
	}{request.Type, request.Level, request.Message, request.Source, request.EntryOccurredTime})
	if err != nil {
		return nil, fmt.Errorf("canonicalizing a AlertEvent for its payload identity: %w", err)
	}
	return DerivePayloadId(request.EventId, entry), nil
}

// DeriveEventIdForPayload computes a base event's identity from its envelope and the
// whole resolved payload it carried, canonicalizing that payload the way the persistence
// worker does.
//
// It exists so the persist path and the live subscription cannot disagree about how a
// payload becomes bytes. DeriveEventId's stated precondition still applies and is still
// the PRODUCER's: the payload must already be canonical, because json.Marshal of a slice
// preserves slice order and nothing here can know which slices carry meaning in theirs.
//
// A payload that has an identity view (eventidentity.Viewer; for measurements, ResolvedMeasurementsPayload.IdentityView) is hashed through it: its calculated (Derived)
// values are left out (ADR-082), so the id is a function of what the device sent and one
// reading keeps one id across a formula change. A payload with no calculated value is
// hashed as is — byte for byte the document it was hashed as before calculated metrics
// existed, which is what every stored event's id was derived from. Both callers come
// through here, so neither can forget the filter.
func DeriveEventIdForPayload(tenantId string, event *Event, payload any) ([]byte, error) {
	encoded, err := json.Marshal(eventidentity.Payload(payload))
	if err != nil {
		return nil, fmt.Errorf("canonicalizing payload for the event identity: %w", err)
	}
	return DeriveEventId(tenantId, event, encoded), nil
}
