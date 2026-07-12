module github.com/devicechain-io/dc-mcp

go 1.26.0

replace github.com/devicechain-io/dc-k8s => ../../k8s

replace github.com/devicechain-io/dc-microservice => ../../core

require (
	github.com/devicechain-io/dc-microservice v0.0.0-00010101000000-000000000000
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/prometheus/client_golang v1.23.2
	github.com/rs/zerolog v1.35.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)
