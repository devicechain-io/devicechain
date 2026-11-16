// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

func TestCampaignPlanningLastError(t *testing.T) {
	const internalMessage = "planning failed because of an internal error; it will be retried"
	internal := errors.New("pq: relation x host=db.internal")
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"internal", internal, internalMessage},
		{"refusal", rejected(RejectCampaignTargetInvalid, "group is not a device group"), "group is not a device group"},
		{"wrapped refusal", fmt.Errorf("pq: host=db.internal: %w", rejected(RejectCampaignTargetInvalid, "group is not a device group")), "group is not a device group"},
		{"multibyte", rejected(RejectCampaignTargetInvalid, "%s", strings.Repeat("界", 200)), strings.Repeat("界", 166)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Logger
			log.Logger = zerolog.New(&logs)
			t.Cleanup(func() { log.Logger = previous })
			api := newCampaignTestApi(t)
			ctx := tenantCtx()
			c, err := api.CreateCampaign(ctx, solarRequest("error-test", "lamps"))
			if err != nil {
				t.Fatal(err)
			}
			// Inject a planCampaign failure at its first cycle query, leaving the
			// last_error write and subsequent read connected to the real store.
			if err := api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:plan-error", func(db *gorm.DB) {
				if db.Statement.Table == "campaign_cycles" {
					db.AddError(tc.cause)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := api.planOrRecord(ctx, c, api.campaignClock(), &campaignPlanState{}); !errors.Is(err, tc.cause) {
				t.Fatalf("planning error = %v, want original cause %v", err, tc.cause)
			}
			var stored Campaign
			if err := api.RDB.DB(ctx).First(&stored, c.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !stored.LastError.Valid || stored.LastError.String != tc.want || !utf8.ValidString(stored.LastError.String) || len(stored.LastError.String) > 500 {
				t.Fatalf("last_error = %q, want valid UTF-8 %q within 500 bytes", stored.LastError.String, tc.want)
			}
			for _, detail := range []string{tc.cause.Error(), `"tenant":"acme"`, `"campaign":"error-test"`} {
				if !strings.Contains(logs.String(), detail) {
					t.Fatalf("server log missing %q: %s", detail, logs.String())
				}
			}
		})
	}
}

func TestCampaignStoredConfigurationLastError(t *testing.T) {
	for _, trigger := range []bool{true, false} {
		t.Run(fmt.Sprint(trigger), func(t *testing.T) {
			api := newCampaignTestApi(t)
			ctx := tenantCtx()
			c, err := api.CreateCampaign(ctx, solarRequest("stored-error", "lamps"))
			if err != nil {
				t.Fatal(err)
			}
			want := "stored steps are unreadable"
			if trigger {
				c.Trigger = []byte(`{}`)
				_, cause := DecodeTrigger(c.Trigger)
				want = "stored trigger no longer validates: " + cause.Error()
			} else {
				c.Steps = []byte(`[]`)
			}
			if err := api.planOrRecord(ctx, c, api.campaignClock(), &campaignPlanState{}); err == nil {
				t.Fatal("expected stored configuration failure")
			}
			var stored Campaign
			if err := api.RDB.DB(ctx).First(&stored, c.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !stored.LastError.Valid || stored.LastError.String != want {
				t.Fatalf("last_error = %q, want %q", stored.LastError.String, want)
			}
		})
	}
}
