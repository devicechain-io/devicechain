// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// capturedPublishes records what an agent publishes, answering every publish with a PUBACK.
type capturedPublishes struct {
	mu   sync.Mutex
	alts []string
}

func (c *capturedPublishes) publish(_ string, payload []byte) error {
	var o struct {
		AltId string `json:"altId"`
	}
	_ = json.Unmarshal(payload, &o)
	c.mu.Lock()
	c.alts = append(c.alts, o.AltId)
	c.mu.Unlock()
	return nil
}

func (c *capturedPublishes) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.alts) }
func (c *capturedPublishes) first() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.alts) == 0 {
		return ""
	}
	return c.alts[0]
}

func retireAgent(t *testing.T, retireOn string, mutate func(*config.Configuration, *Agent)) (*Agent, *capturedPublishes) {
	t.Helper()
	cloud := startCloudBroker(t)
	cap := &capturedPublishes{}
	cfg := config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink: config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1",
			BrokerURL: fmt.Sprintf("tcp://127.0.0.1:%d", cloud), ConnectTimeoutSeconds: 1, RetireOn: retireOn},
	}
	if mutate != nil {
		mutate(&cfg, nil)
	}
	a := newAgent(t, cfg, func(a *Agent) {
		a.publishOverride = cap.publish
		a.overCeilingRetry = time.Second
	})
	t.Cleanup(runReady(t, a))
	// The accept subscription is made only in accept mode, and a refused one fails the connect.
	waitUntil(t, 15*time.Second, a.uplink.Connected)
	return a, cap
}

func publishOne(t *testing.T, a *Agent, device string) {
	t.Helper()
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort), device+"-pub",
		"test/tenant1/devices/"+device+"/events", []byte(`{"eventType":"measurement","values":{"temp":1}}`))
}

// TestAcceptModeKeepsTheEventUntilThePlatformAccepts is the contract: a PUBACK retires nothing,
// the platform's acceptance retires exactly the event it names.
func TestAcceptModeKeepsTheEventUntilThePlatformAccepts(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })

	// PUBACKed, and still in the spool: this is the line that fails on retire-on-PUBACK.
	if got := a.spoolDepth(); got != 1 {
		t.Fatalf("spool depth after PUBACK = %d, want 1 (a PUBACK is not an acceptance)", got)
	}
	// An acceptance for a DIFFERENT key retires nothing.
	a.handleAccept([]string{"edge:someone-else"})
	if got := a.spoolDepth(); got != 1 {
		t.Fatalf("spool depth after another event's acceptance = %d, want 1", got)
	}
	a.handleAccept([]string{cap.first()})
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })
	if a.accepted.Load() != 1 {
		t.Errorf("accepted = %d, want 1", a.accepted.Load())
	}
}

// TestAcceptThatBeatsThePubackStillRetires: the platform's answer can arrive before the agent's
// own PUBACK has been observed; the entry is held before the publish for that reason.
func TestAcceptThatBeatsThePubackStillRetires(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	a.publishOverride = func(topic string, payload []byte) error {
		_ = cap.publish(topic, payload)
		// The acceptance is delivered inside the publish, before it returns (the PUBACK).
		a.handleAccept([]string{cap.first()})
		return nil
	}
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.accepted.Load() == 1 })
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })
}

// TestUnacceptedEventIsOfferedAgainWithTheSameKey: neither an acceptance nor a refusal arrives
// (the answer was lost): the spool offers the event again, with the same key, so the platform
// can suppress the duplicate. Nothing is lost and nothing is retired on a guess.
func TestUnacceptedEventIsOfferedAgainWithTheSameKey(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, func(c *config.Configuration, _ *Agent) {
		c.Uplink.AcceptTimeoutSeconds = 1
	})
	publishOne(t, a, "dev1")
	waitUntil(t, 15*time.Second, func() bool { return cap.count() >= 2 })
	cap.mu.Lock()
	same := cap.alts[0] == cap.alts[1] && strings.HasPrefix(cap.alts[0], "edge:")
	cap.mu.Unlock()
	if !same {
		t.Fatalf("offered again under a different key: %v", cap.alts)
	}
	if a.spoolDepth() != 1 {
		t.Fatalf("spool depth = %d, want 1: an unanswered event must stay buffered", a.spoolDepth())
	}
}

