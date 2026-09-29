// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package decode turns an LwM2M Notify/Send payload and a registration's object list into
// the protocol-neutral shapes the shared ingest adapter consumes: measurement Samples and
// the set of object-instance paths worth observing. It is deliberately transport-free — it
// takes bytes and returns values, so the SenML/IPSO semantics and the CoRE-Link selection
// are golden-testable without a live CoAP/DTLS server, and the future Send /dp handler
// reuses the identical entry points.
package decode

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/plgd-dev/go-coap/v3/message"
)

// ErrUnsupportedContentFormat is returned by Samples for a content format this slice does
// not decode (everything but SenML-JSON in L2 — TLV/CBOR are follow-ups). The caller counts
// it and drops the payload rather than mis-parsing bytes in another format.
var ErrUnsupportedContentFormat = errors.New("lwm2m: unsupported content format")

// ErrUnsupportedSenmlVersion is returned when a pack carries a base version (bver) other
// than the SenML 1.0 we understand. Dropping the whole pack is safer than decoding records
// whose semantics a future version may have changed.
var ErrUnsupportedSenmlVersion = errors.New("lwm2m: unsupported SenML version")

// senmlVersion is the only SenML base version (bver) we decode. RFC 8428 §4.4: bver
// defaults to 10 and SHOULD be omitted when it equals the default, so an absent bver is 10.
const senmlVersion = 10

// absoluteTimeThreshold is RFC 8428 §4.5.3's boundary: a RESOLVED time (bt+t) at or above
// 2^28 is an absolute time in seconds since the Unix epoch; below it is relative to "now".
// We stamp receipt time for the relative case rather than doing now-relative arithmetic —
// matching the Sparkplug decoder's fallback discipline (never persist a device-relative or
// 1970 timestamp).
const absoluteTimeThreshold = 1 << 28

// maxAbsoluteTimeSeconds is an upper sanity bound on a resolved absolute time. bt is
// device-controlled and unbounded, and a huge (or non-finite) bt would overflow the
// seconds→milliseconds int64 conversion — which in Go yields math.MinInt64, a garbage
// timestamp worse than 1970 that the emitter would serialize verbatim. 2^40 seconds is the
// year ~36812: far beyond any legitimate device clock, far below the int64-ms overflow point.
// A resolved time at or above it is treated as a broken clock and stamped as receipt time.
const maxAbsoluteTimeSeconds = 1 << 40

// Skips accounts for every record a decode read and did not turn into a sample. It exists
// because the alternative is a counter that only ever reports the total.
//
// 🔴 THREE VERY DIFFERENT DEVICE FAULTS PRODUCED THE SAME OBSERVATION: an empty sample
// slice and no error. "This device reports only booleans", "this device's firmware is
// sending garbage" and "this device is sending nothing at all" were indistinguishable from
// the metrics — the decode incremented nothing on any of the skip paths, and the caller's
// zero-sample early return counted nothing either. A record that is dropped silently is a
// record nobody can be told about.
//
// The counts are per RECORD, not per sample: one record yields at most one sample, so they
// are the same unit as the samples that did survive.
type Skips struct {
	// NonNumeric is records carrying no numeric value: a boolean/string/opaque reading
	// (vb/vs/vd) or a sum-only record. The expected steady state for a fleet whose IPSO
	// objects are not measurements, and the reason an empty result is not an error.
	NonNumeric int
	// NonFinite is records whose v+bv resolved to NaN or ±Inf. Always a fault: a
	// non-finite value serializes to text the resolver's ParseFloat ACCEPTS, so these
	// would otherwise land in the time-series store.
	NonFinite int
	// Unnamed is records whose resolved name (bn+n) normalised to empty. Always a fault:
	// a sample with no resource path has no series to belong to.
	Unnamed int
}

// Total is every record the decode dropped, for a caller that wants one number.
func (s Skips) Total() int { return s.NonNumeric + s.NonFinite + s.Unnamed }

// Samples decodes one payload of the given CoAP content format into measurement samples,
// stamping the receipt clock (now) wherever the payload carries no absolute time. It returns
// ErrUnsupportedContentFormat for a format this slice does not handle. A successful decode of
// a well-formed pack that happens to contain only non-numeric records returns an empty slice
// and no error (nothing to measure is not an error) — with the skip accounted in Skips, which
// is what tells that case apart from a device that sent nothing.
//
// Every numeric record becomes a sample: the decode does not bound a pack. What bounds a
// Notify's volume is the per-tenant sample budget the caller charges, and what bounds one
// event is the shared emitter, which splits a large batch into events of at most
// eventlimit.MaxReadingsPerEvent readings. See Skips for what the second return value
// accounts for.
func Samples(cf message.MediaType, payload []byte, now func() time.Time) ([]adapter.Sample, Skips, error) {
	switch cf {
	case message.AppSenmlJSON:
		return decodeSenmlJSON(payload, now)
	default:
		return nil, Skips{}, ErrUnsupportedContentFormat
	}
}

