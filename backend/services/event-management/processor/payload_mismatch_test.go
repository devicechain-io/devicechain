// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/mock"
)

// Matching payloads for the four event types PersistEvent type-asserts. Built fresh
// per case so a test can not mutate another's fixture.
func matchingPayloadFor(etype esmodel.EventType) interface{} {
	switch etype {
	case esmodel.Location:
		lat, lon := "33.7490", "-84.3880"
		return &dmmodel.ResolvedLocationsPayload{
			Entries: []dmmodel.ResolvedLocationEntry{{Latitude: &lat, Longitude: &lon}},
		}
	case esmodel.Measurement:
		return &dmmodel.ResolvedMeasurementsPayload{
			Entries: []dmmodel.ResolvedMeasurementsEntry{{
				Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "101.5"}},
			}},
		}
	case esmodel.Alert:
		return &dmmodel.ResolvedAlertsPayload{
			Entries: []dmmodel.ResolvedAlertEntry{{Type: "engine.overheat", Level: 3, Message: "hot", Source: "ecu"}},
		}
	case esmodel.StateChange:
		return &dmmodel.ResolvedStateChangePayload{State: "DISCONNECTED", Reason: "lifetime-lapse", SessionId: 42}
	}
	return nil
}

// A MockApi with every batch create wired to succeed, so the ONLY thing that can make
// PersistEvent fail in these tests is the payload type assertion itself.
func persistingMockApi() *emtest.MockApi {
	api := new(emtest.MockApi)
	api.Mock.On("CreateLocationEvents", mock.Anything, mock.Anything).Return([]*model.LocationEvent{{}}, nil)
	api.Mock.On("CreateMeasurementEvents", mock.Anything, mock.Anything).Return([]*model.MeasurementEvent{{}}, nil)
	api.Mock.On("CreateAlertEvents", mock.Anything, mock.Anything).Return([]*model.AlertEvent{{}}, nil)
	api.Mock.On("CreateStateChangeEvents", mock.Anything, mock.Anything).Return([]*model.StateChangeEvent{{}}, int64(1), nil)
	api.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
	return api
}

// A payload whose Go type does not match its event type is as deterministic as a
// failure gets: the same bytes reproduce the same mismatch on every delivery. The
// dispatch loop in Process classifies with errors.Is(err, ErrDeterministic) — anything
// unwrapped falls to the default branch and is treated as TRANSIENT, so the event is
// left unacked and redelivered until messaging.MaxDeliver, then misfiled as an
// ApiCallFailed rather than as invalid data. So each of the four type-asserting cases
// must wrap ErrDeterministic.
//
// The table is deliberately one row per case in PersistEvent's switch: a fifth event
// type that gains a payload assertion without the wrapper shows up here as a missing
// row rather than as silence.
func TestPayloadTypeMismatchIsDeterministic(t *testing.T) {
	tests := []struct {
		name string
		// etype is the declared event type; wrongPayload is a payload of a DIFFERENT
		// concrete type, chosen so the case's assertion fails.
		etype        esmodel.EventType
		wrongPayload interface{}
	}{
		{
			name:         "location event carrying a measurements payload",
			etype:        esmodel.Location,
			wrongPayload: matchingPayloadFor(esmodel.Measurement),
		},
		{
			name:         "measurement event carrying a locations payload",
			etype:        esmodel.Measurement,
			wrongPayload: matchingPayloadFor(esmodel.Location),
		},
		{
			name:         "alert event carrying a state change payload",
			etype:        esmodel.Alert,
			wrongPayload: matchingPayloadFor(esmodel.StateChange),
		},
		{
			name:         "state change event carrying an alerts payload",
			etype:        esmodel.StateChange,
			wrongPayload: matchingPayloadFor(esmodel.Alert),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worker := &EventPersistenceWorker{Api: persistingMockApi()}
			// Tenant-scoped, as every production caller is: PersistEvent fails closed
			// with ErrNoTenant on a bare context, which would never reach the switch.
			ctx := core.WithTenant(context.Background(), "acme")

			_, err := worker.PersistEvent(ctx, *buildResolvedEvent(tc.etype, tc.wrongPayload))
			if err == nil {
				t.Fatalf("expected a mismatched payload to fail persistence, got nil error")
			}
			if !errors.Is(err, ErrDeterministic) {
				t.Fatalf("expected a mismatched payload to be a DETERMINISTIC failure (dead-lettered on "+
					"first delivery); an unwrapped error is classified transient and burns the whole "+
					"MaxDeliver budget. got: %v", err)
			}
		})
	}
}

