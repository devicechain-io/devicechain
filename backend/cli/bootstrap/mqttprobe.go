// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// mqttProbe opens ONE MQTT 3.1.1 connection to the broker and closes it.
//
// # WHY A CHECKER NEEDS THIS AT ALL
//
// The four $MQTT_* streams are the A0 lever the platform does not pull: their
// replica factor is chosen by nats-server itself, in mqttDetermineReplicas, at
// the first MQTT connect after a broker start — once, never revisited. Until some
// client connects, the streams do not exist, and a check that treated their
// absence as a pass would be reporting success over the one replication decision
// it cannot see any other way.
//
// So on a validation rig, where no device has ever connected, the check has to
// cause the lever to be pulled before reading it. That is sound: the answer
// nats-server gives this connection is the same answer it gives the first real
// device on this broker generation.
//
// It is OFF BY DEFAULT, and that matters. A verifier that creates four streams as
// a side effect of being run is not read-only, and read-only is what makes a
// check safe to run against a production instance at any time. The default is to
// report the streams missing and say what that means.
//
// Hand-rolled rather than reached for from a client library. CONNECT/CONNACK is a
// few dozen bytes, dcctl is a CLI whose dependency surface is worth defending,
// and the alternative — depending on an MQTT stack to send one packet — is the
// kind of weight that never comes back off.
func mqttProbe(addr string, tlsCfg *tls.Config, user, password string) error {
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if tlsCfg != nil {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dialing the MQTT listener at %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := conn.Write(mqttConnectPacket(mqttProbeClientId, user, password)); err != nil {
		return fmt.Errorf("sending MQTT CONNECT: %w", err)
	}
	if err := readConnack(conn); err != nil {
		return err
	}
	// DISCONNECT: a clean close, so the broker tears the session down rather than
	// holding it until keepalive expires. The streams it created on the way in
	// stay, which is the point.
	_, _ = conn.Write([]byte{0xE0, 0x00})
	return nil
}

// mqttProbeClientId names the probe in the broker's session table.
//
// No dots. NATS's MQTT gateway maps a client id into a subject token and rejects
// one containing "." outright — a rule that has cost time before, so the constant
// carries the reason rather than the reader rediscovering it from a connect that
// fails for no visible cause.
const mqttProbeClientId = "dcctl-ha-probe"

// mqttConnectPacket builds an MQTT 3.1.1 CONNECT.
//
// Split out from the dial so the encoding can be asserted without a broker. It is
// the part that is easy to get subtly wrong — a remaining-length varint, a flag
// bit, a length prefix — and wrong in a way whose only symptom is a connection
// the broker drops without explanation.
func mqttConnectPacket(clientId, user, password string) []byte {
	var flags byte = 0x02 // clean session
	if user != "" {
		flags |= 0x80 // username present
		if password != "" {
			flags |= 0x40 // password present
		}
	}
	var variable []byte
	variable = append(variable, mqttString("MQTT")...)
	variable = append(variable, 0x04) // protocol level 4 == MQTT 3.1.1
	variable = append(variable, flags)
	keepalive := make([]byte, 2)
	binary.BigEndian.PutUint16(keepalive, 30)
	variable = append(variable, keepalive...)

	var payload []byte
	payload = append(payload, mqttString(clientId)...)
	if user != "" {
		payload = append(payload, mqttString(user)...)
		if password != "" {
			payload = append(payload, mqttString(password)...)
		}
	}

	body := append(variable, payload...)
	pkt := []byte{0x10} // CONNECT
	pkt = append(pkt, remainingLength(len(body))...)
	return append(pkt, body...)
}

// mqttString is MQTT's length-prefixed UTF-8: a two-byte big-endian length
// followed by the bytes.
func mqttString(s string) []byte {
	out := make([]byte, 2, 2+len(s))
	binary.BigEndian.PutUint16(out, uint16(len(s)))
	return append(out, s...)
}

// remainingLength encodes MQTT's variable-length integer: seven bits per byte,
// the high bit marking continuation.
func remainingLength(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// readConnack reads the broker's answer and turns a refusal into a sentence.
//
// The refusal codes matter here more than they usually would. On a broker with
// authentication enabled (ADR-025) device connects go through the auth callout,
// so an unauthenticated probe is refused — and the useful thing to say is not
// "connection refused" but that the probe cannot pull this lever on this broker
// and a real device has to.
func readConnack(conn net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("reading MQTT CONNACK: %w", err)
	}
	if head[0] != 0x20 {
		return fmt.Errorf("expected an MQTT CONNACK (0x20); got packet type 0x%02x", head[0])
	}
	body := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return fmt.Errorf("reading MQTT CONNACK body: %w", err)
	}
	if len(body) < 2 {
		return fmt.Errorf("MQTT CONNACK was %d byte(s); expected 2", len(body))
	}
	switch code := body[1]; code {
	case 0:
		return nil
	case 4, 5:
		return fmt.Errorf("the broker refused the MQTT probe (return code %d: bad "+
			"credentials / not authorized). This broker has authentication enabled, so "+
			"the $MQTT_* streams can only be created by a real device connect — the "+
			"probe cannot pull that lever here. Connect a device, then re-run without "+
			"--probe-mqtt", code)
	default:
		return fmt.Errorf("the broker refused the MQTT probe with return code %d", code)
	}
}
