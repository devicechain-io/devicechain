// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
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
