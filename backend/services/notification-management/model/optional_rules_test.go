// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"
)

// THE RULE-LIST DECODER, DRIVEN DIRECTLY.
//
// 🔴 THESE TESTS EXIST BECAUSE THE WIRE TESTS COULD NOT SEE THIS CODE, AND THAT WAS
// MEASURED RATHER THAN GUESSED. Two mutants — deleting the unknown-key refusal, and making
// a missing `severity` decode to "" instead of an error — survived the ENTIRE suite,
// graphql/partial_update_wire_test.go included. graphql-go validates a variable's value
// against the input type before any packer runs, so it rejects both shapes first, and the
// wire tests were passing on the library's guarantee while appearing to certify this
// decoder's.
//
// That is not a reason to delete the checks here. It is the reason to test them where they
// can be observed. The scenario they exist for is the one the library CANNOT catch: a field
// added to NotificationRuleCreateRequest in the SDL and forgotten in decodeRule. The
// library would then find the field perfectly well defined and pass it through, and a
// decoder that ignored what it did not recognise would drop it silently — the fork's own
// defect, arriving through a door the fork does not cover. Driving UnmarshalGraphQL
// directly is what makes the refusals observable, and what kills both mutants.
//
// The wire tests keep their own value and their own name: they assert that the composite —
// library validation plus this resolver — refuses a malformed rule and writes nothing.

func ruleListFrom(t *testing.T, input any) OptionalNotificationRuleList {
	t.Helper()
	var list OptionalNotificationRuleList
	if err := list.UnmarshalGraphQL(input); err != nil {
		t.Fatalf("UnmarshalGraphQL(%v): %v", input, err)
	}
	return list
}

func mustRejectRuleList(t *testing.T, input any, wantIn string) {
	t.Helper()
	var list OptionalNotificationRuleList
	err := list.UnmarshalGraphQL(input)
	if err == nil {
		t.Fatalf("UnmarshalGraphQL(%v) was accepted", input)
	}
	if !strings.Contains(err.Error(), wantIn) {
		t.Errorf("the refusal does not mention %q: %v", wantIn, err)
	}
}

// A field the rule input does not define is REFUSED rather than dropped. Dropping it would
// produce a rule that is not the one the caller asked for, reported as success — a rule
// missing its recipients delivers to nobody and looks configured.
func TestRuleListRefusesAnUndefinedField(t *testing.T) {
	mustRejectRuleList(t, []any{map[string]any{
		"severity": "MAJOR", "channelToken": "smtp-ops", "recipient": `["typo@example.invalid"]`,
	}}, `"recipient"`)
}

// A required field that is absent, null, or not a String is refused. The severity case is
// the one that matters most: a rule with an empty severity writes, reads back
// byte-for-byte as written, and matches no alarm for the rest of its life.
func TestRuleListRefusesAMalformedRequiredField(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"severity absent":      {"channelToken": "smtp-ops"},
		"severity null":        {"severity": nil, "channelToken": "smtp-ops"},
		"severity not string":  {"severity": 3, "channelToken": "smtp-ops"},
		"channelToken absent":  {"severity": "MAJOR"},
		"channelToken null":    {"severity": "MAJOR", "channelToken": nil},
		"recipients not a str": {"severity": "MAJOR", "channelToken": "smtp-ops", "recipients": 7},
	} {
		t.Run(name, func(t *testing.T) {
			mustRejectRuleList(t, []any{entry}, "rule at index 0")
		})
	}
}

// An entry that is not an input object at all is refused rather than skipped.
func TestRuleListRefusesANonObjectEntry(t *testing.T) {
	mustRejectRuleList(t, []any{"CRITICAL"}, "NotificationRuleCreateRequest at index 0")
}

// THE COUNTERWEIGHT to every refusal above: a well-formed rule list still decodes exactly
// as sent. Strictness bought by refusing everything would be worthless, and would make the
// refusals pass for the wrong reason.
func TestRuleListDecodesAWellFormedList(t *testing.T) {
	list := ruleListFrom(t, []any{
		map[string]any{"severity": "CRITICAL", "channelToken": "smtp-ops",
			"recipients": `["oncall@example.invalid"]`},
		map[string]any{"severity": "*", "channelToken": "webhook-backup"},
	})
	rules, ok := list.Requested()
	if !ok || len(rules) != 2 {
		t.Fatalf("Requested() = %d rules, ok=%v", len(rules), ok)
	}
	if rules[0].Severity != "CRITICAL" || rules[0].ChannelToken != "smtp-ops" ||
		rules[0].Recipients == nil || *rules[0].Recipients != `["oncall@example.invalid"]` {
		t.Errorf("first rule decoded as %+v", rules[0])
	}
	// recipients is nullable, and absent must decode as nil rather than as the empty
	// string — a webhook rule targets the channel's own endpoint and has none.
	if rules[1].Severity != SeverityAny || rules[1].Recipients != nil {
		t.Errorf("second rule decoded as %+v", rules[1])
	}
}

