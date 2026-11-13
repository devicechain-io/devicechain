// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package dropproxy

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestStartRefusesAnEveryOfZero(t *testing.T) {
	if _, err := Start("127.0.0.1:0", "127.0.0.1:1", 0); err == nil {
		t.Fatal("every=0 would divide by zero (or drop nothing and call itself a control)")
	}
}

// Exactly every Nth QoS 1 publish is acknowledged to the publisher and kept from the broker;
// every other one reaches it.
func TestProxyDropsExactlyEveryNth(t *testing.T) {
	b := fakemqtt.New(t)
	p, err := Start("127.0.0.1:0", b.Addr(), 5)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + p.Addr()).SetClientID("pub"))
	if tok := c.Connect(); !tok.WaitTimeout(2*time.Second) || tok.Error() != nil {
		t.Fatalf("connect: %v", tok.Error())
	}
	defer c.Disconnect(100)
	for i := 1; i <= 23; i++ {
		tok := c.Publish("t", 1, false, fmt.Sprintf(`{"device":"d","occurredTime":"2026-11-07T10:00:%02dZ"}`, i))
		if !tok.WaitTimeout(2*time.Second) || tok.Error() != nil {
			t.Fatalf("publish %d was not acknowledged (the dropped ones must be PUBACKed too): %v", i, tok.Error())
		}
	}
	if p.Seen() != 23 || p.Dropped() != 4 {
		t.Fatalf("seen %d dropped %d, want 23 and 4", p.Seen(), p.Dropped())
	}
	if got := len(b.Stored()); got != 19 {
		t.Fatalf("broker kept %d, want 19", got)
	}
}

// QoS 0 promises nothing, so the control neither counts nor drops it: only a PUBACK is a promise
// the proxy can break.
func TestProxyLeavesQoS0Alone(t *testing.T) {
	b := fakemqtt.New(t)
	p, err := Start("127.0.0.1:0", b.Addr(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + p.Addr()).SetClientID("pub0"))
	if tok := c.Connect(); !tok.WaitTimeout(2*time.Second) || tok.Error() != nil {
		t.Fatalf("connect: %v", tok.Error())
	}
	defer c.Disconnect(100)
	for i := 0; i < 5; i++ {
		c.Publish("t", 0, false, `{"device":"d","occurredTime":"2026-11-07T10:00:00Z"}`).WaitTimeout(time.Second)
	}
	for end := time.Now().Add(2 * time.Second); len(b.Stored()) < 5 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if p.Seen() != 0 || p.Dropped() != 0 || len(b.Stored()) != 5 {
		t.Fatalf("seen %d dropped %d kept %d, want 0, 0, 5", p.Seen(), p.Dropped(), len(b.Stored()))
	}
}
