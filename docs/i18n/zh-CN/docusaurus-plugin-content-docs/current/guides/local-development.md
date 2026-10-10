---
sidebar_position: 1
title: 本地开发
---

# 本地开发 {#local-development}

在本地运行 DeviceChain 只需要两个依赖：NATS 和 TimescaleDB。无需 Java、Kafka、ZooKeeper、Redis、Keycloak 或 Mosquitto。

:::note 状态
DeviceChain 尚未正式发布。本指南介绍如何开发源代码：构建 Go 工作区，并让单个服务连接到自行启动的依赖。要运行完整实例，请使用 `dcctl` 和[快速入门](../quickstart/first-device.md)。`dcctl install` 和 `dcctl bootstrap` 两条命令即可启动所有组件。
:::

## 前提条件 {#prerequisites}

- **Go** 1.26 或更新版本。工作区声明 `go 1.26.6`，CI 使用 `go.work` 指定的版本构建。
- **Node** 22 或更新版本，用于前端和本文档。CI 使用 26 构建。
- **Docker**，用于运行 TimescaleDB。
- **nats-server**，约 10 MB 的单个二进制文件。

## 1. 启动基础设施 {#1-start-the-infrastructure}

在 Docker 中启动 TimescaleDB：

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17
```

NATS 需要启用 JetStream。要通过 MQTT 连接设备，还需启用消息代理内置的 MQTT 网关。MQTT **没有命令行开关**：必须使用配置块，并且依赖 JetStream。集群消息代理还必须设置 `server_name`。请编写一个小型配置文件，而不是传递参数：

```bash
cat > nats.conf <<'EOF'
server_name: dc-local
jetstream: enabled
http_port: 8222
mqtt { port: 1883 }
EOF

nats-server -c nats.conf
```

网关启动后，服务器会记录 `Listening for MQTT clients on mqtt://0.0.0.0:1883`。

如果只需要核心消息传递和 JetStream，`nats-server -js -m 8222` 即可。它不会启动 MQTT 监听器。

## 2. 构建工作区 {#2-build-the-workspace}

后端是一个 Go 工作区（`go.work`），涵盖核心库、Operator、CLI 和各个服务。构建以模块为单位。仓库根目录本身不是 Go 模块，因此从该目录执行 `./...` 模式不会匹配任何内容：

```
pattern ./...: directory prefix . does not contain modules listed in go.work or their selected dependencies
```

从仓库顶层执行 `go build ./...` 或 `go build ./backend/...` 都不可行。请像 CI 一样，从正在开发的模块内部构建：

```bash
cd backend/core     # ...or whichever module you touched
gofmt -l .          # must print nothing
GOWORK=off go mod tidy -diff   # must print nothing
go build ./...
go vet ./...
go test ./... -count=1
```

要检查整个工作区，让 `go.work` 枚举自己的模块，而不是手动列出：

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

这个循环中的四个细节很关键。缺少其中任何一项，检查都可能在没有检查到实际问题的情况下通过：

- **捕获 `gofmt -l` 的输出，而不只是运行它。** 即使列出了文件，它也会以状态 0 退出，因此循环用 `[ -z "$fmt" ]` 检查输出。如果只检查退出状态，这个检查关卡永远不会失败。
- **`-count=1` 不能省略。** 有些测试会读取所在模块之外的文件。Go 的测试缓存不会跟踪这些文件，因此即使修改本应使测试失败，缓存中的 PASS 仍可能继续有效。
- **单独检查每个模块的依赖文件是否整洁。** 工作区通过 `go.work` 构建所有模块，因此即使某个 `go.mod` 缺少依赖声明，或仍列有代码已不再使用的依赖，构建和测试也可能通过。`GOWORK=off go mod tidy -diff` 会将模块自身的文件与 `go mod tidy` 将产生的结果比较，并输出差异。当模块根本无法解析时（例如离线），它也会失败并输出原因，因此循环的消息同时提到这两种情况。CI 对每个模块运行同样的检查。出现差异时，在该模块中运行 `GOWORK=off go mod tidy` 并提交结果。
- **记录 `rc`，而不只是打印失败消息。** 如果只使用 `… || echo "FAILED: $m"`，循环的退出状态就会是最后一次 `echo` 的状态。所有模块都可能失败，但整个检查仍看似通过。

