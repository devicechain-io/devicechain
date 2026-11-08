// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// Alarm disposition labels through the REAL schema: the label is written by one operation and
// read back through two others (the alarm's own field and the listing), and those reads are what
// a backtest consumes. A resolver that compiled and answered null would leave every model test
// green with the feature dead, so the round trip is asserted on the wire.

func dispositionWireCtx(t *testing.T, authorities ...auth.Authority) (context.Context, *gql.Schema, *model.Api) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Alarm{}, &model.AlarmDisposition{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	api := model.NewApi(&rdb.RdbManager{Database: db})
	for i, token := range []string{"alarm-01", "alarm-02", "alarm-03"} {
		alarm := &model.Alarm{
			OriginatorType: "device", OriginatorId: 1, AlarmKey: "over-temp-" + token, MetricKey: "tempC",
			State: string(model.AlarmStateCleared), Severity: string(model.AlarmSeverityMajor),
			RaisedTime: time.Date(2026, 11, 2, 8, 30+i, 0, 250000000, time.UTC),
		}
		alarm.Token = token
		if err := api.RDB.DB(ctx).Create(alarm).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	ctx = auth.WithClaims(ctx, &auth.Claims{Username: "operator@example.com", Authorities: strs})
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return ctx, gql.MustParseSchema(SchemaContent, &SchemaResolver{}), api
}

const setDispositionDoc = `mutation($token: String!, $label: AlarmDispositionLabel!, $note: String, $raisedTime: String) {
  setAlarmDisposition(token: $token, label: $label, note: $note, raisedTime: $raisedTime) {
    token
    raisedTime
    disposition { alarmId alarmToken raisedTime label note recordedBy recordedTime }
  }
}`

type wireDisposition struct {
	AlarmId      string  `json:"alarmId"`
	AlarmToken   string  `json:"alarmToken"`
	RaisedTime   string  `json:"raisedTime"`
	Label        string  `json:"label"`
	Note         *string `json:"note"`
	RecordedBy   *string `json:"recordedBy"`
	RecordedTime string  `json:"recordedTime"`
}

// The label crosses the wire as the AlarmDispositionLabel enum in both directions: sent as a
// variable, served back on the alarm, on a fresh read, and through the listing and its facet.
func TestSetAlarmDispositionRoundTripsTheEnumOnTheWire(t *testing.T) {
	ctx, schema, _ := dispositionWireCtx(t, auth.AlarmRead, auth.AlarmWrite)

	res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{
		"token": "alarm-01", "label": "FALSE_POSITIVE", "note": "probe fouled",
		"raisedTime": "2026-11-02T08:30:00.25Z",
	})
	if len(res.Errors) > 0 {
		t.Fatalf("setAlarmDisposition: %v", res.Errors)
	}
	var set struct {
		Alarm struct {
			Token       string           `json:"token"`
			RaisedTime  string           `json:"raisedTime"`
			Disposition *wireDisposition `json:"disposition"`
		} `json:"setAlarmDisposition"`
	}
	if err := json.Unmarshal(res.Data, &set); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := set.Alarm.Disposition
	if d == nil {
		t.Fatal("setAlarmDisposition returned the alarm with no disposition")
	}
	if d.Label != "FALSE_POSITIVE" || d.Note == nil || *d.Note != "probe fouled" || d.AlarmToken != "alarm-01" {
		t.Fatalf("disposition = %+v, want FALSE_POSITIVE on alarm-01 with its note", d)
	}
	if d.RecordedBy == nil || *d.RecordedBy != "operator@example.com" {
		t.Fatalf("recordedBy = %v, want the authenticated subject", d.RecordedBy)
	}
	if d.RaisedTime != set.Alarm.RaisedTime {
		t.Fatalf("the label names cycle %q, the alarm is in cycle %q", d.RaisedTime, set.Alarm.RaisedTime)
	}

	// A fresh read through the alarm's own field (not the write's in-hand label).
	res = schema.Exec(ctx, `query { alarmsByToken(tokens: ["alarm-01"]) { disposition { label alarmToken } } }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmsByToken: %v", res.Errors)
	}
	if string(res.Data) != `{"alarmsByToken":[{"disposition":{"label":"FALSE_POSITIVE","alarmToken":"alarm-01"}}]}` {
		t.Fatalf("alarm.disposition read back %s", res.Data)
	}

	// The listing, filtered by the enum facet given as a literal.
	res = schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10, label: FALSE_POSITIVE}) {
	  results { label alarmToken alarm { token } }
	  pagination { totalRecords }
	} }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmDispositions: %v", res.Errors)
	}
	want := `{"alarmDispositions":{"results":[{"label":"FALSE_POSITIVE","alarmToken":"alarm-01","alarm":{"token":"alarm-01"}}],"pagination":{"totalRecords":1}}}`
	if string(res.Data) != want {
		t.Fatalf("alarmDispositions = %s, want %s", res.Data, want)
	}
	res = schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10, label: EXPECTED}) { results { label } } }`, "", nil)
	if len(res.Errors) > 0 || string(res.Data) != `{"alarmDispositions":{"results":[]}}` {
		t.Fatalf("the EXPECTED facet answered %s %v, want nothing", res.Data, res.Errors)
	}
}