// Counterweight to the table above: a payload whose type DOES match its event type must
// not be classified as a deterministic payload mismatch. Without this, a mutation that
// returned ErrDeterministic unconditionally would satisfy every row of the table.
//
// The assertion is deliberately narrow — not "must succeed", which would pin unrelated
// persistence behaviour — but "must not be a deterministic failure".
func TestMatchingPayloadIsNotDeterministicFailure(t *testing.T) {
	for _, etype := range []esmodel.EventType{
		esmodel.Location, esmodel.Measurement, esmodel.Alert, esmodel.StateChange,
	} {
		t.Run(etype.String(), func(t *testing.T) {
			worker := &EventPersistenceWorker{Api: persistingMockApi()}
			ctx := core.WithTenant(context.Background(), "acme")

			_, err := worker.PersistEvent(ctx, *buildResolvedEvent(etype, matchingPayloadFor(etype)))
			if errors.Is(err, ErrDeterministic) {
				t.Fatalf("a payload of the CORRECT type for a %s event must not be classified as a "+
					"deterministic payload mismatch, got: %v", etype.String(), err)
			}
		})
	}
}

// The SQLSTATE class-22 floor: a value the database itself rejects can never
// succeed on redelivery, so it dead-letters on the first failure.
//
// 🔑 This exists because the decode-time range check is not, and cannot be, complete
// cover. It lives in the JSON decoder, so it guards every device-facing JSON event
// and nothing that arrives another way: the LwM2M and Sparkplug adapters build their
// payload structs directly and marshal protobuf, and measurement values are not
// range-checked anywhere at all. Without this floor such a value is returned
// unwrapped, read as transient, and redelivered until its budget is gone — the exact
// poison loop the range check was written to prevent, on every path the decoder does
// not sit on.
func TestDatabaseValueRejectionIsDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want bool
	}{
		{"numeric value out of range", "22003", true},
		{"invalid text representation", "22P02", true},
		{"string data right truncation", "22001", true},
		// 🔴 The counterweight, and the reason the test is a class prefix rather than
		// a list. These are recoverable: a deadlock or a serialization failure
		// succeeds on the very next delivery, and filing one as deterministic
		// DISCARDS the event instead of merely delaying it. Widening the prefix to
		// swallow them would pass every case above and lose data in production.
		{"serialization failure", "40001", false},
		{"deadlock detected", "40P01", false},
		{"unique violation", "23505", false},
		{"connection failure", "08006", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPersistFailure(&pgconn.PgError{Code: tc.code, Message: "boom"})
			if errors.Is(got, ErrDeterministic) != tc.want {
				t.Fatalf("SQLSTATE %s: deterministic=%v, want %v (err: %v)",
					tc.code, !tc.want, tc.want, got)
			}
		})
	}

	// A nil error stays nil, and an already-deterministic error is not re-wrapped
	// into a misleading "the database rejected the value" message.
	if got := classifyPersistFailure(nil); got != nil {
		t.Fatalf("nil must stay nil, got %v", got)
	}
	original := fmt.Errorf("%w: %q is not numeric", ErrDeterministic, "abc")
	if got := classifyPersistFailure(original); got != original {
		t.Fatalf("an already-deterministic error must pass through unchanged, got %v", got)
	}

	// 🔑 The case the guard actually exists for, and the one a weaker version of this
	// test missed: an error that is BOTH already deterministic AND a driver error.
	// Without the short-circuit it is wrapped a second time and the reader is told
	// "the database rejected the value" on top of the real reason. Asserted by
	// identity, so double-wrapping is caught even though both spellings would still
	// satisfy errors.Is.
	both := fmt.Errorf("%w: while persisting: %w", ErrDeterministic,
		&pgconn.PgError{Code: "22003", Message: "numeric field overflow"})
	if got := classifyPersistFailure(both); got != both {
		t.Fatalf("an already-deterministic driver error must not be re-wrapped, got %v", got)
	}

	// A plain error the driver did not produce is left on the retry path: an
	// unrecognized failure is treated as recoverable, because that mistake is
	// recoverable and the opposite one is not.
	plain := errors.New("connection reset by peer")
	if errors.Is(classifyPersistFailure(plain), ErrDeterministic) {
		t.Fatal("an unrecognized error must NOT be classified deterministic")
	}
}
