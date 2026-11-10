// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/httpsink"
)

const (
	slackURL = "https://hooks.slack.com/services/T000/B000/XXXXXXXX"
	teamsURL = "https://prod-00.westus.logic.azure.com:443/workflows/abc?sig=SIG"
)

func TestNativeChannelTypesAreInTheCatalogAndAvailable(t *testing.T) {
	for _, id := range []string{ChannelTypeSlack, ChannelTypeTeams, ChannelTypePagerDuty} {
		if !IsKnownChannelType(id) || !IsNativeChannelType(id) {
			t.Errorf("%q must be a known native type", id)
		}
	}
	if IsNativeChannelType(ChannelTypeWebhook) || IsNativeChannelType(ChannelTypeSMTP) || IsNativeChannelType("") {
		t.Error("webhook, smtp and empty are not native types")
	}
	for _, ct := range SupportedChannelTypes {
		if !ct.Available {
			t.Errorf("%q ships an adapter and must say so", ct.Id)
		}
	}
}

func TestCreateNativeChannelSealsTheSecretAndNeverReadsItBack(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, tc := range []struct{ typ, secret string }{
		{ChannelTypeSlack, slackURL}, {ChannelTypeTeams, teamsURL}, {ChannelTypePagerDuty, "R0UTINGKEY0123456789ABCDEF012345"},
	} {
		createChannel(t, api, ctx, "c-"+tc.typ, tc.typ, nil, strPtr(tc.secret))
		if got := channelSecretValue(t, api, ctx, "c-"+tc.typ); got != tc.secret {
			t.Errorf("%s: stored secret = %q", tc.typ, got)
		}
		row := channelRow(t, api, ctx, "c-"+tc.typ)
		if row.Config != nil && strings.Contains(string(*row.Config), tc.secret) {
			t.Errorf("%s: the secret reached the config column", tc.typ)
		}
	}
}

func TestCreateNativeChannelWithoutASecretIsRefusedAndWritesNothing(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, typ := range []string{ChannelTypeSlack, ChannelTypeTeams, ChannelTypePagerDuty} {
		for _, secret := range []*string{nil, strPtr("")} {
			_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
				Token: "n-" + typ, ChannelType: typ, Secret: secret, Enabled: true,
			})
			if !errors.Is(err, httpsink.ErrMissingCredential) {
				t.Fatalf("%s secret=%v: err = %v, want ErrMissingCredential", typ, secret, err)
			}
			if n := channelCount(t, api, ctx, "n-"+typ); n != 0 {
				t.Fatalf("%s: the refused channel was written", typ)
			}
		}
	}
}

func TestCreateNativeChannelRefusesAMalformedSecretWithoutEchoingIt(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	bad := []struct{ typ, secret string }{
		{ChannelTypeSlack, "http://hooks.slack.com/services/PLAINTEXT"},
		{ChannelTypeSlack, "not a url SECRETBITS"},
		{ChannelTypeTeams, "https://user:pw@example.com/SECRETBITS"},
		{ChannelTypeTeams, "https:///nohost"},
		{ChannelTypePagerDuty, "has a space SECRETBITS"},
		{ChannelTypePagerDuty, "ctrl\x01char"},
		{ChannelTypeSlack, "https://x.example/" + strings.Repeat("a", maxSecretLen)},
	}
	for _, b := range bad {
		_, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: "bad", ChannelType: b.typ, Secret: strPtr(b.secret), Enabled: true,
		})
		if err == nil {
			t.Errorf("%s accepted secret %q", b.typ, b.secret)
			continue
		}
		if strings.Contains(err.Error(), "SECRETBITS") || strings.Contains(err.Error(), "PLAINTEXT") {
			t.Errorf("the error echoed the secret: %v", err)
		}
	}
	if n := channelCount(t, api, ctx, "bad"); n != 0 {
		t.Fatal("a refused channel was written")
	}
}

