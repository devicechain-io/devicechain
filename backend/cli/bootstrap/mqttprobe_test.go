// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

// The probe's packet is hand-rolled, so its encoding is asserted here rather than
// discovered from a broker that drops the connection without saying why. A wrong
// length prefix, a wrong flag bit or a wrong varint all produce the same symptom:
// nothing.

// TestRemainingLengthMatchesTheSpecBoundaries walks MQTT 3.1.1's documented
// continuation boundaries. They are the only interesting inputs — everything
// between two boundaries encodes the same way — and getting one wrong is
// invisible until a packet happens to cross it.
func TestRemainingLengthMatchesTheSpecBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7F}},
		{128, []byte{0x80, 0x01}},
		{16383, []byte{0xFF, 0x7F}},
		{16384, []byte{0x80, 0x80, 0x01}},
	} {
		if got := remainingLength(tc.n); !bytes.Equal(got, tc.want) {
			t.Errorf("remainingLength(%d) = % x, want % x", tc.n, got, tc.want)
		}
	}
}

func TestConnectPacketIsWellFormed(t *testing.T) {
	pkt := mqttConnectPacket("probe-id", "", "")

	if pkt[0] != 0x10 {
		t.Fatalf("packet type is 0x%02x, want 0x10 (CONNECT)", pkt[0])
	}
	// The declared remaining length must match what actually follows it, or the
	// broker reads the next packet from the middle of this one.
	body := pkt[2:]
	if int(pkt[1]) != len(body) {
		t.Fatalf("declared remaining length %d but %d byte(s) follow", pkt[1], len(body))
	}

	name, rest := readMqttString(t, body)
	if name != "MQTT" {
		t.Fatalf("protocol name %q, want \"MQTT\"", name)
	}
	if rest[0] != 0x04 {
		t.Fatalf("protocol level 0x%02x, want 0x04 (MQTT 3.1.1)", rest[0])
	}
	if rest[1] != 0x02 {
		t.Fatalf("connect flags 0x%02x, want 0x02 (clean session, no credentials)", rest[1])
	}
	if ka := binary.BigEndian.Uint16(rest[2:4]); ka == 0 {
		t.Fatal("keepalive is 0, which asks the broker never to time the session out — " +
			"a probe that dies mid-connection would then hold a session indefinitely")
	}
	id, tail := readMqttString(t, rest[4:])
	if id != "probe-id" {
		t.Fatalf("client id %q, want \"probe-id\"", id)
	}
	if len(tail) != 0 {
		t.Fatalf("%d trailing byte(s) after the client id: % x", len(tail), tail)
	}
}

func TestConnectPacketCarriesCredentialsWhenGiven(t *testing.T) {
	pkt := mqttConnectPacket("probe-id", "svc", "secret")
	body := pkt[2:]
	_, rest := readMqttString(t, body)
	if rest[1] != 0xC2 {
		t.Fatalf("connect flags 0x%02x, want 0xC2 (username + password + clean session)", rest[1])
	}
	_, after := readMqttString(t, rest[4:]) // client id
	user, afterUser := readMqttString(t, after)
	pass, tail := readMqttString(t, afterUser)
	if user != "svc" || pass != "secret" {
		t.Fatalf("credentials encoded as %q/%q", user, pass)
	}
	if len(tail) != 0 {
		t.Fatalf("%d trailing byte(s): % x", len(tail), tail)
	}
}

// TestConnectPacketOmitsThePasswordFlagWithoutAPassword pins the asymmetry MQTT
// requires: a password flag with no password in the payload makes the packet
// unparseable, and the broker's only available response is to drop it.
func TestConnectPacketOmitsThePasswordFlagWithoutAPassword(t *testing.T) {
	pkt := mqttConnectPacket("probe-id", "svc", "")
	body := pkt[2:]
	_, rest := readMqttString(t, body)
	if rest[1] != 0x82 {
		t.Fatalf("connect flags 0x%02x, want 0x82 (username + clean session, no password)", rest[1])
	}
}

// TestProbeClientIdIsAcceptableToNats pins a constraint that is not MQTT's.
//
// The NATS MQTT gateway maps a client id into a subject token and refuses one
// containing a ".". A probe that tripped that would fail on every instance, with
// a connect refused for a reason nothing in the MQTT spec would explain.
func TestProbeClientIdIsAcceptableToNats(t *testing.T) {
	for _, bad := range []string{".", "*", ">", " "} {
		if strings.Contains(mqttProbeClientId, bad) {
			t.Fatalf("probe client id %q contains %q, which the NATS MQTT gateway rejects",
				mqttProbeClientId, bad)
		}
	}
	if mqttProbeClientId == "" {
		t.Fatal("an empty client id asks the broker to assign one, which requires clean " +
			"session and makes the probe harder to identify in a session table")
	}
}

// TestConnackRefusalExplainsTheAuthCase covers the refusal an operator will
// actually hit: a broker with authentication enabled routes device connects
// through the auth callout, so an unauthenticated probe is turned away. The
// useful sentence is not "refused" but "the probe cannot pull this lever here".
func TestConnackRefusalExplainsTheAuthCase(t *testing.T) {
	for _, code := range []byte{4, 5} {
		err := readConnackFrom(t, []byte{0x20, 0x02, 0x00, code})
		if err == nil {
			t.Fatalf("return code %d must be an error", code)
		}
		if !strings.Contains(err.Error(), "authentication enabled") {
			t.Fatalf("return code %d should explain the auth case; got %q", code, err)
		}
	}
}

func TestConnackSuccessIsAccepted(t *testing.T) {
	if err := readConnackFrom(t, []byte{0x20, 0x02, 0x00, 0x00}); err != nil {
		t.Fatalf("a successful CONNACK must be accepted; got %v", err)
	}
}

// TestConnackRejectsAWrongPacketType stops the probe reading a broker's error
// packet as a successful connect.
func TestConnackRejectsAWrongPacketType(t *testing.T) {
	if err := readConnackFrom(t, []byte{0x30, 0x02, 0x00, 0x00}); err == nil {
		t.Fatal("a non-CONNACK packet must not be accepted as a successful connect")
	}
}

// readConnackFrom feeds bytes to readConnack over a real socket pair, so the
// io.ReadFull framing is exercised rather than bypassed.
func readConnackFrom(t *testing.T, data []byte) error {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		_, _ = server.Write(data)
		server.Close()
	}()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	return readConnack(client)
}

func readMqttString(t *testing.T, b []byte) (string, []byte) {
	t.Helper()
	if len(b) < 2 {
		t.Fatalf("truncated length prefix: % x", b)
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		t.Fatalf("length prefix says %d but only %d byte(s) remain", n, len(b)-2)
	}
	return string(b[2 : 2+n]), b[2+n:]
}
