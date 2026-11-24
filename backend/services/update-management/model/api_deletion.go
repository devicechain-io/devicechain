// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// DeviceDeleted retires pending targets and voids assignments covered by this deletion, including retained
// facts replayed after a token was reused. It is safe to redeliver and to reorder.
func (api *Api) DeviceDeleted(ctx context.Context, token string, incarnation uint64, deletedTime time.Time) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return errors.New("a device deletion requires a tenant")
	}
	api.disabled.forget(tenant + "/" + token)
	if token == "" || deletedTime.IsZero() {
		return nil
	}
	ctx = rdb.WithAuditActor(ctx, "device deleted")
	// What the deleted device reported running goes with it.
	if err := api.forgetInstalled(ctx, token, incarnation, deletedTime); err != nil {
		return err
	}
	// A legacy target is covered by the rollout's freeze time, never the target row's creation
	// time. Explicit tenant scoping also fences the subquery. NULL StartedAt cannot prove coverage.
	started := api.RDB.DB(ctx).Model(&UpdateRollout{}).Select("id").
		Where("tenant_id = ? AND started_at <= ?", tenant, deletedTime)
	pending := api.RDB.DB(ctx).Model(&UpdateRolloutTarget{}).
		Where("device_token = ? AND status = ?", token, TargetPending)
	if incarnation > 0 {
		pending = pending.Where("(device_incarnation <= ? OR (device_incarnation IS NULL AND rollout_id IN (?)))", incarnation, started)
	} else {
		pending = pending.Where("device_incarnation IS NULL AND rollout_id IN (?)", started)
	}
	// Reuse DEVICE_NOT_FOUND: the frozen device no longer exists. CAS keeps redelivery
	// idempotent and leaves already-dispatched or terminal targets to their existing path.
	if err := pending.Updates(map[string]any{"status": TargetSkipped, "skip_code": CodeDeviceNotFound,
		"skip_detail": "the frozen device was deleted", "updated_at": api.now()}).Error; err != nil {
		return err
	}
	q := api.RDB.DB(ctx).Where("device_token = ?", token)
	if incarnation > 0 {
		q = q.Where("(device_incarnation <= ? OR (device_incarnation IS NULL AND created_at <= ?))", incarnation, deletedTime)
	} else {
		q = q.Where("device_incarnation IS NULL AND created_at <= ?", deletedTime)
	}
	var assignments []UpdateAssignment
	if err := q.Find(&assignments).Error; err != nil {
		return err
	}
	for _, asg := range assignments {
		row, err := api.attemptOf(ctx, asg.ID)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("deleted device assignment has no attempt")
		}
		if ota.State(row.State).Terminal() && row.State != string(ota.StateCancelled) {
			continue
		}
		if !ota.State(row.State).Terminal() && !asg.SupersededAt.Valid {
			view, err := api.cancelAssignment(ctx, asg)
			var refusal *RefusalError
			if err != nil && !(errors.As(err, &refusal) && (refusal.Code == CodeBeyondRecall || refusal.Code == CodeAttemptTerminal)) {
				return err
			}
			if err != nil && refusal.Code == CodeAttemptTerminal {
				latest, readErr := api.attemptOf(ctx, asg.ID)
				if readErr != nil {
					return readErr
				}
				if latest == nil {
					return errors.New("deleted device assignment has no attempt")
				}
				if ota.State(latest.State).Terminal() && latest.State != string(ota.StateCancelled) {
					continue
				}
			}
			// A deleted device cannot confirm a cancellation request. Retire its current
			// assignment as a replacement would; never claim its installer was stopped.
			if err != nil || !ota.State(view.Attempt.State).Terminal() {
				if err := api.RDB.DB(ctx).Model(&UpdateAssignment{ID: asg.ID, Token: asg.Token}).
					Where("id = ? AND superseded_at IS NULL", asg.ID).Update("superseded_at", api.now()).Error; err != nil {
					return err
				}
			}
		}
		// Use the existing target vocabulary, releasing the rollout slot even when an
		// unreachable installer is beyond recall or cannot acknowledge cancellation.
		if err := api.RDB.DB(ctx).Model(&UpdateRolloutTarget{}).
			Where("assignment_id = ? AND status = ?", asg.ID, TargetDispatched).
			Updates(map[string]any{"status": TargetCancelled, "updated_at": api.now()}).Error; err != nil {
			return err
		}
	}
	return nil
}
