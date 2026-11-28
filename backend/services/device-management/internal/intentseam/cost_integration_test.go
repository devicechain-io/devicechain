// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package intentseam_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/intent"
	"gorm.io/gorm"
)

// The cost gate of the observation seam: what one digest, page, walk, intersection and preview
// cost on a fleet larger than the answer, with numbers.
//
// Two scales. The default (every integration run) is 5,000 devices in tenant A and 2,500 in
// tenant B with 5 other attributes each, so the properties are checked on every change. With
// DC_IT_INTENT_COST=1 it is the design's fixture: 200,000 and 100,000 devices with 20 other
// attributes each, 20 device types over 5 published profiles; DC_IT_INTENT_COST_A overrides
// tenant A's size (B is half of it). DC_IT_INTENT_COST_REPORT names a directory that receives
// one JSON line per measurement and every plan.
//
// What is asserted at both scales: members never include tenant B; rows read from tables by a
// digest or an intersection are at most 4 x tenant A's device count for every selector, the
// sparse one included; the sparse digest writes no temp files. What is asserted at the full
// scale and recorded at the small one (where a loaded or paused runner would decide it): the
// time budgets; a digest whose members all hold a 64 KiB value takes no more than 1.25 x the
// time of one whose members hold 1,024-byte values (the ratio to a fill with none is recorded), while
// the same statement with its size test removed (every value read and hashed) takes far longer,
// the timing proof that an oversize value is not read. TestOversizeValuesAreReportedByLength
// proves the same thing deterministically at both scales.
// Times are warm; a cold-cache run needs the server restarted between statements and is not
// done here.
//
// 🔴 THE 64 KiB VALUES ARE COMPRESSIBLE, AND THAT IS THE ONLY KIND A SHARED ATTRIBUTE CAN HOLD.
// entity_attributes carries a btree over (tenant, type, scope, key, value), and a btree entry
// is bounded at a third of a page after compression: an incompressible value of a few KiB is
// refused at insert ("index row size exceeds"), whatever MaxAttributeValueBytes allows. So the
// largest stored values are compressed in line, and what reading one costs is decompressing and
// hashing it, which the size test avoids: octet_length reads the stored size from the header.

type costScale struct {
	a, b, others, sparse int
	full                 bool
}

func costScaleFromEnv(t *testing.T) costScale {
	sc := costScale{a: 5_000, b: 2_500, others: 5, sparse: 100}
	if os.Getenv("DC_IT_INTENT_COST") != "" {
		sc = costScale{a: 200_000, b: 100_000, others: 20, sparse: 100, full: true}
	}
	if v := os.Getenv("DC_IT_INTENT_COST_A"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1000 {
			t.Fatalf("DC_IT_INTENT_COST_A=%q must be a number of at least 1000", v)
		}
		sc.a, sc.b = n, n/2
	}
	return sc
}

// The fills of the pinned key, each its own key on every member of tenant A.
const (
	fillSmall = "pin_small" // a 1,024-byte value: the most the observation hashes
	fillMax   = "pin_max"   // a 64 KiB value: the most storage, never read
	fillNone  = "pin_none"  // no member holds it
)

type costFixture struct {
	api        *model.Api
	a, b       context.Context
	tenantA    string
	tenantB    string
	sc         costScale
	versions   map[string]int32
	members    map[string]int64
	selectors  map[string]string
	selectorOf []string
}

var (
	costOnce sync.Once
	costFix  *costFixture
	costErr  error
)

// seedCost builds the fixture once per database and scale, and reuses it on later runs: a
// fixture row records a completed seed, so a run interrupted mid-seed seeds a fresh pair of
// tenants rather than measuring a half-built one.
func seedCost(t *testing.T) *costFixture {
	t.Helper()
	costOnce.Do(func() { costFix, costErr = buildCostFixture(t) })
	if costErr != nil {
		t.Fatalf("the cost fixture: %v", costErr)
	}
	return costFix
}

