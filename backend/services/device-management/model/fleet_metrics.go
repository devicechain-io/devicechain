// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Bulk-change metrics (S1 §9 under served names). Three rules hold for every one of them:
//
//   - every series exists from construction, at zero, so an alert on a rate or a gauge sees a zero
//     series on a healthy instance rather than no series;
//   - every label value comes from a closed list fixed by this release, and a value outside it is
//     not recorded (it never mints a series): a stored vocabulary value a newer release wrote cannot
//     grow the series set;
//   - no tenant, operation or device label. The gauges are instance-wide aggregates, the same on every
//     replica, so the alerts read them with max().
//
// The constructors are called through a factory so the service registers them under its own
// namespace and subsystem (devicechain_devicemanagement_bulk_change_*), and only on a build that serves
// bulk changes (main.go); everywhere else the package holds an unregistered set, so instrumented code
// never needs a nil check.

// BulkChangeMetricFactory is the part of core.Microservice the metrics are built through.
type BulkChangeMetricFactory interface {
	NewCounterVec(name string, help string, labels []string) *prometheus.CounterVec
	NewGaugeVec(name string, help string, labels []string) *prometheus.GaugeVec
	NewGauge(name string, help string) prometheus.Gauge
	NewHistogramVec(name string, help string, labels []string, buckets []float64) *prometheus.HistogramVec
}

// The closed label vocabularies.
var (
	bulkChangeKinds       = []string{FleetKindConfigSet}
	bulkChangePostResults = []string{"posted", "refused", "skipped", "busy", "lost_race", "fence_refused", "fence_busy",
		"lease_expired", "not_activated", "retryable"}
	bulkChangeDispatches = []string{FleetDispatchPlanning, FleetDispatchReady, FleetDispatchRunning, FleetDispatchPaused,
		FleetDispatchDispatched, FleetDispatchAborted}
	// Transitions are the control moves: a create's READY is not one.
	bulkChangeTransitionTargets = []string{FleetDispatchRunning, FleetDispatchPaused, FleetDispatchDispatched, FleetDispatchAborted}
	bulkChangeReasons           = []string{bulkChangeNoReason, FleetPauseUser, FleetPauseAuthorityRevoked, FleetPauseFailureThreshold,
		FleetPauseAmbiguity, FleetPauseDependency, FleetAbortExpiredUnstarted, FleetAbortPlanningDropped, fleetRetentionExpired}
	bulkChangeHoldCodes = []string{FleetHoldAuthorityUnavailable, FleetHoldNotActivated, FleetHoldControlPending,
		FleetHoldPostError, FleetHoldRateLimit}
	bulkChangeSettlements = []string{string(ledger.SettlementConfirmed), string(ledger.SettlementFailed),
		string(ledger.SettlementSuperseded), string(ledger.SettlementAmbiguous), string(ledger.SettlementResolvedConfirmed),
		string(ledger.SettlementResolvedFailed)}
	bulkChangeBases          = []string{"REPORT_LOG", "LATEST_STATE", bulkChangeNoBasis}
	bulkChangeAuthority      = []string{"qualified", "revoked", "unavailable"}
	bulkChangeFenceActions   = []string{"POST", "CREATE", fleetEventStart, fleetEventPause, fleetEventResume, fleetEventAbort, fleetEventExpire, fleetEventAutoPause, fleetEventDispatched, "RESOLVE", "RETENTION"}
	bulkChangeAdmission      = []string{"quota", "busy"}
	bulkChangeMaintainTables = []string{"lines", "events", "contracts", "runtime", "operations"}
)

const (
	bulkChangeNoReason = "none"
	bulkChangeNoBasis  = "NONE"
)

