// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// notificationHistoryRead filters before counting/paging. Background work has no
// caller and remains unaffected. AuthorizeScoped recognizes scoped location grants.
func notificationHistoryRead(db *gorm.DB, attempts bool) *gorm.DB {
	ctx := db.Statement.Context
	if ctx == nil || core.IsSystemContext(ctx) {
		return db
	}
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return db
	}
	if _, err := auth.AuthorizeScoped(ctx, auth.LocationRead); err == nil {
		return db
	}
	if !attempts {
		return db.Where("position_derived IS FALSE")
	}
	// A missing/deleted state is unknown too. Match tenant explicitly: the ledger
	// and the state are tenant scoped, including inside this correlated subquery.
	return db.Where(clause.Expr{
		SQL: "EXISTS (SELECT 1 FROM ? AS visible WHERE visible.alarm_token = ? AND visible.tenant_id = ? AND visible.deleted_at IS NULL AND visible.position_derived IS FALSE)",
		Vars: []any{clause.Table{Name: db.NamingStrategy.TableName("NotificationState")},
			clause.Column{Table: clause.CurrentTable, Name: "alarm_token"},
			clause.Column{Table: clause.CurrentTable, Name: "tenant_id"}},
	})
}

func (s *NotificationState) applyPosition(verdict *bool) bool {
	if s.PositionDerived != nil && *s.PositionDerived {
		return false
	}
	if verdict == nil {
		changed := s.PositionDerived != nil
		s.PositionDerived = nil
		return changed
	}
	changed := s.PositionDerived == nil || *s.PositionDerived != *verdict
	v := *verdict
	s.PositionDerived = &v
	return changed
}

// RecordAlarmPosition classifies existing history even when this event is
// throttled or has no matching policy. Never create a notification just to classify
// it. New rows receive the verdict in their own insert. An old producer's absent
// verdict invalidates false, since it may have added a geofence contributor. The
// predicate makes true sticky atomically under concurrent delivery.
func (api *Api) RecordAlarmPosition(ctx context.Context, alarm string, verdict *bool) error {
	var value any
	if verdict != nil {
		value = *verdict
	}
	return api.RDB.DB(ctx).Model(&NotificationState{}).
		Where("alarm_token = ? AND position_derived IS NOT TRUE", alarm).
		Update("position_derived", value).Error
}
