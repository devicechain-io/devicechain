// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bulkChangeSeriesNames is the complete exported set. A metric added without a line here fails O-1.
var bulkChangeSeriesNames = []string{
	"post_attempts_total", "post_duration_seconds", "pacer_permits_total", "lines_settled_total", "transitions_total",
	"operations", "holds", "oldest_runnable_line_age_seconds", "deadline_lag_seconds", "reconciler_lag_seconds",
	"driver_last_pass_timestamp_seconds", "authority_decisions_total", "fence_wait_seconds", "fence_pending_total",
	"admission_refusals_total", "redaction_repairs_total", "retention_rows_deleted_total", "activated",
}

func gatherBulkChange(t *testing.T, m *BulkChangeMetrics) []*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(m.postAttempts, m.postDuration, m.permits, m.settled, m.transitions, m.operations, m.holds,
		m.oldestRunnable, m.deadlineLag, m.reconcilerLag, m.lastPass, m.authority, m.fenceWait, m.fencePending,
		m.admission, m.repairs, m.deleted, m.activated)
	families, err := reg.Gather()
	require.NoError(t, err)
	return families
}

func seriesCount(families []*dto.MetricFamily) int {
	n := 0
	for _, f := range families {
		n += len(f.Metric)
	}
	return n
}

// O-1: every series exists at zero from construction, every label is a closed vocabulary, no label
// names a tenant, operation or device, and no name says fleet or ledger.
func TestBulkChangeMetricsStartAtZeroWithClosedLabels(t *testing.T) {
	m := NewBulkChangeMetrics(unregisteredBulkChangeMetrics{})
	families := gatherBulkChange(t, m)
	var names []string
	vocabularies := map[string][]string{
		"kind": bulkChangeKinds, "result": append(append(append([]string{}, bulkChangePostResults...), bulkChangeAuthority...), "granted", "denied"),
		"settlement": bulkChangeSettlements, "basis": bulkChangeBases, "to": bulkChangeTransitionTargets, "reason": append(append([]string{}, bulkChangeReasons...), bulkChangeAdmission...),
		"dispatch": bulkChangeDispatches, "code": bulkChangeHoldCodes, "action": bulkChangeFenceActions, "table": bulkChangeMaintainTables,
	}
	for _, f := range families {
		name := f.GetName()
		names = append(names, strings.TrimPrefix(name, "devicechain_devicemanagement_bulk_change_"))
		require.True(t, strings.HasPrefix(name, "devicechain_devicemanagement_bulk_change_"), name)
		require.NotContains(t, name, "fleet")
		require.NotContains(t, name, "ledger")
		require.NotEmpty(t, f.Metric, "%s has no series at startup", name)
		for _, s := range f.Metric {
			for _, l := range s.Label {
				require.NotContains(t, []string{"tenant", "tenant_id", "operation", "token", "device"}, l.GetName(), name)
				vocabulary, ok := vocabularies[l.GetName()]
				require.True(t, ok, "%s has an unexpected label %s", name, l.GetName())
				require.Contains(t, vocabulary, l.GetValue(), name)
			}
			switch {
			case s.Counter != nil:
				require.Zero(t, s.Counter.GetValue(), name)
			case s.Gauge != nil:
				require.Zero(t, s.Gauge.GetValue(), name)
			case s.Histogram != nil:
				require.Zero(t, s.Histogram.GetSampleCount(), name)
			}
		}
	}
	require.ElementsMatch(t, bulkChangeSeriesNames, names)

	// Every combination of a labelled series exists, not only some of them.
	require.Equal(t, len(bulkChangeKinds)*len(bulkChangePostResults), testutil.CollectAndCount(m.postAttempts))
	require.Equal(t, len(bulkChangeSettlements)*len(bulkChangeBases), testutil.CollectAndCount(m.settled))
	require.Equal(t, len(bulkChangeTransitionTargets)*len(bulkChangeReasons), testutil.CollectAndCount(m.transitions))
	require.Equal(t, len(bulkChangeHoldCodes), testutil.CollectAndCount(m.holds))
	require.Equal(t, len(bulkChangeFenceActions), testutil.CollectAndCount(m.fencePending))

	// Values outside the vocabularies are not recorded and never mint a series.
	before := seriesCount(families)
	m.post("not.a.kind", FleetPostResult{Outcome: FleetPosted}, nil, time.Second)
	m.post(FleetKindConfigSet, FleetPostResult{Outcome: "FROM_A_NEWER_RELEASE"}, nil, time.Second)
	m.settle("FROM_A_NEWER_RELEASE", "", 1)
	m.settle(string(FleetPosted), "SOMETHING", 1)
	m.transition("DRAFT", "")
	m.transition(FleetDispatchPaused, "FROM_A_NEWER_RELEASE")
	m.fenced("UNKNOWN_ACTION", time.Second)
	m.pending("UNKNOWN_ACTION")
	m.maintained(m.deleted, "devices", 3)
	after := gatherBulkChange(t, m)
	require.Equal(t, before, seriesCount(after))
	for _, f := range after {
		for _, s := range f.Metric {
			if s.Counter != nil {
				require.Zero(t, s.Counter.GetValue(), f.GetName())
			}
		}
	}

	// Every post outcome this release can return maps onto the vocabulary.
	for _, outcome := range []FleetPostOutcome{FleetPosted, FleetPostRefused, FleetPostSkipped, FleetPostBusy, FleetPostLostRace,
		FleetPostFenceRefused, FleetPostFenceBusy, FleetPostNotActivated, FleetPostLeaseExpired, FleetPostRetry} {
		require.Contains(t, bulkChangePostResults, bulkChangePostResult(FleetPostResult{Outcome: outcome}, nil), outcome)
	}
	require.Equal(t, "retryable", bulkChangePostResult(FleetPostResult{}, errors.New("undecided")))
}

