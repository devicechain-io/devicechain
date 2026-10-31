// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/stretchr/testify/require"
)

type factAck struct{ acked int }

func (a *factAck) Ack() error { a.acked++; return nil }

// recordingApplier records what the consumer asked of the projection, under which context.
type recordingApplier struct {
	calls   int
	tenant  string
	scoped  bool
	claims  bool
	device  string
	anchors []string
	changed time.Time
	err     error
}

func (r *recordingApplier) ApplyDeviceOwnership(ctx context.Context, device string, anchors []string, changed time.Time) (bool, error) {
	r.calls++
	r.tenant, _ = core.TenantFromContext(ctx)
	_, r.scoped = auth.ScopeFromContext(ctx)
	_, r.claims = auth.ClaimsFromContext(ctx)
	r.device, r.anchors, r.changed = device, anchors, changed
	return r.err == nil, r.err
}

func factMessage(t *testing.T, subject string, delivered int, ack *factAck) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDeviceOwnershipChangedEvent(&dmmodel.DeviceOwnershipChangedEvent{DeviceToken: "pump-1",
		Anchors: []string{"customer:globex", "area:north"}, ChangedTime: time.Date(2026, 10, 16, 9, 1, 0, 0, time.UTC)})
	require.NoError(t, err)
	return messaging.NewConsumedMessage(subject, b, delivered, nil, ack)
}

// A fact is applied under the TENANT OF ITS SUBJECT and nothing else — no caller's claims,
// so no scope — with exactly the device, owners and change time it carries, and acked.
func TestAnOwnershipFactIsAppliedAsTheProjectionsOwnWrite(t *testing.T) {
	api := &recordingApplier{}
	ack := &factAck{}
	(&OwnershipProcessor{Api: api}).handle(context.Background(), factMessage(t, "instance1.acme-tenant.device-ownership", 1, ack))
	require.Equal(t, 1, api.calls)
	require.Equal(t, "acme-tenant", api.tenant)
	require.False(t, api.scoped, "the consumer's write must not be bounded by anybody's scope")
	require.False(t, api.claims, "the consumer's write must carry no caller")
	require.Equal(t, "pump-1", api.device)
	require.Equal(t, []string{"customer:globex", "area:north"}, api.anchors)
	require.True(t, api.changed.Equal(time.Date(2026, 10, 16, 9, 1, 0, 0, time.UTC)))
	require.Equal(t, 1, ack.acked)
}

// A fact no redelivery can fix is acked and dropped without a write; a failed write takes the
// platform's disposition — left for redelivery, then given up on at the last delivery, and
// settled at once for an erased tenant.
func TestOwnershipFactDispositions(t *testing.T) {
	cases := []struct {
		name      string
		subject   string
		payload   []byte
		delivered int
		err       error
		wantCalls int
		wantAcked int
	}{
		{name: "no tenant in the subject", subject: "nonsense", delivered: 1, wantCalls: 0, wantAcked: 1},
		{name: "a payload that will not decode", subject: "instance1.t.device-ownership", payload: []byte{0xff, 0x01},
			delivered: 1, wantCalls: 0, wantAcked: 1},
		{name: "a transient failure is left for redelivery", subject: "instance1.t.device-ownership", delivered: 1,
			err: errors.New("connection refused"), wantCalls: 1, wantAcked: 0},
		{name: "the last delivery gives up", subject: "instance1.t.device-ownership", delivered: messaging.MaxDeliver,
			err: errors.New("connection refused"), wantCalls: 1, wantAcked: 1},
		{name: "an erased tenant is settled", subject: "instance1.t.device-ownership", delivered: 1,
			err: fmt.Errorf("write: %w", rdb.ErrTenantPurged), wantCalls: 1, wantAcked: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &recordingApplier{err: c.err}
			ack := &factAck{}
			msg := factMessage(t, c.subject, c.delivered, ack)
			if c.payload != nil {
				msg = messaging.NewConsumedMessage(c.subject, c.payload, c.delivered, nil, ack)
			}
			(&OwnershipProcessor{Api: api}).handle(context.Background(), msg)
			require.Equal(t, c.wantCalls, api.calls)
			require.Equal(t, c.wantAcked, ack.acked)
		})
	}
}