// A label outside the enum is refused when the request is validated: one error, no resolver run,
// nothing written. Both as a variable and as a literal in the listing's facet.
func TestAnUnknownDispositionLabelIsRefusedByValidation(t *testing.T) {
	ctx, schema, _ := dispositionWireCtx(t, auth.AlarmRead, auth.AlarmWrite)
	for _, bad := range []string{"NUISANCE", "false_positive"} {
		res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{"token": "alarm-01", "label": bad})
		if len(res.Errors) != 1 || res.Errors[0].ResolverError != nil {
			t.Fatalf("label %q: want one validation error, got %v", bad, res.Errors)
		}
	}
	res := schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10, label: NUISANCE}) { results { label } } }`, "", nil)
	if len(res.Errors) != 1 {
		t.Fatalf("an unknown facet value: want one validation error, got %v", res.Errors)
	}
	res = schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10}) { results { label } } }`, "", nil)
	if len(res.Errors) > 0 || string(res.Data) != `{"alarmDispositions":{"results":[]}}` {
		t.Fatalf("a refused label was written: %s %v", res.Data, res.Errors)
	}
}

// Labelling is an alarm WRITE: a caller holding only alarm:read is refused before anything is
// written.
func TestSetAlarmDispositionRequiresAlarmWrite(t *testing.T) {
	ctx, schema, _ := dispositionWireCtx(t, auth.AlarmRead)
	res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{"token": "alarm-01", "label": "EXPECTED"})
	if len(res.Errors) == 0 {
		t.Fatal("a caller with only alarm:read labelled an alarm")
	}
	res = schema.Exec(ctx, `query { alarmsByToken(tokens: ["alarm-01"]) { disposition { label } } }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmsByToken: %v", res.Errors)
	}
	if string(res.Data) != `{"alarmsByToken":[{"disposition":null}]}` {
		t.Fatalf("a refused label was written: %s", res.Data)
	}
}

// The listing reads the alarms of a page in ONE call, however many labels the page holds, and
// every label still gets its own alarm.
func TestTheListingReadsAPagesAlarmsOnce(t *testing.T) {
	ctx, _, api := dispositionWireCtx(t, auth.AlarmRead, auth.AlarmWrite)
	for _, token := range []string{"alarm-01", "alarm-02", "alarm-03"} {
		if _, _, err := api.SetAlarmDisposition(ctx, model.AlarmDispositionRequest{
			Token: token, Disposition: string(model.AlarmDispositionTruePositive)}, nil); err != nil {
			t.Fatalf("label %s: %v", token, err)
		}
	}
	found, err := api.AlarmDispositions(ctx, model.AlarmDispositionSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	page := &AlarmDispositionSearchResultsResolver{M: *found, S: &SchemaResolver{}, C: ctx}
	got := map[string]bool{}
	for _, label := range page.Results() {
		alarm, err := label.Alarm(ctx)
		if err != nil || alarm == nil {
			t.Fatalf("label on %s: alarm %v, err %v", label.AlarmToken(), alarm, err)
		}
		if alarm.Token() != label.AlarmToken() {
			t.Fatalf("label on %s resolved alarm %s", label.AlarmToken(), alarm.Token())
		}
		got[alarm.Token()] = true
	}
	if len(got) != 3 {
		t.Fatalf("resolved %d distinct alarms, want 3", len(got))
	}
	if page.page == nil || page.page.reads != 1 {
		t.Fatalf("the page read its alarms %+v times, want once", page.page)
	}
}
