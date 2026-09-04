// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tenantpurge erases one tenant's rows from an instance database (ADR-077).
//
// It works from the POSTGRES CATALOG rather than from a list of areas or a list of
// models, and that choice is the whole point. An instance database holds one schema
// per functional area, every area's migrations are applied by the same owning role,
// and the tables an area created are present whether or not that area is deployed
// right now. So enumerating the catalog answers "what holds this tenant's data"
// completely, while any hand-maintained list answers "what someone remembered to
// add" — and the gap between those two is silent, permanent data retention.
//
// The three ways a hand-maintained list goes wrong here, all of which the catalog
// closes by construction:
//
//   - An area that is not deployed still holds rows. mcp and ai-inference are opt-in;
//     their schemas and data survive being switched off.
//   - A table added by a migration after the purge code was written is invisible to
//     the purge code but perfectly visible to the database.
//   - An area with no purge participant of its own contributes nothing to a
//     per-area ack protocol, and its silence is indistinguishable from success.
//
// What the catalog CANNOT tell us is whether a table with no tenant column is
// genuinely tenant-free or is tenant data the schema fails to mark. That residual
// judgement is the exemption registry in exempt.go, and it is the reason
// ClassUnclassified exists: a table this package cannot explain fails the purge
// instead of being skipped.
package tenantpurge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Class is what the purge knows about one table.
type Class int

const (
	// ClassUnclassified is the fail-closed default: a table with no tenant column,
	// no foreign key into tenant-bearing data, and no exemption. The purge refuses
	// to run rather than guess, because the two possible guesses are "delete rows
	// we cannot identify" and "leave a tenant's data behind and report success".
	ClassUnclassified Class = iota

	// ClassDirect names a table carrying the tenant token in a column of its own.
	// Swept with a predicate on that column.
	ClassDirect

	// ClassTransitive names a table with no tenant column that reaches tenant-bearing
	// rows through a foreign key — a join table, in practice. Its rows are the purged
	// tenant's data even though nothing in the row says so. Swept with a subquery
	// through the foreign key.
	ClassTransitive

	// ClassExempt names a table that holds no data belonging to any one tenant, or
	// whose retention is a stated decision rather than an oversight. Every exemption
	// carries a reason; see exempt.go.
	ClassExempt

	// ClassDeferred names a table that DOES hold tenant data this mechanism cannot
	// erase, recorded as a known hole rather than hidden inside ClassExempt.
	//
	// The distinction from ClassExempt is the honest part of this package. "Holds
	// nothing of the tenant's" and "holds the tenant's data and we are not erasing
	// it yet" are different facts, and collapsing them into one bucket is how a hole
	// becomes invisible: a reviewer scanning a list of exemptions reads them as
	// reassurance. A deferred table is reported in every sweep result, so a purge
	// that completes with one outstanding cannot claim total erasure.
	ClassDeferred

	// ClassExternal names a table that holds tenant data no SQL predicate can reach,
	// and that a NAMED purge store erases by another route.
	//
	// It is the third of the three honest answers, and it exists because the other
	// two both misreport this case. ClassExempt would be a lie (the table holds the
	// tenant's data). ClassDeferred was the truth right up until something erased it,
	// and leaving it there once a store does would block every purge forever over
	// data that is in fact gone.
	//
	// What separates it from an exemption is that the claim has something behind it: the
	// entry names a store, that store is registered with the coordinator, and it reports
	// its own outcome on every pass. So an external table is not skipped — it is erased
	// somewhere else and accounted for there. See ExternalStores, which is what lets the
	// coordinator's side of the tree assert every named store actually exists.
	ClassExternal

	// ClassRedacted names a table whose ROWS are kept and whose IDENTIFIERS are
	// destroyed — the only class in which a purge writes to a table rather than
	// emptying it.
	//
	// It exists for one table today, and for a conflict the other four classes each
	// resolve by giving up half of it. The audit journal is the evidence that a tenant
	// was deleted; sweeping it destroys the record of the erasure, and keeping it whole
	// retains the tenant's people by name past the erasure that record certifies.
	// ClassDirect takes the first, ClassExempt and ClassExternal both claim a falsehood
	// (the table holds this tenant's personal data and nothing else erases it), and
	// ClassDeferred would block every purge on the instance over data that IS being
	// dealt with.
	//
	// 🔴 UNLIKE EVERY OTHER CLASS, THIS ONE IS ASSIGNED BEFORE THE CATALOG SPEAKS, and
	// that inversion is deliberate rather than convenient. Exemptions are applied last
	// precisely so a stale entry can never stop a tenant-bearing table being swept —
	// but audit_events HAS a tenant column, so the catalog would call it direct and an
	// exemption for it would be inert. A retention decision about a tenant-bearing
	// table is exactly the case the last-wins rule cannot express, so it is stated in
	// the one place it can be: ahead of the column check, naming the columns it
	// destroys.
	ClassRedacted
)

