// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dc-microservice/svcclient"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-management/model"
)

// ---- the pacer ----------------------------------------------------------------------------

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
	return nil
}

func pacerOn(rate int) (*pacer, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_000_000, 0)}
	p := newPacer(rate)
	p.now, p.sleep, p.last = c.now, c.sleep, c.t
	return p, c
}

// A second's budget is spendable at once; beyond it the pacer waits for the rate, and an idle
// stretch refills the bucket only up to one second's worth.
func TestPacerHoldsTheRate(t *testing.T) {
	p, c := pacerOn(100)
	require.NoError(t, p.wait(context.Background(), 100))
	assert.Empty(t, c.slept, "a second's budget is free")

	require.NoError(t, p.wait(context.Background(), 50))
	require.Len(t, c.slept, 1)
	assert.Equal(t, 500*time.Millisecond, c.slept[0], "50 rows over the budget at 100 rows a second")

	c.t = c.t.Add(time.Hour) // idle
	c.slept = nil
	require.NoError(t, p.wait(context.Background(), 100))
	assert.Empty(t, c.slept)
	require.NoError(t, p.wait(context.Background(), 100))
	require.Len(t, c.slept, 1, "an hour idle does not bank an hour of rows")
	assert.Equal(t, time.Second, c.slept[0])
}

func TestPacerSpendsNothingForNothingAndStopsOnCancel(t *testing.T) {
	p, c := pacerOn(10)
	require.NoError(t, p.wait(context.Background(), 0))
	require.NoError(t, p.wait(context.Background(), -5))
	assert.Empty(t, c.slept)

	real := newPacer(1)
	real.tokens = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, real.wait(ctx, 1_000_000), context.Canceled, "a long wait ends with the context")
}

// ---- the frozen plan ----------------------------------------------------------------------

func TestPlanDocRoundTripAndCompile(t *testing.T) {
	raw, err := json.Marshal(planDocForTest())
	require.NoError(t, err)
	doc, err := model.ParseCalculatedPlanDoc(raw)
	require.NoError(t, err)
	plan, err := doc.Compile()
	require.NoError(t, err)
	closure, err := plan.Closure([]string{"kw"})
	require.NoError(t, err)
	assert.Equal(t, []string{"power", "kw"}, closure, "a chained metric brings what it reads")
	_, err = plan.Closure([]string{"nope"})
	assert.Error(t, err, "a key the plan lacks is refused")
}

func TestPlanDocRefusesWhatItCannotReadExactly(t *testing.T) {
	_, err := model.ParseCalculatedPlanDoc([]byte(`{"version":1,"surprise":true}`))
	assert.Error(t, err)
	_, err = model.ParseCalculatedPlanDoc([]byte(`not json`))
	assert.Error(t, err)

	doc := planDocForTest()
	doc.Measured = append(doc.Measured, model.CalculatedPlanInput{Key: "label", DataType: "STRING"})
	_, err = doc.Compile()
	assert.Error(t, err, "a measured metric no formula can read is not silently dropped")
}

func TestPlanDocLeavesOutAMetricNoFormulaCanProduce(t *testing.T) {
	doc := planDocForTest()
	doc.Calculated = append(doc.Calculated, model.CalculatedPlanMetric{Key: "text", Expression: "1", DataType: "STRING"})
	plan, err := doc.Compile()
	require.NoError(t, err)
	_, err = plan.Closure([]string{"text"})
	assert.Error(t, err, "selecting it is refused by the closure, as the live resolver leaves it out")
}

func planDocForTest() model.CalculatedPlanDoc {
	return model.CalculatedPlanDoc{
		VersionToken: "m@1", Version: 1, ConstantKeys: []string{},
		Measured: []model.CalculatedPlanInput{{Key: "voltage", DataType: "DOUBLE"}, {Key: "current", DataType: "DOUBLE"}},
		Calculated: []model.CalculatedPlanMetric{
			{Key: "kw", Expression: "power / 1000.0", DataType: "DOUBLE", Classifier: 6},
			{Key: "power", Expression: "voltage * current", DataType: "DOUBLE", Classifier: 5},
		},
	}
}

