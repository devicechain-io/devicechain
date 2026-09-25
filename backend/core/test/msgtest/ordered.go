// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package msgtest

import (
	"context"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// InlineOrderedWriter adapts a MessageWriter to messaging.OrderedWriter by publishing
// synchronously and calling done inline, so a unit test can keep asserting on a
// MockMessageWriter.
//
// It is NOT a production writer: done runs on the submitting goroutine, which is equivalent
// to the real writer's ordering only because a unit test drives one submission at a time.
// Nothing it does exercises a window, a PubAck or the settle goroutine; tests of those build
// the real writer over an embedded broker.
type InlineOrderedWriter struct {
	W messaging.MessageWriter
}

// Publish writes msg through W and reports the result to done.
func (w InlineOrderedWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	done(w.W.WriteMessages(ctx, msg))
}

// Fail reports err to done. Like the real writer it refuses a nil error.
func (w InlineOrderedWriter) Fail(err error, done func(error)) {
	if err == nil {
		panic("msgtest: InlineOrderedWriter.Fail needs a non-nil error")
	}
	done(err)
}

// Draining does nothing: this writer never backs off.
func (InlineOrderedWriter) Draining() {}

// Close does nothing: every outcome was reported inline.
func (InlineOrderedWriter) Close() {}