CI 还会使用 Go 的竞态检测器运行每个模块的测试。要在本地对修改过的模块运行同样的检查，请从仓库根目录执行：

```bash
hack/go-race.sh backend/services/event-processing   # or whichever module you touched
```

它会打印 `race: COVERED <module>`，然后在该模块中运行 `go test -race -count=1 -timeout 20m ./...`。检测器会让测试慢上数倍，因此依赖实际经过时间限制的测试，即使没有竞态也可能失败。应修正这类测试，使其时间限制不依赖二进制程序的运行速度。

**dcctl 的测试绝不会访问你的集群。** 无论当前上下文是什么，`backend/cli` 测试都会从空 kubeconfig 开始。凡是测试可能通过 kubeconfig、Helm 或 OpenTofu 获取集群的包，都有一个在任何测试之前运行的 `TestMain`。它将 `KUBECONFIG` 和 OpenTofu 根目录的 kubeconfig 路径指向空文件，并清除集群内环境变量、`KUBE_*` 和 `HELM_KUBE*` 变量。它还移除 Helm 在 kubeconfig 为空时使用的默认服务器，即 `http://localhost:8080` 或 `KUBERNETES_MASTER` 指定的地址。因此，在你的机器上运行时会与 CI 中表现一致，测试无法操作当前连接的集群。需要集群的测试会设置自己的 `KUBECONFIG`。`backend/cli` 中任何测试能够访问集群的新包，都需要相同的 `TestMain`。`backend/cli/internal/kubeisolation` 中的一项测试会通过导入图查找这类包，并在每个包都具备该入口之前保持失败。

**`backend/k8s` 和 `backend/cli` 的测试需要本地 API 服务器。** 有些测试会将真正的 `kube-apiserver` 和 `etcd` 作为本地进程启动（不涉及集群）：Operator 的验证规则只有 API 服务器才会执行，而 `backend/cli` 会针对 `dcctl` 渲染的每种配置档，将 Chart 安装到本地 API 服务器中，因为有些 Pod 规范规则无法仅靠渲染发现。缺少这些二进制文件时，测试会失败，而不是跳过。安装一次，并将测试指向它们：

```bash
cd backend/k8s && make envtest
export KUBEBUILDER_ASSETS="$(bin/setup-envtest use "$(sed -n 's/^ENVTEST_K8S_VERSION[[:space:]]*=[[:space:]]*//p' Makefile)" -p path)"
```

在 Windows 上，`backend/cli` 中启动 API 服务器的测试不会参与构建：仓库固定版本的启动测试库无法在 Windows 上编译。该包的其余测试仍可编译。`backend/k8s/controllers` 中的测试无法在 Windows 上构建。

### 模糊测试 {#fuzzing}

普通 `go test` 只运行每项模糊测试的种子输入。要进行模糊测试，请使用包装脚本。它会查找工作区内所有已提交的模糊测试（忽略未跟踪文件），并让每项运行固定时长：

```bash
hack/fuzz.sh                         # every fuzz test, 60 s each
FUZZTIME=300s hack/fuzz.sh           # longer
hack/fuzz.sh --list                  # what it would run
hack/fuzz.sh --target backend/core/graphql FuzzRootFieldLimit   # just one
```

它根据运行报告判断结果，而不只看退出状态。除 `PASS` 或 `TOLERATED` 外，任何结果都会使检查失败：

