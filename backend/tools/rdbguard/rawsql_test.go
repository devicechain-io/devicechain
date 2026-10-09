// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixture writes files (name -> source) under a fresh directory and returns the
// slash-separated root, which is also the prefix of every walked path.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.ToSlash(root)
}

// models gives every fixture a tenant-scoped table (widgets, through the embed), a
// tenant-scoped table spelled the plain way (rule_statistics, through a TableName override)
// and an unscoped one (gadgets).
const models = `package model

type TenantScoped struct {
	TenantId string ` + "`gorm:\"index\"`" + `
}

type Widget struct {
	TenantScoped
	Name string
}

type RuleStat struct {
	Tenant string ` + "`gorm:\"primaryKey\"`" + `
}

func (RuleStat) TableName() string { return "rule_statistics" }

type Gadget struct {
	ID   uint
	Name string
}
`

func scanSQL(t *testing.T, src string, allow []siteEntry) Result {
	t.Helper()
	root := fixture(t, map[string]string{"models.go": models, "x.go": src})
	res, err := rawSQLScan(allow, root)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func scanJoin(t *testing.T, src string, allow []siteEntry) Result {
	t.Helper()
	root := fixture(t, map[string]string{"models.go": models, "x.go": src})
	res, err := rawJoinScan(allow, root)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRawSQLFlagsShapes(t *testing.T) {
	flagged := map[string]string{
		"raw select on a tenant table": `package model
func f(db DB) { db.Raw("SELECT * FROM widgets WHERE name = ?", 1).Scan(nil) }`,
		"exec update on a tenant table": `package model
func f(tx DB) error { return tx.Exec("UPDATE widgets SET name = ?", "x").Error }`,
		"exec delete, quoted and schema-qualified": `package model
func f(tx DB) error { return tx.Exec("DELETE FROM \"area\".\"widgets\"").Error }`,
		"plain-spelled tenant table via TableName": `package model
func f(tx DB) error { return tx.Exec("DELETE FROM rule_statistics").Error }`,
		"dynamic sql from a variable": `package model
func f(tx DB, q string) error { return tx.Exec(q).Error }`,
		"dynamic sql from sprintf of a variable": `package model
func f(tx DB, tbl string) error { return tx.Exec(fmt.Sprintf("DELETE FROM %s", tbl)).Error }`,
		"dynamic sql from a call": `package model
func f(tx DB) error { return tx.Exec(build()).Error }`,
		"concatenation reaching a tenant table": `package model
func f(tx DB) error { return tx.Exec("DELETE " + "FROM widgets").Error }`,
		"named constant holding the query": `package model
const q = "SELECT * FROM widgets"
func f(tx DB) { tx.Raw(q) }`,
		"chained receiver, split across lines": `package model
func f(db DB) {
	db.
		WithContext(c).
		Raw("SELECT 1 FROM widgets").
		Scan(nil)
}`,
	}
	for name, src := range flagged {
		t.Run(name, func(t *testing.T) {
			res := scanSQL(t, src, nil)
			if len(res.Findings) != 1 {
				t.Fatalf("want exactly 1 finding, got %d: %v", len(res.Findings), res.Findings)
			}
			if res.Findings[0].Pos.Line == 0 {
				t.Fatalf("finding does not name a line: %v", res.Findings[0])
			}
		})
	}
}

func TestRawSQLIgnoresWhatIsNotTheHazard(t *testing.T) {
	clean := map[string]string{
		"literal over an unscoped table": `package model
func f(db DB) { db.Raw("SELECT * FROM gadgets").Scan(nil) }`,
		"a word that merely contains a tenant table name": `package model
func f(db DB) { db.Raw("SELECT * FROM widgets_archive_x").Scan(nil) }`,
		"advisory lock": `package model
func f(tx DB) error { return tx.Exec("SELECT pg_advisory_xact_lock(?)", 1).Error }`,
		"pgx style, context first": `package model
func f(ctx C, q Q) { q.Exec(ctx, "DELETE FROM widgets") }`,
		"pgx style, request context": `package model
func f(r R, q Q) { q.Exec(r.Context(), "DELETE FROM widgets") }`,
		"a bound value is not SQL": `package model
func f(tx DB, v string) error { return tx.Exec("SELECT 1 FROM gadgets WHERE a = ?", v).Error }`,
	}
	for name, src := range clean {
		t.Run(name, func(t *testing.T) {
			if res := scanSQL(t, src, nil); len(res.Findings) != 0 {
				t.Fatalf("flagged something legitimate: %v", res.Findings)
			}
		})
	}

	// Migrations and tests are out of scope by file name.
	root := fixture(t, map[string]string{
		"models.go":             models,
		"migration_x.go":        "package model\nfunc f(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
		"baseline.go":           "package model\nfunc g(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
		"baseline_snapshot.go":  "package model\nfunc h(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
		"x_test.go":             "package model\nfunc k(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
		"testdata/ignored.go":   "package model\nfunc k(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
		"_legacy/ignored_go.go": "package model\nfunc k(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }",
	})
	res, err := rawSQLScan(nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("migration/test/testdata files must be skipped, got %v", res.Findings)
	}
}

// TestRawSQLFollowsOnlyConstants: a package var can be reassigned (init, a test hook) and a
// local can shadow a const, so neither resolves to the initialiser's text.
func TestRawSQLFollowsOnlyConstants(t *testing.T) {
	flagged := map[string]string{
		"var reassigned in init": `package model
var q = "SELECT 1 FROM gadgets"
func init() { q = "DELETE FROM widgets" }
func f(tx DB) { tx.Raw(q) }`,
		"param shadowing a const": `package model
const q = "SELECT 1 FROM gadgets"
func f(tx DB, q string) { tx.Exec(q) }`,
		"local shadowing a const": `package model
const q = "SELECT 1 FROM gadgets"
func f(tx DB) {
	q := build()
	tx.Exec(q)
}`,
	}
	for name, src := range flagged {
		t.Run(name, func(t *testing.T) {
			res := scanSQL(t, src, nil)
			if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "dynamically") {
				t.Fatalf("want one dynamic-SQL finding, got %v", res.Findings)
			}
		})
	}
	// The counterweight: a const IS followed.
	if res := scanSQL(t, `package model
const q = "SELECT 1 FROM gadgets"
func f(tx DB) { tx.Raw(q) }`, nil); len(res.Findings) != 0 {
		t.Fatalf("a const naming an unscoped table must pass: %v", res.Findings)
	}
}

// TestRawSQLClosureCallsAreSites: an allow-listed function routing statements through a
// local closure must not hide new statements behind one counted site.
func TestRawSQLClosureCallsAreSites(t *testing.T) {
	src := `package model
func f(db DB) {
	exec := func(step, stmt string) { db.Exec(stmt) }
	exec("a", "SELECT 1 FROM gadgets")
	exec("b", "DELETE FROM widgets")
	exec("c", "UPDATE widgets SET a = 1")
}`
	res := scanSQL(t, src, nil)
	if len(res.Findings) != 2 {
		t.Fatalf("want the two tenant-table closure calls flagged (not the inner Exec, not the clean call), got %v", res.Findings)
	}
	root := fixture(t, map[string]string{"models.go": models, "x.go": src})
	// An entry written for ONE statement does not cover the second.
	res, _ = rawSQLScan([]siteEntry{{Path: root + "/x.go", Func: "f", Count: 1, Why: "t"}}, root)
	if len(res.Stale) != 1 {
		t.Fatalf("a statement added through the closure must break the count, got stale=%v", res.Stale)
	}
	res, _ = rawSQLScan([]siteEntry{{Path: root + "/x.go", Func: "f", Count: 2, Why: "t"}}, root)
	if len(res.Findings) != 0 || len(res.Stale) != 0 {
		t.Fatalf("exact count must pass: %v %v", res.Findings, res.Stale)
	}
}

// TestRawSQLCaseAndFileNames kills three mutants: names are matched case-insensitively, and
// only the migration_/baseline prefixes exempt a file — not any file whose name starts "m".
func TestRawSQLCaseAndFileNames(t *testing.T) {
	if res := scanSQL(t, `package model
func f(tx DB) { tx.Exec("DELETE FROM WIDGETS") }`, nil); len(res.Findings) != 1 {
		t.Fatalf("an upper-case table name must be flagged: %v", res.Findings)
	}
	body := "package model\nfunc f(tx DB) { tx.Exec(\"UPDATE widgets SET a = 1\") }"
	for _, name := range []string{"main.go", "mig.go", "migration.go", "base.go", "baselin.go"} {
		root := fixture(t, map[string]string{"models.go": models, name: body})
		res, err := rawSQLScan(nil, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Findings) != 1 {
			t.Errorf("%s must still be scanned, got %v", name, res.Findings)
		}
	}
}

func TestRawSQLAllowList(t *testing.T) {
	src := `package model
func f(tx DB) error { return tx.Exec("UPDATE widgets SET a = 1").Error }
func g(tx DB) error { return tx.Exec("UPDATE widgets SET a = 2").Error }`
	root := fixture(t, map[string]string{"models.go": models, "x.go": src})
	path := root + "/x.go"

	// An allow-listed site is not flagged, and is counted; the other site still is.
	res, err := rawSQLScan([]siteEntry{{Path: path, Func: "f", Count: 1, Why: "test"}}, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "in g") {
		t.Fatalf("want only g flagged, got %v", res.Findings)
	}
	if len(res.Stale) != 0 {
		t.Fatalf("live entry reported stale: %v", res.Stale)
	}

	// Allow-listing both leaves nothing.
	res, _ = rawSQLScan([]siteEntry{
		{Path: path, Func: "f", Count: 1, Why: "test"},
		{Path: path, Func: "g", Count: 1, Why: "test"},
	}, root)
	if len(res.Findings) != 0 || len(res.Stale) != 0 {
		t.Fatalf("want clean, got findings=%v stale=%v", res.Findings, res.Stale)
	}

	// STALE: an entry for a function that does not exist must be reported.
	res, _ = rawSQLScan([]siteEntry{{Path: path, Func: "gone", Count: 1, Why: "test"}}, root)
	if len(res.Stale) != 1 {
		t.Fatalf("an entry matching nothing must be stale, got %v", res.Stale)
	}

	// STALE: an entry for a function that exists but no longer has a flagged site.
	clean := fixture(t, map[string]string{"models.go": models,
		"x.go": "package model\nfunc f(tx DB) error { return tx.Exec(\"SELECT 1 FROM gadgets\").Error }"})
	res, _ = rawSQLScan([]siteEntry{{Path: clean + "/x.go", Func: "f", Count: 1, Why: "test"}}, clean)
	if len(res.Findings) != 0 || len(res.Stale) != 1 {
		t.Fatalf("an entry whose site stopped matching must be stale, got findings=%v stale=%v", res.Findings, res.Stale)
	}

	// STALE: a second site inside an allow-listed function is not silently inherited.
	two := fixture(t, map[string]string{"models.go": models, "x.go": `package model
func f(tx DB) error {
	tx.Exec("UPDATE widgets SET a = 1")
	return tx.Exec("DELETE FROM widgets").Error
}`})
	res, _ = rawSQLScan([]siteEntry{{Path: two + "/x.go", Func: "f", Count: 1, Why: "test"}}, two)
	if len(res.Stale) != 1 {
		t.Fatalf("a grown function must make its entry stale, got %v", res.Stale)
	}

	// Paths match exactly, not as a suffix.
	res, _ = rawSQLScan([]siteEntry{{Path: "x.go", Func: "f", Count: 1, Why: "test"}}, root)
	if len(res.Stale) != 1 || len(res.Findings) != 2 {
		t.Fatalf("a suffix path must not exempt: findings=%v stale=%v", res.Findings, res.Stale)
	}
}

func TestRawJoinShapes(t *testing.T) {
	flagged := map[string]string{
		"join without tenant equality": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = gadgets.widget_id") }`,
		"left join, table name used directly": `package model
func f(db DB) { db.Joins("LEFT JOIN widgets ON widgets.id = gadgets.widget_id AND widgets.deleted_at IS NULL") }`,
		"equality on the wrong table": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND g.tenant_id = ?") }`,
		"tenant column compared with a literal": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND w.tenant_id = 'x'") }`,
		"plain-spelled tenant table": `package model
func f(db DB) { db.Joins("JOIN rule_statistics r ON r.rule_id = g.rule_id") }`,
		"association join": `package model
func f(db DB) { db.Joins("Widget") }`,
		"dynamic join": `package model
func f(db DB, j string) { db.Joins(j) }`,
		"subquery join": `package model
func f(db DB) { db.Joins("JOIN (SELECT * FROM widgets) w ON w.id = g.widget_id") }`,
	}
	for name, src := range flagged {
		t.Run(name, func(t *testing.T) {
			res := scanJoin(t, src, nil)
			if len(res.Findings) != 1 {
				t.Fatalf("want exactly 1 finding, got %d: %v", len(res.Findings), res.Findings)
			}
		})
	}

	clean := map[string]string{
		"equality, joined alias first": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND w.tenant_id = g.tenant_id") }`,
		"equality, joined alias last": `package model
func f(db DB) { db.Joins("JOIN widgets w ON g.tenant_id = w.tenant_id AND w.id = g.widget_id") }`,
		"equality against the table name": `package model
func f(db DB) { db.Joins("JOIN widgets ON widgets.id = g.widget_id AND widgets.tenant_id = g.tenant_id") }`,
		"equality against a bind": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND w.tenant_id = ?", 1) }`,
		"quoted columns": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND \"w\".\"tenant_id\" = \"g\".\"tenant_id\"") }`,
		"plain spelling": `package model
func f(db DB) { db.Joins("JOIN rule_statistics r ON r.rule_id = g.rule_id AND r.tenant = g.tenant") }`,
		"unscoped joined table": `package model
func f(db DB) { db.Joins("JOIN gadgets g2 ON g2.id = widgets.gadget_id") }`,
		"two joins, both equated": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.a AND w.tenant_id = g.tenant_id JOIN rule_statistics r ON r.x = g.b AND r.tenant = g.tenant_id") }`,
	}
	for name, src := range clean {
		t.Run("clean/"+name, func(t *testing.T) {
			if res := scanJoin(t, src, nil); len(res.Findings) != 0 {
				t.Fatalf("flagged a join that carries the equality: %v", res.Findings)
			}
		})
	}

	// One of two joins in a single string lacking the equality is still a finding.
	res := scanJoin(t, `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.a AND w.tenant_id = g.tenant_id JOIN rule_statistics r ON r.x = g.b") }`, nil)
	if len(res.Findings) != 1 {
		t.Fatalf("want the second join flagged, got %v", res.Findings)
	}
}