func (c Class) String() string {
	switch c {
	case ClassDirect:
		return "direct"
	case ClassTransitive:
		return "transitive"
	case ClassExempt:
		return "exempt"
	case ClassDeferred:
		return "deferred"
	case ClassExternal:
		return "external"
	case ClassRedacted:
		return "redacted"
	default:
		return "unclassified"
	}
}

// Table identifies one table by schema and name. Schema names are the functional-area
// names verbatim, so they contain hyphens ("device-management") and only survive SQL
// as quoted identifiers — see rdb.QuoteIdentifier.
type Table struct {
	Schema string
	Name   string
}

func (t Table) String() string { return t.Schema + "." + t.Name }

// quoted renders the table as a schema-qualified quoted identifier.
func (t Table) quoted() string {
	return rdb.QuoteIdentifier(t.Schema) + "." + rdb.QuoteIdentifier(t.Name)
}

// Entry is one table's classification, with everything the sweep needs to act on it
// and everything a reader needs to judge it.
type Entry struct {
	Table Table
	Class Class

	// Column is the tenant column, for ClassDirect and ClassRedacted. Both select
	// their rows the same way and differ only in what the statement built around the
	// predicate does with them — which is also why a redacted table without one is
	// refused at classification rather than at sweep time.
	Column string

	// Links are the foreign keys through which a ClassTransitive table reaches
	// tenant-bearing rows. A row is the purged tenant's if ANY link says so: a join
	// row that references a purged membership is that membership's data regardless
	// of what its other columns point at.
	Links []Link

	// Reason is why a table is exempt, deferred, external or redacted; empty otherwise.
	Reason string

	// Redact names the columns a ClassRedacted table has emptied for the purged
	// tenant; empty otherwise. It is carried on the entry rather than looked up again
	// at sweep time so the plan is a complete description of what the purge will do —
	// a reader of the coverage report can see WHICH identifiers are destroyed, and a
	// residual scan can check exactly those columns without a second source of truth.
	Redact []string

	// Store names the purge store that erases a ClassExternal table; empty otherwise.
	// It is carried out of the registry so a reader of the plan — the coverage gate's
	// report, or a test asserting the store is registered — can follow the claim to
	// the thing that has to make it true.
	Store string

	// Origin explains where a table came from when its own name does not. It is empty
	// for the ordinary case — a table an area's migrations created under its own name —
	// and set for one a maintainer would not otherwise recognise, today the
	// materialization hypertable behind a continuous aggregate.
	//
	// Every message a HUMAN acts on renders it through Describe: the fail-closed refusal,
	// the coverage gate's report, and both of the purge ledger's — the deferral sentence
	// and the residue error. Internal wrapping errors ("sweep %s: %w") deliberately do
	// not, because they already carry the failing statement's own context.
	Origin string
}

// Describe renders a table for a human, with its provenance when it has any.
//
// "_timescaledb_internal._materialized_hypertable_7 cannot be classified" names nothing
// anyone can act on; the same sentence with the aggregate it belongs to is actionable.
func (e Entry) Describe() string { return describeTable(e.Table, e.Origin) }

