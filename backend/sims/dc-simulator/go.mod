module github.com/devicechain-io/dc-simulator

go 1.26.0

require (
	github.com/devicechain-io/dc-event-sources v0.0.1
	github.com/devicechain-io/dc-microservice v0.0.1
	github.com/rs/zerolog v1.35.1
)

require github.com/mattn/go-colorable v0.1.14 // indirect

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/eclipse/paho.mqtt.golang v1.5.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/devicechain-io/dc-microservice => ../../core

replace github.com/devicechain-io/dc-event-sources => ../../services/event-sources
