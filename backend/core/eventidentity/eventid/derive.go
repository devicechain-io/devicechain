// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventid

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// Event is the part of an event its identity is derived from.
type Event struct {
	DeviceToken  string
	EventType    uint64
	OccurredTime time.Time
	// AltId is the device-supplied alternate id, nil when the device made no claim. A present but
	// empty id is a claim the device made and differs from an absent one.
	AltId *string
}

// AppendEventIdHead appends the length-prefixed fields of the event id that precede the payload,
// exactly as Hash feeds them to its digest.
func AppendEventIdHead(dst []byte, tenantId string, e Event) []byte {
	dst = AppendFieldString(dst, tenantId)
	dst = AppendFieldString(dst, e.DeviceToken)
	dst = binary.BigEndian.AppendUint64(dst, 8)
	dst = binary.BigEndian.AppendUint64(dst, e.EventType)
	dst = binary.BigEndian.AppendUint64(dst, 8)
	dst = binary.BigEndian.AppendUint64(dst, uint64(e.OccurredTime.UTC().UnixNano()))
	if e.AltId != nil {
		dst = AppendField(dst, []byte{1})
		dst = AppendFieldString(dst, *e.AltId)
	} else {
		dst = AppendField(dst, []byte{0})
	}
	return dst
}

// DeriveFast hashes the event-id preimage built directly in one pooled buffer. False when the
// payload needs the json.Marshal path.
func DeriveFast(tenantId string, e Event, payload any) ([]byte, bool) {
	bp := IdentityBufs.Get().(*[]byte)
	defer IdentityBufs.Put(bp)
	buf := AppendEventIdHead((*bp)[:0], tenantId, e)
	lenAt := len(buf)
	buf = append(buf, 0, 0, 0, 0, 0, 0, 0, 0)
	var ok bool
	if buf, ok = AppendCanonicalPayload(buf, payload); !ok {
		*bp = buf[:0]
		return nil, false
	}
	binary.BigEndian.PutUint64(buf[lenAt:], uint64(len(buf)-lenAt-8))
	sum := sha256.Sum256(buf)
	*bp = buf[:0]
	return sum[:], true
}

// Hash is the event id of an event whose payload is already canonical JSON: the SHA-256 of the
// length-prefixed tenant, device token, event type, occurred time (nanoseconds), alternate id and
// payload. The caller owes a CANONICAL payload (see event-management's DeriveEventId).
func Hash(tenantId string, e Event, canonicalPayload []byte) []byte {
	h := sha256.New()
	write := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	write([]byte(tenantId))
	write([]byte(e.DeviceToken))
	var u [8]byte
	binary.BigEndian.PutUint64(u[:], e.EventType)
	write(u[:])
	binary.BigEndian.PutUint64(u[:], uint64(e.OccurredTime.UTC().UnixNano()))
	write(u[:])
	if e.AltId != nil {
		write([]byte{1})
		write([]byte(*e.AltId))
	} else {
		write([]byte{0})
	}
	write(canonicalPayload)
	return h.Sum(nil)
}

// OfResolved is the event id of a resolved event as event-management stores it under (device =
// the source device, time = the occurred time, alternate id as sent). Event-processing's
// duplicate guard remembers events by it.
func OfResolved(tenantId string, ev *resolvedevent.ResolvedEvent) ([]byte, error) {
	// Event-management stores the alternate id through sqlnull.Text: trimmed, and absent when
	// blank. The identity must see exactly what it stores.
	var alt *string
	if ev.AltId != nil {
		if t := strings.TrimSpace(*ev.AltId); t != "" {
			alt = &t
		}
	}
	return Derive(tenantId, Event{
		DeviceToken:  ev.SourceDeviceToken,
		EventType:    uint64(ev.EventType),
		OccurredTime: ev.OccurredTime,
		AltId:        alt,
	}, ev.Payload)
}

// Derive is the event id of an event with a resolved payload: the fast path where it can
// reproduce json.Marshal, else json.Marshal of the payload's identity view (calculated values
// excluded) hashed by Hash. Event-management stores events under it and event-processing's
// duplicate guard remembers events by it.
func Derive(tenantId string, e Event, payload any) ([]byte, error) {
	if id, ok := DeriveFast(tenantId, e, payload); ok {
		return id, nil
	}
	encoded, err := json.Marshal(eventidentity.Payload(payload))
	if err != nil {
		return nil, fmt.Errorf("canonicalizing payload for the event identity: %w", err)
	}
	return Hash(tenantId, e, encoded), nil
}
