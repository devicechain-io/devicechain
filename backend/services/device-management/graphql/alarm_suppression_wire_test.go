// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const suppressionSelection = "token name description metadata reason targets { type token } alarmKeys bypassSeverity kind startTime endTime timezone slots validFrom validUntil createdBy endedBy endedAt status openings { start end }"
const suppressionCreateDoc = "mutation($request: AlarmSuppressionWindowCreateRequest!) { createAlarmSuppressionWindow(request:$request) { " + suppressionSelection + " } }"
const suppressionUpdateDoc = "mutation($token:String!,$request:AlarmSuppressionWindowUpdateRequest!) { updateAlarmSuppressionWindow(token:$token,request:$request) { " + suppressionSelection + " } }"

// Exact document from notification-management/processor/suppression_lookup.go.
const notificationSuppressionDocument = "query AlarmSuppressionStatus($tokens: [String!]!) { alarmSuppressionStatus(tokens: $tokens) { alarmToken severity state suppression { windowToken until } } }"

func suppressionWireFixture(t *testing.T) scopeFixture {
	f := newScopeFixture(t)
	suppressionSQLiteOverlap(t, f.db)
	require.NoError(t, f.db.AutoMigrate(&model.AlarmSuppressionWindow{}, &model.AlarmCycleSuppression{}, &model.AlarmDisposition{}, &model.EntityGroup{}))
	require.NoError(t, model.RegisterPositionWithholding(f.db))
	return f
}
func suppressionWireCaller(f scopeFixture, broad, write bool) context.Context {
	authorities := []string{string(auth.AlarmRead), string(auth.DeviceRead)}
	if write {
		authorities = append(authorities, string(auth.AlarmWrite))
	}
	if broad {
		authorities = append(authorities, string(auth.NotificationWrite))
	}
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant, Authorities: authorities}
	claims.Subject = "operator-subject"
	return f.withClaims(claims)
}
func suppressionWireInput(token, typ, target string) map[string]any {
	now := time.Now().UTC()
	targetInput := map[string]any{"type": typ}
	if target != "" {
		targetInput["token"] = target
	}
	return map[string]any{"token": token, "name": "Service work", "reason": "replace driver", "kind": "ONE_SHOT", "targets": []any{targetInput}, "startTime": now.Add(-time.Hour).Format(time.RFC3339Nano), "endTime": now.Add(time.Hour).Format(time.RFC3339Nano)}
}
func suppressionCreate(t *testing.T, ctx context.Context, input map[string]any) map[string]any {
	t.Helper()
	out := serve(t, ctx, suppressionCreateDoc, map[string]any{"request": input})
	requireServed(t, out, "create window")
	return out.data["createAlarmSuppressionWindow"].(map[string]any)
}
func suppressionUpdate(t *testing.T, ctx context.Context, token string, input map[string]any) served {
	t.Helper()
	return serve(t, ctx, suppressionUpdateDoc, map[string]any{"token": token, "request": input})
}
func suppressionRefused(t *testing.T, out served, code string) {
	t.Helper()
	require.NotEmpty(t, out.errs)
	if code != "" {
		found := false
		for _, err := range out.errs {
			if err.Extensions["code"] == code {
				found = true
			}
		}
		require.True(t, found, "want %s: %v", code, out.errs)
	}
}
func TestAlarmSuppressionWireCRUDAndReadSurfaces(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	first := suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	require.Equal(t, "operator-subject", first["createdBy"])
	require.Nil(t, first["endedBy"])
	require.Equal(t, "ACTIVE", first["status"])
	require.Len(t, first["openings"], 1)
	nullLimit := serve(t, ctx, "{alarmSuppressionWindowsByToken(tokens:[\"work\"]){openings(limit:null){start end}}}", nil)
	requireServed(t, nullLimit, "null openings limit uses default")
	require.Len(t, nullLimit.data["alarmSuppressionWindowsByToken"].([]any)[0].(map[string]any)["openings"], 1)
	require.Empty(t, first["alarmKeys"])
	for _, query := range []string{
		"{ alarmSuppressionWindowsByToken(tokens:[\"work\",\"unknown\"]) { token targets { type token } } }",
		"{ alarmSuppressionsForDevice(deviceToken:\"acme-pump\") { token } }",
	} {
		out := serve(t, ctx, query, nil)
		requireServed(t, out, "read")
		for _, rows := range out.data {
			require.Len(t, rows, 1)
		}
	}
	future := suppressionWireInput("future", "DEVICE", "acme-pump")
	future["startTime"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	future["endTime"] = time.Now().Add(3 * time.Hour).Format(time.RFC3339)
	suppressionCreate(t, ctx, future)
	out := serve(t, ctx, "{ alarmSuppressionWindows(criteria:{pageNumber:1,pageSize:1,targetToken:\"acme-pump\"}) { results { token } pagination { totalRecords } } }", nil)
	requireServed(t, out, "page")
	page := out.data["alarmSuppressionWindows"].(map[string]any)
	require.Len(t, page["results"], 1)
	require.EqualValues(t, 2, page["pagination"].(map[string]any)["totalRecords"])
	out = serve(t, ctx, "{ alarmSuppressionWindows(criteria:{pageNumber:1,pageSize:10,status:\"ACTIVE\",targetToken:\"acme-pump\"}) { results { token } pagination { totalRecords } } }", nil)
	requireServed(t, out, "filtered page")
	require.Equal(t, []string{"work"}, tokensAt(t, out.data, "alarmSuppressionWindows", "results"))
	edited := suppressionUpdate(t, ctx, "work", map[string]any{"reason": "extended"})
	requireServed(t, edited, "update")
	require.Equal(t, "extended", edited.data["updateAlarmSuppressionWindow"].(map[string]any)["reason"])
	ended := serve(t, ctx, "mutation { endAlarmSuppressionWindow(token:\"work\") { token endedAt endedBy status } }", nil)
	requireServed(t, ended, "end")
	e := ended.data["endAlarmSuppressionWindow"].(map[string]any)
	require.Equal(t, "operator-subject", e["endedBy"])
	require.NotNil(t, e["endedAt"])
	require.Equal(t, "ENDED", e["status"])
	deleted := serve(t, ctx, "mutation { deleteAlarmSuppressionWindow(token:\"work\") }", nil)
	requireServed(t, deleted, "delete")
	require.Equal(t, true, deleted.data["deleteAlarmSuppressionWindow"])
	gone := serve(t, ctx, "{ alarmSuppressionWindowsByToken(tokens:[\"work\"]) { token } }", nil)
	requireServed(t, gone, "deleted read")
	require.Empty(t, gone.data["alarmSuppressionWindowsByToken"])
}
func TestAlarmSuppressionWireThreeStatesAndOwnedFields(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	unchanged := suppressionUpdate(t, ctx, "work", map[string]any{})
	requireServed(t, unchanged, "omitted")
	require.Equal(t, "replace driver", unchanged.data["updateAlarmSuppressionWindow"].(map[string]any)["reason"])
	for _, field := range []string{"reason", "targets", "kind"} {
		refused := suppressionUpdate(t, ctx, "work", map[string]any{field: nil, "name": "must not write"})
		suppressionRefused(t, refused, "INVALID_VALUE")
		got, err := f.api.AlarmSuppressionWindowByToken(ctx, "work")
		require.NoError(t, err)
		require.Equal(t, "Service work", got.Name.String)
		require.Equal(t, "replace driver", got.Reason)
	}
	for _, field := range []string{"name", "description", "metadata", "alarmKeys", "bypassSeverity"} {
		value := any("value")
		if field == "metadata" {
			value = "{\"note\":\"work\"}"
		}
		if field == "alarmKeys" {
			value = []any{"hot"}
		}
		if field == "bypassSeverity" {
			value = "CRITICAL"
		}
		requireServed(t, suppressionUpdate(t, ctx, "work", map[string]any{field: value}), "set "+field)
		cleared := suppressionUpdate(t, ctx, "work", map[string]any{field: nil})
		requireServed(t, cleared, "clear "+field)
		got := cleared.data["updateAlarmSuppressionWindow"].(map[string]any)[field]
		if field == "alarmKeys" {
			require.Empty(t, got)
		} else {
			require.Nil(t, got)
		}
	}
	recurring := map[string]any{"kind": "RECURRING", "startTime": nil, "endTime": nil, "timezone": "UTC", "slots": "[{\"day\":\"MONDAY\",\"start\":\"10:00\",\"durationMinutes\":60}]", "validFrom": time.Now().Add(-time.Hour).Format(time.RFC3339), "validUntil": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)}
	requireServed(t, suppressionUpdate(t, ctx, "work", recurring), "switch to recurring")
	for _, field := range []string{"validFrom", "validUntil"} {
		requireServed(t, suppressionUpdate(t, ctx, "work", map[string]any{field: nil}), "clear "+field)
	}
	for _, field := range []string{"createdBy", "endedBy", "endedAt", "creatorScope", "version", "token"} {
		input := suppressionWireInput("owned-"+strings.ToLower(field), "DEVICE", "acme-pump")
		input[field] = "forged"
		if field != "token" {
			suppressionRefused(t, serve(t, ctx, suppressionCreateDoc, map[string]any{"request": input}), "")
		}
		suppressionRefused(t, suppressionUpdate(t, ctx, "work", map[string]any{field: "forged"}), "")
	}
	for _, field := range []string{"startTime", "endTime", "validFrom", "validUntil"} {
		input := suppressionWireInput("bad-"+strings.ToLower(field), "DEVICE", "acme-pump")
		input[field] = "yesterday"
		suppressionRefused(t, serve(t, ctx, suppressionCreateDoc, map[string]any{"request": input}), "INVALID_VALUE")
		suppressionRefused(t, suppressionUpdate(t, ctx, "work", map[string]any{field: "yesterday"}), "INVALID_VALUE")
	}
	suppressionRefused(t, serve(t, ctx, "{alarmSuppressionsForDevice(deviceToken:\"acme-pump\",at:\"bad\"){token}}", nil), "INVALID_VALUE")
}

