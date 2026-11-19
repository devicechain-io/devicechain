// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func suppressionTestWindow() AlarmSuppressionWindow {
	start := utcAt(2026, 3, 29, 1, 0)
	end := start.Add(time.Hour)
	return AlarmSuppressionWindow{TokenReference: rdb.TokenReference{Token: "service"}, Reason: "replace pump",
		Targets: []AlarmSuppressionTarget{{Type: "DEVICE", Token: "pump"}}, Kind: SuppressionOneShot, StartTime: &start, EndTime: &end}
}

func suppressionRecurringWindow() AlarmSuppressionWindow {
	w := suppressionTestWindow()
	w.Kind, w.StartTime, w.EndTime = SuppressionRecurring, nil, nil
	w.Timezone = sql.NullString{String: "Europe/Berlin", Valid: true}
	w.Slots = datatypes.JSON(`[{"day":"SUNDAY","start":"02:30","durationMinutes":120}]`)
	return w
}

func TestSuppressionValidation(t *testing.T) {
	cases := map[string]func(*AlarmSuppressionWindow){
		"reason absent":        func(w *AlarmSuppressionWindow) { w.Reason = " " },
		"reason long":          func(w *AlarmSuppressionWindow) { w.Reason = strings.Repeat("x", 1025) },
		"name long":            func(w *AlarmSuppressionWindow) { w.Name.String = strings.Repeat("x", 129) },
		"description long":     func(w *AlarmSuppressionWindow) { w.Description.String = strings.Repeat("x", 1025) },
		"unsafe token":         func(w *AlarmSuppressionWindow) { w.Token = "bad.token" },
		"empty targets":        func(w *AlarmSuppressionWindow) { w.Targets = nil },
		"too many targets":     func(w *AlarmSuppressionWindow) { w.Targets = make([]AlarmSuppressionTarget, 21) },
		"unknown target":       func(w *AlarmSuppressionWindow) { w.Targets[0].Type = "AREA_SUBTREE" },
		"missing target token": func(w *AlarmSuppressionWindow) { w.Targets[0].Token = "" },
		"duplicate targets":    func(w *AlarmSuppressionWindow) { w.Targets = append(w.Targets, w.Targets[0]) },
		"tenant mixed":         func(w *AlarmSuppressionWindow) { w.Targets = append(w.Targets, AlarmSuppressionTarget{Type: "TENANT"}) },
		"tenant token": func(w *AlarmSuppressionWindow) {
			w.Targets = []AlarmSuppressionTarget{{Type: "TENANT", Token: "tenant"}}
		},
		"empty key":      func(w *AlarmSuppressionWindow) { w.AlarmKeys = []string{""} },
		"duplicate key":  func(w *AlarmSuppressionWindow) { w.AlarmKeys = []string{"hot", "hot"} },
		"long key":       func(w *AlarmSuppressionWindow) { w.AlarmKeys = []string{strings.Repeat("x", 257)} },
		"many keys":      func(w *AlarmSuppressionWindow) { w.AlarmKeys = make([]string, 21) },
		"unknown bypass": func(w *AlarmSuppressionWindow) { w.BypassSeverity = sql.NullString{String: "*", Valid: true} },
		"missing start":  func(w *AlarmSuppressionWindow) { w.StartTime = nil },
		"end at start":   func(w *AlarmSuppressionWindow) { w.EndTime = w.StartTime },
		"31 days exceeded": func(w *AlarmSuppressionWindow) {
			end := w.StartTime.Add(31*24*time.Hour + time.Nanosecond)
			w.EndTime = &end
		},
		"one shot zone":   func(w *AlarmSuppressionWindow) { w.Timezone.Valid = true },
		"one shot slots":  func(w *AlarmSuppressionWindow) { w.Slots = datatypes.JSON(`[]`) },
		"one shot bounds": func(w *AlarmSuppressionWindow) { w.ValidFrom = w.StartTime },
		"unknown kind":    func(w *AlarmSuppressionWindow) { w.Kind = "BAD" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			w := suppressionTestWindow()
			edit(&w)
			require.ErrorIs(t, ValidateAlarmSuppression(&w), ErrInvalidAlarmSuppression)
		})
	}
	w := suppressionTestWindow()
	end := w.StartTime.Add(31 * 24 * time.Hour)
	w.EndTime = &end
	require.NoError(t, ValidateAlarmSuppression(&w))
	w.Targets = []AlarmSuppressionTarget{{Type: "TENANT"}}
	require.NoError(t, ValidateAlarmSuppression(&w))
	for name, edit := range map[string]func(*AlarmSuppressionWindow){
		"zone absent":  func(w *AlarmSuppressionWindow) { w.Timezone.Valid = false },
		"zone Local":   func(w *AlarmSuppressionWindow) { w.Timezone.String = "Local" },
		"slots absent": func(w *AlarmSuppressionWindow) { w.Slots = nil },
		"unknown slot field": func(w *AlarmSuppressionWindow) {
			w.Slots = datatypes.JSON(`[{"day":"SUNDAY","start":"02:30","durationMinutes":120,"typo":1}]`)
		},
		"trailing slots": func(w *AlarmSuppressionWindow) { w.Slots = append(w.Slots, []byte(` {}`)...) },
		"whole week": func(w *AlarmSuppressionWindow) {
			w.Slots = datatypes.JSON(`[{"day":"MONDAY","start":"00:00","durationMinutes":10080}]`)
		},
		"overlap": func(w *AlarmSuppressionWindow) {
			w.Slots = datatypes.JSON(`[{"day":"SUNDAY","start":"23:00","durationMinutes":120},{"day":"MONDAY","start":"00:00","durationMinutes":60}]`)
		},
		"start present":   func(w *AlarmSuppressionWindow) { at := time.Now(); w.StartTime = &at },
		"reversed bounds": func(w *AlarmSuppressionWindow) { at := time.Now(); w.ValidFrom, w.ValidUntil = &at, &at },
	} {
		t.Run(name, func(t *testing.T) {
			w := suppressionRecurringWindow()
			edit(&w)
			require.ErrorIs(t, ValidateAlarmSuppression(&w), ErrInvalidAlarmSuppression)
		})
	}
}