func describeTable(t Table, origin string) string {
	if origin == "" {
		return t.String()
	}
	return t.String() + " (" + origin + ")"
}

// Link is one foreign key from a transitive table into a tenant-bearing parent.
type Link struct {
	// Columns are the referencing columns on the child, in constraint order.
	Columns []string
	// Parent is the referenced table.
	Parent Table
	// ParentColumns are the referenced columns on the parent, in constraint order.
	ParentColumns []string
}

// Plan is a full classification of one database: every table in every non-system
// schema, in the order the sweep must delete them.
type Plan struct {
	// Entries holds every table, ordered so that a table is deleted before any table
	// it references. Non-actionable classes keep their place; the sweep skips them.
	Entries []Entry
}

// Actionable returns the entries the sweep deletes from, in delete order.
func (p *Plan) Actionable() []Entry {
	out := make([]Entry, 0, len(p.Entries))
	for _, e := range p.Entries {
		if e.Class == ClassDirect || e.Class == ClassTransitive {
			out = append(out, e)
		}
	}
	return out
}

// OfClass returns every entry of one class, in plan order.
func (p *Plan) OfClass(c Class) []Entry {
	out := []Entry{}
	for _, e := range p.Entries {
		if e.Class == c {
			out = append(out, e)
		}
	}
	return out
}

// tenantColumns are the column names that carry a tenant token directly.
//
// Both spellings are real and neither is a guess: "tenant_id" comes from the
// rdb.TenantScoped embed that most models use, and "tenant" from event-processing's
// projections, which carry the tenant as a plain composite-primary-key column instead
// of the embed. That second spelling is exactly why this list is matched against the
// catalog and then cross-checked: a third spelling appearing in a future model would
// leave its table with no recognised tenant column, which lands it in
// ClassUnclassified and fails the purge — loudly, and before it ships.
var tenantColumns = []string{"tenant_id", "tenant"}

// systemSchemas are never classified: they belong to PostgreSQL and TimescaleDB, not
// to an instance's functional areas.
//
// The prefix rule covers pg_catalog, pg_toast and the pg_temp_N/pg_toast_temp_N
// schemas a session creates; the explicit names cover information_schema, the
// TimescaleDB catalog and its internal chunk schema. _timescaledb_internal is the
// one that matters to get right: it holds the chunks of every hypertable and the
// materialization hypertables of every continuous aggregate. Deleting a tenant's rows
// from the parent hypertable already removes them from its chunks, so classifying
// chunks individually would double-count at best.
//
// A continuous aggregate's materialization is the exception, because it is genuinely
// separate residue rather than a second view of rows already in the plan. It is
// admitted back INDIVIDUALLY, by name, from the aggregate catalog — see aggregate.go.
// Skipping the schema and then re-admitting exactly the relations that need it is what
// keeps thousands of chunks out of the plan without letting the one table that matters
// out with them.
var systemSchemas = []string{
	"information_schema",
	"_timescaledb_catalog",
	"_timescaledb_internal",
	"_timescaledb_config",
	"_timescaledb_cache",
	"_timescaledb_functions",
	"timescaledb_information",
	"timescaledb_experimental",
}

func isSystemSchema(name string) bool {
	if strings.HasPrefix(name, "pg_") {
		return true
	}
	for _, s := range systemSchemas {
		if name == s {
			return true
		}
	}
	return false
}

// column is one catalog column row.
type column struct {
	Schema string
	Table  string
	// Kind is the relation's pg_class relkind. It decides whether a tenant column is
	// enough to make the table sweepable — see sweepableKind.
	Kind string
	Name string
	Type string
}

