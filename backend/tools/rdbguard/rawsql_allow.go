// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

// rawSQLAllowList is the complete set of functions permitted to run raw SQL that names a
// tenant-scoped table or whose SQL is not statically readable. Every entry is one
// statement somebody read and judged; see siteEntry for the anchor and the counting rule.
//
// 🔴 AN ENTRY STEPS AROUND THE TENANT PREDICATE. Raw and Exec statements are invisible to
// the tenant-scope callback, so the reason beside each entry is the ONLY thing standing
// between the statement and every tenant's rows. Categories, as they stand:
//
//   - erasure: tenantpurge sweeps and fences span tenants on purpose and bind their own
//     tenant predicate, under a system context.
//   - ddl-catalog: DDL, grants, catalog and Timescale policy statements. They touch no
//     tenant row at all; the SQL is "dynamic" only because the identifier is formatted in.
//   - control-plane: user-management statements over instance-scoped rows (including a join
//     table that is tenant-bearing only through its parent).
//   - not-sql: an Exec that is not a database call (the GraphQL executor).
//   - tenant-write: the column-array insert, the one raw statement that WRITES tenant
//     rows. Its text is built from the model's schema, and its tenant is bound from the
//     context after the checks a gorm Create runs; see core/rdb/insert_columns.go.
//   - metering: the usage rollup, which aggregates every tenant's events into per-tenant rows
//     under a system context, grouping and keying by tenant_id at every step.
var rawSQLAllowList = []siteEntry{
	// --- not-sql ---
	{
		Path: "backend/core/graphql/schema.go", Func: "Schema.Exec", Count: 1,
		Why: "the GraphQL executor's own Exec, not a database call: its first argument is a context " +
			"wrapped by execContext, which the context-first heuristic cannot see through",
	},
	{
		Path: "backend/core/test/isolationsweep/sweep.go", Func: "Run", Count: 1,
		Why: "cfg.Schema is a *graphql.Schema: this Exec runs a GraphQL document through the service's own " +
			"resolvers (the isolation sweep's negative control), not SQL",
	},
	{
		Path: "backend/core/test/isolationsweep/sweep.go", Func: "judge", Count: 2,
		Why: "cfg.Schema is a *graphql.Schema: these Execs run the probe and its missing twin as GraphQL " +
			"documents through the service's resolvers, not SQL",
	},

	// --- ddl-catalog ---
	{
		Path: "backend/core/test/secretstest/postgres.go", Func: "Stores", Count: 2,
		Why: "integration-test helper only (build tag integration): CREATE SCHEMA and its cleanup's DROP SCHEMA " +
			"... CASCADE for a throwaway schema whose name is secrets_it_ plus a nanosecond timestamp, formatted " +
			"in because a schema name cannot be bound and must be unique per run. DDL on that test schema " +
			"alone; touches no row",
	},
	{
		Path: "backend/core/rdb/storage_growth.go", Func: "measureFromCatalog", Count: 1,
		Why: "reads relation sizes from the pg_class catalog for one table name; returns no tenant rows",
	},
	{
		Path: "backend/core/rdb/token_index.go", Func: "CreatePartialUniqueIndex", Count: 1,
		Why: "CREATE UNIQUE INDEX DDL built from a caller-supplied table and index name; touches no rows",
	},
	{
		Path: "backend/core/rdb/token_index.go", Func: "CreateTenantExternalIdIndex", Count: 1,
		Why: "CREATE UNIQUE INDEX DDL built from a caller-supplied table and index name; touches no rows",
	},
	{
		Path: "backend/services/event-management/model/analytics.go", Func: "ReconcileAnalyticsSurface", Count: 2,
		Why: "role catalog lookup and the grant/revoke statements that reconcile the analytics surface; " +
			"the analytics views enforce their own tenant filter in SQL, and these statements read no rows",
	},
	{
		Path: "backend/services/event-management/model/analytics.go", Func: "execAnalyticsFunction", Count: 1,
		Why: "DDL creating the analytics tenant-resolving function; touches no rows",
	},
	{
		Path: "backend/services/event-management/model/analytics.go", Func: "execAnalyticsSurface", Count: 1,
		Why: "DDL (re)creating the analytics views; issues no grants and touches no rows",
	},
	{
		Path: "backend/services/event-management/model/lifecycle.go", Func: "applyOne", Count: 7,
		Why: "installs INSTANCE-WIDE TimescaleDB chunk, compression and retention policies, one call " +
			"per policy statement (each call to the exec closure counts as a site). The retention " +
			"policy deletes every tenant's rows by age, by operator opt-in; it is table-level " +
			"configuration with no tenant predicate by design",
	},
	{
		Path: "backend/services/event-management/model/lifecycle.go", Func: "applyRollupRetention", Count: 3,
		Why: "reconciles the INSTANCE-WIDE TimescaleDB retention policy on the measurement_rollups continuous " +
			"aggregate: remove_retention_policy/add_retention_policy on the package constant RollupAggregate, " +
			"formatted in by the same statement builders applyOne uses, with the window in days bound. Two " +
			"calls to the exec closure on the enable path and one direct Exec on the disable path. Like the raw " +
			"tables' policy it ages out every tenant's rows by time, by operator opt-in, and is table-level " +
			"configuration with no tenant predicate by design; it reads and writes no row itself",
	},

	{
		Path: "backend/services/event-management/model/rollup_hourly.go", Func: "Api.orderNames", Count: 1,
		Why: "sorts a list of measurement NAMES the read already returned, in the database's own collation: a " +
			"VALUES list of bound text values with no table in it, so it reads and writes no row of any tenant",
	},

	{
		Path: "backend/services/event-management/processor/backfill/hourly.go", Func: "hourlyDBProgress.bounds", Count: 1,
		Why: "the hour backfill finds the EARLIEST bucket any tenant still holds, under a system context, to " +
			"set the floor of a refresh that is itself instance-wide (a continuous aggregate refresh has no " +
			"tenant predicate): constant text returning one min() timestamp, no row of any tenant leaves it",
	},

	// --- metering ---
	{
		Path: "backend/services/event-management/model/usage.go", Func: "rollupHour", Count: 4,
		Why: "the usage rollup reads EVERY tenant's events for one hour on purpose, under a system context, " +
			"and writes per-tenant rows: the hour statement GROUPs BY tenant_id and inserts the device set " +
			"keyed (tenant_id, day, device_token), the day derivation groups the hourly rows by tenant_id and " +
			"counts the device set through an explicit d.tenant_id = h.tenant_id, and the two clears delete " +
			"by hour or day (plus a tenant_id correlation) to replace what is recomputed. Each statement is " +
			"constant text with only the hour, day, floor and clock bound, and the hour statement skips any " +
			"tenant whose erasure fence stands. No count can mix two tenants; the two-tenant integration test " +
			"(usage_integration_test.go) reuses one device token across tenants to prove it",
	},
	{
		Path: "backend/services/event-management/model/usage.go", Func: "initUsageState", Count: 1,
		Why: "reads the TimescaleDB chunk catalog (timescaledb_information.chunks) for the newest compressed " +
			"chunk of the events hypertable; 'events' appears only as the catalog's hypertable_name literal, " +
			"and the statement reads no event row and no tenant's data",
	},
	{
		Path: "backend/services/event-management/model/usage.go", Func: "Api.PruneUsageWorkingTables", Count: 2,
		Why: "deletes the usage rollup's WORKING rows (tenant_usage_hourly, tenant_usage_devices) of every " +
			"tenant for days older than a cutoff, on purpose and under a system context: retention by time, " +
			"not a read or write of any one tenant's data. The predicate is the hour/day alone and is bound; " +
			"the per-tenant record (tenant_usage_daily) is never touched",
	},

	// --- tenant-write ---
	{
		Path: "backend/core/rdb/insert_columns.go", Func: "ColumnTable.exec", Count: 2,
		Why: "the column-array insert and upsert (one statement on Postgres, its VALUES rendering on sqlite). " +
			"The text is built from the model's own gorm schema plus, for an upsert, caller-written SQL " +
			"FRAGMENTS (each SET expression and the WHERE guard): those are constants given once, when the " +
			"table is built, and are not computed. Three things make that so. rdb.Fragment is a named string " +
			"type, so only a constant converts to it implicitly; NewColumnUpsert accepts, at construction, " +
			"only an ALLOWLISTED grammar (t. and excluded. column references, numeric and plain string " +
			"literals, AND/OR/NOT/IS/NULL/CASE, COALESCE, NULLIF, operators, parentheses; commas only in " +
			"function arguments), so a fragment cannot name another table or call another function; and " +
			"this scan's " +
			"column-upsert rule (columnupsert.go) fails any construction site whose fragments are not string " +
			"literals or package constants, so a reviewer reads every one in place. The caller's guard is " +
			"rendered in parentheses ANDed with the statement's own tenant term, SET may not name the tenant " +
			"column, id or created_at, the conflict target must name the tenant column, and the tenant itself " +
			"is bound from the context after the tenant-mismatch check and the erasure fence have run " +
			"(ColumnTable.write)",
	},

	{
		Path: "backend/services/device-management/model/identity/x509_crl_stage.go", Func: "ApplyStagedEntries", Count: 4,
		Why: "the set-based write of one CRL's entries from the transaction's own staging table: two counts, a " +
			"DELETE and an INSERT...SELECT over device_revoked_certificates. Each statement is a constant text " +
			"whose WHERE names tenant_id, trust_anchor_id, revocation_list_id, kind, issuer_key and " +
			"issuer_name, all bound from the caller (the tenant from the request context, the rest from the " +
			"locked anchor and the list row just written), and the INSERT writes only the bound tenant. The " +
			"staging table is a TEMP table private to the transaction. Nothing is formatted into the text " +
			"but package constants. A gorm bulk Create of hundreds of thousands of rows is the cost this " +
			"replaces (see the memory and lock-hold test)",
	},

	{
		Path: "backend/services/device-management/model/identity/x509_crl_stage.go", Func: "KeepNewSerials", Count: 2,
		Why: "copies, into a TEMP table private to the transaction, the staged serials the list does not hold " +
			"yet (the live-session kick's input). The SELECT's NOT EXISTS over device_revoked_certificates is a " +
			"constant text whose WHERE binds tenant_id, trust_anchor_id, revocation_list_id, kind, issuer_key " +
			"and issuer_name from the caller; the two counts are the Postgres and the SQLite spelling of the " +
			"same statement, and only one runs. Only package constants are formatted into the text.",
	},
	{
		Path: "backend/services/device-management/model/api_x509_revocation_kicks.go", Func: "crlRevocationKickTargets", Count: 3,
		Why: "joins a CRL upload's durable set of newly revoked serials (pending_revocation_expansion_serials, one " +
			"generation) to device_credential_chain_links. Three statements, each constant text whose WHERE binds " +
			"tenant_id and trust_anchor_id explicitly on every table (plus the generation, and the issuer name and " +
			"key where it applies): the join of the serials to the links, the count of serials no link accounts " +
			"for, and the legacy depth-0 links of other issuers.",
	},
	{
		Path: "backend/services/device-management/model/api_x509_revocation_markers.go", Func: "stashCrlRevocationSerials", Count: 1,
		Why: "copies the transaction's TEMP table of newly revoked serials into pending_revocation_expansion_serials, " +
			"binding tenant_id and trust_anchor_id from the caller on every row it writes. The only formatted-in " +
			"name is a package constant naming the temp table.",
	},
	{
		Path: "backend/services/device-management/model/api_configuration_report_maintenance.go", Func: "Api.configurationReportTenants", Count: 1,
		Why: "the configuration-report maintenance's tenant listing, which spans tenants on purpose under a system " +
			"context: a loose index scan (recursive CTE) over device_configuration_reports' (tenant_id, created_at) " +
			"index that returns only the distinct tenant_id values and reads no row content. Every row it then " +
			"removes is removed through the model under that tenant's own core.WithTenant. The only formatted-in " +
			"text is the model's quoted table name.",
	},
	{
		Path: "backend/services/device-management/model/api_attribute_owner.go", Func: "lockDeviceAttributes", Count: 1,
		Why: "sets the transaction-local device attribute writer protocol marker (attrprotocol.MarkerSQL, " +
			"SELECT set_config('devicechain.attr_protocol', '1', true)). It reads and writes no table; the SQL is " +
			"\"dynamic\" only because the constant lives in core/attrprotocol, its one definition.",
	},
	{
		Path: "backend/services/device-management/model/api_attribute_owner.go", Func: "tryLockDeviceAttributes", Count: 1,
		Why: "sets the transaction-local device attribute writer protocol marker, exactly as lockDeviceAttributes does " +
			"(attrprotocol.MarkerSQL, SELECT set_config('devicechain.attr_protocol', '1', true)). It reads and writes " +
			"no table; the SQL is \"dynamic\" only because the constant lives in core/attrprotocol, its one definition.",
	},
	{
		Path: "backend/services/device-management/model/api_attribute_orphans.go", Func: "Api.attributeOrphanTenants", Count: 1,
		Why: "the attribute orphan sweep's tenant listing, which spans tenants on purpose under a system context: " +
			"a loose index scan (recursive CTE) over entity_attributes' and entity_attribute_generations' tenant_id " +
			"indexes that returns only the distinct tenant_id values and reads no row content. Every row the sweep " +
			"then removes is removed through the model under that tenant's own core.WithTenant. The only " +
			"formatted-in text is the model's quoted table name.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_operation_drive.go", Func: "Api.AcquireFleetPermits", Count: 1,
		Why: "the bulk-change pacer: ONE statement that locks the operation's runtime row, reads the database clock " +
			"after the lock and grants permits from it, so replicas with skewed process clocks share one window. " +
			"The SQL is a constant that binds the tenant explicitly (tenant_id = @tenant, from core.TenantFromContext, " +
			"refusing core.ErrNoTenant before it runs); the UPDATE touches only the row that predicate locked.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_settlement.go", Func: "fleetSettlementRead", Count: 1,
		Why: "the bulk-change settlement candidate read: one statement so the report log, the state row's gap " +
			"marker and the deciding revision's document are read in one snapshot. It binds l.tenant_id (and, on the " +
			"fast path, the report window's tenant_id) from the caller, and every other table is joined on " +
			"tenant_id = the candidate line's tenant_id, including the correlated deciding-report subquery. The only " +
			"formatted-in text is the models' quoted table names and package constants.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_settlement.go", Func: "fleetDueTenants", Count: 1,
		Why: "the bulk-change deadline pass's tenant discovery, under the system context Api.settlementTenants " +
			"declares: a recursive walk of the deadline index that returns tenant ids only, never row content. Each " +
			"due probe and its operation lookup are bound to the walked tenant (d.tenant_id = walk.tenant_id, " +
			"o.tenant_id = d.tenant_id). Only the models' quoted table names and the protocol predicate constant are " +
			"formatted in.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_settlement.go", Func: "Api.FleetReportCaptureComplete", Count: 1,
		Why: "counts one operation's lines whose target's report capture is incomplete: binds l.tenant_id from the " +
			"request context and joins the configuration state on tenant_id = l.tenant_id. Only the models' quoted " +
			"table names are formatted in.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_settlement.go", Func: "Api.FleetReconcilerLag", Count: 1,
		Why: "the settlement lag gauge's oldest undecided report: binds r.tenant_id from the context and joins lines " +
			"and operations on tenant_id equal to the report's. It returns one timestamp; only the models' quoted " +
			"table names and the protocol predicate constant are formatted in.",
	},
	{
		Path: "backend/services/device-management/model/api_fleet_operation_readiness.go", Func: "fleetOperationEnforcementStates", Count: 1,
		Why: "a catalog read (pg_trigger, pg_class, pg_namespace, pg_proc) that names entity_attributes only as the " +
			"relation whose triggers it inspects, shared by readiness and the posting-time activation check. It reads " +
			"no row of entity_attributes or of any tenant table, so there is no tenant predicate to bypass.",
	},
	{
		Path: "backend/services/device-management/model/fleet_operation_activation.go", Func: "CheckFleetOperationActivation", Count: 1,
		Why: "LOCK TABLE entity_attributes IN ROW EXCLUSIVE MODE: a table-level lock taken before the guard's catalog " +
			"is read, so that DISABLE or DROP TRIGGER waits for the transaction. It reads and writes no row of " +
			"the table, so there is no tenant predicate to bypass; the SQL is a constant.",
	},
	{
		Path: "backend/services/device-management/model/position_classify.go", Func: "Api.ClassifyUnclassifiedAlarms", Count: 1,
		Why: "the alarm position classifier's periodic pass, which spans tenants on purpose: it classifies every " +
			"alarm whose position_derived is NULL (what a replica older than the column writes during a rolling " +
			"upgrade, or a stale false the alarm_position_stale trigger re-opened) for every tenant, under no caller. It writes only alarms.position_derived and the contributor version it judged (position_classified_version), only on NULL rows, " +
			"and reads device_profiles, detection_rules and device_profile_versions solely through joins that " +
			"equate p.tenant_id with the alarm's own tenant_id, so a rule is resolved only within its alarm's " +
			"tenant. The statement is one package constant (classifyAlarmsSQL), the same text the migration " +
			"runs (TestClassifierMatchesTheMigration).",
	},
	{
		Path: "backend/services/device-management/model/position_classify.go", Func: "Api.classifyAlarm", Count: 1,
		Why: "classifies the one alarm the integrator just folded from an edge with no position verdict: the " +
			"classifier constant plus a literal WHERE binding a.tenant_id from the request context and a.id. " +
			"It writes only that row's position_derived and position_classified_version, and its joins equate p.tenant_id with the alarm's " +
			"tenant_id, as for Api.ClassifyUnclassifiedAlarms.",
	},
	{
		Path: "backend/services/event-processing/model/rule_stat_store.go", Func: "RuleStatStore.SweepStranded", Count: 1,
		Why: "the stranded rule-stat sweep's DELETE for ONE tenant: both the outer statement and its bounding " +
			"subquery repeat ruleStatStrandedPredicate, whose first clause binds rule_stats.tenant to the " +
			"tenant the call names (the context carries the same tenant), and its profile_actives subqueries " +
			"equate pp.tenant and pa.tenant with the row's own tenant. It deletes only that tenant's rows, and only derived " +
			"health counters. Raw because the anti-join and the re-checked bounded subquery have no gorm form.",
	},

	// --- erasure ---
	{
		Path: "backend/core/tenantpurge/fence.go", Func: "PlantFence", Count: 1,
		Why: "writes the erasure fence row for the tenant being purged, under a system context; the " +
			"tenant is a bound parameter",
	},
	{
		Path: "backend/core/tenantpurge/fence.go", Func: "LiftFence", Count: 1,
		Why: "lifts the erasure fence for the tenant being purged, under a system context; the tenant " +
			"is a bound parameter",
	},
	{
		Path: "backend/core/tenantpurge/sweep.go", Func: "Sweep", Count: 2,
		Why: "the erasure sweep: a DELETE and a redacting UPDATE per catalog-classified table, each " +
			"binding its own tenant predicate, under a system context. Spanning tenants is its job",
	},
	{
		Path: "backend/core/tenantpurge/sweep.go", Func: "Residue", Count: 2,
		Why: "counts what the sweep left behind for the purged tenant, binding the tenant explicitly, " +
			"under a system context",
	},
	{
		Path: "backend/core/attrprotocol/attrprotocol.go", Func: "lockTenantDevices", Count: 1,
		Why: "the tenant purge's BeforeDelete hook: locks the purged tenant's device rows FOR UPDATE in id " +
			"order before the sweep deletes them, so the purge and a device attribute writer take the device " +
			"row first and cannot deadlock. The tenant is a bound parameter; it reads only ids, under the " +
			"purge's system context",
	},

	// --- control-plane ---
	{
		Path: "backend/services/user-management/purge/detect.go", Func: "Detect.checkpointedPartitions", Count: 1,
		Why: "reads the partition-keyed detect snapshot table, which the purge catalog classes external " +
			"(the detect store erases it out of band) rather than tenant-scoped, through a query " +
			"constant owned by tenantpurge; system path",
	},
	{
		Path: "backend/services/user-management/iam/store.go", Func: "Store.DeleteRole", Count: 3,
		Why: "deletes the identity-, membership- and service-account-to-role join rows of the ONE role being deleted, by its " +
			"primary key, under the system context; the caller has already resolved and authorised that role " +
			"for its own plane (a catalogue role on the admin plane, the caller's own tenant role on the tenant " +
			"plane), so the statements cannot reach another tenant's rows",
	},
}

