module github.com/devicechain-io/dc-apiprobe

go 1.26.5

replace github.com/devicechain-io/dc-microservice => ../../core

// No longer defensive: documents_test.go validates every document this tool sends
// with the library's OWN validator, so apiprobe now compiles graphql-go. That makes
// the pinned fork load-bearing rather than precautionary — upstream accepts
// input-object fields the schema does not define when they arrive by variable, which
// is precisely the class of defect these tests exist to reject. On the unpatched
// library they would validate a bad document and pass.
// The CI fork guard enforces this per module with GOWORK=off.
replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/graph-gophers/graphql-go v1.10.2
	gorm.io/driver/postgres v1.6.2
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-gormigrate/gormigrate/v2 v2.1.6 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gorm.io/datatypes v1.2.7 // indirect
	gorm.io/driver/mysql v1.5.6 // indirect
)
