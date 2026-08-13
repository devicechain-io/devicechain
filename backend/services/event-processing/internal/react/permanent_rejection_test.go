// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// erroringSink fails every Send with a canned error.
type erroringSink struct {
	err   error
	calls int
}

func (s *erroringSink) Send(context.Context, CommandRequest) error {
	s.calls++
	return s.err
}

// TestPermanentRejectionIsDroppedNotRetried pins the fast-fail branch.
//
// The failure it replaces was live: every createCommand rejection arrived as an error
// string, so a command for a DELETED device was retried until the consumer's poison
// cap gave up — spending the entire redelivery budget re-asking a question that had
// already been answered correctly, and looking from the outside exactly like
// command-delivery being down.
//
// Done, not Retry, is the assertion. The dispatcher's only other non-retry disposition
// is to keep the event unacked, and an event that can never succeed must not hold a
// consumer slot until a cap expires.
func TestPermanentRejectionIsDroppedNotRetried(t *testing.T) {
	sink := &erroringSink{err: &PermanentRejection{
		Code: "DEVICE_NOT_FOUND", Reason: `device "device-1" does not exist`,
	}}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: sendCmdRule("setMode", ""), found: true}, sink, nil, nil, nil, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("a permanently-rejected command must be DROPPED (Done), got %v; "+
			"retrying it burns the whole redelivery budget on a verdict that cannot change", out)
	}
	if m.permanent["sendCommand"] != 1 {
		t.Fatalf("a permanent rejection must be COUNTED (it is an authoring defect an operator has "+
			"to see, not a silent drop): %+v", m.permanent)
	}
	if m.dispatched["sendCommand"] != 0 {
		t.Fatalf("a dropped command must not be counted as dispatched: %+v", m.dispatched)
	}
}

// TestPermanentRejectionIsRecognizedThroughAWrap proves the branch survives a sink
// that adds context to the rejection. errors.As, not a type assertion: a sink is
// entitled to wrap ("react: enqueue command %q: %w"), and a type assertion would
// quietly fall through to Retry the first time one did — reintroducing the bug with
// no test failing.
func TestPermanentRejectionIsRecognizedThroughAWrap(t *testing.T) {
	inner := &PermanentRejection{Code: "COMMAND_NOT_IN_VOCABULARY", Reason: "no such command"}
	sink := &erroringSink{err: fmt.Errorf("react: enqueue command %q: %w", "setMode", inner)}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: sendCmdRule("setMode", ""), found: true}, sink, nil, nil, nil, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("a WRAPPED permanent rejection must still be dropped, got %v", out)
	}
	if m.permanent["sendCommand"] != 1 {
		t.Fatalf("the wrapped rejection was not counted: %+v", m.permanent)
	}
}

// TestTransientSinkFailureStillRetries is the counterweight, and it is the more
// important half.
//
// Fast-failing is only safe while everything NOT positively classified as permanent
// still retries. A transient rejection (the tenant's held-command ceiling — the
// tenant is full now and drains as its fleet returns), a code this build does not
// recognize, and a plain transport error must all leave the event unacked. A drop
// here is an actuation lost with no retry and no record beyond a log line.
func TestTransientSinkFailureStillRetries(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"a transport failure", errors.New("command-delivery unreachable")},
		{
			name: "a rejection this build cannot classify as permanent",
			err: fmt.Errorf("react: enqueue command %q for device %q rejected (%s): %s",
				"setMode", "device-1", "HELD_CEILING_EXCEEDED", "the tenant is already holding 10000 commands"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &erroringSink{err: tc.err}
			m := newFakeMetrics()
			d := NewDispatcher(fakeResolver{rule: sendCmdRule("setMode", ""), found: true}, sink, nil, nil, nil, m)

			if out := d.Dispatch(context.Background(), evt()); out != Retry {
				t.Fatalf("want Retry, got %v; dropping a failure that is not a typed permanent "+
					"rejection loses a real actuation", out)
			}
			if m.permanent["sendCommand"] != 0 {
				t.Fatalf("a retryable failure must not be counted as a permanent rejection: %+v", m.permanent)
			}
		})
	}
}
