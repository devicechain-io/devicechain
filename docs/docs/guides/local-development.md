---
sidebar_position: 1
title: Local Development
---

# Local Development

You can run DeviceChain locally with only two dependencies: NATS and TimescaleDB. You do not need
Java, Kafka, ZooKeeper, Redis, Keycloak or Mosquitto.

:::note Status
DeviceChain is pre-release. This guide covers working on the source tree: building the Go workspace
and running a single service against dependencies you started yourself. For a complete running
instance, use `dcctl` and the [Quickstart](../quickstart/first-device.md) instead. Two commands,
`dcctl install` and `dcctl bootstrap`, stand up everything.
:::

## Prerequisites

- **Go** 1.26 or newer. The workspace declares `go 1.26.6`, and CI builds with the version `go.work` names.
- **Node** 22 or newer, for the frontend and these docs. CI builds on 26.
- **Docker**, to run TimescaleDB.
- **nats-server**, a single binary of about 10 MB.

## 1. Start the infrastructure

Start TimescaleDB in Docker:

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17
```

NATS needs JetStream. To connect a device over MQTT, it also needs the broker's built-in MQTT
gateway. MQTT has **no command-line switch**: it is a configuration block, and it requires
JetStream. A clustered broker must also set `server_name`. Write a small config file instead of
passing flags:

```bash
cat > nats.conf <<'EOF'
server_name: dc-local
jetstream: enabled
http_port: 8222
mqtt { port: 1883 }
EOF

nats-server -c nats.conf
```

When the gateway is up, the server logs `Listening for MQTT clients on mqtt://0.0.0.0:1883`.

If you only need core messaging and JetStream, `nats-server -js -m 8222` is enough. It starts no
MQTT listener.

## 2. Build the workspace

The backend is a Go workspace (`go.work`) spanning the core library, the operator, the CLI and the
services. Builds are module-scoped. The repository root is not itself a Go module, so a `./...`
pattern anchored there matches nothing:

```
pattern ./...: directory prefix . does not contain modules listed in go.work or their selected dependencies
```

Neither `go build ./...` nor `go build ./backend/...` works from the top of the tree. Build from
inside the module you are working on, as CI does:

```bash
cd backend/core     # ...or whichever module you touched
gofmt -l .          # must print nothing
GOWORK=off go mod tidy -diff   # must print nothing
go build ./...
go vet ./...
go test ./... -count=1
```

To sweep the whole workspace, let `go.work` enumerate its own modules instead of listing them by
hand:

```bash
rc=0
for m in $(go list -m -f '{{.Dir}}'); do
  ( cd "$m" || exit 1
    fmt="$(gofmt -l .)"; [ -z "$fmt" ] || { echo "not gofmt-clean:"; echo "$fmt"; exit 1; }
    GOWORK=off go mod tidy -diff || { echo "not tidy, or tidiness could not be checked (see above)"; exit 1; }
    go build ./... && go vet ./... && go test ./... -count=1
  ) || { echo "FAILED: $m"; rc=1; }
done
echo "sweep exit status: $rc"
```

Four details in that loop matter. Without each one, a check would pass without looking at
anything:

- **`gofmt -l` is captured, not only run.** It exits 0 *even when it names files*, so the loop tests
  its output with `[ -z "$fmt" ]`. Checking its exit status instead would give a gate that can never
  fail.
- **`-count=1` is not optional.** A few tests read files outside their own module. Go's test cache
  does not track those files, so a cached PASS can survive a change that ought to fail it.
- **Each module is checked for tidiness on its own.** The workspace builds every module through
  `go.work`, so a `go.mod` that is missing a requirement, or still lists one the code no longer
  uses, builds and tests green. `GOWORK=off go mod tidy -diff` compares the module's own files with
  what `go mod tidy` would leave and prints the difference. It also fails, printing the reason, when
  it cannot resolve the module at all (offline, for example), which is why the loop's message names
  both. CI runs the same check for every module. When it prints a diff, run
  `GOWORK=off go mod tidy` in that module and commit the result.
- **`rc` is recorded, not only printed.** With `… || echo "FAILED: $m"` alone, the loop's exit
  status would be that of the last `echo`. Every module could fail and the sweep would still look
  green.

CI also runs every module's tests under Go's race detector. To run the same check locally on the
module you changed, from the repository root:

```bash
hack/go-race.sh backend/services/event-processing   # or whichever module you touched
```

It prints `race: COVERED <module>` and then runs `go test -race -count=1 ./...` in that module. The
detector slows tests down several times over, so a test that asserts a wall-clock budget can fail
under it without any race. Fix such a test so that its budget does not depend on how fast the
binary runs.

