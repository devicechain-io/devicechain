// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventtime

import (
	"errors"
	"fmt"
	"math"
	"sync/atomic"
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

// WireString is an instant's string form: RFC 3339 at full precision, in UTC when RFC 3339
// can carry it there (AppendWireString).
func WireString(t time.Time) string {
	return string(AppendWireString(nil, t))
}

// 🔴 RFC 3339 CARRIES ONLY THE YEARS 0000 TO 9999, and time.Parse refuses anything else, so a
// string form outside them is not a string form at all: every consumer refuses the message.
// The years are counted IN THE OFFSET THE STRING IS WRITTEN IN, which is why UTC is not
// always available. A device can send 9999-12-31T23:59:59-05:00, a valid RFC 3339 instant
// whose UTC year is 10000. Written in UTC it is unparseable; written in its own offset, as
// the envelope always was before the count existed, it is fine.
//
// So the string form prefers UTC and falls back to the instant's own offset, and a reader
// (ParseWireString) normalizes to UTC only what UTC can carry, so the offset that made such
// an instant writable survives to the next producer that writes it.

// rfc3339Year reports whether t's year, in its own location, is one RFC 3339 can carry.
func rfc3339Year(t time.Time) bool {
	y := t.Year()
	return y >= 0 && y <= 9999
}

// AppendWireString appends t's string form: in UTC when RFC 3339 can carry it there, and
// otherwise in t's own offset.
func AppendWireString(b []byte, t time.Time) []byte {
	if u := t.UTC(); rfc3339Year(u) || !rfc3339Year(t) {
		return u.AppendFormat(b, time.RFC3339Nano)
	}
	return t.AppendFormat(b, time.RFC3339Nano)
}

// AppendOwnOffsetWireString appends t's string form in t's own offset, as the release before
// the count wrote an envelope instant (eventtime.WireLegacy writes it so, byte for byte), and
// falls back to AppendWireString only when that offset cannot carry it.
func AppendOwnOffsetWireString(b []byte, t time.Time) []byte {
	if rfc3339Year(t) {
		return t.AppendFormat(b, time.RFC3339Nano)
	}
	return AppendWireString(b, t)
}

// ParseWireString reads a string form: RFC 3339 at any precision, in UTC when UTC can carry
// the instant, and otherwise in the offset it was written in (see AppendWireString).
func ParseWireString(text string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, err
	}
	if u := parsed.UTC(); rfc3339Year(u) {
		return u, nil
	}
	return parsed, nil
}

// WireFormat is which form a PRODUCER writes the event messages in. Every reader in this
// release takes both; what is chosen here is only what gets written.
//
// 🔴 IT EXISTS FOR THE ROLLING UPGRADE, and the order of the two steps is the whole point.
// A pod still on the release before this one reads only the legacy form: it dead-letters
// (device-management) or drops (event-management, device-state, event-processing) a
// compact message as unparseable. So a release cannot both introduce the compact reader
// and write the compact form: during its own rollout, an upgraded producer would be
// feeding consumers that have not been upgraded yet, and every such event would be lost.
//
//   - The release that introduced the compact reader read both forms and wrote WireLegacy
//     by default, byte for byte what the release before it wrote, so upgrading to it lost
//     nothing, whichever pods rolled first. WireCompact was opt-in there.
//   - THIS release writes WireCompact by default (DefaultWireFormat). Its consumers still
//     read both, and so did the release before it, so its own rollout is safe: an
//     upgraded producer feeds consumers that already read compact. Messages written in
//     the legacy form before the upgrade stay on the retained streams and still decode
//     (every reader, the replay preview included). WireLegacy stays selectable
//     (infrastructure.messaging.wireFormat: legacy) as the way back.
//   - 🔴 An upgrade must therefore come FROM a release that reads both. One that skips
//     the release that introduced the compact reader (from a release that reads only
//     legacy) has to set wireFormat: legacy for the upgrade, and compact only once every
//     pod has rolled.
//   - Rolling BACK to a release that reads only legacy needs WireLegacy first, and needs
//     the retained streams to have no compact message the old release would still read.
type WireFormat uint8

const (
	// WireLegacy writes every instant as an RFC 3339 string and every seal in the text
	// form, in the fields every release before this one wrote and read.
	WireLegacy WireFormat = iota + 1
	// WireCompact writes every instant the count can hold as nanoseconds (WireNanos) and
	// every seal in the binary form, in the fields only this release on reads.
	WireCompact
)

// DefaultWireFormat is what a producer writes when the instance document does not set
// infrastructure.messaging.wireFormat. It is the ONE definition of the default: the
// instance configuration's ApplyDefaults and the process setting below both read it.
//
// It is WireCompact from the release AFTER the one that introduced the compact reader,
// never in that release (see WireFormat).
const DefaultWireFormat = WireCompact

// ErrUnknownWireFormat is the refusal of a format that is neither WireLegacy nor
// WireCompact, the zero value included: a producer handed one writes nothing rather
// than guess which form its consumers read.
var ErrUnknownWireFormat = errors.New("eventtime: unknown event wire format")

// wireFormatNames is the single definition of the configurable spellings.
var wireFormatNames = map[string]WireFormat{"legacy": WireLegacy, "compact": WireCompact}

// ParseWireFormat reads a configured format: "legacy" or "compact", exactly as written.
// Anything else, an empty string included, is refused.
func ParseWireFormat(name string) (WireFormat, error) {
	if f, ok := wireFormatNames[name]; ok {
		return f, nil
	}
	return 0, fmt.Errorf("%w: %q; it must be legacy or compact (lowercase, exactly as written)", ErrUnknownWireFormat, name)
}

// Valid reports whether f is one of the formats a producer can write.
func (f WireFormat) Valid() bool { return f == WireLegacy || f == WireCompact }

// String is the configured spelling of f.
func (f WireFormat) String() string {
	switch f {
	case WireLegacy:
		return "legacy"
	case WireCompact:
		return "compact"
	}
	return fmt.Sprintf("WireFormat(%d)", uint8(f))
}

// producerFormat is the format THIS process writes: one setting for every producer in the
// process, so no producer can be wired to a different one. It is applied once, by the
// microservice's own load of the instance document (core.Microservice), the same way the
// log level is; until then, and in a process that never loads one, it is
// DefaultWireFormat.
var producerFormat atomic.Uint32

func init() { producerFormat.Store(uint32(DefaultWireFormat)) }

// SetProducerWireFormat sets the format this process writes. An unknown format is refused
// and leaves the setting as it was.
func SetProducerWireFormat(f WireFormat) error {
	if !f.Valid() {
		return fmt.Errorf("%w: %s", ErrUnknownWireFormat, f)
	}
	producerFormat.Store(uint32(f))
	return nil
}

// ProducerWireFormat is the format this process writes.
func ProducerWireFormat() WireFormat { return WireFormat(producerFormat.Load()) }