// BulkChangeMetrics is one complete set of bulk-change series.
type BulkChangeMetrics struct {
	postAttempts   *prometheus.CounterVec
	postDuration   *prometheus.HistogramVec
	permits        *prometheus.CounterVec
	settled        *prometheus.CounterVec
	transitions    *prometheus.CounterVec
	operations     *prometheus.GaugeVec
	holds          *prometheus.GaugeVec
	oldestRunnable prometheus.Gauge
	deadlineLag    prometheus.Gauge
	reconcilerLag  prometheus.Gauge
	lastPass       prometheus.Gauge
	authority      *prometheus.CounterVec
	fenceWait      *prometheus.HistogramVec
	fencePending   *prometheus.CounterVec
	admission      *prometheus.CounterVec
	repairs        *prometheus.CounterVec
	deleted        *prometheus.CounterVec
	activated      prometheus.Gauge
}

// Posting transactions are bounded at 8 s, fence waits at 15 s.
var bulkChangeSecondsBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 8, 15, 30}

// NewBulkChangeMetrics builds every series and creates each label combination at zero.
func NewBulkChangeMetrics(f BulkChangeMetricFactory) *BulkChangeMetrics {
	m := &BulkChangeMetrics{
		postAttempts: f.NewCounterVec("bulk_change_post_attempts_total",
			"Attempts to apply one bulk change line, by kind and result. retryable is an attempt that failed in a way "+
				"that is tried again; busy is a device another writer held.", []string{"kind", "result"}),
		postDuration: f.NewHistogramVec("bulk_change_post_duration_seconds",
			"How long one attempt to apply a bulk change line took, by kind.", []string{"kind"}, bulkChangeSecondsBuckets),
		permits: f.NewCounterVec("bulk_change_pacer_permits_total",
			"Posting permits asked of the shared per-second pacer, by whether they were granted.", []string{"result"}),
		settled: f.NewCounterVec("bulk_change_lines_settled_total",
			"Bulk change lines given a settlement, by settlement and the evidence that decided it (NONE for a deadline "+
				"or a person's verdict).", []string{"settlement", "basis"}),
		transitions: f.NewCounterVec("bulk_change_transitions_total",
			"Bulk change control transitions that committed, by the dispatch state entered and the reason (none when "+
				"there is no reason).", []string{"to", "reason"}),
		operations: f.NewGaugeVec("bulk_change_operations",
			"Bulk changes this release can drive, by dispatch state, across the instance.", []string{"dispatch"}),
		holds: f.NewGaugeVec("bulk_change_holds",
			"Running bulk changes whose hold was reported in the last 60 seconds, by hold code.", []string{"code"}),
		oldestRunnable: f.NewGauge("bulk_change_oldest_runnable_line_age_seconds",
			"Across running bulk changes that still have lines to apply and no blocking hold, the longest time one has "+
				"gone without applying a line (since its last applied line, its start or its resume), on the database clock. "+
				"0 when none is waiting."),
		deadlineLag: f.NewGauge("bulk_change_deadline_lag_seconds",
			"How far past its settlement deadline the oldest unsettled applied line is, on the database clock. 0 when none is."),
		reconcilerLag: f.NewGauge("bulk_change_reconciler_lag_seconds",
			"Age of the oldest device report that could decide an applied bulk change line and has not yet, on the "+
				"database clock. 0 when none is waiting."),
		lastPass: f.NewGauge("bulk_change_driver_last_pass_timestamp_seconds",
			"When this pod's bulk change driver last completed a pass, as a Unix timestamp. 0 until the first pass."),
		authority: f.NewCounterVec("bulk_change_authority_decisions_total",
			"Fresh authority answers asked for a bulk change's grantee, by result: qualified, revoked (a definite no), "+
				"or unavailable (no answer, which holds the operation).", []string{"result"}),
		fenceWait: f.NewHistogramVec("bulk_change_fence_wait_seconds",
			"How long a bulk change transaction waited for its operation's fence, by action.", []string{"action"}, bulkChangeSecondsBuckets),
		fencePending: f.NewCounterVec("bulk_change_fence_pending_total",
			"Control requests answered FENCE_PENDING because in-flight posting did not drain in time, by action.", []string{"action"}),
		admission: f.NewCounterVec("bulk_change_admission_refusals_total",
			"Creates, starts and resumes refused at admission, by reason: quota (QUOTA_EXCEEDED) or busy (ADMISSION_BUSY).",
			[]string{"reason"}),
		repairs: f.NewCounterVec("bulk_change_redaction_repairs_total",
			"Bulk change rows redacted by the repair pass for devices deleted without it, by table.", []string{"table"}),
		deleted: f.NewCounterVec("bulk_change_retention_rows_deleted_total",
			"Bulk change rows removed by retention, by table.", []string{"table"}),
		activated: f.NewGauge("bulk_change_activated",
			"1 when the database enforces the device attribute writer protocol that bulk changes post under, 0 when it "+
				"does not or cannot be read."),
	}
	for _, kind := range bulkChangeKinds {
		for _, result := range bulkChangePostResults {
			m.postAttempts.WithLabelValues(kind, result)
		}
		m.postDuration.WithLabelValues(kind)
	}
	for _, result := range []string{"granted", "denied"} {
		m.permits.WithLabelValues(result)
	}
	for _, s := range bulkChangeSettlements {
		for _, b := range bulkChangeBases {
			m.settled.WithLabelValues(s, b)
		}
	}
	for _, to := range bulkChangeTransitionTargets {
		for _, reason := range bulkChangeReasons {
			m.transitions.WithLabelValues(to, reason)
		}
	}
	for _, d := range bulkChangeDispatches {
		m.operations.WithLabelValues(d)
	}
	for _, code := range bulkChangeHoldCodes {
		m.holds.WithLabelValues(code)
	}
	for _, result := range bulkChangeAuthority {
		m.authority.WithLabelValues(result)
	}
	for _, action := range bulkChangeFenceActions {
		m.fenceWait.WithLabelValues(action)
		m.fencePending.WithLabelValues(action)
	}
	for _, reason := range bulkChangeAdmission {
		m.admission.WithLabelValues(reason)
	}
	for _, table := range bulkChangeMaintainTables {
		m.repairs.WithLabelValues(table)
		m.deleted.WithLabelValues(table)
	}
	return m
}

