// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventid derives the content identity of a resolved event: the id event-management
// stores it under and the identity event-processing's duplicate guard remembers it by. It is ONE
// function with ONE preimage, so the two cannot disagree about which events are the same event.
//
// The fast path writes the digest input straight into a pooled buffer instead of marshalling a
// document only to hash it (it used to be ~10% of event-management's CPU). The slow path is the
// json.Marshal derivation it must equal byte for byte.
package eventid

import (
	"encoding/binary"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/eventidentity"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// This file builds the bytes an identity digest hashes WITHOUT going through
// encoding/json, which used to be ~10% of event-management's CPU (it marshalled a
// document only to hash it).
//
// 🔴 THE OUTPUT IS BYTE-IDENTICAL TO json.Marshal, BY CONTRACT. An event id and a
// payload id are identity and dedup across the platform: a digest that moves for any
// input stores that input twice when it is redelivered across the deploy. So every
// writer here emits exactly the document json.Marshal would have emitted for the same
// value (declaration-order field names, HTML-escaped strings, the encoding/json float
// format, RFC 3339 nanosecond times, omitempty where the struct had it), and
// identity_bytes_test.go keeps the json.Marshal derivation as an ORACLE and compares the
// digests over many generated inputs.
//
// 🔴 ANYTHING UNUSUAL FALLS BACK TO json.Marshal rather than being reproduced: a payload
// type this file does not know, a string that is not valid UTF-8, a NaN or infinite
// float, a time json would refuse, a sample carrying drop records. The fallback is the
// old path verbatim, so it cannot disagree with it, and it also reports json's own
// error. The fast paths cover the shapes that occur on the hot path; everything else is
// slower but identical.

// AppendJSONString appends s as encoding/json writes a string (HTML escaping on). It
// reports false for a string json would rewrite (invalid UTF-8), so the caller falls back.
func AppendJSONString(dst []byte, s string) ([]byte, bool) {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '"', '\\':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return dst, false
		}
		if r == ' ' || r == ' ' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"'), true
}

// AppendJSONFloat appends f as encoding/json writes a float64. False for NaN and Inf,
// which json refuses.
func AppendJSONFloat(dst []byte, f float64) ([]byte, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dst, false
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, f, format, -1, 64)
	if format == 'e' {
		// clean up e-09 to e-9, as encoding/json does
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, true
}

// AppendJSONTime appends t as time.Time.MarshalJSON writes it. False for an instant
// MarshalJSON refuses or renders in a way this does not reproduce (year outside 0..9999,
// a zone offset that is not whole minutes inside a day).
func AppendJSONTime(dst []byte, t time.Time) ([]byte, bool) {
	if y := t.Year(); y < 0 || y > 9999 {
		return dst, false
	}
	if _, off := t.Zone(); off%60 != 0 || off <= -24*3600 || off >= 24*3600 {
		return dst, false
	}
	dst = append(dst, '"')
	dst = t.AppendFormat(dst, time.RFC3339Nano)
	return append(dst, '"'), true
}

func AppendJSONStringPtr(dst []byte, s *string) ([]byte, bool) {
	if s == nil {
		return append(dst, "null"...), true
	}
	return AppendJSONString(dst, *s)
}

func AppendJSONFloatPtr(dst []byte, f *float64) ([]byte, bool) {
	if f == nil {
		return append(dst, "null"...), true
	}
	return AppendJSONFloat(dst, *f)
}

// AppendKey appends `{"Name":` for the first field, `,"Name":` for the rest.
func AppendKey(dst []byte, first bool, name string) []byte {
	if first {
		dst = append(dst, '{')
	} else {
		dst = append(dst, ',')
	}
	dst = append(dst, '"')
	dst = append(dst, name...)
	return append(dst, '"', ':')
}

// ---- payload documents ------------------------------------------------------

