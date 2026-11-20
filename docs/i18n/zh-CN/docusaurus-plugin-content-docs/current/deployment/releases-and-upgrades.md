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

## 发布线 {#release-lines}

每个次版本开启一条发布线，其补丁版本属于该发布线。最近三条发布线的完整说明在下方按从新到旧排列。较早的发布线在[较早版本](#older-releases)中保留简短条目。

- **[v1.7](#v170-upgrade)**: [v1.7.1](#v171-upgrade) · [v1.7.0](#v170-upgrade)
- **[v1.6](#v160-upgrade)**: [v1.6.0](#v160-upgrade)
- **[v1.5](#v150-upgrade)**: [v1.5.0](#v150-upgrade)

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

两条命令都请使用目标版本自带的 `dcctl`：一个 `dcctl` 只会安装并升级到它自己的版本，如果集群是由更旧的 `dcctl` 安装的，`dcctl upgrade` 会拒绝执行。运行 `dcctl install` 时，请使用安装该集群时的同样设置，例如 `--ha` 或 `--compact`。集群上有实例运行时，它会拒绝改变这些设置；参见[重新运行安装](./bootstrap.md#re-running-install)。

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

操作员检查有一种情况只警告，不拒绝。`dcctl install` 会给它应用的定义打上标识；
**手动**安装的 Operator 没有该标识，`dcctl` 无法区分有意手装与被旧 `dcctl` 覆盖。
该检查不推翻无法观察的选择，而是打印所需 install 命令后继续。
如果你并非手动安装 Operator，应把提示视为原本会发生的拒绝，继续前先运行 `dcctl install`。
下面的安装检查是另一项，仍然适用：`dcctl install` 从未完成的集群没有安装记录，
其升级会被拒绝，直到运行过 `dcctl install`。

**升级还会检查 install 已经运行，并且只升级到自身 dcctl 的发布。** Operator 的定义可以在多个发布之间保持不变而控制器已经变化，所以仅检查 Operator 无法发现 `dcctl install` 被跳过。因此 `dcctl upgrade` 还会读取集群中的安装记录（其中记有最后一次完成 `dcctl install` 的 `dcctl` 版本），并在以下情况**拒绝**：该版本早于执行升级的 `dcctl`、某次安装已开始但未完成，或集群没有可用的记录。拒绝信息会指出需要先运行的 install 命令，且不会更改任何内容。在已安装的版本上再次运行升级不会被拒绝，由更新版本的 `dcctl` 安装的集群也不会。从源码构建的 `dcctl` 不参与比较。从本发布起，用更新的补丁版本 `dcctl` 执行普通的 `dcctl upgrade` 也需要先运行 `dcctl install`。

显式的 `--version` 在 `dcctl upgrade` 和 `dcctl install` 中都受同一规则约束，两个方向都是如此。**早于**当前 `dcctl` 的发布会被拒绝，因为它的 chart 和基础设施配置属于它自己的发布，会围绕从未见过这些配置的镜像来规划；要回退，在该发布的说明允许时，请使用旧发布的 `dcctl`。**晚于**当前 `dcctl` 的发布同样会被拒绝，用当前 `dcctl` 去安装也无济于事：安装记录记录的是执行安装的 `dcctl`，而不是传入的 `--version`。请获取新发布的 `dcctl`，用它运行 `dcctl install`，然后再升级。`--version` 等于当前 `dcctl` 的发布和来自你自己的镜像仓库（`--registry`）的标签不受影响。不带 `--version` 的升级只有在实例声明的发布晚于当前 `dcctl` 时才会被拒绝。这两项拒绝都没有跳过选项。

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

最近三条发布线的完整说明按从新到旧排列。同一发布线内，补丁版本的说明位于其所修补版本的说明之前。

### v1.7.1 — 更新和活动命令只送达其目标设备 {#v171-upgrade}

`v1.7.1` 是补丁版本，可从 `v1.7.0` 原地升级：先对集群执行 `dcctl install`，再对每个实例执行一次 `dcctl upgrade`，如[滚动升级](#rolling-upgrades)所述，并使用 v1.7.1 的 `dcctl` 和 `--version v1.7.1`。`update-management` 在首次启动时追加一个迁移。该迁移添加一个可以为空的列，因此在升级滚动进行期间 `v1.7.0` 的 Pod 仍可正常工作。与每个版本一样，不支持降级。

```bash
dcctl install local --version v1.7.1
dcctl upgrade local devicechain --version v1.7.1 --dry-run
dcctl upgrade local devicechain --version v1.7.1
```

此版本不更改任何受保障的接口。它在现有代码词汇中新增一个拒绝与跳过代码 `DEVICE_REPLACED`。

#### 升级到 v1.7.1 之前 {#v171-before-upgrade}

- 在滚动升级期间新旧 Pod 并存时，`v1.7.0` 的 Pod 在被替换之前仍可能按旧方式推进正在运行的发布。请先完成升级，再删除并重新创建正在运行的发布所针对的设备。
- 升级前启动的发布只记录了其设备的令牌。对于这些发布，`update-management` 会比较每个设备的创建时间与发布的开始时间（参见[发布针对其启动时的设备](#v171-rollout-identity)）。创建时间与发布开始时间相差在五分钟以内的设备会被**暂缓**：不会通知它更新，发布保持 `RUNNING`。如果某个发布一直处于运行状态且有设备在等待，请中止它并启动一个新的发布。

#### v1.7.1 新增内容 {#v171-whats-new}

- **分配给重新创建的设备的更新保持不变。** 如果分配请求在设备被删除并以相同令牌重新创建之前查询了该设备，该请求现在会以 `DEVICE_REPLACED` 被拒绝，不再替换新设备的更新。此前，新设备会不再获得其更新，直到再次为其分配。
- **活动命令在重试时会重新检查分组。** 当活动的批次因暂时性原因创建失败时，这些命令会在稍后重试。现在重试前会先对照活动的分组进行检查：期间已离开分组的设备记录为 `NOT_IN_GROUP`，不会收到命令。如果无法读取分组，命令会等待；如果超过活动的延迟上限仍在等待，则记录为错过。
- **分片实例的备份包含所有分片组流。** 在配置了 `infrastructure.nats.shardGroups` 的实例上，`dcctl backup` 现在会将分片流的每个分片组流（`<stream>-g1`、`<stream>-g2` 等）与其基础流一起导出，并在清单中记录分片布局。已配置的分片组流缺失或无法读取，或存在已部署配置未声明的分片组流时，导出会失败，而不会生成不完整的备份。`dcctl restore --plan` 会列出每个分片组流，并拒绝分片布局与备份不同的目标。使用较早版本 dcctl 为分片实例创建的备份只包含基础流（第 0 组），不包含分片组流；升级 dcctl 后请重新创建备份。未配置分片组的实例不受影响，其清单也保持不变。

#### 发布针对其启动时的设备 {#v171-rollout-identity}

- `startUpdateRollout` 现在会记录分组中每个成员是哪一台设备，而不仅是其令牌。之后以相同令牌创建的设备是另一台设备：即使它在分组中，也会以 `DEVICE_REPLACED` 跳过，并且永远不会通知它更新。在发布启动期间离开分组的成员不属于该发布。
- 删除设备时，仍在等待该设备的发布目标会以 `DEVICE_NOT_FOUND` 跳过。
- v1.7.1 之前启动的发布：在发布开始五分钟之后创建的设备以 `DEVICE_REPLACED` 跳过；在发布开始五分钟之前创建的设备照常更新；在两者之间创建的设备会被暂缓，如[升级到 v1.7.1 之前](#v171-before-upgrade)所述。

### v1.7.0 — 删除会传达到每个服务，会话可安然度过中断 {#v170-upgrade}

`v1.7.0` 可从 `v1.6.0` 原地升级：先为集群运行 `dcctl install`，再为每个实例运行一次 `dcctl upgrade`，详见[滚动升级](#rolling-upgrades)。**两个命令都使用 v1.7.0 的 `dcctl`**，配合 OpenTofu 或 Terraform 1.10 及以上版本，并向 `dcctl install` 提供集群设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`，以及 `--version`。`device-management`、`ai-inference` 和 `update-management` 会在首次启动时执行新增的迁移，升级只能向前：无法回退到 `v1.6.0`。若从 `v1.5.x` 或更早版本升级，请先逐个升级经过每个中间的次版本（包括 `v1.6.x`），并按各版本的说明使用该版本自己的 `dcctl` 和 `--version`：跳过次版本不是经过测试的路径（参见[支持的升级路径](./versioning-policy.md#upgrade-paths)）。`v1.6.0` 的说明见 [`v1.6.0` 说明](#v160-upgrade)。

```bash
dcctl install local --version v1.7.0
dcctl upgrade local devicechain --version v1.7.0 --dry-run
dcctl upgrade local devicechain --version v1.7.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本新增受保障的接口，不移除也不弃用任何接口。地理围栏告警需要 `location:read` 以及 `MANAGED_BY_IDP` 拒绝属于策略允许的安全修复；其他新的拒绝和限制列在[升级到 v1.7.0 前](#v170-before-upgrade)。Helm 值和实例配置不在兼容性保障范围内。

要点：删除设备、客户、区域、资产或分组现在会可靠地发出通知，每个持有相关数据的服务都会据此处理：取消未送达的命令，清除实时状态和待处理的更新，告警结束并停止升级，并清除过时的仪表板共享。网络中断不再让任何人退出控制台或 `/dash` 的登录。使用 NATS 分片组的实例会暂存摄取，而不是将其作为失败消息丢弃。MCP、实时订阅、OTA 传输和 AI 推理新增按租户、设备或 Pod 的限制。

新增内容：

- [升级到 v1.7.0 前](#v170-before-upgrade)
- [v1.7.0 新增内容](#v170-whats-new)
- [删除通知可靠送达](#v170-deletion-announcements)
- [指向已删除客户或分组的共享会被清除](#v170-deleted-anchor-shares)
- [已删除设备的告警会结束并停止升级](#v170-deleted-device-alarms)
- [已删除设备的未送达命令会被取消](#v170-deleted-device-commands)
- [已删除设备的实时状态会被清除](#v170-deleted-device-state)
- [分配给已删除设备的更新会被终止](#v170-deleted-device-updates)
- [重新创建的实体会保留其事件锚点](#v170-deleted-entity-anchors)
- [分片组暂存工作，而不是将其作为失败消息丢弃](#v170-shard-groups)

#### 升级到 v1.7.0 前 {#v170-before-upgrade}

需要采取的操作，按执行顺序排列：

- **`dcctl install`、`dcctl bootstrap` 和 `dcctl preflight` 会检查 OpenTofu 或 Terraform 的版本。** 预检现在会拒绝低于 dcctl 内置 OpenTofu 配置所声明的 `required_version`（目前为 `>= 1.10`）的二进制文件、预发布版本，以及无法读取版本的二进制文件。此前预检只检查二进制文件是否存在，因此使用 OpenTofu 1.9 的安装会在已经更新 Operator 之后才在 `tofu init` 处失败。两者都已安装时仍优先使用 OpenTofu，检查的也是它。运行 v1.7.0 的 `dcctl install` 之前，请安装 1.10 或更高的稳定版 OpenTofu 或 Terraform。`--skip-preflight` 仍会跳过此检查。
- **升级说明：broker 会重启。** 此次升级会替换 broker 镜像，因此每个 broker pod 都会重启。在 `--mqtt-fail-closed` 关闭（默认）时，broker 在某个 stream 切换 leader 期间已确认的 QoS 1 消息可能丢失。如果设备不能丢失已确认的消息，请在升级前将其开启（`dcctl upgrade --mqtt-fail-closed`）（[fail-closed MQTT](./observability.md#mqtt-fail-closed)）。

**使用分片组的实例。**

- **使用分片组的实例：摄取会被暂存，而不是转入失败消息。**[分片组暂存工作，而不是将其作为失败消息丢弃](#v170-shard-groups)中的内容不会改变未设置 `infrastructure.nats.shardGroups` 的实例。对于已设置的实例，分组积压或放置表不可用时，摄取现在会被暂存并重试，而不是转入失败消息；被拒绝的 HTTP 或集成请求返回带 `Retry-After` 的 `503`；无法路由分片流的服务会拒绝该流。请在升级前阅读该小节。
- **使用分片组的实例：放置表不得指向大于等于 `count` 的分组。** 配置了 [NATS 分片组](#v160-nats-shard-groups)后，将虚拟分区分配到大于等于 `nats.shardGroups.count` 的分片组现在会被拒绝，user-management 的放置表镜像也会拒绝发布指向此类分组的表：每一轮这样的同步都会失败，并计入其 `placement_mirror` 同步指标。已经指向此类分组的表不会被部分采用：它属于不可用的放置表，因此在表符合要求之前，实例上所有租户的分片摄取都会被暂存，详见[分片组暂存工作，而不是将其作为失败消息丢弃](#v170-shard-groups)。升级前，请确认没有虚拟分区被分配到大于等于 `count` 的分组；如果有，请先提高 `count` 以覆盖它。未设置 `infrastructure.nats.shardGroups` 的实例不受影响。

**修正现在会被拒绝的内容。**

- **拒绝不生成任何字符的令牌掩码。** 若令牌掩码中唯一的占位符都显式指定宽度为 0（例如 `dev{numeric-0}`），所有实体都会得到相同的令牌，现在保存包含此类掩码的令牌掩码设置会被拒绝。已保存的此类掩码会保留原值，但在修正之前，该设置的下一次保存会失败。因此请在升级前将其改为正数宽度（`{numeric-4}`）、省略宽度（`{alphanumeric}`）、`{slug}` 或 `{uuid}`。
- **存在旧类型的值时，分面的值类型不能更改。** 如果同一租户、成员类型和键下的有效 SHARED 属性仍有标记为旧类型的非空值，更改分面的值类型将被拒绝。请先清除或迁移这些值，再更改声明。保持类型不变时，仍可编辑标签和值词汇表。无需数据库结构迁移或配置更改。检查会随着 device-management Pod 的替换而生效，滚动升级期间旧 Pod 仍会接受这些更改，因此请暂停分面类型更改，直到部署完成。
- **如果存在设备自行创建的关系，则拒绝将该关系类型标记为 tracked。** `updateEntityRelationshipType` 会拒绝将类型标记为 tracked，直到该类型中设备自行创建的关系被删除，因为这些关系会在标记时成为所有权关系；拒绝消息会列出它们。请先删除这些关系，再将类型标记为 tracked。

**滚动升级前检查权限和调用方。**

- **地理围栏告警需要 `location:read`。** 由检测地理围栏包含关系的规则触发的告警，现在只有同时拥有 `location:read` 的调用方才能读取，与 `detectionStream` 已略去该规则检测结果的做法一致：告警的触发和清除时间就是设备进入和离开围栏的时间。没有该权限时，告警查询、告警计数、`alarmDispositions` 和 `alarmStream` 订阅都会略去该告警，对其确认、清除或标注的响应与未知告警相同。其他告警不变。升级会对所有已有告警进行分类，依据是规则定义（草稿或任一已发布版本中）是否提及 `inFence`。分类由 `device-management` 首次启动时的迁移对所有告警执行，随后第二条语句会再次检查第一条判定为非地理围栏告警的每个告警，因此告警表很大时，该次启动可能明显变慢。如果告警的规则已无法找到（例如其设备配置文件已被删除），该告警也按地理围栏告警处理并被略去。滚动升级期间，由旧版本副本触发或更新的告警在完成分类前最多可能被略去一分钟；这也包括已被判定为非地理围栏告警的告警：从该副本更新它的那一刻起即被略去，因此该副本加入的地理围栏规则绝不会被显示。在滚动升级期间新旧 Pod 并存时，`v1.6` Pod 在被替换前仍会向所有调用方显示地理围栏告警。只读基线权限以及通过只读 OAuth 范围签发的令牌都不包含 `location:read`，因此监控或确认地理围栏告警的角色或令牌需要添加该权限。发送到已配置渠道的通知不受影响。
- **运维人员对 IdP 管理的成员资格的编辑会以 `MANAGED_BY_IDP` 被拒绝。** 当连接仍然存在且仍为该租户提供服务时，运维人员禁用、移除成员资格或更改其角色的操作现在会返回 `MANAGED_BY_IDP`；否则，下次 SSO 登录会撤销这些更改。要撤销访问权限，请禁用该身份、将此人从提供商的组中移除，或停止该连接为该租户提供服务。非托管成员资格，以及连接已删除或不再为该租户提供服务的托管成员资格，仍可编辑。
- **启用 IdP 管理的成员资格同样会以 `MANAGED_BY_IDP` 被拒绝。** 当成员资格的连接仍然存在且仍为该租户提供服务时，运维人员启用成员资格的操作现在会返回 `MANAGED_BY_IDP`，与禁用、移除或更改角色的操作一致。每次 SSO 登录都会根据此人的组设置该成员资格的启用状态，因此运维人员所做的启用会与提供商相矛盾，直到下次登录再次将其禁用。要为此人授予访问权限，请将其加入提供商映射到该租户中某个角色的组；该成员资格会在其下次登录时启用。启用非托管成员资格的行为不变；连接已删除或不再为该租户提供服务的托管成员资格仍然无法重新启用：请将其移除，然后添加新的成员资格。
- **动态组访问范围可能在首次启动时扩大。** 升级后首次启动时，`device-management` 会把本应已属于动态组的现有设备、资产、区域和客户加入该组，前提是该组已被检测规则或访问范围启用，且其选择器匹配没有属性的实体（例如否定条件 `!("retired" in attr)`）。此类访问范围的成员随后可能会看到以前无法看到的设备，限定到组的规则也会开始评估这些设备。请在升级前检查用作访问范围且使用此类选择器的动态组。
- **SSO、MFA 和加强认证限制将一个 IPv6 /64 视为同一来源。** 来自同一 /64 内 IPv6 地址的 SSO 发起、MFA 验证和加强认证请求现在共享同一个来源速率限制，防止通过在该地址块内轮换地址耗尽共享的全局配额。从同一 IPv6 /64（例如同一办公室或运营商网络）登录的用户现在共享该限制，可能会更早被拒绝。IPv4 限制、所有突发容量和令牌补充间隔均保持不变。

**会遇到新限制的客户端。**

- **已过期的命令不再被下发。** 此前，已超过到期时间但尚未被到期清理处理的命令仍可能被认领并发布到其设备：例如重新启用一台停用时间超过其命令有效期的设备时、通过唤醒（nudge）时，或在到期步骤失败的下发清理中。现在，下发认领会拒绝此类命令，并将其留给到期清理，由后者将其记录为已过期。恰好处于到期时间或没有到期时间的命令仍会被下发。对于已过期的命令，`markCommandSent` 返回 null，与其未赢得的任何认领相同。
- **按设备限制 HTTP 命令轮询。** 每个设备在每个副本上最多可保持两个等待中的命令轮询（一个，外加一个服务器尚未察觉其旧连接已关闭的重连）。更多的等待轮询会收到 HTTP 429 和 `Retry-After`，为其他设备保留容量。请让每个设备只保持一个等待轮询，并按照提示重试；`wait=0` 请求和租户上限保持不变。
- **设备密码猜测统一计数，无论通过哪种途径到达。** 在 HTTP 设备上行路由上出示的错误密码，现在与错误的 MQTT 密码计入同一个按凭据实施的暂停，所有副本共享计数，因此无法再将猜测分散到两种途径以获得双倍尝试次数。凭据暂停期间，使用它的 HTTP 上行请求会收到 `503` 和 `Retry-After`，使用它的 MQTT 连接则会被拒绝，与之前一样。
- **事件列表查询在 60 秒后停止。** `events`、`locationEvents`、`measurementEvents`、`alertEvents`、`stateChangeEvents` 和 `bucketedMeasurements` 读取若未在 60 秒内完成，现在会被取消并返回要求缩小时间范围的错误，不再一直占用数据库连接直到完成。作用域受限的调用者指定锚点的读取仍保留 15 秒的时限。
- **实时订阅按租户和 Pod 设有上限。** `measurementStream`、`alarmStream` 和 `detectionStream` 在一个服务 Pod 上对每个租户各自最多接受 5,000 个并发订阅，超出的订阅会以 `LIMIT_EXCEEDED` 被拒绝。请关闭不用的实时视图。该上限按 Pod 计算，因此增加副本即可提高整个实例的总量。
- **MCP 服务器每进程 64 个进行中请求的上限现在可以达到，超出的请求会收到 HTTP 429。** 共享请求体预算此前只能容纳 63 个最大尺寸的请求体，因此第 64 个并发请求会在达到 MCP 上限之前收到带 `Retry-After: 1` 的 HTTP 503。该预算现在可容纳 64 个（增加 64 字节），超出上限的请求会收到带 `Retry-After: 1` 的 HTTP 429，与文档一致。此前会重试 503 的客户端应以同样方式重试 429。每个调用者 4 个进行中请求的上限和 1 MiB 的请求体上限保持不变。
- **MCP 客户端必须单独发送 `initialize`。** 打开新 MCP 会话的请求（不带 `Mcp-Session-Id` 请求头的 POST）如果其正文是 JSON-RPC 批处理，现在会以 HTTP 400 和 JSON-RPC 错误 `-32600` 被拒绝。只有使用 2025-06-18 之前协议版本的客户端才能发送批处理；如果您的某个客户端将 `initialize` 与其他调用放在同一批处理中，请在升级前将其改为单独发送 `initialize`，并在其打开的会话上发送其他调用。现有会话上的批处理不受影响。
- **MCP 会话数量限制。** 当同一用户已有 16 个存续会话，或 MCP 进程已有 1,024 个存续会话时，新建会话将收到 HTTP 429 和 `Retry-After`。代理应复用会话，通过 DELETE 关闭闲置会话，并遵守 `Retry-After`。现有会话仍可使用；已删除的会话以及空闲 30 分钟后被清理的会话会释放容量。升级前请检查反复初始化的客户端；无需修改配置。
- **循环请求更新下载的设备现在会被限流。** `update-management` 对每台设备的 `download.request` 最多连续应答五次，此后每秒一次，就像它对 `sync` 已经最多每五秒应答一次。超出该预算的请求会被确认但不予应答，并计入 `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`，因此单台循环请求的设备不再拖慢其他租户的更新报告。设备在重启或下载失败后再次请求时仍会得到应答。
- **单个租户不能再占满一个副本的全部制品上传名额。** 每个租户最多占用副本并发制品上传数（`updates.maxConcurrentUploads`）的一半，向上取整且至少为一个。超出其租户份额的上传会在读取请求体之前被拒绝，返回与副本已满时相同的 `503` 和 `Retry-After`；待该租户的某个上传完成后再重试。副本的总上限不变。
- **AI 推理的速率限制在副本之间共享。** AI 推理现在将每个租户的速率和突发预算保存在其自身的服务数据库中，因此所有 ai-inference 副本共用同一份预算，重启后的副本也不会再获得新的突发额度。升级会追加一个创建准入状态表的迁移；配置和 API 均不变。共享限流器采用失败即拒绝的策略：如果无法读取或更新数据库，请求将被拒绝，而不会按本地额度放行。每个租户的上限仍从 user-management 获取，当 user-management 不可达时仍回退到平台默认值。滚动升级期间，旧 pod 在被替换之前仍使用各自进程内的预算，因此当所有 ai-inference pod 都运行 `v1.7.0` 后，共享上限才完全生效。
- **活动错误详情经过脱敏。** `Campaign.lastError` 保留可据以修正配置的消息；内部故障显示通用重试消息，完整详情记录在服务端日志中。消息仍限制为 500 字节，并在 UTF-8 字符边界截断。GraphQL 字段及其类型保持不变。

**升级后首次启动时执行的操作。**

- **升级后会清除此前删除遗留的共享。** 升级后首次启动 15 分钟后的第一次清理中，`dashboard-management` 会删除指向此前已删除的客户或访问分组的仪表板共享，包括升级前的删除。该清理需要配置服务密钥和 `device-management` 端点；未配置时清理不会启动，并在启动时记录警告。参见[指向已删除客户或分组的共享会被清除](#v170-deleted-anchor-shares)。
- **首次启动会取消已删除设备遗留的命令。** 升级后首次启动时，`command-delivery` 会读取消息代理仍保留的删除记录，并取消这些设备处于排队、保持或暂存状态的命令，包括升级前的删除。参见[已删除设备的未送达命令会被取消](#v170-deleted-device-commands)。
- **首次启动会清除已删除设备遗留的实时状态。** 升级后首次启动时，`device-state` 会清除已不存在的设备的实时状态、最新读数、最新位置和被丢弃读数的记录，包括升级前删除的设备，之后每 10 分钟重复一次该检查。升级前被删除后又以相同令牌重新创建的设备，与被删除的设备共用同一条实时状态记录，因此该记录也会被清除：新设备的最后已知状态和最新读数会保持为空，直到它再次上报。已存储的事件不受影响。参见[已删除设备的实时状态会被清除](#v170-deleted-device-state)。
- **首次启动会终止分配给已删除设备的更新。** 升级后首次启动时，`update-management` 会读取消息代理仍保留的删除记录，并终止这些设备的待处理更新分配，包括升级前的删除。相应的发布目标显示为已取消。参见[分配给已删除设备的更新会被终止](#v170-deleted-device-updates)。
- **测量聚合现在会随保留期过期。** 在 event-management 中设置 `lifecycle.retentionDays` 后，图表和分桶读取使用的每分钟测量聚合会在原始读数之后多保留一个分块间隔（向上取整到天），随后被删除；分桶读取也不再返回早于该窗口的聚合。升级后首次启动起，分桶读取即不再返回早于该窗口的聚合历史，对应时间范围的图表将不显示数据；随后由后台作业按整个存储分块删除这些历史。在从早期版本升级的实例上，聚合的每个存储分块覆盖 70 天，因此较早的聚合会立即被隐藏，但要等到其 70 天分块中最新的数据也早于该窗口时才会被删除。设置为 `retentionDays: 0`（默认值）的实例不受影响。滚动升级期间，仍运行 v1.6 的 Pod 会继续提供较早的聚合，直到被替换。
- **更改刷新令牌的有效期现在会作用于正在运行的实例。** 以前调高或调低 `auth.refreshTokenTtlSeconds` 只影响全新安装：现有的刷新令牌 bucket 仍按创建时的时长使条目过期。现在 `user-management` 在启动时会把该 bucket 调整为配置的有效期，代理管理员也会对其声明有效期的 bucket 做同样的调整。调低有效期会在该次重启时使早于新值的已存条目过期，因此由它们支撑的会话会结束，相关用户需要重新登录。

#### v1.7.0 新增内容 {#v170-whats-new}

**API 与 SDK。**

- **过期保存会携带 `extensions.code: STALE_WRITE`，`@devicechain/client` 导出 `isStaleWrite`。** 因记录在调用方读取之后已被修改而被拒绝的写入（例如 `expectedUpdatedAt` 这类乐观并发前置条件不再成立）现在在所有服务中都会返回 `extensions.code` `STALE_WRITE`；此前只有连接器更新带有代码，其他此类拒绝都没有代码。消息保持不变（"… was modified by another writer; reload and try again"），因此依据该消息识别的客户端仍可正常工作；它也始终不会是 `CONFLICT`，后者仍专用于值已被占用的情况。`@devicechain/client` 中的 `isStaleWrite(err)` 对该代码返回 true，对较旧的服务器回退为匹配该消息，对 `CONFLICT` 永远不会返回 true。控制台在所有带前置条件的保存中都使用它。
- **`updateCustomerType` 可以拒绝过期的保存。** 客户类型编辑现支持在 `updateCustomerType` 中传入可选的 `expectedUpdatedAt`。传入上次读取的 `updatedAt` 可拒绝过期保存；遇到 "modified by another writer" 时，请重新加载后重试。省略该参数将保留最后写入生效的行为。此保护在所有 device-management 副本完成升级后可用；其他注册表编辑操作保持不变。无需数据库迁移。
- **不计算总数的事件列表。** `EventSearchCriteria.includeTotal: false` 跳过匹配事件的精确计数；在大型历史记录中，计数占据了页面读取的大部分时间。此时 `totalRecords` 为 null，新增的 `SearchResultsPagination.hasMore` 表示是否还有下一页。使用默认设置时也会返回 `hasMore`。滚动升级期间，仍运行 v1.6 的 Pod 会将 `includeTotal` 以及对 `hasMore` 的选择都作为未知字段拒绝，因此请等所有 event-management Pod 均运行 v1.7 后再使用其中任何一个。
- **一个测量订阅可覆盖多个设备。** `measurementStream(deviceTokens:)` 可在一次订阅中接收最多 1000 个设备的读数。滚动升级期间，仍运行 v1.6 的 Pod 会拒绝此参数，因此客户端应等到所有 event-management Pod 都运行 v1.7 后再发送此参数，或在被拒绝时回退到每个设备单独订阅。
- **实时订阅按租户共享一个数据流。** 每个 Pod 现在只读取一次租户的实时数据流，并只向每个订阅分发属于它的内容：`measurementStream` 收到其设备的读数，`alarmStream` 收到其发起方的告警，`detectionStream` 收到其配置文件的检测结果，因此包含大量设备的仪表板不再让每个事件的处理工作成倍增加。订阅收到的内容保持不变。新的每 Pod 订阅上限见[升级到 v1.7.0 前](#v170-before-upgrade)。
- **列表总数现在会饱和到上限，而不再溢出回绕。** 当列表中匹配的行数超过 2,147,483,647 时，`totalRecords` 现在报告为 2,147,483,647，而不是回绕后可能为负数的值；页码非常大时，`pageStart`/`pageEnd` 也不再溢出。
- **`@devicechain/client` 可以从 HTTP 401 中恢复。** 注册 `setUnauthorizedHandler({ refresh, onEnd })` 后，访问令牌被服务器拒绝的请求会先进行一次强制刷新并重放一次，然后才结束会话；请将 `createSessionTokens` 新增的 `session.forceRefresh(token)` 用作 `refresh`。临时的刷新失败（网络错误、5xx 或 429）会保留会话并返回错误；被拒绝的刷新，或重放时再次收到 401，都会结束会话。未注册处理程序时，SDK 的行为与以前相同。
- **`@devicechain/client` 可以在自身刷新之外执行一个会消耗刷新令牌的操作。** `createSessionTokens` 现在返回 `session.exchange(spend)`：`spend` 接收当前持有的刷新令牌，并返回替换它的令牌对；它作为该令牌的唯一一次使用运行，排在任何正在进行的刷新之后，而在此期间请求的刷新会等待并采用它的令牌对，而不是再次消耗该令牌。控制台的二次验证（step-up）通过它执行，因此 HTTP 401 之后的强制刷新与二次验证不会再同时消耗同一个刷新令牌，导致用户在输入正确验证码后被登出。
- **网络中断不再导致任何人退出登录。** 无法连接服务器的刷新（无响应、5xx 或 429）现在会在刷新令牌有效期内保持会话，而不是在访问令牌过期时结束会话。在控制台和 `/dash` 中，访问令牌过期期间发出的请求会因中断而失败，服务器恢复后即可再次成功，`/dash` 墙面显示屏会自行持续重试。只有服务器的拒绝或刷新令牌过期才会结束会话。对于 `@devicechain/client` 的使用者：在此期间 `getAccessToken()` 仍像以前一样解析为 `null`，但不再结束会话（不会调用 `onEnd`），下一次调用会再次尝试；`startAutoRefresh()` 会持续重试，直到刷新令牌过期。若希望 `getAccessToken()` 改为以该暂时性错误拒绝，使请求因中断而失败而不是在没有令牌的情况下发出，请向 `createSessionTokens` 传入 `transientRefreshErrors: 'throw'`，控制台和 `/dash` 正是这样做的。
- **`createSessionTokens` 返回 `ManagedSessionTokens`。** 它扩展了 `SessionTokens`（其中 `forceRefresh` 现在是可选的），并保持 `forceRefresh` 为必需，因此使用其返回值的代码不受影响。如果您将自己构建的对象声明为其返回类型，例如声明为 `ReturnType<typeof createSessionTokens>` 的测试 mock，请改用 `SessionTokens` 作为其类型：这是为您自行构建的对象设计的接口，否则以后 `ManagedSessionTokens` 新增成员会导致该 mock 无法编译。
- **实时订阅可以挺过令牌刷新中断。** 此前，当订阅的套接字在访问令牌过期后重新连接，而令牌刷新没有响应、返回 5xx 或 429 时，该区域的所有实时订阅都会以错误结束，仪表板会一直显示数据不可用，直到重新加载。现在，当令牌获取函数以该暂时性错误拒绝时（例如在 `transientRefreshErrors: 'throw'` 下的 `getAccessToken()`，控制台和 `/dash` 均使用此模式），`@devicechain/client` 会以与网络断开相同的退避策略重试该重新连接，并在获得令牌后将其报告为重新连接。被拒绝的刷新仍会结束会话。

**并发写入。**

- **AI 提供商的更新和删除作用于其检查过的提供商。** 未带 `expectedUpdatedAt` 前置条件的 `updateAiProvider`，如果提供商在读取与写入之间已被删除，现在会返回未找到，而不会重新创建它；它也不再撤销并发的重命名。删除提供商时，删除的是已检查过授权的那一行，即使其间发生了重命名并以旧令牌创建了新的提供商；删除操作的审计日志条目会记录该提供商的 id 和标签。
- **并发的资产层级写入不再会让一个资产拥有两个父资产或形成循环。** 所有创建包含关系边的方式（`setAssetParent`、单个或批量创建关系以及认领兑换）现在都会在检查层级之前获取按租户划分的锁，因此并发写入会依次接受检查。与其他写入竞争、并会让资产拥有第二个父资产的关系创建现在会被拒绝，这与两者不重叠时的行为一致；`setAssetParent` 仍会替换当前父资产。
- **设备认领保留并发兑换的结果。** 取消刚刚兑换的认领会返回 `false`，并保持已认领状态。重新开放认领会先撤销当前所有者的关系，再允许再次认领；若重新开放期间记录发生变化，操作将被拒绝，可重试。
- **制品上传租约归属。** 已过期的上传无法再释放或完成后续上传持有的租约，确保多个副本之间每个制品同时只有一个上传。运维人员无需采取任何操作。

**检测与活动。**

- **检测引擎根据快照开销安排快照间隔。** 达到 `checkpointEvents` 个事件后不再立即生成快照：引擎还会等待上一次快照开销的四倍时间，因此检测状态较大时，引擎降低快照频率，但间隔最长不超过 `checkpointIntervalSeconds`，且缓冲的确认达到消费者上限的一半时始终触发检查点。崩溃后可能重放超过 `checkpointEvents` 个事件。无需更改设置。
- **检测检查点携带下降沿记录。** 检测引擎现在会在检查点中记录每条规则近期的下降沿（保留到其已记录位置之后十分钟），并在检查点带有触发的输入时间时读取该时间。每次触发和解除的判定与 `v1.6` 完全相同，滚动升级期间 `v1.6` 的 event-processing Pod 仍可恢复此版本的检查点。后续版本将依赖这些字段，因此请经由 `v1.7` 升级，不要跳过它：跳过次版本不是经过测试的路径（[支持的升级路径](./versioning-policy.md#upgrade-paths)）。
- **重启时会在重放之前加载近期的围栏集版本。** event-processing 启动时现在会在重放事件之前，从 device-management 加载当前围栏集版本及最多三个先前版本，因此近期历史事件可以使用其版本戳对应的几何数据，无需等待围栏事实消息或预览。更早的版本和归档读取失败的版本仍保持不可解析，并产生计数的包含关系求值错误；此功能不会预加载全部历史。启动协调会重试缺失版本，并跳过已加载的版本。在滚动升级期间新旧 Pod 并存时，旧 Pod 在被替换之前启动时仍只加载当前版本。
- **围栏集快照会记录每个围栏的身份。** 新的围栏集快照包含可选的 `fenceId`，与 `GeoFence.id` 一致，因此即使几何形状不变，也能区分删除后使用相同令牌重新创建的围栏。GraphQL 快照条目将其公开为可为空的 `fenceId: ID`。旧快照（包括滚动升级期间由旧 Pod 创建的快照）返回 null；服务不会根据当前令牌推断历史身份。历史记录不会被改写。新 Pod 处理下一次围栏修改时，会在新版本中记录身份，即使只修改了展示字段。无需迁移或操作员干预。
- **活动在后续步骤前重新检查组成员。** 对于后续步骤和各设备的太阳事件时刻，如果预定时间比上次成员检查晚超过一分钟，活动会重新检查组成员。已离组的设备只跳过其已到期（或在接下来一分钟内到期）的步骤；其后续步骤保持待处理状态，并在到期时再次检查成员资格，因此在此期间重新加入该组的设备会收到这些步骤。无法解析组时会阻止发送，规划后加入组的设备仍从下一个规划周期开始参与。运维人员无需采取任何操作。
- **活动的 cron 步长。** 远大于字段范围的步长（例如 `40-59/9223372036854775803`）不再溢出到其指定范围之外的分钟。它只选中范围的第一个值，与此前任何大于范围的步长一致。此前接受的所有表达式仍然被接受。
- **新实体在创建时加入匹配的动态组。** 动态组被检测规则或访问范围启用后，如果组的选择器匹配没有属性的实体（例如否定条件 `!("retired" in attr)`），此后创建的设备、资产、区域或客户现在会在创建时加入该组。此类访问范围的成员现在可以看到组启用后创建的设备，限定到组的规则也会评估这些设备。升级前已启用的组会在 `device-management` 首次启动后一分钟内得到更新：其选择器匹配的所有实体（包括升级前创建的实体）都会加入，此后新实体在创建时加入。在滚动升级期间新旧 Pod 并存时，由 v1.6 Pod 启用的组同样如此。在此期间通过 v1.6 Pod 创建的实体不会在创建时加入。

**设备、摄取与连接器。**

- **发往不存在租户且无法解码的 HTTP 请求体不再记录为解码失败。** 解码失败记录由整个实例共享，因此编造租户名称的发送方可能会把真实租户的记录挤出去。现在此类请求体改为计入 `total_http_ingest_refused` 的 `unknown_tenant` 原因，发送方收到的应答与其他任何格式错误的请求体相同。
- **LwM2M 注册有效期。** 当 Register 或 Update 携带极大的 `lt`（超过约 292 年）时，现在会获得配置的最大有效期。此前该值会溢出，有效期降到一分钟的下限，设备在一分钟后即被标记为已断开。
- **超出出站速率的租户不再淹没死信记录。** `outbound-connectors` 现在会对因速率被丢弃以及终止性无法投递的调度所写入的死信设定预算，按租户和全部租户分别限制，与 REACT 对其自身闸门丢弃的动作所做的一样。超出该预算的调度仍会被确认，并以原有结果计入 `devicechain_outboundconnectors_connector_dispatch_total`，因此 `ConnectorDispatchRateLimited` 告警读取的内容不变；它还会计入新的 `devicechain_outboundconnectors_connector_dispatch_unlettered_total`，每个租户每分钟获得一条汇总死信，取代逐条死信。在最后一次投递尝试后进入死信的调度仍会像以前一样逐条记录。

**运维、指标与 dcctl。**

- **跨服务查询、服务令牌和登录指标。** `devicechain_svcclient_queries_total{result}`（`ok` 或 `error`）和 `devicechain_svcclient_query_duration_seconds` 统计通过调用者范围检查后的跨服务调用，包括本地校验和获取令牌的时间。服务令牌签发按结果计入 `devicechain_svcclient_service_token_mints_total{result}`（`ok` 或 `error`，包括 HTTP、传输和无效响应错误），并由 `devicechain_svcclient_service_token_mint_duration_seconds` 计时；两者都不统计缓存命中或等待同一次签发的调用方，计数也不包括签发前的拒绝。`devicechain_userclient_logins_total{result}`（`ok`、`error` 或 `mfa_challenge`）统计 login 交换，不包括 MFA 验证和租户选择；`devicechain_userclient_refreshes_total{result}`（`ok` 或 `error`）统计回退到 login 之前的刷新交换。所有序列均从零开始，不含租户、身份、凭据、查询、端点或 URL 标签。无需更改配置或数据库；滚动升级期间仅已升级的 Pod 提供这些指标。
- **旧版文本密封的使用会被计数。** 新增两个进程计数器 `devicechain_credential_legacy_text_seal_reads_total` 和 `devicechain_credential_legacy_text_seal_writes_total`，统计事件消息中设备凭据旧版文本密封形式的使用情况。写入计数统计 `infrastructure.messaging.wireFormat` 为 `legacy` 时编码的文本密封；读取计数统计从消息中读到的每次文本密封转换，包括格式错误、空值和重新投递。纯二进制流量不会增加这两个计数器。两者均从零开始且不带标签，因此不暴露凭据或租户数据。旧版文本形式的读取和写入与之前完全相同，没有移除任何内容。无需修改配置或迁移数据；滚动升级期间仅更新后的 Pod 提供这些计数器。
- **服务令牌权限上限不再误报六个未超限的调用方。** `devicechain_usermanagement_service_token_checks_total` 曾把 `command-delivery`、`event-sources`、`outbound-connectors` 和 `event-management` 的签发计为 `over_ceiling`，并把 `device-state` 和 `user-management` 自身的签发以 `caller="other"` 计为 `unknown_caller`，而它们请求的都只是自己使用的权限。上限表现在与每个签发令牌的服务一致，因此这些序列保持为零。该检查仍然只做观测：不会拒绝任何签发。
- **内置面板按优先级类别细分摄取丢弃。** Platform 面板现在除了按来源显示总数外，还按优先级类别（`shed_class`：`best-effort`、`bronze`、`silver` 或 `gold`）显示在租户摄取上限处被拒绝的消息和读数；Ingest 面板新增了在读数上限处被丢弃消息的按类别序列。低优先级流量会先被丢弃，因此该细分可以显示被丢弃流量的重要程度。
- **`JetStreamClusterUnused` 不再在分片组实例上触发。** 启用 [NATS 分片组](#v160-nats-shard-groups)的实例有意运行多服务器代理，且每个流只有一个副本，此前该警告会在这类实例上持续触发。现在只要有任何服务报告分片组流，该告警就保持静默。没有分片组的集群化单副本代理仍会触发该告警。告警名称保持不变。
- **JetStream 预留检查按单个代理服务器评估。** 启用[分片组](#v160-nats-shard-groups)后，`dcctl` 现在计算单个服务器可能需要承载的量：所有未分片的流，加上每个分片流中可能共用同一服务器的各组各计一次。chart 将每个组固定到各自的 `dc-nats` 服务器，因此三组安装不再把三份副本累加后与单个服务器的卷比较；启用三组的 `perf-20k` 安装可以放入该预设的 40Gi 卷。基础设施应用之前的检查现在也会使用您在 `terraform.tfvars`、`*.auto.tfvars` 文件或 `TF_VAR_nats_jetstream_storage` 中声明的 `nats_jetstream_storage`，而不是预设的大小。当大小为字面值时，对单个服务器的份额而言过小的卷，仍会在创建任何内容之前被拒绝。以表达式或引用给出的大小在应用之前无法读取，因此 `dcctl` 会打印一条提示，执行基础设施应用，然后在安装 chart 之前检查实际配置的卷。
- **更清晰的 operator 清单错误。** 当 Kubernetes operator 无法解码其应用的清单中的某个部分时，错误会按从 1 开始的位置指出该部分，并附带解码器自身的消息。此前原因会丢失，错误以 `<nil>` 结尾。
- **`dcctl ha verify --probe-mqtt` 重新可以通过认证。** 自 `v1.1.0` 起，在按功能区划分的代理登录凭据中，代理只允许 event-sources 的登录凭据通过 MQTT 连接，而探测使用的是代理管理员的登录凭据，因此在每个使用按功能区划分的代理登录凭据的实例上都会被拒绝。现在探测使用 event-sources 的登录凭据，代理本来就允许它通过 MQTT 连接；代理权限没有任何变化。探测被拒绝时，错误信息会注明所用的登录凭据。通常无需使用该选项：event-sources 自身的连接就会创建代理的 MQTT 流。
- **被拒绝的 `dcctl bootstrap` 会保持本地实例记录不变。** 当 bootstrap 拒绝在已运行的实例上执行时，`~/.devicechain/instances/<instance>/instance.json` 会逐字节恢复，包括其 `createdAt`。此前，被拒绝的运行会用自己的创建时间和 dcctl 版本重写该文件。
- **`dcctl` 在覆盖你声明的代理拓扑时会给出提示。** `dcctl` 总是自己设置 `ha` 和 `nats_cluster_replicas`，使代理的服务器数量与其流副本数保持一致。你在 `TF_VAR_*` 或 tfvars 文件中为其中任何一个声明的值都不会被使用。现在，当你声明了其中一个时，`dcctl bootstrap` 和 `dcctl upgrade` 会打印一条说明，[哪个值优先](./sizing.md#precedence)也记录了这一例外。
- **通过 dcctl 签发集成令牌。** 创建 LoRaWAN 或 webhook 集成后，运行 `dcctl product rotate-integration-token <integration-token>`。从标准输出捕获新令牌；它仅显示一次。轮换后，每个 event-sources 副本最多仍可在 60 秒内接受旧令牌；在无法连接 device-management 期间，最长可达 15 分钟。device-management 仍会检查每个事件，因此这段时间不会额外授权任何内容。

**控制台。**

- **控制台的 MQTT 命令使用 8883 端口。** 设备页面的 MQTT 连接命令现在连接 8883 端口，即代理在集群外发布的端口，而不是 1883；1883 是代理在集群内的端口，从不对外发布。在本地 kind 集群上，请继续使用 `localhost` 和 1883 端口。
- **电子邮件中含有 `%` 的身份可以在控制台中打开。** 身份页面曾对其地址解码两次，因此这类身份无法打开，或会打开另一个身份。
- **由 IdP 管理的成员资格会说明应如何处理。** 当服务器因身份提供商管理某个成员资格而拒绝更改其角色、启用、禁用或移除它时，身份页面将不再为其提供这些编辑操作，并列出解决办法：在提供商处更改此人的组（不在任何映射到该租户角色的组中的用户会在下次登录时被禁用，加入后会再次启用）、禁用该身份，或停止该连接为该租户提供服务。
- **控制台详情页可以打开包含 `%` 的令牌。** 设备、租户、等级、角色、AI 提供方、身份提供方、集成、预配配置文件、信任锚以及注册表的详情页会对地址中的令牌再解码一次。包含 `%` 的令牌会打开另一个令牌，或者页面无法加载。现在每个页面只读取一次令牌。
- **控制台按自身语言格式化日期和数字。** 时间戳和计数此前遵循浏览器的区域设置，因此设置为西班牙语的控制台可能显示英文日期。现在它们遵循控制台所设置的语言。
- **控制台更多界面提供简体中文。** 将控制台设置为简体中文后，设备相关界面和身份目录现在也已翻译；仪表板此前已经翻译。其他界面仍回退为英文。

**模拟器。**

- **港口模拟场景。** `dcctl sim create --manifest harborpulse` 在一个压缩为 24 分钟的一天中运行一个虚构的港区（潮位计、雨水泵、路灯、电动汽车充电桩、两栋计量建筑和一辆抢险车，共 33 台设备），其间有风暴、天文大潮、馈线停电以及存储转发的读数突发。它编写了七种检测规则、每个潮位计一个会逐级升级严重程度的洪水告警，以及两条其命令会先被模拟设备执行再应答的规则。
- **港口场景可以自我检查。** `harborpulse` 模拟现在会发布一个运维看板和一个供 Quayside Lofts 业主使用的只读看板，在其 `/status` 中报告压缩的一天进行到何处，并接受 `--day-length` 以延长这一天。新的 `scenario-verify` 工具会观察正在运行的 `harborpulse` 模拟中的一天，并从平台读回数据，以确认每个洪水告警都会升级并清除、水泵和充电桩的命令得到应答并改变读数、气堵的水泵和馈线停电恰好在正确的设备上被检测到、码头关联规则会触发，以及缓存的读数按其原始时间存储。全部成立时退出码为 0，有失败时为 1，无法判定时为 2。
- **模拟器使用独立控制端口。** `dcctl sim create` 现在会为新模拟器从 8090–8189 中选择其他已保存的本地模拟器尚未使用的最低端口，让多个模拟器能够同时运行。重新创建已有的模拟器时会保留其原有地址，除非另一个已保存的模拟器现在正在使用该端口，因此 `dcctl sim status` 仍能连上正在运行的模拟器。命令会显示所选地址；如需指定地址，请传入 `--control-addr`。

#### 删除通知可靠送达 {#v170-deletion-announcements}

- 删除设备、客户、区域、资产或分组时，发给其他服务的通知现在与删除操作在同一个数据库事务中记录，`device-management` 会持续发布该通知，直到消息代理接受为止。此前，因代理中断或在不恰当的时刻重启而丢失的通知永远不会再发送，已删除实体的仪表板共享、事件锚点和检测状态因此可能残留。
- 无论通知多晚发布，都携带删除操作发生的时间。
- 在滚动升级期间新旧 Pod 并存时，由 `v1.6` Pod 处理的删除仍按旧方式通知。
- 无需更改配置。

#### 指向已删除客户或分组的共享会被清除 {#v170-deleted-anchor-shares}

- 删除客户或访问分组时，`dashboard-management` 会删除指向它的仪表板共享。如果该清理持续失败，相应的通知现在会保留在死信流中，而不会被丢弃。
- `dashboard-management` 还会每 15 分钟使用实例的服务凭据检查每个共享的客户或分组是否仍然存在，并清除此前（包括升级之前）的删除遗留的共享。查询失败时不会删除任何内容。如果未配置服务密钥或 `device-management` 端点，则不会启动该检查，并在启动时记录警告。

#### 已删除设备的告警会结束并停止升级 {#v170-deleted-device-alarms}

- 删除设备或其他实体时，其每个活动告警现在都会以携带新的 `clearReason: ORIGINATOR_DELETED` 的 `CLEARED` 事件结束（`alarmStream` 订阅的 `AlarmEvent` 上也新增了可为空的 `clearReason` 字段）。升级随即停止，启用了 `notifyOnClear` 的策略会说明告警因其设备被删除而结束。
- `notification-management` 在再次发送通知前还会使用实例的服务凭据向 `device-management` 查询，并结束已不存在的告警的未结束通知，包括升级前的删除操作遗留的通知。如果查询失败，仍会发送通知；失败次数计入 `escalation_lookup_errors_total`。请参阅[已删除设备的告警](../guides/notification-channels.md#deleted-originator)。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` 的 `notification-management` Pod 会将此类清除视为普通清除，由 `v1.6` 的 `device-management` Pod 处理的删除不会发出清除事件；每次再次通知前的检查会停止其升级，定期检查会在这些通知满一小时后将其结束。

#### 已删除设备的未送达命令会被取消 {#v170-deleted-device-commands}

- 删除设备时，`command-delivery` 会取消仍为该设备保留的命令（排队、保持或暂存状态），并拒绝发送删除前创建的任何命令，包括已交给传输通道但尚未确认的命令。之后以相同令牌创建的设备不会收到这些命令；删除后为新设备创建的命令照常发送。
- 升级后首次启动时，`command-delivery` 还会读取消息代理仍保留的删除记录，因此删除记录仍被保留的任何设备所遗留的命令也会以同样方式取消。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` Pod 在被替换前仍可能发送此类命令。

#### 已删除设备的实时状态会被清除 {#v170-deleted-device-state}

- 删除设备时，`device-state` 会清除其实时状态、最新读数、最新位置和被丢弃读数的记录。之后以相同令牌创建的设备不会继承这些数据，因此其新所有者的成员看不到旧设备的最后位置或读数。
- `device-state` 现在还会每 10 分钟使用实例的服务凭据向 `device-management` 查询哪些设备仍然存在，并清除此前（包括升级之前）删除的设备遗留的实时状态。查询失败时不会清除任何数据。如果未配置服务密钥或 `device-management` 与 `user-management` 端点，则跳过该检查，并在启动时记录警告。
- 平台在删除前接受但在删除后才处理的读数，仍可能短暂地重新生成已删除设备的实时状态；如果没有设备持有该令牌，定期检查会将其清除。

#### 分配给已删除设备的更新会被终止 {#v170-deleted-device-updates}

- 删除设备时，`update-management` 会终止其待处理的更新分配，相应的发布目标显示为已取消。之后以相同令牌创建的设备不会收到这些分配，也无法下载其制品，即使使用不同的设备配置文件。
- 升级前创建的分配按时间匹配：早于当前持有该令牌的设备的分配不会提供给该设备。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` Pod 在被替换前仍可能提供此类分配。

#### 重新创建的实体会保留其事件锚点 {#v170-deleted-entity-anchors}

- 删除设备、客户、区域、资产或分组时，`event-management` 会删除在删除之前收到的事件的锚点。此前它按设备报告的时间判断，因此以相同令牌重新创建的设备若上传了日期早于删除时间的缓存读数，这些读数的锚点可能被删除，从而不再显示在其客户、区域或资产下。现在按平台收到每个事件的时间判断。

#### 分片组暂存工作，而不是将其作为失败消息丢弃 {#v170-shard-groups}

- **任一分组出现积压时，所有分组的 MQTT 转发都会暂停。**只要任一分组的 `inbound-events` 流在拒绝写入，捕获读取器就停止拉取，积压的消息留在捕获流中，不会被投递。此前，零号分组以外的积压分组中的事件会被重试，五次投递后被转入 `failed-decode`。MQTT 捕获不提供按分组隔离。
- **HTTP 摄取按租户准入。**启用分片组后，HTTP 或集成请求只会在其所属租户的分组拒绝写入时被拒绝；拒绝（包括放置表不可用导致的拒绝）返回带 `Retry-After` 头的 `503`。其他发布错误仍返回普通的 `503`。
- **放置表缺失或不可用时，摄取会被暂存。**当放置表不存在、无法采用，或指向大于等于 `nats.shardGroups.count` 的分组时，分片摄取会被暂存并重试，而不是转入失败记录。每个受影响服务中的新指标 `jetstream_shard_placement_unavailable` 为 1，两分钟后触发 `JetStreamShardPlacementUnavailable` 告警。恢复兼容的放置表即可继续。
- **所有写入方都遵循放置表。**device-state、sparkplug-ingest 和 lwm2m-ingest 中的 `inbound-events` 写入方现在会把每个租户发布到其所属分组。此前它们把所有租户都发布到零号分组，绕过了放置表和上述背压机制。
- **无法路由分片流的服务会拒绝该流。**未启用分片路由的服务会以明确的错误拒绝读取或写入分片流，而不是静默地只使用零号分组。在部署了 device-state 或 event-processing 时，`dcctl` 和 chart 仍会拒绝对 `resolved-events` 分片，因此运行这些服务的实例请只对 `inbound-events` 分片。
- **对 `resolved-events` 分片的基础功能已随版本提供，但尚未启用。**device-state 可以读取分片后 `resolved-events` 的所有分组；代理客户端可以重放单个分组或租户自身的分组；event-processing 的规则预览和版本比较会读取租户自身的分组。按分组的检测工作器已经构建，但该服务尚未启动它们。只要上述拒绝仍然存在，这些功能都不会运行。

### v1.6.0 — 更严格的权限检查，代理重载时设备保持连接 {#v160-upgrade}

`v1.6.0` 可从 `v1.5.0` 原地升级：先为集群运行 `dcctl install`，再为每个实例运行一次 `dcctl upgrade`，详见[滚动升级](#rolling-upgrades)。**两个命令都使用 v1.6.0 的 `dcctl`**，并向 `dcctl install` 提供集群设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`，以及 `--version`。`device-management` 新增一个迁移，使当前实体属性键保持唯一并排队执行修复。参见[v1.6.0 的架构变更与回退](#v160-going-back)。若从 `v1.4.x` 或更早版本升级，请先逐个升级经过每个中间的次版本（包括 `v1.5.x`），并按各版本的说明使用该版本自己的 `dcctl` 和 `--version`：跳过次版本不是经过测试的路径（参见[支持的升级路径](./versioning-policy.md#upgrade-paths)）。`v1.5.0` 的说明见 [`v1.5.0` 说明](#v150-upgrade)。

```bash
dcctl install local --version v1.6.0
dcctl upgrade local devicechain --version v1.6.0 --dry-run
dcctl upgrade local devicechain --version v1.6.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本新增受保障的接口，不移除任何接口，并弃用旧版 npm 导出，这些导出仍可正常使用（[详情](#v160-npm-deprecations)）。拒绝设备创建特定关系及更严格的权限检查属于策略允许的安全修复。代理操作、规则求值以及新增的备份和恢复规划命令属于 [Preview](./versioning-policy.md#preview-interfaces)。Helm 值和实例配置不在兼容性保障范围内；滚动升级完成前应保持分片组配置缺省。

新增内容：

- [升级到 v1.6.0 前](#v160-before-upgrade)
- [新增值和命令](#v160-values-commands)
- [完整重载代理会保持认证调用已放行的设备连接](#v160-broker-reload)
- [地理围栏规则的实时检测需要位置访问权限](#v160-detection-stream-location)
- [太阳活动需要安装位置访问权限](#v160-solar-campaign-position)
- [自动化动作的权限](#v160-action-authorities)
- [发布配置文件时应用层级的规则上限](#v160-rule-ceiling)
- [删除定义后的草稿作者](#v160-review-deleters)
- [委派管理员不能授予或操作超出自身权限的内容](#v160-no-escalation)
- [安全：设备不能再将自身分配给客户、区域或资产](#v160-device-relationship-refused)
- [存储新建关系的事件](#v160-new-relationship-event)
- [HTTP 设备上行路由限制凭据猜测](#v160-device-uplink-guessing)
- [对 HTTP 凭据的更严格检查](#v160-http-credentials)
- [更改已存储凭据的目标时必须重新提供凭据](#v160-credential-resupply)
- [签名密钥轮换同步到所有副本](#v160-signing-key-follow)
- [ingress-nginx 不再接受配置片段](#v160-nginx-snippets-off)
- [指标和健康检查端点移至 ops 端口](#v160-ops-port)
- [`dcctl destroy` 拒绝将路径作为实例名称，并且不再信任猜测的集群](#v160-destroy-instance-name)
- [MCP 内存限制](#v160-mcp-memory)
- [MCP 发现会公布服务器执行的范围](#v160-mcp-discovery)
- [API 验证与管理](#v160-api-checks)
- [命令保留](#v160-command-holds)
- [活动规划在租户之间让出执行机会](#v160-campaign-planning)
- [下载、发送和通知在租户之间公平分配](#v160-tenant-fairness)
- [升级通知计时](#v160-escalation-clock)
- [Sparkplug 跟踪容量上限](#v160-sparkplug-limits)
- [实体属性的键保持唯一](#v160-entity-attributes)
- [可选的 NATS 分片组](#v160-nats-shard-groups)
- [流和对象备份（Preview）](#v160-backup)
- [控制台和仪表盘编辑](#v160-console-dashboards)
- [控制台标签页不再沿用其他标签页的登录会话](#v160-console-tab-session)
- [编写工具与 SDK](#v160-authoring-tools)
- [旧版 npm 导出已弃用](#v160-npm-deprecations)
- [重新投递的解码失败使用记录预算](#v160-decoder-redelivery-budget)
- [无效的摄取失败记录会标明设备](#v160-invalid-failures-name-the-device)
- [代理操作的指标和告警（Preview）](#v160-agent-metrics)
- [v1.6.0 的其他变更](#v160-other-changes)
- [v1.6.0 的架构变更与回退](#v160-going-back)

#### 升级到 v1.6.0 前 {#v160-before-upgrade}

需要采取的操作，按执行顺序排列：

- **升级说明：工具。** 在运行 `dcctl` 的机器上安装 OpenTofu 1.10 或更新版本（也可使用 Terraform 1.10 或更新版本）；预检只检查是否存在，不检查版本。使用 v1.6.0 工具按集群设置执行安装，再升级每个实例。不支持降级。
- **升级说明：broker 会重启。** 此次升级会替换 broker 镜像，因此每个 broker pod 都会重启。在 `--mqtt-fail-closed` 关闭（默认）时，broker 在某个 stream 切换 leader 期间已确认的 QoS 1 消息可能丢失。如果设备不能丢失已确认的消息，请在升级前将其开启（`dcctl upgrade --mqtt-fail-closed`）（[fail-closed MQTT](./observability.md#mqtt-fail-closed)）。
- **升级说明：`dcctl install` 预检。** 在保留 `ingress-nginx` 控制器的集群上，只要该类的任何 Ingress 带有 `*-snippet` 注解，安装就会拒绝继续。请先升级所属实例或移除该注解（[详情](#v160-nginx-snippets-off)）。

**重启前修正配置。** 检查手工编辑的实例配置；无效的服务设置会阻止启动。以下服务键位于 `functionalAreas.<area>.config` 下。

- **启用轮换时，签名密钥保留期必须覆盖刷新令牌的有效期。** 在 `user-management` 中，当 `auth.signingKeyMaxAgeDays > 0` 时，应用默认值后的 `auth.signingKeyRetentionDays × 86400` 必须大于或等于 `auth.refreshTokenTtlSeconds`；允许相等。此前轮换会静默延长过短的保留期。保留期为 `0` 时取 8 天，默认刷新 TTL 为 604800 秒（7 天）。升级前请增加保留期或缩短刷新 TTL。关闭轮换时不执行此比较。chart 不比较这两个字段，且 `dcctl` 已拒绝覆盖此 auth 块。
- **拒绝令牌 TTL 溢出。** `user-management` 现在会在转换为时长前拒绝超过 **9223372036 秒**的 `auth.accessTokenTtlSeconds` 或 `auth.refreshTokenTtlSeconds`。`0` 仍取默认值（分别为 900 和 604800 秒）；负 TTL 原本就无效。Helm schema 也执行该上限检查。
- **拒绝租户清除时长溢出。** 在 `user-management` 中，`tenantPurge.intervalSeconds`、`tenantPurge.settleSeconds` 和 `tenantPurge.tokenHoldSeconds` 均不得超过 **9223372036 秒**。原有规则继续适用：负间隔会禁用协调器；负 settle/hold 值被拒绝，settle 和 token-hold 的安全下限保持不变。
- **拒绝死信时长溢出。** 在 `user-management` 中，`deadLetters.retentionDays` 不得超过 **106751 天**，`deadLetters.sweepSeconds` 不得超过 **9223372036 秒**。原有的默认值、禁用和最短保留期规则保持不变。
- **拒绝 Sparkplug 存在探测时长溢出。** 在 `sparkplug-ingest` 中，`presence.probeSeconds` 和 `presence.probeWindowSeconds` 均不得超过 **9223372036 秒**。`0` 仍分别取 1800 和 10 秒。原有下限保持不变：显式设置的间隔至少为 60 秒；显式设置的窗口至少为 5 秒且严格小于间隔。

**滚动升级前检查权限和调用方。**

- **升级说明：** 为地理围栏 `detectionStream` 使用方授予 `location:read`；仅有只读 OAuth 作用域并不包含该权限。为管理 `SOLAR` 活动的角色授予 `location:installed:read` 或 `location:read`。
- **升级说明：** 自动化编写者除 `device:write` 外，使用 `sendCommand` 还需 `command:write`，使用 `httpCall` 或 `publish` 还需 `connector:write`，发布和回滚时也会检查。检查委派管理员角色：超出其当前权限的操作现在返回 `ESCALATION`；请使用超级用户执行这些操作。
- **升级说明：** 将设备发送的带跟踪类型或 `contains` 的 `NewRelationship` 分配改为认领码或 `createEntityRelationship`。检查现有跟踪关系；升级不会移除这些关系。滚动期间旧 Pod 仍会接受这些事件。
- **升级说明：** 缩小响应超过 256 KiB 或聚合超过 1,000 个时间桶的 MCP 查询，并处理带 `Retry-After` 的 HTTP 429。将 `evaluateRules` 草稿图限制在 256 条检测规则以内；超限图会在编译前被拒绝。发布前重新运行已过期的自动化审查。
- **升级说明：凭据与发送方。** 更改持有已存储凭据的连接器、通知渠道或 AI 提供方的类型或目标时，客户端必须在同一请求中发送 `secret`（[详情](#v160-credential-resupply)）。`httpCall` 指定 `connector/` 密钥句柄的规则在发布时会被拒绝，已发布的此类规则会进入死信队列（[详情](#v160-http-credentials)）。HTTP 上行发送方收到带 `Retry-After` 的 `429` 或 `503` 时必须退避（[详情](#v160-device-uplink-guessing)）。

**滚动期间与完成后。**

- **在整个 v1.5 → v1.6 滚动更新期间保持 `infrastructure.nats.shardGroups` 缺省。** 如果 `v1.5` Pod 在滚动更新中加载新配置，会将这个新键作为未知字段拒绝。所有 Pod 升级完成后再启用。配置该块时，`count` 必须为 2–16；后缀必须唯一且仅限 `inbound-events` 和 `resolved-events`；`placementTags` 的键必须为 `0..count-1` 范围内的组编号，包括现有的第零组，标签字符串不得为空或仅含空白。`dcctl` 为每个组生成 `dc-nats-<group>` 放置标签，包括 `dc-nats-0`。无效配置块会被拒绝。`dcctl` 接受的范围更窄：参见[可选的 NATS 分片组](#v160-nats-shard-groups)。

- **每个实体和作用域中的属性键只保留一个值。** 升级会合并并发首次写入产生的重复活动行，保留 ID 最小的行，并排队修复组成员关系和配置。创建索引期间会短暂阻塞属性写入。在新旧副本共存期间，旧副本可能拒绝发生竞争的首次写入；请重试。无需更改配置。

- **升级说明：** 将 `AgentExecutionsFailing` 加入告警路由；若保留上游代理备用方案，请同步 `nats:2.15.3-alpine`。使用新增备份命令前暂停写入。
- **升级说明：抓取端口。** 如果使用自己的 Prometheus 配置抓取 Pod 指标，请将目标端口从 8080 改为 8083；chart 的 ServiceMonitor 和探针会自动更新（[详情](#v160-ops-port)）。

#### 新增值和命令 {#v160-values-commands}

| 设置或命令 | 变化 / 运维操作 |
|---|---|
| `--size perf-20k` | 新增吞吐量规格预设：broker 请求 6 CPU / 6Gi，内存上限 10Gi，JetStream 40Gi，事件存储 260Gi。不承诺 50 ms 的 PUBACK 延迟目标；尚未按该预设的完整配置进行实测。 |
| GKE `nats_jetstream_storage_class` | 存在相应 GKE 类时，新 broker 默认使用 `premium-rwo`（`pd-ssd`）；若 GKE `premium-rwo` 类使用其他磁盘类型则拒绝。显式覆盖优先。已有 broker/PVC 和升级保持原存储类；此操作不会迁移磁盘。 |
| GKE `backup_object_store_storage_class` | 新建的备份 PVC 默认使用专用 `pd-standard` 存储类，而非消耗 SSD 配额的 `standard-rwo`/`pd-balanced`。可用此变量覆盖。升级保留已有 PVC 的容量和存储类，不会自动迁移磁盘。其他提供商保持不变。 |
| `infrastructure.nats.shardGroups` | 为两个摄取流按租户路由，需主动启用；缺省时保持原路由。分组流会将预留容量乘以 `count`，启用前需规划容量。仅在滚动更新完成后启用；参见[可选的 NATS 分片组](#v160-nats-shard-groups)。 |
| `dcctl backup`、`dcctl backup inspect --verify`、`dcctl restore --plan` | **预览**命令，用于本地流/对象备份集、字节验证和只读恢复规划。目前没有执行恢复的命令，也不协调数据库检查点。参见[导出流和对象内容](./disaster-recovery.md#export-streams-objects)。 |

#### 完整重载代理会保持认证调用已放行的设备连接 {#v160-broker-reload}

过去，完整重载代理会关闭认证调用已放行的所有连接，其中包括所有使用用户名进行认证的 MQTT 设备。代理现在基于 `nats-server` `v2.15.3` 构建，因此更改代理 TLS Secret 中的 CA 包（CA 轮换）或手动重载时，会保持这些连接打开。只有当 chart 的 config-reloader 边车因挂载的 TLS Secret 发生变化而发送 SIGHUP，或操作员手动运行 `nats-server --signal reload` 时，才会发生不重启的重载。

当重载更改服务器自身的认证设置（其配置的用户、nkeys、标签或 TLS verify/map 设置）时，`nats-server` 仍会将认证调用已放行的连接重新交给认证调用。此时被认证调用拒绝的连接会被关闭。

- **无需操作。**
- **更改配置仍会滚动重启代理。** 代理配置被渲染到 ConfigMap 中，chart 将其哈希写入 StatefulSet 的 pod 模板。渲染后配置的任何变化（设置、容量或账户）都会重启代理 pod，因此设备会像以前一样重新连接；对于任何更改代理镜像或配置的升级，`dcctl upgrade` 都会滚动重启代理。
- **同一 CA 下续期的 TLS 证书**保持自 `v0.27.0` 起的原有行为：无需重载即可应用于新连接。
- **用于回退到上游代理的上游镜像现在是 `nats:2.15.3-alpine`**：按[设备证书](./device-certificates.md)所述，设置 `server_image_repository = "nats"` 和 `server_image_tag = "2.15.3-alpine"`。如果你保留了这种回退准备，请将新标签加入镜像同步列表。
- **仍在启动中的代理 Pod 被停止时，现在会立即停止。** 代理沿用上游 `nats-server` `v2.15.3` 的信号处理：在 JetStream 仍在恢复时请求的重载会在启动完成后执行，而 SIGTERM 或 Pod 的 preStop 钩子发送的 lame duck 请求会在此期间得到处理，不再排在它之后等待。在代理开始接受客户端之前到达的 lame duck 请求会被忽略，因此随后的 SIGTERM 会停止该 Pod。`nats-server -t --test_reload <文件>` 会报告配置文件能否在不重启的情况下重载（不能时退出码为 3）。

#### 地理围栏规则的实时检测需要位置访问权限 {#v160-detection-stream-location}

`detectionStream` 订阅不再向缺少 `location:read` 权限的调用者发送测试设备是否位于地理围栏内的规则的检测结果，与规则预览的权限要求保持一致。其他检测结果不变。监视地理围栏规则的仪表板或智能体需要使用包含位置访问权限的角色或令牌。通过只读 OAuth 作用域签发的令牌不包含 `location:read`。

#### 太阳活动需要安装位置访问权限 {#v160-solar-campaign-position}

创建 `SOLAR` 活动、读取其元素（`campaignElements`）或周期（`campaignCycles`），以及重试其失败项（`retryCampaignFailed`），现在还需要 `location:installed:read`（或 `location:read`）。太阳计划中每台设备的时间会透露该设备的安装位置；对于只有一台设备的组，周期计数也可能透露由位置推导出的结果。`CRON` 和 `CALENDAR` 活动保持不变。请为管理太阳活动的角色添加此权限。

#### 自动化动作的权限 {#v160-action-authorities}

编写或发布自动化动作需要具备执行该动作的权限。创建或更新包含 `sendCommand` 动作的规则，以及发布或回滚到启用规则中包含该动作的配置文件版本，除了 `device:write` 之外，还需要 `command:write`。`httpCall` 和 `publish` 动作需要 `connector:write`。仅具备 `device:write` 的角色仍可编写和发布只触发告警的规则。 已发布的规则继续运行；系统将在下次发布或回滚时进行检查。升级前，请检查负责编写自动化规则的角色。旧版 `device-management` 副本在被替换之前不会执行此检查。

#### 发布配置文件时应用层级的规则上限 {#v160-rule-ceiling}

- **可选规则上限：** 设备配置发布和回滚仅在租户层级显式设置 `rulesPerTenant` 时执行上限。缺失/null 值及旧版 user-management 架构保留现有行为；保持或减少规则数量不会读取 user-management。其他查询错误仅拒绝增加规则的更改。

#### 删除定义后的草稿作者 {#v160-review-deleters}

变更审查通常不会将仅删除了其他配置文件上的规则、指标或命令的人员视为草稿作者。对于未归属到具体行的删除操作（升级前执行的操作，或滚动升级期间由尚未升级的 pod 执行的操作），系统仍采取保守处理：如果审查中的配置文件缺少某个定义，且没有已归属的删除操作能够解释，则自该定义最后一次已归属的变更以来，所有执行过未归属删除操作的人员都算作作者。已运行的审查保留当时记录的作者列表；如果某位批准人被排除，请重新运行审查。删除配置文件时，现在会为其删除的每个定义写入一条审计记录。

- **无需操作。** 如果某位批准人因其他配置文件上的删除操作而被排除，请重新运行审查。

#### 委派管理员不能授予或操作超出自身权限的内容 {#v160-no-escalation}

运营方管理 API 现在将系统角色不是超级用户的身份限制在其当前系统角色授予的权限内。租户 API 在缩减租户角色权限前，也会检查该角色的当前权限及其成员。

- **运营方管理 API：** 委派身份只能分配自身拥有全部权限的系统角色；只有拥有目标身份的系统角色授予的全部权限，才能操作该身份（密码、登录关联、会话、启用或删除、系统角色）；创建、更改或删除角色也必须在自身权限范围内。
- **成员资格和登录角色映射：** 在运营方管理 API 中，委派身份只能使用自身拥有全部权限的租户角色来添加或更改租户成员资格；只有拥有该成员资格的角色授予的全部权限，才能更改、禁用或移除该成员资格；也只能将身份提供方群组或即时预配登录映射到自身拥有全部权限的租户角色。 对其他身份执行操作还要求拥有其租户成员资格授予的全部权限；将账号关联到租户身份提供方连接、更改该连接服务的租户范围或更改其群组声明时，必须拥有该连接授予的所有角色。
- **租户 API：** 租户管理员只有拥有租户角色授予的全部权限，才能更改该角色；只有拥有其成员拥有的全部权限，才能移除该角色的权限。
- **需要检查：** 如果委派系统角色需要管理超级用户或定义超出自身权限的角色，现在会收到 `ESCALATION`；请以超级用户身份执行这些更改。

#### 安全：设备不能再将自身分配给客户、区域或资产 {#v160-device-relationship-refused}

- 如果 `NewRelationship` 事件指定了跟踪关系类型（内置 `assigned` 或任何标记为跟踪的类型）或内置 `contains`，则在第一次投递时拒绝该事件，并以原因 `Invalid` 记录到 `failed-events` 流，不会重试。这改变了已接受的设备事件的行为；[版本策略](./versioning-policy.md)允许安全修复作出此类更改。
- 请使用认领码或 `createEntityRelationship` mutation 分配设备。
- 设备此前创建的关系不会被删除。请检查设备的跟踪关系。
- 非跟踪类型（包括 `member`）仍会被创建，除非租户已将该类型标记为跟踪。`NewRelationship` 事件本身不再把新目标作为锚点携带：非跟踪关系不代表归属，因此该事件按设备现有的锚点限定范围，与其下一个遥测事件相同。
- 滚动升级期间，仍运行 v1.5 的 Pod 在被替换之前仍会创建该关系。
- 新增计数器 `resolve_relationship_refused_total` 统计此类拒绝，不带租户或设备标签；警告日志会标明租户和关联信息。

#### 存储新建关系的事件 {#v160-new-relationship-event}

设备发送的 `NewRelationship` 事件现在会连同其锚点一起存储，并显示在 `events`（事件类型 `0`）中。在此版本之前，即使关系已创建成功，该事件仍会被重试，随后以 `API_CALL_FAILED` 原因记录为摄取失败。对于事件存储不支持的事件类型，现在会在首次投递时以 `INVALID` 原因拒绝，而不再重试。升级前发送的事件不会恢复。

- **无需进行任何操作。**

#### HTTP 设备上行路由限制凭据猜测 {#v160-device-uplink-guessing}

通过 `httpIngest.deviceUplinks` 按需启用的 HTTP 设备上行路由现在会按来源地址限制请求（返回 `429` 和 `Retry-After`）。某个设备的凭据反复验证失败后，路由将暂停回答认证猜测（返回 `503` 和 `Retry-After`），而不再重复返回 `401`。在此设备的凭据遭到猜测期间，该设备的每条 HTTP 上行请求（包括使用正确凭据的请求）都会收到 `503` 和 `Retry-After`，直到暂停结束（每次最长 60 秒）。

- **通过代理的 MQTT 连接通信的设备不受影响。**
- **收到 `503` 时应退避重试**，即使提交的是错误凭据：至少等待 `Retry-After` 指定的时间再重试。

#### 对 HTTP 凭据的更严格检查 {#v160-http-credentials}

`httpCall` 动作不能再指定连接器的凭据。发布规则时，`connector/` 命名空间中的密钥句柄会被拒绝；已发布规则若使用此类句柄，会以 `invalid` 结果进入死信队列，而不会发送连接器的凭据。旧版 `outbound-connectors` 副本在被替换之前不会执行此限制。

#### 更改已存储凭据的目标时必须重新提供凭据 {#v160-credential-resupply}

**更改已存储凭据的目标。** `updateConnector`、`rollbackConnector`、`updateNotificationChannel` 和 `updateAiProvider` 会在以下情况下返回 `SECRET_RESUPPLY_REQUIRED`：请求更改持有已存储凭据的连接器、通知渠道或 AI 提供方的类型或目标，但未在同一请求中发送 `secret`。请重新提供凭据，或使用 `secret: null` 删除凭据。不改变目标的编辑（名称、主题、QoS、自定义请求头、链接基础 URL、模型、启用状态）不受影响，现有连接器、渠道和提供方会继续投递。若要将连接器回滚到目标不同的版本，请改用 `updateConnector` 并提供凭据。使用 `dcctl product apply` 时，如果清单更改目标，请设置连接器的凭据句柄。旧副本在被替换之前不会执行此检查。

#### 签名密钥轮换同步到所有副本 {#v160-signing-key-follow}

设置 `auth.signingKeyMaxAgeDays` 后，user-management 现在会在运行期间轮换超龄的签名密钥：副本启动约 15 分钟后开始检查，此后每当密钥超过设定年龄时都会轮换，不再只在启动时轮换。所有副本会在 30 秒内采用新密钥，并在遇到需要该密钥的令牌或密钥请求时立即采用它来验证令牌和提供公钥。早期版本仅在执行轮换的副本中应用新密钥，因此在有多个副本时，部分登录会失败，直到其他副本重启。

- **无需操作。** 当此设置为 0 时，行为不变。

#### ingress-nginx 不再接受配置片段 {#v160-nginx-snippets-off}

`dcctl install` 会关闭集群保留的 `ingress-nginx` 控制器上的配置片段，并将其注解风险级别恢复为控制器的默认值。在应用更改前，只要该控制器类的任何 Ingress 带有 `*-snippet` 注解，安装就会拒绝继续，并列出每个 Ingress 和注解。请使用 `dcctl upgrade` 升级所属实例，或移除该注解，然后重新运行 `dcctl install`。

自 `v0.25.2` 起，没有任何 chart 设置配置片段。仅运行 Traefik 的新集群不受影响。此更改会在集群下一次运行 `dcctl install` 时生效。

#### 指标和健康检查端点移至 ops 端口 {#v160-ops-port}

服务指标和健康端点已从 API 端口移出。每个功能区域的 Pod 和 Service 在端口 8083（`ops`）提供 `/metrics`、`/healthz` 和 `/readyz`，无法再通过 `/api/<area>/...` 访问。chart 的 ServiceMonitor 和探针会自动更新。如果使用自己的 Prometheus 配置抓取 Pod 指标，请将目标端口从 8080 改为 8083。滚动升级期间，尚未替换的 Pod 不会被抓取指标。

#### `dcctl destroy` 拒绝将路径作为实例名称，并且不再信任猜测的集群 {#v160-destroy-instance-name}

`dcctl destroy` 会拒绝不是简单名称的实例参数（`.`、`..` 或任何包含路径分隔符的值）。早期版本会将其解析为 `~/.devicechain/instances` 下的目录，并删除解析后的目标。

- **清理本地状态需要依据。** 只有当实例拥有集群记录且该集群记录中的上下文不再响应时，`dcctl destroy` 才会清理缺失集群的本地状态。没有记录的实例会执行正常卸载；如果无法连接集群，卸载会失败。请传入 `--kube-context`，或者在确认集群确实已不存在时手动删除该实例的目录。

#### MCP 内存限制 {#v160-mcp-memory}

- **MCP 内存限制：**超过 256 KiB 的下游 GraphQL 响应和超过 1,000 个时间桶的聚合结果会被整体拒绝；请缩短时间范围、选择单个指标或增大聚合间隔。POST 请求和携带请求体的请求在读取或解析前受到限制：每个调用者最多同时处理 4 个请求，每个进程最多 64 个。消息准入为每个批量请求最多预留 4 个名额；符合配置的批量请求（默认最多 16 条）会在此上限内分组执行工具。每条消息仍计入调用者的速率限制；超出限制的请求返回 HTTP 429 和 Retry-After。HTTP 请求结束时会取消工具执行并释放执行名额。请求体读取预算会在工具执行前释放，单个请求体仍限制为 1 MiB。无需更改配置。

#### MCP 发现会公布服务器执行的范围 {#v160-mcp-discovery}

MCP 发现现在提供 `read-only` 和 `location`，并在 MCP 的 `agentActuation.enabled` 开启时提供 `act:*`。认证质询允许客户端使用这些元数据，不再将范围固定为 `read-only`。接入仍只要求 `read-only`；操作工具保留各自的范围和权限检查。无需更改配置，现有令牌继续有效。请重新授权客户端以授予新发现的范围；使用提案时还需在授权服务器上开启代理操作。

#### API 验证与管理 {#v160-api-checks}

- `evaluateRules`（Preview）在编译任何规则前，以 `LIMIT_EXCEEDED` 拒绝超过 256 条检测规则的草稿图。规则验证会保留每条诊断的字段路径，便于调用方定位错误字段。
- `existingEntityRefs` 在执行任何查询前拒绝未知实体类型或同一类型超过 1,000 个查询键的请求（包括重复项）；此前可能在执行其他查询后才拒绝。
- 管理 API 的 `updateRole` 新增可选 `expectedUpdatedAt`；版本过期时拒绝保存，调用方可重新加载。省略时仍为无条件更新。
- `setTenantEnabled` 拒绝已开始清除的租户，并在持锁时重新检查身份，避免并发清除或令牌复用切换其他租户的状态。
- 代理执行决策（Preview）在读取会话前，将提议者和批准者主体限制为 256 个字符，并要求提供的会话 ID 为规范的 36 字符 UUID；取消请求会停止后续读取。服务账号提议者可以省略会话 ID。
- 已过期的获批自动化审查在读取和筛选时显示为 `EXPIRED`，包括恰好到期的时刻；发布返回 `REVIEW_EXPIRED`，拒绝审查返回 `REVIEW_NOT_PENDING`。过期根据已存到期时间和当前策略计算，无需后台更新记录。

#### 命令保留 {#v160-command-holds}

删除维护窗口不再无条件释放等待的命令。明确确认设备类型已无窗口的命令仍会在约一分钟内释放；停用设备或尚未解析类型的命令继续受限，解析后跟随其类型。无法确定命令通道时，命令保持 `QUEUED` 且绝不发布，恢复后的首次投递扫描再处理（默认 30 秒），不再转为 `HELD`。保留协调器会在持续释放命令时继续读取，最多使用协调间隔的一半，遇到没有释放的页面便停止。新增计数器：`command_delivery_dispatch_deferred_total`。无需操作。滚动升级完成前，旧副本仍采用原有行为。

#### 活动规划在租户之间让出执行机会 {#v160-campaign-planning}

- **活动规划在租户之间轮流进行。** 每个租户每轮最多规划 1,000,000 个活动元素和 128 个周期；规划预算 2 秒用完后不再开始新的周期，已开始的周期总会完成，每个周期完成后都会保存进度，其余周期在下一轮继续。规划时的分组和设备位置查询在 2 秒后超时，作为本轮错误上报，但不会将活动标记为失败。最多同时处理四个租户。大型计划可能需要多轮才能填满 36 小时窗口。这些限制无需新配置或迁移，随 command-delivery Pod 滚动升级生效。

#### 下载、发送和通知在租户之间公平分配 {#v160-tenant-fairness}

- **OTA 下载：** 每个副本的 OTA 下载现在限制每个（租户、设备、分配）最多一个进行中的流。只有当其他租户占用下载名额时，已达到或超过 `updates.maxConcurrentDownloads` 一半的租户（向下取整，至少一个；默认总数为 16 时为八个）才会被拒绝启动新流。单个租户可以使用全部名额。超出限制的请求返回现有的 503 busy 响应和 `Retry-After: 5`；请在该延迟后重试。无需更改配置。
- **连接器发送和 LoRaWAN 下行：** 出站连接器派发和 LoRaWAN 下行现在将每个租户的并发数限制为每个副本发送池的一半（向下取整，至少一个），由 `maxConcurrentSends` 决定。超出限制的消息等待代理重新投递，不占用发送工作线程。只有一个工作线程的池仍会串行处理租户；至少使用两个才能提供隔离。无需新增配置；每个 Pod 升级后即应用此限制。
- **通知调度按租户限制处理时间。** 四个工作线程按轮换顺序处理租户。当有其他租户在等待工作线程时，达到完整发送预算的两倍（默认 80 秒）后，该租户不再启动新的暂存通知释放或升级通知；已启动的警报仍保留完整发送预算，其余警报留待下一轮。恢复通知在各租户任务内使用独立预算处理。某个渠道的超时重试耗尽后，本次租户任务将跳过该渠道；仅发往这些渠道的升级通知保留升级级别，并在下一轮重试。首次探测仍可能消耗一个级别。无需更改配置，现有配置在滚动升级期间仍然有效。

#### 升级通知计时 {#v160-escalation-clock}

升级通知的等待时间现在从通知发送时开始计算。由延迟到达的读数触发的告警不再在调度器下一次运行时立即升级，设备时钟快于平台时钟也不再延迟升级通知。

- **无需操作。** 升级会自动添加一个可为空的时间戳列；现有未关闭告警在下次发送通知前仍按原来的方式计时。

#### Sparkplug 跟踪容量上限 {#v160-sparkplug-limits}

**升级说明：** 每个 Sparkplug 来源最多跟踪 50,000 个节点会话，其中未发送 NBIRTH 的节点最多 1,024 个。达到上限后，新的身份消息会被丢弃，不写入数据，也不请求重生；现有节点仍可重生，在线会话不会被驱逐。空闲占位会话在 10 分钟后过期，离线会话在 24 小时后过期；达到容量上限时，新生节点可回收最旧的离线会话。活动历史最多保留 50,000 个身份，容量不足时清理旧记录，仍满时拒绝新记录（已满期间，探测不会将未记录的身份判定为静默）。组、节点和设备主题层级超过 256 个字符时会被拒绝，与现有设备注册上限一致。 容量拒绝通过 `devicechain_sparkplugingest_session_refused_total` 统计，`reason` 为 `placeholder_cap`、`session_cap` 或 `seen_cap`。容量和队列已满警告分别限制为每个来源每 10 分钟一次，逐消息日志改为 debug。滚动升级无需更改配置。

#### 实体属性的键保持唯一 {#v160-entity-attributes}

- **每个实体和作用域中的属性键只保留一个值。** 升级会合并并发首次写入产生的重复活动行，保留 ID 最小的行，并排队修复组成员关系和配置。创建索引期间会短暂阻塞属性写入。在新旧副本共存期间，旧副本可能拒绝发生竞争的首次写入；请重试。无需更改配置。

#### 可选的 NATS 分片组 {#v160-nats-shard-groups}

NATS 分片组默认仍处于关闭状态。新实例可在安装时通过 `--values` 在
`instance.config.infrastructure.nats.shardGroups` 处启用。该设置支持 2–5 个分组，
使用 R1 流，且要求集群至少有三台代理服务器，并拒绝 `--ha`。放置标签使用对应的
`dc-nats` Pod 名称；显式标签必须与这些名称一致。默认配置文件支持对 `inbound-events`
分片；当部署了 device-state 或 event-processing 时会拒绝 `resolved-events`，因为这些
消费者仍只读取第零组。每个选定的流都会在每个分组中预留其完整的字节上限，`dcctl`
会在创建或更改任何基础设施之前检查该预留，因此如果检查拒绝，请先扩容 JetStream 卷。

对于代理为单台服务器的现有实例，`dcctl upgrade` 会拒绝启用分片组，因为不支持将
已存有数据的独立 JetStream 服务器转换为集群。如果某次升级的 `--values`（或
`--reset-values`）会删除已记录的 `shardGroups` 块、降低其 `count`，或从其 `suffixes`
中移除某个流，该升级也会被拒绝，因为这些分组可能存有数据：请在每个替换用的 values
文件中保留该块。

#### 流和对象备份（Preview） {#v160-backup}

`dcctl backup <instance> --destination <dir>` 将需保留以供检查点恢复的流及其消费者和对象内容导出到新的本地备份集。先暂停写入；此命令不协调数据库恢复截止点或维护模式。它适用于所有部署配置，并记录因功能区未部署而不存在的流。每个流、KV 桶和对象存储都声明其备份类别。

`dcctl backup inspect <dir> --verify` 可离线验证清单及每个列出文件的大小和 SHA-256；它不能证明清单完整或检查点一致。`dcctl restore <instance> --from <dir> --plan` 在读取目标流和对象前验证字节、身份和覆盖范围，并拒绝可能覆盖较新内容的计划。规划时应暂停目标写入。`--force-foreign` 允许不同实例或租户身份，但不会绕过覆盖范围和较新内容检查。不会应用恢复。对于文件系统 blob，请使用 `--blob-directory` 指定只读挂载或导出的 PVC 根目录。参见[导出流和对象内容](./disaster-recovery.md#export-streams-objects)。

#### 控制台和仪表盘编辑 {#v160-console-dashboards}

- 仪表盘编写者可以选择断点并移动、缩放或放置该断点的组件；基础布局修改仍会传递到继承它的布局。现有布局仍然有效。
- 规则编辑器为支持的表达式新增 Filter、Exists 和 Math 表单；仍可使用自定义表达式。
- **Admin → Settings → Functional areas** 显示已部署的功能区及启用其他功能区的方法。
- 仅显示一次的密钥和邀请对话框必须选择 **Done** 才会关闭；Escape 和外部点击会保持值可见。详情页在导航后忽略过期响应。
- 批量触发在重试时复用幂等令牌，设备命令面板忽略重复提交。命令控制组件在切换设备时清除待处理状态；告警分流页面在事件风暴期间合并刷新。
- 仪表盘在设备类型查询先前未命中后会重新查询。限定作用域的图表配色会随宿主主题变化而刷新。GraphQL 文档采用直接导入，以缩小控制台包体积。
- 共享仪表盘组件的其余文本已翻译为西班牙语和简体中文。

#### 控制台标签页不再沿用其他标签页的登录会话 {#v160-console-tab-session}

控制台标签页的会话结束后，不再沿用其他标签页为不同租户或用户开启的会话，而是返回登录页面。

- **无需操作。**

#### 编写工具与 SDK {#v160-authoring-tools}

- 在 `frontend` 中运行 `npm run create-widget -- acme/tank-level ../my-widget` 可生成带类型的自定义组件包，将 SDK 作为对等依赖，并拒绝已有目标目录。若对等版本为 `0.0.0-dev`，请在安装前改为已发布版本。
- 在 `backend/services/event-processing` 中运行 `go run ./cmd/rule-schema`，或在 `backend/services/outbound-connectors` 中运行 `go run ./cmd/connector-schema mqtt`，可输出规则和连接器编写所需的 JSON Schema。
- 已发布的客户端和组件 SDK 包现在包含其代理指导文件。

#### 旧版 npm 导出已弃用 {#v160-npm-deprecations}

以下导出在 `v1.6.0` 中被弃用，但仍按原样工作。请将导入或调用更新为下列替代项。本次不会移除任何导出。只有在弃用至少两个次要版本之后，才能在主要版本中移除；从 1.6 开始弃用的导出，最早也只能在 1.7 之后的某个主要版本中移除。`BuiltinOptionIssue` 表示现有的内置组件问题结构（`widgetType`）；通用的 `@devicechain/dashboards.OptionIssue`（`widgetId`）仍受支持。

- `@devicechain/widgets` / `pickSample` 已弃用；请改用 `@devicechain/widget-sdk.pickSample`。
- `@devicechain/widgets` / `optBoolean` 已弃用；请改用 `@devicechain/widget-sdk.optBoolean`。
- `@devicechain/widgets` / `WidgetProps` 已弃用；请改用 `@devicechain/widget-sdk.WidgetProps`。
- `@devicechain/widgets` / `WidgetComponent` 已弃用；请改用 `@devicechain/widget-sdk.WidgetComponent`。
- `@devicechain/widgets` / `WidgetFrame` 已弃用；请改用 `@devicechain/widget-sdk.WidgetFrame`。
- `@devicechain/widgets` / `WidgetSubjectProvider` 已弃用；请改用 `@devicechain/widget-sdk.WidgetSubjectProvider`。
- `@devicechain/widgets` / `WidgetSelectProvider` 已弃用；请改用 `@devicechain/widget-sdk.WidgetSelectProvider`。
- `@devicechain/widgets` / `useWidgetSelect` 已弃用；请改用 `@devicechain/widget-sdk.useWidgetSelect`。
- `@devicechain/widgets` / `WidgetTimeZoneProvider` 已弃用；请改用 `@devicechain/widget-sdk.WidgetTimeZoneProvider`。
- `@devicechain/widgets` / `useWidgetTimeZone` 已弃用；请改用 `@devicechain/widget-sdk.useWidgetTimeZone`。
- `@devicechain/widgets` / `WidgetThemeRootProvider` 已弃用；请改用 `@devicechain/widget-sdk.WidgetThemeRootProvider`。
- `@devicechain/widgets` / `useWidgetThemeRoot` 已弃用；请改用 `@devicechain/widget-sdk.useWidgetThemeRoot`。
- `@devicechain/widgets` / `WidgetCandidatesProvider` 已弃用；请改用 `@devicechain/widget-sdk.WidgetCandidatesProvider`。
- `@devicechain/widgets` / `useWidgetCandidates` 已弃用；请改用 `@devicechain/widget-sdk.useWidgetCandidates`。
- `@devicechain/widgets` / `useFlashOnChange` 已弃用；请改用 `@devicechain/widget-sdk.useFlashOnChange`。
- `@devicechain/widgets` / `flashTextStyle` 已弃用；请改用 `@devicechain/widget-sdk.flashTextStyle`。
- `@devicechain/widgets` / `flashBackgroundStyle` 已弃用；请改用 `@devicechain/widget-sdk.flashBackgroundStyle`。
- `@devicechain/widgets` / `FlashValue` 已弃用；请改用 `@devicechain/widget-sdk.FlashValue`。
- `@devicechain/widgets` / `fitValueFont` 已弃用；请改用 `@devicechain/widget-sdk.fitValueFont`。
- `@devicechain/widgets` / `useMeasurementStream` 已弃用；请改用 `@devicechain/widget-sdk.useMeasurementStream`。
- `@devicechain/widgets` / `useAlarmStream` 已弃用；请改用 `@devicechain/widget-sdk.useAlarmStream`。
- `@devicechain/widgets` / `useCommandStream` 已弃用；请改用 `@devicechain/widget-sdk.useCommandStream`。
- `@devicechain/widgets` / `useLocationStream` 已弃用；请改用 `@devicechain/widget-sdk.useLocationStream`。
- `@devicechain/widgets` / `useDatasourceAvailability` 已弃用；请改用 `@devicechain/widget-sdk.useDatasourceAvailability`。
- `@devicechain/widgets` / `useDashboardHub` 已弃用；请改用 `@devicechain/widget-sdk.useDashboardHub`。
- `@devicechain/widgets` / `useHubConnectionState` 已弃用；请改用 `@devicechain/widget-sdk.useHubConnectionState`。
- `@devicechain/widgets` / `HubConnectionBadge` 已弃用；请改用 `@devicechain/widget-sdk.HubConnectionBadge`。
- `@devicechain/widgets` / `useResolvedBindings` 已弃用；请改用 `@devicechain/widget-sdk.useResolvedBindings`。
- `@devicechain/widgets` / `useViewerBindings` 已弃用；请改用 `@devicechain/widget-sdk.useViewerBindings`。
- `@devicechain/widgets` / `useSlotCandidates` 已弃用；请改用 `@devicechain/widget-sdk.useSlotCandidates`。
- `@devicechain/widgets` / `useCandidates` 已弃用；请改用 `@devicechain/widget-sdk.useCandidates`。
- `@devicechain/widgets` / `css` 已弃用；请改用 `@devicechain/widget-sdk.css`。
- `@devicechain/widgets` / `FlashDirection` 已弃用；请改用 `@devicechain/widget-sdk.FlashDirection`。
- `@devicechain/widgets` / `WidgetFrameProps` 已弃用；请改用 `@devicechain/widget-sdk.WidgetFrameProps`。
- `@devicechain/widgets` / `WidgetSelect` 已弃用；请改用 `@devicechain/widget-sdk.WidgetSelect`。
- `@devicechain/widgets` / `WidgetCandidates` 已弃用；请改用 `@devicechain/widget-sdk.WidgetCandidates`。
- `@devicechain/widgets` / `MeasurementStreamState` 已弃用；请改用 `@devicechain/widget-sdk.MeasurementStreamState`。
- `@devicechain/widgets` / `AlarmStreamState` 已弃用；请改用 `@devicechain/widget-sdk.AlarmStreamState`。
- `@devicechain/widgets` / `CommandStreamState` 已弃用；请改用 `@devicechain/widget-sdk.CommandStreamState`。
- `@devicechain/widgets` / `LocationStreamState` 已弃用；请改用 `@devicechain/widget-sdk.LocationStreamState`。
- `@devicechain/widgets` / `CandidatesState` 已弃用；请改用 `@devicechain/widget-sdk.CandidatesState`。
- `@devicechain/widgets` / `DatasourceAvailability` 已弃用；请改用 `@devicechain/widget-sdk.DatasourceAvailability`。
- `@devicechain/widgets` / `HubConnectionBadgeProps` 已弃用；请改用 `@devicechain/widget-sdk.HubConnectionBadgeProps`。
- `@devicechain/widgets` / `ThemeVar` 已弃用；请改用 `@devicechain/widget-sdk.ThemeVar`。
- `@devicechain/widgets` / `EChart` 已弃用；请改用 `@devicechain/widget-sdk/echarts.EChart`。
- `@devicechain/widgets` / `buildLineOption` 已弃用；请改用 `@devicechain/widget-sdk/echarts.buildLineOption`。
- `@devicechain/widgets` / `buildGaugeOption` 已弃用；请改用 `@devicechain/widget-sdk/echarts.buildGaugeOption`。
- `@devicechain/widgets` / `resolveChartTheme` 已弃用；请改用 `@devicechain/widget-sdk/echarts.resolveChartTheme`。
- `@devicechain/widgets` / `useChartTheme` 已弃用；请改用 `@devicechain/widget-sdk/echarts.useChartTheme`。
- `@devicechain/widgets` / `EChartOption` 已弃用；请改用 `@devicechain/widget-sdk/echarts.EChartOption`。
- `@devicechain/widgets` / `LineChartOptions` 已弃用；请改用 `@devicechain/widget-sdk/echarts.LineChartOptions`。
- `@devicechain/widgets` / `GaugeChartOptions` 已弃用；请改用 `@devicechain/widget-sdk/echarts.GaugeChartOptions`。
- `@devicechain/widgets` / `ChartTheme` 已弃用；请改用 `@devicechain/widget-sdk/echarts.ChartTheme`。
- `@devicechain/widgets` / `WIDGET_CHANNEL` 已弃用；请改用 `builtinWidgetDescriptors[id].channel`。
- `@devicechain/widgets` / `WIDGET_BINDS_DATASOURCE` 已弃用；请改用 `builtinWidgetDescriptors[id].datasource.mode !== 'none'`。
- `@devicechain/widgets` / `isTerminalStatus` 已弃用；请改用 `@devicechain/dashboards.isTerminalCommandStatus`。
- `@devicechain/widgets` / `OptionIssue` 已弃用；请改用 `@devicechain/widgets.BuiltinOptionIssue`。

#### 重新投递的解码失败使用记录预算 {#v160-decoder-redelivery-budget}

重新投递的载荷解码器失败（例如重启后积压了某个解码器持续失败的设备类型的上行消息）现在与其他失败一样，使用按设备类型分配的失败记录预算。预算耗尽后，该失败会计为已抑制，不会写入 failed-events 流，即使首次投递时从未生成记录也是如此。过去，重新投递的失败总会被记录，因此重启可能会记录积压的每一条上行消息。

- **无需操作。**

#### 无效的摄取失败记录会标明设备 {#v160-invalid-failures-name-the-device}

对于可读取的事件（值不符合指标定义、事件早于 366 天前或被解码器拒绝），device-management 记录的 `INVALID` 摄取失败现在会标明设备和事件类型，并像其他原因一样按设备限制记录数。在此版本之前，这些记录不显示设备，整个租户共用每小时十条记录的上限。升级前已索引的记录不会重写。

- **无需操作。** 不会回填历史记录。现有上限现在适用：每个设备、每种原因每小时十条记录，每个租户、每种原因每小时最多逐条列出 1,000 条记录。整个设备群发送无效载荷时可能达到此上限。

#### 代理操作的指标和告警（Preview） {#v160-agent-metrics}

`device-management` 现在会报告代理操作：按类型和结果统计的提议、按结果统计的批准、按类型和结果统计的执行及其耗时，以及已过期的提议。Platform 看板新增 **Agent actions (preview)** 一行，并新增警告级告警 `AgentExecutionsFailing`，在最终批准持续无法完成时触发。拒绝不会触发该告警：依赖变化、策略拒绝、紧急停止栅栏关闭或批准人被拒绝都会计入各自的结果。这些指标列在[指标参考](./metrics-reference.md)中，该告警在[告警处理手册](./alert-runbook.md#agentexecutionsfailing)中有对应条目。与代理操作一样，它们属于 Preview：名称和标签仍可能在次要版本中变化。

#### v1.6.0 的其他变更 {#v160-other-changes}

- bootstrap 在首次写入前被拒绝时会保持已记录的阶段；MQTT 上行覆盖配置被拒绝时会指出冲突选项。
- OpenTelemetry 诊断和错误采用服务的结构化 JSON 日志。代理关闭时会关闭指标端点并等待其退出。
- PostgreSQL 客户端依赖升级到 `pgx` 5.12.0，包含 `GHSA-j38q-h3ph-jjpq` 和 `GHSA-95fj-3fxw-g62g` 的安全修复。无需更改配置。

#### v1.6.0 的架构变更与回退 {#v160-going-back}

`device-management` 新增一个迁移，添加实体属性修复队列和当前属性键的唯一索引。重复项保留最小行 ID，并排队修复组成员关系和配置；参见[实体属性](#v160-entity-attributes)。修复后，每个实体和作用域中的每个键有一个当前属性值。本版本其他功能区未新增架构迁移。

不支持降级。升级失败时，使用 v1.6.0 工具重试、向前升级到修正版，或从合适的备份恢复。Preview 流和对象命令不能替代数据库备份，也不会应用恢复。

### v1.5.0 — 自动化变更先评审，代理审批绑定登录会话 {#v150-upgrade}

`v1.5.0` 是从 `v1.4.0` 起的原地升级：先对集群运行 `dcctl install`，再对每个实例运行一次 `dcctl upgrade`，方法见[滚动升级](#rolling-upgrades)。**两个命令都使用新版本的 `dcctl`**，并向 `dcctl install` 提供集群原有设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`，以及 `--version`。升级会运行四项追加迁移：`device-management` 三项，`user-management` 一项。它们添加代理停用屏障和效果发件箱、自动化评审及其强制执行机制，以及执行代次列。参见[模式变更与回退](#v150-going-back)。如果来自 `v1.3.x` 或更早版本，请先逐个升级经过每个中间的次版本（包括 `v1.4.x`），并按各版本的说明使用该版本自己的 `dcctl` 和 `--version`：跳过次版本不是经过测试的路径（参见[支持的升级路径](./versioning-policy.md#upgrade-paths)）。`v1.4.0` 的说明见 [`v1.4.0` 说明](https://github.com/devicechain-io/devicechain/blob/v1.4.0/docs/docs/deployment/releases-and-upgrades.md#v140-upgrade)。

```bash
dcctl install local --version v1.5.0
dcctl upgrade local devicechain --version v1.5.0 --dry-run
dcctl upgrade local devicechain --version v1.5.0
```

本版本遵循[版本与升级策略](./versioning-policy.md)，增加受保障接口，不删除任何接口。自动化变更评审、代理提案工具、审批访问和会话内二次验证属于 [Preview 层级](./versioning-policy.md#preview-interfaces)，仍可能在次版本中改变。Helm values 不受兼容性策略保障；新增的可选功能默认关闭。

新增内容：

- **自动化变更可以要求先通过评审（Preview）。** 发布前将草稿与历史记录比较。参见[自动化变更评审（preview）](#v150-automation-review)。
- **代理审批绑定登录会话（Preview），** 并提供 MCP 提案工具、会话内二次验证和本地停用屏障。参见[代理操作（preview）](#v150-agent-actions)。
- **`dcctl` 只允许向前升级，** 并记住 MQTT 上行和关系存储 WAL 选择。参见[dcctl 只允许向前升级](#v150-dcctl)。
- **配置遇到无效值时拒绝启动，API 输入有明确上限。** 参见[配置失败时拒绝运行](#v150-configuration)、[GraphQL 输入限制](#v150-input-limits)和[现有操作的更严格检查](#v150-tightenings)。
- **LoRaWAN 队列接受不再被记作设备应答。** 参见[LoRaWAN 与命令](#v150-lorawan)。
- **链路追踪可跨服务跟踪事件，** 默认关闭。参见[可观测性](#v150-observability)。
- **部分更新保留并发修改。** 参见[保留并发修改](#v150-concurrent-edits)和[其他变更](#v150-other-changes)。

#### 升级前 {#v150-before-you-upgrade}

需要操作的事项，按执行顺序排列：

- **升级说明：** 计划向前修复或从备份恢复。`dcctl upgrade` 和重新运行 bootstrap 会拒绝比实例声明版本更早的版本；`dcctl install` 会拒绝比集群上次安装器更旧的 `dcctl`。没有绕过选项。使用目标版本的工具，按集群设置安装，再升级每个实例。
- **升级说明：** 重启前修正旧版本曾接受的值。chart 模式和 `dcctl` overlay 校验会在渲染时拒绝超出范围的签名密钥天数、负数摄取上限、负数命令交付设置、溢出的秒数设置和格式错误的 LwM2M token。服务启动时也会拒绝。检查 `functionalAreas.user-management.config.auth.signingKeyMaxAgeDays` 和 `signingKeyRetentionDays`：允许范围为 0–106751。`-1` 不再关闭轮换；最大年龄用 `0`（保留期为 `0` 时采用默认的 8 天）。在 `event-sources`、`lwm2m-ingest` 和 `sparkplug-ingest` 中删除负数 `ingestRateLimit.readingsPerSecond` 或 `ingestRateLimit.burst`。在 `command-delivery` 中删除负数 `defaultCommandTtlSeconds`、`heldCommandCeiling`、`sweepIntervalSeconds`、`maxDispatchFailures` 或 `deliveryMachineryReserve`。零或未设置仍采用默认值。
- **升级说明：** 检查秒数设置不超过 9223372036：`updates.sweepIntervalSeconds`、`updates.deadlines.*`、`deliverySeconds`、`stateRetentionSeconds`、`attemptRetentionSeconds`、`retentionSweepSeconds` 和 `escalationSweepSeconds`。将 LwM2M `deviceTypeToken` 修正为字母、数字、连字符和下划线，且以字母或数字开头，即使 `autoRegister` 关闭也一样；用 `--lwm2m-identities` 传入修正后的身份文件，该选项也会校验。这些检查同样在渲染时执行。`dcctl` 和 chart 默认不会渲染其中大多数设置；受影响的是操作员提供的值，默认安装不受影响。
- **升级说明：** 检查手写配置中仅在启动时拒绝的内容：负数 `maxOpenConnections` 或 `maxIdleConnections`、大于 65535 的端口、负数 `terminationGracePeriodSeconds`、配置文档后的多余内容（即使只是 `]` 或 `}`），以及会溢出的 `aheadHoldBudgetMillis`。重启前修正。`dcctl` 和 chart 安装通常不会生成这些无效文档。
- **升级说明：** 发布含有超过 1,000 条已启用检测规则的配置文件前，拆分或禁用部分规则。此类发布现在返回 `LIMIT_EXCEEDED`；`validateDetectionRules` 也会在编译前拒绝超过 1,000 条规则。按[输入限制](#v150-input-limits)和[写入检查](#v150-tightenings)检查 API 调用方。
- **升级说明：** 完成全部升级后再启用代理操作（Preview）。本版本之前记录的审批不再计数：请重新审批。升级前已登录的人必须刷新 token 或重新登录，其审批才会计数。混合版本部署会拒绝执行：新版 device-management 调用旧版 user-management 时返回 `POLICY_UNAVAILABLE`，新版 user-management 不计入旧版 device-management 提供的未绑定审批。如果租户通过新版 pod 被禁用后在滚动期间重新启用，请通过新版 pod 操作；旧版 pod 不会递增代次，代理操作会一直被拒绝，直到再次通过新版启用。
- **升级说明：** 完成部署后再为配置文件启用自动化评审保护。在 user-management 升到 `v1.5.0` 前，受保护发布返回 `REVIEW_UNAVAILABLE`；运行评审还需要 event-processing 为 `v1.5.0`。`dcctl` 不会替你安排这些区域的升级顺序。升级不会自动保护任何配置文件。
- **升级说明：** 新增可选功能只在部署完成后开启，并各自单独进行一次升级：`infrastructure.tracing` 和 MCP 区域的 `agentActuation.enabled`。重启的 `v1.4.0` pod 会拒绝不认识的实例或区域配置键。关闭追踪时整个块会从文档省略，因此默认安全。`--mqtt-device-uplinks` 不在此列：`v1.4.0` 已认识 `mqttDeviceUplinks` 键，新选项只记录选择。
- **升级说明：** 如果手写实例 id 不符合 chart 语法（小写字母、数字及内部连字符，最多 50 个字符），请计划用合规 id 重建：派生的 broker 或数据库名称可能冲突。服务仍会启动，但记录警告并将各区域的指标（例如 `devicechain_devicemanagement_instance_id_noncanonical`）设为 1。`dcctl` 和 chart 安装原本就符合要求。

新增 Helm values 和 `dcctl` 设置，默认关闭或保持原行为：

| 值 | 默认值 | 作用 |
| --- | --- | --- |
| `instance.config.infrastructure.tracing.enabled` | `false` | 启用 OpenTelemetry 追踪；关闭时从实例文档省略该块。 |
| `instance.config.infrastructure.tracing.protocol` / `endpoint` / `insecure` | `grpc` / `""` / `false` | 通过 `grpc` 或 `http` 导出 OTLP，指定收集器端点和传输安全设置。 |
| `instance.config.infrastructure.tracing.samplingRatio` | 未设置；启用时为 `0.1` | 基于父 span 的采样；允许显式 `0`。 |
| `functionalAreas.mcp.config` 下的 `agentActuation.enabled` | 未设置（`false`） | 注册 MCP 提案工具；仍受 user-management 实例开关和租户策略约束。 |
| `--mqtt-device-uplinks` / `spec.mqttDeviceUplinks` | 未设置；没有既有选择时关闭 | 记录 MQTT 上行选择；`=false` 关闭，省略则保留已记录选择。 |
| `dcctl install --relational-store-max-wal-mib` | 省略保留记录；无记录保留旧行为 | 记录关系存储的 `max_wal_size`；`0` 恢复 PostgreSQL 默认值，否则至少 64 MiB。 |

#### dcctl 只允许向前升级 {#v150-dcctl}

不支持降级。Upgrade 和重新运行 bootstrap 会拒绝比实例声明版本更早的版本，install 拒绝比上次集群安装器更旧的工具，且没有绕过选项。升级失败时，用新版本工具重试、向前升级到修复版本，或从备份恢复。发布镜像仓库的别名现在与标准写法采用相同的版本前缀及固定 broker digest 处理。

`--mqtt-device-uplinks` 记录 `spec.mqttDeviceUplinks`，后续不传选项的重跑和升级会保留它。如果省略该选项，新提供的 values overlay 与已记录选择冲突时会在写入前拒绝；允许继承的旧 overlay，且已记录选择优先。旧 CRD 若裁剪该字段，会在应用前拒绝。

`dcctl install --relational-store-max-wal-mib` 在应用前持久记录关系存储 WAL 目标，应用失败后重试也会保留。一旦记录，它优先于 `TF_VAR_postgres_max_wal_size_mib`；省略会保留。这修复了以前每次安装都要重复传入设置的问题。它不会从 `--size` 推断，实例运行期间也可修改。

HA 性能规格 bootstrap（包括 dry run）现在会在创建任何内容前检查：是否至少有三个可调度节点各能容纳一个 broker 的 CPU 和内存请求。不足时拒绝。选择较小规格、添加更大节点，或显式设置 `nats_cpu_request` / `nats_memory_request`（跳过相应资源的检查）。Upgrade 不受影响。`--lwm2m-identities` 现在会在配置前校验设备类型 token 语法。

#### 配置失败时拒绝运行 {#v150-configuration}

[升级前](#v150-before-you-upgrade)列出的无效值会在拒绝时指出键名，而不是悄悄采用默认值、关闭轮换、让时长溢出或在启动时崩溃。零和未设置仍保留文档规定的默认值。chart 模式检查文档所用的键名拼写；手写文档仍需启动校验。配置只能有一个完整文档，末尾不得有被忽略的内容。

#### GraphQL 输入限制 {#v150-input-limits}

- 仪表板名称搜索文本超过 256 字节时返回 `LIMIT_EXCEEDED`。
- 租户和管理平面的审计日志 `actor` 筛选文本超过 256 字节时返回 `LIMIT_EXCEEDED`。
- `calculatedMetricBackfills(statuses:)` 超过五项时返回 `LIMIT_EXCEEDED`；已接受列表中的重复项会被忽略。
- 设备和设备类型子串搜索的 `nameContains` 超过 256 个 UTF-8 字节时返回 `LIMIT_EXCEEDED`，长度在去除首尾空白前计算。
- 命令搜索的 `statuses` 或 `outcomes` 列表超过 1,000 项（含重复项）时返回 `LIMIT_EXCEEDED`。
- 事件页面和测量聚合的 `eventTypes` 超过 1,000 项（含重复项）时返回 `LIMIT_EXCEEDED`。
- `facetValues` 超过 1,000 个不同值时返回 `LIMIT_EXCEEDED`，不返回部分列表。
- 每个 GraphQL WebSocket 连接最多允许 1,000 个并发订阅操作。后续 `subscribe` 返回 `LIMIT_EXCEEDED`，连接保持打开。

#### 现有操作的更严格检查 {#v150-tightenings}

- **仪表板。** 每租户已有 10,000 个未删除仪表板时，`createDashboard` 返回 `DASHBOARD_LIMIT`。既有仪表板仍可使用，即使已经超限；删除不用的仪表板后可再创建。每个仪表板达到 1,000 个发布版本后，发布返回 `DASHBOARD_VERSION_LIMIT`；既有历史、已发布指针和草稿仍可使用。
- **仪表板草稿选择器。** 保存 `kind: "device"` 数据源要求 `deviceToken` 为字符串、`measurements` 为字符串数组，两个键都必须存在。格式错误的手写输入会在保存草稿时被拒绝；已存仪表板仍可读取、发布、回滚和渲染。
- **通知策略。** 每次提供的规则集最多接受 100 条规则（`LIMIT_EXCEEDED`）。既有更大策略继续运行；省略 `rules` 的更新不会改变它们。
- **命令活动。** 创建活动时拒绝末尾有额外内容的触发器 JSON，不再静默丢弃。

#### 自动化变更评审（preview） {#v150-automation-review}

配置文件的 **Change review** 选项卡会在记录的事故窗口和正常窗口中，将草稿规则变更与当前生效基线比较。比较引擎让两个版本在相同历史事件上运行，不产生副作用，比较告警转换和反应请求，包括内容。证据不完整或不受支持时返回 `INCONCLUSIVE`，不会通过。上限为 50 个设备、两个各不超过 24 小时的窗口、最多六小时的预热输入、十项预期和每窗口 2,000 项列出变更。Night Shift 模拟提供已记录场景：`dcctl sim create --episode night-shift` 和 `dcctl sim windows`。

评审保留输入摘要、stream 边界、预期和结果，可以分享，要求 `event:read`。审批需要近期第二因素验证；草稿作者被排除，自我审批遵循配置文件策略。发布和回滚现在也写入审计日志。

保护需要在已发布且符合条件的配置文件上主动启用，可以要求一或两次审批。受保护发布必须提供 `automationReviewToken`，只消费一次已批准评审，并检查冻结草稿、基线、证据、有效期、当前策略及审批者当前资格。缺少评审返回 `REVIEW_REQUIRED`；资格检查不可用返回 `REVIEW_UNAVAILABLE`。发布版本链接回评审和证据。未保护的配置文件沿用原发布路径。

受保护回滚、将设备类型改为指向或不再指向受保护配置文件，或将设备移出其绑定类型，都需要记录紧急变更理由并完成近期第二因素验证。回滚改变基线，使待用审批失效。受保护配置文件不能删除。PostgreSQL 保护触发器对旧版本 pod 同样强制这些条件：旧写入被拒绝，不能绕过保护。收紧立即生效；放宽需要另一人批准，或在允许自我审批时等待 24 小时。仅在部署完成后保护配置文件。

参见[自动化变更审查](../concepts/automation-change-review.md)和[审查规则变更](../guides/reviewing-a-rule-change.md)。

#### 代理操作（preview） {#v150-agent-actions}

MCP 新增 `propose_alarm_action`、`get_proposal` 和 `list_my_proposals`，仅在 MCP `agentActuation.enabled` 开启且调用方有相应 act scope 时注册。请求转发调用方自己的 bearer token。人员在控制台审批；MCP 没有审批或执行工具。`agentApprovalAccess` 允许符合条件的审批者无需 `agent:admin` 即可进入收件箱；停用开关仍要求 `agent:admin`。会话内 `stepUp(refreshToken, code)` 用验证器代码刷新 `mfa_time`；不可用或使用 SSO 时退回重新登录。

已禁用或正在清除的租户，其提案及执行决策返回 `OPERATOR_SUSPENDED`。禁用或清除也会发布停用消息；重新启用递增执行代次。Device-management 本地停用屏障将执行与已消费的停用消息串行化，消息流不可用或过期时返回 `FENCE_UNAVAILABLE`。告警效果进入事务发件箱，因此已提交效果会重试发布。策略或租户变更提交后，停用发布仍可能失败：重复该操作以重新发布。

授权数据库事实来自同一个读取快照；登录记录在快照前读取一次。执行在全部数据库工作后、提交前请求最终决策，期限为四秒，决策后不再运行 SQL。拒绝或迟到答复会回滚全部内容；请求决策后闲置会话被终止或连接丢失会映射为 `POLICY_UNAVAILABLE`。在决策快照之后提交的撤权仍可能被先行提交的操作越过，窗口为最多五秒加提交耗时。只有全部 device-management 和 user-management pod 升级后，此界限才成立。

**审批绑定审批者的登录会话。** 退出登录、重置密码或第二因素、或禁用该人员，都会结束这次审批；需从新登录重新批准。以前没有登录绑定的审批不再计数。控制台访问 token 现在携带 `sid`，控制台变更也会填充审计日志的会话列。完整部署后再启用代理操作。参见[提案与审批](../concepts/mcp.md#agent-proposals)。

#### LoRaWAN 与命令 {#v150-lorawan}

网络服务器仅排队的未确认下行仍以 `SUCCESSFUL` 结束，但不记录 `deliveredTime` 或 `deliveryEvidence: DEVICE_RESPONSE`：设备并未应答。确认下行的确认消息仍记录 `DEVICE_RESPONSE`。命令指南解释各结果由哪个组件结算。LoRaWAN 指南现在向设备发送确认命令，`dc-simulator` 可用 `--lorawan-downlink-listen` 和 `--lorawan-ack` 模拟 ChirpStack 下行队列和确认消息流。

#### 可观测性 {#v150-observability}

可选 OpenTelemetry 追踪贯穿摄取、NATS 发布和消费、device-management 解析、事件存储及 event-processing。通过 gRPC 或 HTTP 导出 OTLP。GraphQL span 不记录文档、变量或 token；摄取 span 不记录凭据或设备 token。在摄取边界，调用方的 `traceparent` 只链接到新的本地根 span，绝不采用为父上下文：远端 `tracestate` 被丢弃，远端采样标志被忽略。内部 span 继续平台上下文。关闭追踪时不安装 provider、不启动 goroutine 或 exporter，消息字节保持不变。失败事件发布和拆分重试会开启新 trace。

不规范的实例 id 会记录警告并将各区域的指标（例如 `devicechain_devicemanagement_instance_id_noncanonical`）设为 1，不拒绝启动；参见[升级前](#v150-before-you-upgrade)。

#### 保留并发修改 {#v150-concurrent-edits}

客户、通知渠道、设备配置文件草稿、资产类型、资产、实体组、维护窗口、指标定义、设备类型、检测规则草稿、命令定义、区域类型、客户类型、关系类型、凭据、设备和地理围栏的部分更新仅写入点名字段。省略字段不会再覆盖并发修改；空更新不写任何内容。过期的区域重命名不再恢复并发删除的边界围栏链接。若凭据编辑会覆盖并发密码轮换，则返回重试错误。

#### 其他变更 {#v150-other-changes}

- **停止与交接。** Event-sources 刷出未解码上行的待写计数，并等待正在写入的防抖断开消息。LwM2M 等待已删除租户扫描完成后才交接领导权。Messaging 断开前完成后续缓存驱逐。治理刷新会被取消并等待结束。通知停止在排空预算内取消卡住的发送，避免重发已送达通知。Command-delivery 在领取命令前取消调度提醒，命令保留排队。集成认证等待者在调用方取消时停止等待；共享查询有独立的十秒期限。
- **Operator 领导选举。** Operator 接受 `--leader-elect`，随附清单已启用。
- **分析访问。** 协调覆盖直接和嵌套组成员，不依赖 reader 名称前缀。名称冲突的超级用户被跳过、记录并计数；与服务登录角色或模式所有者冲突时拒绝启动。其他角色拥有的视图会在修复或授权变更前诊断。
- **控制台与组件。** 遥测列标题、告警表和命令表单及历史遵循查看者语言。告警表滚动区域可通过键盘操作。个人地理围栏底图 URL 和署名一起应用；不完整的配对会保留原地图。分面声明将改变既有轴值类型时，控制台会先警告。
- **既有客户端和编辑。** 过期的连接器更新返回 `STALE_WRITE`；控制台保留未保存修改并提示重新加载和重试。旧 `dcsa_` OAuth 客户端仍是可维护的普通客户端，绝不作为服务账户；删除前先注册替代客户端。
- **运行修复。** 网关前一次停止超时后仍在运行时，不能重新启动。快照重置同时比较序列号和水位，保留继任者的新检查点。被拒绝的 `CONNECTED` 声明不再推进最后活动时间。通知混合交付结果计为部分完成而非完成，硬失败优先级保留。载荷编码器精度拒绝会指出参数名。仅对端自身明确的未知字段拒绝才识别为围栏归档版本差异。计数查询失败的列表会返回该错误，而不是带有虚构总数的页面。
- **Chart 与文档。** 候选 chart 在 Artifact Hub 标为预发布。所属区域运行时计数磁贴显示零，积压包括处理中消息。CRL 指南澄清：流式上传超过 2 MiB 必须提高 `deviceManagement.callout.revocation.maxCrlBytes`（最多 32 MiB）。解码器参考将 `base64.decode` 输入文本上限更正为 5467 字节，包括换行；运行行为不变。

#### 模式变更与回退 {#v150-going-back}

恰好四项迁移，均为追加式增量变更。新表初始为空，既有数据保留。

- **代理停用屏障与效果发件箱**（`device-management`，`20261110000100`）：添加 `agent_actuation_fences` 和 `agent_effect_outboxes`。
- **自动化评审**（`device-management`，`20261111150000`）：添加 `automation_reviews`。
- **自动化评审强制执行**（`device-management`，`20261126120000`）：为保护策略、待定策略变更和紧急变更记录添加三张表，添加一个可空列 `device_profile_versions.automation_review_token`、一个函数和六个 `BEFORE` 行触发器。迁移不保护任何配置文件；启用保护后，约束也适用于旧版本 pod。
- **执行代次**（`user-management`，`20261110130000`）：添加 `iam_agent_policies.actuation_generation`。

**升级只能向前；不能降级到 `v1.4.0`；`dcctl` 现在会拒绝。** 用 `v1.5.0` 工具重试失败升级、向前升级到修复版本，或从备份恢复。参见[版本与升级策略](./versioning-policy.md)。

#### 已知问题 {#v150-known-issues}

- **确认 LoRaWAN 下行未收到网络服务器报告时，在截止时以 `TIMEOUT` 结束，** 交付情况未知。升级前记录的未确认命令保留旧证据。
- **在清除被标记前已领取的升级通知仍可能在清除后发送；** 清除被领取后才记录的目标不会收到清除通知。
- **仅告警 ACK/CLEAR 提案会执行。** 其他操作即使批准也不执行。停用开关不会撤销已执行操作。
- **同一开放登录会话内移除再授予审批者角色，会恢复该会话的审批。** 会话绑定无法检测中间的角色变更。
- **已满 24 小时的评审保护放宽在下一次受保护操作时应用，** 不由后台定时器应用。
- **评审保护资格使用保守文本搜索，** 不解析规则；看起来像被排除的动态依赖项的标识符或键可能阻止启用。
- **代理效果 relay 至少交付一次。** Broker 去重覆盖 120 秒；告警事件可能乱序，失败 100 次的行会保留以供操作员处理。

## 较早版本 {#older-releases}

新增次版本发布线时，最早一条完整记录的发布线会以简短条目移到此处。每个条目链接到该版本文档中的完整说明（保持其发布时的内容），以及其 GitHub 发布页面。所有版本都列在 [GitHub Releases](https://github.com/devicechain-io/devicechain/releases) 上。

### v1.4.0 — LoRaWAN 确认下行、通过 MQTT 发送设备自有载荷，以及标明设备的通知 {#v140-upgrade}

完整说明：[v1.4 文档](/1.4/deployment/releases-and-upgrades#v140-upgrade)。GitHub 发布：[v1.4.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.4.0)。

### v1.3.0 — 命令可送达 HTTP 和 LoRaWAN 设备，投递留下凭据 {#v130-upgrade}

<a id="v130-token-audience"></a>

完整说明：[v1.3 文档](/1.3/deployment/releases-and-upgrades#v130-upgrade)。GitHub 发布：[v1.3.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.3.0)。

### v1.2.0 — 租户自行编写的载荷解码器，以及会结束会话的证书吊销 {#v120-upgrade}

完整说明：[v1.2 文档](/1.2/deployment/releases-and-upgrades#v120-upgrade)。GitHub 发布：[v1.2.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.2.0)。

### v1.1.0 — 摄取每个事件更省 CPU，集群外的设备可通过 MQTT 接入 {#v110-upgrade}

<a id="v110-ingest"></a>

完整说明：[v1.1 文档](/1.1/deployment/releases-and-upgrades#v110-upgrade)。GitHub 发布：[v1.1.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.1.0)。

### v1.0.0 — 首个稳定版本，数据库故障时事件被保留，Traefik 成为默认入口 {#v100-upgrade}

完整说明：[v1.0 文档](/1.0/deployment/releases-and-upgrades#v100-upgrade)。GitHub 发布：[v1.0.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.0.0)。

<a id="v0270-upgrade"></a><a id="v0260-upgrade"></a><a id="v0252-upgrade"></a><a id="v0251-upgrade"></a><a id="v0250-upgrade"></a><a id="v0240-upgrade"></a><a id="v0230-upgrade"></a><a id="v0220-upgrade"></a><a id="v0220-packages"></a><a id="v0211-upgrade"></a><a id="v0210-upgrade"></a><a id="v0200-upgrade"></a><a id="v0190-upgrade"></a><a id="v0180-upgrade"></a><a id="v0170-upgrade"></a><a id="v0160-upgrade"></a><a id="v0150-upgrade"></a><a id="v0120-upgrade"></a>

`v1.0.0` 之前的版本不在此列出。其说明见 [GitHub Releases](https://github.com/devicechain-io/devicechain/releases)，以及仓库中各版本的标签。

## 数据持久性 {#data-durability}

数据库层刻意与应用**生命周期独立**。两个数据库作为带销毁保护的独立基础设施提供，因此升级、重新安装或卸载*应用*从不触及数据库。这是常见情况，也是安全的。

:::caution 从基础设施配置移除数据库是另一种操作
保护规则只在数据库*位于*基础设施配置时保护它。一旦从配置中*移除*便不再保护：移除的资源不再受配置声明规则覆盖，移除计划会成功。数据库集群还拥有自己的卷，因此移除数据库会带走数据，而不会留下未挂接的卷。

不要通过编辑基础设施配置移除数据库来替换它。

升级在数据库迁移到 Operator 之前创建的实例，是会遇到此问题的情况，计划阶段会拒绝而不冒险。先导出两个数据库，再用 `--allow-legacy-db-removal` 重新运行 `dcctl install` 处理关系数据库，并带此参数 bootstrap 处理事件存储——此参数只是声明你已处理数据，不验证任何内容。对于本地实例，重建更简单，并刻意丢弃数据：销毁实例、重建集群、再 install 和 bootstrap，见 [v0.16.0 及更早构建的实例](#pre-declaration-recreate)。
:::

这是运行卷的持久性，不能替代生产基础设施提供的计划备份和时间点恢复。参见[部署与 Operator](./kubernetes-operator.md)，了解基础设施层与应用层如何分离。