**dcctl's tests never reach your cluster.** The `backend/cli` tests start from an empty kubeconfig,
whatever your current context is. Every package whose tests could pick up a cluster from the
kubeconfig, from Helm or from OpenTofu has a `TestMain` that runs before any test. It points
`KUBECONFIG` and the OpenTofu roots' kubeconfig path at an empty file, and it clears the in-cluster,
`KUBE_*` and `HELM_KUBE*` variables. It also removes the default server Helm falls back to over an
empty kubeconfig, `http://localhost:8080` or whatever `KUBERNETES_MASTER` names. A run on your
machine then behaves as it does in CI, and no test can act on the cluster you are connected to. A
test that needs a cluster sets its own `KUBECONFIG`. A new package in `backend/cli` whose tests can
reach a cluster needs the same `TestMain`. A test in `backend/cli/internal/kubeisolation` finds such
packages from the import graph and fails until each has one.

### Fuzzing

Plain `go test` runs each fuzz test's seed inputs only. To fuzz, use the wrapper. It finds every
fuzz test committed to the workspace (untracked files are ignored) and runs each one for a fixed time:

```bash
hack/fuzz.sh                         # every fuzz test, 60 s each
FUZZTIME=300s hack/fuzz.sh           # longer
hack/fuzz.sh --list                  # what it would run
hack/fuzz.sh --target backend/core/graphql FuzzRootFieldLimit   # just one
```

It judges each run by what the run reported, not only by its exit status. Anything other than
`PASS` or `TOLERATED` fails:

- `FINDING`: an input failed. Go saves it under the package's `testdata/fuzz/<Name>/`, and the
  wrapper copies it into its log directory. Commit it there to make it a permanent regression test.
- `NOT-RUN`: no fuzzing happened. `go test -fuzz` exits 0 when its pattern matches no fuzz test, so
  an exit status alone would call this a pass.
- `SEED-FAIL`: one of the committed seed inputs failed before fuzzing started.
- `HANG`: the run did not finish within its time budget. `KILLED`: it was killed outright, either
  by the budget or by the system running out of memory.
- `TOLERATED`: the run fuzzed for its whole time and found nothing, but Go's fuzzing engine then
  reported `context deadline exceeded` as a failure. That is a known race in the Go toolchain's
  fuzz coordinator at the end of the time budget, not a finding. Only that exact, single line is
  accepted, and only when the run reached its full time; it is still reported as a warning.
- `FAILED`: anything else, such as a fuzzing worker that died. The full log says why.

Each run gets its own temporary directory, and its whole log is kept. Outside CI it uses two
fuzzing workers, because each worker is a separate process with its own memory; set
`FUZZ_PARALLEL` to change that. The same runs happen every night on `main`, and their logs are kept
as a workflow artifact.

## 3. Run a service

Each service is a single binary that takes no flags. It does not start on an empty environment. At
startup it reads its identity from environment variables and its settings from two documents at
fixed paths. The Helm chart mounts the same two documents on every pod.

- `DC_INSTANCE_ID` and `DC_MS_FUNCTIONAL_AREA` are **required**: the instance the service
  belongs to, and the service's own area (`event-sources` for the command below). The service
  refuses to start when either is missing.
- `DC_LOG_CONSOLE=1` switches the log output from JSON to a human-readable console format. It
  does not change how much is logged. The level comes from `infrastructure.logging.level` in the
  instance document, and is `info` when the document does not set it (see
  [Logs](../deployment/observability.md#logs)).
- `/etc/dci-config/instance` is the instance-wide document: NATS hostname and port, the database
  and persistence settings, and the rest of the shared infrastructure. Its shape is the
  `InstanceConfiguration` type in the core library's `config` package. This is where you point the
  service at the NATS and TimescaleDB you started in step 1.
- `/etc/dct-config/<functional-area>` is the per-service document, typed in that service's own
  `config` package (the event-sources service's, here). An empty document is valid and applies the
  typed defaults.

Both documents are decoded strictly: an unknown key is refused, not ignored. Both paths are
constants with no flag or environment override. To run a service against your own infrastructure,
write those two files under `/etc` and export the variables:

```bash
export DC_INSTANCE_ID=dc-local DC_MS_FUNCTIONAL_AREA=event-sources DC_LOG_CONSOLE=1
go run ./backend/services/event-sources
```

`go run` takes a path to one package, so it resolves inside that module and works from the
repository root, unlike the `./...` patterns above.

To have the files rendered for you instead of writing them by hand, use `dcctl install` and
`dcctl bootstrap`, which produce a complete instance (see the note at the top of this page).

## Repository layout

```
backend/
  core/                 shared library (lifecycle, NATS, GORM, GraphQL, config, auth, secrets)
  k8s/                  operator + CRD types
  services/             one module per microservice — user-management, device-management,
                        event-sources, event-management, device-state, command-delivery,
                        dashboard-management, notification-management, event-processing,
                        outbound-connectors, ai-inference, mcp, and the edge ingest areas
  edge/                 the edge agent
  sims/                 the device simulator
  cli/                  dcctl
  tools/                maintainer-only tools (not shipped)
frontend/               npm workspace: the console and dashboard apps plus the shared packages
docs/                   this documentation site
deploy/                 Helm chart + OpenTofu modules
sdks/                   client SDKs
```

## Next steps

- [Connecting a Device](./connecting-a-device.md)
- [Architecture](../concepts/architecture.md)
