// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package a

import (
	broker "github.com/nats-io/nats.go"
)

// An import ALIAS changes every spelling at the call site and nothing about what the
// type is. Reported.
func underAnAlias(nc *broker.Conn, h broker.MsgHandler) {
	nc.Subscribe("x", h) // want "core NATS DROPS a publish"
}

// The suppression that recurs in this repo: subscribe, then publish the request the
// subscription is waiting for, on the SAME connection. Both writes land in one
// buffer under one lock, so the server reads SUB before PUB and a flush would buy
// nothing but a round trip.
func sameConnection(nc *broker.Conn, h broker.MsgHandler) error {
	// The directive sits at the END of the explanation, which is where a conclusion
	// belongs — house comments here run to paragraphs, and a directive forced to
	// lead one would read as a header.
	//
	//subconfirm:ok the publish below is on this same connection, so the SUB is ordered ahead of it
	nc.Subscribe("reply", h)
	return nc.Publish("request", nil)
}

func trailingForm(nc *broker.Conn, h broker.MsgHandler) {
	nc.Subscribe("reply", h) //subconfirm:ok trailing placement, same reason as above
}

// A reason longer than one line runs on UNDERNEATH the directive, because that is
// how anyone actually writes one. The comment BLOCK is what anchors to the statement,
// not the directive's own line — an earlier version of this pass matched the line,
// and every multi-line reason in the repo silently stopped suppressing.
func reasonRunsOnToTheNextLine(nc *broker.Conn, h broker.MsgHandler) error {
	//subconfirm:ok the publish below is on this same connection, so the server reads
	// SUB before PUB and a flush would buy a round trip and nothing else
	nc.Subscribe("reply", h)
	return nc.Publish("request", nil)
}

// A directive suppresses ONE STATEMENT, not a function — and that boundary is worth
// a test of its own, because the tempting generalization is the dangerous one. A
// directive that covered a whole function would silently extend to the next
// subscribe somebody adds to it, which is the exact shape of "a guard quietly stops
// guarding" this analyzer exists to prevent. So a directive that has drifted away
// from its statement fails LOUDLY, twice: the subscribe is reported, and the
// directive is reported for suppressing nothing.
//
// want +2 "suppresses nothing here"
//
//subconfirm:ok this reads as if it covers the whole function; it does not
func driftedAwayFromItsStatement(nc *broker.Conn, h broker.MsgHandler) {
	// Any comment at all between the directive and the statement is enough.
	nc.Subscribe("reply", h) // want "core NATS DROPS a publish"
}

// A reason is the whole point. Without one the next reader cannot tell a deliberate
// exemption from a warning somebody silenced to get a build green.
func noReason(nc *broker.Conn, h broker.MsgHandler) {
	// want +1 "needs a reason"
	//subconfirm:ok
	nc.Subscribe("reply", h)
}

// A suppression that outlives the call it covered leaves an exemption lying around
// for a subscribe nobody has written yet.
func staleDirective(nc *broker.Conn) error {
	// want +1 "suppresses nothing here"
	//subconfirm:ok this used to cover a subscribe that is now behind the wrapper
	return nc.Publish("request", nil)
}
