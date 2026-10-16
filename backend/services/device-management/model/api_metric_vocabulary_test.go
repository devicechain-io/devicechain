// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/datatypes"
)

// seedMetric creates a DRAFT metric definition on the profile seedDeviceWithCommands made
// for deviceToken. It is published only by a later PublishDeviceProfile.
func seedMetric(t *testing.T, api *Api, ctx context.Context, deviceToken string, def *MetricDefinition) {
	t.Helper()
	def.DeviceProfileId = v1ProfileIdFor(t, api, ctx, deviceToken)
	def.Token = "md-" + def.MetricKey + "-" + deviceToken
	if err := api.RDB.DB(ctx).Create(def).Error; err != nil {
		t.Fatalf("seed metric definition: %v", err)
	}
}

// The vocabulary is what the console's connectivity helper builds its example payload
// from, so it must be the PUBLISHED reading ingest validates against: a metric that is
// still only a draft is not one ingest knows, and its bounds are not the bounds it checks.
func TestDeviceMetricVocabulary(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	// The resolution also stamps the tenant's fence-set version, so it reads that table.
	newApi := func(t *testing.T) *Api {
		api := newEnqueueTestApi(t)
		if err := api.RDB.Database.AutoMigrate(&GeoFenceSetVersion{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return api
	}

	t.Run("nonexistent device reports no device", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		v, err := api.DeviceMetricVocabulary(ctx, "ghost")
		if err != nil {
			t.Fatal(err)
		}
		if v.DeviceExists {
			t.Fatal("a token that resolves to nothing must not report a live device")
		}
	})

	t.Run("an unpublished profile has no metrics", func(t *testing.T) {
		api := newApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-unpub", nil)
		seedMetric(t, api, ctx, "dev-unpub", &MetricDefinition{MetricKey: "temp", DataType: string(MetricDouble)})
		v, err := api.DeviceMetricVocabulary(ctx, "dev-unpub")
		if err != nil {
			t.Fatal(err)
		}
		if !v.DeviceExists || len(v.Metrics) != 0 {
			t.Fatalf("want an existing device with no metrics, got exists=%t metrics=%d", v.DeviceExists, len(v.Metrics))
		}
	})

	t.Run("the published version is listed, a later draft is not", func(t *testing.T) {
		api := newApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-pub", nil)
		enum := datatypes.JSON(`["1","2","3"]`)
		seedMetric(t, api, ctx, "dev-pub", &MetricDefinition{
			MetricKey: "level", DataType: string(MetricInt),
			Unit:     sql.NullString{String: "%", Valid: true},
			MinValue: sql.NullFloat64{Float64: 1, Valid: true},
			MaxValue: sql.NullFloat64{Float64: 3, Valid: true},
			Enum:     &enum,
		})
		if _, err := api.PublishDeviceProfile(ctx, "prof-dev-pub", nil, nil, "test"); err != nil {
			t.Fatalf("publish: %v", err)
		}
		seedMetric(t, api, ctx, "dev-pub", &MetricDefinition{MetricKey: "draft-only", DataType: string(MetricDouble)})

		v, err := api.DeviceMetricVocabulary(ctx, "dev-pub")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Metrics) != 1 {
			t.Fatalf("want exactly the one published metric, got %d", len(v.Metrics))
		}
		m := v.Metrics[0]
		if m.Key() != "level" || m.DataType() != string(MetricInt) {
			t.Fatalf("got %s/%s", m.Key(), m.DataType())
		}
		if unit, ok := m.Unit(); !ok || unit != "%" {
			t.Errorf("unit = %q, %t", unit, ok)
		}
		if min, ok := m.MinValue(); !ok || min != 1 {
			t.Errorf("min = %v, %t", min, ok)
		}
		if max, ok := m.MaxValue(); !ok || max != 3 {
			t.Errorf("max = %v, %t", max, ok)
		}
		var got []string
		for i := 0; i < m.AllowedValueCount(); i++ {
			got = append(got, m.AllowedValue(i))
		}
		if !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
			t.Errorf("allowed = %v", got)
		}
	})
}
