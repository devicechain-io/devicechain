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

:::warning 本节描述 v1.0.0 之前的发布

`v1.0.0` 是首个稳定版本，[版本与升级策略](./versioning-policy.md)自该版本起适用。在 **v1.0.0** 之前，任何发布，包括补丁发布，都可能改变 API、Schema 或行为，不提供兼容层。
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

运行 `dcctl install` 时，请使用安装该集群时的同样设置，例如 `--ha` 或 `--compact`。集群上有实例运行时，它会拒绝改变这些设置；参见[重新运行安装](./bootstrap.md#re-running-install)。

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

你通过文件交给 `dcctl` 的凭据，即 [LwM2M 预共享密钥](./edge-services.md#lwm2m-identities)和 [Sparkplug 代理密码](./edge-services.md#sparkplug-sources)，保存在 `dcctl` 自己写入、位于 Helm 发布之外的 Secret 中。升级不会触碰它们，也不会从发布中读取任何机密内容。早期版本配置了 LwM2M 密钥的实例，其第一次升级会在发布变更之前把密钥迁移到专属 Secret；Helm 历史记录中更早的版本仍保存着旧的值，因此如果这对你有影响，请在升级后轮换密钥。你手动设置在发布的 `lwm2m-ingest` 块上的配置不再随升级保留（只保留身份及其投射）；请把它们放进通过 `--values` 传入的[值文件](./tuning-an-instance.md)。 移除密钥（撤销时则是整个 Secret）要等到发布成功之后，因此失败的升级不会让仍在运行的 Pod 在容器重启时缺少它所读取的密钥；如果发布成功而只有这一步清理失败，`dcctl` 会给出警告并附上完成它的命令。

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

滚动重启代理服务器后，最后重启的那台不领导任何流或消费者，其余服务器领导全部；在实测负载下，这使发布确认延迟增加了约一倍。服务滚动完成后，触及过代理的升级会请求每个负载过高的流和消费者的领导者把领导权交给一个已同步的副本，并打印每台服务器在前后各领导多少。移动逐个进行，每次都等新的领导者同步后再进行下一个。已经向设备确认过的设备数据所在的流被刻意排除在重新均衡之外：代理自带的 MQTT 流、设备采集流，以及摄入服务写入的所有流。这些流上的领导者变更曾丢失已告知设备“安全”的消息，任何延迟收益都不值得，也没有包含它们的选项；它们的不均衡会在下一次代理滚动时或自行消除。该步骤有时间上限且尽力而为：无法完成时只给出警告，升级仍然成功。`dcctl broker rebalance <instance>` 可按需运行同一步骤，用于以其他方式重启过的代理。在重新均衡之前，承载每个已解析事件的流会保持在不领导设备采集流或入站流的服务器之外（仅当它们的领导者落在同一台服务器时才会移动，仅靠重新均衡可能让它们留在一起）。摘要的末尾会写出这些流由谁领导：已解析事件的流显示为 “separated”；当没有任何已同步的副本位于空闲服务器时显示 “SHARED”（服务器数量少于这类流、空闲服务器上没有它的副本，例如五台服务器配三个副本，或副本落后）；移动失败或被跳过时显示 “NOT CONFIRMED”。设备采集流和入站流从不移动，因此当同一台服务器同时领导二者时，该行只是如实说明，并会在下一次代理滚动时消除。

### 升级还检查什么 {#upgrade-checks}

升级同时检查两件事，因为版本升级可靠地发生在运行实例上，而日历并不保证执行检查。

**代理证书。**消息代理使用有效期一年的证书，由 `dcctl` 在 bootstrap 时创建并保存在集群的 CA 签发。
升级在最后 30 天内，或证书不再覆盖代理互连所用全部名称时重新签发。
后者会发生在实例扩容时，即使证书离过期还很远。
签发使用**同一** CA，无需重新建立信任。仅证书变化时**不会**重启代理：代理原地切换证书，升级随后最多等待五分钟，直到每个代理都报告新证书的到期时间。
若某个代理在此期间未切换，升级会给出警告并重启代理，确保没有代理继续提供旧证书。
若变化不止证书（CA 或服务器数量变化），则立即重启代理；若升级同时更改了代理镜像或配置，则该变更本身就会滚动代理。
条件不满足时，检查运行但不修改。

该证书由 `dcctl` 创建并续期，而不是 cert-manager；cert-manager 只签发实例的网页证书，与代理证书无关。
代理证书位于实例命名空间的 `dc-nats-tls` Secret 中，由 `dcctl upgrade` 重写。
代理在同一 CA 下获得新证书后，会向新连接提供它，而无需重新加载配置，因此证书变更本身不会断开代理已有的 MQTT 会话。
升级确实进行的重启是普通的滚动重启，一次一个代理，与任何代理滚动一样会让设备重新连接。

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

### 事件信封 {#event-envelopes}

事件信封把同一租户的多个事件放进 resolved-events 流（对于原样接收的事件则是 inbound-events 流）中的一条消息，而不是每个事件一条消息，从而减少代理为每个事件所做的工作。它由两个设置控制，**两者默认都关闭**：`infrastructure.messaging.envelopes.resolved`（device-management 把已解析的事件写成信封）和 `infrastructure.messaging.envelopes.inbound`（event-sources 把入站事件写成信封，最多等待 `inboundLingerMillis` 毫秒，默认 5，最大 10，以等待繁忙租户的第二个事件）。在值文件的 `instance.config.infrastructure.messaging.envelopes` 下设置，参见[调优实例](./tuning-an-instance.md)。读取不是开关：自 `v1.1.0` 起，无论设置如何，所有服务都同样读取信封和单事件消息；设置只决定服务是否写入信封。下面的每条规则对其中任一个以及两者同时适用。

- **何时可以安全开启。** 实例的每个 Pod 都运行 `v1.1.0` 或更高版本之后，而不是之前。较早版本的 Pod 无法读取信封，会将其作为失败消息搁置，其中的事件对该 Pod 而言即告丢失。请先升级，等所有 Pod 都完成滚动，再设置该值。
- **检测引擎如何处理重复。** 在少数情况下（例如重试期间确认丢失），同一个事件会两次进入流。事件存储和设备状态本来就能吸收这种情况。该值开启时，检测引擎还会记住约过去十分钟代理时间（平台自身的重新投递跨度）内它已应用的每个事件的标识，跳过第二份副本，并在 `detect_duplicate_events_skipped_total` 上计数。该计数持续不为零，说明上游有东西在重复存储事件。该值关闭时，检测引擎不保存这份记忆，其开销和行为不变。标识与事件存储使用的相同，因此同一设备在同一纳秒的两次完全相同的读数，在两处都只计一次。
- **开销。** 在约每秒 20,000 个事件时，内存保存约 1,200 万个标识，约 540 MB（外推值），仅在检测主节点上（备节点接任时才构建）。实测在每秒 6,000 个事件时增加约 190 MB，在每秒 12,000 个事件时增加约 300 MB，每个标识约 45 至 55 字节；在每秒 5,000 个事件时预计约 150 MB。开关开启时，请按[服务规格](./bootstrap.md#service-sizing)中的说明为 `event-processing` 配置内存。超过约每秒 25,000 个事件时会触发上限，保存的时间不足十分钟（每秒 100,000 个事件时约 2.7 分钟）。重启或更换主节点后，检测引擎会先重新读取流中最近十分钟的内容再继续，因此在繁忙的流上这一步耗时更长；它记录在 `detect_guard_prime_seconds` 和 `detect_guard_prime_events_total` 上，而 `detect_guard_prime_partial_total` 统计流已无法回溯那么远的次数。
- **不覆盖什么。** 如果副本在第一份之后超过十分钟才到达（例如入站消费者被重建并从头重新投递），检测引擎会第二次应用它，在计数类或窗口类规则中可能被计两次。
- **关闭。** 将该值设为 `false`。流中已有的信封会保留到过期或被消费，并仍会被读取。
- **迁移到低于 `v1.1.0` 的版本。** `dcctl upgrade` 会记录该值何时开启，并在关闭时记录入站流和已解析流中最新消息的位置。迁移到低于 `v1.1.0` 的版本会在以下情况被拒绝：该值仍处于开启状态；任一流仍保留关闭之前的消息；缺少关闭时间的记录（请在当前版本再运行一次 `dcctl upgrade` 来生成）；以及在流的抽样中发现信封（每个流的最旧保留消息和最新的 256 条，这也能发现在 `dcctl` 之外被更改的设置）。当它无法检查，或目标不是发布版本标签时，同样会拒绝。不改变版本的运行永远不会被拒绝。`--allow-envelope-downgrade` 会在给出警告的前提下跳过拒绝：较早版本上的服务会将信封作为失败消息搁置，其中的事件对该服务而言即告丢失。

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

### v1.2.0 {#v120-upgrade}

`v1.2.0` 是原地升级，方式见[v0.17.0 的说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade)。本版本更改的 Helm 值不在兼容性策略的覆盖范围内，次版本在本节中说明这一点。

需要处理的事项：

- **了解有两个内存上限变高了。** `device-management` 的上限现在是 512Mi（原为 256Mi）；只要任一事件信封设置处于开启状态，`event-processing` 的上限为 768Mi（原为 384Mi）。两个信封设置都关闭时，`event-processing` 不变。内存请求不变，因此调度不变；每个上限只是 Pod 可使用的上限。参见[内存上限](#v120-memory)。
- **检查你自己设置的内存上限。** 在 `functionalAreas.<服务>.resources.limits.memory` 下为 `device-management` 或 `event-processing` 设置的上限按原值使用，更高的顶层 `resources.limits.memory` 也是如此。如果你的值低于[服务规格](./bootstrap.md#service-sizing)中对应你的设备规模或事件速率的数字，请调高。
- **请了解：`device-management` 在内存中保存更多设备群，其内存上限为 768Mi，而不是上面的 512Mi。** 逐设备缓存（`inMemoryCache.perDeviceCacheMiB`）现在每个为 80 MiB，原为 24，因为 24 MiB 只容纳 8,000 至 10,000 台设备。设备群达到该规模或更大时，每个副本现在使用更多内存。如果你自己设置了 `device-management` 的内存上限，请对照[服务规模](./bootstrap.md#service-sizing)中的规则检查。如果你自己设置了 `perDeviceCacheMiB`，则按你的值使用。参见[缓存与代理的规模设定](#v120-sizing-defaults)。
- **开启事件信封且每秒 16,000 个事件或更高时，请把 `event-processing` 设为约 1Gi。** 768Mi 下限不随速率增长。参见[缓存与代理的规模设定](#v120-sizing-defaults)。
- **如果代理服务器的工作集处于其软上限，请调高 `nats_memory_limit`。** 默认的 4Gi 从约每秒 12,000 个事件起就会到达该点；处于该点的服务器是变慢，而不是重启。参见[缓存与代理的规模设定](#v120-sizing-defaults)。
- **`dcctl bootstrap` 现在会拒绝属于另一个集群的本地状态。** 包括 `--dry-run`。如果您在 `dcctl` 之外重建了集群并复用实例名，请先移开 `~/.devicechain/instances/<名称>`；详见[说明](#v120-stale-state)。
- **检查是否有客户端为本实例不提供的资源申请令牌。** 默认情况下，本版本会统计这类请求以及使用这些令牌的 API 调用，但一律不拒绝。`v1.3` 默认拒绝它们。`infrastructure.tokenAudience.enforcement: observe` 可以在那一个次版本内保留本版本的行为，`v1.4` 会在启动时以及在 chart 的 values schema 中拒绝 `observe`。参见[令牌按其资源进行检查](#v120-token-audience)。
- **让指定了 `resource` 的 `client_credentials` 请求同时指定 `scope`。** 这样的请求在本版本中会被统计并记录日志，仍然成功，只会在下一个主版本中被拒绝。参见[令牌按其资源进行检查](#v120-token-audience)。
- **如果你自己编写实例配置**（`instance.existingSecret`），请添加 `infrastructure.tokenAudience`。在 MCP 区域已部署而文档没有列出它的资源时，`dcctl upgrade` 会打印警告，给出需要添加的配置块；当 `enforcement` 为 `enforce` 时则拒绝继续。参见[令牌按其资源进行检查](#v120-token-audience)。
- **请知悉：滚动升级期间重启的 `v1.1` Pod 将无法启动。**实例配置现在带有 `infrastructure.tokenAudience`，而 `v1.1` 的服务在启动时会拒绝它不认识的键。在配置变更之后、滚动替换它之前被重新调度、驱逐或重启的 `v1.1` Pod，会一直无法启动，直到被 `v1.2` 的 Pod 替换，因此请让滚动升级持续进行，不要中途暂停。已经在运行的 Pod 继续运行。参见[令牌按其资源校验](#v120-token-audience)。
- **如果你自己设置了 MCP 服务器的 `resourceUrl`，请在强制执行之前把它的 scheme 和主机写成小写。** 参见[令牌按其资源进行检查](#v120-token-audience)。
- **了解滚动升级期间，配置文件和设备类型的变更会较晚到达已升级的副本。** 参见[滚动升级期间的配置文件变更](#v120-profile-cache)。
- **请注意：带有载荷解码器的设备类型的上行消息会在一个小型流中等待。** 参见[解码器上行消息与 device-management 中断](#v120-decoder-stream)。
- **处理新的失败事件原因 `DECODE_FAILED`。** 对 `FailedEventReason` 做穷举 `switch` 的客户端会多看到一个值。参见[新的失败事件原因](#v120-decode-failed)。
- **预期持续失败的载荷解码器不再灌满失败事件。** 对每个设备类型，每个 `device-management` 副本记录前 20 次解码失败，之后每分钟记录 10 次；其余失败的源消息会被确认（ack），并计为已抑制，不再记录。新增两条告警 `PayloadDecoderFailuresHigh` 和 `PayloadDecoderSuppressing`：请把它们加入告警路由。参见[持续失败的载荷解码器有预算限制](#v120-decoder-budget)。
- **请注意 `DeviceState.inactivityAlarmTime` 已恢复为 `v1.1.0` 之前的行为。** `v1.1.0` 由 `lastDisconnectTime` 推导该字段，因此对于因断开报告而离线、从未沉默的设备，它也会返回一个时间。现在它重新表示不活跃扫描将设备标记为不活跃的时刻；对于尚未被扫描标记或已重新活跃的设备则为 null。该字段仍已弃用，将在下一个主版本中移除，仍应使用 `lastDisconnectTime`。`v1.2.0` 首次启动会修复 `v1.1.0` 留下的行：清除活跃设备上残留的值，并为不活跃且没有该值的推断设备用 `lastDisconnectTime` 填充。滚动升级仍在进行时，被 `v1.1.0` 的 Pod 标记为不活跃的设备，该字段返回该 Pod 标记它的时刻（从 `lastDisconnectTime` 读取），与 `v1.0.0` 一致。随后第二个迁移会处理第一个迁移运行期间被 `v1.1.0` 写入方持有锁的行，并重试约半分钟。之后仍被持有的行会被记录并保留，读取该字段时仍会得到正确的结果。
- **只有当每个 `device-management` 副本都运行本版本之后，才发布配置文件的载荷解码器。**在旧副本仍在提供服务时发布的版本，可能缺少它的解码器；参见[载荷解码器](#v120-payload-decoders)。
- **请知悉：X.509 证书吊销列表是新增的可选功能。** 升级本身无需任何操作；上传之前请先阅读[吊销列表](#v120-x509-crl)。
- **升级前检查事件库的剩余空间。** 对已有实例，`dcctl upgrade` 会把事件库的 `max_wal_size` 提高到其卷容量的五分之一（默认 32Gi 时为 6.4 GiB，原为 1 GiB），预写日志现在可以在数据卷上增长到这么大，归档落后时还会更大。该值升高时，升级会打印一条提示；卷的剩余空间小于新值时，会给出警告。要保留原来的额度，请运行 `dcctl upgrade --event-store-max-wal-mib 1024`。参见[事件库的预写日志额度](#v120-event-store-wal)。
- **如果用随附的看板绘制边缘代理的图表，请以作业名 `dc-edge-agent` 抓取它们。** 看板的进程面板（内存、CPU、goroutine、打开的文件）现在会选择该作业，因此同一台机器上的其他作业不再把自己的进程指标加进某个代理的面板。作业名不同时，这些面板会显示为空。参见[边缘代理看板与抓取作业](#v120-edge-board-job)。
- **`dcctl sim create` 现在会拒绝未声明所需限额的已有套餐层级。**`--tier` 指向已存在的层级时，`dcctl` 只会提高低于场景或 `--tier-config` 所要求的限额，并且看不到该层级继承的默认值。此前层级中缺失的限额会被读作 0 并设为下限，这可能覆盖层级继承的较大值；现在该层级会被拒绝，并指出缺失的键。请把该键加到层级中（user-management 管理 API 的 `updateTenantTier` 变更），或选择已声明它的层级。在 `v1.1` 上运行 `dcctl sim create --tier-config` 可能会把这类键写入已有的层级，如 `v1.1.0` 的说明所述；从本版本起不再如此。`--tier-config` 中的 `null` 限额同样被拒绝，此前它会被读作 0。参见[其他变更](#v120-other-changes)。
- **请知悉：broker 滚动之后，`dcctl upgrade` 现在可能以 `upgrade incomplete` 失败。**broker 服务器就绪后，升级会在最长两分钟内重新读取它们的卷声明，如果某个声明的容量仍小于其请求就会失败，因为 broker 的文件存储上限跟随请求值。请完成或撤销该卷的扩容（`kubectl -n <namespace> describe pvc`），然后再次运行 `dcctl upgrade`。参见[其他变更](#v120-other-changes)。
- **请知悉：吊销证书现在会结束其影响到的已打开会话。** 吊销证书颁发机构可能让整个设备群同时重新连接。参见[吊销证书会结束已打开的会话](#v120-x509-sessions)。
- **请知悉：`POST /{instanceId}/{tenant}/events` 现在可能返回 `500`。** 在构建其消息时被丢弃的事件（它携带的凭据无法密封，或事件无法编码）此前会收到 `202`，却没有在任何地方存储。现在它返回 `500`，事件不会被存储，重试会以同样方式失败；对所有非 `202` 响应都重试的客户端应停止重试该端点的 `500`。参见 [HTTP 接入不再对被丢弃的事件返回 202](#v120-http-build-dropped)。

- **新的 `--size` 选项无需任何操作。** `dcctl bootstrap --size perf-12k` 会应用一套经过实测的高吞吐规格，`dcctl install --size compact` 等同于 `--compact`（后者会保留）。未指定规格的实例渲染结果与之前完全相同。见[规格预设](#v120-size-presets)。
- **请知悉：`devicechain_eventprocessing_detect_pending_timers` 现在统计已启用的键。** 此前它统计静默或会话规则推入且尚未到期的所有定时器，随事件速率增长；引擎现在为每个已启用的设备只保留一个条目，因此该指标随设备数量变化，静默和会话规则的内存不再随事件速率增长。名称和告警不变；按旧的较大数值调整过的面板或告警阈值可以调低。

#### 检测分片并行运行 {#v120-detect-shards}

event-processing 的 `detectShards` 设置（默认 1，范围 1 到 64）按键拆分检测引擎的状态。在 `v1.1.0` 中各分片依次应用，因此大于 1 的取值结果相同、也没有提速。从本版本起每个分片在各自的 goroutine 中运行，引擎自身的工作可以使用多个核心。无论取何值，检测结果、事件时间时钟和已保存的检查点都完全一致，因此无需迁移，该设置可在任意一次重启时更改或还原。

- **如果 `detectShards` 保持为 1，无需任何操作。** 引擎运行的仍是原来的代码。
- **如果把 `detectShards` 设为大于 1**，除读取并规划消息的那一个循环外，event-processing Pod 现在最多可为引擎使用该数量的核心。依赖这一点之前请先检查 Pod 的 CPU 限制，并阅读[让引擎使用更多核心](./detection-engine.md#detect-shards)，确认引擎是否真是你的瓶颈。
- **新增两条告警**：`DetectShardSaturated`（某个分片是检测速度的上限）和 `DetectShardInvariantViolated`（各分片在检查点上意见不一致，检查点被拒绝）。无论取何值都会安装，但只有 `detectShards` 大于 1 时才可能触发。请把它们加入告警路由。同时新增一组 `detect_shard_*` 指标和 `detect_barrier_wait_seconds`；其中两个不带标签的在取 1 时以 0 导出。
- **内存随 `detectShards` 增长。** 每个分片最多持有 1024 条排队消息，因此已在大于 1 的取值下运行的 Pod，在循环领先于最慢分片时，内存中最多可能积压 `detectShards` 倍于此数量的消息。依赖新设置之前请检查 Pod 的内存限制。
- **回退**只需用旧值重启。检查点完全相同，没有需要撤销的内容。

#### 内存上限 {#v120-memory}

在 Google Kubernetes Engine 上最高每秒 16,000 个事件的测试中，这两个上限都与服务实测占用量只差一档：

- **`device-management`** 在每个副本中都把整个设备群保存在内存里。其 Go 堆在 6,000 台设备时为 154 MiB，在 12,000 至 16,000 台时为 193 至 218 MiB，而原上限为 256Mi，其软上限（`GOMEMLIMIT`）为 192 MiB。因此约 12,000 台设备的设备群就会触及软上限，更大的设备群则有 Pod 因超出上限而被终止的风险。chart 现在把下限设为 512Mi（软上限 384 MiB）。
- **开启事件信封的 `event-processing`** 会保存[事件信封](#event-envelopes)中描述的重复防护。在每秒 4,500 至 6,000 个事件时占用 265 至 269 MiB，在每秒 12,000 个事件时占用 388 MiB（Go 堆 353 MiB），而上限为 384Mi，软上限为 288 MiB。在每秒约 8,000 个事件以上，Pod 会被软上限卡住、持续回收垃圾，或被终止。只要 `resolved` 或 `inbound` 之一开启，chart 现在就把下限设为 768Mi（软上限 576 MiB），否则为 384Mi。此前对该防护的估计（每秒 20,000 个事件时约 180 MB）低了三到四倍；更正后的数字见[事件信封](#event-envelopes)。

两者都是下限，仅在本来适用的上限更低时才生效。你在服务上设置的上限，或更大的顶层上限，按原值使用，软上限随实际生效的上限而定。新值 `functionalAreas.<服务>.memoryLimitFloorWithEnvelopes` 保存开启信封时的下限。`dcctl` 会根据它写入的实例配置设置 `eventEnvelopesEnabled`。如果你自己用 `instance.existingSecret` 安装 chart，chart 读不到该配置：开启任一信封设置时，请设置 `eventEnvelopesEnabled: true`。

#### 缓存与代理的规模设定 {#v120-sizing-defaults}

第 30 轮在 Google Kubernetes Engine 上（每秒 8,000 至 20,000 个事件，一台代理服务器）发现，三项随产品发布的大小低于其文档所称的负载。

- **`device-management` 的逐设备缓存是 80 MiB，而不是 24。** 按令牌缓存中一台设备占 2.5 至 3.1 KB，所以 24 MiB 只容纳 8,000 至 10,000 台设备。16,000 台设备时，缓存在 73% 的查找中淘汰了条目，每事件 CPU 在 8,000 至 16,000 台设备之间从每 1,000 个事件 0.048 CPU 秒升到 0.105，20,000 台时读取落到键值桶并超时。`DeviceLookupCacheTooSmall` 从 12,000 台设备起就已触发；它不变。80 MiB 按令牌容纳 27,000 台设备，即规模表所针对的 20,000 台设备再加四分之一余量。其他设备群请把 `inMemoryCache.perDeviceCacheMiB` 设为每 1,000 台按令牌解析的设备 4。规则及其所需的内存上限见[服务规模](./bootstrap.md#service-sizing)和[键值缓存](./observability.md#kv-caches)。
- **`device-management` 的内存下限是 768Mi（软上限 576 MiB），取代[内存上限](#v120-memory)中的 512Mi。** 上限变大后，所有缓存写满时连同服务约占 370 MiB，512Mi 的软上限（384 MiB）只有其 1.04 倍，768Mi 则是 1.5 倍。下限规则不变：你在服务上设置的上限，或更高的顶层上限，按原值使用。
- **开启信封时 `event-processing` 的下限仍是 768Mi，并且不随速率增长。** 每秒 8,000、12,000、16,000 和 20,000 个事件时，其 Go 堆为 441、488、511 和 516 MiB（常驻 500 至 571 MiB），软上限为 576 MiB，所以从约每秒 16,000 个事件持续起，它会在上限处回收。请在那里设为约 1Gi（`functionalAreas.event-processing.resources.limits.memory`）。
- **4Gi 代理内存上限的软上限在约每秒 12,000 个事件时到达。** 一台服务器的工作集在每秒 8,000、12,000、16,000 和 20,000 个事件时为 1.9、3.0、3.8 和 4.8 GiB，而软上限为 3.2 GiB。超过软上限也不会被终止：服务器持续回收，每秒 12,000 个事件时第 99 百分位确认时间在 4Gi 下为 101 ms，在 20Gi 下为 25 ms。当工作集与上限的 80% 相差不到 10% 时就应调高 `nats_memory_limit`，而不只是在 `OOMKilled` 之后；按设计速率每每秒 1,000 个事件取 0.35 GiB。参见[消息代理](./bootstrap.md#broker-sizing)。

#### `dcctl bootstrap` 会拒绝属于另一个集群的本地状态 {#v120-stale-state}

当某个实例名在 `~/.devicechain/instances/<名称>` 下的本地状态记录的是与命令所指向的集群不同的集群时，`dcctl bootstrap`（以及 `dcctl bootstrap --dry-run`）现在会拒绝执行。以前这份记录会被新集群的标识悄悄改写，随后基于旧集群的 OpenTofu 状态和 broker 凭据做规划，之后以无关的信息失败（例如拒绝缩减一个并不存在的 broker 集群）。拒绝信息会列出两个集群并打印修复命令。如果记录的集群已经不在了，请把该目录移到一边；其中有凭据，请保留而不要删除：

```bash
mv ~/.devicechain/instances/<名称> ~/.devicechain/<名称>.stale-state
```

如果记录的集群仍在运行，请换一个实例名。由不含集群标识的旧版 `dcctl` 写下的记录不会被拒绝。没有 `dcctl` 命令或标志发生变化。
#### 令牌按其资源进行检查 {#v120-token-audience}

OAuth 客户端可以在授权请求和令牌请求中用 `resource` 参数申请绑定到某个资源的令牌。此前授权服务器接受任意值，也没有任何 API 检查这种绑定，因此绑定到一个资源的令牌会被所有 API 接受。本版本新增两项检查，**在本版本中，两者默认都只统计和记录日志，不拒绝任何请求**，除非你自己设置了 `enforcement: enforce`。 规则见[令牌受众](../concepts/security-model.md#token-audience)。

- **授权服务器检查所申请的资源。** 允许的资源包括本实例的 MCP 服务器、各区域位于 `<origin>/api/<区域>` 的 API，以及 `infrastructure.tokenAudience.externalResources` 中列出的资源。检查在授权端点、`client_credentials` 授权和每次刷新时进行。不在列表中的值会计入 `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}`；仅在授权端点，不是绝对 `https` URL 的值（仅回环主机允许 `http`）和带有多个 `resource` 的请求也会计入。`client_credentials` 授权本来就拒绝这两种情况。
- **各 API 检查令牌的受众。** 一个区域的 API 接受未绑定的令牌、绑定到该区域的令牌或绑定到 MCP 服务器的令牌。其他令牌计入 `devicechain_<区域>_token_audience_checks_total{verdict="would_reject"}`。通过登录控制台、仪表盘应用、C# SDK 或 `dcctl` 获得的令牌没有绑定，因此不受影响。MCP 服务器对各 API 的调用带有 MCP 绑定，会被接受。

两个计数器都附带一个 `info` 级告警（`TokenAudienceWouldReject` 和 `OAuthResourceWouldReject`），服务日志中还会有限频的警告，写明 OAuth 客户端和租户。读取方法见[令牌受众检查](./observability.md#token-audience-checks)。

**`v1.3` 默认拒绝这些请求。** 授权请求会带着 `invalid_target` 被送回客户端的重定向地址，`client_credentials` 请求返回 `invalid_target`，绑定到已不再提供的资源的刷新或授权码返回 `invalid_grant`，`resource` 与其授权码或刷新令牌签发时不同的请求返回 `invalid_target`，API 返回 `401`。因资源已不再提供而被拒绝的刷新令牌会被作废：切回 `observe` 或回退版本可以恢复配置，但恢复不了那个会话，客户端必须重新授权。如果任一计数器有变化，请根据日志行找到该客户端，并修改它发送的 `resource`：省略它，或改用上面列出的值之一。如果你自己的服务用公开的密钥集验证平台令牌，并申请绑定到自身的令牌，请把它的标识符加入 `externalResources`。平台 API 永远不接受绑定到外部资源的令牌，授权服务器的 `userinfo` 端点也不接受。

- **若要再保留本版本的行为一个次版本**，请在升级到 `v1.3` 之前设置 `infrastructure.tokenAudience.enforcement: observe`。本版本已经接受它，先行设置可以保证滚动期间没有 Pod 执行强制检查。`v1.4` 会拒绝该值：服务在启动时停止并给出指引，chart 的 values schema 也会拒绝它，使 `helm upgrade` 在任何 Pod 滚动之前就停下。
- **若要提前开启强制执行**，请在本版本中设置 `enforcement: enforce`，这会让 Pod 滚动一次。`v1.3` 自己切换默认值时，滚动期间新旧 Pod 并存，同一个令牌可能在几分钟内被一个副本接受而被另一个副本拒绝。先行设置可以避免这种情况。

chart 和 `dcctl` 会根据 ingress 主机和 MCP 服务器的地址为你写入 `infrastructure.tokenAudience`。不要在 `instance.config` 下设置 `areaBaseUrl` 或 `mcpResources`：渲染会拒绝。在非 `localhost` 主机上未启用 TLS 的 ingress 没有区域资源，正如它也没有授权服务器。你可以设置的键是 `enforcement`、`unscopedClientCredentials` 和 `externalResources`，参见[配置](../concepts/security-model.md#token-audience-config)。

**MCP 服务器的注册。** `mcp` 在启动时检查它的 `resourceUrl` 是否列在 `infrastructure.tokenAudience.mcpResources` 中。如果没有，`mcp` 每次启动都会记录一条 Error，并把仪表 `devicechain_mcp_token_audience_registered` 报告为 0，它转发的调用会被计为本应拒绝的请求。在 `enforcement: enforce` 下它会拒绝启动。

- **由 chart 写入配置的实例**：chart 总会把它列入，所以该仪表为 1。例外有两种：显式设置的 `functionalAreas.mcp.config.resourceUrl` 的 scheme 或主机含有大写字母，或者它等于某个平台区域自己的标识符，例如 `<origin>/api/device-management`。列表只接受小写的 scheme 和主机，并且逐字节比较。在 observe 下 chart 会省略这样的值，症状同上；在 `enforce` 下渲染会失败。`mcp` 自身仍接受该值，所以请在 `v1.3` 之前把它改成小写且不是区域标识符的地址。
- **如果你自己编写实例配置**（`instance.existingSecret`），chart 不会写入该配置块。如果你的文档没有 `tokenAudience` 配置块，或者其 `mcpResources` 没有列出 MCP 服务器的 `resourceUrl`，仪表就停留在 0，只要缺少这一项，每次启动都会重复这条 Error。这会一直持续，直到你添加它。在 MCP 区域已部署时，`dcctl upgrade` 会打印警告并给出需要添加的配置块，在 `enforce` 下则拒绝继续。请添加该配置块，其中 `areaBaseUrl: <origin>/api`，`mcpResources: [<MCP 服务器的 resourceUrl>]`，并更新 `instance.existingSecretChecksum`。

**已弃用：指定了 `resource` 却未指定 `scope` 的 `client_credentials` 请求。** 这样的令牌带有服务账号各角色的全部权限。请随资源一起发送 `scope`（`read-only`，可同时加上 `location`）。带 scope 的令牌只带有该 scope 的读取权限，因此需要写入的集成应改为省略 `resource`。本版本中缺少 scope 本身从不构成拒绝的理由，所以该请求仍然成功。每次都会计入 `devicechain_usermanagement_oauth_unscoped_client_credentials_total`，并连同其客户端记录到日志，它将在下一个主版本中以 `invalid_scope` 被拒绝。在此之前，`infrastructure.tokenAudience.unscopedClientCredentials: enforce` 在启动时会被拒绝。参见[服务账号](../user-guide/service-accounts.md#binding-a-token-to-a-resource)。

**回退。** `v1.1` 的服务在启动时会拒绝 `tokenAudience` 配置块。`dcctl` 渲染的 `v1.1` 配置不含它。如果你自己编写配置，请在回退前删除该配置块并更新 `instance.existingSecretChecksum`。

#### 新的失败事件原因 {#v120-decode-failed}

`FailedEventReason` 新增 `DECODE_FAILED`：载荷解码器拒绝了某个集成的上行消息。错误文本的开头说明了原因（例如 `no_section`、`eval_error` 或 `invalid_output`）。它在第一次投递时即被记录，不会重试，因为相同的上行消息和解码器会给出相同的结果。版本策略允许在次要版本中新增枚举值；对原因做穷举 `switch` 的客户端应处理该值，较旧的 event-management 会把该编号显示为 `UNKNOWN`，并附带 `reasonCode`。`dcctl` 的 `--reason` 接受 `DECODE_FAILED`。

#### 持续失败的载荷解码器有预算限制 {#v120-decoder-budget}

对某个设备不正确的解码器，会在该设备的每条上行消息上失败，而每次失败过去都会产生一条失败事件记录。在流量大的租户上，这会在全平台共用的流里产生大量记录。从本版本起，`device-management` 在写入记录之前，对每个设备类型执行预算限制：

- **记录。** 在每个副本上，对每个租户和设备类型，前 20 次解码失败会记入失败事件（原因为 `DECODE_FAILED`，并归档该上行消息）。之后每分钟再记录 10 次。其余失败的源消息会被确认而不产生记录，并计为**已抑制**。只有在该设备类型已写入 20 条记录之后才会出现已抑制的失败，所以损坏的解码器不会悄无声息。
- **样本。** 在每个副本上，对每个租户和配置文件，每 10 秒保留一条失败的上行消息，每个配置文件保留最新的 20 条。测试面板可以把其中一条载入解码器测试，得到它在线上时的结果。样本是尽力而为的：无法保存的样本（写入失败，或携带的 JSON 无法重新编码）会计入 `resolve_decode_samples_dropped_total`，其失败仍会被记录。
- **预算按副本计算，** 因此租户的总量会随副本数增加。没有可配置的选项。
- **每条上行消息每次投递只计一次。** 配置文件的解码器健康度按分钟汇总已解码、已忽略、失败（已记录）和已抑制（未记录）的上行消息。`payloadDecoderHealth` 中的计数是 `Float` 而不是 `Int`，因为流量大的解码器一周内就会超过 32 位整数；它们是整数，在 2^53 以内精确。`device-management` GraphQL API 上的 `payloadDecoderHealth` 和 `payloadDecoderSamples` 查询是新增的、向后兼容的，并且需要 `device:write`；样本的字节只能通过带 `sampleId` 的 `testPayloadDecoder` 获取，权限相同。健康度记录保留 7 天。
- **崩溃后计数可能偏少。** 汇总每分钟写入一次，所以崩溃最多丢失一分钟的计数，不会丢失失败事件记录。崩溃后，或失败事件流不可用期间，被重新投递的上行消息可能被计两次。被重新投递的失败总是会被记录，并且仍会消耗预算中的一个令牌。
- **新增两条告警。** 当 15 分钟内超过 5% 的已解码上行消息失败时触发 `PayloadDecoderFailuresHigh`，当 10 分钟内被抑制的失败超过 50 次时触发 `PayloadDecoderSuppressing`。它们不带租户标签；失败事件索引和配置文件的解码器健康度会指明租户和配置文件。新增指标：`resolve_decode_suppressed_total`、`resolve_decode_samples_dropped_total`、`resolve_decode_health_flush_failures_total`、`resolve_decode_health_lost_total` 和 `resolve_decode_unattributed_total`。
- **升级时无需任何操作。** 这些表由本版本较早的迁移创建，不会运行新的迁移。早于本版本的副本不会写入健康度或样本，也不会对失败应用预算。

#### 解码器上行消息与 device-management 中断 {#v120-decoder-stream}

带有载荷解码器的设备类型的上行消息，通过专用的流从 event-sources 传给 device-management。该流很小且大小固定（32 MiB），因此默认的 JetStream 卷和已发布的容量表都不会变化。如果 device-management 停止读取，该流在高速率下几秒内就会写满，随后 event-sources 对这些集成的请求返回 503。会重试失败投递的发送方会在该流排空后送达上行消息；不重试的发送方则不会。The Things Stack 的 webhook 仅在启用其可选的 webhook 重试功能（Cloud Plus 功能，任何非 2xx 响应都算一次失败尝试）时才会重试失败的投递，而 ChirpStack v4 的 HTTP 集成没有文档说明任何重试，因此应将其在中断期间发送的上行消息视为不会被重新投递。已被接受（返回 202）的上行消息不会丢失。没有解码器的设备类型的上行消息照常处理，不受影响。请运行多个 device-management 副本（至少足以让重启其中一个不会中断读取），使中断保持短暂。

#### 滚动升级期间的配置文件变更 {#v120-profile-cache}

`device-management` 现在用一个新的键缓存每个设备类型的配置文件解析结果，因此运行本版本的副本不会读取旧副本写入的条目（旧条目不含载荷解码器）。在本版本副本上所做的每次变更都会同时清除两个键下的条目。在**旧**副本上所做的变更只清除旧键，因此已升级的副本会继续提供其持有的内容，直到条目过期。

- **滚动升级进行期间所做的配置文件和设备类型变更，可能需要大约两倍的缓存存活时间才能到达已升级的副本**（按 `metricDefCacheTtlSeconds` 和 `inMemoryCache.ttlSeconds` 默认的 300 秒计，约 10 分钟）。这包括发布、回滚、设备类型更换配置文件以及地理围栏变更。所有副本都运行本版本后，变更会像以前一样立即生效。
- **只有在每个 `device-management` 副本都运行本版本之后，才发布配置文件的载荷解码器。** 从旧副本发布的版本不包含该解码器。
- **此缓存的进程内副本变大了。** 每个副本上限为 32 MiB（原为 4 MiB），因为带有载荷解码器的条目持有其编译后的程序。不带解码器的条目大小不变。该上限由 `inMemoryCache.profileResolutionCacheMiB` 设置，`inMemoryCache.profileResolutionTenantSharePercent`（默认 25）限制单个租户可占用的比例，因此拥有许多大型解码器的租户会被限制在自己的份额内，并先驱逐自己的条目。当某副本的整个缓存已满时，占用最多的租户先让出条目，所以份额以内的租户在更大的占用者还有条目可让时保留自己的条目。租户的份额就是其大型解码器必须共同容纳的空间：一个最大的解码器计约 6 MiB，因此默认的 8 MiB 份额只容纳一个，同一租户同时使用两个时会轮流占用，每次读取都要重新编译解码器。请按租户同时使用的大型解码器数量来设定缓存和份额。当租户的份额（`profileResolutionCacheMiB` x `profileResolutionTenantSharePercent`）低于 7 MiB 时，`device-management` 现在会拒绝启动，7 MiB 是限制所允许的最大解码器的计费大小：更小的份额无法让它保持解码状态，其设备类型的每个事件都会重新编译它。较小的解码器需要的更少。副本会导出 `payload_decoder_compiles_total`，当某副本连续 15 分钟每秒编译超过一次时，chart 会以 `PayloadDecoderRecompileSustained` 告警。如果你自行设置了内存上限，请计入额外的 28 MiB。

#### 载荷解码器 {#v120-payload-decoders}

设备配置文件现在可以携带**载荷解码器**：用表达式写成的规则，把二进制帧、通用 JSON 正文或网络服务器解码出的对象转换成测量。新的[载荷解码器](../guides/payload-decoders.md)指南对它们做了说明，[函数参考](../reference/decoder-functions.md)列出了函数库中的函数和各项限制。升级本身无需做任何事，没有解码器的配置文件行为与以前完全一致。

- **编写和测试包含在本版本中。**控制台的配置文件页面有一个 **Decoder** 选项卡。在 `device-management` 的 GraphQL API 上，`setDeviceProfileDecoder` 和 `clearDeviceProfileDecoder` 写入配置文件的草稿解码器，`testPayloadDecoder` 在不存储任何内容的情况下对一个输入运行解码器，`payloadDecoderFunctions` 列出函数库。`DeviceProfile.decoder` 和 `DeviceProfileVersion.decoder` 是新增的可空字段。以上全部是增量变化。
- **对实时上行的解码已包含在本版本中。**当[入站集成](../guides/inbound-integrations.md)上行所属的设备，其设备类型的配置文件版本带有解码器时，`device-management` 会用该解码器解码这条上行，网络服务器解码出的内容不会被存储。event-sources 会把设备类型的解码器查询结果记住约一分钟，因此在你发布解码器之后的一分钟内，上行仍可能按网络服务器解码出的内容存储。没有解码器的设备类型的上行照旧存储。上行在被接受时计入租户的接入额度：至少是解码器的最大读数数（集成存储链路指标时再加 3），运行代价高的解码器更多，且最多 256。如果期间发布的解码器产生了更多读数，这些读数仍会被存储，`device-management` 会统计超出的部分。解码失败的上行不会存储任何内容，而是被记录为原因为 `DECODE_FAILED` 的失败事件；参见[新的失败事件原因](#v120-decode-failed)。集成端点现在还会向 `device-management` 询问设备的类型是否有解码器，无法得到答复、且没有记住最近 15 分钟内的答复时返回 `503`，而不是存储本应由解码器产生的读数。如果 `device-management` 还不认识这个查询（滚动升级期间可能出现），上行照旧存储。
- **发布取决于解码器。**带有解码器的配置文件，只有在解码器能针对该版本的指标编译、且所有已保存的测试向量都通过时，才能发布或回滚到该版本。没有解码器的配置文件不受影响。
- **迁移是增量的。**它为设备配置文件增加一个可空列，以及两张由 `device-management` 写入解码器健康度与失败样本的表（参见[持续失败的载荷解码器有预算限制](#v120-decoder-budget)）。旧副本不会读取它们。
- **测试入口按副本限速。**`testPayloadDecoder` 允许每个租户在每个 `device-management` 副本上每秒 10 个请求，突发 20 个，因此租户的总速率会随副本数增加而提高。超出限制的请求会以 `RATE_LIMITED` 被拒绝。
- **滚动升级期间，只有当每个 `device-management` 副本都运行本版本之后，才发布解码器。**由旧副本处理请求时发布的版本，会在没有解码器的情况下被冻结，因为该副本不认识这个字段。升级完成后，请检查你发布的版本上是否存在 `DeviceProfileVersion.decoder`，如果没有，请重新发布。

#### X.509 吊销列表 {#v120-x509-crl}

租户现在可以把证书颁发机构的吊销列表（CRL）存放到信任锚上，每个信任锚也有了吊销模式。没有列表、也没有手动吊销的信任锚，行为与以前完全一致。

- **CRL 通过 GraphQL 或流式 `PUT` 上传**，平台会用信任锚校验其签名。可接受的大小由固定设置 `deviceManagement.callout.revocation.maxCrlBytes` 决定（默认 2 MiB，最大 32 MiB）。**请按 Pod 的内存上限来设定它。** 校验 CRL 需要整体解析，一次上传的堆内存峰值约为 CRL 大小的 20 到 30 倍：2 MiB 的列表需要 40 到 65 MiB，包含 300,000 条记录的 7 MB 列表约需 190 MiB。`maxConcurrentWrites`（默认 1）是单个副本同时处理的上传数。
- **较旧的 CRL 永远不能替换较新的 CRL**，即使列表已被删除也一样：平台会保留每个颁发者已接受的最高 CRL 编号。**删除信任锚会连同这些编号、它的列表和列表中的条目一并丢弃**，因此用同一个 CA 重新创建的信任锚会从头开始，并会接受此前被拒绝的 CRL。只有在没有任何凭据和配置档案依赖某个信任锚时，才能删除它。
- **只要信任锚上还有手动吊销，删除它就会被拒绝。** 请先取消吊销（`unrevokeDeviceCertificate`）；这些是您自己的决定，不会被静默丢弃。
- **`REQUIRE_CURRENT` 按信任锚自行选择启用。** 如果信任锚没有任何有效列表，或者某个已注册证书的颁发 CA 没有有效列表，平台会拒绝切换到该模式，因为切换后所有此类设备都会被拒绝。
- **滚动升级期间**，尚未替换的副本在回答代理的授权回调时不检查吊销，因此吊销只有在滚动完成后才会在各处生效。回退到 `v1.1.0` 时这些表保留但不再被读取：吊销将不再生效。

#### 吊销证书会结束已打开的会话 {#v120-x509-sessions}

无论是手动吊销证书，还是上传点名该证书的 CRL，现在都会同时结束受影响设备已打开的会话。此前，已打开的会话会一直保持到令牌过期（默认最长 12 小时）。没有任何吊销操作的实例不受影响。

- **叶证书会结束持有它的设备的会话。** 吊销证书颁发机构的范围更大：当您通过提供 CA 证书来吊销 CA，或某个 CRL 点名了已注册凭据被发现使用过的中间证书时，该信任锚下**所有拥有证书凭据的设备**的会话都会被结束。这些设备会自行重新连接；证书链仍受信任的设备会立即被接纳。一次调用自身会结束最多约 5,000 台设备的会话，因此 CA 级吊销可能让大约这么多设备在同一时刻重新连接；其余的在后台结束。**在从早期版本升级而来的实例上**，升级前注册的凭据没有记录中间证书，因此当某个 CRL 新增了与任何已存储凭据都不匹配的条目时，也会结束这些旧凭据的会话，以防该条目是它们所用的中间证书。
- **会话会被结束两次。** 第一次在吊销提交时，第二次约 10 秒之后。第二次用于捕获吊销瞬间正在被接纳的设备。延迟由固定设置 `deviceManagement.callout.revocation.resweepAfter` 决定（默认 10 秒，最长 5 分钟）；它必须大于 callout 查询超时加上代理 5 秒的授权超时，否则 `device-management` 会拒绝启动。因此 CA 级吊销会对整个信任锚询问两次。
- **面对响应缓慢的服务时，吊销可能耗时较长。** 调用最多会等待负责结束会话的服务（`event-sources`）约 90 秒。如果无法确认，吊销依然有效，新连接会被拒绝；结果会显示 `sessionsEnded: false`，平台会持续重试，重复执行吊销或上传也会再次询问。
- **在滚动升级期间**，新的会话终止请求带有 `event-sources` 必须识别的新原因。在 `event-sources` 被替换之前，这些请求会保持待处理并被重试，因此升级过程中进行的吊销会在 `event-sources` 更新完成后结束已打开的会话。上一版本的 Pod 既不会处理这些待处理的请求，也不会删除它们。

#### 事件库的预写日志额度 {#v120-event-store-wal}

事件库的 `max_wal_size` 现在是其卷容量的五分之一，且不低于 Postgres 自带的 1 GiB；升级会按已有卷的大小把它应用到已有的事件库。默认 32Gi 时为 6.4 GiB，而升级后的实例原来是 1 GiB。`max_wal_size` 是检查点触发频率的目标值，既不是上限，也不是预留：检查点跟不上或 WAL 归档落后时，日志会超过它。但日志位于数据卷上，所以已经接近写满的事件库能让出的空间不到这么多，归档长时间停滞时占用的空间还可能超过该值。

- **该值升高时，`dcctl upgrade` 会告知。** 计划中会打印一行，写明旧值和新值。它还会读取事件库卷的剩余空间，在剩余空间小于新值或无法读取时打印警告。它不会拒绝执行：剩余空间少的事件库在当前速率下可能完全健康，剩余空间的读取只是尽力而为，而补救办法只需一个标志。
- **要保留较小的额度**，给升级加上 `--event-store-max-wal-mib`，例如 `dcctl upgrade local devicechain --event-store-max-wal-mib 1024` 即保留原来的 1 GiB。可先用 `--dry-run` 查看效果。省略该标志时，之后的升级会保留此值；`--event-store-max-wal-mib 0` 则恢复为推导出的值。`--event-store-checkpoint-timeout` 以同样方式设置检查点间隔（默认 `15min`；传入空值 `--event-store-checkpoint-timeout=""` 即恢复默认）。`dcctl` 会在应用变更之前就记录这两个值，因此升级中途失败后不带这些标志再次运行时，它们仍会保留。两者都通过重新加载生效，事件库无需重启。`dcctl` 把它们记录在实例 OpenTofu 状态旁边的 `zz-dcctl-event-store.auto.tfvars.json` 中，位于执行升级的那台机器上；参见[事件库调优](./tuning-an-instance.md)。
- **如果你自己管理实例的 OpenTofu**，照旧设置 `timescale_max_wal_size_mib` 和 `timescale_checkpoint_timeout`。

#### HTTP 接入不再对被丢弃的事件返回 202 {#v120-http-build-dropped}

HTTP 事件端点（`POST /{instanceId}/{tenant}/events`）会对一个在构建入站消息时被丢弃的事件返回 `202 Accepted`：这类事件包括所携带的凭据无法密封，或事件无法编码。调用方被告知数据已安全，实际上它没有存储在任何地方。从本版本起，端点对这些事件返回 `500 Internal Server Error`，不带 `Retry-After`，响应中也不包含原因。

- **如果没有客户端看到 `500`，则无需任何操作。** 这些情况是服务上的实例密钥缺失或失败，或事件无法编码，正常运行中都不会出现。
- **对所有非 `202` 响应都重试的客户端，不应重试该端点的 `500`：** 同一请求会以同样方式失败。`503`（无论是否带 `Retry-After`）和 `429` 的含义不变。
- **新增计数器 `total_msg_build_dropped{source,cause}`**，在所有传输上统计这些丢弃，`cause` 取 `no_tenant`、`no_transit_key`、`seal_failed` 或 `marshal_failed`。它为每个已配置的来源导出初值 0。任何大于 0 的值都表示有事件未被存储。
- 集成端点在消息被丢弃时仍保留文档中说明的 `202`。

#### 规格预设 {#v120-size-presets}

`dcctl bootstrap` 新增了 `--size` 选项，`dcctl install` 接受 `--size base` 和 `--size compact`。不传就没有任何变化：未指定规格的实例渲染结果与此变更之前逐字节一致，`--compact` 的行为与之前完全相同。

- **`perf-12k` 是第一个规格。** 它设置五个繁忙服务的副本数、CPU 与内存，Broker 的请求与内存上限，JetStream 与事件库的卷，以及事件库预写日志的额度，这些数值来自一个在开启事件信封的情况下持续每秒 12,000 个事件的集群。具体数字见[选择规格](./sizing.md#perf-12k)。
- **已有的紧凑实例会在下次写入时获得 `spec.size: "compact"`。** 用 `--compact` 构建的实例，其声明会在 `dcctl` 下次写入时（升级会写入）连同 `compact: true` 一起写上规格名；实例本身没有任何变化。
- **它记录在实例上**（声明中的 `spec.size`，base 时为空），每次 `dcctl upgrade` 都会重新应用。某个版本如果调整了某个规格，用该规格记录的实例会在下一次升级时随之改变，并在此说明。
- **你自己的设置优先。** 从低到高：Chart 默认值、规格、`dcctl` 自己算出的内容、你的 `--values` 覆盖文件，最后是你自行声明的 OpenTofu 变量。见[哪个值优先](./sizing.md#precedence)。
- **它不会开启别的东西。** 规格不会开启事件信封或 MQTT 故障关闭确认；与 `--ha` 同用时，会提示这种组合尚未实测。

#### 边缘代理看板与抓取作业 {#v120-edge-board-job}

边缘代理看板的进程面板除选择代理外，还选择 `job="dc-edge-agent"`。`agent` 是机器上采集器的全局外部标签，因此如果采集器还抓取了机器上的其他进程（例如 node exporter），那个进程的内存、CPU、goroutine 和打开的文件就会被加进每个代理的面板。文档中的抓取配置已使用 `job_name: dc-edge-agent`，按它导入的看板无需改动。如果您给作业起了别的名字，请在采集器中改名，或把看板上五个进程与运行时查询（`process_*` 和 `go_goroutines`）改成您的作业名。请从 `backend/edge/dc-edge-agent/dashboards/edge-agent.json` 重新导入看板以应用此变更。

#### 本版本的其他变更 {#v120-other-changes}

除上面列表中的两项 `dcctl` 变更外，这里的内容都不需要你采取行动。每一项都是对已发布功能的修复或补充。

- **`dcctl sim create` 与层级。**`--tier` 指向已存在的层级时，该层级必须声明场景或 `--tier-config` 所列出的每一项限额，否则 `create` 会拒绝并指出缺失的键：`dcctl` 只会提高限额，看不到继承的默认值。由 `create` 自己创建的层级不受影响。`--tier-config` 中的 `null` 会被拒绝，而不再被读作 0。
- **`dcctl upgrade` 与 broker 的卷。**如果某个声明唯一未完成的步骤是扩容的文件系统部分，并且它的 broker 服务器 Pod 已不存在，升级不再永远被拒绝：升级重新创建的 StatefulSet 会完成这次扩容。正在进行的扩容、没有扩容记录却容量不足的声明、节点侧扩容失败以及仍存在的 Pod，依然会导致拒绝。滚动结束后，升级会按上面列表所述检查这些声明。不带 `--mqtt-host` 的 `dcctl upgrade --mqtt-exposure <模式>` 会沿用实例已记录的主机；若没有记录，仍会被拒绝。
- **`dcctl sim`。**`dcctl sim status`、`start` 和 `stop` 在输出到终端时会缩进控制 API 的 JSON；管道输出不变。模拟设备群可以运行空中升级的设备端（`--ota`，并可注入错误哈希、错误签名、中断的下载和离线设备）。
- **当实例不是默认实例时，`dcctl bootstrap` 和 `dcctl sim create` 会给出更多信息。**bootstrap 摘要中的 `GraphQL:` 行现在与 `Console:` 行使用相同的主机和端口，此前它会省略非默认端口。当 `--instance` 仍是默认值 `devicechain`，导致 `dcctl sim create`（或 `sim destroy`）无法读取超级用户密码时，错误信息现在会附上本机已记录的实例，并建议使用 `--instance <name>`。标志、默认值以及命令作用于哪个实例都没有变化。
- **`dcctl bootstrap` 和 `dcctl upgrade` 多打印一行**：当 MQTT 接入路径未设置为失败即关闭时，提示在持续过载下已确认的事件可能被丢弃，并给出 `--mqtt-fail-closed`。你不操作就不会有任何变化。
- **`dcctl broker rebalance` 让最繁忙的两个逐事件流保持在不同服务器上。**broker 滚动之后，`resolved-events` 与固定的接入流的 leader 可能落在同一台服务器上，按流数量均衡看不出这一点。该步骤现在会在有可用服务器时，把 `resolved-events` 移到不领导任何逐事件流的服务器上，其摘要会把它报告为已分离、共享或未确认。v1.0 摘要中的字段保持不变。
- **事件库：早于刷新策略覆盖范围的历史数据。**日期早于 `bucketedMeasurements` 背后的汇总所刷新的最近 30 天的读数（先存后发、回填），此前会被存储并由 `measurementEvents` 返回，却从未进入汇总。现在每小时一次的清扫会刷新它，因此这类读数大约一小时内就会出现在分桶查询中。没有架构变更。
- **禁用或删除设备会可靠地结束它的 broker 会话。**以前，结束会话只是在变更保存后尝试一次；如果 broker 没有确认，只能靠重复调用来重试。现在它会与变更一起排队并在后台重试，与凭据吊销已有的做法一致，重新启用设备会撤回尚未执行的结束请求。
- **表示“请重试”的命令入队失败。**当平台因所需服务不可用而无法决定一次入队（校验关卡、归属查询、分组成员关系、维护窗口计划）时，GraphQL 错误会带上 `extensions.code: UNAVAILABLE`。消息不变，它仍是错误，而不是拒绝。
- **检测：由死人开关撤防而清除的告警，不再停留在触发状态。**此前如果之后没有新消息就遇到检查点或引擎任期结束，待发布的 Resolved 会丢失；现在会先发布。
- **事件信封（可选）：消息已从流中被淘汰的轮次会被跳过**并计数（`envelope_rounds_evicted_total`、`resolve_envelope_rounds_evicted_total`），遍历池占满也不再阻止服务关闭。
- **新增两条告警**：`SparkplugIngestLeaderMetricAbsent` 和 `Lwm2mIngestLeaderMetricAbsent`。已部署的边缘接入服务如果不导出 leader 序列，原有的 leader 数量告警会保持沉默。请把它们加入你的告警路由。图表中新增两块 Grafana 看板，分别用于 Sparkplug 和 LwM2M 接入；仓库还附带一个边缘代理的仪表盘文件；参见 [Grafana 看板](./grafana-boards.md)。
- **控制台**在设备有未完成命令时会轮询其命令状态，告警值的格式与仪表盘一致，自动化画布的缩放控件会应用主题，设备配置文件列表以名称为首。
- **严格 CSP 宿主中的仪表盘。**`configureStrictCsp`（默认关闭，因此不会有变化）让看板回放包可以在 `style-src 'self'` 下运行；参见[编写自定义小组件](../guides/writing-a-custom-widget.md)。
- **基础设施。**未设置 `nats_mqtt_capable_users` 时，OpenTofu 的 broker 模块不再写出会被 broker 拒绝的空 `allowed_connection_types`；事件库备份的证书修复器会监视运营商配置的命名空间。
- **`dcctl upgrade` 现在接受 `--yes`（`-y`），与 `dcctl install` 和 `dcctl bootstrap` 一致，并能找到用 `--escrow-file` 写出的托管文件。**`--yes` 表示对提示一律回答是；升级不会提问，所以它不改变任何行为，脚本现在可以把它同时传给这三个命令。`dcctl bootstrap --escrow-file` 会记录托管文件的写入位置，升级的根密钥托管检查会到那里查找，因此对于托管文件位于自定义路径的实例，不再报告“没有托管”。对于更早创建的实例，请给升级传一次 `--escrow-file`，它会记录该路径。

#### 架构变更与回退 {#v120-going-back}

六个迁移追加了以下内容。每个新列都允许为空，因此运行在新架构上的 `v1.1.0` 服务写入的行仍然可以存储。

- **X.509 吊销**（`device-management`，迁移 `20261105100000`）。四张新表：`device_revocation_issuers`、`device_revocation_lists`、`device_revoked_certificates` 和 `device_credential_chain_links`。四个新列：`device_trust_anchors` 上的 `revocation_mode`、`revocation_stale_grace` 和 `revocation_rows`，以及 `pending_session_kicks` 上的 `not_before`。已有的信任锚的 `revocation_mode` 保持为空，按 `ENTRIES` 读取：证书只有列在名单上才会被拒绝。
- **载荷解码器**（`device-management`，迁移 `20261110120000`）。两张新表 `profile_decoder_healths` 和 `profile_decoder_samples`，以及一个新列：`device_profiles` 上的 `decoder_declaration`。
- **吊销触发的会话终止队列**（`device-management`，迁移 `20261111120000`）。一张新表 `pending_revocation_kicks`，以及一个允许为空的新列：`device_revoked_certificates` 上的 `kick_queued_at`。该表与现有的会话终止队列分开，因此 `v1.1.0` 的副本永远不会读取或删除其中的行。
- **投影存储**（`device-state`，迁移 `20261104120000`）。不新增表或列，只为 `device_states`、`latest_measurements` 和 `latest_locations` 设置存储参数（填充因子 75 和 autovacuum 阈值）。已有的行只有在其页面被重写时才获得填充因子留出的余量，因此升级后的实例会缓慢感受到这一变化。
- **放置表**（`user-management`，迁移 `20261105120000`）。三张新表：`iam_partition_config`、`iam_vpartition_assignments` 和 `iam_tenant_vpartition_overrides`。迁移本身不写入任何行；`user-management` 首次启动时会写入默认放置（`iam_partition_config` 一行和 `iam_vpartition_assignments` 64 行），并在消息 broker 中创建 `dc_partition_placement` 存储桶。
- **不活动告警时间修复**（`device-state`，迁移 `20261106120000`）。不改变架构，只对 `device_states` 中的 `inactivity_alarm_time` 做仅数据修复，每批 5,000 个，恢复该列在 `v1.1.0` 之前的内容。回退到 `v1.1.0` 时无需撤销。

除这份默认放置外，新表一开始都是空的。**回退到 `v1.1.0` 时，数据库中无需撤销任何内容：** 新的表、列和存储参数留在原处，`v1.1.0` 的服务会忽略它们，只有一个例外：见下文的租户删除。能向前走就向前走：升级失败时，再次运行 `dcctl upgrade` 即可修复。回退所需的配置变更，参见[令牌按其资源受到检查](#v120-token-audience)。

**当运行租户删除的是 `v1.1.0` 的 `user-management` 副本时，删除会被卡住。** `v1.1.0` 的副本不认识这三张放置表，它的关系型清除在遇到无法归类的表时会拒绝运行。只要由它执行清除，关系型存储就会为该删除报告错误，日志和账本失败记录中会点出 `iam_partition_config`、`iam_vpartition_assignments` 或 `iam_tenant_vpartition_overrides`，同时 `tenant_purge_store_blocked{store="rdb",reason="error"}` 大于零。被拒绝的这一步不会从数据库删除任何内容，也不会丢失任何数据；其他存储在同一轮中照常被清除。删除无法完成：租户保持在 `purging` 状态。运行本版本的副本会在下一轮接手，并在其通常的检查满足后完成。滚动升级期间，最后一个 `v1.1.0` 副本消失后会自行完成；回退到 `v1.1.0` 后则一直被卡住，直到你再次向前升级。条件允许时，请在升级完成后再发起删除。

### v1.1.0：摄取每个事件更省 CPU，集群外的设备可通过 MQTT 接入 {#v110-upgrade}

`v1.1.0` 是从 `v1.0.0` 起的原地升级：先对集群运行 `dcctl install`，再对每个实例运行一次 `dcctl upgrade`，方法见[v0.17.0 的说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade)。请给 `dcctl install` 传入集群安装时使用的设置（如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`），以及 `--version`。升级时会运行两个追加的迁移（一个在 `event-management`，一个在 `device-state`）；每个迁移只新增一张表，不改写任何现有行。`Instance` 的新字段（参见[集群外的设备](#v110-mqtt-exposure)）随 operator 一同发布，由 `dcctl install` 更新，因此请如上所述先运行它，再升级实例。如果从 `v0.27.0` 或更早版本升级，请先阅读 [`v1.0.0` 的说明](#v100-upgrade)，并在其中使用 `--version v1.1.0`。

```bash
dcctl install local --version v1.1.0
dcctl upgrade local devicechain --version v1.1.0 --dry-run
dcctl upgrade local devicechain --version v1.1.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本扩充了受保护的接口（新的 `dcctl` 命令与标志、新的 GraphQL 查询、新的 `Instance` 字段），没有移除任何接口。有一个 GraphQL 字段已被[弃用](#v110-deprecations)，并保留到下一个主版本。

需要采取的行动，按执行顺序如下：

- **请了解：按事件计的流现在按故障缓冲时长定容。** `inbound-events`、`resolved-events` 和设备捕获流现在按“设计速率下若干分钟的事件量”定容，而不再是固定的 1 GiB。使用 chart 默认值时，上限不变。如果设置了 `streamDesignRate`，升级会原地增大这些上限，JetStream 卷必须容得下结果；`dcctl` 会在写入任何内容之前对照卷检查预留量，并指出缺口。参见[流按故障缓冲时长定容](#v110-runway)。
- **除非明确要求，否则会拒绝调低流的上限。** 如果你的值文件把某个上限设得低于该流当前的数据量，代理管理器会停止并指出该流和缺口。参见[这一保护](#v110-shrink)。
- **检查所有使用平台服务登录名连接 MQTT 监听器的工具。** 自本版本起，只有 `event-sources` 自己的登录名可以通过 MQTT 认证；其余静态平台登录名仅限使用代理的原生协议。设备不受影响。参见[升级说明](#v110-upgrade-notes)。
- **请了解：重新运行 `dcctl bootstrap` 现在会保留代理的证书颁发机构。** 以前每次重新运行都会生成新的。如果你依赖重新运行来轮换它，现在不会再轮换。参见[升级说明](#v110-upgrade-notes)。
- **请了解：`device-state` 的 `projection.maxBatch` 现在默认为 64**（原为 32）。如果你已自行设置，则不受影响。参见[升级说明](#v110-upgrade-notes)。
- **如果有客户端读取 `DeviceState.inactivityAlarmTime`，请改用 `lastDisconnectTime`。** 参见[弃用](#v110-deprecations)。
- **事件信封和 MQTT 对外暴露默认关闭**，升级时无需任何操作。要使用它们，请参见[事件信封](#v110-envelope)和[集群外的设备](#v110-mqtt-exposure)。
- **更新告警路由。** 新增四个告警，变更两个。参见[告警](#v110-alerts)。
- **更新读取 `kubectl get dci` 的脚本。** 列已变化。参见[实例上报观测到的状态](#v110-status)。
- **安装 .NET SDK 8.0.427**，如果你自行构建 C# SDK 或演示项目。参见[较小的变更](#v110-smaller)。
- **回退。** 参见[回退](#v110-going-back)。

#### 摄取每个事件消耗的 CPU 更少 {#v110-ingest}

事件写入器现在会把批次填满。一个 Pod 的十个写入器过去各自从一个共享通道收集自己的批次，因此一个批次只有寥寥几个事件，等待时间几乎没有带来任何好处。现在，一个 Pod 内各写入器之间的收集是串行化的：同一时间只有一个写入器持有收集权，且仅在它接收消息期间持有，并在解码、租户准入和提交之前释放，因此这些步骤仍然并行。因此 `event-management` 和 `device-state` 的批量都大幅增加。`device-state` 还通过共享数据库层中新增的按列数组 upsert 模式，把热点 upsert（心跳、加锁或创建、合并后的回写、最新测量值和最新位置）按每批一条语句写入，每列一个数组。该模式从请求上下文绑定租户，要求冲突目标包含租户列，并依据允许列表语法校验其更新保护条件，因此它与其旁边的插入具有相同的租户隔离。

在 Google Kubernetes Engine 的 `us-east4-b` 区域测量，两列使用相同的负载形态和相同的测试工具：三个服务节点（`t2d-standard-4`）、三个数据库节点（`n2d-standard-4`）、一个负载生成器节点（`n2d-standard-4`），代理（上游原版，未做修改）运行在由三个私有 `t2d-standard-4` 节点组成的独立节点池上（为代理设置了污点）；启动盘为 100 GB 标准持久化磁盘，默认存储类为 `premium-rwo`，实例通过 `dcctl install --ha` 和 `dcctl bootstrap --no-tls --no-escrow` 安装，事件信封关闭，失败即关闭的 MQTT 确认关闭；混合负载为 500 台 HTTP 设备（每台 5 事件/秒，共 2,500 事件/秒）、1,500 个 MQTT over TLS 会话（每个 1 事件/秒）、500 台检测背景设备（每台 4 事件/秒，共 2,000 事件/秒）外加探针，以及命令（约 6,200 事件/秒入库），取 15 分钟稳态窗口。第一列不是已发布的 `v1.0.0` 镜像，而是主分支上写入器变更之前的最后一个构建。在代理与服务共用节点的形态下，该构建的测量结果与 `v1.0.0` 候选发布版相同；表中采用独立节点池形态，该次运行中代理的 CPU 因领导者分布不均而只是上限。

| 混合负载，约 6,200 事件/秒 | 之前（写入器变更前的最后一个构建） | v1.1.0 |
| --- | --- | --- |
| 每 1,000 个事件的平台 CPU | 2.60 CPU 秒 | 2.06 CPU 秒（降低 21%） |
| 每 1,000 个事件的关系型数据库 CPU | 0.402 CPU 秒 | 0.241 CPU 秒（降低 40%） |
| 每 1,000 个事件的事件存储（TimescaleDB）CPU | 0.377 CPU 秒 | 0.193 CPU 秒（降低 49%） |
| 每 1,000 个事件的 `device-state` CPU | 0.154 CPU 秒 | 0.064 CPU 秒（降低 58%） |
| 每条 `INSERT` 写入 `events` 的行数 | 8.4（代理位于服务节点上时测得） | 41.9 |
| 持久化延迟，中位数 / 第 99 百分位 | 21 ms / 167 ms | 17.6 ms / 107 ms |

持久化延迟没有上升。未在此配置下重复测量的项目（例如 3,000 个会话的纯 MQTT 负载）不在此列。

在相同的形态下（每一步使用各自全新的租户，预热 3 分钟，在 11 分钟稳态窗口内测量），MQTT over TLS（每个会话每秒一个事件）在 5,250 事件/秒以内，确认时间的第 99 百分位保持在 50 ms 以下，到 6,000 事件/秒时超过（整个运行期间为 60.1 ms；稳态窗口内每 10 秒第 99 百分位的平均值为 55.7 ms）。此时最繁忙的资源是一台代理服务器，服务节点和数据库节点仍有富余。这些都是单次运行，数字描述的是这一形态，而不是容量保证。

另有两项变更缩短了事件存储的写入路径。每个事件的标识现在直接计算，而不再经过 JSON 文档；标识逐字节不变，因此重新投递的事件仍能被识别。批次现在先用普通的 `INSERT` 写入，只有出现重复时才回退到 `ON CONFLICT DO NOTHING`，这去掉了仲裁器为每一行写入的第二条日志记录。在一次独立测量中，每个事件的事件存储 WAL 约降低 8%。新增计数器 `devicechain_eventmanagement_persist_insert_fallbacks_total` 显示批次走回退路径的频率；预期这种情况很少。

调优与指标：`device-state` 的 `projection.lingerMillis` 现在默认为 10（显式设置的 `0` 仍为 `0`）。`event-management` 中新增的指标 `persist_collect_full_total` 和 `persist_handoff_depth`，以及 `device-state` 中的 `state_collect_full_total` 和 `state_handoff_depth`，用于显示批次是否正在填满。`device-state` 的 `projection.maxBatch` 现在默认为 64（原为 32）。收集到的批次按租户拆分，每个租户的部分是一条 upsert，因此当多个租户并行运行时，32 的上限使每条语句只能装下批次的一小部分。在 4,500 事件/秒的 MQTT 负载下，64 对比 32：每个 `device_states` 批次的行数从 31.4 升到 49.4，`device-state` CPU 降低 10%，关系型数据库 CPU 降低 15%；由于批次要等待填满，`device-state` 交接时间的中位数从 8.0 ms 升到 13.2 ms，第 99 百分位不变。多租户负载看到的批次仍比单租户负载更小。显式设置 `maxBatch` 即可保持 32。

#### 流按故障缓冲时长定容 {#v110-runway}

为每个摄取的事件承载一条消息的那些流，过去的上限固定为 1 GiB 和 500 万条消息。这个上限是平台在消费者或数据库故障期间的缓冲时长，也是规则预览和检测重放能够回读的范围。在每秒 20,000 个事件时，它只能把 `resolved-events` 保留约 2.8 分钟。

- **两个新的 chart 值。** `instance.config.infrastructure.nats.streamDesignRate` 是预期整个实例（跨租户）摄取的事件速率峰值，单位为事件/秒。它是磁盘定容所依据的速率，不是限速。`instance.config.infrastructure.nats.streamRunwayMinutes` 是流按该速率保留多少分钟的事件（默认 15，范围 10 到 10080）。三个流各自至少获得 速率 × 分钟数 × 60 × 其单事件成本（捕获流和入站流为 384 字节，已解析流为 448 字节）。`streamMaxBytes` 仍是下限，`streamMaxMsgs` 会被调高，使消息数量不会缩短缓冲时长。
- **默认值让上限保持原样。** 未设置速率时，各流按以前的方式定容。chart 自身对 `streamDesignRate` 的默认值为 0（不按时间定容）；`dcctl` 的标准配置提供每秒 1,000 个事件、15 分钟，仍在 1 GiB 上限之内，`--compact` 提供每秒 50 个，保持 64 MiB。超出范围的值会导致启动失败。
- **设置方法：**在值文件的 `instance.config.infrastructure.nats` 下放入这两个键，然后运行 `dcctl upgrade <provider> <instance> --values <file>`。现有的流会原地采用新的上限。按每秒 20,000 个事件、15 分钟定容的流，需要约 33 GiB 的 JetStream 卷；完整对照表见[事件流能等待多久的故障](./observability.md#outage-runway)。
- **预留量预检会指明缓冲时长。** 当装不进卷的正是缓冲时长时，`dcctl` 会在写入任何内容之前失败并说明这一点。

##### 调低流的上限需要显式标志 {#v110-shrink}

把某个按事件计的流的上限调到低于它当前的数据量，会使代理立即丢弃最旧的消息，包括尚未读取的。代理管理器现在会拒绝这样做，并指出该流和缺口。当你确实要这样做时，例如在一次有意缩短缓冲时长的升级中，请传入一次，随后移除：

- chart 值 `brokerAdmin.allowCeilingShrink: true`（默认 `false`），
- `dcctl broker reconcile --allow-ceiling-shrink`。

调高上限，或者调低到该流仍然容得下的程度，不需要任何额外操作。

#### 限额内的 MQTT 设备在平台停滞后不再被丢弃 {#v110-rate-gate}

`event-sources` 按每个租户的上限计量其摄取。在捕获流中等待超过固定五秒的消息，按设备发送它的时间计量；更新的消息则按被读取的时间计量。当从捕获到入站的这一跳停滞数秒时，排队的流量会全部按停滞结束的那一刻计量，租户的突发额度只能吸收其中一两秒，其余的被丢弃，尽管租户在其上限之内，并且代理已经向设备确认了这些消息。

- **阈值按租户计算。** 当消息的年龄超过“原来的五秒”与“租户上限可吸收的突发量（突发量除以速率）”两者中较小的一个时，它被视为积压，并按其发送时间计量。该判定对每条消息只做一次，并同时交给两个计量阶段，因此消息阶段和读数阶段不可能把同一条消息路由到不同的限流器。
- **不变的部分。** HTTP 及其他不受信任的流量、集成和外部 MQTT 源仍像以前一样按读取时间计量。真正超过上限的租户仍会被限流。
- **新计数器与告警。** `total_msg_shed_acknowledged{source}` 统计代理已确认之后被丢弃的消息，仅限 MQTT 源，并以 0 导出。告警 `MqttIngestShedAfterAcknowledgement`（警告）在 `increase(...[10m]) > 0` 时触发，没有 `for:`，因此持续数秒后停止的突发仍能被看到。最后一次之后它保持十分钟。上限之内的租户不会丢弃任何消息，所以稳态下该序列为 0；超过上限的租户会使其升高，这也是应该的。

#### 告警 {#v110-alerts}

- **`JetStreamOutageRunwayShort`**（警告）为新增。它读取起门控作用的消费者的未读积压增长有多快，并在按该速率二十分钟内流将开始拒绝新事件时触发（流在 90% 时拒绝）。积压必须高于 10%，且条件须持续两分钟。持平或正在清空的积压保持安静。其文本提示：当流量超出设计速率时，应把 `streamDesignRate` 与 JetStream 卷一同调高。
- **`JetStreamUnreadBacklogNearFull`** 现在在 75% 且持续一分钟时触发（原为 80% 且持续五分钟）。按 R 分钟缓冲时长定容的流在 0.75R 时达到 75%，在 0.9R 时拒绝，所以对于小于约 17 分钟的缓冲时长，旧阈值会在拒绝之后才触发。
- **`MqttIngestShedAfterAcknowledgement`**（警告）为新增。参见[上文](#v110-rate-gate)。
- **`BrokerCertificateExpiringSoon`**（警告，30 到 7 天）和 **`BrokerCertificateExpiryCritical`**（严重，不足 7 天或已过期）为新增。它们读取代理 TLS 监听器当前正在提供的到期时间（`dc_broker_mqtt_tls_cert_not_after_seconds`），因此代理从未采用的续期会持续告警。
- **`JetStreamReplicaPeersDegraded` 读取在线对等节点，** 滞后有了自己的告警 **`JetStreamReplicaPeersLagging`**。旧的比较把稍有落后的跟随者与已离线的同等看待，所以在负载下会不断重新触发。新的指标 `jetstream_peers_online` 统计领导者加上每个未离线的对等节点。
- **`DeviceX509IssuerPinUnavailable` 不再因代理滚动而进入 pending。** 滚动期间代理系统账户出现两秒的抖动，过去会使告警保持 pending 约两分钟。现在这个等待写在表达式里：拒绝率必须在最近十次一分钟评估中的每一次都不为零。真正的故障仍在约十分钟后触发。

#### 实例上报观测到的状态 {#v110-status}

Operator 现在会在每个 `Instance` 上写入**观测到的**状态，并且是唯一的写入者。此前，`kubectl get dci` 在 `Phase` 列显示的是 dcctl 的意图，对象的 `status` 从未被写入。

```
NAME   PROVIDER   CLUSTER           PROFILE   REQUESTED   OPERATION   READY   RUNNING   AGE
prod   local      kind-devicechain  default   1.1.0       Ready       True    1.1.0     12d
```

- **列的变化。** `Phase` 改名为 `Operation`，`Version` 改名为 `Requested`，以免与新增的 `Running` 列混淆，并新增 `Ready` 和 `Running`。
- **状态内容。** `status.observedGeneration`，条件 `WorkloadsAvailable` 和 `RolloutComplete` 及其原因，`status.version.requested` 和 `.running`，以及每个功能区域一条 `status.areas[]`（期望、就绪和已更新的副本数，镜像）。不按租户或设备保存任何内容。控制器会监视实例的 Deployment，因此状态随滚动更新而变化。Operator 获得对 `apps/deployments` 的 `get`、`list` 和 `watch` 权限，其管理器缓存被限制为带实例标签的 Deployment。Instance CRD 随 operator 一同发布，不在 Helm chart 中。
- **代理证书到期。** 新的 `BrokerCertificateValid` 条件报告代理的 TLS 叶证书与证书颁发机构：`CertificateValid`、`CertificateExpiringSoon`（30 天或更少，仍为 True）、`CertificateExpired`（False，并使 `Ready` 为 False），以及 `CertificateNotPublished` 或 `CertificateUnreadable`（Unknown）。叶证书与颁发机构中较差的一个起决定作用。叶证书签发期限为一年，并在 `dcctl upgrade` 中续期，因此一个约十一个月没有升级的实例，过去会在没有任何提示的情况下失去代理 TLS。`dcctl bootstrap` 和 `dcctl upgrade` 会把两个 `notAfter` 日期作为注解发布在 Instance 上（`core.devicechain.io/broker-certificate-not-after` 和 `.../broker-ca-not-after`）。较旧的实例会在下次升级时补全，在此之前该条件显示为 `CertificateNotPublished`。

参见[实例状态](./kubernetes-operator.md#instance-status)和[代理证书到期](./kubernetes-operator.md#broker-certificate-expiry)。

#### 被拒绝的事件可以查看 {#v110-failed-events}

平台拒绝的事件会进入一个流，而此前没有任何东西读取它：“为什么我设备的数据没有出现”只能靠 Pod 日志回答。

- **被拒绝事件的索引。** `event-management` 把失败事件流读入新的 `failed_event_records` 表，保留七天（`lifecycle.failedEventRetentionDays`）。一行包含租户、时间、来源区域、原因、声称的设备令牌、事件类型、有长度上限的错误文本，以及原始载荷是否仍在流上；载荷不会被复制。同一租户、原因和设备在任意一小时内最多保留十行，其余记录会折叠进最新一行的 `suppressed` 计数，因此用捏造的设备令牌进行探测无法淹没该表。
- **租户 GraphQL。** `failedEvents`、`failedEventSummary` 和 `failedEventCount`，面向持有 `device:write` 的成员（限定客户范围的调用者会被拒绝）。最多回读 31 天。没有任何东西会重放或重试事件，API 也不会返回原始载荷。
- **控制台。** 一个摄取失败视图，带原因标签、设备过滤器和时间范围；当该设备的事件在过去 24 小时内被拒绝时，设备页面上会显示提示。
- **被丢弃的测量键会被记录。** 发送 `{"status_text":"ok","battery":50}` 的设备会得到确认，`battery` 被存储，`status_text` 被丢弃。[`v1.0.0` 的计数器](#v100-measurement-drops)说明整个设备群中这种情况的频率；现在 `device-state` 会记录哪台设备因何原因丢失了哪个键，包括计数、首次和最近时间，以及最近一次的值（截断为 64 字节），每台设备最多 32 个键。通过 `droppedMeasurements` 查询提供，并显示在设备页面上。计数反映的是频率，而不是精确总数。事件标识不变。

#### 可以回查在线状态 {#v110-presence}

- **在线状态时间线。** `event-management` 新增租户 GraphQL 查询 `stateChangeEvents`，按设备令牌和时间范围分页返回设备的在线状态变化（已连接、已断开、不活跃），从新到旧排列。它需要 `event:read`，并像其他事件查询一样通过事件的锚点限定范围，因此限定到客户的调用方只能看到自己的设备。控制台的设备页面把它显示为在线状态时间线。
- **设备页面显示更多连接信息。** 连接状态卡片现在显示最近一次在线状态报告及其所属的传输会话。
- **统一的不活跃窗口。** 在线状态为推断的设备在十分钟没有上报后被标记为不活跃，全平台统一；它从来不能按设备配置。清扫每 60 秒运行一次，因此这类设备在静默后 10 到 11 分钟离线。在线状态为断言的设备遵循显式的在线状态转换，不参与清扫。`DeviceState.inactivityTimeout` 始终报告该窗口（600 秒）。`inactivityAlarmTime` 字段已被[弃用](#v110-deprecations)。

#### 弃用 {#v110-deprecations}

**`DeviceState.inactivityAlarmTime`（device-state GraphQL）已被弃用。** 请改用 `lastDisconnectTime`。对于当前处于不活跃状态的推断设备，两者是同一时刻。被弃用的字段现在返回该值，对其他任何设备返回 null，因此设备恢复活跃后，它不再保留过去的不活跃时段。该字段将在下一个主版本中移除。`DeviceState.inactivityTimeout` 的形态不变，现在始终报告全平台统一的窗口（600 秒）。本版本没有其他弃用项。

#### 集群外的设备 {#v110-mqtt-exposure}

云集群上，不在集群内的设备过去没有受支持的方式连接平台的 MQTT 代理。现在可以按实例对外暴露代理在 TCP 8883 上的 TLS 监听器。**它默认关闭，升级后在你提出要求之前不会有任何变化。** 页面[从集群外连接设备](./connecting-from-outside.md)给出完整步骤：DNS、端口与防火墙、设备必须信任的证书、如何从外部检查，以及故障排查。

```bash
dcctl install   <provider> --mqtt-gateway                                   # 每个集群一次，仅 gateway 模式
dcctl bootstrap <provider> <instance> --mqtt-exposure gateway|loadbalancer|none --mqtt-host mqtt.example.com
dcctl upgrade   <provider> <instance> --mqtt-exposure ... --mqtt-host ...   # 或用 --mqtt-exposure none 关闭
dcctl upgrade   <provider> <instance> --broker-max-connections N          # 可选上限；0 表示清除
```

- **两种模式。** `gateway` 在实例的 Gateway 上按设备所连接的主机名做 TLS 透传路由，因此多个实例可共用一个地址。`loadbalancer` 创建一个 `LoadBalancer` Service，把 8883 直接指向代理 Pod，适用于不支持 Gateway API 的集群，或你想使用自己的负载均衡器时。
- **TLS 在代理处终止，绝不在入口处终止。** 两种模式都不会发布明文 MQTT（1883），并且如果代理自身的 TLS 已关闭，chart 会拒绝对其做暴露。设备的认证方式不变，仍通过凭据或[证书](./device-certificates.md)。
- **在写入任何内容之前就会被拒绝的情形：** 主机缺失或格式错误、主机已被集群上另一个实例占用、入口控制器未监听 8883 或实例不是经 Gateway 前端提供服务时使用 gateway 模式，以及代理证书无法包含的主机。
- **连接数上限。** `--broker-max-connections` 限制代理接受的连接数（设备与平台客户端合计），以抵御对外暴露端口上的连接洪泛。默认不设置，此时沿用服务器自身的限制。
- **记录在 `Instance` 上。** `spec.mqttExposure`、`spec.mqttHost` 和 `spec.brokerMaxConnections` 为新增字段。不带标志的升级会沿用其中记录的值。
- **限制。** 不支持 PROXY 协议，因此在 gateway 模式下代理看到的是入口控制器的地址而不是设备的地址，`callout.jitSourceTrusted` 必须保持关闭。在负载均衡器模式下，仅当前面的均衡器会保留客户端地址时才打开它。

#### 升级说明 {#v110-upgrade-notes}

- **平台服务登录名不能再通过 MQTT 认证。** 所有静态平台登录名现在都仅限使用代理的原生协议，只有 `event-sources` 自己的登录名例外，它的 MQTT 源和在线状态探测需要使用 MQTT。因此，泄露的平台凭据无法通过对外暴露的 MQTT 端口使用。设备不受影响，因为它们通过认证回调认证。如果你的脚本或工具用平台服务登录名连接 MQTT 监听器，请改用设备凭据或原生协议。
- **重新运行 `dcctl bootstrap` 会保留代理的证书颁发机构。** 设备会固定（pin）它，因此重新运行现在会沿用实例现有的颁发机构，只签发新的叶证书；如果设置了 MQTT 主机，叶证书会包含它。以前，重新运行会生成新的颁发机构。如果你依赖重新运行来轮换颁发机构，现在不会再轮换。新实例，以及被销毁后重建的实例，仍会得到新的颁发机构；这包括恢复到重建的集群，因为颁发机构不在任何数据库备份或根密钥托管中，固定了它的设备此时需要新的 `instance-ca.crt`。颁发机构存在但无法读取时，运行会失败，而不是替换它。
- **`Instance` 的新字段。** `spec.mqttExposure`、`spec.mqttHost` 和 `spec.brokerMaxConnections` 被加入 `Instance` 资源定义，该定义随 operator 一同发布，而不在 Helm chart 中。`dcctl install` 会带来它，这就是它要排在实例升级之前的原因。
- **`device-state` 的 `projection.maxBatch` 默认为 64。** 参见[摄取每个事件消耗的 CPU 更少](#v110-ingest)。
- **不带 `--mqtt-gateway` 的 `dcctl install` 会保留入口控制器已有的 8883 端口**，并且只要有实例声明了 gateway 暴露，`--mqtt-gateway=false` 就会被拒绝。

#### `dcctl` {#v110-dcctl}

- **`dcctl attr set|get|unset <kind> <token> <key> [value]`** 通过脚本写入分面值，与控制台的“分面”标签页作用相同。类型有设备、资产、区域和客户。`get` 输出表格或 `-o json`。写入进入共享作用域，这是分面选择器唯一读取的作用域；值的类型取自分面的声明，与之矛盾的 `--type` 会被拒绝。未声明的键需要 `--type`。它以租户成员身份登录（`--tenant`、`--email`、`--password`、`--otp`），并使用现有的 device-management GraphQL 调用，因此授权由服务器决定。不包含 CSV 批量导入和值的重命名。
- **`dcctl events failed`** 按从新到旧列出租户被拒绝的事件，支持 `--reason`、`--device`、`--since` 和 `--until`（RFC 3339 或 `6h` 这样的时长）、`--limit`（默认 100；匹配更多时输出会说明）和 `-o table|json`。`--summary` 输出按原因的计数和主要设备，默认统计最近 24 小时。它以租户成员身份登录，并需要 `device:write`。
- **`dcctl sim create --tier <token> --tier-config '<json>'`** 在负载运行所指名的层级不存在时创建它，已存在时只会调高（绝不调低）。例如 `--tier rc-100k --tier-config '{"ingestReadingsPerSecond":100000}'`。没有 `--tier` 的 `--tier-config` 会被拒绝，单独的 `--tier` 仍表示已存在的层级。层级缺失且没有 `--tier-config` 时，现在会失败并写明修复办法。
- **`dcctl sim create` 和 `dcctl sim destroy` 会发送实例的主机名。** 当 `--server` 是地址（IP 或 `localhost`）而不是名称时，它们拨向该地址，并把实例记录的主机名作为 Host 头和 TLS 服务器名发送，这样绑定了主机名的 Gateway 监听器才会接受请求。以前登录会因未指明原因的 404 而失败。新增的 `--host-header` 标志可覆盖记录的主机名，DNS 形式的 `--server` 保持不变，模拟器也遵循同一个头。如果无法读取实例的声明且登录失败，错误信息会指出该标志。
- **用于[集群外的设备](#v110-mqtt-exposure)的新标志：** `dcctl install --mqtt-gateway`，以及 `bootstrap` 和 `upgrade` 上的 `--mqtt-exposure`、`--mqtt-host` 和 `--broker-max-connections`。
- **`dcctl upgrade --allow-envelope-downgrade`** 见[事件信封](#v110-envelope)。
- **`dcctl broker reconcile --allow-ceiling-shrink`** 见[上文](#v110-shrink)。
- **`dcctl broker rebalance` 在代理滚动后可用。** 它此前找不到代理管理器的登录信息，修复后仍然只完成了部分领导者迁移，因为管理器可以读取流，却不能读取迁移所涉及的消费者，而拒绝表现为超时。管理器现在可以读取这些消费者；其权限的其他方面没有变化。`dcctl upgrade` 在结束时运行同一步骤。
- **`dcctl install` 修复卡住的 Barman Cloud 插件证书。** 在全新集群上，cert-manager 可能让插件的两个证书之一在一小时退避期内一直未签发，使插件停在 `ContainerCreating`，直到 Helm 等待超时。现在安装会在应用前置组件期间监视这两个证书，对创建三分钟后仍未就绪的证书，将其删除并重新创建。
- **`dcctl --values` 接受分析器开关：** `functionalAreas.<area>.profiler.enabled`、`.blockRate` 和 `.mutexFraction`。`profiler.address` 被刻意排除，因为绑定到非回环地址会暴露未经认证的性能剖析；请通过端口转发采集。

#### 性能剖析开关 {#v110-profiling}

这些开关默认全部关闭，用于性能调查。阻塞和互斥锁剖析过去一直是空的，因为采样从未开启；设置了 `profiler.blockRate` 和 `profiler.mutexFraction` 后，相应端点会提供它们。只设置其中之一而不设置 `profiler.enabled` 会拒绝启动，chart 在渲染时也会拒绝。OpenTofu 集群根新增用于关系型数据库上 `pg_stat_statements` 的 `postgres_statement_stats`；实例根新增用于代理的 `nats_profiling_port` 和 `nats_profiling_block_rate`，以及用于事件存储上 `pg_stat_statements` 的 `timescale_statement_stats`。监控模块还会在带污点的节点池上运行 node exporter（对每个污点都设置容忍），因此专用的代理节点池不再在节点 CPU 中缺失，也不会触发 `KubeDaemonSetRolloutStuck` 或 `TargetDown`。

#### 较小的变更 {#v110-smaller}

- **.NET SDK 8.0.427。** 固定的 SDK 从 8.0.425 升到 8.0.427（运行时 8.0.33），以应对 .NET Brotli 解码器中一个已公布的安全公告，较旧的运行时包含该问题。所有已锁定的 .NET 项目，包括 C# SDK、其 AOT 宿主以及演示和模拟器项目，都重新锁定到 8.0.33 包；变化的只有版本和内容哈希。如果你自行构建其中任何一个，请先安装 SDK 8.0.427。
- **前端工作区把 `minimatch` 固定为 11.0.1 或更高版本**，以应对一个已公布的安全公告，并包含例行的 Go、npm 和容器基础镜像更新。
- **文档。** 新增：从集群外连接设备的页面、实例状态一节、事件信封一节、故障缓冲时长页面、故障事件指南中的“从命令行”一节，以及分面的脚本编写一节。实测吞吐量页面为 `v1.0.0` 新增了六十分钟混合负载一行。在线状态页面现在说明全平台统一的不活跃窗口。发布说明页头不再带有 1.0 之前的警告。
- **单样本脉冲**到上升沿的映射现在由测试固定；行为不变。

#### 事件信封（可选启用） {#v110-envelope}

事件信封把同一租户的多个事件放进入站事件流和已解析事件流的一条消息中，而不是每个事件一条消息，从而减少代理处理每个事件的工作量。它由两个设置控制，**两者默认都关闭**：`infrastructure.messaging.envelopes.resolved` 和 `infrastructure.messaging.envelopes.inbound`。关闭时，流的格式不变，无需任何操作。无论设置如何，本版本中的所有服务都同样读取信封和单事件消息；这些设置只决定服务是否写入信封。开启它们的规则、检测引擎重复保护的开销以及降级保护，见[事件信封](#event-envelopes)。

- **何时安全。** 实例的每个 Pod 都运行 `v1.1.0` 或更高版本之后，而不是之前：较早版本的 Pod 会把信封当作失败消息搁置。请先升级，等所有 Pod 都已滚动完成，再设置开关。
- **入站信封。** `event-sources` 以简短的预测性等待对同一租户的事件分组：安静的租户立即以单事件形式发布，繁忙的租户最多等待 `inboundLingerMillis`（默认 5 ms，最大 10）等下一个事件。`HTTP /events` 只有在代理确认了包含该事件的消息之后才返回 202。随后 `device-management` 为每个入站信封写入一个已解析信封。
- **检测引擎跳过重复。** 开关开启后，检测引擎会在平台自身的重投递跨度（约十分钟的代理时间）内记住它已应用的每个事件的标识，并跳过第二份副本，计入 `detect_duplicate_events_skipped_total`。在 6,000 事件/秒时内存约 190 MB，12,000 事件/秒时约 300 MB，20,000 事件/秒时约 540 MB（外推值），仅在检测领导者上；开关开启期间，chart 会提高 `event-processing` 的内存上限（参见[服务规格](./bootstrap.md#service-sizing)）。晚于该跨度到达的副本会被应用两次；[事件信封](#event-envelopes)说明了这一残余情形。
- **新指标：** `inbound_envelope_members{hop}`（每个信封中的事件数）和 `inbound_envelope_linger_seconds{hop}`、结算计数器，以及 `detect_guard_*` 系列。
- **在本地集群上测得，尚未在云集群上测量。** 在有三个 worker 的本地 `kind` 集群上，以 1,500 事件/秒（1,000 经 MQTT，500 经 HTTP）、两个开关都开启、每个信封约八个事件的条件下，每个事件的代理消息数下降约 41%（116.5 降到 68.2），每个事件的代理字节数下降约 37% 到 40%，每个事件的平台总 CPU 下降约 12%（每 1,000 个事件从 7.48 降到 6.60 CPU 秒）。`device-management` 每个事件的 CPU 减少 31%，`device-state` 减少 24%，检测引擎减少 47%，代理减少 14%。事件存储做的工作更多而不是更少：它每个事件的 CPU 上升约 23%，每条 `INSERT` 的行数从 16.1 降到 12.9，因为它从信封消息收到的批次更小、更多。持久化和确认延迟在测试环境的噪声范围内没有明显变化。请把这些数字看作在一种负载形态下效果的方向和大致幅度，并在依赖它们之前测量你自己的负载。
- **关闭与回退。** 把两个值都设为 `false`。已在流中的信封会保留到过期或被消费。`dcctl upgrade` 会记录开关何时被开启，并在两者都关闭且滚动完成后记录每个流最新消息的位置。此后，只要有开关处于开启状态，或某个流中仍有开关关闭之前的消息，它就会拒绝降级到 `v1.1.0` 以下，并指出每个处于开启状态的开关。要回退，请关闭两个开关，让流越过记录的位置，然后再回退。`--allow-envelope-downgrade` 会带着警告跳过该拒绝，之后较早版本的服务会丢失它遇到的任何信封中的事件。代理管理器对这两个流拥有读取权限，用于这项检查。

#### 回退 {#v110-going-back}

两个迁移各新增一张表（事件存储中的 `failed_event_records` 和 `device-state` 中的 `dropped_measurements`）。数据库中没有需要撤销的内容。尽量向前推进：升级失败时，再次运行 `dcctl upgrade` 即可修复。

- **事件信封。** 如果两个开关中有任何一个曾被开启，请在回退到 `v1.1.0` 以下之前，关闭两个开关，并让事件流越过 `dcctl` 记录的位置。否则 `dcctl upgrade` 会拒绝该变更，而 `v1.1.0` 以下的版本无法读取信封。参见[事件信封](#v110-envelope)。
- **对外暴露的 MQTT。** `v1.1.0` 以下的版本不认识暴露设置。请先运行 `dcctl upgrade <provider> <instance> --mqtt-exposure none`，有意识地关闭该端口。
- **流的大小。** 如果在设置了 `streamDesignRate` 之后回退到 `v1.0.0`，`v1.0.0` 在启动时会把事件流恢复为它自己的固定上限，而容量超出该上限的流会立即丢弃最旧的消息（`v1.0.0` 没有调低保护）。回退之前，请让事件流排空，直到每个流的数据量都远低于其 `v1.0.0` 上限，或者继续使用 `v1.1.0`。

#### 已知问题 {#v110-known-issues}

- **失败即关闭的 MQTT 确认默认关闭，** 与 `v1.0.0` 相同。[MQTT 传输](../reference/transport-matrix.md#mqtt--the-platform-broker)页面给出了关闭和开启时的实测限制。
- **更改代理配置仍会断开由认证调用放行的设备，** 直到代理库包含上游修复。证书续期不会。

### v1.0.0：首个稳定版本，数据库故障时事件被保留，Traefik 成为默认入口 {#v100-upgrade}

`v1.0.0` 是首个稳定版本。它是从 `v0.27.0` 起的原地升级：先对集群运行 `dcctl install`，再对每个实例运行一次 `dcctl upgrade`，方法见[v0.17.0 的说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade)。请给 `dcctl install` 传入集群安装时使用的设置（如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`），以及 `--version`。本版本不运行任何迁移，也不改写任何行，因此除升级本身外没有数据步骤。如果从 `v0.26.0` 或更早版本升级，请先阅读 [`v0.27.0` 的说明](#v0270-upgrade)，并在其中使用 `--version v1.0.0`。由 `v0.16.0` 或更早版本创建的实例仍然[低于升级下限](#pre-declaration-recreate)，需要重建而不是升级。

```bash
dcctl install local --version v1.0.0
dcctl upgrade local devicechain --version v1.0.0 --dry-run
dcctl upgrade local devicechain --version v1.0.0
```

从本版本起适用[版本与升级策略](./versioning-policy.md)：其中列为受保护的接口，只在主版本中发生不兼容变更。CI 中的检查会把每个受保护的接口（设备线路格式、GraphQL Schema、`dcctl` 命令与标志、npm 包和 C# SDK）与上一个发布比较，任何破坏性变更都必须写进检查所读取的文件。

需要采取的行动，按执行顺序如下：

- **把告警过滤器 `originator` 改名为 `originatorToken`。** 在 `device-management` 的 GraphQL Schema 中，`AlarmSearchCriteria`、`AlarmCountCriteria` 和 `Subscription.alarmStream` 此前接受 `originator`，而 `Alarm`、`AlarmEvent` 和 `AlarmState` 早已返回 `originatorToken`。这三个输入现在接受 `originatorToken`，且没有别名：仍然发送 `originator` 的请求会被拒绝。MCP 工具 `list_alarms` 也改为接受 `originatorToken`。控制台和仪表板包已经更新。参见[告警过滤器用令牌指明发起者](#v100-alarm-filter)。
- **如果直接用 Helm（而非 `dcctl`）安装 chart，且你的实例由 ingress-nginx 提供服务，请在升级 chart 之前设置 `ingress.controller=nginx`。** chart 的默认值现在是 `gateway`。新的默认值会渲染 Gateway 及其路由并删除 Ingress 对象，如果集群没有 Traefik 的 `GatewayClass`，该主机将不再响应。`dcctl` 总是自行指明入口，因此不受影响。参见[Traefik 是默认入口](#v100-front-end)。
- **如果你直接应用 OpenTofu 的集群根，** `enable_ingress_nginx` 现在默认为 `false`，新集群不会再安装 ingress-nginx。设为 `true` 可在已运行该控制器的集群上保留它。`dcctl install` 在集群中发现 ingress-nginx 时会替你这样做。只要还有属于其类的 Ingress，该根仍然拒绝移除控制器。
- **当数据库无法接受写入时，事件会被保留而不是丢弃。** 这对在故障期间监视死信去向的任何东西都是行为变化。随之而来的告警见[数据库故障会保留消息](#v100-store-hold)。
- **回退。** 参见[回退](#v100-going-back)。

#### 数据库故障会保留消息，而不是丢弃 {#v100-store-hold}

在一次使用 32 GiB 事件存储卷的负载测试中，卷在事件存储的行数达到约 5100 万时被写满。主库陷入崩溃重启循环，报 “No space left on device”，负载工具统计到 1,722,366 个事件已被代理确认、但平台没有存储。原因是投递次数上限。事件存储拒绝写入时，`event-management` 让消息保持未确认，代理每 60 秒重新投递一次。到第五次投递（约四分钟后），它把事件发布到一个无人读取的去处并将其确认。如果数据库对每条语句都立即返回错误，整个在途窗口都会被丢弃，故障持续多久，就每四分钟丢弃一个窗口。没有任何告警。

- **`event-management` 保留存储拒绝的事件。** 当服务器表示无法接受写入（磁盘已满、连接过多、只读、I/O 错误），或连接类错误经 `SELECT 1` 探测失败确认时，写入器会让该批消息保持未确认，告知代理仍在处理，并且只在存储通过探测应答后才再次写入。积压会在流中等待、不被读取，现有的背压闸门会在流丢弃未读消息之前拒绝新事件。存储正常应答时让写入器崩溃的消息，仍像以前一样在用完投递预算后被搁置。新增指标为 `devicechain_eventmanagement_persist_store_held_events`。
- **同样的保留覆盖其他在最后一次投递时丢弃消息的消费者。** `device-management` 中：事件解析器、`raise-alarm` 和 `device-reports`。`command-delivery` 中：`command-responses`、`dead-letter-writeback` 和 `device-roster`。`notification-management` 中：`alarm-notification`。`update-management` 中：`device-reports` 和 `device-roster`。`device-state` 中的实时状态投影有意不处理，因为它可以从流重建；其所有权消费者会保留。每个保留中的消费者都会上报 `devicechain_<service>_store_held_messages{consumer}`。
- **通知既不会重复，也不会遗漏。** 已发送通知之后的写入（通知记录，以及清除静默时段所保留的内容）现在会原地保留，并在存储应答后重新执行。只重试写入，不重复发送。Webhook 或 SMTP 发送无法做到恰好一次：如果服务在发送与写入之间停止，记录会丢失，并且会写入日志。
- **保留中的服务每重启一次，就消耗一条消息五次投递中的一次。** 跨越多次重启的保留仍可能走到最后一次投递。
- **五个新告警。** 事件存储卷剩余空间不足 80%，且按最近两小时的速率将在 24 小时内写满时，触发 `DatabaseVolumeFillsWithinDay`（警告）。任何数据库卷使用超过 95% 时触发 `DatabaseVolumeCritical`（严重）。`event-management` 正在保留其数据库拒绝的事件时触发 `EventStoreRefusingWrites`（严重）。积压高于 1,000 且持续十分钟稳步上升时触发 `EventPersistBacklogGrowing`（警告）。`ConsumerHoldingForDatabase`（严重，按服务和消费者）覆盖其他服务。现有的卷告警和 JetStream 告警不变。参见[事件存储无法接受写入时](./observability.md#event-store-down)和[其他服务的数据库无法接受写入时](./observability.md#service-store-down)。
- **三个新的持久化指标**给出每个已存储行的延迟：`persist_rows_total`、`persist_batch_rows` 和 `persist_commit_duration_seconds`（1 ms 到 2.5 s 的分桶）。`persist_duration_seconds` 不变；它按消息计量并包含批次等待，因此会随一次提交中的行数上升。
- **容量规划指引。** 一行事件存储数据实测约 600 字节（未压缩）。[事件存储容量规划](./tuning-an-instance.md#event-store-sizing)给出计算指标带来的倍数、保留期和压缩，以及如何在线扩容卷。

这一保留机制在 kind 集群上做了端到端验证：事件存储被设为只读七分钟，同时以每秒 200 个事件的速率发送 QoS 1 MQTT。两次运行中，全部 144,000 个事件都恰好存储一次，四个死信去向均未增加任何消息。同样的故障下，保留机制之前的构建丢失了约 37,500 个。该对比以 `hack/outage-rig.sh` 运行。慢速失败的故障（连接挂起而不是报错）会使写入器变慢而不是耗尽投递次数，因此更早的构建丢失多少取决于故障形态；保留机制对两种情况都适用。

这一变更的限制：代理已向 MQTT 设备确认、但仍在捕获流中的事件，在该流写满时仍可能被淘汰，除非开启了失败即关闭的 MQTT 确认。参见[已知问题](#v100-known-issues)。

#### 检测更快，重建的设备可以再次触发告警 {#v100-detection}

- **检测在一个周期内发布。** 触发的检测过去要等到下一个检查点（默认 10 秒）才进入反应阶段，所以在安静的流上，规则的告警及其 `sendCommand` 会在读数之后 0 到 10 秒才出现。告警与命令此前总是在几毫秒内先后写入。现在待发布的检测在每个周期都会发布。提前发布不提交也不确认任何内容；崩溃后，同样的检测会以同样的标识重新推导，流的重复窗口会将其合并。
- **删除后以相同令牌重建的设备可以再次触发告警。** 设备被删除后，引擎仍保留该令牌的已触发锁存、游标、窗口和定时器，所以重建后的设备的第一个越限读数发现规则已被锁存，在其离开条件再重新进入之前不会触发任何告警。现在设备被删除时会清除该状态，并在每个领导任期开始时依据持久的花名册进行对账，因此任期结束时仍在排队的删除也能覆盖。
- **规则触发次数是统计值，不是下限。** 检测引擎页面曾说，被重放放大的计数应当作下限。重放之后它可能偏高，规则健康计数器也可能偏低，所以两个边界都不成立。要得到准确的账目，请使用按标识合并后的告警和命令。另外，一行只存储一次，并不证明只发生了一次实际动作，因为向设备或接收方的投递可能重复。

#### 被丢弃的测量值现在会计数 {#v100-measurement-drops}

设备在有效读数旁发布一个文本值，例如 `status_text` 为 `"ok"`、`battery` 为 50，该事件会被确认，有效读数会被存储；文本条目过去被丢弃，只在 `device-management` 日志中留下一条警告。计数器 `resolve_measurement_dropped_total{reason}` 现在对此计数，原因为 `undeclared_non_numeric`、`undeclared_unstorable` 和 `declared_not_storable`。警告按租户、设备、指标和原因各记录一次，并说明事件的其余部分已存储。契约不变：事件不会被拒绝，类型错误的已声明指标仍会进入死信。真/假值只对声明为布尔的指标转换为 1 或 0。

#### 告警过滤器用令牌指明发起者 {#v100-alarm-filter}

GraphQL 接口上其他所有令牌引用都叫 `...Token`，`Alarm`、`AlarmEvent` 和 `AlarmState` 也暴露 `originatorToken`，但告警过滤器却接受 `originator`，因此把字段名当过滤器名复用的脚本会报错。现在过滤器与之一致：

| 输入 | 之前 | 现在 |
| --- | --- | --- |
| `AlarmSearchCriteria` | `originator` | `originatorToken` |
| `AlarmCountCriteria` | `originator` | `originatorToken` |
| `Subscription.alarmStream` 参数 | `originator` | `originatorToken` |

`originatorType` 不变，提供令牌时仍然必填。这三处移除已在 GraphQL 兼容性检查的破坏性变更文件中声明。由 Schema 重新生成的强类型客户端在改用新名称之前无法编译。

#### Traefik 是默认入口，ingress-nginx 已弃用 {#v100-front-end}

Kubernetes 项目已停用 ingress-nginx。新集群现在仅由 Traefik 和 Gateway API 提供服务，ingress-nginx 已弃用。不会有任何悄然迁移：`dcctl` 明确指定入口，因此 chart 的默认值只影响直接用 Helm 做的安装。

- **只有 ingress-nginx 的集群** 继续用它为实例提供服务，`dcctl upgrade` 每次运行都会打印弃用警告。
- **同时有两个控制器的集群** 与 `v0.26.0` 和 `v0.27.0` 相比没有变化。实例会在其 Ingress 对象旁获得一个 Gateway，迁移 DNS 由你负责，`--drop-legacy-ingress` 和 `--remove-legacy-ingress` 仍受保护。
- **已记录的 `--ingress-controller nginx` 选择会在升级中保留。**
- **在已有 ingress-nginx 的集群上运行 `dcctl install` 会保留它。** 它会传入 `enable_ingress_nginx=true`，使新的默认值不会被当作移除。
- **`nginx` 选项再保留一个次版本。** 请利用这段时间[把实例迁移到 Traefik](./ingress-cutover.md)。

#### 安装与运行 `dcctl` {#v100-dcctl}

这些问题是在一台干净的、装有 Docker Desktop 的 Windows 机器上，从公开发布安装时发现的。

- **`dcctl preflight` 检查主机端口 80、443 和 1883。** 被其他容器占用的端口会使检查失败，早于 `dcctl install` 以 Docker 自己的消息失败，并且检查会指出占用者。已持有这些端口的集群通过检查。Docker Desktop 不再被报告为不适用，首次安装前没有当前上下文仅为提示。
- **Preflight 在 Windows 和 macOS 上检查磁盘。** 它检查保存 `dcctl` 状态和 Docker Desktop 磁盘映像的卷的可用空间（低于 40 GiB 警告，低于 20 GiB 失败），并打印 Docker 自己的磁盘用量。Docker Desktop 的虚拟机上限无法从命令行读取，所以输出会提示你自行确认。
- **打印的控制台 URL 和 `dcctl sim create` 的 MQTT 地址取自 kind 控制平面容器实际发布的端口。** 无法确定时，会打印此前的文字。
- **被强制终止的 `dcctl bootstrap` 可以重新运行。** 在基础设施步骤中被终止的运行会留下托管（escrow）制品，需要 `--restore-root-key` 才能完成。现在重新运行会在该制品指明本实例、实例的凭据 Secret 已在集群中、且口令能打开它时采纳该制品。其他情况仍像以前一样拒绝。过期锁的拒绝信息现在会说明持有者何时是本机上已不再运行的进程。
- **没有终端且没有 `--yes` 的 `dcctl install`** 不再报告创建被拒绝。
- **`dc-simulator` 附在每个发布中，** 与 `dcctl` 和 `dc-edge-agent` 并列，覆盖 Linux、macOS 和 Windows 的 `amd64` 与 `arm64`，附校验和与来源证明。`dcctl sim run --simulator ./dc-simulator` 不再需要 Go 编译器，`dc-simulator -version` 会报告发布版本。`dcctl sim run` 通过 `event-sources` 的设备接入监听端口发布数据，标准安装不会在集群外暴露该端口，因此运行期间请保持 `kubectl -n dci-devicechain port-forward svc/event-sources 8081:8081` 处于运行状态（参见[首个设备指南的第 5 步](../quickstart/first-device.md#5-open-a-path-to-the-ingest-endpoint)）。

#### 仪表板 {#v100-dashboards}

- **由设备类型的配置文件生成的看板修复了四处问题。** 它按单位各画一张图，而不是把所有测量值放在同一根轴上（没有单位的测量值单独一张图；每个看板最多四张图，每张图最多四个系列）。它的设备选择器只提供该类型的设备，这通过设备槽上新增的可选 `candidates.deviceType` 过滤器实现。仪表板令牌会自动填入，而不再因占位符而阻塞“创建”。缺少 `command:write` 的查看者看不到命令按钮的表单和“发送”按钮；界面会显示原因，并保留只读历史。
- **限定到客户的查看者不再触发被拒绝的调用。** 打开仪表板时，每个区域和资产锚点都会先发起一次被拒绝的 `anchorDevices` 调用，再不带关系类型重试。现在会从访问令牌读取查看者的范围，不再发送关系类型。服务器不变，因此限定范围的成员能看到的内容没有扩大。

#### 其他较小的变更 {#v100-smaller}

- **`update-management` 说明如何配置对象存储。** `createArtifact` 上的拒绝 `OBJECT_STORE_NOT_CONFIGURED` 现在会指出 `dcctl bootstrap --object-store filesystem|s3` 和 chart 值 `instance.config.infrastructure.blob.backend`。
- **空中升级的文档以已验证的内容为准。** 受管升级仅支持 MQTT，LwM2M 设备用普通命令升级，Sparkplug 没有定义固件升级，分批、金丝雀和计划在 1.0 中不提供。一套可重复的测试台现在覆盖签名验证失败、上传时摘要不匹配、续传下载、离线分配、取消、截止时间和暂停的发布。
- **凭据类型词汇表已冻结**，由带负向对照的测试保证，并移除了六条已过期的兼容性破坏声明。
- **新的指南和示例。** [功能导览](../quickstart/feature-tour.md)走完从设备类型到只看到自己设备的客户用户的整条路径，只需更改一个参数文件即可为第二个站点运行。`examples/migration-pilot` 是迁移自其他平台的实操评估，链接自[迁移指南](../guides/migrating-from-another-platform.md)。其他示例涵盖使用 X.509 证书登录的设备、ChirpStack 上行消息，以及触发告警并向设备发送命令的规则。快速入门新增了获取 `dcctl` 的步骤，并说明了必须空闲的主机端口，以及如何从被强制终止的 bootstrap 中恢复。
- **Sitepulse 和 Towerpulse 演示重新显示平台读数。** 在 `v0.27.0` 中，它们的实时设备卡片从未显示平台上报的内容，因为共享观察器创建了测量流却从未启动它；设备本身发布正常。请用此版本重新构建演示播放器。`dcctl sim create --manifest towerpulse` 现在会自行配置其演示层级，仅为该层级启用计算指标；所有默认层级中计算指标仍保持关闭。
- **西班牙语翻译现在像中文一样**按新鲜度对照英文页面进行跟踪。

#### 回退 {#v100-going-back}

本版本不运行任何迁移，因此数据库中没有需要撤销的内容。请尽量向前走：升级失败后，再运行一次 `dcctl upgrade` 即可修复。如果回退到 `v0.27.0`，已改为发送 `originatorToken` 的 GraphQL 客户端也必须一并回退。

#### 已知问题 {#v100-known-issues}

- **失败即关闭的 MQTT 确认默认关闭，** 与 `v0.24.0` 到 `v0.27.0` 相同。关闭时，代理已确认的事件在捕获流写满时仍可能丢失。[MQTT 传输](../reference/transport-matrix.md#mqtt--the-platform-broker)说明了关闭与开启时的实测限制和丢失上界。
- **更改代理配置仍会断开由认证调用放行的设备，** 直到代理库包含上游修复。证书续期不会。
- **有两条路径不在保留机制的覆盖范围内。** 读取器已提前取到、且早于确认等待时间一半的消息，在写入器保留时会被不确认地丢弃并消耗一次投递，记录达到最后一次投递的消息的记录器可能在写入器保留它之前为其写入一封信。记录器的重新发布也不在覆盖范围内。

### v0.27.0：设备使用证书登录，Sparkplug 命令可用 {#v0270-upgrade}

`v0.27.0` 是从 `v0.26.0` 起的原地升级：先对集群运行 `dcctl install`，再对每个实例运行一次 `dcctl upgrade`，方法见[v0.17.0 的说明](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade)。请给 `dcctl install` 传入集群安装时使用的设置（如 `--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`），以及新的 `--version`。如果从 `v0.25.2` 或更早版本升级，请先阅读 [`v0.26.0` 的说明](#v0260-upgrade)，并在其中使用 `--version v0.27.0`。

```bash
dcctl install local --version v0.27.0
dcctl upgrade local devicechain --version v0.27.0 --dry-run
dcctl upgrade local devicechain --version v0.27.0
```

该版本让设备可以[使用 X.509 证书认证](../guides/device-x509.md)，让 Sparkplug B 命令可用，在不断开设备会话的情况下续期代理证书，并为客户用户提供区域和资产仪表板。需要采取的行动，按执行顺序如下：

- **如果不使用 `dcctl` 安装 chart，且代理的认证调用已开启，请为其提供代理的系统登录。** 认证调用现在会检查证书连接请求是否来自在线的代理服务器，并向代理的系统账户索取该列表。没有 `infrastructure.nats.auth.sysUser` 和 `sysPassword`，device-management 会拒绝启动。请设置它们，或设置 `callout.issuerPinDisabled: true` 以不做该检查运行（服务会记录一条警告）。使用 `dcctl` 的安装已经具备该登录。参见[将请求绑定到代理](./device-certificates.md#issuer-pin)。
- **对已有 Sparkplug 数据源或 LwM2M 身份的实例再次运行 `dcctl bootstrap`，必须说明如何处理它们。**（对正在运行的实例，`dcctl bootstrap` 本来就拒绝重建；这里针对的是重试未完成的 bootstrap。）现在除非再次传入同一个文件（`--sparkplug-source`、`--lwm2m-identities`），或有意撤销它们（`--withdraw-sparkplug-sources`、`--withdraw-lwm2m-identities`），否则会拒绝执行。此前不带该标志重新运行 bootstrap 会悄悄移除这些凭据。`dcctl upgrade` 与以前一样会保留它们。
- **升级后请轮换 LwM2M 预共享密钥。** 参见 [LwM2M 密钥离开 Helm release](#v0270-lwm2m)。
- **你手动添加到 release 的 `lwm2m-ingest` 块中的设置，升级时不再被保留。** 请把它们放进[值文件](./tuning-an-instance.md)，并用 `--values` 传入。
- **如果你在 chart 值中设置了 `extraSecrets`，请注意它的校验和变了。** `checksum/extra-secrets` 现在只涵盖每个 Secret 的名称和键名，从不涵盖值。只改一个值不再重启 Pod；值变化时请自行重启。
- **带类型的 GraphQL 客户端必须接受 `ProvisioningProfileSecretResult.issuedSecret` 为 `null`。** 指定信任锚的自动注册配置没有密钥。有密钥的配置仍与以前一样只返回一次。
- **从此版本只能向前，不要回退。** 参见[回退](#v0270-going-back)。

升级会运行两个迁移，每个在拥有它的服务首次启动时运行，被中断后都可以再次运行：`device-management` 中是一张信任锚表，以及凭据和自动注册配置上指向信任锚的列；`user-management` 中是租户清除记录上的两个可为空的列，记录数据何时被确认已擦除，以及最后一次检查的时间。两者都不重写任何行。

#### Sparkplug B 命令可用，`dcctl` 接受 Sparkplug 数据源 {#v0270-sparkplug}

在 `v0.26.0` 中，带点号的命令键会被拒绝，而 Sparkplug 命令类型（`sparkplug.write` 以及 rebirth 和 reboot 命令）正是这样命名的，所以无法创建任何 Sparkplug 命令。现在命令键可以用点号划分命名空间，每一段遵循与以前相同的规则。与 `v0.26.0` 一样，Sparkplug 命令在你为某个数据源开启之前保持关闭。

- **被拒绝的命令会说明原因。** 在到达节点之前被拒绝的命令（`FAILED` 或 `NOT_DELIVERED`）现在会把机器可读的原因作为存储的响应，例如 `commands_not_enabled`、`node_offline`、`node_rebirthing`、`metric_not_writable` 或 `reboot_not_allowed`。控制台会逐一解释。你之前依据旧错误文字写的匹配逻辑，应改为匹配原因代码。
- **先于设备类型出现的节点会被要求 rebirth。** 平台会记住该节点，并在注册失败 10 秒后请求它 rebirth，之后以翻倍的间隔（最长一分钟）重复，直到它注册成功。新增两个指标 `birth_recovery_pending` 和 `birth_recovery_rebirths_total` 可以观察这一过程。
- **`dcctl bootstrap` 和 `dcctl upgrade` 接受 `--sparkplug-source <文件>`。** 文件列出各数据源，代理密码保存在由 `dcctl` 自己写入的 Secret 中，之后的升级会保留两者。带该标志的 bootstrap 会开启该区域；带该标志的升级在该区域未运行时会拒绝，并指向 `dcctl enable-area`。参见[为 `dcctl` 提供 Sparkplug 数据源](./edge-services.md#sparkplug-sources)。`--values` 覆盖文件仍不能设置数据源。

#### LwM2M 密钥离开 Helm release {#v0270-lwm2m}

此前 `dcctl` 把每个 LwM2M 设备预共享密钥放在 Helm release 的值中，`helm get values` 会打印它，Helm 历史也会在每个修订中保留它。现在 `dcctl` 自己把密钥写入 Secret `dci-<实例>-lwm2m-identities`，位于 release 之外，release 只带有该 Secret 的名称和一个不由任何密钥派生的戳。

- **第一次升级会迁移密钥。** 它从 release 中读取一次密钥，在改变其他任何东西之前写入新的 Secret，删除旧的 `extraSecrets` 条目，然后运行 Helm 升级。设备保留它们已有的密钥。仍在运行的 Pod 会在环境中保留密钥，直到重启。
- **之后请轮换密钥。** Helm 在本次升级之前记录的修订（最多保留十个）仍保存旧值，任何对 release 的备份也一样。请用新密钥运行 `dcctl upgrade local <实例> --lwm2m-identities <文件>`；再次传入文件会重写 Secret 并重启 Pod。在旧修订过期之前，请限制谁能读取 Helm 存放 release 的命名空间（`default`）中的 Secret。
- **`dcctl upgrade` 新增 `--lwm2m-identities` 和 `--withdraw-lwm2m-identities`，** Sparkplug 的 Secret 以同样方式工作。不是 `dcctl` 写入的 Secret 永远不会被触碰。
- **缩减或删除要等 release 成功之后。** `dcctl` 在 release 之前添加密钥，只在 release 成功之后才移除不再被引用的密钥。失败的升级会为运行中的 Pod 保留旧密钥，下一次运行会完成收尾。如果 release 成功后最后一步失败，运行会给出确切的命令并发出警告，但不会失败。

#### 证书认证 {#v0270-certificates}

设备可以用 X.509 客户端证书向平台代理认证，证书可以固定到你创建的设备，也可以在首次连接时通过证书自动注册配置完成注册。在你添加信任锚之前，不会信任任何东西，所以升级不会改变任何设备的行为。令牌和密码设备照常连接。添加第一个信任锚之前请阅读[升级已在运行的实例](./device-certificates.md#cutover)，具体步骤见[使用证书连接](../guides/device-x509.md)。

- **代理的证书请求不再列出可接受的签发者。** Go 客户端或模拟器会扣留签发者不在代理列表中的证书；现在代理不再发送列表，设备会出示它所拥有的证书。
- **每个证书连接都会对照在线的代理服务器检查。** 参见[将请求绑定到代理](./device-certificates.md#issuer-pin)。新告警 `DeviceX509IssuerPinUnavailable` 在持续中断时告警，而不会在代理滚动时告警。
- **chart 带来三个告警和一个延迟直方图。** 在没有信任锚的实例上，`DeviceX509ChainFailuresSustained`、`DeviceX509JitRefusedSustained` 和 `DeviceTrustAnchorsExpiringSoon` 保持安静。`callout_duration_seconds{path,result}` 和警告级告警 `DeviceCalloutLatencyHigh` 覆盖认证调用的每条路径。

#### 代理证书续期不会断开设备 {#v0270-broker}

- **续期后的 TLS 证书不再断开 MQTT 会话。** 代理对同一机构下变更的证书直接用于新连接，而不重新加载配置，所以已连接的设备保持连接。在一次测试中，认证调用之后保持的 30 个会话在一次轮换后全部存活，下一次连接获得了新证书。
- **当到期是唯一原因时，`dcctl upgrade` 续期代理叶证书而不滚动代理：** 机构不变，且代理已按证书签发时的副本数运行。它写入 Secret，然后最多等待五分钟，直到每个代理 Pod 报告新的到期时间。如果无法确认，它会发出警告并滚动代理，所以不会在一次看似成功的续期之后仍然提供旧证书。机构、名称或副本数的变化仍会滚动代理。
- **更改代理配置仍会触发重新加载，** 重新加载仍会断开经认证调用放行的设备会话，直到代理库包含一个尚未出现在任何版本中的上游修复。凡是改变代理镜像或配置的升级，`dcctl upgrade` 都会滚动代理。
- **代理库是 nats-server 2.15.2，** 这是一个安全版本；其修复之一是认证调用现在遵守它所允许的连接类型。`dc-nats-server` 基于它构建。回退到上游代理的对照镜像是 `nats:2.15.2-alpine`，使用的设置是 `server_image_tag = "2.15.2-alpine"`。
- **升级滚动代理之后，`dcctl` 会平衡领导权。** 最后重启的服务器过去不领导任何流，发布确认的 p99 大约翻倍，直到领导权转移。`dcctl upgrade` 的最后一步现在会移动流和消费者的领导者以使各服务器均衡，并打印前后各服务器的领导者数量。这是尽力而为：失败只是警告，绝不会让升级失败。[`dcctl broker rebalance <实例>`](../reference/dcctl.md#dcctl-broker-rebalance) 可按需执行同样的操作，新告警 `BrokerLeadershipImbalanced` 在某台服务器领导的数量不足平均值一半时触发。

#### 入口滚动发布不再丢请求 {#v0270-ingress}

在云负载均衡器之后，Traefik 和 ingress-nginx 的 Pod 现在会在退出前先等待（`drain_seconds`，默认 40），新 Pod 必须就绪 `min_ready_seconds`（15）之后才会替换下一个，所以滚动发布不再在负载均衡器的健康检查追上之前丢失请求。这些值是 OpenTofu 控制器模块的变量；设为 0 即关闭。在 GKE 上，Traefik 滚动发布期间通过云负载均衡器每秒一次的请求循环没有出现失败请求，而此前的测量出现过失败。没有云负载均衡器的本地集群不变。在云集群上，首次应用这一变化的 `dcctl install` 会以更长的排空时间把每个控制器滚动一次。参见[边缘处的变化](./ingress-cutover.md#ingress-cutover-differences)。

#### 客户用户与控制台 {#v0270-customers}

- **客户用户可以看到其可见设备所在的区域和资产。** 该规则在存储层，与租户谓词并列：只要调用者可见的设备与某个区域或资产存在被跟踪的关系，该区域或资产就会出现，`anchorDevices` 列出其上的设备。区域和资产的名称对任何能看到其中设备的人可见；其他字段不予显示。关系图本身不会开放。
- **告警和事件历史可以按锚点过滤。** `alarms`、`alarmCountsBySeverity`、`alarmCountsByDeviceType` 和 `alarmStream` 订阅接受可选的 `anchor`（客户、区域或资产），结果限于调用者自己的范围。告警保留在其设备被触发时所在的区域之下。超出调用者范围的客户锚点会被拒绝。无需迁移。
- **控制台隐藏服务器会拒绝的操作。** 不再向客户用户提供新建设备、导入、取消命令或仪表板写入操作，显示最新读数的统计卡片会用最近存储的值填充，而不是等待下一个读数。

#### 租户删除会说明数据何时被擦除 {#v0270-tenant-erasure}

租户删除记录新增 `erased`、`erasedAt` 和 `lastVerifiedAt`。只有当所有存储在沉淀窗口内都保持干净时才会作出该声明；任何存储出现回退就撤回；空的账本读作“仍在检查”，绝不读作已擦除。`dcctl tenant-deletion status <令牌> [--wait] [--until erased|complete]` 在数据被确认擦除之前以非零状态退出。指标 `tenant_purge_erased_total` 统计这些声明。参见[确认数据已擦除](./tenant-deletion.md#erased-signal)，以及[擦除大型租户需要多久](./tenant-deletion.md#measured)中的实测结果及其未涵盖的范围。

#### 失败即关闭的 MQTT 确认仍为可选 {#v0270-fail-closed}

失败即关闭的 MQTT 确认（`--mqtt-fail-closed`）与 `v0.26.0` 一样，除非你开启否则保持关闭，并且在 1.0 之前仍是可选项。[MQTT 传输](../reference/transport-matrix.md#mqtt--the-platform-broker)现在说明了关闭和开启时的实测限制与丢失上界。不设置它的实例没有任何变化。

#### 回退 {#v0270-going-back}

从此版本只能向前，不要回退。两个新迁移添加了 `v0.26.0` 不认识的表和列，使用 LwM2M 的实例把密钥放在 `v0.26.0` 不会读取的 Secret 中。失败的升级通过再次运行 `dcctl upgrade` 来修复。

#### 已知问题 {#v0270-known-issues}

- **更改代理配置仍会断开经认证调用放行的设备，** 直到代理库包含上游修复。证书续期不会。

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

- **代理滚动重启之后（包括这次升级），最后一个重启的服务器不领导任何流，** 发布确认的 p99 大约翻倍，直到领导权重新平衡。**已在 `v0.27.0` 中修复：**`dcctl upgrade` 在滚动代理后会平衡领导权；参见[代理证书续期不会断开设备](#v0270-broker)。
- **TLS 证书轮换会使代理重新加载，并断开大多数 MQTT 会话。** 设备会自行重连。**已在 `v0.27.0` 中修复：**代理无需重新加载，即可对新连接使用续期后的证书，已建立的会话保持不变。更改代理配置仍会触发重新加载；在代理的 `nats-server` 库包含上游修复之前，重新加载仍会断开经认证调用放行的设备会话。
- **入口控制器的滚动发布会在 28 到 40 秒内丢弃约 5% 的请求。** 请在安静的时段进行。**已在 `v0.27.0` 中修复：**两个控制器会在 Pod 退出前先排空，并在移除旧 Pod 前等待，因此在云负载均衡器之后滚动发布不再丢失请求；参见[边缘处的变化](./ingress-cutover.md#ingress-cutover-differences)。

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
