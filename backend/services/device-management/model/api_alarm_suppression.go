// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// CreateAlarmSuppressionWindow validates and authorizes the complete window.
// Server-owned fields on the input are ignored.
func (api *Api) CreateAlarmSuppressionWindow(ctx context.Context, input *AlarmSuppressionWindow) (*AlarmSuppressionWindow, error) {
	if err := ValidateAlarmSuppression(input); err != nil {
		return nil, err
	}
	w := *input
	w.Model = gorm.Model{}
	w.TenantId = ""
	w.EndedAt, w.EndedBy = nil, sql.NullString{}
	w.Version = 1
	if err := api.authorizeSuppressionWrite(ctx, nil, &w); err != nil {
		return nil, err
	}
	claims, _ := auth.ClaimsFromContext(ctx)
	w.CreatedBy = sql.NullString{String: claims.Subject, Valid: claims.Subject != ""}
	w.CreatorScope = nil
	scope, scoped := auth.ScopeFromContext(ctx)
	w.CreatorScoped = scoped
	if scoped {
		w.CreatorScope = append(w.CreatorScope, scope.Anchors()...)
	}
	anchors, err := api.suppressionAnchors(ctx, &w)
	if err != nil {
		return nil, err
	}
	w.ScopeAnchors = anchors
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkSuppressionCapacity(ctx, tx, time.Now().UTC()); err != nil {
			return err
		}
		return tx.Create(&w).Error
	})
	if err != nil {
		return nil, err
	}
	api.invalidateSuppressionWindows(ctx)
	return &w, nil
}

// checkSuppressionCapacity must run in the transaction that adds a live window.
func checkSuppressionCapacity(ctx context.Context, tx *gorm.DB, at time.Time) error {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return err
	}
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tenant+":alarm-suppression").Error; err != nil {
			return err
		}
	}
	var count int64
	// Count the entire tenant, including windows invisible to a scoped creator.
	if err := tx.WithContext(suppressionFactContext(ctx)).Model(&AlarmSuppressionWindow{}).Scopes(suppressionLiveAt(at)).Count(&count).Error; err != nil {
		return err
	}
	if count >= 500 {
		return suppressionInvalid("at most 500 live windows per tenant")
	}
	return nil
}

func suppressionWindowLiveAt(w *AlarmSuppressionWindow, at time.Time) bool {
	return (w.EndedAt == nil || w.EndedAt.After(at)) &&
		(w.EndTime == nil || w.EndTime.After(at)) &&
		(w.ValidUntil == nil || w.ValidUntil.After(at))
}

func (api *Api) AlarmSuppressionWindowByToken(ctx context.Context, token string) (*AlarmSuppressionWindow, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	var w AlarmSuppressionWindow
	if err := api.RDB.DB(ctx).Where("token = ?", token).First(&w).Error; err != nil {
		return nil, err
	}
	visible, err := api.suppressionTargetsInScope(ctx, &w)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, gorm.ErrRecordNotFound
	}
	return &w, nil
}

func (api *Api) AlarmSuppressionWindows(ctx context.Context) ([]AlarmSuppressionWindow, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	var windows []AlarmSuppressionWindow
	err := api.RDB.DB(ctx).Order("token").Find(&windows).Error
	if err != nil {
		return nil, err
	}
	visible := make([]AlarmSuppressionWindow, 0, len(windows))
	for _, w := range windows {
		allowed, err := api.suppressionTargetsInScope(ctx, &w)
		if err != nil {
			return nil, err
		}
		if allowed {
			visible = append(visible, w)
		}
	}
	return visible, nil
}

// SuppressionConflictError means the window changed after it was read; callers may retry.
type SuppressionConflictError struct{ Token string }

func (e *SuppressionConflictError) Error() string {
	return fmt.Sprintf("suppression window %q changed concurrently", e.Token)
}

// Preserve the window-specific error while exposing the shared stale-write refusal.
func (e *SuppressionConflictError) Unwrap() error {
	return rdb.NewStaleWriteError("suppression window")
}
func (e *SuppressionConflictError) Extensions() map[string]any {
	return rdb.NewStaleWriteError("suppression window").Extensions()
}

