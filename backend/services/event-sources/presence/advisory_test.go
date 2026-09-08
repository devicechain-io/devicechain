// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"strings"
	"testing"
	"time"
)

// 🔴 EVERY FIXTURE IN THIS FILE IS RAW BROKER JSON, CAPTURED FROM nats-server v2.14.4.
// That is the whole point of the file. The defect these tests exist to catch is a
// decoder reading the wrong JSON key — the advisory spells the MQTT client id
// `client_id` while a CONNZ reply spells it `mqtt_client` — and a fixture built by
// marshalling our own struct cannot see that: it would round-trip through the same
// tags the decoder uses and agree with itself no matter which key was wrong. A wrong
// key does not fail, it yields "", which reads as "no devices are connected".
//
// Fields the code does not read are kept in the fixtures deliberately. They are what
// the broker actually sends, and trimming them to what we happen to parse would hide
// a future field collision.

const connectAdvisory = `{
  "client": {
    "acc": "APP",
    "client_id": "inst-1:acme:sensor-001",
    "client_type": "mqtt",
    "host": "127.0.0.1",
    "id": 7,
    "kind": "Client",
    "name_tag": "APP",
    "start": "2026-08-12T12:37:44.076882575-04:00",
    "user": "dev"
  },
  "id": "Xmf3wmrFrgeBK9RbHBvRIN",
  "server": {"host":"127.0.0.1","id":"NCC5R4","name":"spike","seq":14,"ver":"2.14.4"},
  "timestamp": "2026-08-12T16:37:44.076978675Z",
  "type": "io.nats.server.advisory.v1.client_connect"
}`

const disconnectAdvisory = `{
  "client": {
    "acc": "APP",
    "client_id": "inst-1:acme:sensor-001",
    "client_type": "mqtt",
    "host": "127.0.0.1",
    "id": 7,
    "kind": "Client",
    "name_tag": "APP",
    "start": "2026-08-12T12:37:44.076882575-04:00",
    "stop": "2026-08-12T12:37:44.087679375-04:00",
    "user": "dev"
  },
  "id": "Xmf3wmrFrgeBK9RbHBvRoY",
  "reason": "Client Closed",
  "received": {"bytes":0,"msgs":0},
  "sent": {"bytes":0,"msgs":0},
  "server": {"host":"127.0.0.1","id":"NCC5R4","name":"spike","seq":52,"ver":"2.14.4"},
  "timestamp": "2026-08-12T12:37:44.087679375-04:00",
  "type": "io.nats.server.advisory.v1.client_disconnect"
}`

// A service connection: no MQTT client id at all. This is most of what the tap sees.
const serviceAdvisory = `{
  "client": {"acc":"APP","kind":"Client","lang":"go","start":"2026-08-12T12:37:44.075159775-04:00","user":"dc_service"},
  "id": "Xmf3wmrFrgeBK9RbHBvRAA",
  "server": {"host":"127.0.0.1","id":"NCC5R4","name":"spike","ver":"2.14.4"},
  "type": "io.nats.server.advisory.v1.client_connect"
}`

// The start instant carried by both fixtures above, as the session id must derive it.
const wantSession = uint64(1786552664076882575)

func mustTransition(t *testing.T, instance string, connected bool, raw string) Transition {
	t.Helper()
	got, skip := TransitionFor(instance, connected, []byte(raw))
	if skip != "" {
		t.Fatalf("advisory was skipped as %q; it must produce a transition", skip)
	}
	return got
}