func TestNativeConfigIsStrict(t *testing.T) {
	for _, tc := range []struct{ typ, cfg string }{
		{ChannelTypeSlack, `{"linkBase":"https://c"}`},
		{ChannelTypeTeams, `{"unknown":true}`},
		{ChannelTypePagerDuty, `{"region":"ap"}`},
		{ChannelTypePagerDuty, `{"region":"us","extra":1}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"javascript:alert(1)"}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"https://console.example?x=1"}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"https://console.example#alarm"}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"https://console.example?"}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"https://console.example#"}`},
		{ChannelTypeSlack, `{"linkBaseUrl":"https://u:p@c.example"}`},
		{ChannelTypeSlack, `{} {}`},
		{ChannelTypePagerDuty, `{"source":"` + strings.Repeat("s", 256) + `"}`},
	} {
		if err := validateNativeChannel("c", tc.typ, strPtr(tc.cfg), strPtr("https://x.example/h"), true); err == nil {
			t.Errorf("%s accepted config %s", tc.typ, tc.cfg)
		}
	}
	// Accepted shapes, with defaults and normalisation applied.
	for _, region := range []string{"us", "eu"} {
		if _, err := ParsePagerDutyConfig("c", strPtr(`{"region":"`+region+`"}`)); err != nil {
			t.Fatalf("region %s refused: %v", region, err)
		}
	}
	pd, err := ParsePagerDutyConfig("c", nil)
	if err != nil || pd.Region != "us" || pd.Source != "devicechain" {
		t.Fatalf("defaults = %+v, %v", pd, err)
	}
	l, err := ParseLinkConfig("c", ChannelTypeSlack, strPtr(`{"linkBaseUrl":"https://console.example.com///"}`))
	if err != nil || l.LinkBaseURL != "https://console.example.com" {
		t.Fatalf("link = %+v, %v", l, err)
	}
	if l, err := ParseLinkConfig("c", ChannelTypeSlack, strPtr(`{"linkBaseUrl":"http://console.example"}`)); err != nil || l.LinkBaseURL != "http://console.example" {
		t.Fatalf("http base: %+v, %v", l, err)
	}
	if _, err := ParseLinkConfig("c", ChannelTypeSlack, strPtr("  ")); err != nil {
		t.Fatalf("blank config is no config: %v", err)
	}
}

func TestUpdateNativeChannelJudgesTheSecretAndConfigItIsGiven(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "sl", ChannelTypeSlack, nil, strPtr(slackURL))

	// Clearing the secret of a native channel is refused: it would deliver nowhere.
	_, err := api.UpdateNotificationChannel(ctx, "sl", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalString{Set: true, Value: nil},
	})
	if !errors.Is(err, httpsink.ErrMissingCredential) {
		t.Fatalf("clear: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "sl"); got != slackURL {
		t.Fatalf("a refused update changed the secret to %q", got)
	}
	// A malformed replacement is refused and the old secret stands.
	_, err = api.UpdateNotificationChannel(ctx, "sl", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalString{Set: true, Value: strPtr("http://insecure")},
	})
	if err == nil {
		t.Fatal("an http webhook URL was accepted")
	}
	if got := channelSecretValue(t, api, ctx, "sl"); got != slackURL {
		t.Fatalf("secret = %q", got)
	}
	// A bad config is refused.
	_, err = api.UpdateNotificationChannel(ctx, "sl", &NotificationChannelUpdateRequest{
		Config: dcgraphql.OptionalString{Set: true, Value: strPtr(`{"bogus":1}`)},
	})
	if err == nil {
		t.Fatal("bad config accepted")
	}
	// Rotation works, and a rename-style edit that touches neither keeps the secret.
	if _, err := api.UpdateNotificationChannel(ctx, "sl", &NotificationChannelUpdateRequest{
		Secret: dcgraphql.OptionalString{Set: true, Value: strPtr(teamsURL)},
	}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := api.UpdateNotificationChannel(ctx, "sl", &NotificationChannelUpdateRequest{
		Name: dcgraphql.OptionalString{Set: true, Value: strPtr("renamed")},
	}); err != nil {
		t.Fatalf("name-only edit: %v", err)
	}
	if got := channelSecretValue(t, api, ctx, "sl"); got != teamsURL {
		t.Fatalf("secret = %q", got)
	}
}

func TestUpdateChannelTypeToNativeNeedsASecret(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "w", ChannelTypeWebhook, strPtr(noneHookConfig), nil)
	_, err := api.UpdateNotificationChannel(ctx, "w", &NotificationChannelUpdateRequest{
		ChannelType: dcgraphql.OptionalString{Set: true, Value: strPtr(ChannelTypePagerDuty)},
		Config:      dcgraphql.OptionalString{Set: true, Value: nil},
	})
	if !errors.Is(err, httpsink.ErrMissingCredential) {
		t.Fatalf("err = %v", err)
	}
	if got := channelRow(t, api, ctx, "w").ChannelType; got != ChannelTypeWebhook {
		t.Fatalf("the refused update changed the type to %q", got)
	}
}

func TestDisablingABrokenNativeChannelIsNotBlocked(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	createChannel(t, api, ctx, "pd", ChannelTypePagerDuty, nil, strPtr("KEY"))
	// Only enabling is judged, as for a webhook: switching a channel OFF must always work.
	if _, err := api.UpdateNotificationChannel(ctx, "pd", &NotificationChannelUpdateRequest{
		Enabled: dcgraphql.OptionalBool{Set: true, Value: boolPtr(false)},
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }
