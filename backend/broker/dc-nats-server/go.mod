// The module path ends in /nats-server so that ko names the binary nats-server, which is what
// the nats chart's preStop hook runs (see main.go). The image is named after this directory.
module github.com/devicechain-io/dc-broker/nats-server

go 1.26.0

// The image is built from this file alone (GOWORK=off, see build_paths_test.go), so go.work's
// toolchain line does not reach it: this one does, and must equal it. It is what carries the
// standard library's security fixes into the broker.
toolchain go1.27.3

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/nats-io/jwt/v2 v2.8.2
	github.com/nats-io/nats-server/v2 v2.15.2
	github.com/nats-io/nats.go v1.55.0
	github.com/nats-io/nkeys v0.4.16
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.8.0-default-no-op // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)
