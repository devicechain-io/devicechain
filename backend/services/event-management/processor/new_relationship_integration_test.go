// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

func TestANewRelationshipRedeliveryIsStoredOnceOnPostgres(t *testing.T) {
	if os.Getenv("DC_IT_PGPORT") == "" && os.Getenv("DC_IT_PGHOST") == "" {
		t.Skip("set DC_IT_PGPORT or DC_IT_PGHOST to run against Postgres")
	}
	mgr := newRelationshipPostgresManager(t)
	api := model.NewApi(mgr)
	worker := &EventPersistenceWorker{Api: api}
	tenant := fmt.Sprintf("nr%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	ev := relationshipEvent()
	ev.OccurredTime = time.Now().UTC().Truncate(time.Microsecond)
	ev.ProcessedTime = ev.OccurredTime.Add(time.Second)
	for i := 0; i < 2; i++ {
		if _, err := worker.PersistEvent(ctx, ev); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	var events int64
	if err := mgr.DB(ctx).Model(&model.Event{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("events=%d; want 1", events)
	}
	var count int64
	if err := mgr.DB(ctx).Model(&model.EventAnchor{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("anchors=%d; want 2", count)
	}
	for _, a := range ev.Anchors {
		rows, err := api.Events(ctx, model.EventSearchCriteria{EventTypes: []eventtype.EventType{0}, AnchorType: &a.AnchorType, AnchorToken: &a.AnchorToken})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.Results) != 1 || rows.Pagination.TotalRecords != 1 {
			t.Fatalf("anchor %+v: results=%+v", a, rows)
		}
		got := rows.Results[0]
		want, err := model.DeriveEventIdForPayload(tenant, &got, ev.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if got.EventType != 0 || !bytes.Equal(got.EventId, want) || !got.OccurredTime.Equal(ev.OccurredTime) {
			t.Fatalf("wrong event: %+v", got)
		}
		anchors, err := api.AnchorsForEvent(ctx, got.EventId, got.OccurredTime)
		if err != nil {
			t.Fatal(err)
		}
		if len(anchors) != 2 {
			t.Fatalf("event has %d anchors; want 2", len(anchors))
		}
	}
}

// Use the same Postgres configuration and real migration chain as the redelivery
// integration rig. This test compiles in the ordinary suite and skips without an
// explicit database endpoint, so its API calls stay checked even on DB-free runs.
func newRelationshipPostgresManager(t *testing.T) *rdb.RdbManager {
	t.Helper()
	env := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DC_IT_PGPORT", "5432"))
	if err != nil {
		t.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := env("DC_IT_PGHOST", "localhost"), env("DC_IT_PGUSER", "postgres"), env("DC_IT_PGPASSWORD", "postgres")
	const instance = "embatchtest"
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		t.Fatalf("create the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
		Migrations:   model.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "timescaledb", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": user, "password": pass,
		}},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("run migrations on the real server: %v", err)
	}
	t.Cleanup(func() {
		if db, err := mgr.Database.DB(); err == nil {
			_ = db.Close()
		}
	})
	return mgr
}
