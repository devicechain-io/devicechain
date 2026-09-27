// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 AN ALERT LEVEL WIDER THAN A GraphQL Int IS REFUSED ON READ, NOT WRAPPED.
//
// The level is a uint32 in a bigint column, and the wire type is a 32-bit Int. The read
// used to narrow with a bare int32 conversion, so a stored 2147483648 read back as
// -2147483648 and 4294967295 as -1. event-sources now refuses such a level on arrival;
// this holds the read for a row stored before that.
func TestAnAlertLevelWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	for _, level := range []uint32{math.MaxInt32 + 1, math.MaxUint32} {
		got, err := (&AlertEventResolver{M: model.AlertEvent{Level: level}}).Level()
		if got != 0 {
			t.Errorf("level %d read back as %d, want no value", level, got)
		}
		if !errors.Is(err, gqlcore.ErrStoredIntOutOfRange) {
			t.Errorf("level %d: err = %v, want ErrStoredIntOutOfRange", level, err)
		}
	}
	for _, level := range []uint32{0, 5, math.MaxInt32} {
		if got, err := (&AlertEventResolver{M: model.AlertEvent{Level: level}}).Level(); err != nil || got != int32(level) {
			t.Errorf("level %d: got (%d, %v), want %d", level, got, err, level)
		}
	}
}

// What a caller receives, through the served schema and a real sqlite-backed Api: level
// is non-null inside a non-null list, so one stored out-of-range row makes the whole
// alertEvents answer null with the refusal as its error. The release note says so, and
// this holds it — by the data, not just by an error coming back.
func TestAnOutOfRangeAlertLevelNullsTheListingOnTheWire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&model.AlertEvent{}), "migrate")
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, level := range []uint32{3, math.MaxInt32 + 1} {
		require.NoError(t, db.WithContext(ctx).Create(&model.AlertEvent{
			TenantScoped: rdb.TenantScoped{TenantId: "acme"},
			EventId:      []byte{byte(i + 1)}, PayloadId: []byte{byte(i + 1)},
			DeviceToken: "pump-1", OccurredTime: occurred.Add(time.Duration(i) * time.Second),
			Type: "overheat", Level: level,
		}).Error, "seed alert")
	}
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
	ctx = auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.EventRead)}})

	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	query := `{ alertEvents(criteria: {pageNumber: 1, pageSize: 10, deviceToken: "pump-1"}) { results { type level } } }`
	resp := schema.Exec(ctx, query, "", nil)

	if string(resp.Data) != "null" && len(resp.Data) != 0 {
		t.Errorf("data = %s, want null: level is non-null inside a non-null list", resp.Data)
	}
	if len(resp.Errors) != 1 || fmt.Sprint(resp.Errors[0].Path) != "[alertEvents results 0 level]" {
		t.Errorf("errors = %v, want one refusal at alertEvents.results.0.level", resp.Errors)
	}

	// The counterweight: the in-range row alone reads back with its level.
	resp = schema.Exec(ctx, `{ alertEvents(criteria: {pageNumber: 1, pageSize: 10, deviceToken: "pump-1", `+
		`endTime: "2026-09-01T12:00:00Z"}) { results { type level } } }`, "", nil)
	require.Empty(t, resp.Errors, "an in-range alert must read cleanly")
	require.JSONEq(t, `{"alertEvents":{"results":[{"type":"overheat","level":3}]}}`, string(resp.Data))
}
