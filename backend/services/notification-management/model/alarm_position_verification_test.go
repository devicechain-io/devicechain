// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

type positionOracleFunc func(context.Context, []string) (map[string]bool, error)

func (f positionOracleFunc) VisibleWithoutLocation(ctx context.Context, tokens []string) (map[string]bool, error) {
	return f(ctx, tokens)
}

func TestHistoryVerifiesSilentGeofenceAndRollingOldPodFalse(t *testing.T) {
	for _, read := range []string{"states", "tokens", "attempts"} {
		for _, mode := range []string{"refused", "stale", "error", "nil", "visible", "retry-refused"} {
			t.Run(read+"/"+mode, func(t *testing.T) {
				api := newTestApi(t)
				ctx := tenantCtx("A")
				no := false
				for _, token := range []string{"a", "b"} {
					require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: token, AlarmKey: "k", PositionDerived: &no}).Error)
					require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: token}))
				}
				require.NoError(t, api.RDB.DB(tenantCtx("B")).Create(&NotificationState{AlarmToken: "a", AlarmKey: "k", PositionDerived: &no}).Error)
				if mode == "stale" {
					require.NoError(t, api.RecordAlarmPosition(ctx, "a", nil))
					require.NoError(t, api.RecordAlarmPosition(ctx, "a", &no))
				}
				calls := 0
				if mode != "nil" {
					api.PositionOracle = positionOracleFunc(func(_ context.Context, tokens []string) (map[string]bool, error) {
						calls++
						if mode == "error" {
							return map[string]bool{"a": true, "b": true}, errors.New("DM unavailable")
						}
						if mode == "visible" {
							return map[string]bool{"a": true, "b": true}, nil
						}
						if mode == "retry-refused" && calls == 1 {
							return map[string]bool{"b": true}, nil
						}
						return map[string]bool{}, nil
					})
				}
				run := func(c context.Context) (int, error) {
					page := rdb.Pagination{PageNumber: 1, PageSize: 10}
					switch read {
					case "tokens":
						rows, err := api.NotificationStatesByAlarmToken(c, []string{"a", "b"})
						return len(rows), err
					case "states":
						rows, err := api.NotificationStates(c, NotificationStateSearchCriteria{Pagination: page})
						if err != nil {
							require.Nil(t, rows)
							return 0, err
						}
						require.Equal(t, int32(len(rows.Results)), rows.Pagination.TotalRecords)
						return len(rows.Results), nil
					default:
						rows, err := api.NotificationAttempts(c, NotificationAttemptSearchCriteria{Pagination: page})
						if err != nil {
							require.Nil(t, rows)
							return 0, err
						}
						require.Equal(t, int32(len(rows.Results)), rows.Pagination.TotalRecords)
						return len(rows.Results), nil
					}
				}
				caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
				count, err := run(caller)
				if mode == "error" || mode == "nil" || mode == "retry-refused" {
					require.Error(t, err)
					require.Zero(t, count)
				} else {
					require.NoError(t, err)
					if mode == "visible" {
						require.Equal(t, 2, count)
					} else {
						require.Zero(t, count)
					}
				}
				if mode == "refused" || mode == "stale" {
					rows, e := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
					require.NoError(t, e)
					require.Nil(t, rows[0].PositionDerived)
					rows, e = api.NotificationStatesByAlarmToken(tenantCtx("B"), []string{"a"})
					require.NoError(t, e)
					require.Equal(t, &no, rows[0].PositionDerived)
				}
				before := calls
				for _, c := range []context.Context{ctx, auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead), string(auth.LocationRead)}})} {
					count, e := run(c)
					require.NoError(t, e)
					require.Equal(t, 2, count)
				}
				require.Equal(t, before, calls)
			})
		}
	}
}

func TestPositionVerificationSystemContextUnchanged(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(auth.WithClaims(tenantCtx("A"), &auth.Claims{Authorities: []string{string(auth.NotificationRead)}}))
	api.PositionOracle = positionOracleFunc(func(context.Context, []string) (map[string]bool, error) {
		t.Fatal("system read called oracle")
		return nil, nil
	})
	_, err := api.NotificationStates(ctx, NotificationStateSearchCriteria{})
	require.NoError(t, err)
	_, err = api.NotificationStatesByAlarmToken(ctx, []string{"a"})
	require.NoError(t, err)
	_, err = api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{})
	require.NoError(t, err)
}