// ownersOf reads the event's anchors in the form a scope names, keeps an empty set as a
// stamp of its own, drops an anchor missing either half, and takes the event's RECEIPT time
// as the stamp's precedence — never its occurred time, which the device chose.
func TestAResolvedEventCarriesItsOwnersStamp(t *testing.T) {
	occurred := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	received := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	got := ownersOf(&resolvedevent.ResolvedEvent{OccurredTime: occurred, ProcessedTime: received, Anchors: []resolvedevent.ResolvedAnchor{
		{AnchorType: "customer", AnchorToken: "acme"}, {AnchorType: "area", AnchorToken: "north"},
		{AnchorType: "", AnchorToken: "orphan"}, {AnchorType: "customer", AnchorToken: ""},
	}})
	require.Equal(t, []string{"customer:acme", "area:north"}, got.Anchors)
	require.True(t, got.At.Equal(received), "precedence is the receipt time, got %v", got.At)

	none := ownersOf(&resolvedevent.ResolvedEvent{OccurredTime: occurred, ProcessedTime: received})
	require.NotNil(t, none.Anchors, "owned by nobody is a stamp, not an absent one")
	require.Empty(t, none.Anchors)
	require.True(t, none.At.Equal(received))
}

// outageApplier refuses ApplyDeviceOwnership with err for the first `failures` calls.
type outageApplier struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (o *outageApplier) ApplyDeviceOwnership(context.Context, string, []string, time.Time) (bool, error) {
	if o.calls.Add(1) <= o.failures {
		return false, o.err
	}
	return true, nil
}

func holdFor(store *holdtest.Store) *storehold.Holder {
	return &storehold.Holder{Name: "test", Probe: store.Probe, Tune: holdtest.FastTuning()}
}

func finishes(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// 🔴 A DROPPED OWNERSHIP FACT DOES NOT REPAIR ITSELF for a device that was sold and reports
// nothing: it stays in its old owner's scoped view. On its LAST delivery, with the database
// refusing writes, the fact is held and applied once the store answers. The control (no hold)
// drops it; a poison fact (connection error, live store) still takes its budget.
func TestAnOwnershipFactIsHeldThroughAStoreOutage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hold    bool
		err     error
		applied bool
	}{
		{"store down, held", true, holdtest.DiskFull, true},
		{"control: no hold drops it", false, holdtest.DiskFull, false},
		{"poison, store answers", true, holdtest.ConnLost, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &outageApplier{failures: 3, err: tc.err}
			if tc.hold && !tc.applied {
				api.failures = 1 << 30
			}
			op := &OwnershipProcessor{Api: api}
			if tc.hold {
				op.Hold = holdFor(&holdtest.Store{})
			}
			ack := &factAck{}
			finishes(t, "handle", func() {
				op.handle(context.Background(), factMessage(t, "instance1.acme.device-ownership", messaging.MaxDeliver, ack))
			})
			require.Equal(t, 1, ack.acked)
			if tc.applied {
				require.Equal(t, int32(4), api.calls.Load(), "three refusals, then applied")
			} else if tc.hold {
				require.Equal(t, int32(1), api.calls.Load(), "a poison fact is not re-run")
			}
		})
	}
}

// Shutdown during a hold leaves the fact unacked even on its last delivery.
func TestAHeldOwnershipFactIsLeftUnackedOnShutdown(t *testing.T) {
	api := &outageApplier{failures: 1 << 30, err: holdtest.DiskFull}
	ctx, cancel := context.WithCancel(context.Background())
	op := &OwnershipProcessor{Api: api, Hold: holdFor(&holdtest.Store{}), procCtx: ctx}
	ack := &factAck{}
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	finishes(t, "a held fact on shutdown", func() {
		op.handle(ctx, factMessage(t, "instance1.acme.device-ownership", messaging.MaxDeliver, ack))
	})
	require.Zero(t, ack.acked)
}