// TestOverCeilingRefusalLosesNothingInAcceptMode is the proof for the over-ceiling path. The
// platform shed the event at the tenant's ingest ceiling after the broker PUBACKed it.
func TestOverCeilingRefusalLosesNothingInAcceptMode(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })
	alt := cap.first()

	a.handleNotice(messaging.GatewayNotice{AltId: alt, Device: "dev1", Reason: messaging.GatewayNoticeReasonOverCeiling})

	// Counted, never silent.
	if a.overCeiling.Load() != 1 || a.refused.Load() != 1 {
		t.Fatalf("over_ceiling=%d refused=%d, want 1 and 1", a.overCeiling.Load(), a.refused.Load())
	}
	// Kept: the event is still in the spool, and not dead-lettered (it is not a bad event).
	if got := a.spoolDepth(); got != 1 {
		t.Fatalf("spool depth after an over-ceiling refusal = %d, want 1", got)
	}
	if a.refusedKept.Load() != 0 {
		t.Errorf("an over-ceiling event was dead-lettered; the spool still holds it")
	}
	// Offered again after the retry delay, under the same key, and then accepted.
	waitUntil(t, 15*time.Second, func() bool { return cap.count() >= 2 })
	if cap.alts[1] != alt {
		t.Fatalf("re-offered under key %q, want %q", cap.alts[1], alt)
	}
	a.handleAccept([]string{alt})
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })
}

// TestOverCeilingRefusalLosesTheEventWhenRetiringOnPuback characterises what the default (and
// every release before accept mode) does: the event is gone from the spool by the time the
// platform sheds it, held only as a best-effort dead-letter copy. It is the negative control
// of the test above, kept so the difference between the two modes stays pinned.
func TestOverCeilingRefusalLosesTheEventWhenRetiringOnPuback(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnPuback, nil)
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })

	a.handleNotice(messaging.GatewayNotice{AltId: cap.first(), Device: "dev1", Reason: messaging.GatewayNoticeReasonOverCeiling})
	if got := a.spoolDepth(); got != 0 {
		t.Fatalf("spool depth = %d: in puback mode the event is already retired", got)
	}
	time.Sleep(2500 * time.Millisecond)
	if cap.count() != 1 {
		t.Fatalf("offered %d times in puback mode, want 1: nothing re-offers a retired event", cap.count())
	}
}

// TestTerminalRefusalRetiresTheHeldEntry: a refusal that will never succeed (the device is not
// the gateway's child) dead-letters the event and retires it, rather than resending it forever.
func TestTerminalRefusalRetiresTheHeldEntry(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })
	a.handleNotice(messaging.GatewayNotice{AltId: cap.first(), Device: "dev1", Reason: messaging.GatewayNoticeReasonNotGatewayChild})
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })
	if a.refusedKept.Load() != 1 {
		t.Errorf("refusedKept = %d, want 1: the refused event is in the dead-letter file", a.refusedKept.Load())
	}
}

func TestPendingAcceptsExpireAndBound(t *testing.T) {
	p := newPendingAccepts()
	p.put("a", nil)
	p.put("", nil) // no key: nothing to match an answer to
	if p.len() != 1 {
		t.Fatalf("len = %d, want 1", p.len())
	}
	time.Sleep(20 * time.Millisecond)
	if n := p.expire(time.Hour); n != 0 {
		t.Fatalf("expired a fresh entry")
	}
	if n := p.expire(time.Millisecond); n != 1 || p.len() != 0 {
		t.Fatalf("expire = %d, len = %d, want 1 and 0", n, p.len())
	}
}

