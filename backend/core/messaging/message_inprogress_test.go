// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "testing"

type progressAck struct{ acks, progress int }

func (p *progressAck) Ack() error        { p.acks++; return nil }
func (p *progressAck) InProgress() error { p.progress++; return nil }

type plainAck struct{ acks int }

func (p *plainAck) Ack() error { p.acks++; return nil }

// InProgress reaches an acknowledger that can do it, settles nothing, and is a quiet no-op on a
// message that cannot (a produced message, or an acknowledger without the capability).
func TestInProgressDelegatesToAnAcknowledgerThatSupportsItAndSettlesNothing(t *testing.T) {
	a := &progressAck{}
	m := NewConsumedMessage("s", nil, 1, nil, a)
	if err := m.InProgress(); err != nil {
		t.Fatal(err)
	}
	if a.progress != 1 || a.acks != 0 {
		t.Fatalf("in-progress=%d acks=%d: it must mark progress and ack nothing", a.progress, a.acks)
	}

	p := &plainAck{}
	if err := NewConsumedMessage("s", nil, 1, nil, p).InProgress(); err != nil || p.acks != 0 {
		t.Fatalf("an acknowledger without the capability: err=%v acks=%d", err, p.acks)
	}
	if err := (Message{}).InProgress(); err != nil {
		t.Fatalf("a message with no acknowledger: %v", err)
	}
}