func TestRawJoinAllowList(t *testing.T) {
	src := `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id") }`
	root := fixture(t, map[string]string{"models.go": models, "x.go": src})
	path := root + "/x.go"

	res, _ := rawJoinScan([]siteEntry{{Path: path, Func: "f", Count: 1, Why: "test"}}, root)
	if len(res.Findings) != 0 || len(res.Stale) != 0 {
		t.Fatalf("allow-listed join: findings=%v stale=%v", res.Findings, res.Stale)
	}
	res, _ = rawJoinScan([]siteEntry{{Path: path, Func: "gone", Count: 1, Why: "test"}}, root)
	if len(res.Findings) != 1 || len(res.Stale) != 1 {
		t.Fatalf("stale entry: findings=%v stale=%v", res.Findings, res.Stale)
	}

	// An entry whose join gained the equality (the site stopped matching) is stale.
	fixed := fixture(t, map[string]string{"models.go": models, "x.go": `package model
func f(db DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND w.tenant_id = g.tenant_id") }`})
	res, _ = rawJoinScan([]siteEntry{{Path: fixed + "/x.go", Func: "f", Count: 1, Why: "test"}}, fixed)
	if len(res.Findings) != 0 || len(res.Stale) != 1 {
		t.Fatalf("an entry whose join is now equated must be stale: findings=%v stale=%v", res.Findings, res.Stale)
	}
}

