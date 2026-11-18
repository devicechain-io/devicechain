// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// AlarmLifecycle is the service plane's state-only view of an alarm.
type AlarmLifecycle struct {
	Token string
	State string
}

// AlarmLifecycles bypasses position withholding for service-plane reconciliation only.
// The resolver requires a service token: tenant callers must not probe withheld alarms.
// Removing claims preserves tenant scoping, and selecting only token and state exposes
// no position fields, originator, classification or timestamps.
func (api *Api) AlarmLifecycles(ctx context.Context, tokens []string) ([]AlarmLifecycle, error) {
	rows := make([]*Alarm, 0)
	if err := rdb.FindByKeys(api.RDB.DB(auth.WithClaims(ctx, nil)).Select("token", "state"), &rows, "token", tokens); err != nil {
		return nil, err
	}
	result := make([]AlarmLifecycle, 0, len(rows))
	for _, row := range rows {
		result = append(result, AlarmLifecycle{Token: row.Token, State: row.State})
	}
	return result, nil
}
