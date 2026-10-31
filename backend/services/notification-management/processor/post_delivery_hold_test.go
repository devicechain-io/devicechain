// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/gorm"
)

// 🔴 THE LIVE FINDING THESE TESTS PIN. A database outage that began AFTER a page went out failed
// the follow-up writes (record the notification; drop what quiet hours held about the alarm)
// with an error log and an ack. The page was sent and nothing remembered it: the alarm never
// entered the escalation substrate, the throttle had no basis, and a quiet-hours deferral that
// the page had answered stayed behind to page a second time, about an older event.
//
// The fix settles those writes under the store hold. What each test asserts is the pair that
// matters: the record is not lost AND the page is not sent twice.

// writeFaults fails every statement the notifier's database runs, once armed, to stand for a
// store that goes down right after a delivery. It is a real gorm callback on the real
// persistence Api, so the error reaches the notifier exactly as a driver's would.
type writeFaults struct {
	armed  atomic.Bool
	left   atomic.Int32 // statements still to fail; healing by count (refusing-writes)
	failed atomic.Int32
	err    error
	store  *holdtest.Store
	probes atomic.Int32
	// healOnProbe is the probe on which a connection-loss outage ends; 0 heals by count.
	healOnProbe int32
}

func injectWriteFaults(db *gorm.DB, f *writeFaults) {
	cb := func(tx *gorm.DB) {
		if !f.armed.Load() {
			return
		}
		if f.healOnProbe == 0 && f.left.Add(-1) < 0 {
			f.armed.Store(false)
			return
		}
		f.failed.Add(1)
		_ = tx.AddError(f.err)
	}
	db.Callback().Query().Before("gorm:query").Register("fault:query", cb)
	db.Callback().Create().Before("gorm:create").Register("fault:create", cb)
	db.Callback().Update().Before("gorm:update").Register("fault:update", cb)
	db.Callback().Delete().Before("gorm:delete").Register("fault:delete", cb)
}

// arm is the moment the store goes down: called from inside the delivery, after the send.
func (f *writeFaults) arm() {
	if f.healOnProbe != 0 {
		f.store.Down.Store(true)
	}
	f.armed.Store(true)
}

// probe is the holder's probe: for a connection loss the store comes back on the Nth probe.
func (f *writeFaults) probe(ctx context.Context) error {
	if f.healOnProbe != 0 && f.armed.Load() && f.probes.Add(1) >= f.healOnProbe {
		f.armed.Store(false)
		f.store.Down.Store(false)
	}
	return f.store.Probe(ctx)
}

type outageShape struct {
	name string
	make func(store *holdtest.Store, writes int32) *writeFaults
}

// The two shapes the hold classifies: the server saying it cannot take writes (SQLSTATE 53100
// here; 25006 is the same class), and a lost connection, which is an outage only while a probe
// also fails.
var outageShapes = []outageShape{
	{"refusing writes 53100", func(s *holdtest.Store, writes int32) *writeFaults {
		f := &writeFaults{err: holdtest.DiskFull, store: s}
		f.left.Store(writes)
		return f
	}},
	{"connection lost", func(s *holdtest.Store, _ int32) *writeFaults {
		return &writeFaults{err: holdtest.ConnLost, store: s, healOnProbe: 3}
	}},
}

func alarmMessage(t *testing.T, ev *dmmodel.AlarmStateChangeEvent) (*holdtest.Ack, func(np *NotificationProcessor)) {
	t.Helper()
	b, err := dmproto.MarshalAlarmStateChangeEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	msg, ack := holdtest.Msg("instance1.acme.alarm-events", b, 1)
	return ack, func(np *NotificationProcessor) {
		finishes(t, "dispatchOne", func() { np.dispatchOne(context.Background(), msg) })
	}
}

// A page whose state write is refused is held, the write lands once the store answers, the
// message is acked, and the page is sent exactly once. The control (no hold) shows the old
// behaviour: acked, paged, and nothing recorded.
func TestAPageIsRecordedAfterAStoreOutageThatBeganAfterTheSend(t *testing.T) {
	for _, shape := range outageShapes {
		for _, withHold := range []bool{true, false} {
			t.Run(shape.name+"/hold="+boolName(withHold), func(t *testing.T) {
				api := fencedApi(t)
				ctx := tenantScoped()
				seedRoutedAlarm(t, api, ctx)
				store := &holdtest.Store{}
				faults := shape.make(store, 2)
				injectWriteFaults(stateDB(api), faults)

				sent := 0
				n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &funcAdapter{fn: func(int) error {
					sent++
					faults.arm()
					return nil
				}}})
				n.api, n.store = api, &fakeSecretStore{}

				ev := raisedEvent("CRITICAL")
				ev.AlarmToken = "alarm-1"
				ack, run := alarmMessage(t, ev)
				np := newTestProcessor(n)
				if withHold {
					np.Hold = holdForProbe(faults.probe)
				}
				run(np)
				faults.armed.Store(false) // leftover injected failures must not fail the assertions' reads

				if sent != 1 {
					t.Fatalf("the page was sent %d time(s), want exactly 1", sent)
				}
				if ack.Acked.Load() != 1 {
					t.Fatalf("acked %d time(s), want 1: a delivered page is acked", ack.Acked.Load())
				}
				states, err := api.NotificationStatesByAlarmToken(ctx, []string{"alarm-1"})
				if err != nil {
					t.Fatal(err)
				}
				if withHold {
					if len(states) != 1 || states[0].NotifyCount != 1 {
						t.Fatalf("the page went out and was not recorded after the store recovered: %+v", states)
					}
					if faults.failed.Load() == 0 {
						t.Fatal("no write was refused: the fixture proved nothing")
					}
				} else if len(states) != 0 {
					t.Fatalf("control: without a hold the state write is lost, got %+v", states)
				}
			})
		}
	}
}