// sweepableKind reports whether a relation of this pg_class relkind can be erased with
// a DELETE, which is the only thing this package knows how to do.
//
// An ordinary or partitioned table can. A MATERIALIZED VIEW cannot — `DELETE FROM` a
// matview is a syntax error, and its rows are a copy that outlives any delete against
// the base table until someone refreshes it. A FOREIGN TABLE's rows are not even in this
// database. Both therefore need a mechanism this package does not have, so a tenant
// column on one does NOT make it direct: it falls through to ClassUnclassified and has
// to be answered for explicitly. Silently classifying one as direct would produce a
// sweep that fails at the statement (matview) or reaches across a wrapper into somebody
// else's system (foreign table) — and classifying it as exempt would be the retention
// bug wearing a reassuring label.
func sweepableKind(kind string) bool { return kind == "r" || kind == "p" }

// kindOf returns a relation's relkind from its columns. Every column row carries the
// same value, so the first one answers for the relation.
//
// An empty kind means the caller built the columns by hand rather than reading them
// from the catalog, which the unit tests do; it is treated as an ordinary table so a
// synthetic catalog does not have to restate the common case on every row.
func kindOf(cols []column) string {
	if len(cols) == 0 || cols[0].Kind == "" {
		return "r"
	}
	return cols[0].Kind
}

// constraint is one catalog foreign key, flattened to parallel column lists.
type constraint struct {
	Schema        string
	Table         string
	Columns       string
	ParentSchema  string
	ParentTable   string
	ParentColumns string
}

// Classify reads the catalog of the database behind db and classifies every table in
// every non-system schema.
//
// It never reads or writes tenant data — the result describes the SHAPE of the database,
// which is why it can be asserted on in CI against a freshly migrated database holding
// no rows at all.
//
// 🔴 Classify per purge; do NOT cache a plan across them. A plan is a snapshot of the
// catalog, and migrations append normally now, so a plan held across a deploy that added
// a table would simply never sweep it — and nothing would say so, because checkClassified
// inspects the PLAN rather than the database. The cost of getting it fresh is two catalog
// reads against a table count in the tens.
func Classify(ctx context.Context, db *gorm.DB) (*Plan, error) {
	cols, err := loadColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	fks, err := loadForeignKeys(ctx, db)
	if err != nil {
		return nil, err
	}
	admitted, err := admittedRelations(ctx, db)
	if err != nil {
		return nil, err
	}
	return classify(cols, fks, admitted)
}

