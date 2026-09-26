// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

// A webhook channel's config states how it authenticates (`auth`: none | bearer | header),
// and the channel's secret has to agree with it. These tests pin that the disagreement is
// refused when the channel is SAVED — and, because a refusal that still writes is worse than
// none, every refusal also asserts what the store holds afterwards.

const (
	credHookURL       = "https://hook.example.invalid/a"
	bearerHookConfig  = `{"url":"` + credHookURL + `","auth":"bearer"}`
	noneHookConfig    = `{"url":"` + credHookURL + `","auth":"none"}`
	noAuthHookConfig  = `{"url":"` + credHookURL + `"}`
	bearerHookConfig2 = `{"url":"https://hook.example.invalid/b","auth":"bearer"}`
)

func createChannel(t *testing.T, api *Api, ctx context.Context, token, ctype string, config, secret *string) {
	t.Helper()
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: token, ChannelType: ctype, Config: config, Secret: secret, Enabled: true,
	}); err != nil {
		t.Fatalf("create %q: %v", token, err)
	}
}

func channelRow(t *testing.T, api *Api, ctx context.Context, token string) *NotificationChannel {
	t.Helper()
	rows, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil || len(rows) != 1 {
		t.Fatalf("load %q: rows=%d err=%v", token, len(rows), err)
	}
	return rows[0]
}

func channelCount(t *testing.T, api *Api, ctx context.Context, token string) int {
	t.Helper()
	rows, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("load %q: %v", token, err)
	}
	return len(rows)
}

func wantCredentialRefusal(t *testing.T, err error, mentions string) {
	t.Helper()
	if err == nil {
		t.Fatal("the save was accepted")
	}
	if !strings.Contains(err.Error(), mentions) {
		t.Fatalf("err = %q, want it to mention %q", err, mentions)
	}
}

func TestCreateWebhookDeclaringBearerWithoutSecretIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "hook", ChannelType: ChannelTypeWebhook, Config: strPtr(bearerHookConfig), Enabled: true,
	})
	wantCredentialRefusal(t, err, "no secret")
	if n := channelCount(t, api, ctx, "hook"); n != 0 {
		t.Fatalf("the refused channel was written (%d rows)", n)
	}
}

// "" has always meant "no secret" on this API, so it must not satisfy a declared credential.
func TestCreateBearerWithEmptySecretIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "hook", ChannelType: ChannelTypeWebhook, Config: strPtr(bearerHookConfig),
		Secret: strPtr(""), Enabled: true,
	})
	wantCredentialRefusal(t, err, "no secret")
	if n := channelCount(t, api, ctx, "hook"); n != 0 {
		t.Fatalf("the refused channel was written (%d rows)", n)
	}
}

func TestCreateWebhookWithoutAuthIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "hook", ChannelType: ChannelTypeWebhook, Config: strPtr(noAuthHookConfig),
		Secret: strPtr("s"), Enabled: true,
	})
	wantCredentialRefusal(t, err, "missing auth")
	if n := channelCount(t, api, ctx, "hook"); n != 0 {
		t.Fatalf("the refused channel was written (%d rows)", n)
	}
}

func TestCreateWebhookDeclaringNoneWithASecretIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "hook", ChannelType: ChannelTypeWebhook, Config: strPtr(noneHookConfig),
		Secret: strPtr("s"), Enabled: true,
	})
	wantCredentialRefusal(t, err, "never present")
	if n := channelCount(t, api, ctx, "hook"); n != 0 {
		t.Fatalf("the refused channel was written (%d rows)", n)
	}
}

// The counterweights on create: each mode with the secret it needs is accepted.
func TestCreateWebhookWithAgreeingAuthAndSecretIsAccepted(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "bearer", ChannelTypeWebhook, strPtr(bearerHookConfig), strPtr("s"))
	createChannel(t, api, ctx, "anon", ChannelTypeWebhook, strPtr(noneHookConfig), nil)
	createChannel(t, api, ctx, "header", ChannelTypeWebhook,
		strPtr(`{"url":"`+credHookURL+`","auth":"header","authHeader":"X-API-Key"}`), strPtr("k"))
	if got := channelSecretValue(t, api, ctx, "bearer"); got != "s" {
		t.Fatalf("bearer secret = %q", got)
	}
	if got := channelSecretValue(t, api, ctx, "anon"); got != "" {
		t.Fatalf("anonymous channel holds a secret %q", got)
	}
}

// Clearing the secret of a channel that presents one is the path this whole change began
// with: it was accepted, and the channel went on posting with no credential.
func TestClearingTheSecretOfABearerWebhookIsRefused(t *testing.T) {
	for name, clear := range map[string]dcgraphql.OptionalString{
		"null":         dcgraphql.ClearedString(),
		"empty string": dcgraphql.OptionalStringOf(""),
	} {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(bearerHookConfig), strPtr("s"))

			_, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{Secret: clear})
			wantCredentialRefusal(t, err, "no secret")
			if got := channelSecretValue(t, api, ctx, "hook"); got != "s" {
				t.Fatalf("the refused update still changed the secret to %q", got)
			}
		})
	}
}

