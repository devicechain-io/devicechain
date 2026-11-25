// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func resolveContext(ctx context.Context, now time.Time) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{Username: "resolver", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceWrite), string(auth.FleetOperate)},
		Amr:         auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now),
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(fleetClock())}})
}

func resolveRequest(token string) FleetAmbiguityResolution {
	return FleetAmbiguityResolution{Token: token, AllAmbiguous: true, Verdict: "FAILED", Note: "operator investigated"}
}

func TestResolveRequiresABoundedNonemptyNote(t *testing.T) {
	for _, note := range []string{"", " \t\n", strings.Repeat("界", 1001), string([]byte{0xff})} {
		api, ctx, line := settlementFixture(t)
		ctx = resolveContext(ctx, fleetClock())
		db := api.RDB.DB(ctx)
		require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
		req := resolveRequest("settle")
		req.Note = note
		_, err := api.ResolveAmbiguousFleetLines(ctx, req)
		requireFleetCode(t, err, FleetCodeNoteRequired)
		require.NoError(t, db.First(line, line.ID).Error)
		require.Equal(t, "AMBIGUOUS", line.Settlement.String)
		var n int64
		require.NoError(t, db.Model(&FleetOperationEvent{}).Where("kind IN ('RESOLUTION','VERDICT')").Count(&n).Error)
		require.Zero(t, n)
	}
}

func TestResolveOnlyAmbiguousLines(t *testing.T) {
	for _, state := range []string{"", "CONFIRMED", "FAILED", "SUPERSEDED", "RESOLVED_CONFIRMED", "RESOLVED_FAILED", "AMBIGUOUS", "erased", "redacted"} {
		t.Run(state, func(t *testing.T) {
			api, ctx, line := settlementFixture(t)
			ctx = resolveContext(ctx, fleetClock())
			db := api.RDB.DB(ctx)
			set := map[string]any{"settlement": sql.NullString{String: state, Valid: state != ""}}
			if state == "erased" || state == "redacted" {
				set["settlement"] = "AMBIGUOUS"
				if state == "erased" {
					set["code"] = "TARGET_ERASED"
				} else {
					set["redacted_at"] = fleetClock()
				}
			}
			require.NoError(t, db.Model(line).Updates(set).Error)
			require.NoError(t, db.First(line, line.ID).Error)
			before := *line
			op, err := api.ResolveAmbiguousFleetLines(ctx, resolveRequest("settle"))
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			var n int64
			require.NoError(t, db.Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind = 'VERDICT'", op.ID).Count(&n).Error)
			if state != "AMBIGUOUS" {
				require.Equal(t, before, *line, "a non-ambiguous or erased line is byte-for-byte unchanged")
				require.Zero(t, n)
				return
			}
			require.EqualValues(t, 1, n)
			require.Equal(t, "RESOLVED_FAILED", line.Settlement.String)
			require.Equal(t, before.PostedRevision, line.PostedRevision)
			require.Equal(t, before.PostedValue, line.PostedValue)
			require.Equal(t, "resolver", line.ResolvedBy.String)
			require.True(t, line.ResolvedAt.Valid)
			require.Equal(t, "CLOSED", op.Evidence)
			// Replaying with the opposite verdict cannot flip a resolved line.
			saved := *line
			req := resolveRequest("settle")
			req.Verdict = "CONFIRMED"
			_, err = api.ResolveAmbiguousFleetLines(ctx, req)
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			require.Equal(t, saved, *line)
		})
	}
}

// I-12 exercises the real resolution path before BC-6 sees a deciding APPLIED.
func TestResolveThenContradictingEvidenceIsRecordedOnce(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	ctx = resolveContext(ctx, fleetClock())
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
	op, err := api.ResolveAmbiguousFleetLines(ctx, resolveRequest("settle"))
	require.NoError(t, err)
	require.Equal(t, "CLOSED", op.Evidence)
	require.NoError(t, db.First(line, line.ID).Error)
	resolved := *line
	settlementReport(t, db, line, 5, "APPLIED", "REPORT")
	for i := 0; i < 5; i++ {
		_, err := api.ReconcileFleetLines(ctx, false)
		require.NoError(t, err)
		_, err = api.DeadlineFleetLines(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, resolved.Settlement, line.Settlement)
	require.Equal(t, resolved.ResolutionId, line.ResolutionId)
	require.Equal(t, resolved.ResolvedAt, line.ResolvedAt)
	require.Equal(t, "EVIDENCE_CONTRADICTS_VERDICT", line.EvidenceFlag)
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id = ? AND kind = 'CONTRADICTING_EVIDENCE'", line.ID).Count(&n).Error)
	require.EqualValues(t, 1, n)
}

func TestResolveRechecksAmbiguousAtTheUpdate(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	ctx = resolveContext(ctx, fleetClock())
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
	// Inject evidence after selection, immediately before the conditional UPDATE.
	// The real PostgreSQL interleaving is also covered in the tagged suite.
	injected := false
	const callback = "test:resolve_evidence_wins"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		set, ok := tx.Statement.Dest.(map[string]any)
		if !ok || set["resolution_id"] == nil || injected {
			return
		}
		injected = true
		err := tx.Session(&gorm.Session{NewDB: true}).Model(&FleetOperationLine{}).Where("id = ?", line.ID).
			UpdateColumns(map[string]any{"settlement": "CONFIRMED", "evidence_revision": 5}).Error
		if err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
	_, err := api.ResolveAmbiguousFleetLines(ctx, resolveRequest("settle"))
	require.NoError(t, err)
	require.True(t, injected)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "CONFIRMED", line.Settlement.String)
	require.False(t, line.ResolutionId.Valid)
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id = ? AND kind = 'VERDICT'", line.ID).Count(&n).Error)
	require.Zero(t, n)
}

func TestResolveEventsAndVerdictAreAtomic(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	ctx = resolveContext(ctx, fleetClock())
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
	const callback = "test:resolve_verdict_insert_fails"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if event, ok := tx.Statement.Dest.(*FleetOperationEvent); ok && event.Kind == "VERDICT" {
			tx.AddError(errors.New("verdict insert failed"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove(callback)) })
	_, err := api.ResolveAmbiguousFleetLines(ctx, resolveRequest("settle"))
	require.ErrorContains(t, err, "verdict insert failed")
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "AMBIGUOUS", line.Settlement.String)
	require.False(t, line.ResolutionId.Valid)
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("kind IN ('RESOLUTION','VERDICT')").Count(&n).Error)
	require.Zero(t, n)
}