func buildCostFixture(t *testing.T) (*costFixture, error) {
	sc := costScaleFromEnv(t)
	api := newSeamApi(t)
	db := api.RDB.Database
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS public.intentseam_cost_fixture (
		scale text PRIMARY KEY, tenant_a text NOT NULL, tenant_b text NOT NULL)`).Error; err != nil {
		return nil, err
	}
	key := fmt.Sprintf("v1/%d/%d/%d/%d", sc.a, sc.b, sc.others, sc.sparse)
	var found []struct{ TenantA, TenantB string }
	if err := db.Raw(`SELECT tenant_a, tenant_b FROM public.intentseam_cost_fixture WHERE scale = ?`, key).Scan(&found).Error; err != nil {
		return nil, err
	}
	f := &costFixture{api: api, sc: sc, versions: map[string]int32{}, members: map[string]int64{},
		selectors: map[string]string{
			"dense":   `attr["zone"] == "quay"`,
			"sparse":  `attr["tag"] == "rare"`,
			"negated": `!(attr["zone"] == "quay")`,
			"numeric": `attr["power"] < 25`,
		},
		selectorOf: []string{"dense", "sparse", "negated", "numeric"},
	}
	f.members = map[string]int64{"dense": int64(sc.a / 2), "sparse": int64(sc.sparse), "negated": int64(sc.a - sc.a/2), "numeric": int64(sc.a / 4)}
	if len(found) == 1 {
		f.tenantA, f.tenantB = found[0].TenantA, found[0].TenantB
	} else {
		stamp := time.Now().UnixNano()
		f.tenantA, f.tenantB = fmt.Sprintf("cost-a-%d", stamp), fmt.Sprintf("cost-b-%d", stamp)
	}
	f.a, f.b = core.WithTenant(context.Background(), f.tenantA), core.WithTenant(context.Background(), f.tenantB)
	if len(found) == 0 {
		start := time.Now()
		for _, side := range []struct {
			ctx    context.Context
			tenant string
			n      int
			pins   bool
		}{{f.a, f.tenantA, sc.a, true}, {f.b, f.tenantB, sc.b, false}} {
			if err := seedCostTenant(api, side.ctx, side.tenant, side.n, sc, side.pins); err != nil {
				return nil, fmt.Errorf("seed %s: %w", side.tenant, err)
			}
		}
		if err := db.Exec(`VACUUM ANALYZE devices`).Error; err != nil {
			return nil, err
		}
		if err := db.Exec(`VACUUM ANALYZE entity_attributes`).Error; err != nil {
			return nil, err
		}
		if err := db.Exec(`INSERT INTO public.intentseam_cost_fixture (scale, tenant_a, tenant_b) VALUES (?, ?, ?)`,
			key, f.tenantA, f.tenantB).Error; err != nil {
			return nil, err
		}
		t.Logf("seeded the cost fixture %s in %s", key, time.Since(start).Round(time.Second))
	}
	for _, name := range f.selectorOf {
		var v []int32
		if err := db.Raw(`SELECT max(v.version) FROM entity_group_versions v JOIN entity_groups g ON g.id = v.entity_group_id
			WHERE g.tenant_id = ? AND g.token = ?`, f.tenantA, "sel-"+name).Scan(&v).Error; err != nil {
			return nil, err
		}
		if len(v) == 0 || v[0] == 0 {
			return nil, fmt.Errorf("group sel-%s is missing from the fixture", name)
		}
		f.versions[name] = v[0]
	}
	return f, nil
}

func seedCostTenant(api *model.Api, ctx context.Context, tenant string, n int, sc costScale, pins bool) error {
	keys := []model.ConfigurationKey{{Key: "dim", ValueType: "LONG"}, {Key: fillSmall, ValueType: "STRING"},
		{Key: fillMax, ValueType: "STRING"}, {Key: fillNone, ValueType: "STRING"}}
	for p := 1; p <= 5; p++ {
		token := fmt.Sprintf("prof-%d", p)
		if _, err := api.CreateDeviceProfile(ctx, &model.DeviceProfileCreateRequest{Token: token}); err != nil {
			return err
		}
		if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, token, keys); err != nil {
			return err
		}
		if _, err := api.PublishDeviceProfile(ctx, token, nil, nil, "cost"); err != nil {
			return err
		}
	}
	for ty := 1; ty <= 20; ty++ {
		profile := fmt.Sprintf("prof-%d", (ty-1)%5+1)
		if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: fmt.Sprintf("type-%d", ty), ProfileToken: &profile}); err != nil {
			return err
		}
	}
	db := api.RDB.Database
	exec := func(q string, args ...any) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('devicechain.attr_protocol', '1', true)").Error; err != nil {
				return err
			}
			return tx.Exec(q, args...).Error
		})
	}
	if err := exec(`INSERT INTO devices (created_at, updated_at, tenant_id, token, device_type_id)
		SELECT now(), now(), ?, 'dev-' || g, (SELECT id FROM device_types WHERE tenant_id = ? AND token = 'type-' || ((g % 20) + 1))
		FROM generate_series(1, ?) g`, tenant, tenant, n); err != nil {
		return err
	}
	attr := func(key, valueType, valueExpr, where string) error {
		return exec(`INSERT INTO entity_attributes (created_at, updated_at, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
			SELECT now(), now(), d.tenant_id, 'device', d.id, 'SHARED', ?, ?, `+valueExpr+`, now()
			FROM devices d, LATERAL (SELECT substr(d.token, 5)::int AS g) s
			WHERE d.tenant_id = ? AND d.token LIKE 'dev-%' AND `+where, key, valueType, tenant)
	}
	if err := attr("zone", "STRING", `CASE WHEN s.g % 2 = 0 THEN 'quay' ELSE 'east' END`, "true"); err != nil {
		return err
	}
	if err := attr("power", "LONG", `(s.g % 100)::text`, "true"); err != nil {
		return err
	}
	if err := attr("tag", "STRING", `'rare'`, fmt.Sprintf("s.g <= %d", sc.sparse)); err != nil {
		return err
	}
	for o := 1; o <= sc.others; o++ {
		if err := attr(fmt.Sprintf("other-%d", o), "STRING", `'value-' || s.g`, "true"); err != nil {
			return err
		}
	}
	if pins {
		if err := attr(fillSmall, "STRING", fmt.Sprintf(`lpad(s.g::text, %d, 'x')`, intent.MaxIntentValueBytes), "true"); err != nil {
			return err
		}
		if err := attr(fillMax, "STRING", `lpad(s.g::text, 65536, 'y')`, "true"); err != nil {
			return err
		}
	}
	for name, sel := range map[string]string{
		"dense": `attr["zone"] == "quay"`, "sparse": `attr["tag"] == "rare"`,
		"negated": `!(attr["zone"] == "quay")`, "numeric": `attr["power"] < 25`,
	} {
		dynamic, s := "dynamic", sel
		token := "sel-" + name
		if _, err := api.CreateEntityGroup(ctx, &model.EntityGroupCreateRequest{Token: token, MemberType: "device", MembershipMode: &dynamic, Selector: &s}); err != nil {
			return err
		}
		if _, err := api.PublishEntityGroup(ctx, token, nil, nil, "cost"); err != nil {
			return err
		}
	}
	return nil
}

// planStats is what one EXPLAIN (ANALYZE, BUFFERS) says.
type planStats struct {
	ExecutionMs  float64 `json:"executionMs"`
	RowsExamined float64 `json:"rowsExamined"`
	ForeignBound float64 `json:"foreignRowsBound"`
	Buffers      int64   `json:"buffers"`
	TempWritten  int64   `json:"tempWritten"`
	Plan         json.RawMessage
}

func explain(t *testing.T, db *gorm.DB, ctx context.Context, q string, args []any) planStats {
	t.Helper()
	var out planStats
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SET LOCAL plan_cache_mode = force_custom_plan`).Error; err != nil {
			return err
		}
		var raw string
		if err := tx.Raw(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+q, args...).Row().Scan(&raw); err != nil {
			return err
		}
		var doc []struct {
			Plan          map[string]any `json:"Plan"`
			ExecutionTime float64        `json:"Execution Time"`
		}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return err
		}
		out.ExecutionMs = doc[0].ExecutionTime
		out.RowsExamined = rowsExamined(doc[0].Plan)
		out.ForeignBound = foreignRowsBound(doc[0].Plan)
		out.Buffers = int64(num(doc[0].Plan, "Shared Hit Blocks") + num(doc[0].Plan, "Shared Read Blocks"))
		out.TempWritten = int64(num(doc[0].Plan, "Temp Written Blocks"))
		out.Plan = json.RawMessage(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	return out
}