func TestSuppressionMatcherTargets(t *testing.T) {
	facts := deviceFacts{deviceToken: "pump", deviceTypeToken: "pumps", groupTokens: map[string]bool{"north": true}, currentAnchors: map[string]bool{"area:yard": true, "asset:line": true, "customer:acme": true}}
	for _, target := range []AlarmSuppressionTarget{{"DEVICE", "pump"}, {"DEVICE_TYPE", "pumps"}, {"ENTITY_GROUP", "north"}, {"AREA", "yard"}, {"ASSET", "line"}, {"CUSTOMER", "acme"}, {"TENANT", ""}} {
		t.Run(target.Type, func(t *testing.T) {
			w := suppressionTestWindow()
			w.Targets = []AlarmSuppressionTarget{target}
			compiled := compileSuppressionWindows([]AlarmSuppressionWindow{w})
			require.NotNil(t, matchWindows(compiled, facts, "hot", "MINOR", *w.StartTime))
			if target.Type != "TENANT" {
				w.Targets[0].Token = "other"
				require.Nil(t, matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", *w.StartTime))
			}
		})
	}
}

func TestSuppressionBypassAndKeys(t *testing.T) {
	w := suppressionTestWindow()
	w.BypassSeverity = sql.NullString{String: "MAJOR", Valid: true}
	w.AlarmKeys = []string{"hot"}
	facts := deviceFacts{deviceToken: "pump"}
	for _, tc := range []struct {
		key, severity string
		want          bool
	}{{"hot", "MINOR", true}, {"hot", "MAJOR", false}, {"hot", "CRITICAL", false}, {"hot", "FUTURE", false}, {"other", "MINOR", false}} {
		t.Run(tc.key+tc.severity, func(t *testing.T) {
			require.Equal(t, tc.want, matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, tc.key, tc.severity, *w.StartTime) != nil)
		})
	}
	w.BypassSeverity.Valid = false
	require.Nil(t, matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "UNKNOWN", *w.StartTime))
}

func TestSuppressionEndedEarly(t *testing.T) {
	w := suppressionTestWindow()
	ended := w.StartTime.Add(20 * time.Minute)
	w.EndedAt = &ended
	windows := compileSuppressionWindows([]AlarmSuppressionWindow{w})
	facts := deviceFacts{deviceToken: "pump"}
	require.Nil(t, matchWindows(windows, facts, "hot", "MINOR", ended))
	match := matchWindows(windows, facts, "hot", "MINOR", ended.Add(-time.Second))
	require.NotNil(t, match)
	require.Equal(t, ended, match.Until)
	require.Nil(t, matchWindows(windows, facts, "hot", "MINOR", w.StartTime.Add(-time.Second)))
}

func TestSuppressionRecurringDSTAndBounds(t *testing.T) {
	w := suppressionRecurringWindow()
	facts := deviceFacts{deviceToken: "pump"}
	at := utcAt(2026, 3, 29, 1, 0) // nonexistent 02:30 opens at the spring-forward jump
	match := matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", at)
	require.NotNil(t, match)
	require.Equal(t, at.Add(2*time.Hour), match.Until.UTC())
	bound := at.Add(30 * time.Minute)
	w.ValidUntil = &bound
	match = matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", at)
	require.NotNil(t, match)
	require.Equal(t, bound, match.Until)
	require.Nil(t, matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", bound))
	w.ValidUntil = nil
	w.ValidFrom = &bound
	require.Nil(t, matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", at))
	w.ValidFrom = nil
	at = utcAt(2026, 10, 25, 0, 30) // first occurrence of 02:30 at fall-back
	match = matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{w}), facts, "hot", "MINOR", at)
	require.NotNil(t, match)
	require.Equal(t, at.Add(2*time.Hour), match.Until.UTC())
}

