// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the backfill worker against a REAL TimescaleDB, through the real
// migration chain and the real core/rdb path.
//
// The default `go test ./...` skips them (the build tag). Run them with:
//
//	docker run -d --name s82a-tsdb -e POSTGRES_PASSWORD=devicechain -p 127.0.0.1:55460:5432 \
//	  timescale/timescaledb:latest-pg16 -c max_connections=200
//	DC_IT_PGPORT=55460 go test -tags integration ./processor/backfill/ -count=1
package backfill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// rig is one test's database, worker and fixtures. Every test gets its own database, named
// by the test, so tests never share rows.
type rig struct {
	t       *testing.T
	mgr     *rdb.RdbManager
	api     *model.Api
	metrics *Metrics
	roster  *fakeRoster
	cfg     Config
	tenant  string
	worker  *Worker
}

func newRig(t *testing.T, name string) *rig {
	t.Helper()
	port, err := strconv.Atoi(envOr("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	// A database of its own per run: the rollup's materialization is not emptied by truncating the
	// table it aggregates, so a reused database would carry a previous run's buckets.
	instance := "s82a" + name + strconv.FormatInt(time.Now().Unix(), 36)
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), envOr("DC_IT_PGHOST", "localhost"), port,
		envOr("DC_IT_PGUSER", "postgres"), envOr("DC_IT_PGPASSWORD", "devicechain"), instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
		Migrations:   model.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": envOr("DC_IT_PGHOST", "localhost"), "port": port,
				"username": envOr("DC_IT_PGUSER", "postgres"), "password": envOr("DC_IT_PGPASSWORD", "devicechain"),
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	// Chunks of a day, as the lifecycle reconciler sets them; the hypertable's own default is a
	// week, which would put a test's three days of readings in one chunk.
	require.NoError(t, sys.Exec(`SELECT set_chunk_time_interval('"event-management".measurement_events', INTERVAL '1 day')`).Error)
	for _, table := range []string{"events", "measurement_events", "calculated_backfills", "purged_tenants"} {
		_ = sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "event-management".%q`, table)).Error
	}
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	r := &rig{t: t, mgr: mgr, api: model.NewApi(mgr), metrics: NewMetrics(ms), roster: &fakeRoster{}, tenant: "acme",
		cfg: Config{RowsPerSecond: 1_000_000, ChunkInterval: 24 * time.Hour}}
	return r
}

// build makes the worker; call it after adjusting r.cfg and r.roster.
func (r *rig) build() *Worker {
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	r.worker = NewWorker(ms, r.mgr, r.roster, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	return r.worker
}

func (r *rig) ctx() context.Context { return core.WithTenant(context.Background(), r.tenant) }
func (r *rig) tdb() *gorm.DB        { return r.mgr.Database.WithContext(r.ctx()) }
func (r *rig) sys() *gorm.DB {
	return r.mgr.Database.WithContext(core.WithSystemContext(context.Background()))
}

// ---- the profile --------------------------------------------------------------------------

type metricSpec struct {
	id                  uint
	key, dataType, expr string
	unit                string
	min                 *float64
}

func profileSpec() []metricSpec {
	zero := 0.0
	return []metricSpec{
		{id: 1, key: "voltage", dataType: "DOUBLE"},
		{id: 2, key: "current", dataType: "DOUBLE"},
		{id: 3, key: "fan", dataType: "INT"},
		{id: 5, key: "power", dataType: "DOUBLE", expr: "voltage * current", unit: "W"},
		{id: 6, key: "kw", dataType: "DOUBLE", expr: "power / 1000.0 * device.gain", unit: "kW", min: &zero},
		{id: 7, key: "flag", dataType: "BOOLEAN", expr: "power > 100.0"},
	}
}

// planDoc is the device-management plan door's answer for profileSpec, as a job stores it.
func planDoc() model.CalculatedPlanDoc {
	doc := model.CalculatedPlanDoc{VersionToken: "meter@1", Version: 1, ConstantKeys: []string{"gain"}}
	for _, m := range profileSpec() {
		if m.expr == "" {
			doc.Measured = append(doc.Measured, model.CalculatedPlanInput{Key: m.key, DataType: m.dataType})
			continue
		}
		pm := model.CalculatedPlanMetric{Key: m.key, Expression: m.expr, DataType: m.dataType,
			MinValue: m.min, Classifier: float64(m.id), NewInVersion: true}
		if m.unit != "" {
			u := m.unit
			pm.Unit = &u
		}
		doc.Calculated = append(doc.Calculated, pm)
	}
	return doc
}

// liveOracle is the LIVE resolver for the same profile: a device-management ProfileResolution,
// run through BeginCalculation, Set and Append exactly as the event resolver runs it.
type liveOracle struct{ res dmmodel.ProfileResolution }

func newLiveOracle() *liveOracle {
	var defs []*dmmodel.MetricDefinition
	for _, m := range profileSpec() {
		d := &dmmodel.MetricDefinition{MetricKey: m.key, DataType: m.dataType}
		d.ID = m.id
		if m.expr != "" {
			d.Expression = sql.NullString{String: m.expr, Valid: true}
		}
		if m.unit != "" {
			d.Unit = sql.NullString{String: m.unit, Valid: true}
		}
		if m.min != nil {
			d.MinValue = sql.NullFloat64{Float64: *m.min, Valid: true}
		}
		defs = append(defs, d)
	}
	return &liveOracle{res: dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "meter@1"}, defs)}
}

type nopObserver struct{}

func (nopObserver) Computed(string)                             {}
func (nopObserver) Skipped(string, calcfield.SkipReason, error) {}

func (o *liveOracle) derive(raw map[string]string, md *datatypes.JSON) []resolvedevent.ResolvedMeasurementEntry {
	calc := o.res.BeginCalculation(o.res.DeviceConstants(md))
	defer calc.Release()
	for k, v := range raw {
		if m, ok := o.res.Metric(k); ok && !m.Calculated() {
			calc.Set(&m, v)
		}
	}
	return calc.Append(nil, nopObserver{})
}

// ---- seeding ------------------------------------------------------------------------------

// entry is one raw reading of a message.
type entry struct {
	at    time.Time
	name  string
	value string
}

// message is one event: its readings, possibly at several instants.
type message struct{ entries []entry }

func rawClassifier(name string) uint64 {
	for _, m := range profileSpec() {
		if m.key == name {
			return uint64(m.id)
		}
	}
	panic("unknown metric " + name)
}

// seed persists the messages for a device through the real write path. With live set it also
// persists the derived entries the live resolver computes for each sample (the readings of the
// message sharing an instant), so the device ends up holding what live would have stored.
func (r *rig) seed(device string, msgs []message, live *liveOracle, md *datatypes.JSON) {
	r.t.Helper()
	for n, m := range msgs {
		first := m.entries[0].at
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", device, n, first.UnixNano())))
		ev := model.Event{EventId: sum[:], DeviceToken: device, EventType: esmodel.Measurement,
			OccurredTime: first, Source: "mqtt", ProcessedTime: time.Now().UTC()}
		var reqs []*model.MeasurementEventCreateRequest
		byInstant := map[time.Time]map[string]string{}
		var instants []time.Time
		for _, e := range m.entries {
			cls := rawClassifier(e.name)
			req, err := model.MeasurementRequestFor(ev, e.at, model.MeasurementReading{Name: e.name, Value: e.value, Classifier: &cls})
			require.NoError(r.t, err)
			reqs = append(reqs, req)
			if byInstant[e.at] == nil {
				byInstant[e.at] = map[string]string{}
				instants = append(instants, e.at)
			}
			byInstant[e.at][e.name] = e.value
		}
		if live != nil {
			for _, at := range instants {
				for _, d := range live.derive(byInstant[at], md) {
					cls, dt := *d.Classifier, *d.DataType
					req, err := model.MeasurementRequestFor(ev, at, model.MeasurementReading{
						Name: d.Name, Value: d.Value, Classifier: &cls, Unit: d.Unit, DataType: &dt, Derived: true})
					require.NoError(r.t, err)
					reqs = append(reqs, req)
				}
			}
		}
		_, err := r.api.CreateMeasurementEvents(r.ctx(), r.tdb(), reqs)
		require.NoError(r.t, err)
	}
}

// row is a stored measurement as a test compares it.
type row struct {
	Device   string
	At       string
	Name     string
	Value    string
	Class    string
	Unit     string
	DataType string
}

// derived returns the derived rows (names the profile calculates) of the devices, sorted.
func (r *rig) derived(devices ...string) []row {
	r.t.Helper()
	calculated := []string{"power", "kw", "flag"}
	var rows []model.MeasurementEvent
	require.NoError(r.t, r.tdb().Where("device_token IN ? AND name IN ?", devices, calculated).
		Order("device_token, occurred_time, name").Find(&rows).Error)
	out := make([]row, 0, len(rows))
	for _, m := range rows {
		x := row{Device: m.DeviceToken, At: m.OccurredTime.UTC().Format(time.RFC3339Nano), Name: m.Name,
			Value: strconv.FormatFloat(m.Value.Float64, 'f', 8, 64)}
		if m.Classifier != nil {
			x.Class = strconv.Itoa(int(*m.Classifier))
		}
		if m.Unit != nil {
			x.Unit = *m.Unit
		}
		if m.DataType != nil {
			x.DataType = *m.DataType
		}
		out = append(out, x)
	}
	return out
}

// withoutDevice drops the device name so two devices' rows compare.
func withoutDevice(rows []row) []row {
	out := make([]row, len(rows))
	for i, x := range rows {
		x.Device = ""
		out[i] = x
	}
	return out
}

func (r *rig) countRows(device string) int64 {
	var n int64
	require.NoError(r.t, r.tdb().Model(&model.MeasurementEvent{}).Where("device_token = ?", device).Count(&n).Error)
	return n
}

// ---- jobs ---------------------------------------------------------------------------------

func (r *rig) newJob(token string, from, to time.Time, deviceTokens ...string) *model.CalculatedBackfill {
	r.t.Helper()
	doc := planDoc()
	rawPlan := mustJSON(doc)
	job := &model.CalculatedBackfill{
		Token: token, ProfileToken: "meter", ProfileVersionToken: "meter@1", ProfileVersion: 1,
		MetricKeys: mustJSON([]string{"power", "kw", "flag"}), Plan: rawPlan,
		RangeFrom: from, RangeTo: to,
	}
	job.TenantId = r.tenant
	if len(deviceTokens) > 0 {
		job.DeviceFilter = mustJSON(deviceTokens)
	}
	require.NoError(r.t, model.CreateBackfill(r.ctx(), r.tdb(), job))
	return job
}

func (r *rig) job(token string) model.CalculatedBackfill {
	r.t.Helper()
	var j model.CalculatedBackfill
	require.NoError(r.t, r.tdb().Where("token = ?", token).Take(&j).Error)
	return j
}

// runUntilDone makes passes until the job leaves the active statuses, failing if it does not
// within a bound.
func (r *rig) runUntilDone(token string) model.CalculatedBackfill {
	r.t.Helper()
	for i := 0; i < 200; i++ {
		_ = r.worker.RunOnce(context.Background())
		j := r.job(token)
		if j.Status != model.BackfillQueued && j.Status != model.BackfillRunning {
			return j
		}
	}
	r.t.Fatalf("job %s did not finish: %+v", token, r.job(token))
	return model.CalculatedBackfill{}
}

// ---- the roster ---------------------------------------------------------------------------

// fakeRoster serves a fixed device list in keyset pages, no larger than pageCap devices when
// that is set, and can be made to fail.
type fakeRoster struct {
	mu      sync.Mutex
	devices []RosterEntry
	pageCap int
	fail    error
	calls   int
}

func (f *fakeRoster) Page(_ context.Context, _, _ string, _ []string, tokens []string, afterId int64, limit int) (*RosterPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	if f.pageCap > 0 && limit > f.pageCap {
		limit = f.pageCap
	}
	want := map[string]bool{}
	for _, t := range tokens {
		want[t] = true
	}
	var eligible []RosterEntry
	for _, d := range f.devices {
		if d.DeviceId > afterId && (len(want) == 0 || want[d.Token]) {
			eligible = append(eligible, d)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].DeviceId < eligible[j].DeviceId })
	page := &RosterPage{}
	if afterId == 0 {
		var total int64
		for _, d := range f.devices {
			if len(want) == 0 || want[d.Token] {
				total++
			}
		}
		page.Total = &total
	}
	if len(eligible) > limit {
		page.Entries = eligible[:limit]
		page.NextCursor = page.Entries[len(page.Entries)-1].DeviceId
	} else {
		page.Entries = eligible
	}
	return page, nil
}

func device(id int64, token string, created time.Time, gain *float64) RosterEntry {
	e := RosterEntry{DeviceId: id, Token: token, CreatedAt: created}
	if gain != nil {
		e.Constants = []calcfield.Constant{calcfield.NewConstant("gain", *gain)}
	}
	return e
}

func fptr(v float64) *float64 { return &v }

func mustJSON(v any) []byte {
	b, err := jsonMarshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
