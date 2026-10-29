---
sidebar_position: 4
title: 发布与升级
---

# 发布与升级 {#releases--upgrades}

DeviceChain 以预构建、带版本的容器镜像及 Helm chart 发布。
运行时**不需要**构建任何内容：拉取已发布版本、安装 chart，即可通过滚动更新原地升级。

:::warning 某些版本无法直接升级

- **由 `v0.16.0` 或更早版本构建的实例**无法升级到之后的版本，因为它没有记录实例声明。参见[从较早版本升级](#pre-declaration-recreate)。
- **`v0.19.0`：事件存储包含超过 4,000,000 条尚未压缩的行**，或某张表超过 500 个 chunk、且无法移除最旧事件的实例。首次启动会重建这些行上的事件键，超过边界就拒绝。从 `v0.17.x` 或 `v0.18.x` 升级时，请先阅读[该版本的说明](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-upgrade)。

如果属于其中任何一种情况，执行其他操作前，先阅读对应章节。
:::

:::caution 请经由 v0.23.0 升级
`v0.23.0` 之后的版本以 `v0.22.0` 及更早版本无法读取的形式写入事件。从 `v0.22.0` 或更早版本升级时，请先将每个实例升级到 `v0.23.0`，再升级到之后的版本。参见[事件在代理上的编码格式](#event-wire-format)。
:::

## 版本模型 {#versioning-model}

每次发布对应一个语义版本 Git 标签（`vX.Y.Z`）。
同一版本覆盖**全部组件**：各服务镜像、Operator、Helm chart 和 `dcctl` CLI 都以相同版本发布。
无需考虑逐服务版本不一致：一次部署只有一个统一版本号。

两个命令移动全部组件，分工遵循组件的**生命周期**。
Operator 是每集群一个控制器，由集群上的所有实例共用，因此 `dcctl install` 随集群其他前置组件一起更新它。
配置文档、chart release 和服务镜像属于实例，由 `dcctl upgrade` 逐实例更新。
步骤参见[滚动升级](#rolling-upgrades)。

- **稳定版本**使用 `vX.Y.Z`，例如 `v1.2.0`。`:latest` 标签跟随最新稳定版。
- **预发布版本**使用 `vX.Y.Z-rc.N`，例如 `v1.2.0-rc.1`，不会移动 `:latest`。

## 1.0 之前的稳定性 {#pre-10-stability}

:::warning DeviceChain 尚未达到 1.0

在 **v1.0.0** 之前，任何发布，包括补丁发布，都可能改变 API、Schema 或行为，不提供兼容层。
这是有意的：数据模型仍在稳定过程中，我们更倾向于干净切换，而非永久维护兼容层。

**每项破坏性变更都会列在对应发布说明最前面。升级前务必阅读。**
它们是权威清单；仅凭版本号无法判断该发布对你的部署是否安全。

:::

具体而言，v1.0.0 之前的发布可能：

- **收紧验证**，让以前成功的请求被拒绝，通常因为它过去被无声接受或丢弃；
- **改变或删除 GraphQL 字段**，不经过一轮弃用；
- **修改数据库 Schema**，降级也无法撤销；
- **直接替换迁移基线**，完全移除升级路径，而非仅让它单向。
  发生时发布说明会在开头明确指出，唯一前进方式是重建实例。`v0.9.0` 和 `v0.10.0` 就属于这种发布；
- **不再能从较旧实例升级**，原因甚至与 Schema 无关。
  `v0.16.0` 后的版本读取旧版从未记录的实例声明，缺失时拒绝，而不是猜测；
- **超过数据规模边界时拒绝原地升级**：`v0.19.0` 拒绝超过 4,000,000 条尚未压缩行的事件存储，
  或超过 500 个 chunk 的表，并在说明开头指出。

上面的“通过滚动更新原地升级”描述滚动升级的*机制*，不是承诺 1.0 之前变更版本后，现有 API 调用仍具有相同含义。

从 v1.0.0 起，本节替换为语义版本兼容承诺：只有主版本可以包含破坏性变更。参见[版本与升级策略](./versioning-policy.md)。

GA 之前发布频繁，因此**次版本**表示里程碑，例如重要功能或子系统落地；
**补丁版本**承载持续修复和加固。此阶段补丁升级不自动等于低风险，仍以发布说明为准。

## 镜像 {#images}

镜像发布到公开 GitHub Container Registry 的 `ghcr.io/devicechain-io` 下，
例如 `ghcr.io/devicechain-io/device-management`。
它们支持多架构（`linux/amd64` 和 `linux/arm64`），基于 distroless nonroot 镜像构建，
以非特权用户运行，无 shell，攻击面最小化。

注册表公开，因此拉取已发布镜像无需凭据。

## 安装指定版本 {#installing-a-specific-version}

将镜像标签固定为所需发布：

下面的 `DC_ROOT_KEY` 是实例密钥存储的根密钥，每种 profile 都必需。
用 `openssl rand -base64 32` 生成一次，在每次安装和升级中原样传入。
原因参见[使用 Helm 部署](./kubernetes-operator.md#deploying-with-helm)。

将 `<version>` 替换为真实发布标签，[发布页面](https://github.com/devicechain-io/devicechain/releases)提供清单。
未发布值会在拉取时失败，而非安装时失败。

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

Helm chart 本身也以 OCI 制品发布，因此无需检出仓库即可安装。
chart 版本是去掉前导 `v` 的发布版本：`--version 0.16.0` 安装 `v0.16.0`。
无需查找另一套版本号；`helm show chart oci://ghcr.io/devicechain-io/charts/devicechain` 显示最新版本，
`--version` 会拒绝从未发布的版本：

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version <chart-version> \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

chart 也列在 [Artifact Hub](https://artifacthub.io/packages/helm/devicechain/devicechain)，
展示每个发布版本、默认值和渲染后的模板。

### 升级仅用 chart 安装的实例 {#chart-only-upgrade}

通过 `helm install` 而非 `dcctl bootstrap` 安装的实例，使用 `helm upgrade` 升级，
且仍有一个 `dcctl` 路径没有的陷阱。

`dcctl upgrade` 不适用：它从集群读回实例声明和配置文档，而仅用 chart 的安装没有这两者。

下方 release 名为 `dc`，因为上面的 `helm install` 选择此名。
通过 `dcctl bootstrap` 安装的实例，其 release 按实例命名：`devicechain` 安装为 `dc-devicechain`。
针对这些实例执行任何 `helm` 命令时，应使用该名称。

```bash
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=<new-version>

rm dc-values.yaml   # this file holds your instance's secrets
```

:::warning 保留原值：只使用 `--set image.tag=…` 不会成功
陷阱在 Helm 的规则。完全**不传**值的升级会复用 release 已有值。
但只要传入*任何*值，包括升级必需的单个镜像版本 `--set`，Helm 就从 chart 默认值开始，
安装时设置的所有内容都消失。包括实例根密钥，没有它无法读取运行中实例的已存储密钥。

发生时不会损坏数据，因为 chart 缺少根密钥时拒绝渲染：

```
Error: UPGRADE FAILED: execution error at (devicechain/templates/instance-config.yaml:21:4): instance.config.infrastructure.secrets.rootKey is required: every instance seals its token-signing key under it, along with any integration credentials it stores, and user-management cannot start without it. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically.
```

`--reuse-values` 也可用，但 chart 默认值跨版本改变时，它会无声保留旧条目。
因此更建议导出值，并通过 `-f` 传入，让你能够看到它们。
:::

:::caution 实例配置来自你管理的 Secret 时
通过 `instance.existingSecret` 挂载非 chart 写入的 Secret，即 External Secrets 和 sealed-secrets 常用形态，
现在必须满足四项条件。任何一项不满足，`helm upgrade` **在渲染时失败**，不会继续。
release 中不会发生任何变化；拒绝就是全部效果。

- **Secret 必须命名为 `dci-<instance.id>-config`**，位于实例命名空间。
  其他名称会被拒绝，因为 `dcctl` 按这个固定名称读回配置，判断实例是否存在；
  缺少 Secret 会被视为新安装，重跑会在运行中的实例上生成新的根密钥、数据库和代理凭据。
  如果 Secret 使用其他名称，升级前以此名称重新创建，例如 External Secrets 的 `target.name` 或 sealed secret 的 `metadata.name`。
- **必须提供 `instance.existingSecretChecksum`**：Secret `instance` 键下文档的 SHA256，64 个小写十六进制字符。
  chart 无法读取你的 Secret，因此它用来在配置变化时滚动 Pod；没有它，凭据轮换会应用成功却不重启任何组件。
  文档每次改变都应重新计算：

  ```bash
  kubectl get secret dci-<instance.id>-config -n dci-<instance.id> \
    -o jsonpath='{.data.instance}' | base64 -d | sha256sum | cut -d' ' -f1
  ```

- **`networkPolicy.enabled` 开启时必须提供 `networkPolicy.externalConfigPorts`**，
  用 `nats` 和 `rdb` 键重述文档中的代理和数据库端口。
  否则 chart 从自己的默认值读取，端口不一致会无声阻断服务自身出站流量，表现为代理或数据库中断。
- **`metrics.natsPodMonitor` 开启时必须提供 `metrics.natsBrokerHost`**，即文档中的代理主机名。
  代理位于实例命名空间时使用短 Service 名，位于其他位置时使用 `<service>.<namespace>`。
  它决定 PodMonitor 监视哪个命名空间；不一致的默认值会导致什么也不监视。
  也可设置 `metrics.natsPodMonitor=false`。

每个 `helm` 错误都会指出所需值和原因。
chart 正常写文档时的两项转换，即 `infrastructure.shutdown` 块，以及未部署 AI 功能区时移除
`infrastructure.aiInference`，仍像以前一样由你自行复现。
:::

<a id="zero-downtime-upgrades"></a>

## 滚动升级 {#rolling-upgrades}

升级是**两个命令**：先升级集群，再升级其上每个实例。
chart 和服务设计为逐个 Pod 滚动前进，因此多副本的功能区在滚动期间持续提供服务。下文记录五种例外：
持久接入切换仍是普通升级，但有可见副作用；**`v0.9.0`、`v0.10.0` 和任何由
`v0.16.0` 或更早版本构建的实例完全无法直接升级**；
以及 **`v0.19.0` 中事件存储超过 4,000,000 条尚未压缩行**，
或某张表超过 500 个 chunk 且无法移除最旧事件的情况，参见[检查行数](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-row-count)。
运行前检查目标版本的发布说明：

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

:::note 已测量的内容
升级曾在本地集群上手动演练：一个以 `--ha` 安装的实例在 MQTT 流量持续运行时被原地升级（四台设备以 QoS 1 发布，合计每秒 20 条消息，`infrastructure.nats.mqttFailClosed` 保持默认的关闭状态），其代理滚动两次，设备在每次滚动后重新连接。在一次演练中，代理已确认的每条消息之后仍被保存（11,009 条中的 11,009 条）。在另一次演练中，11,262 条已确认消息中丢失了 11 条，全部发生在某台代理服务器重启、MQTT 捕获流更换主节点期间。`mqttFailClosed` 关闭时，无论流量大小，每次代理滚动都应预期出现一个类似的短暂丢失窗口；见 [MQTT：平台代理](../reference/transport-matrix.md#mqtt--the-platform-broker)。这些演练是手动进行的。作为发布关卡的自动升级检查在没有流量的情况下运行。

尚未在生产规模的集群上、持续生产负载下测量过升级，因此 DeviceChain 不承诺不中断的升级。请预期在替换 Pod 的位置出现短暂中断：只有一个副本的功能区，以及每台代理服务器。未使用 `--ha` 时，唯一的代理服务器会重启，期间代理不可用。在断开连接期间发送数据的设备必须重试。
:::

一次发布中的服务镜像、chart、Operator 和 `dcctl` 使用同一版本。
两个命令按组件归属分工：

**`dcctl install` 更新集群。**Operator 的命名空间、CRD、RBAC 和控制器，
从 `dcctl` 内嵌的清单应用。它不属于 Helm chart，chart 内的内容无法更新它。
会应用整个渲染后的清单流，而非只改控制器镜像，因为里面包含 CRD：
若 Schema 停留在实例初次 bootstrap 的版本，会无声丢弃后续发布新增的字段。
此命令也更新集群其他共享前置组件，参见[安装集群](./bootstrap.md#install)。

**`dcctl upgrade` 更新一个实例**，不修改任何共享内容：

1. **实例基础设施**：消息代理 NATS 和事件存储，按本发布提供的 OpenTofu 配置应用，
   确保改变这些内容的发布也到达已有实例，而非只影响新实例。
   参见[升级会应用哪些基础设施](#upgrade-infrastructure)；
2. **配置文档**：各服务从中读取凭据和端点，由本版本 chart 重新组合，并由拥有它的 `dcctl` 写入；
3. **Helm release**：运行服务，滚动到新镜像，并等待每个功能区完成。

**顺序很重要，升级会检查。**实例声明由 Operator 定义，
因此更新实例前，Operator 必须先到新版本。
`dcctl upgrade` 读取集群的 Operator，**拒绝**没有 Operator，或 Operator 明确属于其他发布的实例，
并指出需要先运行的 install 命令。它不会自行应用 Operator：
多实例集群中，升级一个实例时这样做会悄悄连带更新其他实例的控制器。

有一种情况只警告，不拒绝。`dcctl install` 会记录安装定义的发布；
**手动**安装的 Operator 没有该记录，`dcctl` 无法区分有意手装与被旧 `dcctl` 覆盖。
它不推翻无法观察的选择，而是打印所需 install 命令后继续。
如果你并非手动安装 Operator，应把提示视为原本会发生的拒绝，继续前先运行 `dcctl install`。

需要查看会改什么时，可以先给任一命令加 `--dry-run`。
`dcctl upgrade` 从实例记录取得目标集群，不作猜测，并明确输出该集群。

:::warning 先升级平台，再升级边缘代理
使用[网关上行](./edge-services.md#gateway-identity)的边缘代理在网关命名空间中发布。如果平台的 `event-sources` 不认识该命名空间，并且开启了 `infrastructure.nats.mqttFailClosed`，它会在消息代理处确认这些消息然后丢弃，而代理这一侧看不出任何迹象。请先完成集群和每个实例的升级（`event-sources` 与 `device-management` 的每个副本，以及消息代理的授权），再重启任何使用该上行的版本的代理。消息代理尚未升级的代理无法订阅其通知，也不会报告其上行已连接。
:::

:::tip 读取所有凭据，不生成任何凭据
`dcctl upgrade` 保留实例当前使用的数据库所有者密码、代理 authority 和登录凭据、跨服务密钥、密钥存储根密钥，
以及在集群部署它们时，监控仪表盘管理员密码和集群内备份存储凭据。
版本变化不能变成凭据变化。

这经过验证，而非只是声明：对运行中实例升级前后，比较所有这些凭据的摘要，
唯一变化是每个服务、控制台和 Operator 的镜像标签。
:::

:::warning 升级不能用于轮换凭据
因为设计上保留凭据，它不会轮换任何凭据。
需要改变凭据时，升级不会替你完成；其中几种目前还没有受支持的轮换流程。
:::

:::note 更新版本，不改变实例形态
profile、拓扑和启用的功能区来自实例自身声明，即 `dcctl bootstrap` 在集群记录的内容，而非这里输入的标志。
改变实例*是什么*是另一个问题，答案也不同：例如提高副本数不会重新复制按旧副本数创建的消息流。要为运行中的实例添加功能领域，使用 [`dcctl enable-area`](./bootstrap.md#enable-area)，它会将该领域记录在声明中。

此命令也有意不做一件事：除让服务执行自身迁移外，不接触数据库数据，而且绝不调整卷大小。
:::

### 运维人员的调优会被保留 {#upgrade-tuning}

升级不会改变实例的形态，但会保留你为它设置的调优。传给 `dcctl bootstrap --values` 或 `dcctl upgrade --values` 的 values 文件会记录在实例上，之后每次升级都会再次应用；`--reset-values` 可将其移除。参见[调优实例](./tuning-an-instance.md)。

MQTT 故障关闭确认也以同样方式保留：`dcctl bootstrap` 或 `dcctl upgrade` 上的 `--mqtt-fail-closed` 会记录在实例上，省略该参数的升级会保留已记录的值，无论开启还是关闭。参见 [fail-closed MQTT](./observability.md#mqtt-fail-closed)。

### 已配置的凭据会被保留 {#upgrade-provisioned-credentials}

你通过文件交给 `dcctl` 的凭据，即 [LwM2M 预共享密钥](./edge-services.md#lwm2m-identities)和 [Sparkplug 代理密码](./edge-services.md#sparkplug-sources)，保存在 `dcctl` 自己写入、位于 Helm 发布之外的 Secret 中。升级不会触碰它们，也不会从发布中读取任何机密内容。早期版本配置了 LwM2M 密钥的实例，其第一次升级会在发布变更之前把密钥迁移到专属 Secret；Helm 历史记录中更早的版本仍保存着旧的值，因此如果这对你有影响，请在升级后轮换密钥。

### 升级会应用哪些基础设施 {#upgrade-infrastructure}

更新服务前，`dcctl upgrade` 应用实例 OpenTofu 配置，即 `dcctl bootstrap` 构建实例时使用的配置。
其中包含实例 NATS 服务器和事件存储：数据库镜像、设置、放置位置，以及备份和归档配置。
升级先计划并打印变化；`--dry-run` 打印计划后停止。
与 bootstrap 一样，需要 `PATH` 中有 `tofu`。
准备配置时会向 provider 注册表或你的镜像索取 provider；dry run 也会在用于计划的本地工作目录中这样做。

它保留实例当前运行配置：如上保留每项凭据，JetStream 卷和事件存储卷保持原大小。
新版本为新实例使用不同大小时，升级会说明；自行扩容参见[事件存储卷](./bootstrap.md#event-store-volume)。
事件存储保留归档路径；恢复过的存储也保留其恢复来源归档。

以下情况会拒绝，且不修改任何内容：

- **本机没有实例状态。**配置状态位于初次 bootstrap 机器的 `~/.devicechain/instances/<instance>/`。
  在该机器升级，或先复制此目录。里面含凭据，应保持私有，权限为 `0700`。
- **计划会删除或替换配置管理的内容**，例如代理或事件存储。
  拒绝会指出对象。对应发布说明会解释如何迁移。
  检查针对配置管理的资源；删除资源内部对象会表现为该资源的变更。
- **计划会缩短事件存储恢复窗口，或不再声明某个分析读取用户**，而升级读取的内容没有声明这些设置。
  这些值是手动设置的，直接应用会清理备份，或让读取用户密码失去管理。
  按下文声明后重新升级。
- **计划会关闭事件存储备份。**任何声明都无法绕过：`dcctl` 根据集群安装方式决定备份是否开启。
  `--skip-infrastructure` 只更新服务，保留事件存储原样。
- **开始前代理或事件存储不健康**，例如服务器未就绪，或存储正在故障转移。先恢复它。
  唯一例外是代理上次滚动未完成，部分服务器已用新设置，而其中一个未就绪。
  只有 apply 能改变正在滚动的目标设置，因此升级会覆盖应用，在计划中说明，并等待新设置完成滚动。
  若所有服务器已运行相同设置但少了一个，例如丢失节点，仍拒绝。
- **计划同时改变事件存储镜像和设置**，且存储不止一个实例；数据库 Operator 拒绝这种操作。
  拒绝会说明应按什么顺序应用。

`--skip-infrastructure` 仅更新服务，与本发布之前的升级行为相同；升级结束时会说明保留了什么。

**你自行设置的配置值。**与 bootstrap 一样，升级使用 `dcctl` 的值应用配置。
声明在实例状态旁 `~/.devicechain/instances/<instance>/infra/instance/` 中的
`terraform.tfvars` 或 `*.auto.tfvars`，或 `TF_VAR_<name>` 环境变量中的值会保留：
每次 `dcctl` apply 时 OpenTofu 都读取它们。手动通过 `-var` 传入的值不会保留。
运维人员通过此方式设置的值包括 `backup_retention_tsdb`、`timescale_analytics_readers`
（参见 [SQL 和 BI 访问](../guides/sql-and-bi-access.md)），以及代理的
`nats_cpu_request`、`nats_memory_request`、`nats_memory_limit`。
升级若降低代理请求或限制，会在计划中打印警告。

这只适用于 `dcctl` 自身不传入的变量，因为命令行 `-var` 会覆盖这些文件和 `TF_VAR_`。
每次 apply，`dcctl` 都会传入：

- **使用 `--compact` 安装的实例**：代理的 `nats_cpu_request`、`nats_memory_request` 和卷大小。
  在 `terraform.tfvars` 设置这些请求对 compact 实例无效。`nats_memory_limit` 不在此列。
- **根据集群安装方式**：数据库备份是否开启（`enable_database_backups`）、快照类
  （`backup_snapshot_class`）和数据库节点放置。通过 `dcctl install` 修改，而非此文件。

升级警告和拒绝会说明某值属于哪种情况。

运行中实例会看到：

- **NATS 服务器设置变化时重启。**`--ha` 下逐个重启，每个 Ready 后下一个才停止。
  没有 `--ha` 时单服务器重启，期间代理不可用，通常约一分钟。
  服务和设备自行重连，期间发布被拒绝。
  节点无法容纳服务器新请求时，会停在 `Pending`：`--ha` 下其他两个仍服务，
  非 HA 下代理一直**停机**，直到有空间；升级最多 15 分钟后失败并说明原因。
- **设备认证回调改为加密，其密钥也随之移动。**过去设备密码会以明文经过代理，任何监听的服务都能读到。
  `dcctl upgrade` 会创建名为 `dci-<实例>-callout` 的 Secret，保存回调的签名种子和新的加密种子，
  只挂载到 `device-management`；实例配置只保留它们的公钥部分。首次执行此变更的升级分两步应用代理，
  以便设备持续连接：先把服务滚动到既能读取加密请求、也能读取未加密请求的响应方，然后才重新应用代理并开启加密。
  第二步是 NATS 服务器的又一次滚动重启。连接在被重启服务器上的设备会自行重连，`--ha` 下在数秒内完成；
  没有 `--ha` 时代理照常不可用约一分钟。若第二步失败，设备仍可连接，再次运行 `dcctl upgrade` 即可完成。
- **所有人需要重新登录一次，登录状态迁入独立账户。**刷新令牌、OAuth 授权码、单点登录记录和登录限流计数，
  过去都放在所有服务共用的 NATS 账户里，任何服务凭据都能读取它们，或把它们复制到自己的流中。
  `dcctl upgrade` 会创建一个新的登录凭据，以及名为 `dci-<实例>-identity` 的 Secret，保存其密码，
  只挂载到 `user-management`。代理新增一个独立的 `IDENTITY` 账户，只有该凭据能进入，
  与另一个账户不共享任何内容。旧的存储桶会被**删除，而不是复制**：其中的令牌曾可被所有服务读取，
  迁移只会让这种暴露继续存在。升级前的所有会话都会结束，人们需要在控制台和仪表板重新登录，
  MCP 和 OAuth 客户端需要重新授权。已签发的访问令牌在到期前仍然有效，默认 15 分钟。
  在 `user-management` 滚动更新期间，到达仍运行旧版本 Pod 的登录或令牌刷新可能在几秒内报错，重试即可成功。
  不会有任何静默丢失：`user-management` 首次启动时会记录它删除的存储桶，`dcctl upgrade` 结束时也会说明。
  代理为添加该账户重启一次，`--ha` 下逐个服务器重启。如果实例声明该账户存在而密码缺失，
  `user-management` 会拒绝启动，而不是退回共用账户。
- **每个功能区获得自己的消息代理登录凭据。**此前所有服务都用同一个共用登录凭据连接消息代理，因此任何 Pod 都可以出示、任何一次配置泄露都会暴露所有服务的凭据。
  `dcctl upgrade` 会为每个使用消息代理的功能区各创建一个登录凭据，并为代理管理再创建一个，各自保存在名为 `dci-<实例>-nats-<功能区>` 的 Secret 中，只挂载到该功能区的 Pod。
  `mcp` 和 `ai-inference` 从不打开代理连接，因此不挂载任何一个。每个登录凭据只被限定在其功能区所使用的范围内：它写入的流、读取时使用的消费者（名称精确指定，因此一个功能区无法改写、领取或确认另一个功能区的消息）、它打开的键值存储桶，以及它自己的答复主题。登录凭据不再能向其他功能区的流发布消息、写入其他功能区的缓存，或订阅其他功能区的答复。这些授权由服务所受检查的同一份声明生成，因此不会落后于服务的实际行为。共用凭据在本次升级中保留在新凭据旁边，权限与以往相同，因此尚未重启的服务仍能连接；在下一次升级之前，它仍在每个 Pod 读取的配置中，因此 Pod 仍可出示它，这还不是针对被攻破服务的边界。再运行一次 `dcctl upgrade` 即可停用它：当每个功能区都已使用自己的登录凭据、且上一次升级已经完成时，该次升级会把共用凭据从代理和实例配置中移除，代理再重启一次，结束时的摘要会说明执行了哪个阶段（或为什么没有停用）。新实例从不拥有共用凭据。停用之后不支持回滚到更早的版本：请重新运行较新版本的 `dcctl upgrade`。另有两项授权有意更宽：event-sources 保留对代理 MQTT 存储的更新权限，user-management 可以在所有流中清除已删除租户的数据，但 event-sources 用来保证已确认消息持久性的那些消费者除外。不使用 dcctl 运行 Chart 并自带消息代理的安装，可以用 `dcctl broker permissions` 打印同样的授权。
  代理为添加这些用户重启一次，`--ha` 下逐个服务器重启。如果实例声明这些凭据存在而某个功能区缺少自己的那一个，该功能区会拒绝启动，而不是退回共用凭据。
  已有的凭据永不替换：升级只创建缺失的。
- **流和存储桶现在由一个管理登录凭据在服务滚动之前创建。**流的配置决定了消息以哪个主题存储，因此让每个服务自行创建和修改它所使用的流，意味着其中任何一个都可以改造某个流来捕获或伪造消息。Chart 现在会在每次安装和升级之前运行一个作业 `<实例>-broker-admin`（Helm 的 pre-install 和 pre-upgrade 钩子）。它使用任何 Deployment 都不挂载的代理管理登录凭据，做的是过去每个服务在启动时各自做的事：创建缺失的对象，把已有对象的主题、去重窗口、丢弃策略、大小上限和副本数恢复为声明的值；对于旧版本以不同保留策略创建的那一个捕获流，则等待它的读取方把积压读完，再按声明重新创建。这一等待可能需要几分钟。服务不再创建或修改任何东西：它检查自己打开的每个流和存储桶是否与其版本的声明一致，缺失或不一致时拒绝启动，并指明是哪个流、哪里不同。如果作业失败，升级会在任何服务滚动之前失败，`dcctl upgrade` 会打印作业的日志；上一版本的服务继续运行。`dcctl broker reconcile <实例>` 会再次运行同一个作业，这也是恢复被手工删除的流或存储桶的办法。功能区登录凭据不再有创建、更新或删除流和存储桶的权利，但有两个有意保留的例外：event-sources 保留对代理自带 MQTT 存储的权限，user-management 保留删除旧的登录状态存储桶副本的权利。event-processing 对 resolved-events 流的重放（重启后的追赶、实时缺口填补和规则预览）现在按序列号读取该流，不会在代理上创建任何消费者，因此没有任何功能区对消息流上其他功能区的消费者拥有权利；管理作业会为此打开该流的直接读取。随之有两处行为变化。缓存存储桶的存活时间固定为五分钟，因为现在由作业创建它们，不再向每个服务询问；device-management 的缓存存活时间设置只限制副本在自己内存中保留条目的时间。在实例配置中调高的副本数会在作业运行时（即下一次升级）生效，而不是在每次 Pod 重启时。上一版本的 Pod 如果在代理更新之后、作业运行之前重启，在尝试重新配置某个流时会被拒绝；向前升级后即可启动。不使用 dcctl 运行 Chart 的安装需要在第一次安装前准备好命名空间和管理登录凭据的 Secret，因为作业在 Chart 创建任何对象之前运行；它可以用 `brokerAdmin.enabled=false` 关闭该作业，并用 `dc-broker-admin` 镜像自行创建流和存储桶。
- **事件存储 Pod 变化时重启**，例如新镜像或放置位置。仅设置变化无需重启，直接重新加载。
  `--ha` 下先重启备用实例，再把主角色切换过去。
  没有 `--ha` 时唯一实例重启，回来前 event-management 无法存储事件。
  事件在接入流中等待并重试，只有失败重试约四分钟后才放弃。
- **两者可能同时重启。**它们一起应用，升级等待两者健康后才更新服务。

改变事件存储镜像的发布，现在通过这一步到达已有实例：它们会重启到新镜像。
对应发布说明会列出其他要求。

#### 代理的领导权会被重新均衡 {#upgrade-broker-leadership}

滚动重启代理服务器后，最后重启的那台不领导任何流或消费者，其余服务器领导全部；在实测负载下，这使发布确认延迟增加了约一倍。服务滚动完成后，触及过代理的升级会请求每个负载过高的流和消费者的领导者把领导权交给一个已同步的副本，并打印每台服务器在前后各领导多少。移动逐个进行，每次都等新的领导者同步后再进行下一个。已经向设备确认过的设备数据所在的流被刻意排除在重新均衡之外：代理自带的 MQTT 流、设备采集流，以及摄入服务写入的所有流。这些流上的领导者变更曾丢失已告知设备“安全”的消息，任何延迟收益都不值得，也没有包含它们的选项；它们的不均衡会在下一次代理滚动时或自行消除。该步骤有时间上限且尽力而为：无法完成时只给出警告，升级仍然成功。`dcctl broker rebalance <instance>` 可按需运行同一步骤，用于以其他方式重启过的代理。

### 升级还检查什么 {#upgrade-checks}

升级同时检查两件事，因为版本升级可靠地发生在运行实例上，而日历并不保证执行检查。

**代理证书。**消息代理使用有效期一年的证书，由 `dcctl` 在 bootstrap 时创建并保存在集群的 CA 签发。
升级在最后 30 天内，或证书不再覆盖代理互连所用全部名称时重新签发。
后者会发生在实例扩容时，即使证书离过期还很远。
签发使用**同一** CA，无需重新建立信任；并重启代理，确保真正提供新证书，而非一直持有旧证书等待无关滚动。
条件不满足时，检查运行但不修改。

在 `dcctl` 开始保存 CA 之前 bootstrap 的实例无法原地重新签发。
升级会说明并继续，不会失败；重建实例才能生成新 CA 和证书。

**根密钥托管。**每次升级都检查实例托管制品是否仍保护实际运行的根密钥。
检查**无需口令**：制品记录被保护密钥的指纹，与运行密钥比较不会解开任何内容。

| 检查结果 | 行为 |
|---|---|
| 制品保护运行中的密钥 | 说明后保持原样 |
| 制品保护**不同**密钥 | 明确警告。它通常属于同名旧实例；从它恢复会得到无法读取自身密钥的集群 |
| 没有制品 | 传入 `--escrow-passphrase-file` 或设置 `DCCTL_ESCROW_PASSPHRASE` 时创建一个，否则警告根密钥唯一副本位于集群内 |

这让最初用 `--no-escrow` 创建的实例以后取得托管。
这些结果都不会让升级失败：托管问题涉及未来灾难，而当前升级涉及运行实例；
无法升级的运维人员往往会绕过检查，而非修复问题。

:::note 实例部分过去是 `helm upgrade`
过去流程是用 `helm get values` 把当前 release 的值写到文件，
通过 `-f` 与新镜像标签一起传回，因包含密钥而删除文件，然后单独更新 Operator。

这套步骤仅因实例生成凭据保存在 Helm release 中；一旦传入任意值，Helm 就从 chart 默认值开始，
不手动带上凭据的升级会失去它们。
现在 `dcctl` 拥有配置文档，release 不再保存这些凭据，把密钥写入文件的步骤因此消失。

旧形式留下的缺口是升级停在 `helm` 部分，新服务长期运行在实例最初 bootstrap 的控制器上，且没有错误提示。
上面的拒绝补上此缺口。两个命令仍是两个，因为 Operator 属于集群，release 属于实例；
但 `dcctl upgrade` 现在会读取并说明集群使用的 Operator，能区分版本时拒绝，无法区分时警告。
:::

保证滚动安全的机制：

- **先增加，再终止。**Deployment 默认使用 `RollingUpdate`，`maxUnavailable: 0`、`maxSurge: 1`。
  新 Pod 必须**先**通过 `/readyz` 就绪探针，旧 Pod 才移除，滚动期间容量不下降。
  四个功能区改用 `strategy: Recreate` 和单副本，因为同一时间只能一个 Pod 服务：
  `event-processing`（规则引擎单写入者）、`mcp`（客户端会话位于建立它的 Pod）、
  `sparkplug-ingest`（每 Pod 一个 Sparkplug Host）和 `lwm2m-ingest`（每 Pod 一个 CoAP/UDP socket）。
  它们先停止所有旧 Pod，再启动新 Pod，滚动有意存在短暂间隙；chart 拒绝 `Recreate` 配合多副本。
- **优雅关闭和连接排空。**Pod 被要求终止时，先报告“未就绪”，让 Service 不再路由新请求，
  等待短暂排空窗口使变化传播，再完成进行中的工作并关闭。
  用 `shutdownDrainSeconds` 配置窗口，默认 `5`；须安全地低于默认 `30` 的 `terminationGracePeriodSeconds`。
  两者共享一个预算，服务会检查：排空最多占宽限期的**一半**，因为窗口只负责等待，
  完成请求、排空代理消费者、关闭数据库池都在它之后发生，kubelet 到期无论是否完成都会发 SIGKILL。
  过大窗口会在服务启动时（以及 `dcctl bootstrap` 安装前）拒绝，而非等 Pod 已关闭时才发现。
  设置 `shutdownDrainSeconds: 0` 可跳过排空，适合没有 Service 需退出的单实例运行。
- **协调 Schema 迁移。**服务在数据库级锁下运行迁移，多副本同时启动时恰好一个执行迁移，
  其他等待，没有竞争和重复 DDL。

:::tip 生产环境至少运行两个副本
要在滚动期间持续提供服务，每个支持多 Pod 服务的功能区使用 `replicas: 2` 或更多，让滚动始终有存活 Pod 处理流量。
单副本替换时仍有短暂间隙。可全局设置 `--set replicas=2`，或逐区设置 `functionalAreas.<area>.replicas`。
上述四个单 Pod 功能区是例外：`mcp`、`sparkplug-ingest` 和 `lwm2m-ingest` 在任何策略下都拒绝多副本；
`event-processing` 只接受第二副本作为热备，并须同时设置 `strategy: RollingUpdate`，否则渲染失败并说明原因。
多副本功能区自动渲染 `PodDisruptionBudget`，使节点排空无法一次驱逐全部副本。

速率上限由各副本独立执行，所以两个 `event-sources`、`outbound-connectors` 或 `ai-inference` 副本，
最多可接受租户上限的两倍。参见[治理](../concepts/governance.md#per-replica)。
:::

### 事件在代理上的编码格式 {#event-wire-format}

设备发送的每个事件都会以消息形式两次经过代理，写入保留的流：一次是接收时的原样，一次是针对其设备解析之后。实例配置中的 `infrastructure.messaging.wireFormat` 决定这些消息的编码方式：

| 取值 | 编码 |
| --- | --- |
| `compact` | **自 `v0.24.0` 起为默认值。** 每个时间以纳秒计数表示，每个设备凭据的封印以二进制表示。每条消息约小三分之一，即每个事件占用更少的代理磁盘、网络和保留容量。 |
| `legacy` | 每个时间以文本表示，每个封印以文本形式表示，与 `v0.22.0` 及更早版本写入的字节完全相同。`v0.23.0` 默认写入此格式。 |

其他任何值（包括 `Compact`）都会被拒绝：服务不会启动，其日志会指出该键。它属于挂载的配置，与[日志级别](./observability.md#the-log-level)相同，设置方式也相同。

- **自 `v0.23.0` 起的每个版本都能读取两种格式。** 升级前以 `legacy` 形式写入的消息仍保留在流中，照常读取，包括对检测规则在升级前开始的时间窗口所做的回放预览。
- **请经由 `v0.23.0` 升级，不要从 `v0.22.0` 或更早版本直接升级。** `v0.22.0` 及更早版本只能读取 `legacy`：这些版本上的服务会丢弃 `compact` 消息，或将其作为失败消息搁置，因此从这些版本直接滚动升级到写入 `compact` 的版本，会丢失新旧 Pod 同时运行期间写入的事件。请先将每个实例升级到 `v0.23.0`，等所有 Pod 都运行该版本后，再升级到之后的版本。使用 chart 安装时，也可以在升级期间设置 `legacy`，待所有 Pod 都完成滚动后再移除。`dcctl` 没有设置它的选项。
- **回滚到 `v0.23.0`** 在编码格式方面无需任何操作：它能读取之后版本写入的 `compact` 消息。
- **回滚到 `v0.22.0` 或更早版本**前，需要先设置 `legacy`，并等消费者处理完流中所有 `compact` 消息。

## 从较早版本升级 {#pre-declaration-recreate}

<a id="v090-baseline-squash"></a>
<a id="v0100-event-key"></a>

由 **`v0.16.0` 或更早版本**初始化的实例无法升级到之后的版本。`dcctl upgrade` 会直接拒绝而不是尝试，拒绝不会改动任何内容：Helm release、Operator 和每一行数据都保持原样。更早的两个无法原地到达的版本同理：`v0.9.0`（用一个冻结基线取代每个服务的迁移链）和 `v0.10.0`（事件表换用新的主键）。旧到需要它们的实例，都低于这条界线。

`dcctl bootstrap` 会记录一份**声明**，这是集群范围的对象，说明实例是什么：它的 profile、拓扑、暴露方式和功能领域。`dcctl upgrade` 读取它来决定部署什么。直到 `v0.16.0` 的各版本都没有写入声明，所以无可读取，拒绝信息也会这样说明：

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

没有兼容层，`v1.0.0` 之前也不会有：事后编造的声明，等于在运行中的实例上套用猜测。如果之后的版本提高了这条界线，它的发布说明会写明。

**要升级到之后的版本，请重建实例及其所在的集群：**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

对于通过 `--kube-context` 访问、`dcctl` 从不删除的集群，也要把该 `--kube-context` 传给 `destroy`，用创建它的工具重建集群，并同样传给 `install` 和 `bootstrap`。`dcctl upgrade` 拒绝时会打印这套步骤。

`dcctl` 现在把本地状态保存在 `~/.devicechain/instances/<instance>/` 下。较早版本保存在 `~/.devicechain/<instance>/`，新的 `dcctl` 不会读取、列出或删除它，所以 `destroy` 会根据实例名推断集群，并且需要 `--without-state`。实例删除后，请自行删除旧目录（`rm -rf ~/.devicechain/<instance>`；其中以明文保存基础设施状态），并且不要动 `~/.devicechain/escrow/`：根密钥托管文件存放在那里，仍可打开该实例的数据库备份（参见[灾难恢复](./disaster-recovery.md#after-destroy)）。

:::caution 先导出：重建丢弃数据
[销毁保护](#data-durability)只保护普通 helm 操作，不保护有意 destroy。
有需要的遥测、设备定义或仪表盘，开始前导出。
:::

当你升级到记录声明的版本后，就恢复为普通的原地升级。

## 近期版本 {#recent-releases}

最近三个次版本的说明完整保留，按从新到旧排列。

### v0.26.0：设备自行注册，前端迁移到 Traefik {#v0260-upgrade}

`v0.26.0` 是从 `v0.25.2` 起的原地升级：先对集群运行 `dcctl install`，再对每个实例运行一次 `dcctl upgrade`，方法见[v0.17.0 的说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade)。请给 `dcctl install` 传入集群安装时使用的设置（如 `--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`），以及新的 `--version`。如果从 `v0.25.1` 或 `v0.25.0` 升级，请同时阅读中间各版本的说明。

```bash
dcctl install local --version v0.26.0
dcctl upgrade local devicechain --version v0.26.0 --dry-run
dcctl upgrade local devicechain --version v0.26.0
```

在承载两个 default 配置档实例的集群上，请给这条 `dcctl install` 加上 `--max-connections 700`。

该版本带来[设备自注册](../guides/device-self-registration.md)、面向 LoRaWAN 网络服务器和 webhook 的[入站集成](../guides/inbound-integrations.md)、面向 Sparkplug B 节点的可选命令下行、[双重身份验证](#after-v0252-mfa)，以及从 ingress-nginx 到 Traefik 的迁移，并移除 MQTT 正文凭据的剥离。需要采取的行动，按执行顺序如下：

- **如果集群承载两个 default 配置档的实例，请在升级前先加大数据库连接预算。** 新集群获得的预算现在是 `700`；用旧默认值安装的集群保持 `600`，第二个实例的升级可能被拒绝。请重新运行安装该集群时使用的 `dcctl install`，并加上 `--max-connections 700`：例如 `dcctl install local --max-connections 700`，以及安装时使用的其他标志，如 `--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`。参见[下文关于 update-management 的说明](#v0260-updates)和[连接预算](./bootstrap.md#connection-budget)。
- **如果你设置过 `functionalAreas.event-sources.config.capture.bodyCredential`，请删除它，** 并从脚本中移除 `--mqtt-body-credential`。参见[MQTT 正文凭据的剥离被移除](#after-v0252-body-credential)。
- **检查所有用脚本管理自动注册配置的做法，** 并轮换每个现有配置的密钥。参见[自动注册配置](#self-registration-profiles)。
- **登录的 API 客户端必须处理 `MFA_CHALLENGE`。** 参见[双重身份验证](#after-v0252-mfa)。
- **如果你把现有安装迁移到 Traefik，请在删除 Ingress 对象之前检查 Gateway 的证书。** 跨越签发者切换升级的实例，可能留下一份不是由实例自己的证书颁发机构签发的 `devicechain-tls-gateway` 证书。请检查其签发者，如果不是实例 CA，就重新签发；这项检查是[迁移指南](./ingress-cutover.md#ingress-cutover-steps)中的一个步骤，位于删除 Ingress 对象之前。
- **监控栈升级到新的主版本线**（Prometheus 3）。参见[监控栈](#v0260-monitoring)。
- **从此版本只能向前，不要回退。** device-management 中把自动注册配置和设备认领的密钥替换为摘要的迁移无法撤销，采集读取器也迁移到了新的持久消费者（参见[消息](#v0260-messaging)）。

升级会运行六个迁移，每个在拥有它的服务首次启动时运行，被中断后都可以再次运行：`device-management` 中是设备自注册、录入密钥摘要、集成和设备别名；`user-management` 中是第二因素和自注册治理维度。启动时，`device-management` 还会把所有已存的自动注册配置密钥和设备认领密钥转换为带密钥的摘要；升级时处于打开状态的认领，仍可使用交给其持有人的密钥。

在此之前，自动注册配置可以创建和编辑，但没有任何东西会读取它：没有设备能通过它注册。因此，没有任何昨天还能工作的东西会因为某个设备群在用它而停止工作。变化的是配置的创建方式以及它的要求。

该版本还新增了[双重身份验证](#after-v0252-mfa)，它改变了 API 客户端登录已开启该功能的账号的方式。

#### 自动注册配置 {#self-registration-profiles}

- **预配密文由服务器生成。**`createProvisioningProfile` 不再接受 `provisionSecret`。它返回 `{ profile, issuedSecret }`，而过去返回的是配置本身，并且 `issuedSecret` **只显示一次**。从结果中选取 `token` 的脚本现在必须选取 `profile { token }`。`updateProvisioningProfile` 同样不再接受 `provisionSecret`。`rotateProvisioningProfileSecret(token)` 是替换密文的唯一方式。两者对基于旧 Schema 构建的客户端都是破坏性的：请重新生成基于旧 Schema 构建的客户端，并检查你的脚本。新增操作列在[自动注册配置](../reference/graphql-api.md#provisioning-profiles)下。
- **现有配置在你轮换之前会被拒绝。**新端点不接受由运维人员选定的密文，只接受服务器生成的密文。升级时已存在的每个配置，在注册时都会返回 `401 PROVISIONING_FAILED`，控制台会将其标记为**需要轮换（Rotate required）**（GraphQL 中 `secretIssued` 为 `false`）。轮换它，复制新密文，并交给设备群。配置的其他设置都不变。**本次升级无法撤销。**数据库中的自动注册配置密文和设备认领密文会被替换为摘要，而该迁移拒绝回滚（明文已不存在，删除摘要会销毁所有密文）。若必须回到更早的版本，请从备份恢复。
- **预配密钥按租户唯一，而不是整个实例唯一。**两个租户现在可以使用相同的密钥。让它在实例范围内唯一的数据库约束，被按租户的约束取代。
- **配置可以有上限。**`maxDevices` 是新增的可选项。未设置它的配置仍然没有上限。

#### 设备 {#self-registration-devices}

- **只有已为某配置预指定的设备，该配置才会采用。**在两种策略下（包括 `ALLOW_NEW`），已存在但未为该配置预指定的设备都会被 `409 DEVICE_NOT_EARMARKED` 拒绝。预指定的方式是在 `createDevice` 和 `importDevices` 上使用 `provisioningProfileToken`，或使用控制台的**预期自动注册配置（Expected provisioning profile）**。现有设备没有预指定，而预指定只能在创建设备时设置：`updateDevice` 没有这个字段，对已存在设备的 `importDevices` 行，只要其预指定不同（包括设备没有预指定），就是 `CONFLICT`。因此，配置永远无法采用在本次升级之前创建的设备。这样的设备仍会用它已有的凭据继续工作；若要把它纳入某个配置，请创建一台为该配置预指定的新设备。
- **只要配置还有签发出去的凭据，就不能删除它**，只要还有配置使用某个设备类型，就不能删除该设备类型。两者都返回 `REFERENCE_VIOLATION`。请先重置或删除相关设备。

#### 运行与维护 {#self-registration-operating}

- **端点需要 API Ingress。**`POST /api/device-management/provisioning/{tenant}/register` 通过与 GraphQL API 相同的 `/api/<area>` 路由访问。`ingress.enabled: false` 的实例没有通往它的公开路径。
- **`dcctl bootstrap` 和 `dcctl upgrade` 现在会根据集群的 Pod 地址范围渲染 `infrastructure.http.trustedProxies`**，使按来源的限制看到的是设备，而不是 Ingress 控制器。实例配置会变化，因此各服务会随滚动升级一起重启。如果你运行自己的 Ingress 控制器，或位于负载均衡器之后，请检查它：[客户端地址与受信任代理](./bootstrap.md#trusted-proxies)。没有它，每个 Ingress Pod 之下的所有设备会共用同一份请求配额，陌生人可以把它用光。
- **新增配置**，全部可选，位于 device-management 的 `provisioning` 键下：`readDeadline`、`maxInFlight`、`maxConcurrentRegistrations`、`sourceBurst` 和 `sourceEvery`。默认值见[限制](../reference/device-protocol.md#self-registration-limits)。
- **新增指标和三条告警**（`ProvisioningAuthFailuresSustained`、`ProvisioningRateLimited`、`ProvisioningMisconfigured`）随 Chart 一起提供。在不使用该端点的实例上它们保持安静。见[设备自注册](./observability.md#self-registration-alerts)。
- **新增一个治理维度**，限制租户能进行的注册数：默认突发 500，每秒 20 次，可按层级或按租户设置。见[治理与配额](../concepts/governance.md)。

#### 前端从 ingress-nginx 迁移到 Traefik {#after-v0252-front-end}

新安装通过 Traefik 和 Gateway API 路由提供主机服务，没有 `ingress-nginx` 的集群也不再安装它。**现有集群会保留 `ingress-nginx` 并继续让它提供服务，同时在它旁边增加 Traefik。** 在你迁移之前，主机不会有任何变化：

- `dcctl install` 在 `ingress-nginx` 旁边安装 Traefik 和 Gateway API 定义，使用自己的负载均衡器和地址。
- `dcctl upgrade` 为每个实例创建 Gateway 和路由，并且只要集群上有 `ingress-nginx`，就在它们旁边保留实例的 Ingress 对象。两者使用不同的证书：Ingress 对象保留 `devicechain-tls`，Gateway 使用 `devicechain-tls-gateway`。在迁移窗口内，升级会等待 Gateway 的证书 30 秒，然后给出警告并继续；`--drop-legacy-ingress` 会拒绝执行，直到它 `Ready`，而 `--remove-legacy-ingress` 会检查每个实例。
- 你在自己选定的时间把 DNS 迁移到 Traefik 的地址。然后 `dcctl upgrade --drop-legacy-ingress` 停止生成 Ingress 对象，`dcctl install --remove-legacy-ingress` 移除 `ingress-nginx`。只要还有 Ingress 对象指向其类，移除就会被拒绝。
- 只要 `ingress-nginx` 仍已安装，`dcctl upgrade --ingress-controller nginx` 就能把实例放回它上面。该选择会被记录，并由之后的升级保留；`--ingress-controller auto` 将其清除。
- **通过 `nginx` Ingress 类解决 HTTP-01 的 ACME 签发者在迁移窗口内继续可用，`ingress-nginx` 被移除后续期会失败。** 在 DNS 指向 Traefik 之前保持原样，之后在 `--drop-legacy-ingress` 和 `--remove-legacy-ingress` 之前为它添加 `gatewayHTTPRoute` 求解器。更早设置会失败。DNS-01 求解器全程可用。
- 在 AWS 上，经典负载均衡器会隐藏客户端地址。`dcctl install --aws-nlb traefik` 会为 Traefik 的 Service 配置 NLB（其他取值是 `nginx`、`all` 和 `none`）；更改 Service 会替换其负载均衡器并更改主机名。
- HA 安装运行两个 Traefik 副本，分散到不同节点，并带有保持一个副本可用的 PodDisruptionBudget。
- 在本地集群上没有 DNS 可迁移：浏览器访问的是占用节点 80 和 443 端口的那个控制器。`ingress-nginx` 占用它们，直到 `--remove-legacy-ingress` 把端口交给 Traefik，所以请连续运行 `--drop-legacy-ingress` 和移除操作：两者之间主机不响应。全新的本地集群从一开始就由 Traefik 占用这些端口。

每一步、命令、你可能看到的拒绝信息以及回滚方法，都在[将现有安装迁移到 Traefik](./ingress-cutover.md)中。新增的标志见 [`dcctl` 参考](../reference/dcctl.md)。

#### Ingress-nginx 保留客户端地址 {#after-v0252-nginx-local}

只要集群上有 `ingress-nginx`，`dcctl install` 现在就会在它的 LoadBalancer Service 上设置 `externalTrafficPolicy: Local`。否则互联网上的每个设备都会按节点共用一份配额。见[客户端地址与受信任代理](./bootstrap.md#trusted-proxies)。这个更改在生效的那一刻有代价：

- 在负载均衡器的健康检查把没有运行控制器 Pod 的节点标记为不健康之前，它仍会向这些节点发送流量。**预计发往这些节点的流量会一直中断到那时（几十秒）。**
- 非 HA 安装只运行一个控制器副本，所以每次控制器滚动更新都会有一次短暂中断。HA 安装运行两个，并分散到不同节点。

#### 自签名实例保留旧证书 {#after-v0252-self-signed}

提供自签名证书的实例，现在由 Chart 在实例命名空间中创建的证书颁发机构来签发证书，客户端可以通过证书 Secret 中的 `ca.crt` 信任该机构（见[TLS、标志与笔记本安装](./bootstrap.md#install-tls-and-flags)）。现有实例**在下次续期之前保留原来的叶子证书**。删除 `devicechain-tls` Secret，即可立即用该机构重新签发；客户端随后需要重新从该 Secret 获取 `ca.crt`。

已迁移到 Gateway 的实例会一直使用 `devicechain-tls-gateway`，其中是同一机构签发的新叶子证书。迁移之后，客户端应从 `devicechain-tls-gateway` 获取 `ca.crt`。

#### MQTT 正文凭据的剥离被移除 {#after-v0252-body-credential}

平台 MQTT 代理上的正文凭据现在一律被拒绝，没有任何东西需要配置：

- **`capture.bodyCredential` 已被移除，只要它被设置，`event-sources` 就拒绝启动**，不论取值如何。错误信息会指出该键以及该怎么做。如果你自己管理 Chart 的值，请在升级前**删除 `functionalAreas.event-sources.config.capture.bodyCredential`**。
- **`dcctl bootstrap` 和 `dcctl upgrade` 不再接受 `--mqtt-body-credential`。** 传入该标志的脚本会失败：请删除这个标志。这是命令行上的破坏性变更。
- 此前处于 `strip` 的、由 dcctl 管理的实例，升级后处于拒绝状态，升级时会打印一条通知。仍在发送正文凭据的设备会失去响应。请通过 `devicechain_eventsources_total_msg_credential_in_mqtt_body` 观察它们，并按[设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)更新它们。
- 边缘代理自身对设备载荷中凭据的剥离（`body_credentials_stripped_total`）不变。

#### 升级期间 MQTT 消息可能丢失 {#after-v0252-mqtt-roll}

升级会逐个重启平台代理的服务器。在一次使用预发布构建对本次升级的代理滚动重启所做的演练中，集群是七节点的 GKE 集群（三个服务节点、三个数据库节点和一个负载生成节点），以 `--ha` 安装，其代理运行三个服务器，MQTT 设备全程以每秒 300 条的速度发送 QoS 1 消息，**代理已确认的 448,346 条消息中有 23 条从未被存储**。丢失发生在三次代理服务器重启前后的突发中。QoS 1 的至少一次投递在**代理滚动重启期间无法保证**：已收到 PUBACK 的设备不会重发。此前的测量见[滚动升级](#rolling-upgrades)，代理的行为见[MQTT：平台代理](../reference/transport-matrix.md#mqtt--the-platform-broker)，能改变这一结果的故障关闭设置是 `--mqtt-fail-closed`。

#### 双重身份验证 {#after-v0252-mfa}

账号现在可以开启[双重身份验证](../guides/two-factor-authentication.md)：密码之后再输入验证器应用生成的代码，并附十个一次性恢复码。在所有者主动开启之前，所有账号均为关闭状态，因此对什么都不做的用户没有任何变化。控制台、仪表板应用和 MCP 授权页面都会要求输入代码，租户和层级可以强制要求它。user-management 数据库通过一次自动迁移新增了保存该因素及其恢复码的表。

- **`login` 现在可能以 `MFA_CHALLENGE` 失败，因此 API 客户端必须处理它。** 对设有第二因素的账号，正确的密码不再返回令牌：`login` 以错误码 `MFA_CHALLENGE` 失败，并在 `extensions` 中带有质询，客户端用新的 `verifyMfa(challenge, code)` 完成登录。不了解它的客户端会看到一个带有清晰信息的登录失败，而不会拿到令牌。没有第二因素的账号登录方式与以前完全相同。C# SDK 会抛出 `MfaRequiredException` 并新增 `VerifyMfaAsync`；`dcctl` 在 `dead-letters`、`presence` 和 `sim` 上新增 `--otp`。参见[使用双重身份验证登录](../reference/graphql-api.md#two-factor-sign-in)。
- **`login` 审计记录的含义变了。** 它现在在登录**完成**时写入，标签为 `pwd`、`pwd+otp` 或 `pwd+recovery`。对设有第二因素的账号，密码这一步是新的 `mfa_challenged` 记录，因此原先把 `login` 记录当作密码验证成功来统计的查询，现在统计的是已完成的登录。新增的记录涵盖设置、重置、失败与被锁定的尝试以及恢复码的使用。
- **设置和关闭该因素都会结束账号的会话。** 设置会结束其他所有会话，控制台会换上一个新的。关闭会结束所有会话，包括调用者的会话。
- **租户可以强制要求它**，层级可以用层级键 `mfaRequired` 设置下限。没有该因素的成员会被引导去设置，已在租户内的会话在下一次刷新时结束。超级用户、服务账号和单点登录会话不受租户设置约束。
- **新的 break-glass 命令 `dcctl instances break-glass mfa-reset`** 用于重置丢失了因素、无法登录的运维人员。它需要集群凭据，总会更换密码，并只打印一次新密码。
- **随 chart 到来的有两条新告警 `MfaIdentityLocked` 和 `MfaVerificationFailuresSustained`**，以及四个指标。在没有人设置第二因素时它们保持安静。锁定的补救办法是重置密码，而不是重置因素。参见[第二因素锁定](./observability.md#mfa-alerts)。
- **节点需要正常的时间同步。** 代码在其 30 秒步长及前后各一个步长内有效。

#### 空中（OTA）更新进入 default 配置档 {#v0260-updates}

从该版本起，[空中（OTA）更新](../concepts/over-the-air-updates.md)属于 `default` 配置档，因此运行标准系统的实例在升级时会获得 `update-management` 服务。升级的其他方面没有变化。开始之前请检查以下几点：

- **可能需要先提高关系型数据库的连接预算。**`update-management` 是又一个使用数据库的服务，所以一个 default 配置档实例现在预留 320 个连接，之前是 280 个。新集群获得的预算现在是 `700`，可容纳两个 default 配置档实例。**用之前的默认值 `600` 安装的集群会保留该值**，如果它容纳了两个 default 配置档实例，第二个实例的升级可能被拒绝。请在升级前提高预算：重新运行安装该集群时使用的 `dcctl install`，并加上 `--max-connections 700`，例如 `dcctl install local --max-connections 700`；如果集群安装时用了 `--kube-context`、`--ha` 或 `--compact`，也要一并带上。参见[连接预算](./bootstrap.md#connection-budget)，其中也说明了提高预算的代价：数据库实例会逐个重启。
- **如果你的对象存储是你用 `dcctl` 指向的集群上的 `ReadWriteOnce` 卷，服务将在没有存储的情况下运行。**这样的卷只能被一个服务挂载，而其他服务已经占用了它，所以 `dcctl` 不会拒绝，而是把 `update-management` 设为 `updates.objectStore: none` 来升级实例，并在升级时说明这一点。声明制品和上传内容会以 `OBJECT_STORE_NOT_CONFIGURED` 被拒绝，控制台会在“更新”页面显示提示，`updateManagementInfo.objectStoreConfigured` 为 `false`。实例无法通过升级获得存储；要使用更新，请改用 S3 或 `ReadWriteMany` 卷重新创建实例。参见[没有对象存储的实例](../concepts/over-the-air-updates.md#no-object-store)。可共享的卷、S3 以及 `dcctl` 自己创建的集群不受影响。
- **注册签名者之前，不能分配任何制品。**默认情况下只有由处于活动状态的签名者签名的制品才能被分配，否则分配会以 `UNVERIFIED_ARTIFACT_REFUSED` 被拒绝。注册签名者需要 `artifact:trust` 权限。请按[签名更新](../guides/signing-updates.md)操作。
- **下载基址现在会被推导。**`updates.downloadBaseUrl` 过去在你设置之前一直未设置，因此没有设备能获得下载链接。现在省略它时，Chart 会根据实例的 Ingress 主机推导出 `<来源>/api/update-management`，Ingress TLS 开启时使用 `https`。你设置的值仍然优先，空字符串则关闭下载。没有 Ingress 的实例无从推导，控制台会提示下载未配置。
- **在 `default` 实例上执行 `dcctl enable-area local <实例> update-management` 会报告它已启用。**`telemetry` 和 `ingest-only` 配置档的实例仍不包含它。
- **能送达的设备与以前相同。**只有通过 MQTT 连接的设备才能接收托管更新。仅使用 HTTP 的设备、LwM2M 设备和 Sparkplug 设备无法送达；参见[哪些设备可以更新](../concepts/over-the-air-updates.md#transports)。

#### 关于空中（OTA）更新的更多内容 {#v0260-updates-more}

除加入 default 配置档外，更新功能在该版本中已经完成。一个[可更新的参考设备](../guides/building-an-updatable-device.md)（A/B 槽位、启动器和回滚）随 `examples/ota-device` 提供。四个新告警 `UpdateDownloadsNotConfigured`、`UpdateReportsMostlyRefused`、`UpdateRolloutStepsFailing` 和 `UpdateAttemptsGoingUnknown` 随图表到来，仅在拥有 `update-management` 的实例上生成；不使用更新的实例上它们保持静默。参见[告警运行手册](./observability.md)。

#### Sparkplug B 命令（可选） {#v0260-sparkplug-commands}

DeviceChain 现在可以向 Sparkplug 节点和设备发送命令：写入一个指标、要求节点重新发布出生消息（rebirth），或重启它，命令通过平台已与你的代理建立的 Host Application 会话发布。它默认关闭，按数据源通过图表值中的 `commands.enabled` 开启（`dcctl` 不接受 Sparkplug 数据源，所以这是 Helm 值设置）。Sparkplug 没有命令确认机制，因此命令在节点之后报告了所请求的值时才算确认，节点没有报告的命令以超时而不是失败结束。写入仅限于数据源列出的指标，重启需要显式设置，熔断器会暂停其代理拒绝发布的数据源。数据源的代理凭据需要有在节点和设备命令主题上发布的权限。三个新告警分别涵盖未确认的命令、已打开的熔断器和内部命令失败：`SparkplugCommandsUnconfirmed`、`SparkplugCommandBreakerOpen` 和 `SparkplugCommandResponsesFailing`。

对于 Sparkplug 数据源保持 `commands.enabled` 关闭的安装，没有任何变化。某个数据源开启它时，图表会自行设置 `command-delivery` 的能力标志；如果 `command-delivery` 没有部署，或者你手工设置了 `functionalAreas.command-delivery.config.sparkplugCommands`，渲染会失败。

#### 计算指标 {#v0260-calculated-metrics}

[计算指标](../user-guide/calculated-metrics.md)仍然保持关闭：允许租户编写它们的层级上限默认仍为 `0`，该版本没有改变它。围绕它有两处变化。**把设备配置回滚到较早版本，现在与发布一样受同一个上限约束：** 上限在发布之后被调低的租户，不能再重新启用计算指标数量超出其层级允许值的版本；这样的回滚会以 `LIMIT_EXCEEDED` 被拒绝，读不到上限时同样被拒绝。回滚到不含计算指标的版本，从不依赖该上限。控制台现在会说明为什么计算指标不能组合它的输入。`hack/` 中提供了一个衡量计算指标成本的测试工具，供维护者使用，不属于安装的一部分。

#### 入站集成与设备别名 {#v0260-integrations}

[集成](../guides/inbound-integrations.md)是租户通过 HTTP 发送设备数据的已认证发送方，例如 LoRaWAN 网络服务器，类型有 ChirpStack v4、The Things Stack v3、通用 JSON 和通用二进制。图表在实例的前端上只发布一条路径，`POST /api/integrations/{tenant}/{integration}[/{messageType}]`，同时经过 Gateway 和 Ingress 对象。通用的设备摄取路由不会发布。租户默认最多可以有五个集成（层级键 `integrationsPerTenant`，取值 0 到 100）。被集成限定范围的设备类型不能删除。设备通过**别名**匹配，来自已不再持有该别名的设备的上行消息会被拒绝为 `IntegrationAliasMismatch`，因此移除或移动别名最迟五分钟生效，而不是一小时。控制台新增集成页面、连接代码片段、未注册设备和设备别名。

#### 监控栈 {#v0260-monitoring}

`dcctl install` 会升级监控栈（kube-prometheus-stack 65.1.1 到 92.13.1）：**Prometheus 2.54 到 3.15**、Grafana 11.2 到 13.2、Prometheus Operator 0.77 到 0.94。Prometheus、Alertmanager、Grafana、kube-state-metrics 和 operator 各重启一次。operator 的 CRD 由一个升级前作业更新，该作业会拉取 `docker.io/busybox:1.37.0` 和 `registry.k8s.io/kubectl`，无法访问这些镜像仓库的集群必须自行镜像。Grafana 保留其管理员登录、文件夹和看板。在本地集群上，Prometheus 的数据只保存在节点磁盘上，因此其历史会从空开始。对该 Prometheus 使用自己的 PromQL 的人应当知道它现在是 Prometheus 3：范围选择器左开，正则表达式中的 `.` 会匹配换行，经典直方图的 `le` 标签会被规范化。DeviceChain 自带的规则和看板加载时没有规则错误。

图表附带的 Grafana 看板也已补全：摄取、消息、持久化与状态、投递和平台看板，一个链接到各看板的实例概览，以及概览上的缩容至 0 状态。

#### 消息与命令行 {#v0260-messaging}

- **设备采集读取器迁移到新的持久消费者** `<instance>_event-sources_device-events-capture_unfiltered`，它创建时不带代理过滤器。在带过滤器的消费者上落后的读取器会降到每秒约 2,000 个事件，并且无法追上。滚动升级期间，旧的 `event-sources` Pod 保留自己的消费者。回退到 `v0.25.x` 需要先删除新消费者（`nats consumer rm <instance>_device-events-capture <instance>_event-sources_device-events-capture_unfiltered`），否则它的采集读取器会一直处于重试状态；告警选择器和看板已同时匹配两个名称。
- **REACT 现在按设备为键并发分发派生事件，** 过去由一个工作线程依次处理；挂起的设备不再让同一通道的其他设备损失投递尝试。无需配置。
- **`dcctl bootstrap` 和 `dcctl upgrade` 新增 `--mqtt-fail-closed`**（以及 `--mqtt-fail-closed=false`），用于开启和关闭故障关闭的 MQTT 确认；省略时保留已记录的选择。此外，每次 `dcctl upgrade` 现在都会在更改任何内容之前，拒绝流预留超出代理文件存储上限的配置。
- **OpenTofu 作用于 `dcctl` 解析出的集群。** 它的提供程序现在收到与客户端相同的 kubeconfig 和上下文；当两者指向不同集群，或设置了覆盖提供程序的环境变量（`KUBE_HOST`、`KUBE_TOKEN` 等）时，`dcctl` 拒绝运行。

#### 请求设备证书的代理 {#v0260-broker}

代理现在是 DeviceChain 自己构建的 `nats-server`，即 `dc-nats-server`：上游 `v2.15.1` 加一项设置，在启动时和每次重新加载时应用，使 MQTT 监听器请求（但不要求）TLS 客户端证书。使用令牌或密码的设备不受影响，目前还没有设备通过证书认证。该镜像与其他镜像一样经过签名，像上游镜像一样以 root 运行，并在 7778 端口导出 `dc_broker_mqtt_client_auth_request`、`dc_broker_mqtt_tls` 和 `dc_broker_info`。新告警 `BrokerMqttClientAuthLost` 在监听器不再请求证书，或代理 Pod 不是该构建时触发。**要回退到上游镜像，** 设置 `server_image_repository = "nats"` 和 `server_image_tag = "2.15.1-alpine"`；这会停用证书设备。请把 `dc-nats-server` 加入你镜像的镜像列表。

`dcctl` 按版本推送的摘要（digest）固定代理，因此代理部分没有变化的版本不会重启它。从较旧的 `dcctl` 进行的第一次升级会把 Pod 模板从标签改为摘要，这相当于一次滚动重启。`dcctl version` 会输出一行 `broker:`，内容为摘要或 `by tag`。

#### 已知问题 {#v0260-known-issues}

- **代理滚动重启之后（包括这次升级），最后一个重启的服务器不领导任何流，** 发布确认的 p99 大约翻倍，直到领导权重新平衡。
- **TLS 证书轮换会使代理重新加载，并断开大多数 MQTT 会话。** 设备会自行重连。后续版本已修复：代理无需重新加载，即可对新连接使用续期后的证书，已建立的会话保持不变。更改代理配置仍会触发重新加载；在代理的 `nats-server` 库包含上游修复之前，重新加载仍会断开经认证调用放行的设备会话。
- **入口控制器的滚动发布会在 28 到 40 秒内丢弃约 5% 的请求。** 请在安静的时段进行。

### v0.25.2：服务令牌签发离开公共端口 {#v0252-upgrade}

`v0.25.2` 是 `v0.25.1` 的安全补丁。没有迁移，API、主题、权限均无变化。实例配置新增一个键
`infrastructure.userManagement.internalPort`，chart 将其设为 `8082`；请保持该值不变，因为 chart 的
Service、pod 和网络策略都使用这个固定端口。[v0.25.1](#v0251-upgrade) 和 [v0.25.0](#v0250-upgrade)
两节所描述的内容（包括其已知限制）仍完全适用。

先升级集群，再升级每个实例。除新的 `--version` 外，还要把集群安装时用的设置（例如 `--ha` 和
`--database-node-selector`）传给 `dcctl install`：

```bash
dcctl install local --version v0.25.2
dcctl upgrade local devicechain --version v0.25.2 --dry-run
dcctl upgrade local devicechain --version v0.25.2
```

**从 `v0.25.0` 或 `v0.24.0` 升级时，**按 [v0.25.1 说明](#v0251-upgrade)操作，并使用
`--version v0.25.2`：其内容原样适用，其中关于 v0.25.0 两次升级的说明也同样适用。

#### 内部端点离开公共端口 {#v0252-internal-endpoints}

`user-management` 过去在其公共端口上提供两个仅供平台自身服务使用的端点：`/auth/service-token`，用共享
服务密钥换取短期服务令牌；`/auth/jwks`，每个服务用来校验令牌的密钥集。唯一让它们不暴露在互联网上的，是
chart 的 API ingress 上的一个 nginx 配置片段（snippet）。**因此，自带 ingress 控制器的安装会让这两个端点
从外部可达，**仅由以恒定时间比较的共享服务密钥保护。

从本版本起，`user-management` 在单独的内部端口 `8082` 上提供这两个端点，chart 只在集群内部的 Service
上发布该端口。没有任何 ingress、路由或负载均衡器指向它，无论前面是什么，公共端口对这两个路径都返回
404，因此**自带的控制器无需任何边缘规则来隐藏它们。**现在每个服务都在内部端口上获取密钥集并签发令牌。
参见[不会离开集群的端点](../concepts/security-model.md#internal-endpoints)。

- **chart 不再设置任何 nginx 配置片段。**
- **`dcctl install` 安装的 ingress 控制器在本版本中保持不变**，仍接受配置片段，因为尚未升级的实例依赖
  它们。后续版本会关闭配置片段，并由 `dcctl install` 的检查把关：只要该控制器类的任何 Ingress 仍带有
  `*-snippet` 注解，它就拒绝继续。
- **外部令牌校验方不受影响：**它们使用授权服务器元数据中的 `jwks_uri`，即 `/oauth/jwks`，该端点仍然公开。

#### 预期情况 {#v0252-during}

- **`user-management` 与其对端服务必须一起滚动更新。**旧版本的 pod 会向公共端口请求密钥集和令牌签发，
  而公共端口现在返回 404，且它无法靠重试恢复：在该 pod 被本版本的 pod 替换之前，它的签发请求都会失败。
  `dcctl upgrade` 会替换所有服务的 pod，因此这种情况只持续到滚动更新结束；不要让任何区域停留在旧镜像上。
- **在 `dcctl upgrade` 写入新配置之后重启的旧 pod 无法启动。**旧的二进制文件严格解码实例配置，会拒绝新的
  `internalPort` 键，因此这样的 pod 会反复崩溃重启，直到被本版本的 pod 替换。
- **如果你运行自己的网络策略引擎，**请允许实例命名空间中的 pod 访问 `user-management` 的 `8082` 端口，
  且仅此而已。chart 自带的针对该端口的网络策略默认关闭，`dcctl` 安装也以关闭状态运行。
- **chart 自带的 Grafana 看板**现在都以该实例正在触发的告警表开头，并链接到该实例的其他看板；新增的第三个
  看板 *Instance overview* 目前只包含这张表。

### v0.25.1：数据库和备份存储的镜像，针对当前安全公告重新构建 {#v0251-upgrade}

`v0.25.1` 是 `v0.25.0` 的补丁。没有迁移，API、主题、权限、chart 值、配置键均无变化。它把集群和每个
实例运行数据库与备份存储所用的三个第三方镜像，换成不再带有下列安全公告的构建，并修正两个服务向
Prometheus 报告的内容以及 chart 为它们自带的 Grafana 看板（参见[指标与看板](#v0251-metrics)）。[v0.25.0 一节](#v0250-upgrade)所描述的内容仍完全适用。

[v0.25.0 的已知限制](#v0250-upgrade)仍然适用，包括这一条：`mqttFailClosed` 关闭（默认）时，在持续负载下即使没有代理重启，当 `event-sources` 落后、捕获流写满时，已确认的 QoS 1 消息也可能丢失。`devicechain_<area>_jetstream_consumer_unread_skipped_total` 会统计这些丢失，`JetStreamDurableLostUnread` 会告警。

先升级集群，再升级每个实例。除新的 `--version` 外，还要把集群安装时用的设置（例如 `--ha` 和
`--database-node-selector`）传给 `dcctl install`：

```bash
dcctl install local --version v0.25.1
dcctl upgrade local devicechain --version v0.25.1 --dry-run
dcctl upgrade local devicechain --version v0.25.1
```

**从 `v0.24.0` 升级时，**按 [v0.25.0 说明](#v0250-upgrade)操作，并使用 `--version v0.25.1`：其内容原样
适用，包括运行两次 `dcctl upgrade`。**从 `v0.25.0` 升级时，**如果 v0.25.0 的两次升级都已运行，一次
`dcctl upgrade` 即可。如果只运行了第一次，共享的消息代理登录仍然存在，本次升级会停用它，并为此重启一次
消息代理；其 `--dry-run` 和结束时的摘要会说明这一点（见[消息代理的登录，分两次升级](#v0250-broker-logins)）。

#### 变动的内容 {#v0251-images}

| 镜像 | 运行 | 从 | 到 | 不再带有的安全公告 |
| --- | --- | --- | --- | --- |
| `ghcr.io/devicechain-io/postgresql-timescaledb` | 每个实例的事件存储 | `17.10-ts2.28.3-r1` | `17.11-ts2.28.3-r1` | Perl `5.36.0-7+deb12u3`：CVE-2026-13221、CVE-2026-42496、CVE-2026-8376（严重），CVE-2026-42497、CVE-2026-48962、CVE-2026-57432、CVE-2026-57433（高）。PCRE2 `10.42-1`：CVE-2026-103111、CVE-2026-86145、CVE-2026-89157、CVE-2026-89161（高） |
| `ghcr.io/cloudnative-pg/postgresql` | 集群共享的关系数据库（`dc-rdb`，位于 `dc-system`） | `17.10-standard-bookworm` | `17.11-standard-bookworm` | 相同的 Perl 和 PCRE2 公告 |
| `cgr.dev/chainguard/minio` | 集群内的备份存储（备份存放在集群内时） | 摘要 `sha256:32c504be…` | 摘要 `sha256:73297936…` | CVE-2026-111497（Brotli 解码器，高），CVE-2026-111943（Go `crypto/x509`，位于随附的 `mc` 客户端中，高） |

事件存储镜像是 DeviceChain 基于 CloudNativePG PostgreSQL 基础镜像自行构建的，因此随该基础镜像一起变动。
PostgreSQL 从 17.10 升到 17.11，属于小版本：数据文件原样保留，无需转储或恢复。TimescaleDB 仍为 2.28.3。
用 Trivy 扫描已有修复的高危和严重公告，所有新镜像均未报告任何问题。

#### 指标与看板 {#v0251-metrics}

- **`event-processing` 在副本不再担任领导者时，把仅由领导者写入的指标清零。** 失去检测租约的副本过去会
  继续导出其最后一个任期的积压、延迟和状态大小。看板以及 `DetectConsumerBacklogHigh`、
  `DetectWatermarkLagHigh` 和 `DetectTenantOverStateBudget` 告警取各副本中的最大读数，因此这个冻结的
  读数可能显示一个没有任何副本存在的积压，并使告警无法解除。现在备用副本对这些指标报告零。已加载规则的
  数量不受影响，因为每个副本都会加载规则。
- **`command-delivery` 从启动起就以零导出其命令批次计数器**，因此在第一个批次之前，其看板显示零而不是
  "No data"，对它们的 `increase()` 也能看到第一个批次。
- **chart 自带的两个看板已修正：** 其序列与服务导出的指标一致，罕见事件以计数显示，混合单位的面板被拆分，
  并新增了保留样本预算的面板。

#### 预期情况 {#v0251-during}

- **`dcctl install` 会让关系数据库的实例重启一次**，换到新镜像：在 `--ha` 下先重启备用实例，再切换到其中
  之一，因此写入只在切换期间暂停。单实例的关系数据库在其唯一实例重启完成之前不可用，期间的写入会重试。
  `dcctl install` 会等到每个实例都运行新镜像。
- **`dcctl install` 会让备份存储的 Pod 重启一次**（当集群把备份保存在集群内时）。已存储的备份和预写日志都会
  保留。Pod 重启期间归档暂停，数据库在本地保留预写日志。通过 `--backup-credentials-file` 把备份发送到你自己
  的对象存储的集群不运行备份存储，那里没有任何变化。
- **`dcctl upgrade` 会让实例的事件存储重启一次**，换到新镜像，这发生在移动服务之前的基础设施步骤中：在
  `--ha` 下先重启备用实例，再进行切换；没有 `--ha` 时，唯一的实例会重启，在它恢复之前 event-management
  无法存储事件。这些事件在接入流中等待并重试；只有在大约四分钟的重试都失败后才会放弃某个事件。升级会等到
  事件存储的每个实例都运行新镜像，然后才移动服务。消息代理的设置没有变化，因此其服务器不会重启。
- **各服务逐个 Pod 切换到 `v0.25.1` 镜像**，与任何升级相同。除上述两项指标变更外，其代码与 `v0.25.0` 相同。
- **如果你把镜像同步到自己的镜像仓库，**请在升级前同步上面的三个新引用，并照常同步 DeviceChain 的
  `v0.25.1` 镜像。
- **如果你自己设置了 `timescale_image`**（在实例基础设施状态旁的 `terraform.tfvars` 中），升级会保留你的
  值，事件存储会停留在它指定的镜像上。要切换，请把它设为
  `ghcr.io/devicechain-io/postgresql-timescaledb:17.11-ts2.28.3-r1`，或删除该行。
- **在同一次运行中更改数据库设置会被拒绝**（对于多于一个实例的数据库）：数据库 Operator 不接受同时更换镜像和
  更改设置。让 `dcctl install` 只使用集群已有的设置（例如不同的 `--max-connections` 就属于这种更改），
  并在单独的一次运行中更改该设置。

### v0.25.0：每个领域以自己的身份登录消息代理，且发布版本已签名 {#v0250-upgrade}

`v0.25.0` 可从 `v0.24.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行 `dcctl upgrade`，
如 [v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade) 所述。除新的 `--version` 外，还要把集群安装时用的设置（例如 `--ha` 和
`--database-node-selector`）一并传给 `dcctl install`。**与大多数版本不同，`dcctl upgrade` 要执行两次。**
第一次为每个领域创建各自的消息代理登录，并保留共享登录与之并存；第二次则停用共享登录（见
[消息代理的登录，分两次升级](#v0250-broker-logins)）。**无法从 `v0.22.0` 或更早版本原地升级到本版本：**
请先升级到 `v0.23.0` 和 `v0.24.0`（见[升级之前](#v0250-before)）。它的三项迁移共新增五张表、一个列及其索引，
不改写任何已有的行（见[升级期间](#v0250-during)）。**升级后只能向前，不能回退**（见[回退](#v0250-going-back)）。

命令如下，版本号带 `v`，并先做一次演练：

```bash
dcctl install local --version v0.25.0
dcctl upgrade local devicechain --version v0.25.0 --dry-run
dcctl upgrade local devicechain --version v0.25.0
# when that has finished and every area is Ready, run it again to retire the shared broker login:
dcctl upgrade local devicechain --version v0.25.0
```

`--version` 取带 `v` 的标签。集群不是默认的本地集群时，请向 `dcctl install` 和 `dcctl upgrade` 传入
`--kube-context`；镜像来自默认之外的镜像仓库时，再传入 `--registry`。两次执行各自的 `--dry-run` 会说明它将
执行两者中的哪一次。如果实例引导时没有根密钥托管，`dcctl upgrade` 会打印托管警告；这是预期行为，与本次
升级无关。为 `v0.24.0` 设置的 `--mqtt-body-credential` 取值，在省略该标志的升级中会被保留；该标志已在 `v0.26.0` 中移除（见 [`v0.24.0`](#v0240-mqtt-body-credential)）。

本版本在信任方面做了两件事。在集群内部，**任何服务都不再与其他服务共用一个消息代理凭据**：每个领域以自己的
身份登录消息代理，权限仅限于它用到的内容，并且只有一个管理登录可以创建和修改流与桶。在集群外部，
**每个镜像和 chart 都经过签名**，你可以核对所运行的内容确实由 DeviceChain 的发布工作流构建。此外：设备的
访问令牌改为以带密钥的摘要存储，边缘代理成为拥有自己身份的网关，租户可以为其集成创建服务账号，空中更新
也可以向设备组发布。对某些人有影响的变化：

- **执行两次 `dcctl upgrade`。** 第一次升级保留共享的 `dc_service` 登录；第二次将其移除。见
  [消息代理的登录，分两次升级](#v0250-broker-logins)。
- **设备的访问令牌不能再被读回。** 凡是读取 `ACCESS_TOKEN` 凭据的 `credentialId` 并把它当作令牌使用的做法，
  现在拿到的都是指纹。见[设备访问令牌以摘要形式存储](#v0250-access-tokens)。
- **边缘代理必须在升级之前迁移到网关身份**；旧的直连设备上行链路已经移除。见
  [边缘代理是网关](#v0250-edge-gateway)。
- **消息代理拒绝的 C# SDK 连接现在会抛出 `MqttConnectRefusedException`，** 而不是被当作网络故障而重试。见
  [C# SDK](#v0250-csharp-sdk)。
- **不通过 `dcctl` 运行 chart 的安装**，需要在首次安装之前准备好实例命名空间和消息代理管理员的 Secret，
  或设置 `brokerAdmin.enabled=false`。见[消息代理的登录，分两次升级](#v0250-broker-logins)。
- **两次升级中，消息代理各重启一次，** 使用 `--ha` 时一次重启一台服务器。见[升级期间](#v0250-during)。
- **无法回退到 `v0.24.0`。** 见[回退](#v0250-going-back)。
- **从 `v0.22.0` 或更早版本升级时，请先升级到 `v0.23.0` 和 `v0.24.0`。**
- **三项迁移：** `update-management` 中两张表，`user-management` 中三张表，`device-management` 中一个列和
  一个索引。见[升级期间](#v0250-during)。
- **发布版本已签名。** 无须操作；见[首个已签名的发布版本](#v0250-signing)。
- **你手工设置的调优，除非通过 `--values` 提供，否则会被升级还原。** 这一点以前就是如此；本版本提供了保留它的
  办法。见[运维人员的调优会被保留](#upgrade-tuning)。

#### 升级之前 {#v0250-before}

1. **从 `v0.22.0` 或更早版本升级时，请先停下。** 先升级到 `v0.23.0`，再升级到 `v0.24.0`，每次都要等所有
   pod 都在运行新版本，之后才能升级到 `v0.25.0`。`v0.24.0` 设为默认的 compact 线路格式，`v0.22.0` 及更早版本
   无法读取（见[事件在代理上的编码格式](#event-wire-format)），而跳过一个次版本不是经过测试的路径（见
   [支持的升级路径](./versioning-policy.md#upgrade-paths)）。也请阅读 [`v0.24.0` 说明](#v0240-upgrade)。由
   `v0.16.0` 或更早版本构建的实例仍然无法升级；见
   [v0.16.0 及更早版本构建的实例](#pre-declaration-recreate)。
2. **找出读回设备访问令牌的地方。** 凡是从 `deviceCredentials` 或 `deviceCredentialsByToken` 获取令牌的脚本、
   配置工具或电子表格，现在收到的都是以 `dctk1$` 开头的指纹。设备中的令牌仍然有效。请决定每个你没有记录的
   令牌将从哪里获得：为该凭据设置新令牌（见[设备访问令牌以摘要形式存储](#v0250-access-tokens)）。
3. **如果运行边缘代理，请在升级它之前规划好它向网关的迁移。** 为每个代理创建一个带 `MQTT_BASIC` 凭据的网关
   设备，并把该站点由它前置的设备分配给它。在平台（包括其消息代理）完成升级之前，不要在本版本上重启任何代理。
   见[边缘代理是网关](#v0250-edge-gateway)。
4. **如果不通过 `dcctl` 运行 chart，请先创建命名空间和管理员的 Secret，** 或者设置
   `brokerAdmin.enabled=false`，并使用 `dc-broker-admin` 镜像自行创建流和桶。见
   [升级会应用哪些基础设施](#upgrade-infrastructure)。自带消息代理的安装可以用 `dcctl broker permissions`
   打印每个登录所需的授权。
5. **如果把镜像镜像到自己的镜像仓库，请加上 `dc-broker-admin`。** pre-upgrade 作业会运行它，如果拉取不到，
   升级会在任何服务滚动之前失败。它同样在[已签名镜像](./verifying-releases.md#what)的列表中。
6. **如果你手工调优过实例**（副本数、资源、某个领域自己的配置），请把它写入 values 文件，并在第一次升级时
   通过 `--values` 传入。升级会根据声明重建实例的设置，因此以其他方式设置的内容会被还原。见
   [调优实例](./tuning-an-instance.md)。
7. **如果有 C# 或 Unity 设备程序连接平台的 MQTT 消息代理，** 请在重新构建它之前阅读
   [C# SDK](#v0250-csharp-sdk)。

#### 升级期间 {#v0250-during}

- **运行三项迁移，各在拥有它的服务首次启动时运行。** 没有任何一项改写行，被中断后也都可以重新运行。
  - `device-management` 新增可为空的列 `devices.gateway_id`，记录设备位于哪个网关之后（所有已有设备该列
    为空），以及 `(tenant_id, gateway_id)` 上的一个部分索引。该索引用普通的 `CREATE INDEX` 构建，构建期间
    会对 `devices` 的写入加锁；它只覆盖有网关的设备，因此首次运行时是空的。
  - `update-management` 创建 `update_rollouts` 和 `update_rollout_targets`，用于保存设备组发布。两张表初始为空。
  - `user-management` 创建 `iam_service_accounts`、`iam_service_account_roles` 和
    `iam_service_account_secrets`，用于保存服务账号及其密钥的摘要。三张表初始都为空。
- **`device-management` 在启动时把每个已存储的访问令牌转换为其摘要。** 这不是迁移：它在迁移链之后、每次
  启动时运行，覆盖所有租户和已软删除的行，仅当行仍保持读取时的值才写入，第二次运行时无事可做。设备保留
  它已有的令牌；同一个字符串在转换前后都能通过认证。
- **`device-management` 滚动期间，使用访问令牌认证的设备可能连接失败。** 一旦本版本的某个 pod 完成了行的转换，
  仍在滚动中的 `v0.24.0` pod 会按明文查找出示的令牌，什么也找不到，因此在新旧并存期间，它会拒绝访问令牌的
  连接和逐事件检查。设备会重试并连上本版本的 pod。在并存期间由上一版本的 pod 写入的令牌，会在本版本的 pod
  下一次启动时被转换。使用 `MQTT_BASIC` 认证的设备不受影响。
- **消息代理在第一次升级中重启一次，在第二次升级中再重启一次。** 第一次向消息代理添加登录；第二次移除共享
  登录。使用 `--ha` 时，每次一台服务器地重启，前一台就绪后才停下一台；不使用 `--ha` 时，单台服务器重启，
  其间消息代理不可用，通常约一分钟。设备和服务会自行重连。**消息代理滚动期间，不要手工重启任何一台服务器。**
  见[升级会应用哪些基础设施](#upgrade-infrastructure)。
- **有一个作业在消息代理与服务之间运行。** chart 把 `<instance>-broker-admin` 作为 Helm pre-install 和
  pre-upgrade 钩子运行。它创建或修复每个流和键值桶，包括为 `resolved-events` 流开启直接读取，event-processing
  的重放现在会用到这一点。如果它失败，升级会在任何服务滚动之前失败，`dcctl` 会打印该作业的日志，而
  `v0.24.0` 的服务继续运行。该作业默认允许运行 540 秒（`brokerAdmin.activeDeadlineSeconds`），并重试一次
  （`brokerAdmin.backoffLimit`）。
- **升级运行期间重启的上一版本 pod 无法启动。** `dcctl` 在该作业之前写入新的实例配置，其中带有 `v0.24.0` 没有
  声明的键（`areaLogins`），所以在此之后重启的 `v0.24.0` pod 会拒绝该配置。继续运行的 pod 不受影响。请让升级
  完成，而不要手工重启 pod；替换它们的 pod 运行的是本版本。
- **每个领域等待自己的登录。** 第一次升级之后，如果实例表明各领域的登录已经存在而某个领域自己的 Secret 缺失，
  该领域会拒绝启动，而不会回退到共享登录。

#### 回退 {#v0250-going-back}

**只能向前，不要回退到 `v0.24.0`，无论使用 `helm rollback` 还是其他方式。** 如果升级中途失败，请排除原因，
并使用相同的 `--version` 再次执行 `dcctl upgrade`。回退会留下一个无法工作的实例：

- **每台使用访问令牌的设备都会被拒绝。** 令牌现在以摘要存储，而 `v0.24.0` 按明文查找出示的令牌。回退不会恢复
  明文，明文也没有保存在任何地方。
- **共享的消息代理登录可能已经消失。** 第二次升级之后，消息代理不再有 `dc_service`，实例配置也不再提到它，
  所以 `v0.24.0` 的服务没有可用来连接的登录。`dcctl` 在停用该登录时会这样说明：不支持跨越本版本回退，请改为
  重新执行较新的 `dcctl upgrade`。
- **配置是为本版本写入的。** `dcctl` 把实例配置写入 Secret `dci-<instance>-config`，位于 chart 的发布之外，
  所以 `helm rollback` 会恢复上一版本的工作负载，而配置仍保持本版本写入的样子。其中带有 `v0.24.0` 没有
  声明的键 `areaLogins`；服务启动时会拒绝未知的配置键。

新增的表和新增的列不会造成妨碍：`v0.24.0` 读取同一个数据库，对它们闲置不用。

#### 升级之后 {#v0250-after}

- **在第一次升级以所有领域就绪结束后，执行第二次 `dcctl upgrade`。** 见
  [消息代理的登录，分两次升级](#v0250-broker-logins)。
- **如果愿意，检查所安装内容的签名。** 见[首个已签名的发布版本](#v0250-signing)。
- **保持 `metrics.enabled` 和 `metrics.alerts` 开启**（默认均为开启），以安装三条新的租户删除告警：
  `TenantPurgeStoreFailing`、`TenantPurgeStoreHolding` 和 `TenantPurgeCoordinatorFailing`。见
  [租户删除](./tenant-deletion.md#blocked-store)。
- **平台完成后，迁移每个边缘代理。** 见[边缘代理是网关](#v0250-edge-gateway)。
- **为你没有记录的任何访问令牌凭据设置新令牌。** 见
  [设备访问令牌以摘要形式存储](#v0250-access-tokens)。

##### 消息代理的登录，分两次升级 {#v0250-broker-logins}

迄今为止，每个服务都用同一个共享登录登录消息代理，所以任何 pod 都能出示、任何一次配置泄露都会暴露所有服务
的凭据。其机制在[升级会应用哪些基础设施](#upgrade-infrastructure)中说明，该节涵盖了各领域的登录、管理作业和
`dcctl broker reconcile`；对升级而言，其含义如下：

- **第一次升级是两个阶段中的第 1 阶段。** 它为每个使用消息代理的领域（`mcp` 和 `ai-inference` 从不打开消息代理
  连接，因此没有）创建一个登录，并为消息代理管理创建一个登录，每个都放在名为 `dci-<instance>-nats-<area>`
  的 Secret 中，只有该领域会挂载它。共享的 `dc_service` 登录保留，权限与以往相同，因此尚未滚动的 pod 保持
  连接。在第二次升级之前，它仍在每个 pod 读取的配置中，所以 pod 仍然可以出示它。结尾的摘要写道：
  "This was phase 1 of 2: run `dcctl upgrade` once more to retire it."
- **第二次升级停用它。** 只有当起点实例已经让每个领域都运行在自己的登录上时，它才会这样做。它会检查：
  - 正在运行的配置是否已经告诉各领域使用自己的登录；
  - 每个登录的 Secret 是否在本次运行之前就已存在，即没有任何一个是在本次创建的；
  - 正在运行的消息代理是否已经为每个领域和管理员持有登录；
  - 正在运行的版本是否带有消息代理管理作业；
  - 对该实例的上一次运行是否以就绪结束；以及
  - 本次运行是否应用消息代理，所以 `--skip-infrastructure` 永远不会停用该登录。

  全部满足时，消息代理和实例配置不再带有共享登录，消息代理再重启一次，服务再滚动一次以采用变更后的配置。
  结尾的摘要写道："The shared message broker login is retired."。有任何一项不满足时，该登录会被保留，
  摘要写道："The shared message broker login was NOT retired"，并指出原因；请解决后再次执行 `dcctl upgrade`。
- **不要向任何一次执行传入 `--skip-infrastructure`。** 它不对消息代理应用任何内容，所以既不会添加登录，
  也不会停用共享登录。
- **新实例从来没有共享登录。** `dcctl bootstrap` 只创建各领域的登录。
- **不支持跨越第二次升级回退。** 见[回退](#v0250-going-back)。
- **服务负责检查，管理员负责修改。** 服务会校验它打开的每个流和桶，当某个缺失或不正确时，会报错停止，并指出
  是哪个流以及哪里不同。执行 `dcctl broker reconcile <instance>` 可将其恢复，这也能恢复被手工删除的流或桶。
- **随之有两处行为变化。** 缓存桶的存活时间现在固定为五分钟，因为由作业创建它们，不再由各服务请求；
  `device-management` 的缓存存活时间设置现在只限定副本在自己内存中保留条目的时长。另外，实例配置中调高的
  副本数在作业运行时（即下一次升级）生效，而不是在每次 pod 重启时。
- **仅使用 chart 的安装**需要在首次安装之前准备好命名空间和管理 Secret，因为该作业在 chart 创建任何内容之前
  运行。该 Secret 名为 `dci-<instance>-nats-broker-admin`，包含 `user` 和 `password`。`dcctl broker permissions`
  会为你自己运行的消息代理打印授权。
- **从备份恢复会得到新的登录。** 见[灾难恢复](./disaster-recovery.md)。

关于这些登录能保护什么、不能保护什么，请参阅[安全模型](../concepts/security-model.md)。

##### 设备访问令牌以摘要形式存储 {#v0250-access-tokens}

`ACCESS_TOKEN` 凭据的令牌就是它的凭据 id，过去按发送时的样子存储和返回，所以数据库或备份一旦泄露，
每个使用令牌的设备都会得到一个可用的凭据。现在它以带密钥的摘要存储，与 `MQTT_BASIC` 密码已有的方式相同：
对租户和令牌计算 HMAC-SHA-256，密钥由实例根密钥派生。存储形式为 `dctk1$<key id>$<digest>`。

- **已有设备保留它们的令牌。** 无需重新配置任何设备。
- **令牌只显示一次，** 在设置它的调用的响应中（创建、轮换令牌的更新、生成令牌的设备替换），以及控制台的
  横幅中，之后不再显示。读取返回的是指纹，它用于标识该凭据，不能当作令牌出示。
- **要为你没有记录令牌的凭据获得可用的令牌，请设置新令牌：** 在控制台中点击**设置新令牌**，或只带
  `credentialId` 调用 `updateDeviceCredential`。你这样做之后，设备当前的令牌立即无法再建立连接。见
  [密码与令牌只显示一次，以及轮换](../guides/device-credentials.md#show-once)。
- **`deviceCredentials(criteria: {credentialId})` 同时接受令牌和指纹。**
- **把凭据类型改为或改出 `ACCESS_TOKEN` 时，必须在同一请求中指明 `credentialId`，** 因为存储的值在一种类型下
  表示摘要，在另一种类型下表示用户名。没有指明的请求会以 `INVALID_VALUE` 被拒绝。超过 256 字节的令牌会以
  `LIMIT_EXCEEDED` 被拒绝。
- **控制台的连接代码片段**对访问令牌凭据显示 `<access-token>`，就像它们之前对密码显示占位符一样。
- **不同的根密钥会让每个摘要都无法识别。** 与密码摘要一样，恢复到错误根密钥旁的实例，会记录一条错误，
  说明其密钥无法生成多少个已存储的摘要。

##### 边缘代理是网关 {#v0250-edge-gateway}

边缘代理以自己的设备（即**网关**）身份登录平台，并在它自己的命名空间中发布每台本地设备的事件。平台只接受
分配给该网关的设备的事件。这是唯一的上行链路模式：旧的直连设备模式（代理把消息原样转发到云端消息代理）
已经移除。见[网关身份](./edge-services.md#gateway-identity)和
[网关命名空间](../reference/device-protocol.md#gateway-namespace)。

- **代理的配置必须修改。** `agentId` 不再是一个键，残留的该键会让代理在启动时停止，并给出指明以
  `uplink.gatewayToken` 取代它的错误。`uplink.tenant`、`uplink.gatewayToken`、`uplink.username`（形式为
  `{tenant}:{credentialId}`，否则在加载时被拒绝）和 `uplink.passwordEnv` 是必需的。
- **请在代理清空其积压之前创建网关设备并分配其设备。** 旧模式下缓冲的事件会在新的命名空间中转发，对尚未分配的
  设备的事件会被拒绝。缓冲区及其 durable 会就地收窄到 `uplink.tenant`；任何其他租户的缓冲事件不会被转发，
  并留在缓冲区中，直到其环形缓冲区将其淘汰。
- **使用 `assignDeviceGateway` 分配设备，** 或在创建设备时使用 `gatewayToken`，或在控制台设备的**网关**标签页中
  操作。一台设备最多只有一个网关，网关本身不能位于另一个网关之后，仍有设备的网关不能删除。这需要
  `device:write` 权限，并在设备的下一个事件时生效。
- **被拒绝的事件不会悄无声息。** 平台以原因 `NotGatewayChild` 把它记录在失败事件流中，计入
  `resolve_not_gateway_child_total`，并向网关发送通知；代理会记录日志、计数，并把该事件保存在其存储目录的
  `refused-events.jsonl` 中。
- **先升级平台。** 在 `infrastructure.nats.mqttFailClosed` 开启时运行 `v0.24.0` 的 `event-sources`，会确认它
  无法归属的网关消息并丢弃它们，而代理看到的是成功。请先完成每个 `event-sources` 和 `device-management`
  副本以及消息代理的升级，再在本版本上重启任何代理。消息代理尚未升级的代理无法订阅其通知，也不会报告
  `uplink_connected`。见[滚动升级](#rolling-upgrades)下的警告。
- **发往网关之后设备的命令尚未路由。** 代理只转发事件。

##### C# SDK {#v0250-csharp-sdk}

SDK 过去无法识别被拒绝的连接。MQTT 库通过抛出异常来报告非成功的 CONNACK，而 SDK 把它当作普通的传输故障，
所以会话的重连循环会不断重试消息代理已拒绝的凭据，就像网络中断一样。现在对于判定了客户端的 CONNACK
（不支持的协议版本、客户端标识符无效、用户名或密码错误、未授权、被封禁、认证方法错误），它会抛出
`MqttConnectRefusedException`，会话的重连循环会因此停止，并报告该会话失明。"服务器不可用"或"服务器繁忙"
的 CONNACK，以及连接中途失败的套接字，仍然会重试。`MqttConnectRefusedException` 新增一个接受底层异常的
构造函数，这是公共 API 的新增；没有移除任何内容。

对设备程序而言，变化在于：依赖无限重试的程序，现在必须处理这一拒绝，例如在其凭据被撤销或替换，或其访问令牌
被重新设置之后。

##### 首个已签名的发布版本 {#v0250-signing}

每个服务镜像、operator、边缘代理、消息代理管理器和控制台，以及 Helm chart，都使用 cosign 以无密钥方式按摘要
签名。每个镜像还附带已签名的 SPDX SBOM 和 SLSA 构建溯源，chart 附带溯源。签名绑定到发布工作流和标签，所以
来自任何其他工作流、仓库或分支的签名，都不会被验证为发布版本。在 `hack/verify-release-signatures.sh <tag>`
对已推送的内容通过之前，GitHub Release 不会发布。

- **要检查你下载的内容，** 请按[验证发布版本](./verifying-releases.md)操作；该脚本需要 `cosign` 3 或更高版本、
  `jq` 和 GitHub CLI，不需要我们的账号或密钥。
- **`v0.25.0` 之前的发布版本没有签名，** 验证时会报错 "no signatures found"。
- **数据库镜像不在范围内。** 运行事件存储的 operand 镜像由另一个工作流发布，在本版本中未签名。
- **`dcctl` 和边缘代理归档**附带构建溯源，自 [`v0.18.0`](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/docs/deployment/releases-and-upgrades.md#v0180-provenance) 起即是如此。

升级无须任何操作。如何报告问题，请参阅[安全策略](https://github.com/devicechain-io/devicechain/security/policy)。

##### 服务账号 {#v0250-service-accounts}

租户管理员可以在**设置**中、**成员**旁边创建**服务账号**，供不应依赖某个人的刷新令牌运行的脚本和集成使用。
账号只持有赋予它的角色（没有查看者基线），可选地限定于某些客户或组，并通过 OAuth `client_credentials`
授权获得有效期五分钟的普通访问令牌。密钥只显示一次，以带密钥的摘要存储，并会过期（默认 365 天，最长 730 天）；
轮换密钥时，旧密钥在你选定的一段重叠时间内仍然可用，默认 24 小时。一个租户可以持有 200 个账号，账号不能管理
租户。见[服务账号](../user-guide/service-accounts.md)。运维人员的 OAuth 客户端不能使用这种授权，服务账号的凭据
在其他授权类型上不能认证任何内容。

##### 空中更新的设备组发布 {#v0250-rollouts}

`createUpdateRollout`、`startUpdateRollout`、`pauseUpdateRollout`、`resumeUpdateRollout` 和
`abortUpdateRollout` 把一个制品分配给设备组的成员，同时有未完成更新的设备不超过 `maxInFlight` 台（1 到
1,000），控制台中的 **更新 → 发布** 会显示它们。开始发布会冻结该组的成员；超过 10,000 台设备的组会被拒绝。被
分配规则拒绝的设备会带着该规则的代码被跳过，且不占用名额。可选的失败阈值会让发布自动暂停，之后恢复它需要
更高的阈值。中止会取消尚未开始的设备，并让进行中的设备完成。发布需要 `artifact:write` 才能修改，需要
`artifact:read` 才能查看，仅限于租户一部分的成员会被拒绝。见
[向设备组发布](../concepts/over-the-air-updates.md#rollouts)。新增指标为
`update_rollout_dispatches_total`、`update_rollout_transitions_total` 和 `update_rollout_step_failures_total`。

#### 其他变化 {#v0250-what-changed}

##### 已知限制 {#v0250-known-limitations}

- **在第一次升级期间，共享登录仍可被每个 pod 读取。** 这就是两次升级之间的窗口；请尽快执行第二次。
- **有两项消息代理授权按设计更宽：** `event-sources` 保留对消息代理自身 MQTT 存储的更新权限，
  `user-management` 可以跨所有流清除已删除租户的数据，但 `event-sources` 保存其已确认消息持久性的
  消费者除外。
- **网关的命令不会被路由，** 发给网关的通知是尽力而为的，代理保留它所转发内容的一份有界副本（8,192 个事件和
  16 MiB），用于与通知匹配。
- **发布没有分批或金丝雀阶段，** 没有计划，也没有针对停滞的发布驱动器的告警。
- **SBOM 由每个镜像的 `amd64` 变体生成。**
- **`mqttFailClosed` 关闭时（默认值），在 MQTT 捕获流更换 leader 期间，消息代理已确认的 QoS 1 消息可能丢失，
  与负载高低无关，** 且没有任何计数器记录（`JetStreamCaptureLeaderChanged` 会警告发生过切换）。在持续负载下，即使没有更换 leader 也可能丢失：在对默认安装的一次负载测试中，`event-sources` 读取捕获流时落后，该流写满到上限并丢弃最早的未读消息，丢失了 1,357,188 个已确认的事件，约占 MQTT 流量的 7% 到 13%。`devicechain_<area>_jetstream_consumer_unread_skipped_total` 会统计这些丢失，`JetStreamDurableLostUnread` 会告警。`event-sources` 为何落后仍在调查中。消息代理的滚动
  重启会重启每台服务器，包括 leader，而本次升级会滚动消息代理两次，因此除非开启了 fail-closed，否则每次消息代理
  滚动期间都应预期一个短暂的丢失窗口。在本次升级的一次演练中，四台设备合计每秒发送 20 条 QoS 1 消息，11,262 条
  已确认消息中丢失了 11 条，全部发生在消息代理重启期间；更早的一次演练没有丢失。要关闭这个窗口，请开启
  `infrastructure.nats.mqttFailClosed`。本版本的
  `dcctl` 没有对应的选项，`--values` 也不接受它，因此在由 `dcctl` 管理的实例上手动设置的值会被下一次
  `dcctl upgrade` 覆盖；后续版本会为 `dcctl bootstrap` 和 `dcctl upgrade` 增加 `--mqtt-fail-closed`，
  并把这一选择记录在实例上（见 [fail-closed MQTT](./observability.md#mqtt-fail-closed)）。代价是：消息代理的消息存储没有 leader 期间，QoS 1 发布不会被确认，设备必须重新发送；
  而且实例能跟上的流量低于关闭该设置时。在一个测试集群上，只要能跟上，它就没有丢失任何消息（每秒 6,000 和 9,000
  个事件，期间多次更换 leader）；在每秒 12,000 个事件时它跟不上，两台消息代理服务器内存耗尽，已确认的消息丢失。
  请按低于其上限的规模部署。见 [fail-closed MQTT](./observability.md#mqtt-fail-closed)。
- **沿用自 `v0.24.0`：** 计算指标回填会跳过已压缩的历史；消息代理在积压量大时崩溃不在 `mqttFailClosed` 的覆盖
  范围内。

##### 本版本的其他内容 {#v0250-also}

- **`dcctl bootstrap` 和 `dcctl upgrade` 接受 `--values`，** 这是一个调优的 YAML 文件（副本数、资源、某个领域
  自己的配置），会记录在实例上，并由之后的每次升级再次应用。`dcctl` 拥有的键，例如镜像、凭据或端点，会在
  任何改动发生之前按名称被拒绝。`--reset-values` 将其移除，`--dry-run` 打印将发生的变化。见
  [调优实例](./tuning-an-instance.md)。
- **`dcctl broker reconcile <instance>` 和 `dcctl broker permissions`** 是新增的；见上文。
- **实体的审计条目与变更在同一个事务中保存。** 过去它在变更提交之后才写入，所以审计写入失败时，调用方被告知
  变更失败，而变更实际上已经保存，且没有留下记录。现在两者要么都保存，要么都不保存，这意味着审计写入失败
  会让变更失败。登录和认证事件仍是尽力而为。
- **租户删除会显示是什么在拖住它。** `tenant_purge_store_blocked{store,reason}` 按存储系统统计未完成的删除，
  同时还有 `tenant_purge_store_rows_erased_total`、`tenant_purge_store_erase_duration_seconds`、
  `tenant_purge_completed_total`、`tenant_purge_failures_total`、`tenant_purge_duration_seconds` 和
  `tenant_purge_clean_duration_seconds`，以及三条告警。见[租户删除](./tenant-deletion.md#blocked-store)。
- **租户删除在两处更快。** 键值擦除一次运行 32 个清除，而不是一个；S3 对象删除改为批量，每个请求最多 1,000 个
  键。在单节点回环测试中，擦除 63,000 个键从 28.9 秒降到 3.1 秒。批次中任何一个键删除失败，现在都会让这一轮
  失败，并重复进行直到不剩任何内容。
- **写入方会跟随 leader 选举，直到追上。** 路由到流的 leader 的发布者，过去只计算一次新 leader，如果那一轮
  失败就保留旧目标。现在它以逐渐增大的间隔重试，最多 30 秒。这只影响性能。
- **看板。** 看板可以显示一个固定的时间窗口，通过**自定义范围**选择，或从 `?from=&to=` 链接打开，以取代滚动
  范围（见[自定义范围](../user-guide/viewing-dashboards-in-dash.md#custom-range)）。新的历史表格组件把一台设备
  的已存储历史列为各时间桶平均值的行，有可交付内容的组件带有 CSV 或 PNG 的下载按钮。没有内容可绘制的组件
  现在会说明五种原因中的哪一种：未选择设备、此设备未上报、此范围内没有数据、无权访问，或数据源失败。
  `/dash?rotate=tokA,tokB&every=60` 在墙面显示屏上轮播两到 24 个看板，每个 10 到 3,600 秒（见
  [在多个看板之间轮播](../user-guide/viewing-dashboards-in-dash.md#rotating-boards)）。这些全部是增量的：
  已保存的看板的读取和渲染与以前相同。
- **版本与安全文档。** [版本与升级策略](./versioning-policy.md)说明了从 `v1.0.0` 起哪些内容是稳定的，
  [安全模型](../concepts/security-model.md)描述了信任边界以及各由什么来强制执行。滚动升级一节不再声称零停机。
- **每次变更都会运行兼容性检查。** 每个对外提供的 GraphQL schema、`dcctl` 命令和标志、设备线路契约、设备主题
  布局、C# SDK 的公共 API 以及 npm 包的导出，都会与上一个发布版本比较，破坏性变更必须声明。与 `v0.24.0`
  相比，本版本没有声明任何破坏性变更。
- **Sitepulse 展示的演示播放器会在每台设备的凭据上设置自己的令牌，** 因为它已无法读回一个令牌。

### v0.24.0：可自行编写的计算指标，以及承载更多事件的事件路径 {#v0240-upgrade}

`v0.24.0` 可从 `v0.23.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行
`dcctl upgrade`，如 [v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade) 所述。除新的 `--version` 外，还要把集群安装时用的设置
（例如 `--ha` 和 `--database-node-selector`）一并传给 `dcctl install`。**无法从 `v0.22.0` 或更早版本
原地升级到本版本：请先升级到 `v0.23.0`。** 它的四项迁移共新增两张表和两个索引，不改写
任何已有的行（见[升级期间](#v0240-during)）。**升级后只能向前，不能回退：** `v0.23.0` 无法在本版本写入的
配置上启动（见[回退](#v0240-going-back)）。从 `v0.22.0` 或更早版本升级时，也请阅读
[`v0.23.0` 说明](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0230-upgrade)；任何跨越该版本的升级都适用。

命令如下，版本号带 `v`，并先做一次演练：

```bash
dcctl install local --version v0.24.0
dcctl upgrade local devicechain --version v0.24.0 --dry-run
dcctl upgrade local devicechain --version v0.24.0
```

`--version` 取带 `v` 的标签。集群不是默认的本地集群时，请向 `dcctl install` 和 `dcctl upgrade` 传入
`--kube-context`；镜像来自默认之外的镜像仓库时，再传入 `--registry`。如果仍有 MQTT 设备在事件正文中发送
凭据，请在最后一条命令中加上 `--mqtt-body-credential=strip`：见[升级之前](#v0240-before)。
`dcctl upgrade --dry-run` 不会打印 `--mqtt-body-credential` 的取值，所以请在升级后确认它已生效（见
[设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)中的步骤）。如果实例引导时没有根密钥托管，
`dcctl upgrade` 会打印托管警告；这是预期行为，与本次升级无关。

本版本让计算指标成为可以由你自己编写并补全历史的东西：带检查的公式编辑器、每台设备的常量、读取其他
计算指标的公式、预览，以及为指标出现之前已存储的读数计算该指标的**回填**。其余内容围绕一条承载更多
事件的事件路径：事件默认以更小的形式写入，检测结果并行发布，告警变更也同时应用多个。本版本还封堵了两处密钥曾处于所有平台服务都能读取的位置的问题：设备在 MQTT
事件正文中的凭据，以及登录状态。对某些人有影响的变化：

- **设备必须停止在经由平台 MQTT 代理发布的事件正文中发送凭据。** 这样的事件现在会被拒绝。要在更新固件期间让设备群继续上报，请使用
  `--mqtt-body-credential=strip` 升级。见
  [设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)。
- **C# SDK 的 `IDeviceEventCarrier` 新增一个抽象成员** `CarriesBodyCredential`；自定义的承载器在声明它
  之前无法编译。见同一节。
- **所有人需要再登录一次，代理会重启两次。** 登录状态移到代理上自己的账户中且不会迁移，设备认证
  callout 也改为加密。见[代理的登录被收窄](#v0240-broker-logins)。
- **请先升级到 `v0.23.0`。** 事件现在默认以 `compact` 形式写入，`v0.22.0` 及更早版本无法读取。见
  [事件的线路格式](#event-wire-format)。
- **代理在上述第一次重启中升级到 NATS 2.15.1。** 见[代理升级到 NATS 2.15](#v0240-nats)。
- **无法回退到 `v0.23.0`。** 请向前修复：`helm rollback` 会留下无法启动的服务。见[回退](#v0240-going-back)。
- **四项迁移：** `event-management` 新增保存回填作业的表，`device-management` 新增配置文件激活日志表和
  一个索引，`command-delivery` 新增一个索引。见[升级期间](#v0240-during)。
- **`device` 成为计算指标公式中的保留名称。** 见[计算指标](#v0240-calculated-metrics)。
- **对于没有自带时间的 JSON 读数，计算值的 id 会改变。** 见[计算指标](#v0240-calculated-metrics)。
- **NaN、无穷大或过大的数值测量会被拒绝。** 见[测量值](#v0240-measurements)。
- **若干默认值变化：** 检测引擎一次发布 32 条检测结果，`device-management` 在多条通道上应用告警变更，
  MCP 端点限制批量大小并限制每个调用方，GKE 参考模块中服务节点池的机型也不同了。见
  [变化的默认值](#v0240-defaults)。

#### 升级之前 {#v0240-before}

1. **从 `v0.22.0` 或更早版本升级时，请先停下，升级到 `v0.23.0`。** `v0.22.0` 及更早版本只能读取
   `legacy` 线路格式。运行这些版本的 pod 会丢弃 `compact` 消息，或在 `device-management` 中把它标记为
   失败并搁置，因此直接滚动升级到 `v0.24.0` 会丢失新旧 pod 并存期间写入的事件。请先把每个实例升级到
   `v0.23.0`，等所有 pod 都在运行该版本，再升级到 `v0.24.0`。使用 chart 安装（不是 `dcctl`）时，也可以
   在升级期间改为设置 `infrastructure.messaging.wireFormat: legacy`，待所有 pod 都滚动完成后再移除。
   `dcctl` 没有设置它的选项。
2. **如果有 MQTT 设备在事件正文中发送凭据，请使用 `--mqtt-body-credential=strip` 升级。** 在
   `deviceAuthMode: required` 下，每台 MQTT 设备都是如此，因为 `v0.23.0` 会拒绝正文中不带凭据的 MQTT
   事件。所以不能先更新固件：在升级之前就更新的设备，其事件在升级完成之前会被拒绝。执行
   `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip`，然后按
   [设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)中的步骤操作。
   **不要用设置 `deviceAuthMode: optional` 来过渡。** 它会让整个实例接受不带凭据的 HTTP 事件，仅凭设备令牌即可。
3. **如果实例基础设施状态旁的 `terraform.tfvars` 固定了 `nats_chart_version`，请把它升到 `2.15.1` 或
   删除。** 低于 `2.15.0` 的 NATS chart 在生成计划时就会被拒绝。
4. **不要在同一次升级中更改代理的复制份数（`streamReplicas`、`nats_cluster_replicas`）。** 服务器重启
   期间，有的运行 `2.14.4`，有的运行 `2.15.1`，在此期间发起的副本数或放置变更可能卡住。请在升级之前或
   之后进行。见[代理升级到 NATS 2.15](#v0240-nats)。
5. **如果某个已发布的计算指标公式把键为 `device` 的测量指标当作裸标识符读取，请在升级前改写它**，写成
   `double(m["device"])`（或其类型对应的转换）。升级后这样的公式将无法编译，对任何读数都不产生值。
   在某个层级的 `calculatedMetricsPerProfile` 调到 0 以上之前，计算指标一直是关闭的，所以这只影响你已经
   调高过的层级。
6. **如果你自行安装了 `nats` 模块，请让 `jetstream.limits.default_max_consumers` 保持为 `-1`。**
   否则 NATS 2.15 会把每个流限制在 1,000 个消费者，第 1,001 个 QoS 1 或 2 的 MQTT 订阅会被拒绝，而其
   设备仍保持连接。

#### 升级期间 {#v0240-during}

- **运行四项迁移，各在其所属服务首次启动时运行。** 都不写入或改写任何行，被中断后都可以重新运行。
  - `event-management` 创建 `calculated_backfills` 表及其索引。
  - `device-management` 创建 `device_profile_activations`，即记录设备配置文件的哪个版本在何时成为激活
    版本的日志（它从空开始：升级之前的激活不会被重建），以及 `devices (tenant_id, device_type_id)` 上的
    一个索引，供告警表按设备类型分标签页使用。
  - `command-delivery` 创建 `idx_commands_device_time`，供在时间轴上标出某台设备命令的图表标记使用。

  每个索引都用普通的 `CREATE INDEX` 构建，构建期间会持有阻止该表写入的锁，所以对于有数百万行的表，
  请在安静的时段启动该服务。
- **代理会重启；在首次加入加密 callout 的升级中会重启两次。** 第一次重启带来升级到 NATS 2.15.1 和新的
  登录账户；第二次在服务滚动到能够读取加密请求的应答方之后开启 callout 加密（没有设备认证 callout 的
实例无需加密，只重启一次）。使用 `--ha` 时逐个服务器重启，前一个
  Ready 之后才停止下一个；不使用 `--ha` 时，单个服务器重启，每次代理约有一分钟不可用。见
  [代理的登录被收窄](#v0240-broker-logins)。设备会自行重新连接，持久 MQTT 会话会保留其订阅和为它保留的
  QoS 1 消息。只有使用 `--ha` 时，设备重新连接才不会丢失消息。在使用 `--ha` 的 `kind` 上（代理两次滚动重启），升级总共约
  4 到 5 分钟：第一次滚动约 90 秒，其间约 40 秒无任何动作，第二次滚动约 80 秒。**代理滚动期间不要手动重启代理服务器。** 见[代理升级到 NATS 2.15](#v0240-nats)。
- **服务在滚动过程中开始写入 `compact` 事件。** `v0.23.0` 的 pod 两种形式都能读取，所以这两个版本之间
  的滚动更新是安全的。
- **滚动期间撤销设备的权限可能失败，或留下仍打开的会话。** 禁用或删除设备，或更改凭据，现在还会请
  `event-sources` 结束该设备已打开的 MQTT 会话，而只有本版本的 `event-sources` pod 会应答。在还没有这样的
  pod 运行时，更改会被保存，但调用返回错误（“the change was saved, but the device's live broker sessions
  could not be confirmed ended ... repeat the operation”）。由旧版本 `device-management` pod 处理的更改会被
  保存，但不会结束任何会话，与升级前相同。滚动结束后重做这些更改即可；重复执行是安全的，并会再次请求。
- **滚动期间重启的旧版本 pod 起不来。** 在服务开始滚动之前，`dcctl` 会写入本版本的实例配置，而
  `v0.23.0` 在启动时会拒绝它（见[回退](#v0240-going-back)）。在此期间被驱逐、因内存不足被终止或失去所在
  节点的 `v0.23.0` pod 会一直停着，直到新版本替换它，所以请让升级完成，不要手动重启 pod。
- **进入旧版本 `user-management` pod 的登录或令牌刷新可能失败几秒钟，** 因为登录状态正在迁移。重试即可
  成功。
- **对于时刻带有亚微秒位数的读数，旧版本 pod 与新版本 pod 存储的派生值可能带有不同的 id。** 见
  [计算指标](#v0240-calculated-metrics)。

#### 回退 {#v0240-going-back}

**请向前修复。不要回退到 `v0.23.0`，无论是用 `helm rollback` 还是其他方式。** 如果升级中途失败，请排除
原因后用同一个 `--version` 再次执行 `dcctl upgrade`。回退会留下一个无法工作的实例：

- **`v0.23.0` 的服务无法启动。** `dcctl` 把实例配置写入 Secret `dci-<instance>-config`，它位于 chart 的
  release 之外，所以 `helm rollback` 会恢复上一个 release 的工作负载，却让配置保持本版本写入的样子。该配置
  带有 `v0.23.0` 不认识的键，例如 `identityAccount`、`calloutIssuerPublic` 和 `calloutXKeyPublic`，而
  `v0.23.0` 在启动时会拒绝配置中任何它不认识的键。它启动的每个服务都会失败，而且 callout 的签名种子已经
  根本不在该配置中了。
- **设备无法连接。** 代理不属于 chart 的 release，所以它仍停留在 NATS 2.15，并继续加密设备认证
  callout。callout 的种子存放在 `v0.23.0` 不会读取的 Secret 中，因此没有能为代理请求服务的应答方，所有
  设备都会被拒绝。
- **代理也无法回退。** NATS 只支持从 2.15 回退到 2.14.7 或更高版本，而上一个版本运行的是 2.14.4。
- **登录状态不会恢复。** 上一个版本的登录存储桶已被删除，所有人都需要再登录一次。

新增的表和索引本身不会造成妨碍：`v0.23.0` 读取同一个数据库，并且不使用它们。回退到 `v0.22.0` 或更早
版本，需要先设置 `legacy`，并让流中所有 `compact` 消息被消费完（见[事件的线路格式](#event-wire-format)）。

#### 升级之后 {#v0240-after}

- **无需任何操作。** 所有新设置都有默认值。
- **代理的升级是单向的。** NATS 只支持从 2.15 回退到 2.14.7 或更高版本，而上一个版本运行的是 2.14.4。
- **保持 `metrics.enabled` 和 `metrics.alerts` 开启**（默认都开启），以安装新的
  `MqttGatewayStreamUnbounded` 告警。
- **找出仍在正文中发送凭据的设备。** `devicechain_eventsources_total_msg_credential_in_mqtt_body` 按来源统计被拒绝的事件，使用 `strip` 时
  也统计被去掉凭据的事件。更新每台设备的固件或 SDK，并轮换所有被统计到的设备的凭据（见下一节）。

##### 设备必须停止在 MQTT 事件正文中发送凭据 {#v0240-mqtt-body-credential}

平台 MQTT 代理上的设备在连接时通过设备认证 callout 完成认证。已发布的契约还要求设备在每个事件的正文中
重复发送其凭据，而代理在任何平台代码运行之前就把该正文写入了自己的流，所以明文的密码或访问令牌会留在
所有平台服务、任何能访问卷的人以及卷快照都可读取的流中。现在改为拒绝正文中的凭据：

- **经由平台 MQTT 代理发布、正文带有 `credentialType`、`credentialId` 或 `credentialSecret` 的事件会被
  拒绝**，除非你为这次升级把 `capture.bodyCredential` 设为 `strip`（见下文）。它会被计入 `devicechain_eventsources_total_msg_credential_in_mqtt_body`，在凭据被涂黑后存放到 failed-decode 流，且
  绝不转发。代理此前已经确认了该消息，所以拒绝只对你可见而对设备不可见：继续发送旧正文的设备会在自以为
  仍在发布的情况下变得沉默。
- **不带凭据的事件会被接受**，并归属于连接上来的设备。在 `deviceAuthMode: required` 下，所有事件类型
  都会被接受。
- **HTTP 接入以及你作为来源接入的 MQTT 代理不变。** 它们仍要求在正文中带凭据。
- **把被拒绝的设备视为已暴露。** 明文在消息被确认之前的片刻存在于代理上，在有订阅关注期间存在于 MQTT
  网关的存储中，并在压缩之前存在于尚未回收的文件块中。轮换所有被统计到的设备的凭据。
- **先用 `strip` 升级，再更新 SDK 和固件，最后切换为 `refuse`。** `v0.23.0` 会拒绝正文中不带凭据的 MQTT 事件，
  而本版本会拒绝带凭据的，所以设备群无法在升级之前更新固件。过渡办法是 `capture.bodyCredential: strip`，它会接受
  来自平台 MQTT 代理的事件并去掉其中的凭据。按此顺序操作：
  1. 使用 `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip` 升级。事件继续流入，
     每个带凭据的事件都会被计数。该命令的 `--dry-run`
     不会打印此标志的取值；要确认升级已采用它，请查看 `event-sources` 启动时说明该设置为过渡性的警告，或检查
     `devicechain_eventsources_capture_body_credential_strip` 是否为 `1`。使用 chart 安装（不是 `dcctl`）时，请改为设置
     `functionalAreas.event-sources.config.capture.bodyCredential: strip`。
  2. 推出不再在正文中发送凭据的 SDK 或固件。
  3. 观察 `devicechain_eventsources_total_msg_credential_in_mqtt_body`，直到它停止增长的时间超过你最慢那台设备的
     上报间隔。**请边观察边收集设备清单。** `event-sources` 日志中的警告会写明每个仍在发送凭据的设备所属的租户和设备
     （每个 `event-sources` pod 上每台设备每小时一次）；请在这一阶段把这些租户和设备对从日志中复制出来，因为只有这里会点名它们。
  4. 执行 `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=refuse`。此后 `event-sources`
     日志不再点名任何设备，指标也没有设备标签；留下的唯一按设备的记录是解码失败 stream，它保留设备名称并对凭据做了涂抹。
  5. 轮换第 3 步收集的清单中每台设备的凭据（以及第 4 步之后解码失败 stream 点名的设备）。

  `strip` 不会消除暴露：代理在这两种设置生效之前就已存下了正文，所以 `strip` 与 `refuse` 留下的静态明文相同，
  两者都需要第 5 步。`strip` 只作用于平台 MQTT 代理。之后不带该选项的 `dcctl upgrade` 会保留这个值。`strip` 是过渡性
  的，将在 `v0.26.0` 中移除：从该版本起，正文中的凭据一律被拒绝，`--mqtt-body-credential` 标志不复存在，
  只要仍设置着 `capture.bodyCredential`，`event-sources` 就会拒绝启动。升级到 `v0.26.0` 之前请删除该键。

  升级滚动期间，本版本的 `event-sources` pod 可能会在 `device-management` 完成滚动之前就去掉某个事件的凭据。
  `v0.23.0` 的 `device-management` 会拒绝这个已不带凭据的事件，并让它在一分钟后再次投递，总共最多投递五次；
  本版本的 `device-management` 会接受它。因此，只要 `device-management` 在第一个新的 `event-sources` pod 启动后
  约四分钟内完成滚动（正常升级都是如此），就不会丢失任何事件。第五次投递时仍被拒绝的事件会作为未认证事件被放到
  失败事件流中，其中不含凭据。
- **设备上要改什么。** C# SDK（以及基于它的 Unity 包）在本版本中不再通过 MQTT 发送凭据，通过 HTTP
  仍会发送。事件信封的 JSON Schema 现在把这三个成员标注为仅限 HTTP。自定义的 `IDeviceEventCarrier`
  必须声明新增的 `CarriesBodyCredential` 成员（经由 HTTP 的承载器为 `true`，平台 MQTT 为 `false`）；这是
  有意设计的编译错误，而不是默认值。手工编写正文的固件必须去掉这三个成员。边缘代理原样转发正文，本
  版本不支持它向会认证设备的平台代理上行。
- **会话不再比其凭据活得更久。** 连接令牌现在随凭据一同到期（取 12 小时与凭据自身到期时间中较早者），
  撤销凭据、轮换其密钥、禁用或删除设备、或替换设备，都会结束该设备已打开的 MQTT 会话。设备重新连接，
  并用其出示的凭据重新认证，若凭据已被撤销，数据库会拒绝。该设备的所有会话都会结束，所以撤销两个
  凭据中的一个会让另一个凭据上的会话短暂中断。如果平台无法确认会话已结束，更改仍会被保存，调用返回
  说明这一点的错误；请重试。
- **轮换其消息被拒绝或进入死信的访问令牌设备的令牌。** failed-decode 和 dead-letter 记录过去会保留访问
  令牌的 `credentialId`，对该类凭据来说它就是令牌本身（MQTT_BASIC 则是登录名）。现在它们会被涂黑，但
  不会有任何机制改写旧版本写下的记录：这些流 7 天后过期，死信表由其保留清理任务修剪，数据库备份会把它们
  保留到备份过期。
- **只要代理运行设备认证 callout，`event-sources` 就需要代理的系统账户登录才能启动**（`dcctl bootstrap`
  会创建它），并且该登录现在仅限列出和结束连接。`dcctl upgrade` 会拒绝有 callout 却没有系统登录的实例，
  而不是让 `event-sources` 陷入崩溃循环。
- **共享的服务登录不能再向设备的事件主题发布。** 以前持有它的人只需一次发布就能伪造任何设备的事件。
  这并不是对抗持有该登录者的边界，它仍可管理各服务的流。

见[连接设备](../guides/connecting-a-device.md)、[设备凭据](../guides/device-credentials.md)和
[设备协议参考](../reference/device-protocol.md)。

##### 代理的登录被收窄 {#v0240-broker-logins}

两项原本所有服务都能读取的密钥，现在只有一个服务能读取。机制和重启情况见
[升级会对基础设施应用什么](#upgrade-infrastructure)；对某些人有影响的变化：

- **设备认证 callout 被加密。** 设备的密码过去会明文穿过代理，任何监听的服务都能看到。`dcctl upgrade`
  创建 Secret `dci-<instance>-callout`（签名种子和新的加密种子），只挂载到 `device-management`。实例配置
  只保留它们的公开部分。仅用 chart 的安装必须自行创建该 Secret。
- **登录状态移到代理上自己的账户，所有人需要再登录一次。** 刷新令牌、OAuth 授权码、单点登录记录和登录
  限流计数移到只有 `user-management` 能进入的 `IDENTITY` 账户，密码在 Secret `dci-<instance>-identity`
  中。上一个版本的副本会被**删除而不是迁移**，所以升级之前的所有会话都会结束：人们需要在控制台和仪表板
  中重新登录，MCP 和 OAuth 客户端需要重新授权。已签发的访问令牌在到期之前仍然有效，默认 15 分钟。仅用
  chart 的安装必须把该账户添加到自己的代理中，否则没有任何变化。
- **在同时做这两件事的首次升级中，代理会重启两次，** 如上所述。使用 `--ha` 时设备在数秒内重连，不使用
  时约一分钟。如果第二步失败，设备仍能连接；再次执行 `dcctl upgrade` 即可完成。
- **签名种子是迁移而不是替换。** callout 的签名种子过去在实例配置中，`dcctl upgrade` 把同一个种子移入
  新的 Secret。`dcctl` 不把配置放进 Helm 的 values，所以它的发布历史中没有副本。以内联方式提供配置的
  chart 安装，在升级之前 Helm 保存的发布中仍包含该种子；如果你在意，请删除这些发布 Secret。
- **恢复出的实例会得到新的 `IDENTITY` 登录，** 登录状态不会从备份中恢复。
- **在集群中，这些 Secret 是种子的唯一副本。** 执行过 `dcctl bootstrap` 的机器也会把它们保存在
  `~/.devicechain/instances/<instance>/` 中。没有轮换命令，在 `dcctl` 之外编辑任一 Secret 都不受支持。能
  读取实例命名空间中 Secret 的人仍然可以读取它们。

##### 代理升级到 NATS 2.15 {#v0240-nats}

实例的消息代理从 NATS `2.14.4` 升级到 `2.15.1`（官方 `nats` chart，同一版本号）。由 `dcctl upgrade`
在其第一次代理应用中完成，也就是同时添加登录账户的那一次，所以这次升级本身不额外增加重启：在 `--ha`
下其服务器逐台重启，每台 Ready 后下一台才停止；不使用 `--ha` 时，唯一的服务器重启，代理约有一分钟不可用。
开启 callout 加密的第二次重启在服务滚动之后进行（见[升级期间](#v0240-during)）。设备会自行重连，持久 MQTT 会话会保留其订阅，以及设备离线期间
为其保留的 QoS 1 消息。

- **不要在同一次升级中更改代理的复制设置。** 服务器重启期间，部分运行 `2.14.4`，部分运行 `2.15.1`。
  NATS 2.15 改变了流或消费者更改副本数或放置位置的方式，只有 NATS `2.14.7` 及更高版本理解这一新机制。
  本次升级从 `2.14.4` 开始，因此只要还有服务器运行它，期间发起的副本数或放置更改就可能卡住，而不只是
  被推迟。整个过程中流始终可读可写，创建或删除流也不受影响。请在本次升级之前或之后提高
  `streamReplicas` 或 `nats_cluster_replicas`，不要在升级期间进行。
- **升级滚动期间不要手动重启代理服务器。** 新配置会同时到达所有服务器，但每台服务器只有轮到它时才会
  换成 `2.15.1`。在此之前它继续以启动时的配置运行；如果它在这段时间内重启（手动重启，或其节点故障），
  就无法以新配置启动，并会一直停机，直到升级替换它。在 `--ha` 下，这会让集群在剩余的滚动过程中少一台
  服务器，并且在下一台服务器重启期间，多副本流无法写入。
- **拒绝早于 `2.15.0` 的 NATS chart。** 代理配置现在按 NATS 2.15 编写，2.14 服务器在启动时会拒绝它，
  因此低于 `2.15.0` 的 `nats_chart_version` 会让计划失败，而不是让代理陷入反复重启。如果实例基础设施
  状态旁的 `terraform.tfvars` 固定了 `nats_chart_version`，请在升级前将其提高到 `2.15.1` 或删除它。
  请将升级到 2.15 视为单向操作：NATS 只支持从 2.15 回退到 2.14.7 或更高版本，而上一个版本运行的是 2.14.4。
- **取消 NATS 2.15 为每个流新增的 1,000 个消费者上限。** 每个 QoS 1 或 2 的 MQTT 订阅都是代理自身消息流
  上的一个消费者，因此在新的默认值下，整个设备群中第 1,001 个此类订阅会被拒绝，而其设备仍保持连接，
  永远收不到命令。计数的是订阅而不是设备：一个持有两个 QoS 1 订阅的设备占用两个。如果您自行安装
  `nats` 模块，请保持 `jetstream.limits.default_max_consumers` 为 `-1`。

改进之处：

- **同一 MQTT 连接上的 QoS 1 发布不再互相等待。** 代理现在可同时存储一个连接的最多 1,024 条 QoS 1
  消息，而不是逐条存储。在一个三台服务器的测试集群上，单个连接发送 5,000 条消息，每秒确认数从约 1,100
  提升到约 20,000。
- **重新加载代理配置不再中断对已连接设备的 QoS 1 和 2 投递。** MQTT 监听器启用 TLS（默认如此）时，
  每次重新加载都会重新应用 MQTT 设置，导致每个已连接会话在重连之前都收不到 QoS 1 或 2 消息，包括命令。
  证书续期就可能触发一次重新加载。
- **设备登录和权限方面的修复**，其中包括代理如何判断哪些登录跳过设备登录检查，以及登录被拒绝现在报告为
  身份验证错误。
- **边缘代理的本地代理不再拒绝站点的第 1,001 个 QoS 1 或 2 订阅。** `dc-edge-agent` 内嵌 NATS 2.15，
  从 `v0.20.0` 到 `v0.23.0`，其本地 MQTT 监听器也有同样的 1,000 个消费者上限：站点的第 1,001 个 QoS 1
  或 2 订阅会被拒绝，而设备仍保持连接。这一上限在那里也已取消。如果某个站点的设备在其监听器上合计持有
  超过 1,000 个此类订阅，请升级该站点的代理。

##### 计算指标 {#v0240-calculated-metrics}

在某个层级的 `calculatedMetricsPerProfile` 调到 0 以上之前（该设置自 `v0.23.0` 起提供），计算指标保持
关闭。本版本新增的内容，凡是已开启计算指标的地方都可使用：

- **在控制台中编写。** 设备配置文件的“指标”标签页有“类型”开关（测量或计算）和带检查的**公式**编辑器。
  草稿由发布检查和解析器所用的同一个编译器编译，并显示其成本相对于上限 100 的占用。
  [用户指南](../user-guide/calculated-metrics.md)有专门一章。
- **每台设备的数值常量。** `device.calibration` 或 `device["ct-ratio"]` 读取设备元数据中的一个数字。
  只有 JSON 数字才算；字符串、布尔值、null、对象或列表属于“存在但不可用”，不会被强制转换。
  **`device` 现在是保留名称**：键为 `device` 的测量指标要写成 `m["device"]` 读取，并附上其类型转换。
  以前把这样的指标当作裸标识符读取的已发布公式将无法编译：它对任何读数都不产生值，会在
  `devicechain_devicemanagement_calcmetric_skipped_total` 中计为 `eval_error`，并记录在
  `device-management` 的日志中。设备在该计算指标的键下发送的值仍会被丢弃。
- **链式引用。** 公式可以读取另一个计算指标，最多四层。形成环（`a -> b -> a`）或超过四层的链会在发布时
  被拒绝，并指出这条链。
- **预览。** `previewRule` 接受草稿配置文件的指标，并按草稿重新推导计算值；控制台里的“用近期历史试一试”
  面板则在设备已存储的读数上评估草稿公式。
- **回填。** 为其版本发布之前已存储的读数计算某个计算指标，并写入缺失的值的作业。见
  [回填历史数据](#v0240-backfill)。
- **Sparkplug 和 LwM2M 设备的指标现在会被计算。** 每个条目只带一个指标的设备（Sparkplug、LwM2M 或 JSON
  批量）过去会让所有多输入公式因 `missing_input` 被跳过，因为每个条目都被当成一次独立的读数。现在，
  一次读数是同一条消息中带有完全相同时刻的所有测量，所以 `power = voltage * current` 在两者共用同一
  时刻时就会被计算。Sparkplug 和 LwM2M 的时间戳精确到毫秒，因此逐个给指标打时间戳的节点（数值相差几
  毫秒），或只上报变化内容的节点，其多输入公式仍然得不到值。如果同一时刻某个指标出现两次，公式读取
  第一个。同一个 SenML 数据包里的 LwM2M 读数现在共用同一个接收时刻。用户指南说明了
  [一次读数是如何构成的](../user-guide/calculated-metrics.md#what-a-calculated-metric-is)。
- **同一时刻同名的两个计算样本只保留一行存储记录**，即先写入的那一行。
- **派生值的 id 现在跟随其以 UTC 表示、精确到微秒的时刻。** 时刻精确到微秒的读数（所有 Sparkplug 和
  LwM2M 读数，以及自带时间、小数位不超过六位的 JSON 读数）id 不变。带有亚微秒位数的读数会得到新的派生
  id，其中包括所有没有自带时间的 JSON 读数，因为平台会以纳秒精度把接收时刻作为其时间。如果同一条读数
  在升级后再次被存储，原因是事件存储再次收到了它（滚动更新时它尚未被确认），或设备重发了升级之前的读数，那么它的派生值
  会作为第二行存储。原始行不受影响，旧行也不会被改写。
- **不带公式任何输入的消息不再被计为跳过。** 对于把两个量分在不同消息中发送的设备，电池公式过去会为每条
  温度消息记一次 `missing_input` 跳过（每秒一条消息时约每小时 3,600 次）。这样的消息现在直接不被计算，
  而带有部分输入但不是全部输入的消息仍计为 `missing_input`。升级后，带 `missing_input` 的
  `devicechain_devicemanagement_calcmetric_skipped_total` 和相应的日志行会下降，所以基于它的仪表板或告警
  读数会变低。已存储的值不变。
- **读取设备常量的公式现在能对使用凭据认证的设备（MQTT 登录或访问令牌，任何传输方式）的实时读数进行
  计算。** 过去它们对每条这样的读数都以 `missing_input` 被跳过，而从请求中获取常量的控制台预览却能正常
  工作。你修改的常量会在下一条读数生效。
- **在控制台中，** 设备配置文件的“版本”标签页会针对激活版本显示每个计算指标在最近 24 小时或 7 天内
  是正在计算、没有计算还是没有输入读数（根据已存储的读数统计）；当实例无法运行回填时，回填会预先被
  禁用并给出原因（`calculatedMetricBackfillAvailability`）；`calculatedMetricPlan` 会标出名称同时被另一个
  配置文件的激活版本使用的指标（`nameSharedWithOtherProfiles`）。
- **设备配置文件会记录哪个版本在何时成为激活版本。** `DeviceProfile.activations` 按从新到旧列出发布和
  回滚。日志从升级时开始；此前发布的配置文件在下一次发布或回滚之前没有条目，条目缺失表示未知，而不是
  未激活。

##### 回填历史数据 {#v0240-backfill}

回填会把计算指标补入定义它的版本发布之前已存储的历史，使过去时段的图表和导出也能显示它。凡是有计算
指标的地方都可使用；除了已发布且含有计算指标的配置文件版本外，无需再开启任何东西。

- **启动和取消回填需要 `device:write`；读取和列出作业需要 `event:read`。** 受限于客户的调用方会被这四个
  操作全部拒绝。控制台从配置文件的“版本”标签页启动回填，并在你发布新增了计算指标的版本后提示一次。
- **作业有边界。** 每个作业最多 90 天，且不晚于当前时刻，针对一个配置文件的某个已发布版本，最多指定
  1,000 台设备，或覆盖该配置文件的所有设备。早于保留期允许范围的区间会被拒绝，而不是被缩短。每个租户
  同时只有一个活动作业（排队中或运行中）。
- **只新增值，且静默进行。** 已存储的值从不改变。回填的值会出现在仪表板、历史和导出中；它不会改变设备
  的最新值，检测规则也永远看不到它。
- **同一输入在一条读数中出现两次且值不同时，该读数会作为 `ambiguous` 跳过**，而实时计算读取第一个值。
  作业按原因统计每一次跳过。
- **本版本会跳过已压缩的历史**（事件存储默认压缩 7 天以前的块），作业会统计被跳过的内容。
- **每个实例由一个工作进程以受限速率运行作业。** `event-management` 配置中的
  `calculatedBackfill.enabled` 有三种状态。未设置（默认）时，连接池在容纳持久化写入器之余还有空间，
  工作进程就运行；没有空间时则保持关闭，并在启动时给出点名相关设置的警告，因此在本版本之前调优过的
  实例照常启动。`true` 表示连接池容纳不下工作进程时，服务在启动时停止。`false` 关闭工作进程，此时启动
  回填会因“不可用”而被拒绝，并给出原因。`calculatedBackfill.rowsPerSecond` 默认为 20000，范围 1 到
  200000，该默认值是暂定的。见[计算指标回填](./observability.md#calculated-backfill)（其中还列出了
  相关指标，以及取消卡住作业的 SQL），以及用户指南中的
  [回填历史数据](../user-guide/calculated-metrics.md#backfilling-history)。

##### 测量值：NaN、无穷大和过大的值会被拒绝 {#v0240-measurements}

数值测量如果是 NaN、无穷大，或绝对值达到 1,000,000,000,000 及以上，现在会在事件被解析时被拒绝。

- 对于**配置文件中已声明**的指标，整个事件会被作为无效事件拒绝。以前，1e12 及以上的值就已经会从历史中
  丢失（列拒绝了它）；现在它还会被挡在实时状态和规则评估之外，NaN 也不会再绕过指标的最小值和最大值。
- 对于**未声明的键**，只丢弃那一个值，并在 `device-management` 日志中给出警告。以前是整个事件存储失败。
- NaN 或无穷大的值不再导致事件存储对该消息重试约五分钟（五次投递），然后把它报告为下游失败。

##### 变化的默认值 {#v0240-defaults}

| 设置 | 原值 | 现值 | 恢复原有行为 |
| --- | --- | --- | --- |
| `infrastructure.messaging.wireFormat` | `legacy` | `compact` | `legacy`（仅限 chart 安装；见[升级之前](#v0240-before)） |
| `event-processing` 的 `derivedPublishing.maxInFlight`（新增） | 一次一条检测结果 | 32 | 设为 `1`。每台设备仍最多只有一条未完成的检测结果，因此每台设备的检测结果按顺序到达。无重复的重新发布在派生事件流 30 分钟的去重窗口内成立。 |
| `device-management` 的 `raiseAlarm.workers`（新增，固定设置，启动时读取） | 一次一个变更 | `max(1, min(8, pool - resolution.workers - 4))`，默认连接池为 20 时即 6 | 设为 `1` 即恢复原来的串行路径。同一条告警的变更始终按顺序应用。显式给出的值会按连接池校验，绝不会被调整。 |
| `mcp` 的 `maxBatchEntries`、`callsPerSecond`、`callsBurst`（位于 `functionalAreas.mcp.config`） | 无上限 | 16、20 和 40 | 调高它们；`callsBurst` 不能小于 `maxBatchEntries`。 |
| `event-sources` 的 `capture.bodyCredential`（新增，固定设置，位于 `functionalAreas.event-sources.config`） | 无：平台 MQTT 代理上正文中的凭据会被接受 | `refuse` | 已在 `v0.26.0` 中移除：正文中的凭据一律被拒绝。见[设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)。 |
| GKE 参考模块的 `services_machine_type`（`deploy/gke`） | `n2-custom-4-8192` | `t2d-standard-4` | 显式设置。更改现有节点池的机型会重建其节点。 |

- **检测：** 不同设备的检测结果并行发布，一次最多 32 条，而不再逐条等待确认。在一个 GKE 测试集群上，以每秒约
  2,500 条检测结果的触发密集型负载测试，窗口为 32 时每秒发布 2,441 条检测结果，窗口为 1 时为 528 条，
  水位线滞后为 5.1 秒，而不是 139 秒。字节预算 `derivedPublishing.maxBufferedBytes` 现在统计每一份
  已准备好但尚未确认的载荷。见[检测引擎页面](./detection-engine.md#configuration)。
- **告警：** `device-management` 过去在其读取循环中逐个应用 raise-alarm 变更，在本地数据库上无论服务多么
  空闲，每秒也只有大约一百个。现在它在 `raiseAlarm.workers` 条通道上应用，同一条告警的变更进入同一
  条通道。在本地测试中，8 条通道时的速度是原来的四到七倍。默认值会在解析器之外再留出四个连接池连接；
  两组工作者共用连接池，请一并规划。见
  [告警的应用速度](./detection-engine.md#how-fast-alarms-are-applied)。
- **MCP：** 超过上限的批量请求会被整体拒绝（HTTP 400，JSON-RPC 无效请求，不执行任何内容），超过速率的
  调用方会收到带 `Retry-After` 的 HTTP 429。N 条消息的批量按 N 条计，调用方是已验证令牌的主体（租户
  和邮箱）。见 [MCP 服务器的限制](../concepts/mcp.md#limits-and-boundaries)。
- **GKE：** 在 3+3 的 HA 安装上，T2D 服务节点每秒持久化的事件多约 18%（约 17,200，对比 14,500 和
  14,700；通过 HTTP 提供 18,000），在每秒 9,000 个事件时 MQTT QoS 1 确认的 p99 为 38 毫秒，对比 578 和
  171 毫秒。备选的 `n2d-custom-4-8192` 在噪声范围内与 N2 机型相当，每个节点约便宜 13%。`T2D_CPUS` 是
  独立的配额，并非每个可用区都提供 T2D（`us-east4-a`、`-b` 和 `-c` 提供）。数据库节点池仍为
  `n2-standard-4`。

##### MQTT 故障关闭确认 {#v0240-mqtt-failclosed}

`infrastructure.nats.mqttFailClosed` 现在会被配置接受（`v0.23.0` 会拒绝它），并且**默认仍然关闭**：
除非你设置它，否则什么都不会变。开启后，QoS 1 的 PUBACK 表示平台已持有该消息。代理自己的 MQTT 消息存储
（`$MQTT_msgs`）被声明为有界且满后丢弃新消息，所以存储写满时会拒绝 QoS 1 发布（设备收不到 PUBACK 并
重发），而不是丢弃已经确认过的消息。`event-sources` 从存储中转发，滚动更新期间存储也能在 pod 之间安全
移交。存储接近写满时会让设备退避：存储达到 80% 时，`device-management` 拒绝新的 MQTT 连接，直到降到
60% 以下，设备看到的是 CONNACK 代码 5（未授权），因此固件必须在遇到它时继续重试。在同时服务另一个实例
的代理上，该设置会被拒绝。

限制已在[可观测性](./observability.md#mqtt-fail-closed)和[传输矩阵](../reference/transport-matrix.md)
页面中说明：QoS 0 从不被存储；积压很大时代理崩溃不在保护范围内；开启后 PUBACK 更慢（一次测试中每秒
6,000 个事件时 p99 为 336 毫秒，关闭时为 35 毫秒）；每秒接近 10,000 个事件时，两种模式下内存余量都很
紧，因此如果某个服务器被 OOM 终止，请调高 `nats_memory_limit`。

##### 新安装中 MQTT 网关的流是有界的 {#v0240-gateway-streams}

在全新的 `v0.22.0` 或 `v0.23.0` 安装中，MQTT 网关的 `$MQTT_msgs`、`$MQTT_out` 和 `$MQTT_qos2in` 流是
在第一个 MQTT 设备连接时由代理创建的，没有大小限制，并一直保持无界，直到 `event-sources` 重启。无界的
`$MQTT_msgs` 可能写满代理的卷。现在每个 `event-sources` pod 每 30 秒检查一次这些流并设置上限，所以在已有
且正在运行的实例上，新 pod 一启动它们就变为有界。新告警 `MqttGatewayStreamUnbounded`（警告，10 分钟）
会在无法设置上限时触发，通常是因为代理没有剩余存储可供预留。新指标：
`devicechain_eventsources_mqtt_gateway_stream_unbounded{stream}` 和
`devicechain_eventsources_mqtt_gateway_stream_bounded_late_total{stream}`。

#### 其他变化 {#v0240-what-changed}

##### 已知限制 {#v0240-known-limitations}

- **沿用自 `v0.23.0`：** 在 `mqttFailClosed` 关闭时，重负载下 MQTT 捕获流更换领导者，可能丢失已确认的
  QoS 1 事件且不被计数（`JetStreamCaptureLeaderChanged` 会发出警告）。文档记载的 MQTT 上限在三个 4 vCPU
  节点上约为每秒 6,000 个事件，受限于代理。
- **积压很大时代理崩溃，不在 `mqttFailClosed` 的保护范围内。** 在一次每秒 9,000 个 QoS 1 事件的负载测试中，
  在存储的读取器被加快之前，三个 NATS 服务器中有两个内存耗尽，约三分之一已确认的事件始终没有到达事件
  存储。尚未查明它们丢失在何处。该设置是可选的，默认关闭。
- **回填会跳过已压缩的历史**，其默认速率（`rowsPerSecond`）也尚未在有负载的实例上测量过。

##### 本版本的其他内容 {#v0240-also}

- **`dcctl upgrade` 在记录新版本之前先检查对象存储的挂载。** 会被第二个 pod 挂载的 ReadWriteOnce 存储，
  在记录版本之前就会被拒绝，与 `--enable-area` 已有的行为一致。“recorded binding”消息不再重复两次上下文
  名称。
- **`sendCommand` 规则动作可以限定命令等待设备的时长。** 可选的 `expiresAfter` 接受 `1m` 到 `168h` 的时长；
  不设置时命令仍按 7 天的默认值等待。超出范围或无法读取的值会在保存或发布规则时被拒绝，已存储规则中的
  无效值会被丢弃（以 `invalid_action` 为原因计数），而不是不带限制地发送。
- **仪表板与组件。** 告警表可以按设备类型把告警分成标签页，每页有各自的计数和最高严重级别；统计卡片可以
  显示趋势迷你图；新的设备群计数卡片统计一个设备组中的设备或其中需要关注的设备，并排除其配置文件未声明
  该测量的设备；设备状态卡片显示一台设备是否在线以及上次被看到的时间，在线环形图把一个设备组划分为在线、
  离线和尚无状态（两者都读取平台记录的设备状态，需要读取设备状态的权限）；命令滑块新增
  步进模式；点击地图标记会选中设备，链接可以打开已绑定某台设备的看板；告警严重级别细分按严重级别显示
  活动告警；时序图可以在时间轴上标出告警和命令，并通过 `markAlarmsScope` 选项限定为与其绘制的测量有关
  的告警（新图表默认限定；没有保存该选项的图表会标出设备的所有告警），还可以用 `groupBy: device` 为一个
  组中的每台设备画一条线（默认 5 条，最多 12 条）；组件编辑器会列出设备的指标并标出计算指标；设备类型“配置文件”标签页上的 **Create dashboard**
  会根据配置文件声明的内容生成看板草稿。这些都是增量的：已保存的仪表板读取和渲染方式与之前相同。见
  [用户指南](../user-guide/dashboards.md)。
- **控制台：表单不再保存到错误的实体上。** 切换注册表、租户、层级、角色或设备之后，表单可能保留上一个
  实体的数据并把它保存到新实体上。现在控制台的所有查询都把用于显示的数据与用于操作的数据区分开。
- **GraphQL。** `event-management` 上的占位变更 `_empty` 已移除，因为该服务现在有了真正的变更（启动和
  取消回填）。调用过 `_empty` 的客户端会收到未知字段错误。
- **检测仪表板。** 两个延迟面板（规则健康写入以及派生发布确认的 p50 和 p99）加入规则健康写入面板。
- **持续失败的任务不再刷满日志。** 周期任务（例如每秒运行一次的检测 tick）的“Maintenance pass failed”
  日志行，在一连串失败开始时记录一次，之后最多每分钟一次并附带被省略的行数，任务恢复时再记录一次。每个
  Sparkplug 来源各自记录自己的失败序列，因此一个失败的来源不会被健康的来源掩盖。此前没有记录
  各轮耗时、结果和上次成功时间的七个循环现在会记录，使用现有的
  `devicechain_<area>_<task>_passes_total` 系列。
- **流和存储桶的访问按服务声明。** 每个服务只能打开声明为可读或可写的流和键值存储桶，如果请求其他的，
  会拒绝该调用并指出流的名称和修复办法。调用方已放弃的代理启动（关停）不再为每个流花费约一分钟重试。
- **规则健康。** 规则健康的触发计数器改为在检测循环之外批量写入（在 PostgreSQL 上每条已发布的检测结果
  约节省 2.6 毫秒），检测仪表板新增一个面板，展示 `detect_rule_stat_write_failures_total` 和新的
  `detect_rule_stat_admission_contended_total`。
- **设备所有权。** 所有权事实的时间取自数据库的时钟，反复重新入队的设备在积压队列中保持其位置，因此不会
  再被饿死。
- **重新创建的故障关闭持久消费者从存储的第一个序号开始，** 因此被删除的消费者不会留下已确认却未读取的
  消息（仅在开启 `mqttFailClosed` 时）。目标流的去重窗口会折叠重复项，超出窗口的重复项会被再次存储。
- **客户用户。** 受限于部分客户的成员又能看到自己的“更新”列表，客户的“用户”标签页在成员超过 10,000 名时
  显示前 10,000 名并给出提示，而不再报错。
- **Go 1.27.3。** 工具链升级到 Go 1.27.3，其中包含对一项 `crypto/x509` 安全公告的修复。
- **更换实例根密钥意味着重建实例。** 没有轮换命令；见[灾难恢复](./disaster-recovery.md#replace-root-key-losses)。
- **从源码构建。** C# SDK 现在使用 `global.json` 中固定的 .NET SDK（8.0.425）构建，因此运行器镜像更换补丁
  版本时，锁定的还原不再失败。
- **文档。** 新增计算指标的用户指南章节（英文、西班牙文和中文），以及快速入门中的托管选项、MQTT CA 提取
  和本地 TLS 测试。

## 较早版本 {#older-releases}

发布新的次版本时，最早的详细版本会移入此表；每个“详情”链接打开该版本在其标签下的完整说明。

| 版本 | 摘要 | 是否需要操作 | 详情 |
| --- | --- | --- | --- |
| <a id="v0230-upgrade"></a>v0.23.0 | 带范围限制的客户用户，以及丢事件更少的 MQTT 接入 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0230-upgrade) |
| <a id="v0220-upgrade"></a>v0.22.0 | 仪表板改用方形单元格网格，事件路径等待更少 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.22.0/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0220-upgrade) |
| <a id="v0211-upgrade"></a>v0.21.1 | 补丁，无需操作 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.21.1/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0211-upgrade) |
| <a id="v0210-upgrade"></a>v0.21.0 | 设备可被整体停用，更新带签名 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.21.0/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0210-upgrade) |
| <a id="v0200-upgrade"></a>v0.20.0 | 通过你自己的身份提供商登录，组件以包的形式发布 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.20.0/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0200-upgrade) |
| <a id="v0190-upgrade"></a>v0.19.0 | 按每秒 6,000 事件实测定规格，满流拒绝新事件 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-upgrade) |
| <a id="v0180-upgrade"></a>v0.18.0 | 让静默失败可见，接入吞吐跟上限额 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/docs/deployment/releases-and-upgrades.md#v0180-upgrade) |
| <a id="v0170-upgrade"></a>v0.17.0 | 实例不再拥有所在集群 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade) |
| <a id="v0160-upgrade"></a>v0.16.0 | 设备必须指定所答复的派发 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.16.0/docs/docs/deployment/releases-and-upgrades.md#v0160-upgrade) |
| <a id="v0150-upgrade"></a>v0.15.0 | 更新不再删除未发送的内容 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.15.0/docs/docs/deployment/releases-and-upgrades.md#v0150-upgrade) |
| <a id="v0140-upgrade"></a>v0.14.0 | 构建所依赖的包 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.14.0/docs/docs/deployment/releases-and-upgrades.md#v0140-upgrade) |
| <a id="v0130-upgrade"></a>v0.13.0 | 围栏限制成为套餐设置 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.13.0/docs/docs/deployment/releases-and-upgrades.md#v0130-upgrade) |
| <a id="v0121-upgrade"></a>v0.12.1 | 补丁，无需操作 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.12.1/docs/docs/deployment/releases-and-upgrades.md#v0121-upgrade) |
| <a id="v0120-upgrade"></a>v0.12.0 | 改变约定的升级 | 是 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.12.0/docs/docs/deployment/releases-and-upgrades.md#v0120-upgrade) |
| <a id="v0110-upgrade"></a>v0.11.0 | 恢复普通升级 | 否 | [完整说明](https://github.com/devicechain-io/devicechain/blob/v0.11.0/docs/docs/deployment/releases-and-upgrades.md#v0110-upgrade) |

## 数据持久性 {#data-durability}

数据库层刻意与应用**生命周期独立**。两个数据库作为带销毁保护的独立基础设施提供，因此升级、重新安装或卸载*应用*从不触及数据库。这是常见情况，也是安全的。

:::caution 从基础设施配置移除数据库是另一种操作
保护规则只在数据库*位于*基础设施配置时保护它。一旦从配置中*移除*便不再保护：移除的资源不再受配置声明规则覆盖，移除计划会成功。数据库集群还拥有自己的卷，因此移除数据库会带走数据，而不会留下未挂接的卷。

不要通过编辑基础设施配置移除数据库来替换它。

升级在数据库迁移到 Operator 之前创建的实例，是会遇到此问题的情况，计划阶段会拒绝而不冒险。先导出两个数据库，再用 `--allow-legacy-db-removal` 重新运行 `dcctl install` 处理关系数据库，并带此参数 bootstrap 处理事件存储——此参数只是声明你已处理数据，不验证任何内容。对于本地实例，重建更简单，并刻意丢弃数据：销毁实例、重建集群、再 install 和 bootstrap，见 [v0.16.0 及更早构建的实例](#pre-declaration-recreate)。
:::

这是运行卷的持久性，不能替代生产基础设施提供的计划备份和时间点恢复。参见[部署与 Operator](./kubernetes-operator.md)，了解基础设施层与应用层如何分离。