func TestBulkChangeExecutorDecision(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) sql.NullTime { return sql.NullTime{Time: now.Add(-ago), Valid: true} }
	base := fleetExecutorFacts{Dispatch: FleetDispatchRunning, Now: now, DriverSeenAt: at(time.Second), Progressed: at(time.Second), Planned: true}
	for _, tc := range []struct {
		name string
		edit func(*fleetExecutorFacts)
		want string
	}{
		{"working", func(*fleetExecutorFacts) {}, "WORKING"},
		{"heartbeat exactly 60 s old", func(f *fleetExecutorFacts) { f.DriverSeenAt = at(60 * time.Second) }, "WORKING"},
		{"heartbeat 61 s old", func(f *fleetExecutorFacts) { f.DriverSeenAt = at(61 * time.Second) }, "UNKNOWN"},
		{"never seen", func(f *fleetExecutorFacts) { f.DriverSeenAt = sql.NullTime{} }, "UNKNOWN"},
		{"a silent driver outranks its last hold", func(f *fleetExecutorFacts) {
			f.DriverSeenAt, f.HoldCode, f.HoldSeenAt = at(61*time.Second), FleetHoldAuthorityUnavailable, at(time.Second)
		}, "UNKNOWN"},
		{"fresh authority hold", func(f *fleetExecutorFacts) {
			f.HoldCode, f.HoldSeenAt, f.Progressed = FleetHoldAuthorityUnavailable, at(time.Second), at(time.Hour)
		}, "HELD"},
		{"fresh activation hold", func(f *fleetExecutorFacts) { f.HoldCode, f.HoldSeenAt = FleetHoldNotActivated, at(60*time.Second) }, "HELD"},
		{"fresh control hold", func(f *fleetExecutorFacts) { f.HoldCode, f.HoldSeenAt = FleetHoldControlPending, at(time.Second) }, "HELD"},
		{"a hold not reported for 61 s is no hold", func(f *fleetExecutorFacts) {
			f.HoldCode, f.HoldSeenAt, f.Progressed = FleetHoldAuthorityUnavailable, at(61*time.Second), at(5*time.Minute)
		}, "STALLED"},
		{"rate sharing is not a hold", func(f *fleetExecutorFacts) {
			f.HoldCode, f.HoldSeenAt, f.Progressed = FleetHoldRateLimit, at(time.Second), at(5*time.Minute)
		}, "STALLED"},
		{"a line that keeps failing stalls, it does not hold", func(f *fleetExecutorFacts) {
			f.HoldCode, f.HoldSeenAt, f.Progressed = FleetHoldPostError, at(time.Second), at(5*time.Minute)
		}, "STALLED"},
		{"a line that keeps failing while others apply is working", func(f *fleetExecutorFacts) {
			f.HoldCode, f.HoldSeenAt = FleetHoldPostError, at(time.Second)
		}, "WORKING"},
		{"no progress for 4m59s", func(f *fleetExecutorFacts) { f.Progressed = at(5*time.Minute - time.Second) }, "WORKING"},
		{"no progress for 5 minutes", func(f *fleetExecutorFacts) { f.Progressed = at(5 * time.Minute) }, "STALLED"},
		{"nothing left to apply", func(f *fleetExecutorFacts) { f.Progressed, f.Planned = at(time.Hour), false }, "WORKING"},
		{"paused", func(f *fleetExecutorFacts) { f.Dispatch = FleetDispatchPaused; f.DriverSeenAt = sql.NullTime{} }, "IDLE"},
		{"ready", func(f *fleetExecutorFacts) { f.Dispatch = FleetDispatchReady }, "IDLE"},
		{"dispatched", func(f *fleetExecutorFacts) { f.Dispatch = FleetDispatchDispatched; f.Progressed = at(time.Hour) }, "IDLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			tc.edit(&f)
			require.Equal(t, tc.want, fleetExecutorState(f))
		})
	}
}

