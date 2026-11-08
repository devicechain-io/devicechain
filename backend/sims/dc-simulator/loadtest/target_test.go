// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/internal/dropproxy"
	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/mqttload"
)

// targetCreds is n token-authenticated devices.
func targetCreds(n int) []mqttload.Credential {
	out := make([]mqttload.Credential, n)
	for i := range out {
		d := fmt.Sprintf("rig-%03d", i)
		out[i] = mqttload.Credential{Device: d, ClientID: d, Username: "tok-" + d, Topic: "devices/" + d + "/events"}
	}
	return out
}

// targetProfileFor aims a short run at publishAddr (a fake broker or a proxy in front of one)
// with the tap on tapAddr.
func targetProfileFor(publishAddr, tapAddr string, creds []mqttload.Credential) TargetProfile {
	return TargetProfile{
		MQTTProfile: MQTTProfile{
			Rate: 200, Duration: 1500 * time.Millisecond,
			Identity: IdentityFull, MinAccepted: 10, MinOfferedRatio: 0.5, ReportInterval: -1,
			QuiescePoll: 10 * time.Millisecond, QuiesceTimeout: 600 * time.Millisecond, QuiesceSettle: 20 * time.Millisecond,
			MQTT: mqttload.Config{
				Broker: "tcp://" + publishAddr, QoS: 1, CleanSession: true,
				ConnectTimeout: 2 * time.Second, AckTimeout: 500 * time.Millisecond,
				BackoffBase: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
			},
		},
		Credentials: creds,
		Tap:         mqttload.TapConfig{Broker: "tcp://" + tapAddr, ConnectTimeout: 2 * time.Second},
	}
}

func fanoutBroker(t *testing.T) *fakemqtt.Broker {
	b := fakemqtt.New(t)
	b.Fanout = true
	return b
}

