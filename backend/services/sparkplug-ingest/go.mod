module github.com/devicechain-io/dc-sparkplug-ingest

go 1.26.0

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/prometheus/client_golang v1.24.0
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.11.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.0 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/devicechain-io/dc-microservice => ../../core

replace github.com/devicechain-io/dc-k8s => ../../k8s

replace github.com/graph-gophers/graphql-go => github.com/devicechain-io/graphql-go v1.10.2-dc.2