func TestTenantTablesDerivation(t *testing.T) {
	root := fixture(t, map[string]string{"models.go": models, "other.go": `package model

// not a gorm model: a JSON claim that happens to be called Tenant.
type Claims struct {
	Tenant string ` + "`json:\"tenant\"`" + `
}

// embeds a tenant-bearing mixin through another struct.
type Base struct{ TenantScoped }
type Derived struct {
	Base
	Other string
}
`})
	tables, err := TenantTables(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"widgets", "rule_statistics", "deriveds", "bases"} {
		if _, ok := tables[want]; !ok {
			t.Errorf("table %q missing from %v", want, tables)
		}
	}
	// M13: a TableName() literal that differs from gorm's default must be honoured, and the
	// default spelling of that type must NOT be what was recorded.
	for _, not := range []string{"gadgets", "claims", "tenant_scopeds", "rule_stats"} {
		if _, ok := tables[not]; ok {
			t.Errorf("table %q must not be classified tenant-scoped", not)
		}
	}
}

// TestTenantFieldNamesMatchCore pins the restated spellings to core/rdb's.
func TestTenantFieldNamesMatchCore(t *testing.T) {
	src, err := os.ReadFile("../../core/rdb/tenant_scope.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	var core []string
	for _, re := range []string{`(?m)^const tenantFieldName = "(\w+)"`, `(?m)^const plainTenantFieldName = "(\w+)"`} {
		m := regexp.MustCompile(re).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("core/rdb/tenant_scope.go no longer matches %s — re-derive tenantFieldNames", re)
		}
		core = append(core, m[1])
	}
	if !regexp.MustCompile(`var tenantFieldNames = \[\]string\{tenantFieldName, plainTenantFieldName\}`).MatchString(text) {
		t.Fatal("core/rdb's tenantFieldNames list changed shape — a third spelling may exist")
	}
	if len(core) != len(tenantFieldNames) {
		t.Fatalf("core spells %v, rdbguard %v", core, tenantFieldNames)
	}
	for _, n := range core {
		if !tenantFieldNames[n] {
			t.Errorf("core/rdb spells the tenant field %q but rdbguard does not know it", n)
		}
	}
	// The column names are what gorm's naming makes of the field names.
	want := map[string]bool{"tenant_id": true, "tenant": true}
	for _, c := range tenantColumnNames {
		if !want[c] {
			t.Errorf("unexpected tenant column %q", c)
		}
	}
}