// Switching an anonymous channel to bearer without sending a secret: the store holds none.
func TestSwitchingASecretlessWebhookToBearerIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(noneHookConfig), nil)

	_, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalStringOf(bearerHookConfig),
	})
	wantCredentialRefusal(t, err, "no secret")
	if got := string(*channelRow(t, api, ctx, "hook").Config); got != noneHookConfig {
		t.Fatalf("the refused update still changed the config to %q", got)
	}
}

// The inverse, and the only case that needs the store to answer TRUE: a bearer channel
// switched to none while its secret is still stored would keep a credential it never sends.
func TestSwitchingABearerWebhookToNoneWhileASecretIsStoredIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(bearerHookConfig), strPtr("s"))

	_, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalStringOf(noneHookConfig),
	})
	wantCredentialRefusal(t, err, "never present")
	if got := string(*channelRow(t, api, ctx, "hook").Config); got != bearerHookConfig {
		t.Fatalf("the refused update still changed the config to %q", got)
	}

	// ...and doing both in one request — the documented way to go anonymous — is accepted.
	if _, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalStringOf(noneHookConfig),
		Secret: dcgraphql.ClearedString(),
	}); err != nil {
		t.Fatalf("switching to none and clearing the secret together was refused: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "hook"); got != "" {
		t.Fatalf("secret after going anonymous = %q", got)
	}
}

// Flipping the type alone makes the stored SMTP config a webhook config, which has no url
// and no auth.
func TestFlippingAnSMTPChannelToWebhookWithItsSMTPConfigIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "mail", ChannelTypeSMTP, strPtr(`{"host":"smtp.example.invalid"}`), nil)

	_, err := api.UpdateNotificationChannel(ctx, "mail", &NotificationChannelUpdateRequest{
		ChannelType: dcgraphql.OptionalStringOf(ChannelTypeWebhook),
	})
	if err == nil {
		t.Fatal("the flip was accepted")
	}
	if got := channelRow(t, api, ctx, "mail").ChannelType; got != ChannelTypeSMTP {
		t.Fatalf("the refused update still changed the type to %q", got)
	}
}

// A valid edit to a channel that already agrees is not refused: the store is asked, and
// answers yes.
func TestUpdatingTheURLOfABearerWebhookKeepsItsSecret(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(bearerHookConfig), strPtr("s"))

	if _, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalStringOf(bearerHookConfig2),
	}); err != nil {
		t.Fatalf("a url change on a bearer channel with its secret was refused: %v", err)
	}
	if got := string(*channelRow(t, api, ctx, "hook").Config); got != bearerHookConfig2 {
		t.Fatalf("config = %q", got)
	}
}

// legacyWebhook writes a webhook row the way one saved before `auth` existed looks: past
// the create path's check, straight into the table.
func legacyWebhook(t *testing.T, api *Api, ctx context.Context, token string, enabled bool) {
	t.Helper()
	cfg := datatypes.JSON([]byte(noAuthHookConfig))
	row := &NotificationChannel{ChannelType: ChannelTypeWebhook, Config: &cfg, Enabled: enabled}
	row.Token = token
	if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
}

// A channel saved before `auth` existed must still be switchable OFF. Refusing
// `enabled: false` on a broken channel would refuse the one edit that stops it failing.
func TestDisablingAWebhookWithoutAuthIsAccepted(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	legacyWebhook(t, api, ctx, "legacy", true)

	if _, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Name:    dcgraphql.OptionalStringOf("Old hook"),
		Enabled: dcgraphql.OptionalBoolOf(false),
	}); err != nil {
		t.Fatalf("disabling a legacy channel was refused: %v", err)
	}
	if channelRow(t, api, ctx, "legacy").Enabled {
		t.Fatal("the channel is still enabled")
	}
}

// ...but switching one ON is the moment it would start being refused at every alarm, so
// that is refused here instead, where the caller is present to read why.
func TestEnablingAWebhookWithoutAuthIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	legacyWebhook(t, api, ctx, "legacy", false)

	_, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(true),
	})
	wantCredentialRefusal(t, err, "missing auth")
	if channelRow(t, api, ctx, "legacy").Enabled {
		t.Fatal("the refused update still enabled the channel")
	}
}

// Rotating a legacy channel's secret without adding `auth` names the secret, so it is
// judged — and the stored config says nothing about intent.
func TestRotatingTheSecretOfAWebhookWithoutAuthIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	legacyWebhook(t, api, ctx, "legacy", true)

	_, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalStringOf("rotated"),
	})
	wantCredentialRefusal(t, err, "missing auth")
	if got := channelSecretValue(t, api, ctx, "legacy"); got != "" {
		t.Fatalf("the refused update still stored secret %q", got)
	}

	// Adding auth in the same request is accepted.
	if _, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalStringOf(bearerHookConfig),
		Secret: dcgraphql.OptionalStringOf("rotated"),
	}); err != nil {
		t.Fatalf("rotating with auth added was refused: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "legacy"); got != "rotated" {
		t.Fatalf("secret = %q", got)
	}
}
