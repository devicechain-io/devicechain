// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

// policyWithSeverity is the smallest CREATE that carries one rule at a given severity,
// and updateWithSeverity is its update counterpart. They build the same rule from the
// same two arguments, so the fixture cannot drift between the two paths — which is the
// property the vocabulary check depends on, since buildRules is a single choke point only
// while both paths actually reach it.
func policyWithSeverity(token, severity string) *NotificationPolicyCreateRequest {
	return &NotificationPolicyCreateRequest{
		Token:   token,
		Enabled: true,
		Rules:   []*NotificationRuleCreateRequest{ruleAtSeverity(severity)},
	}
}

func updateWithSeverity(severity string) *NotificationPolicyUpdateRequest {
	return &NotificationPolicyUpdateRequest{
		Rules: OptionalNotificationRuleListOf([]*NotificationRuleCreateRequest{ruleAtSeverity(severity)}),
	}
}

func ruleAtSeverity(severity string) *NotificationRuleCreateRequest {
	return &NotificationRuleCreateRequest{Severity: severity, ChannelToken: "smtp"}
}

// The restated vocabulary equals device-management's declaration exactly.
//
// 🔴 THIS IS THE TEST THAT MAKES THE COPY IN validation.go SAFE, and it is the reason
// the copy is allowed to exist. A tier added, removed or reordered upstream fails here,
// on the same PR that changes it. Order is asserted too: the slice is documented as most
// severe first, the rejection message prints it in that order, and a silently reordered
// list would make that message lie about the platform's ranking.
//
// It lives in _test.go on purpose. dmmodel's package transitively pulls cel-go, antlr,
// NATS and an MQTT client; a test-only import gives this package the drift check without
// handing that graph to everything that consumes it.
func TestTheRuleVocabularyMatchesTheAlarmTiers(t *testing.T) {
	declared := dmmodel.AlarmSeverities()
	if len(declared) == 0 {
		t.Fatal("the alarm vocabulary came back empty, so this test would compare nothing")
	}
	tiers := make([]string, 0, len(declared))
	for _, tier := range declared {
		tiers = append(tiers, tier.String())
	}
	if !slices.Equal(ruleSeverities, tiers) {
		t.Fatalf("the rule vocabulary has drifted from the alarm tiers.\n"+
			"  notification-management: %v\n"+
			"  device-management:       %v\n"+
			"Update ruleSeverities in validation.go to match, in the same order.",
			ruleSeverities, tiers)
	}
}

// Every tier the alarm vocabulary declares is accepted, plus the wildcard.
//
// This is the counterweight to the rejection tests below: refusing bad severities is only
// worth anything while every GOOD one still writes. It ranges over AlarmSeverities() rather
// than a list written here, so a tier added to the vocabulary is exercised the day it lands
// instead of the day someone remembers to extend this fixture.
func TestEveryAlarmTierIsAcceptedAsARuleSeverity(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	mkSeverityChannel(t, api, ctx)

	accepted := []string{SeverityAny}
	for _, tier := range dmmodel.AlarmSeverities() {
		accepted = append(accepted, tier.String())
	}
	if len(accepted) < 2 {
		t.Fatal("the vocabulary came back empty, so this test would pass without testing anything")
	}

	for i, severity := range accepted {
		policy, err := api.CreateNotificationPolicy(ctx, policyWithSeverity(tokenFor(i), severity))
		if err != nil {
			t.Fatalf("severity %q must be accepted: %v", severity, err)
		}
		if len(policy.Rules) != 1 || policy.Rules[0].Severity != severity {
			t.Fatalf("severity %q did not round-trip: %+v", severity, policy.Rules)
		}
	}
}