// senmlRecord is one SenML-JSON record (RFC 8428 §5). Every optional numeric field is a
// pointer so an absent field is distinguishable from a present zero — a present bt=0 means
// something different from an omitted bt, and a present v=0 is a real measurement.
type senmlRecord struct {
	BaseName    *string  `json:"bn"`
	BaseTime    *float64 `json:"bt"`
	BaseValue   *float64 `json:"bv"`
	BaseVersion *int     `json:"bver"`
	Name        string   `json:"n"`
	Value       *float64 `json:"v"`
	BoolValue   *bool    `json:"vb"`
	StringValue *string  `json:"vs"`
	DataValue   *string  `json:"vd"`
	Sum         *float64 `json:"s"`
	Time        *float64 `json:"t"`
}

// decodeSenmlJSON resolves a SenML-JSON pack into numeric samples. Base fields
// (bn/bt/bv/bver) are STICKY per RFC 8428 §4.1: a base field applies to its own record and
// every record after it until another record sets that same base field — so they are carried
// forward, not read only from record 0. A record whose base version is not the one we decode
// rejects the pack whole, wherever in the pack it sits: every record is examined. Non-numeric
// records (vb/vs/vd), sum-only records (s with no v),
// and records resolving to a non-finite value or an empty name are skipped, never emitted.
func decodeSenmlJSON(payload []byte, now func() time.Time) ([]adapter.Sample, Skips, error) {
	var records []senmlRecord
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, Skips{}, err
	}

	var (
		baseName    string
		baseTime    float64
		baseValue   float64
		baseVersion = senmlVersion
		skips       Skips
	)
	out := make([]adapter.Sample, 0, len(records))
	for i := range records {
		r := &records[i]
		// Apply sticky base fields in declaration order before resolving this record.
		if r.BaseName != nil {
			baseName = *r.BaseName
		}
		if r.BaseTime != nil {
			baseTime = *r.BaseTime
		}
		if r.BaseValue != nil {
			baseValue = *r.BaseValue
		}
		if r.BaseVersion != nil {
			baseVersion = *r.BaseVersion
		}
		if baseVersion != senmlVersion {
			return nil, Skips{}, ErrUnsupportedSenmlVersion
		}

		// Numeric value only (ADR-016). A record with no v — a boolean/string/data reading
		// or a sum-only record — is not a measurement.
		//
		// Every skip below is COUNTED. Dropping is right in all three cases; doing it
		// silently is what made "only booleans", "garbage" and "nothing at all" read
		// identically from outside.
		if r.Value == nil {
			skips.NonNumeric++
			continue
		}
		value := *r.Value + baseValue
		if math.IsNaN(value) || math.IsInf(value, 0) {
			// A v+bv overflow renders "+Inf", which the resolver's ParseFloat ACCEPTS —
			// so a non-finite value would land in the TSDB. Drop it at the source.
			skips.NonFinite++
			continue
		}

		name, ok := normalizeName(baseName + r.Name)
		if !ok {
			skips.Unnamed++
			continue
		}

		out = append(out, adapter.Sample{
			Name:  name,
			Value: value,
			Time:  resolveTimeMs(baseTime, r.Time, now),
		})
	}
	return out, skips, nil
}

// normalizeName canonicalises a resolved SenML name (baseName+name) into a stable LwM2M
// resource path so the same resource always maps to the same measurement series. It collapses
// repeated slashes (a "/3303/0/"+"/5700" split would otherwise yield "/3303/0//5700", a
// distinct series forever), guarantees a leading slash, strips a trailing slash (so
// "/3303/0/5700" and "/3303/0/5700/" are one series, not two), and rejects an empty name.
func normalizeName(s string) (string, bool) {
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" { // was "" or "/"
		return "", false
	}
	return s, true
}

// resolveTimeMs resolves a record's time to milliseconds since the Unix epoch. The resolved
// time is baseTime+time (RFC 8428 §4.5.3); at or above 2^28 it is an absolute time in seconds
// (converted to ms, preserving the fractional part), otherwise it is relative and we stamp the
// receipt clock rather than doing now-relative arithmetic.
func resolveTimeMs(baseTime float64, recTime *float64, now func() time.Time) int64 {
	resolved := baseTime
	if recTime != nil {
		resolved += *recTime
	}
	// The upper bound also rejects +Inf/NaN (both fail the < comparison), so a bt+t overflow
	// falls back to the receipt clock rather than converting to a garbage int64.
	if resolved >= absoluteTimeThreshold && resolved < maxAbsoluteTimeSeconds {
		return int64(math.Round(resolved * 1000))
	}
	return now().UnixMilli()
}
