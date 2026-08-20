// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// ConfigDefaulter is implemented by configuration types that fill zero-valued
// fields with explicit defaults after decoding (ADR-022 decision 1). ApplyDefaults
// runs after a successful decode and before ConfigValidator.Validate, so defaults
// are authoritative regardless of which keys the document supplied.
type ConfigDefaulter interface {
	ApplyDefaults()
}

// ConfigValidator is implemented by configuration types that enforce semantic
// constraints after decoding and defaulting (ADR-022 decision 1). A non-nil error
// fails the load closed, moving config errors from pod runtime to load time.
type ConfigValidator interface {
	Validate() error
}

// ConfigRetirer is implemented by configuration types that have REMOVED a key an
// earlier release accepted. It returns the retired key mapped to the guidance an
// operator needs: what replaced it, or where the setting moved to.
//
// 🔴 IT EXISTS BECAUSE DisallowUnknownFields CANNOT TELL A TYPO FROM A REMOVAL, and
// those must not have the same consequence. Failing closed on a key nobody defined is
// the whole point of the fail-closed posture: a misspelled setting that is silently
// ignored is a setting the operator believes is in force. But a key THIS PROJECT
// published and then deleted is a different thing entirely — the operator did nothing
// wrong, they upgraded — and refusing the load turns every future config removal into
// an upgrade that stops the pod.
//
// That is not hypothetical. maxEventFutureSkewSeconds was documented on the detection
// engine's deployment page, in both locales, and then moved to another service; without
// this, every operator who had set the key as documented got a crash-looping
// event-processing on upgrade, with an error naming the key as unknown.
//
// A retired key is stripped from the document before the strict decode and reported at
// WARN. The warning must say the value is NOT being applied, because the failure this
// replaces was at least loud: an operator who reads "ignored" and does nothing is in the
// same place as one who never set it, which is the outcome we are choosing.
//
// Keys are matched case-insensitively and at the TOP LEVEL of the document only, which
// is how encoding/json matches field names and where every retired key has lived. A
// nested key would need its own path syntax; nothing has needed one, so nothing offers
// one — a retired key that is not top-level will simply not match, and the load will
// fail closed as it does today.
type ConfigRetirer interface {
	RetiredConfigKeys() map[string]string
}

// LoadConfiguration decodes a microservice configuration document into a typed
// struct with fail-closed semantics (ADR-022 decision 1): unknown fields are
// rejected so a typo or stale key is an error rather than a silently ignored
// setting, and any trailing data after the document is rejected. Keys the target
// declares as RETIRED are stripped and reported first, so a removal this project
// made does not read as a typo the operator made. After a successful decode it
// applies defaults and validates when the target implements ConfigDefaulter /
// ConfigValidator. An empty (or whitespace-only) document is treated as "no
// overrides" so a service still runs on its defaults; defaulting and validation
// always run so the result is never an unvalidated zero value.
func LoadConfiguration(raw []byte, into any) error {
	if len(bytes.TrimSpace(raw)) > 0 {
		if r, ok := into.(ConfigRetirer); ok {
			raw = stripRetiredKeys(raw, r.RetiredConfigKeys())
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			return fmt.Errorf("config: decode failed: %w", err)
		}
		if dec.More() {
			return fmt.Errorf("config: unexpected trailing data after configuration document")
		}
	}
	if d, ok := into.(ConfigDefaulter); ok {
		d.ApplyDefaults()
	}
	if v, ok := into.(ConfigValidator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("config: validation failed: %w", err)
		}
	}
	return nil
}

// stripRetiredKeys removes the retired keys a document actually carries, reporting each
// one at WARN, and returns the document to decode.
//
// It returns the ORIGINAL bytes untouched whenever it changes nothing — no retired keys
// declared, the document is not a JSON object, or none of them is present. That matters
// beyond efficiency: every existing configuration then reaches the decoder byte-for-byte
// as before, so the trailing-data check and every decode error message stay exactly what
// they were, and this mechanism can only affect a service that opted into it.
//
// A document that does not parse as an object is handed on rather than reported here. It
// is about to fail the strict decode anyway, and that error describes the document the
// operator actually wrote — a complaint from this function would name a mechanism they
// have never heard of instead.
func stripRetiredKeys(raw []byte, retired map[string]string) []byte {
	if len(retired) == 0 {
		return raw
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	stripped := false
	for present := range doc {
		for key, guidance := range retired {
			// Case-insensitively, because encoding/json matches field names that way: a
			// document writing MaxEventFutureSkewSeconds bound to the field just as well
			// as maxEventFutureSkewSeconds did. Matching exactly here would retire one
			// spelling and leave the other to fail the load as unknown — the same
			// crash-loop this exists to prevent, reachable by capitalisation.
			if !strings.EqualFold(present, key) {
				continue
			}
			log.Warn().Str("key", present).
				Msg("Configuration key is RETIRED and its value is NOT being applied — the service is starting without it. " + guidance)
			delete(doc, present)
			stripped = true
			break
		}
	}
	if !stripped {
		return raw
	}
	// Re-marshalling loses key order and insignificant whitespace. Neither is read by
	// anything: the result goes straight into a struct decode.
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}