// rawJoinAllowList is the complete set of functions permitted to join a tenant-scoped
// table without a tenant-column equality in the ON clause.
//
// 🔴 EVERY ENTRY BELOW IS SAFE BY PROVENANCE OF THE FOREIGN KEY, NOT BY THE QUERY: the
// joined row is reached through a key that a tenant-scoped write set. That holds as long
// as every writer of the key stays tenant-scoped — the property the guard cannot see, and
// the reason the preferred resolution is to add the equality to the ON clause and delete
// the entry.
var rawJoinAllowList = []siteEntry{
	{
		Path: "backend/services/device-management/model/api_credentials.go", Func: "Api.credentialsWithDevice", Count: 1,
		Why: "association join to the credential's device, which the callback does not scope; the " +
			"function itself drops any row outside the calling tenant and clears any device that is " +
			"not the credential's own (id and tenant) before returning, as its comment explains",
	},
	{
		Path: "backend/services/device-management/model/api_group_members.go", Func: "Api.staticGroupMembers", Count: 1,
		Why: "joins entity_relationship_types by the relationship's type id, a key written under a " +
			"tenant-scoped write that resolves the type by token in the same tenant",
	},
	{
		Path: "backend/services/device-management/model/api_group_members.go", Func: "Api.IsGroupMember", Count: 1,
		Why: "joins entity_relationship_types by the relationship's type id, a key written under a " +
			"tenant-scoped write that resolves the type by token in the same tenant",
	},
	{
		Path: "backend/services/device-management/model/api_group_targets.go", Func: "Api.staticDeviceTargets", Count: 1,
		Why: "joins entity_relationship_types by the relationship's type id, a key written under a " +
			"tenant-scoped write that resolves the type by token in the same tenant",
	},
	{
		Path: "backend/services/device-management/model/api_detect_reconcile.go", Func: "Api.rosterEntries", Count: 2,
		Why: "joins device_types and device_profiles through the device's type id and the type's " +
			"profile id, keys written under tenant-scoped writes that resolve both by token in the " +
			"same tenant",
	},
	{
		Path: "backend/services/device-management/model/api_detect_reconcile.go", Func: "Api.DeviceThresholdAttributePage", Count: 1,
		Why: "joins devices through entity_attributes.entity_id, written by a tenant-scoped attribute " +
			"write that resolves the entity token under the same tenant",
	},
	{
		Path: "backend/services/device-management/model/api_alarm_state.go", Func: "Api.AlarmCountsByDeviceType", Count: 1,
		Why: "joins a DERIVED table built through gorm (Model(&Device{}), alarmDevices), so the tenant " +
			"and scope callbacks filter it as its own statement; the ON clause also equates " +
			"d.tenant_id with alarms.tenant_id, which this guard cannot read through the subquery",
	},
}