func TestAlarmSuppressionWireAuthorityMatrix(t *testing.T) {
	f := suppressionWireFixture(t)
	narrow := suppressionWireCaller(f, false, true)
	broad := suppressionWireCaller(f, true, true)
	read := suppressionWireCaller(f, true, false)
	suppressionCreate(t, narrow, suppressionWireInput("narrow", "DEVICE", "acme-pump"))
	for _, typ := range []string{"DEVICE_TYPE", "ENTITY_GROUP", "AREA", "ASSET", "CUSTOMER", "TENANT", "RECURRING", "LONG"} {
		t.Run(typ, func(t *testing.T) {
			target := "pump"
			switch typ {
			case "ENTITY_GROUP":
				target = "fleet"
			case "AREA":
				target = "floor-acme"
			case "ASSET":
				target = "asset-acme"
			case "CUSTOMER":
				target = "acme"
			case "TENANT":
				target = ""
			}
			input := suppressionWireInput("broad-"+strings.ToLower(typ), typ, target)
			if typ == "RECURRING" {
				input = suppressionWireInput("recurring", "DEVICE", "acme-pump")
				input["kind"] = "RECURRING"
				delete(input, "startTime")
				delete(input, "endTime")
				input["timezone"] = "UTC"
				input["slots"] = "[{\"day\":\"MONDAY\",\"start\":\"10:00\",\"durationMinutes\":60}]"
			}
			if typ == "LONG" {
				input = suppressionWireInput("long", "DEVICE", "acme-pump")
				input["endTime"] = time.Now().Add(25 * time.Hour).Format(time.RFC3339)
			}
			suppressionRefused(t, serve(t, narrow, suppressionCreateDoc, map[string]any{"request": input}), "FORBIDDEN")
			suppressionCreate(t, broad, input)
			token := input["token"].(string)
			for _, op := range []string{"endAlarmSuppressionWindow", "deleteAlarmSuppressionWindow"} {
				selection := ""
				if strings.HasPrefix(op, "end") {
					selection = "{token}"
				}
				response := serve(t, narrow, fmt.Sprintf("mutation{%s(token:%q)%s}", op, token, selection), nil)
				if strings.HasPrefix(op, "end") {
					requireServed(t, response, "alarm:write can end a broad visible window")
				} else {
					suppressionRefused(t, response, "FORBIDDEN")
				}
			}
		})
	}
	suppressionRefused(t, serve(t, read, suppressionCreateDoc, map[string]any{"request": suppressionWireInput("read-only", "DEVICE", "acme-pump")}), "FORBIDDEN")
	longEnd := time.Now().Add(25 * time.Hour).Format(time.RFC3339)
	suppressionRefused(t, suppressionUpdate(t, narrow, "narrow", map[string]any{"endTime": longEnd}), "FORBIDDEN")
	requireServed(t, suppressionUpdate(t, broad, "narrow", map[string]any{"endTime": longEnd}), "broaden update")
	suppressionRefused(t, suppressionUpdate(t, narrow, "narrow", map[string]any{"endTime": time.Now().Add(time.Hour).Format(time.RFC3339)}), "FORBIDDEN")
}
func TestAlarmSuppressionWireScopeAndHiddenWindow(t *testing.T) {
	f := suppressionWireFixture(t)
	operator := suppressionWireCaller(f, true, true)
	member := f.member("customer:acme")
	suppressionCreate(t, operator, suppressionWireInput("own", "DEVICE", "acme-pump"))
	suppressionCreate(t, operator, suppressionWireInput("other", "DEVICE", "globex-pump"))
	mixed := suppressionWireInput("mixed", "DEVICE", "acme-pump")
	mixed["targets"] = []any{map[string]any{"type": "DEVICE", "token": "acme-pump"}, map[string]any{"type": "DEVICE", "token": "globex-pump"}}
	suppressionRefused(t, serve(t, member, suppressionCreateDoc, map[string]any{"request": mixed}), "FORBIDDEN")
	suppressionCreate(t, operator, mixed)
	for _, token := range []string{"other", "mixed"} {
		suppressionRefused(t, suppressionUpdate(t, member, token, map[string]any{"reason": "edit"}), "")
		suppressionRefused(t, serve(t, member, fmt.Sprintf("mutation{endAlarmSuppressionWindow(token:%q){token}}", token), nil), "")
		suppressionRefused(t, serve(t, member, fmt.Sprintf("mutation{deleteAlarmSuppressionWindow(token:%q)}", token), nil), "")
	}
	list := serve(t, member, "{alarmSuppressionWindows(criteria:{pageNumber:1,pageSize:1}){results{token} pagination{totalRecords}} alarmSuppressionWindowsByToken(tokens:[\"own\",\"other\",\"mixed\"]){token}}", nil)
	requireServed(t, list, "scoped list")
	require.Equal(t, []string{"own"}, tokensAt(t, list.data, "alarmSuppressionWindows", "results"))
	require.Equal(t, []string{"own"}, tokensAt(t, list.data, "alarmSuppressionWindowsByToken"))
	require.EqualValues(t, 1, list.data["alarmSuppressionWindows"].(map[string]any)["pagination"].(map[string]any)["totalRecords"])
	// The visible alarm is held by a window that also names another customer's
	// device. Hidden window tokens are opaque and the whole window is null.
	requireServed(t, serve(t, operator, "mutation{endAlarmSuppressionWindow(token:\"own\"){token}}", nil), "end own")
	devices, err := f.api.DevicesByToken(operator, []string{"acme-pump"})
	require.NoError(t, err)
	require.Len(t, devices, 1)
	nonPosition := false
	alarm := model.Alarm{TokenReference: rdb.TokenReference{Token: "visible-alarm"}, OriginatorType: "device", OriginatorId: devices[0].ID, AlarmKey: "hot", Severity: "MINOR", State: "ACTIVE", RaisedTime: time.Now().UTC(), ScopeAnchors: rdb.Anchors{"customer:acme"}, PositionDerived: &nonPosition}
	require.NoError(t, f.db.WithContext(operator).Create(&alarm).Error)
	hidden := serve(t, member, "{alarmsByToken(tokens:[\"visible-alarm\"]){suppression{windowToken until window{token targets{token}}}}}", nil)
	requireServed(t, hidden, "hidden window")
	suppression := hidden.data["alarmsByToken"].([]any)[0].(map[string]any)["suppression"].(map[string]any)
	require.Equal(t, "restricted", suppression["windowToken"])
	require.NotEmpty(t, suppression["until"])
	require.Nil(t, suppression["window"])
	control := serve(t, operator, "{alarmsByToken(tokens:[\"visible-alarm\"]){suppression{window{token}}}}", nil)
	requireServed(t, control, "operator window")
	require.Contains(t, fmt.Sprint(control.data), "mixed")
	require.NoError(t, f.db.WithContext(operator).Create(&model.AlarmCycleSuppression{AlarmId: alarm.ID, AlarmToken: alarm.Token, RaisedTime: alarm.RaisedTime, WindowToken: "mixed", ScopeAnchors: alarm.ScopeAnchors}).Error)
	service := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant, Authorities: []string{string(auth.AlarmRead)}})
	for _, caller := range []struct {
		ctx  context.Context
		want string
	}{{member, "restricted"}, {operator, "mixed"}, {service, "mixed"}} {
		out := serve(t, caller.ctx, notificationSuppressionDocument, map[string]any{"tokens": []any{alarm.Token}})
		requireServed(t, out, "status token visibility")
		got := out.data["alarmSuppressionStatus"].([]any)[0].(map[string]any)["suppression"].(map[string]any)
		require.Equal(t, caller.want, got["windowToken"], "service status must always preserve the real token for notification-management")
		if caller.ctx == service {
			continue
		}
		out = serve(t, caller.ctx, "{alarmsByToken(tokens:[\"visible-alarm\"]){raisedDuringSuppression}}", nil)
		requireServed(t, out, "history token visibility")
		require.Equal(t, caller.want, out.data["alarmsByToken"].([]any)[0].(map[string]any)["raisedDuringSuppression"])
	}
	sdl := strings.Replace(SchemaContent, "query: Query", "query: SuppressionEventQuery", 1)
	sdl = strings.Replace(sdl, "mutation: Mutation", "", 1)
	sdl = strings.Replace(sdl, "subscription: Subscription", "", 1)
	sdl += "\ntype SuppressionEventQuery { event: AlarmEvent! }"
	for _, caller := range []struct {
		ctx  context.Context
		want string
	}{{member, "restricted"}, {operator, "mixed"}, {service, "mixed"}} {
		event := &model.AlarmStateChangeEvent{SuppressionWindowToken: "mixed", RaisedDuringSuppressionToken: "mixed"}
		schema := gql.MustParseSchema(sdl, &suppressionEventRoot{E: &AlarmEventResolver{E: event, S: &SchemaResolver{}, C: caller.ctx}})
		out := schema.Exec(caller.ctx, "{event{suppressionWindowToken raisedDuringSuppression}}", "", nil)
		require.Empty(t, out.Errors)
		var data struct{ Event map[string]string }
		require.NoError(t, json.Unmarshal(out.Data, &data))
		require.Equal(t, caller.want, data.Event["suppressionWindowToken"])
		require.Equal(t, caller.want, data.Event["raisedDuringSuppression"])
		require.Equal(t, "mixed", event.SuppressionWindowToken, "subscriber redaction must not mutate shared events")
	}
	require.NoError(t, f.db.WithContext(operator).Where("token = ?", "mixed").Delete(&model.AlarmSuppressionWindow{}).Error)
	out := serve(t, member, "{alarmsByToken(tokens:[\"visible-alarm\"]){raisedDuringSuppression}}", nil)
	requireServed(t, out, "deleted window history")
	require.Equal(t, "restricted", out.data["alarmsByToken"].([]any)[0].(map[string]any)["raisedDuringSuppression"])

}
func TestAlarmSuppressionWireStatusHistoryAndSearch(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	devices, err := f.api.DevicesByToken(ctx, []string{"acme-pump"})
	require.NoError(t, err)
	require.NoError(t, f.api.ApplyAlarmContributorEdge(ctx, devices[0].ID, "hot", "temperature", "rule", "raised", "MINOR", nil, time.Now().UTC(), new(bool)))
	var alarm model.Alarm
	require.NoError(t, f.db.WithContext(ctx).Where("alarm_key = ?", "hot").First(&alarm).Error)
	out := serve(t, ctx, notificationSuppressionDocument, map[string]any{"tokens": []any{alarm.Token, "unknown"}})
	requireServed(t, out, "notification document")
	raw, err := json.Marshal(out.data)
	require.NoError(t, err)
	var decoded struct {
		AlarmSuppressionStatus []struct {
			AlarmToken  string
			Severity    string
			State       string
			Suppression *struct{ WindowToken, Until string }
		}
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.AlarmSuppressionStatus, 1)
	require.Equal(t, alarm.Token, decoded.AlarmSuppressionStatus[0].AlarmToken)
	require.Equal(t, "MINOR", decoded.AlarmSuppressionStatus[0].Severity)
	require.Equal(t, "ACTIVE", decoded.AlarmSuppressionStatus[0].State)
	require.Equal(t, "work", decoded.AlarmSuppressionStatus[0].Suppression.WindowToken)
	until, err := time.Parse(time.RFC3339Nano, decoded.AlarmSuppressionStatus[0].Suppression.Until)
	require.NoError(t, err)
	require.True(t, until.After(time.Now()))
	tokens := make([]any, 200)
	for i := range tokens {
		tokens[i] = alarm.Token
	}
	requireServed(t, serve(t, ctx, notificationSuppressionDocument, map[string]any{"tokens": tokens}), "200 tokens")
	suppressionRefused(t, serve(t, ctx, notificationSuppressionDocument, map[string]any{"tokens": append(tokens, "unknown")}), "INVALID_VALUE")
	for _, stamped := range []bool{true, false} {
		search := serve(t, ctx, fmt.Sprintf("{alarms(criteria:{pageNumber:1,pageSize:10,raisedDuringSuppression:%t}){results{token raisedDuringSuppression suppression{windowToken}} pagination{totalRecords}}}", stamped), nil)
		requireServed(t, search, "history filter")
		rows := search.data["alarms"].(map[string]any)["results"].([]any)
		if stamped {
			require.Len(t, rows, 1)
			require.Equal(t, "work", rows[0].(map[string]any)["raisedDuringSuppression"])
		} else {
			require.Empty(t, rows)
		}
	}
	require.NoError(t, f.db.WithContext(ctx).Model(&alarm).Update("raised_time", alarm.RaisedTime.Add(time.Hour)).Error)
	history := serve(t, ctx, fmt.Sprintf("{alarmsByToken(tokens:[%q]){raisedDuringSuppression}}", alarm.Token), nil)
	requireServed(t, history, "new cycle")
	require.Nil(t, history.data["alarmsByToken"].([]any)[0].(map[string]any)["raisedDuringSuppression"])
	for _, change := range []map[string]any{{"state": "CLEARED"}, {"state": "ACTIVE", "originator_type": "asset"}} {
		require.NoError(t, f.db.WithContext(ctx).Model(&alarm).Updates(change).Error)
		quiet := serve(t, ctx, notificationSuppressionDocument, map[string]any{"tokens": []any{alarm.Token}})
		requireServed(t, quiet, "not suppressed")
		require.Nil(t, quiet.data["alarmSuppressionStatus"].([]any)[0].(map[string]any)["suppression"])
	}
}

