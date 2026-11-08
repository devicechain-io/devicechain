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
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// Alarm disposition labels through the REAL schema: the label is written by one operation and
// read back through two others (the alarm's own field and the listing), and those reads are what
// a backtest consumes. A resolver that compiled and answered null would leave every model test
// green with the feature dead, so the round trip is asserted on the wire.

func dispositionWireCtx(t *testing.T, authorities ...auth.Authority) (context.Context, *gql.Schema) {
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
	alarm := &model.Alarm{
		OriginatorType: "device", OriginatorId: 1, AlarmKey: "over-temp", MetricKey: "tempC",
		State: string(model.AlarmStateCleared), Severity: string(model.AlarmSeverityMajor),
		RaisedTime: time.Date(2026, 11, 2, 8, 30, 0, 250000000, time.UTC),
	}
	alarm.Token = "alarm-01"
	if err := api.RDB.DB(ctx).Create(alarm).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	ctx = auth.WithClaims(ctx, &auth.Claims{Username: "operator@example.com", Authorities: strs})
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return ctx, gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}

const setDispositionDoc = `mutation($token: String!, $disposition: String!, $note: String, $raisedTime: String) {
  setAlarmDisposition(token: $token, disposition: $disposition, note: $note, raisedTime: $raisedTime) {
    token
    state
    raisedTime
    disposition { alarmId raisedTime disposition note recordedBy recordedTime }
  }
}`

type wireDisposition struct {
	AlarmId      string  `json:"alarmId"`
	RaisedTime   string  `json:"raisedTime"`
	Disposition  string  `json:"disposition"`
	Note         *string `json:"note"`
	RecordedBy   *string `json:"recordedBy"`
	RecordedTime string  `json:"recordedTime"`
	Alarm        *struct {
		Token string `json:"token"`
	} `json:"alarm"`
}

func TestSetAlarmDispositionRoundTripsOnTheWire(t *testing.T) {
	ctx, schema := dispositionWireCtx(t, auth.AlarmRead, auth.AlarmWrite)

	res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{
		"token": "alarm-01", "disposition": "FALSE_POSITIVE", "note": "probe fouled",
		"raisedTime": "2026-11-02T08:30:00.25Z",
	})
	if len(res.Errors) > 0 {
		t.Fatalf("setAlarmDisposition: %v", res.Errors)
	}
	var set struct {
		Alarm struct {
			Token       string           `json:"token"`
			State       string           `json:"state"`
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
	if d.Disposition != "FALSE_POSITIVE" || d.Note == nil || *d.Note != "probe fouled" {
		t.Fatalf("label = %+v, want FALSE_POSITIVE with its note", d)
	}
	if d.RecordedBy == nil || *d.RecordedBy != "operator@example.com" {
		t.Fatalf("recordedBy = %v, want the authenticated subject", d.RecordedBy)
	}
	if d.RaisedTime != set.Alarm.RaisedTime {
		t.Fatalf("the label names cycle %q, the alarm is in cycle %q", d.RaisedTime, set.Alarm.RaisedTime)
	}

	// Read back through the alarm's own field, on a fresh read (not the write's in-hand label).
	res = schema.Exec(ctx, `query { alarmsByToken(tokens: ["alarm-01"]) { disposition { disposition recordedBy } } }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmsByToken: %v", res.Errors)
	}
	var read struct {
		Alarms []struct {
			Disposition *wireDisposition `json:"disposition"`
		} `json:"alarmsByToken"`
	}
	if err := json.Unmarshal(res.Data, &read); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(read.Alarms) != 1 || read.Alarms[0].Disposition == nil || read.Alarms[0].Disposition.Disposition != "FALSE_POSITIVE" {
		t.Fatalf("alarm.disposition did not read the label back: %s", res.Data)
	}

	// And through the listing a backtest reads.
	res = schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10, disposition: "FALSE_POSITIVE"}) {
	  results { disposition alarm { token } }
	  pagination { totalRecords }
	} }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmDispositions: %v", res.Errors)
	}
	var listed struct {
		List struct {
			Results    []wireDisposition `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarmDispositions"`
	}
	if err := json.Unmarshal(res.Data, &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.List.Pagination.TotalRecords != 1 || len(listed.List.Results) != 1 ||
		listed.List.Results[0].Alarm == nil || listed.List.Results[0].Alarm.Token != "alarm-01" {
		t.Fatalf("alarmDispositions = %s, want the one label with its alarm", res.Data)
	}
}

// A label outside the vocabulary is refused as an invalid value, which the served handler answers
// with INVALID_VALUE (core/graphql's integrity answer, which schema.Exec does not run, so the class
// it reads is asserted instead), and nothing is written.
func TestSetAlarmDispositionRefusesAnUnknownLabelOnTheWire(t *testing.T) {
	ctx, schema := dispositionWireCtx(t, auth.AlarmRead, auth.AlarmWrite)
	res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{"token": "alarm-01", "disposition": "NUISANCE"})
	if len(res.Errors) != 1 {
		t.Fatalf("want one error, got %v", res.Errors)
	}
	if class, ok := integrity.Refused(res.Errors[0].ResolverError); !ok || class != integrity.ClassInvalid {
		t.Fatalf("refusal class = %v (%v), want invalid: %v", class, ok, res.Errors[0])
	}
	res = schema.Exec(ctx, `query { alarmDispositions(criteria: {pageNumber: 1, pageSize: 10}) { results { disposition } } }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmDispositions: %v", res.Errors)
	}
	if string(res.Data) != `{"alarmDispositions":{"results":[]}}` {
		t.Fatalf("a refused label was written: %s", res.Data)
	}
}

// Labelling is an alarm WRITE: a caller holding only alarm:read is refused before anything is
// written.
func TestSetAlarmDispositionRequiresAlarmWrite(t *testing.T) {
	ctx, schema := dispositionWireCtx(t, auth.AlarmRead)
	res := schema.Exec(ctx, setDispositionDoc, "", map[string]any{"token": "alarm-01", "disposition": "EXPECTED"})
	if len(res.Errors) == 0 {
		t.Fatal("a caller with only alarm:read labelled an alarm")
	}
	res = schema.Exec(ctx, `query { alarmsByToken(tokens: ["alarm-01"]) { disposition { disposition } } }`, "", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("alarmsByToken: %v", res.Errors)
	}
	if string(res.Data) != `{"alarmsByToken":[{"disposition":null}]}` {
		t.Fatalf("a refused label was written: %s", res.Data)
	}
}
