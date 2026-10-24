// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
)

// brokerStore is the platform's side of a test: what the fake broker kept, read back through the
// same two interfaces the real run reads the event API through. The broker keeping an event is
// the stand-in for "the platform persisted it".
type brokerStore struct{ b *fakemqtt.Broker }

func (s brokerStore) inWindow(w Window) []fakemqtt.Stored {
	var out []fakemqtt.Stored
	for _, e := range s.b.Stored() {
		if !e.OccurredTime.Before(w.Start) && !e.OccurredTime.After(w.End) {
			out = append(out, e)
		}
	}
	return out
}

func (s brokerStore) Count(_ context.Context, w Window) (int64, error) {
	return int64(len(s.inWindow(w))), nil
}

func (s brokerStore) DeviceIdentities(_ context.Context, device string, w Window) ([]int64, int, error) {
	var times []int64
	for _, e := range s.inWindow(w) {
		if e.Device == device {
			times = append(times, e.OccurredTime.UnixMicro())
		}
	}
	return times, 1, nil
}

const (
	mqttTestInstance = "dcinst"
	mqttTestTenant   = "acme"
)

func mqttTestProfile(b *fakemqtt.Broker, identity string) MQTTProfile {
	return MQTTProfile{
		Manifest: "devicepulse", Rate: 200, Duration: 1500 * time.Millisecond, MetricKey: "speed_kph",
		// The rate floor is the verdict test's subject, not these runs': a short run on a busy
		// test machine must not fail it by a few ticks.
		Identity: identity, MinAccepted: 10, MinOfferedRatio: 0.5, ReportInterval: -1,
		QuiescePoll: 10 * time.Millisecond, QuiesceTimeout: 400 * time.Millisecond, QuiesceSettle: 20 * time.Millisecond,
		MQTT: mqttload.Config{
			Broker: b.URL(), TLS: false, InstanceId: mqttTestInstance, Tenant: mqttTestTenant,
			QoS: 1, CleanSession: true, ConnectTimeout: 2 * time.Second, AckTimeout: 300 * time.Millisecond,
			BackoffBase: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
		},
	}
}

