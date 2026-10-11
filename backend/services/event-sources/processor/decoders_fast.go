// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
)

// decodeFast decodes the one shape devices overwhelmingly send — a Measurement envelope
// whose payload is entries[] of {measurements, occurredTime} — with a hand-written scanner
// and no reflection. It is the first thing Decode tries. ok=false means "take the next
// path": it never reports an error and never returns a partial result, so every error a
// device is sent is still the reference decode's.
//
// It only takes input whose meaning it can state without encoding/json's help, and
// DECLINES everything else, which costs only speed:
//
//   - the envelope keys are JsonEvent's exact lower-camel spellings, each at most once;
//     an unknown key, a re-spelled one or a repeat declines (the reference folds case,
//     ignores unknown fields and lets a repeat win);
//   - eventType is exactly "Measurement"; the payload object holds "entries" and nothing
//     else; each entry holds "measurements" (required) and "occurredTime" and nothing else;
//   - every value is a JSON string of printable ASCII with no escape: no number, bool,
//     null, backslash, control byte or byte at or above 0x80 anywhere. A string like that
//     is its own decoded value and survives the reference's map re-marshal unchanged, so
//     a substring of the body is exactly what the reference would have built;
//   - a measurement name repeats exactly: the reference keeps the last, and the fast decode
//     DECLINES rather than reproduce that. Names differing only in case are distinct map
//     keys on both paths, so they are taken;
//   - whitespace is JSON's four bytes, between tokens and around the document.
//
// The times go through the same functions as on the reference path: the envelope's
// through AssembleEvent (time.Parse), an entry's through time.Time.UnmarshalJSON on the
// quoted bytes, which is what the typed entry decode calls. The zero instant in an entry,
// the one refusal of the reference's entry-time probe that the typed decode does not make
// itself, declines here; so does anything AssembleEvent or checkBuilt refuses.
//
// All strings in the result are substrings of ONE copy of the body, so a reading costs no
// allocation of its own. The copy lives as long as any of them does, which is as long as
// the event: the body is bounded at 1 MiB and the event is published and dropped.
func (jd *JsonDecoder) decodeFast(payload []byte, receivedAt time.Time) (*model.UnresolvedEvent, interface{}, bool) {
	sc := fastScanner{s: string(payload), b: payload}
	var (
		jevent      JsonEvent
		seen        uint16
		entries     []model.UnresolvedMeasurementsEntry
		havePayload bool
	)
	if !sc.open('{') {
		return nil, nil, false
	}
	for more := true; more; {
		key, ok := sc.key()
		if !ok {
			return nil, nil, false
		}
		bit := envelopeKeyBit(key)
		if bit == 0 || seen&bit != 0 {
			return nil, nil, false
		}
		seen |= bit
		if bit == envPayload {
			if entries, ok = sc.measurementsPayload(); !ok {
				return nil, nil, false
			}
			havePayload = true
		} else {
			value, ok := sc.str()
			if !ok {
				return nil, nil, false
			}
			switch bit {
			case envDevice:
				jevent.Device = value
			case envEventType:
				if value != "Measurement" {
					return nil, nil, false
				}
				jevent.EventType = value
			case envAltId:
				jevent.AltId = &value
			case envRelationship:
				jevent.Relationship = &value
			case envOccurredTime:
				jevent.OccurredTime = &value
			case envCredentialType:
				jevent.CredentialType = &value
			case envCredentialId:
				jevent.CredentialId = &value
			case envCredentialSecret:
				jevent.CredentialSecret = &value
			}
		}
		if more, ok = sc.next('}'); !ok {
			return nil, nil, false
		}
	}
	if !sc.end() || !havePayload || jevent.EventType == "" {
		return nil, nil, false
	}
	event, err := jd.AssembleEvent(&jevent, receivedAt)
	if err != nil {
		return nil, nil, false
	}
	built := &model.UnresolvedMeasurementsPayload{Entries: entries}
	if err := checkBuilt(event, built); err != nil {
		return nil, nil, false
	}
	return event, built, true
}

// The envelope keys decodeFast takes, one bit each so a repeat is caught.
const (
	envDevice uint16 = 1 << iota
	envEventType
	envPayload
	envAltId
	envRelationship
	envOccurredTime
	envCredentialType
	envCredentialId
	envCredentialSecret
)

func envelopeKeyBit(key string) uint16 {
	switch key {
	case "device":
		return envDevice
	case "eventType":
		return envEventType
	case "payload":
		return envPayload
	case "altId":
		return envAltId
	case "relationship":
		return envRelationship
	case "occurredTime":
		return envOccurredTime
	case "credentialType":
		return envCredentialType
	case "credentialId":
		return envCredentialId
	case "credentialSecret":
		return envCredentialSecret
	}
	return 0
}

// fastScanner reads the strict subset of JSON decodeFast takes. Every method answers
// ok=false on anything outside it.
type fastScanner struct {
	s string
	b []byte // the same bytes as s, for the one call that takes a slice
	i int
}