type suppressionEventRoot struct{ E *AlarmEventResolver }

func (r *suppressionEventRoot) Event() *AlarmEventResolver { return r.E }
func TestAlarmSuppressionWireEventFields(t *testing.T) {
	// Replace only the streaming transport with a query root; retain real AlarmEvent.
	sdl := strings.Replace(SchemaContent, "query: Query", "query: SuppressionEventQuery", 1)
	sdl = strings.Replace(sdl, "mutation: Mutation", "", 1)
	sdl = strings.Replace(sdl, "subscription: Subscription", "", 1)
	sdl += "\ntype SuppressionEventQuery { event: AlarmEvent! }"
	now := time.Now().UTC()
	for _, event := range []*model.AlarmStateChangeEvent{{}, {SuppressedUntil: &now, SuppressionWindowToken: "work", RaisedDuringSuppressionToken: "history"}} {
		schema := gql.MustParseSchema(sdl, &suppressionEventRoot{E: &AlarmEventResolver{E: event}})
		out := schema.Exec(context.Background(), "{event{suppressedUntil suppressionWindowToken raisedDuringSuppression}}", "", nil)
		require.Empty(t, out.Errors)
		var decoded struct{ Event map[string]*string }
		require.NoError(t, json.Unmarshal(out.Data, &decoded))
		if event.SuppressedUntil == nil {
			require.Nil(t, decoded.Event["suppressedUntil"])
			require.Nil(t, decoded.Event["suppressionWindowToken"])
			require.Nil(t, decoded.Event["raisedDuringSuppression"])
		} else {
			require.Equal(t, now.Format(time.RFC3339Nano), *decoded.Event["suppressedUntil"])
			require.Equal(t, "work", *decoded.Event["suppressionWindowToken"])
			require.Equal(t, "history", *decoded.Event["raisedDuringSuppression"])
		}
	}
}

