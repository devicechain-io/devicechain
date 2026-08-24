module github.com/devicechain-io/dc-migrationdiff

go 1.26.6

require (
	github.com/devicechain-io/dc-ai-inference v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-command-delivery v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-dashboard-management v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-device-management v0.0.1
	github.com/devicechain-io/dc-device-state v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-event-management v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-event-processing v0.0.1
	github.com/devicechain-io/dc-microservice v0.0.1
	github.com/devicechain-io/dc-notification-management v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-outbound-connectors v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-user-management v0.0.0-00010101000000-000000000000
	github.com/go-gormigrate/gormigrate/v2 v2.1.6
	github.com/stretchr/testify v1.12.1
	gorm.io/driver/postgres v1.6.2
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/devicechain-io/dc-event-sources v0.0.0-00010101000000-000000000000 // indirect
	github.com/eclipse/paho.mqtt.golang v1.5.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.53.1 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gorm.io/datatypes v1.2.7 // indirect
	gorm.io/driver/mysql v1.5.6 // indirect
)

replace github.com/devicechain-io/dc-microservice => ../../core

replace github.com/devicechain-io/dc-ai-inference => ../../services/ai-inference

replace github.com/devicechain-io/dc-command-delivery => ../../services/command-delivery

replace github.com/devicechain-io/dc-dashboard-management => ../../services/dashboard-management

replace github.com/devicechain-io/dc-device-management => ../../services/device-management

replace github.com/devicechain-io/dc-device-state => ../../services/device-state

replace github.com/devicechain-io/dc-event-management => ../../services/event-management

replace github.com/devicechain-io/dc-event-processing => ../../services/event-processing

replace github.com/devicechain-io/dc-event-sources => ../../services/event-sources

replace github.com/devicechain-io/dc-k8s => ../../k8s

replace github.com/devicechain-io/dc-notification-management => ../../services/notification-management

replace github.com/devicechain-io/dc-outbound-connectors => ../../services/outbound-connectors

replace github.com/devicechain-io/dc-user-management => ../../services/user-management

replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2
