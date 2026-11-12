// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func breakGlass(ctx context.Context, tx *gorm.DB, profile *DeviceProfile, action, target string, reason *string) error {
	text := ""
	if reason != nil {
		text = strings.TrimSpace(*reason)
	}
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 512 {
		return reviewRefusal("BREAK_GLASS_REASON_REQUIRED", "a break-glass reason of at most 512 characters is required")
	}
	person, err := policyPerson(ctx)
	if err != nil {
		return err
	}
	view, err := reviewPolicyView(tx, profile.ID, reviewNow())
	if err != nil {
		return err
	}
	if err = actiongate.CheckStepUp(person, auth.StepUp{MaxAge: time.Duration(view.Effective.StepUpMaxAgeSeconds) * time.Second}, reviewNow()); err != nil {
		return err
	}
	amr, err := json.Marshal(person.Amr)
	if err != nil {
		return err
	}
	row := &AutomationReviewBreakGlass{DeviceProfileId: profile.ID, Action: action, Target: target, Reason: text, Actor: person.Principal, Amr: datatypes.JSON(amr)}
	if person.MfaTime != nil {
		row.MfaTime = sql.NullTime{Time: *person.MfaTime, Valid: true}
	}
	if tx.Dialector.Name() == "postgres" {
		if err = tx.Raw("SELECT txid_current()").Scan(&row.Txid).Error; err != nil {
			return err
		}
	}
	return tx.Create(row).Error
}

func (api *Api) guardProfileBreakGlass(ctx context.Context, tx *gorm.DB, ids []uint, action, target string, reason *string) error {
	if api.skipReviewGuardForOldPodTest {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id == 0 || i > 0 && ids[i-1] == id {
			continue
		}
		p, err := lockProfile(tx, id)
		if err != nil {
			return err
		}
		if err = applyMaturedPolicyChange(tx, id, reviewNow()); err != nil {
			return err
		}
		view, err := reviewPolicyView(tx, id, reviewNow())
		if err != nil {
			return err
		}
		if view.Effective.Required {
			if err = breakGlass(ctx, tx, p, action, target, reason); err != nil {
				return err
			}
		}
	}
	return nil
}