func TestSuppressionLatestUntilAndInvalidStored(t *testing.T) {
	a := suppressionTestWindow()
	b := suppressionTestWindow()
	b.Token = "b"
	longer := b.EndTime.Add(time.Hour)
	b.EndTime = &longer
	bad := suppressionTestWindow()
	bad.Token = "bad"
	bad.Reason = ""
	before := testutil.ToFloat64(invalidSuppressionWindows)
	windows := compileSuppressionWindows([]AlarmSuppressionWindow{a, bad, b})
	require.Len(t, windows, 2)
	require.Equal(t, before+1, testutil.ToFloat64(invalidSuppressionWindows))
	facts := deviceFacts{deviceToken: "pump"}
	require.Equal(t, "b", matchWindows(windows, facts, "hot", "MINOR", *a.StartTime).WindowToken)
	a.EndTime = b.EndTime
	require.Equal(t, "b", matchWindows(compileSuppressionWindows([]AlarmSuppressionWindow{a, b}), facts, "hot", "MINOR", *a.StartTime).WindowToken)
	// "b" sorts before "service", independent of input order.
}

func newSuppressionTestApi(t *testing.T) (*Api, context.Context, uint) {
	t.Helper()
	api := scopeAnchorsApi(t)
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&DeviceType{}, &Alarm{}, &PendingAlarmEvent{}, &AlarmDisposition{}, &AlarmSuppressionWindow{}, &AlarmCycleSuppression{}, &EntityGroupVersion{}, &EntityAttribute{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(api.RDB.Database.WithContext(core.WithSystemContext(context.Background())), &AlarmSuppressionWindow{}))
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"*"}, Tenant: "acme"}
	claims.Subject = "operator"
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "acme"), claims)
	id := seedAnchorDevice(t, api, ctx, "acme", "pump")
	return api, ctx, id
}

func suppressionCaller(ctx context.Context, notification bool, anchors ...string) context.Context {
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: []string{string(auth.AlarmWrite), string(auth.AlarmRead)}, ScopeAnchors: anchors}
	claims.Subject = "member"
	if notification {
		claims.Authorities = append(claims.Authorities, string(auth.NotificationWrite))
	}
	return auth.WithClaims(ctx, claims)
}

func TestSuppressionReachAuthorization(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	for name, edit := range map[string]func(*AlarmSuppressionWindow){
		"narrow":    func(w *AlarmSuppressionWindow) {},
		"type":      func(w *AlarmSuppressionWindow) { w.Targets[0].Type = "DEVICE_TYPE" },
		"group":     func(w *AlarmSuppressionWindow) { w.Targets[0].Type = "ENTITY_GROUP" },
		"recurring": func(w *AlarmSuppressionWindow) { *w = suppressionRecurringWindow() },
		"long":      func(w *AlarmSuppressionWindow) { end := w.StartTime.Add(24*time.Hour + time.Second); w.EndTime = &end },
	} {
		t.Run(name, func(t *testing.T) {
			w := suppressionTestWindow()
			edit(&w)
			err := api.authorizeSuppressionWrite(suppressionCaller(ctx, false), nil, &w)
			if name == "narrow" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, auth.ErrForbidden)
			}
			require.NoError(t, api.authorizeSuppressionWrite(suppressionCaller(ctx, true), nil, &w))
			narrow := suppressionTestWindow()
			err = api.authorizeSuppressionWrite(suppressionCaller(ctx, false), &w, &narrow)
			if name != "narrow" {
				require.ErrorIs(t, err, auth.ErrForbidden)
			}
			err = api.authorizeSuppressionWrite(suppressionCaller(ctx, false), &narrow, &w)
			if name != "narrow" {
				require.ErrorIs(t, err, auth.ErrForbidden)
			}
		})
	}
}

func TestSuppressionScopedAuthorization(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	caller := suppressionCaller(ctx, true, "customer:A", "group:north")
	for _, tc := range []struct {
		target AlarmSuppressionTarget
		want   bool
	}{
		{AlarmSuppressionTarget{"DEVICE", "pump"}, true}, {AlarmSuppressionTarget{"DEVICE", "missing"}, false},
		{AlarmSuppressionTarget{"CUSTOMER", "A"}, true}, {AlarmSuppressionTarget{"CUSTOMER", "B"}, false},
		{AlarmSuppressionTarget{"ENTITY_GROUP", "north"}, true}, {AlarmSuppressionTarget{"ENTITY_GROUP", "south"}, false},
		{AlarmSuppressionTarget{"AREA", "yard"}, false}, {AlarmSuppressionTarget{"TENANT", ""}, false},
	} {
		t.Run(tc.target.Type+tc.target.Token, func(t *testing.T) {
			w := suppressionTestWindow()
			w.Targets = []AlarmSuppressionTarget{tc.target}
			err := api.authorizeSuppressionWrite(caller, nil, &w)
			if tc.want {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, auth.ErrForbidden)
			}
		})
	}
	before := suppressionTestWindow()
	after := suppressionTestWindow()
	after.Targets = []AlarmSuppressionTarget{{"CUSTOMER", "A"}}
	require.ErrorIs(t, api.authorizeSuppressionWrite(caller, &before, &after), auth.ErrForbidden)
	before.Targets = append(before.Targets, AlarmSuppressionTarget{"CUSTOMER", "B"})
	require.ErrorIs(t, api.authorizeSuppressionWrite(caller, &before, nil), auth.ErrForbidden)
}

func TestSuppressionFollowsCurrentOwnership(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	edge := seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	w := suppressionTestWindow()
	created, err := api.CreateAlarmSuppressionWindow(suppressionCaller(ctx, false, "customer:A"), &w)
	require.NoError(t, err)
	require.True(t, created.CreatorScoped)
	require.Equal(t, rdb.Anchors{"customer:A"}, created.ScopeAnchors)
	// A historical alarm remains stamped A while the device itself changes hands.
	alarm := Alarm{ScopeAnchors: rdb.Anchors{"customer:A"}, OriginatorId: id, AlarmKey: "hot", RaisedTime: *w.StartTime}
	alarm.Token = "old-cycle"
	require.NoError(t, api.RDB.DB(ctx).Create(&alarm).Error)
	match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.NoError(t, api.RDB.DB(ctx).Model(edge).Update("target_token", "B").Error)
	match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.Nil(t, match, "customer A must stop suppressing after transfer to B")
}

func TestSuppressionInternalCRUDCacheAndCap(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	w := suppressionTestWindow()
	w.CreatedBy = sql.NullString{String: "spoof", Valid: true}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	require.Equal(t, "operator", created.CreatedBy.String)
	require.EqualValues(t, 1, created.Version)
	match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	reason := "updated"
	updated, err := api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version)
	require.Equal(t, w.Kind, updated.Kind)
	ended, err := api.EndAlarmSuppressionWindow(ctx, w.Token)
	require.NoError(t, err)
	require.EqualValues(t, 3, ended.Version)
	require.Equal(t, "operator", ended.EndedBy.String)
	match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match, "historical evaluation before ended_at must still match")
	for _, at := range []time.Time{*ended.EndedAt, ended.EndedAt.Add(time.Second)} {
		match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", at)
		require.NoError(t, err)
		require.Nil(t, match)
	}
	deleted, err := api.DeleteAlarmSuppressionWindow(ctx, w.Token)
	require.NoError(t, err)
	require.True(t, deleted.DeletedAt.Valid)
	require.EqualValues(t, 4, deleted.Version)
	_, err = api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	other := core.WithTenant(ctx, "other")
	rows, err := api.AlarmSuppressionWindows(other)
	require.NoError(t, err)
	require.Empty(t, rows)
	bad := suppressionTestWindow()
	bad.Token = "broken"
	bad.Reason = ""
	require.NoError(t, api.RDB.DB(ctx).Create(&bad).Error)
	_, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	// Fill the tenant to the cap through storage to avoid 500 redundant graph reads.
	for i := 0; i < 500; i++ {
		row := suppressionTestWindow()
		start := time.Now().UTC()
		end := start.Add(time.Hour)
		row.StartTime, row.EndTime = &start, &end
		row.Token = strings.Repeat("x", 1) + time.Unix(int64(i), 0).Format("150405")
		require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
	}
	w.Token = "over-cap"
	_, err = api.CreateAlarmSuppressionWindow(ctx, &w)
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
}

func TestSuppressionWholeTargetIntersection(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	seedEdge(t, api, ctx, "acme", "a", id, rt, "customer", "A")
	id2 := seedAnchorDevice(t, api, ctx, "acme", "second")
	seedEdge(t, api, ctx, "acme", "b", id2, rt, "customer", "B")
	w := suppressionTestWindow()
	w.Targets = append(w.Targets, AlarmSuppressionTarget{"DEVICE", "second"})
	anchors, err := api.suppressionAnchors(ctx, &w)
	require.NoError(t, err)
	require.Empty(t, anchors)
	seedEdge(t, api, ctx, "acme", "common", id2, rt, "customer", "A")
	anchors, err = api.suppressionAnchors(ctx, &w)
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"customer:A"}, anchors)
}

func TestSuppressionRecurringTouchingSlots(t *testing.T) {
	w := suppressionRecurringWindow()
	slots := []maintwindow.Slot{{Day: "MONDAY", Start: "01:00", DurationMinutes: 60}, {Day: "MONDAY", Start: "02:00", DurationMinutes: 60}}
	encoded, err := json.Marshal(slots)
	require.NoError(t, err)
	w.Slots = encoded
	require.NoError(t, ValidateAlarmSuppression(&w))
}

