// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventtime

import (
	"math"
	"time"
)

// The event messages (the inbound event an ingest source publishes and the resolved event
// device-management publishes) carry each instant as a count of nanoseconds since the Unix
// epoch: eight fixed bytes, against the thirty-odd of the RFC 3339 string they carried
// before, with nothing lost, because nanoseconds are what a time.Time holds.
//
// A count of nanoseconds in an int64 holds only the years 1677 to 2262. An event can carry
// an instant outside that: a device clock reports whatever it likes, and only resolution
// bounds it, and only when the skew bound is configured. So the count is the USUAL form and
// not the only one: an instant it cannot hold travels as an RFC 3339 string, the form every
// message carried before. WireNanos says which form an instant takes.
//
// What the count does not carry is a time zone. Every instant reads back in UTC, which
// is the same instant: nothing downstream stores or compares the device's offset, and the
// event store keeps a timestamptz.

var (
	minWireTime = time.Unix(0, math.MinInt64)
	maxWireTime = time.Unix(0, math.MaxInt64)
)

// WireNanos returns t as nanoseconds since the Unix epoch, or false when t is outside the
// range that count can hold, in which case the caller writes the string form instead.
func WireNanos(t time.Time) (int64, bool) {
	if t.Before(minWireTime) || t.After(maxWireTime) {
		return 0, false
	}
	return t.UnixNano(), true
}

// FromWireNanos is the instant a count of nanoseconds since the Unix epoch names, in UTC.
func FromWireNanos(nanos int64) time.Time {
	return time.Unix(0, nanos).UTC()
}

// WireString is an instant's string form: RFC 3339 at full precision, in UTC.
func WireString(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
