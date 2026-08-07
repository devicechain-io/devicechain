// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// mqttSubackFailure is the SUBACK return code a broker sends to REFUSE a subscription
// (MQTT 3.1.1 §3.9.3). Any other value is a granted QoS, possibly downgraded from the
// one asked for — a downgrade is legal and not a failure.
//
// It is defined here because paho exports no constant for it, which is a large part of
// why the check below is so easy to omit.
const mqttSubackFailure byte = 0x80

// SubscribeMqttConfirmed subscribes and does not return until the broker has GRANTED
// the subscription.
//
// # 🔴 Waiting for the SUBACK is not the same as reading it
//
// paho's token tells you the broker answered. It does not tell you what the broker
// said. On SUBACK, paho copies the return codes into the token's Result() map and
// calls flowComplete() — it never calls setError. So a broker that REFUSES a
// subscription (0x80: an ACL that denies read on the filter, a filter the broker will
// not accept) produces a token that waits successfully and whose Error() is nil.
//
// Both obvious spellings are therefore wrong in the same way:
//
//	token := c.Subscribe(f, 1, h); token.Wait()             // ignores the answer entirely
//	err := mqtt.WaitTokenTimeout(c.Subscribe(f, 1, h), d)   // returns token.Error(), still nil
//
// paho's own WaitTokenTimeout has the blind spot, which is what makes this worth a
// shared helper rather than a note: the failure is a service that starts cleanly,
// reports healthy, logs that it subscribed, and then ingests nothing for the life of
// the process. There is no later signal — no retry, no error, no gap — because from
// the client's side nothing went wrong.
//
// The timeout is the other half. A bare Wait() is unbounded, so a broker that accepts
// the connection and then stops answering hangs startup forever rather than failing it.
func SubscribeMqttConfirmed(client mqtt.Client, filter string, qos byte, cb mqtt.MessageHandler, timeout time.Duration) error {
	token := client.Subscribe(filter, qos, cb)
	if !token.WaitTimeout(timeout) {
		return fmt.Errorf("the broker did not acknowledge the subscription to %q within %v", filter, timeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("subscribing to %q: %w", filter, err)
	}

	// The half paho will not do for us.
	sub, ok := token.(*mqtt.SubscribeToken)
	if !ok {
		// Unreachable with paho's own client; a fake in a test could do it. Refusing is
		// the safe read — the alternative is claiming a grant we never saw.
		return fmt.Errorf("subscribing to %q: the client returned a %T rather than a "+
			"SubscribeToken, so the broker's grant cannot be read", filter, token)
	}
	for granted, code := range sub.Result() {
		if code == mqttSubackFailure {
			return fmt.Errorf("the broker REFUSED the subscription to %q (SUBACK 0x80). "+
				"The connection is fine and paho reports no error, so nothing else will "+
				"report this: the credential is most likely not permitted to read that "+
				"topic", granted)
		}
	}
	return nil
}
