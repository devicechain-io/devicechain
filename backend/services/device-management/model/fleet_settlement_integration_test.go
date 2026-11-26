// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func postedSettlementPG(t *testing.T) *postPG {
	t.Helper()
	p := newPostPG(t, "v1")
	pool, poolErr := p.api.RDB.Database.DB()
	require.NoError(t, poolErr)
	pool.SetMaxOpenConns(6)
	for i := range p.lines {
		got, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[i].ID, p.lease)
		require.NoError(t, err)
		require.Equal(t, FleetPosted, got.Outcome)
		require.NoError(t, p.api.RDB.DB(p.ctx).First(&p.lines[i], p.lines[i].ID).Error)
	}
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(p.op).Update("dispatch", "DISPATCHED").Error)
	return p
}

func TestSettlementReportHistoryOnPostgres(t *testing.T) {
	for _, first := range []ConfigurationStatus{ConfigurationApplied, ConfigurationRejected} {
		for _, laterStatus := range []ConfigurationStatus{ConfigurationApplied, ConfigurationReceived} {
			for _, deadlineFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/deadline=%v", first, laterStatus, deadlineFirst), func(t *testing.T) {
					p := postedSettlementPG(t)
					db := p.api.RDB.DB(p.ctx)
					line := &p.lines[0]
					require.NoError(t, db.Model(line).Update("settle_by", time.Now().Add(-time.Second)).Error)
					if deadlineFirst {
						_, err := p.api.DeadlineFleetLines(p.ctx)
						require.NoError(t, err)
					}
					var rev DeviceConfigurationRevision
					require.NoError(t, db.Where("device_id = ? AND revision = ?", line.TargetId.Int64, line.PostedRevision.Int64).First(&rev).Error)
					require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, "dev", rev.Revision, rev.Digest, first, nil))
					other := ConfigurationApplied
					if other == first {
						other = ConfigurationRejected
					}
					require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, "dev", rev.Revision, rev.Digest, other, nil))
					// A later APPLIED document displaced the key. The deciding revision still wins.
					setConfigAttr(t, p.api, p.ctx, AttributeScopeShared, "k", AttributeValueString, "v2")
					later := readConfig(t, p.api, p.ctx).Desired
					require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, "dev", later.Revision, later.Digest, laterStatus, nil))
					_, err := p.api.ReconcileFleetLines(p.ctx, true)
					require.NoError(t, err)
					_, err = p.api.DeadlineFleetLines(p.ctx)
					require.NoError(t, err)
					require.NoError(t, db.First(line, line.ID).Error)
					want := "CONFIRMED"
					if first == ConfigurationRejected {
						want = "FAILED"
					}
					require.Equal(t, want, line.Settlement.String)
					require.Equal(t, "REPORT_LOG", line.EvidenceBasis.String)
					require.Equal(t, rev.Revision, line.EvidenceRevision.Int64)
				})
			}
		}
	}
}

