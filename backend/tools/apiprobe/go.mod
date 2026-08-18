module github.com/devicechain-io/dc-apiprobe

go 1.26.5

replace github.com/devicechain-io/dc-microservice => ../../core

// Defensive, per the repo convention: apiprobe resolves graphql-go transitively
// through core and compiles none of it today. Without this, the day it does
// import the library it would silently get the UNPATCHED upstream, which accepts
// input-object fields the schema does not define when they arrive by variable.
// The CI fork guard enforces this per module with GOWORK=off.
replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2

require github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000

require golang.org/x/sync v0.22.0 // indirect
