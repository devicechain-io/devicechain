// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---- fixture models -------------------------------------------------------------------

// scopedConfig is tenant configuration every member may read: Catalogue.
type scopedConfig struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

func (scopedConfig) ScopedBy() ScopeRule { return Catalogue() }

// scopedReading carries its anchors in a set table, like an event.
type scopedReading struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

// scopedReadingAnchor is the set table: one row per (reading, anchor). Live lets a test
// prove that AnchorSet.Where is applied.
type scopedReadingAnchor struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	ReadingID   uint
	AnchorType  string
	AnchorToken string
	Live        bool
}

func (scopedReading) ScopedBy() ScopeRule {
	return AnchorTable(AnchorSet{
		Model:      &scopedReadingAnchor{},
		Keys:       []KeyPair{{Set: "reading_id", Row: "id"}},
		KindColumn: "anchor_type", TokenColumn: "anchor_token",
		Where: scopeSetAlias + ".live = true",
	})
}

// scopedAlarm stamps its own anchors (AnchorColumn) and permits the two scoped writes.
// SQLite has no arrays, so statements against it run DryRun here; the predicate's rows
// are proved against PostgreSQL in scope_callback_integration_test.go.
type scopedAlarm struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name         string
	Acked        bool
	ScopeAnchors Anchors
}

func (scopedAlarm) ScopedBy() ScopeRule {
	return AnchorColumn("scope_anchors", AllowScopedCreate, AllowScopedUpdate)
}

// scopedNote stamps anchors but permits no scoped write.
type scopedNote struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	ScopeAnchors Anchors
}

func (scopedNote) ScopedBy() ScopeRule { return AnchorColumn("scope_anchors") }

// ---- helpers --------------------------------------------------------------------------

func newScopeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTestDB(t) // widget (unruled, tenant-scoped), gadget (not tenant-scoped)
	if err := db.AutoMigrate(&scopedConfig{}, &scopedReading{}, &scopedReadingAnchor{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// scopedCtx is a scoped member's request context, built the way the transports build
// it: the tenant from the token, and the verified claims.
func scopedCtx(tenant string, anchors ...string) context.Context {
	if anchors == nil {
		anchors = []string{}
	}
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: tenant,
		Authorities: []string{"*"}, ScopeAnchors: anchors}
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), claims)
}

func unscopedCtx(tenant string) context.Context {
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: tenant, Authorities: []string{"*"}}
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), claims)
}

func isScopeRefusal(err error) bool { return errors.Is(err, auth.ErrScopeUnsupported) }

// seedReadings writes readings for two tenants and two customers:
//
//	A: acme-pump (customer:acme-corp), globex-pump (customer:globex), stale-pump (customer:acme-corp, NOT live)
//	B: b-pump (customer:acme-corp — the same anchor, another tenant)
func seedReadings(t *testing.T, db *gorm.DB) {
	t.Helper()
	add := func(tenant, name, anchor string, live bool) {
		ctx := core.WithTenant(context.Background(), tenant)
		r := &scopedReading{Name: name}
		if err := db.WithContext(ctx).Create(r).Error; err != nil {
			t.Fatal(err)
		}
		kind, token, _ := strings.Cut(anchor, ":")
		if err := db.WithContext(ctx).Create(&scopedReadingAnchor{ReadingID: r.ID, AnchorType: kind,
			AnchorToken: token, Live: live}).Error; err != nil {
			t.Fatal(err)
		}
	}
	add("A", "acme-pump", "customer:acme-corp", true)
	add("A", "globex-pump", "customer:globex", true)
	add("A", "stale-pump", "customer:acme-corp", false)
	add("B", "b-pump", "customer:acme-corp", true)

	// A FORGED set row: tenant B's anchor table naming tenant A's globex-pump as owned by
	// customer:acme-corp. Row ids are global, so an honest set row never names another
	// tenant's row; this one stands in for a corrupt or hostile one, and only the set
	// table's own tenant clause keeps it from handing Acme's member Globex's pump.
	var globex scopedReading
	if err := db.WithContext(core.WithTenant(context.Background(), "A")).Where("name = ?", "globex-pump").First(&globex).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(core.WithTenant(context.Background(), "B")).Create(&scopedReadingAnchor{
		ReadingID: globex.ID, AnchorType: "customer", AnchorToken: "acme-corp", Live: true}).Error; err != nil {
		t.Fatal(err)
	}
}

func readingNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var rows []scopedReading
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		t.Fatalf("reading: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

// ---- unruled models: fail closed --------------------------------------------------------

// 🔴 FAIL CLOSED on every operation, not just reads: a model that declares no rule is
// refused to a scoped caller for query, count, row, update, delete and create, and for a
// subquery built through gorm.
func TestEveryOperationOnAnUnruledModelIsRefused(t *testing.T) {
	db := newScopeTestDB(t)
	if err := db.WithContext(unscopedCtx("A")).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := scopedCtx("A", "customer:acme-corp")
	var n int64
	ops := map[string]func() error{
		"find":  func() error { return db.WithContext(ctx).Find(&[]widget{}).Error },
		"first": func() error { return db.WithContext(ctx).First(&widget{}).Error },
		"count": func() error { return db.WithContext(ctx).Model(&widget{}).Count(&n).Error },
		"rows": func() error {
			rows, err := db.WithContext(ctx).Model(&widget{}).Select("name").Rows()
			if err == nil {
				_ = rows.Close()
			}
			return err
		},
		"update": func() error { return db.WithContext(ctx).Model(&widget{}).Where("1 = 1").Update("name", "x").Error },
		"delete": func() error { return db.WithContext(ctx).Where("1 = 1").Delete(&widget{}).Error },
		"create": func() error { return db.WithContext(ctx).Create(&widget{Name: "new"}).Error },
	}
	for name, op := range ops {
		if err := op(); !isScopeRefusal(err) {
			t.Errorf("%s on an unruled model by a scoped caller: %v", name, err)
		}
	}
	// Negative control: nothing was written or changed by the refused statements, and
	// the unscoped caller still reads the one row.
	var rows []widget
	if err := db.WithContext(unscopedCtx("A")).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].Name != "w" {
		t.Fatalf("the unscoped control after refusals: err=%v rows=%v", err, rows)
	}
}

// A SUBQUERY over an unruled model, built through gorm, is refused too — but gorm does
// not carry a subquery's error out to the statement that embeds it: it renders the
// subquery's SQL, and a refused subquery renders NONE. The outer statement then reads
// `IN ()`, which PostgreSQL rejects as a syntax error and SQLite answers with no rows.
// Either way the embedded read yields nothing, never the unfiltered rows. (The tenant
// callback's own refusals reach a subquery the same way.)
func TestASubqueryOverAnUnruledModelYieldsNothing(t *testing.T) {
	db := newScopeTestDB(t)
	a := unscopedCtx("A")
	w := &widget{Name: "w"}
	if err := db.WithContext(a).Create(w).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(a).Create(&scopedConfig{ID: w.ID, Name: "c"}).Error; err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context) ([]scopedConfig, error) {
		var out []scopedConfig
		sub := db.WithContext(ctx).Model(&widget{}).Select("id")
		err := db.WithContext(ctx).Where("id IN (?)", sub).Find(&out).Error
		return out, err
	}
	if got, err := read(scopedCtx("A", "customer:acme-corp")); err == nil && len(got) != 0 {
		t.Fatalf("a scoped subquery over an unruled model selected %v", got)
	}
	// Negative control: unscoped, the subquery selects the row.
	if got, err := read(a); err != nil || len(got) != 1 {
		t.Fatalf("the unscoped control: err=%v rows=%v", err, got)
	}
}

// What the callback does NOT touch, on purpose: a model with no tenant field (an
// instance-global table — the `me` query reads identities this way), and the system
// context, which bypasses the tenant predicate too. A service-token context is not
// scoped at all, so the enqueue validator's lookup reads as it always has.
func TestTheScopeLeavesUnscopedTablesSystemContextsAndServiceTokensAlone(t *testing.T) {
	db := newScopeTestDB(t)
	if err := db.WithContext(unscopedCtx("A")).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&gadget{Name: "g"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(scopedCtx("A", "customer:acme-corp")).Find(&[]gadget{}).Error; err != nil {
		t.Errorf("a scoped caller was refused a table that is not tenant-scoped: %v", err)
	}
	sys := core.WithSystemContext(scopedCtx("A", "customer:acme-corp"))
	if err := db.WithContext(sys).Find(&[]widget{}).Error; err != nil {
		t.Errorf("a system context was refused: %v", err)
	}
	svc := auth.WithClaims(core.WithTenant(context.Background(), "A"),
		&auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{"device:read"}})
	var rows []widget
	if err := db.WithContext(svc).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Errorf("a service-token context was scoped: err=%v rows=%v", err, rows)
	}
}