// unregisteredBulkChangeMetrics builds the same names, registered nowhere.
type unregisteredBulkChangeMetrics struct{}

const bulkChangeMetricSubsystem = "devicemanagement"

func (unregisteredBulkChangeMetrics) NewCounterVec(name, help string, labels []string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: core.METRICS_NAMESPACE, Subsystem: bulkChangeMetricSubsystem,
		Name: name, Help: help}, labels)
}

func (unregisteredBulkChangeMetrics) NewGaugeVec(name, help string, labels []string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: core.METRICS_NAMESPACE, Subsystem: bulkChangeMetricSubsystem,
		Name: name, Help: help}, labels)
}

func (unregisteredBulkChangeMetrics) NewGauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Namespace: core.METRICS_NAMESPACE, Subsystem: bulkChangeMetricSubsystem,
		Name: name, Help: help})
}

func (unregisteredBulkChangeMetrics) NewHistogramVec(name, help string, labels []string, buckets []float64) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: core.METRICS_NAMESPACE, Subsystem: bulkChangeMetricSubsystem,
		Name: name, Help: help, Buckets: buckets}, labels)
}

// bulkMetrics is the set every instrumented path writes to. UseBulkChangeMetrics replaces it once, at
// startup, before any task runs.
var bulkMetrics = NewBulkChangeMetrics(unregisteredBulkChangeMetrics{})

// UseBulkChangeMetrics installs the registered set. Call it before any bulk-change task starts.
func UseBulkChangeMetrics(m *BulkChangeMetrics) {
	if m == nil {
		panic("model: UseBulkChangeMetrics needs a metric set")
	}
	bulkMetrics = m
}

func bulkChangeLabel(vocabulary []string, value string) bool {
	for _, v := range vocabulary {
		if v == value {
			return true
		}
	}
	return false
}

// ---- recording ----

func bulkChangePostResult(result FleetPostResult, err error) string {
	if err != nil || result.Outcome == FleetPostRetry {
		return "retryable"
	}
	return strings.ToLower(string(result.Outcome))
}