// The positive half of the control: a broker that delivers everything it acknowledges passes,
// every device authenticates with its own credential, and the report says what it measured.
func TestTargetRunPassesAgainstAHonestBroker(t *testing.T) {
	b := fanoutBroker(t)
	b.Authorize = func(user, _, clientID string) bool {
		return strings.HasPrefix(clientID, "dc-rehearsal-tap-") || user == "tok-"+clientID
	}

	rep, err := RunTarget(context.Background(), targetProfileFor(b.Addr(), b.Addr(), targetCreds(4)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExitCode() != ExitPass {
		t.Fatalf("exit %d, want pass:\n%s", rep.ExitCode(), rep.Human())
	}
	if rep.Storage.Promised == 0 || rep.Storage.Persisted != rep.Storage.Promised || !rep.Storage.LostExact || rep.Storage.IdentityMode != IdentityFull {
		t.Fatalf("storage %+v", rep.Storage)
	}
	if rep.Target == nil || rep.Target.Authentication["password"] != 4 || rep.Target.Limit != TargetLimit {
		t.Fatalf("target info %+v", rep.Target)
	}
	if rep.Tenant != TargetTenant || rep.Manifest != TargetManifest {
		t.Fatalf("a target run names no tenant or scenario: %s/%s", rep.Tenant, rep.Manifest)
	}
	if !strings.Contains(rep.Human(), "broker-to-subscriber loss") {
		t.Fatalf("the human report must state the verdict's limit:\n%s", rep.Human())
	}
}

// The NEGATIVE CONTROL: the same run through a proxy that PUBACKs every 10th publish and never
// forwards it is a LOSS, exits 3, and names exactly the number of messages the proxy dropped.
func TestTargetRunThroughTheDropProxyIsALoss(t *testing.T) {
	b := fanoutBroker(t)
	px, err := dropproxy.Start("127.0.0.1:0", b.Addr(), 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(px.Close)

	// Publishers dial the proxy; the tap reads the real broker.
	rep, err := RunTarget(context.Background(), targetProfileFor(px.Addr(), b.Addr(), targetCreds(4)))
	if err != nil {
		t.Fatal(err)
	}
	if px.Dropped() == 0 {
		t.Fatal("the proxy dropped nothing: the control proves nothing")
	}
	if rep.ExitCode() != ExitLoss || !rep.Loss {
		t.Fatalf("exit %d, want loss:\n%s", rep.ExitCode(), rep.Human())
	}
	if rep.Storage.Lost != px.Dropped() || !rep.Storage.LostExact {
		t.Fatalf("verdict says %d lost (exact=%v), the proxy dropped %d:\n%s", rep.Storage.Lost, rep.Storage.LostExact, px.Dropped(), rep.Human())
	}
	if want := rep.Drive.Acked - px.Dropped(); rep.Storage.Persisted != want {
		t.Fatalf("delivered %d, want PUBACKed %d - dropped %d", rep.Storage.Persisted, rep.Drive.Acked, px.Dropped())
	}
}

// A broker that PUBACKs without keeping (the loss the platform mode was built to find) is found
// the same way, by the tap, on a target that is not DeviceChain.
func TestTargetRunFindsABrokerThatAcksWithoutKeeping(t *testing.T) {
	b := fanoutBroker(t)
	b.AckOnly = func(n int64) bool { return n%25 == 0 }

	rep, err := RunTarget(context.Background(), targetProfileFor(b.Addr(), b.Addr(), targetCreds(4)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExitCode() != ExitLoss {
		t.Fatalf("exit %d, want loss:\n%s", rep.ExitCode(), rep.Human())
	}
	if want := b.Received() / 25; rep.Storage.Lost != want {
		t.Fatalf("lost %d, want %d", rep.Storage.Lost, want)
	}
}

// A tap whose own session dropped cannot call a shortfall the target's loss.
func TestTapDropMakesALossInconclusive(t *testing.T) {
	r := &MQTTReport{
		Loss:       true,
		Invariants: []Invariant{{Name: InvPubackDurability, Passed: false, Detail: "LOSS: 3 PUBACKed event(s) are not persisted"}},
	}
	applyTapEvidence(r, mqttload.TapStats{ConnectionLosses: 1})
	if r.Loss {
		t.Fatal("a loss the tap cannot attribute must not exit 3")
	}
	if r.ExitCode() != ExitFail {
		t.Fatalf("exit %d, want %d", r.ExitCode(), ExitFail)
	}
	if got := invariantByName(r.Invariants, InvTapContinuity); got == nil || got.Passed {
		t.Fatalf("continuity invariant %+v", got)
	}
	if d := invariantByName(r.Invariants, InvPubackDurability).Detail; !strings.HasPrefix(d, "inconclusive") {
		t.Fatalf("durability detail %q", d)
	}
}

func TestRegressionInvariant(t *testing.T) {
	for _, tc := range []struct {
		cur, base, ratio float64
		ok               bool
	}{
		{10, 10, 0.25, true},
		{12.5, 10, 0.25, true},
		{12.6, 10, 0.25, false},
		{0, 10, 0.25, false}, // no PUBACKs is not a fast run
	} {
		if got := regressionInvariant(tc.cur, tc.base, tc.ratio); got.Passed != tc.ok {
			t.Errorf("cur %v base %v ratio %v: passed=%v want %v (%s)", tc.cur, tc.base, tc.ratio, got.Passed, tc.ok, got.Detail)
		}
	}
}

func TestParseTarget(t *testing.T) {
	for _, tc := range []struct {
		in, broker string
		tls        bool
		err        string
	}{
		{"mqtt://mosquitto:1883", "tcp://mosquitto:1883", false, ""},
		{"mqtts://broker.example:8883", "ssl://broker.example:8883", true, ""},
		{"wss://broker.example/mqtt", "", false, "not driven by this build"},
		{"https://ingest.example", "", false, "not driven by this build"},
		{"mosquitto:1883", "", false, "no scheme"},
		{"mqtt://user:pw@host:1883", "", false, "credentials in the URL"},
		{"mqtt://host:1883/topic", "", false, "path"},
		{"", "", false, "empty"},
		{"ftp://host", "", false, "not one of"},
	} {
		b, tls, err := ParseTarget(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || b != tc.broker || tls != tc.tls {
			t.Errorf("%q: got %q %v %v", tc.in, b, tls, err)
		}
	}
}

func TestTargetValidateNeedsABaselineForARegressionCeiling(t *testing.T) {
	p, err := targetProfileFor("127.0.0.1:1", "127.0.0.1:1", targetCreds(1)).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	p.MaxP99Regression = 0.2
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "--baseline") {
		t.Fatalf("err %v", err)
	}
}

// A baseline that itself failed is no baseline: a regression is measured against a run that held.
func TestTargetValidateRefusesAFailedBaseline(t *testing.T) {
	p, err := targetProfileFor("127.0.0.1:1", "127.0.0.1:1", targetCreds(1)).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	failed := &MQTTReport{Invariants: []Invariant{{Name: InvPubackDurability, Passed: false}}}
	failed.Drive.AckLatency.P99 = 5
	p.Baseline, p.MaxP99Regression = failed, 0.2
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("err %v", err)
	}
}

// A credential row's own topic that the tap filter does not cover would be delivered and read
// back as lost: refused before anything is sent.
func TestTargetValidateRefusesATopicTheTapCannotSee(t *testing.T) {
	creds := targetCreds(2)
	creds[1].Topic = "elsewhere/rig-001"
	p, err := targetProfileFor("127.0.0.1:1", "127.0.0.1:1", creds).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("err %v", err)
	}
}

// A baseline with no allowed growth would compare nothing and pass any latency.
func TestTargetValidateRefusesABaselineWithoutACeiling(t *testing.T) {
	p, err := targetProfileFor("127.0.0.1:1", "127.0.0.1:1", targetCreds(1)).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	ok := &MQTTReport{Invariants: []Invariant{{Name: InvPubackDurability, Passed: true}}}
	ok.Drive.AckLatency.P99 = 5
	p.Baseline, p.MaxP99Regression = ok, 0
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "--max-p99-regression") {
		t.Fatalf("err %v", err)
	}
}

// A rejected address must not be echoed: it may carry the password it was rejected for.
func TestParseTargetNeverEchoesAPassword(t *testing.T) {
	for _, in := range []string{"mqtt://user:s3cret@host:1883", "mqtt://user:s3cret@ho st:1883", "mqtt://us er:s3cret@host"} {
		_, _, err := ParseTarget(in)
		if err == nil {
			t.Fatalf("%q must be refused", in)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("%q: error leaks the password: %v", in, err)
		}
	}
}
