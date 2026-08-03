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
	default:
		return "unclassified"
	}
}

// Table identifies one table by schema and name. Schema names are the functional-area
// names verbatim, so they contain hyphens ("device-management") and only survive SQL
// as quoted identifiers — see quoteIdent.
type Table struct {
	Schema string
	Name   string
}

func (t Table) String() string { return t.Schema + "." + t.Name }

// quoted renders the table as a schema-qualified quoted identifier.
func (t Table) quoted() string { return quoteIdent(t.Schema) + "." + quoteIdent(t.Name) }

// Entry is one table's classification, with everything the sweep needs to act on it
// and everything a reader needs to judge it.
type Entry struct {
	Table Table
	Class Class

	// Column is the tenant column, for ClassDirect only.
	Column string

	// Links are the foreign keys through which a ClassTransitive table reaches
	// tenant-bearing rows. A row is the purged tenant's if ANY link says so: a join
	// row that references a purged membership is that membership's data regardless
	// of what its other columns point at.
	Links []Link

	// Reason is why a table is exempt or deferred; empty otherwise.
	Reason string
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
// chunks individually would double-count at best; the continuous aggregate's
// materialization is genuinely separate residue and is handled as its own step
// against the aggregate's own catalog, not by scanning this schema.
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
	Name   string
	Type   string
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
// It never reads or writes tenant data — the result describes the SHAPE of the
// database, so it can be computed once and reused across purges, and can be asserted
// on in CI against a freshly migrated database with no rows in it at all.
func Classify(ctx context.Context, db *gorm.DB) (*Plan, error) {
	cols, err := loadColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	fks, err := loadForeignKeys(ctx, db)
	if err != nil {
		return nil, err
	}
	return classify(cols, fks)
}

// loadColumns reads every column of every ordinary table in every non-system schema.
//
// The table_type filter keeps VIEWs and MATERIALIZED VIEWs out: a view holds no rows
// of its own, so deleting through one is at best a no-op and at worst an updatable-view
// write to the underlying table that the base table's own entry already covers.
func loadColumns(ctx context.Context, db *gorm.DB) ([]column, error) {
	var out []column
	err := db.WithContext(ctx).Raw(`
		SELECT c.table_schema AS schema, c.table_name AS table,
		       c.column_name AS name, c.data_type AS type
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE t.table_type = 'BASE TABLE'
		ORDER BY c.table_schema, c.table_name, c.ordinal_position`).Scan(&out).Error
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
func classify(cols []column, fks []constraint) (*Plan, error) {
	tables, order := groupColumns(cols)

	entries := make(map[Table]*Entry, len(tables))
	for _, t := range order {
		e := &Entry{Table: t}
		if col, ok := directColumn(tables[t]); ok {
			e.Class = ClassDirect
			e.Column = col
		}
		entries[t] = e
	}

	edges := groupForeignKeys(fks)
	resolveTransitive(entries, edges)

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
func groupColumns(cols []column) (map[Table][]column, []Table) {
	tables := map[Table][]column{}
	order := []Table{}
	for _, c := range cols {
		if isSystemSchema(c.Schema) {
			continue
		}
		t := Table{Schema: c.Schema, Name: c.Table}
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

// resolveTransitive marks every table that reaches tenant-bearing rows through
// foreign keys, to a fixpoint.
//
// The fixpoint matters because reachability chains: a join table whose parent is
// itself only transitively tenant-bearing is still the tenant's data, and a single
// pass in table order would classify it or not depending on which name sorted first.
// Iterating until nothing changes makes the result independent of ordering.
func resolveTransitive(entries map[Table]*Entry, edges map[Table][]Link) {
	bearing := func(t Table) bool {
		e, ok := entries[t]
		return ok && (e.Class == ClassDirect || e.Class == ClassTransitive)
	}
	for {
		changed := false
		for t, e := range entries {
			if e.Class != ClassUnclassified {
				continue
			}
			links := []Link{}
			for _, l := range edges[t] {
				if l.Parent != t && bearing(l.Parent) {
					links = append(links, l)
				}
			}
			if len(links) == 0 {
				continue
			}
			sort.Slice(links, func(i, j int) bool {
				return links[i].Parent.String() < links[j].Parent.String()
			})
			e.Class = ClassTransitive
			e.Links = links
			changed = true
		}
		if !changed {
			return
		}
	}
}

// deleteOrder sorts tables so that every table is deleted BEFORE any table it
// references, which is what the foreign keys require: deleting a parent row while a
// child still references it is what the constraint exists to prevent.
//
// None of the platform's foreign keys are ON DELETE CASCADE — they are plain
// references — so the ordering is doing real work rather than papering over a
// constraint the database would have handled. Verified against the golden schemas:
// 15 foreign keys, none cascading.
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

// quoteIdent renders a SQL identifier as a double-quoted literal.
//
// Every identifier this package emits goes through here, and it is not optional
// hygiene: functional-area schema names contain hyphens, so "device-management".devices
// is the only spelling PostgreSQL parses — unquoted it is a subtraction. Embedded
// double quotes are doubled per the SQL standard.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