// A device's connect becomes a CONNECTED transition for the right tenant and device,
// at the broker's start instant, with the session id derived from it.
func TestAConnectAdvisoryAssertsTheDevice(t *testing.T) {
	got := mustTransition(t, "inst-1", true, connectAdvisory)

	if got.Tenant != "acme" || got.DeviceToken != "sensor-001" {
		t.Errorf("transition = %s, want tenant acme device sensor-001", got.describe())
	}
	if !got.Event.Connected {
		t.Error("a connect advisory produced a DISCONNECTED transition")
	}
	if got.Event.SessionId != wantSession {
		t.Errorf("session = %d, want %d (the connection's start in UnixNano)", got.Event.SessionId, wantSession)
	}
	// The event time is the broker's start, not anything of ours. Asserted as an
	// absolute instant so a receipt-clock implementation cannot pass.
	wantAt := time.Date(2026, 8, 12, 16, 37, 44, 76882575, time.UTC)
	if !got.Event.OccurredAt.Equal(wantAt) {
		t.Errorf("occurredAt = %v, want the broker's start %v", got.Event.OccurredAt, wantAt)
	}
}

// 🔴 The death is stamped at STOP, not at start. Both instants are in the same
// advisory, they differ by ~11ms here, and picking the wrong one records the wrong
// death time — which is exactly what the offline alarm and LastDisconnectTime report.
// The session id must STILL come from start, because that is what the birth used.
func TestADisconnectAdvisoryIsStampedAtTheStopInstant(t *testing.T) {
	got := mustTransition(t, "inst-1", false, disconnectAdvisory)

	if got.Event.Connected {
		t.Error("a disconnect advisory produced a CONNECTED transition")
	}
	wantStop := time.Date(2026, 8, 12, 16, 37, 44, 87679375, time.UTC)
	if !got.Event.OccurredAt.Equal(wantStop) {
		t.Errorf("occurredAt = %v, want the broker's stop %v", got.Event.OccurredAt, wantStop)
	}
	// The discriminator between "stamped at stop" and "stamped at start" is that the
	// two differ; assert that explicitly so a fixture edit that made them equal would
	// break the test rather than quietly disarm it.
	wantStart := time.Date(2026, 8, 12, 16, 37, 44, 76882575, time.UTC)
	if wantStop.Equal(wantStart) {
		t.Fatal("fixture start and stop are equal, so this test cannot tell the two implementations apart")
	}
	if got.Event.SessionId != wantSession {
		t.Errorf("session = %d, want %d — a death must name the session that was born", got.Event.SessionId, wantSession)
	}
	// The broker's reason rides through to the operator.
	if got.Event.Reason != "Client Closed" {
		t.Errorf("reason = %q, want the broker's own %q", got.Event.Reason, "Client Closed")
	}
}

// A birth and its death must name ONE session, or presence.Decide sees two unrelated
// transitions and the death never applies. This is the property the whole design rests
// on and it is cheap to state directly.
func TestABirthAndItsDeathShareOneSession(t *testing.T) {
	birth := mustTransition(t, "inst-1", true, connectAdvisory)
	death := mustTransition(t, "inst-1", false, disconnectAdvisory)

	if birth.Event.SessionId != death.Event.SessionId {
		t.Errorf("birth session %d != death session %d", birth.Event.SessionId, death.Event.SessionId)
	}
	if !death.Event.OccurredAt.After(birth.Event.OccurredAt) {
		t.Errorf("death at %v does not follow birth at %v, so Decide would reject it",
			death.Event.OccurredAt, birth.Event.OccurredAt)
	}
}

