// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"

	"github.com/devicechain-io/dc-microservice/eventidentity/eventid"
)

// The byte-for-byte event-id derivation (the JSON writers and the payload appenders) lives in
// core/eventidentity/eventid, shared with event-processing's duplicate guard so the two cannot
// disagree about which events are one. This file keeps the payload-ROW preimages.

// Aliases for the tests that pin the shared code from here.
var (
	appendCanonicalPayload = eventid.AppendCanonicalPayload
	appendField            = eventid.AppendField
)

func appendEventIdHead(dst []byte, tenantId string, event *Event) []byte {
	return eventid.AppendEventIdHead(dst, tenantId, eventidEvent(event))
}

// eventidEvent is the part of an event its identity is derived from.
func eventidEvent(event *Event) eventid.Event {
	e := eventid.Event{DeviceToken: event.DeviceToken, EventType: uint64(event.EventType), OccurredTime: event.OccurredTime}
	if event.AltId.Valid {
		e.AltId = &event.AltId.String
	}
	return e
}

// derivePayloadIdFast hashes (eventId, entry) where entry is appended by build. False when
// build reports it cannot reproduce json.Marshal.
func derivePayloadIdFast(eventId []byte, build func([]byte) ([]byte, bool)) ([]byte, bool) {
	bp := eventid.IdentityBufs.Get().(*[]byte)
	defer eventid.IdentityBufs.Put(bp)
	buf := eventid.AppendField((*bp)[:0], eventId)
	lenAt := len(buf)
	buf = append(buf, 0, 0, 0, 0, 0, 0, 0, 0)
	var ok bool
	if buf, ok = build(buf); !ok {
		*bp = buf[:0]
		return nil, false
	}
	binary.BigEndian.PutUint64(buf[lenAt:], uint64(len(buf)-lenAt-8))
	sum := sha256.Sum256(buf)
	*bp = buf[:0]
	return sum[:], true
}

// ---- payload-row preimages (the anonymous structs of payload_identity.go) ----

func appendMeasurementEntry(dst []byte, r *MeasurementEventCreateRequest) ([]byte, bool) {
	ok := true
	if r.Derived {
		dst = eventid.AppendKey(dst, true, "Kind")
		dst = append(dst, `"derived"`...)
		dst = eventid.AppendKey(dst, false, "Name")
		if dst, ok = eventid.AppendJSONString(dst, r.Name); !ok {
			return dst, false
		}
		dst = eventid.AppendKey(dst, false, "Occurred")
		if dst, ok = eventid.AppendJSONTime(dst, CanonicalEntryInstant(r.EntryOccurredTime)); !ok {
			return dst, false
		}
		return append(dst, '}'), true
	}
	dst = eventid.AppendKey(dst, true, "Name")
	if dst, ok = eventid.AppendJSONString(dst, r.Name); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Value")
	if dst, ok = eventid.AppendJSONFloatPtr(dst, r.Value); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Classifier")
	if r.Classifier == nil {
		dst = append(dst, "null"...)
	} else {
		dst = strconv.AppendUint(dst, uint64(*r.Classifier), 10)
	}
	dst = eventid.AppendKey(dst, false, "Unit")
	if dst, ok = eventid.AppendJSONStringPtr(dst, r.Unit); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "DataType")
	if dst, ok = eventid.AppendJSONStringPtr(dst, r.DataType); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Occurred")
	if dst, ok = eventid.AppendJSONTime(dst, r.EntryOccurredTime); !ok {
		return dst, false
	}
	return append(dst, '}'), true
}

func appendLocationEntry(dst []byte, r *LocationEventCreateRequest) ([]byte, bool) {
	ok := true
	for k, f := range [...]struct {
		name string
		v    *float64
	}{{"Lat", r.Latitude}, {"Lon", r.Longitude}, {"Elev", r.Elevation},
		{"Accuracy", r.Accuracy}, {"Speed", r.Speed}, {"Heading", r.Heading}} {
		dst = eventid.AppendKey(dst, k == 0, f.name)
		if dst, ok = eventid.AppendJSONFloatPtr(dst, f.v); !ok {
			return dst, false
		}
	}
	dst = eventid.AppendKey(dst, false, "Occurred")
	if dst, ok = eventid.AppendJSONTime(dst, r.EntryOccurredTime); !ok {
		return dst, false
	}
	return append(dst, '}'), true
}

func appendAlertEntry(dst []byte, r *AlertEventCreateRequest) ([]byte, bool) {
	ok := true
	dst = eventid.AppendKey(dst, true, "Type")
	if dst, ok = eventid.AppendJSONString(dst, r.Type); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Level")
	dst = strconv.AppendUint(dst, uint64(r.Level), 10)
	dst = eventid.AppendKey(dst, false, "Message")
	if dst, ok = eventid.AppendJSONString(dst, r.Message); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Source")
	if dst, ok = eventid.AppendJSONString(dst, r.Source); !ok {
		return dst, false
	}
	dst = eventid.AppendKey(dst, false, "Occurred")
	if dst, ok = eventid.AppendJSONTime(dst, r.EntryOccurredTime); !ok {
		return dst, false
	}
	return append(dst, '}'), true
}