func TestSuppressionGroupUsesActivePublishedVersion(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	group := EntityGroup{MemberType: "device", MembershipMode: string(MembershipDynamic), Selector: sql.NullString{String: `!("retired" in attr)`, Valid: true}}
	group.Token = "fleet"
	require.NoError(t, api.RDB.DB(ctx).Create(&group).Error)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"ENTITY_GROUP", "fleet"}}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.Nil(t, match, "an unpublished group must match nothing")
	v1 := EntityGroupVersion{EntityGroupId: group.ID, Version: 1, MemberType: "device", Selector: `"retired" in attr`, SelectorSchema: 1}
	v2 := EntityGroupVersion{EntityGroupId: group.ID, Version: 2, MemberType: "device", Selector: `!("retired" in attr)`, SelectorSchema: 1}
	require.NoError(t, api.RDB.DB(ctx).Create(&v1).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&v2).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&group).Update("active_version", 1).Error)
	match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.Nil(t, match, "the draft and newer inactive version must not decide membership")
	require.NoError(t, api.RDB.DB(ctx).Model(&group).Update("active_version", 2).Error)
	match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
}

func TestSuppressionStaticGroupMembership(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	group := EntityGroup{MemberType: "device", MembershipMode: string(MembershipStatic)}
	group.Token = "fleet"
	require.NoError(t, api.RDB.DB(ctx).Create(&group).Error)
	rt := seedRelType(t, api, ctx, "acme", MembershipRelationshipType, false)
	edge := EntityRelationship{SourceType: "group", SourceId: group.ID, TargetType: "device", TargetId: id, TargetToken: "pump", RelationshipTypeId: rt}
	edge.Token = "member"
	require.NoError(t, api.RDB.DB(ctx).Create(&edge).Error)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"ENTITY_GROUP", "fleet"}}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.NoError(t, api.RDB.DB(ctx).Delete(&edge).Error)
	match, err = api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.Nil(t, match)
}

func TestSuppressionLoadErrorsPropagate(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	_, err := api.SuppressionFor(context.Background(), id, "hot", "MINOR", time.Now())
	require.Error(t, err)
	w := suppressionTestWindow()
	start, end := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	w.StartTime, w.EndTime = &start, &end
	_, err = api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&EntityRelationship{}))
	_, err = api.SuppressionFor(ctx, id, "hot", "MINOR", time.Now())
	require.Error(t, err)
}

func TestSuppressionPartialUpdateAuthorizesMergedAndExisting(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	w := suppressionTestWindow()
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	end := w.StartTime.Add(25 * time.Hour)
	_, err = api.UpdateAlarmSuppressionWindow(suppressionCaller(ctx, false), w.Token, &AlarmSuppressionUpdateRequest{EndTime: dcgraphql.OptionalStringOf(end.Format(time.RFC3339Nano))})
	require.ErrorIs(t, err, auth.ErrForbidden)
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.True(t, stored.EndTime.Equal(*w.EndTime))
	_, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{EndTime: dcgraphql.OptionalStringOf(end.Format(time.RFC3339Nano))})
	require.NoError(t, err)
	_, err = api.UpdateAlarmSuppressionWindow(suppressionCaller(ctx, false), w.Token, &AlarmSuppressionUpdateRequest{EndTime: dcgraphql.OptionalStringOf(w.EndTime.Format(time.RFC3339Nano))})
	require.ErrorIs(t, err, auth.ErrForbidden, "shrinking an existing broad window still needs NotificationWrite")
	_, err = api.EndAlarmSuppressionWindow(suppressionCaller(ctx, false), w.Token)
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, err = api.DeleteAlarmSuppressionWindow(suppressionCaller(ctx, false), w.Token)
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestSuppressionMalformedStoredDocumentIsSkipped(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	w := suppressionTestWindow()
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	bad := suppressionTestWindow()
	bad.Token = "bad-shape"
	require.NoError(t, api.RDB.DB(ctx).Create(&bad).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&bad).Update("alarm_keys", datatypes.JSON(`{"not":"an array"}`)).Error)
	before := testutil.ToFloat64(invalidSuppressionWindows)
	match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, w.Token, match.WindowToken)
	require.Equal(t, before+1, testutil.ToFloat64(invalidSuppressionWindows))
}

