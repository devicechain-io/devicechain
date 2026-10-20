// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// recordActivation appends one entry to a profile's activation log. It takes the caller's
// transaction and nothing else: the entry must commit or roll back with the move of
// active_version it describes, which is why there is no variant that opens its own.
func recordActivation(tx *gorm.DB, profileId uint, version int32, kind string, at time.Time, by string) error {
	return tx.Create(&DeviceProfileActivation{
		DeviceProfileId: profileId,
		Version:         version,
		Kind:            kind,
		ActivatedAt:     at,
		ActivatedBy:     by,
	}).Error
}

// DeviceProfileActivations reads a profile's activation log, newest first, one keyset page
// at a time. afterId is the ID of the last entry of the previous page (0 starts at the
// newest), taken as the uint64 the cursor parses to so no caller narrows it; limit is clamped to [1, rdb.MaxPageSize]. The second result is the cursor for the
// next page, 0 when this page was the last.
//
// The order is the log's own (ID), which is the order the activations committed in, not
// ActivatedAt: two replicas moving one profile at once can store the earlier instant in the
// later commit (see nextActivationInstant).
func (api *Api) DeviceProfileActivations(ctx context.Context, token string, afterId uint64, limit int) ([]*DeviceProfileActivation, uint64, error) {
	if limit < 1 {
		return nil, 0, &InvalidArgumentError{Msg: fmt.Sprintf("first must be at least 1, got %d", limit)}
	}
	if limit > rdb.MaxPageSize {
		limit = rdb.MaxPageSize
	}
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, 0, err
	}
	q := api.RDB.DB(ctx).Where("device_profile_id = ?", profile.ID)
	if afterId > 0 {
		q = q.Where("id < ?", afterId)
	}
	rows := make([]*DeviceProfileActivation, 0)
	// One extra row says whether another page exists without a second query.
	if err := q.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(rows) > limit {
		rows = rows[:limit]
		next = uint64(rows[limit-1].ID)
	}
	return rows, next, nil
}
