---
sidebar_position: 3
title: 部署与 Operator
---

# 部署与 Operator {#deployment--operator}

DeviceChain 分两层部署，每层都由一个 `dcctl` 命令就位：

- **`dcctl install`** 准备**集群**。它安装所有实例共享的前提组件，以及 Kubernetes **Operator**（基于 controller-runtime 构建）和资源定义。它不处理任何特定实例。
- **`dcctl bootstrap`** 创建**实例**。它写入集群范围的 **`Instance`** 资源，声明即将构建的内容，再由 **Helm Chart** 渲染该实例的工作负载。

Operator 监视 `Instance` 资源。租户不在其职责内：租户是控制平面数据库记录（参见[自定义资源](#custom-resources)）。

:::note 状态
已可用：Helm Chart 渲染各服务工作负载和配置，`dcctl bootstrap` 与 `dcctl upgrade` 驱动实例生命周期。Operator 观察 `Instance` 资源，目前不对其中内容执行任何操作。它的另一项工作针对使用 [`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups) 安装的集群：每十分钟删除已超出恢复窗口的数据库卷快照基础备份，CloudNativePG 不会执行这项清理。实例状态聚合和按环境划分的 Kustomize 覆盖层已规划，但尚未开始。
:::

状态聚合将加入 Operator 的监视循环。在实现之前，`Instance` 不报告状态，因此应读取工作负载本身或使用 `dcctl` 判断实例是否健康。

## 使用 Helm 部署 {#deploying-with-helm}

`deploy/helm/devicechain` 中的 Chart 渲染：

- 每个已启用功能域对应一个 Deployment 和 Service；
- 各服务配置 ConfigMap；
- 实例配置 Secret。它携带持久化凭据，因此使用 Secret，而不是 ConfigMap。

每个 Pod 都暴露 `/healthz`（存活检查）和 `/readyz`（就绪检查），因此未就绪的服务不会参与流量分配。

可以通过具名配置档或显式服务集合选择运行哪些服务：

| 配置档 | 功能域 |
|---|---|
| `default` | user-management、device-management、event-sources、event-management、device-state、dashboard-management、command-delivery、notification-management、event-processing——标准系统，也是未设置配置档时的选择 |
| `full` | 此构建发布的全部功能：`default` 加 `ai-inference`、`outbound-connectors`、`mcp`、`sparkplug-ingest` 和 `lwm2m-ingest`；这些功能不包含在 `default` 中，因为每一项都需要明确作出决定（付费提供商密钥、出站访问接口、面向代理的 API，以及绑定独立入站端口的 Sparkplug B 或 LwM2M 设备传输） |
| `telemetry` | user-management、device-management、event-sources、event-management、device-state、dashboard-management |
| `ingest-only` | user-management、device-management、event-sources |

### 秘密存储根密钥 {#root-key}

每种配置档都需要实例的**秘密存储根密钥**。所有配置档都运行的 `user-management` 会用它封装登录令牌签名密钥。存储集成凭据的功能域也用它封装凭据。缺少时，Chart 会使渲染失败，而不是让 `user-management` 陷入崩溃重启循环。

生成一个值（`openssl rand -base64 32`），妥善保存，并在每次安装和升级时传入**同一个**值。新密钥会使旧密钥保护的已有秘密不可读，并阻止所有人登录。`dcctl bootstrap` 会代你生成并托管保管此密钥；只有直接操作 Chart 时才需自行提供。

```bash
DC_ROOT_KEY="$(openssl rand -base64 32)"   # generate ONCE, then keep it

helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"

# Run a smaller set of services. Every profile needs the root key, the smallest
# ones included.
helm install dc deploy/helm/devicechain --set profile=telemetry \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"
```

### 安装已发布版本 {#released-version}

要安装正式发布的版本，将镜像标签固定为版本号。发布镜像在 `ghcr.io/devicechain-io` 公开提供，因此无需本地构建。用实际发布的标签替换 `<version>`；[发布页面](https://github.com/devicechain-io/devicechain/releases)列出了它们。

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

[发布与升级](./releases-and-upgrades.md)介绍版本模型和升级流程。升级方法取决于实例的创建方式：

- **通过引导创建的实例**需要两条命令。`dcctl install` 升级属于集群的 Operator；`dcctl upgrade` 升级属于实例的消息代理、事件存储、配置文档及发布版本。Operator 不包含在 Chart 中，因此必须由 Chart 外的工具升级。
- **只由 Chart 管理的实例**使用 `helm upgrade`，并[手动沿用配置值](./releases-and-upgrades.md#chart-only-upgrade)。

### 服务选择规则 {#selection-rules}

`user-management` 和 `device-management` 是必需核心。`event-management`、`device-state` 和 `command-delivery` 各自可选。如果选择中遗漏必需核心服务，或遗漏已启用服务的硬依赖，Chart 会**使渲染失败**，因此安装时就能发现无效拓扑，而不是等到 Pod 崩溃重启。应用时根据 Chart 的 `values.schema.json` 验证配置值。

## 自定义资源 {#custom-resources}

`dcctl install` 定义两种集群范围的自定义资源：

- **`Instance`**（`instances.core.devicechain.io`，短名 `dci`）：每次安装一个，声明实例身份和配置。
- **`InstanceConfiguration`**（`instanceconfigurations.core.devicechain.io`，短名 `dcic`）：可保存实例配置文档的资源。定义会被安装，但 `dcctl bootstrap` 不会创建该资源，Operator 也不监视它。每个 Pod 从挂载的实例配置 Secret 读取实例配置。

[`dcctl install`](./bootstrap.md#install) 将两种定义和控制器一起安装。它们在各方面都是集群范围的：每个集群一份，由其上的所有实例共享，版本跟随集群，而不是某个实例。因此，准备集群的命令也是升级它们的命令。

`dcctl bootstrap` 和 `dcctl upgrade` 只读取定义。集群没有定义，或定义能够识别为来自不同发布版本时，它们会拒绝操作，并指出应运行的安装命令。手动安装的定义没有安装版本记录，因此允许通过，但会提示同一个命令，而不是拒绝。

租户**不是**自定义资源。它们是通过实例管理员 API 和 `/admin` 控制台创建的控制平面数据库记录，共享实例服务（参见[多租户](../concepts/multi-tenancy.md)）。

```bash
kubectl get instances      # platform
```

## 实例声明 {#instance-declaration}

`dcctl bootstrap` 为即将构建的实例写入 `Instance` 对象。随后读回对象，并依据读回内容工作，而不是依据生成对象的参数。这让集群，而不是输入命令的机器，成为实例定义的记录来源：

- 所属提供商和集群；
- 运行的配置档和镜像版本；
- 数据库是否从归档恢复；
- 最后写入它的 `dcctl` 构建版本；
- 上次运行试图执行的操作。

因此，另一台机器上的运维人员无需任何本地状态就能读取实例：

```bash
kubectl --context <kube-context> get instances
kubectl --context <kube-context> get instance <id> -o yaml
```

部分规范写入后**不可更改**。集群绑定最重要，因为改写它会使 `dcctl destroy` 指向另一个集群。两层都会拒绝这种编辑：

- CRD 通过验证表达式携带规则，因此 API 服务器会拒绝用 `kubectl` 进行的手动编辑。
- `dcctl` 写入前，会将同样的字段与集群内已有声明比较。

### 读取 PHASE 列 {#phase}

`kubectl get instances` 打印 **PHASE** 列，其值来自声明的 `core.devicechain.io/phase` 注解。`dcctl instances list` 读取同一注解，并在 STATUS 列中用文字显示。该命令仅在引导实例的机器上工作，依据该机器的本地记录。

:::warning 阶段表示意图，而不是健康状态
它记录**上次 `dcctl` 运行试图执行的操作**。写入它的流程没有检查任何 Pod，因此 `dcctl instances list` 从不报告 `running`。要知道工作负载是否运行，请直接查看：`kubectl get pods -n dci-<id>`。
:::

| PHASE | `dcctl instances list` STATUS | 含义 |
|---|---|---|
| `Bootstrapping` | `bootstrap started, not finished` | `dcctl bootstrap` 已写入声明，但尚未写入最终阶段。另一个终端中正在正常执行的引导也显示此状态。 |
| `Upgrading` | `upgrade started, not finished` | 相同含义，对应 `dcctl upgrade`。 |
| `Ready` | `declared ready` | 上次引导或升级已完成，不说明当前 Pod 状态。 |
| `Failed` | `last run failed` | 上次引导或升级返回错误。 |
| `Destroying` | ``PART-WAY DESTROYED — re-run `dcctl destroy` `` | `dcctl destroy` 已开始但未完成。它在删除任何内容**之前**写入，因此销毁在之后任何时点被终止都会在此可见。 |
| *（空白）* | `declared, phase not recorded` | 声明由该注解出现之前的 `dcctl` 写入。 |

处理 `Bootstrapping` 或 `Upgrading` 之前，先检查运行是否仍在进行。正常运行结束时会将阶段重写为 `Ready` 或 `Failed`，包括用 Ctrl+C 中断的情况。只有没能写入结束状态的运行才留下这些值，例如机器断电或终端被终止。二者都不会阻止任何操作：`dcctl bootstrap` 和 `dcctl upgrade` 可以直接在其上运行。命令会采取行动的唯一阶段值是 `Destroying`（见[下文](#finalizer)）。

除了阶段之外，还有一个注解会影响行为。`core.devicechain.io/bootstrap-unfinished: "true"` 标记首次引导尚未成功结束的实例。只要该注解仍存在且阶段不是 `Ready`，即使配置文档存在，`dcctl bootstrap` 也会继续运行以完成实例。成功结束的引导或升级会在记录 `Ready` 的同一次写入中移除它。不要手动添加：对于正在运行且阶段不是 `Ready` 的实例，它会允许引导再次执行并覆盖该实例。

显示 `declared, unknown phase "…"` 的行，由比当前列出它的版本更新的 `dcctl` 写入。

`dcctl instances list` 在处理阶段之前还会检查几项情况。每种都有自己的状态文字，而不会显示为健康：

| STATUS | 情况 |
|---|---|
| `PART-WAY DESTROYED` | 来自本机的销毁标记，即使无法连接集群也会显示 |
| `cluster gone — stale local state` | 集群已不存在 |
| `no record — destroy will guess the cluster` | 实例在开始记录集群之前引导创建 |
| `cluster present, no declaration` | 集群存在，但没有声明 |
| `declaration marked for deletion` | 见[下文](#finalizer) |
| `could not check: …` | 探测失败或超时 |

### `kubectl delete instance` 不会完成 {#finalizer}

声明携带 **finalizer**。手动删除会保留对象并将其标记为删除，直到 `dcctl` 清除 finalizer：

```bash
kubectl delete instance prod    # does not return; the object stays, now terminating
```

这是刻意的，保护两个不同方面：

- **实例。** 手动删除声明会使仍在运行的实例成为孤儿：命名空间、数据库、卷和工作负载都还在运行，但集群中没有记录说明它们属于什么。
- **集群绑定。** 不可变规则通过比较对象的新旧版本工作。重新创建的对象没有旧版本可比较，因此先删除再重新应用，会通过两个单独看都合法的步骤重新指向集群绑定。

**`dcctl destroy` 自行清除 finalizer**，作为其在集群中的最后一步：在命名空间消失之后、本地状态移除之前。一般无需手动操作。`destroy` 从不删除集群本身，因此由这一步移除声明。

销毁失败时会刻意留下声明，除非仅在移除本地状态时失败，而当时声明已经消失。保留的声明：

- 仍记录实例所在集群，供重新运行使用；
- 显示 `Destroying` 而不是 `Ready`，让下一个读取者知道拆除正在进行。

:::note 引导和升级会拒绝销毁了一半的实例
对这样的声明运行 `dcctl bootstrap` 或 `dcctl upgrade` 都会拒绝，而不会在半个旧实例上构建半个新实例。拒绝信息会要求用可恢复执行的 `dcctl destroy <id>` 完成拆除；或者，如果确定实例已无任何残留，可用不会销毁任何内容的 `dcctl instances release <id>` 删除声明。
:::

### 移除声明而不销毁任何内容 {#release}

```bash
dcctl instances release <id> --kube-context <kube-context>
```

这会清除 finalizer 并删除声明。**它不销毁任何内容。** 命名空间、数据库、卷和工作负载之后仍在运行，而 `dcctl` 不再有记录说明它们属于什么。因此命令要求你明确确认这正是想要的结果。它之所以存在，是因为负责移除 finalizer 的工具消失时，否则集群会留下无人能够删除的对象。

命令会打印即将执行的操作，并要求再次输入实例名称；脚本使用时可用 `--yes` 跳过提示。任何未引导该实例的机器都需要 `--kube-context`，因为它告诉 `dcctl` 哪个集群保存声明。

要移除**实例**，请运行 `dcctl destroy`。

## 职责分离 {#separation-of-concerns}

DeviceChain 刻意将每层交给一个工具：

| 层 | 工具 | 职责 |
|---|---|---|
| 基础设施 | **OpenTofu** | NATS、TimescaleDB、命名空间、入口、TLS |
| 工作负载 | **Helm Chart** | Deployment、Service 和各功能域配置 ConfigMap |
| 生命周期 | **Operator** | 监视 `Instance` 资源，目前不对其中内容采取行动（已规划状态聚合）；按恢复窗口清理卷快照基础备份 |
| 实例身份与配置 | **`dcctl`** | 写入 `Instance` 声明及工作负载挂载的实例配置 Secret |
| 业务配置 | 实例管理员 API / `/admin` 控制台，以及每个租户自己的 API / 控制台 | 租户及各租户自己的设置（例如品牌） |

各层在不同时间运行：

- **OpenTofu** 在安装集群时运行（`dcctl install`，配置所有实例共享的前提组件），也在引导实例时运行（配置该实例自己的消息代理和事件存储）。
- **`dcctl install`** 还会应用 Operator 及其定义，使用嵌入 CLI 的清单，而不通过其他两层。
- **Chart** 渲染工作负载。
- **Operator** 监视 `Instance` 资源（目前如何处理它，参见本页顶部的状态说明）。

集群引导从不放在应用或 Operator 代码中，而是基础设施层的工作。OpenTofu 模块位于 [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu)。它们为数据库层配置保留保护，使数据库能在应用拆除后继续存在（参见[发布与升级](./releases-and-upgrades.md#data-durability)）。
