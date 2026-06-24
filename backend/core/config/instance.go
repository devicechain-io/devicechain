// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// Redis configuration parameters
type RedisConfiguration struct {
	Hostname string
	Port     int32
}

// Kafka configuration parameters
type KafkaConfiguration struct {
	Hostname                      string
	Port                          uint32
	DefaultTopicPartitions        uint32
	DefaultTopicReplicationFactor uint32
}

// Prometheus metrics configuration
type MetricsConfiguration struct {
	Enabled  bool
	HttpPort int32
}

// Keycloak connectivity configuration
type KeycloakConfiguration struct {
	Hostname string
	Port     uint32
}

// Infrastructure configuration section
type InfrastructureConfiguration struct {
	Redis    RedisConfiguration
	Kafka    KafkaConfiguration
	Metrics  MetricsConfiguration
	Keycloak KeycloakConfiguration
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
			Kafka: KafkaConfiguration{
				Hostname:                      "dc-kafka-kafka-bootstrap.dc-system",
				Port:                          9092,
				DefaultTopicPartitions:        4,
				DefaultTopicReplicationFactor: 1,
			},
			Metrics: MetricsConfiguration{
				Enabled:  true,
				HttpPort: 9090,
			},
			Keycloak: KeycloakConfiguration{
				Hostname: "dc-keycloak.dc-system",
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
