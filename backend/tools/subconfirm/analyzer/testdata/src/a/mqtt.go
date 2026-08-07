// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package a

import (
	"decoy"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func mqttSubscribes(c mqtt.Client, h mqtt.MessageHandler) {
	c.Subscribe("t", 1, h)                          // want "paho never reports a REFUSAL"
	c.SubscribeMultiple(map[string]byte{"t": 1}, h) // want "paho never reports a REFUSAL"
	c.Publish("t", 1, false, nil)                   // not a subscribe
}

// paho's own recommended spelling — wait for the token, return its error — is the
// one that is wrong, so it must still be reported. The wait is real; the answer is
// never read.
func waitedForButNotRead(c mqtt.Client, h mqtt.MessageHandler) error {
	token := c.Subscribe("t", 1, h) // want "paho never reports a REFUSAL"
	token.Wait()
	return token.Error()
}

// Two types spelled exactly like the ones above, declared somewhere else. A check
// that matched on the receiver's name rather than its declaring package would report
// both of these, and the rest of this suite would still be green.
func lookalikesAreNotReported(c *decoy.Conn, m *decoy.Client) {
	c.Subscribe("query")
	m.Subscribe("t", 1)
}