// SQLite does not implement PostgreSQL's text[] overlap operator. Keep the real
// scope callbacks, translating only their generated overlap expression to exact
// quoted-element membership in rdb.Anchors' array encoding. Production overlap
// SQL is exercised by the model's Postgres integration suite.
func suppressionSQLiteOverlap(t *testing.T, db *gorm.DB) {
	t.Helper()
	var translate func(clause.Expression) clause.Expression
	translate = func(expr clause.Expression) clause.Expression {
		switch e := expr.(type) {
		case clause.Where:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.AndConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.OrConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.NotConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.Expr:
			if strings.HasPrefix(e.SQL, "? && ARRAY[") {
				parts := make([]string, 0, len(e.Vars)-1)
				vars := make([]any, 0, 2*(len(e.Vars)-1))
				for _, anchor := range e.Vars[1:] {
					parts = append(parts, "instr(?, ?) > 0")
					vars = append(vars, e.Vars[0], fmt.Sprintf("%q", anchor))
				}
				return clause.Expr{SQL: "(" + strings.Join(parts, " OR ") + ")", Vars: vars}
			}
		}
		return expr
	}
	callback := func(tx *gorm.DB) {
		if where, ok := tx.Statement.Clauses["WHERE"]; ok {
			where.Expression = translate(where.Expression)
			tx.Statement.Clauses["WHERE"] = where
		}
	}
	require.NoError(t, db.Callback().Query().After("dc:scope_query").Before("gorm:query").Register("test:suppression_overlap", callback))
	require.NoError(t, db.Callback().Row().After("dc:scope_row").Before("gorm:row").Register("test:suppression_overlap", callback))
	require.NoError(t, db.Callback().Update().After("dc:scope_update").Before("gorm:update").Register("test:suppression_overlap", callback))
}