// ---- Catalogue ------------------------------------------------------------------------

// Catalogue reads are unfiltered within the tenant — and only within it. Writes refused.
func TestACatalogueModelIsReadableAndUnwritableByAScopedCaller(t *testing.T) {
	db := newScopeTestDB(t)
	for _, tc := range []struct{ tenant, name string }{{"A", "pump-type"}, {"B", "b-type"}} {
		if err := db.WithContext(unscopedCtx(tc.tenant)).Create(&scopedConfig{Name: tc.name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := scopedCtx("A", "customer:acme-corp")
	var rows []scopedConfig
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		t.Fatalf("a scoped catalogue read was refused: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "pump-type" {
		t.Fatalf("a scoped catalogue read crossed the tenant: %v", rows)
	}
	if err := db.WithContext(ctx).Model(&scopedConfig{}).Where("1 = 1").Update("name", "x").Error; !isScopeRefusal(err) {
		t.Errorf("a scoped caller updated catalogue configuration: %v", err)
	}
	if err := db.WithContext(ctx).Create(&scopedConfig{Name: "x"}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped caller created catalogue configuration: %v", err)
	}
	if err := db.WithContext(ctx).Where("1 = 1").Delete(&scopedConfig{}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped caller deleted catalogue configuration: %v", err)
	}
}

// ---- AnchorTable ----------------------------------------------------------------------

// 🔴 SCOPE LEAKAGE ACROSS CUSTOMERS AND ACROSS TENANTS, and the Where clause. Acme's
// member in tenant A sees acme-pump and nothing else: not Globex's pump (another
// customer), not stale-pump (its anchor row is not live), and not tenant B's pump
// even though B's anchor row names the very same customer token.
func TestAnAnchorTableReadSeesOnlyTheScopesRows(t *testing.T) {
	db := newScopeTestDB(t)
	seedReadings(t, db)

	if got := readingNames(t, db, scopedCtx("A", "customer:acme-corp")); !reflect.DeepEqual(got, []string{"acme-pump"}) {
		t.Fatalf("acme's scoped member in tenant A saw %v", got)
	}
	if got := readingNames(t, db, scopedCtx("A", "customer:globex")); !reflect.DeepEqual(got, []string{"globex-pump"}) {
		t.Fatalf("globex's scoped member saw %v", got)
	}
	// Several anchors are OR'd.
	if got := readingNames(t, db, scopedCtx("A", "customer:acme-corp", "customer:globex")); !reflect.DeepEqual(got, []string{"acme-pump", "globex-pump"}) {
		t.Fatalf("a two-anchor scope saw %v", got)
	}
	// An anchor of another KIND with the same token matches nothing.
	if got := readingNames(t, db, scopedCtx("A", "group:acme-corp")); len(got) != 0 {
		t.Fatalf("a group anchor matched a customer's rows: %v", got)
	}
	// 🔴 No anchors means nothing — never everything.
	if got := readingNames(t, db, scopedCtx("A")); len(got) != 0 {
		t.Fatalf("an anchorless scope saw %v", got)
	}
	// Negative control: unscoped, tenant A sees all three of its own.
	if got := readingNames(t, db, unscopedCtx("A")); !reflect.DeepEqual(got, []string{"acme-pump", "globex-pump", "stale-pump"}) {
		t.Fatalf("the unscoped control saw %v", got)
	}
	// Count goes through the same callback.
	var n int64
	if err := db.WithContext(scopedCtx("A", "customer:acme-corp")).Model(&scopedReading{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("a scoped count: n=%d err=%v", n, err)
	}
}

// 🔴 The scope predicate is ADDED to the tenant predicate, never substituted for it: the
// rendered statement carries both, and the anchor subquery carries a tenant clause of its
// own on the set table.
func TestTheScopePredicateSitsBesideTheTenantPredicate(t *testing.T) {
	db := newScopeTestDB(t)
	stmt := db.Session(&gorm.Session{DryRun: true}).WithContext(scopedCtx("A", "customer:acme-corp")).
		Find(&[]scopedReading{}).Statement
	if stmt.Error != nil {
		t.Fatal(stmt.Error)
	}
	sql := stmt.SQL.String()
	if !strings.Contains(sql, "`scoped_readings`.`tenant_id` = ?") {
		t.Errorf("the tenant predicate is missing: %s", sql)
	}
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM `scoped_reading_anchors` AS `dc_scope_set` WHERE `dc_scope_set`.`tenant_id` = ?") {
		t.Errorf("the anchor subquery has no tenant clause of its own: %s", sql)
	}
	tenants := 0
	for _, v := range stmt.Vars {
		if v == "A" {
			tenants++
		}
	}
	if tenants != 2 {
		t.Errorf("want the tenant bound twice (row and set table), got %d in %v", tenants, stmt.Vars)
	}
}

// Scoped writes to an AnchorTable model are refused: nothing a scoped member does writes
// a row whose visibility lives in another table.
func TestAnAnchorTableModelRefusesScopedWrites(t *testing.T) {
	db := newScopeTestDB(t)
	seedReadings(t, db)
	ctx := scopedCtx("A", "customer:acme-corp")
	if err := db.WithContext(ctx).Model(&scopedReading{}).Where("1 = 1").Update("name", "x").Error; !isScopeRefusal(err) {
		t.Errorf("scoped update: %v", err)
	}
	if err := db.WithContext(ctx).Create(&scopedReading{Name: "x"}).Error; !isScopeRefusal(err) {
		t.Errorf("scoped create: %v", err)
	}
	if err := db.WithContext(ctx).Where("1 = 1").Delete(&scopedReading{}).Error; !isScopeRefusal(err) {
		t.Errorf("scoped delete: %v", err)
	}
}

// ---- AnchorColumn -----------------------------------------------------------------------

func dry(db *gorm.DB, ctx context.Context) *gorm.DB {
	return db.Session(&gorm.Session{DryRun: true}).WithContext(ctx)
}

// The AnchorColumn predicate: the overlap of the row's anchors with the scope's, beside
// the tenant predicate, each anchor a bound parameter.
func TestAnAnchorColumnReadAddsTheOverlapPredicate(t *testing.T) {
	db := newTestDB(t)
	stmt := dry(db, scopedCtx("A", "customer:acme-corp", "group:north")).Find(&[]scopedAlarm{}).Statement
	if stmt.Error != nil {
		t.Fatal(stmt.Error)
	}
	sql := stmt.SQL.String()
	if !strings.Contains(sql, "`scoped_alarms`.`tenant_id` = ?") ||
		!strings.Contains(sql, "`scoped_alarms`.`scope_anchors` && ARRAY[?, ?]::text[]") {
		t.Fatalf("predicates: %s", sql)
	}
	if !reflect.DeepEqual(stmt.Vars, []any{"A", "customer:acme-corp", "group:north"}) {
		t.Fatalf("vars: %v", stmt.Vars)
	}
	// An anchorless scope reads nothing.
	stmt = dry(db, scopedCtx("A")).Find(&[]scopedAlarm{}).Statement
	if !strings.Contains(stmt.SQL.String(), "1 = 0") || strings.Contains(stmt.SQL.String(), "&&") {
		t.Fatalf("an anchorless scope: %s", stmt.SQL.String())
	}
	// Negative control: an unscoped read carries no scope predicate at all.
	stmt = dry(db, unscopedCtx("A")).Find(&[]scopedAlarm{}).Statement
	if strings.Contains(stmt.SQL.String(), "scope_anchors") || strings.Contains(stmt.SQL.String(), "1 = 0") {
		t.Fatalf("an unscoped read carries a scope predicate: %s", stmt.SQL.String())
	}
}

// 🔴 PRIVILEGE ESCALATION through a permitted write: a scoped update is limited to rows
// in scope and may not rewrite the anchors that decide who sees the row — by any of the
// routes gorm offers.
func TestAScopedUpdateMayNotRewriteTheAnchors(t *testing.T) {
	db := newTestDB(t)
	ctx := scopedCtx("A", "customer:acme-corp")

	// Permitted: acknowledging — the predicate is added, the anchors are untouched.
	stmt := dry(db, ctx).Model(&scopedAlarm{ID: 7}).Update("acked", true).Statement
	if stmt.Error != nil {
		t.Fatalf("a permitted scoped update was refused: %v", stmt.Error)
	}
	if !strings.Contains(stmt.SQL.String(), "`scoped_alarms`.`scope_anchors` && ARRAY[?]::text[]") {
		t.Fatalf("a scoped update carries no scope predicate: %s", stmt.SQL.String())
	}
	if err := dry(db, ctx).Model(&scopedAlarm{ID: 7}).Updates(map[string]any{"acked": true}).Error; err != nil {
		t.Fatalf("a permitted map update was refused: %v", err)
	}
	if err := dry(db, ctx).Model(&scopedAlarm{ID: 7}).Updates(&scopedAlarm{Acked: true}).Error; err != nil {
		t.Fatalf("a permitted struct update (anchors zero) was refused: %v", err)
	}

	steal := Anchors{"customer:acme-corp", "customer:globex"}
	routes := map[string]func() error{
		"Update(column)": func() error {
			return dry(db, ctx).Model(&scopedAlarm{ID: 7}).Update("scope_anchors", steal).Error
		},
		"Updates(map)": func() error {
			return dry(db, ctx).Model(&scopedAlarm{ID: 7}).Updates(map[string]any{"acked": true, "scope_anchors": steal}).Error
		},
		"Updates(struct)": func() error {
			return dry(db, ctx).Model(&scopedAlarm{ID: 7}).Updates(&scopedAlarm{Acked: true, ScopeAnchors: steal}).Error
		},
		"Select+Updates": func() error {
			return dry(db, ctx).Model(&scopedAlarm{ID: 7}).Select("acked", "scope_anchors").Updates(&scopedAlarm{Acked: true}).Error
		},
		"Save": func() error {
			return dry(db, ctx).Save(&scopedAlarm{ID: 7, Acked: true}).Error
		},
		"SET clause": func() error {
			return dry(db, ctx).Model(&scopedAlarm{ID: 7}).Clauses(clause.Set{{Column: clause.Column{Name: "scope_anchors"}, Value: steal}}).
				Updates(map[string]any{}).Error
		},
	}
	for name, route := range routes {
		if err := route(); !isScopeRefusal(err) {
			t.Errorf("%s: a scoped update rewrote the anchors: %v", name, err)
		}
	}
	// A rule that permits no update refuses even a harmless one.
	if err := dry(db, ctx).Model(&scopedNote{ID: 1}).Update("id", 1).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped update of a model permitting none: %v", err)
	}
	// Deletes are never permitted.
	if err := dry(db, ctx).Delete(&scopedAlarm{ID: 7}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped delete: %v", err)
	}
}

// 🔴 CONFUSED DEPUTY / ESCALATION through a permitted create: a scoped caller may create
// only rows it will itself be able to see, and never through an upsert, whose update arm
// would reach a row the predicate never saw.
func TestAScopedCreateMustLandInsideTheScope(t *testing.T) {
	db := newTestDB(t)
	ctx := scopedCtx("A", "customer:acme-corp")

	if err := dry(db, ctx).Create(&scopedAlarm{Name: "ok", ScopeAnchors: Anchors{"customer:globex", "customer:acme-corp"}}).Error; err != nil {
		t.Fatalf("a create inside the scope was refused: %v", err)
	}
	if err := dry(db, ctx).Create(&[]scopedAlarm{
		{Name: "a", ScopeAnchors: Anchors{"customer:acme-corp"}},
		{Name: "b", ScopeAnchors: Anchors{"customer:acme-corp"}},
	}).Error; err != nil {
		t.Fatalf("a batch inside the scope was refused: %v", err)
	}

	refused := map[string]func() error{
		"foreign anchors": func() error {
			return dry(db, ctx).Create(&scopedAlarm{Name: "x", ScopeAnchors: Anchors{"customer:globex"}}).Error
		},
		"no anchors": func() error { return dry(db, ctx).Create(&scopedAlarm{Name: "x"}).Error },
		"one bad row in a batch": func() error {
			return dry(db, ctx).Create(&[]scopedAlarm{
				{Name: "a", ScopeAnchors: Anchors{"customer:acme-corp"}},
				{Name: "b", ScopeAnchors: Anchors{"customer:globex"}},
			}).Error
		},
		"map": func() error {
			return dry(db, ctx).Model(&scopedAlarm{}).Create(map[string]any{"name": "x", "scope_anchors": Anchors{"customer:acme-corp"}}).Error
		},
		"upsert": func() error {
			return dry(db, ctx).Clauses(clause.OnConflict{UpdateAll: true}).
				Create(&scopedAlarm{Name: "x", ScopeAnchors: Anchors{"customer:acme-corp"}}).Error
		},
		"model permitting no create": func() error {
			return dry(db, ctx).Create(&scopedNote{ScopeAnchors: Anchors{"customer:acme-corp"}}).Error
		},
	}
	for name, create := range refused {
		if err := create(); !isScopeRefusal(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A rule that names a column the model does not have refuses, rather than reading
// without a predicate.
type misruledModel struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
}

func (misruledModel) ScopedBy() ScopeRule { return AnchorColumn("no_such_column") }

func TestAMisdeclaredRuleRefuses(t *testing.T) {
	db := newTestDB(t)
	if err := dry(db, scopedCtx("A", "customer:acme-corp")).Find(&[]misruledModel{}).Error; !isScopeRefusal(err) {
		t.Fatalf("a rule naming a missing column: %v", err)
	}
}

// The scope callbacks are part of tenant scoping's registration — there is no handle
// with the tenant predicate and without the scope one.
func TestRegisteringTenantScopingRegistersTheScopeCallbacks(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dc:scope_query", "dc:scope_row", "dc:scope_update", "dc:scope_delete", "dc:scope_create"} {
		var cb func(*gorm.DB)
		switch {
		case strings.HasSuffix(name, "query"):
			cb = db.Callback().Query().Get(name)
		case strings.HasSuffix(name, "row"):
			cb = db.Callback().Row().Get(name)
		case strings.HasSuffix(name, "update"):
			cb = db.Callback().Update().Get(name)
		case strings.HasSuffix(name, "delete"):
			cb = db.Callback().Delete().Get(name)
		default:
			cb = db.Callback().Create().Get(name)
		}
		if cb == nil {
			t.Errorf("%s is not registered by RegisterTenantScoping", name)
		}
	}
}

// ---- KeyFilterOnly --------------------------------------------------------------------

// scopedKeys is a model that is only ever the inside of a key subquery.
type scopedKeys struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

func (scopedKeys) ScopedBy() ScopeRule { return KeyFilterOnly() }

// A key-filter-only model is usable inside a subquery under a scoped caller (within the
// tenant), and refused when read directly or written: it holds device-derived rows that no
// scoped caller may list.
func TestAKeyFilterOnlyModelServesASubqueryButIsNeverReadDirectly(t *testing.T) {
	db := newScopeTestDB(t)
	if err := db.AutoMigrate(&scopedKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tenant, name string }{{"A", "k-a"}, {"B", "k-b"}} {
		if err := db.WithContext(unscopedCtx(tc.tenant)).Create(&scopedKeys{Name: tc.name}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(unscopedCtx(tc.tenant)).Create(&scopedConfig{Name: tc.name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := scopedCtx("A", "customer:acme-corp")

	var direct []scopedKeys
	if err := db.WithContext(ctx).Find(&direct).Error; !isScopeRefusal(err) || len(direct) != 0 {
		t.Errorf("a scoped direct read of a key-filter-only model was not refused: %v %v", err, direct)
	}
	var n int64
	if err := db.WithContext(ctx).Model(&scopedKeys{}).Count(&n).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped count of a key-filter-only model was not refused: %v", err)
	}
	if err := db.WithContext(ctx).Create(&scopedKeys{Name: "x"}).Error; !isScopeRefusal(err) {
		t.Errorf("a scoped write of a key-filter-only model was not refused: %v", err)
	}

	var rows []scopedConfig
	sub := db.WithContext(ctx).Model(&scopedKeys{}).Select("name")
	if err := db.WithContext(ctx).Where("name IN (?)", sub).Find(&rows).Error; err != nil {
		t.Fatalf("the subquery use was refused: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "k-a" {
		t.Errorf("the subquery crossed the tenant or lost the row: %v", rows)
	}
	// control: an unscoped caller reads it directly
	if err := db.WithContext(unscopedCtx("A")).Find(&direct).Error; err != nil || len(direct) != 1 {
		t.Errorf("control: an unscoped direct read failed: %v %v", err, direct)
	}
}
