// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// THE REGISTRY OF CONVERTED FAMILIES.
//
// Both of this service's update mutations carry the platform-wide partial-update
// semantic, and both are declared here, once, as data. The harness in core drives all of
// its properties against every one.
//
// 🔴 The `seeded` values in each field list are what the family's seed must actually
// write. They are not documentation: SeedPopulatesEveryFieldDistinctly reads the row back
// and fails if the two disagree, because a fixture that does not hold what the table
// claims makes "the update preserved it" unobservable.
//
// There are no exemptions. partial_update_guard_test.go reflects over *Api's Update*
// methods and requires each to be covered by a family here, so an update added tomorrow on
// the full-replace shape fails there whether or not anyone remembers this file.
func partialUpdateFamilies() []putest.Family[*Api] {
	return []putest.Family[*Api]{
		notificationChannelFamily(),
		notificationPolicyFamily(),
	}
}

// ─── shared fixture values ─────────────────────────────────────────────────
//
// Declared once and read by both the seed and the field table, so the "seeded" column
// cannot drift away from what the seed actually wrote.

const (
	channelToken      = "smtp-primary"
	otherChannelToken = "webhook-backup"
	policyToken       = "ops-policy"
)

var channelSeed = struct {
	Name, Description, ChannelType, Config, Secret, Metadata string
	Enabled                                                  bool
}{
	Name:        "Original name",
	Description: "Original description",
	ChannelType: ChannelTypeSMTP,
	Config:      `{"host":"smtp.example.invalid","port":587}`,
	Secret:      "seeded-secret",
	Metadata:    `{"fleet":"north"}`,
	Enabled:     true,
}

var policySeed = struct {
	Name, Description, Metadata                           string
	ThrottleSeconds, EscalateAfterSeconds, MaxEscalations int32
	Enabled                                               bool
}{
	Name:                 "Original name",
	Description:          "Original description",
	Metadata:             `{"fleet":"north"}`,
	ThrottleSeconds:      300,
	EscalateAfterSeconds: 900,
	MaxEscalations:       2,
	Enabled:              true,
}

// The rule sets the policy family seeds and replaces. Two entries in the replacement so
// "the update wrote it" is not satisfied by a one-for-one swap that a length check alone
// would miss, and a DIFFERENT channel in it so the rendering's channel half is exercised
// rather than being constant across both readings.
func seededRules() []*NotificationRuleCreateRequest {
	return []*NotificationRuleCreateRequest{
		{Severity: "CRITICAL", ChannelToken: channelToken, Recipients: strPtr(`["oncall@example.invalid"]`)},
	}
}

func replacementRules() []*NotificationRuleCreateRequest {
	return []*NotificationRuleCreateRequest{
		{Severity: "MAJOR", ChannelToken: otherChannelToken, Recipients: strPtr(`["dayshift@example.invalid"]`)},
		{Severity: SeverityAny, ChannelToken: channelToken},
	}
}

// ─── the delivery-channel family ───────────────────────────────────────────

func notificationChannelFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:       "notificationChannel",
		Token:      channelToken,
		Migrate:    []any{&NotificationChannel{}},
		NewRequest: func() any { return &NotificationChannelUpdateRequest{} },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateNotificationChannel(ctx, token, req.(*NotificationChannelUpdateRequest))
			return err
		},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			t.Helper()
			if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token:       channelToken,
				Name:        strPtr(channelSeed.Name),
				Description: strPtr(channelSeed.Description),
				ChannelType: channelSeed.ChannelType,
				Config:      strPtr(channelSeed.Config),
				Secret:      strPtr(channelSeed.Secret),
				Enabled:     channelSeed.Enabled,
				Metadata:    strPtr(channelSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed channel: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			t.Helper()
			found, err := api.NotificationChannelsByToken(ctx, []string{channelToken})
			row := requireOne(t, "notification channel", found, err)
			return map[string]string{
				"name":        nullStr(row.Name),
				"description": nullStr(row.Description),
				"channelType": row.ChannelType,
				"config":      jsonStr(row.Config),
				"secret":      readChannelSecret(t, api, ctx, row.ID),
				"enabled":     putest.BoolString(row.Enabled),
				"metadata":    jsonStr(row.Metadata),
			}
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", channelSeed.Name, "Renamed",
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", channelSeed.Description, "Rewritten",
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			// A NOT NULL vocabulary column. Not clearable: folding a null to "" would
			// leave a channel that routes nowhere, written successfully. Both values are
			// real catalog entries, because a replacement the catalog refuses would make
			// this property fail for a reason that has nothing to do with the fold.
			putest.RequiredStringField("channelType", channelSeed.ChannelType, ChannelTypeWebhook,
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.ChannelType }),
			putest.OptionalStringField("config", channelSeed.Config, `{"host":"smtp2.example.invalid","port":465}`,
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.Config }),
			// 🔴 THE SECRET IS DECLARED LIKE ANY OTHER CLEARABLE FIELD, WHICH IS THE
			// WHOLE CLAIM OF THIS CONVERSION. It used to be the one field on the request
			// whose null meant PRESERVE while every other field's meant clear, with the
			// empty string as the only spelling of a removal. Declaring it here puts it
			// through the same three properties as `name`: absent preserves, null clears,
			// a value rotates — and the harness fails if any of the three stops holding.
			// It is not a column, so its reading comes from the secret store (see
			// readChannelSecret); NullMarker is "no secret is stored".
			putest.OptionalStringField("secret", channelSeed.Secret, "rotated-secret",
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.Secret }),
			putest.RequiredBoolField("enabled", channelSeed.Enabled,
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			putest.OptionalStringField("metadata", channelSeed.Metadata, `{"fleet":"south"}`,
				func(r *NotificationChannelUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// readChannelSecret renders the channel's delivery secret for the Read contract: the
// stored value, or NullMarker when none is stored.
//
// 🔴 IT RESOLVES THROUGH THE REAL STORE RATHER THAN READING A COLUMN, because there is no
// column — the secret is envelope-encrypted under the channel's immutable id. A read that
// checked store.Exists instead would report "a secret is present" and would therefore be
// unable to tell a ROTATION from a no-op, so the "sent with a value" property would pass
// against an update that ignored the field entirely.
func readChannelSecret(t *testing.T, api *Api, ctx context.Context, id uint) string {
	t.Helper()
	ref, err := ChannelSecretRef(ctx, id)
	if err != nil {
		t.Fatalf("channel secret ref: %v", err)
	}
	value, err := api.Secrets.Resolve(ctx, ref)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return nullMarker
	}
	if err != nil {
		t.Fatalf("resolve channel secret: %v", err)
	}
	return string(value)
}

// ─── the routing-policy family ─────────────────────────────────────────────

func notificationPolicyFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:  "notificationPolicy",
		Token: policyToken,
		// The channel tables come with it: a policy's rules name channels by token and
		// resolve them at write, so a policy family without channels could not seed a
		// single rule.
		Migrate:    []any{&NotificationPolicy{}, &NotificationRule{}, &NotificationChannel{}},
		NewRequest: func() any { return &NotificationPolicyUpdateRequest{} },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateNotificationPolicy(ctx, token, req.(*NotificationPolicyUpdateRequest))
			return err
		},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			t.Helper()
			for _, tok := range []string{channelToken, otherChannelToken} {
				if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
					Token: tok, ChannelType: ChannelTypeSMTP, Enabled: true,
				}); err != nil {
					t.Fatalf("seed channel %q: %v", tok, err)
				}
			}
			if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
				Token:                policyToken,
				Name:                 strPtr(policySeed.Name),
				Description:          strPtr(policySeed.Description),
				ThrottleSeconds:      &policySeed.ThrottleSeconds,
				EscalateAfterSeconds: &policySeed.EscalateAfterSeconds,
				MaxEscalations:       &policySeed.MaxEscalations,
				Enabled:              policySeed.Enabled,
				Rules:                seededRules(),
				Metadata:             strPtr(policySeed.Metadata),
			}); err != nil {
				t.Fatalf("seed policy: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			t.Helper()
			found, err := api.NotificationPoliciesByToken(ctx, []string{policyToken})
			row := requireOne(t, "notification policy", found, err)
			return map[string]string{
				"name":                 nullStr(row.Name),
				"description":          nullStr(row.Description),
				"throttleSeconds":      nullIntStr(row.ThrottleSeconds),
				"escalateAfterSeconds": nullIntStr(row.EscalateAfterSeconds),
				"maxEscalations":       nullIntStr(row.MaxEscalations),
				"enabled":              putest.BoolString(row.Enabled),
				"rules":                renderStoredRules(t, row.Rules),
				"metadata":             jsonStr(row.Metadata),
			}
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", policySeed.Name, "Renamed",
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", policySeed.Description, "Rewritten",
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.OptionalInt32Field("throttleSeconds", policySeed.ThrottleSeconds, 600,
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalInt32 { return &r.ThrottleSeconds }),
			putest.OptionalInt32Field("escalateAfterSeconds", policySeed.EscalateAfterSeconds, 1200,
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalInt32 { return &r.EscalateAfterSeconds }),
			putest.OptionalInt32Field("maxEscalations", policySeed.MaxEscalations, 5,
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalInt32 { return &r.MaxEscalations }),
			putest.RequiredBoolField("enabled", policySeed.Enabled,
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			rulesField(),
			putest.OptionalStringField("metadata", policySeed.Metadata, `{"fleet":"south"}`,
				func(r *NotificationPolicyUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// rulesField declares the policy's rule set to the harness.
//
// 🔴 IT IS BUILT BY HAND RATHER THAN BY A CONSTRUCTOR, because core has none for a list of
// INPUT OBJECTS — OptionalStringListField covers a `[String!]`, and a generic
// list-of-input-objects optional does not exist there for the same reason
// OptionalNotificationRuleList lives in this package: one consumer. The shape is
// deliberately the same as OptionalStringListField's, including the two things that make
// that constructor correct:
//
//   - Kind is Clearable and Cleared reads "[]", not NullMarker. A list has no third stored
//     outcome; null and [] are one request spelled two ways, and both leave a policy with
//     no rules. Declaring it any other way would make the harness assert something the
//     fold does not do.
//   - SetEmpty is supplied, so the harness's EmptyListIsTheSameAsANull property drives the
//     [] spelling as well as the null one. That claim is worth nothing until both are
//     sent, and [] is the spelling a form with nothing selected actually produces.
//
// The seeded rendering is non-empty by construction (seededRules returns one rule), which
// matters for the same reason OptionalStringListField panics on an empty seed: against an
// empty rule set, "the update preserved it" and "it was never set" are the same
// observation.
func rulesField() putest.Field {
	return putest.Field{
		Name:    "rules",
		Seeded:  renderRuleRequests(seededRules()),
		Replace: renderRuleRequests(replacementRules()),
		Cleared: renderRuleRequests(nil),
		Kind:    putest.Clearable,
		Set: func(req any, v string) {
			req.(*NotificationPolicyUpdateRequest).Rules =
				OptionalNotificationRuleListOf(parseRenderedRules(v))
		},
		SetNull: func(req any) {
			req.(*NotificationPolicyUpdateRequest).Rules = ClearedNotificationRuleList()
		},
		SetEmpty: func(req any) {
			req.(*NotificationPolicyUpdateRequest).Rules =
				OptionalNotificationRuleListOf([]*NotificationRuleCreateRequest{})
		},
	}
}

// renderedRule is how one rule reads back into the harness's map[string]string.
//
// 🔴 THE RENDERING IS JSON AND THE LIST IS SORTED, and both halves are load-bearing.
//
// JSON, for the reason core's RenderStringList gives: the obvious rendering — join the
// entries on a separator — makes an EMPTY list and a one-entry list holding blanks read
// alike, and "the caller emptied the rule set" is a state this fold produces on purpose.
// The three readings that must stay distinct are "[]" (emptied), a rule with no
// recipients, and a rule whose recipients are the empty array; none collide here.
//
// Sorted, because the harness compares rendered STRINGS and the database does not promise
// an order for the rule rows. An unsorted rendering would make the property fail on a
// reordering that means nothing — or, worse, pass and fail intermittently. The sort key is
// the rendered rule itself, so it is total: two rules that render identically are the same
// rule as far as anything downstream can tell.
type renderedRule struct {
	Severity     string  `json:"severity"`
	ChannelToken string  `json:"channelToken"`
	Recipients   *string `json:"recipients"`
}

func renderRuleRequests(rules []*NotificationRuleCreateRequest) string {
	out := make([]renderedRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, renderedRule{Severity: r.Severity, ChannelToken: r.ChannelToken, Recipients: r.Recipients})
	}
	return marshalRules(out)
}

// renderStoredRules is the same rendering taken off the ROWS, so the seeded value the
// field table declares and the value the database holds are compared in one alphabet. It
// fails rather than rendering a placeholder when a rule's channel did not preload: a rule
// pointing at nothing is a real state (the delete guard documents it), and papering over
// it here would let the rule set read as unchanged while its routing had gone.
func renderStoredRules(t *testing.T, rules []NotificationRule) string {
	t.Helper()
	out := make([]renderedRule, 0, len(rules))
	for _, r := range rules {
		if r.Channel == nil {
			t.Fatalf("rule %d has no resolved channel, so its rendering would hide where it routes", r.ID)
		}
		rendered := renderedRule{Severity: r.Severity, ChannelToken: r.Channel.Token}
		if r.Recipients != nil {
			s := string(*r.Recipients)
			rendered.Recipients = &s
		}
		out = append(out, rendered)
	}
	return marshalRules(out)
}

func marshalRules(rules []renderedRule) string {
	sort.Slice(rules, func(i, j int) bool { return oneRule(rules[i]) < oneRule(rules[j]) })
	encoded, err := json.Marshal(rules)
	if err != nil {
		panic("rendering a rule list: " + err.Error())
	}
	return string(encoded)
}

func oneRule(r renderedRule) string {
	encoded, err := json.Marshal(r)
	if err != nil {
		panic("rendering a rule: " + err.Error())
	}
	return string(encoded)
}

// parseRenderedRules is marshalRules' inverse, used by the field's Set closure — the
// harness carries every value as a string, so the round trip has to exist. It panics on
// anything the rendering did not produce, because the only caller is the harness itself
// and a silently-empty parse would mean the "sent with a value" state was sending nothing.
func parseRenderedRules(v string) []*NotificationRuleCreateRequest {
	decoded := []renderedRule{}
	if err := json.Unmarshal([]byte(v), &decoded); err != nil {
		panic("not a rendered rule list: " + v + ": " + err.Error())
	}
	out := make([]*NotificationRuleCreateRequest, 0, len(decoded))
	for _, r := range decoded {
		out = append(out, &NotificationRuleCreateRequest{
			Severity: r.Severity, ChannelToken: r.ChannelToken, Recipients: r.Recipients,
		})
	}
	return out
}