// goldenTables parses the frozen per-area schemas under migrationdiff/golden and returns
// every table with a tenant column (direct), and every table with a foreign key into one of
// those (transitive — the class the tenant purge sweeps through its parent).
func goldenTables(t *testing.T) (direct, transitive map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("../migrationdiff/golden/*.sql")
	if err != nil || len(files) < 10 {
		t.Fatalf("expected the golden schemas (>=10), got %d files, err=%v", len(files), err)
	}
	create := regexp.MustCompile(`^CREATE TABLE (?:"[^"]+"|\w+)\.("?[\w-]+"?) \($`)
	col := regexp.MustCompile(`^\s+"?(tenant_id|tenant)"?\s`)
	alter := regexp.MustCompile(`^ALTER TABLE (?:ONLY )?(?:"[^"]+"|\w+)\.("?[\w-]+"?)$`)
	fk := regexp.MustCompile(`FOREIGN KEY .* REFERENCES (?:"[^"]+"|\w+)\.("?[\w-]+"?)\(`)
	direct, transitive = map[string]bool{}, map[string]bool{}
	type edge struct{ child, parent string }
	var edges []edge
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cur, alt := "", ""
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimRight(line, "\r")
			if m := create.FindStringSubmatch(line); m != nil {
				cur = strings.Trim(m[1], `"`)
				continue
			}
			if cur != "" {
				if line == ");" {
					cur = ""
				} else if col.MatchString(line) {
					direct[cur] = true
				}
				continue
			}
			if m := alter.FindStringSubmatch(line); m != nil {
				alt = strings.Trim(m[1], `"`)
				continue
			}
			if m := fk.FindStringSubmatch(line); m != nil && alt != "" {
				edges = append(edges, edge{alt, strings.Trim(m[1], `"`)})
			}
		}
	}
	for _, e := range edges {
		if direct[e.parent] && !direct[e.child] {
			transitive[e.child] = true
		}
	}
	return direct, transitive
}

