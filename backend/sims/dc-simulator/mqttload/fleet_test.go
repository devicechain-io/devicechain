// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	testInstance = "dcinst"
	testTenant   = "acme"
)

func devices(n int) []sim.DeviceInstance {
	out := make([]sim.DeviceInstance, n)
	for i := range out {
		out[i] = sim.DeviceInstance{Token: fmt.Sprintf("dev-%03d", i), CredentialId: fmt.Sprintf("cred-%03d", i)}
	}
	return out
}

func testConfig(b *fakemqtt.Broker) Config {
	return Config{
		Broker: b.URL(), TLS: strings.HasPrefix(b.URL(), "ssl://"), TLSInsecure: strings.HasPrefix(b.URL(), "ssl://"),
		InstanceId: testInstance, Tenant: testTenant, QoS: 1, CleanSession: true,
		ConnectTimeout: 2 * time.Second, AckTimeout: 500 * time.Millisecond,
		BackoffBase: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
	}
}

func startFleet(t *testing.T, cfg Config, n int) *Fleet {
	t.Helper()
	f, err := NewFleet(cfg, devices(n))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Start(ctx)
	t.Cleanup(func() { cancel(); f.Close() })
	if got, err := f.AwaitConnected(ctx, 10*time.Second); err != nil {
		t.Fatalf("connected %d of %d: %v", got, n, err)
	}
	return f
}

func spec(rate float64, d time.Duration) DriveSpec {
	return DriveSpec{Rate: rate, Duration: d, MetricKey: "speed_kph"}
}

// Every device authenticates the way the platform's auth callout expects, publishes to its OWN
// events topic, and sends a body that carries no credential; every publish the broker answers
// is counted PUBACKed and filed accepted.
func TestFleetPublishesAsARealDevice(t *testing.T) {
	b := fakemqtt.New(t)
	b.Authorize = func(user, pass, clientID string) bool {
		return strings.HasPrefix(user, testTenant+":cred-") && pass == "" && strings.HasPrefix(clientID, testInstance)
	}
	f := startFleet(t, testConfig(b), 4)
	led := sim.NewIdentityLedger()

	res, err := f.Drive(context.Background(), spec(40, 1500*time.Millisecond), led)
	if err != nil {
		t.Fatal(err)
	}
	if res.End.Before(res.Start) || res.Drained.Before(res.End) {
		t.Fatalf("window out of order: %+v", res)
	}
	c := f.Counters.Snapshot()
	if c.Sent < 30 || c.Acked != c.Sent || c.Unconfirmed != 0 {
		t.Fatalf("sent %d acked %d unconfirmed %d: every publish the broker answered must be PUBACKed", c.Sent, c.Acked, c.Unconfirmed)
	}
	if c.Inflight != 0 {
		t.Fatalf("%d still in flight after the drive drained", c.Inflight)
	}
	stored := b.Stored()
	if int64(len(stored)) != c.Acked {
		t.Fatalf("broker kept %d, fleet counted %d PUBACKed", len(stored), c.Acked)
	}
	for _, s := range stored {
		wantClient, _ := messaging.DeviceClientID(testInstance, testTenant, s.Device)
		wantTopic := fmt.Sprintf("%s/%s/devices/%s/events", testInstance, testTenant, s.Device)
		if s.ClientID != wantClient || s.Topic != wantTopic {
			t.Fatalf("device %s published as %q to %q, want %q to %q", s.Device, s.ClientID, s.Topic, wantClient, wantTopic)
		}
		if want := testTenant + ":" + strings.Replace(s.Device, "dev-", "cred-", 1); s.Username != want {
			t.Fatalf("username %q, want %q", s.Username, want)
		}
		var body map[string]any
		if err := json.Unmarshal(s.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"credentialType", "credentialId", "credentialSecret"} {
			if _, has := body[k]; has {
				t.Fatalf("the MQTT body carries %q", k)
			}
		}
	}
	snap := led.Snapshot()
	if got := snap.Measurements(); got.Accepted != c.Acked || got.Ambiguous != 0 || got.Refused != 0 {
		t.Fatalf("ledger %+v, want %d accepted and nothing else", got, c.Acked)
	}
	if p := f.Lat.Percentiles(); p.Count != c.Acked || p.P50 <= 0 || p.P99 < p.P50 || p.Max < p.P99 {
		t.Fatalf("latency percentiles %+v for %d acks", p, c.Acked)
	}
}