func setFleetRuntime(t *testing.T, e *driveEnv, changes map[string]any) {
	t.Helper()
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", e.op.ID).Updates(changes).Error)
}

// M14: one RUNNING operation, four executor states. Dispatch alone cannot answer this.
func TestBulkChangeExecutorReadsTheStoredFacts(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	db := e.api.RDB.DB(e.ctx)
	ts := func(ago time.Duration) sql.NullTime { return sql.NullTime{Time: e.now.Add(-ago), Valid: true} }
	executor := func() string {
		t.Helper()
		state, err := e.api.FleetExecutor(e.ctx, e.op)
		require.NoError(t, err)
		return state
	}
	// Started, never driven.
	require.NoError(t, db.Model(e.op).Update("state_changed_at", ts(time.Minute)).Error)
	require.Equal(t, "UNKNOWN", executor())
	setFleetRuntime(t, e, map[string]any{"driver_seen_at": ts(time.Second)})
	require.Equal(t, "WORKING", executor())
	setFleetRuntime(t, e, map[string]any{"hold_code": FleetHoldAuthorityUnavailable, "hold_seen_at": ts(time.Second)})
	require.Equal(t, "HELD", executor())
	setFleetRuntime(t, e, map[string]any{"hold_code": "", "hold_seen_at": nil, "last_progress_at": ts(6 * time.Minute)})
	require.NoError(t, db.Model(e.op).Update("state_changed_at", ts(7*time.Minute)).Error)
	require.Equal(t, "STALLED", executor())
	// A resume restarts the clock the stall is measured from.
	require.NoError(t, db.Model(e.op).Update("state_changed_at", ts(time.Second)).Error)
	require.Equal(t, "WORKING", executor())

	// The caller's copy of the header is an identity, not a fact: the stored dispatch decides.
	stale := *e.op
	stale.Dispatch = FleetDispatchReady
	state, err := e.api.FleetExecutor(e.ctx, &stale)
	require.NoError(t, err)
	require.Equal(t, "WORKING", state)
	require.NoError(t, db.Model(e.op).Update("dispatch", FleetDispatchPaused).Error)
	require.Equal(t, "IDLE", executor())

	// Another tenant cannot read it.
	_, err = e.api.FleetExecutor(core.WithTenant(e.ctx, "someone-else"), e.op)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestBulkChangeDrivePassIsCounted(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	posted := testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, "posted"))
	durations := histogramCount(t, bulkMetrics.postDuration.WithLabelValues(FleetKindConfigSet))
	granted := testutil.ToFloat64(bulkMetrics.permits.WithLabelValues("granted"))
	qualified := testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("qualified"))
	dispatched := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchDispatched, bulkChangeNoReason))
	fenceWaits := histogramCount(t, bulkMetrics.fenceWait.WithLabelValues("POST"))

	r := e.drive(t)
	require.Equal(t, 2, r.Posted)
	require.True(t, r.Dispatched)
	require.Equal(t, posted+2, testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, "posted")))
	require.Equal(t, durations+2, histogramCount(t, bulkMetrics.postDuration.WithLabelValues(FleetKindConfigSet)))
	require.Equal(t, granted+2, testutil.ToFloat64(bulkMetrics.permits.WithLabelValues("granted")))
	require.Equal(t, qualified+1, testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("qualified")))
	require.Equal(t, dispatched+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchDispatched, bulkChangeNoReason)))
	require.Equal(t, fenceWaits+2, histogramCount(t, bulkMetrics.fenceWait.WithLabelValues("POST")))
}

