// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
)

const (
	FleetCodeStepUpRequired       = "STEP_UP_REQUIRED"
	FleetCodeSecondFactorRequired = "SECOND_FACTOR_REQUIRED"
	FleetCodeNoteRequired         = "NOTE_REQUIRED"
	fleetResolveMaxAge            = 5 * time.Minute
)

func fleetResolveFresh(claims *auth.Claims, now time.Time) error {
	if !auth.StepUpFresh(claims, auth.StepUp{MaxAge: fleetResolveMaxAge}, now) {
		return fleetErr(FleetCodeStepUpRequired, "resolving a bulk change needs a second factor verified within the last five minutes")
	}
	return nil
}

// FleetAmbiguityResolution selects either explicit ordinals or every ambiguous
// line. A resolution's note is held once, on its RESOLUTION event.
type FleetAmbiguityResolution struct {
	Token        string
	Ordinals     []int32
	AllAmbiguous bool
	Verdict      string
	Note         string
}

// ResolveAmbiguousFleetLines records a person's verdict, never replacing device
// evidence or another person's verdict. The resolver checks both authorities;
// this path also requires a person's access token and fresh MFA before any write.
func (api *Api) ResolveAmbiguousFleetLines(ctx context.Context, req FleetAmbiguityResolution) (*FleetOperation, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) || claims.TokenType == auth.TokenTypeService ||
		strings.HasPrefix(claims.Username, auth.ServiceAccountSubjectPrefix) || strings.HasPrefix(claims.Subject, auth.ServiceAccountSubjectPrefix) {
		return nil, fleetErr(FleetCodeAgentNotPermitted, "a bulk change must be resolved by a person acting under their own access token")
	}
	if claims.TokenType != auth.TokenTypeAccess {
		return nil, auth.ErrForbidden
	}
	if !claims.Mfa() {
		return nil, fleetErr(FleetCodeSecondFactorRequired, "resolving a bulk change needs a sign-in with a second factor")
	}
	db := api.RDB.DB(ctx)
	// The database wall clock is shared by every replica. Refreshing a token
	// changes iat, never mfa_time. The process clock is only a SQLite test seam.
	var now time.Time
	if db.Dialector.Name() == "postgres" {
		if err := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return nil, err
		}
	} else {
		now = fleetClock()
	}
	if err := fleetResolveFresh(claims, now); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Note) == "" || !utf8.ValidString(req.Note) || utf8.RuneCountInString(req.Note) > 1000 {
		return nil, fleetErr(FleetCodeNoteRequired, "a resolution needs a note of 1 to 1000 characters")
	}
	if req.Verdict != "CONFIRMED" && req.Verdict != "FAILED" {
		return nil, fleetErr(FleetCodeInvalidRequest, "a resolution verdict must be CONFIRMED or FAILED")
	}
	if req.AllAmbiguous == (len(req.Ordinals) > 0) {
		return nil, fleetErr(FleetCodeInvalidRequest, "select ordinals or allAmbiguous")
	}
	if len(req.Ordinals) > ledger.MaxLinesPerOperation {
		return nil, fleetErr(FleetCodeOperationTooLarge, "a resolution selects at most 10000 ordinals")
	}
	for _, ordinal := range req.Ordinals {
		if ordinal < 1 {
			return nil, fleetErr(FleetCodeInvalidRequest, "resolution ordinals start at 1")
		}
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("a bulk change needs a tenant in the context")
	}
	head, err := api.fleetOperationByToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var resolved int64
	err = db.Transaction(func(tx *gorm.DB) error {
		resolved = 0
		if err := fleetSetLockTimeout(tx, fleetFenceWait); err != nil {
			return err
		}
		// Retention holds this fence exclusively. Resolve shares it and never
		// locks or changes the header; only the line CAS decides who wins.
		if err := fleetFence(tx, fleetOpKey(tenant, head.ID), true, "RESOLVE"); err != nil {
			return err
		}
		var op FleetOperation
		if err := tx.First(&op, head.ID).Error; err != nil {
			return err
		}
		if !ledger.Drivable(ledger.DriverProtocol(op.DriverProtocol)) {
			return fleetErr(FleetCodeNewerThanServer, "this bulk change was created by a newer release; retry, or pause it")
		}
		// Sample again after any wait. Event and resolution timestamps are
		// database time, and waiting cannot extend a factor's freshness.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
				return err
			}
		}
		if err := fleetResolveFresh(claims, now); err != nil {
			return err
		}
		q := tx.Where("operation_id = ? AND settlement = 'AMBIGUOUS' AND posting = 'POSTED' AND redacted_at IS NULL AND code <> 'TARGET_ERASED'", op.ID)
		if !req.AllAmbiguous {
			q = q.Where("ordinal IN ?", req.Ordinals)
		}
		var lines []FleetOperationLine
		if err := q.Order("id").Find(&lines).Error; err != nil {
			return err
		}
		resolution := FleetOperationEvent{OperationId: int64(op.ID), Kind: "RESOLUTION", At: now,
			Actor: sql.NullString{String: claims.Username, Valid: true}, Note: sql.NullString{String: req.Note, Valid: true}}
		if err := tx.Create(&resolution).Error; err != nil {
			return err
		}
		settlement := "RESOLVED_" + req.Verdict
		for _, line := range lines {
			res := tx.Model(&FleetOperationLine{}).
				Where("id = ? AND operation_id = ? AND settlement = 'AMBIGUOUS' AND posting = 'POSTED' AND redacted_at IS NULL AND code <> 'TARGET_ERASED'", line.ID, op.ID).
				UpdateColumns(map[string]any{"settlement": settlement, "resolution_id": resolution.ID, "resolved_by": claims.Username, "resolved_at": now, "settled_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			resolved += res.RowsAffected
			if err := tx.Create(&FleetOperationEvent{OperationId: int64(op.ID), Kind: "VERDICT", At: now,
				LineId: sql.NullInt64{Int64: int64(line.ID), Valid: true}, RefEventId: sql.NullInt64{Int64: int64(resolution.ID), Valid: true},
				Actor: resolution.Actor, Reason: settlement}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if lockTimedOut(err) {
			return nil, fleetErr(FleetCodeFencePending, "the bulk change is busy; retry")
		}
		return nil, err
	}
	bulkMetrics.settle("RESOLVED_"+req.Verdict, "", int(resolved))
	// Close evidence with no other locks held, as the reconciler does. Never
	// acquire the header row while holding line locks (control locks it first).
	if err := closeFleetEvidence(db, map[int64]bool{int64(head.ID): true}); err != nil {
		return nil, err
	}
	return api.fleetOperationByToken(ctx, req.Token)
}

// FleetResolutionNote reads the shared event under the caller's tenant. BC-8
// redacts this single note for every line referring to it.
func (api *Api) FleetResolutionNote(ctx context.Context, line *FleetOperationLine) (*string, error) {
	if !line.ResolutionId.Valid || line.RedactedAt.Valid {
		return nil, nil
	}
	var event FleetOperationEvent
	err := api.RDB.DB(ctx).Where("id = ? AND operation_id = ? AND kind = 'RESOLUTION' AND note_redacted_at IS NULL", line.ResolutionId.Int64, line.OperationId).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !event.Note.Valid || event.Note.String == "" {
		return nil, nil
	}
	return &event.Note.String, nil
}