- `FINDING`：某个输入导致失败。Go 将其保存到包的 `testdata/fuzz/<Name>/` 下，包装脚本将其复制到日志目录。将该输入提交到那里，使其成为永久回归测试。
- `NOT-RUN`：没有进行模糊测试。`go test -fuzz` 的模式未匹配任何模糊测试时，也会以状态 0 退出，因此只检查退出状态会误判为通过。
- `SEED-FAIL`：某个已提交的种子输入在模糊测试开始前失败。
- `HANG`：运行未在时间预算内完成。`KILLED`：运行被直接终止，可能因为超时或系统内存耗尽。
- `TOLERATED`：运行完成了全部模糊测试时长且未发现问题，但随后 Go 的模糊测试引擎将 `context deadline exceeded` 报告为失败。这是 Go 工具链模糊测试协调器在时间预算结束时的已知竞态，并非发现的问题。只有完全相同的单行消息才会被接受，而且必须已达到完整运行时长；它仍会作为警告报告。
- `FAILED`：其他情况，例如模糊测试工作进程退出。完整日志会说明原因。

每次运行都有自己的临时目录，并保留完整日志。在 CI 之外，默认使用两个模糊测试工作进程，因为每个工作进程都是拥有独立内存的单独进程；可通过 `FUZZ_PARALLEL` 更改。相同的测试每天夜间在 `main` 上运行，日志作为工作流产物保留。

## 3. 运行服务 {#3-run-a-service}

每个服务都是不接受命令行参数的单个二进制程序。它不会在空环境中启动。启动时，它从环境变量读取身份，并从两个固定路径的文档读取设置。Helm Chart 将同样的两个文档挂载到每个 Pod。

- `DC_INSTANCE_ID` 和 `DC_MS_FUNCTIONAL_AREA` **必填**：分别指定服务所属实例和服务自身的功能域（下面的命令使用 `event-sources`）。缺少任意一项时，服务拒绝启动。
- `DC_LOG_CONSOLE=1` 将日志输出从 JSON 切换为便于阅读的控制台格式。它不会改变日志量。级别来自实例文档中的 `infrastructure.logging.level`；文档未设置时为 `info`（见[日志](../deployment/observability.md#logs)）。
- `/etc/dci-config/instance` 是实例范围的文档：包括 NATS 主机名和端口、数据库及持久化设置，以及其余共享基础设施。其结构由核心库 `config` 包中的 `InstanceConfiguration` 类型定义。在这里将服务指向步骤 1 中启动的 NATS 和 TimescaleDB。
- `/etc/dct-config/<functional-area>` 是每个服务的配置文档，其类型定义在该服务自己的 `config` 包中（此处为 event-sources 服务）。空文档有效，会应用类型定义的默认值。

两个文档都严格解码：未知键会被拒绝，而不是忽略。两个路径都是常量，无法通过参数或环境变量覆盖。要让服务连接到自己的基础设施，请在 `/etc` 下写入这两个文件，并导出变量：

```bash
export DC_INSTANCE_ID=dc-local DC_MS_FUNCTIONAL_AREA=event-sources DC_LOG_CONSOLE=1
go run ./backend/services/event-sources
```

`go run` 接受单个包的路径，因此可在该模块内部解析，并能从仓库根目录运行，这与上面的 `./...` 模式不同。

如果希望自动渲染文件而不是手动编写，请使用 `dcctl install` 和 `dcctl bootstrap`，它们会生成完整实例（参见本页顶部的说明）。

## 仓库布局 {#repository-layout}

```
backend/
  core/                 shared library (lifecycle, NATS, GORM, GraphQL, config, auth, secrets)
  k8s/                  operator + CRD types
  services/             one module per microservice — user-management, device-management,
                        event-sources, event-management, device-state, command-delivery,
                        dashboard-management, notification-management, event-processing,
                        outbound-connectors, ai-inference, mcp, update-management, and the
                        edge ingest areas
  edge/                 the edge agent
  sims/                 the device simulator
  cli/                  dcctl
  tools/                maintainer-only tools (not shipped)
frontend/               npm workspace: the console and dashboard apps plus the shared packages
docs/                   this documentation site
deploy/                 Helm chart + OpenTofu modules
sdks/                   client SDKs
```

## 后续步骤 {#next-steps}

- [连接设备](./connecting-a-device.md)
- [架构](../concepts/architecture.md)
