// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IsKnownChannelType reports whether id names a channel type in the catalog
// (model.SupportedChannelTypes). A channel may be configured for a known type even
// before that type's adapter ships (Available=false): the config is accepted and
// stored, it simply will not deliver until the adapter lands (N.C). An UNKNOWN
// type is rejected — there is nowhere for it to route.
func IsKnownChannelType(id string) bool {
	for i := range SupportedChannelTypes {
		if SupportedChannelTypes[i].Id == id {
			return true
		}
	}
	return false
}

// validateChannelType fails closed on a channel type absent from the catalog.
func validateChannelType(ct string) error {
	if !IsKnownChannelType(ct) {
		return fmt.Errorf("unknown channel type %q (known: %s)", ct, strings.Join(channelTypeIds(), ", "))
	}
	return nil
}

// channelTypeIds returns the catalog ids for error messages.
func channelTypeIds() []string {
	ids := make([]string, 0, len(SupportedChannelTypes))
	for i := range SupportedChannelTypes {
		ids = append(ids, SupportedChannelTypes[i].Id)
	}
	return ids
}

// validateJSONObject checks that a nil-or-JSON string is a well-formed JSON
// object (not an array or scalar). Channel config and rule recipients are opaque
// to this slice; the model only guarantees they are structurally valid so a later
// adapter can trust the shape it reads. A nil pointer is valid (unset).
func validateJSONObject(s *string, field string) error {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	return nil
}

// validateStringArray checks that a nil-or-JSON string is a well-formed JSON array
// of strings (the recipients shape). A nil pointer is valid (no recipients — a
// webhook rule targets the channel's endpoint, so recipients may be empty).
func validateStringArray(s *string, field string) error {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
		return fmt.Errorf("%s must be a JSON array of strings: %w", field, err)
	}
	return nil
}