func TestSuppressionCacheReplicaTTLAndTenantIsolation(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	w := suppressionTestWindow()
	start := time.Now().UTC().Add(-time.Minute)
	end := start.Add(time.Hour)
	w.StartTime, w.EndTime = &start, &end
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	// An unrelated high version must not mask an edit to a lower version.
	high := suppressionTestWindow()
	high.StartTime, high.EndTime = &start, &end
	high.Token, high.Version = "unrelated", 100
	high.Targets = []AlarmSuppressionTarget{{"DEVICE", "elsewhere"}}
	require.NoError(t, api.RDB.DB(ctx).Create(&high).Error)
	replica := NewApi(api.RDB)
	match, err := replica.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Empty(t, match.WindowName)
	require.NoError(t, api.RDB.DB(ctx).Model(&AlarmSuppressionWindow{}).Where("token = ?", w.Token).Updates(map[string]any{"name": "new name", "version": 2}).Error)
	match, err = replica.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "new name", match.WindowName, "sum(version) detects the edit before the TTL")
	require.NoError(t, api.RDB.DB(ctx).Model(&AlarmSuppressionWindow{}).Where("token = ?", w.Token).Update("name", "TTL name").Error)
	// TTL still refreshes writes whose signature is unchanged. Fresh device facts
	// are never part of this tenant cache.
	replica.suppressionCache.Lock()
	entry := replica.suppressionCache.tenants["acme"]
	entry.loaded = time.Now().Add(-5 * time.Second)
	replica.suppressionCache.tenants["acme"] = entry
	replica.suppressionCache.Unlock()
	match, err = replica.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "TTL name", match.WindowName)
	other := core.WithTenant(ctx, "other")
	otherID := seedAnchorDevice(t, api, other, "other", "pump")
	match, err = replica.SuppressionFor(other, otherID, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.Nil(t, match, "cached windows must not cross tenant boundaries")
}

func TestSuppressionPartialNullableFieldsAndScheduleSwitch(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	w := suppressionTestWindow()
	w.Name = sql.NullString{String: "original", Valid: true}
	w.Description = sql.NullString{String: "description", Valid: true}
	metadata := datatypes.JSON(`{"work":"original"}`)
	w.Metadata = &metadata
	w.AlarmKeys = []string{"hot"}
	w.BypassSeverity = sql.NullString{String: "MAJOR", Valid: true}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	reason := "new reason"
	updated, err := api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
	require.Equal(t, w.Name, updated.Name)
	require.Equal(t, w.Description, updated.Description)
	require.Equal(t, string(*w.Metadata), string(*updated.Metadata))
	require.Equal(t, w.AlarmKeys, updated.AlarmKeys)
	require.Equal(t, w.BypassSeverity, updated.BypassSeverity)
	updated, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{
		Name: dcgraphql.ClearedString(), Description: dcgraphql.ClearedString(), Metadata: dcgraphql.ClearedString(),
		BypassSeverity: dcgraphql.ClearedString(), AlarmKeys: dcgraphql.ClearedStringList(),
	})
	require.NoError(t, err)
	require.False(t, updated.Name.Valid)
	require.False(t, updated.Description.Valid)
	require.Nil(t, updated.Metadata)
	require.Empty(t, updated.AlarmKeys)
	require.False(t, updated.BypassSeverity.Valid)
	name := sql.NullString{String: "replacement", Valid: true}
	metadata = datatypes.JSON(`{"work":"replacement"}`)
	keys := []string{"cold"}
	bypass := sql.NullString{String: "CRITICAL", Valid: true}
	updated, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{
		Name: dcgraphql.OptionalStringOf(name.String), Description: dcgraphql.OptionalStringOf(name.String), Metadata: dcgraphql.OptionalStringOf(string(metadata)), AlarmKeys: dcgraphql.OptionalStringListOf(keys), BypassSeverity: dcgraphql.OptionalStringOf(bypass.String),
	})
	require.NoError(t, err)
	require.Equal(t, name, updated.Name)
	require.Equal(t, name, updated.Description)
	require.Equal(t, string(metadata), string(*updated.Metadata))
	require.Equal(t, keys, updated.AlarmKeys)
	require.Equal(t, bypass, updated.BypassSeverity)
	_, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{StartTime: dcgraphql.ClearedString(), Name: dcgraphql.ClearedString()})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.Equal(t, name, stored.Name, "an invalid merged schedule must not write the accompanying name edit")
	recurring := suppressionRecurringWindow()
	from, until := *w.StartTime, w.StartTime.Add(7*24*time.Hour)
	updated, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{
		Kind: OptionalAlarmSuppressionKind{Set: true, Value: func() *AlarmSuppressionKind { k := AlarmSuppressionKind(recurring.Kind); return &k }()}, StartTime: dcgraphql.ClearedString(), EndTime: dcgraphql.ClearedString(),
		Timezone: dcgraphql.OptionalStringOf(recurring.Timezone.String), Slots: dcgraphql.OptionalStringOf(string(recurring.Slots)), ValidFrom: dcgraphql.OptionalStringOf(from.Format(time.RFC3339Nano)), ValidUntil: dcgraphql.OptionalStringOf(until.Format(time.RFC3339Nano)),
	})
	require.NoError(t, err)
	require.Equal(t, SuppressionRecurring, updated.Kind)
	require.Nil(t, updated.StartTime)
	require.Nil(t, updated.EndTime)
	updated, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{ValidFrom: dcgraphql.ClearedString(), ValidUntil: dcgraphql.ClearedString()})
	require.NoError(t, err)
	require.Nil(t, updated.ValidFrom)
	require.Nil(t, updated.ValidUntil)
	_, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Timezone: dcgraphql.ClearedString()})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
}