func AppendMeasurementsPayload(dst []byte, p *resolvedevent.ResolvedMeasurementsPayload) ([]byte, bool) {
	if p == nil {
		return append(dst, "null"...), true
	}
	ok := true
	dst = AppendKey(dst, true, "Entries")
	if p.Entries == nil {
		dst = append(dst, "null"...)
	} else {
		dst = append(dst, '[')
		for i := range p.Entries {
			if i > 0 {
				dst = append(dst, ',')
			}
			sample := &p.Entries[i]
			// Defensive: IdentityView strips drop records before the payload gets here, so this
			// does not run on the identity path. A payload that still carries one is marshalled
			// by json (which writes the field) rather than reproduced.
			if len(sample.Dropped) > 0 {
				return dst, false
			}
			dst = AppendKey(dst, true, "Entries")
			if sample.Entries == nil {
				dst = append(dst, "null"...)
			} else {
				dst = append(dst, '[')
				for j := range sample.Entries {
					if j > 0 {
						dst = append(dst, ',')
					}
					mx := &sample.Entries[j]
					dst = AppendKey(dst, true, "Name")
					if dst, ok = AppendJSONString(dst, mx.Name); !ok {
						return dst, false
					}
					dst = AppendKey(dst, false, "Value")
					if dst, ok = AppendJSONString(dst, mx.Value); !ok {
						return dst, false
					}
					dst = AppendKey(dst, false, "Classifier")
					if mx.Classifier == nil {
						dst = append(dst, "null"...)
					} else {
						dst = strconv.AppendUint(dst, *mx.Classifier, 10)
					}
					dst = AppendKey(dst, false, "Unit")
					if dst, ok = AppendJSONStringPtr(dst, mx.Unit); !ok {
						return dst, false
					}
					dst = AppendKey(dst, false, "DataType")
					if dst, ok = AppendJSONStringPtr(dst, mx.DataType); !ok {
						return dst, false
					}
					if mx.Derived {
						dst = append(dst, `,"Derived":true`...)
					}
					dst = append(dst, '}')
				}
				dst = append(dst, ']')
			}
			dst = AppendKey(dst, false, "OccurredTime")
			if dst, ok = AppendJSONTime(dst, sample.OccurredTime); !ok {
				return dst, false
			}
			dst = append(dst, '}')
		}
		dst = append(dst, ']')
	}
	return append(dst, '}'), true
}

func AppendLocationsPayload(dst []byte, p *resolvedevent.ResolvedLocationsPayload) ([]byte, bool) {
	if p == nil {
		return append(dst, "null"...), true
	}
	ok := true
	dst = AppendKey(dst, true, "Entries")
	if p.Entries == nil {
		dst = append(dst, "null"...)
	} else {
		dst = append(dst, '[')
		for i := range p.Entries {
			if i > 0 {
				dst = append(dst, ',')
			}
			e := &p.Entries[i]
			for k, f := range [...]struct {
				name string
				v    *string
			}{{"Latitude", e.Latitude}, {"Longitude", e.Longitude}, {"Elevation", e.Elevation},
				{"Accuracy", e.Accuracy}, {"Speed", e.Speed}, {"Heading", e.Heading}} {
				dst = AppendKey(dst, k == 0, f.name)
				if dst, ok = AppendJSONStringPtr(dst, f.v); !ok {
					return dst, false
				}
			}
			dst = AppendKey(dst, false, "OccurredTime")
			if dst, ok = AppendJSONTime(dst, e.OccurredTime); !ok {
				return dst, false
			}
			dst = append(dst, '}')
		}
		dst = append(dst, ']')
	}
	return append(dst, '}'), true
}

func AppendAlertsPayload(dst []byte, p *resolvedevent.ResolvedAlertsPayload) ([]byte, bool) {
	if p == nil {
		return append(dst, "null"...), true
	}
	ok := true
	dst = AppendKey(dst, true, "Entries")
	if p.Entries == nil {
		dst = append(dst, "null"...)
	} else {
		dst = append(dst, '[')
		for i := range p.Entries {
			if i > 0 {
				dst = append(dst, ',')
			}
			e := &p.Entries[i]
			dst = AppendKey(dst, true, "Type")
			if dst, ok = AppendJSONString(dst, e.Type); !ok {
				return dst, false
			}
			dst = AppendKey(dst, false, "Level")
			dst = strconv.AppendUint(dst, uint64(e.Level), 10)
			dst = AppendKey(dst, false, "Message")
			if dst, ok = AppendJSONString(dst, e.Message); !ok {
				return dst, false
			}
			dst = AppendKey(dst, false, "Source")
			if dst, ok = AppendJSONString(dst, e.Source); !ok {
				return dst, false
			}
			dst = AppendKey(dst, false, "OccurredTime")
			if dst, ok = AppendJSONTime(dst, e.OccurredTime); !ok {
				return dst, false
			}
			dst = append(dst, '}')
		}
		dst = append(dst, ']')
	}
	return append(dst, '}'), true
}

