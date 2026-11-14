// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/limit"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type RulesCeilingResolver interface {
	Resolve(context.Context, string) (int, error)
}

func (api *Api) rulesCeiling(ctx context.Context) (int, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return 0, err
	}
	if api.RulesCeiling == nil {
		return governance.DefaultRulesPerTenant, nil
	}
	n, err := api.RulesCeiling.Resolve(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("cannot read the tenant's rule ceiling: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid rule ceiling: %d", n)
	}
	return n, nil
}

// checkActiveRuleCount runs inside the activation transaction, serialized by a
// tenant lock shared by publish and rollback. Count frozen active snapshots, never
// drafts or superseded versions. Replacing the candidate's own version costs no
// extra slots. Existing over-budget tenants may reduce their active rule count.
func checkActiveRuleCount(tx *gorm.DB, profileID uint, candidate datatypes.JSON, ceiling int) error {
	rules, err := enabledSnapshotRulesStrict(candidate)
	if err != nil {
		return err
	}
	total := len(rules)
	var profiles []DeviceProfile
	if err := tx.Where("active_version IS NOT NULL").Find(&profiles).Error; err != nil {
		return err
	}
	previous := 0
	for _, p := range profiles {
		var v DeviceProfileVersion
		if err := tx.Where("device_profile_id = ? AND version = ?", p.ID, p.ActiveVersion.Int32).First(&v).Error; err != nil {
			return err
		}
		active, err := enabledSnapshotRulesStrict(v.Snapshot)
		if err != nil {
			return err
		}
		if p.ID == profileID {
			previous = len(active)
		} else {
			total += len(active)
		}
	}
	if total > ceiling && len(rules) > previous {
		return limit.Exceeded("active detection rules (rulesPerTenant)", total, ceiling)
	}
	return nil
}
