// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package a

import (
	"context"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
)

// All three flush spellings confirm, so a change that recognised only the one the
// repo happens to use today would pass every other case here.
func flushByAnyName(nc *nats.Conn, h nats.MsgHandler, ctx context.Context) error {
	nc.Subscribe("a", h)
	if err := nc.Flush(); err != nil {
		return err
	}
	nc.Subscribe("b", h)
	if err := nc.FlushWithContext(ctx); err != nil {
		return err
	}
	nc.Subscribe("c", h)
	return nc.FlushTimeout(5)
}

// 🔴 A flush on a DIFFERENT connection confirms that connection's subscriptions and
// says nothing about this one. A service holding an ingest connection and a
// control-plane one is the ordinary case, not a contrived one.
func flushOnTheWrongConnection(ingest, control *nats.Conn, h nats.MsgHandler) error {
	ingest.Subscribe("x", h) // want "core NATS DROPS a publish"
	return control.Flush()
}

// A connection reached through a field is the same connection when the field path is
// the same, and a different one when it is not.
type twoConns struct {
	ingest  *nats.Conn
	control *nats.Conn
}

func (t *twoConns) throughMatchingFields(h nats.MsgHandler) error {
	t.ingest.Subscribe("x", h)
	return messaging.ConfirmSubscribed(t.ingest)
}

func (t *twoConns) throughMismatchedFields(h nats.MsgHandler) error {
	t.ingest.Subscribe("x", h) // want "core NATS DROPS a publish"
	return messaging.ConfirmSubscribed(t.control)
}

// 🔴 The circular one. The flush is inside the handler of the very subscription it
// would be excusing, so it can only run if the subscription was already confirmed.
func flushInsideItsOwnHandler(nc *nats.Conn) {
	nc.Subscribe("x", func(*nats.Msg) { // want "core NATS DROPS a publish"
		_ = nc.Flush()
	})
}

// A flush in a closure that is returned rather than called confirms nothing here.
func flushInAReturnedClosure(nc *nats.Conn, h nats.MsgHandler) func() error {
	nc.Subscribe("x", h) // want "core NATS DROPS a publish"
	return func() error { return nc.Flush() }
}

// EncodedConn is deprecated and unused here, so the rule covering it exists only so
// that reaching for it does not quietly bypass the check.
func encodedConnIsNotAWayOut(ec *nats.EncodedConn) {
	ec.Subscribe("x", nil)           // want "same asynchronous subscribe"
	ec.QueueSubscribe("x", "q", nil) // want "same asynchronous subscribe"
}

// One trailing directive must not silence a second subscribe that happens to share
// the statement — nor pre-exempt a third somebody adds to it later.
func twoSubscribesInOneStatement(nc *nats.Conn, h nats.MsgHandler) {
	consume2(
		one(nc.Subscribe("a", h)), //subconfirm:ok written for this one only
		one(nc.Subscribe("b", h)), // want "core NATS DROPS a publish"
	)
}

func one(sub *nats.Subscription, err error) *nats.Subscription { return sub }

func consume2(a, b *nats.Subscription) {}
