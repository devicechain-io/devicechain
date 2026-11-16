// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/credbinding"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

var configs = []struct {
	config any
	fields map[string]bool
}{
	{WebhookConfig{}, webhookFields}, {LinkConfig{}, linkFields}, {PagerDutyConfig{}, pagerDutyFields}, {SMTPConfig{}, smtpFields},
}

func TestCredentialConfigFieldsAreClassified(t *testing.T) {
	for _, tc := range configs {
		var walk func(reflect.Type, string)
		walk = func(typ reflect.Type, prefix string) {
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if f.Anonymous {
					walk(f.Type, prefix)
					continue
				}
				name := prefix + strings.Split(f.Tag.Get("json"), ",")[0]
				if _, ok := tc.fields[name]; !ok {
					t.Errorf("unclassified field %s.%s (%s)", typ.Name(), f.Name, name)
				}
				if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct {
					walk(f.Type.Elem(), name+".")
				}
			}
		}
		walk(reflect.TypeOf(tc.config), "")
	}
}

func TestChangingAChannelTypeWithoutItsSecretIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, beforeKind, afterKind, before, after, secret string }{
		{"slack to teams", ChannelTypeSlack, ChannelTypeTeams, `{}`, `{}`, "https://hooks.example/slack"},
		{"slack to webhook", ChannelTypeSlack, ChannelTypeWebhook, `{}`, bearerHookConfig, "https://hooks.example/slack"},
		{"pagerduty to webhook", ChannelTypePagerDuty, ChannelTypeWebhook, `{}`, bearerHookConfig, "routing-key"},
		{"webhook to slack", ChannelTypeWebhook, ChannelTypeSlack, bearerHookConfig, `{}`, "old"},
		{"header name", ChannelTypeWebhook, ChannelTypeWebhook, `{"url":"https://hook.example/a","auth":"header","authHeader":"X-Key"}`, `{"url":"https://hook.example/a","auth":"header","authHeader":"X-Token"}`, "old"},
		{"header scheme", ChannelTypeWebhook, ChannelTypeWebhook, `{"url":"https://hook.example/a","auth":"header","authHeader":"X-Key"}`, `{"url":"https://hook.example/a","auth":"header","authHeader":"X-Key","authScheme":"Token"}`, "old"},
		{"smtp host", ChannelTypeSMTP, ChannelTypeSMTP, `{"host":"mail.example","from":"a@example"}`, `{"host":"other.example","from":"a@example"}`, "old"},
		{"pagerduty region", ChannelTypePagerDuty, ChannelTypePagerDuty, `{}`, `{"region":"eu"}`, "routing-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			createChannel(t, api, ctx, "sink", tc.beforeKind, strPtr(tc.before), strPtr(tc.secret))
			before := channelRow(t, api, ctx, "sink")
			_, err := api.UpdateNotificationChannel(ctx, "sink", &NotificationChannelUpdateRequest{ChannelType: dcgraphql.OptionalStringOf(tc.afterKind), Config: dcgraphql.OptionalStringOf(tc.after)})
			var refusal *credbinding.ResupplyRequiredError
			require.True(t, errors.As(err, &refusal), "%v", err)
			require.Equal(t, credbinding.Code, refusal.Extensions()["code"])
			after := channelRow(t, api, ctx, "sink")
			require.Equal(t, tc.before, string(*after.Config))
			require.Equal(t, tc.beforeKind, after.ChannelType)
			require.True(t, before.UpdatedAt.Equal(after.UpdatedAt))
			require.Equal(t, tc.secret, channelSecretValue(t, api, ctx, "sink"))
		})
	}
}
func TestChannelBindingControls(t *testing.T) {
	for _, mode := range []string{"neutral", "same", "replace", "clear", "anonymous"} {
		t.Run(mode, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			secret := strPtr("old")
			config := bearerHookConfig
			if mode == "anonymous" {
				secret = nil
				config = noneHookConfig
			}
			createChannel(t, api, ctx, "sink", ChannelTypeWebhook, strPtr(config), secret)
			req := &NotificationChannelUpdateRequest{Config: dcgraphql.OptionalStringOf(bearerHookConfig2)}
			want := "old"
			switch mode {
			case "neutral":
				req.Config = dcgraphql.OptionalStringOf(`{"url":"` + credHookURL + `","auth":"bearer","headers":{"X-Notice":"yes"},"linkBaseUrl":"https://console.example"}`)
				req.Name = dcgraphql.OptionalStringOf("new")
				req.Enabled = dcgraphql.OptionalBoolOf(false)
			case "same":
				req.Config = dcgraphql.OptionalStringOf(`{"auth":"bearer","method":"POST","url":"` + credHookURL + `"}`)
			case "replace":
				req.Secret = dcgraphql.OptionalStringOf("new")
				want = "new"
			case "clear":
				req.Secret = dcgraphql.OptionalString{Set: true}
			case "anonymous":
				req.Config = dcgraphql.OptionalStringOf(`{"url":"https://other.example","auth":"none"}`)
				want = ""
			}
			_, err := api.UpdateNotificationChannel(ctx, "sink", req)
			if mode == "clear" {
				require.Error(t, err)
				var refusal *credbinding.ResupplyRequiredError
				require.False(t, errors.As(err, &refusal))
				require.Equal(t, bearerHookConfig, string(*channelRow(t, api, ctx, "sink").Config))
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, want, channelSecretValue(t, api, ctx, "sink"))
		})
	}
}

func TestChannelBindingNeutralFieldsAndDefaults(t *testing.T) {
	for _, tc := range []struct{ kind, before, after string }{
		{ChannelTypeWebhook, bearerHookConfig, `{"url":"` + credHookURL + `","auth":"bearer","method":"POST","headers":{"Host":"ignored.example"},"linkBaseUrl":"https://console.example"}`},
		{ChannelTypeSMTP, `{"host":"mail.example","from":"one@example"}`, `{"host":"mail.example","port":587,"security":"starttls","from":"two@example","username":"user"}`},
		{ChannelTypePagerDuty, `{}`, `{"region":"us","source":"other","linkBaseUrl":"https://console.example"}`},
		{ChannelTypeSlack, `{}`, `{"linkBaseUrl":"https://console.example"}`},
		{ChannelTypeTeams, `{}`, `{"linkBaseUrl":"https://console.example"}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			before := channelBinding("sink", tc.kind, &tc.before)
			after := channelBinding("sink", tc.kind, &tc.after)
			if changed := before.Diff(after); len(changed) != 0 {
				t.Fatalf("neutral edit changed binding: %v", changed)
			}
		})
	}
}