func (m *BulkChangeMetrics) post(kind string, result FleetPostResult, err error, took time.Duration) {
	label := bulkChangePostResult(result, err)
	if !bulkChangeLabel(bulkChangeKinds, kind) || !bulkChangeLabel(bulkChangePostResults, label) {
		return
	}
	m.postAttempts.WithLabelValues(kind, label).Inc()
	m.postDuration.WithLabelValues(kind).Observe(took.Seconds())
}

func (m *BulkChangeMetrics) permitted(wanted, granted int) {
	m.permits.WithLabelValues("granted").Add(float64(max(granted, 0)))
	m.permits.WithLabelValues("denied").Add(float64(max(wanted-granted, 0)))
}

func (m *BulkChangeMetrics) settle(settlement, basis string, n int) {
	if basis == "" {
		basis = bulkChangeNoBasis
	}
	if n <= 0 || !bulkChangeLabel(bulkChangeSettlements, settlement) || !bulkChangeLabel(bulkChangeBases, basis) {
		return
	}
	m.settled.WithLabelValues(settlement, basis).Add(float64(n))
}

func (m *BulkChangeMetrics) transition(to, reason string) {
	if reason == "" {
		reason = bulkChangeNoReason
	}
	if bulkChangeLabel(bulkChangeTransitionTargets, to) && bulkChangeLabel(bulkChangeReasons, reason) {
		m.transitions.WithLabelValues(to, reason).Inc()
	}
}

func (m *BulkChangeMetrics) decided(lease *FleetLease, hold string, err error) {
	result := "qualified"
	switch {
	case err != nil || hold != "":
		result = "unavailable"
	case lease == nil:
		result = "revoked"
	}
	m.authority.WithLabelValues(result).Inc()
}

func (m *BulkChangeMetrics) fenced(action string, waited time.Duration) {
	if bulkChangeLabel(bulkChangeFenceActions, action) {
		m.fenceWait.WithLabelValues(action).Observe(waited.Seconds())
	}
}

func (m *BulkChangeMetrics) pending(action string) {
	if bulkChangeLabel(bulkChangeFenceActions, action) {
		m.fencePending.WithLabelValues(action).Inc()
	}
}

// refused counts an admission refusal; any other error is not one.
func (m *BulkChangeMetrics) refused(err error) {
	var refusal *FleetOperationError
	if !errors.As(err, &refusal) {
		return
	}
	switch refusal.Code {
	case FleetCodeQuotaExceeded:
		m.admission.WithLabelValues("quota").Inc()
	case FleetCodeAdmissionBusy:
		m.admission.WithLabelValues("busy").Inc()
	}
}

func (m *BulkChangeMetrics) maintained(counter *prometheus.CounterVec, table string, rows int64) {
	if rows > 0 && bulkChangeLabel(bulkChangeMaintainTables, table) {
		counter.WithLabelValues(table).Add(float64(rows))
	}
}

// fleetFence takes the operation fence and records how long the wait was.
func fleetFence(tx *gorm.DB, key int64, shared bool, action string) error {
	start := time.Now()
	err := fleetLock(tx, key, shared)
	bulkMetrics.fenced(action, time.Since(start))
	return err
}

// ---- the executor ----

const (
	fleetExecutorFreshness = 60 * time.Second
	fleetExecutorStall     = 5 * time.Minute
)

// fleetBlockingHold is a hold the executor reports as HELD: the operation is waiting on something
// outside it. RATE_LIMIT is sharing, not a hold (the operation is still runnable), and POST_ERROR is
// the dependency clock of a line that keeps failing: an operation stuck there is STALLED, which is what
// the operator must see before the 24 h auto-pause.
func fleetBlockingHold(code string) bool {
	switch code {
	case FleetHoldAuthorityUnavailable, FleetHoldNotActivated, FleetHoldControlPending:
		return true
	}
	return false
}

// fleetExecutorFacts is everything the executor state is decided from, read from storage. Now is the
// database's clock; every stamp is a stored value.
type fleetExecutorFacts struct {
	Dispatch     string
	Now          time.Time
	DriverSeenAt sql.NullTime
	HoldCode     string
	HoldSeenAt   sql.NullTime
	// Progressed is the latest of the last applied line and the last start or resume (the header's
	// state_changed_at, which every control transition sets).
	Progressed sql.NullTime
	// Planned says lines remain to be applied, whether or not one is backing off right now.
	Planned bool
}