// A job whose frozen plan this build cannot run is refused when it is claimed, naming why.
func TestNewRunRefusesAJobItCannotRun(t *testing.T) {
	w := &Worker{cfg: Config{RowsPerSecond: 1000}}
	good := func() *model.CalculatedBackfill {
		raw, _ := json.Marshal(planDocForTest())
		return &model.CalculatedBackfill{Plan: raw, MetricKeys: []byte(`["kw"]`), Skipped: []byte(`{}`)}
	}
	run, err := w.newRun(nil, good())
	require.NoError(t, err)
	assert.Equal(t, []string{"voltage", "current", "kw"}, run.names, "a read selects the inputs and the selected keys")
	assert.Equal(t, 500, run.readRows)

	for name, mutate := range map[string]func(*model.CalculatedBackfill){
		"plan is not json":      func(j *model.CalculatedBackfill) { j.Plan = []byte(`[`) },
		"no keys":               func(j *model.CalculatedBackfill) { j.MetricKeys = []byte(`[]`) },
		"keys are not json":     func(j *model.CalculatedBackfill) { j.MetricKeys = []byte(`{`) },
		"key the plan lacks":    func(j *model.CalculatedBackfill) { j.MetricKeys = []byte(`["ghost"]`) },
		"skip counts are junk":  func(j *model.CalculatedBackfill) { j.Skipped = []byte(`[`) },
		"device filter is junk": func(j *model.CalculatedBackfill) { j.DeviceFilter = []byte(`{`) },
	} {
		j := good()
		mutate(j)
		_, err := w.newRun(nil, j)
		assert.Error(t, err, name)
	}
}

// ---- small decisions ----------------------------------------------------------------------

func TestRetentionMarginIsTwoChunks(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	w := &Worker{now: func() time.Time { return now }, cfg: Config{RetentionDays: 10, ChunkInterval: 24 * time.Hour}}
	r := &jobRun{w: w}
	// horizon now-10d, plus two chunks: now-8d.
	floor := now.AddDate(0, 0, -8)
	assert.True(t, r.beyondRetention(chunkWindow{End: floor}), "a chunk ending at the margin is skipped")
	assert.False(t, r.beyondRetention(chunkWindow{End: floor.Add(time.Second)}))
	w.cfg.RetentionDays = 0
	assert.False(t, r.beyondRetention(chunkWindow{End: floor.AddDate(-1, 0, 0)}), "no retention, no skip")
}

func TestOnlyTheDecompressionLimitIsMapped(t *testing.T) {
	assert.True(t, isDecompressionLimit(&pgconn.PgError{Code: "53400"}))
	assert.False(t, isDecompressionLimit(&pgconn.PgError{Code: "55P03"}))
	assert.False(t, isDecompressionLimit(context.Canceled))
}

func TestAccumulationFoldsIntoOneDelta(t *testing.T) {
	a, b := newAcc(), newAcc()
	a.d.Samples, a.d.WindowsCompressed, a.skips["missing_input"] = 2, 1, 3
	b.d.Samples, b.d.Written, b.skips["missing_input"], b.skips["non_finite"] = 5, 4, 1, 2
	a.add(b)
	d := a.delta(map[string]int64{"missing_input": 10, "range": 1})
	assert.EqualValues(t, 7, d.Samples)
	assert.EqualValues(t, 4, d.Written)
	assert.EqualValues(t, 1, d.WindowsCompressed)
	assert.Equal(t, map[string]int64{"missing_input": 14, "non_finite": 2, "range": 1}, d.Skipped,
		"the skip map is the job's whole map after the unit")
	assert.True(t, newAcc().empty())
	assert.False(t, a.empty())
	assert.Nil(t, newAcc().delta(map[string]int64{"x": 1}).Skipped, "nothing skipped leaves the stored map alone")
}

// ---- the device-management door -----------------------------------------------------------

// The document the worker sends is validated against the schema device-management serves, since
// the compiler does not read it: a door that renamed a field would otherwise fail only at run time.
//
// RUN WITH -count=1 AFTER TOUCHING THAT SCHEMA: the SDL lives outside this module and Go's test
// cache does not track it.
func TestRosterQueryIsValidAgainstTheServedSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	require.NoError(t, err)
	require.True(t, served, "device-management serves a tenant schema")
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	require.NoError(t, err)
	for _, e := range schema.Validate(rosterQuery) {
		// No variable values are supplied, so a required variable is "missing"; that is the
		// one rule that cannot apply here.
		if e.Rule == "VariablesOfCorrectType" {
			continue
		}
		t.Errorf("the roster query is invalid against the served schema: %v", e)
	}
	// And the check can fail: a field the door does not have is reported.
	bad := `query { calculatedBackfillRoster(profileToken: "p", constantKeys: [], limit: 1) { entries { nope } } }`
	assert.NotEmpty(t, schema.Validate(bad))
}

func rosterServer(t *testing.T, respond func(vars map[string]any) any) (*httptest.Server, *svcclient.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "t", ExpiresAt: 1 << 40})
			return
		}
		assert.Equal(t, "acme", r.Header.Get(auth.ServiceTenantHeader))
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": respond(body.Variables)})
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return srv, svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "s", "event-management",
		[]string{string(auth.DeviceRead)})
}

