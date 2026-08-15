// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"slices"
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

// validateSeverity fails closed on a rule severity outside the alarm tier vocabulary.
//
// 🔴 WHY THIS HAS TO BE CHECKED AT THE WRITE. A rule's severity is compared to the ALARM's
// by an exact string match (processor.severityMatches), and an alarm's tier is uppercase and
// validated where it is raised. So a rule authored as "critical" writes successfully, reads
// back byte-for-byte as written, and then matches nothing for the rest of its life. The
// policy's OTHER rules keep delivering, so the operator sees notifications arriving and has
// no reason to suspect a gap — which makes this worse than the deviceTypeToken case one field
// over, where the whole policy visibly refuses.
//
// The dispatcher cannot help: its severity skip logs nothing, and deliberately so. A rule
// that does not match THIS alarm's tier is ordinary operation on every alarm the tenant
// raises, not a fault, so logging it would bury the genuine misconfigurations its
// neighbouring branches do report (a missing channel, an absent adapter). A mistyped tier
// is indistinguishable there from a correctly-typed one that simply did not match. It is
// distinguishable HERE, which is the whole argument for the check living at the write.
//
// The lowercase form is named in the error because it is the LIKELY mistake rather than a
// random typo: a DETECT rule's own authoring severity is lowercase, and the two vocabularies
// reject each other's casing by contract (see dmmodel.AlarmSeverityFromRuleSeverity), so an
// operator arriving from the rule editor has just been typing "major".
func validateSeverity(severity string) error {
	if severity == SeverityAny || slices.Contains(ruleSeverities, severity) {
		return nil
	}
	hint := ""
	if upper := strings.ToUpper(severity); slices.Contains(ruleSeverities, upper) {
		hint = fmt.Sprintf(" — did you mean %q? a notification rule matches the ALARM's tier, "+
			"which is uppercase, not a detection rule's lowercase authoring severity", upper)
	}
	return fmt.Errorf("unknown severity %q (known: %s, or %q for any)%s",
		severity, strings.Join(ruleSeverities, ", "), SeverityAny, hint)
}

// ruleSeverities is the alarm tier vocabulary a rule may name, most severe first.
//
// 🔴 IT IS RESTATED HERE RATHER THAN READ FROM device-management, AND THAT IS A
// DELIBERATE TRADE — do not "fix" it by importing the real vocabulary. The tiers are
// declared in dmmodel, whose model package transitively pulls cel-go, antlr, NATS and
// an MQTT client (through its config, selector and messaging imports). This package is
// consumed by two maintainer tools that touch none of that; importing dmmodel HERE, at
// run time, added 17 indirect modules to drdrill's go.mod — a DR instrument acquiring
// a CEL engine so it can spell five words.
//
// What makes the copy safe is that it is not trusted: TestTheRuleVocabularyMatchesTheAlarmTiers
// asserts this slice equals dmmodel.AlarmSeverities() exactly, order included, and that
// test imports dmmodel from _test.go — where it does not propagate to anyone consuming
// this package. So a tier added, removed or reordered upstream fails CI here on the same
// PR, which is the property a shared declaration was wanted for; the weight is what got
// dropped.
var ruleSeverities = []string{"CRITICAL", "MAJOR", "MINOR", "WARNING", "INDETERMINATE"}

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
