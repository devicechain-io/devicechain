module github.com/devicechain-io/dc-sparkplug-ingest

go 1.26.0

require (
	github.com/stretchr/testify v1.11.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/devicechain-io/dc-microservice => ../../core

replace github.com/devicechain-io/dc-k8s => ../../k8s

replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2