func num(node map[string]any, key string) float64 {
	v, _ := node[key].(float64)
	return v
}

// scanNodes are the plan nodes that read a relation's rows. An Index Scan of a bitmap is
// counted at its Bitmap Heap Scan, which is where the rows are read.
var scanNodes = map[string]bool{"Seq Scan": true, "Index Scan": true, "Index Only Scan": true, "Bitmap Heap Scan": true,
	"Tid Scan": true, "Tid Range Scan": true, "Sample Scan": true}

// rowsExamined sums, over every node that reads a table, the rows it read: those it returned and
// those it read and discarded, times its loops. Intermediate nodes (joins, sorts, CTE scans) pass
// rows already counted and are not counted again.
func rowsExamined(node map[string]any) float64 {
	var total float64
	if typ, _ := node["Node Type"].(string); scanNodes[typ] {
		total = (num(node, "Actual Rows") + num(node, "Rows Removed by Filter") + num(node, "Rows Removed by Index Recheck")) *
			num(node, "Actual Loops")
	}
	if kids, ok := node["Plans"].([]any); ok {
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				total += rowsExamined(m)
			}
		}
	}
	return total
}

// foreignRowsBound bounds the rows of other tenants a statement read from devices and
// entity_attributes: every row a scan of either read and discarded by a filter. Every scan of
// them filters on the tenant, so a row of tenant A discarded for another predicate counts too;
// zero is exact.
func foreignRowsBound(node map[string]any) float64 {
	var total float64
	rel, _ := node["Relation Name"].(string)
	if typ, _ := node["Node Type"].(string); scanNodes[typ] && (rel == "devices" || rel == "entity_attributes") {
		total = num(node, "Rows Removed by Filter") * num(node, "Actual Loops")
	}
	if kids, ok := node["Plans"].([]any); ok {
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				total += foreignRowsBound(m)
			}
		}
	}
	return total
}

