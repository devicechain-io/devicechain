// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"fmt"
	"testing"
)

// erasedRefusal stands in for rdb.ErrTenantPurged, which this package's tests may not link
// (database_free_test.go): it answers TenantErased the way the real one does. The real
// sentinel is driven through Dispose by core/rdb's fence_disposition_test.go.
type erasedRefusal struct{ answer bool }

func (e *erasedRefusal) Error() string      { return "this tenant has been deleted" }
func (e *erasedRefusal) TenantErased() bool { return e.answer }

// The disposition is decided by value, for a refusal wrapped the ways the services wrap it, on
// every delivery from the first to past the cap.
func TestDispose(t *testing.T) {
	sentinel := &erasedRefusal{answer: true}
	transient := errors.New("connection reset")
	// The shape a fence that cannot be read is reported in: it is NOT the refusal.
	unreadable := fmt.Errorf("reading the erasure fence: %w", errors.New("no such table: purged_tenants"))
	refusal := fmt.Errorf("%w (tenant %q)", sentinel, "gone")
	for _, tc := range []struct {
		name      string
		err       error
		delivered int
		want      Disposition
	}{
		{"refusal, first delivery", refusal, 1, TenantErased},
		{"refusal, final delivery: still not lettered", refusal, MaxDeliver, TenantErased},
		{"refusal, past the cap", refusal, MaxDeliver + 1, TenantErased},
		{"bare sentinel", sentinel, 1, TenantErased},
		{"refusal wrapped twice", fmt.Errorf("tenant gone: %w", fmt.Errorf("merge: %w", refusal)), 2, TenantErased},
		{"refusal joined with another error", errors.Join(transient, refusal), 1, TenantErased},
		// An error that has the method but answers false is not the refusal.
		{"a marker answering false", &erasedRefusal{answer: false}, 1, Retry},
		{"unreadable fence, first delivery", unreadable, 1, Retry},
		{"unreadable fence, final delivery", unreadable, MaxDeliver, Exhausted},
		{"transient, first delivery", transient, 1, Retry},
		{"transient, one short of the cap", transient, MaxDeliver - 1, Retry},
		{"transient, final delivery", transient, MaxDeliver, Exhausted},
		{"transient, past the cap", transient, MaxDeliver + 1, Exhausted},
		// A synthetic message (NumDelivered 0) is below the cap like a first delivery.
		{"transient, synthetic message", transient, 0, Retry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{Subject: "instance1.gone.resolved-events", NumDelivered: tc.delivered}
			if got := Dispose(msg, tc.err); got != tc.want {
				t.Errorf("Dispose(delivery %d, %v) = %v; want %v", tc.delivered, tc.err, got, tc.want)
			}
		})
	}
}

// An error that merely READS like the refusal is not the refusal: recognition is by what the
// error answers, not its text, so a message cannot be settled by an error that only quotes one.
func TestDisposeDoesNotMatchTheRefusalByText(t *testing.T) {
	quoted := errors.New((&erasedRefusal{answer: true}).Error())
	if got := Dispose(Message{NumDelivered: 1}, quoted); got != Retry {
		t.Errorf("Dispose(an error quoting the refusal) = %v; want retry", got)
	}
}

// Asking about a success is a defect in the caller, and it fails loudly rather than
// answering a plausible "retry".
func TestDisposeRefusesANilError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Dispose(nil) returned; want a panic")
		}
	}()
	Dispose(Message{NumDelivered: 1}, nil)
}

// A released hold is never judged: even on the final delivery Dispose answers Retry, so a
// consumer cannot letter and ack a message whose only fault was a shutdown during an outage.
func TestDisposeNeverExhaustsAReleasedHold(t *testing.T) {
	msg := Message{NumDelivered: MaxDeliver}
	if got := Dispose(msg, fmt.Errorf("apply: %w", ErrHoldReleased)); got != Retry {
		t.Fatalf("Dispose(released, last delivery) = %v; want Retry", got)
	}
}
