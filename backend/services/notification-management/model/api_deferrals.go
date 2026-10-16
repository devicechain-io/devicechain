// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"gorm.io/gorm/clause"
)

// DeferNotifications records notifications that quiet hours held, one row per (alarm,
// policy). An existing row for the same alarm and policy is REPLACED by the newer event —
// newer by occurred time, so an out-of-order redelivery of an older event cannot overwrite
// a later one — and its release time moves to the new one either way, since that is the
// end of the stretch the policy is quiet for NOW.
//
// It is idempotent, which is what lets the dispatcher write it BEFORE delivering anything
// else for the alarm: a failure here returns an error with nothing sent, and the
// redelivery writes the same rows again.
func (api *Api) DeferNotifications(ctx context.Context, deferrals []*NotificationDeferral) error {
	if len(deferrals) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "alarm_token"}, {Name: "policy_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"release_at": clause.Column{Table: "excluded", Name: "release_at"},
			"updated_at": clause.Column{Table: "excluded", Name: "updated_at"},
			"attempts":   clause.Column{Table: "excluded", Name: "attempts"},
			// The event columns move together, and only forward in event time.
			"alarm_key":     newerOf("alarm_key"),
			"severity":      newerOf("severity"),
			"event":         newerOf("event"),
			"occurred_time": newerOf("occurred_time"),
		}),
	}).Create(&deferrals).Error
}

// newerOf is the upsert value of a held event's column: the incoming one when the incoming
// event is at least as recent as the stored one, otherwise the stored one.
func newerOf(column string) clause.Expr {
	return clause.Expr{SQL: "CASE WHEN excluded.occurred_time >= notification_deferrals.occurred_time " +
		"THEN excluded." + column + " ELSE notification_deferrals." + column + " END"}
}

// DueNotificationDeferrals loads the caller tenant's deferrals whose quiet stretch has
// ended at `now`, earliest first, at most limit of them. The scheduler releases them.
func (api *Api) DueNotificationDeferrals(ctx context.Context, now time.Time, limit int) ([]*NotificationDeferral, error) {
	found := make([]*NotificationDeferral, 0)
	err := api.RDB.DB(ctx).Where("release_at <= ?", now).
		Order("release_at ASC, id ASC").Limit(limit).Find(&found).Error
	return found, err
}

// ClaimNotificationDeferral takes a deferral for release by DELETING it, and reports
// whether this caller won. It is a compare-and-swap on (id, updated_at): two replicas
// releasing the same row race on the delete and exactly one sees a row affected — and a
// row a newer event REPLACED since it was read is not claimed at all (its updated_at moved),
// so the next pass releases the newer event instead of this caller sending the stale one.
//
// Claim-before-send has the escalation claim's tradeoff: a pod that dies between the claim
// and the send loses that page. A release that reaches no channel is written back through
// DeferNotifications with its attempt count raised, which covers every failure except the
// process dying.
func (api *Api) ClaimNotificationDeferral(ctx context.Context, d *NotificationDeferral) (bool, error) {
	result := api.RDB.DB(ctx).Where("updated_at = ?", d.UpdatedAt).
		Delete(&NotificationDeferral{ID: d.ID})
	return result.RowsAffected == 1, result.Error
}

// RescheduleNotificationDeferral moves a deferral's release to `at`: the policy is quiet
// for it again (its quiet hours were edited, or a new stretch began before the scheduler
// reached it). The (id, updated_at) guard leaves a row a newer event replaced alone; that
// event's write already set the release it needs.
func (api *Api) RescheduleNotificationDeferral(ctx context.Context, d *NotificationDeferral, at time.Time) error {
	// The model carries the row's id so the audit journal records which row moved.
	return api.RDB.DB(ctx).Model(&NotificationDeferral{ID: d.ID}).
		Where("updated_at = ?", d.UpdatedAt).
		Update("release_at", at).Error
}

// DistinctDeferralTenants lists the tenants holding any deferred notification. It runs
// under a system context, like DistinctStateTenants: a tenant whose only alarm so far was
// held by quiet hours has no notification state yet, and must still be visited to be
// released.
func (api *Api) DistinctDeferralTenants(ctx context.Context) ([]string, error) {
	var tenants []string
	err := api.RDB.DB(ctx).Model(&NotificationDeferral{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// NotificationDeferralsByAlarmToken loads the caller tenant's deferrals for the given
// alarms.
func (api *Api) NotificationDeferralsByAlarmToken(ctx context.Context, alarmTokens []string) ([]*NotificationDeferral, error) {
	found := make([]*NotificationDeferral, 0)
	err := api.RDB.DB(ctx).Where("alarm_token IN ?", alarmTokens).Order("id ASC").Find(&found).Error
	return found, err
}