// fleetExecutorState is the H21 decision, in its order: UNKNOWN when nothing has looked at a running
// operation for 60 s; HELD when a blocking hold was reported in the last 60 s; STALLED when lines remain,
// no hold blocks them and nothing was applied for 5 minutes; otherwise WORKING (running) or IDLE.
func fleetExecutorState(f fleetExecutorFacts) string {
	if f.Dispatch != FleetDispatchRunning {
		return "IDLE"
	}
	fresh := func(t sql.NullTime) bool { return t.Valid && f.Now.Sub(t.Time) <= fleetExecutorFreshness }
	if !fresh(f.DriverSeenAt) {
		return "UNKNOWN"
	}
	if fleetBlockingHold(f.HoldCode) && fresh(f.HoldSeenAt) {
		return "HELD"
	}
	if f.Planned && (!f.Progressed.Valid || f.Now.Sub(f.Progressed.Time) >= fleetExecutorStall) {
		return "STALLED"
	}
	return "WORKING"
}

func fleetLatest(times ...sql.NullTime) sql.NullTime {
	var out sql.NullTime
	for _, t := range times {
		if t.Valid && (!out.Valid || t.Time.After(out.Time)) {
			out = t
		}
	}
	return out
}

// FleetExecutor reports whether the engine applying an operation is making progress. Every fact is
// re-read from storage under the caller's tenant (the dispatch included: the header the caller holds
// is only an identity), and time is the database's, never this process's.
func (api *Api) FleetExecutor(ctx context.Context, op *FleetOperation) (string, error) {
	db := api.RDB.DB(ctx)
	var cur FleetOperation
	if err := db.Select("id", "dispatch", "state_changed_at").Where("id = ?", op.ID).First(&cur).Error; err != nil {
		return "", err
	}
	facts := fleetExecutorFacts{Dispatch: cur.Dispatch}
	if cur.Dispatch != FleetDispatchRunning {
		return fleetExecutorState(facts), nil
	}
	var rt FleetOperationRuntime
	if err := db.Where("operation_id = ?", cur.ID).Limit(1).Find(&rt).Error; err != nil {
		return "", err
	}
	var planned []uint
	if err := db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", cur.ID, FleetPostingPlanned).
		Limit(1).Pluck("id", &planned).Error; err != nil {
		return "", err
	}
	now, err := fleetDBNow(db)
	if err != nil {
		return "", err
	}
	facts.Now, facts.DriverSeenAt, facts.HoldCode, facts.HoldSeenAt = now, rt.DriverSeenAt, rt.HoldCode, rt.HoldSeenAt
	facts.Progressed = fleetLatest(rt.LastProgressAt, cur.StateChangedAt)
	facts.Planned = len(planned) > 0
	return fleetExecutorState(facts), nil
}

// ---- the gauges ----

// The gauges are refreshed by the driver loop, at most this often per replica: they aggregate
// across tenants, and the loop runs every second.
const fleetMetricsRefreshEvery = 15 * time.Second

type fleetMetricsRefresh struct {
	sync.Mutex
	last time.Time
}

// fleetGaugeSample is one refresh's instance-wide aggregate, applied only when it is complete.
type fleetGaugeSample struct {
	operations     map[string]float64
	holds          map[string]float64
	oldestRunnable float64
	deadlineLag    float64
}

