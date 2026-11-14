// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/limit"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type RulesCeilingResolver interface {
	Resolve(context.Context, string) (*int, error)
}

func (api *Api) rulesCeiling(ctx context.Context) (*int, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	if api.RulesCeiling == nil {
		return nil, nil
	}
	n, err := api.RulesCeiling.Resolve(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("cannot read the tenant's rule ceiling: %w", err)
	}
	if n != nil && *n < 0 {
		return nil, fmt.Errorf("invalid rule ceiling: %d", *n)
	}
	return n, nil
}

// checkActiveRuleCount runs inside the activation transaction, serialized by a
// tenant lock shared by publish and rollback. Count frozen active snapshots, never
// drafts or superseded versions. Replacing the candidate's own version costs no
// extra slots. Existing over-budget tenants may reduce their active rule count.
func (api *Api) checkActiveRuleCount(ctx context.Context, tx *gorm.DB, profileID uint, candidate datatypes.JSON) error {
	rules, err := enabledSnapshotRulesStrict(candidate)
	if err != nil {
		return err
	}
	previous := 0
	var profile DeviceProfile
	if err := tx.First(&profile, profileID).Error; err != nil {
		return err
	}
	if profile.ActiveVersion.Valid {
		var v DeviceProfileVersion
		if err := tx.Where("device_profile_id = ? AND version = ?", profileID, profile.ActiveVersion.Int32).First(&v).Error; err != nil {
			return err
		}
		active, err := enabledSnapshotRulesStrict(v.Snapshot)
		if err != nil {
			return err
		}
		previous = len(active)
	}
	// Emergency reverts and unchanged counts never depend on user-management.
	if len(rules) <= previous {
		return nil
	}
	ceiling, err := api.rulesCeiling(ctx)
	if err != nil {
		return err
	}
	if ceiling == nil {
		return nil
	}
	total := len(rules)
	var profiles []DeviceProfile
	if err := tx.Where("active_version IS NOT NULL").Find(&profiles).Error; err != nil {
		return err
	}
	for _, p := range profiles {
		if p.ID == profileID {
			continue
		}
		var v DeviceProfileVersion
		if err := tx.Where("device_profile_id = ? AND version = ?", p.ID, p.ActiveVersion.Int32).First(&v).Error; err != nil {
			return err
		}
		active, err := enabledSnapshotRulesStrict(v.Snapshot)
		if err != nil {
			return err
		}
		total += len(active)
	}
	if total > *ceiling {
		return limit.Exceeded("active detection rules (rulesPerTenant)", total, *ceiling)
	}
	return nil
}
