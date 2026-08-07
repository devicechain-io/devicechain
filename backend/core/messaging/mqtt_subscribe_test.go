// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging_test

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// A broker that answers SUBSCRIBE with a return code of the test's choosing.
//
// # Why a hand-rolled broker rather than a fake mqtt.Client
//
// The behaviour under test is paho's: on SUBACK it copies the broker's return codes
// into the token and completes it WITHOUT setting an error. A fake client cannot
// reproduce that, because SubscribeToken's result map is unexported — anything a test
// could construct would be asserting on its own fixture rather than on paho.
//
// So the refusal has to arrive over the wire, from something paho believes is a
// broker. It speaks just enough MQTT 3.1.1 to do that, using paho's own packet codec.
func startFakeBroker(t *testing.T, subackCode byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				for {
					pkt, err := packets.ReadPacket(conn)
					if err != nil {
						return
					}
					switch p := pkt.(type) {
					case *packets.ConnectPacket:
						ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
						ack.ReturnCode = packets.Accepted
						if err := ack.Write(conn); err != nil {
							return
						}
					case *packets.SubscribePacket:
						ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
						ack.MessageID = p.MessageID
						// One code per requested filter, all the same — enough to
						// decide grant vs refuse, which is the whole question.
						for range p.Topics {
							ack.ReturnCodes = append(ack.ReturnCodes, subackCode)
						}
						if err := ack.Write(conn); err != nil {
							return
						}
					case *packets.PingreqPacket:
						resp := packets.NewControlPacket(packets.Pingresp)
						if err := resp.Write(conn); err != nil {
							return
						}
					case *packets.DisconnectPacket:
						return
					}
				}
			}()
		}
	}()
	return "tcp://" + ln.Addr().String()
}

func connectToFakeBroker(t *testing.T, url string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(url)
	opts.SetClientID("suback-test")
	opts.SetConnectRetry(false)
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		t.Fatal("connect did not settle")
	}
	if err := tok.Error(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

// 🔴 THE FINDING THIS PINS. A broker refusing a subscription (0x80) leaves paho's
// token successful and its Error() nil, so both obvious spellings — token.Wait() and
// mqtt.WaitTokenTimeout — report success. A service built on either starts cleanly,
// logs that it subscribed, and ingests nothing forever, with no later signal because
// from the client's side nothing went wrong.
func TestSubscribeMqttConfirmedCatchesARefusal(t *testing.T) {
	const refused = 0x80
	url := startFakeBroker(t, refused)
	c := connectToFakeBroker(t, url)

	// First: establish that this IS invisible to what the code used to do. Without
	// this, the assertion below could be satisfied by a helper that rejects
	// everything, and the finding would be unproven.
	tok := c.Subscribe("denied/topic", 1, func(mqtt.Client, mqtt.Message) {})
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("the fake broker did not answer the control SUBSCRIBE")
	}
	if err := tok.Error(); err != nil {
		t.Fatalf("PREMISE FAILED: paho reported an error (%v) for a refused subscription. "+
			"If paho now surfaces 0x80 through Error(), SubscribeMqttConfirmed's extra "+
			"check is redundant and its comment is wrong.", err)
	}
	if err := mqtt.WaitTokenTimeout(tok, time.Second); err != nil {
		t.Fatalf("PREMISE FAILED: WaitTokenTimeout surfaced the refusal (%v)", err)
	}

	// Now the helper, on the same refusing broker.
	err := messaging.SubscribeMqttConfirmed(c, "denied/topic", 1,
		func(mqtt.Client, mqtt.Message) {}, 5*time.Second)
	if err == nil {
		t.Fatal("SubscribeMqttConfirmed reported success for a subscription the broker " +
			"REFUSED with 0x80 — the exact silent-no-ingest failure it exists to catch.")
	}
	if !strings.Contains(err.Error(), "denied/topic") {
		t.Errorf("the error does not name the refused filter, which is the one thing an "+
			"operator needs from it: %v", err)
	}
}

// The counterweight. Rejecting a refusal is only useful while a GRANT still passes —
// without this, a helper that always returned an error would satisfy the test above
// perfectly. Both granted QoS values are covered, including the DOWNGRADE (asking for
// 1 and being granted 0), which is legal and must not read as a failure.
func TestSubscribeMqttConfirmedAcceptsAGrant(t *testing.T) {
	for _, granted := range []byte{0, 1} {
		t.Run(fmt.Sprintf("granted-qos-%d", granted), func(t *testing.T) {
			url := startFakeBroker(t, granted)
			c := connectToFakeBroker(t, url)
			if err := messaging.SubscribeMqttConfirmed(c, "allowed/topic", 1,
				func(mqtt.Client, mqtt.Message) {}, 5*time.Second); err != nil {
				t.Fatalf("a subscription the broker GRANTED at QoS %d was reported as failed "+
					"(%v). A QoS downgrade is legal and is not a refusal.", granted, err)
			}
		})
	}
}

// The timeout half: a broker that accepts the connection and then never answers the
// SUBSCRIBE must fail startup rather than hang it. The fake broker answers CONNECT and
// PINGREQ but is given no SUBACK code to send, so it simply stays silent.
func TestSubscribeMqttConfirmedBoundsASilentBroker(t *testing.T) {
	url := startSilentSubackBroker(t)
	c := connectToFakeBroker(t, url)

	started := time.Now()
	err := messaging.SubscribeMqttConfirmed(c, "quiet/topic", 1,
		func(mqtt.Client, mqtt.Message) {}, 300*time.Millisecond)
	if err == nil {
		t.Fatal("a broker that never sent a SUBACK was reported as a successful subscription")
	}
	if cost := time.Since(started); cost > 5*time.Second {
		t.Fatalf("the wait took %v: the timeout is not bounding anything", cost)
	}
}

// startSilentSubackBroker is startFakeBroker minus the SUBACK.
func startSilentSubackBroker(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				for {
					pkt, err := packets.ReadPacket(conn)
					if err != nil {
						return
					}
					if _, ok := pkt.(*packets.ConnectPacket); ok {
						ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
						ack.ReturnCode = packets.Accepted
						if err := ack.Write(conn); err != nil {
							return
						}
					}
					// Everything else, SUBSCRIBE included, is deliberately ignored.
				}
			}()
		}
	}()
	return "tcp://" + ln.Addr().String()
}