// refreshFleetMetrics recomputes the gauges, one tenant at a time under that tenant's own context,
// summing counts and taking the worst age. A failed refresh leaves the previous values in place
// rather than publishing a partial, healthier-looking sample.
func (api *Api) refreshFleetMetrics(ctx context.Context) error {
	activated := 0.0
	if api.FleetOperationEnforcement(ctx) == "ENFORCED" {
		activated = 1
	}
	bulkMetrics.activated.Set(activated)

	tenants, err := api.RDB.TenantsWith(ctx, &FleetOperation{})
	if err != nil {
		return err
	}
	sample := fleetGaugeSample{operations: map[string]float64{}, holds: map[string]float64{}}
	for _, tenant := range tenants {
		if err := api.sampleFleetTenant(core.WithTenant(ctx, tenant), &sample); err != nil {
			return err
		}
	}
	for _, d := range bulkChangeDispatches {
		bulkMetrics.operations.WithLabelValues(d).Set(sample.operations[d])
	}
	for _, code := range bulkChangeHoldCodes {
		bulkMetrics.holds.WithLabelValues(code).Set(sample.holds[code])
	}
	bulkMetrics.oldestRunnable.Set(sample.oldestRunnable)
	bulkMetrics.deadlineLag.Set(sample.deadlineLag)
	return nil
}

func (api *Api) sampleFleetTenant(ctx context.Context, sample *fleetGaugeSample) error {
	db := api.RDB.DB(ctx)
	now, err := fleetDBNow(db)
	if err != nil {
		return err
	}
	var counts []struct {
		Dispatch string
		N        int64
	}
	if err := db.Model(&FleetOperation{}).Scopes(fleetDrivableScope).Select("dispatch, count(*) AS n").
		Group("dispatch").Scan(&counts).Error; err != nil {
		return err
	}
	for _, c := range counts {
		sample.operations[c.Dispatch] += float64(c.N)
	}

	var running []FleetOperation
	if err := db.Scopes(fleetDrivableScope).Select("id", "state_changed_at").
		Where("dispatch = ?", FleetDispatchRunning).Find(&running).Error; err != nil {
		return err
	}
	for _, op := range running {
		var rt FleetOperationRuntime
		if err := db.Where("operation_id = ?", op.ID).Limit(1).Find(&rt).Error; err != nil {
			return err
		}
		heldNow := rt.HoldCode != "" && rt.HoldSeenAt.Valid && now.Sub(rt.HoldSeenAt.Time) <= fleetExecutorFreshness
		if heldNow {
			sample.holds[rt.HoldCode]++
			if fleetBlockingHold(rt.HoldCode) {
				continue
			}
		}
		var planned []uint
		if err := db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPlanned).
			Limit(1).Pluck("id", &planned).Error; err != nil {
			return err
		}
		if len(planned) == 0 {
			continue
		}
		since := fleetLatest(rt.LastProgressAt, op.StateChangedAt)
		if since.Valid {
			sample.oldestRunnable = max(sample.oldestRunnable, now.Sub(since.Time).Seconds())
		}
	}

	// The deadline pass settles only lines of operations this release drives.
	var overdue []time.Time
	drivable := db.Model(&FleetOperation{}).Scopes(fleetDrivableScope).Select("id")
	if err := db.Model(&FleetOperationLine{}).Where("posting = ? AND settlement IS NULL AND settle_by < ? AND operation_id IN (?)",
		FleetPostingPosted, now, drivable).Order("settle_by").Limit(1).Pluck("settle_by", &overdue).Error; err != nil {
		return err
	}
	if len(overdue) > 0 {
		sample.deadlineLag = max(sample.deadlineLag, now.Sub(overdue[0]).Seconds())
	}
	return nil
}

// maybeRefreshFleetMetrics refreshes the gauges when the last refresh on this replica is old enough.
// The interval is this replica's own scheduling, so it is measured on this process's clock.
func (api *Api) maybeRefreshFleetMetrics(ctx context.Context) {
	state := &api.fleetMetrics
	state.Lock()
	if !state.last.IsZero() && time.Since(state.last) < fleetMetricsRefreshEvery {
		state.Unlock()
		return
	}
	state.last = time.Now()
	state.Unlock()
	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := api.refreshFleetMetrics(refreshCtx); err != nil && ctx.Err() == nil {
		log.Warn().Err(err).Msg("Unable to refresh the bulk change gauges; keeping the previous values")
	}
}

// fleetRowCounts collects the rows a maintenance transaction changed, by table, so they are counted
// only once it commits. A nil set records nothing.
type fleetRowCounts map[string]int64

func (c fleetRowCounts) add(table string, n int64) {
	if c != nil && n > 0 {
		c[table] += n
	}
}
