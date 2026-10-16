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

// ClaimNotificationDeferral takes a deferral by DELETING it, and reports
// whether this caller won. It is a compare-and-swap on (id, updated_at): two replicas
// releasing the same row race on the delete and exactly one sees a row affected — and a
// row a newer event REPLACED since it was read is not claimed at all (its updated_at moved),
// so the next pass releases the newer event instead of this caller sending the stale one.
//
// It is the DROP path's claim. A release that SENDS leases the row instead (see
// LeaseNotificationDeferral), because deleting the only page about an alarm before it is
// sent loses it if the sender dies.
func (api *Api) ClaimNotificationDeferral(ctx context.Context, d *NotificationDeferral) (bool, error) {
	// occurred_time is in the guard as well as updated_at: a replacement by a newer event
	// always moves it, so a stale copy loses even where the store's clock is too coarse for
	// updated_at to have moved (SQLite keeps milliseconds).
	result := api.RDB.DB(ctx).Where("updated_at = ? AND occurred_time = ?", d.UpdatedAt, d.OccurredTime).
		Delete(&NotificationDeferral{ID: d.ID})
	return result.RowsAffected == 1, result.Error
}

// LeaseNotificationDeferral takes a deferral for release WITHOUT deleting it: its release
// time moves to `until`, a lease long enough to send it, and the caller reports whether it
// won. It is a compare-and-swap on (id, updated_at), so two replicas releasing the same
// row race on the update and exactly one wins, and a row a newer event REPLACED since it
// was read is not leased at all.
//
// 🔴 WHY A LEASE AND NOT A DELETE. A held notification is the ONLY page about its alarm:
// nothing went out when it was raised. Deleting it before sending (the escalation claim's
// shape) means a pod that dies between the claim and the send — a rolling deploy at the
// end of the night is exactly when the morning backlog releases — loses that page with no
// trace at all, and so does a failed write-back after a failed send. With a lease the row
// survives until the send is known: a release that never finishes comes due again when
// the lease runs out and is sent then. The cost is the other direction, a page sent twice
// if the pod dies between the send and the finish, and for an alerting service a
// duplicate page is the failure to prefer.
func (api *Api) LeaseNotificationDeferral(ctx context.Context, d *NotificationDeferral, until time.Time) (bool, error) {
	// release_at is in the guard as well as updated_at: the lease always moves it (from due
	// to the end of the lease), so a second lease from the same read loses even where the
	// store's clock is too coarse for updated_at to have moved (SQLite keeps milliseconds).
	result := api.RDB.DB(ctx).Model(&NotificationDeferral{ID: d.ID}).
		Where("updated_at = ? AND release_at = ?", d.UpdatedAt, d.ReleaseAt).
		Update("release_at", until)
	return result.RowsAffected == 1, result.Error
}

// FinishNotificationDeferral removes a leased deferral once its release is decided (sent,
// or given up on). It is keyed on the held event's occurred time, not on updated_at: the
// lease itself moved updated_at, and a NEWER event that replaced the row while it was
// being sent must stay held, while a redelivery of the same or an older event (which the
// upsert never lets replace it) changes nothing that matters.
func (api *Api) FinishNotificationDeferral(ctx context.Context, d *NotificationDeferral) error {
	return api.RDB.DB(ctx).Where("occurred_time = ?", d.OccurredTime).
		Delete(&NotificationDeferral{ID: d.ID}).Error
}

// RetryNotificationDeferral puts a leased deferral whose release reached no channel back
// for another try at `at`, with its attempt count raised. Keyed like
// FinishNotificationDeferral: a newer event that replaced the row is left as it is.
func (api *Api) RetryNotificationDeferral(ctx context.Context, d *NotificationDeferral, at time.Time) error {
	return api.RDB.DB(ctx).Model(&NotificationDeferral{ID: d.ID}).
		Where("occurred_time = ?", d.OccurredTime).
		Updates(map[string]any{"release_at": at, "attempts": d.Attempts + 1}).Error
}

// ResolveNotificationDeferrals deletes what is held for an alarm that was acknowledged or
// cleared at `at`, for every policy, and reports how many rows went. Only events that
// occurred at or before `at` are resolved by it; a newer one is a later cycle of the same
// alarm token and stays held.
//
// This is what makes "acknowledged or cleared while held" hold across alarm cycles. The
// notification state's terminal stamps record the FIRST acknowledgement and clear of the
// alarm token and never move, so for a second cycle they are stamps from before its held
// event and cannot show that it was resolved.
func (api *Api) ResolveNotificationDeferrals(ctx context.Context, alarmToken string, at time.Time) (int64, error) {
	result := api.RDB.DB(ctx).Where("alarm_token = ? AND occurred_time <= ?", alarmToken, at).
		Delete(&NotificationDeferral{})
	return result.RowsAffected, result.Error
}

// SupersedeNotificationDeferrals deletes what the given policies hold for an alarm about an
// event that occurred at or before `at`, because a page about the alarm as of `at` has just
// gone out through those policies. Releasing the held one afterwards would page twice, the
// second time with the older event.
func (api *Api) SupersedeNotificationDeferrals(ctx context.Context, alarmToken string, policyIDs []uint,
	at time.Time) (int64, error) {
	if len(policyIDs) == 0 {
		return 0, nil
	}
	result := api.RDB.DB(ctx).
		Where("alarm_token = ? AND policy_id IN ? AND occurred_time <= ?", alarmToken, policyIDs, at).
		Delete(&NotificationDeferral{})
	return result.RowsAffected, result.Error
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