// SchemaExists reports whether a schema is present in the database behind db.
//
// It answers one question a purge caller cannot answer any other way: has a functional
// area ever run against this database? A schema is created by its own service on first
// startup and by nothing else, so its absence means the area was never deployed here —
// which for a store that spans clusters is the difference between "nothing to erase" and
// "something is wrong". Nothing in a service's configuration carries that fact: the
// credentials for every cluster reach every pod, and there is no runtime area discovery.
//
// 🔴 IT IS ONLY EVER SAFE TO READ AS "NOTHING TO ERASE" WITH A LOGGED REASON. The same
// answer is returned by a database that is mid-restore, and a purge that completes on it
// writes an erasure record that is false. The caller owns that judgement; this function
// only reports the fact.
func SchemaExists(ctx context.Context, db *gorm.DB, schema string) (bool, error) {
	var n int64
	// pg_namespace rather than information_schema.schemata: the latter shows only schemas
	// the CURRENT ROLE has privileges on, so a schema owned by another service's role
	// would read as absent — which is exactly the fail-open this is guarding against.
	err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM pg_namespace WHERE nspname = ?`, schema).Scan(&n).Error
	if err != nil {
		return false, fmt.Errorf("checking whether the %q schema exists: %w", schema, err)
	}
	return n > 0, nil
}

// loadColumns reads every column of every row-holding relation in every non-system
// schema.
//
// 🔴 IT READS pg_class RATHER THAN information_schema, AND THAT IS NOT A STYLE CHOICE.
// A MATERIALIZED VIEW does not appear in information_schema at all — not in .tables and
// not in .columns; verified live on PostgreSQL 16, both queries return zero rows for one.
// So an information_schema-based classifier does not merely misclassify a matview of
// tenant data, it cannot see that it exists: the table never reaches ClassUnclassified,
// the fail-closed gate never fires, and the rows are retained forever behind a green CI
// run. pg_class has no such gap.
//
// relkind is filtered to the relations that hold rows of their own:
//
//	r  ordinary table
//	p  partitioned table (its partitions are also 'r' and classify separately; the
//	   resulting double delete is harmless, the second one matches nothing)
//	m  materialized view — holds a physical copy of its query's rows
//	f  foreign table — rows live elsewhere, which is worse, not better
//
// Plain views ('v') are excluded because they hold no rows: deleting through one is at
// best a no-op and at worst an updatable-view write to a base table that has its own
// entry. TimescaleDB's continuous aggregates are 'v' — the rows they serve live in a
// materialization hypertable under _timescaledb_internal, which is admitted to the plan
// by name from the aggregate catalog (aggregate.go) rather than by scanning schemas.
//
// No schema filter is applied HERE, deliberately, even though it would keep every chunk
// of every hypertable out of the result. isSystemSchema is the one authority on which
// schemas are ours, and expressing the same rule a second time in SQL would put the two a
// refactor away from disagreeing — in the direction where a table silently leaves the
// plan, which is the failure this package exists to prevent.
//
// The cost that buys off, measured on PG17/TimescaleDB 2.28: about 11 catalog rows per
// chunk, ~35 for a compressed one — 5ms at one chunk, 28ms at a thousand, 99ms at three
// thousand. 🔴 It has NO CEILING, because retention ships opt-in and off: chunks
// accumulate for as long as an instance keeps events, so this grows without bound while
// everything else in a pass does not. It is still only ~30% of a warm pass today (the
// per-hypertable statements dominate, almost entirely in PLANNING against the chunk
// count), and it runs on a 60s ticker only while a tenant is being erased.
//
// If it ever does need fixing, DERIVE the SQL predicate from systemSchemas and the
// admitted map rather than hand-writing a second copy of the rule — that keeps the one
// property this comment is defending.
func loadColumns(ctx context.Context, db *gorm.DB) ([]column, error) {
	var out []column
	err := db.WithContext(ctx).Raw(`
		SELECT ns.nspname AS schema, c.relname AS table, c.relkind AS kind,
		       a.attname AS name, format_type(a.atttypid, NULL) AS type
		FROM pg_class c
		JOIN pg_namespace ns ON ns.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE c.relkind IN ('r', 'p', 'm', 'f')
		ORDER BY ns.nspname, c.relname, a.attnum`).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("read catalog columns: %w", err)
	}
	return out, nil
}

// loadForeignKeys reads every foreign key as (child columns) -> (parent columns).
//
// It reads pg_constraint rather than information_schema.key_column_usage because the
// information_schema view does not expose a composite key's column pairing reliably
// across versions, and a mispaired composite key would build a subquery that silently
// matches the wrong rows. The unnest-with-ordinality join below pairs conkey with
// confkey by POSITION, which is how PostgreSQL itself defines the correspondence.
func loadForeignKeys(ctx context.Context, db *gorm.DB) ([]constraint, error) {
	var out []constraint
	err := db.WithContext(ctx).Raw(`
		SELECT
		  child_ns.nspname  AS schema,
		  child.relname     AS table,
		  string_agg(child_att.attname,  ',' ORDER BY k.ord) AS columns,
		  parent_ns.nspname AS parent_schema,
		  parent.relname    AS parent_table,
		  string_agg(parent_att.attname, ',' ORDER BY k.ord) AS parent_columns
		FROM pg_constraint con
		JOIN pg_class      child     ON child.oid  = con.conrelid
		JOIN pg_namespace  child_ns  ON child_ns.oid  = child.relnamespace
		JOIN pg_class      parent    ON parent.oid = con.confrelid
		JOIN pg_namespace  parent_ns ON parent_ns.oid = parent.relnamespace
		JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(child_attnum, parent_attnum, ord) ON TRUE
		JOIN pg_attribute child_att  ON child_att.attrelid  = con.conrelid  AND child_att.attnum  = k.child_attnum
		JOIN pg_attribute parent_att ON parent_att.attrelid = con.confrelid AND parent_att.attnum = k.parent_attnum
		WHERE con.contype = 'f'
		GROUP BY con.oid, child_ns.nspname, child.relname, parent_ns.nspname, parent.relname
		ORDER BY 1, 2, 4, 5`).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("read catalog foreign keys: %w", err)
	}
	return out, nil
}

// classify is the pure core: catalog facts in, plan out. Kept separate from the
// queries so the classification rules can be tested on synthetic catalogs, including
// shapes no migration has produced yet.
// admitted maps a relation that lives in a system schema but must still be classified to
// the sentence explaining where it came from — see aggregate.go, which is the only thing
// that produces one today.
//
// 🔑 THE MAP IS THE ADMISSION SET AND THE PROVENANCE SOURCE AT ONCE, which is the point of
// its shape: nothing can be admitted without supplying the sentence that explains it, and
// a relation whose generated name means nothing on its own cannot enter the plan
// anonymously.
type admitted map[Table]string

func classify(cols []column, fks []constraint, extra admitted) (*Plan, error) {
	tables, order := groupColumns(cols, extra)

	// 🔴 Every relation admitted by name must have reached the plan. Admission is only as
	// good as the name matching, and a mismatch — a TimescaleDB release that reports a
	// materialization differently, a view whose materialization has been dropped — would
	// take it out of the plan SILENTLY, which is the exact shape of retention bug the
	// schema scan was made catalog-driven to avoid. Two catalogs disagreeing is a fact
	// worth failing on.
	for t, origin := range extra {
		if _, ok := tables[t]; !ok {
			return nil, fmt.Errorf("%s was admitted to the purge as %s, but no such relation is in "+
				"the catalog — its rows cannot be erased and a purge would silently retain them",
				t, origin)
		}
	}

	entries := make(map[Table]*Entry, len(tables))
	for _, t := range order {
		e := &Entry{Table: t, Origin: extra[t]}
		// 🔴 A REDACTION IS DECIDED AHEAD OF THE COLUMN CHECK, and it is the only rule
		// that runs before the catalog. See ClassRedacted for why the last-wins ordering
		// that governs every exemption cannot express this one.
		if rule, ok := redactionFor(t); ok {
			if cols := tables[t]; sweepableKind(kindOf(cols)) {
				if col, ok := directColumn(cols); ok {
					e.Class = ClassRedacted
					e.Reason = rule.Reason
					e.Redact = rule.Columns
					e.Column = col
					entries[t] = e
					continue
				}
			}
			// 🔴 A REDACTION WITHOUT A TENANT COLUMN FALLS THROUGH TO UNCLASSIFIED, which
			// fails the purge before a row is touched — and fails it in CI, where the
			// coverage gate runs, rather than inside the sweep transaction of a real
			// customer's deletion. This rule runs ahead of the catalog, so without the
			// fall-through a matview or a foreign table that happened to carry the
			// registered name would be classified redacted with nothing to select on,
			// pass every gate, and then error on every pass forever.
		}
		// A tenant column only makes a relation direct if a DELETE can act on it.
		// A matview or foreign table with a tenant column needs an explicit answer,
		// not a statement that fails or reaches into another system.
		if cols := tables[t]; sweepableKind(kindOf(cols)) {
			if col, ok := directColumn(cols); ok {
				e.Class = ClassDirect
				e.Column = col
			}
		}
		entries[t] = e
	}

	// A relation a DELETE cannot act on must not become transitive either — the
	// statement would fail exactly as it would for a direct one.
	sweepable := make(map[Table]bool, len(order))
	for _, t := range order {
		sweepable[t] = sweepableKind(kindOf(tables[t]))
	}

	edges := groupForeignKeys(fks)
	resolveTransitive(entries, edges, sweepable)

	// Anything still unclassified gets its exemption, if it has one. Exemptions are
	// applied LAST so that a table which genuinely carries tenant data is swept even
	// if a stale exemption still names it — an exemption can only ever excuse a table
	// the catalog could not explain, never override the catalog.
	for _, e := range entries {
		if e.Class != ClassUnclassified {
			continue
		}
		if rule, ok := exemptionFor(e.Table); ok {
			e.Class = rule.Class
			e.Reason = rule.Reason
			e.Store = rule.Store
		}
	}

	sorted, err := deleteOrder(order, entries, edges)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Entries: make([]Entry, 0, len(sorted))}
	for _, t := range sorted {
		plan.Entries = append(plan.Entries, *entries[t])
	}
	return plan, nil
}

// groupColumns buckets catalog columns by table and returns a stable table order.
//
// extra names the relations that live in a system schema and are classified anyway. It is
// keyed by the exact table so it can only ever re-admit relations someone named
// individually — a schema-level escape hatch here would let every chunk back in.
func groupColumns(cols []column, extra admitted) (map[Table][]column, []Table) {
	tables := map[Table][]column{}
	order := []Table{}
	// The catalog read is ordered by schema, so consecutive rows almost always share one.
	// Caching the verdict turns a per-ROW linear scan of the system-schema list into a
	// per-SCHEMA one — on a mature telemetry database that is the difference between a
	// million-odd string comparisons and a couple of hundred.
	lastSchema, lastIsSystem := "", false
	for _, c := range cols {
		if c.Schema != lastSchema {
			lastSchema, lastIsSystem = c.Schema, isSystemSchema(c.Schema)
		}
		t := Table{Schema: c.Schema, Name: c.Table}
		if _, ok := extra[t]; !ok && lastIsSystem {
			continue
		}
		if _, seen := tables[t]; !seen {
			order = append(order, t)
		}
		tables[t] = append(tables[t], c)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].Schema != order[j].Schema {
			return order[i].Schema < order[j].Schema
		}
		return order[i].Name < order[j].Name
	})
	return tables, order
}

// directColumn returns the tenant column of a table, if it has one.
//
// The type check is not decoration. A column named "tenant_id" that is an integer is
// a foreign key to some other table's row id, not a tenant token, and building
// `WHERE tenant_id = 'acme'` against it fails at runtime with a type error — during a
// purge, which is the worst possible time to discover it. A non-character tenant_id
// therefore does NOT make a table direct, which leaves it unclassified and fails the
// purge up front with a name attached.
func directColumn(cols []column) (string, bool) {
	for _, want := range tenantColumns {
		for _, c := range cols {
			if c.Name == want && isCharacterType(c.Type) {
				return c.Name, true
			}
		}
	}
	return "", false
}

func isCharacterType(dataType string) bool {
	switch dataType {
	case "character varying", "character", "text":
		return true
	}
	return false
}

// groupForeignKeys indexes constraints by child table.
func groupForeignKeys(fks []constraint) map[Table][]Link {
	edges := map[Table][]Link{}
	for _, fk := range fks {
		if isSystemSchema(fk.Schema) || isSystemSchema(fk.ParentSchema) {
			continue
		}
		child := Table{Schema: fk.Schema, Name: fk.Table}
		edges[child] = append(edges[child], Link{
			Columns:       strings.Split(fk.Columns, ","),
			Parent:        Table{Schema: fk.ParentSchema, Name: fk.ParentTable},
			ParentColumns: strings.Split(fk.ParentColumns, ","),
		})
	}
	return edges
}

// resolveTransitive marks every table that reaches tenant-bearing rows through foreign
// keys, and gives each one the full set of links by which it does so.
//
// 🔴 IT IS TWO PHASES, AND COLLAPSING THEM INTO ONE IS A REAL BUG THAT LOOKS LIKE AN
// OPTIMISATION. Membership and links have to be settled separately, because a table's
// link set is only correct once the bearing set has stopped growing. Computing a link
// set at the moment a table is first marked captures the parents that were bearing AT
// THAT MOMENT — and the walk is over a Go map, so which those are is random per run.
//
// The shape that breaks: T references both A (direct) and B, and B references A. If the
// walk reaches T before B, T is marked transitive with a link to A only, then skipped
// forever, and its link to B is never added. A T row whose a_id is NULL and whose b_id
// points at the purged tenant's B row is then not matched — the row survives the sweep,
// and the later delete of B trips the foreign key and aborts the whole transaction. A
// measured single-phase version produced the incomplete link set in 25 of 200 runs.
//
// Note what a test has to assert to see this: the CLASS is right in every run — only
// len(Links) is wrong. An assertion on Class alone passes with the bug present.
func resolveTransitive(entries map[Table]*Entry, edges map[Table][]Link, sweepable map[Table]bool) {
	bearing := func(t Table) bool {
		e, ok := entries[t]
		return ok && (e.Class == ClassDirect || e.Class == ClassTransitive)
	}
	linksInto := func(t Table) []Link {
		links := []Link{}
		for _, l := range edges[t] {
			if l.Parent != t && bearing(l.Parent) {
				links = append(links, l)
			}
		}
		sort.Slice(links, func(i, j int) bool {
			return links[i].Parent.String() < links[j].Parent.String()
		})
		return links
	}

	// Phase 1 — settle WHICH tables are tenant-bearing, to a fixpoint. Reachability
	// chains, so a single pass would classify a table or not depending on the order the
	// map happened to yield.
	for {
		changed := false
		for t, e := range entries {
			if e.Class != ClassUnclassified || !sweepable[t] || len(linksInto(t)) == 0 {
				continue
			}
			e.Class = ClassTransitive
			changed = true
		}
		if !changed {
			break
		}
	}

	// Phase 2 — derive every transitive table's links from the FINAL bearing set.
	for t, e := range entries {
		if e.Class == ClassTransitive {
			e.Links = linksInto(t)
		}
	}
}

// deleteOrder sorts tables so that every table is deleted BEFORE any table it
// references, which is what the foreign keys require: deleting a parent row while a
// child still references it is what the constraint exists to prevent.
//
// The ordering is doing real work rather than papering over something the database
// would have handled: the platform's foreign keys are plain references, not ON DELETE
// CASCADE, so a parent deleted while a child still references it is rejected. (No count
// is given here on purpose — a number frozen in prose only ever drifts away from the
// tree. Ask it instead: `grep -h "FOREIGN KEY" backend/tools/migrationdiff/golden/*.sql`.)
//
// A cycle is reported rather than broken. Breaking one silently would produce a sweep
// that fails partway through on a constraint violation, which reads as a database
// problem rather than as the schema shape it actually is.
func deleteOrder(order []Table, entries map[Table]*Entry, edges map[Table][]Link) ([]Table, error) {
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := map[Table]int{}
	out := make([]Table, 0, len(order))

	var visit func(t Table, path []Table) error
	visit = func(t Table, path []Table) error {
		switch state[t] {
		case done:
			return nil
		case active:
			return fmt.Errorf("foreign-key cycle in the schema, which the sweep cannot order: %s",
				cyclePath(append(path, t)))
		}
		state[t] = active
		// Visit children first: everything that references t must be emitted before
		// t. The edge list is child -> parent, so this walks it backwards.
		refs := []Table{}
		for child, links := range edges {
			if _, known := entries[child]; !known {
				continue
			}
			for _, l := range links {
				if l.Parent == t && child != t {
					refs = append(refs, child)
					break
				}
			}
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
		for _, child := range refs {
			if err := visit(child, append(path, t)); err != nil {
				return err
			}
		}
		state[t] = done
		out = append(out, t)
		return nil
	}

	for _, t := range order {
		if err := visit(t, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func cyclePath(path []Table) string {
	names := make([]string, 0, len(path))
	for _, t := range path {
		names = append(names, t.String())
	}
	return strings.Join(names, " -> ")
}
