// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestIsolationSweep calls every operation event-management serves as acme's member,
// aimed at globex's device, and fails on any answer that carries globex's events or tells
// globex's device apart from a missing one (ADR-081). The store is SQLite, so the
// TimescaleDB-only reads (bucketedMeasurements) answer an error here — the same error for
// the hidden device as for a missing one, which leaks nothing; their rows under a scope
// are proved on PostgreSQL in scope_ownership_integration_test.go.
func TestIsolationSweep(t *testing.T) {
	// One shared in-memory database: a bare ":memory:" is one database per pooled
	// connection, and the anchor subquery may run on another.
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{}, &model.MeasurementEvent{},
		&model.LocationEvent{}, &model.AlertEvent{}))
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
		// A reading, so the draft-formula preview (calculatedEvents) has a sample to evaluate:
		// globex's must be as invisible to acme's member as a missing device's.
		require.NoError(t, db.WithContext(write).Create(&model.MeasurementEvent{EventId: event.EventId,
			PayloadId: []byte(owner + "-temp"), DeviceToken: device, EventType: eventtype.Measurement,
			OccurredTime: at, Name: "temp", Value: sql.NullFloat64{Float64: 21.5, Valid: true}}).Error)
	}

	authorities := []string{string(auth.EventRead), string(auth.LocationRead)}
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   scopedCtx(api, tenant, []string{"customer:acme"}, authorities...),
		Unscoped: scopedCtx(api, tenant, nil, authorities...),
		Probe:    "globex-pump",
		Hidden:   []string{"globex"},
		// calculatedEvents' formula, input and range cannot be guessed from their types; with
		// these the sweep reaches its read rather than stopping at the compile.
		Literals: map[string]string{
			"expression": `"temp * 2.0"`, "dataType": `"DOUBLE"`, "key": `"temp"`,
			"from": `"2026-09-01T00:00:00Z"`, "to": `"2026-09-02T00:00:00Z"`,
		},
	})
	o, ok := isolationsweep.OutcomeOf(out, "query events")
	require.True(t, ok && o.Data, "the opted-in events read answered acme's member %+v", o)
	o, ok = isolationsweep.OutcomeOf(out, "query calculatedEvents")
	require.True(t, ok && o.Data, "calculatedEvents did not answer acme's member with data, so the sweep never reached its read: %+v", o)
}
