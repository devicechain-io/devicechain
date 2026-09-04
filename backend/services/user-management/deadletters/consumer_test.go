// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingAck struct{ acks int }

func (r *recordingAck) Ack() error { r.acks++; return nil }

func testConsumer(t *testing.T, s *Store) *Consumer {
	t.Helper()
	c := &Consumer{
		store:      s,
		stored:     prometheus.NewCounter(prometheus.CounterOpts{Name: "stored_total"}),
		unstorable: prometheus.NewCounter(prometheus.CounterOpts{Name: "unstorable_total"}),
	}
	c.procCtx, c.procCancel = context.WithCancel(context.Background())
	t.Cleanup(c.procCancel)
	return c
}

func letterMsg(t *testing.T, subject string, e deadletter.Envelope, seq uint64,
	ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := deadletter.Marshal(e)
	require.NoError(t, err)
	return messaging.NewConsumedMessage(subject, b, 1, nil, ack)
}

func counterOf(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

func TestALetterIsStoredUnderTheTenantFromItsSubject(t *testing.T) {
	s := testStore(t)
	c := testConsumer(t, s)
	ack := &recordingAck{}
	e := envelope(deadletter.KindNotification, "alarm-1", time.Now().UTC())

	msg := letterMsg(t, "inst.acme.dead-letters", e, 0, ack)
	c.handle(msg)

	page, err := s.List(context.Background(), SearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	assert.Equal(t, "acme", page.Results[0].TenantId,
		"the tenant comes from the subject, which is the only field a producer cannot forge")
	assert.Equal(t, "alarm-1", page.Results[0].Reference)
	assert.Equal(t, 1, ack.acks)
	assert.Equal(t, float64(1), counterOf(t, c.stored))
}

// 🔴 A MESSAGE THAT CAN NEVER BE MADE SENSE OF IS ACKED, NOT RETRIED. No redelivery gives a
// subject a tenant it never had or makes a body parse, so leaving it unacked would park
// the whole store's consumer behind something that will never move.
func TestAnUnintelligibleLetterIsAckedAndCounted(t *testing.T) {
	for name, msg := range map[string]messaging.Message{
		"no tenant in subject": messaging.NewConsumedMessage("dead-letters", []byte(`{}`), 1, nil, nil),
		"not an envelope": messaging.NewConsumedMessage("inst.acme.dead-letters",
			[]byte("not json"), 1, nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			c := testConsumer(t, s)
			ack := &recordingAck{}
			m := msg
			m = messaging.NewConsumedMessage(m.Subject, m.Value, 1, nil, ack)

			c.handle(m)

			assert.Equal(t, 1, ack.acks, "an unintelligible letter must be acked, not parked")
			assert.Equal(t, float64(1), counterOf(t, c.unstorable))
			assert.Equal(t, float64(0), counterOf(t, c.stored))
		})
	}
}

// 🔴 A LETTER THAT COULD NOT BE STORED IS LEFT UNACKED, and this consumer is the one place
// in the platform where that is right. Everywhere else giving up at the cap is safe
// BECAUSE the giving-up is recorded; this is where the recording happens, so a give-up
// here is the failure with nothing behind it.
func TestALetterThatCannotBeStoredIsLeftUnacked(t *testing.T) {
	s := testStore(t)
	// Drop the table out from under it: the store then fails the way a database that is
	// away fails, which is the case this disposition exists for.
	require.NoError(t, s.db(context.Background()).Migrator().DropTable(&DeadLetter{}))
	c := testConsumer(t, s)
	ack := &recordingAck{}

	c.handle(letterMsg(t, "inst.acme.dead-letters",
		envelope(deadletter.KindNotification, "a", time.Now().UTC()), 0, ack))

	assert.Equal(t, 0, ack.acks,
		"a letter that could not be stored was acked, so the record of a failure is gone")
	assert.Equal(t, float64(0), counterOf(t, c.stored))
	assert.Equal(t, float64(0), counterOf(t, c.unstorable),
		"a storage failure is not the same as an unintelligible message and must not be counted as one")
}

// The counterweight for the test above: a storable letter IS acked. Without it a consumer
// that never acked anything would satisfy the assertion there.
func TestAStorableLetterIsAcked(t *testing.T) {
	s := testStore(t)
	c := testConsumer(t, s)
	ack := &recordingAck{}
	c.handle(letterMsg(t, "inst.acme.dead-letters",
		envelope(deadletter.KindNotification, "a", time.Now().UTC()), 0, ack))
	assert.Equal(t, 1, ack.acks)
}

func TestTheSweeperPrunesOnItsWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.Record(ctx, "acme", 1, envelope(deadletter.KindNotification, "old", now.Add(-40*24*time.Hour))))
	require.NoError(t, s.Record(ctx, "acme", 2, envelope(deadletter.KindNotification, "new", now)))

	sw := &Sweeper{store: s, retention: 30 * 24 * time.Hour}
	sw.RunOnce(ctx)

	page, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	assert.Equal(t, "new", page.Results[0].Reference)
}

// A sweep that cannot run must not take the loop down with it — the next tick tries again.
func TestASweepThatFailsDoesNotPanic(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.db(context.Background()).Migrator().DropTable(&DeadLetter{}))
	sw := &Sweeper{store: s, retention: time.Hour}
	assert.NotPanics(t, func() { sw.RunOnce(context.Background()) })
	_ = errors.New("")
}
