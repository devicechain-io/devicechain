// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PositionOracle returns only tokens device-management currently shows to a
// principal without location:read. Absent tokens (including deleted alarms) are
// withheld; errors must never be interpreted as visibility.
type PositionOracle interface {
	VisibleWithoutLocation(context.Context, []string) (map[string]bool, error)
}

func requiresPositionVerification(ctx context.Context) bool {
	if ctx == nil || core.IsSystemContext(ctx) {
		return false
	}
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return false
	}
	// Authorize, not AuthorizeScoped: this service admits no scoped caller (every resolver
	// gates with Authorize), so a scoped member never reaches these reads. Should one ever
	// arrive, Authorize refuses it and its history is verified like any caller without
	// location:read: fail closed, rather than opting the module in to scoped callers.
	return auth.Authorize(ctx, auth.LocationRead) != nil
}

// notificationHistoryRead applies the local cache as a counting/paging pre-filter.
// A cached false is never authority: device-management is verified at read time.
func notificationHistoryRead(db *gorm.DB, attempts bool) *gorm.DB {
	if !requiresPositionVerification(db.Statement.Context) {
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

// maxHistoryVerificationTokens bounds verification across the full filtered set.
// It is a variable so tests can exercise the boundary with a small fixture.
var maxHistoryVerificationTokens = 10000

// verifiedHistory retries once after invalidating refused cache entries. tokens
// must cover the full filtered set feeding the count, even for an empty page.
func verifiedHistory[T any](ctx context.Context, api *Api, query func(*gorm.DB) (T, error), tokens func(*gorm.DB, T) ([]string, error)) (T, error) {
	var zero T
	if !requiresPositionVerification(ctx) {
		return query(api.RDB.DB(ctx))
	}
	if api.PositionOracle == nil {
		return zero, fmt.Errorf("notification history classification cannot be verified: %w", auth.ErrForbidden)
	}
	for pass := 0; pass < 2; pass++ {
		var result T
		var allTokens []string
		base := api.RDB.DB(ctx)
		var options []*sql.TxOptions
		if base.Dialector.Name() == "postgres" {
			options = append(options, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		}
		// COUNT, page rows and verification tokens must describe one candidate
		// snapshot, including tokens counted but absent from an out-of-range page.
		// Each retry starts a fresh snapshot after the cache repair below.
		err := base.Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = query(tx)
			if err != nil {
				return err
			}
			allTokens, err = tokens(tx, result)
			return err
		}, options...)
		if err != nil {
			return zero, err
		}
		seen := map[string]bool{}
		distinct := []string{}
		for _, token := range allTokens {
			if !seen[token] {
				seen[token] = true
				distinct = append(distinct, token)
			}
		}
		if len(distinct) > maxHistoryVerificationTokens {
			return zero, fmt.Errorf("notification history is too broad to verify; narrow the filter: %w", auth.ErrForbidden)
		}
		refused := []string{}
		for start := 0; start < len(distinct); start += rdb.MaxLookupKeys {
			end := min(start+rdb.MaxLookupKeys, len(distinct))
			visible, err := api.PositionOracle.VisibleWithoutLocation(ctx, distinct[start:end])
			if err != nil {
				return zero, fmt.Errorf("verify notification history classification: %w", err)
			}
			for _, token := range distinct[start:end] {
				if !visible[token] {
					refused = append(refused, token)
				}
			}
		}
		if len(refused) == 0 {
			return result, nil
		}
		if pass == 1 {
			return zero, fmt.Errorf("notification history classification cannot be verified after cache downgrade: %w", auth.ErrForbidden)
		}
		// The read transaction has closed; repair on the manager's write handle.
		// Remove caller scope claims for this internal cache repair, retaining the
		// tenant context so RDB's tenant callback still bounds every update.
		for _, token := range refused {
			if err := api.RDB.DB(auth.WithClaims(ctx, nil)).Model(&NotificationState{}).
				Where("alarm_token = ? AND position_derived IS FALSE", token).
				Update("position_derived", nil).Error; err != nil {
				return zero, err
			}
		}
	}
	return zero, fmt.Errorf("notification history classification cannot be verified: %w", auth.ErrForbidden)
}
