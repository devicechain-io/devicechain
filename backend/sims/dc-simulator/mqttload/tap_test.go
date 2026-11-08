// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func publishOnce(t *testing.T, b *fakemqtt.Broker, topic, body string) {
	t.Helper()
	o := mqtt.NewClientOptions().AddBroker(b.URL()).SetClientID("pub-" + body[:3])
	c := mqtt.NewClient(o)
	if tok := c.Connect(); !tok.WaitTimeout(2*time.Second) || tok.Error() != nil {
		t.Fatalf("connect: %v", tok.Error())
	}
	defer c.Disconnect(100)
	if tok := c.Publish(topic, 1, false, body); !tok.WaitTimeout(2*time.Second) || tok.Error() != nil {
		t.Fatalf("publish: %v", tok.Error())
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The tap files each delivery under (device, instant), counts a re-delivery once, and treats a
// body that is not an event as someone else's traffic, not as the run's.
func TestTapRecordsIdentitiesOnce(t *testing.T) {
	b := fakemqtt.New(t)
	b.Fanout = true
	tap, err := NewTap(context.Background(), TapConfig{Broker: b.URL(), ClientID: "tap", Filter: "devices/+/events"})
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()

	ev := `{"device":"d1","occurredTime":"2026-11-07T10:00:00.000001Z"}`
	publishOnce(t, b, "devices/d1/events", ev)
	publishOnce(t, b, "devices/d1/events", ev) // a re-delivery
	publishOnce(t, b, "devices/d2/events", `{"device":"d2","occurredTime":"2026-11-07T10:00:01Z"}`)
	publishOnce(t, b, "devices/d3/events", `not json`)
	publishOnce(t, b, "elsewhere/x", `{"device":"d9","occurredTime":"2026-11-07T10:00:00Z"}`) // not in the filter

	waitFor(t, "four deliveries", func() bool { return tap.Stats().Received == 4 })
	st := tap.Stats()
	if st.Unique != 2 || st.Duplicates != 1 || st.Unparsed != 1 {
		t.Fatalf("stats %+v", st)
	}
	lo := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	if n := tap.CountBetween(lo, lo.Add(time.Minute)); n != 2 {
		t.Fatalf("count %d", n)
	}
	if n := tap.CountBetween(lo.Add(time.Hour), lo.Add(2*time.Hour)); n != 0 {
		t.Fatalf("a window with no events reads %d, not 0", n)
	}
	if got := tap.DeviceBetween("d1", lo, lo.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("d1 %v", got)
	}
}

func TestTapCountsItsOwnConnectionLoss(t *testing.T) {
	b := fakemqtt.New(t)
	tap, err := NewTap(context.Background(), TapConfig{Broker: b.URL(), ClientID: "tap", Filter: "devices/+/events"})
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	b.DropConnections()
	waitFor(t, "the loss to be seen", func() bool { return tap.Stats().ConnectionLosses == 1 })
}

func TestTapWithoutAFilterIsRefused(t *testing.T) {
	if _, err := NewTap(context.Background(), TapConfig{Broker: "tcp://127.0.0.1:1", ClientID: "x"}); err == nil {
		t.Fatal("a tap that subscribes to nothing would report every event lost")
	}
}

// A broker that refuses the subscription (the tap user may not read the topic) is an error at
// start, not a run in which every event reads as lost.
func TestTapRefusedSubscriptionFailsAtStart(t *testing.T) {
	b := fakemqtt.New(t)
	b.RefuseSubscribe = true
	if _, err := NewTap(context.Background(), TapConfig{Broker: b.URL(), ClientID: "tap", Filter: "devices/+/events"}); err == nil {
		t.Fatal("a refused subscription must fail the tap")
	}
}

// A broker that grants the tap less than QoS 1 would let a run pass without the delivery
// guarantee the verdict is defined on.
func TestTapRefusesADowngradedSubscription(t *testing.T) {
	b := fakemqtt.New(t)
	b.GrantQoS0 = true
	if _, err := NewTap(context.Background(), TapConfig{Broker: b.URL(), ClientID: "tap", Filter: "devices/+/events"}); err == nil {
		t.Fatal("a QoS 0 grant must fail the tap")
	}
}