func AppendStateChangePayload(dst []byte, p *resolvedevent.ResolvedStateChangePayload) ([]byte, bool) {
	if p == nil {
		return append(dst, "null"...), true
	}
	ok := true
	dst = AppendKey(dst, true, "State")
	if dst, ok = AppendJSONString(dst, p.State); !ok {
		return dst, false
	}
	dst = AppendKey(dst, false, "Reason")
	if dst, ok = AppendJSONString(dst, p.Reason); !ok {
		return dst, false
	}
	dst = AppendKey(dst, false, "ExpectedSessionId")
	dst = strconv.AppendUint(dst, p.ExpectedSessionId, 10)
	dst = AppendKey(dst, false, "SessionId")
	dst = strconv.AppendUint(dst, p.SessionId, 10)
	dst = AppendKey(dst, false, "OccurredTime")
	if dst, ok = AppendJSONStringPtr(dst, p.OccurredTime); !ok {
		return dst, false
	}
	return append(dst, '}'), true
}

func AppendNewRelationshipPayload(dst []byte, p *resolvedevent.ResolvedNewRelationshipPayload) ([]byte, bool) {
	if p == nil {
		return append(dst, "null"...), true
	}
	ok := true
	dst = AppendKey(dst, true, "RelationshipTypeId")
	dst = strconv.AppendUint(dst, p.RelationshipTypeId, 10)
	dst = AppendKey(dst, false, "TargetType")
	if dst, ok = AppendJSONStringPtr(dst, p.TargetType); !ok {
		return dst, false
	}
	dst = AppendKey(dst, false, "TargetId")
	if p.TargetId == nil {
		dst = append(dst, "null"...)
	} else {
		dst = strconv.AppendUint(dst, *p.TargetId, 10)
	}
	return append(dst, '}'), true
}

// AppendCanonicalPayload appends the document json.Marshal(eventidentity.Payload(payload))
// would produce, or reports false when it cannot be sure to reproduce it.
func AppendCanonicalPayload(dst []byte, payload any) ([]byte, bool) {
	switch p := eventidentity.Payload(payload).(type) {
	case nil:
		return append(dst, "null"...), true
	case *resolvedevent.ResolvedMeasurementsPayload:
		return AppendMeasurementsPayload(dst, p)
	case resolvedevent.ResolvedMeasurementsPayload:
		return AppendMeasurementsPayload(dst, &p)
	case *resolvedevent.ResolvedLocationsPayload:
		return AppendLocationsPayload(dst, p)
	case resolvedevent.ResolvedLocationsPayload:
		return AppendLocationsPayload(dst, &p)
	case *resolvedevent.ResolvedAlertsPayload:
		return AppendAlertsPayload(dst, p)
	case resolvedevent.ResolvedAlertsPayload:
		return AppendAlertsPayload(dst, &p)
	case *resolvedevent.ResolvedStateChangePayload:
		return AppendStateChangePayload(dst, p)
	case resolvedevent.ResolvedStateChangePayload:
		return AppendStateChangePayload(dst, &p)
	case *resolvedevent.ResolvedNewRelationshipPayload:
		return AppendNewRelationshipPayload(dst, p)
	case resolvedevent.ResolvedNewRelationshipPayload:
		return AppendNewRelationshipPayload(dst, &p)
	}
	return dst, false
}

// ---- digest input -----------------------------------------------------------

var IdentityBufs = sync.Pool{New: func() any { b := make([]byte, 0, 1024); return &b }}

func AppendField(dst, value []byte) []byte {
	dst = binary.BigEndian.AppendUint64(dst, uint64(len(value)))
	return append(dst, value...)
}

func AppendFieldString(dst []byte, value string) []byte {
	dst = binary.BigEndian.AppendUint64(dst, uint64(len(value)))
	return append(dst, value...)
}