func TestGraphQLRosterDecodesAPage(t *testing.T) {
	var sent map[string]any
	srv, client := rosterServer(t, func(vars map[string]any) any {
		sent = vars
		return map[string]any{"calculatedBackfillRoster": map[string]any{
			"entries": []map[string]any{
				{"deviceId": "7", "deviceToken": "d7", "createdAt": "2026-10-01T00:00:00.5Z",
					"constants": []map[string]any{{"key": "gain", "number": 2.5}, {"key": "bad", "number": nil}}},
				{"deviceId": "9", "deviceToken": "d9", "createdAt": "2026-10-02T00:00:00Z", "constants": []any{}},
			},
			"nextCursor": "9", "total": 120.0,
		}}
	})
	page, err := NewGraphQLRoster(client, srv.URL).Page(context.Background(), "acme", "meter",
		[]string{"gain", "bad"}, []string{"d7"}, 5, 100)
	require.NoError(t, err)
	assert.Equal(t, "5", sent["afterId"])
	assert.EqualValues(t, 100, sent["limit"])
	assert.Equal(t, []any{"d7"}, sent["deviceTokens"])
	require.Len(t, page.Entries, 2)
	assert.EqualValues(t, 7, page.Entries[0].DeviceId)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 500_000_000, time.UTC), page.Entries[0].CreatedAt)
	assert.Len(t, page.Entries[0].Constants, 2, "a present-but-unusable constant is kept, as invalid")
	assert.Empty(t, page.Entries[1].Constants)
	assert.EqualValues(t, 9, page.NextCursor)
	require.NotNil(t, page.Total)
	assert.EqualValues(t, 120, *page.Total)
}

func TestGraphQLRosterLastPageAndFirstPageVariables(t *testing.T) {
	var sent map[string]any
	srv, client := rosterServer(t, func(vars map[string]any) any {
		sent = vars
		return map[string]any{"calculatedBackfillRoster": map[string]any{"entries": []any{}, "nextCursor": nil, "total": nil}}
	})
	page, err := NewGraphQLRoster(client, srv.URL).Page(context.Background(), "acme", "meter", nil, nil, 0, 100)
	require.NoError(t, err)
	assert.Zero(t, page.NextCursor)
	assert.Nil(t, page.Total)
	assert.NotContains(t, sent, "afterId", "the first page names no cursor")
	assert.NotContains(t, sent, "deviceTokens", "no filter is no filter")
	assert.Equal(t, []any{}, sent["constantKeys"], "the required list is sent empty, never null")
}

func TestGraphQLRosterRefusesWhatItCannotRead(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"id":   {"deviceId": "x", "deviceToken": "d", "createdAt": "2026-10-01T00:00:00Z", "constants": []any{}},
		"time": {"deviceId": "1", "deviceToken": "d", "createdAt": "yesterday", "constants": []any{}},
	} {
		srv, client := rosterServer(t, func(map[string]any) any {
			return map[string]any{"calculatedBackfillRoster": map[string]any{"entries": []any{entry}}}
		})
		_, err := NewGraphQLRoster(client, srv.URL).Page(context.Background(), "acme", "meter", nil, nil, 0, 1)
		assert.Error(t, err, name)
	}
}

// A stored sample that carries none of a metric's inputs is not its sample: the backfill counts
// it as no skip, exactly as live resolution reports nothing for it. One that carries some of
// the inputs is a real missing_input.
func TestEvaluateDoesNotCountSamplesThatCarryNoneOfTheInputs(t *testing.T) {
	doc := &model.CalculatedPlanDoc{
		Measured: []model.CalculatedPlanInput{{Key: "temp", DataType: "DOUBLE"}, {Key: "battery", DataType: "DOUBLE"},
			{Key: "voltage", DataType: "DOUBLE"}, {Key: "current", DataType: "DOUBLE"}},
		Calculated: []model.CalculatedPlanMetric{
			{Key: "battery_pct", Expression: "battery * 100.0", DataType: "DOUBLE"},
			{Key: "power", Expression: "voltage * current", DataType: "DOUBLE"},
		},
	}
	plan, err := doc.Compile()
	require.NoError(t, err)
	keys := []string{"battery_pct", "power"}
	ev, err := model.NewPlanEvaluator(plan, keys, nil)
	require.NoError(t, err)
	defer ev.Release()
	r := &jobRun{doc: doc, plan: plan, keys: keys, selected: map[string]bool{"battery_pct": true, "power": true}}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	row := func(name string, v float64) model.MeasurementEvent {
		return model.MeasurementEvent{EventId: []byte("e"), DeviceToken: "d", OccurredTime: at, Name: name,
			Value: sql.NullFloat64{Float64: v, Valid: true}}
	}
	local := newAcc()
	// temp only: neither metric reads it.
	_, err = r.evaluate(ev, RosterEntry{Token: "d"}, []model.MeasurementEvent{row("temp", 20)}, local)
	require.NoError(t, err)
	assert.Empty(t, local.skips)
	// voltage without current: a real partial-input skip, for power only.
	_, err = r.evaluate(ev, RosterEntry{Token: "d"}, []model.MeasurementEvent{row("voltage", 230)}, local)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"missing_input": 1}, local.skips)
}
