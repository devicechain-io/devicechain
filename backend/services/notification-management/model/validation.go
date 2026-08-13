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

// validateDeviceTypeScoping rejects a policy scoped to a device type, because the
// dispatcher does not honour that scoping yet — policy_notifier skips any policy
// with a non-empty DeviceTypeToken rather than applying it tenant-wide, which
// would over-notify. Accepting the field therefore produced the worst possible
// outcome: the mutation returned success and the policy delivered NOTHING, with
// no error and no log an operator would think to look for.
//
// Failing the write instead is the fail-closed reading — a policy that cannot
// route is refused at the point the operator can still do something about it.
// When the cross-service originator→device-type resolution lands, delete this and
// the skip in policy_notifier together; a scoped policy that writes but does not
// deliver is exactly the state this exists to prevent.
func validateDeviceTypeScoping(deviceTypeToken *string) error {
	if deviceTypeToken == nil || strings.TrimSpace(*deviceTypeToken) == "" {
		return nil
	}
	return fmt.Errorf("deviceTypeToken is not honoured yet: a device-type-scoped policy is skipped by the dispatcher and would deliver nothing, so it is refused rather than silently accepted; leave deviceTypeToken unset for a tenant-wide policy")
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
