// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// Per-microservice datastore configuration.
//
// MaxOpenConnections / MaxIdleConnections size the database connection pool for
// the owning service. They must comfortably exceed that service's writer count
// (event-management's persistence.writers or device-state's projection.writers, 5
// by default, each refused unless it is below the pool size) plus the GraphQL
// server's request concurrency, otherwise writers and GraphQL contend for the
// same handles and throughput is capped. This struct is embedded in each
// service's config and does NOT implement ApplyDefaults/Validate itself; a
// zero/unset value here is treated as "use the default" at the point of use in
// the rdb package (see rdb.initializePostgres), not as a literal 0 (which the
// database/sql pool would interpret as unlimited/closed).
type MicroserviceDatastoreConfiguration struct {
	SqlDebug bool

	MaxOpenConnections int
	MaxIdleConnections int
}
