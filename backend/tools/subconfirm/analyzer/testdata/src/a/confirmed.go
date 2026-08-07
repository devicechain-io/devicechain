// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package a

import (
	"github.com/devicechain-io/dc-microservice/messaging"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"
)

// The wrappers are the ordinary answer and are not a call on the connection at all,
// so nothing here is reported.
func throughTheWrapper(nc *nats.Conn, h nats.MsgHandler) {
	messaging.SubscribeSynced(nc, "x", h)
}

// ChanSubscribe has no wrapper, so confirming it explicitly is the intended shape —
// and it is the shape the diagnostic recommends. Reporting it would make the pass
// contradict its own advice.
func confirmedExplicitly(nc *nats.Conn, ch chan *nats.Msg) error {
	nc.ChanSubscribe("x", ch)
	return messaging.ConfirmSubscribed(nc)
}

// One round trip confirms every subscription issued on the connection so far, which
// is why the confirmation does not have to sit next to any particular subscribe.
func severalConfirmedTogether(nc *nats.Conn, h nats.MsgHandler) error {
	nc.Subscribe("a", h)
	nc.Subscribe("b", h)
	nc.QueueSubscribe("c", "q", h)
	return messaging.ConfirmSubscribed(nc)
}

// A raw flush is the same thing with the wrapper's error message stripped off.
func flushedDirectly(nc *nats.Conn, h nats.MsgHandler) error {
	nc.Subscribe("a", h)
	return nc.FlushTimeout(5)
}

// 🔴 A flush BEFORE the subscribe confirms the subscriptions that preceded it and
// says nothing about this one. Ordering is the whole subject of this pass, so a
// check that ignored it would be measuring the wrong thing entirely.
func flushedTooEarly(nc *nats.Conn, h nats.MsgHandler) {
	nc.FlushTimeout(5)
	nc.Subscribe("a", h) // want "core NATS DROPS a publish"
}

// A flush in the enclosing function does not run when the closure does.
func flushOutsideTheClosure(nc *nats.Conn, h nats.MsgHandler, run func(func())) error {
	run(func() {
		nc.Subscribe("late", h) // want "core NATS DROPS a publish"
	})
	return messaging.ConfirmSubscribed(nc)
}

// MQTT has no after-the-fact confirmation: the SUBACK arrives with the subscribe and
// is gone if nobody reads it. A flush of any kind must not silence it.
func mqttIsNotFlushable(nc *nats.Conn, c mqtt.Client, h mqtt.MessageHandler) error {
	c.Subscribe("t", 1, h) // want "paho never reports a REFUSAL"
	return messaging.ConfirmSubscribed(nc)
}