// A publish the broker never answers is NOT counted PUBACKed: it times out unconfirmed and the
// ledger files it ambiguous, because it may or may not have been stored.
func TestUnansweredPublishIsUnconfirmedNotAcked(t *testing.T) {
	b := fakemqtt.New(t)
	b.Silent = func(n int64) bool { return n%2 == 0 }
	f := startFleet(t, testConfig(b), 2)
	led := sim.NewIdentityLedger()

	if _, err := f.Drive(context.Background(), spec(20, 1*time.Second), led); err != nil {
		t.Fatal(err)
	}
	c := f.Counters.Snapshot()
	if c.Unconfirmed == 0 || c.Acked == 0 {
		t.Fatalf("expected both answered and unanswered publishes, got acked %d unconfirmed %d", c.Acked, c.Unconfirmed)
	}
	if c.Acked+c.Unconfirmed != c.Sent {
		t.Fatalf("acked %d + unconfirmed %d != sent %d: a publish went unaccounted", c.Acked, c.Unconfirmed, c.Sent)
	}
	m := led.Snapshot().Measurements()
	if m.Accepted != c.Acked || m.Ambiguous != c.Unconfirmed {
		t.Fatalf("ledger %+v disagrees with counters acked %d unconfirmed %d", m, c.Acked, c.Unconfirmed)
	}
}

// A broker that drops every connection mid-run: the fleet reconnects every session with
// backoff, publishes resume, the drop is counted, and the ticks that found a session down are
// counted rather than silently missing.
func TestFleetReconnectsAfterTheBrokerDropsEveryConnection(t *testing.T) {
	b := fakemqtt.New(t)
	f := startFleet(t, testConfig(b), 3)
	led := sim.NewIdentityLedger()

	go func() {
		time.Sleep(500 * time.Millisecond)
		b.DropConnections()
	}()
	if _, err := f.Drive(context.Background(), spec(60, 2500*time.Millisecond), led); err != nil {
		t.Fatal(err)
	}
	c := f.Counters.Snapshot()
	if c.ConnectionLosses < 3 {
		t.Fatalf("connection losses = %d, want at least the 3 dropped", c.ConnectionLosses)
	}
	if c.Connects < 6 {
		t.Fatalf("connects = %d: the 3 sessions must each connect again after the drop", c.Connects)
	}
	if c.Up != 3 {
		t.Fatalf("%d sessions up at the end, want 3", c.Up)
	}
	late := 0
	for _, s := range b.Stored() {
		if time.Since(s.OccurredTime) < 1500*time.Millisecond {
			late++
		}
	}
	if late == 0 {
		t.Fatal("nothing was stored after the reconnect: the sessions did not resume publishing")
	}
	// Whatever the drop took, every publish handed to the transport is accounted for.
	if c.Acked+c.Unconfirmed != c.Sent {
		t.Fatalf("acked %d + unconfirmed %d != sent %d", c.Acked, c.Unconfirmed, c.Sent)
	}
}

// The reconnect delay doubles from the base to the cap and is jittered inside its band, so a
// fleet that lost its broker together does not return together.
func TestBackoffIsBoundedAndJittered(t *testing.T) {
	f := &Fleet{cfg: Config{BackoffBase: 100 * time.Millisecond, BackoffMax: 800 * time.Millisecond}}
	for n, ceil := range []time.Duration{100, 200, 400, 800, 800, 800} {
		ceil *= time.Millisecond
		seen := map[time.Duration]bool{}
		for i := 0; i < 200; i++ {
			d := f.backoff(n)
			if d < ceil/2 || d > ceil {
				t.Fatalf("backoff(%d) = %s, want within [%s, %s]", n, d, ceil/2, ceil)
			}
			seen[d] = true
		}
		if len(seen) < 10 {
			t.Fatalf("backoff(%d) produced only %d distinct delays in 200 draws: not jittered", n, len(seen))
		}
	}
}

