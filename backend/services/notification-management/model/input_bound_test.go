// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

func TestChannelConfigOverTheBoundIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	big := `{"auth":"none","url":"https://example.com/` + strings.Repeat("x", rdb.MaxJSONInputBytes) + `"}`
	_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "big-config", ChannelType: ChannelTypeWebhook, Config: &big, Enabled: true,
	})
	if _, ok := limit.As(err); !ok {
		t.Fatalf("want a limit refusal, got %v", err)
	}
	if got, _ := api.NotificationChannelsByToken(ctx, []string{"big-config"}); len(got) != 0 {
		t.Fatal("a refused channel left a row behind")
	}
}

func TestRecipientsOverTheBoundAreRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "smtp", ChannelType: ChannelTypeSMTP, Config: strPtr(`{"host":"h","port":587}`),
		Secret: strPtr("s"), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	list := func(n int) *string {
		items := make([]string, n)
		for i := range items {
			items[i] = `"a` + strings.Repeat("x", 3) + `@example.com"`
		}
		s := "[" + strings.Join(items, ",") + "]"
		return &s
	}
	mk := func(token string, n int) error {
		_, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
			Token: token, Enabled: true,
			Rules: []*NotificationRuleCreateRequest{{Severity: SeverityAny, ChannelToken: "smtp", Recipients: list(n)}},
		})
		return err
	}
	if err := mk("at-bound", MaxRecipientsPerRule); err != nil {
		t.Fatalf("a rule at the bound must be accepted: %v", err)
	}
	if _, ok := limit.As(mk("over-bound", MaxRecipientsPerRule+1)); !ok {
		t.Fatal("a rule over the bound must be a limit refusal")
	}
}

func policyRuleCountFixture(t *testing.T) (*Api, *NotificationChannel) {
	t.Helper()
	api := newTestApi(t)
	channel, err := api.CreateNotificationChannel(tenantCtx("A"), &NotificationChannelCreateRequest{
		Token: "webhook", ChannelType: ChannelTypeWebhook,
		Config: strPtr(anonymousWebhookConfig), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return api, channel
}

func policyRuleCountRequests(n int) []*NotificationRuleCreateRequest {
	rules := make([]*NotificationRuleCreateRequest, n)
	for i := range rules {
		rules[i] = &NotificationRuleCreateRequest{Severity: SeverityAny, ChannelToken: "webhook"}
	}
	return rules
}

func countPolicyRuleChannelSelects(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	count := new(int)
	if err := db.Callback().Query().Before("gorm:query").Register("test:policy_rule_count_channels", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_channels" {
			(*count)++
		}
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPolicyRuleCountCreate(t *testing.T) {
	for _, n := range []int{100, 101} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			api, _ := policyRuleCountFixture(t)
			ctx := tenantCtx("A")
			db := api.RDB.DB(ctx)
			selects := countPolicyRuleChannelSelects(t, db)
			_, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
				Token: "bounded", Enabled: true, Rules: policyRuleCountRequests(n),
			})
			wantPolicies, wantRules := int64(1), int64(n)
			if n > 100 {
				if _, ok := limit.As(err); !ok {
					t.Fatalf("want limit refusal, got %v", err)
				}
				if *selects != 0 {
					t.Fatalf("oversized create selected channels %d times", *selects)
				}
				wantPolicies, wantRules = 0, 0
			} else if err != nil {
				t.Fatal(err)
			} else if *selects != n {
				t.Fatalf("accepted create selected channels %d times, want %d", *selects, n)
			}
			var policies, rules int64
			if err := db.Model(&NotificationPolicy{}).Count(&policies).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&NotificationRule{}).Count(&rules).Error; err != nil {
				t.Fatal(err)
			}
			if policies != wantPolicies || rules != wantRules {
				t.Fatalf("stored policies/rules = %d/%d, want %d/%d", policies, rules, wantPolicies, wantRules)
			}
		})
	}
}

func TestPolicyRuleCountUpdateRefusesBeforeChanges(t *testing.T) {
	api, _ := policyRuleCountFixture(t)
	ctx := tenantCtx("A")
	before, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "bounded", Name: strPtr("old name"), Enabled: true, Rules: policyRuleCountRequests(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	selects := countPolicyRuleChannelSelects(t, api.RDB.DB(ctx))
	request := &NotificationPolicyUpdateRequest{
		Name:  dcgraphql.OptionalStringOf("new name"),
		Rules: OptionalNotificationRuleListOf(policyRuleCountRequests(101)),
	}
	// Both unguarded and stale guarded updates must report the input bound first.
	for _, expected := range []*string{nil, strPtr("2000-01-01T00:00:00Z")} {
		if _, err := api.UpdateNotificationPolicy(ctx, "bounded", request, expected); err == nil {
			t.Fatal("oversized replacement succeeded")
		} else if _, ok := limit.As(err); !ok {
			t.Fatalf("want limit refusal, got %v", err)
		}
	}
	if *selects != 0 {
		t.Fatalf("oversized updates selected channels %d times", *selects)
	}
	stored, err := api.NotificationPoliciesByToken(ctx, []string{"bounded"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("reload: rows=%d err=%v", len(stored), err)
	}
	after := stored[0]
	if after.Name != before.Name || !slices.Equal(ruleIds(after), ruleIds(before)) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("refused update changed name, rule IDs or UpdatedAt: before=%+v after=%+v", before, after)
	}
}

func TestPolicyRuleCountOmittedRulesAllowsStoredOversize(t *testing.T) {
	api, channel := policyRuleCountFixture(t)
	ctx := tenantCtx("A")
	policy, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{Token: "legacy", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	rules := make([]NotificationRule, 101)
	for i := range rules {
		rules[i] = NotificationRule{PolicyId: policy.ID, Severity: SeverityAny, ChannelId: channel.ID}
	}
	if err := api.RDB.DB(ctx).Create(&rules).Error; err != nil {
		t.Fatal(err)
	}
	before := &NotificationPolicy{Rules: rules}
	after, err := api.UpdateNotificationPolicy(ctx, "legacy", &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("renamed"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name.String != "renamed" || len(after.Rules) != 101 || !slices.Equal(ruleIds(after), ruleIds(before)) {
		t.Fatal("update omitting rules did not preserve the oversized stored rule set")
	}
}

func TestPolicyRuleCountBuildRulesRefusesBeforeChannelLookup(t *testing.T) {
	api, _ := policyRuleCountFixture(t)
	db := api.RDB.DB(tenantCtx("A"))
	selects := countPolicyRuleChannelSelects(t, db)
	if _, err := api.buildRules(db, 1, policyRuleCountRequests(101)); err == nil {
		t.Fatal("oversized rule set succeeded")
	} else if _, ok := limit.As(err); !ok {
		t.Fatalf("want limit refusal, got %v", err)
	}
	if *selects != 0 {
		t.Fatalf("oversized rule set selected channels %d times", *selects)
	}
}
