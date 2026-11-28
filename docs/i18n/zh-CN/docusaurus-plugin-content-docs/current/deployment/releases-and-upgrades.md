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

- **[v1.10](#v1100-upgrade)**: [v1.10.0](#v1100-upgrade)
- **[v1.9](#v190-upgrade)**: [v1.9.0](#v190-upgrade)
- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)

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

### v1.10.0 — 设备可接受批量变更，指标可携带文本，安装可以引导完成 {#v1100-upgrade}

`v1.10.0` 是从 `v1.9.x` 就地升级：先为集群执行 `dcctl install`，再为每个实例执行一次 `dcctl upgrade`，详见[滚动升级](#rolling-upgrades)。两条命令都要使用 **v1.10.0 的 `dcctl`**，并配合 OpenTofu 或 Terraform 1.10 及以上版本；除 `--version` 外，还要向 `dcctl install` 提供集群的设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`。本版本追加九个迁移，在各服务首次启动时运行：`device-management` 六个，`device-state` 一个，`event-management` 一个，`update-management` 一个。每个迁移在失败后都可以重新运行。它们创建表、索引、两个函数和一个触发器，并添加可为空的列。本版本新增的配置键均为可选，除非你设置，否则不会出现，因此升级滚动期间，`v1.9.x` 的 Pod 可与 `v1.10.0` 的 Pod 并存运行。升级只能向前：无法降级回 `v1.9.x`。`dcctl upgrade` 会拒绝跳过次版本。如果你来自 `v1.8.x` 或更早版本，请先逐个升级中间的次版本，每个版本都使用其自己的 `dcctl` 和 `--version`，并遵循各自的说明（参见[受支持的升级路径](./versioning-policy.md#upgrade-paths)）。对于 `v1.9.0`，请见 [`v1.9.0` 说明](#v190-upgrade)。

```bash
dcctl install local --version v1.10.0
dcctl upgrade local devicechain --version v1.10.0 --dry-run
dcctl upgrade local devicechain --version v1.10.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本新增受保障的接口，且不移除任何接口。它弃用了 `fleetOperationReadiness` 及其 `FleetOperationReadiness` 和 `FleetOperationEnforcement` 类型、`dcctl fleet-op readiness`、通知策略的 `deviceTypeToken` 字段，以及嵌入在备份 `endpointUrl` 中的凭据。这些都会继续可用，直到弃用期结束后，在之后的某个主版本中被拒绝。由规则发送的命令现在计入租户的出站速率，回放也按进程设有上限；请在[升级到 v1.10.0 之前](#v1100-before-upgrade)中检查这两项。Helm values 和实例配置不在受保障的兼容范围内。

亮点：设备批量变更可在最多 10,000 台设备上设置一个已声明的共享配置键，并且只有设备自己上报后才算完成（预览功能）。指标可以声明为 `STRING`，其文本会显示在设备页面、仪表板中并提供给 AI 代理，但绝不会作为数字绘图。`dcctl install` 和 `dcctl bootstrap` 提供可选的终端界面，在安装任何内容之前需要你批准审核，`dcctl setup local`（预览功能）会继续创建第一个实例。`dcctl install --output json` 以事件流的形式报告安装过程，dcctl 会隐去写在 URL 中的凭据。`Instance` 资源现在也以 `core.devicechain.io/v1` 提供（预览功能），大于 64 MiB 的更新制品可以存储在 S3 上，较早的小时汇总会在后台补全。

新增内容：

- [升级到 v1.10.0 之前](#v1100-before-upgrade)
- [v1.10.0 的新增内容](#v1100-whats-new)

#### 升级到 v1.10.0 前 {#v1100-before-upgrade}

- **数据库现在会拒绝绕过写入协议的设备属性写入。** device-management 数据库的属性表上带有一个触发器：任何对设备 `SHARED` 属性的写入，只要不是在已将 `devicechain.attr_protocol` 设为 `1` 的事务内完成，就会被以 SQLSTATE `DCAP1` 拒绝。所有 v1.9 的 Pod 本来就这样做，因此从 v1.9 正常升级不受影响。若跳过了次要版本（从 v1.8 或更早版本直接升级到本版本，这是不受支持的），尚未升级的 Pod 在滚动更新完成之前，其设备 `SHARED` 属性写入会以 `DCAP1` 失败，所有 Pod 都运行本版本后即恢复正常；期间不会写入任何错误数据。其他属性作用域和其他所有者（资产、区域、客户）不受影响。对设备共享属性手工执行的 SQL 必须改为通过 API，或在自己的事务中用 `SELECT set_config('devicechain.attr_protocol', '1', true)` 设置该标记。升级后的实例上，`bulkChangeReadiness`（以及已弃用的 `fleetOperationReadiness`）现在会报告 `enforcement: ENFORCED`。
- **备份端点中嵌入凭据的用法已弃用。** `dcctl install` 仍接受包含用户信息、查询字符串或片段的 `endpointUrl`，但现在会发出警告，且不会显示凭据。这些值将在弃用期结束后，在之后的某个主版本中被拒绝。请使用不含凭据的 `endpointUrl`，并在 `--backup-credentials-file` 中使用 `accessKeyId` 和 `secretAccessKey` 字段。端点仍会原样存储在 `dc-infra/dc-install` ConfigMap 中，因为后续的 bootstrap 和 destroy 操作需要它。
- **设备批量变更（Preview）。** 从 v1.10 起，设备“群操作”称为“批量变更”。请使用 GraphQL `bulkChangeReadiness`（`BulkChangeReadiness`、`BulkChangeEnforcement`）和 `dcctl bulk-change readiness`。已弃用的 `fleetOperationReadiness` 字段、其 `FleetOperationReadiness` / `FleetOperationEnforcement` 类型以及 `dcctl fleet-op readiness` 仍可使用，包括 v1.9 客户端。旧命令会向 stderr 输出一行弃用提示。`fleet:operate` 权限保持不变。预览期限仍从 v1.9 开始。
- **批量变更只在已升级的 Pod 上应用行。** device-management 从 v1.9 滚动升级到 v1.10 期间，仍运行 v1.9 的 Pod 会把批量变更调用视为未知字段，`dcctl bulk-change` 会报告 **still upgrading; retry**。整个过程中，设备仍接受来自两个版本的普通属性写入。无需任何操作：滚动升级完成后，当强制执行状态为 `ENFORCED` 且租户没有孤立属性行时，`dcctl bulk-change readiness` 会列出 `config.set`。
- **请轮换您曾在传给 dcctl 的 URL 中携带的凭据，并以新的方式读取两个 OpenTofu 输出。** 如果您曾向 dcctl 提供带有用户名、密码或令牌的恢复源、镜像仓库、备份端点、对象存储端点或 broker URL，请将之前运行的日志和 CI 输出视为已记录了该凭据，并轮换它。另外，OpenTofu 根模块的 `backup_endpoint_url` 和 `database_restored_from` 输出以及计划中的恢复源现在属于敏感值，显示为 `(sensitive value)`。dcctl 的读取方式不变；直接读取 `tofu output` 的脚本需改用 `tofu output -raw <名称>` 或 `tofu output -json`。
- **`Instance` 资源现在也以 `core.devicechain.io/v1`（预览版）提供服务。** `dcctl install` 应用的 CRD 在 `v1beta1` 之外同时提供 `v1`，两者的 schema 完全相同，存储形式仍为 `v1beta1`，因此不会重写任何对象，也无需转换。`dcctl` 和 operator 继续读写 `v1beta1`，现有清单照常应用。发生变化的是资源发现：Kubernetes 优先选择较新的版本，因此不带版本限定的 `kubectl get dci`，以及任何向 API 服务器请求首选版本的通用客户端，现在使用的是 `v1`。输出中会打印 `apiVersion` 的命令（例如 `kubectl get dci -o yaml`）将显示 `core.devicechain.io/v1`。只有 `Instance` 新增了 `v1`：该组的其他类型（`InstanceOperation`、`ExecutorEnrollment`、`InstanceConfiguration`）仍然只以 `v1beta1` 提供，因此把该组的首选版本套用到组内所有类型的工具必须按类型分别查询。如果脚本依赖旧版本，请使用 `kubectl get instances.v1beta1.core.devicechain.io` 固定版本。只要两个版本同时提供服务，它们在结构上就保持一致：新增字段要么同时加入两者，要么都不加。
- **Instance 与执行器兼容性策略。** 版本策略现承诺从 v2.0 起将 `core.devicechain.io/v1 Instance` 纳入保障；v1 在 v1.10 中为预览。Instance `v1beta1` 在整个 v2.x 系列继续提供服务。v2.0 不会移除 `spec.grafanaSSO`、`spec.compact` 或 `InstanceConfiguration`。`ExecutorEnrollment` 和 `InstanceOperation` 在 v2.0 及之前保持 v1beta1；它们从预览提升的触发条件是在已发布版本中启用操作执行，并且拒绝不安全实例操作的检查已发布、已具备明确的提供方权限，而非通常的两个次版本期限。存储迁移和可执行操作尚未发布。参见[版本与升级策略](./versioning-policy.md#instance-api-compatibility)。
- **通知策略的 `deviceTypeToken` 自 v1.10 起弃用。** 设备类型作用域从未生效。该字段在创建输入、策略输出和搜索条件中继续保留，并完全保持原有行为，直到弃用期结束后在之后的某个主版本中被拒绝：非空白值写入被拒绝，已接受的策略读取时该字段为 null，筛选器只要提供了值，就不会匹配任何通过 API 创建的策略。租户级策略请省略该字段和筛选器。计划支持按锚点或组限定作用域，但不承诺发布版本。
- **重放现在默认设有上限。** 每个 event-processing 进程同时最多运行 8 个规则预览、版本比较和 `dcctl test` 评估（`preview.maxConcurrent`），无需任何设置。因此繁忙的实例在升级后可能出现此前没有的 BUSY 拒绝或降级的预览结果；作者重试即可。如果租户需要更多，请在所有 Pod 都运行此版本后提高 `preview.maxConcurrent`。
- **预览提升条件。** 预览规则现按接口分别规定提升条件，默认是进入预览后经过两个次版本。规则测试和产品即代码从 v1.10 起受兼容性承诺保障。代理操作和自动化审查从 v2.0 起受兼容性承诺保障，届时审批将采用通用审批模型。新列出的接口包括 command-delivery 的提议操作（含 `proposeCommand`）；MCP 的 `propose_alarm_action`、`propose_command`、`get_proposal`、`list_my_proposals`；device-management 的自动化审查操作；以及 event-processing 中仅供服务调用的 `compareRuleVersions`。升级前请阅读版本策略中各接口的条件。
- **规则发送的命令现在计入租户的出站速率。** `sendCommand` 动作现在获得与连接器调用（`httpCall` 和 `publish`）大小相同的独立预算，沿用相同的出站设置和租户自定义上限：除非租户有自己的自定义上限，否则为每秒 100 个动作、突发 200。若某租户的规则发送的命令超过该上限，超出部分将被拒绝，而上一版本会照常发送。升级前，请仅将各租户的 sendCommand 峰值与其上限比较。筛选为 `sendCommand` 的 `react_actions_dispatched_total` 可供参考。该指标没有租户标签，因此是任一租户速率的上界。若某租户可能超过其上限，请提高该租户的自定义上限或平台默认值（检测引擎设置中的 `outboundCallsPerSecond` 和 `outboundBurst`，与 outbound-connectors 的值保持一致）。此外，同一规则一分钟内最多向同一设备发送 120 条命令，以切断通过所控制的设备不断重新触发自身的规则；该限制是固定的。该循环防护在一分钟内每个租户最多跟踪 16,384 个规则与设备组合，每个副本上所有租户合计最多 65,536 个。会使组合数超出任一上限的命令将以 `command_guard_full` 被拒绝，且某个租户用尽其份额不会导致其他租户的命令被拒绝。两项限制都由每个副本保存在内存中，重启或故障转移后重新开始。检测引擎在重启后根据重启前已收到的事件重新推导出的命令仍会发送，在下游只生效一次，且不占用任何一项额度。检测引擎未运行期间到达的事件属于新事件，其命令计入两项限制。被拒绝的命令不会发送。它被记录为原因为 `shed`、详细信息为 `command_rate_limited`、`command_loop` 或 `command_guard_full` 的死信（见 `dcctl dead-letters`），并计入 `result="unwanted"` 的 `react_command_refused_total`。因租户上限尚未解析而按平台默认值放行的命令，按 `cause` 在 `react_command_governance_unresolved_admissions_total` 中单独计数。超出死信预算的拒绝会被计数，并按租户每分钟汇总为一条死信。
- **已存在的 `STRING` 指标定义会在升级后开始存储读数。** 以前声明 `STRING` 会被拒绝，因此这样的定义只可能通过其他方式创建；升级完成后，它的读数会作为文本存储，而不再被丢弃。
- **滚动升级期间**，上一版本的 Pod 对 `STRING` 指标的处理不同，因此请在升级完成之后再声明 `STRING` 指标。尚未升级的 device-management Pod 会把已声明的 `STRING` 值作为 `declared_not_storable` 丢弃；如果一条消息在新旧 device-management Pod 之间被重新投递，其数值读数可能被存储两次，使该消息在小时汇总中的总和与计数翻倍。尚未升级的 device-state Pod 会把看起来像数字的 `STRING` 值（`"12"`）存为数字 12，并让之前的文本留在旁边，直到更新的读数替换该行。尚未升级的 event-processing Pod 会让规则把这样的值当作数字读取。

#### v1.10.0 新增内容 {#v1100-whats-new}

- **分析视图在滚动升级中保持不变。** 旧版本的 event-management Pod 如果发现分析（SQL/BI）视图已处于更新的代次，会保持原样而不重建，并以 info 级别记录这次跳过，计入 `devicechain_eventmanagement_analytics_reconcile_newer_surface_skips_total`。
- **审批收件箱中的命令提案会显示其原因。** 在 Commands 选项卡中，每一行现在会显示该提案的前两条原因，原因多于两条时附带“另有 N 条”的计数；详情页仍列出全部原因。告警行保持不变。
- **批量变更能够可靠地完成、恢复并报告错误。** 所有行都已应用的批量变更现在总会到达 `DISPATCHED`，即使最后几行使其达到失败或不明确上限、且应用这些行的那一轮被中断；此前它可能被永久暂停。恢复后的批量变更现在总会先获得属于自己的 24 小时，才会以 `DEPENDENCY_UNAVAILABLE` 暂停，因此暂停之前遗留的等待不会再让它立即再次暂停，已生效的恢复也会报告为已完成。`lastErrorCode` 现在按文档所述报告任一行遇到的最后一个可重试错误。`bulkChangeLines` 新增 `afterOrdinal` 条件，`dcctl bulk-change lines` 按其分页，因此在运行中的批量变更上按 `--posting` 或 `--settlement` 过滤不会再漏掉行。`bulkChangeLines` 的 `posting` 和 `settlement` 过滤条件以及 `bulkChanges` 的 `dispatch` 和 `evidence` 过滤条件，现在会以 `INVALID_REQUEST` 拒绝不属于批量变更状态的值（例如小写的 `ambiguous`），而不是返回空结果。
- **设备批量变更以 Preview 形式提供。** 一次批量变更在最多 10,000 台设备上设置或移除一个已声明的 SHARED 配置键（`config.set`）：先规划、再创建、审阅其契约、启动，然后跟踪每台设备的行，直到设备本身对其作出回应。device-management GraphQL API 提供 `planBulkChange`、`createBulkChange`、`startBulkChange`、`pauseBulkChange`、`resumeBulkChange`、`abortBulkChange`、`reverseBulkChange`、`resolveAmbiguousBulkChangeLines`、`previewBulkChangeResume`、`previewBulkChangeReversal`、`bulkChange`、`bulkChanges` 和 `bulkChangeLines`，`dcctl bulk-change` 新增 `plan`、`create`、`start`、`status`、`lines`、`pause`、`resume`、`abort`、`reverse` 和 `resolve`。只有设备自己的报告才能使一行被确认；截止时间前没有报告作出判定的行会成为不明确状态，用户可在近期完成第二因素验证后为其记录裁定。创建、启动、恢复、撤销和裁定需要 `fleet:operate` 权限，任何内置角色都不授予该权限（拥有全部权限 `*` 的角色具备该权限；否则请在自定义角色中授予）；暂停和中止只需 `device:write`。仍有孤立的设备属性行时，启动或恢复会以 `BULK_CHANGES_NOT_ACTIVATED` 被拒绝；无法完成对这些行的检查时，会以可重试的 `ACTIVATION_CHECK_UNAVAILABLE` 被拒绝。代理和服务身份以及具有受限范围的成员会被拒绝。以上全部为 Preview，可能在次版本中变更。参见[设备批量变更](../guides/bulk-changes.md)。
- **批量变更的告警和指标。** 启用 `metrics.alerts` 时，chart 会新增一个 `bulk-changes` PrometheusRule，包含七条告警——`BulkChangesNotAdvancing`、`BulkChangesStalled`、`BulkChangesDriverSilent`、`BulkChangesAuthorityUnavailable`、`BulkChangesSettlementLagging`、`BulkChangesFencePending` 和 `BulkChangesNotActivated`——每条在告警运行手册中都有对应条目。这些告警读取 `devicechain_devicemanagement_bulk_change_*` 序列，这些序列不带租户、操作或设备标签。仍在 v1.9 上的 device-management Pod 不导出这些序列，因此在 device-management 滚动升级期间，这些告警只读取已升级的 Pod；无需任何操作。 `BulkChangesSettlementLagging` 还会读取 `devicechain_devicemanagement_bulk_change_reconciler_lag_seconds` 指标；每个 Pod 每分钟测量一次，Pod 自身没有成功测量时该指标为 `NaN`，当没有任何 Pod 能够测量时，该告警会触发。
- **`/dash` 查看器应用现在可以对照其实例所提供的 schema 检查自己的请求。** 与控制台一样，查看器会通过带认证的 `OPTIONS` 请求向每个 GraphQL 端点获取其所提供的 schema，并在发送之前拒绝实例无法处理的请求，错误信息会说明缺少什么。它仅在查看器可能与其实例版本不同的场景下启用：使用 `vite dev` 时，或使用 `VITE_SCHEMA_NEGOTIATION=on` 构建时（适用于与所连接实例分开托管的查看器）。由其自身实例提供服务的查看器保持关闭，因此应答 `OPTIONS` 的代理不会使其失效；设置 `VITE_SCHEMA_NEGOTIATION=off` 可在开发模式下也将其关闭。登录、令牌刷新和退出登录不受检查。早于 v1.9.0 的实例不提供该发现功能，因此开启协商的查看器会拒绝向其发送请求，并给出提及 v1.9.0 的错误。如果 CORS 代理或 Web 应用防火墙自行应答 `OPTIONS`，发现将失败并拒绝所有请求，因此请让 `OPTIONS` 到达 GraphQL 端点。
- **仪表板视口预览。** 以 390、768、960、1366、1920 或 3840 像素宽度预览布局，并查看该宽度下的布局问题。返回“容器”可继续编辑；预览选项不会更改已保存的仪表板或实时设备绑定。
- **指向命令的链接会直接定位到该命令。** 形如 `/devices/<token>?tab=commands#command-<token>` 的设备链接（例如审批收件箱生成的链接）现在会滚动到该命令所在行并短暂高亮，无论它位于设备历史的哪一页。不在已加载页面中的命令会按令牌单独读取，并固定显示在历史上方，且不提供 Cancel 按钮。已不存在、属于其他设备或无法读取的命令会显示一条提示。
- **Docker CPU 诊断。** 当无法读取 Docker 的 CPU 数量时，`dcctl preflight` 和安装前检查现在会显示明确的 `UNKNOWN` 行，并提示检查 `docker info`。对于 Compact 和 Standard 安装，该结果仍仅作提示，不会阻止安装，也不会再报告所有检查均已通过。
- **海滨 3D 场景现在绘制其场景中的全部设备。** `demos/harborpulse-unity` 现在显示 `harborpulse` 模拟器在街区之外预置的六台设备：两台使用设备令牌通过 MQTT 连接的充电桩、两台使用客户端证书连接的充电桩，以及两盏通过 HTTP 拉取命令的路灯。充电桩接在北码头的充电位之后，路灯立在岸壁旁的登岸处。每一类外观各不相同，并且只显示它上报的内容：充电桩在上报期间指示灯亮起，沉默后变为紫色；拉取式路灯的灯杆用四个光点显示它上报的调光级别。开场字幕现在统计 43 台设备。场景的布局文件随之改变，因此请使用同一版本的场景和模拟器：在此版本之前构建的场景遇到更新的模拟器时会关闭其导览。实时或预览播放器还接受 `-hp-eye x/y/z/lookX/lookY/lookZ/fov`，在整个运行期间把镜头固定在一处。
- **海滨 3D 场景的导览现在会停在六台自行保持会话的设备前。** `demos/harborpulse-unity` 的实时一天在北码头增加了一页，在 12:00 断开 MQTT 会话的充电桩于 13:00 重新连接时显示。它说明：平台为断开的会话发出了告警并自行清除；调光命令一直等到两盏自行拉取命令的路灯来取，两盏灯现在都上报了新的级别；两台充电桩使用各自的证书连接，并以配置的方式收到了更低的功率上限；在连接器过热那一页之后，还说明有一张证书被禁用，只有那一台充电桩被拒之门外。前两条只有在平台自己的告警和读数能够证明时才会显示。录制的 90 秒和五分钟剪辑没有变化。
- **历史小时汇总。** 事件管理现在会在后台从新到旧补齐历史小时汇总，每轮最多处理四个小时的数据，工作时间预算为 30 秒。每个小时完成前，查询继续使用分钟汇总。该任务复用现有历史回填锁和连接池空闲连接要求。运维人员可以通过 `devicechain_eventmanagement_rollup_hour_backfill_complete_from_timestamp_seconds`、`devicechain_eventmanagement_rollup_hour_backfill_remaining_windows` 和 `devicechain_eventmanagement_rollup_hour_backfill_windows_total` 指标以及标准周期任务指标查看进度。原始数据块过期后，小时回填仍会保留分钟汇总。与刷新策略发生锁冲突时将在下一轮重试。
- **`dcctl install --output json`。** `dcctl install` 新增 `-o, --output` 标志。`--output json` 以面向自动化的事件流报告安装过程：在 stdout 上每行一个 JSON 对象，且 stdout 上没有其他内容；内容涵盖每个阶段的开始、进展、完成或失败，预检报告，以日志行形式呈现的安装自身输出，以及带有结论的最后一行总结。事件流从不读取 stdin：`--yes` 提供安装唯一会提出的确认；没有它时，需要该确认的运行会在做出任何改动之前被拒绝。机密值从不写入事件流。退出状态和 stderr 上的失败信息保持不变。默认的 `--output text` 打印的内容与以前完全相同。事件说明见[安装事件流参考](../reference/install-events.md)。
- **`dcctl install --output json` 在边界情况下也保持其保证。** 退出状态始终与事件流的最终结论一致，为 `0` 或 `1`，即使安装所运行的工具被杀死或 dcctl 自身意外失败也是如此，此时事件流同样以总结行结束；文本输出也重新只以 `0` 或 `1` 退出。`--help` 与 `--output json` 一起使用时，帮助打印在 stderr 上，stdout 保持为空。安装所运行的任何东西都无法读取 stdin，因此 kubeconfig 的 exec 凭据插件以非交互方式运行。dcctl 无法解析的 kubeconfig 现在只按其路径报告，不再附带解析器给出的原因（该原因可能引用文件中的令牌和密码），两种输出格式都是如此；kubeconfig 中的凭据也会从事件流的日志行中移除。使用 `--output json` 时，打印在 stderr 上的错误以及运行期间到达 dcctl 自身 stdout 和 stderr 的其他所有内容（包括 kubeconfig 的 exec 凭据插件的 stderr）也会被替换；使用 `--output text` 时，dcctl 打印的错误会被替换。无法读取或无法构建客户端的 kubeconfig 按问题类型、文件和上下文名称报告。kubeconfig 中的凭据只作为完整单词被替换，绝不会改变运行的行为；过短而无法替换的值（不足 4 字节）会给出警告，而不是被默默放过。[安装事件流参考](../reference/install-events.md)现在列出每个事件的全部字段，并说明进度如何合并，以及被中断的 OpenTofu 命令何时可能比运行存在得更久。
- **JSON 模式可在限定时间内结束。** 当原始 stderr 的使用方停止读取时，`dcctl install --output json` 不再因排空进程输出而挂起。读取器已收到的诊断信息会保留至排空期限；超过期限后，尚未输出的诊断信息可能被丢弃。
- **dcctl 不再打印您提供的 URL 中嵌入的凭据。** 恢复源（`--restore-rdb-from`、
  `--restore-tsdb-from`）、镜像仓库（`--registry`）、备份端点、对象存储端点和 broker URL
  （`--nats-url`）以前会被完整打印，因此写在其中的用户名、密码、签名查询或片段令牌会出现在终端、
  `--output json` 事件流和终端界面中。现在，dcctl 在显示此类值时，会将用户信息、查询和片段替换为
  `REDACTED`（`s3://user:pass@bucket/archive` 显示为 `s3://REDACTED@bucket/archive`），即使它们经过
  百分号编码或该值不是格式正确的 URL 也是如此。dcctl 运行的工具（OpenTofu、ko、docker）会回显这些值，
  其输出也以同样方式过滤。不含凭据的值的显示与以前完全相同。
- **预留的意图权限。** 新增的租户权限 `intent:read` 和 `intent:write` 为即将推出的功能（声明式意图）预留；目前尚无任何功能检查它们，因此在本版本中授予它们不会给角色带来任何访问权限。它们不会被添加到任何内置角色中，也不会加入每个成员默认获得的访问权限（持有 `*` 的租户管理员已满足这两项权限）。请仅在**升级的滚动部署全部完成后**才将其授予自定义角色：尚未升级的 user-management Pod 会拒绝权限集合中包含未知名称的任何角色编辑，即使该角色已持有其中之一，且此次编辑与其无关。
- **MCP 提议审计。** 审计格式版本 3 在工具调用日志中新增 `proposal` 和 `verdict`：Preview 提议工具记录已保存提议的令牌、平台裁决及允许记录的拒绝代码。读取操作的这两个字段为空，现有审计字段的含义保持不变。
- **规则预览、版本比较和 `dcctl test` 评估现在按 event-processing 进程设有上限。** 每个租户原本已限制为同时两个，但它们的总和没有任何限制，因此在已解析事件流未按租户分片的实例上，N 个繁忙租户可能同时运行 2N 次整条事件流的重放，拖慢所有人的检测。现在，一个 event-processing 进程在所有租户之间同时最多运行 8 个（`preview.maxConcurrent`，范围 1 到 256）；三者共用同一份额度，每个副本有各自的额度，因此 N 个副本最多同时运行 N × 该设置值。找不到空闲名额的请求会立即被拒绝并标记为繁忙，预览返回降级结果，比较或评估返回拒绝，绝不会作为空的成功结果返回，作者稍后重试即可。名额按公平方式分配：同一租户的第二个并发名额只在已用名额少于一半时授予，因此各自同时运行两个的租户无法把一个没有运行任何任务的租户挡在外面。该键默认不存在。仅在实例的所有 Pod 都运行了认识该键的版本之后再设置，因为较旧的 Pod 会拒绝包含未知键的配置文档。
- **规则编写来源。** 已发布的设备配置版本现在会记录发布者的稳定主体 ID，以及保存草稿规则时使用的入口集合：表单、画布、描述、产品即代码或 API。随后通过表单编辑不会抹去“描述”的 AI 辅助来源。现有记录显示为 UNKNOWN；原有发布者名称和画布图保持不变。客户端可选择指定保存入口；省略时记录为 API。
- **指标可以使用 `STRING` 数据类型。** 设备可以把状态或模式作为文本上报（例如 `"heating"`），前提是其配置文件中该指标已声明为 `STRING`。可在控制台的指标表单中、通过 API 或使用 `dcctl` 声明。该读数会与设备的历史一起存储，并作为其最近已知值保留（`latestMeasurements` 和事件查询中的 `textValue`），还会显示在控制台的读数列表中。它绝不会被求平均、求和或按数字绘图：对它做聚合会被拒绝，检测规则也不会把它当作数字，即使文本看起来像数字（`"12"`）。引用 `STRING` 指标的规则永远不会触发，控制台的规则编写和 AI 规则草拟也不会提供它。值最多 4,096 字节，必须是有效的 UTF-8 且不含 NUL 字节；过长或无效的值会被单独丢弃，计入 `declared_not_storable`，并像其他被丢弃的读数一样显示在设备页面上，消息的其余部分会保留。配置文件未声明的键所带的文本仍会被丢弃。`STRING` 指标不能带公式。测量值的分析视图暂未提供文本列。
- **设备的最新读数可以是文本。** `latestMeasurements` 所返回的最新读数投影在 `value` 旁新增了可为空的 `textValue` 字段：STRING 指标的读数把文本保存在其中，并将 `value` 置为 null，数值读数保持不变。设备页面会显示该文本。升级前存储的所有读数仍保留其 `value`，`textValue` 为 null。此变更为 device-state 存储新增一个可为空的列，不回填数据，也没有默认值。
- **仪表板样本可以携带文本读数。** `@devicechain/dashboards` 中的 `MeasurementSample` 新增可选的 `textValue`，实时测量流现在会选取它，因此 `STRING` 指标的读数会作为文本到达小组件，而不是一个无法解释的空值。数值读数以及早于 `STRING` 指标的服务器上不会有该字段。内置小组件仍然只绘制和聚合数字，并会跳过 `value` 为空的样本，因此文本读数绝不会被画成缺口或零。
- **`get_latest_measurements` 和 `query_measurements` 会返回 `STRING` 指标的文本。** 此前，助手读取设备最新值或历史时会把 `STRING` 指标看作空值。现在文本会作为 `text` 返回在结果的 `untrusted` 成员下，并标记为设备上报，因此助手会把它当作数据，而不是指令。数值读数保持不变，空文本则表现为没有文本。
- **规则编辑器会在规则引用 `STRING` 指标时给出警告。** 检测规则从不读取文本读数，因此引用了在其配置文件中声明为 `STRING` 的指标的规则永远不会触发。控制台的规则表单现在会在编写规则时指出这一点并给出指标名称；规则仍会被接受并保存。API 客户端也可使用此检查：`validateDetectionRules` 接受可选的 `metrics` 列表（每项包含 `key` 和 `dataType`，最多 1,000 项），并为规则引用的每个此类指标返回一条 `namesStringMetric` 警告。省略该列表则不会给出此类警告。未知的 `dataType`，或同一键以不同类型重复列出，会被拒绝。控制台仅在配置文件含有 `STRING` 指标时才发送该列表，因此在滚动升级期间，旧版 event-processing Pod 只是暂时不为此类配置文件提供行内检查，直到升级完成。
- **超过 64 MiB 的更新制品现在可以存储在 S3 对象存储上。** 上传到 S3 后端时，内容现在会分段流式写入存储，而不再缓冲在内存中，因此只适用于 S3 的 64 MiB 限制已经取消。在所有后端上，制品大小都由 `updates.maxArtifactBytes`（默认 512 MiB）限制；上传过程中仍会校验大小和摘要，被拒绝的上传不会留下任何残缺对象。将 `updates.maxArtifactBytes` 调高到 64 MiB 以上现在会在 S3 上生效，此前它会被悄悄限制在 64 MiB。`ARTIFACT_TOO_LARGE_FOR_BACKEND` 拒绝码不再返回。

#### 安装和设置的终端界面（可选） {#v1100-install-terminal-ui}

`dcctl install local --ui=tui` 在全屏终端界面中完成整个集群安装，新增的 `dcctl setup local`（Preview）则在此基础上继续创建第一个实例。两者都是可选的：不加 `--ui`，或使用 `--ui=plain`（这也是屏幕阅读器用户应使用的方式）时，`dcctl install` 的输出、提示和退出方式与以前完全相同。参见[安装终端界面](./install-terminal-ui.md)。

- **如何选用。** 只要 stdin、stdout 或 stderr 不是交互式终端、在 CI 中、`TERM=dumb` 或使用 `--dry-run`，`--ui=tui` 都会回退到纯文本输出。`--ui=auto` 只在交互式终端上选择终端界面，并且在使用 `--yes` 或 `--dev` 时从不选择；使用 `--yes` 或 `--dev` 时，`--ui=tui` 只显示进度，安装结束后自动退出。在支持的本地终端上，开头的徽标会显示最长 1.2 秒的动画，其他情况下显示静态徽标或一行标题；`--no-splash` 可跳过它。每个屏幕在设置 `NO_COLOR` 时以及在 16 色或仅 ASCII 的终端上都可读，并且可在 Windows 控制台（Windows Terminal、PowerShell 或 `cmd`）上运行，退出时会恢复控制台。
- **欢迎和预检。** 安装会先显示将要做什么（目标、规模、拓扑和镜像来源，并注明 "No changes have been made"），然后在屏幕上运行只读的预检。每项检查都以文字显示为 PASS、WARN、FAIL、UNKNOWN、NOT NEEDED 或 NOT CHECKED，选中的检查会显示其修复方法。修复问题后可用 Recheck 重新运行检查；规模可以在 Standard 和 Compact 之间切换，切换前会说明这会改变什么，切换后会针对新规模重新运行所有检查。只要有任何检查失败，Continue 就不可用，也没有绕过的办法；`--skip-preflight` 仍然是跳过检查的唯一方式。
- **集群选项。** 预检之后，安装会显示目标（可重命名的托管本地 kind 集群，或从 kubeconfig 中的名称里选择的现有上下文）、规模（Standard 或 Compact，并在下方说明 Compact 会省略哪些内容）以及拓扑（Single 或 HA），每一项都标明是由哪个命令行参数设置的。预检屏幕上也提供 Choose another context。Advanced 会打开一个面板，其中可编辑监控组件、内置的 CloudNativePG operator，以及在 Compact 下的 TLS，并按计划的实际结果显示是否进行数据库备份；命令行中给出的其他所有参数都会按名称列在其中，并原样带入安装，凭据只显示为 "configured (hidden)"。每项更改都按命令行的方式进行校验，因此命令行会拒绝的组合在屏幕上同样会被拒绝，并且会针对更改后的计划重新运行只读检查：在检查通过之前，Continue 始终不可用。
- **也会以建议的形式检查现有集群。** 使用 `--kube-context` 时，预检之后会增加一组 "Existing cluster (advisory)"：API 服务器的名称能否解析，它是否以该上下文信任的 TLS 证书应答，该上下文的凭据是否被接受，这些凭据是否可以创建安装所创建的资源（通过不会改变任何内容的访问审查来询问），以及本机能否访问安装所用的提供程序、chart 和镜像仓库。它们从不经过代理：当上下文或环境（`HTTPS_PROXY` 等）中配置了代理时，需要经过该代理的行显示为 NOT CHECKED。这些检查只做读取，不跟随重定向，连接超过 5 秒、应答超过 15 秒即放弃，并且从不阻止 Continue：发现的问题显示为 WARN，无法判断的显示为 UNKNOWN 或 NOT CHECKED，绝不显示为 PASS。在 Windows 上，被拒绝的连接显示为 WARN（"refused the connection"）。仓库检查跟随计划：计划不包含的组件（Compact 或 `--no-monitoring` 下的监控，`--no-cnpg` 下的 CloudNativePG operator，不带 TLS 的 Compact 下的 cert-manager）不会被检查，也不会产生警告；Ingress 控制器的 chart 跟随集群的 IngressClass：始终检查 Traefik 的，仅当集群运行 ingress-nginx 且安装会保留它时才检查 ingress-nginx 的。纯文本输出、`dcctl preflight` 以及安装自身的预检均保持不变。
- **凭据插件。** 通过 exec 插件获取凭据的上下文（GKE、EKS 和 AKS 的上下文通常如此）同样会检查身份验证和权限：这些建议性检查会自行运行该插件，从不以交互方式运行（没有终端，输入为空），最长 10 秒；时间用完或您离开该屏幕时，插件及其启动的所有进程都会被一并停止。插件输出的凭据只用于该上下文的 API 服务器，使用与其他凭据相同的只读、不跟随重定向、不经过代理的连接，过期后不再使用，并且从不显示。条目中写明 `interactiveMode: Always` 的插件不会运行（NOT CHECKED），未安装的插件显示为 WARN，运行失败或没有输出可用凭据的插件显示为 UNKNOWN；插件输出的错误信息从不显示，因为其中可能包含令牌或密钥。旧式的 `auth-provider` 条目从不运行。
- **未经您的批准不会安装任何内容。** 在集群选项中选择 Continue 会打开一个计划审核页面，按命令行的解析方式展示计划：目标及其是新建还是复用、版本和镜像仓库、规模和拓扑、TLS、监控、备份、各项警告，以及集群状态的保存位置。页面还会显示等效的非交互式 `dcctl install` 命令，它使用 `--yes` 重现同一计划；告警 webhook 等凭据只以占位符显示，此时该命令会被标记为模板。审核页面打开时焦点位于 Back，页面稳定之前按下的键会被忽略，因此多按一次 Enter 永远不会批准它。只有选择 Approve and install 后安装才会开始，该批准同时会回答是否创建本地 kind 集群的问题，且仅针对该集群。返回并更改任何内容都会生成新的计划：其检查会重新运行，并需要单独审核。在您批准之前，选择 Exit、按 Ctrl+C、收到中断信号或终端界面停止，命令都会结束且不做任何更改。
- **进度和结果。** 进度屏幕显示安装的各个阶段及其状态。第一次按 Ctrl+C 会安全地停止安装，界面会一直保留到安装停止；第二次按 Ctrl+C 会立即退出。安装失败时会指出失败的阶段、原因、可能已更改的内容、集群锁是否已释放、用于检查集群的命令，以及修复原因后重新运行的命令；不会回滚任何内容，dcctl 仍以 1 退出。安装成功时会显示接下来要运行的 `dcctl bootstrap … --ui=tui` 命令。
- **每个屏幕都提供帮助。** 在安装和 `dcctl setup local` 的每个屏幕上（包括进度屏幕），按 `?` 或 F1 都会打开帮助。帮助列出的按键与屏幕底部显示的完全相同，说明该屏幕的作用，在较小的终端中可以滚动，并且不会更改任何内容：它从不回答问题，从不暂停安装；在批准后会启动操作的屏幕上，关闭帮助后该屏幕会等键盘再次安静下来。在可以输入 `?` 的地方（名称或口令中），按 F1 打开帮助。
- **`dcctl setup local`（Preview）：先引导安装，再执行 bootstrap，共两次批准。** 一次终端会话即可准备集群并在其上创建第一个实例。集群阶段就是安装自身的屏幕和审核；集群安装完成后，交接屏幕会进入实例和根密钥托管的选项以及实例自己的审核。实例的任何部分都不会在没有其自身批准的情况下运行，在审核屏幕稳定之前输入的按键也不会批准它。托管口令在屏幕上询问，从不显示。如果在集群阶段之后退出或实例创建失败，集群仍保持已安装状态，setup 会打印在同样的屏幕上创建该实例的确切 `dcctl bootstrap … --ui=tui` 命令。它只能以引导方式使用：没有交互式终端时不做任何更改并以 2 退出，也没有 `--yes`。它的退出状态说明留下了什么：0 两者均完成，1 集群阶段失败，2 未做任何更改，3 集群已安装但未创建实例，4 集群已安装但实例创建失败。`dcctl install` 和 `dcctl bootstrap` 不受影响。参见[安装终端界面](./install-terminal-ui.md#setup-local)。

- **`dcctl bootstrap --ui=tui`：在终端界面中创建实例。** `dcctl bootstrap local <instance> --ui=tui` 在全屏终端界面中创建实例：实例和根密钥托管的选项按命令行预先填写；对确切的 bootstrap 进行审核并显示其等效命令，只有**批准并创建实例**才会开始运行；运行时显示 bootstrap 的各个阶段，托管口令在屏幕上询问且从不显示；随后显示托管回执、一次性的首次登录密码和实例摘要。屏幕不选择的每个标志，以及屏幕选择但保持不变的每个标志，都会按原样（包括空值）带入运行，位于实例状态目录之内的托管路径会在批准之前被拒绝。使用 `--yes` 或 `--dev` 时只显示进度。`dcctl bootstrap` 上的 `--ui` 与 `dcctl install` 上的取值、默认值（`plain`）和回退规则相同：不加该标志、不在交互式终端中、在 CI 中、`TERM=dumb` 以及使用 `--dry-run` 时，`dcctl bootstrap` 的输出、提示和退出方式与以前完全相同，托管口令仍依次来自 `--escrow-passphrase-file`、`DCCTL_ESCROW_PASSPHRASE` 和人工输入。在 `dcctl setup local` 中，集群的欢迎屏幕现在会说明接下来是实例。参见[安装终端界面](./install-terminal-ui.md#bootstrap-ui)。

### v1.9.0 — 更新按批次推出，工具会先检查实例所提供的能力 {#v190-upgrade}

`v1.9.0` 是从 `v1.8.x` 就地升级：先为集群执行 `dcctl install`，再为每个实例执行一次 `dcctl upgrade`，详见[滚动升级](#rolling-upgrades)。两条命令都要使用 **v1.9.0 的 `dcctl`**，并配合 OpenTofu 或 Terraform 1.10 及以上版本；除 `--version` 外，还要向 `dcctl install` 提供集群的设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`。本版本追加十三个迁移，在各服务首次启动时运行：`command-delivery` 三个，`device-management` 四个，`event-management` 四个，`update-management` 一个，`event-processing` 一个。每个迁移在失败后都可以重新运行。它们创建表、索引和一个汇总，并添加可以为空的列；本版本新增的配置键均为可选，除非你设置，否则不会出现，因此升级滚动期间，`v1.8.x` 的 Pod 可与 `v1.9.0` 的 Pod 并存运行。升级只能向前：无法降级回 `v1.8.x`。`dcctl upgrade` 现在会拒绝跳过次版本。如果你来自 `v1.7.x` 或更早版本，请先逐个升级中间的次版本（包括 `v1.8.x`），每个版本都使用其自己的 `dcctl` 和 `--version`，并遵循各自的说明（参见[受支持的升级路径](./versioning-policy.md#upgrade-paths)）。对于 `v1.8.0`，请见 [`v1.8.0` 说明](#v180-upgrade)。

```bash
dcctl install local --version v1.9.0
dcctl upgrade local devicechain --version v1.9.0 --dry-run
dcctl upgrade local devicechain --version v1.9.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本新增受保障的接口，且不移除任何接口。它弃用了 `InstanceConfiguration` 自定义资源，以及 `Instance` 声明的 `spec.grafanaSSO` 和 `spec.compact` 字段，这些在后续主版本之前仍可继续使用。GraphQL 库的更新会拒绝一些此前被接受的数字字面量；新增的拒绝情形和行为变化列在[升级到 v1.9.0 之前](#v190-before-upgrade)和 [v1.9.0 的新增内容](#v190-whats-new)中。Helm values 和实例配置不在受保障的兼容范围内。

亮点：OTA 推出可以按具名金丝雀批次和累计百分比逐步推进，并在失败率达到阈值时暂停，控制台可直接操作。`dcctl` 和控制台在发送请求之前会检查实例所提供的架构，每个版本还会发布其实例配置的 JSON Schema。代理可以通过 MCP 提出设备命令，并由人在控制台中批准（预览功能，默认关闭），自动化发送的命令会记录发送原因。开启故障关闭模式的 MQTT 时，`event-sources` 现在可以写入事件信封（可选，默认关闭）。分面值可以批量导入，控制台也支持从 CSV 文件导入，关系可以通过 `dcctl` 管理，长时间范围的图表读取一小时汇总。通过重新创建的设备的令牌读取时，不再返回已删除设备的事件；`device-management` 短暂不可用时，入站集成仍会继续接收上行数据；安装时会均衡代理的主节点分布。

新增内容：

- [升级到 v1.9.0 之前](#v190-before-upgrade)
- [v1.9.0 的新增内容](#v190-whats-new)

#### 升级到 v1.9.0 前 {#v190-before-upgrade}

- `dcctl bootstrap` 和 `dcctl upgrade` 现在会在操作前读取 InstanceOperation。运行这两个命令的身份需要对 `instanceoperations.core.devicechain.io` 拥有 `list` 权限；cluster-admin 已有此权限。资源定义不存在时，视为没有请求。
- **当另一个 dcctl 运行持有集群锁时，`dcctl upgrade` 现在会拒绝执行，而不是发出警告后继续。** 它在第一次写入之前获取锁，并在持有锁时再次检查集群的 Operator，因此 `dcctl install` 无法在其运行期间更换 Operator。每个集群只有一把锁，因此同一集群上*不同*实例的两次升级不能再同时运行：请依次执行。在 `dc-k8s-system` 中对 `leases.coordination.k8s.io` 没有 `create` 和 `delete` 权限的账户，现在会被拒绝而不是仅收到警告。被终止的运行会留下锁：请用 `dcctl instances reclaim` 获取它（该命令会等待一个租约时长，并要求你输入持有者标识）。仍在运行的运行会持续续约，无法被回收：请先停止该进程。两项相关变更：当集群的 Operator 与当前 dcctl 不匹配时，`dcctl instances release` 会拒绝执行（传入 `--force` 可强制释放；完全没有 Operator 的集群仍然放行，这正是该命令的用途；它现在需要对 `customresourcedefinitions` 的 `get` 权限）；dcctl 整体替换实例声明的写入（初始化、升级及其注解记录）如果遇到含有当前 dcctl 不认识字段的声明，会拒绝写入，而不是将其删除。请使用与集群匹配的 dcctl。
- **设备属性写入会获取按设备的锁。** 现在，对设备属性的每一次写入都在事务中执行，并首先获取该设备的锁：设置或删除属性（包括以前不使用事务写入的 CLIENT 作用域）、恢复或复制设备配置、为设备导入分面值，以及删除设备。对同一设备的两次写入会依次执行；对不同设备的写入不会相互等待。锁本身新增一个数据库函数（下文所述的写入协议会新增一张表）。如果属性行在五次短暂重试后仍处于占用状态，`setEntityAttribute`、`deleteEntityAttribute`、`restoreDeviceConfiguration` 和 `copyDeviceConfiguration` 会返回新的可重试错误代码 `ATTRIBUTE_WRITE_BUSY`，且不会写入任何内容；请重试该请求。在升级滚动进行期间，由已升级 pod 处理的写入可能会短暂让步于由尚未升级的 pod 处理的同一属性写入。三个新指标会记录这些情况：`device_attribute_lock_wait_seconds`、`device_attribute_write_yields_total` 和 `device_attribute_write_deadlocks_total`。
- **设备属性写入遵循写入协议。** 现在，对设备属性的每一次写入以及租户清除，都会将其事务标记为遵循设备属性写入协议；设备属性的每一次设置、取消设置或重新创建，都会推进保存在新表 `entity_attribute_generations` 中的按键代次。本版本尚不强制执行该协议；后续版本依赖于本版本的所有 pod 都遵循它，因此请逐个次版本升级。在升级滚动进行期间，由尚未升级的 pod 删除的设备可能会遗留属性行；在单个副本上运行的清理过程会在十分钟内将其删除，`device_attribute_orphans_removed_total{table}` 会统计删除的数量。
- **预留的设备群操作权限。** 新增的租户权限 `fleet:operate` 为后续版本中的设备群操作预留；本版本尚无任何 API 检查它。它不会被添加到任何内置角色中（持有 `*` 的租户管理员已满足该权限）。请仅在**升级的滚动部署全部完成后**才将其授予自定义角色：尚未升级的 user-management Pod 会拒绝权限集合中包含未知名称的任何角色编辑，即使该角色已持有此权限，且此次编辑与该权限无关。
- **`deploy/gke` 中的 GKE 配置现在通过共享模块创建集群。** 变量、默认值和输出均未改变；由较早检出版本应用过的状态会在下一次 `tofu apply` 时迁移到新地址（`tofu plan` 会把每个资源显示为一次移动，不会规划其他任何更改）。状态一旦由此版本应用，就不要再用较旧的检出版本应用它：旧配置在原地址找不到这些资源，会规划删除集群并创建新集群。如果您从多个检出目录运行负载测试，请先全部更新。README 中“If an apply fails part-way”一节现在把集群写作 `module.gke_cluster.google_container_cluster.this`。
- **已弃用：`InstanceConfiguration` 自定义资源，以及 `Instance` 声明中的 `spec.grafanaSSO` 和 `spec.compact` 字段。** 本版本中实例不受任何影响，后续的主版本将删除它们。`InstanceConfiguration`（`dcic`）未被使用，因为每个服务都从挂载的 Secret 读取实例配置；现在读取或写入该资源时 API 服务器会发出警告。`spec.grafanaSSO` 不起作用：Grafana 的登录针对整个集群配置。`spec.compact` 由 `spec.size: compact` 取代，dcctl 已在其旁写入后者：**请保留两个字段，不要删除 `compact`**。dcctl 仍读取 `compact`，并拒绝 `size` 为 `compact` 但未设置 `compact` 的声明，因此手动删除它会阻止之后的每次初始化和升级。无需任何操作。
- **第二个或第三个本地实例只能用本版本或更高版本的 `dcctl` 升级。** 本版本分配了 MQTT 节点端口 31884 或 31885 的实例，在之后的每次升级中都会保留该端口。旧版 `dcctl` 只认识 31883，用它升级会移除该实例的 MQTT 端口，使代理只能在集群内访问。
- **`dcctl upgrade` 拒绝跳过次版本。** 跳过次版本从来都不是受支持的路径；现在 `dcctl upgrade` 会强制执行这一点。它读取实例工作负载实际运行的版本（从运行中的镜像读取，而不是声明的版本，因此中途停止的升级会按照仍在运行的版本来判断），并拒绝比该版本高出不止一个次版本的目标，同时告诉你应先升级到哪个次版本。跨越主版本时，只允许升级到该主版本的第一个次版本。如果无法读取正在运行的版本，升级同样会被拒绝。`--allow-skipped-minor` 可以覆盖此拒绝；该路径不受支持且未经测试，并且该选项绝不会禁用数据库强制执行的任何保护。

#### v1.9.0 新增内容 {#v190-whats-new}

- **自动注册持续失败的设备不再拖慢其数据源。** 当集成、Sparkplug 或 LwM2M 数据源自动注册设备，而 device-management 拒绝创建（例如数据源指定的设备类型不存在）时，该设备现在会先等待一段短暂的退避期再重试。退避期从 10 秒开始，在持续失败期间加倍，最长 5 分钟。设备在退避期内发送的消息会被丢弃，并与其他未注册设备的丢弃一起计数，不会再次调用 device-management。退避按设备计算，因此同一数据源上的其他设备不受影响；设备一旦注册成功，退避立即结束。处于退避期的 LwM2M 设备会被告知稍后重新注册。
- **安装时即分散代理领导权，并且可以查看其位置。** `dcctl bootstrap` 现在最后会均衡消息代理各服务器所领导的流，这与 `dcctl upgrade` 在重启代理后执行的尽力而为步骤相同，且绝不会导致 bootstrap 失败。该步骤、`dcctl upgrade` 和 `dcctl broker rebalance` 现在还会让已解析事件流避开领导代理 MQTT 消息存储的服务器，并在某个服务器领导的逐事件流多于其副本所在服务器时，将已解析事件的消费者移走。设备接入流和 MQTT 流仍然永远不会被移动。新命令 `dcctl ha leaders` 在不移动任何内容的情况下，报告每个逐事件流和每个已解析事件消费者由哪个服务器领导，以及重新均衡能否进一步分离它们（`PASS`、`FAIL` 或 `UNSETTLED`，支持 `--json` 和 `--expect-fail`）。旧版 `dcctl` 不了解 MQTT 消息存储，可能会把已解析事件流移到该服务器上，因此请使用本版本的 `dcctl` 执行重新均衡。
- **批量导入分面值。** 新增的 GraphQL 变更 `importFacetValues` 支持通过令牌定位现有设备、资产、区域和客户，每次最多导入 1,000 个值。它按分面声明的类型验证每个值，写入 SHARED 作用域，并报告每一行的结果，包括失败原因。使用 `dryRun: true` 预览；重复提交未更改的值时返回 `EXISTS`，不会再次写入。设备 CSV 导入仍可使用现有的 `importDevices` 变更。
- **由自动化发送的命令会记录发送原因。** 当规则的发送命令动作发送命令时，命令现在会带有 `cause`：触发的规则和配置文件、检测类型、检测的精确时间和触发值；如果同一规则在该检测上还指向某个告警，还包括该告警的键（这本身并不能证明告警已产生）。command-delivery GraphQL API 新增了 `Command.cause` 以及 `CommandCause.deviceId`，即使设备令牌被重复使用，读取方也能将告警对应到正确的设备。查看原因需要 `device:read`；原因涉及告警时还需要 `alarm:read`，规则检查地理围栏时还需要 `location:read`。仅限部分客户的成员看不到原因。只有平台服务可以设置原因；使用个人令牌设置原因的请求会被拒绝。MCP 工具 `list_commands` 也会返回原因，`list_alarms` 现在会返回每个告警的 `originatorId`。由人员或批量操作发送的命令以及本版本之前发送的命令没有原因，升级滚动期间发送的部分命令也可能没有原因。新增的 `react_command_cause_omitted_total` 指标统计去掉原因后重新发送才被接受的命令。
- **控制台不再覆盖他人对区域、资产、客户或分组所做的更改。** 编辑区域、资产或客户的名称、描述或类型，或编辑区域、资产、客户或设备分组的名称或描述时，控制台现在会发送打开表单时的版本。如果在此期间他人已保存了更改，此次保存会被拒绝并给出相应提示，你输入的内容仍保留在表单中。此前，后一次保存会悄无声息地替换前一次。
- **批量创建设备现在接受带多余空格的令牌模板。** 控制台的批量创建表单在发送前本来就会去掉令牌模板首尾的空格，但校验却针对未去除空格的文本，因此首尾带空格的模板会被拒绝，并提示“生成的令牌无效”。现在校验的就是实际发送的模板。
- **在控制台中通过 CSV 文件导入分面值。** 在“分面”页面中，**导入值**可读取最多 10,000 行、5 MiB 的 CSV 文件，映射其列（实体类型、实体令牌、分面键、值），先以试运行预览每一行，再按每批最多 1,000 个值写入。文件中任何位置重复出现的实体和分面键组合只发送第一行，其余行会被拦截并报告。结果可筛选和下载，被拒绝的行可单独重试；失败行文件只保留四个导入列，因此源文件中的凭据列不会被再次写出。
- **控制台现在可以管理分阶段的 OTA 发布。** 使用分组（cohort）创建的发布会在发布页面显示其计划（每个分组的规模、是否由操作员推进，以及已完成、当前或待处理状态），设备表也新增“分组”列。当某个分组顺利完成时，发布会暂停为“分组已完成，可推进”，点击**推进到下一分组**即可放行下一个分组；按钮针对屏幕上显示的分组，因此过期的页面或另一位操作员不会造成重复推进。当某个分组结束时存在失败或结果未知的设备，**忽略失败并推进**会在接受这些结果之前要求确认；因失败率上限而中止的发布则提供需确认的**忽略失败率中止并恢复**，针对屏幕上显示的那一次中止。这三项操作与其他发布控件一样需要 `artifact:write`。页面上的计数仍涵盖整个发布，而非单个分组。新增文案提供英文和西班牙文；在简体中文目录翻译完成之前，更新页面仍显示英文。
- **控制台和 `@devicechain/client` 可以拒绝实例无法处理的请求。** 为较新版本构建的控制台连接到较旧的实例时，过去会发送实例无法理解的请求，并以难以理解的错误失败。协商功能通过经过身份验证的 `OPTIONS` 请求，向每个 GraphQL 端点获取其提供的 schema，并在发送前用它检查每个请求。使用了实例所没有的字段或输入的请求会被拒绝，不会发送任何内容，错误信息会指出缺少什么。不会裁剪或改写任何内容。早于 v1.9.0 的实例不提供这种发现，因此无法确认其版本：请求会被拒绝，并给出提到 v1.9.0 的错误，而不会凭猜测发送。控制台仅在可能与其实例版本不同的情况下启用协商：使用 `vite dev` 时，或使用 `VITE_SCHEMA_NEGOTIATION=on` 构建时。由其自身实例提供服务的控制台保持关闭，因此自行应答 `OPTIONS` 的代理不会使其出错。登录、令牌刷新以及实时订阅不做检查。对于您自己的应用，`@devicechain/client` 新增了 `enableSchemaNegotiation`（可选 `release` 参数）、`disableSchemaNegotiation`、`resetSchemaNegotiation`、`SchemaSkewError` 和 `isSchemaSkewError`；除非您启用，否则默认关闭，并且需要您已安装的 `graphql` 对等依赖包。如果 CORS 代理或 Web 应用防火墙自行应答 `OPTIONS`，发现会失败，协商会拒绝所有请求，因此请让 `OPTIONS` 到达 GraphQL 端点。`/dash` 查看器应用暂未启用该功能。
- **控制台现已完整支持简体中文。** 所有控制台页面，包括空中升级（OTA）更新页面、更新签名者、浏览分面、设备配置文件、租户、成员和管理页面，现在都以简体中文显示，不再回退到英文。
- **集群内执行器（Preview）。** `dcctl executor enroll/status/unenroll` 用于安装、查看和移除由所有者固定镜像的执行器，包括其命名空间、ServiceAccount、Deployment、RBAC、注册资源和未绑定的请求方角色。本版本不会执行任何操作：请求会被拒绝，原因为 `NotExecutableInThisBuild`，现有实例不会改变。注册要求每个实例均有指纹与已部署根密钥匹配的托管文件（`OwnerOffline`）。`dcctl install` 会安装 `ExecutorEnrollment` 和 `InstanceOperation` 两种新资源定义，但不会部署执行器。与每次发布一样，旧版 dcctl 会拒绝操作资源定义已由 1.9 版 `dcctl install` 更新的集群。
- **更多 `dcctl` 命令可输出 JSON 供脚本使用。** `dcctl dead-letters list`、`dcctl tenant-deletion status` 和 `dcctl instances list` 支持 `-o json`（与 `dcctl events failed` 的标志相同），`dcctl ha verify` 和 `dcctl ha verify-db` 支持 `--json`（与 `dcctl ha leaders` 相同）。使用这些标志时，标准输出只包含 JSON，没有实例时 `instances list` 输出 `[]` 而不是一句话。退出状态不变：除非达到目标，`tenant-deletion status` 仍以非零状态退出；`--expect-fail` 仍会反转 `ha` 检查的结果，其负向对照消息在 JSON 模式下输出到 stderr。
- **通过 dcctl 管理实体关系。** 使用 `dcctl relationship add`、`list` 和 `remove`，通过租户 API 创建、查看和删除有类型的实体关系。这些命令支持 `--instance` 或 `--server`、双因素登录，以及表格或 JSON 输出（`--json` 或 `--output json`）。列表支持分页，并可按来源、目标、关系类型和是否跟踪进行筛选。
- **dcctl 协商 API 能力。** 租户和管理命令在发送操作或输入字段之前检查目标实例的架构。不支持的功能会给出升级建议。每个会话按完整端点 URL 缓存成功的发现结果，并拒绝发现请求的重定向。较新的实例向已认证的客户端提供实际架构，无需启用匿名自省。对于较旧的实例，dcctl 使用集群 Instance 资源中记录的实例版本，通过本地实例记录或 `--instance` 定位该资源。记录为稳定版 v1.7.x/v1.8.x 的实例可在匹配的端点上使用保守的 v1.7 API 契约。OPTIONS 请求被拒绝不能证明目标版本：版本未知时仍须发现架构，并提示选择可读取的实例记录或使用 v1.9 的架构发现。core 库调用方通过 `WithLegacyEndpointVersion` 注册经过独立验证的版本；预览功能和超出旧版契约的功能需要 v1.9 的架构发现。现有命令和参数仍然可用。
- **`dcctl bootstrap --sharding`。** 新标志用于在安装实例时选择其分片级别。该级别记录在实例上，之后不能更改、添加或移除。默认值 `off` 生成的内容与该标志出现之前 dcctl 生成的完全相同，且不记录任何内容。`scale` 会因尚不可用而被拒绝。在使用 `--sharding` 之前，请先运行 `dcctl install`，使集群的 Instance 定义能够记录该字段。如果不指定该标志，而在 `--values` 中设置自己的 `shardGroups` 块，其行为与以前完全相同，bootstrap 摘要和 `dcctl upgrade` 会将这种配置报告为 `custom`。
- **长时间跨度下，载荷解码器健康读取依然快速。** 解码器健康统计（`payloadDecoderHealth`）现在在数据库中求和，不再把每个副本每分钟的行都加载到 device-management。新的迁移为解码器健康表添加了两个索引：一个用于按配置文件的读取，另一个用于每小时清理旧行。返回结果不变，包括报告哪条最后错误。该迁移在升级期间运行，建索引时会短暂阻塞对该表的写入。
- 并发删除设备时，若另一次删除先完成，则返回 `false`；事件源关闭时会以 `reason="shutdown"` 统计放弃的分片放置等待消息。
- **已删除实体表的大小现在会被上报，并带有告警。** 事件存储为每个曾被删除的不同实体令牌保留一行，使得之后以相同令牌创建的实体无法读取已删除实体的事件历史。该表不会被清理。它现在出现在 `append_only_table_bytes` 和 `append_only_table_rows_estimate` 序列中（`table="entity_deletion_watermarks"`，由 event-management 导出），新增的 `EntityDeletionWatermarksLarge` 警告会在其持续超过 1 GiB（约五百万个不同的已删除令牌）一小时后触发。告警手册中的对应条目给出了找出相关租户的查询。
- **设备配置报告历史。** 服务现在在内部保留 200 天的已接受设备配置结果历史；该历史尚未通过 API 提供。新增指标 `device_configuration_reports_pruned_total{reason="orphan|retention"}` 统计维护过程中删除的行数。
- **并发删除设备。** 修复了在创建子设备和更新属性的同时删除网关，以及在恢复或复制配置的同时删除实体时发生的数据库死锁。仍无法删除拥有子设备的网关。
- 已删除设备的持有者修复现在会将确认不存在后的重试间隔从一小时逐步延长至七天，减少对 device-management 的重复查询。设备名册消息仍会及时检测重新创建的设备。如果名册消息丢失，成功的扫描会在下一次到期时检测到重新创建的设备，最长为七天加一个扫描间隔。任何删除记录都不会被永久跳过。
- **代理停止指令耗尽投递重试次数后仍保持执行阻断。** 如果 device-management 在最后一次投递时仍无法应用停止指令，它会写入原因为 `unprocessable` 的死信，并阻止该进程内所有租户的代理告警执行，直到进程重启。存储恢复后，请重新发出停止指令，然后重启受影响的 device-management 进程；成功应用后续停止指令或正常轮询消息代理均不会解除此锁定。
- **现在会上报设备令牌持有者表的大小。** 事件存储为每个曾被在用设备持有过的设备令牌保留一行。正是借助这张表，按一个被删除后又重新创建的令牌读取时，只返回新设备的事件。该表不会被清理。它现在出现在 `append_only_table_bytes` 和 `append_only_table_rows_estimate` 序列中（`table="device_token_holders"`，由 event-management 导出）。它没有单独的大小告警：它随设备规模增长，所以表大通常意味着设备多。如果反复删除设备并以新令牌重新创建，已删除实体表也会随之增长，该表由 `EntityDeletionWatermarksLarge` 监控；事件存储的卷容量告警则覆盖存储空间。
- **重新创建的设备不再显示已删除设备的事件。** 删除一个设备并用相同令牌创建新设备后，按该令牌读取事件（事件、测量值、位置、告警、在线状态变化、分桶测量值以及计算指标预览，包括通过 MCP）只返回平台自删除时刻起为新设备接收的内容，其中包括日期更早的缓存读数。尚未重新创建的已删除令牌仍像以前一样读取其设备的完整历史。重新创建的令牌的分桶测量值改为根据已存储的读数计算，而不是使用预聚合的汇总。此行为适用于 v1.8.0 或更高版本处理的删除。
- **保护并发编辑。** device-management 的 GraphQL 变更 `updateArea`、`updateAsset`、`updateCustomer` 和 `updateEntityGroup` 现已支持可选参数 `expectedUpdatedAt`。传入上次读取的实体 `updatedAt`，即可在保存过期数据时收到 `STALE_WRITE` 错误；重试前请重新加载。省略该参数时仍保留最后写入生效的行为。
- **设备群操作就绪检查（Preview）。** `fleetOperationReadiness` 查询和 `dcctl fleet-op readiness` 命令报告此实例是否可以运行设备群操作。在此版本中，它们报告尚未安装强制执行机制，且没有可执行的操作类型，这是预期结果。需要 `fleet:operate` 权限；具有受限范围的成员无法使用。预览期限从 v1.9 开始。
- **GraphQL 请求的解析更加严格。** GraphQL 库已更新，以前被接受的数字字面量现在会作为语法错误被拒绝：前导零（`01`）、末尾或开头的小数点（`1.`、`.5`）、超出范围的整数，以及位于非数字之前的减号。此外，每个文档在验证期间的字段重叠比较次数上限为 100,000 次。格式正确的请求不受影响。
- **高可用性验证会拒绝不完整的消费者列表。** 当某个流列出的消费者数量与消息代理报告的数量不一致时，`dcctl ha verify` 现在会拒绝评估该快照。错误信息会指出流名称和两个数量；请重新运行命令以收集完整快照。
- **海滨 3D 场景在小镇后方有了海岸线，所有模型都更加精细。** 在 `demos/harborpulse-unity` 中，薄雾现在会淡化远处的森林，因此数量更少但更饱满的树木（枫树、桦树、橡树、云杉和松树）立在有林间空地、花岗岩露头和映衬天空的山顶的山丘上。小镇的建筑数量更少但更精细，带有门廊、装饰线条、烟囱和多样的屋顶。路灯、充电桩、水位计、泵站、卡车、船只、浮标、信标和栈桥的模型更加精细，码头呈现出混凝土接缝、石质压顶、铺路石和标线。路灯不再与充电桩或汽车重叠，并有测试确保任何设施都不会与其他设施重叠。夜间的告警数量更易阅读。
- **海滨 3D 场景在小镇后方有了纵深，时间也一眼可辨。** 在 `demos/harborpulse-unity` 中，最后一排树木与远处山脊之间新增了林木覆盖的山麓和一道中景山岭，地平线以下的薄雾颜色更深，因此陆地层层后退，而不再止于一条苍白的色带。低角度的太阳照亮朝向它的立面并从下方映亮云层，月光为夜晚的云镀上银色，所以即使在朝北的总览视角中，黎明、白天、黄昏和夜晚也各不相同。9:16 总览有了新的取景，画面中有天空和日出；开阔海岸有了岩石滩和礁石；花岗岩露头看起来像岩石；点亮的路灯带有柔和的圆形光晕；近看时维修卡车是一个完整的模型。浏览器构建现在从页面地址读取预览选项（`?hp-offline&hp-start-hour=17.3`），并能正确绘制水面。
- **海滨 3D 场景现在会说明平台为什么发出一条命令。** 在 `demos/harborpulse-unity` 中，当过热充电桩的降额命令带有平台随命令记录的原因，并且该原因指向画面上的那条告警时，实时运行的一天会显示“The platform tells it to derate”；场景按告警键、设备记录和检测时间把两者对应起来，如果缺少这一关联，或读取方无权查看，则只显示“Alarm raised”。风暴中水泵的流程条也以同样方式显示 `start-pump sent`。`hack/harborpulse-rig.sh` 会检查每个模拟器提供的令牌能否读取命令的原因；以 `-hp-control-no-cause` 启动的播放器则会有意显示回退文案。场景还新增了一座从总览中即可辨认的高大花岗岩突岩，9:16 总览的远处有了成片的雨带，风暴天空更为平静，渡轮码头候船厅缩短后不再遮挡充电桩，泵井视图前方不再停放车辆、水位标签也不再与字幕重叠，清晨的色调不再偏蓝。
- **海滨 3D 场景加入导演模式。** `demos/harborpulse-unity` 现在把 Port Alder 绘制成一个紧凑的马蹄形港口，一个镜头即可容纳，每台设备都画在保持其所属码头及沿岸顺序的位置上。导演跟随模拟的一天：把镜头移到每个故事节点，给出简短字幕，并在屏幕上逐步构建因果链，每一步只有在读到平台自身的相应记录后才会出现。无法确认的步骤绝不会显示为已完成，场景宁可少说。自由漫游可随时接管镜头，空闲 20 秒后交还。带有预设缺陷的运行会显示红色 TEST CONTROL 横幅，而为不同布局构建的场景会关闭导演并加以说明。每次实时运行都会录制为可事后检查的文件。场景仍然只读。
- **海滨 3D 场景采用阴天海港的光照，夜景也清晰可辨。** 在 `demos/harborpulse-unity` 中，白天光线柔和、云层覆盖，阴影轻淡，山丘上有薄雾。风暴使天空变暗，但小镇依然清晰可辨；夜间环境光保持在月光水平。窗户、路灯、码头灯和告警信标现在不仅在编辑器中、在构建出的播放器中也会发光，此前在播放器中可能显得暗淡。小镇有了自己的路灯，东部的窗户和路灯会在东侧馈线停电期间熄灭。每一页的字幕都清晰可读。新增的 `-hp-frame-stats` 参数会记录帧时间。
- **海滨 3D 场景展现了更多天空、更茂密的森林，以及看得见到来的天气。** 在 `demos/harborpulse-unity` 中，小镇后方的山丘更低，故事镜头的机位也更低，因此每个镜头里都有天空。天空跟随场景时钟和风暴变化：玫瑰色的黎明、正午的晴天云、风暴前三小时逐渐堆积的云层、带雨的阴暗天空，以及之后放晴直至月夜的天空。黎明、黄昏和风暴期间都有足够的光照，小镇始终清晰可辨。森林更加茂密，约五分之三是绿色针叶树，其余为秋色，坡上嵌有草地和花岗岩岩层，并在远处山脊之前隐入薄雾。靠近镜头的建筑、汽车和船只增加了门、窗框、门廊、车轮和配件，远处的则保持简洁。
- **海滨 3D 场景中的风暴现在有了日落，黎明也不再昏暗。** 在 `demos/harborpulse-unity` 中，覆盖场景每个黄昏的风暴云层不再一直压到西边的地平线：在一天的最后一小时，云层下方露出一道明亮的天空，低垂的太阳透过它照到小镇，前方的雨幕被逆光照亮。黎明更明亮，早晨的码头也不再偏蓝。小镇后方的山岭有了林线，秋色成片分布；花岗岩岩台上有了散落的石块和几处突岩；9:16 总览把港口放在画面中部；检修路灯的班组带来了高空作业斗车。远处的雨成束垂落，而不再是一片灰色。静帧可以渲染为晴天外观检查（`-hp-stills-weather fair`），画面上会如实标注，且不会改变场景报告的任何内容。
- **海滨 3D 场景在 9:16 竖屏画面中可以正常阅读，地面也不再露出网格。** 在 `demos/harborpulse-unity` 中，泵井剖视图有了专用于竖屏画面的版本，水位标签位于字幕与事件流程条之间，不再被它们遮挡。9:16 总览在内港西侧的树林中新增了一座花岗岩突岩，两座突岩现在显示的是细窄的层理缝，而不是明暗相间的条纹。地面颜色沿地形平滑过渡，不再逐格变化。雨带只在高处视角下绘制，在那里看起来才像雨；从 16:9 总览和码头看去，远处的山峦重新均匀地淡出。连接器过热页面的广角镜头不再越过停泊渡轮的顶棚。
- **海滨 3D 场景有了真正的小镇。** 在 `demos/harborpulse-unity` 中，港口后方由一模一样的房屋排成的均匀网格被一个更小、更质朴的小镇取代：一条向内陆攀升的主街和几条支路，木板墙房屋、砖砌商铺楼、水边棚屋、一家旅店和一座白色教堂错落其间，房屋之间有树木，背后是秋色的林木山丘。海滨新增带凉亭的公园草坪、立在木桩上的木栈桥和系泊的船只。港内水面现在呈现随距离淡去的柔和倒影，而不是生硬的镜面，码头沿岸的浅水带有青绿色调。码头和每台设备的位置都完全不变。
- **新数据的长时间范围图表更快。** 按整数小时（1 小时、2 小时、6 小时、24 小时等）分桶的测量读取，现在改由建立在现有 1 分钟汇总之上的第二层 1 小时汇总提供，因此较长的图表每个指标只需读取几百行预聚合数据，而不再是数万行。升级时只创建新的汇总而不回填历史，所以加速作用于升级之后写入的数据；升级之前的读数仍像以前一样从 1 分钟层读取，两种方式的结果相同。范围中途截断的小时（包括仍在进行的当前小时）同样从 1 分钟层读取。该汇总每分钟在后台刷新一次，并沿用与 1 分钟汇总相同的保留期。现有的 `DisableRollupReads` 开关会同时关闭两层汇总读取。
- **每个版本均提供实例配置模式。** 从目标版本的 GitHub Release 下载 `instance-config.schema.json`，或运行该版本的 `dcctl config-schema` 离线获取相同模式。该模式从 Go 配置类型生成，用于验证实例文档的结构并拒绝未知键。启动时仍会检查语义约束和跨字段约束。
- **在 device-management 短暂不可用期间，入站集成会继续接受上行消息。** event-sources 会将集成标识符解析到的设备记住五分钟。在此版本之前，一旦该结果超过五分钟，在无法连接 device-management 时到达的上行消息会得到 `503` 响应。现在，最近一次结果会在 device-management 给出后最多使用 15 分钟（与集成令牌和负载解码器查询已经使用的上限相同），上行消息会被接受，并在 device-management 恢复后处理。device-management 仍会针对每个事件检查集成、设备及其标识符，因此在此期间被禁用、删除或改用其他标识符的设备会在那时被拒绝。device-management 实际给出的结果（例如标识符未注册或集成已禁用）绝不会被较早的结果替代。下行报告（网络服务器发送的确认）不在此列：在无法查询 device-management 时，仍会像以前一样得到 `503` 响应。以这种方式处理的请求会计入 `integration_requests_total` 新增的 `resolve_stale` 结果（同时也计入其通常的结果），并且 event-sources 会记录一条警告。
- **第二个和第三个本地实例也有各自的 MQTT 端口。** 在本地 kind 集群上，`dcctl bootstrap` 现在会为每个新实例分配集群发布的第一个空闲 MQTT 节点端口。此前只有第一个实例能从主机访问，其余实例只能在集群内访问。本版本创建的集群发布三个端口：主机端口 1883、1884 和 1885。较早创建的集群只发布 1883，行为与以前相同。bootstrap 摘要会打印地址（`MQTT: ssl://localhost:<端口>`）。该端口会记录在实例中，因此 `dcctl sim create` 会使用它。升级会保留实例已有的端口，绝不会分配新端口。创建新的本地集群现在除 1883 外还需要主机端口 1884 和 1885 空闲；`dcctl preflight` 会检查这些端口。
- **失效的 MQTT 代理不再卡住繁忙的客户端。** 外部 MQTT 事件源、Sparkplug 主机以及边缘代理的云端上行链路现在会将每次向代理的套接字写入限制为 5 秒，并显式声明 30 秒的 keep-alive。此前，如果发布方已向不再响应的对端写满套接字，可能会在该写入上无限期阻塞且察觉不到连接已失效；现在它会断开连接并重新连接。
- **分阶段 OTA 发布。** 可先指定具名金丝雀设备，再按累计设备百分比分阶段发布。SKIPPED 和 CANCELLED 目标不会阻止下一阶段。失败或 UNKNOWN 结果会以 `COHORT_FAILED` 暂停推进；必须通过 `promoteUpdateRollout` 的 `overrideFailure: true` 显式接受，待处理或执行中的目标仍会阻止推进。UNKNOWN 仍占用并发名额。如果 UNKNOWN 在所有金丝雀设备分配完成前占满并发名额，发布会明确暂停；解决或放弃该尝试后，恢复发布以完成待处理的金丝雀设备。支持手动推进。可选的失败率策略采用观察窗口和最小样本数，阈值范围为 1–99，严格超过才暂停。显式恢复会保留相关证据；仅更改已确认尝试的版本不会再次触发暂停。
- **升级后启用。** 固定实例设置 `infrastructure.updateManagement.rolloutSafety` 默认为 false。关闭时，创建或启动带 cohort 或 `failureRate` 的发布将返回 `ROLLOUT_SAFETY_DISABLED`。版本滚动升级期间必须保持关闭。待**所有 update-management 副本升级完成后**，再通过**单独的配置滚动更新**设为 true。旧副本会绕过阶段和失败率保护、允许普通恢复操作，也无法表示新的暂停原因。普通发布的默认行为及并发限制保持不变。
- **被挂起的投递在 15 分钟后放弃。** 租户的分片放置不可用时，event-sources 会挂起该租户的最后一次投递。放置始终不恢复的租户不再无限期占用挂起容量：15 分钟后该投递保持未确认，与没有挂起时相同。新增计数器 `total_placement_hold_fallback{source,reason}`，统计因挂起已满而未被挂起（`full`）或已不再被挂起（`expired`）的投递。恢复后的挂起消息也不再被重复计为入站。
- **代理重启期间，副本流保持副本状态。** 在集群模式的代理上，如果某个服务的连接在滚动重启期间短暂处于两个服务器之间，该服务会把代理当作单个服务器。它会记录所有流和存储桶都以单副本创建，而在那一刻创建的流或存储桶可能以单副本创建，并一直保持如此。现在，服务在重新连接期间会沿用它最后看到的拓扑；在尚未看到任何拓扑之前，则使用配置的副本数。副本数已经低于配置值的流或存储桶会由 `JetStreamNotReplicatedAsConfigured` 告警报告，`dcctl broker reconcile` 会将其提升。
- **已解析事件会等待分片放置恢复。** 当 resolved-events 启用分片组时，如果某个入站事件的最后一次投递中有部分已解析事件因分片放置不可用而被拒绝，系统现在会保留该消息，并在放置恢复后完成处理，而不是将其记录为死信、其事件也永远不会被解析。系统只会重新发布被拒绝的已解析事件，已存储的事件不会重复发布。此行为同样适用于入站事件、集成上行消息和已解析事件信封。新增的 `resolve_placement_held_sources` 指标显示正在保留的最终投递数量，`resolve_placement_hold_fallback_total` 按原因统计未被保留或不再保留的投递数量。保留数量有上限，每条消息最多保留 15 分钟，之后会像以前一样保持未确认状态；保留仅持续到本次运行结束，关闭时保留的消息不会被确认，因此耗尽投递次数的消息仍可能被记录。滚动升级无需更改通信协议或配置，旧 pod 保持原有行为。
- **规则健康统计不再为已被取代的配置文件版本累积。** 每次发布设备配置文件都会为该版本的规则建立新的触发统计。被取代版本的统计此前会无限期保留，尽管规则健康视图只有在回滚使该版本重新生效时才会显示它们。现在，事件处理会在该版本的规则最后一次触发满一周后删除这些统计。配置文件活动版本的统计始终保留，没有活动版本的配置文件的统计也会保留。如果某个版本的统计被删除后又因回滚而重新生效，规则健康会明确说明：`RuleHealth` 上新增的 `historyRemovedAt` 字段给出删除时间，控制台会显示提示，此时触发次数和最近触发时间只包含该时间之后的触发。清理每小时由一个副本执行，按租户分批且每批有上限。新指标 `devicechain_eventprocessing_rule_stat_sweep_deleted_total` 统计其删除的行数。
- **分片事件处理启动。** 启动分片实例时，不再尝试从非零分组的数据流中删除未分片的旧版消费者，因此不会再产生相应的消息代理权限警告。原地升级时，零号分组仍会清理其旧版消费者。消息代理权限保持不变。
- **对 resolved-events 分片时，租户删除可以完成。** 在 resolved-events 使用分片组的实例上，删除租户不再停留在 "no DETECT engine answered" 并一直保留租户令牌。event-processing 已从每个组中移除该租户，但代理拒绝了它的应答：它通过同一个连接为每个组各发送一个应答，而代理对每个请求只接受一个。现在它只发送一个涵盖所有组的应答。在滚动升级期间，新旧 user-management 或 event-processing Pod 并存时删除可能仍被阻塞，两者都升级后，删除会在协调器的下一轮继续，并在通常的稳定等待和令牌保留等待之后完成。未对 resolved-events 分片的实例不受影响。
- **`dcctl sim create` 不再把模拟器指向另一个实例的代理。** 在本地集群上，如果所有已发布的 MQTT 端口都属于其他实例，该实例只能在集群内访问。此前对这类实例执行 `dcctl sim create` 会退回到集群的第一个 MQTT 端口，而那是另一个实例的代理。现在它会拒绝并提示传入 `--mqtt-broker`。实例记录现在会明确写明实例没有 MQTT 端口。对于本版本之前记录的实例，dcctl 会改为读取该实例自己的 Service，因此确实持有第一个端口的实例仍会解析到该端口。
- **Site Pulse 演示中的推土机现在有自己的设备配置文件。** 推土机不载货也没有轮胎，却与装载机和运输卡车共用同一个设备配置文件，因此车队表为它声明了载重和胎压，并显示“范围内无数据”。推土机类型现在使用独立的配置文件，仅声明燃油、发动机温度和发动机小时数，命令以及低油量和过热规则保持不变，但没有轮胎规则，因此这些单元格显示“不上报此项”。由早期版本创建的演示实例在重新创建之前仍使用共用的配置文件。
- **拒绝单点登录链接请求现在会被审计。** 使用 `rejectSsoLinkRequest` 拒绝待处理的请求时，认证审计日志会记录一条 `identity_link_rejected`，与批准请求时记录 `identity_linked` 的方式一致。该记录包含执行拒绝的管理员、请求和连接，不会记录提供方声明的邮箱或 subject。拒绝已不存在的请求，或被拒绝执行的操作，不会产生记录。
- **每个概念统一使用一个中文术语。** 简体中文控制台与文档现在对同一事物使用相同的词：预配配置文件、制品、分面、推出、身份提供商、机密和签名者不再在不同页面之间、或控制台与文档之间出现不同译法。
- **故障关闭模式下的 MQTT 可以写入事件信封（可选，默认关闭）。** 开启 `infrastructure.nats.mqttFailClosed` 时，`event-sources` 此前总是把每条 MQTT 消息单独写入 inbound-events 流，即使已开启[事件信封](#event-envelopes)。新的 `event-sources` 设置 `capture.mqttStoreEnvelopes` 让它改为以信封写入，从而减少代理为每个事件所做的工作。只有当 `infrastructure.nats.mqttFailClosed` 和 `infrastructure.messaging.envelopes.inbound` 都开启时它才生效，否则不改变任何行为。只有当消息所在的信封已写入 inbound-events 流后，该消息才会在 MQTT 消息存储中被确认；重新投递的消息会被识别，不会被写入两次。请在每个 `event-sources` Pod 都运行本版本之后，通过单独一次 `dcctl upgrade` 开启它，在值文件中设置 `functionalAreas.event-sources.config.capture.mqttStoreEnvelopes`（参见[调优实例](./tuning-an-instance.md)）。设置改变时，`event-sources` 会先等待按旧设置处理中的消息完成，再按新方式写入。
- **Harborpulse 模拟器：** 新增 MQTT 和证书认证充电桩、连接状态告警、期望与上报配置，以及通过 HTTP 拉取命令的路灯。新增基于平台数据的验证和相互独立的负面控制，并保留原有设备群及其升级和停电流程。
- **按批次查看更新进度。** 更新管理 GraphQL API 现在提供每个更新批次的结果计数，并支持按批次筛选目标设备。批次计数与整体更新进度采用相同的结果统计规则。
- **Harborpulse 租户隔离和 MCP 问答。** 验证工具现可检查使用相同设备标识符的两个租户，并将真实 MCP 服务返回的设备群答案与 GraphQL 数据比较。每次运行都会注册一个公共 OAuth 客户端，并使用租户成员通过 PKCE 获取的用户令牌。两个植入缺陷分别验证两项新检查能够独立发现错误。
- **代理命令提案（Preview）。** 命令新增 `causeDetails` 和 `proposal` 字段。原有 `cause` 保持检测来源的含义，对于代理提案创建的命令返回 null。

- MCP 新增静态工具 `propose_command`，要求 `act:commands`，且仅在 MCP 操作开关与 `infrastructure.mcp.commandProposals` 同时开启时提供。`get_proposal` 和 `list_my_proposals` 接受 `area: "command-delivery"`；省略时保持告警行为。启用配置键后 MCP 会话须刷新工具列表。控制台批准收件箱新增独立分页的命令页签，显示完整 JSON 参数、设备实例标识、影响、定义指纹、截止时间、批准倒计时及不可信纯文本理由。批准仅提交提议令牌和显示的摘要，将命令加入常规交付队列；命令仍可能等待维护窗口、到期或失败，且批准绝不能撤回已发送命令。
- Command-delivery 使用调用方自己的令牌读取设备信息：提出提议的代理和批准人员都需要 `device:read`，否则以 `FORBIDDEN` 拒绝。每个客户端及每个人分别每小时最多提交 200 个提议，每个客户端最多保留 20 个待决提议；策略可收紧限制。代理通过 `get_proposal(area: "command-delivery").resultRef` 跟踪已执行提议；`list_commands` 不显示命令来自哪个提议。

- Command-delivery 提供 `proposeCommand`、`approveAgentProposal`、`rejectAgentProposal`、`agentProposal` 和 `agentProposals`（Preview）。`infrastructure.mcp.commandProposals` 默认关闭；关闭时拒绝提案和批准。仅在版本滚动部署全部完成后，通过单独的一次升级启用：较早版本的 Pod 会拒绝该配置键。Command-delivery 对代理终止消息流的新增读取权限由消息代理的授权回调授予，因此启用前必须完成包含授权回调在内的整个版本部署。
- 每个提案仅面向一个指定设备。人员在控制台中根据设备已发布命令定义的快照批准提案，批准时会重新检查该定义。此即时检查并非跨服务锁：后续远程定义变更仍可能与事务提交并发发生。命令交付截止时间在提出提案时固定：默认一小时，最长 24 小时。
- 平台默认限制为：每个代理客户端和每个人分别每小时 50 次执行；每个租户 100 个待决提案、每小时 1000 个提案及每小时 200 次执行；试运行（dry run）另有限额，每个代理客户端和每个人每分钟 60 次。已决提案及其请求键保留 30 天，代理的 `requestId` 在此期间会被记住。租户级终止操作会结束待决提案，并取消仍在等待的代理命令（排队、暂缓或停放）；无法撤回已发送的命令。

### v1.8.0 — 维护窗口暂存告警通知，投递可安然度过中断 {#v180-upgrade}

`v1.8.0` 可从 `v1.7.x` 原地升级：先为集群运行 `dcctl install`，再为每个实例运行一次 `dcctl upgrade`，详见[滚动升级](#rolling-upgrades)。**两个命令都使用 v1.8.0 的 `dcctl`**，配合 OpenTofu 或 Terraform 1.10 及以上版本，并向 `dcctl install` 提供集群设置，例如 `--cluster`、`--kube-context`、`--ha`、`--compact` 和 `--database-node-selector`，以及 `--version`。本版本新增六个迁移，在各服务首次启动时执行：`device-management` 两个，`notification-management` 两个，`event-management` 一个，`event-processing` 一个。每个迁移在失败后都可以重新执行。这些迁移创建表并添加可为空的列，也没有新增任何配置项，因此在升级滚动进行期间，`v1.7.x` 的 Pod 可与 `v1.8.0` 的 Pod 并存并继续正常工作。告警抑制要等 `device-management` 和 `notification-management` 都完成滚动部署后才会完全生效，详见[升级到 v1.8.0 前](#v180-before-upgrade)。升级只能向前：无法回退到 `v1.7.x`。若从 `v1.7.0` 升级，`update-management` 还会执行 `v1.7.1` 的迁移，[`v1.7.1` 说明](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1)同样适用。若从 `v1.6.x` 或更早版本升级，请先逐个升级经过每个中间的次版本（包括 `v1.7.x`），并按各版本的说明使用该版本自己的 `dcctl` 和 `--version`：跳过次版本不是经过测试的路径（参见[支持的升级路径](./versioning-policy.md#upgrade-paths)）。`v1.7.0` 的说明见 [`v1.7.0` 说明](#v170-upgrade)。

```bash
dcctl install local --version v1.8.0
dcctl upgrade local devicechain --version v1.8.0 --dry-run
dcctl upgrade local devicechain --version v1.8.0
```

适用[版本与升级策略](./versioning-policy.md)。本版本新增受保障的接口，不移除也不弃用任何接口。对没有 `location:read` 的成员隐藏地理围栏衍生告警的通知历史属于策略允许的安全修复；其他新的拒绝和行为变化列在[升级到 v1.8.0 前](#v180-before-upgrade)。Helm 值和实例配置不在兼容性保障范围内。

要点：告警维护窗口可针对设备、设备类型、组、区域、资产、客户或整个租户声明，窗口内触发的告警会标记为已抑制，其通知暂存至窗口结束；控制台可启动和管理维护窗口，MCP 会显示它们。告警状态变更的通知现在与变更本身一起记录，并从该记录发送，直到 broker 接受为止，即使 broker 中断或 `device-management` 重启也是如此；在 broker 不可达时，删除设备和更改授权也会更快返回。使用 NATS 分片组时，捕获的消息会等待分片放置恢复，实时订阅会跟随放置变更，新安装的实例可以对 `resolved-events` 分片。实体删除后才存储的事件不再重新关联到使用相同令牌重新创建的实体。`harborpulse` 模拟器场景现在覆盖出站投递、LoRaWAN、设备群活动、签名更新和一个维护窗口，并由其验证器逐项判定。

新增内容：

- [升级到 v1.8.0 前](#v180-before-upgrade)
- [v1.8.0 新增内容](#v180-whats-new)

#### 升级到 v1.8.0 前 {#v180-before-upgrade}

- **告警抑制窗口现已可用。** 可为设备、设备类型、组、区域、资产、客户或整个租户声明一次性或周期性窗口。告警仍会触发、记录并显示，同时标记为已抑制；通知暂存至窗口结束，届时若告警仍处于活动状态且尚未确认，则发送通知。小范围设备窗口需要 `alarm:write`；更大范围的窗口还需要 `notification:write`。结束窗口只需要 `alarm:write`。升级期间，窗口内的告警仍可能发送通知；请在 device-management 和 notification-management 均完成滚动部署后再依赖抑制功能。
- **告警通知在到达 broker 之前，会先排入 `device-management` 的数据库。** 较早的版本不会发送排在那里的内容。如果需要重新运行较早版本的 `device-management`，请先等待所有副本上的 `alarm_events_pending` 指标变为 0；较早版本接管时仍在队列中的内容不会被发送。
- **本地安装拒绝资源不足的 Docker 引擎。** 创建或复用本地 kind 集群的非紧凑模式 `dcctl install local`，现在会在 Docker 报告少于 4 个 CPU 或不足 7.5 GiB 内存时拒绝继续。内存介于 7.5 GiB 与 8 GiB 之间时仍仅发出警告，因此设置为 8 GB 的 Docker Desktop（报告值略低）不会被拒绝。此前在较小引擎上忽略警告继续安装的运维人员必须增加 Docker 资源，或使用 `--compact`（或 `--size compact`）选择低资源占用预设。紧凑模式安装、`dcctl preflight` 和 `dcctl bootstrap` 仍仅发出警告。
- **通知历史需要位置权限。** 拥有 `notification:read` 但没有 `location:read` 的成员将无法查看地理围栏衍生告警或位置分类未知告警的通知状态和投递尝试。升级前写入的行和旧版 device-management 副本发出的事件没有分类结果，因此会被隐藏，直到告警通过新版生产者再次改变状态。这些历史查询会向 device-management 验证缓存的分类。如果 device-management 无法访问，或未配置其端点或服务密钥，查询将返回错误。对于 device-management 已不再保存的告警，这些成员无法查看其历史。请为需要此历史的成员授予 `location:read`。发送到管理员所选渠道的通知保持不变。
- **策略拒绝现在计为 `unwanted`，不再计为 `dropped`。** `resolve`、`device_reports` 和 `update_reports` 处理循环不再把已禁用设备的流量，或超出记录预算而被抑制的解码失败，计为 `result="dropped"`。这类流量现在计为新的 `result="unwanted"`，概览仪表板的失败面板也不再包含它。`dropped` 现在始终表示已放弃处理某条消息。没有指标被重命名。滚动升级期间，仍在运行旧版本的 Pod 会把这类流量计为 `dropped`，直到它们被替换。

#### v1.8.0 新增内容 {#v180-whats-new}

- 控制台现可启动和管理告警维护窗口：设备、区域、组和资产页面提供“开始维护”操作，告警显示“维护中”标签，并在设置中提供“告警维护”页面。
- MCP 服务器会显示告警是否处于维护窗口内，新增的 `list_suppression_windows` 工具可列出维护窗口。
- **告警通知在 broker 中断或 `device-management` 重启后不再丢失。** 告警状态变更（由检测触发、升级、清除，或由运维人员确认或清除）现在会与变更本身一起记录，并从该记录发送到 broker，直到 broker 接受为止。以前，在 broker 不可达时或 `device-management` 重启前做出的变更可能已保存，却没有发出任何通知。在 broker 不可达期间，确认或清除告警现在约两秒内即可返回，批量确认也是如此；以前每个告警会使调用挂起约 30 秒。通知会在 broker 恢复后发出。维护窗口抑制在变更发生时判定，而不是在通知最终发出时判定。三个新指标报告积压情况：`alarm_events_pending`、`alarm_event_oldest_age_seconds` 和 `alarm_events_quarantined`。被隔离的事件无法解码或编码，会被保留以供检查并列入死信列表，不会重试。两条新告警会监控这些情况：`AlarmEventQuarantined`（严重）和 `AlarmEventRelayStalled`（某个事件等待超过两分钟并持续五分钟）。`alarm_event_dead_lettered_total` 仍然注册，但不再增长。
- **系统设置会保留您刚刚保存的内容。** 在**系统设置**页面中，若在列表仍在重新加载时保存或重置某项设置，页面会短暂显示其先前的值；此时所做的编辑基于该旧值，因此下一次保存会悄无声息地撤销第一次保存。现在，保存一旦返回，页面就会显示服务器所存储的值。
- **系统设置提供简体中文。** 将控制台设置为简体中文后，**系统设置**界面现已翻译，包括令牌掩码、语言、地图和品牌编辑器。其译文此前已存在但从未加载，因此该界面一直显示为英文。
- **在 `device-management` 无法连接 broker 时，设备删除和权限变更会更快响应。** 以前，当 `device-management` 与 broker 断开时，删除或禁用设备、或更改凭据的调用会被挂起约一分钟，因此位于 ingress 之后的客户端会对一个已经保存的变更收到网关超时。现在，一旦 `device-management` 检测到连接丢失（最多约 40 秒），调用会在几秒内响应；在此之前发起的调用会在检测到连接丢失后响应。删除在保存后即返回 `true`，删除通知会在 broker 恢复可达后发出。如果无法确认设备已打开的 MQTT 会话已结束，调用会返回现有错误（"the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"）。重复该操作是安全的，平台也会在后台重试。已保存的变更所需的每次缓存刷新现在最多挂起调用两秒，然后在后台完成。
- **在 `device-management` 无法连接 broker 时，更改设备类型的配置文件或批量创建设备不再长时间挂起。** 这些变更会逐个向检测引擎通告每个受影响的设备，每个设备一条消息。broker 不可达时，每条消息要等待约五秒才失败，因此一个拥有一千台设备的类型会使调用挂起一个多小时。现在，一旦 `device-management` 检测到连接丢失，变更会在第一条消息被拒绝时停止通告并返回。变更本身无论如何都会保存，未被通告的设备会在检测引擎下一次与 `device-management` 比对时被纳入，该比对每五分钟运行一次。
- **已删除实体的事件保持解除关联。** 在客户、区域、资产、组或设备删除前收到的事件，即使在删除处理完成后才存储，也不会再出现在使用相同令牌重新创建的实体下。
- **捕获的消息会等待分片放置恢复。** 启用分片组时，如果捕获消息的最后一次投递遇到分片放置不可用，系统现在会保留该消息，并在放置恢复后转发，而不是将其记录为不含载荷的死信。此行为适用于捕获流和 MQTT 消息存储。新增的 `placement_held_messages` 指标显示正在保留的最终投递数量。每个来源的保留数量有上限，且仅持续到本次运行结束；关闭时保留的消息不会被确认，因此耗尽投递次数的消息仍可能被记录。滚动升级无需更改通信协议或配置，旧 pod 保持原有行为。
- **实时订阅跟随租户放置变更。** 启用分片组后，在租户放置变更之后新建的实时订阅会跟随租户的新分片组。
- **新安装的实例可以对 `resolved-events` 分片。** 当 `infrastructure.nats.shardGroups.suffixes` 包含 `resolved-events` 时，event-processing 现在为每个分片组运行一个检测工作器，每个工作器有自己的检查点，device-state 读取所有分片组。仅在实例首次安装时支持：在已部署 event-processing 的现有实例上，`dcctl upgrade` 拒绝开始对 `resolved-events` 分片，也拒绝在分片期间更改 `count`；event-processing 在已有检测历史的实例上、在分片组数量或后缀发生变化时、或在启用分片后又关闭时拒绝启动。如果您直接使用 Helm 升级，升级前没有任何检查：请不要将 `resolved-events` 添加到现有实例的配置中。`resolved-events` 分片后请保持租户放置不变。在每个分片组内，检测水位线和定时器的最终确定随该组自身的流量推进。除非您进行配置，分片仍保持关闭。
- **两条通用警告覆盖所有维护任务和处理循环。** 当某个维护任务在任何副本上都没有在其声明的执行间隔的三倍加五分钟内成功过，或在声明了间隔的副本上从未成功过时，会触发 `PeriodicTaskStalled`。当某个处理循环在过去 15 分钟内放弃了某条消息时，会触发 `ProcessorLoopDropping`；计为 `unwanted` 的策略拒绝不会触发它，device-state 的状态循环仍由 `DeviceStateDroppingEvents` 覆盖。两者都是警告级别，新增的循环无需单独规则即可被覆盖。滚动升级期间，仍在运行旧版本的 Pod 会把已禁用设备的流量计为 `dropped`，因此在仍有已禁用设备持续发送数据的实例上，`ProcessorLoopDropping` 最多可能触发约 15 分钟。
- **`PeriodicTaskStalled` 不再对完成频率低于轮询频率的健康任务误报。** 汇总尾部清扫任务每五分钟唤醒一次，但有意只大约每小时完成一次清扫，因此该告警会在每次成功约三十分钟后把这个健康任务误判为停滞。此类任务现在会以 `<task>_expected_success_interval_seconds` 声明两次成功之间预期的间隔，告警以该值与轮询间隔中的较大者为准。告警名称、严重级别和十分钟的持续时间均不变，未声明该间隔的任务的判定方式与之前完全相同。
- **`dcctl broker facts` 列出平台已发布的删除事实。** 删除实体时，`device-management` 会在 broker 上发布删除通知，以便每个持有该实体引用的服务都能移除该引用。`dcctl broker facts <instance>` 以 broker 管理员身份从 broker 读取这些通知。它显示每条通知的存储时间、租户、实体的类型、id 和 token、删除时间以及消息 id，并支持 `--tenant`、`--token` 和 `--since` 过滤条件。无需访问 broker 的磁盘，即可回答"X 的删除是否已发布，何时发布？"。broker 管理员登录仅获得对删除通知的读取权限：新增的访问权限不包含删除、清除或发布消息的权利。实例在使用本版本的 `dcctl` 升级后获得该访问权限。
- **浏览器 MCP 客户端。** MCP 端点现在响应 CORS 预检请求，并公开会话和身份验证质询响应头，允许浏览器客户端使用 bearer 令牌连接。运维人员无需执行任何操作。
- **港口场景可以向平台外投递，并解码 LoRaWAN。** `harborpulse` 模拟现在还会在水位计超过严重洪水水位时调用经过签名的紧急 webhook，通过 MQTT 连接器发布洪水通报，针对每个重大或严重告警给值班人员发送邮件，并在无人确认时再次发送，在应急车辆位于洪泛区地理围栏内时触发告警，根据水位和井深计算每台水泵的集水井充满度，并为四个潮位计模拟 LoRaWAN 网络服务器：它们的上行消息经由 ChirpStack 集成到达，并由租户的负载解码器解码，其中一个解码器是有意设置为错误的。`dcctl sim create --manifest harborpulse` 会将租户置于专用的层级 `hp-showcase`，该层级允许计算指标。投递的目标是集群内的接收端，即新的 `scenario-sink`，位于固定的集群地址：要完成投递，实例需要启用 `outbound-connectors` 区域，并在其出站许可中允许该地址。`scenario-verify` 新增九项不变量的判定，其中五项在提供 `--sink` 时读取接收端的记录。请使用本版本的 dcctl 重新创建现有的 `harborpulse` 模拟，因为其记录中缺少新的端点。
- **港口场景以设备群的方式运行其设备。** `harborpulse` 模拟现在为路灯和水泵打上共享属性，并据此发布四个动态设备组；将集水井提前预警限定于关键水泵；通过两个 SOLAR 活动跟随日出日落，按每盏路灯的安装位置分别规划（日落活动将次日声明为公共假日）；在每个模拟日创建一个将某一码头路灯调暗的 CALENDAR 活动，以及另一个向水泵发送破坏性冲洗命令的活动，并由水泵控制器的维护窗口暂缓该冲洗，直到作业班组的时段开启；同时将其片区建模为资产层级，客户电表位于各自的建筑之下，并通过带类型的 `hp-feeds` 关系将每条馈线连接到其供电的设备。活动和维护窗口按真实时钟运行，因此模拟会在压缩的一天推进时写入当天的 CALENDAR 任务和窗口时段，日照活动则依据平台为每盏路灯规划的内容进行判定。`scenario-verify` 新增八项不变量的判定，模拟器新增八个 `--scenario-defect` 取值，每个用于使其中一项变红。场景的地图坐标现已位于开阔海域，因为虚构的城镇不应与真实城镇重合。
- **Harborpulse 新增签名更新、受限恢复和实体设备更换演示。** 六台充电器在次日凌晨 02:00 接收合成的 2.4.1 固件，每次最多同时更新两台，其中一台故意验证失败。East Quay 在 21:15 恢复供电时会重试真实的摄取限流拒绝，并保留原始读数。第 0 天 12:30，一个水位计更换实体单元和凭据，同时保留逻辑身份。`scenario-verify` 通过平台回读检查八项新增不变量。`dcctl sim create --manifest harborpulse` 现在默认使用 `hp-governed` 层级（每秒 40 个读数、突发容量 256、每个配置文件一项计算指标）；手动创建的模拟也需要使用相同层级才能获得全部通过的结果。握手新增可选字段 `endpoints.updateMgmtGraphQL`；旧记录会从 `/api/device-management/graphql` 推导该端点，自定义路由必须明确提供。每台充电器在停电期间记录约 90 条数据，不受发送间隔和模拟日长度影响。
- **Harborpulse 新增共享阈值、LoRaWAN 命令和计算指标回填验证。** 水泵告警使用各设备的共享属性，并验证白天修改阈值后的行为。带类型的水位计命令通过平台的 LPWAN 下行适配器进入场景接收端的 ChirpStack 队列，返回 Class A 确认，并改变已存储读数的采样间隔。回填任务补齐因最初缺少井深常量而缺失的计算指标。三个新不变量均包含故意植入的缺陷和离线对照测试。测试脚本启用下行适配器和回填工作进程；平台的出站地址仍仅允许场景接收端。
- harborpulse 场景演示告警维护窗口：East Quay 灯具更换驱动器期间，闪烁告警通知被暂存，窗口结束后再发送；North Quay 灯具出现相同闪烁时则立即发送通知。
- **海滨场景的洪泛区现在名副其实。** 在 `harborpulse` 模拟器中，"East Quay flood plain" 地理围栏和工程车库现在位于 East Quay 后方、路灯的内陆一侧，因此控制台地图显示的位置与场景描述一致。卡车仍在 17:45 进入洪泛区、21:45 离开。现有模拟器会在下次配置时移动这两个地理围栏。3D 场景的布局文件现在还包含场景的计划提示时间、卡车路线和一个外包框，模拟器的 `/status` 会报告 `scenario.layoutDigest`，即其生成的布局的 SHA-256。
- **海滨场景的 3D 画面（第一个里程碑）。** `demos/harborpulse-unity` 是一个 Unity 6（URP）项目，按场景自身的坐标绘制 `harborpulse` 模拟器的港区；天空中的太阳、月亮、星星和风暴跟随模拟器在 `/status` 上报告的压缩一天变化。港口水面使用自定义着色器，带有 Gerstner 波浪、泡沫和雨滴，每个码头的水位取平台报告的该码头各潮位计读数的中位数。路灯显示平台最后一次收到的每盏灯的状态：亮、灭或沉默。该场景通过 DeviceChain C# SDK 读取平台数据，从不写入。另有明确标注的离线预览，无需实例即可运行。
- **海滨 3D 场景，第二个里程碑。** `demos/harborpulse-unity` 现在用代码构建码头、泵站（带剖开的湿井）、潮位计、电动汽车充电桩、路灯、建筑和工程卡车，并为港口水面加入调色和平面反射。场景按平台记录的样子展示各项运营：湿井告警原地升级、带平台自身时间戳的水泵启动命令、卡车最后上报的位置、馈线停电及其沉默的设备、同一设备背后被更换的潮位计、路灯检修的抑制窗口，以及充电桩的签名固件推送和每个充电桩的结果。天气、施工队和停电本身作为场景世界绘制，从不显示为平台告警。场景仍然只读。模拟器的 `/status` 现在还会列出故事各阶段及其时间。
- **并发创建模拟器。** `dcctl sim create` 现在跨进程预留自动分配的控制端口，避免并发创建时选择同一端口。创建失败时会释放预留；没有已保存模拟器的遗留预留会在 24 小时后过期。显式指定的 `--control-addr` 值保持不变。
- **通过 dcctl 设置 harborpulse 的一天时长。** 运行 `dcctl sim run <name> --day-length 48m`，将一个压缩模拟日延长至 48 分钟。默认值 `0` 保留场景自身的 24 分钟一天；负值会被拒绝，最短一天时长由模拟器校验。

## 较早版本 {#older-releases}

新增次版本发布线时，最早一条完整记录的发布线会以简短条目移到此处。每个条目链接到该版本文档中的完整说明（保持其发布时的内容），以及其 GitHub 发布页面。所有版本都列在 [GitHub Releases](https://github.com/devicechain-io/devicechain/releases) 上。

### v1.7.0 — 删除会传达到每个服务，会话可安然度过中断 {#v170-upgrade}

<a id="v171-upgrade"></a><a id="v171-before-upgrade"></a><a id="v171-whats-new"></a><a id="v171-rollout-identity"></a><a id="v170-before-upgrade"></a><a id="v170-whats-new"></a><a id="v170-deletion-announcements"></a><a id="v170-deleted-anchor-shares"></a><a id="v170-deleted-device-alarms"></a><a id="v170-deleted-device-commands"></a><a id="v170-deleted-device-state"></a><a id="v170-deleted-device-updates"></a><a id="v170-deleted-entity-anchors"></a><a id="v170-shard-groups"></a>

完整说明：[v1.7 文档](/1.7/deployment/releases-and-upgrades#v170-upgrade)。GitHub 发布：[v1.7.1](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1), [v1.7.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.0)。

### v1.6.0 — 更严格的权限检查，代理重载时设备保持连接 {#v160-upgrade}

<a id="v160-before-upgrade"></a><a id="v160-values-commands"></a><a id="v160-broker-reload"></a><a id="v160-detection-stream-location"></a><a id="v160-solar-campaign-position"></a><a id="v160-action-authorities"></a><a id="v160-rule-ceiling"></a><a id="v160-review-deleters"></a><a id="v160-no-escalation"></a><a id="v160-device-relationship-refused"></a><a id="v160-new-relationship-event"></a><a id="v160-device-uplink-guessing"></a><a id="v160-http-credentials"></a><a id="v160-credential-resupply"></a><a id="v160-signing-key-follow"></a><a id="v160-nginx-snippets-off"></a><a id="v160-ops-port"></a><a id="v160-destroy-instance-name"></a><a id="v160-mcp-memory"></a><a id="v160-mcp-discovery"></a><a id="v160-api-checks"></a><a id="v160-command-holds"></a><a id="v160-campaign-planning"></a><a id="v160-tenant-fairness"></a><a id="v160-escalation-clock"></a><a id="v160-sparkplug-limits"></a><a id="v160-entity-attributes"></a><a id="v160-nats-shard-groups"></a><a id="v160-backup"></a><a id="v160-console-dashboards"></a><a id="v160-console-tab-session"></a><a id="v160-authoring-tools"></a><a id="v160-npm-deprecations"></a><a id="v160-decoder-redelivery-budget"></a><a id="v160-invalid-failures-name-the-device"></a><a id="v160-agent-metrics"></a><a id="v160-other-changes"></a><a id="v160-going-back"></a>

完整说明：[v1.6 文档](/1.6/deployment/releases-and-upgrades#v160-upgrade)。GitHub 发布：[v1.6.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.6.0)。

### v1.5.0 — 自动化变更先评审，代理审批绑定登录会话 {#v150-upgrade}

<a id="v150-before-you-upgrade"></a><a id="v150-dcctl"></a><a id="v150-configuration"></a><a id="v150-input-limits"></a><a id="v150-tightenings"></a><a id="v150-automation-review"></a><a id="v150-agent-actions"></a><a id="v150-lorawan"></a><a id="v150-observability"></a><a id="v150-concurrent-edits"></a><a id="v150-other-changes"></a><a id="v150-going-back"></a><a id="v150-known-issues"></a>

完整说明：[v1.5 文档](/1.5/deployment/releases-and-upgrades#v150-upgrade)。GitHub 发布：[v1.5.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.5.0)。

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
