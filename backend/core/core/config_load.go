// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// LoadConfiguration decodes a microservice configuration document into a typed
// struct with fail-closed semantics (ADR-022 decision 1): unknown fields are
// rejected so a typo or stale key is an error rather than a silently ignored
// setting, and any trailing data after the document is rejected. After a
// successful decode it applies defaults and validates when the target implements
// ConfigDefaulter / ConfigValidator. An empty (or whitespace-only) document is
// treated as "no overrides" so a service still runs on its defaults; defaulting
// and validation always run so the result is never an unvalidated zero value.
func LoadConfiguration(raw []byte, into any) error {
	if len(bytes.TrimSpace(raw)) > 0 {
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