func TestPositionVerificationChunksDistinctTokens(t *testing.T) {
	api := newTestApi(t)
	ctx := auth.WithClaims(tenantCtx("A"), &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
	tokens := []string{}
	for i := 0; i < rdb.MaxLookupKeys+1; i++ {
		tokens = append(tokens, fmt.Sprint(i))
	}
	tokens = append(tokens, tokens...)
	calls := 0
	seen := map[string]bool{}
	api.PositionOracle = positionOracleFunc(func(_ context.Context, chunk []string) (map[string]bool, error) {
		calls++
		require.LessOrEqual(t, len(chunk), rdb.MaxLookupKeys)
		visible := map[string]bool{}
		for _, token := range chunk {
			require.False(t, seen[token])
			seen[token] = true
			visible[token] = true
		}
		return visible, nil
	})
	result, err := verifiedHistory(ctx, api, func() ([]string, error) { return tokens, nil }, func(rows []string) ([]string, error) { return rows, nil })
	require.NoError(t, err)
	require.Equal(t, tokens, result)
	require.Equal(t, 2, calls)
	require.Len(t, seen, rdb.MaxLookupKeys+1)
}

// Empty pages must still verify every alarm included in TotalRecords.
func TestHistoryVerifiesOutOfRangePageCount(t *testing.T) {
	for _, read := range []string{"attempts", "states"} {
		t.Run(read, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			no := false
			require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: "geofence", AlarmKey: "k", PositionDerived: &no}).Error)
			require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: "geofence"}))
			caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
			var asked []string
			api.PositionOracle = positionOracleFunc(func(_ context.Context, tokens []string) (map[string]bool, error) {
				asked = append(asked, tokens...)
				return map[string]bool{}, nil
			})
			page := rdb.Pagination{PageNumber: 2, PageSize: 1}
			if read == "attempts" {
				rows, err := api.NotificationAttempts(caller, NotificationAttemptSearchCriteria{AlarmToken: strPtr("geofence"), Pagination: page})
				if err == nil {
					require.Empty(t, rows.Results)
					require.Zero(t, rows.Pagination.TotalRecords, "count disclosed refused history on an empty page")
				} else {
					require.Nil(t, rows)
				}
			} else {
				rows, err := api.NotificationStates(caller, NotificationStateSearchCriteria{AlarmKey: strPtr("k"), Pagination: page})
				if err == nil {
					require.Empty(t, rows.Results)
					require.Zero(t, rows.Pagination.TotalRecords, "count disclosed refused history on an empty page")
				} else {
					require.Nil(t, rows)
				}
			}
			require.Equal(t, []string{"geofence"}, asked, "empty pages must verify the full counted set")
		})
	}
}

func TestHistoryVerificationTokenCap(t *testing.T) {
	previous := maxHistoryVerificationTokens
	maxHistoryVerificationTokens = 2
	t.Cleanup(func() { maxHistoryVerificationTokens = previous })
	for _, read := range []string{"attempts", "states"} {
		t.Run(read, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			no := false
			for _, token := range []string{"a", "b", "c"} {
				require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: token, AlarmKey: "k", PositionDerived: &no}).Error)
				require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: token}))
			}
			calls := 0
			api.PositionOracle = positionOracleFunc(func(_ context.Context, tokens []string) (map[string]bool, error) {
				calls++
				visible := map[string]bool{}
				for _, token := range tokens {
					visible[token] = true
				}
				return visible, nil
			})
			caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
			page := rdb.Pagination{PageNumber: 1, PageSize: 1}
			var err error
			if read == "attempts" {
				rows, e := api.NotificationAttempts(caller, NotificationAttemptSearchCriteria{Pagination: page})
				err = e
				require.Nil(t, rows)
			} else {
				rows, e := api.NotificationStates(caller, NotificationStateSearchCriteria{Pagination: page})
				err = e
				require.Nil(t, rows)
			}
			require.ErrorIs(t, err, auth.ErrForbidden)
			require.ErrorContains(t, err, "history is too broad to verify")
			require.ErrorContains(t, err, "narrow the filter")
			require.Zero(t, calls, "over-cap sets must be refused before any oracle call")

			for _, trusted := range []context.Context{ctx, auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead), string(auth.LocationRead)}}), core.WithSystemContext(caller)} {
				if read == "attempts" {
					_, err = api.NotificationAttempts(trusted, NotificationAttemptSearchCriteria{Pagination: page})
				} else {
					_, err = api.NotificationStates(trusted, NotificationStateSearchCriteria{Pagination: page})
				}
				require.NoError(t, err)
			}
			require.Zero(t, calls)
		})
	}
}
