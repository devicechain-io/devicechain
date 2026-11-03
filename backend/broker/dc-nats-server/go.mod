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
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

// The image runs DeviceChain's fork of nats-server, not upstream. Upstream's flushOutbound
// writes one TLS record and makes one syscall per queued buffer, so a connection that
// flushes many small buffers (an MQTT fan-in over TLS) pays a record header, an AEAD seal
// and a write per buffer. The fork coalesces small buffers into records of up to 16 KiB.
// It is cut from the v2.15.2 TAG, not from upstream main, so the broker runs no unreleased
// upstream change it did not choose; branch dc-release-v2.15.2 carries exactly one commit
// on top of the tag. Only the -dc.N tags carry the change: the fork inherits upstream's
// tags, so v2.15.2 on it is unpatched, which is why the CI `nats-server fork guard` step
// matches the exact version. Drop this replace (and that step) once upstream ships an
// equivalent and bump to that release. See "Forked dependencies" in CLAUDE.md.
replace github.com/nats-io/nats-server/v2 => github.com/devicechain-io/nats-server/v2 v2.15.2-dc.1