// A device the broker refuses never connects, and the fleet says so rather than pretending.
func TestRefusedCredentialNeverConnects(t *testing.T) {
	b := fakemqtt.New(t)
	b.Authorize = func(string, string, string) bool { return false }
	f, err := NewFleet(testConfig(b), devices(2))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Start(ctx)
	defer func() { cancel(); f.Close() }()
	n, err := f.AwaitConnected(ctx, 600*time.Millisecond)
	if err == nil || n != 0 || f.NeverConnected() != 2 {
		t.Fatalf("connected %d, err %v, never %d: a refused device must not count as connected", n, err, f.NeverConnected())
	}
	if f.Counters.Snapshot().ConnectFailures == 0 {
		t.Fatal("refused connects were not counted as failures")
	}
}

// THE FLAG TELLS THE TRUTH. TLS on dials TLS and reports the version it negotiated; TLS off
// dials plaintext. A flag that disagrees with the broker fails to connect instead of quietly
// running the other transport.
func TestTLSFlagIsTruthful(t *testing.T) {
	t.Run("tls on against a tls broker", func(t *testing.T) {
		b := fakemqtt.NewTLS(t)
		f := startFleet(t, testConfig(b), 2)
		c := f.Counters.Snapshot()
		if c.TLSConns < 2 || c.PlainConns != 0 {
			t.Fatalf("tls %d plaintext %d, want only TLS", c.TLSConns, c.PlainConns)
		}
		if v := f.TLSVersions(); len(v) == 0 {
			t.Fatal("no negotiated TLS version was recorded")
		}
		if !strings.HasPrefix(f.Broker(), "ssl://") {
			t.Fatalf("dialled %q with TLS on", f.Broker())
		}
	})
	t.Run("tls off against a plaintext broker", func(t *testing.T) {
		b := fakemqtt.New(t)
		f := startFleet(t, testConfig(b), 2)
		c := f.Counters.Snapshot()
		if c.PlainConns < 2 || c.TLSConns != 0 {
			t.Fatalf("tls %d plaintext %d, want only plaintext", c.TLSConns, c.PlainConns)
		}
		if !strings.HasPrefix(f.Broker(), "tcp://") {
			t.Fatalf("dialled %q with TLS off", f.Broker())
		}
	})
	t.Run("tls off against a tls broker does not connect", func(t *testing.T) {
		b := fakemqtt.NewTLS(t)
		cfg := testConfig(b)
		cfg.TLS, cfg.TLSInsecure = false, false
		f, err := NewFleet(cfg, devices(1))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		f.Start(ctx)
		defer func() { cancel(); f.Close() }()
		if n, err := f.AwaitConnected(ctx, 700*time.Millisecond); err == nil || n != 0 {
			t.Fatalf("connected %d (err %v): a plaintext dial of a TLS broker must fail, not be upgraded", n, err)
		}
		if c := f.Counters.Snapshot(); c.TLSConns != 0 || c.PlainConns == 0 {
			t.Fatalf("tls %d plaintext %d: the attempt was not plaintext", c.TLSConns, c.PlainConns)
		}
	})
	t.Run("tls on verifies the certificate unless told not to", func(t *testing.T) {
		b := fakemqtt.NewTLS(t)
		cfg := testConfig(b)
		cfg.TLSInsecure = false
		f, err := NewFleet(cfg, devices(1))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		f.Start(ctx)
		defer func() { cancel(); f.Close() }()
		if n, err := f.AwaitConnected(ctx, 700*time.Millisecond); err == nil || n != 0 {
			t.Fatalf("connected %d (err %v): a self-signed certificate must be refused when verification is on", n, err)
		}
	})
}