// startRun connects a fleet of n devices to b and returns the run wired to read the broker back.
func startRun(t *testing.T, b *fakemqtt.Broker, p MQTTProfile, n int) mqttRun {
	t.Helper()
	p = p.withDefaults()
	p.Devices = n
	var devs []sim.DeviceInstance
	var tokens []string
	for i := 0; i < n; i++ {
		d := sim.DeviceInstance{Token: fmt.Sprintf("dev-%03d", i), CredentialId: fmt.Sprintf("cred-%03d", i)}
		devs = append(devs, d)
		tokens = append(tokens, d.Token)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	f, err := mqttload.NewFleet(p.MQTT, devs)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Start(ctx)
	t.Cleanup(func() { cancel(); f.Close() })
	if _, err := f.AwaitConnected(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	store := brokerStore{b}
	return mqttRun{
		Tenant: mqttTestTenant, Profile: p, Seed: 7, Fleet: f, Devices: tokens,
		Ledger: sim.NewIdentityLedger(), Counter: store, Reader: store,
	}
}

func inv(t *testing.T, r *MQTTReport, name string) Invariant {
	t.Helper()
	i := invariantByName(r.Invariants, name)
	if i == nil {
		t.Fatalf("no %s invariant in %+v", name, r.Invariants)
	}
	return *i
}

// THE POSITIVE CONTROL: a broker that keeps what it acknowledges passes, with the exact
// identity read-back, and the report says what it compared.
func TestMQTTRunPassesWhenEveryPubackIsPersisted(t *testing.T) {
	b := fakemqtt.New(t)
	run := startRun(t, b, mqttTestProfile(b, IdentityFull), 4)
	rep, err := run.driveAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed() || rep.Loss || rep.ExitCode() != ExitPass {
		t.Fatalf("a lossless run did not pass:\n%s", rep.Human())
	}
	if rep.Storage.Promised < 100 || rep.Storage.Persisted != rep.Storage.Promised || rep.Storage.Lost != 0 || !rep.Storage.LostExact {
		t.Fatalf("storage %+v", rep.Storage)
	}
	if rep.Identity == nil || !rep.Identity.Reconciled || rep.Drive.Connections != 4 || rep.Drive.Devices != 4 {
		t.Fatalf("identity %+v drive %+v", rep.Identity, rep.Drive)
	}
	if rep.Transport.PlainObserved != 4 || rep.Transport.TLSObserved != 0 {
		t.Fatalf("transport %+v", rep.Transport)
	}
	if !strings.Contains(rep.Human(), "PASS") {
		t.Fatalf("human summary lacks PASS:\n%s", rep.Human())
	}
}

// THE NEGATIVE CONTROL, AND THE POINT OF THE MODE: a broker that PUBACKs events it does not keep
// is reported as LOSS, with the exact number, and the run exits with the loss code. Without this
// the harness could be reporting PUBACKed == persisted by construction.
func TestMQTTRunReportsPubackedButNotPersistedAsLoss(t *testing.T) {
	for _, mode := range []string{IdentityFull, IdentityCount} {
		t.Run(mode, func(t *testing.T) {
			b := fakemqtt.New(t)
			b.AckOnly = func(n int64) bool { return n%10 == 0 } // acknowledged, not kept
			run := startRun(t, b, mqttTestProfile(b, mode), 4)
			rep, err := run.driveAndReconcile(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			dropped := b.Received() - int64(len(b.Stored()))
			if dropped < 10 {
				t.Fatalf("the fake broker dropped only %d events; the control is too weak", dropped)
			}
			if !rep.Loss || rep.Passed() || rep.ExitCode() != ExitLoss {
				t.Fatalf("PUBACKed > persisted was not reported as loss (exit %d):\n%s", rep.ExitCode(), rep.Human())
			}
			if rep.Storage.Lost != dropped {
				t.Fatalf("reported %d lost, the broker dropped %d", rep.Storage.Lost, dropped)
			}
			if rep.Storage.LostExact != (mode == IdentityFull) {
				t.Fatalf("lostExact = %v in %s mode", rep.Storage.LostExact, mode)
			}
			d := inv(t, rep, InvPubackDurability)
			if d.Passed || !strings.Contains(d.Detail, "LOSS") {
				t.Fatalf("durability invariant %+v", d)
			}
			if h := rep.Human(); !strings.Contains(h, "LOSS") {
				t.Fatalf("the human summary does not say LOSS:\n%s", h)
			}
			raw, err := rep.JSON()
			if err != nil {
				t.Fatal(err)
			}
			var back map[string]any
			if err := json.Unmarshal(raw, &back); err != nil || back["loss"] != true {
				t.Fatalf("the JSON report does not carry loss=true: %v %s", err, raw)
			}
		})
	}
}

// THE BLIND SPOT OF A COUNT, MADE VISIBLE. An unconfirmed publish (no PUBACK, so no promise) that
// IS stored takes the place of a PUBACKed one that is not: the totals agree and the count passes.
// The identity read-back sees both, which is why the long-run mode reports a masked-loss bound
// and the short-run default reads every event back.
func TestACountCanMaskLossThatTheIdentityReadBackCatches(t *testing.T) {
	// Every 13th publish is PUBACKed and dropped (7.7%); every 11th is stored and never answered
	// (9.1%), so the stored-but-unconfirmed copies outnumber the dropped ones.
	build := func() *fakemqtt.Broker {
		b := fakemqtt.New(t)
		b.AckOnly = func(n int64) bool { return n%13 == 0 && n%11 != 0 }
		b.StoreOnly = func(n int64) bool { return n%11 == 0 }
		return b
	}

	b := build()
	run := startRun(t, b, mqttTestProfile(b, IdentityCount), 4)
	rep, err := run.driveAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Loss || rep.Storage.LostExact || rep.Drive.Unconfirmed == 0 || rep.Storage.MaskedLossBound != rep.Drive.Unconfirmed {
		t.Fatalf("premise: the count should be fooled and say how far (masked bound %d, unconfirmed %d, loss %v)\n%s",
			rep.Storage.MaskedLossBound, rep.Drive.Unconfirmed, rep.Loss, rep.Human())
	}
	if !strings.Contains(inv(t, rep, InvPubackDurability).Detail, "could be masked") {
		t.Fatalf("a count-only comparison must say what it cannot see: %+v", inv(t, rep, InvPubackDurability))
	}
	// A count that cannot exclude a loss must not pass as having excluded one: no verdict, and
	// not the loss code either (it did not find one).
	if rep.Passed() || rep.ExitCode() != ExitFail || inv(t, rep, InvPubackDurability).Passed {
		t.Fatalf("a count with a masked-loss bound of %d passed (exit %d):\n%s", rep.Storage.MaskedLossBound, rep.ExitCode(), rep.Human())
	}
	if !strings.Contains(rep.Human(), "NO DURABILITY VERDICT") {
		t.Fatalf("the human summary does not say the count reached no verdict:\n%s", rep.Human())
	}

	b2 := build()
	run2 := startRun(t, b2, mqttTestProfile(b2, IdentityFull), 4)
	rep2, err := run2.driveAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep2.Loss || rep2.ExitCode() != ExitLoss || !rep2.Storage.LostExact || rep2.Storage.Lost == 0 {
		t.Fatalf("the identity read-back missed a PUBACKed loss that a stored-but-unconfirmed publish masked:\n%s", rep2.Human())
	}
}

// A QoS 0 shortfall is a measurement: reported, gated only by the tolerance, and never the
// PUBACK-loss exit code (QoS 0 promised nothing).
func TestMQTTRunQoS0ShortfallIsGatedByTheTolerance(t *testing.T) {
	run := func(tolerance float64) *MQTTReport {
		b := fakemqtt.New(t)
		b.AckOnly = func(n int64) bool { return n%10 == 0 }
		p := mqttTestProfile(b, IdentityCount)
		p.MQTT.QoS = 0
		p.QoS0Tolerance = tolerance
		r := startRun(t, b, p, 3)
		rep, err := r.driveAndReconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	strict := run(0)
	if strict.Passed() || strict.Loss || strict.ExitCode() != ExitFail || strict.Storage.Lost == 0 {
		t.Fatalf("a QoS 0 shortfall at zero tolerance: exit %d loss %v lost %d\n%s", strict.ExitCode(), strict.Loss, strict.Storage.Lost, strict.Human())
	}
	if !run(0.2).Passed() {
		t.Fatal("a 10% QoS 0 shortfall failed a 20% tolerance")
	}
}

// The verdict's pure branches, with no broker.
func TestMQTTVerdict(t *testing.T) {
	base := func() mqttMeasured {
		return mqttMeasured{
			Profile: MQTTProfile{Rate: 100, MinAccepted: 10, MinOfferedRatio: 0.9, Devices: 5, MQTT: mqttload.Config{QoS: 1, TLS: true}},
			Drive:   mqttload.Snapshot{Sent: 1000, Acked: 1000, TLSConns: 5},
			Held:    10 * time.Second, Persisted: 1000, Reached: true, Mode: IdentityCount,
		}
	}
	failed := func(inv []Invariant) []string {
		var out []string
		for _, i := range inv {
			if !i.Passed {
				out = append(out, i.Name)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		mut      func(*mqttMeasured)
		wantFail []string
		wantLoss bool
	}{
		{"clean", func(*mqttMeasured) {}, nil, false},
		{"loss by count", func(m *mqttMeasured) { m.Persisted = 990 }, []string{InvPubackDurability}, true},
		{"duplicates", func(m *mqttMeasured) { m.Persisted = 1005 }, []string{InvPubackDurability}, false},
		// Unconfirmed publishes can explain extra rows, but in a count they can also stand in for
		// lost ones, so the count reaches no verdict.
		{"unconfirmed in a count is no verdict", func(m *mqttMeasured) { m.Drive.Sent, m.Drive.Unconfirmed, m.Persisted = 1005, 5, 1005 }, []string{InvPubackDurability}, false},
		{"re-sent in a count is no verdict", func(m *mqttMeasured) { m.Drive.Resent = 3 }, []string{InvPubackDurability}, false},
		{"unconfirmed with the identity read-back is fine", func(m *mqttMeasured) {
			m.Drive.Sent, m.Drive.Unconfirmed, m.Persisted = 1005, 5, 1005
			m.Mode, m.Identity, m.IdentityInv = IdentityFull, &IdentityReport{}, &Invariant{Name: InvIdentity, Passed: true}
		}, nil, false},
		{"duplicates by identity", func(m *mqttMeasured) {
			m.Persisted = 1003
			m.Mode, m.Identity, m.IdentityInv = IdentityFull, &IdentityReport{ExtraCopies: 3}, &Invariant{Name: InvIdentity, Passed: true}
		}, []string{InvPubackDurability}, false},
		{"stopped short is loss", func(m *mqttMeasured) { m.Persisted, m.Reached = 990, false }, []string{InvPubackDurability}, true},
		{"still rising is not loss", func(m *mqttMeasured) { m.Persisted, m.Reached, m.Rising = 990, false, true }, []string{InvPubackDurability}, false},
		{"not drained", func(m *mqttMeasured) { m.Persisted, m.Reached = 1000, false }, []string{InvPubackDurability}, false},
		{"never connected", func(m *mqttMeasured) { m.NeverConnected = 1 }, []string{InvFleetConnected}, false},
		{"plaintext when tls requested", func(m *mqttMeasured) { m.Drive.TLSConns, m.Drive.PlainConns = 0, 5 }, []string{InvTransport}, false},
		{"tls when plaintext requested", func(m *mqttMeasured) { m.Profile.MQTT.TLS = false; m.Drive.PlainConns = 0 }, []string{InvTransport}, false},
		{"mixed transports", func(m *mqttMeasured) { m.Drive.PlainConns = 1 }, []string{InvTransport}, false},
		{"under the load floor", func(m *mqttMeasured) {
			m.Profile.Rate = 0.5
			m.Drive.Acked, m.Persisted, m.Drive.Sent = 5, 5, 5
		}, []string{InvLoadApplied}, false},
		// Sent at the rate, but the broker acknowledged half: the rate was not sustained.
		{"acknowledged too little", func(m *mqttMeasured) {
			m.Drive.Acked, m.Drive.Unconfirmed, m.Persisted = 500, 500, 500
			m.Mode, m.Identity, m.IdentityInv = IdentityFull, &IdentityReport{}, &Invariant{Name: InvIdentity, Passed: true}
		}, []string{InvLoadOffered}, false},
		{"offered too little", func(m *mqttMeasured) { m.Drive.Sent, m.Drive.Acked, m.Persisted = 500, 500, 500 }, []string{InvLoadOffered}, false},
		{"ack latency ceiling", func(m *mqttMeasured) {
			m.Profile.MaxAckP99 = 50 * time.Millisecond
			m.Latency.P99 = 80
		}, []string{InvAckLatency}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mut(&m)
			inv, st, loss := mqttVerdict(m)
			got := failed(inv)
			if m.Rising && (st.Lost != 0 || st.Undrained != m.Drive.Acked-m.Persisted) {
				t.Fatalf("a rising shortfall: lost %d undrained %d", st.Lost, st.Undrained)
			}
			if strings.Join(got, ",") != strings.Join(tc.wantFail, ",") || loss != tc.wantLoss {
				t.Fatalf("failed %v loss %v, want %v loss %v", got, loss, tc.wantFail, tc.wantLoss)
			}
		})
	}
}

// An inconclusive identity read-back must not turn into a clean exact verdict.
func TestInconclusiveIdentityFallsBackToCounts(t *testing.T) {
	m := mqttMeasured{
		Profile: MQTTProfile{Rate: 100, MinAccepted: 10, MinOfferedRatio: 0.9, Devices: 1, MQTT: mqttload.Config{QoS: 1, TLS: true}},
		Drive:   mqttload.Snapshot{Sent: 1000, Acked: 1000, TLSConns: 1},
		Held:    10 * time.Second, Persisted: 1000, Reached: true, Mode: IdentityFull,
		Identity: &IdentityReport{Inconclusive: []string{"read failed"}},
	}
	bad := Invariant{Name: InvIdentity, Passed: false, Detail: "inconclusive"}
	m.IdentityInv = &bad
	invs, st, loss := mqttVerdict(m)
	if st.LostExact || loss {
		t.Fatalf("an inconclusive read-back produced an exact verdict: %+v", st)
	}
	rep := &MQTTReport{Invariants: invs}
	if rep.Passed() {
		t.Fatal("a run whose identity read-back failed passed")
	}
}

func TestMQTTProfileValidate(t *testing.T) {
	good := MQTTProfile{Rate: 10, Duration: time.Second, MetricKey: "m", MQTT: mqttload.Config{Broker: "ssl://h:1883", TLS: true, InstanceId: "i", Tenant: "t", QoS: 1}}.withDefaults()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*MQTTProfile){
		"no rate":        func(p *MQTTProfile) { p.Rate = 0 },
		"no duration":    func(p *MQTTProfile) { p.Duration = 0 },
		"unknown mode":   func(p *MQTTProfile) { p.Identity = "some" },
		"other manifest": func(p *MQTTProfile) { p.Manifest, p.MetricKey = "buildingpulse", "" },
		"bad ratio":      func(p *MQTTProfile) { p.MinOfferedRatio = 1.5 },
		"bad tolerance":  func(p *MQTTProfile) { p.QoS0Tolerance = -0.1 },
		"no broker":      func(p *MQTTProfile) { p.MQTT.Broker = "" },
	} {
		p := good
		mut(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMQTTReportWithoutADurabilityVerdictFails(t *testing.T) {
	r := &MQTTReport{Invariants: []Invariant{{Name: InvFleetConnected, Passed: true}}}
	if r.Passed() {
		t.Fatal("a report with no durability verdict passed")
	}
	if (&MQTTReport{}).Passed() {
		t.Fatal("an empty report passed")
	}
}

// laggingCounter is a platform still draining: every read shows a few more rows, and the count
// stays short of everything stored for as long as the test looks.
type laggingCounter struct {
	brokerStore
	mu    sync.Mutex
	shown int64
}

func (c *laggingCounter) Count(ctx context.Context, w Window) (int64, error) {
	all, _ := c.brokerStore.Count(ctx, w)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shown++
	return min(c.shown, all/2), nil
}

// A count still RISING when the quiesce timeout ends is a pipeline that has not drained, not one
// that lost events: the run fails as inconclusive with the shortfall reported, and never takes
// the loss exit code a release gate reads as "PUBACKed events are gone".
//
// GUARD: kills a run that skips the re-read after the timeout (every slow drain would be a LOSS).
func TestADrainStillRisingAtTheTimeoutIsNotALoss(t *testing.T) {
	b := fakemqtt.New(t)
	run := startRun(t, b, mqttTestProfile(b, IdentityCount), 4)
	run.Counter = &laggingCounter{brokerStore: brokerStore{b}}
	rep, err := run.driveAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Loss || rep.ExitCode() != ExitFail || rep.Storage.Lost != 0 || rep.Storage.Undrained == 0 {
		t.Fatalf("a still-rising count: loss %v exit %d lost %d undrained %d\n%s",
			rep.Loss, rep.ExitCode(), rep.Storage.Lost, rep.Storage.Undrained, rep.Human())
	}
	if d := inv(t, rep, InvPubackDurability); d.Passed || !strings.Contains(d.Detail, "still rising") {
		t.Fatalf("durability %+v", d)
	}
}

// A duplicate the broker stored is never a pass. With the identity read-back it is named (an
// extra copy, not a loss); a count sees the same rows as an excess. And where duplicates exactly
// offset PUBACKed losses, the count is blind (nothing unconfirmed, nothing re-sent, totals
// equal) while the identity read-back still names the loss: the reason the rc run reads every
// event back.
func TestStoredDuplicatesNeverPass(t *testing.T) {
	t.Run("duplicates alone", func(t *testing.T) {
		for _, mode := range []string{IdentityFull, IdentityCount} {
			b := fakemqtt.New(t)
			b.Duplicate = func(n int64) bool { return n%9 == 0 }
			run := startRun(t, b, mqttTestProfile(b, mode), 4)
			rep, err := run.driveAndReconcile(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if rep.Passed() || rep.Loss || rep.Storage.Excess == 0 {
				t.Fatalf("%s: stored duplicates: passed %v loss %v excess %d\n%s", mode, rep.Passed(), rep.Loss, rep.Storage.Excess, rep.Human())
			}
		}
	})
	t.Run("duplicates offsetting losses", func(t *testing.T) {
		build := func() *fakemqtt.Broker {
			b := fakemqtt.New(t)
			b.AckOnly = func(n int64) bool { return n%10 == 0 }
			b.Duplicate = func(n int64) bool { return n%10 == 5 }
			return b
		}
		b := build()
		run := startRun(t, b, mqttTestProfile(b, IdentityFull), 4)
		rep, err := run.driveAndReconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Loss || rep.ExitCode() != ExitLoss || !rep.Storage.LostExact {
			t.Fatalf("the identity read-back missed losses offset by duplicates:\n%s", rep.Human())
		}
	})
}