// mutateSuppression authorizes without holding a transaction or pooled connection.
// The version predicate makes the eventual write atomic against concurrent changes.
func (api *Api) mutateSuppression(ctx context.Context, token string, change func(*AlarmSuppressionWindow) error, ending, deleting bool) (*AlarmSuppressionWindow, error) {
	var existing AlarmSuppressionWindow
	if err := api.RDB.DB(ctx).Where("token = ?", token).First(&existing).Error; err != nil {
		return nil, err
	}
	result := existing
	if ending {
		if _, err := auth.AuthorizeScoped(ctx, auth.AlarmWrite); err != nil {
			return nil, err
		}
		if visible, err := api.suppressionTargetsInScope(ctx, &existing); err != nil {
			return nil, err
		} else if !visible {
			return nil, auth.ErrForbidden
		}
	} else if deleting {
		if err := api.authorizeSuppressionWrite(ctx, &existing, nil); err != nil {
			return nil, err
		}
	} else {
		if err := change(&result); err != nil {
			return nil, err
		}
		if err := ValidateAlarmSuppression(&result); err != nil {
			return nil, err
		}
		if err := api.authorizeSuppressionWrite(ctx, &existing, &result); err != nil {
			return nil, err
		}
		if !sameSuppressionTargets(existing.Targets, result.Targets) {
			anchors, err := api.suppressionAnchors(ctx, &result)
			if err != nil {
				return nil, err
			}
			if existing.CreatorScoped {
				anchors = intersectSuppressionAnchors(anchors, existing.CreatorScope)
			}
			result.ScopeAnchors = anchors
		}
	}
	if ending {
		if existing.EndedAt != nil {
			return &existing, nil
		}
		now := time.Now().UTC()
		claims, _ := auth.ClaimsFromContext(ctx)
		result.EndedAt = &now
		result.EndedBy = sql.NullString{String: claims.Subject, Valid: claims.Subject != ""}
	}
	if deleting {
		result.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}
	}
	result.Version++
	omit := []string{"id", "created_at", "tenant_id", "token", "created_by", "creator_scoped", "creator_scope"}
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		omit = append(omit, "scope_anchors")
	}
	write := func(tx *gorm.DB) error {
		q := tx.Model(&existing).Where("id = ? AND version = ?", existing.ID, existing.Version).
			Select("*").Omit(omit...).Updates(&result)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected == 0 {
			return &SuppressionConflictError{Token: token}
		}
		return nil
	}
	now := time.Now().UTC()
	var err error
	if !ending && !deleting && !suppressionWindowLiveAt(&existing, now) && suppressionWindowLiveAt(&result, now) {
		err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if err := checkSuppressionCapacity(ctx, tx, now); err != nil {
				return err
			}
			return write(tx)
		})
	} else {
		err = write(api.RDB.DB(ctx))
	}
	if err != nil {
		return nil, err
	}
	api.invalidateSuppressionWindows(ctx)
	return &result, nil
}

func (api *Api) UpdateAlarmSuppressionWindow(ctx context.Context, token string, r *AlarmSuppressionUpdateRequest) (*AlarmSuppressionWindow, error) {
	if r == nil {
		return nil, suppressionInvalid("update is required")
	}
	return api.mutateSuppression(ctx, token, r.apply, false, false)
}

// EndAlarmSuppressionWindow restores paging and needs alarm:write plus visibility.
func (api *Api) EndAlarmSuppressionWindow(ctx context.Context, token string) (*AlarmSuppressionWindow, error) {
	return api.retrySuppressionMutation(ctx, token, true)
}

func (api *Api) DeleteAlarmSuppressionWindow(ctx context.Context, token string) (*AlarmSuppressionWindow, error) {
	// Soft deletion retains creation's reach-based authority requirement and
	// increases the version in the audit journal.
	return api.retrySuppressionMutation(ctx, token, false)
}

func (api *Api) invalidateSuppressionWindows(ctx context.Context) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return
	}
	api.suppressionCache.Lock()
	delete(api.suppressionCache.tenants, tenant)
	api.suppressionCache.Unlock()
}

// End/delete operate on the freshly read state; user edits remain single-attempt.
func (api *Api) retrySuppressionMutation(ctx context.Context, token string, ending bool) (*AlarmSuppressionWindow, error) {
	var result *AlarmSuppressionWindow
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, err = api.mutateSuppression(ctx, token, nil, ending, !ending)
		var conflict *SuppressionConflictError
		if !errors.As(err, &conflict) {
			return result, err
		}
	}
	return result, err
}