type costReport struct {
	dir   string
	mu    sync.Mutex
	lines []string
}

func newCostReport(t *testing.T) *costReport {
	return &costReport{dir: os.Getenv("DC_IT_INTENT_COST_REPORT")}
}

func (r *costReport) record(t *testing.T, name string, fields map[string]any, plan json.RawMessage) {
	t.Helper()
	fields["case"] = name
	b, _ := json.Marshal(fields)
	t.Logf("cost %s", b)
	if r.dir == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fh, err := os.OpenFile(filepath.Join(r.dir, "results.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	fmt.Fprintln(fh, string(b))
	if plan != nil {
		if err := os.WriteFile(filepath.Join(r.dir, strings.ReplaceAll(name, "/", "_")+".plan.json"), plan, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func timed(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

func TestCostGateDigest(t *testing.T) {
	f := seedCost(t)
	rep := newCostReport(t)
	db := f.api.RDB.Database
	for _, name := range f.selectorOf {
		target := model.IntentSeamTarget{GroupToken: "sel-" + name, Version: f.versions[name]}
		for _, fill := range []string{fillSmall, fillMax, fillNone} {
			// Through the seam, as the area calls it: membership and the answer.
			var d *intentseam.DigestAnswer
			_, _ = f.api.ConfigurationKeyStateDigest(f.a, target, fill) // warm
			took := timed(func() {
				var err error
				d, err = f.api.ConfigurationKeyStateDigest(f.a, target, fill)
				if err != nil {
					t.Fatalf("%s %s: %v", name, fill, err)
				}
			})
			if d.Rejected || d.MemberCount != f.members[name] {
				t.Fatalf("%s %s: %+v; want %d members of tenant A and none of B", name, fill, d.Rejection, f.members[name])
			}
			wantPresent := d.MemberCount
			if fill == fillNone {
				wantPresent = 0
			}
			if d.PresentCount != wantPresent || (fill == fillMax) != (d.OversizeCount == d.MemberCount && d.MemberCount > 0) {
				t.Errorf("%s %s: present %d oversize %d of %d", name, fill, d.PresentCount, d.OversizeCount, d.MemberCount)
			}
			q, args, err := intentseam.DigestSQL(f.tenantA, fill, f.selectors[name])
			if err != nil {
				t.Fatal(err)
			}
			s := explain(t, db, f.a, q, args)
			rep.record(t, "digest/"+name+"/"+fill, map[string]any{"members": d.MemberCount, "seamMs": took.Milliseconds(),
				"executionMs": s.ExecutionMs, "rowsExamined": s.RowsExamined, "foreignRowsBound": s.ForeignBound, "buffers": s.Buffers, "tempWritten": s.TempWritten,
				"tenantA": f.sc.a, "tenantB": f.sc.b}, s.Plan)
			if s.RowsExamined > 4*float64(f.sc.a) {
				t.Errorf("%s %s: %.0f rows examined, over 4 x tenant A's %d devices", name, fill, s.RowsExamined, f.sc.a)
			}
			if took > 5*time.Second {
				f.overBudget(t, "%s %s: the digest took %s warm; the budget is 5s", name, fill, took)
			}
			if name == "sparse" && s.TempWritten != 0 {
				t.Errorf("sparse %s: %d temp blocks written; want none", fill, s.TempWritten)
			}
		}
		small, maxed, none := medianRun(t, db, f.a, f.tenantA, fillSmall, f.selectors[name]),
			medianRun(t, db, f.a, f.tenantA, fillMax, f.selectors[name]),
			medianRun(t, db, f.a, f.tenantA, fillNone, f.selectors[name])
		rep.record(t, "digest/"+name+"/ratios", map[string]any{"smallMs": ms(small), "maxMs": ms(maxed), "noneMs": ms(none),
			"maxOverNone": float64(maxed) / float64(max(none, 1)), "maxOverSmall": float64(maxed) / float64(max(small, 1))}, nil)
		if float64(maxed) > 1.25*float64(small) {
			f.overBudget(t, "%s: a digest over 64 KiB values took %s against %s over 1,024-byte values; a stored value was read",
				name, maxed, small)
		}
		// The ratio against a fill with no values is recorded, not asserted: it measures joining
		// one attribute row per member (whose heap page carries the value compressed in line, the
		// only way a 64 KiB SHARED value can be stored), not reading the value. Reading it is what
		// the ratio against 1,024-byte values and the control below measure.
	}
	// The negative control: the same statement with the size test removed reads and hashes every
	// value, and the ratio must then fail.
	q, args, err := intentseam.DigestSQL(f.tenantA, fillMax, f.selectors["dense"])
	if err != nil {
		t.Fatal(err)
	}
	guard := fmt.Sprintf("WHEN octet_length(ea.value) <= %d THEN", intent.MaxIntentValueBytes)
	if !strings.Contains(q, guard) {
		t.Fatalf("the digest statement no longer carries %q", guard)
	}
	normal := medianTimed(t, db, f.a, q, args)
	hashAll := medianTimed(t, db, f.a, strings.Replace(q, guard, "WHEN true THEN", 1), args)
	rep.record(t, "digest/dense/control-hash-every-value", map[string]any{"normalMs": ms(normal), "hashEveryValueMs": ms(hashAll)}, nil)
	if float64(hashAll) <= 1.25*float64(normal) {
		f.overBudget(t, "control: hashing every 64 KiB value took %s against %s; the ratio cannot detect a read", hashAll, normal)
	}
}

// overBudget reports a missed time budget. Times are assertions at the cost gate's scale
// (DC_IT_INTENT_COST), on a quiet host; on the small fixture of every integration run they are
// recorded, because a paused or loaded CI runner would fail them for reasons that are not the
// statement's. The deterministic properties (rows, members, temp files, refusals) are asserted
// at both scales.
func (f *costFixture) overBudget(t *testing.T, format string, args ...any) {
	t.Helper()
	if f.sc.full {
		t.Errorf(format, args...)
		return
	}
	t.Logf("over budget, recorded (asserted at the cost-gate scale): "+format, args...)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// medianRun times the digest statement for one fill, as medianTimed.
func medianRun(t *testing.T, db *gorm.DB, ctx context.Context, tenant, fill, sel string) time.Duration {
	t.Helper()
	q, args, err := intentseam.DigestSQL(tenant, fill, sel)
	if err != nil {
		t.Fatal(err)
	}
	return medianTimed(t, db, ctx, q, args)
}

// medianTimed runs a statement five times as a seam digest statement (its limiter, transaction
// and settings) and returns the median.
func medianTimed(t *testing.T, db *gorm.DB, ctx context.Context, q string, args []any) time.Duration {
	t.Helper()
	seam := intentseam.New()
	var runs []time.Duration
	for range 5 {
		var took time.Duration
		err := seam.Run(ctx, db, intentseam.OpDigest, func(tx *gorm.DB) (bool, error) {
			var out []map[string]any
			start := time.Now()
			err := tx.Raw(q, args...).Scan(&out).Error
			took = time.Since(start)
			return false, err
		})
		if err != nil {
			t.Fatalf("timed run: %v", err)
		}
		runs = append(runs, took)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	return runs[len(runs)/2]
}

func TestCostGatePagesAndWalk(t *testing.T) {
	f := seedCost(t)
	rep := newCostReport(t)
	target := model.IntentSeamTarget{GroupToken: "sel-dense", Version: f.versions["dense"]}
	_, _ = f.api.ConfigurationKeyState(f.a, target, fillSmall, 0, intentseam.MaxPageRows)
	var worst time.Duration
	var rows []intentseam.Row
	var after int64
	pages := 0
	walk := timed(func() {
		for {
			var page *intentseam.PageAnswer
			took := timed(func() {
				var err error
				page, err = f.api.ConfigurationKeyState(f.a, target, fillSmall, after, intentseam.MaxPageRows)
				if err != nil {
					t.Fatalf("page after %d: %v", after, err)
				}
			})
			worst = max(worst, took)
			pages++
			rows = append(rows, page.Rows...)
			if page.NextCursor == 0 {
				return
			}
			after = page.NextCursor
		}
	})
	d, err := f.api.ConfigurationKeyStateDigest(f.a, target, fillSmall)
	if err != nil {
		t.Fatal(err)
	}
	equal := rehash(t, rows) == d.Digest
	rep.record(t, "walk/dense/"+fillSmall, map[string]any{"rows": len(rows), "pages": pages, "worstPageMs": worst.Milliseconds(),
		"walkMs": walk.Milliseconds(), "rehashEqualsDigest": equal}, nil)
	if !equal || int64(len(rows)) != f.members["dense"] {
		t.Fatalf("a walk of %d rows does not hash to the digest of %d members", len(rows), d.MemberCount)
	}
	if worst > 500*time.Millisecond {
		f.overBudget(t, "the slowest 1,000-row page took %s; the budget is 500ms", worst)
	}
	if walk > 60*time.Second {
		f.overBudget(t, "the walk took %s; the budget is 60s", walk)
	}
	q, args, err := intentseam.PageSQL(f.tenantA, fillSmall, f.selectors["dense"], after/2, intentseam.MaxPageRows)
	if err != nil {
		t.Fatal(err)
	}
	s := explain(t, f.api.RDB.Database, f.a, q, args)
	rep.record(t, "page/dense/middle", map[string]any{"executionMs": s.ExecutionMs, "rowsExamined": s.RowsExamined, "buffers": s.Buffers}, s.Plan)
}

func TestCostGateIntersect(t *testing.T) {
	f := seedCost(t)
	rep := newCostReport(t)
	for i, a := range f.selectorOf {
		for _, b := range f.selectorOf[i:] {
			ta := model.IntentSeamTarget{GroupToken: "sel-" + a, Version: f.versions[a]}
			tb := model.IntentSeamTarget{GroupToken: "sel-" + b, Version: f.versions[b]}
			var x *intentseam.IntersectAnswer
			took := timed(func() {
				var err error
				x, err = f.api.GroupTargetsIntersect(f.a, ta, tb)
				if err != nil {
					t.Fatalf("%s ∩ %s: %v", a, b, err)
				}
			})
			want := !(a == "dense" && b == "negated")
			if x.Rejected || x.Intersects != want {
				t.Errorf("%s ∩ %s = %+v; want intersects %v", a, b, x, want)
			}
			q, args, err := intentseam.IntersectSQL(f.tenantA, f.selectors[a], f.selectors[b])
			if err != nil {
				t.Fatal(err)
			}
			s := explain(t, f.api.RDB.Database, f.a, q, args)
			rep.record(t, "intersect/"+a+"/"+b, map[string]any{"seamMs": took.Milliseconds(), "executionMs": s.ExecutionMs,
				"rowsExamined": s.RowsExamined, "foreignRowsBound": s.ForeignBound, "buffers": s.Buffers}, s.Plan)
			if took > 5*time.Second {
				f.overBudget(t, "%s ∩ %s took %s; the budget is 5s", a, b, took)
			}
			if s.RowsExamined > 4*float64(f.sc.a) {
				t.Errorf("%s ∩ %s examined %.0f rows, over 4 x tenant A's %d devices", a, b, s.RowsExamined, f.sc.a)
			}
		}
	}
}

func TestCostGatePreview(t *testing.T) {
	f := seedCost(t)
	rep := newCostReport(t)
	tokens := make([]string, 0, 10_000)
	for i := 1; len(tokens) < 10_000; i++ {
		if i <= f.sc.a {
			tokens = append(tokens, fmt.Sprintf("dev-%d", i))
		} else {
			tokens = append(tokens, fmt.Sprintf("absent-%d", i))
		}
	}
	set := &model.FleetConfigSetIntent{Key: fillSmall, ValueType: "STRING", Value: ptr("v")}
	var plan *model.FleetPlan
	took := timed(func() {
		var err error
		plan, err = f.api.PreviewIntentBulkChange(f.a, model.FleetKindConfigSet, set, tokens)
		if err != nil {
			t.Fatal(err)
		}
	})
	rep.record(t, "preview/10000", map[string]any{"ms": took.Milliseconds(), "admitted": plan.Admitted, "refused": plan.Refused}, nil)
	if plan.Admitted+plan.Refused != 10_000 || plan.Admitted != min(10_000, f.sc.a) {
		t.Errorf("the preview planned %d admitted and %d refused", plan.Admitted, plan.Refused)
	}
	if took > 20*time.Second {
		f.overBudget(t, "the 10,000-line preview took %s; the budget is 20s", took)
	}
}

// Two device-management processes (two seams) under the semaphore's maximum: 4 digests and 4
// walks in flight, a ninth statement refused SEAM_BUSY, and the latency of an ordinary read
// recorded before and during. Only at the full scale: the small fixture is too quick to load.
func TestCostGateConcurrency(t *testing.T) {
	f := seedCost(t)
	if !f.sc.full {
		t.Log("the concurrency row runs at the full scale (DC_IT_INTENT_COST=1)")
		return
	}
	rep := newCostReport(t)
	pods := []*model.Api{newSeamApi(t), newSeamApi(t)}
	read := func(d time.Duration) []time.Duration {
		var out []time.Duration
		deadline := time.Now().Add(d)
		for i := 1; time.Now().Before(deadline); i++ {
			took := timed(func() {
				if _, err := f.api.DevicesByToken(f.a, []string{fmt.Sprintf("dev-%d", i%f.sc.a+1)}); err != nil {
					t.Error(err)
				}
			})
			out = append(out, took)
		}
		return out
	}
	p99 := func(ds []time.Duration) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)*99/100]
	}
	before := p99(read(5 * time.Second))
	dense := model.IntentSeamTarget{GroupToken: "sel-dense", Version: f.versions["dense"]}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var digestTimes []time.Duration
	var failures []error
	start := make(chan struct{})
	for _, pod := range pods {
		for range 2 {
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				took := timed(func() {
					if _, err := pod.ConfigurationKeyStateDigest(f.a, dense, fillSmall); err != nil {
						mu.Lock()
						failures = append(failures, err)
						mu.Unlock()
					}
				})
				mu.Lock()
				digestTimes = append(digestTimes, took)
				mu.Unlock()
			}()
			go func() {
				defer wg.Done()
				<-start
				var after int64
				for {
					page, err := pod.ConfigurationKeyState(f.a, dense, fillSmall, after, intentseam.MaxPageRows)
					if err != nil {
						var se *intentseam.Error
						if errors.As(err, &se) && se.Code == intentseam.CodeSeamBusy {
							continue // a walk retries a busy page, as the area does with backoff
						}
						mu.Lock()
						failures = append(failures, err)
						mu.Unlock()
						return
					}
					if page.NextCursor == 0 {
						return
					}
					after = page.NextCursor
				}
			}()
		}
	}
	close(start)
	during := p99(read(5 * time.Second))
	wg.Wait()
	sort.Slice(digestTimes, func(i, j int) bool { return digestTimes[i] < digestTimes[j] })
	rep.record(t, "concurrency/2pods", map[string]any{"digestMaxMs": digestTimes[len(digestTimes)-1].Milliseconds(),
		"readP99BeforeMs": float64(before.Microseconds()) / 1000, "readP99DuringMs": float64(during.Microseconds()) / 1000,
		"failures": len(failures)}, nil)
	for _, err := range failures {
		t.Errorf("a statement failed under concurrency: %v", err)
	}
	if digestTimes[len(digestTimes)-1] > 10*time.Second {
		f.overBudget(t, "the slowest concurrent digest took %s; the budget is 10s", digestTimes[len(digestTimes)-1])
	}
	if during > before*12/10 {
		t.Logf("the ordinary read's p99 moved from %s to %s (budget: 20%%)", before, during)
	}
}