func histogramCount(t *testing.T, o prometheus.Observer) uint64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, o.(prometheus.Metric).Write(&m))
	return m.GetHistogram().GetSampleCount()
}

func TestBulkChangeAuthorityAndPacerRefusalsAreCounted(t *testing.T) {
	e := driveFixture(t, 3, FleetSafetyRequest{})
	unavailable := testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("unavailable"))
	e.standing.err = errors.New("user-management is down")
	r := e.drive(t)
	require.Equal(t, FleetHoldAuthorityUnavailable, r.Hold)
	require.Equal(t, unavailable+1, testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("unavailable")))

	e.standing.err = nil
	denied := testutil.ToFloat64(bulkMetrics.permits.WithLabelValues("denied"))
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(e.op).Update("eff_posts_per_second", 1).Error)
	r = e.drive(t)
	require.Equal(t, 1, r.Posted)
	// One permit was asked for (the limit) and granted; nothing was denied yet.
	require.Equal(t, denied, testutil.ToFloat64(bulkMetrics.permits.WithLabelValues("denied")))
	r = e.drive(t)
	require.Equal(t, FleetHoldRateLimit, r.Hold)
	require.Equal(t, denied+1, testutil.ToFloat64(bulkMetrics.permits.WithLabelValues("denied")))

	revoked := testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("revoked"))
	paused := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseAuthorityRevoked))
	e.standing.answer = map[string]bool{alice.Name: false}
	r = e.drive(t)
	require.Equal(t, FleetPauseAuthorityRevoked, r.Paused)
	require.Equal(t, revoked+1, testutil.ToFloat64(bulkMetrics.authority.WithLabelValues("revoked")))
	require.Equal(t, paused+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseAuthorityRevoked)))
}

func TestBulkChangeControlTransitionsAndAdmissionAreCounted(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	paused := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseUser))
	resumed := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchRunning, bulkChangeNoReason))
	quota := testutil.ToFloat64(bulkMetrics.admission.WithLabelValues("quota"))
	starts := histogramCount(t, bulkMetrics.fenceWait.WithLabelValues(fleetEventStart))
	ops := make([]*FleetOperation, 0)
	for i := 0; i < 2; i++ {
		ops = append(ops, running(t, api, ctx, "r"+string(rune('a'+i)), FleetSafetyRequest{}))
	}
	require.Equal(t, resumed+2, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchRunning, bulkChangeNoReason)))
	require.Equal(t, starts+2, histogramCount(t, bulkMetrics.fenceWait.WithLabelValues(fleetEventStart)))
	_, err := api.PauseFleetOperation(ctx, ops[0].Token, alice.Name)
	require.NoError(t, err)
	require.Equal(t, paused+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseUser)))
	// A refused transition is not a transition.
	_, err = api.PauseFleetOperation(ctx, ops[0].Token, alice.Name)
	require.Error(t, err)
	require.Equal(t, paused+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseUser)))

	third := readyOp(t, api, ctx, "third", FleetSafetyRequest{}, "d1")
	_, err = api.StartFleetOperation(ctx, "third", third.ContractDigest, alice)
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe), "%v", err)
	require.Equal(t, FleetCodeQuotaExceeded, fe.Code)
	require.Equal(t, quota+1, testutil.ToFloat64(bulkMetrics.admission.WithLabelValues("quota")))
}

