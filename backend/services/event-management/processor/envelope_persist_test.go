// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// An event envelope on the resolved stream is persisted member by member through the same
// batch path a message is, and settles as one message: acknowledged when every member is
// settled, left to redeliver whole when one must retry.

// envelopeOf is the resolved events as ONE message on tenant's subject, as the Nth delivery of
// stream sequence seq, whose ack records idx in log.
func envelopeOf(t *testing.T, log *ackLog, idx int, tenant string, seq uint64, delivered int, evs ...resolvedevent.ResolvedEvent) messaging.Message {
	t.Helper()
	members := make([]eventenvelope.Event, len(evs))
	for i := range evs {
		b, err := resolvedevent.MarshalResolvedEvent(&evs[i])
		require.NoError(t, err)
		members[i] = eventenvelope.Event{Index: uint32(i), Bytes: b}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: tenant, SourceSeq: seq}, members)
	require.NoError(t, err)
	m := messaging.NewConsumedMessage("instance1."+tenant+".resolved-events", body, delivered,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, recordingAck{log: log, idx: idx})
	m.StreamSeq = seq
	return m
}

type recordedFailure struct {
	device string
	reason uint
	dedup  string
}

func recordFailures(r *batchRig) func() []recordedFailure {
	var mu sync.Mutex
	var out []recordedFailure
	r.ep.Failed = func(_ string, reason uint, ev resolvedevent.ResolvedEvent, _ error, _, dedup string) {
		mu.Lock()
		defer mu.Unlock()
		out = append(out, recordedFailure{device: ev.SourceDeviceToken, reason: reason, dedup: dedup})
	}
	return func() []recordedFailure {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedFailure(nil), out...)
	}
}

func envelopeEvents(n int) []resolvedevent.ResolvedEvent {
	out := make([]resolvedevent.ResolvedEvent, n)
	for i := range out {
		out[i] = batchEvent(i, true, batchT0)
	}
	return out
}

// Gate (b), the poison member: one member that can never be stored costs its neighbours
// nothing. The other k-1 are stored, the one is recorded as ONE failed event under the id
// fe:<tenant>:<envelope seq>:<member index>, and the envelope is acknowledged once and never
// redelivered.
func TestAPoisonMemberIsRecordedAloneAndTheEnvelopeIsAckedOnce(t *testing.T) {
	r := newBatchRig(t, 16)
	failures := recordFailures(r)
	r.api.failAnchorsFor = "dev-2" // a deterministic refusal: class-22 data error

	r.run([]messaging.Message{envelopeOf(t, r.acks, 0, fenceCostTenant, 77, 1, envelopeEvents(6)...)})

	for _, dev := range []string{"dev-0", "dev-1", "dev-3", "dev-4", "dev-5"} {
		e, m := eventsFor(t, r.db, dev)
		require.EqualValues(t, 1, e, dev)
		require.EqualValues(t, 3, m, dev)
	}
	e, m := eventsFor(t, r.db, "dev-2")
	require.Zero(t, e)
	require.Zero(t, m)
	require.Equal(t, []recordedFailure{{device: "dev-2", reason: uint(resolvedevent.FailureReason_Invalid),
		dedup: "fe:" + fenceCostTenant + ":" + "25" + ":2"}}, failures(), "one record, for the member, under its own id")
	require.Equal(t, map[int]int{0: 1}, r.acks.acked(), "the envelope is acknowledged exactly once")
}

