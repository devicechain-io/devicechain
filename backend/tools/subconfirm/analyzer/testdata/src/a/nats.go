// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package a

import (
	"github.com/nats-io/nats.go"
)

// Every subscribe on a connection is reported, including the two whose names read as
// if they had already waited for something.
func everyMethod(nc *nats.Conn, h nats.MsgHandler, ch chan *nats.Msg) {
	nc.Subscribe("x", h)                        // want "core NATS DROPS a publish"
	nc.SubscribeSync("x")                       // want "core NATS DROPS a publish"
	nc.QueueSubscribe("x", "q", h)              // want "core NATS DROPS a publish"
	nc.QueueSubscribeSync("x", "q")             // want "core NATS DROPS a publish"
	nc.QueueSubscribeSyncWithChan("x", "q", ch) // want "core NATS DROPS a publish"
	nc.ChanSubscribe("x", ch)                   // want "core NATS DROPS a publish"
	nc.ChanQueueSubscribe("x", "q", ch)         // want "core NATS DROPS a publish"
	nc.Publish("x", nil)                        // not a subscribe
	_, _ = nc.Subscribe("assigned", h)          // want "core NATS DROPS a publish"
}

// The receiver is never a bare `nc` in real code as often as it is in examples. None
// of these spellings is reachable by a search for a variable name.
type holder struct {
	conn *nats.Conn
}

func (hd *holder) throughAField(h nats.MsgHandler) {
	hd.conn.Subscribe("x", h) // want "core NATS DROPS a publish"
}

func dial() *nats.Conn { return nil }

func throughACall(h nats.MsgHandler) {
	dial().Subscribe("x", h) // want "core NATS DROPS a publish"
}

func inAnArgument(h nats.MsgHandler, nc *nats.Conn) {
	consume(nc.Subscribe("x", h)) // want "core NATS DROPS a publish"
}

func consume(sub *nats.Subscription, err error) {}

// JetStream creates its consumer with a request to the JetStream API, so the
// subscription is confirmed by the time it returns. Spelled identically; reported by
// nothing. This is the case a grep cannot get right in either direction.
func jetStreamIsFine(js nats.JetStreamContext, h nats.MsgHandler) {
	js.Subscribe("x", h)
	js.QueueSubscribe("x", "q", h)
}
