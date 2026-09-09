module github.com/devicechain-io/dc-promqlguard

// 🔴 go.sum HERE CARRIES MORE `/go.mod` HASHES THAN A FROM-SCRATCH `go mod tidy` WRITES,
// AND THAT IS DELIBERATE. This module is the first in the workspace that has a deep
// dependency (prometheus/prometheus, for its PromQL parser) and does NOT resolve
// graph-gophers/graphql-go. The `graphql-go fork guard` in CI asks every module
// `GOWORK=off go list -m github.com/graph-gophers/graphql-go` and treats "not a known
// dependency" as nothing to check — but PROVING a module absent forces the FULL module
// graph, not the pruned one a build needs, so the query wants the go.mod hash of every
// module prometheus/prometheus requires. Tidy does not record those, so with a
// minimally-tidied go.sum the guard cannot resolve the query at all and fails the module
// with a message about the fork that has nothing to do with the fork.
//
// `go mod tidy` KEEPS these once they are present — it is additive over an existing
// go.sum — so running it here is a no-op and this does not rot. If go.sum is ever deleted
// and regenerated, or prometheus/prometheus is bumped, restore them with:
//
//	GOWORK=off GOFLAGS=-mod=mod go list -m github.com/graph-gophers/graphql-go
//
// which prints the "not a known dependency" the guard expects and writes what it needed
// to say so.
go 1.26.6

require (
	github.com/prometheus/prometheus v0.314.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2 v1.46.0 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.3 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.49.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dennwc/varint v1.0.0 // indirect
	github.com/grafana/regexp v0.0.0-20250905093917-f7b3be9d1853 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	k8s.io/client-go v0.37.0 // indirect
)