func TestAlarmSuppressionWireBoundariesTargetsAndReadOnly(t *testing.T) {
	f := suppressionWireFixture(t)
	narrow := suppressionWireCaller(f, false, true)
	broad := suppressionWireCaller(f, true, true)
	reader := suppressionWireCaller(f, false, false)
	input := suppressionWireInput("boundary", "DEVICE", "acme-pump")
	start := time.Now().UTC()
	input["startTime"] = start.Format(time.RFC3339Nano)
	input["endTime"] = start.Add(24 * time.Hour).Format(time.RFC3339Nano)
	suppressionCreate(t, narrow, input)
	for _, query := range []string{
		"mutation{endAlarmSuppressionWindow(token:\"boundary\"){token}}",
		"mutation{deleteAlarmSuppressionWindow(token:\"boundary\")}",
	} {
		suppressionRefused(t, serve(t, reader, query, nil), "FORBIDDEN")
	}
	suppressionRefused(t, suppressionUpdate(t, reader, "boundary", map[string]any{"reason": "edit"}), "FORBIDDEN")
	input = suppressionWireInput("maximum", "TENANT", "")
	input["startTime"] = start.Format(time.RFC3339Nano)
	input["endTime"] = start.Add(31 * 24 * time.Hour).Format(time.RFC3339Nano)
	suppressionCreate(t, broad, input)
	input["token"] = "too-long"
	input["endTime"] = start.Add(31*24*time.Hour + time.Second).Format(time.RFC3339Nano)
	suppressionRefused(t, serve(t, broad, suppressionCreateDoc, map[string]any{"request": input}), "INVALID_VALUE")
	// Custom optional object-list packing supports both lists and GraphQL singleton coercion.
	for _, value := range []any{[]any{map[string]any{"type": "DEVICE", "token": "globex-pump"}}, map[string]any{"type": "DEVICE", "token": "globex-pump"}} {
		out := suppressionUpdate(t, broad, "boundary", map[string]any{"targets": value})
		requireServed(t, out, "replace targets")
		targets := out.data["updateAlarmSuppressionWindow"].(map[string]any)["targets"].([]any)
		require.Equal(t, "globex-pump", targets[0].(map[string]any)["token"])
	}
	suppressionRefused(t, suppressionUpdate(t, broad, "boundary", map[string]any{"targets": []any{}}), "INVALID_VALUE")
	suppressionRefused(t, suppressionUpdate(t, broad, "boundary", map[string]any{"targets": []any{map[string]any{"type": "DEVICE", "token": "acme-pump", "subtree": true}}}), "")
	member := f.member("customer:acme")
	suppressionCreate(t, member, suppressionWireInput("member-own", "DEVICE", "acme-pump"))
	requireServed(t, suppressionUpdate(t, member, "member-own", map[string]any{"reason": "member edit"}), "scoped own edit")
	requireServed(t, serve(t, member, "mutation{endAlarmSuppressionWindow(token:\"member-own\"){token}}", nil), "scoped own end")
	requireServed(t, serve(t, member, "mutation{deleteAlarmSuppressionWindow(token:\"member-own\")}", nil), "scoped own delete")
	noAlarmRead := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}})
	suppressionRefused(t, serve(t, noAlarmRead, notificationSuppressionDocument, map[string]any{"tokens": []any{"unknown"}}), "FORBIDDEN")
}

