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
)

require golang.org/x/sync v0.22.0 // indirect
