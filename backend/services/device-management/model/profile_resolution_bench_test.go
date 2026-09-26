// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// benchDefinitions builds n published-shaped metric definitions, filled the way a real
// snapshot row is: timestamps, names, descriptions, metadata, bounds and a unit.
func benchDefinitions(n int) []*MetricDefinition {
	defs := make([]*MetricDefinition, 0, n)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	meta := datatypes.JSON(`{"source":"catalog","revision":3}`)
	for i := 0; i < n; i++ {
		d := &MetricDefinition{
			Model:           gorm.Model{ID: uint(1000 + i), CreatedAt: now, UpdatedAt: now},
			TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
			TokenReference:  rdb.TokenReference{Token: fmt.Sprintf("metric-def-%d", i)},
			NamedEntity:     rdb.NamedEntity{Name: sql.NullString{String: fmt.Sprintf("Metric %d", i), Valid: true}, Description: sql.NullString{String: "a declared metric sizing the published version", Valid: true}},
			MetadataEntity:  rdb.MetadataEntity{Metadata: &meta},
			DeviceProfileId: 7,
			MetricKey:       fmt.Sprintf("m%d", i),
			DataType:        "DOUBLE",
			Unit:            sql.NullString{String: "Cel", Valid: true},
			MinValue:        sql.NullFloat64{Float64: -40, Valid: true},
			MaxValue:        sql.NullFloat64{Float64: 125, Valid: true},
		}
		defs = append(defs, d)
	}
	return defs
}

// BenchmarkDecodeProfileResolution is the per-event decode cost of the per-device-type
// cache entry as it is cached — the scope plus the metric definitions projected to what
// resolution reads — against what decoding the full definitions would cost. Every
// resolved event of every type pays the projected row once.
func BenchmarkDecodeProfileResolution(b *testing.B) {
	for _, n := range []int{0, 20, 200} {
		defs := benchDefinitions(n)
		res := NewProfileResolution(ProfileScope{DeviceTypeToken: "dt", ProfileVersionToken: "p@3", FenceSetVersion: 4}, defs)
		projected, err := json.Marshal(res)
		if err != nil {
			b.Fatal(err)
		}
		full, err := json.Marshal(defs)
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("projected/metrics=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(projected)))
			for i := 0; i < b.N; i++ {
				var out ProfileResolution
				if err := json.Unmarshal(projected, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("full-definitions/metrics=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(full)))
			for i := 0; i < b.N; i++ {
				var out []*MetricDefinition
				if err := json.Unmarshal(full, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
