// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

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

// outageResponseApi refuses MarkResponse with err for the first `failures` calls.
type outageResponseApi struct {
	*fakeApi
	failures int32
	err      error
	calls    atomic.Int32
}

func (a *outageResponseApi) MarkResponse(ctx context.Context, token, responder, nonce string, success bool,
	reach model.Reach, payload, errText *string) (*model.Command, error) {
	if a.calls.Add(1) <= a.failures {
		return nil, a.err
	}
	return a.fakeApi.MarkResponse(ctx, token, responder, nonce, success, reach, payload, errText)
}

// 🔴 THE REGRESSION: a device command RESPONSE is the device's only copy of what it did. At its
// last delivery it used to be lettered and acked after a database outage past the delivery
// budget, and the letter's own write-back needs the store that was down. With the store
// refusing writes the response must stay unlettered, be recorded when the store answers, and
// be acked once. The control (no hold) shows it lettered.
func TestACommandResponseIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, withHold := range []bool{true, false} {
		api := &outageResponseApi{fakeApi: &fakeApi{}, failures: 3, err: holdtest.DiskFull}
		dead := &deadRecorder{}
		ack := &gateAck{}
		p := &CommandDeliveryProcessor{Api: api, dead: testDeadSink(dead),
			CommandResponsesReader: &oneMessageReader{msg: responseMessage(
				[]byte(`{"commandToken":"cmd-1","success":true}`), messaging.MaxDeliver, nil, ack)}}
		if withHold {
			p.Hold = holdFor(&holdtest.Store{})
		}
		finishes(t, "the response handler", func() { readAndHandleOne(p, context.Background()) })
		if withHold {
			require.Empty(t, dead.msgs, "a response refused by a full store must not be lettered")
			require.Equal(t, 1, ack.n, "acked once, after it was recorded")
			require.Equal(t, int32(4), api.calls.Load(), "three refusals, then recorded")
		} else {
			require.Len(t, dead.msgs, 1, "control: without a hold the response is lettered at the cap")
		}
	}
}

// A connection-class failure with a LIVE store is a poison response: it takes its delivery
// budget and is lettered at the cap as before.
func TestAPoisonCommandResponseStillTakesItsDeliveryBudget(t *testing.T) {
	api := &outageResponseApi{fakeApi: &fakeApi{}, failures: 1 << 30, err: holdtest.ConnLost}
	dead := &deadRecorder{}
	ack := &gateAck{}
	p := &CommandDeliveryProcessor{Api: api, dead: testDeadSink(dead), Hold: holdFor(&holdtest.Store{}),
		CommandResponsesReader: &oneMessageReader{msg: responseMessage(
			[]byte(`{"commandToken":"cmd-1","success":true}`), messaging.MaxDeliver, nil, ack)}}
	finishes(t, "the response handler", func() { readAndHandleOne(p, context.Background()) })
	require.Len(t, dead.msgs, 1)
	require.Equal(t, 1, ack.n)
}

// Shutdown during a hold leaves the response unacked and unlettered even on its last delivery.
func TestAHeldCommandResponseIsLeftUnackedOnShutdown(t *testing.T) {
	api := &outageResponseApi{fakeApi: &fakeApi{}, failures: 1 << 30, err: holdtest.DiskFull}
	dead := &deadRecorder{}
	ack := &gateAck{}
	p := &CommandDeliveryProcessor{Api: api, dead: testDeadSink(dead), Hold: holdFor(&holdtest.Store{}),
		CommandResponsesReader: &oneMessageReader{msg: responseMessage(
			[]byte(`{"commandToken":"cmd-1","success":true}`), messaging.MaxDeliver, nil, ack)}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	finishes(t, "a held response on shutdown", func() { readAndHandleOne(p, ctx) })
	require.Empty(t, dead.msgs)
	require.Zero(t, ack.n)
}

// outageResumer refuses ResumeDevice with err for the first `failures` calls.
type outageResumer struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (r *outageResumer) ResumeDevice(context.Context, string, string) error {
	if r.calls.Add(1) <= r.failures {
		return r.err
	}
	return nil
}

// A roster fact whose resume the store refuses is held, not lettered, on its last delivery, and
// acked once the resume lands; a poison one (live store) is lettered at the cap as before.
func TestARosterFactIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hold     bool
		err      error
		lettered bool
	}{
		{"store down, held", true, holdtest.DiskFull, false},
		{"control: no hold", false, holdtest.DiskFull, true},
		{"poison, store answers", true, holdtest.ConnLost, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := realApi(t)
			resumer := &outageResumer{failures: 3, err: tc.err}
			if tc.lettered && tc.hold {
				resumer.failures = 1 << 30
			}
			dead := &deadRecorder{}
			rc := newTestRosterConsumer(api, nil)
			rc.Resumer = resumer
			rc.dead = testDeadSink(dead)
			if tc.hold {
				rc.Hold = holdFor(&holdtest.Store{})
			}
			ack := &gateAck{}
			m := rosterMsg(t, "dev-1", false, true)
			finishes(t, "the roster handler", func() {
				rc.handle(context.Background(), messaging.NewConsumedMessage(m.Subject, m.Value, messaging.MaxDeliver, nil, ack).
					WithOrigin(messaging.Origin{Suffix: streams.DeviceRoster, Stream: "inst_device-roster",
						Consumer: "inst_command-delivery_device-roster", Seq: 5}))
			})
			require.Equal(t, tc.lettered, len(dead.msgs) > 0)
			require.Equal(t, 1, ack.n)
			if !tc.lettered {
				require.Equal(t, int32(4), resumer.calls.Load())
			}
		})
	}
}

// outageDisposition refuses MarkResponseLost with err for the first `failures` calls.
type outageDisposition struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (d *outageDisposition) MarkResponseLost(context.Context, string) (bool, error) {
	if d.calls.Add(1) <= d.failures {
		return false, d.err
	}
	return true, nil
}

// A dead letter whose write-back the store refuses is held on its last delivery (otherwise the
// command is STRANDED: it reads in flight until its TTL, then lapses to TIMEOUT), and settled
// once the store answers. A poison one is still counted stranded at the cap.
func TestAWritebackIsHeldThroughAStoreOutageNotStranded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hold     bool
		err      error
		stranded float64
	}{
		{"store down, held", true, holdtest.DiskFull, 0},
		{"control: no hold", false, holdtest.DiskFull, 1},
		{"poison, store answers", true, holdtest.ConnLost, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &outageDisposition{failures: 3, err: tc.err}
			if tc.stranded > 0 && tc.hold {
				api.failures = 1 << 30
			}
			w := newTestWriteback(t, api)
			if tc.hold {
				w.Hold = holdFor(&holdtest.Store{})
			}
			ack := &countingAck{}
			finishes(t, "the write-back", func() {
				w.Handle(letter(t, "tenant-a", responseLetter("cmd-9"), messaging.MaxDeliver, ack))
			})
			require.Equal(t, 1, ack.acks)
			require.Equal(t, tc.stranded, testutil.ToFloat64(w.stranded))
			if tc.stranded == 0 {
				require.Equal(t, int32(4), api.calls.Load())
			}
		})
	}
}
