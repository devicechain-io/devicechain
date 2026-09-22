// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
)

// readAndHandleOne reads one inbound event and hands it on, reporting whether the read loop
// would have STOPPED — the per-message seam ProcessMessage used to provide before the loop
// itself moved into messaging.RunConsumer.
//
// 🔑 IT LIVES IN THE TESTS RATHER THAN IN THE PROCESSOR ON PURPOSE. A processor method that
// reads one message and handles it would be a second path production never runs, and the
// tests using it would then pin a shape nothing ships. The division is: the pacing tests
// drive the REAL loop (readLoop) and cover the wiring, while the tests using this care only
// about what one message does, and say so by reading exactly one.
//
// Note the polarity flip. ProcessMessage reported "stop"; handOff reports "carry on".
func readAndHandleOne(p *InboundEventsProcessor, ctx context.Context) bool {
	msg, err := p.InboundEventsReader.ReadMessage(ctx)
	if err != nil {
		return true
	}
	return !p.handOff(ctx, msg)
}
