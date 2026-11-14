// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ruleaction"
	"gorm.io/datatypes"
)

func authorizeRuleActions(ctx context.Context, definition []byte) error {
	// Scan errors require both authorities, never an empty authority set.
	kinds, _ := ruleaction.Kinds(definition)
	for _, authority := range ruleaction.Required(kinds) {
		if err := auth.Authorize(ctx, authority); err != nil {
			return err
		}
	}
	return nil
}

func authorizeSnapshotActions(ctx context.Context, raw datatypes.JSON) error {
	snap, err := parseProfileSnapshot(raw)
	if err != nil {
		return err
	}
	for _, rule := range snap.Rules {
		if rule == nil {
			return auth.ErrForbidden
		}
		if rule.Enabled {
			if err := authorizeRuleActions(ctx, rule.Definition); err != nil {
				return err
			}
		}
	}
	return nil
}
