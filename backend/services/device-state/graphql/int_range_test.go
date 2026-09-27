// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 🔴 A STORED NUMBER WIDER THAN A GraphQL Int IS REFUSED ON READ, NOT WRAPPED.
//
// classifier (the bound metric definition's id, a *uint) and inactivityTimeout (an int)
// are bigint columns, and the wire type is a 32-bit Int. Both reads used to narrow with a
// bare int32 conversion, so a stored 2147483648 read back as -2147483648 — a plausible
// wrong number with nothing to say it was wrong.
func TestAClassifierWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	wide := uint(math.MaxInt32 + 1)
	got, err := (&LatestMeasurementResolver{M: model.LatestMeasurement{Classifier: &wide}}).Classifier()
	if got != nil {
		t.Errorf("classifier 2147483648 read back as %d, want no value", *got)
	}
	if !errors.Is(err, gqlcore.ErrStoredIntOutOfRange) {
		t.Errorf("classifier 2147483648: err = %v, want ErrStoredIntOutOfRange", err)
	}

	seven := uint(7)
	if got, err := (&LatestMeasurementResolver{M: model.LatestMeasurement{Classifier: &seven}}).Classifier(); err != nil || got == nil || *got != 7 {
		t.Errorf("classifier 7: got (%v, %v), want 7", got, err)
	}
	if got, err := (&LatestMeasurementResolver{}).Classifier(); got != nil || err != nil {
		t.Errorf("no classifier: got (%v, %v), want (nil, nil)", got, err)
	}
}

func TestAnInactivityTimeoutWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	got, err := (&DeviceStateResolver{M: model.DeviceState{InactivityTimeout: math.MaxInt32 + 1}}).InactivityTimeout()
	if got != 0 {
		t.Errorf("inactivityTimeout 2147483648 read back as %d, want no value", got)
	}
	if !errors.Is(err, gqlcore.ErrStoredIntOutOfRange) {
		t.Errorf("inactivityTimeout 2147483648: err = %v, want ErrStoredIntOutOfRange", err)
	}
	if got, err := (&DeviceStateResolver{M: model.DeviceState{InactivityTimeout: 600}}).InactivityTimeout(); err != nil || got != 600 {
		t.Errorf("inactivityTimeout 600: got (%d, %v), want 600", got, err)
	}
}

// intRangeWireCtx carries a real sqlite-backed Api holding one out-of-range row of each
// kind, so the queries below run the whole path from the database to the response.
func intRangeWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.LatestMeasurement{}, &model.DeviceState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	wide := uint(math.MaxInt32 + 1)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rows := []any{
		&model.LatestMeasurement{TenantScoped: rdb.TenantScoped{TenantId: "acme"}, DeviceToken: "pump-1",
			Name: "temp", Value: sql.NullFloat64{Float64: 21.5, Valid: true}, Classifier: &wide, OccurredTime: now},
		&model.DeviceState{TenantScoped: rdb.TenantScoped{TenantId: "acme"}, DeviceToken: "pump-1",
			InactivityTimeout: math.MaxInt32 + 1, PresenceSource: "INFERRED"},
	}
	for _, row := range rows {
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
	return withAuthorities(ctx, auth.StateRead)
}

// What a caller actually receives, through the served schema. The release note makes wire
// claims — a wide classifier is an error on that field alone, a wide inactivityTimeout
// takes the listing with it — and these hold them. Both assert the DATA, not just that an
// error came back.
func TestAnOutOfRangeRowOnTheWire(t *testing.T) {
	ctx := intRangeWireCtx(t)
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	t.Run("classifier is nulled alone and the reading still resolves", func(t *testing.T) {
		resp := schema.Exec(ctx, `{ latestMeasurements(deviceToken: "pump-1") { name value classifier } }`, "", nil)
		var data struct {
			LatestMeasurements []struct {
				Name       string   `json:"name"`
				Value      *float64 `json:"value"`
				Classifier *int32   `json:"classifier"`
			} `json:"latestMeasurements"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatalf("decode data %s: %v", resp.Data, err)
		}
		if len(data.LatestMeasurements) != 1 {
			t.Fatalf("latestMeasurements = %s, want the one reading", resp.Data)
		}
		m := data.LatestMeasurements[0]
		if m.Classifier != nil {
			t.Errorf("classifier read back as %d, want null", *m.Classifier)
		}
		if m.Name != "temp" || m.Value == nil || *m.Value != 21.5 {
			t.Errorf("the reading did not survive the refusal: %s", resp.Data)
		}
		if len(resp.Errors) != 1 || fmt.Sprint(resp.Errors[0].Path) != "[latestMeasurements 0 classifier]" {
			t.Errorf("errors = %v, want one refusal at latestMeasurements.0.classifier", resp.Errors)
		}
	})

	t.Run("inactivityTimeout nulls the whole listing", func(t *testing.T) {
		resp := schema.Exec(ctx, `{ deviceStatesByDeviceToken(deviceTokens: ["pump-1"]) { deviceToken inactivityTimeout } }`, "", nil)
		if string(resp.Data) != "null" && len(resp.Data) != 0 {
			t.Errorf("data = %s, want null: the field is non-null inside a non-null list", resp.Data)
		}
		if len(resp.Errors) != 1 || fmt.Sprint(resp.Errors[0].Path) != "[deviceStatesByDeviceToken 0 inactivityTimeout]" {
			t.Errorf("errors = %v, want one refusal at deviceStatesByDeviceToken.0.inactivityTimeout", resp.Errors)
		}
	})
}
