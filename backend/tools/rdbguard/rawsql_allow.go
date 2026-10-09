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
//   - control-plane: user-management statements over instance-scoped rows.
//   - not-sql: an Exec that is not a database call (the GraphQL executor).
var rawSQLAllowList = []siteEntry{
	// --- not-sql ---
	{
		Path: "backend/core/graphql/schema.go", Func: "Schema.Exec", Count: 1,
		Why: "the GraphQL executor's own Exec, not a database call: its first argument is a context " +
			"wrapped by execContext, which the context-first heuristic cannot see through",
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
		Why: "DDL creating and granting the analytics views; touches no rows",
	},
	{
		Path: "backend/services/event-management/model/lifecycle.go", Func: "applyOne", Count: 2,
		Why: "TimescaleDB retention/compression/continuous-aggregate policy functions; table-level " +
			"configuration, no per-tenant row is read or written",
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
		Why: "reads the partition-keyed detect snapshot table, a declared erasure exemption that is not " +
			"tenant-scoped, through a query constant owned by tenantpurge; system path",
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
		Path: "backend/services/device-management/model/api_credentials.go", Func: "Api.presentedCredentialStatement", Count: 1,
		Why: "association join to the credential's device, which the callback does not scope; the " +
			"caller re-checks the joined device against the credential (credentialDevice) before " +
			"trusting it, as the function's own comment explains",
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
}