func TestSettlementLateCommitSweepAndReportSerializationOnPostgres(t *testing.T) {
	p := postedSettlementPG(t)
	db := p.api.RDB.DB(p.ctx)
	line := &p.lines[0]
	var rev DeviceConfigurationRevision
	require.NoError(t, db.Where("device_id = ? AND revision = ?", line.TargetId.Int64, line.PostedRevision.Int64).First(&rev).Error)
	held := db.Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	local := NewApi(&rdb.RdbManager{Database: held})
	require.NoError(t, local.ApplyDeviceConfigurationReport(p.ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	var second DeviceConfigurationRevision
	require.NoError(t, db.Where("device_id=? AND revision=?", p.lines[1].TargetId.Int64, p.lines[1].PostedRevision.Int64).First(&second).Error)
	require.NoError(t, local.ApplyDeviceConfigurationReport(p.ctx, "dev2", second.Revision, second.Digest, ConfigurationApplied, nil))
	var holder int
	require.NoError(t, held.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)
	pid, done := make(chan int, 1), make(chan error, 1)
	go func() {
		tx := db.Begin()
		if tx.Error != nil {
			done <- tx.Error
			return
		}
		defer tx.Rollback()
		var waiter int
		if err := tx.Raw("SELECT pg_backend_pid()").Scan(&waiter).Error; err != nil {
			done <- err
			return
		}
		pid <- waiter
		err := NewApi(&rdb.RdbManager{Database: tx}).ApplyDeviceConfigurationReport(p.ctx, "dev", rev.Revision, rev.Digest, ConfigurationRejected, nil)
		if err == nil {
			err = tx.Commit().Error
		}
		done <- err
	}()
	var waiter int
	select {
	case waiter = <-pid:
	case err := <-done:
		t.Fatalf("second report could not start: %v", err)
	case <-p.ctx.Done():
		t.Fatal(p.ctx.Err())
	}
	require.Eventually(t, func() bool {
		var blocked int64
		err := db.Raw("SELECT count(*) FROM pg_locks WHERE pid=? AND NOT granted AND ? = ANY(pg_blocking_pids(pid))", waiter, holder).Scan(&blocked).Error
		return err == nil && blocked > 0
	}, 5*time.Second, 10*time.Millisecond, "second report must wait on the first report's configuration lock")
	_, err := p.api.ReconcileFleetLines(p.ctx, true)
	require.NoError(t, err)
	// Retain the actual report transaction past the fast-path window. The
	// second device has no subsequent recent report to mask a missing sweep.
	time.Sleep(31 * time.Second)
	require.NoError(t, held.Commit().Error)
	require.NoError(t, <-done)
	_, err = p.api.ReconcileFleetLines(p.ctx, true)
	require.NoError(t, err)
	require.NoError(t, db.First(&p.lines[1], p.lines[1].ID).Error)
	require.False(t, p.lines[1].EvidenceRevision.Valid, "the committed report is outside the fast window")
	// Both report transactions serialized; APPLIED has the first id and decides.
	require.NoError(t, p.api.SweepFleetSettlement(p.ctx))
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "CONFIRMED", line.Settlement.String)
	require.NoError(t, db.First(&p.lines[1], p.lines[1].ID).Error)
	require.Equal(t, "CONFIRMED", p.lines[1].Settlement.String)
}

func TestSettlementUncapturedV18WriterOnPostgres(t *testing.T) {
	frozen, err := os.ReadFile("testdata/configuration_report_v18.go.txt")
	require.NoError(t, err)
	compiled, err := os.ReadFile("configuration_report_v18_integration_test.go")
	require.NoError(t, err)
	want := string(frozen[strings.Index(string(frozen), "func (api *Api)"):])
	got := strings.ReplaceAll(string(compiled[strings.Index(string(compiled), "func (api *Api)"):]), "applyV18ConfigurationReport(", "ApplyDeviceConfigurationReport(")
	require.Equal(t, strings.TrimSpace(want), strings.TrimSpace(got), "the non-capturing writer is a frozen copy")
	for _, capturing := range []bool{false, true} {
		t.Run(fmt.Sprint(capturing), func(t *testing.T) {
			p := postedSettlementPG(t)
			line := &p.lines[0]
			db := p.api.RDB.DB(p.ctx)
			var first DeviceConfigurationRevision
			require.NoError(t, db.Where("device_id=? AND revision=?", line.TargetId.Int64, line.PostedRevision.Int64).First(&first).Error)
			require.NoError(t, p.api.applyV18ConfigurationReport(p.ctx, "dev", first.Revision, first.Digest, ConfigurationApplied, nil))
			setConfigAttr(t, p.api, p.ctx, AttributeScopeShared, "k", AttributeValueString, "v2")
			later := readConfig(t, p.api, p.ctx).Desired
			if capturing {
				require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, "dev", later.Revision, later.Digest, ConfigurationReceived, nil))
			} else {
				// Both terminal reports go through the frozen non-capturing
				// writer. A newer document retaining the value decides from state.
				later.ID = 0
				later.Revision++
				later.Document = first.Document
				later.Digest = first.Digest
				require.NoError(t, db.Create(later).Error)
				require.NoError(t, p.api.applyV18ConfigurationReport(p.ctx, "dev", later.Revision, later.Digest, ConfigurationApplied, nil))
			}
			_, err := p.api.ReconcileFleetLines(p.ctx, false)
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			require.Equal(t, "CONFIRMED", line.Settlement.String)
			require.Equal(t, "LATEST_STATE", line.EvidenceBasis.String)
		})
	}
}

func TestSettlementDisabledDeviceOnPostgres(t *testing.T) {
	for _, value := range []string{"v1", "v2"} {
		t.Run(value, func(t *testing.T) {
			p := newPostPG(t, "v1")
			db := p.api.RDB.DB(p.ctx)
			line := &p.lines[0]
			require.NoError(t, db.Model(p.devs[0]).Update("disabled_at", time.Now()).Error)
			got, err := p.api.PostFleetLine(p.ctx, p.op.ID, line.ID, p.lease)
			require.NoError(t, err)
			require.Equal(t, FleetPosted, got.Outcome)
			require.NoError(t, db.Model(line).Update("settle_by", time.Now().Add(-time.Second)).Error)
			_, err = p.api.DeadlineFleetLines(p.ctx)
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			require.Equal(t, "AMBIGUOUS", line.Settlement.String)
			require.Equal(t, "device disabled at deadline", line.Detail)
			require.NoError(t, db.Model(p.devs[0]).Update("disabled_at", nil).Error)
			// Simulate a newer device document, retaining or displacing the value.
			var rev DeviceConfigurationRevision
			require.NoError(t, db.Where("device_id=? AND revision=?", line.TargetId.Int64, line.PostedRevision.Int64).First(&rev).Error)
			rev.ID = 0
			rev.Revision++
			rev.Document = `{"k":"` + value + `"}`
			rev.Digest = ConfigurationDigest([]byte(rev.Document))
			require.NoError(t, db.Create(&rev).Error)
			require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
			_, err = p.api.ReconcileFleetLines(p.ctx, true)
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			want := "CONFIRMED"
			if value == "v2" {
				want = "SUPERSEDED"
			}
			require.Equal(t, want, line.Settlement.String)
		})
	}
}

