// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// AlarmAnchor narrows an alarm read to the alarms stamped with one anchor: a customer, an
// area or an asset the alarm's device was tracked to WHEN THE ALARM CYCLE BEGAN. It is the
// server-side form of "the alarms of this area", so a widget bound to an area makes one read
// instead of one per device.
type AlarmAnchor struct {
	Type  string
	Token string
}

// IsAlarmAnchorType reports whether kind is an anchor alarms can be filtered by.
func IsAlarmAnchorType(kind string) bool {
	switch kind {
	case string(entity.TypeCustomer), string(entity.TypeArea), string(entity.TypeAsset):
		return true
	}
	return false
}

// ResolveAlarmAnchor validates an anchor filter for the caller in ctx and returns the stamp
// ("area:floor-4") the alarms carry for it. The ONE place the rule lives, so the queries and
// the live stream cannot drift:
//
//   - a type that is not customer, area or asset is an error, never an empty answer;
//   - for a caller limited to part of the tenant, a CUSTOMER anchor must be one its scope
//     names, decided from the claims alone, because an alarm may be stamped with two
//     customers and filtering by one outside the scope would name the co-owner. An existing
//     and a non-existent customer are refused alike (ErrForbidden);
//   - an AREA or ASSET anchor needs no pre-check: the caller's own scope predicate is
//     applied beside it, so the answer is the intersection and an area none of the caller's
//     devices raised in is simply empty.
func ResolveAlarmAnchor(ctx context.Context, a AlarmAnchor) (string, error) {
	if !IsAlarmAnchorType(a.Type) {
		return "", fmt.Errorf("anchor type %q is not one alarms are filtered by: use customer, area or asset", a.Type)
	}
	if a.Token == "" {
		return "", fmt.Errorf("anchor token is required")
	}
	stamp := a.Type + ":" + a.Token
	// Fails closed like the resolvers' isScopedCaller: a context with no claims at all is a
	// wiring fault and is treated as a scoped caller naming nothing.
	_, hasClaims := auth.ClaimsFromContext(ctx)
	scope, scoped := auth.ScopeFromContext(ctx)
	if (scoped || !hasClaims) && a.Type == auth.AnchorKindCustomer {
		named := false
		for _, held := range scope.Anchors() {
			if held == stamp {
				named = true
				break
			}
		}
		if !named {
			return "", auth.ErrForbidden
		}
	}
	return stamp, nil
}

// alarmAnchorFilter is the anchor facet of an alarm search or count: nil is no facet.
func alarmAnchorFilter(ctx context.Context, a *AlarmAnchor) (func(*gorm.DB) *gorm.DB, error) {
	if a == nil {
		return func(db *gorm.DB) *gorm.DB { return db }, nil
	}
	stamp, err := ResolveAlarmAnchor(ctx, *a)
	if err != nil {
		return nil, err
	}
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("alarms.scope_anchors @> ?::text[]", rdb.Anchors{stamp})
	}, nil
}
