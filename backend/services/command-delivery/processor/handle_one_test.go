// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
)

// readAndHandleOne reads one message and handles it, reporting whether the read loop would
// have STOPPED — which is the per-message seam ProcessMessage used to provide before the
// loop itself moved into messaging.RunConsumer.
//
// 🔑 IT LIVES IN THE TESTS RATHER THAN IN THE PROCESSOR ON PURPOSE. A processor method that
// reads one message and handles it would be a second path production never runs, and the
// tests using it would then pin a shape nothing ships. The division is: the pacing tests
// drive the REAL loop (readLoop) and cover the wiring, while the tests below care only about
// what one response does, and say so by reading exactly one.
//
// Note the polarity flip. ProcessMessage reported "stop"; a messaging.ConsumerHandler
// reports "carry on". A read error is a stop here because that is what the loop does with
// the errors these tests can produce — the reader is a one-message fake, so the error is
// always its EOF.
func readAndHandleOne(p *CommandDeliveryProcessor, ctx context.Context) bool {
	msg, err := p.CommandResponsesReader.ReadMessage(ctx)
	if err != nil {
		return true
	}
	return !p.handleResponse(ctx, msg)
}
