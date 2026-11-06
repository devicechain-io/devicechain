module github.com/devicechain-io/dc-docsgql

go 1.26.5

replace github.com/devicechain-io/dc-microservice => ../../core

// The checker compiles graphql-go and asks its OWN validator whether each docs example is
// a valid document.

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/graph-gophers/graphql-go v1.10.3
	go.yaml.in/yaml/v3 v3.0.6
)

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/google/cel-go v0.30.0 // indirect
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