// TestDerivedSetCoversGoldenSchemas is the cross-check against the catalog the tenant purge
// classifies from: every golden table with a tenant column, and every table with a foreign
// key into one, must be in the derived set. Extra derived names are allowed — over-flagging
// is the safe direction.
func TestDerivedSetCoversGoldenSchemas(t *testing.T) {
	tables, err := TenantTables("../../core", "../../services")
	if err != nil {
		t.Fatal(err)
	}
	direct, transitive := goldenTables(t)
	if len(direct) < 40 {
		t.Fatalf("only %d direct tenant tables parsed from the golden schemas; the parser is blind", len(direct))
	}
	if !transitive["iam_membership_tenant_roles"] {
		t.Fatalf("the golden parser did not find the known transitive join table; transitive=%v", transitive)
	}
	for name := range direct {
		if _, ok := tables[name]; !ok {
			t.Errorf("golden table %q has a tenant column but is not in the derived set", name)
		}
	}
	for name := range transitive {
		if _, ok := tables[name]; !ok {
			t.Errorf("golden table %q has a foreign key into a tenant table but is not in the derived set", name)
		}
	}
	// Tables known NOT to carry a tenant column must not be classified tenant-scoped.
	for _, not := range []string{"iam_tenants", "iam_roles", "system_settings", "signing_keys"} {
		if _, ok := tables[not]; ok {
			t.Errorf("%q has no tenant column but was classified tenant-scoped", not)
		}
	}
}

// TestAllowListsAreWellFormed keeps the lists readable: every entry has a reason and a
// positive count, and no (file, function) pair appears twice.
func TestAllowListsAreWellFormed(t *testing.T) {
	for name, list := range map[string][]siteEntry{"raw-sql": rawSQLAllowList, "raw-join": rawJoinAllowList} {
		seen := map[string]bool{}
		for _, e := range list {
			if strings.TrimSpace(e.Why) == "" || e.Count < 1 || e.Path == "" || e.Func == "" {
				t.Errorf("%s: malformed entry %+v", name, e)
			}
			if seen[e.key()] {
				t.Errorf("%s: duplicate entry %s", name, e.key())
			}
			seen[e.key()] = true
		}
	}
}
