// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

const (
	reversePreviewWire = `query ($request: BulkChangeReversalRequest!) {
  previewBulkChangeReversal(request: $request) {
    reversibleCount changedSinceCount redactedCount alreadyReversedCount notPostedCount requestDigest planDigest contractDigest
  }
}`
	reverseWire = `mutation ($request: BulkChangeReversalRequest!) {
  reverseBulkChange(request: $request) { token dispatch contractDigest reversalOf { token } }
}`
	reverseLinksWire = `query ($t: String!) {
  bulkChange(token: $t) { reversals { token dispatch } reversalOf { token } }
  bulkChangeLines(token: $t, criteria: { pageNumber: 1, pageSize: 10 }) { results { ordinal reversedBy { ordinal } } }
}`
)

// seedReversibleOriginal stores a DISPATCHED operation "orig" with one applied line on device d1. The
// line's images name generation 0 and no revision, which is exactly the state of a device with no
// configuration history, so nothing reads as changed since.
func seedReversibleOriginal(t *testing.T, ctx context.Context, api *model.Api) {
	t.Helper()
	db := api.RDB.DB(ctx)
	require.NoError(t, db.AutoMigrate(&model.DeviceConfigurationRevision{}))
	var d1 model.Device
	require.NoError(t, db.Where("token = ?", "d1").First(&d1).Error)
	op := &model.FleetOperation{Token: "orig", Name: "orig", Kind: model.FleetKindConfigSet, Dispatch: model.FleetDispatchDispatched,
		Evidence: "OPEN", RequestDigest: "sha256:r", PlanDigest: "sha256:p", ContractDigest: "sha256:c", ContractVersion: 1,
		Intent: `{"key":"site","unset":false,"value":"north","valueType":"STRING"}`, DriverProtocol: 1, TargetKind: "DEVICE_LIST", TargetCount: 1,
		MaxPostsPerSecond: 10, EffPostsPerSecond: 10, SettleDeadlineS: 3600, Initiator: "alice@example.com"}
	require.NoError(t, db.Create(op).Error)
	require.NoError(t, db.Create(&model.FleetOperationLine{OperationId: int64(op.ID), Ordinal: 1, Posting: model.FleetPostingPosted,
		TargetId: sql.NullInt64{Int64: int64(d1.ID), Valid: true}, TargetKey: "d1",
		Prior:       sql.NullString{String: `{"present":true,"valueType":"STRING","value":"south","generation":0}`, Valid: true},
		PostedValue: sql.NullString{String: `{"present":true,"valueType":"STRING","value":"north","generation":0}`, Valid: true}}).Error)
}

func reverseWireRequest(extra map[string]any) map[string]any {
	req := map[string]any{"original": "orig", "token": "undo", "name": "Undo"}
	for k, v := range extra {
		req[k] = v
	}
	return map[string]any{"request": req}
}

func TestReverseOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, api := fleetWireCtx(t, person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead))
	seedReversibleOriginal(t, ctx, api)

	data, errs := execFleet(t, ctx, reversePreviewWire, reverseWireRequest(nil))
	require.Empty(t, errs)
	var preview struct {
		P struct {
			ReversibleCount, ChangedSinceCount, RedactedCount, AlreadyReversedCount, NotPostedCount int
			ContractDigest                                                                          string
		} `json:"previewBulkChangeReversal"`
	}
	require.NoError(t, json.Unmarshal(data, &preview))
	require.Equal(t, 1, preview.P.ReversibleCount)
	require.Zero(t, preview.P.ChangedSinceCount+preview.P.RedactedCount+preview.P.AlreadyReversedCount+preview.P.NotPostedCount)

	data, errs = execFleet(t, ctx, reverseWire, reverseWireRequest(nil))
	require.Empty(t, errs)
	var created struct {
		R struct {
			Token, Dispatch, ContractDigest string
			ReversalOf                      struct{ Token string }
		} `json:"reverseBulkChange"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	require.Equal(t, "READY", created.R.Dispatch)
	require.Equal(t, "orig", created.R.ReversalOf.Token)
	require.Equal(t, preview.P.ContractDigest, created.R.ContractDigest, "what was previewed is what is created")

	data, errs = execFleet(t, ctx, reverseLinksWire, map[string]any{"t": "orig"})
	require.Empty(t, errs)
	var links struct {
		B struct {
			Reversals  []struct{ Token string }
			ReversalOf *struct{ Token string }
		} `json:"bulkChange"`
		L struct {
			Results []struct {
				Ordinal    int
				ReversedBy *struct{ Ordinal int }
			}
		} `json:"bulkChangeLines"`
	}
	require.NoError(t, json.Unmarshal(data, &links))
	require.Len(t, links.B.Reversals, 1)
	require.Equal(t, "undo", links.B.Reversals[0].Token)
	require.Nil(t, links.B.ReversalOf)
	require.Len(t, links.L.Results, 1)
	require.Nil(t, links.L.Results[0].ReversedBy, "nothing is restored until the reversal applies its line")

	// The original can no longer be reversed while it is not finished.
	require.NoError(t, api.RDB.DB(ctx).Model(&model.FleetOperation{}).Where("token = 'orig'").Update("dispatch", "PAUSED").Error)
	_, errs = execFleet(t, ctx, reverseWire, reverseWireRequest(map[string]any{"token": "undo2"}))
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], model.FleetCodeOriginalStillDispatching+"|"), errs[0])
}

// I-23 for reversal: both authorities, a person's own unscoped token, and a fresh second factor for an override.
func TestReverseAuthorizationOverTheWire(t *testing.T) {
	agent := person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead)
	agent.TokenType = auth.TokenTypeService
	for _, tc := range []struct {
		name   string
		claims *auth.Claims
		extra  map[string]any
		code   string
	}{
		{"without fleet:operate", person(auth.DeviceWrite, auth.DeviceRead), nil, ""},
		{"without device:write", person(auth.FleetOperate, auth.DeviceRead), nil, ""},
		{"a service token", agent, nil, model.FleetCodeAgentNotPermitted},
		{"an override without a second factor", person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead), map[string]any{"overrideChanged": true}, model.FleetCodeSecondFactorRequired},
		{"a line override without a second factor", person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead), map[string]any{"overrideOrdinals": []any{1}}, model.FleetCodeSecondFactorRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEngine(t)
			ctx, api := fleetWireCtx(t, tc.claims)
			seedReversibleOriginal(t, auth.WithClaims(ctx, resolvePerson()), api)
			for _, q := range []string{reversePreviewWire, reverseWire} {
				if tc.extra != nil && q == reversePreviewWire {
					continue // a preview writes nothing and needs no second factor
				}
				_, errs := execFleet(t, ctx, q, reverseWireRequest(tc.extra))
				require.Len(t, errs, 1)
				if tc.code != "" {
					require.True(t, strings.HasPrefix(errs[0], tc.code+"|"), errs[0])
				}
			}
			var n int64
			require.NoError(t, api.RDB.DB(auth.WithClaims(ctx, resolvePerson())).Model(&model.FleetOperation{}).Where("reversal_of IS NOT NULL").Count(&n).Error)
			require.Zero(t, n, "nothing was created")
		})
	}
	// Negative control: with a fresh second factor the override is accepted.
	withEngine(t)
	ctx, api := fleetWireCtx(t, resolvePerson())
	seedReversibleOriginal(t, ctx, api)
	_, errs := execFleet(t, ctx, reverseWire, reverseWireRequest(map[string]any{"overrideChanged": true}))
	require.Empty(t, errs)
}

func TestReversalIsNotServedUntilTheEngineIs(t *testing.T) {
	for _, name := range []string{"reverseBulkChange", "previewBulkChangeReversal", "BulkChangeReversalRequest", "reversedBy"} {
		require.NotContains(t, ServedSchema(), name)
		require.Contains(t, SchemaContentWithFleetOperations, name)
	}
}
