// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// The SIMPLE measurement alarm evaluator was retired by the 6d cutover (ADR-057): DETECT's
// edge-triggered rules now drive alarms through the contributor-set integrator
// (ApplyAlarmContributorEdge, api_alarm_contributor.go). The one piece kept from the old
// evaluator is this shared lookup — both the integrator and the operator ack/clear path key
// their alarm reads on it.

// alarmByOriginatorKey returns the single live alarm for (originator, alarmKey), or
// (nil, nil) when none exists. The partial unique index guarantees at most one live
// row; the default soft-delete scope and the tenant callback confine the lookup to
// live, in-tenant rows.
func (api *Api) alarmByOriginatorKey(ctx context.Context, originatorType string,
	originatorId uint, alarmKey string) (*Alarm, error) {
	var found Alarm
	result := api.RDB.DB(ctx).Where(
		"originator_type = ? AND originator_id = ? AND alarm_key = ?",
		originatorType, originatorId, alarmKey).First(&found)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, result.Error
	}
	return &found, nil
}
