// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"gorm.io/gorm"
)

// NotificationStatesByAlarmToken loads the notification state for one or more
// raised alarms by their alarm tokens. Returns an empty slice when none exist
// (the dispatcher has not yet notified about the alarm).
func (api *Api) NotificationStatesByAlarmToken(ctx context.Context, alarmTokens []string) ([]*NotificationState, error) {
	found := make([]*NotificationState, 0)
	result := api.RDB.DB(ctx).Find(&found, "alarm_token in ?", alarmTokens)
	return found, result.Error
}

// NotificationStates searches per-alarm notification state by criteria. This is
// the operator-facing "who was paged, and has it been acknowledged/escalated"
// surface the escalation scheduler (N.D) and the console read.
func (api *Api) NotificationStates(ctx context.Context,
	criteria NotificationStateSearchCriteria) (*NotificationStateSearchResults, error) {
	results := make([]NotificationState, 0)
	db, pag := api.RDB.ListOf(ctx, &NotificationState{}, func(result *gorm.DB) *gorm.DB {
		if criteria.AlarmKey != nil {
			result = result.Where("alarm_key = ?", *criteria.AlarmKey)
		}
		if criteria.Severity != nil {
			result = result.Where("severity = ?", *criteria.Severity)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &NotificationStateSearchResults{Results: results, Pagination: pag}, nil
}
