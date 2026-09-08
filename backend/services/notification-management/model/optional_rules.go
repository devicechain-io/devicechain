// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// OptionalNotificationRuleList carries a nullable list of rule input objects —
// `[NotificationRuleCreateRequest!]` — through the packer with its present/absent
// distinction intact. It is the policy's half of the platform-wide three-state update
// semantic; the scalar folds it sits beside live in core's graphql package.
//
// # Why it is HERE and not in core
//
// Core carries OptionalStringList, and deliberately no generic list-of-input-objects
// optional. There is exactly one such field on the platform — a policy's rule set — and
// one consumer does not justify a shared type whose element would have to be a type
// parameter or an `any`. If a second appears, this is the shape to lift.
//
// # 🔴 THE THREE STATES COLLAPSE TO TWO, AND THAT IS THE DECISION
//
// The same collapse core documents for OptionalStringList, for the same reason: a list
// has no third stored outcome. "Cleared" and "set to []" are one rule set with nothing
// in it.
//
//	ABSENT    the field was not in the request     -> leave the stored rule set alone
//	NULL      the field was sent as null           -> the policy now has no rules
//	[]        the field was sent as an empty list  -> the policy now has no rules
//	[a, b]    the field was sent with entries      -> the rule set is now exactly [a, b]
//
// A value REPLACES the stored rule set wholesale, which is what the field meant before
// this conversion and is still what a caller who wants whole-replace sends. What changed
// is that whole-replace is no longer the ONLY thing the field can express: absent now
// leaves the rules — and their ids, and their updated_at — untouched, where before every
// update deleted and reinserted them.
//
// # 🔴 THIS DECODER IS THE ONLY THING BETWEEN A RULE AND STORAGE, SO IT REFUSES WHAT IT
// DOES NOT UNDERSTAND
//
// A field whose Go type implements decode.Unmarshaler gets an unmarshalerPacker, which
// hands the RAW value straight here — so graphql-go's StructPacker, and with it the
// unknown-input-field rejection this repo forked the library to get, never runs on a rule.
// The library's variable validation does walk list elements and does reject an entry the
// input type does not define, so the fork's protection is not actually lost; but relying
// on that alone would mean a field ADDED to NotificationRuleCreateRequest in the SDL and
// forgotten here is silently DROPPED — validated as legal, decoded as nothing, written as
// absent. That is the fork's own defect arriving through the back door, so unknown keys
// are refused here too, by this decoder, on both the literal and the variable path.
type OptionalNotificationRuleList struct {
	// Set is true only when the field was PRESENT in the request, whether its value was
	// null, an empty list, or a list with entries.
	Set bool
	// Value is nil when the field was present and explicitly null, and a non-nil empty
	// slice when it was present as []. Both mean the same thing — read it through
	// Requested rather than testing it for nil and inventing a third reading.
	Value []*NotificationRuleCreateRequest
}

// ruleListSchemaType is the schema type this unmarshals, spelled as graphql-go spells it
// (ast.List.String is "[" + element + "]", and a non-null element renders with its "!").
// Accepting only this spelling means a field declared `[NotificationRuleCreateRequest]` —
// a list whose ENTRIES may be null — fails at SCHEMA CONSTRUCTION rather than reaching the
// decoder with a nil entry it has no honest reading for.
const ruleListSchemaType = "[NotificationRuleCreateRequest!]"

func (OptionalNotificationRuleList) ImplementsGraphQLType(name string) bool {
	return name == ruleListSchemaType
}

// Nullable marks this as a type that can accept an explicit null. REQUIRED: for a
// NULLABLE schema field held in a NON-POINTER Go type, graphql-go's makePacker takes its
// isNullable branch, and isNullable tests for decode.Unmarshaler PLUS this marker.
// Without it the schema FAILS TO BUILD, and it is also what routes an explicit null
// through to UnmarshalGraphQL(nil) instead of a zero value.
func (OptionalNotificationRuleList) Nullable() {}