func TestSuppressionCreatorFullScopeSurvivesTransfer(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	edge := seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	w := suppressionTestWindow()
	w.CreatorScope = rdb.Anchors{"customer:spoof"}
	created, err := api.CreateAlarmSuppressionWindow(suppressionCaller(ctx, false, "customer:A", "customer:B"), &w)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"customer:A", "customer:B"}, created.CreatorScope)
	require.Equal(t, rdb.Anchors{"customer:A"}, created.ScopeAnchors)
	for _, owner := range []string{"A", "B", "C"} {
		require.NoError(t, api.RDB.DB(ctx).Model(edge).Update("target_token", owner).Error)
		match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", *w.StartTime)
		require.NoError(t, err)
		require.Equal(t, owner != "C", match != nil)
	}
	reason := "admin edit"
	updated, err := api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
	require.Equal(t, created.CreatorScope, updated.CreatorScope)
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.Equal(t, created.CreatorScope, stored.CreatorScope)
}

func TestSuppressionExpiredWindowsDoNotConsumeCap(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	for i := 0; i < 500; i++ {
		w := suppressionTestWindow()
		w.Token = fmt.Sprintf("expired-%d", i)
		require.NoError(t, api.RDB.DB(ctx).Create(&w).Error)
	}
	w := suppressionTestWindow()
	start := time.Now().UTC()
	end := start.Add(time.Hour)
	w.StartTime, w.EndTime = &start, &end
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	windows, err := api.suppressionWindows(ctx)
	require.NoError(t, err)
	require.Len(t, windows, 1)
}

func TestSuppressionVersionConflictAndSingleConnection(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	w := suppressionTestWindow()
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	db, err := api.RDB.Database.DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	timed, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = api.mutateSuppression(timed, w.Token, func(result *AlarmSuppressionWindow) error {
		// Deterministically interleave a second writer after the read, before CAS.
		require.NoError(t, api.RDB.DB(timed).Model(&AlarmSuppressionWindow{}).Where("token = ?", w.Token).
			Updates(map[string]any{"reason": "concurrent writer", "version": 2}).Error)
		result.Reason = "stale writer"
		return nil
	}, false, false)
	var conflict *SuppressionConflictError
	require.ErrorAs(t, err, &conflict)
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.Equal(t, "concurrent writer", stored.Reason)
	require.EqualValues(t, 2, stored.Version)
	seedAnchorDevice(t, api, ctx, "acme", "second")
	targets := []AlarmSuppressionTarget{{"DEVICE", "second"}}
	_, err = api.UpdateAlarmSuppressionWindow(timed, w.Token, &AlarmSuppressionUpdateRequest{Targets: OptionalAlarmSuppressionTargets{Set: true, Value: targets}})
	require.NoError(t, err, "anchor computation must not hold the only pooled connection")
}

func TestSuppressionDeviceTypeIgnoresCallerScope(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	typ := DeviceType{}
	typ.Token = "pumps"
	require.NoError(t, api.RDB.DB(ctx).Create(&typ).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", id).Update("device_type_id", typ.ID).Error)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"DEVICE_TYPE", "pumps"}}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	match, err := api.SuppressionFor(suppressionCaller(ctx, false, "customer:A"), id, "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	require.NotNil(t, match)
}

func TestSuppressionCacheDoesNotLockDuringDatabaseWork(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	calls := 0
	check := func(_ *gorm.DB) {
		calls++
		unlocked := api.suppressionCache.TryLock()
		require.True(t, unlocked, "cache mutex held during database work")
		if unlocked {
			api.suppressionCache.Unlock()
		}
	}
	require.NoError(t, api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:suppression-cache-unlocked", check))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Row().Remove("test:suppression-cache-unlocked") })
	_, err := api.suppressionWindows(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, calls, 2, "signature and window load must both be observed")
}

func TestSuppressionHistoricalAndRecentEndedWindows(t *testing.T) {
	for _, age := range []time.Duration{time.Minute, time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			api, ctx, id := newSuppressionTestApi(t)
			start := time.Now().UTC().Add(-age)
			end := start.Add(2 * time.Hour)
			ended := start.Add(30 * time.Second)
			w := suppressionTestWindow()
			w.StartTime, w.EndTime, w.EndedAt = &start, &end, &ended
			require.NoError(t, api.RDB.DB(ctx).Create(&w).Error)
			for _, at := range []time.Time{start, ended.Add(-time.Nanosecond), ended, ended.Add(time.Second)} {
				match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", at)
				require.NoError(t, err)
				require.Equal(t, at.Before(ended), match != nil)
			}
			if age > 10*time.Minute {
				require.Empty(t, api.suppressionCache.tenants)
			}
		})
	}
}