func TestSettlementFiftySilentDevicesAndProtocolOnPostgres(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 50)
	op, err := api.CreateFleetOperation(ctx, setReq("silent", tokens...), "creator")
	require.NoError(t, err)
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(op).Update("dispatch", "DISPATCHED").Error)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id=?", op.ID).Updates(map[string]any{"posting": "POSTED", "posted_revision": 5, "settle_by": time.Now().Add(-time.Second)}).Error)
	var before, after []FleetOperationLine
	require.NoError(t, db.Where("operation_id=?", op.ID).Order("id").Find(&before).Error)
	require.NoError(t, db.Model(op).Update("driver_protocol", 2).Error)
	_, err = api.ReconcileFleetLines(ctx, true)
	require.NoError(t, err)
	_, err = api.ReconcileFleetLines(ctx, false)
	require.NoError(t, err)
	_, err = api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Where("operation_id=?", op.ID).Order("id").Find(&after).Error)
	require.Equal(t, before, after)
	require.NoError(t, db.Model(op).Update("driver_protocol", 1).Error)
	n, err := api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 50, n)
	for range 2 {
		_, err = api.ReconcileFleetLines(ctx, false)
		require.NoError(t, err)
		_, err = api.DeadlineFleetLines(ctx)
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id=? AND settlement='AMBIGUOUS' AND evidence_revision IS NULL", op.ID).Count(&count).Error)
	require.EqualValues(t, 50, count)
	require.NoError(t, db.First(op, op.ID).Error)
	require.Equal(t, "OPEN", op.Evidence)
}

func TestSettlementConcurrentTenThousandOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	pool, poolErr := api.RDB.Database.DB()
	require.NoError(t, poolErr)
	pool.SetMaxOpenConns(6)
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("settleload%d", time.Now().UnixNano())), 2*time.Minute)
	defer cancel()
	op := &FleetOperation{Token: "load", Kind: "config.set", Name: "load", Dispatch: "DISPATCHED", Evidence: "OPEN", DriverProtocol: 1, RequestDigest: "x", Intent: `{"key":"k"}`, TargetKind: "DEVICE_LIST", TargetCount: 10000, MaxPostsPerSecond: 50, EffPostsPerSecond: 50, SettleDeadlineS: 60, Initiator: "test"}
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Create(op).Error)
	lines := make([]FleetOperationLine, 10000)
	reports := make([]DeviceConfigurationReport, 10000)
	revisions := make([]DeviceConfigurationRevision, 10000)
	for i := range lines {
		// Synthetic device ids are isolated by tenant; the journal intentionally has no device FK.
		id := uint(op.ID)*100000 + uint(i) + 1
		lines[i] = FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 1), TargetId: sql.NullInt64{Int64: int64(id), Valid: true}, Posting: "POSTED", PostedRevision: sql.NullInt64{Int64: 5, Valid: true}, PostedValue: sql.NullString{String: `{"present":true,"valueType":"STRING","value":"v"}`, Valid: true}, SettleBy: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true}}
		reports[i] = DeviceConfigurationReport{DeviceId: id, Revision: 5, Status: "APPLIED", Source: "REPORT", Digest: "x", CreatedAt: time.Now()}
		revisions[i] = DeviceConfigurationRevision{DeviceId: id, Revision: 5, Document: `{"k":"v"}`, Digest: "x"}
	}
	require.NoError(t, db.CreateInBatches(lines, 500).Error)
	require.NoError(t, db.CreateInBatches(revisions, 500).Error)
	// Half expire before any report is committed. They must flip once when
	// evidence arrives, while the other half settle directly from evidence.
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id=? AND ordinal>5000", op.ID).Update("settle_by", time.Now().Add(time.Hour)).Error)
	ambiguous, err := api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5000, ambiguous)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id=?", op.ID).Update("settle_by", time.Now().Add(-time.Minute)).Error)
	require.NoError(t, db.CreateInBatches(reports, 500).Error)
	var wg sync.WaitGroup
	counts := make([]int64, 3)
	errs := make([]error, 3)
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i == 2 {
				counts[i], errs[i] = api.DeadlineFleetLines(ctx)
			} else {
				counts[i], errs[i] = api.ReconcileFleetLines(ctx, false)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 10000+5000, ambiguous+counts[0]+counts[1]+counts[2], "UPDATE-count oracle: initial decisions plus AMBIGUOUS-to-evidence flips")
	var n int64
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ? AND settlement = 'CONFIRMED'", op.ID).Count(&n).Error)
	require.EqualValues(t, 10000, n)
}