// Gate (b), the stubborn member: a member that fails for a reason that may clear (the store
// refused it, here, and keeps refusing) never blocks the other k-1 and is never redelivered
// forever. The envelope is left unacknowledged and comes back whole; the healthy members are
// written again as no-ops (gate (c): one row each however many deliveries); at the final
// delivery the stubborn member alone is recorded and the envelope acknowledged.
func TestAStubbornMemberIsRetriedWithItsEnvelopeThenRecordedAlone(t *testing.T) {
	r := newBatchRig(t, 16)
	failures := recordFailures(r)
	r.api.failAnchorsFor = "dev-4"
	r.api.anchorErrFor = errors.New("injected: transient refusal")

	for delivery := 1; delivery < messaging.MaxDeliver; delivery++ {
		r.run([]messaging.Message{envelopeOf(t, r.acks, 0, fenceCostTenant, 90, delivery, envelopeEvents(6)...)})
		require.Empty(t, r.acks.acked(), "delivery %d: a member left to retry leaves the envelope unacknowledged", delivery)
		require.Empty(t, failures(), "delivery %d: nothing is recorded before the budget is spent", delivery)
		// The healthy members were stored on the first delivery and are not stored again.
		for _, dev := range []string{"dev-0", "dev-1", "dev-2", "dev-3", "dev-5"} {
			e, m := eventsFor(t, r.db, dev)
			require.EqualValues(t, 1, e, "delivery %d: %s", delivery, dev)
			require.EqualValues(t, 3, m, "delivery %d: %s", delivery, dev)
		}
	}

	r.run([]messaging.Message{envelopeOf(t, r.acks, 0, fenceCostTenant, 90, messaging.MaxDeliver, envelopeEvents(6)...)})
	require.Equal(t, []recordedFailure{{device: "dev-4", reason: uint(resolvedevent.FailureReason_ApiCallFailed),
		dedup: "fe:" + fenceCostTenant + ":2i:4"}}, failures(), "the stubborn member alone is recorded, once")
	require.Equal(t, map[int]int{0: 1}, r.acks.acked(), "and then the envelope is acknowledged")
	for _, dev := range []string{"dev-0", "dev-1", "dev-2", "dev-3", "dev-5"} {
		e, _ := eventsFor(t, r.db, dev)
		require.EqualValues(t, 1, e, dev)
	}
	e, _ := eventsFor(t, r.db, "dev-4")
	require.Zero(t, e)
}

// Gate (c): persisting a whole envelope again - a lost ack, a pod restart - stores nothing a
// second time.
func TestRedeliveringAWholeEnvelopeStoresEachEventOnce(t *testing.T) {
	r := newBatchRig(t, 16)
	for delivery := 1; delivery <= 3; delivery++ {
		r.run([]messaging.Message{envelopeOf(t, r.acks, 0, fenceCostTenant, 5, delivery, envelopeEvents(5)...)})
	}
	for i := 0; i < 5; i++ {
		e, m := eventsFor(t, r.db, batchEvent(i, true, batchT0).SourceDeviceToken)
		require.EqualValues(t, 1, e)
		require.EqualValues(t, 3, m)
	}
	require.Equal(t, map[int]int{0: 3}, r.acks.acked(), "each delivery was acknowledged once it was stored")
}

// An envelope no reader can open is poison: reported once, acknowledged, and costs the others
// nothing.
func TestAnUnreadableEnvelopeIsPoison(t *testing.T) {
	r := newBatchRig(t, 16)
	good := envelopeOf(t, r.acks, 1, fenceCostTenant, 6, 1, envelopeEvents(3)...)
	bad := envelopeOf(t, r.acks, 0, fenceCostTenant, 5, 1, envelopeEvents(3)...)
	bad.Value = append([]byte(nil), bad.Value...)
	bad.Value[len(bad.Value)/2] ^= 0xff // the checksum catches it

	r.run([]messaging.Message{bad, good})

	require.Equal(t, 1, r.invalid)
	require.Equal(t, map[int]int{0: 1, 1: 1}, r.acks.acked())
	e, _ := eventsFor(t, r.db, "dev-0")
	require.EqualValues(t, 1, e, "the good envelope was stored")
}

// An envelope for another tenant than its subject names is poison, not stored under the
// subject's tenant.
func TestAnEnvelopeForAnotherTenantThanItsSubjectIsPoison(t *testing.T) {
	r := newBatchRig(t, 16)
	m := envelopeOf(t, r.acks, 0, "other-tenant", 5, 1, envelopeEvents(3)...)
	m.Subject = "instance1." + fenceCostTenant + ".resolved-events"
	r.run([]messaging.Message{m})
	require.Equal(t, 1, r.invalid)
	require.Equal(t, map[int]int{0: 1}, r.acks.acked())
	require.Zero(t, tenantEvents(t, r.db, fenceCostTenant))
}