func (o *OptionalNotificationRuleList) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	// A single value where a list is expected is coerced to a one-entry list, which is
	// what the GraphQL specification's list input coercion says and what the library's
	// own listPacker does. Diverging would make `rules: {…}` mean one thing on a plain
	// field and another on this one, under the same schema.
	entries, ok := input.([]any)
	if !ok {
		entries = []any{input}
	}
	// Non-nil even at length zero, so an empty list is not mistaken for a null by
	// anything reading Value directly. Requested treats them the same regardless.
	out := make([]*NotificationRuleCreateRequest, 0, len(entries))
	for i, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("expected a NotificationRuleCreateRequest at index %d of %s, got %T",
				i, ruleListSchemaType, entry)
		}
		rule, err := decodeRule(i, fields)
		if err != nil {
			return err
		}
		out = append(out, rule)
	}
	o.Value = out
	return nil
}

// Requested says what the caller asked the policy's rule set to hold afterwards, and
// whether they asked at all.
//
// ok is false when the field was ABSENT: leave the stored rules exactly as they are,
// which is the state that did not exist before this conversion. When ok is true the
// returned slice is the whole new rule set — non-nil and EMPTY for both an explicit null
// and [], because those are one request spelled two ways.
//
// It exists so no call site reads Value and invents a third reading out of its nil-ness.
func (o OptionalNotificationRuleList) Requested() ([]*NotificationRuleCreateRequest, bool) {
	if !o.Set {
		return nil, false
	}
	if o.Value == nil {
		return []*NotificationRuleCreateRequest{}, true
	}
	return o.Value, true
}

// OptionalNotificationRuleListOf builds a field in the "sent with a value" state, for the
// callers that build requests in Go rather than receiving them off the wire — tests,
// dcctl, the SDKs.
//
// Passing nil or an empty slice builds the EMPTY state, which is the same request
// ClearedNotificationRuleList builds. Say that one when emptying is what you mean.
func OptionalNotificationRuleListOf(v []*NotificationRuleCreateRequest) OptionalNotificationRuleList {
	return OptionalNotificationRuleList{Set: true, Value: v}
}

// ClearedNotificationRuleList builds the "sent as null" state, which leaves the policy
// with NO rules. The zero OptionalNotificationRuleList is the absent state and leaves the
// stored rules alone — the two are easy to confuse in a literal, so say which you mean.
func ClearedNotificationRuleList() OptionalNotificationRuleList {
	return OptionalNotificationRuleList{Set: true}
}

// The rule input's fields, spelled as the SDL spells them. They are named once so the
// decoder and its unknown-key refusal cannot disagree about what is defined.
const (
	ruleFieldSeverity     = "severity"
	ruleFieldChannelToken = "channelToken"
	ruleFieldRecipients   = "recipients"
)

// decodeRule turns one entry of the list into a rule request, refusing anything it does
// not understand rather than dropping it.
//
// Required-ness is enforced here as well as by the library's own validation of the input
// type, and for the same reason the unknown-key check exists: this decoder is what
// actually produces the value that gets written, so a rule missing its severity must fail
// HERE rather than arriving as a rule with an empty severity that matches no alarm for
// the rest of its life.
func decodeRule(index int, fields map[string]any) (*NotificationRuleCreateRequest, error) {
	for name := range fields {
		switch name {
		case ruleFieldSeverity, ruleFieldChannelToken, ruleFieldRecipients:
		default:
			return nil, fmt.Errorf("rule at index %d has a field %q that NotificationRuleCreateRequest "+
				"does not define — it is refused rather than dropped, because a silently ignored "+
				"field produces a rule that is not the one the caller asked for", index, name)
		}
	}
	severity, err := requiredRuleString(index, fields, ruleFieldSeverity)
	if err != nil {
		return nil, err
	}
	channelToken, err := requiredRuleString(index, fields, ruleFieldChannelToken)
	if err != nil {
		return nil, err
	}
	rule := &NotificationRuleCreateRequest{Severity: severity, ChannelToken: channelToken}
	// recipients is a nullable String: absent and null are both "no recipients", which is
	// legal (a webhook rule targets the channel's own endpoint).
	if raw, ok := fields[ruleFieldRecipients]; ok && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("rule at index %d: %s must be a String, got %T",
				index, ruleFieldRecipients, raw)
		}
		rule.Recipients = &s
	}
	return rule, nil
}

// requiredRuleString reads a `String!` field of a rule, refusing an absent, null or
// wrongly-typed one.
func requiredRuleString(index int, fields map[string]any, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || raw == nil {
		return "", fmt.Errorf("rule at index %d has no %s, which is required", index, name)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("rule at index %d: %s must be a String, got %T", index, name, raw)
	}
	return s, nil
}