func TestBulkChangeGaugesAggregateAcrossTenants(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	db := e.api.RDB.DB(e.ctx)
	ts := func(ago time.Duration) sql.NullTime { return sql.NullTime{Time: e.now.Add(-ago), Valid: true} }
	require.NoError(t, db.Model(e.op).Update("state_changed_at", ts(10*time.Minute)).Error)
	setFleetRuntime(t, e, map[string]any{"driver_seen_at": ts(time.Second), "last_progress_at": ts(7 * time.Minute)})
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchRunning)))
	require.InDelta(t, 420, testutil.ToFloat64(bulkMetrics.oldestRunnable), 0.001)
	require.Zero(t, testutil.ToFloat64(bulkMetrics.activated), "SQLite has no enforcing trigger")

	// A fresh blocking hold takes the operation out of the not-advancing age and into the holds.
	setFleetRuntime(t, e, map[string]any{"hold_code": FleetHoldAuthorityUnavailable, "hold_seen_at": ts(time.Second)})
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Zero(t, testutil.ToFloat64(bulkMetrics.oldestRunnable))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.holds.WithLabelValues(FleetHoldAuthorityUnavailable)))
	// Stale, it is neither.
	setFleetRuntime(t, e, map[string]any{"hold_seen_at": ts(61 * time.Second)})
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Zero(t, testutil.ToFloat64(bulkMetrics.holds.WithLabelValues(FleetHoldAuthorityUnavailable)))
	require.InDelta(t, 420, testutil.ToFloat64(bulkMetrics.oldestRunnable), 0.001)
	// A line that keeps failing is counted as its hold AND still ages: it is not advancing.
	setFleetRuntime(t, e, map[string]any{"hold_code": FleetHoldPostError, "hold_seen_at": ts(time.Second)})
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.holds.WithLabelValues(FleetHoldPostError)))
	require.InDelta(t, 420, testutil.ToFloat64(bulkMetrics.oldestRunnable), 0.001)

	// An overdue applied line is deadline lag; a second tenant's numbers add to the first's.
	var line FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", e.op.ID).Order("ordinal").First(&line).Error)
	require.NoError(t, db.Model(&line).Updates(map[string]any{"posting": FleetPostingPosted, "settle_by": e.now.Add(-90 * time.Second)}).Error)
	other := core.WithTenant(e.ctx, "tenant-b")
	seedConfiguredDevice(t, e.api, other, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	v := "v"
	_, err := e.api.CreateFleetOperation(other, &FleetOperationCreateRequest{Token: "b", Name: "b", Kind: FleetKindConfigSet,
		DeviceTokens: []string{"d1"}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v}}, "creator")
	require.NoError(t, err)
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchRunning)))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchReady)))
	require.InDelta(t, 90, testutil.ToFloat64(bulkMetrics.deadlineLag), 0.001)

	// The operation leaves RUNNING: its age and hold are gone, not frozen at their last values.
	require.NoError(t, db.Model(e.op).Update("dispatch", FleetDispatchAborted).Error)
	require.NoError(t, e.api.refreshFleetMetrics(e.ctx))
	require.Zero(t, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchRunning)))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchAborted)))
	require.Zero(t, testutil.ToFloat64(bulkMetrics.oldestRunnable))
	require.Zero(t, testutil.ToFloat64(bulkMetrics.holds.WithLabelValues(FleetHoldPostError)))
}

func TestBulkChangeDriverPassStampsAndThrottlesTheGauges(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	bulkMetrics.lastPass.Set(0)
	require.NoError(t, e.api.RunFleetDriver(e.ctx))
	require.Greater(t, testutil.ToFloat64(bulkMetrics.lastPass), 0.0)
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchDispatched)))
	// Within the refresh interval a second pass does not recompute the gauges.
	bulkMetrics.operations.WithLabelValues(FleetDispatchDispatched).Set(7)
	require.NoError(t, e.api.RunFleetDriver(e.ctx))
	require.Equal(t, 7.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchDispatched)))
	e.api.fleetMetrics.last = time.Now().Add(-fleetMetricsRefreshEvery)
	require.NoError(t, e.api.RunFleetDriver(e.ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.operations.WithLabelValues(FleetDispatchDispatched)))
}

