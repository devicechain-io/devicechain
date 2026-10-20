// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// calculatedEvents reads through the scoped measurement read: a member limited to Acme
// evaluates a formula over Acme's device and gets nothing for Globex's, which the unscoped
// member — the control — does see. It requires event:read.
func TestCalculatedEventsIsScopedAndAuthorized(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{}, &model.MeasurementEvent{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})

	const tenant = "tenant1"
	write := core.WithTenant(context.Background(), tenant)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, owner := range []string{"acme", "globex"} {
		device := owner + "-pump"
		event := model.Event{DeviceToken: device, EventType: eventtype.Measurement, OccurredTime: at,
			ProcessedTime: at, Source: "mqtt"}
		event.EventId = model.DeriveEventId(tenant, &event, nil)
		require.NoError(t, db.WithContext(write).Create(&event).Error)
		require.NoError(t, db.WithContext(write).Create(&model.EventAnchor{EventId: event.EventId,
			DeviceToken: device, EventType: eventtype.Measurement, OccurredTime: at,
			AnchorType: "customer", AnchorToken: owner}).Error)
		require.NoError(t, db.WithContext(write).Create(&model.MeasurementEvent{EventId: event.EventId,
			PayloadId: []byte(owner), DeviceToken: device, EventType: eventtype.Measurement,
			OccurredTime: at, Name: "temp", Value: sql.NullFloat64{Float64: 21.5, Valid: true}}).Error)
	}
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	query := func(ctx context.Context, device string) (int, []string) {
		resp := schema.Exec(ctx, `query { calculatedEvents(deviceToken: "`+device+`", expression: "temp * 2.0",
			dataType: "DOUBLE", inputs: [{key: "temp", dataType: "DOUBLE"}],
			from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z") {
			samples { value } inputDecimalPlaces } }`, "", nil)
		var codes []string
		for _, e := range resp.Errors {
			if c, ok := e.Extensions["code"].(string); ok {
				codes = append(codes, c)
			} else {
				codes = append(codes, e.Message)
			}
		}
		var data struct {
			CalculatedEvents struct {
				Samples            []struct{ Value *string }
				InputDecimalPlaces int
			}
		}
		if len(resp.Data) > 0 && string(resp.Data) != "null" {
			require.NoError(t, json.Unmarshal(resp.Data, &data))
		}
		if len(data.CalculatedEvents.Samples) == 1 {
			assert.Equal(t, "43", *data.CalculatedEvents.Samples[0].Value)
			assert.Equal(t, 8, data.CalculatedEvents.InputDecimalPlaces)
		}
		return len(data.CalculatedEvents.Samples), codes
	}

	read := string(auth.EventRead)
	scoped := scopedCtx(api, tenant, []string{"customer:acme"}, read)
	unscoped := scopedCtx(api, tenant, nil, read)

	n, codes := query(scoped, "acme-pump")
	assert.Equal(t, 1, n, "acme's member must evaluate acme's readings: %v", codes)
	n, codes = query(scoped, "globex-pump")
	assert.Equal(t, 0, n, "acme's member evaluated globex's readings: %v", codes)
	assert.Empty(t, codes, "a hidden device must answer like a missing one, not with an error")
	n, _ = query(unscoped, "globex-pump")
	assert.Equal(t, 1, n, "the control: an unscoped member sees globex's reading, so the fixture holds one to hide")

	_, codes = query(scopedCtx(api, tenant, nil, string(auth.DeviceRead)), "acme-pump")
	assert.NotEmpty(t, codes, "calculatedEvents answered without event:read")
}