func TestResolveSelectionProtocolAndTenant(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 2)
	ctx = resolveContext(ctx, fleetClock())
	op := readyOp(t, api, ctx, "resolve", FleetSafetyRequest{}, "d1", "d2")
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Updates(map[string]any{"posting": "POSTED", "settlement": "AMBIGUOUS"}).Error)
	for _, req := range []FleetAmbiguityResolution{
		{Token: op.Token, Verdict: "FAILED", Note: "why"},
		{Token: op.Token, Ordinals: []int32{1}, AllAmbiguous: true, Verdict: "FAILED", Note: "why"},
		{Token: op.Token, Ordinals: []int32{0}, Verdict: "FAILED", Note: "why"},
		{Token: op.Token, Ordinals: []int32{-1}, Verdict: "FAILED", Note: "why"},
		{Token: op.Token, AllAmbiguous: true, Verdict: "SUPERSEDED", Note: "why"},
	} {
		_, err := api.ResolveAmbiguousFleetLines(ctx, req)
		requireFleetCode(t, err, FleetCodeInvalidRequest)
	}
	req := resolveRequest(op.Token)
	other := core.WithTenant(ctx, "other-tenant")
	_, err := api.ResolveAmbiguousFleetLines(other, req)
	require.Error(t, err)
	require.NoError(t, db.Model(op).Update("driver_protocol", 2).Error)
	_, err = api.ResolveAmbiguousFleetLines(ctx, req)
	requireFleetCode(t, err, FleetCodeNewerThanServer)
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("kind IN ('VERDICT','RESOLUTION')").Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, db.Model(op).Update("driver_protocol", 1).Error)
	req.AllAmbiguous, req.Ordinals, req.Verdict = false, []int32{2, 2, 999}, "CONFIRMED"
	_, err = api.ResolveAmbiguousFleetLines(ctx, req)
	require.NoError(t, err)
	var lines []FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", op.ID).Order("ordinal").Find(&lines).Error)
	require.Equal(t, "AMBIGUOUS", lines[0].Settlement.String)
	require.Equal(t, "RESOLVED_CONFIRMED", lines[1].Settlement.String)
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("kind = 'VERDICT'").Count(&n).Error)
	require.EqualValues(t, 1, n)
}

func TestResolveFiveMinuteBoundaryAndDatabaseClockSeam(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	prev := fleetClock
	fleetClock = func() time.Time { return now }
	t.Cleanup(func() { fleetClock = prev })
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
	_, err := api.ResolveAmbiguousFleetLines(resolveContext(ctx, now.Add(-5*time.Minute-time.Second)), resolveRequest("settle"))
	requireFleetCode(t, err, FleetCodeStepUpRequired)
	req := resolveRequest("settle")
	req.Note = strings.Repeat("界", 1000)
	_, err = api.ResolveAmbiguousFleetLines(resolveContext(ctx, now.Add(-5*time.Minute)), req)
	require.NoError(t, err)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, now, line.ResolvedAt.Time)
	var event FleetOperationEvent
	require.NoError(t, db.Where("kind = 'RESOLUTION'").First(&event).Error)
	require.Equal(t, now, event.At)
	require.Equal(t, req.Note, event.Note.String)
}
