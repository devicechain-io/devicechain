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

	// --- metering ---
	{
		Path: "backend/services/event-management/model/usage.go", Func: "Api.RollupUsageHours", Count: 3,
		Why: "the usage rollup reads EVERY tenant's events for one hour on purpose, under a system context, " +
			"and writes per-tenant rows: the hourly upsert GROUPs BY tenant_id, the device-set insert selects " +
			"tenant_id with each device and keys its conflict on (tenant_id, day, device_token), and the daily " +
			"derivation groups the hourly rows by tenant_id and counts the device set through an explicit " +
			"d.tenant_id = h.tenant_id. Each statement is constant text with only the hour, day and clock bound. " +
			"No count can mix two tenants; the two-tenant integration test (usage_integration_test.go) reuses " +
			"one device token across tenants to prove it",
	},
	{
		Path: "backend/services/event-management/model/usage.go", Func: "Api.PruneUsageWorkingTables", Count: 2,
		Why: "deletes the usage rollup's WORKING rows (tenant_usage_hourly, tenant_usage_devices) of every " +
			"tenant for days older than a cutoff, on purpose and under a system context: retention by time, " +
			"not a read or write of any one tenant's data. The predicate is the hour/day alone and is bound; " +
			"the per-tenant record (tenant_usage_daily) is never touched",
	},
	{
		Path: "backend/services/event-management/model/usage.go", Func: "Api.TenantUsage", Count: 1,
		Why: "the instance admin plane's read of ONE tenant's usage record, under a system context (the " +
			"/admin/graphql caller is an identity, not a tenant member, so there is no tenant in context for " +
			"the callback to inject). The tenant is bound explicitly as WHERE tenant_id = ?, the function " +
			"refuses an empty tenant, and the statement reads no other table",
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
		Path: "backend/services/device-management/model/api_x509_crl_stage.go", Func: "applyStagedEntries", Count: 4,
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
		Path: "backend/services/device-management/model/api_x509_crl_stage.go", Func: "keepNewSerials", Count: 2,
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