func TestSuppressionCurrentTargetScopeAfterTransfer(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	edge := seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	w := suppressionTestWindow()
	for _, owner := range []string{"A", "B"} {
		require.NoError(t, api.RDB.DB(ctx).Model(edge).Update("target_token", owner).Error)
		for _, scope := range []string{"A", "B"} {
			visible, err := api.suppressionTargetsInScope(suppressionCaller(ctx, false, "customer:"+scope), &w)
			require.NoError(t, err)
			require.Equal(t, owner == scope, visible)
		}
	}
	w.Targets = append(w.Targets, AlarmSuppressionTarget{"CUSTOMER", "A"})
	visible, err := api.suppressionTargetsInScope(suppressionCaller(ctx, true, "customer:B"), &w)
	require.NoError(t, err)
	require.False(t, visible, "every target must be currently within scope")
}

func TestSuppressionEndIsIdempotent(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	now := time.Now().UTC()
	start, end := now.Add(-2*time.Hour), now.Add(time.Hour)
	w := suppressionTestWindow()
	w.StartTime, w.EndTime = &start, &end
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	first, err := api.EndAlarmSuppressionWindow(ctx, w.Token)
	require.NoError(t, err)
	// Store T1 in the past so both historical sides can be checked without sleeping.
	t1 := now.Add(-time.Hour)
	require.NoError(t, api.RDB.DB(ctx).Model(first).Update("ended_at", t1).Error)
	first, err = api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	_, err = api.EndAlarmSuppressionWindow(auth.WithClaims(ctx, &auth.Claims{Tenant: "acme"}), w.Token)
	require.ErrorIs(t, err, auth.ErrForbidden, "an ended window still requires authorization")
	second, err := api.EndAlarmSuppressionWindow(suppressionCaller(ctx, false), w.Token)
	require.NoError(t, err)
	require.Equal(t, first.EndedAt, second.EndedAt)
	require.Equal(t, first.EndedBy, second.EndedBy)
	require.Equal(t, first.Version, second.Version)
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.Equal(t, first, stored, "a repeated end must not write the row")
	for _, at := range []time.Time{t1.Add(-time.Second), t1, t1.Add(time.Minute)} {
		match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", at)
		require.NoError(t, err)
		require.Equal(t, at.Before(t1), match != nil)
	}
}

func TestSuppressionReviveRespectsCap(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	now := time.Now().UTC()
	start, expiredEnd, liveEnd := now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(time.Hour)
	w := suppressionTestWindow()
	w.StartTime, w.EndTime = &start, &expiredEnd
	expired, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	for i := 0; i < 500; i++ {
		live := suppressionTestWindow()
		live.Token = fmt.Sprintf("live-%d", i)
		live.StartTime, live.EndTime = &start, &liveEnd
		require.NoError(t, api.RDB.DB(ctx).Create(&live).Error)
	}
	_, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{EndTime: dcgraphql.OptionalStringOf(liveEnd.Format(time.RFC3339Nano))})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	require.EqualError(t, err, suppressionInvalid("at most 500 live windows per tenant").Error())
	stored, err := api.AlarmSuppressionWindowByToken(ctx, w.Token)
	require.NoError(t, err)
	require.Equal(t, expired.Version, stored.Version)
	require.True(t, stored.EndTime.Equal(expiredEnd))
	reason := "edited at cap"
	updated, err := api.UpdateAlarmSuppressionWindow(ctx, "live-0", &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
	require.Equal(t, reason, updated.Reason)
	_, err = api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err, "an expired window that stays expired consumes no capacity")
}

func TestSuppressionNegativeCacheIsTenantLocalAndInvalidated(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	count := 0
	counter := func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "alarm_suppression_windows" {
			count++
		}
	}
	db := api.RDB.Database
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:negative", counter))
	t.Cleanup(func() { _ = db.Callback().Row().Remove("test:negative") })
	_, err := api.suppressionWindows(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	_, err = api.suppressionWindows(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count, "empty tenant needs no repeat database query")
	_, err = api.suppressionWindows(core.WithTenant(ctx, "other"))
	require.NoError(t, err)
	require.Equal(t, 4, count, "negative cache is tenant-local")
	w := suppressionTestWindow()
	_, err = api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	api.suppressionCache.Lock()
	_, exists := api.suppressionCache.tenants["acme"]
	api.suppressionCache.Unlock()
	require.False(t, exists, "local creation invalidates the negative cache")
	_, err = api.suppressionWindows(ctx)
	require.NoError(t, err)
	require.Equal(t, 6, count)
	api.suppressionCache.Lock()
	entry := api.suppressionCache.tenants["other"]
	entry.loaded = time.Now().Add(-5 * time.Second)
	api.suppressionCache.tenants["other"] = entry
	api.suppressionCache.Unlock()
	_, err = api.suppressionWindows(core.WithTenant(ctx, "other"))
	require.NoError(t, err)
	require.Equal(t, 8, count, "negative cache expires at five seconds")
}
