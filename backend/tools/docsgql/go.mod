module github.com/devicechain-io/dc-docsgql

go 1.26.5

replace github.com/devicechain-io/dc-microservice => ../../core

// The checker compiles graphql-go and asks its OWN validator whether each docs example is
// a valid document.

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/graph-gophers/graphql-go v1.10.3
	go.yaml.in/yaml/v3 v3.0.5
)