func TestPendingAcceptsRedeliveryDoesNotRefreshAge(t *testing.T) {
	p := newPendingAccepts()
	p.put("a", nil)
	time.Sleep(30 * time.Millisecond)
	p.put("a", nil) // the spool offered it again; nothing answered
	if n := p.expire(20 * time.Millisecond); n != 1 {
		t.Fatalf("expire = %d, want 1: a redelivery must not restart the entry's age", n)
	}
}

// TestUnansweredEventIsCountedUnaccepted: an event that is offered again and never answered
// reaches unaccepted_total.
func TestUnansweredEventIsCountedUnaccepted(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	publishOne(t, a, "dev1")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })
	key := cap.first()
	a.pending.mu.Lock()
	e := a.pending.m[key]
	e.at = time.Now().Add(-time.Hour)
	a.pending.m[key] = e
	a.pending.mu.Unlock()
	// A redelivery arrives meanwhile.
	a.pending.put(key, e.msg)
	a.expirePending()
	if got := a.unaccepted.Load(); got != 1 {
		t.Fatalf("unaccepted = %d, want 1", got)
	}
}

// TestKeylessPayloadIsRetiredOnPubackInAcceptMode: a payload that is not a JSON object has no
// key, so no acceptance can name it. It must leave the spool on PUBACK, counted, not be resent.
func TestKeylessPayloadIsRetiredOnPubackInAcceptMode(t *testing.T) {
	a, cap := retireAgent(t, config.RetireOnAccept, nil)
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort), "bin-pub",
		"test/tenant1/devices/dev1/events", []byte{0x00, 0x01, 0x02, 0xff})
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 1 })
	waitUntil(t, 5*time.Second, func() bool { return a.spoolDepth() == 0 })
	if got := a.keylessRetired.Load(); got != 1 {
		t.Fatalf("keylessRetired = %d, want 1", got)
	}
	if a.pending.len() != 0 {
		t.Fatalf("a keyless entry is held pending")
	}
	// Control: a keyed event in the same mode is still held after its PUBACK.
	publishOne(t, a, "dev2")
	waitUntil(t, 10*time.Second, func() bool { return a.forwarded.Load() == 2 })
	if got := a.spoolDepth(); got != 1 {
		t.Fatalf("keyed event spool depth = %d, want 1", got)
	}
	if got := a.keylessRetired.Load(); got != 1 {
		t.Fatalf("keylessRetired = %d after a keyed event, want 1", got)
	}
	time.Sleep(2500 * time.Millisecond)
	if cap.count() != 2 {
		t.Fatalf("offered %d times, want 2: the keyless entry must not be resent", cap.count())
	}
}

func TestRetireConfigValidation(t *testing.T) {
	base := func() config.Configuration {
		c := config.Configuration{
			InstanceId: "i", Local: config.LocalConfiguration{StoreDir: "/x"},
			Uplink: config.UplinkConfiguration{BrokerURL: "tcp://h:1883", Tenant: "t", GatewayToken: "g", Username: "t:c", PasswordEnv: "P"},
		}
		c.ApplyDefaults()
		return c
	}
	c := base()
	if c.Uplink.RetireOn != config.RetireOnPuback {
		t.Fatalf("default retireOn = %q, want puback (an agent upgraded ahead of its platform must keep working)", c.Uplink.RetireOn)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c = base()
	c.Uplink.RetireOn = "never"
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown retireOn must be refused")
	}
	c = base()
	c.Local.Priority.Rules = []config.PriorityRule{{Match: "x-*", Class: "urgent"}}
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown class must be refused")
	}
	c = base()
	c.Local.Priority.Rules = []config.PriorityRule{{Match: "[", Class: config.ClassBulk}}
	if err := c.Validate(); err == nil {
		t.Fatal("a bad glob must be refused")
	}
	c = base()
	c.Local.Priority.Rules = []config.PriorityRule{{Match: "x-*", Class: config.ClassBulk}}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("rules with defaulted fractions: %v", err)
	}
	c.Local.Priority.ShedToFraction = 0.95
	if err := c.Validate(); err == nil {
		t.Fatal("shedTo >= shedAt must be refused")
	}
}