func TestAlarmSuppressionWireStatusVisibilityAndAlarmNulls(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	devices, err := f.api.DevicesByToken(ctx, []string{"acme-pump", "globex-pump"})
	require.NoError(t, err)
	byToken := map[string]*model.Device{}
	for _, device := range devices {
		byToken[device.Token] = device
	}
	for _, seed := range []struct {
		token, owner, device string
		position             bool
	}{
		{"own-alarm", "acme", "acme-pump", false}, {"other-alarm", "globex", "globex-pump", false}, {"position-alarm", "acme", "acme-pump", true},
	} {
		position := seed.position
		a := model.Alarm{TokenReference: rdb.TokenReference{Token: seed.token}, ScopeAnchors: rdb.Anchors{"customer:" + seed.owner}, OriginatorType: "device", OriginatorId: byToken[seed.device].ID, AlarmKey: seed.token, State: "ACTIVE", Severity: "MINOR", RaisedTime: time.Now().UTC(), PositionDerived: &position}
		require.NoError(t, f.db.WithContext(ctx).Create(&a).Error)
	}
	member := f.member("customer:acme")
	out := serve(t, member, notificationSuppressionDocument, map[string]any{"tokens": []any{"own-alarm", "other-alarm", "position-alarm", "unknown"}})
	requireServed(t, out, "visible status")
	rows := out.data["alarmSuppressionStatus"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, "own-alarm", rows[0].(map[string]any)["alarmToken"])
	require.NotNil(t, rows[0].(map[string]any)["suppression"])
	service := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant, Authorities: []string{string(auth.AlarmRead)}})
	serviceOut := serve(t, service, notificationSuppressionDocument, map[string]any{"tokens": []any{"position-alarm"}})
	requireServed(t, serviceOut, "service position status")
	serviceRows := serviceOut.data["alarmSuppressionStatus"].([]any)
	require.Len(t, serviceRows, 1)
	require.NotNil(t, serviceRows[0].(map[string]any)["suppression"])
	for _, change := range []map[string]any{{"state": "CLEARED"}, {"state": "ACTIVE", "originator_type": "asset"}} {
		require.NoError(t, f.db.WithContext(ctx).Model(&model.Alarm{}).Where("token = ?", "own-alarm").Updates(change).Error)
		out = serve(t, ctx, "{alarmsByToken(tokens:[\"own-alarm\"]){suppression{windowToken}}}", nil)
		requireServed(t, out, "null live alarm suppression")
		require.Nil(t, out.data["alarmsByToken"].([]any)[0].(map[string]any)["suppression"])
	}
}