func TestBulkChangeSettlementsAreCountedOncePerDecision(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	ambiguous := bulkMetrics.settled.WithLabelValues(string("AMBIGUOUS"), bulkChangeNoBasis)
	before := testutil.ToFloat64(ambiguous)
	for range 2 {
		_, err := api.DeadlineFleetLines(ctx)
		require.NoError(t, err)
	}
	require.Equal(t, before+1, testutil.ToFloat64(ambiguous))
	settlementReport(t, api.RDB.DB(ctx), line, 5, "APPLIED", "REPORT")
	confirmed := bulkMetrics.settled.WithLabelValues("CONFIRMED", "REPORT_LOG")
	before = testutil.ToFloat64(confirmed)
	for range 2 {
		_, err := api.ReconcileFleetLines(ctx, false)
		require.NoError(t, err)
	}
	require.Equal(t, before+1, testutil.ToFloat64(confirmed))
}

func TestBulkChangeLostSettlementRaceIsNotCounted(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	ambiguous := bulkMetrics.settled.WithLabelValues("AMBIGUOUS", bulkChangeNoBasis)
	before := testutil.ToFloat64(ambiguous)
	// Another replica settles the line between this pass's read and its conditional write.
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:lost-settlement", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*FleetOperationLine); ok {
			require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE fleet_operation_lines SET settlement = 'AMBIGUOUS' WHERE id = ?", line.ID).Error)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:lost-settlement") })
	n, err := api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, before, testutil.ToFloat64(ambiguous))
}

func TestBulkChangeRetentionAndRepairAreCounted(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	now := time.Now().UTC()
	op := seedExpiryOperation(t, api, ctx, "old", "RUNNING", now)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Update("posting", "POSTED").Error)
	require.NoError(t, api.RDB.DB(ctx).Model(op).Updates(map[string]any{"evidence": "OPEN", "posted_line_count": 1,
		"created_at": now.Add(-180 * 24 * time.Hour)}).Error)
	var lines int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Count(&lines).Error)
	require.Positive(t, lines)
	deletedLines := testutil.ToFloat64(bulkMetrics.deleted.WithLabelValues("lines"))
	aborted := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchAborted, fleetRetentionExpired))
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	require.Equal(t, deletedLines+float64(lines), testutil.ToFloat64(bulkMetrics.deleted.WithLabelValues("lines")))
	require.Equal(t, aborted+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchAborted, fleetRetentionExpired)))
	operations := testutil.ToFloat64(bulkMetrics.deleted.WithLabelValues("operations"))
	require.NoError(t, api.RetainFleetOperations(ctx, now.Add(400*24*time.Hour)))
	require.Equal(t, operations+1, testutil.ToFloat64(bulkMetrics.deleted.WithLabelValues("operations")))

	armed(t)
	api2, ctx2 := fleetFixture(t, 2)
	running(t, api2, ctx2, "redact", FleetSafetyRequest{})
	var dev Device
	require.NoError(t, api2.RDB.DB(ctx2).Where("token = 'd1'").First(&dev).Error)
	repaired := testutil.ToFloat64(bulkMetrics.repairs.WithLabelValues("lines"))
	require.NoError(t, api2.RDB.DB(ctx2).Unscoped().Delete(&dev).Error)
	require.NoError(t, api2.RepairFleetErasure(ctx2))
	require.Equal(t, repaired+1, testutil.ToFloat64(bulkMetrics.repairs.WithLabelValues("lines")))
}

// A transition is counted only once it commits: one decided under the fence whose write then fails
// is not a transition.
func TestBulkChangeTransitionThatDoesNotCommitIsNotCounted(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := running(t, api, ctx, "rollback", FleetSafetyRequest{})
	db := api.RDB.Database
	broken := errors.New("the event row cannot be written")
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail-pause-event", func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Model.(*FleetOperationEvent); ok && ev.Kind == fleetEventPause {
			_ = tx.AddError(broken)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove("test:fail-pause-event") })
	paused := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseUser))
	_, err := api.PauseFleetOperation(ctx, op.Token, alice.Name)
	require.ErrorIs(t, err, broken)
	require.Equal(t, paused, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseUser)))
	current, err := api.FleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, current.Dispatch, "the pause rolled back")
}