// Everything that is not one of this instance's plain device connections is skipped,
// by a named reason. The service connection is the live case — the tap sees one for
// every service on the instance, continuously.
func TestWhatDoesNotDrivePresence(t *testing.T) {
	discriminated := strings.Replace(connectAdvisory,
		`"client_id": "inst-1:acme:sensor-001"`, `"client_id": "inst-1:acme:sensor-001:pub"`, 1)
	otherInstance := strings.Replace(connectAdvisory,
		`"client_id": "inst-1:acme:sensor-001"`, `"client_id": "inst-9:acme:sensor-001"`, 1)
	noStart := strings.Replace(connectAdvisory,
		`"start": "2026-08-12T12:37:44.076882575-04:00",`, ``, 1)
	zeroStart := strings.Replace(connectAdvisory,
		`"start": "2026-08-12T12:37:44.076882575-04:00"`, `"start": "0001-01-01T00:00:00Z"`, 1)

	for _, tc := range []struct {
		name      string
		connected bool
		raw       string
		want      SkipReason
	}{
		{"a service connection", true, serviceAdvisory, SkipNotADeviceConnection},
		{"another instance's device", true, otherInstance, SkipNotThisInstance},
		{"a device's second connection", true, discriminated, SkipDiscriminatedSession},
		{"an advisory with no start", true, noStart, SkipNoConnectionTime},
		{"an advisory whose start is the zero instant", true, zeroStart, SkipNoConnectionTime},
		{"a death with no stop", false, connectAdvisory, SkipNoConnectionTime},
		{"not JSON at all", true, "}{", SkipMalformed},
		{"an empty payload", true, "", SkipMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, skip := TransitionFor("inst-1", tc.connected, []byte(tc.raw))
			if skip != tc.want {
				t.Fatalf("skip = %q, want %q (transition %+v)", skip, tc.want, got)
			}
			if got != (Transition{}) {
				t.Errorf("a skipped advisory still produced %s", got.describe())
			}
		})
	}
}

// 🔑 THE DISCRIMINATED-SESSION SKIP IS THE ONE THAT LOOKS LIKE A BUG, so it gets an
// explicit statement of the hazard it prevents rather than a table row alone. A
// subscribed device holds a long-lived connection; a one-shot publisher connects
// beside it with a discriminator and hangs up. If that death drove presence, the
// device would flip asserted-offline while its real session is up — and an asserted
// row is not resurrected by data events (the freshness branch in Api.MergeDeviceState
// guards on PresenceSourceAsserted, device-state/model/api.go), so it would stay wrong
// until the device reconnected entirely.
func TestASideConnectionCannotKillALiveDevice(t *testing.T) {
	sideDeath := strings.Replace(disconnectAdvisory,
		`"client_id": "inst-1:acme:sensor-001"`, `"client_id": "inst-1:acme:sensor-001:pub"`, 1)

	got, skip := TransitionFor("inst-1", false, []byte(sideDeath))
	if skip != SkipDiscriminatedSession {
		t.Fatalf("a side connection's death produced %s (skip %q); it must not drive presence",
			got.describe(), skip)
	}

	// The counterweight: the device's OWN death must still be honoured, or the rule
	// above would be satisfied by a tap that never emits a disconnect at all.
	own, skip := TransitionFor("inst-1", false, []byte(disconnectAdvisory))
	if skip != "" {
		t.Fatalf("the device's own death was skipped as %q", skip)
	}
	if own.Event.Connected {
		t.Error("the device's own death did not produce a DISCONNECTED transition")
	}
}

// SessionIDFrom must refuse rather than return a plausible-looking zero. A session id
// of 0 is the projection's "no session yet", so a device whose start could not be read
// would be given the identity of a device that has never connected — and every real
// transition after it would look like a new session.
func TestSessionIDRefusesWhatItCannotDerive(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	before := time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		start *time.Time
	}{
		{"nil", nil},
		{"the zero instant", &time.Time{}},
		{"the unix epoch itself", &epoch},
		{"before the unix epoch", &before},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := SessionIDFrom(tc.start); ok {
				t.Errorf("derived session %d from %v; it must be refused", got, tc.start)
			}
		})
	}
	// The counterweight: an ordinary instant must still derive, or the guard above
	// would be satisfied by a function that refuses everything.
	now := time.Date(2026, 8, 12, 16, 37, 44, 76882575, time.UTC)
	got, ok := SessionIDFrom(&now)
	if !ok || got != wantSession {
		t.Errorf("SessionIDFrom(%v) = (%d, %v), want (%d, true)", now, got, ok, wantSession)
	}
}
