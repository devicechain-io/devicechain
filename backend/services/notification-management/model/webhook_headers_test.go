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

// A webhook channel's custom headers are grammar-checked when the channel is SAVED, by the
// same httpsink.ValidateHeader / IsReservedHeader the rules path uses, so a malformed header
// is refused at the request that wrote it rather than failing every dispatch.

func hookWithHeaders(headersJSON string) string {
	return `{"url":"` + credHookURL + `","auth":"none","headers":` + headersJSON + `}`
}

// malformedHeaderShapes are the shapes the shared validator refuses; each must name the field.
var malformedHeaderShapes = map[string]string{
	"invalid name character":           `{"Bad Name":"v"}`,
	"empty name":                       `{"":"v"}`,
	"CR in value":                      `{"X-Ok":"a\rb"}`,
	"LF in value":                      `{"X-Ok":"a\nb"}`,
	"NUL in value":                     `{"X-Ok":"a\u0000b"}`,
	"reserved Authorization":           `{"Authorization":"Bearer x"}`,
	"reserved X-DC-*":                  `{"x-dc-tenant":"victim"}`,
	"duplicate after canonicalization": `{"x-custom":"1","X-Custom":"2"}`,
}

func wantHeaderRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the save was accepted")
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Fatalf("err = %q, want it to address the headers field", err)
	}
}

func TestCreateWebhookWithMalformedHeaderIsRefused(t *testing.T) {
	for name, headers := range malformedHeaderShapes {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			cfg := hookWithHeaders(headers)
			_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: "hook", ChannelType: ChannelTypeWebhook, Config: &cfg, Enabled: true,
			})
			wantHeaderRefusal(t, err)
			if n := channelCount(t, api, ctx, "hook"); n != 0 {
				t.Fatalf("the refused create still wrote %d row(s)", n)
			}
		})
	}
}

func TestUpdateWebhookToMalformedHeaderIsRefused(t *testing.T) {
	for name, headers := range malformedHeaderShapes {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(noneHookConfig), nil)

			_, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
				Config: dcgraphql.OptionalStringOf(hookWithHeaders(headers)),
			})
			wantHeaderRefusal(t, err)
			if got := string(*channelRow(t, api, ctx, "hook").Config); got != noneHookConfig {
				t.Fatalf("the refused update still changed the config to %q", got)
			}
		})
	}
}

// The partial-update path: an update that does not touch config but ENABLES the channel
// re-judges the stored config, so a bad-header row is refused the moment it would go live.
func TestEnablingAWebhookWithAStoredMalformedHeaderIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	legacyHeaderWebhook(t, api, ctx, "legacy", false)

	_, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(true),
	})
	wantHeaderRefusal(t, err)
}

// A row saved before this check existed is not migrated: it still loads, can be renamed and
// disabled (edits that do not touch what is judged), and fails at dispatch as before.
func TestStoredMalformedHeaderRowStillLoadsAndCanBeDisabled(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	legacyHeaderWebhook(t, api, ctx, "legacy", true)

	if _, err := api.UpdateNotificationChannel(ctx, "legacy", &NotificationChannelUpdateRequest{
		Name:    dcgraphql.OptionalStringOf("Old hook"),
		Enabled: dcgraphql.OptionalBoolOf(false),
	}); err != nil {
		t.Fatalf("renaming/disabling a legacy channel was refused: %v", err)
	}
	// The delivery-side parser is untouched by the save-time check: it does not crash on the row.
	row := channelRow(t, api, ctx, "legacy")
	if _, err := ParseWebhookConfig("legacy", dcgraphql.MetadataStr(row.Config)); err != nil {
		t.Fatalf("delivery-side parse of a stored row changed: %v", err)
	}
}

func legacyHeaderWebhook(t *testing.T, api *Api, ctx context.Context, token string, enabled bool) {
	t.Helper()
	cfg := datatypes.JSON([]byte(hookWithHeaders(`{"Bad Name":"v"}`)))
	row := &NotificationChannel{ChannelType: ChannelTypeWebhook, Config: &cfg, Enabled: enabled}
	row.Token = token
	if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
}

func TestWebhookWithValidHeadersIsAccepted(t *testing.T) {
	for name, headers := range map[string]string{
		"none":              `{}`,
		"custom":            `{"X-Custom":"ok"}`,
		"token punctuation": `{"X_Api.Key-1":"v"}`,
		"tab in value":      `{"X-Ok":"a\tb"}`,
		"empty value":       `{"X-Ok":""}`,
		"two distinct":      `{"X-A":"1","X-B":"2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			createChannel(t, api, ctx, "hook", ChannelTypeWebhook, strPtr(hookWithHeaders(headers)), nil)
			if _, err := api.UpdateNotificationChannel(ctx, "hook", &NotificationChannelUpdateRequest{
				Config: dcgraphql.OptionalStringOf(hookWithHeaders(headers)),
			}); err != nil {
				t.Fatalf("update refused: %v", err)
			}
		})
	}
}
