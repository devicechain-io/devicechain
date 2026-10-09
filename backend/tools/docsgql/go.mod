module github.com/devicechain-io/dc-docsgql

go 1.26.5

replace github.com/devicechain-io/dc-microservice => ../../core

// The checker compiles graphql-go and asks its OWN validator whether each docs example is
// a valid document. Upstream is not a safe stand-in (it accepts input-object fields the
// schema does not define when they arrive by variable), so the pinned fork is load-bearing.
// The CI fork guard enforces this per module with GOWORK=off.
replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/graph-gophers/graphql-go v1.10.2
)