// THE FOUR STATES, read through Requested — the accessor every call site uses, so that no
// caller reads Value and invents a third meaning out of its nil-ness.
func TestRuleListRequestedFoldsTheFourStates(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		// The zero value: UnmarshalGraphQL is never called for a field the request did not
		// mention, which is the whole mechanism.
		rules, ok := (OptionalNotificationRuleList{}).Requested()
		if ok {
			t.Fatalf("an absent rule list reported a request for %d rules", len(rules))
		}
	})
	t.Run("explicit null empties", func(t *testing.T) {
		rules, ok := ruleListFrom(t, nil).Requested()
		if !ok || rules == nil || len(rules) != 0 {
			t.Fatalf("null gave rules=%v ok=%v, want a non-nil empty set", rules, ok)
		}
	})
	t.Run("empty list empties", func(t *testing.T) {
		rules, ok := ruleListFrom(t, []any{}).Requested()
		if !ok || rules == nil || len(rules) != 0 {
			t.Fatalf("[] gave rules=%v ok=%v, want a non-nil empty set", rules, ok)
		}
	})
	t.Run("null and [] agree", func(t *testing.T) {
		fromNull, _ := ruleListFrom(t, nil).Requested()
		fromEmpty, _ := ruleListFrom(t, []any{}).Requested()
		if len(fromNull) != len(fromEmpty) {
			t.Fatalf("null and [] disagree: %d vs %d — they are one request spelled two ways, "+
				"and [] is the one a form actually sends", len(fromNull), len(fromEmpty))
		}
	})
	t.Run("constructors say which state they build", func(t *testing.T) {
		if _, ok := ClearedNotificationRuleList().Requested(); !ok {
			t.Error("ClearedNotificationRuleList built the ABSENT state, which leaves the rules alone")
		}
		if rules, ok := OptionalNotificationRuleListOf(seededRules()).Requested(); !ok || len(rules) != 1 {
			t.Errorf("OptionalNotificationRuleListOf built %d rules, ok=%v", len(rules), ok)
		}
	})
}

// A single object where a list is expected is coerced to a one-entry list, which is what
// the specification's list input coercion says and what graphql-go's own listPacker does
// for a plain slice field. Diverging would make one spelling mean different things on two
// fields of the same schema.
func TestRuleListCoercesASingleObject(t *testing.T) {
	rules, ok := ruleListFrom(t, map[string]any{
		"severity": "MAJOR", "channelToken": "smtp-ops",
	}).Requested()
	if !ok || len(rules) != 1 || rules[0].Severity != "MAJOR" {
		t.Fatalf("a single rule object decoded as %+v (ok=%v)", rules, ok)
	}
}

// The markers that make the type three-state at all. Without Nullable() the schema does
// not build; without the exact schema-type spelling it fails at construction with "can not
// unmarshal". Both are silent-at-write, loud-at-startup failures, and both are easy to
// break with a rename — so they are pinned rather than left to the schema test to discover
// as a panic nobody can read.
func TestRuleListCarriesTheThreeStateMarkers(t *testing.T) {
	var list OptionalNotificationRuleList
	if !list.ImplementsGraphQLType("[NotificationRuleCreateRequest!]") {
		t.Error("the type no longer claims [NotificationRuleCreateRequest!], so the schema will " +
			"not build against a field declared that way")
	}
	// A list whose ENTRIES may be null is a different datatype with no honest reading here,
	// and refusing it means the mistake fails at schema construction rather than arriving
	// as a nil entry at run time.
	if list.ImplementsGraphQLType("[NotificationRuleCreateRequest]") {
		t.Error("the type claims a list of NULLABLE rules, which it has no reading for")
	}
	// A compile-time assertion that Nullable() is declared: without it graphql-go's
	// isNullable is false, the packer refuses a non-pointer nullable field, and the schema
	// fails to build with a message that names neither this type nor the field.
	var _ interface{ Nullable() } = list
}
