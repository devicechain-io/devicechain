// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type storageGrowthExtra struct{ ID uint }

func TestRdbSpecRegistersStorageGrowthBeforeAfterRdb(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "nil registerer"
		if registered {
			name = "service registry"
		}
		t.Run(name, func(t *testing.T) {
			ephemeralProbes(t)
			ms := testMicroservice(t)
			reg := prometheus.NewRegistry()
			if registered {
				ms.UseMetricsRegistry(reg)
			}
			// Isolate the process registry too: the nil case must register nowhere.
			oldReg, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
			global := prometheus.NewRegistry()
			prometheus.DefaultRegisterer, prometheus.DefaultGatherer = global, global
			t.Cleanup(func() { prometheus.DefaultRegisterer, prometheus.DefaultGatherer = oldReg, oldGatherer })

			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(&rdb.AuditEvent{}, &storageGrowthExtra{}))
			// SQLite has no Postgres catalog. Keep the collector's table parameter and
			// supply catalog readings only for the two tables this fixture created.
			require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:catalog", func(tx *gorm.DB) {
				tx.Statement.SQL.Reset()
				tx.Statement.SQL.WriteString("SELECT 7, 4096 WHERE ? IN ('audit_events', 'storage_growth_extras')")
			}))
			hookRan := false
			svc := New(ms, Spec{
				Rdb: &RdbSpec{AppendOnly: []any{&storageGrowthExtra{}}},
				AfterRdb: func(context.Context, *Managers) error {
					hookRan = true
					families, err := reg.Gather()
					require.NoError(t, err)
					growth := families[:0]
					for _, family := range families {
						if strings.Contains(family.GetName(), "append_only_table_") {
							growth = append(growth, family)
						}
					}
					families = growth
					if registered {
						require.Len(t, families, 2)
						for _, family := range families {
							require.Contains(t, []string{"devicechain_svctest_append_only_table_bytes", "devicechain_svctest_append_only_table_rows_estimate"}, family.GetName())
							require.Len(t, family.Metric, 2)
							tables := map[string]float64{}
							for _, metric := range family.Metric {
								require.Len(t, metric.Label, 1)
								require.Equal(t, "table", metric.Label[0].GetName())
								tables[metric.Label[0].GetValue()] = metric.GetGauge().GetValue()
							}
							expected := float64(7)
							if family.GetName() == "devicechain_svctest_append_only_table_bytes" {
								expected = 4096
							}
							require.Equal(t, map[string]float64{"audit_events": expected, "storage_growth_extras": expected}, tables)
						}
					} else {
						require.Empty(t, families)
					}
					return nil
				},
			})
			require.NoError(t, svc.initialize(context.Background(), func(_ context.Context, manager *rdb.RdbManager) error {
				manager.Database = db
				return nil
			}))
			require.True(t, hookRan)
			families, err := global.Gather()
			require.NoError(t, err)
			require.Empty(t, families, "storage growth must never register globally")
			rec := httptest.NewRecorder()
			ms.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			series := `devicechain_svctest_append_only_table_bytes{table="audit_events"} 4096`
			if registered {
				require.Contains(t, rec.Body.String(), series)
			} else {
				require.NotContains(t, rec.Body.String(), "append_only_table_")
			}
		})
	}
}
