module github.com/devicechain-io/dc-event-processing

go 1.26.0

require (
	github.com/devicechain-io/dc-device-management v0.0.1
	github.com/devicechain-io/dc-event-sources v0.0.0-00010101000000-000000000000
	github.com/devicechain-io/dc-microservice v0.0.1
	github.com/glebarez/sqlite v1.11.0
	github.com/go-gormigrate/gormigrate/v2 v2.1.6
	github.com/google/cel-go v0.29.2
	github.com/graph-gophers/graphql-go v1.10.2
	github.com/prometheus/client_golang v1.23.2
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.11.1
	gorm.io/gorm v1.31.2
)

require (
	cel.dev/expr v0.25.1 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/friendsofgo/graphiql v0.2.2 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
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
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.52.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20251219203646-944ab1f22d93 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260406210006-6f92a3bedf2d // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gorm.io/datatypes v1.2.7 // indirect
	gorm.io/driver/mysql v1.5.6 // indirect
	gorm.io/driver/postgres v1.6.0 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)

replace github.com/devicechain-io/dc-device-management => ../device-management

replace github.com/devicechain-io/dc-event-sources => ../event-sources

replace github.com/devicechain-io/dc-k8s => ../../k8s

replace github.com/devicechain-io/dc-microservice => ../../core

replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2
