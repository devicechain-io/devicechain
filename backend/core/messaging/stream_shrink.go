// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

// shrinkRefusal says why a reconcile must not lower a per-event stream's ceiling to the
// bounds it was asked for, or "" when it may.
//
// 🔴 LOWERING A CEILING BELOW WHAT THE STREAM HOLDS EVICTS THE OVERFLOW AT ONCE. UpdateStream
// applies DiscardOld immediately, oldest first, whether or not a consumer has read it, and the
// backpressure gate cannot help: it acts on publish, and this is not one. On the streams sized
// by time (streams.Stream.RunwayBytesPerMessage) the oldest messages are the replay history
// detection reads back from, and on the capture stream they are MQTT events the broker has
// already acknowledged to the device and event-sources has not forwarded. A configuration that
// shortens the runway (a lower design rate or window, a smaller byte floor) would therefore
// drop unread events during an upgrade hook that reports success.
//
// So the administrator refuses, naming the stream and the shortfall, and the override is
// explicit: dc-broker-admin --allow-ceiling-shrink (the chart's brokerAdmin.allowCeilingShrink,
// or `dcctl broker reconcile --allow-ceiling-shrink`). Raising a ceiling, or lowering it to
// something the stream still fits under, is never refused. Streams not sized by time keep the
// old behaviour: they are bounded history, not a runway.
func shrinkRefusal(suffix, name string, have nats.StreamConfig, want streamBounds, state nats.StreamState) string {
	if streams.RunwayBytesPerMessageFor(suffix) <= 0 {
		return ""
	}
	var parts []string
	if want.maxBytes > 0 && have.MaxBytes != want.maxBytes && int64(state.Bytes) > want.maxBytes {
		parts = append(parts, fmt.Sprintf("its byte ceiling would go from %d to %d while it holds %d bytes (%d MiB over)",
			have.MaxBytes, want.maxBytes, state.Bytes, (int64(state.Bytes)-want.maxBytes)>>20))
	}
	if want.maxMsgs > 0 && have.MaxMsgs != want.maxMsgs && int64(state.Msgs) > want.maxMsgs {
		parts = append(parts, fmt.Sprintf("its message ceiling would go from %d to %d while it holds %d messages (%d over)",
			have.MaxMsgs, want.maxMsgs, state.Msgs, int64(state.Msgs)-want.maxMsgs))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("stream %s: %s. Applying that would discard its oldest messages at once, read or not, "+
		"including replay history and events devices were told were accepted. Nothing has been changed on it. "+
		"Keep the ceiling (raise streamDesignRate or streamRunwayMinutes back), wait for the stream to hold "+
		"less, or accept the loss deliberately with --allow-ceiling-shrink (the chart's "+
		"brokerAdmin.allowCeilingShrink, or `dcctl broker reconcile <instance> --allow-ceiling-shrink`)",
		name, joinSentences(parts))
}

func joinSentences(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += "; and " + p
	}
	return out
}
