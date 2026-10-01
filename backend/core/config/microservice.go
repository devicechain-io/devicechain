// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// Per-microservice datastore configuration.
//
// MaxOpenConnections / MaxIdleConnections size the database connection pool for
// the owning service. They must comfortably exceed that service's writer count
// (event-management's persistence.writers, 10 by default, or device-state's
// projection.writers, 5 by default, each refused unless it is below the pool size)
// plus the GraphQL server's request concurrency, otherwise writers and GraphQL
// contend for the same handles and throughput is capped. This struct is embedded in each
// service's config and does NOT implement ApplyDefaults/Validate itself; a
// zero/unset value here is treated as "use the default" at the point of use in
// the rdb package (see rdb.initializePostgres), not as a literal 0 (which the
// database/sql pool would interpret as unlimited/closed).
//
// MaxIdleConnections defaults to MaxOpenConnections: a pool keeps every connection it
// has opened, up to its size, open between uses. Set it lower only to hold fewer
// connections on the database, at the cost of a new connection and login for every
// query that finds more than that many in use.
type MicroserviceDatastoreConfiguration struct {
	SqlDebug bool

	MaxOpenConnections int
	MaxIdleConnections int
}