func (sc *fastScanner) ws() {
	for sc.i < len(sc.s) {
		switch sc.s[sc.i] {
		case ' ', '\t', '\n', '\r':
			sc.i++
		default:
			return
		}
	}
}

// open consumes optional whitespace and then c, which must open a NON-EMPTY container:
// every container decodeFast reads is one the reference refuses when it is empty, or (the
// envelope) one that cannot be a Measurement when it is.
func (sc *fastScanner) open(c byte) bool {
	sc.ws()
	if sc.i >= len(sc.s) || sc.s[sc.i] != c {
		return false
	}
	sc.i++
	sc.ws()
	return sc.i < len(sc.s) && sc.s[sc.i] != closerOf(c)
}

func closerOf(c byte) byte {
	if c == '{' {
		return '}'
	}
	return ']'
}

// next consumes whitespace and then either a comma (more=true) or closer (more=false).
func (sc *fastScanner) next(closer byte) (more, ok bool) {
	sc.ws()
	if sc.i >= len(sc.s) {
		return false, false
	}
	switch sc.s[sc.i] {
	case ',':
		sc.i++
		return true, true
	case closer:
		sc.i++
		return false, true
	}
	return false, false
}

// end reports whether only whitespace remains.
func (sc *fastScanner) end() bool {
	sc.ws()
	return sc.i == len(sc.s)
}

// str consumes whitespace and a string of printable ASCII with no escape, and returns its
// contents.
func (sc *fastScanner) str() (string, bool) {
	sc.ws()
	if sc.i >= len(sc.s) || sc.s[sc.i] != '"' {
		return "", false
	}
	start := sc.i + 1
	for j := start; j < len(sc.s); j++ {
		switch c := sc.s[j]; {
		case c == '"':
			sc.i = j + 1
			return sc.s[start:j], true
		case c < 0x20 || c >= 0x80 || c == '\\':
			return "", false
		}
	}
	return "", false
}

// key is str followed by the colon.
func (sc *fastScanner) key() (string, bool) {
	k, ok := sc.str()
	if !ok {
		return "", false
	}
	sc.ws()
	if sc.i >= len(sc.s) || sc.s[sc.i] != ':' {
		return "", false
	}
	sc.i++
	return k, true
}

// measurementsPayload reads {"entries":[entry,...]} with at least one entry.
func (sc *fastScanner) measurementsPayload() ([]model.UnresolvedMeasurementsEntry, bool) {
	if !sc.open('{') {
		return nil, false
	}
	if k, ok := sc.key(); !ok || k != "entries" {
		return nil, false
	}
	if !sc.open('[') {
		return nil, false
	}
	entries := make([]model.UnresolvedMeasurementsEntry, 0, 1)
	for more := true; more; {
		entry, ok := sc.entry()
		if !ok {
			return nil, false
		}
		entries = append(entries, entry)
		if more, ok = sc.next(']'); !ok {
			return nil, false
		}
	}
	// The payload object holds entries and nothing else.
	if more, ok := sc.next('}'); !ok || more {
		return nil, false
	}
	return entries, true
}

// entry reads {"measurements":{...},"occurredTime":"..."} in either order, measurements
// required and non-empty, occurredTime optional and not the zero instant.
func (sc *fastScanner) entry() (model.UnresolvedMeasurementsEntry, bool) {
	var entry model.UnresolvedMeasurementsEntry
	if !sc.open('{') {
		return entry, false
	}
	var haveTime bool
	for more := true; more; {
		k, ok := sc.key()
		if !ok {
			return entry, false
		}
		switch k {
		case "measurements":
			if entry.Measurements != nil {
				return entry, false
			}
			if entry.Measurements, ok = sc.readings(); !ok {
				return entry, false
			}
		case "occurredTime":
			if haveTime {
				return entry, false
			}
			haveTime = true
			sc.ws()
			start := sc.i
			if _, ok := sc.str(); !ok {
				return entry, false
			}
			// The quoted bytes, as the typed decode hands them to the same method.
			at := new(time.Time)
			if err := at.UnmarshalJSON(sc.b[start:sc.i]); err != nil || at.IsZero() {
				return entry, false
			}
			entry.OccurredTime = at
		default:
			return entry, false
		}
		if more, ok = sc.next('}'); !ok {
			return entry, false
		}
	}
	return entry, entry.Measurements != nil
}

// readings reads a non-empty flat object of string values with no repeated name.
func (sc *fastScanner) readings() (map[string]string, bool) {
	if !sc.open('{') {
		return nil, false
	}
	m := make(map[string]string, 8)
	for more := true; more; {
		name, ok := sc.key()
		if !ok {
			return nil, false
		}
		value, ok := sc.str()
		if !ok {
			return nil, false
		}
		if _, dup := m[name]; dup {
			return nil, false
		}
		m[name] = value
		if more, ok = sc.next('}'); !ok {
			return nil, false
		}
	}
	return m, true
}
