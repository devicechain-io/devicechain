// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/schedule"
)

func TestCampaignTriggerTrailingData(t *testing.T) {
	valid := solarRequest("trigger", "lamps").Trigger
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{"closing object", "}"},
		{"closing array", "]"},
		{"second JSON value", " {}"},
		{"trailing garbage", " garbage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := valid + tc.suffix
			t.Run("decode", func(t *testing.T) {
				trigger, err := DecodeTrigger([]byte(doc))
				if !errors.Is(err, schedule.ErrInvalid) || trigger != nil {
					t.Fatalf("DecodeTrigger = %v, %v; want nil, invalid schedule", trigger, err)
				}
			})
			t.Run("create", func(t *testing.T) {
				api := newCampaignTestApi(t)
				ctx := tenantCtx()
				req := solarRequest("trigger", "lamps")
				req.Trigger = doc
				campaign, err := api.CreateCampaign(ctx, req)
				var rej *EnqueueRejected
				if !errors.As(err, &rej) || rej.Code != RejectCampaignTriggerInvalid || campaign != nil {
					t.Errorf("CreateCampaign = %v, %v; want nil, %s", campaign, err, RejectCampaignTriggerInvalid)
				}
				var rows int64
				if err := api.RDB.DB(ctx).Unscoped().Model(&Campaign{}).Count(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if rows != 0 {
					t.Fatalf("malformed trigger wrote %d campaign rows", rows)
				}
			})
		})
	}
}

func TestDecodeTriggerWhitespace(t *testing.T) {
	for _, suffix := range []string{"", " \t\r\n"} {
		t.Run(suffix, func(t *testing.T) {
			api := newCampaignTestApi(t)
			req := solarRequest("trigger", "lamps")
			req.Trigger += suffix
			trigger, err := DecodeTrigger([]byte(req.Trigger))
			if err != nil || trigger == nil {
				t.Fatalf("DecodeTrigger = %v, %v; want valid trigger", trigger, err)
			}
			if _, err := api.CreateCampaign(tenantCtx(), req); err != nil {
				t.Fatalf("CreateCampaign: %v", err)
			}
		})
	}
}