func TestSettlementSustainsFiveHundredPerSecondOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	base, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	contexts := make([]context.Context, 10)
	reports := make([][]DeviceConfigurationReport, 10)
	stamp := time.Now().UnixNano()
	for tenant := range contexts {
		ctx := core.WithTenant(base, fmt.Sprintf("settlerate%d-%02d", stamp, tenant))
		contexts[tenant] = ctx
		db := api.RDB.DB(ctx)
		op := &FleetOperation{Token: "load", Kind: "config.set", Name: "load", Dispatch: "DISPATCHED", DriverProtocol: 1, RequestDigest: "x", Intent: `{"key":"k"}`, TargetKind: "DEVICE_LIST", TargetCount: 1000, MaxPostsPerSecond: 50, EffPostsPerSecond: 50, SettleDeadlineS: 60, Initiator: "test"}
		require.NoError(t, db.Create(op).Error)
		lines := make([]FleetOperationLine, 1000)
		revisions := make([]DeviceConfigurationRevision, 1000)
		reports[tenant] = make([]DeviceConfigurationReport, 1000)
		for i := range lines {
			id := uint(op.ID)*100000 + uint(i) + 1
			lines[i] = FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 1), TargetId: sql.NullInt64{Int64: int64(id), Valid: true}, Posting: "POSTED", PostedRevision: sql.NullInt64{Int64: 5, Valid: true}, PostedValue: sql.NullString{String: `{"present":true,"valueType":"STRING","value":"v"}`, Valid: true}}
			revisions[i] = DeviceConfigurationRevision{DeviceId: id, Revision: 5, Document: `{"k":"v"}`, Digest: "x"}
			reports[tenant][i] = DeviceConfigurationReport{DeviceId: id, Revision: 5, Status: "APPLIED", Source: "REPORT", Digest: "x"}
		}
		require.NoError(t, db.CreateInBatches(lines, 500).Error)
		require.NoError(t, db.CreateInBatches(revisions, 500).Error)
	}
	var decided atomic.Int64
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-base.Done():
				done <- base.Err()
				return
			case <-ticker.C:
				for _, ctx := range contexts {
					n, err := api.ReconcileFleetLines(ctx, true)
					if err != nil {
						done <- err
						return
					}
					decided.Add(n)
				}
			}
		}
	}()
	start := time.Now()
	for batch := 0; batch < 40; batch++ {
		for tenant, ctx := range contexts {
			rows := reports[tenant][batch*25 : (batch+1)*25]
			for i := range rows {
				rows[i].CreatedAt = time.Now()
			}
			require.NoError(t, api.RDB.DB(ctx).Create(&rows).Error)
		}
		if delay := time.Until(start.Add(time.Duration(batch+1) * 500 * time.Millisecond)); delay > 0 {
			time.Sleep(delay)
		}
	}
	require.LessOrEqual(t, time.Since(start), 21*time.Second, "ingestion must sustain 50 reports/s for each of 10 tenants")
	close(stop)
	require.NoError(t, <-done)
	var tail int64
	drainStart := time.Now()
	for _, ctx := range contexts {
		n, err := api.ReconcileFleetLines(ctx, true)
		require.NoError(t, err)
		tail += n
	}
	if tail > 0 {
		require.GreaterOrEqual(t, float64(tail)/time.Since(drainStart).Seconds(), 500.0, "reconciler must sustain the instance post ceiling")
	}
	require.EqualValues(t, 10000, decided.Load()+tail)
	var lag time.Duration
	for _, ctx := range contexts {
		age, err := api.FleetReconcilerLag(ctx)
		require.NoError(t, err)
		if age > lag {
			lag = age
		}
	}
	require.Less(t, lag, 15*time.Second, "reconciler lag gauge at the end of the 10-tenant run")
	require.NoError(t, api.RunFleetSettlement(base, true, false))
	require.Less(t, testutil.ToFloat64(bulkMetrics.reconcilerLag), 15.0)
}
