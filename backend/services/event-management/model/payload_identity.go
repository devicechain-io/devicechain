// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"
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

// DeriveMeasurementPayloadId computes the payload_id of one measurement row from its
// parent event and the reading's own content.
//
// This is the one with two callers — the persist path in model/api.go, and the live
// subscription, which builds a request for a reading it is about to stream rather than
// store — so it is the reason the other two moved here with it.
func DeriveMeasurementPayloadId(request *MeasurementEventCreateRequest) ([]byte, error) {
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
func DeriveEventIdForPayload(tenantId string, event *Event, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing payload for the event identity: %w", err)
	}
	return DeriveEventId(tenantId, event, encoded), nil
}