// A severity outside the vocabulary is refused at the write, on BOTH paths.
//
// The lowercase case is the one that motivated the check: it is what an operator types
// after authoring a detection rule, it used to write successfully, and the rule it produced
// could never match an alarm — silently, because the dispatcher's severity skip logs
// nothing (rightly: a non-matching tier is ordinary on every alarm).
func TestAnUnknownRuleSeverityIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	mkSeverityChannel(t, api, ctx)

	// A policy that exists, so the update path has something to be refused against.
	if _, err := api.CreateNotificationPolicy(ctx, policyWithSeverity("live", SeverityAny)); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	for i, severity := range []string{"critical", "Critical", "", "URGENT", "*!"} {
		t.Run("create "+severity, func(t *testing.T) {
			if _, err := api.CreateNotificationPolicy(ctx, policyWithSeverity("rejected", severity)); err == nil {
				t.Fatalf("severity %q was accepted on create", severity)
			}
			// The refused write must leave nothing behind — the policy row and its rules
			// are one aggregate, so a partially-applied create would be worse than the
			// defect being fixed. Asserted once: it is the same transaction on every
			// iteration, and api_test.go already pins the property for the unknown-channel
			// refusal. Repeating it per severity would measure the same rollback five times.
			if i == 0 {
				if got, _ := api.NotificationPoliciesByToken(ctx, []string{"rejected"}); len(got) != 0 {
					t.Fatalf("refused create left a policy behind for severity %q", severity)
				}
			}
		})

		t.Run("update "+severity, func(t *testing.T) {
			if _, err := api.UpdateNotificationPolicy(ctx, "live", updateWithSeverity(severity)); err == nil {
				t.Fatalf("severity %q was accepted on update", severity)
			}
			// An update that names a rule set replaces it wholesale, so a refusal that
			// still cleared the old rules would leave the policy delivering nothing at all.
			got, err := api.NotificationPoliciesByToken(ctx, []string{"live"})
			if err != nil || len(got) != 1 {
				t.Fatalf("reload after refused update: %v (%d policies)", err, len(got))
			}
			if len(got[0].Rules) != 1 || got[0].Rules[0].Severity != SeverityAny {
				t.Fatalf("refused update disturbed the surviving rule set: %+v", got[0].Rules)
			}
		})
	}
}

// The rejection names the accepted vocabulary, and names it from the same declaration the
// check tests membership in.
//
// An error that merely says "invalid" leaves the operator exactly where the silent skip did.
// Asserting the message is not style-policing here: the message IS the fix's delivery
// mechanism, because there is no console screen and no dcctl command for notification
// policies — a hand-written GraphQL mutation is the only authoring path, so this string is
// the only place an operator can learn the correct casing.
func TestTheSeverityRejectionNamesWhatIsAccepted(t *testing.T) {
	err := validateSeverity("critical")
	if err == nil {
		t.Fatal("expected lowercase critical to be refused")
	}
	msg := err.Error()

	for _, tier := range dmmodel.AlarmSeverities() {
		if !strings.Contains(msg, tier.String()) {
			t.Fatalf("rejection does not name the %s tier: %s", tier, msg)
		}
	}
	if !strings.Contains(msg, SeverityAny) {
		t.Fatalf("rejection does not mention the %q wildcard: %s", SeverityAny, msg)
	}
	// 🔴 Asserted on the hint's own words, not on "CRITICAL" appearing somewhere in the
	// message. The enumeration above already contains every tier name, so a Contains
	// check for "CRITICAL" is satisfied by the list and passes with the hint deleted
	// entirely — the assertion and the thing it claims to test do not overlap.
	const hint = "did you mean"
	if !strings.Contains(msg, hint) || !strings.Contains(msg, `"CRITICAL"`) {
		t.Fatalf("rejection does not suggest the uppercase form: %s", msg)
	}
	// And the counterweight: a value that is not a case variant of a real tier must NOT
	// carry the hint, or it is noise on every rejection rather than a clue on one.
	if plain := validateSeverity("URGENT"); plain == nil || strings.Contains(plain.Error(), hint) {
		t.Fatalf("a value that is not a case variant must not carry a did-you-mean hint: %v", plain)
	}
}

func mkSeverityChannel(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp", ChannelType: ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
}

func tokenFor(i int) string {
	return "policy-" + strconv.Itoa(i)
}
