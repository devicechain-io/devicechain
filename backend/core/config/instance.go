// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// Redis configuration parameters
type RedisConfiguration struct {
	Hostname string
	Port     int32
}

// NATS configuration parameters
type NatsConfiguration struct {
	Hostname string
	Port     uint32
	// StreamReplicas is the JetStream replica count for created streams
	// (1 for single-node dev; raise to 3 for the HA topology in ADR-018).
	StreamReplicas uint32
}

// Prometheus metrics configuration
type MetricsConfiguration struct {
	Enabled  bool
	HttpPort int32
}

// User-management connectivity configuration. The user-management service
// issues the platform's RS256 JWT signing key (ADR-008); every other service
// fetches the public key from this host at startup to validate access tokens.
type UserManagementConfiguration struct {
	Hostname string
	Port     uint32
}

// Infrastructure configuration section
type InfrastructureConfiguration struct {
	Redis          RedisConfiguration
	Nats           NatsConfiguration
	Metrics        MetricsConfiguration
	UserManagement UserManagementConfiguration
}

// Generic datastore configuration
type DatastoreConfiguration struct {
	Type          string
	Configuration map[string]interface{}
}

// Configuration of persistence stores
type PersistenceConfiguration struct {
	Rdb  DatastoreConfiguration
	Tsdb DatastoreConfiguration
}

// Instance-level configuration settings
type InstanceConfiguration struct {
	Infrastructure InfrastructureConfiguration
	Persistence    PersistenceConfiguration
}

// Creates the default instance configuration
func NewDefaultInstanceConfiguration() *InstanceConfiguration {
	return &InstanceConfiguration{
		Infrastructure: InfrastructureConfiguration{
			Redis: RedisConfiguration{
				Hostname: "dc-redis-master.dc-system",
				Port:     6379,
			},
			Nats: NatsConfiguration{
				Hostname:       "dc-nats.dc-system",
				Port:           4222,
				StreamReplicas: 1,
			},
			Metrics: MetricsConfiguration{
				Enabled:  true,
				HttpPort: 9090,
			},
			UserManagement: UserManagementConfiguration{
				Hostname: "dc-user-management.dc-system",
				Port:     8080,
			},
		},
		Persistence: PersistenceConfiguration{
			Rdb: DatastoreConfiguration{
				Type: "postgres95",
				Configuration: map[string]interface{}{
					"hostname":       "dc-postgresql.dc-system",
					"port":           5432,
					"maxConnections": 5,
					"username":       "devicechain",
					"password":       "devicechain",
				},
			},
			Tsdb: DatastoreConfiguration{
				Type: "timescaledb",
				Configuration: map[string]interface{}{
					"hostname":       "dc-timescaledb-single.dc-system",
					"port":           5432,
					"maxConnections": 5,
					"username":       "postgres",
					"password":       "devicechain",
				},
			},
		},
	}
}
