// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventlimit holds the platform's bound on what ONE event may carry, and the one way
// a producer cuts a larger batch down to it.
//
// It is its own package, importing nothing, so an ingest module takes the number without
// taking core's heavier dependencies (the core/writerbatch precedent). The counting of a
// payload's readings lives with the payload types (event-sources' model.ReadingCount), since
// core cannot import them; the number it is compared with lives only here.
package eventlimit

// MaxReadingsPerEvent is the most readings one event may carry, on every transport. It is a
// platform constant, not a setting.
//
// 🔴 THE UNIT IS THE READING, NOT THE ENTRY. A reading is one stored datum: a metric key of a
// measurement entry, or one location or alert entry. A measurement entry carries a map, and
// the map is unbounded, so one entry holding 30 000 keys is one entry and still 30 000 stored
// rows across 30 000 series, each its own evaluation on the detection goroutine every tenant
// shares. A limit counting entries would wave that through while claiming to bound it.
//
// What a producer does with a batch over it depends on who is asking:
//   - a device's own event (the JSON device event on HTTP and MQTT) is one message and one
//     event, so a message over the limit is REFUSED WHOLE, never truncated. Truncating would
//     answer a device 202 for data that was silently cut short, which nobody at either end
//     could detect; refusing tells the device the count, the limit and the remedy.
//   - a protocol gateway (Sparkplug, LwM2M) whose devices legitimately report more in one
//     message SPLITS it into consecutive events of at most this many (Split below), and
//     drops nothing.
//
// It bounds ONE EVENT — its stored rows, the parameters of the statement that inserts them,
// and the work one event costs downstream. It does not bound a message's total fan-out on
// the gateway paths, since a split message becomes several events; the ingest rate limits
// bound volume, and a rate limit cannot stand in for this bound, because it charges one
// token however much a message carries.
const MaxReadingsPerEvent = 256

// Split cuts s, in order, into consecutive pieces of at most MaxReadingsPerEvent elements.
//
// It is deterministic, and that is what makes a split batch idempotent under redelivery:
// the same input always yields the same pieces, and each piece's identity downstream is a
// digest of its own content. A slice at or under the limit is returned as its single piece,
// unchanged, so a batch that fits is published exactly as it was before the limit existed.
// An empty or nil slice yields no pieces.
//
// The pieces alias s and are capacity-limited (s[i:j:j]), so an append to one piece cannot
// overwrite the start of the next.
func Split[T any](s []T) [][]T {
	var out [][]T
	for i := 0; i < len(s); i += MaxReadingsPerEvent {
		j := min(i+MaxReadingsPerEvent, len(s))
		out = append(out, s[i:j:j])
	}
	return out
}