// The second write: a quiet-hours deferral the page answered must be dropped, not left to page
// a second time at release.
func TestADeferralAnsweredByAPageIsDroppedAfterAStoreOutage(t *testing.T) {
	for _, shape := range outageShapes {
		for _, withHold := range []bool{true, false} {
			t.Run(shape.name+"/hold="+boolName(withHold), func(t *testing.T) {
				clock := insideQuiet
				n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
				store := &holdtest.Store{}
				faults := shape.make(store, 4) // both writes refused twice
				injectWriteFaults(stateDB(api), faults)

				// Inside quiet hours: held, nobody paged.
				if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
					t.Fatal(err)
				}
				if held := heldRows(t, api, ctx); len(held) != 1 || fa.calls != 0 {
					t.Fatalf("setup: held %d, paged %d; want 1 and 0", len(held), fa.calls)
				}

				// After the stretch: paged now; the store dies as the send completes.
				clock = quietEnds.Add(time.Minute)
				n.adapters[model.ChannelTypeSMTP] = &funcAdapter{fn: func(int) error {
					fa.calls++
					faults.arm()
					return nil
				}}
				ack, run := alarmMessage(t, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock))
				np := newTestProcessor(n)
				if withHold {
					np.Hold = holdForProbe(faults.probe)
				}
				run(np)
				faults.armed.Store(false) // leftover injected failures must not fail the assertions' reads

				if fa.calls != 1 {
					t.Fatalf("paged %d time(s), want exactly 1", fa.calls)
				}
				if ack.Acked.Load() != 1 {
					t.Fatalf("acked %d time(s), want 1", ack.Acked.Load())
				}
				held := heldRows(t, api, ctx)
				if withHold && len(held) != 0 {
					t.Fatalf("%d deferral(s) survive a page that answered them: the release would "+
						"page a second time about an older event", len(held))
				}
				if !withHold && len(held) != 1 {
					t.Fatalf("control: without a hold the deferral survives, got %d", len(held))
				}
			})
		}
	}
}

// The scheduler's release path pages from a held row and then records it; that record is held
// through an outage on the notifier's own Holder and the page is not re-sent.
func TestAReleasedPageIsRecordedAfterAStoreOutage(t *testing.T) {
	for _, shape := range outageShapes {
		for _, withHold := range []bool{true, false} {
			t.Run(shape.name+"/hold="+boolName(withHold), func(t *testing.T) {
				clock := insideQuiet
				n, api, _, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
				holdOne(t, n, api, ctx, clock)

				store := &holdtest.Store{}
				faults := shape.make(store, 2)
				injectWriteFaults(stateDB(api), faults)
				sent := 0
				n.adapters[model.ChannelTypeSMTP] = &funcAdapter{fn: func(int) error {
					sent++
					faults.arm()
					return nil
				}}
				if withHold {
					n.Hold = holdForProbe(faults.probe)
				}
				clock = quietEnds
				finishes(t, "ReleaseHeld", func() {
					if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
						t.Error(err)
					}
				})
				faults.armed.Store(false)

				if sent != 1 {
					t.Fatalf("paged %d time(s), want 1", sent)
				}
				states, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
				if err != nil {
					t.Fatal(err)
				}
				if withHold && (len(states) != 1 || states[0].NotifyCount != 1) {
					t.Fatalf("the released page was not recorded: %+v", states)
				}
				if rows := heldRows(t, api, ctx); withHold && len(rows) != 0 {
					t.Fatalf("%d deferral row(s) outlive the page that answered them: the lease "+
						"ending would release (page) it again", len(rows))
				}
				if !withHold && len(states) != 0 {
					t.Fatalf("control: without a hold the record is lost, got %+v", states)
				}
			})
		}
	}
}

func holdForProbe(probe func(context.Context) error) *storehold.Holder {
	return &storehold.Holder{Name: "test", Probe: probe, Tune: holdtest.FastTuning()}
}

func boolName(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