func TestResolveBrokerFollowsTheTLSFlag(t *testing.T) {
	for _, tc := range []struct {
		in      string
		tls     bool
		want    string
		wantErr bool
	}{
		{"ssl://10.0.0.1:1883", true, "ssl://10.0.0.1:1883", false},
		{"ssl://10.0.0.1:1883", false, "tcp://10.0.0.1:1883", false},
		{"tcp://10.0.0.1:1883", true, "ssl://10.0.0.1:1883", false},
		{"mqtts://broker:8883", false, "tcp://broker:8883", false},
		{"broker:1883", true, "ssl://broker:1883", false},
		{"wss://broker/mqtt", false, "ws://broker/mqtt", false},
		{"ws://broker/mqtt", true, "wss://broker/mqtt", false},
		{"http://broker", true, "", true},
		{"ssl://", true, "", true},
	} {
		got, err := ResolveBroker(tc.in, tc.tls)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ResolveBroker(%q, %v) = %q, %v; want %q (err %v)", tc.in, tc.tls, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestConfigValidateRefusesWhatCannotRun(t *testing.T) {
	ok := Config{Broker: "ssl://h:1883", TLS: true, InstanceId: "i", Tenant: "t", QoS: 1}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"no broker":             func(c *Config) { c.Broker = "" },
		"qos 2":                 func(c *Config) { c.QoS = 2 },
		"insecure without tls":  func(c *Config) { c.TLS = false; c.TLSInsecure = true },
		"bad tenant":            func(c *Config) { c.Tenant = "a b" },
		"websocket broker":      func(c *Config) { c.Broker = "wss://h/mqtt" },
		"too much in flight":    func(c *Config) { c.MaxInflight = MaxMaxInflight + 1 },
		"backoff base over max": func(c *Config) { c.BackoffBase = time.Minute; c.BackoffMax = time.Second },
	} {
		c := ok
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDriveSpecValidate(t *testing.T) {
	good := DriveSpec{Rate: 100, Duration: time.Second, MetricKey: "m"}
	if err := good.Validate(10); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*DriveSpec){
		"zero rate":     func(s *DriveSpec) { s.Rate = 0 },
		"zero duration": func(s *DriveSpec) { s.Duration = 0 },
		"no metric":     func(s *DriveSpec) { s.MetricKey = "" },
		"negative pad":  func(s *DriveSpec) { s.PadBytes = -1 },
		"too fast":      func(s *DriveSpec) { s.Rate = 1e6 },
	} {
		s := good
		mut(&s)
		if err := s.Validate(10); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The synchronous wire, the one a scenario's EmitAll drives, returns nil only for a real
// PUBACK, files the right outcome, and files Refused (certainly absent) only when nothing was
// sent.
func TestPostFilesWhatTheBrokerSaid(t *testing.T) {
	b := fakemqtt.New(t)
	var silent atomic.Bool
	b.Silent = func(int64) bool { return silent.Load() }
	f := startFleet(t, testConfig(b), 1)
	led := sim.NewIdentityLedger()
	rt := &sim.Runtime{Identity: led, Wire: f, InstanceId: testInstance, Tenant: testTenant}
	d := devices(1)[0]
	post := func() error {
		stamp := sim.EventTimestamp()
		payload, err := sim.MeasurementPayload(map[string]float64{"speed_kph": 1}, stamp, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f.Post(context.Background(), rt, d, "Measurement", stamp, payload)
	}

	if err := post(); err != nil {
		t.Fatalf("acknowledged publish returned %v", err)
	}
	silent.Store(true)
	if err := post(); err == nil {
		t.Fatal("an unanswered publish returned nil: it must not look acknowledged")
	}
	silent.Store(false)
	b.DropConnections()
	deadline := time.Now().Add(5 * time.Second)
	for f.Counters.Up.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// With the session down the publish is never sent. (The fleet may already have
	// reconnected on a slow machine, in which case it is acknowledged; either is truthful.)
	if err := post(); err != nil && !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unexpected error %v", err)
	}
	m := led.Snapshot().Measurements()
	if m.Accepted < 1 || m.Ambiguous != 1 {
		t.Fatalf("ledger %+v, want >=1 accepted and exactly 1 ambiguous", m)
	}
}

func TestLatencyHistPercentiles(t *testing.T) {
	var h LatencyHist
	for i := 1; i <= 1000; i++ {
		h.Observe(time.Duration(i) * time.Millisecond)
	}
	p := h.Percentiles()
	near := func(name string, got, want float64) {
		t.Helper()
		if got < want*0.9 || got > want*1.15 {
			t.Errorf("%s = %.1fms, want about %.0fms", name, got, want)
		}
	}
	near("p50", p.P50, 500)
	near("p90", p.P90, 900)
	near("p99", p.P99, 990)
	if p.Max != 1000 || p.Count != 1000 {
		t.Errorf("max %v count %d", p.Max, p.Count)
	}
	if (&LatencyHist{}).Percentiles().Count != 0 {
		t.Error("empty histogram reports samples")
	}
}

// A scenario's own emit path (sim.EmitAll, the one the detection harness's background fleet and
// probes use) runs over the fleet unchanged: Stats.Emitted counts PUBACKs, the unanswered ones
// are Failed, and the identity ledger agrees with the driver's counters.
func TestEmitAllRunsOverTheFleetWire(t *testing.T) {
	b := fakemqtt.New(t)
	b.Silent = func(n int64) bool { return n%5 == 0 }
	f := startFleet(t, testConfig(b), 5)
	led := sim.NewIdentityLedger()
	rt := &sim.Runtime{Identity: led, Wire: f, InstanceId: testInstance, Tenant: testTenant, Devices: devices(5)}
	for round := 0; round < 4; round++ {
		_ = sim.EmitAll(context.Background(), rt, 5, func(int, sim.DeviceInstance) map[string]float64 {
			return map[string]float64{"speed_kph": 7}
		})
	}
	snap := rt.Stats.Snapshot(time.Now())
	m := led.Snapshot().Measurements()
	if snap.Emitted == 0 || snap.Failed == 0 || snap.Emitted+snap.Failed != 20 {
		t.Fatalf("emitted %d failed %d of 20", snap.Emitted, snap.Failed)
	}
	if m.Accepted != snap.Emitted || m.Ambiguous != snap.Failed {
		t.Fatalf("ledger %+v disagrees with the driver's counters %+v", m, snap)
	}
	if int64(len(b.Stored())) < snap.Emitted {
		t.Fatalf("broker kept %d, fewer than the %d it PUBACKed", len(b.Stored()), snap.Emitted)
	}
}

// A persistent session that reconnects re-sends its unacknowledged publish, and paho completes
// the ORIGINAL token with no error at that moment although no PUBACK exists. The fleet must not
// read that as an acknowledgement (core/test/mqttack): the publish is acked when the re-send's
// PUBACK arrives, and not before.
func TestAResumedPublishIsAckedOnlyByItsPuback(t *testing.T) {
	b := fakemqtt.New(t)
	b.Silent = func(n int64) bool { return n == 1 } // the first publish is lost on the wire
	cfg := testConfig(b)
	cfg.CleanSession = false
	cfg.AckTimeout = 5 * time.Second
	f := startFleet(t, cfg, 1)
	led := sim.NewIdentityLedger()
	rt := &sim.Runtime{Identity: led, Wire: f, InstanceId: testInstance, Tenant: testTenant}
	d := devices(1)[0]

	stamp := sim.EventTimestamp()
	payload, err := sim.MeasurementPayload(map[string]float64{"speed_kph": 1}, stamp, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		b.DropConnections()
	}()
	start := time.Now()
	if err := f.Post(context.Background(), rt, d, "Measurement", stamp, payload); err != nil {
		t.Fatalf("the re-sent publish was never acknowledged: %v", err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("acknowledged after %s, before the connection even dropped: a completed token was read as a PUBACK", time.Since(start))
	}
	if n := len(b.Stored()); n != 1 {
		t.Fatalf("broker kept %d copies, want exactly the re-send", n)
	}
	if m := led.Snapshot().Measurements(); m.Accepted != 1 || m.Ambiguous != 0 {
		t.Fatalf("ledger %+v", m)
	}
}