func TestAlarmSuppressionWireVersionRaceIsStaleWrite(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("race", "DEVICE", "acme-pump"))
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("test:suppression-version-race", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AlarmSuppressionWindow" {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "version", Value: -1}}})
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove("test:suppression-version-race") })
	out := suppressionUpdate(t, ctx, "race", map[string]any{"reason": "race"})
	suppressionRefused(t, out, "STALE_WRITE")
	_, err := f.api.UpdateAlarmSuppressionWindow(ctx, "race", &model.AlarmSuppressionUpdateRequest{})
	var conflict *model.SuppressionConflictError
	var stale *rdb.StaleWriteError
	require.ErrorAs(t, err, &conflict)
	require.ErrorAs(t, err, &stale)
}

func TestAlarmSuppressionListQueryCountBounded(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	devices, err := f.api.DevicesByToken(ctx, []string{"acme-pump"})
	require.NoError(t, err)
	var count atomic.Int32
	counter := func(*gorm.DB) { count.Add(1) }
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("test:bounded", counter))
	require.NoError(t, f.db.Callback().Row().Before("gorm:row").Register("test:bounded", counter))
	t.Cleanup(func() {
		_ = f.db.Callback().Query().Remove("test:bounded")
		_ = f.db.Callback().Row().Remove("test:bounded")
	})
	var first int32
	for _, n := range []int{1, 20} {
		for i := 0; i < n; i++ {
			a := model.Alarm{TokenReference: rdb.TokenReference{Token: fmt.Sprintf("count-%d-%d", n, i)}, OriginatorType: "device", OriginatorId: devices[0].ID, AlarmKey: fmt.Sprintf("count-%d-%d", n, i), State: "ACTIVE", Severity: "MINOR", RaisedTime: time.Now().UTC(), PositionDerived: new(bool)}
			require.NoError(t, f.db.WithContext(ctx).Create(&a).Error)
			require.NoError(t, f.db.WithContext(ctx).Create(&model.AlarmCycleSuppression{AlarmId: a.ID, RaisedTime: a.RaisedTime, AlarmToken: a.Token, WindowToken: "work"}).Error)
		}
		count.Store(0)
		out := serve(t, ctx, "{alarms(criteria:{pageNumber:1,pageSize:100}){results{suppression{windowToken until} raisedDuringSuppression}}}", nil)
		requireServed(t, out, "bounded list")
		rows := out.data["alarms"].(map[string]any)["results"].([]any)
		if n == 1 {
			require.Len(t, rows, 1)
		} else {
			require.Len(t, rows, 21)
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			require.Equal(t, "work", row["suppression"].(map[string]any)["windowToken"])
			require.Equal(t, "work", row["raisedDuringSuppression"])
		}
		if n == 1 {
			first = count.Load()
		} else {
			require.LessOrEqual(t, count.Load(), first, "queries must not grow with alarm count")
		}
	}
}

