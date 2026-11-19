// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"

	"gorm.io/gorm"
)

// Refresh history as well as live windows. The outbox retains its page if any
// refresh fails, and retries before publishing ownership on its next pass.
func (api *Api) refreshSuppressionOwnership(ctx context.Context, facts []*DeviceOwnershipChangedEvent) error {
	tokens := make([]string, 0, len(facts))
	for _, fact := range facts {
		tokens = append(tokens, fact.DeviceToken)
	}
	ctx = suppressionFactContext(ctx)
	db := api.RDB.DB(ctx)
	var windows []AlarmSuppressionWindow
	query := db
	if db.Dialector.Name() == "postgres" {
		query = query.Where("EXISTS (SELECT 1 FROM jsonb_array_elements(targets) AS target WHERE target->>'type' = 'DEVICE' AND target->>'token' IN ?)", tokens)
	} else {
		query = query.Where("EXISTS (SELECT 1 FROM json_each(targets) AS target WHERE json_extract(target.value, '$.type') = 'DEVICE' AND json_extract(target.value, '$.token') IN ?)", tokens)
	}
	if err := query.Find(&windows).Error; err != nil {
		return err
	}
	for _, w := range windows {
		if err := api.refreshSuppressionWindow(ctx, w); err != nil {
			return err
		}
	}
	return nil
}

func (api *Api) refreshSuppressionWindow(ctx context.Context, w AlarmSuppressionWindow) error {
	// Same bounded retry policy as End/Delete. Re-read AND recompute after a CAS
	// miss: the winning user edit may have changed targets or deleted the window.
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			var fresh AlarmSuppressionWindow
			if err := api.RDB.DB(ctx).First(&fresh, w.ID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil
				}
				return err
			}
			w = fresh
		}
		anchors, err := api.suppressionAnchors(ctx, &w)
		if err != nil {
			return err
		}
		if w.CreatorScoped {
			anchors = intersectSuppressionAnchors(anchors, w.CreatorScope)
		}
		if slices.Equal(anchors, w.ScopeAnchors) {
			return nil
		}
		result := w
		result.ScopeAnchors, result.Version = anchors, w.Version+1
		q := api.RDB.DB(ctx).Model(&w).Where("id = ? AND version = ?", w.ID, w.Version).
			Select("scope_anchors", "version", "updated_at").Updates(&result)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected > 0 {
			// Invalidate immediately: even if a later window fails, this write
			// committed and the audit callback journalled it.
			api.invalidateSuppressionWindows(ctx)
			return nil
		}
	}
	return &SuppressionConflictError{Token: w.Token}
}
