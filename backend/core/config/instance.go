// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "fmt"

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

// Prometheus metrics configuration. Metrics are served on the GraphQL HTTP port
// (8080) at /metrics via the shared mux; there is no separate metrics listener,
// so the former HttpPort field was dead config and has been removed (E14).
// Enabled is informational — scrape discovery is gated by the Helm chart's
// metrics.enabled value (the ServiceMonitor), not by the running service.
type MetricsConfiguration struct {
	Enabled bool
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

// ApplyDefaults fills unset infrastructure fields with their defaults so an
// instance document that omits them is still well-formed (ADR-022 decision 1 /
// review E3). It is applied after decoding and before Validate.
func (c *InstanceConfiguration) ApplyDefaults() {
	if c.Infrastructure.Nats.StreamReplicas == 0 {
		c.Infrastructure.Nats.StreamReplicas = 1
	}
}

// Validate fails closed on an instance configuration missing the infrastructure
// a service cannot run without (ADR-022 decision 1 / review E3): the NATS
// backbone and the user-management endpoint every service validates tokens
// against. A misrendered config Secret then surfaces at startup rather than as a
// confusing downstream connection failure.
func (c *InstanceConfiguration) Validate() error {
	if c.Infrastructure.Nats.Hostname == "" || c.Infrastructure.Nats.Port == 0 {
		return fmt.Errorf("infrastructure.nats hostname and port are required")
	}
	if c.Infrastructure.UserManagement.Hostname == "" || c.Infrastructure.UserManagement.Port == 0 {
		return fmt.Errorf("infrastructure.userManagement hostname and port are required")
	}
	return nil
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
				Enabled: true,
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