func TestAlarmSuppressionWireStatusDeviceFailureIsolated(t *testing.T) {
	f := suppressionWireFixture(t)
	ctx := suppressionWireCaller(f, true, true)
	suppressionCreate(t, ctx, suppressionWireInput("work", "DEVICE", "acme-pump"))
	devices, err := f.api.DevicesByToken(ctx, []string{"acme-pump", "globex-pump"})
	require.NoError(t, err)
	tokens := []any{}
	goodToken := ""
	for _, device := range devices {
		a := model.Alarm{TokenReference: rdb.TokenReference{Token: "status-" + device.Token}, OriginatorType: "device", OriginatorId: device.ID, AlarmKey: "status-" + device.Token, State: "ACTIVE", Severity: "MINOR", RaisedTime: time.Now().UTC(), PositionDerived: new(bool)}
		require.NoError(t, f.db.WithContext(ctx).Create(&a).Error)
		tokens = append(tokens, a.Token)
		if device.Token == "acme-pump" {
			goodToken = a.Token
		} else {
			require.NoError(t, f.db.WithContext(ctx).Delete(device).Error)
		}
	}
	out := serve(t, ctx, notificationSuppressionDocument, map[string]any{"tokens": tokens})
	requireServed(t, out, "isolated status evaluation failure")
	rows := out.data["alarmSuppressionStatus"].([]any)
	require.Len(t, rows, 2)
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["alarmToken"] == goodToken {
			require.NotNil(t, row["suppression"])
		} else {
			require.Nil(t, row["suppression"])
		}
	}
}
