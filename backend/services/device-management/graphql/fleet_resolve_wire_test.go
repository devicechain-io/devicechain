// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const resolveWire = `mutation ($request: BulkChangeAmbiguityResolution!) {
  resolveAmbiguousBulkChangeLines(request: $request) { token evidence progress { resolved ambiguous } }
}`

func TestBulkChangeLineResolutionFieldsAreUnique(t *testing.T) {
	_, body, found := strings.Cut(fleetOperationSchema, "type BulkChangeLine {")
	require.True(t, found)
	body, _, found = strings.Cut(body, "\n}")
	require.True(t, found)
	for _, field := range []string{"resolvedBy", "resolvedAt", "resolutionNote", "evidenceFlag"} {
		require.Equal(t, 1, strings.Count(body, "\n  "+field+":"), field)
	}
}

func resolveWireRequest(note string) map[string]any {
	return map[string]any{"request": map[string]any{"token": "resolve", "allAmbiguous": true, "verdict": "FAILED", "note": note}}
}

func resolvePerson() *auth.Claims {
	c := person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead)
	c.Amr = auth.AmrPasswordOTP()
	c.MfaTime = jwt.NewNumericDate(time.Now())
	c.IssuedAt = jwt.NewNumericDate(time.Now())
	return c
}

// I-9 and M8: refreshing a token cannot refresh its second factor. Refusals happen
// before any write, even when the operation token does not exist.
func TestResolveStepUpAndNoteOverTheWire(t *testing.T) {
	for _, tc := range []struct {
		name, note, code string
		change           func(*auth.Claims)
	}{
		{"no factor", "reason", "SECOND_FACTOR_REQUIRED", func(c *auth.Claims) { c.Amr = auth.AmrPasswordOnly(); c.MfaTime = nil }},
		{"fresh iat stale factor", "reason", "STEP_UP_REQUIRED", func(c *auth.Claims) { c.MfaTime = jwt.NewNumericDate(time.Now().Add(-6 * time.Minute)) }},
		{"missing factor time", "reason", "STEP_UP_REQUIRED", func(c *auth.Claims) { c.MfaTime = nil }},
		{"future factor", "reason", "STEP_UP_REQUIRED", func(c *auth.Claims) { c.MfaTime = jwt.NewNumericDate(time.Now().Add(time.Minute)) }},
		{"recovery code", "reason", "STEP_UP_REQUIRED", func(c *auth.Claims) { c.Amr = auth.AmrPasswordRecovery() }},
		{"empty note", "", "NOTE_REQUIRED", nil},
		{"blank note", " \t\n", "NOTE_REQUIRED", nil},
		{"long note", strings.Repeat("界", 1001), "NOTE_REQUIRED", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := resolvePerson()
			if tc.change != nil {
				tc.change(c)
			}
			ctx, api := fleetWireCtx(t, c)
			_, errs := execFleet(t, ctx, resolveWire, resolveWireRequest(tc.note))
			require.Len(t, errs, 1)
			require.True(t, strings.HasPrefix(errs[0], tc.code+"|"), errs[0])
			var n int64
			require.NoError(t, api.RDB.DB(auth.WithClaims(ctx, resolvePerson())).Model(&model.FleetOperationEvent{}).Count(&n).Error)
			require.Zero(t, n)
		})
	}
}

// I-23: resolve requires both authorities and a person's unscoped access token.
func TestResolveAuthorizationOverTheWire(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*auth.Claims)
	}{
		{"without device write", "FORBIDDEN", func(c *auth.Claims) { c.Authorities = []string{string(auth.FleetOperate)} }},
		{"without fleet operate", "FORBIDDEN", func(c *auth.Claims) { c.Authorities = []string{string(auth.DeviceWrite)} }},
		{"agent client", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.ClientId = "agent" }},
		{"agent scope", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.Scope = "mcp" }},
		{"service account", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.Username = auth.ServiceAccountSubjectPrefix + "robot" }},
		{"service", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.TokenType = auth.TokenTypeService }},
		{"identity", "FORBIDDEN", func(c *auth.Claims) { c.TokenType = auth.TokenTypeIdentity }},
		{"refresh", "FORBIDDEN", func(c *auth.Claims) { c.TokenType = auth.TokenTypeRefresh }},
		{"scoped member", "FORBIDDEN", func(c *auth.Claims) { c.ScopeAnchors = []string{"customer:acme"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := resolvePerson()
			tc.change(c)
			ctx, api := fleetWireCtx(t, c)
			_, errs := execFleet(t, ctx, resolveWire, resolveWireRequest("reason"))
			require.Len(t, errs, 1)
			require.True(t, strings.HasPrefix(errs[0], tc.code+"|"), errs[0])
			var n int64
			require.NoError(t, api.RDB.DB(auth.WithClaims(ctx, resolvePerson())).Model(&model.FleetOperationEvent{}).Count(&n).Error)
			require.Zero(t, n)
		})
	}
	ctx := importWireCtx(t)
	_, errs := execFleet(t, ctx, resolveWire, resolveWireRequest("reason"))
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "unauthenticated")
}

func TestResolveAndSharedNoteOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, api := fleetWireCtx(t, resolvePerson())
	_, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("resolve", "d1", "d2"))
	require.Empty(t, errs)
	op, err := api.FleetOperationByToken(ctx, "resolve")
	require.NoError(t, err)
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(&model.FleetOperationLine{}).Where("operation_id = ?", op.ID).
		Updates(map[string]any{"posting": "POSTED", "settlement": "AMBIGUOUS"}).Error)
	note := strings.Repeat("界", 1000)
	_, errs = execFleet(t, ctx, resolveWire, resolveWireRequest(note))
	require.Empty(t, errs)
	var lines []model.FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", op.ID).Order("ordinal").Find(&lines).Error)
	require.Len(t, lines, 2)
	require.Equal(t, lines[0].ResolutionId, lines[1].ResolutionId)
	require.True(t, lines[0].ResolutionId.Valid)
	for _, l := range lines {
		require.Equal(t, "RESOLVED_FAILED", l.Settlement.String)
		require.Equal(t, "alice@example.com", l.ResolvedBy.String)
		require.True(t, l.ResolvedAt.Valid)
	}
	var events []model.FleetOperationEvent
	require.NoError(t, db.Where("operation_id = ? AND kind IN ('RESOLUTION','VERDICT')", op.ID).Order("id").Find(&events).Error)
	require.Len(t, events, 3)
	require.Equal(t, "RESOLUTION", events[0].Kind)
	require.Equal(t, note, events[0].Note.String)
	for _, e := range events[1:] {
		require.Equal(t, "VERDICT", e.Kind)
		require.EqualValues(t, events[0].ID, e.RefEventId.Int64)
		require.False(t, e.Note.Valid)
	}
	require.NoError(t, db.Model(&lines[0]).Update("evidence_flag", "EVIDENCE_CONTRADICTS_VERDICT").Error)
	query := `{ bulkChangeLines(token: "resolve", criteria: {pageNumber: 1, pageSize: 10}) { results { resolutionNote resolvedAt evidenceFlag } } }`
	data, errs := execFleet(t, ctx, query, nil)
	require.Empty(t, errs)
	var read struct {
		Lines struct {
			Results []struct{ ResolutionNote, ResolvedAt, EvidenceFlag *string }
		} `json:"bulkChangeLines"`
	}
	require.NoError(t, json.Unmarshal(data, &read))
	require.Equal(t, note, *read.Lines.Results[0].ResolutionNote)
	require.NotNil(t, read.Lines.Results[0].ResolvedAt)
	require.Equal(t, "EVIDENCE_CONTRADICTS_VERDICT", *read.Lines.Results[0].EvidenceFlag)
	// BC-8 clears this one shared event; neither line may disclose its note afterwards.
	require.NoError(t, db.Model(&events[0]).Updates(map[string]any{"note": sql.NullString{}, "note_redacted_at": time.Now()}).Error)
	data, errs = execFleet(t, ctx, query, nil)
	require.Empty(t, errs)
	require.NoError(t, json.Unmarshal(data, &read))
	for _, l := range read.Lines.Results {
		require.Nil(t, l.ResolutionNote)
	}
	// The production schema must continue to refuse the new mutation until BC-13.
	require.NotContains(t, ServedSchema(), "resolveAmbiguousBulkChangeLines")
}
