---
sidebar_position: 4
title: 发布与升级
---

# 发布与升级 {#releases--upgrades}

DeviceChain 以预构建、带版本的容器镜像及 Helm chart 发布。
运行时**不需要**构建任何内容：拉取已发布版本、安装 chart，即可原地零停机升级。

:::warning 某些版本无法直接升级
历史上有四个节点可能要求重建实例，而不是升级：

- **`v0.9.0`** 把每个服务的迁移链替换为单个冻结基线，所以 `v0.8.x` 数据库遇到它会因 `already exists` 失败。
  参见[v0.9.0 基线合并](#v090-baseline-squash)。
- **`v0.10.0`** 改变了事件表的主键，修复无声丢弃遥测的缺陷。
  参见[v0.10.0 事件键变更](#v0100-event-key)。
- **任何由 `v0.16.0` 或更早版本构建的实例**，没有记录实例声明，而升级现在需要读取该记录以决定部署内容。
  参见[v0.16.0 及更早版本构建的实例](#pre-declaration-recreate)。
- **`v0.19.0`：事件存储包含超过 4,000,000 条尚未压缩的行**，或某张表超过 500 个 chunk、且无法移除最旧事件的实例。
  首次启动会重建这些行上的事件键，超过边界就拒绝。
  参见[检查行数](#v0190-row-count)。

如果属于其中任何一种情况，执行其他操作前，先阅读下文对应章节。
:::

:::caution 请经由 v0.23.0 升级
`v0.23.0` 之后的版本以 `v0.22.0` 及更早版本无法读取的形式写入事件。从 `v0.22.0` 或更早版本升级时，请先将每个实例升级到 `v0.23.0`，再升级到之后的版本。参见[事件在代理上的编码格式](#event-wire-format)。
:::

:::caution 跨越 v0.12.0 前需要先做几项修改
`v0.12.0` 支持原地升级，但改变设备答复命令的主题、移动一个权限，
并改变多项接口形状没有变化的行为。无论是否完成调整，升级都会报告成功。
开始前阅读[v0.12.0：改变约定的升级](#v0120-upgrade)。

这适用于**任何**跨越 `v0.12.0` 的升级，而非只升级到它：从 `v0.11.0` 直接升到后续补丁也不会跳过这些变化。
:::

## 版本模型 {#versioning-model}

每次发布对应一个语义版本 Git 标签（`vX.Y.Z`）。
同一版本覆盖**全部组件**：各服务镜像、Operator、Helm chart 和 `dcctl` CLI 都以相同版本发布。
无需考虑逐服务版本不一致：一次部署只有一个统一版本号。

两个命令移动全部组件，分工遵循组件的**生命周期**。
Operator 是每集群一个控制器，由集群上的所有实例共用，因此 `dcctl install` 随集群其他前置组件一起更新它。
配置文档、chart release 和服务镜像属于实例，由 `dcctl upgrade` 逐实例更新。
步骤参见[零停机升级](#zero-downtime-upgrades)。

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

上面的“原地零停机升级”描述滚动升级的*机制*，不是承诺 1.0 之前变更版本后，现有 API 调用仍具有相同含义。

v1.0.0 发布后，本节会替换为通常的语义版本兼容承诺：只有主版本可以包含破坏性变更。

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

## 零停机升级 {#zero-downtime-upgrades}

升级是**两个命令**：先升级集群，再升级其上每个实例。
chart 和服务设计为滚动升级，不丢弃流量。下文记录五种例外：
持久接入切换仍是普通升级，但有可见副作用；**`v0.9.0`、`v0.10.0` 和任何由
`v0.16.0` 或更早版本构建的实例完全无法直接升级**；
以及 **`v0.19.0` 中事件存储超过 4,000,000 条尚未压缩行**，
或某张表超过 500 个 chunk 且无法移除最旧事件的情况，参见[检查行数](#v0190-row-count)。
运行前检查目标版本的发布说明：

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

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
  `mcp` 和 `ai-inference` 从不打开代理连接，因此不挂载任何一个。新凭据的权限与共用凭据相同，因此功能区能做的事情不变；共用凭据暂时保留，回滚仍然可用。
  代理为添加这些用户重启一次，`--ha` 下逐个服务器重启。如果实例声明这些凭据存在而某个功能区缺少自己的那一个，该功能区会拒绝启动，而不是退回共用凭据。
  已有的凭据永不替换：升级只创建缺失的。
- **事件存储 Pod 变化时重启**，例如新镜像或放置位置。仅设置变化无需重启，直接重新加载。
  `--ha` 下先重启备用实例，再把主角色切换过去。
  没有 `--ha` 时唯一实例重启，回来前 event-management 无法存储事件。
  事件在接入流中等待并重试，只有失败重试约四分钟后才放弃。
- **两者可能同时重启。**它们一起应用，升级等待两者健康后才更新服务。

改变事件存储镜像的发布，现在通过这一步到达已有实例：它们会重启到新镜像。
对应发布说明会列出其他要求。

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
要真正零停机，每个支持多 Pod 服务的功能区使用 `replicas: 2` 或更多，让滚动始终有存活 Pod 处理流量。
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

### v0.9.0 基线合并 {#v090-baseline-squash}

`v0.9.0` 是两个无法原地升级的发布中的**第一个**，另一个是 [`v0.10.0`](#v0100-event-key)。

此前每个服务的 Schema 通过依次执行迁移链构建。
`v0.9.0` 把所有迁移链替换为**单个冻结基线**：每服务一次迁移创建当前完整 Schema。
`v0.8.x` 创建的数据库已经应用旧迁移链，遇到基线时会尝试创建已有表，因 `already exists` 失败。
失败在启动时明确发生，不损坏任何内容。

没有迁移路径，`v1.0.0` 前也不会提供。
为仍在变化的 Schema 维护兼容层，正是本项目在所有安装仍属早期阶段时选择不承担的成本。

**迁移到 `v0.9.0`，需要重建实例：**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

使用当前 `dcctl` 时，还要重建集群，不只是实例；参见[原因](#pre-declaration-recreate)。

:::caution 先导出：重建会丢弃数据
[销毁保护](#data-durability)保护数据库免于普通 `helm` 操作，不保护有意执行的 `dcctl destroy`。
如果实例包含需要保留的遥测、设备定义或仪表盘，开始前导出。
本发布没有能够保留这些内容的原地路径。
:::

Schema 变化通常向基线**追加**新迁移，属于普通原地升级。这是规则，适用于几乎所有发布。

:::note 本节曾承诺不会再次发生
它曾说上述合并是“单次发布，而非新政策”。随后 `v0.10.0` 因无关原因也要求重建。
准确规则是：追加迁移是常态，但 `v1.0.0` 前，无法用其他方式修复缺陷时，仍可能要求重建。
**任何这样的发布都会在说明和本页明确指出。**升级前检查两者，不应按版本号猜测。
最新例子是 `v0.19.0`，原因是数据大小而非形状：事件存储超过 4,000,000 条尚未压缩行，
或表超过 500 个 chunk 且无法移除最旧事件，参见[检查行数](#v0190-row-count)。
:::

### v0.10.0 事件键变更 {#v0100-event-key}

`v0.10.0` 是第二个**无法原地升级**的发布，原因不同于基线合并。

事件过去按租户、设备、类型和时间戳组合标识。但该组合不唯一：
设备采样两个传感器，在同一时间戳分别发送消息，会产生两个真正不同、却在数据库看来相同的事件。
第二个无声丢弃，其读数存入第一个事件；被丢弃后无法识别为重复，因此每次后续重试都会再添加一份读数。

任何只标记整秒的设备在一秒内发送两次都可能触发此问题，发布的 .NET SDK 在本版本前也使用整秒。

`v0.10.0` 为每个事件、读数、关系记录赋予由自身内容派生的身份，并把它作为键。
正确存储遥测需要改变系统最大几张表的主键，而这些表已压缩，数据库引擎无法原地修改压缩数据上的键。
没有保留现有行的升级路径。

**迁移到 `v0.10.0`，需要重建实例：**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

使用当前 `dcctl` 时，还要重建集群，不只是实例；参见[原因](#pre-declaration-recreate)。

与上文相同：重建丢弃遥测、设备定义和仪表盘，开始前导出所有需要内容。

同时有两项 API 时间报告变化，无需操作：

- 时间戳按记录精度返回；以前输出时向下取整到秒，使相差 200 毫秒的读数看起来同时发生。
  整秒时间戳的线格式不变。
- 使用记录 `updatedAt` 避免覆盖他人编辑的请求，也按相同精度检查。
  过去一秒内的两次编辑都可能通过检查，后者无声覆盖从未看到的变化。

### v0.11.0：恢复普通升级 {#v0110-upgrade}

`v0.11.0` 是 `v0.8.5` 以来第一个可原地升级的版本。
Schema 变化**增加**三个迁移，不替换基线，因此现有 `v0.10.0` 数据库完整保留行继续向前，无需重建。

数据库新增：

- 记录租户删除进度和历史的两张表；
- 租户表上跟踪生命周期状态的两列。

添加列时所有已有租户都设为正常活动状态，因此真正删除租户前，运行实例行为不变。

:::note 测试了什么，没有测试什么
发布前执行了两项检查，测量内容不同，应区分。

**有数据数据库上的迁移。**构建 `v0.10.0` Schema，填充代表性行，然后迁移。
每行逐字节不变，最终 Schema 与全新 `v0.11.0` 安装完全相同，覆盖每个功能区，不仅变化的那个。

**运行中实例上的升级。**从已发布 `v0.10.0` 镜像构建实例，添加真实租户和身份，再按上述命令升级。
全部服务完成滚动，67 张表的行数除新增迁移条目和产生的审计记录外不变；
`v0.10.0` 创建的账户仍可登录，升级实例的新租户删除 API 可以响应。

明确列出四项限制：

- **只验证数据库。**两项检查都不覆盖代理 JetStream 状态、对象存储和键值状态。
- **只验证 PostgreSQL 16。**新安装验证两个受支持主版本，升级路径只在 16 上测量。
- **逐行比较来自第一项，不是第二项。**运行实例只比较行*数*，无法发现原地修改而非删除的行。
- **第二项中控制台仍使用 `v0.10.0` 镜像**，所以未在升级实例上验证 `v0.11.0` 控制台。
:::

### v0.12.0：改变约定的升级 {#v0120-upgrade}

`v0.12.0` 可原地升级，Schema 变化**追加**迁移而非替换基线，
现有 `v0.11.0` 数据库完整保留行继续向前。这在运行实例上实测过，而非仅推断。

变化的是**约定**：设备答复命令的 MQTT 主题、若干 GraphQL 操作，以及形状没变化的多项内容含义。
这些不会在“升级成功”中体现，因此运行前阅读本节。

#### 升级前完成这些操作 {#do-these-before-you-upgrade}

**1. 更新所有响应命令的设备。**发布命令响应的主题现在限定于设备：

```
# before
{instanceId}/{tenant}/command-responses
# now
{instanceId}/{tenant}/command-responses/{deviceToken}
```

设备签发凭据不再允许旧主题，因此未更新设备的响应会被代理拒绝。
设备仍收到并执行命令，但平台永远无法记录执行结果，所有命令最终超时。

原因是旧主题允许租户内**任何**设备发布**任何**命令的响应，包括发给其他设备的命令。
响应没有指出发送者，无法区分。现在设备 token 属于主题，而主题属于代理签名内容，因此设备只能为自身答复。

能先升级设备就先做。切换期间旧主题响应被拒绝，不排队；升级瞬间少量已在传输中的响应会丢弃，不投递。

**2. 重命名 ID 恰好为 `lwm2m` 的事件源。**LwM2M 服务用此值记录自身设备在线状态，
在线记录按完全相等匹配，因此该来源与服务会互相覆盖行。
`event-sources` 现在拒绝在这种 ID 下启动，会停止整个实例接入。

只是*看起来*像传输的 ID，例如 `sparkplug:plant-a` 或 `lwm2m:site-a`，现在启动时警告而非拒绝，方便时重命名。
两种情况都注意：旧 ID 下的在线记录不迁移，也不会回填。

**3. 检查谁读取位置历史。**返回设备位置的查询现在需要 `location:read`，而非 `event:read`。
此权限不在查看者只读基线中，因此 `v0.11.0` 能读位置历史的账户在 `v0.12.0` 可能无法读取。
给需要它的角色明确授权。

同一权限现在还控制**预览测试围栏包含关系的规则**。
这种预览逐设备返回进入、离开区域的时刻，无论如何请求，本质都是读取位置，
因此 `previewRule` 除所有预览需要的 `device:read` 外，还需 `location:read`。
原本能预览所有草稿的作者，在授权前会被拒绝包含关系草稿。无包含关系的预览不受影响。

**也检查通过 AI 助手读取位置的人。**MCP 新增 `query_locations`，返回设备报告的位置，
访问需要明确分开的**两项**授权：代理授权包含新增 OAuth `location` scope，
*且*授权人拥有授予 `location:read` 的角色。任一项单独不足：scope 是令牌能携带能力的上限，不是权限授予。

仅授权 `read-only` 的代理，无论用户权限多大，都不能读取位置。
单独 scope 而非扩大 `read-only` 是有意的：同意页面显示原始 scope 字符串，
把位置并入 `read-only` 会让前后外观相同的授权现在包含设备、常常也包含携带设备的人的历史位置。
分开意味着授予代理可观察性与授予位置历史不是同一操作，用户可以允许其一、拒绝另一项。

已注册 MCP 客户端仍可工作，但仍不能读位置，直到授权请求加入 `read-only location`，用户重新授权。
查看者基线不变：成员仍不默认获得 `location:read`。参见 [AI 访问（MCP）](../concepts/mcp.md)。

**4. 检查针对 API 编写的代码是否使用以下 GraphQL 操作：**

| 操作 | 变化 |
| --- | --- |
| `createCommand` | 返回 `CreateCommandResult!`，不再是 `Command!`。命令位于 `command` 字段，旁边的 `rejection` 解释拒绝。 |
| `updateDeviceType` | `request` 现在必需，类型为 `DeviceTypeUpdateRequest!`，语义改为**部分更新**。省略字段保留存储值而非删除，明确 null 清除。过去靠省略清除字段的客户端需发送 null；反过来，改类型名称不再解除其设备解析能力所用的配置文件。读取类型再发回全部字段的旧完整记录客户端仍可工作，仍写入所发送内容。输入移除 `token`，不能再移动类型 token。未知字段拒绝而非忽略。 |
| `assertedActiveDeviceStates` | 替换为 `assertedDeviceStates`，接受 `activeOnly`，通过 `afterId` 和 `pageSize` 分页。 |
| `deviceCredentials`、`deviceCredentialsById`、`deviceCredentialsByToken` | 现在需要 `device:write`。某类凭据的可读标识符本身就是 bearer token，因此以前每个启用成员拥有的 `device:read` 足以作为租户任意设备建立代理会话。 |
| `locationEvents` | 如上，需要 `location:read`。 |
| `geoFenceSetSnapshot`、`currentGeoFenceSet` | `fences` 改为分页，接受必需 `pagination`，返回 `results` 和 `pagination` 记录，不再是普通列表。读取至 `pageEnd` 达到 `totalRecords`。文档规定上限下的围栏集超过单次响应容量，因此最可能查询的大租户原本完全无法返回列表。 |
| 任意 `...ById(ids: [])` 查询 | 空 ID 列表现在返回空结果，过去返回整张表且不分页。 |

**5. 不再给实体 ID 补前导零。**`id` 参数现在只按十进制解析。
过去从字面量推断进制，所以固定宽度格式的 `"017"` 被当作**八进制**，成功返回第 15 行这个错误实体，没有错误提示。
`"0x2"`、`"0b101"`、`"1_0"` 同样被接受。现在四种形式都直接拒绝，应发送 `"17"`。

**6. 预期每个服务 Pod 都滚动一次。**交给服务的实例配置文档现在移除部署未启用功能区的坐标。
没有 `ai-inference` 的部署，即除了 `full` 之外所有 profile，文档字节及滚动所用 checksum 注解都会变化，
所以所有服务重启，而非仅镜像变化者。这是普通滚动更新，无需操作；说明它是为了避免把全量滚动误认为异常。

移除坐标，是因为指向未部署服务的主机名比没有主机名更糟。
规则编写界面会据此构建自然语言“Describe”入口，解析主机名失败后，
却报告租户未同意外部 AI 路由，把运维人员从未安装的服务归咎于租户设置。
现在准确说明当前部署未启用此功能。

#### 签名未变化的行为变化 {#changes-with-no-signature-change}

这些是客户端无法通过查看 Schema 发现的变化。

**更新设备配置文件会清除位置声明。**配置文件现在能声明其设备报告位置，
而 `updateDeviceProfile` 替换整个配置文件。针对 `v0.11.0` 编写的客户端不发送新增字段，
所以任何更新，例如改名或编辑描述，都会无声撤销该配置文件所有设备的位置声明。
唯一症状是地图界面不再更新。发送该字段，或在旧客户端每次更新后重新设置声明。

:::note[此建议已不再适用]
`updateDeviceProfile` 后来改为[部分更新](../reference/graphql-api.md#which-mutations-are-partial-updates)：
请求不提声明就保持原样，清除需明确 `null`。
上文建议适用于 `v0.12.x` 或 `v0.13.x`；当前实例无需额外带上字段。
配置文件改名有[独立变更](../reference/graphql-api.md#renaming-a-record)。
:::

**窗口检测规则不再计入窗口之外的缓冲读数。**重复、滑动聚合和关联规则过去纳入任意过去时刻的读数，
导致“十秒内三次读数”能被相隔一小时的读数触发，常见于存储转发设备上传缓冲。
现在这些规则像滚动窗口和会话规则一样，丢弃所属窗口已经过去的迟到读数。
批量上传设备群在这些规则下应预期**更少**告警，查看 `detect_late_samples_total` 了解丢弃量。
读数仍照常存储和绘图，只影响检测。参见[运行检测引擎](./detection-engine.md#timing-what-when-means)。

**围栏几何验证更严格，存储按规范化结果而非发送原文。**创建和更新围栏时有三项变化：

- 位置必须恰好是 `[longitude, latitude]`，过去接受并忽略第三及后续坐标。
- 几何文档只能包含平台读取的键：顶层 `kind`、`geometry`，内部 `type`、`coordinates`。
  过去其他键存储后从未被读取。
- 坐标存储前改写为普通十进制表示。`1e-300` 读回为完整十进制展开。
  不舍入任何值，不改变围栏形状，但读回文档不再与发送内容逐字节相同。

存储形式超过 32 KiB 的围栏现在也会拒绝，约为使用所有允许顶点的围栏大小两倍，普通几何不受影响。
它拒绝的是因数值表示而非形状造成超大的文档。
控制台一直使用符合要求的位置形式，因此控制台绘制围栏不受影响。
已有围栏**不重写**且继续工作，但违反上述规则者下次保存会被拒绝。

**取消命令记录 `CANCELLED`。**过去记录 `EXPIRED`，与自然到期命令共用状态。
通过 `EXPIRED` 识别主动取消的代码现在不再找到它。

**命令现在可处于 `HELD` 或 `PARKED`。**平台知道设备不在线时扣留，不发布；
已经派发却发现设备不可达时停放。两者都在等待，并未结束，且都是新状态。
把除 `QUEUED`、`SENT` 外所有状态视为终结的代码会出错。
完整状态集现在是 `QUEUED`、`HELD`、`SENT`、`PARKED`、`SUCCESSFUL`、`FAILED`、`TIMEOUT`、`EXPIRED`、`CANCELLED`。

**读数按采样时刻存储。**多样本消息各自携带时间戳，例如所有 Sparkplug、LwM2M 上传和离线缓冲设备，
过去按消息到达时刻存储；现在按自身时刻存储。
上传一小时缓冲读数时，它们分布在该小时，而非集中在上传瞬间，历史、图表、保留和检测都按真正所属时间处理。

**停止运行的在线状态来源现在交还设备。**`ASSERTED` 设备过去无限保留最后状态：
不活动扫描跳过明确声明的设备，数据事件也不能改变它；来源消失时在线设备永久显示在线，离线设备命令永久扣留。
代理明确报告的 MQTT 在线状态，现在被有意禁用或缺少 NATS 系统账户凭据时释放设备，
回到 `INFERRED`，不明确宣称任何连接状态。
适用实例会为每设备产生一个有节奏的状态变更事件，计入 `presence_events_total{state="demoted"}`，
设备重新受十分钟不活动扫描管理。
Sparkplug 和 LwM2M 没有自动释放；`dcctl presence demote` 和 device-state 新增
`demoteAssertedPresence` 可以手动处理任意来源。
该变更需要默认角色都没有的新增 `state:demote` 权限。
新增 gauge `presence_tap_off{reason}` 表明代理在线状态跟踪是否在运行；以前安静设备群和从未启动的跟踪从外部看一样。
参见[让设备恢复推断在线状态](./edge-services.md#demoting-a-device)。

**重新投递的读数不再重复行。**测量事件身份来自其内容摘要，使重新投递无害。
JSON 传输中的多指标读数，过去按平台自行生成的顺序计算摘要，而非设备发送顺序，
约五分之四会解析为不同身份。未确认发布或短暂写入失败时，平台经常重新投递，
重复无法识别，测量行写入第二次，每小时汇总重复计数。
单指标读数以及 Sparkplug、LwM2M 从未受影响。
修复只向前生效：升级前重复行保留，汇总仍偏高。
多指标设备图表过去偏高的原因可能就是这个，升级后新数据会正确。

**每个分页列表现在使用声明的顺序。**37 个列表端点中，31 个没有任何顺序，
可能在两页返回同一行却遗漏另一行，这个实际缺陷已两次被报告为操作界面重排。
现在各列表都按完整、无歧义键排序。
依赖某个查询偶然顺序的代码，现在看到稳定顺序，但可能不同于以前。
设备凭据排序是有意选择，而非机械处理：剩余有效期最长者优先，
因为无限制读取用于凭据复用；按 ID 排序会先返回最接近过期者。

**纯文本命令答复现在记录内容。**设备用非 JSON 内容，例如 `acknowledged` 或普通状态词答复，
过去导致数据库类型错误，命令留在 `SENT`，终生每分钟重试同一个注定失败的写入，最终对正确答复的设备记超时。
现在答复无损保存为 JSON 字符串。
**API 调用者**提供的值不变，仍需有效 JSON，因为发送无效 JSON 的调用者应该被告知。

**发给 Sparkplug 设备的命令现在立即失败，而非丢失。**平台没有到此类设备的命令路径，
节点位于你自己的 MQTT 基础设施，没有桥接。
原本应拒绝的检查比较了设备从不携带的值，什么也匹配不到，命令全被接受后悄悄无处可去。
现在立即记录 `FAILED` 并说明原因，计入 `command_delivery_undeliverable_total`。
过去等待 TTL 后 `TIMEOUT` 的命令，现在应预期迅速失败。参见[命令](../concepts/commands.md)。

**平台失去跟踪的命令重新激活，而非归咎设备。**发布 Pod 在记录结果前终止，命令可能达到
`SENT` 却无人再处理；过去只等 TTL，对从未收到任何内容的设备记 `TIMEOUT`。
后台扫描现在找到并重新激活为 `PARKED`，在设备下次唤醒时投递。
`command_delivery_stranded_recovered_total` 的 `{disposition}` 说明各命令最终状态。
**只适用于 LwM2M**：普通 MQTT 中，看似无处可去与已经到达但答复丢失无法区分，行为不变，
`command_delivery_stranded_skipped_total{reason="transport"}` 的持续速率并非故障。
参见[平台失去命令跟踪时](../concepts/commands.md#stranded-commands)。

**永远无法投递的规则动作丢弃而非重试。**REACT 动作因无法通过重试改变的原因被拒绝，
例如 `sendCommand` 指向已不存在设备，或不在设备已发布命令词汇中的命令，
过去重试到重新投递上限再算毒消息，把编写错误和基础设施故障混为一谈。
现在第一次永久拒绝就丢弃，计入按动作类型标记的 `react_actions_permanently_rejected_total`。
持续增加表示规则指向设备不能接受的内容，原来被它推高的毒消息计数终于反映真实含义。

**跨服务响应截断会计数。**服务最多读取彼此响应 1 MiB，超限过去无声截断。
现在计入按 peer 标记的 `devicechain_svcclient_responses_truncated_total`。
应保持零；非零说明某服务正在按部分答复行动，在症状到达界面前值得了解。

#### 过去接受、现在拒绝的输入 {#input-that-used-to-be-accepted-and-now-is-not}

- 携带 `deviceTypeToken` 的通知策略。按设备类型限定策略未实现，过去写入成功却不投递任何内容。
- `severity` 不是大写等级或 `*` 的通知规则。过去小写严重级别能写入并原样读回，却永不匹配告警。
- `occurredTime: 0001-01-01T00:00:00Z`。它是有效时间戳，但平台保留它表示没有报告时间。
- 使租户超过**扣留命令上限**的入队。不在线设备的扣留命令没有自然刹车，休眠设备群积压可停留数天，过去无限制。
  现在依次解析租户覆盖、等级覆盖、平台默认 10,000，各层都没有无限制值。
  拒绝携带 `HELD_CEILING_EXCEEDED`，是入队门控唯一临时拒绝，设备回来后释放额度。
  把每个拒绝视为永久的客户端应对此特殊处理。参见[租户能保留多少积压](../concepts/commands.md#held-command-ceiling)。
- 使租户超过该上限中**为投递保留份额**的入队。默认 20% 留给平台自身命令投递，
  避免一次设备群写入占满，导致所有自动 `sendCommand` 在积压排空前被拒绝。
  控制台、SDK、`dcctl` 和你的集成等所有代你发命令者都受剩余份额约束。
  实际结果是过去完整接受的大批次可能部分拒绝；允许部分扇出的批次记录会说明哪些设备不适合额度。
  参见[部分上限保留给投递](../concepts/commands.md#delivery-machinery-reserve)。

#### Bootstrap 和 CLI {#bootstrap-and-the-cli}

这些通过 `dcctl bootstrap` 和基础设施 apply 到达实例，而非通过 release，
所以不会在上述升级中生效。列在这里是因为它们改变失败行为。

**代理配置改变现在重启代理。**`nats-server` 不能热加载 authorization-callout 块或 JetStream 限制，
且会拒绝整个 reload，包括同次 apply 的所有无关变化。
外部看到 apply 成功、ConfigMap 有新值，运行代理却仍使用启动配置，唯一证据是代理日志一行。
服务认证失败，而 ConfigMap 又“证明”凭据正确。
现在 StatefulSet Pod 模板携带渲染配置 hash，服务器始终按收到文件启动。
代价是配置变更会滚动 Pod，而过去只有 chart 或镜像变化才滚动。
按每 Pod 约 50–70 秒预算，单服务器短暂全停，三服务器滚动重启。

**第三方 chart 版本已固定。**过去 `ingress-nginx` 和 `cert-manager` 安装仓库最新版本，
让 chart 仓库同时成为*计划*和应用的依赖。发布资产主机返回 503 时，
计划错误既不指出 chart，也不指出网络，经过两次失败 bootstrap 才找到原因。
现在分别固定为演练集群使用的 `4.15.1` 和 `v1.21.1`。
过去依赖自动取得新版本的人，现在须有意升级。

**自行构建的 `dcctl` 现在有可用默认镜像标签。**`make -C backend/cli build`
过去从仓库 `VERSION` 文件取默认标签，那个值从未被发布设置，也从未推送镜像。
bootstrap 一路报告健康进展，几分钟后所有工作负载才进入 `ImagePullBackOff`。
本地构建现在默认 `dev`，未发布版本保护识别后尽早明确拒绝，而非晚些返回难懂失败。
发布的 `dcctl` 从未受影响，它的标签来自发布本身。

#### 配置 {#configuration}

一个键移动了。`maxEventFutureSkewSeconds` 限制设备报告时间比平台时钟领先多少，
过去属于 `event-processing`，现在属于 `device-management`，因为实时检测和回放的事件时间现在由同一处决定。

仍在 `event-processing` 设置它的配置**正常启动**，并警告新位置。
旧值不生效；如果修改过默认 300 秒，请改设到 `device-management`。

chart values 没有移除任何内容，`v0.11.0` values 文件可原样应用。

:::caution 此次升级期间暂停设备流量，否则可能需要重置检测引擎
边界移动，滚动期间**两侧都可能没有实施它**。
`v0.11.0` 只有检测引擎限制设备时间，`v0.12.0` 只有事件解析限制。
两个服务独立滚动，因此可能新 event-processing 已替换，旧 device-management 仍发布，窗口内事件双方都不检查。

如果一条事件带极端未来时间到达，检测的全实例统一时间前沿会被推进，所有租户待处理计时器一起触发。
恢复需要重置引擎快照。

**这仅是一次升级边界，不是持续弱点**。
两服务都在 `v0.12.0` 后永久一致，销毁重建实例从不暴露。
设备发送中进行原地升级时，请停止设备流量完成滚动，或准备事后重置检测快照。
:::

**拒绝自身配置的服务现在以非零状态退出。**过去记录“refusing to start”后以 0 退出，
Pod 显示 `Completed`，与正常关闭一样，乍看无法区分。
现在进入 `CrashLoopBackOff`。
拒绝哪些配置没有变化，只是拒绝现在能在 `kubectl get pods`、重启数和相应告警中看到。
无法干净关闭的服务也因相同原因这样报告。
如果告警把 `Completed` 服务 Pod 当作正常，本发布开始让底层失败到达你的监控。

### v0.12.1：补丁，无需操作 {#v0121-upgrade}

`v0.12.1` 是从 `v0.12.0` 的普通原地升级，无新增迁移，不接触数据库；
API、主题、权限、配置键均无变化，上文 v0.12.0 描述仍完全适用。

两项值得了解的修复：

- **控制台状态颜色**现在在明暗主题都满足 WCAG AA 对比度。
  `pending`、`online` 徽章过去两种主题都不达标，错误文字暗色模式也不达标。
  颜色*含义*不变，但填充徽章明显变深，因为这样白字才能可读。
- **不活动监视器**不再每轮把所有租户全部设备读入内存，也不再为每个切换设备单独往返数据库，
  而是用一条语句决定和写入。设备仍按完全相同时间表转为不活动，这是成本变化而非行为变化，
  对大型设备群以及在线状态来源刚交还设备时尤其重要。

### v0.13.0：围栏限制成为套餐设置 {#v0130-upgrade}

`v0.13.0` 是普通原地升级，不改变主题、权限、配置键。

数据库采用增量变化：创建围栏形状表，给租户记录增加三个可空列，并一次性原地把围栏历史重写为新形式。
不删除任何内容，无需重建。

已有围栏用户应了解最后一步。从 `v0.13.0` 起，围栏形状只存一次，按内容引用，
不再复制到每个围栏集版本；升级把已有历史重写为同样按形状引用。
围栏和历史内容不变，只改变存储方式。步骤可安全重跑，已执行实例上不做任何操作。

过去统一固定的两个限制，每围栏 512 个位置、每租户 100 个围栏，现在成为**套餐设置**，
并新增整个围栏集总位置数限制。三者默认保留此前额度，
所以**从未修改过设置的租户仍按原限额计量**，无需操作。

升级前了解两点：

- **新总量限额默认就是前两者隐含的值**：51,200 个位置，即 100 个各 512 的围栏。
  正好按文档限额使用的租户*达到*新上限，绝不超过。
  总量计数**不同**形状，因此两个完全相同的围栏只计一次。
- **只有让数量增大的变化才拒绝。**运维人员后来把限额降到已占用量以下时，所有围栏仍保留。
  编辑名称、描述和删除始终可用，检查增长而非大小，防止套餐变化使创建时合法的围栏无法使用。
  缩小围栏通常也可行；例外是总量按*不同*形状计数，
  修改多个相同围栏中的一个会使它与其他分离，即使该围栏变小也可能让总量增长。

规划时注意：删除围栏会降低存储总量，超限租户删除后不能重新创建。
要改 token，**先创建新的，再删除旧的**，需要两者同时存在时一个额外围栏槽位。

制定等级的运维人员应知道这些有真实上界，不只是租户独享的额度：
总量是所有租户共用几何缓存的一部分，围栏数限制需要放进单条代理消息的公告大小。
拒绝会指出数量及需提高的设置，`geofence_cap_refusals_total` 按拒绝的限额计数。

### v0.14.0：构建所依赖的包 {#v0140-upgrade}

`v0.14.0` 是从 `v0.13.x` 的普通原地升级，无新增迁移，不接触数据库，
不改变 API、主题、权限、配置键。**仅运行平台者无需操作。**

变化在外围：构建依赖的制品，以及运行平台的 CLI。

**Web 运行时已发布。**`@devicechain/client`、`@devicechain/dashboards`、
`@devicechain/widgets`、`@devicechain/brand` 已在 npm 上，
自己的应用嵌入仪表盘或组件只需安装，无需针对源码树构建。
四者同版本一起发布，并相互固定版本。安装命令和 dist-tag 政策见[npm 包](../reference/npm-packages.md)。

**从源码构建组件者，需要自行完成一项修改。**`maplibre-gl` 现在是 `@devicechain/widgets` 的 peer dependency，
由你的应用提供库、Worker URL、样式表，不再由组件包决定。
这让包能用于项目无法控制的打包工具，但没有宿主接线的地图现在显示明确提示，而非空白画布。
升级未接线就会看到该症状。[渲染地图](../reference/npm-packages.md#map-host-wiring)列出简短接线步骤，服务器不变。

**.NET 和 Unity 客户端 SDK 已发布**到 nuget.org，名称 `DeviceChain.Sdk`。

**`dcctl` 能列出已 bootstrap 的内容，并关闭全部实例。**

```bash
# every instance, the cluster it lives in, and whether that cluster is still there
dcctl instances list

# tear down all of them
dcctl destroy --all
```

🔴 **这修复了一项需要行动而非仅了解的缺陷。**
此前没有记录实例 bootstrap 到哪个集群，在创建和销毁时都从实例名称推导。
任何用 `--kube-context` bootstrap 的实例都会推导错误，且失败方向很糟：
`dcctl destroy` 请求 provider 删除不存在集群，无声成功，移除本地状态并报告销毁，实际集群仍运行。
**如果你曾用 `--kube-context` bootstrap，之后销毁该实例，其集群很可能仍在运行。**
销毁已移除记录，所以 `dcctl instances list` 无法显示。
直接询问 provider，本地使用 `kind get clusters`，删除你确认的集群。

本发布起，bootstrap 记录集群，destroy 读回。
结束时明确说明三种结果：已删除记录集群；集群已不存在，仅清本地状态；或记录不可信，什么也未修改。
不再在仍运行集群上打印旧成功句子。

旧版创建的实例无记录，列表显示 `no record — destroy will guess the cluster`。
仍可销毁，回退到旧推导，所以上述注意事项继续仅适用于它们。

:::note `dcctl destroy` 不再删除集群
当前发布中，它只移除实例的 Helm release、NATS 代理和事件存储、数据库与登录、命名空间及本地状态，
绝不删除集群或 `dcctl install` 放置的前置组件。
所以 `dcctl destroy --all` 移除所有实例，但让所有集群继续运行。
删除本地集群使用 `kind delete cluster --name <name>`，参见[移除实例](./bootstrap.md#destroy)。
:::

### v0.15.0：更新不再删除未发送的内容 {#v0150-upgrade}

`v0.15.0` 是从 `v0.14.x` 的普通原地升级。
服务启动时自行执行新迁移，无需重建，无需手动移动数据。

破坏性变化在 **API** 和**出站网络访问**，不是升级本身。
通过控制台运行平台者无需操作。
下文面向直接调用 API、通过内部网络发送通知、运行 MCP 服务器或自定义 event-sources 配置的人。

#### 更新操作不再替换完整记录 {#update-operations-no-longer-replace-the-whole-record}

这是影响最多人的变化，也是本发布标为破坏性的原因。

过去更新替换整个记录：**省略字段都会删除**。
现在未提及字段原样保留，清除必须明确 `null`。

请求形状改变，不再携带记录自己的名称：更新和重命名分离，
需要改名的四种类型有专用 `rename…` 变更。
直接调用 API 的应用必须**从更新请求移除名称，并重新生成客户端代码**。

**旧形状请求直接拒绝**，错误指出不再接受的字段。
不会部分应用，也不会无声失败，所以第一次调用就发现，而不是等记录丢失一半内容后才发现。

:::caution 唯一无声变化的情况
通过**省略字段**清除值的应用，现在会保留旧值。
没有错误，只是更新比以前做得少。依赖省略清除者应改发明确 `null`。

并非所有字段接受 `null`，有些必需字段会明确报错，它们本来就不应被清除。
:::

:::danger 值得了解的一个陷阱
如果按**每字段一个独立变量**绑定更新请求，未提供的变量会变为**明确 null**，而非字段缺失，
明确 null 表示*清除*。通知策略 `rules` 因此会清空整组规则并返回成功。
将整个请求对象绑定为一个变量，或只包含真正想改的字段。
:::

#### 存储事件的 ID 改变 {#the-id-on-a-stored-event-has-changed}

事件 `id` 现在是事件自身标识，不再由设备 token、事件类型、时间戳组合。
**以前保存的 ID 不再匹配任何内容。**

旧形式也不唯一：同一设备同一瞬间报告两个测量，会得到**相同 ID**，
以它为键的规范化缓存会无声合并读数。保存过 ID 的请重新读取；以它为键的，这既是破坏变化，也是正确性修复。

#### 现在拒绝到私有地址的出站连接 {#outbound-connections-to-private-addresses-are-now-refused}

通知 webhook、**SMTP 中继**、连接器 HTTP 调用，不再能到达回环、私有、运营商级 NAT、链路本地或云元数据地址。
连接时检查，拒绝是**最终的，不重试**。

默认开启，没有关闭开关。

:::caution 集群内邮件中继会停止告警邮件
这是最容易意外遇到的失败，看起来不像网络策略变化：通知停止，失败记为永久而非等待。
允许所用具体地址：

```yaml
instance:
  config:
    infrastructure:
      egress:
        allowedDestinations:
          - 10.96.0.25/32      # the in-cluster SMTP relay
```

每个目标使用独立 `/32`。公共互联网目标不受影响，无需条目。
:::

#### 运行 MCP 服务器时 {#if-you-run-the-mcp-server}

两项需要操作，其中一项阻止服务启动：

- **资源 URL 末尾斜杠现在在启动时拒绝。**标识符精确比较，末尾斜杠让令牌绑定到始终无法准确匹配的地址。
  过去接受后无声不匹配，现在启动明确失败。移除斜杠。
- **受保护资源元数据移动**到规范定义位置，well-known 片段在主机与路径之间。
  chart 自动路由；**自行终止 Ingress 时，为 `/.well-known/` 前缀加一条不重写路径的路由**。

#### 移除的两个配置键行为不同 {#two-configuration-keys-were-removed-and-they-behave-differently}

- **`eventSources` 条目内的 `debug`**：严格配置验证使保留它**阻止 event-sources 启动**，错误指出字段。请移除。
- **`inboundEventBatching` 及其 `maxBatchSize` / `batchTimeoutMs`**：退役而非拒绝，加载时移除并警告，服务正常启动，方便时移除。

区别并非任意：退役键仍可按名称识别，所以可替你移除；列表项内部嵌套键无法这样处理，因此第一项必须停止服务。

#### 本发布其他内容 {#also-in-this-release}

命令入队后立即派发，不等下一扫描；扫描间隔可配置，用于改变安全网运行频率。
死信可以读取和查询，不再只能计数。有可供 BI 工具使用的报告视图。
资产新增父子层次和文档化属性约定，设备新增替换操作，告警新增批量确认，租户可选择控制台打开语言。

本发布 npm 包和 .NET/Unity SDK 无源码变化，但自己的代码通过它们发送更新变更时，仍需自行重新生成。

### v0.16.0：设备必须指定所答复的派发 {#v0160-upgrade}

`v0.16.0` 是从 `v0.15.x` 的普通原地升级。
command-delivery 启动时自动执行一个新迁移，添加带默认值的列，并在同一语句回填已有行，无需操作。

有**一项值得升级前执行的预检**，以及影响设备而非 API 调用者的一项破坏变化。
其他主要是服务拒绝过去接受却无声忽略的配置；这更安全，也可能停止已正常运行数月的 Pod。

#### 升级前检查监听端口冲突 {#before-you-upgrade-audit-your-listener-ports-for-a-collision}

event-sources 一个进程运行多个 HTTP 监听器，GraphQL 独立端口加每个配置的 HTTP 事件源。
过去两个落在同一端口会**无声**杀掉一种接入传输：失败监听器在 goroutine 中退出，再无提示。

现在同步绑定，失败致命，所以已无声损坏数月的冲突会**让部署进入崩溃循环**。
这是正确行为，也是最容易意外遇到的变化，因为此前没有警告。

检查每来源 `port` 与其他来源及 GraphQL 端口，确认 chart `extraPorts` 与来源自身 `port` 一致。
面向设备的 `port: "0"` 现在也拒绝。

#### 响应命令的设备必须回显派发 nonce {#any-device-that-answers-a-command-must-echo-the-dispatch-nonce}

这是本发布唯一破坏性线协议变化，**影响本仓库之外构建的设备**，包括固件、网关和直接使用命令协议的组件。

投递信封包含 `dispatchNonce`。设备答复时必须在响应信封回传相同值。
省略它或指定命令已离开的派发，会**拒绝并记录为死信**，不会结算命令。

原因是实际缺陷：同一命令可以合法地发布多次，例如释放回队列后再次派发。
没有 nonce 无法区分答复属于哪次派发，过时派发的答复会用旧结果结算较新派发。

:::caution 如何判断设备群是否安全
平台无法列出外部构建设备，因此通过计数帮你判断。升级后监视：

- **`devicechain_commanddelivery_command_delivery_responses_without_nonce_total`**：
  未指定任何派发的答复，大多来自未更新设备；全部使用当前约定时应为**零**。
  它计入任何无 nonce 答复，因此已结算命令的重复或迟到答复也会计入。
- **`devicechain_commanddelivery_command_delivery_responses_stale_nonce_total`**：
  指定命令已离开派发的答复。通常意味着命令发布多次，正是该变化修复的缺陷；设备重放旧发件箱条目也会产生。

（重复的 `command_delivery` 不是拼写错误，指标前缀包含平台命名空间和功能区，查询应粘贴上述完整名称。）

被拒绝答复**不丢弃**，而是写入死信，因为设备对执行结果的报告不存在于其他地方。
因此处理第一个计数器时仍可找到这些答复。
:::

使用 **.NET/Unity SDK** 的设备，只需升级 SDK，它会在两个方向替你传递 nonce。
LwM2M 下行适配器和设备模拟器也同时更新。
边缘代理不受影响，它只发送遥测，不接收命令。

新增相关计数器 `devicechain_commanddelivery_command_delivery_responses_not_answerable_total`，
计入指定当前派发却仍无法结算的答复。当前命令状态词汇不会产生此情况，应为**零**，
用于捕捉未来新增状态却未决定答复能否结算的遗漏。

#### 服务现在拒绝过去接受的启动条件 {#services-now-refuse-to-start-on-things-they-used-to-accept}

各项都是失败时关闭的修复，都可能停止以前运行的 Pod：

| 对象 | 现在拒绝的条件 |
| --- | --- |
| 密钥存储 | 格式正确但**错误**的实例根密钥；过去启动后第一次读取密钥才失败 |
| 实例配置 | **拼错的键**；过去无声丢弃并应用默认值 |
| 实例配置 | 仍设置 `DC_SHUTDOWN_DRAIN_SECONDS`；环境变量已移除，改用 `infrastructure.shutdown.drainSeconds` |
| 实例配置 | 关闭排空窗口超过 Pod `terminationGracePeriodSeconds` 的**一半** |
| 监听器 | 两监听器共用端口，或设备端口为 `port: "0"` |
| 任意 HTTP 监听器 | 端口已占用；过去 goroutine 内记日志，服务仍报告启动成功 |

拼错键值得注意。实测 `maxSubscriptionMessageBytes` 拼错使有效帧上限减半，完全无日志，
键丢弃后用默认值，看起来配置正常。严格解码现在让你在启动时发现。

#### 指标：新增十一条序列，无重命名或删除 {#metrics-eleven-new-series-none-renamed-or-removed}

v0.15.0 已有序列精确名称全部保留，即使每服务改用独立指标注册表也不重命名或删除。

新增：command-delivery 五个计数器（上述两个 nonce、派发耗尽、状态不接受答复、死信无法写入导致响应丢失），
device-management 两个告警死信计数器，event-sources 一个提前关闭计数器，
lwm2m-ingest 的 `is_serving`，以及下述两个 Sparkplug rebirth 计数器。

:::caution lwm2m-ingest 的 `is_leader` 含义变化，文档建议用它告警
gauge 现在在副本**取得**租约时置位，而非构建任期完成后。
构建每个绑定租户最多 30 秒，过去刚赢得故障转移的副本持有租约，却可能每租户 30 秒一直报告 `is_leader=0`。

使用部署指南建议的 `sum(devicechain_lwm2mingest_is_leader) != 1` 告警时，这个虚假无领导者窗口消失。
新增 **`devicechain_lwm2mingest_is_serving`** 区分“领导者仍构建任期”和“领导者正在服务”，以前一个 gauge 无法表达。
`is_leader == 1` 且持续 `is_serving == 0` 表示领导者卡在构建。
chart 没有提供两者的告警规则，这是编写指导，不是默认继承规则。
:::

**sparkplug-ingest 新增 `rebirth_enqueued_total`、`rebirth_dropped_total`。**
过去饱和 rebirth 队列与空闲队列在全部导出指标上不可区分，因为 `rebirth_requests_total`
只计成功发布，饱和反而让它*停止增长*。
一起查看新指标：丢弃增加而请求维持上限，表示扇出超过健康发布者容量；丢弃增加而请求不变，指向代理连接。

#### 其他值得了解的行为 {#other-behaviour-worth-knowing-about}

- **平台无法发布的命令现在失败。**过去每次扫描在 queued、sent 间循环，直到数天后 TTL 到期记超时，
  错误表示设备未答复，其实从未派发。现在达到边界后停止，默认 20 次、默认 30 秒扫描下约十分钟，
  记录指明平台的失败。可设置 `functionalAreas.command-delivery.config.maxDispatchFailures`。
- **连接器目标被阻止的死信 `reason` 改为 `unprocessable`**，不再是 `exhausted`。
  更新相关告警或保存查询，已有记录原样读回。
- **无法发布的告警状态变化现在写死信并计数**，`alarm_event_dead_letter_lost_total` 加入 `DeadLetterWriteLost` 告警。
- **无法解码的入站消息不再全文归档。**记录按 subject 和流序号指向原始内容。
- **关闭时 GraphQL 订阅用 `1001` 帧干净关闭**，入站帧现在有上限：
  `infrastructure.graphql.maxSubscriptionMessageBytes`，默认 4 MiB，这是唯一新增 chart 值，有默认值。
- **治理刷新限速**：每治理维度每秒 50 次查询、burst 100，失败后负缓存十秒。
  一个解析器可保持约 3000 租户温热而不触及边界。
  超过后，已解析租户继续用**最后已知值**，不降为平台默认；只有从未解析者用默认。
- **另一个独立变化**：平台默认接入上限 `0` 现在下限为每秒 100 消息、burst 200，不再完全拒绝。
  这与上述查询速率是不同维度。
- **关闭有界**：预算从宽限期减去排空窗口和两秒余量，并全程遵守取消。
  **消费者读循环**退避后让进程失败，而非忙循环或永久重试。
- **容易踩到的两项 chart 细节。**`instance.config.infrastructure.metrics.httpPort` 已**退役**，
  仍携带的文档警告并正常启动，chart 不再写它。
  `instance.config.infrastructure.shutdown` 现在由 chart 根据顶层 `shutdownDrainSeconds`
  和 `terminationGracePeriodSeconds` **代写**；手动设置该块会令 Helm 渲染失败，而非无声与 Pod spec 不一致。
  使用 `instance.existingSecret` 提供配置时，由你添加此块。
- **自行结束的 Pod 退出时释放领导租约。**lwm2m-ingest 过去持租约退出的两条路径都修复了。
  接替者等待 30 秒现在只发生于节点故障、`kill -9` 等**突然**丢失，而非 Pod 主动停止。

#### 发布的包 {#the-published-packages}

**.NET/Unity SDK 包含上述命令 nonce 变化**，升级它才能让基于它的设备继续答复。

`@devicechain/client`、`@devicechain/dashboards`、`@devicechain/widgets`、`@devicechain/brand` 无源码变化。
自行安装 widgets 应注意：**`maplibre-gl` peer 范围从 `^6.6.0` 改到 `^6.7.0`**。
固定 6.6.x 会出现 peer 不满足警告，严格执行 peer 的包管理器可能安装失败。其他内容不变。

### v0.17.0：实例不再拥有所在集群 {#v0170-upgrade}

🔴 **没有原地升级到 `v0.17.0` 的路径。**所有 `v0.16.0` 或更早构建的实例必须销毁重建，
`dcctl upgrade` 拒绝并打印步骤，不做部分操作。
阅读[下一节](#pre-declaration-recreate)，先导出：没有保留遥测、设备定义、仪表盘跨越本发布的路径。

以下是到达该版本后的变化。

#### `dcctl bootstrap` 拆为两个命令 {#dcctl-bootstrap-is-now-two-commands}

`dcctl install <provider>` 一次准备一个**集群**。
`dcctl bootstrap <provider> <instance>` 在已准备集群构建一个**实例**，按所需实例数重复。

```bash
dcctl install local
dcctl bootstrap local my-instance
```

所有决定集群大小和形态的设置移动到 install 并在那里记录，
`--ha`、`--compact`、`--no-monitoring`、`--no-cnpg`、`--max-connections` 只设置**一次**，所有实例遵循。
bootstrap 不再有这些标志。未完成 install 的集群会拒绝 bootstrap，并指出要运行的命令。

`dcctl destroy` 只拆除一个实例基础设施，**保留集群**。
`--keep-cluster` 移除，因为 destroy 现在总是这样做。

#### 后续升级需要两个命令，第一个属于集群 {#a-future-upgrade-is-two-commands-and-the-first-one-is-the-clusters}

Operator 随拆分移动。它是**每集群一个控制器**，供全部实例共用，由 dcctl install 安装和更新：

```bash
dcctl install local --version <new-version>
dcctl upgrade local <instance> --version <new-version>
```

dcctl upgrade 不再应用 Operator，而是**读取**集群已有版本，
缺少或明确为另一发布时拒绝，并指出 install 命令。
多实例用户应了解原因：自行应用 Operator 会在升级一个实例时，无声移动**所有**实例的控制器。

一种情况只警告：**手动**安装的 Operator 没有版本来源记录，无法与旧 dcctl 覆盖区分，
所以打印 install 提示后继续。非手装者应视为本会发生的拒绝。

#### 一个集群容纳任意构建数量的实例 {#one-cluster-now-holds-as-many-instances-as-you-build}

每实例有自己的 **`dci-<instance>`** 命名空间，不是裸实例 ID，
包含自己的代理、事件存储，以及共享关系存储上的独立登录和数据库。
两项仍只能由一个实例占用，bootstrap 冲突时拒绝：**Ingress 主机名**和本地 MQTT NodePort。

前缀避免实例名称与集群自身命名空间碰撞。

#### 为 `dcctl` 明确授予 RBAC 时 {#if-you-grant-dcctl-explicit-rbac}

别人管理的集群中，dcctl 需要新的动词：
`instances.core.devicechain.io` 上在 `get`、`create`、`update` 外增加 **`list`、`patch`、`delete`**，
以及 dc-system 中 Secret 的 **`list`**。
每次 bootstrap 和升级查询已有实例及其占用资源，这需要 list。
只有旧文档权限的账户会在 bootstrap 中途被拒绝。

#### 两项移动，一项移除 {#two-things-that-moved-and-one-that-is-gone}

- **移除通过 DeviceChain 单点登录 Grafana。**通过 Service 端口转发访问，无 Ingress 路由，
  用 `dc-grafana-admin` Secret 中每集群管理员凭据登录。参见[可观察性](./observability.md)。
- **dcctl 本地状态移动。**实例记录在 `~/.devicechain/instances/<instance>/`，
  新增 `~/.devicechain/clusters/<cluster-uid>/` 保存集群基础设施状态。
  按集群身份而非名称索引，是状态**唯一**副本，没有备份包含它。
  只能在持有该目录的机器重新安装集群，参见[安装集群](./bootstrap.md#install)。
- **bootstrap 在生成根密钥前查询关系存储已有内容。**同名数据库只能是比创建它的集群活得更久，
  所以停止，而非生成无法解密已有行的新密钥。

### v0.16.0 及更早版本构建的实例 {#pre-declaration-recreate}

由 **`v0.16.0` 或更早** bootstrap 的实例，**无法升级到其后的发布**，dcctl upgrade 直接拒绝，不尝试。

dcctl bootstrap 现在记录**声明**，一个集群级对象，说明实例*是什么*：profile、拓扑、暴露方式、功能区。
升级读取它决定部署内容，从而无需重新提供实例形态就能移动版本。
到 v0.16.0 为止都没有此记录，升级无从读取。

它会明确说明，不会把实例当成不存在的名称：

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

没有兼容层，v1.0.0 前也不会提供。
旧配置从未写成新版可读形式，事后编造声明等于在运行实例上应用猜测。

**迁移到本发布，需要重建实例和底层集群。**旧版没有 dcctl install，
在实例部分安装共享前置组件，没有 install 记录。
因此 destroy 拒绝对旧状态运行 tofu destroy，需要 `--without-state`，却仍留下前置组件；
bootstrap 拒绝无 install 记录集群，install 又会与旧组件碰撞。
中间应创建全新集群：

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

用 `--kube-context` 访问且 dcctl 从不删除的集群，destroy 行也传此标志，原因见下。
随后用最初创建它的方式删除并重建，再给 install、bootstrap 传相同标志。
拒绝升级时 dcctl 会打印同样步骤。

:::note 本发布的 dcctl 不读取旧版本地状态
现在实例记录在 `~/.devicechain/instances/<instance>/`，
到 v0.16.0 为止保存在上一层 `~/.devicechain/<instance>/`。
**新 dcctl 不读取、列出、移除旧目录**，有意不迁移，因为无法区分旧实例目录与自行创建的目录。
上述步骤因此有三个结果：

- **instances list 不显示旧实例。**只有旧实例的机器输出
  `No DeviceChain instances on this machine (nothing under ~/.devicechain/instances).`
  没有丢失，实例仍在集群，`helm list -A` 仍显示 release。
- **destroy 猜集群。**旧集群记录在不读取的目录，会打印
  `No record of which cluster instance "<instance>" lives in — GUESSING cluster … from its name`，
  从实例名推导。对步骤中同名本地实例正确，对 **`--kube-context` 创建者错误**，所以 destroy 也须传该标志。
  `--without-state` 仍必需，基础设施状态也在不再读取的目录。
- **旧目录留在磁盘。**destroy 移除新路径（此类实例下本来为空），保留旧路径，
  里面有 `infra/terraform.tfstate`（明文数据库超级用户密码和代理 TLS 私钥）与 `broker-credentials.json`。
  实例移除后自行删除：

  ```bash
  rm -rf ~/.devicechain/<instance>
  ```

  **不要**动 `~/.devicechain/escrow/`。
  根密钥托管制品一直位于实例目录之外，仍可解开该实例数据库备份，参见[灾难恢复](./disaster-recovery.md#after-destroy)。
:::

:::caution 先导出：重建丢弃数据
[销毁保护](#data-durability)只保护普通 helm 操作，不保护有意 destroy。
有需要的遥测、设备定义或仪表盘，开始前导出。
:::

:::tip 被拒绝的升级不改变任何内容
dcctl upgrade 在写入前读取实例，拒绝发生在第一个变化前：
Helm release 保持原 revision，Operator 保持原镜像，每行保持原位置。
运行看看提示不产生代价。
拒绝和事后实例不变两个方面，每次发布都在真实集群验证。
:::

进入记录声明的版本后，恢复普通原地升级。
dcctl instances list 显示声明内容及所在集群。

### v0.18.0 — 让静默失败可见，接入吞吐跟上限额 {#v0180-upgrade}

`v0.18.0` 支持从 `v0.17.0` 就地升级：先为集群运行 `dcctl install`，再为其中每个实例运行 `dcctl upgrade`，步骤与 [v0.17.0](#v0170-upgrade) 相同。由 `v0.16.0` 或更早版本创建的实例仍需[销毁并重新创建](#pre-declaration-recreate)。

许多变更让过去容易忽略的故障显现出来：丢失的消息现在会被计数或记录为死信；无法工作的服务会重启，而非一直显示就绪；平台无法遵守的设置会阻止服务启动。事件持久化和实时状态合并采用批处理，解析也并行执行，因此默认部署能跟上每个租户获准发送的每秒 1000 条消息。

**哪些用户需要采取行动：**

- **所有用户：** 每位用户都需要重新登录一次，OAuth 客户端（包括通过 MCP 连接的 AI 助手）也需要重新授权，无须事先准备。
- **本版本之前创建的每个实例：** 升级后检查超级用户密码。升级不会修改它，它可能仍为以前公开的默认密码。
- **使用 webhook 通知渠道的用户：** 升级前为每个渠道添加 `auth` 键，否则投递会停止。
- **配置了本版本拒绝的设置、为单个服务设置了 `resources`、自行确定 JetStream 卷大小、限制节点可拉取的镜像仓库，或允许租户连接器访问私有地址的用户：** 参见[升级前](#v0180-before)。
- **自行编写代码或脚本调用 GraphQL API 的用户：** 阅读 [API 与错误](#v0180-api)。告警的 `message` 字段已删除，多种拒绝响应新增错误码，服务器会拒绝某些过去接受的请求。
- **监控 Pod 重启次数或搜索服务日志的用户：** 消息代理连接永久关闭，或读取消息持续失败两分钟的服务，现在会重启，而非一直显示就绪（参见[消息处理](#v0180-messaging)）。服务日志级别现在为 `info`，过去搜索的调试日志可能已消失（参见[详情](#v0180-log-level)）。
- **按告警名称设置路由或静默的用户：** 一个告警已重命名，并新增了多个告警，参见[新增和重命名的告警](#v0180-alerts)。

其余内容按领域列于[变更内容](#v0180-what-changed)。每项都说明是否需要操作，大多数不需要。

#### 升级前 {#v0180-before}

请按顺序完成以下步骤。每步都有链接指向详细说明。

1. **为每个 webhook 通知渠道添加 `auth` 键。** 没有此键的渠道在升级后会停止投递，包括当前正常工作的渠道。当前版本接受该键但忽略它，因此先添加不会造成中断。在每个租户中，查找 `config` 没有 `auth` 的 webhook 渠道：

   ```graphql
   query {
     notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) {
       results { token config hasSecret enabled }
       pagination { totalRecords }
     }
   }
   ```

   如果 `totalRecords` 超过 100，还需读取下一 `pageNumber`。取值方式参见 [webhook 通知渠道必须声明认证方式](#v0180-webhook-auth)。
2. **移除或修改新服务拒绝的配置。** 下列设置都会阻止服务启动，错误会指明设置名称。
   - `functionalAreas.outbound-connectors.config` 下的 `dispatchBacklog`：删除它（[详情](#v0180-dispatch-backlog)）。
   - `functionalAreas.user-management.config` 下的 `auth.superuserPassword`：删除它（[详情](#v0180-superuser)）。
   - `event-processing` 的 `checkpointIntervalSeconds` 大于 30：降低它（[详情](#v0180-checkpoint-interval)）。
   - 连接池大小不大于新 worker 数量：`event-management` 会拒绝不大于 5 的 `tsdbConfiguration.maxOpenConnections`，`device-state` 会拒绝不大于 5 的 `rdbConfiguration.maxOpenConnections`，除非同时将 `persistence.writers`（event-management）或 `projection.writers`（device-state）设为更小的值（[详情](#v0180-batched-persistence)）。`device-management` 会拒绝不大于 10 的 `rdbConfiguration.maxOpenConnections`，除非将 `resolution.workers` 设为更小的值（[详情](#v0180-resolution-workers)）。
3. **仅通过 chart 安装 `telemetry` 或 `ingest-only` 时，设置实例根密钥。** Chart 现在拒绝渲染任何没有根密钥的配置。由 `dcctl bootstrap` 创建的实例已有根密钥（[详情](#v0180-root-key)）。
4. **如果 values 为单个服务设置了 `resources`，检查渲染后的 Pod。** 服务 `resources` 现在逐键覆盖合并到顶层 `resources`，而非整体替换；`device-management` 和 `event-management` 还各自获得 2 核 CPU 上限。因此，超过 2 核的顶层 CPU 上限会在这两个服务中降低到 2 核，用户添加的命名空间 `ResourceQuota` 或 `LimitRange` 也可能拒绝它们（[详情](#v0180-cpu-limits)）。
5. **如果自行设置了 JetStream 卷大小，检查剩余容量。** 登录计数新增 128 MiB 预留，MQTT 连接计数新增 128 MiB，device-management 的新缓存桶新增 64 MiB；在删除被替代的两个旧桶之前，这些预留都会存在（compact 预设分别为 16、16 和 4 MiB）。空间不足时 device-management 无法启动（[详情](#v0180-profile-cache-bucket)）。使用 `--compact` 安装的现有实例仍保留 2Gi JetStream 卷：`dcctl upgrade` 不重新应用实例基础设施，因此不会调整卷大小，新桶仍能放入该卷。只有由本版本 `dcctl bootstrap` 创建的实例才使用 compact 预设的新 3Gi 卷。不要为了调整大小而删除 `dc-nats` StatefulSet：没有任何 dcctl 命令会在已有实例中重建它（[详情](#v0180-mqtt-connect-backoff)）。
6. **确保节点能从 `cgr.dev` 拉取镜像。** 集群内备份存储现在运行按摘要固定的 `cgr.dev/chainguard/minio`；请在出站规则中允许它，或按同一摘要建立镜像副本（[详情](#v0180-backup-store-image)）。如果运行 `dcctl` 的环境无法访问 OpenTofu provider registry，请允许访问该 registry 或 provider 镜像：每次 `dcctl install`、`dcctl bootstrap` 和 `dcctl destroy` 都会查询固定的 provider 版本（[详情](#v0180-dcctl-install-rerun)）。
7. **如果租户连接器向私有地址发布数据，为其添加允许规则。** MQTT、Kafka、SNS 和 SQS 连接器现在不能访问回环、私有、运营商级 NAT、链路本地或云元数据地址，包括 Amazon MSK 消息代理、Amazon MQ，以及通过启用私有 DNS 的接口端点访问的 SNS 或 SQS。在 `instance.config.infrastructure.egress.allowedDestinations` 中为每个地址添加独立的 `/32`。同时检查 MQTT URL scheme 和按客户端 ID 设置的 Kafka ACL（[详情](#v0180-connector-egress)）。
8. **为 `event-processing` 和 `outbound-connectors` 设置相同的 `outboundMessagesPerSecond` 和 `outboundBurst`。** 否则，当按平台默认值计量的租户发送速度超过两者中的较低值时，新告警 `ConnectorDispatchRateLimited` 就会触发（[详情](#v0180-new-warnings)）。
9. **更新自行编写的 GraphQL 客户端。** 从告警选择集中删除 `message`，并与平台一起升级 `@devicechain/dashboards` 和 `@devicechain/widgets`（[详情](#v0180-alarm-message)）。调用 `tenantDeletions` 时使用新的 criteria 参数（[详情](#v0180-tenant-deletions)）。[API 与错误](#v0180-api)中的其他变更，仅在代码依赖旧行为时才需处理。
10. **修复在设备缺少属性时始终为真的 CEL 条件。** 阈值或持续时间条件，例如 `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]`，升级后将停止运行。修正后的写法在当前版本同样有效（[详情](#v0180-cel-attribute-conditions)）。
11. **如果使用自行运行的 MQTT 消息代理或 Sparkplug 数据源，检查消息代理 ACL。** 外部 MQTT 数据源的订阅被拒绝时，现在会停止整个 `event-sources` 服务（[详情](#v0180-external-mqtt-resubscribe)）；Sparkplug 数据源则会保持离线（[详情](#v0180-sparkplug-refused-group)）。
12. **更新引用变更名称的告警路由、静默和仪表板。** `EventProcessingStreamNearFull` 重命名为 `JetStreamStreamNearFull`（[详情](#v0180-unread-loss)）。`JetStreamLeaseBucketNotReplicated` 的摘要已更新（[详情](#v0180-new-warnings)）。三个死信告警移到独立规则组（[详情](#v0180-dead-letter-rule-group)）。五个死信丢失指标序列改为每个服务一个名称（[详情](#v0180-dead-letter-lost)）。

#### 升级期间 {#v0180-during}

- **所有用户都会被注销一次。** 控制台、仪表板和 SDK 会话在下次刷新时结束；若未修改访问令牌有效期，最长为 15 分钟。滚动更新完成后，升级前签发的所有访问令牌和刷新令牌都失效。OAuth 客户端，包括 MCP 客户端，必须重新授权（[详情](#v0180-sessions)）。
- **数秒内，请求可能返回 `401 invalid or expired token`，登录也可能返回 `invalid or expired token`**，即使令牌刚刚签发。此窗口持续到最后一个旧版 user-management Pod 停止；之后重新登录并重试即可成功（[详情](#v0180-sessions)）。
- **多个组件会重启一次。** `device-management` 和 `event-management` 因新的 CPU 上限而逐个重启 Pod（[详情](#v0180-cpu-limits)）。`dcctl install` 会重启关系存储实例；`--ha` 模式会出现短暂写入中断，单实例中断更久（[详情](#database-primary-failover-in-seconds)）。备份对象存储也会重启到新镜像，拉取镜像期间归档暂停（[详情](#v0180-backup-store-image)）。
- **如果滚动更新停滞，告警可能延迟或丢失。** 旧版 `device-management` Pod 无法存储新告警；告警约每分钟重试一次，通常由升级后的 Pod 存储。请尽快完成滚动更新。升级前打开的控制台标签页在重新加载前会在告警列表中报错（[详情](#v0180-alarm-message)）。
- **滚动更新期间发送的 LwM2M 命令可能延迟几分钟**，因为 `lwm2m-ingest` 和 `command-delivery` 可能运行不同版本（[详情](#v0180-lwm2m-confirm)）。
- **设备配置发布、回滚或地理围栏编辑可能需要最长一个缓存 TTL 才能到达所有 `device-management` 副本**，只要新旧版本仍同时运行（[详情](#v0180-profile-cache-bucket)）。
- **部分记录可能出现两次。** 放弃处理可能同时由旧 Pod 和消息代理通知记录为死信（[详情](#v0180-no-outcome-dead-letters)）。没有 `occurredTime` 且尚未确认的事件也可能被存储两次（[详情](#v0180-processed-time)）。
- **处于 pending 或 firing 的死信告警会重新开始计时**，因为规则移到了新组（[详情](#v0180-dead-letter-rule-group)）。

#### 升级后 {#v0180-after}

以下操作都不是平台运行的必要条件，目的是清理升级留下的状态。

- **检查超级用户密码。** 对于没有自动生成密码的实例，升级会在结束时打印警告。如果从未修改超级用户密码，它仍为 `devicechain`：请登录并修改（[详情](#v0180-superuser)）。
- **删除 `device-management` 不再使用的两个缓存桶。** 它们在删除前仍占用预留容量。使用 `nats` CLI，以及能够管理平台 account 中 JetStream 的登录凭据执行：

  ```bash
  nats stream rm KV_<instance>_device-management_metric-defs-by-type
  nats stream rm KV_<instance>_device-management_profile-scope-by-type
  ```

  （[详情](#v0180-profile-cache-bucket)）
- **为旧事件存储应用新的关闭设置。** `dcctl upgrade` 不会重新应用实例数据库，因此本版本之前创建的实例会继续保留旧事件存储设置，直到手动 patch（[详情](#database-primary-failover-in-seconds)）。
- **清除由现在被拒绝的 CEL 条件触发的告警。** 规则不再运行，也就没有机制解除它们（[详情](#v0180-cel-attribute-conditions)）。
- **查找滚动更新期间未触发的告警**：使用 `dcctl dead-letters list --kind detection-action --source device-management`（[详情](#v0180-alarm-message)）。
- **重新提交开头或结尾应包含空格的设备密码。** 本版本之前保存的值已被去除首尾空格（[详情](#v0180-credential-values)）。
- **为保存了空秘密的 provisioning profile 设置有效秘密**：使用 `updateProvisioningProfile`（[详情](#v0180-provisioning-secret)）。
- **查找已存储且超过 2147483647 的告警级别**，包含这些记录的告警列表现在会报错（[详情](#v0180-int-range)）。
- **继续保护升级前的备份。** 它们包含本次升级淘汰的令牌签名密钥（[详情](#v0180-sessions)）。

#### 变更内容 {#v0180-what-changed}

下文按领域介绍运维人员或 API 调用者能够观察到的变更。

#### 数据接入和解析 {#v0180-ingest}

##### HTTP 接入拥有独立配额 {#v0180-http-allowance}

HTTP 接入请求现在使用独立的租户配额。本版本之前，它们与租户的 MQTT、NATS 和消息代理在线状态流量共享配额。HTTP 从请求路径获取租户名称，只有请求通过配额检查后才验证设备凭据，因此任何能访问 8081 端口并知道租户名称的调用者，都可能耗尽配额，导致租户 MQTT 遥测被丢弃，包括消息代理已向设备确认的消息。现在，这样的调用者只能耗尽 HTTP 配额。

这改变了租户等级接入上限的含义。在每个 `event-sources` 副本上，上限原本就分别应用于实时设备流量和中断后积压的补发流量；HTTP 现在成为第三个独立配额。因此，在最坏情况下，每个副本可以接纳达到租户上限三倍的流量（参见[接入可能超过租户上限的情况](../concepts/governance.md#ingest-above-ceiling)）。如果使用等级上限规划计费或容量，请考虑这一点。

只应在 NetworkPolicy 或进行调用者认证的 ingress 等网络控制之后开放 8081 端口。Chart 不会通过 ingress 路由该端口，但默认情况下集群内任何 Pod 都能访问它。

##### HTTP 接入中的虚构租户名称不再无限增长内存，并新增两个告警 {#v0180-unconfirmed-tenants}

过去，HTTP 接入端点会为请求路径中的每个租户名称创建独立限流配额，无论租户是否已确认，因此持续发送虚构名称会让 `event-sources` 内存无限增长。控制平面未确认的名称现在共享固定的 1024 个配额；超出后共享一个使用平台默认值的配额。MQTT、NATS 或 LwM2M 接入的租户不受影响（参见[无法确认的租户名称](../concepts/governance.md#unconfirmed-tenants)）。

新增两个警告：

- `RateLimiterOverflowInUse`：共享配额被使用时触发。
- `TenantsMeteredAtPlatformDefault`：服务连续 15 分钟无法从 user-management 读取租户上限，并按平台默认值计量时触发（参见[租户上限尚未确定时](../concepts/governance.md#unresolved-ceilings)）。

每个服务现在导出 `…_governance_unresolved_admissions_total{dimension,cause}`，`event-sources` 还导出 `…_ratelimit_overflow_admissions_total`。

租户配额现在也使用不会倒退的单调时间进行计量。过去，服务按照每条消息发送时间处理积压时，若时间倒退（例如消息代理 leader 切换到时钟不同步的服务器），或租户上限在处理过程中变化，就可能接纳超过上限的流量。现在，这些消息按配额已经见过的最新时间计量，可能多丢弃一些消息，但不会多接纳。

##### `processedTime` 现在表示平台接收事件的时间 {#v0180-processed-time}

事件的 `processedTime`（GraphQL 字段，以及分析 `events` 视图的 `processed_time` 列）现在表示平台**接收**事件的时间。对于平台消息代理上的 MQTT，它是消息代理存储消息的时刻。本版本之前，它是 `event-sources` 解码消息的时刻。正常情况下只差几毫秒，但 `event-sources` 中断后可能相差整个中断时长。

未携带 `occurredTime` 的事件现在也使用接收时间，因此在平台内等待中断恢复的读数会保留到达时间，而非处理时间。由此带来以下影响：

- **中断后，窗口检测规则可能遗漏这些读数。** `event-sources` 停止期间，LwM2M 或 Sparkplug 到达的读数会让检测引擎 frontier 保持当前时间。当 `event-sources` 追赶积压时，时间被标记为中断期间的读数会迟于重复、滑动聚合和关联规则的窗口：它们仍被存储和绘图，但不计入这些窗口，`detect_late_samples_total` 会增加。自带 `occurredTime` 的读数一直如此。参见[检测引擎中时间的含义](./detection-engine.md#timing-what-when-means)。
- **跨升级重复解码的事件可能存储两次。** 事件 ID 从内容（包括 `occurredTime`）派生。没有 `occurredTime`、升级前已存储且升级后再次投递的消息，现在会获得不同时间，从而获得不同 ID。这只可能发生在滚动更新时尚未得到确认的消息上。
- 升级前写入的数据行不会改变。
- 测量值汇总按接收时间将积压放入对应时间桶。汇总会刷新过去 30 天，因此更短中断中的数据不会被遗漏。
- 对外动作按遥测到达平台的时间计量。等待 `event-sources` 恢复的遥测，现在按到达时间而非解码时间计量。若租户所有流量都在消息代理中等待，追赶积压就不会被计为一次突发。若租户中断期间也通过 LwM2M 或 Sparkplug 发送遥测，对外计量时间已经推进到当前时刻，积压仍会像以前一样集中计量。

##### MQTT 设备事件采用并行转发 {#v0180-mqtt-forwarding}

升级时无须操作。

- **`event-sources` 现在允许最多 128 次向 inbound-events 的发布同时等待消息代理确认**，用于通过平台 MQTT 消息代理发送的设备事件，而不再最多只有五次。设备消息仍只有在其事件已存储后才会确认；发布失败的消息仍留待重新投递。HTTP 和配置的外部 MQTT 消息代理事件仍按原方式发布。
- **向 inbound-events 持续发布失败时，`event-sources` 会减速**，与 `device-management` 相同：失败后等待半秒，再逐次翻倍至两秒，在一次发布成功前始终逐条发送。
- **`event-sources` 突然停止后，可能重新投递更多设备消息：** 除此前已持有的消息外，最多还有 128 条正在等待消息代理的消息。每条都携带与此前相同的去重 ID，所以已存储事件不会再次存储。
- **每次投递都失败的设备消息仍路由到 failed-decode，同时最多四条。** 四条正在路由时到达的消息留给消息代理结束处理，并记录为死信，而不会进入 failed-decode。
- **设备事件到达 inbound-events 的顺序仍可能轻微乱序：** 五个解码器并行处理已捕获消息，且每个副本都会发布；此前也是如此。
- **`devicechain_eventsources_jetstream_publish_duration_seconds` 为 `suffix="inbound-events"` 新增 `mode="pipelined"` 序列。** [可观测性](./observability.md)介绍了这些模式。

##### 外部 MQTT 数据源重连后会重新订阅 {#v0180-external-mqtt-resubscribe}

过去，读取用户自行运行消息代理的 MQTT 数据源只在启动时订阅一次。客户端使用 clean session 自行重连，因此消息代理重启或网络中断后，不再保留该订阅：数据源仍保持连接、不报告问题，却一直不接入数据，直到 Pod 重启。现在每次连接都会重新订阅。默认部署的 gateway 数据源读取平台 stream，不受原问题影响。

- **消息代理重连后拒绝订阅，现在会停止整个 `event-sources` 服务**；一直不确认订阅也会如此。Kubernetes 会重启 Pod，若启动时仍被拒绝，服务会再次停止，与过去启动时的行为一致。因此，消息代理 ACL 变更现在会表现为 crash loop，HTTP 和平台消息代理接入也会同时停止。升级前，请检查数据源凭据是否仍可订阅其 topic。参见[传输能力矩阵](../reference/transport-matrix.md)。
- 发送订阅期间连接中断，交由客户端重连机制处理。
- **消息代理在线状态无法恢复时，`event-sources` 现在会完整执行关闭流程。** 过去会立即退出，跳过 readiness drain、各数据源停止、GraphQL 服务器停止和消息代理 drain。现在这些都会在退出前执行，Pod 仍按原方式重启（参见[设备在线状态](../concepts/device-presence.md)）。

##### Sparkplug 数据源中任一 group 被拒绝时保持离线 {#v0180-sparkplug-refused-group}

如果消息代理接受 Sparkplug 数据源连接，却拒绝其任一 group 的订阅（通常因为凭据没有读取该 group 的权限），数据源不再声明上线。它不会接入任何 group 的数据，而会断开并按逐渐增加、最长 30 秒的间隔重试，直到所有 group 都获准。一个被拒绝的 group 就会阻止整个数据源，直到修复 ACL。如果消息代理不确认上线声明，也会发生同样的情况。断开前，数据源会发布离线状态，避免消息代理已保存但未确认的上线声明继续残留。

过去，即使缺少该 group，数据源也会声明上线。该 group 的边缘节点随后会将缓冲数据发送到不存在的订阅中，数据源后来又因设备持续沉默而标记它们断开。新增 `devicechain_sparkplugingest_subscribe_failures_total` 计数器统计放弃的会话；应对其任何增长设置告警。日志会指明被拒绝的 group。如果监控 Sparkplug host 状态，这类数据源现在显示为离线，而非在线。[边缘服务](./edge-services.md)提供了详细说明。

##### 已解析事件采用并行发布 {#v0180-resolved-publish}

升级时无须操作。

- **`device-management` 现在允许最多 128 次已解析事件发布同时等待消息代理确认**，不再逐次等待，因此单个 Pod 的解析不再受限于一次发布往返。入站事件仍只有在其产生的所有已解析事件都已存储后才会确认；发布失败的事件仍留待重新投递。
- **入站事件重新投递时，已解析事件只存储一次。** 每次发布携带从入站事件派生的去重 ID。如果发布已存储但确认丢失，重投时发布的副本会被消息代理丢弃，而非再次存储。消息代理为每个 ID 保留两分钟，在此窗口内每个已解析事件都会占用 NATS 内存，进程内测量约为每条 100 字节。
- **失败事件只有在其记录存入 failed-events stream 后才会确认。** 过去，记录交给发布流程后就确认入站事件，发布失败则记录丢失。现在会重新投递入站事件，或在最后一次投递时记录为死信。失败记录携带同类去重 ID，因此重投不会将失败记录两次。
- **向 resolved-events 持续发布失败时，`device-management` 会减速**，而不是全速让整个入站积压失败：发布失败后等待半秒，逐次翻倍至两秒，在发布成功前逐条发送。同时失败的发布共享一次等待，例如消息代理连接中断时所有正在进行的发布。
- **设备已解析事件到达 stream 仍可能轻微乱序：** 每个副本都会发布，滚动更新时两个 Pod 同时运行；此前也可能如此。检测的 [`watermarkLatenessSeconds`](./detection-engine.md) 会容忍这种情况。发布失败的事件至少 60 秒后才会再次发布，超过默认容忍窗口。
- **新增直方图 `devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`**，测量每次 JetStream 发布。[可观测性](./observability.md)提供说明。

##### device-management 同时解析更多事件 {#v0180-resolution-workers}

过去，`device-management` 使用代码中固定的五个解析器处理入站事件。解析事件的大部分时间都在等待：先等待数据库验证凭据，再逐次等待消息代理键值存储返回设备配置和关系。因此，即使 CPU 大多空闲，五个解析器也限制了单个 Pod 每秒可解析的事件数。在测试集群中，Pod 使用其 4 核中的约 1.5 核，每秒解析约 1600 个事件；超过此速率的事件先在 Pod 中等待，每次约 140 个，再在 stream 中积压，检测也随之落后。现在默认运行 10 个解析器，且数量可配置。在三服务器消息代理、每次查询耗时 750 µs 的进程内测量中，5 个解析器每秒处理约 1500 个事件，10 个约 2900 个。参见[事件解析](./observability.md#event-resolution)。

- **新增设置 `resolution.workers`**（默认 `10`）。它必须小于服务连接池大小（`rdbConfiguration.maxOpenConnections`，未设置时为 20）。超出范围会阻止服务启动，错误会指明设置名称。只有将 `device-management` 的 `maxOpenConnections` 设为不大于 10 时，默认值才会被拒绝；升级前请将 `resolution.workers` 设为更小的值。
- **解析事件期间，device-management 会持有更多数据库连接。** 每个解析器验证事件凭据时占用一个连接；默认 `required` 设备认证下，每个事件都需要验证。所有解析器忙碌时，现在最多占用 10 个连接，而非 5 个。同一连接池还供 GraphQL API、MQTT 连接检查和应用告警触发/解除的 consumer 使用。若将 `maxOpenConnections` 设为小于 20，请检查剩余连接是否足够。允许解析器使用超过一半的连接池，并会在启动时记录日志。
- **已达到 CPU 上限的 Pod 不会因增加解析器而受益。** 只有 CPU 仍有余量时，才能提高处理速率。
- **新增 `resolve_workers` 指标**，报告 Pod 中运行的解析器数量；`resolve_inflight` 现在可以达到 10。若 `resolve_inflight` 长期等于 `resolve_workers`，说明事件到达速度超过解析速度。
- **回滚：** 旧版 `device-management` 会拒绝包含 `resolution.workers` 的配置，与其他未知设置一样。回滚前请移除此设置。

##### device-management 为每个设备类型保留一个缓存桶，而非两个 {#v0180-profile-cache-bucket}

设备类型的缓存测量定义和规则范围现在合并为一个键值桶：`<instance>_device-management_profile-resolution-by-type`。每个测量事件只需读取一次设备类型的已发布配置，而非三次，也不再可能按一个配置版本验证事件，却给它标记另一个版本。除非自行设置 JetStream 卷容量或手动管理桶，否则无须操作。

- **升级会新增一个缓存桶的 JetStream 预留容量**（默认 64 MiB，compact 预设为 4 MiB）。device-management 启动时创建新桶，被替代的两个旧桶在删除前继续保留容量（见下一项）。若 JetStream 卷剩余空间不足一个缓存桶，新桶会因存储不足创建失败，device-management 无法启动。删除两个旧桶后，预留容量比升级前减少一个缓存桶，新安装同样采用这一预留量。
- **升级后的实例仍保留两个被替代的桶**：`<instance>_device-management_metric-defs-by-type` 和 `<instance>_device-management_profile-scope-by-type`。升级后不再写入它们，条目会在创建桶时的缓存 TTL 内过期（默认 60 秒，除非升级前修改了 `metricDefCacheTtlSeconds`），但每个桶仍预留其容量上限，直到删除。删除需要 `nats` CLI，以及能够管理平台 account 中 JetStream 的登录身份；`dcctl` 没有对应命令：`nats stream rm KV_<instance>_device-management_metric-defs-by-type` 和 `nats stream rm KV_<instance>_device-management_profile-scope-by-type`。保留它们只消耗这些预留容量。租户删除机制还会在下一版发布前继续清理它们。
- **滚动升级期间**，配置发布、回滚或地理围栏编辑可能需要最长一个缓存 TTL 才能到达所有 device-management 副本：旧副本只清理旧桶，新副本只清理新桶。由此遗漏的围栏编辑，会让位置事件在最长一个 TTL 内继续携带旧围栏集。

##### NATS 服务器从网络中消失时，device-management 继续解析事件 {#v0180-nats-server-drop}

升级时无须操作。

- **`device-management` 的键值缓存查询最多等待半秒**，不再等待 NATS 请求允许的五秒。过去，NATS 服务器从网络中消失但未关闭连接时，部分查询仍发送给它，每次等待完整五秒，使事件解析在约一分钟内降至每秒几个事件，却没有日志。
- **超时或没有服务器响应的缓存会被跳过五秒**，期间直接查询数据库。`device-management` 会在开始跳过时记录警告，并在缓存重新响应时记录日志。四个新指标参见[停止响应的缓存](./observability.md#kv-caches)。
- **变更后的缓存条目移除从不会被跳过**，移除失败现在记录日志（`A key-value cache eviction failed`）；过去失败没有提示。
- **事件解析超过五秒现在会记录警告**（`Event resolution is slow`），最多每 30 秒一次。

#### 检测 {#v0180-detection}

##### 缺少属性时对所有设备恒真的阈值或持续时间条件现在被拒绝 {#v0180-cel-attribute-conditions}

**检测规则。** 如果用 CEL 编写的阈值或持续时间条件，在设备缺少其读取的属性时，不论事件内容如何都会为真，现在会在发布配置时被拒绝。常见形式是取反的属性存在检查与 `||` 组合，例如 `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]`，或单独使用取反的存在检查。这种规则过去会为所有缺少属性的设备触发告警，不论设备报告什么，并在属性一直缺失时保持告警。这里也包括属性不是数值、或使用 `CLIENT` scope 的设备，不仅是从未设置属性的设备。

可以观察到：

- 发布含有这种规则的配置会失败，错误指出条件并说明原因。
- 升级前已发布的此类规则，包括滚动更新期间发布的规则，会在升级时**停止运行**。规则健康状态报告 `COMPILE_ERROR` 及同一原因，`event-processing` 日志记录以 `Published detection rule failed to compile; skipping` 开头的消息。
- **规则已经触发的告警会保持激活，直到手动清除。** 规则不再运行，没有机制解除它们。
- 回滚到升级前发布的配置版本，会让该规则以 `COMPILE_ERROR` 状态返回。请改为发布修正后的版本。

不受影响的包括：在表单或画布中创建的动态阈值；仍测试事件本身的 CEL 条件，例如 `!("tempLimit" in attr) && m["temp"] > 80.0`；以及用作重复、变化率、窗口聚合或区域关联规则过滤器的条件，例如 `!("maint" in attr)`。

要修复被拒绝的规则，请正向检查属性是否存在，或将默认分支写为独立比较（`"temp" in m && ("tempLimit" in attr ? m["temp"] > attr["tempLimit"] : m["temp"] > 80.0)`），然后重新发布配置。参见 [CEL 表达式中的动态阈值](../concepts/event-processing.md#dynamic-thresholds-in-cel)。

预览文档也已更正。预览不会解析设备属性，因此 CEL 默认分支会应用于每个设备，而非从不触发。

##### 持续时间规则按读数自身时间处理迟到数据 {#v0180-duration-late-readings}

持续时间规则（例如“温度高于 80 持续 10 分钟”）现在根据读数采集时间，而非到达顺序排列读数；满足条件但落后检测引擎 frontier 超过规则保持时间的读数会被丢弃。过去，迟到读数被当作最新读数应用：

- **不满足条件的迟到读数会取消由更新读数仍支持的持续区间。** 设备上传缓冲读数时，可能每次都使持续时间告警延迟一个完整保持周期，甚至在条件始终成立时阻止告警触发。现在，早于当前连续区间的迟到读数被忽略；若读数表明条件中途不再成立，则从最新满足条件的读数重新开始计时。
- **满足条件的迟到读数可能跨过引擎已见过的中断，重新开启连续区间**，从而触发读数并不支持的告警。现在会忽略它。
- **远早于 frontier 的读数可能开启一个保持时间早已过去的区间**，并在下一事件触发告警。现在会丢弃它，并计入 `detect_late_samples_total`；该指标描述现在也包括持续时间规则和滑动窗口规则。

不满足条件的读数，无论多晚到达，仍会结束已触发的持续时间告警，除非它早于该告警：告警触发前采集、触发后才到达的读数不会撤回告警。如果它采集于产生该告警的连续区间内，则计入 `detect_late_samples_total`；早于整个区间的读数会被忽略。按正确时间放置读数后，持续时间告警可能比过去更早或更晚触发，取决于到达顺序；但不再为读数已证明中断的区间触发告警。与过去一样，只有条件持续时间达到保持时间加迟到容忍时间时，才保证触发。参见[检测引擎中时间的含义](./detection-engine.md#timing-what-when-means)。

Frontier 由整个实例共享，因此，如果某设备因时钟慢或传输慢，时间戳持续落后设备群超过持续时间规则的保持时间加迟到容忍时间，它永远不会触发该规则：每条满足条件的读数都被丢弃并计为迟到。过去，这类读数仍会被应用。

画布预览没有迟到容忍时间，因此不会为迟到读数留出额外余量。预览现在报告持续时间和滑动窗口规则中因迟到而跳过的读数数量。

**资源代价。** 为区分迟到读数与条件中断，持续时间规则现在为每个上报规则指标但不满足条件的设备保存一条小记录和一个过期定时器，保留到该设备最新此类读数之后一个保持周期。过去，这些设备不保存任何状态。若设备每个保持周期至少上报一次，这些记录会在持续上报期间一直存在：

- **每台设备、每条持续时间规则占用两个实时键**，计入租户实时键上限；该上限只计量，不强制执行。100,000 台上报设备应用五条持续时间规则，仅这些记录就达到默认 `maxLiveKeysPerTenant` 的 1,000,000，触发 `DetectTenantOverStateBudget`。如果设备群符合此规模，请提高 `maxLiveKeysPerTenant`。
- **每台设备、每条持续时间规则约占 290 字节检测 checkpoint**（`detect_snapshot_bytes`），这是使用 26 字符规则 ID 和 19 字符设备 token 的进程内测量值；两者越长，占用越大。
- **每次空闲间隔都会推进 frontier。** 过期定时器属于待处理任务，因此只要实例有持续时间规则，且某设备持续上报对应指标，即使 stream 安静，frontier 也会推进并写入 checkpoint，而非保持静止。

已触发告警的设备还会继续保留连续区间，直到条件停止成立；过去在触发告警时就释放它。

升级前写入的 checkpoint 按原样恢复，包括当时已触发的持续时间告警。随后回滚到旧版不会将新记录变成告警：旧版只从 checkpoint 读取开放的连续区间，忽略其他内容。升级时无须操作。

##### 一个动作失败不再阻止规则的其他动作 {#v0180-failing-action}

升级时无须操作。如果规则列出多个动作，请阅读本节。

过去，规则动作按列出顺序执行，第一个失败动作会阻止后续动作。每次重试都会再次执行前面的动作，后面的动作始终不执行。因此，如果命令因租户达到暂存命令上限等原因，几分钟内无法入队，列在其后的告警就永远不会触发。现在每次投递都会尝试所有动作，无论其他动作结果如何。

- **规则不再需要将最重要的动作放在第一位。** 为绕过旧行为而调整过顺序的规则可以保持原样。
- **依赖旧行为，仅在前一动作成功后执行后续动作的规则不再具有这种语义：** 每个动作都会独立执行。
- **首次尝试后约十分钟内，检测引擎重试不会再次发送 webhook 或连接器发布。** 这段时间内重复发送的告警更新和连接器请求由消息总线识别，只存储一次。涉及的两个 stream 在新版本启动时自动重新配置；为每个请求保存十分钟去重信息，会按规则触发频率消耗 NATS 内存。超过该窗口，或连接器服务自身重试调用时，请求仍可能两次到达目标。
- **只要检测动作中仍有一个失败，每次重试都会再次按租户对外速率计量 webhook 和连接器动作**，即使重复请求只存储一次。因此，租户持续达到暂存命令上限等故障，可能导致同租户其他规则的动作被限流丢弃。
- **检测在重试结束后记录为死信时，详情现在会列出最后一次尝试失败的每个动作**，按类型和幂等键显示，例如 `sendCommand/failed/<key>`；被对外速率拒绝的动作显示为 `httpCall/shed/<key>`，与 shed 死信使用同一形式。`ReactPoisonDropping` 告警摘要和说明也相应更新。
- **检测引擎现在每次从消息总线获取一个检测结果**，避免结果在慢任务后等待到被重复投递；对无响应服务的尝试会随本次投递时间结束，不再继续运行。每个动作获得其应有的时间份额，因此无响应服务的命令不能耗尽全部时间，使后面的告警无法触发。

##### 检测引擎追赶积压时不再丢弃对外动作 {#v0180-catch-up-metering}

重启、滚动更新或故障切换后，检测引擎处理停机期间到达的遥测。过去，积压产生的 webhook 和连接器动作按同时发生计入租户对外速率，因此大多被丢弃，只留一个指标。现在，无论在触发处还是连接器服务中，都按遥测到达平台的时间计量。未超过限额的租户不会因追赶积压丢失动作，也不会因此被减速。

仍超过限额的动作记录为 reason 为 `shed`、kind 为 `detection-action` 的死信，预算约为每租户每秒一封（可一次突发 60 封），全局每秒十封。超出预算的动作被计数，并汇总为每租户每分钟一封死信。`event-processing` 的四个设置可调整预算：`shedLetterPerSecond`、`shedLetterBurst`、`shedLetterGlobalPerSecond` 和 `shedLetterGlobalBurst`。新增 `ReactShedLettersOverBudget` 和 `RateMeteringClockFallback` 两个警告。

引擎重启后重新发布的检测结果现在会在 30 分钟窗口内被消息总线识别并只存储一次，因此 derived-events feed 的订阅者会看到更少重复。每个派生事件现在包含 `triggeredAt` 字段。如果按 reason 过滤死信，请预期 kind 为 `detection-action` 的 `shed` 死信。升级时无须操作。

##### 使用温备时，仅执行检测的副本派发动作 {#v0180-warm-standby}

带温备的 `event-processing` 部署中，备用副本过去会分担检测动作（命令、告警和连接器调用），并按自己的租户对外上限副本计量连接器调用，因此租户可能达到上限的两倍。现在，只有持有检测分区的副本派发动作。分区迁移时，两个副本仍可能同时派发约五秒，该窗口内重复的连接器调用会两次到达目标。

单副本（默认）下通常没有变化，只有 Pod 未正常关闭（崩溃或内存不足被终止）后有所不同。待派发动作现在会在替代副本取得分区后恢复，最长约 35 秒，而不是新副本一启动就恢复。检测仍会在额外交接等待和重放后恢复，与过去相同。

`event-sources`、`outbound-connectors` 和 `ai-inference` 的速率上限由各副本独立执行。现在[治理](../concepts/governance.md#per-replica)文档明确说明了这一点；运行多个这些服务副本时应予以考虑。

##### `checkpointIntervalSeconds` 上限为 30，部分静默失败现在会发出警告 {#v0180-checkpoint-interval}

`checkpointIntervalSeconds` 大于 30 时，`event-processing` 现在拒绝启动。检测引擎只有在写入 checkpoint 时才确认输入，因此接近或超过消息代理 60 秒确认窗口的间隔，会让安静 stream 上的消息等到消息代理再次投递，五个窗口后被计为投递耗尽，从而在健康引擎上触发 `ReplayCoveredDeliveriesExhausted`。30 秒为 checkpoint 本身留出余量。限制在启动时执行，而不是 chart 校验，因此设置更大值时 `helm upgrade` 会成功，但 Pod 随后无法启动。若 values 设置了更大值，请在升级前降低。默认值 10 不受影响。

以下过去在默认日志级别下没有提示的失败，现在会记录警告：

- 对 stream 或 KV 桶大小和副本数、或 consumer 未读数据丢失情况的采样失败；
- 检测引擎 consumer lag 采样失败；
- 消息代理设备认证因设备凭据之外的原因失败：凭据存储故障，或已保存的凭据没有秘密，永远无法认证。

设备提交错误、未知、过期或已撤销的凭据，仍仅记录 debug 日志。消息代理中断期间，采样警告每轮重复，每个 stream 约 30 秒一次；关闭流程中被中断的采样仍为 debug。凭据存储（数据库）中断期间，每次设备连接尝试都会产生认证警告，因此设备群持续重连时每次尝试都有一条警告。

##### 丢失的规则、设备和属性变更会自动修复 {#v0180-fact-repair}

发布或回滚设备配置、创建设备或修改设备类型、将设备类型指向另一个配置，以及设置阈值属性，都会向检测引擎发送一次通知。过去，通知丢失后从不重试：新发布的配置版本**完全不运行规则**，从未上报的设备不受静默检测，动态阈值继续使用旧值，而且没有失败状态或告警。文档建议的补救措施是重新发布配置。

`event-processing` 现在在接管检测时，以及此后每五分钟，将自身保存的各租户已发布规则、活动配置版本、设备和阈值属性与 `device-management` 比较，并修正差异。丢失的变更约七分钟内修复，删除设备或属性约十分钟内修复。消息代理中断后不再需要重新发布。

可以观察到：

- **升级为 `device-management` 数据库新增两列**：每个配置的活动版本何时激活，以及每个设备何时加入当前配置。两列可为空，随使用填充，因此任何设备规模下迁移都很快，旧副本在滚动更新期间仍可工作。升级前选定的活动版本视为自其发布时间开始激活；如果是回滚选择的版本，则视为从最新版本发布时间之后开始激活。
- **新增三个警告**：`DeviceFactPublishFailing`（通知发送失败）、`DetectFactsRepaired`（引擎修正了未被通知的内容）和 `DetectFactReconcileFailing`（比较本身失败）。`DetectFactsRepaired` 只统计实际丢失；通知仍在传输中的变更留给下次比较，因此升级后仅在此前确实丢失通知时才触发。
- `device-management` 导出 `fact_publish_failures_total`，`event-processing` 导出 `detect_fact_reconcile_repairs_total` 和 `detect_fact_reconcile_failures_total`。
- 回滚现在携带 `device-management` 保存的执行时间。每次配置发布或回滚的时间戳都晚于前次，即使执行它们的副本时钟不同步。同一时刻对同一配置的两次变更是例外：任何一次都可能保留较早时间，但检测引擎最终仍使用 `device-management` 保存的版本。
- `event-processing` 现在还使用现有的地理围栏服务秘密调用 `user-management` 列出租户，以及调用 `device-management` 读取规则、设备和属性。如果未配置服务秘密或任一地址，比较会关闭，并在启动时记录警告，与围栏评估相同。

##### 自动化画布支持 Connectivity 规则，并阻止覆盖无法完整展示的规则 {#v0180-canvas}

升级时无须操作。

- **画布新增 Connectivity 节点**，因此“设备离线”规则既可在表单构建器中创建，也可在画布中创建。
- **画布打开已有规则时，会检查保存是否能保留完整规则。** 过去，无法展示的规则类型会打开成空画布，没有解释；不支持的字段或动作类型会被遗漏。随后保存会用简化规则覆盖原规则。现在，画布说明无法展示的内容，并关闭该规则的保存功能；请通过 API 编辑。
- **通过 API 修改定义的画布规则，会在打开时按当前定义重新布局**，而非使用旧的保存布局，避免画布保存撤销 API 变更。发生这种情况时画布会提示。例外是已保存但无法编译的画布，它无法与规则比较，因此按原样打开，并提示保存会撤销此类变更。
- **画布保留通过 API 设置的告警键模板。** 模板只读显示，保存时保持不变。
- **规则定义未携带名称或描述时，画布保存不再清除它们。** 只有在画布中编辑了这些字段，才会发送。

#### 命令 {#v0180-commands}

##### 无法记录的命令响应再次记录为死信 {#v0180-command-responses}

在 `v0.16.0` 和 `v0.17.0` 中，command-delivery 无法写入任何死信。每次尝试都在写入前被拒绝，并计为丢失，设备响应也随之消失。因此，即使消息代理健康，`DeadLetterWriteLost` 仍可能触发。此问题已修复，相关命令的行为也有变化：

- 所有尝试后仍无法记录的响应会列为死信，对应命令进入 `FAILED`，错误说明设备已响应但响应丢失。过去，命令会一直处于执行中，直到其他机制结束它。
- 未指定 dispatch，或指定的 dispatch 已不再是命令当前派发的响应，会列为死信，但不改变任何命令状态。命令保持原状。

##### LwM2M 命令在到达设备之前再次确认 {#v0180-lwm2m-confirm}

LwM2M 命令现在会在适配器执行前立即向 command-delivery 确认。平台已经重新准备或重新发送的旧投递会被丢弃，不会第二次到达设备。中断或故障切换后可能出现这类迟到投递；过去，它可能再次驱动已经由后续投递执行过的设备。如果无法访问 command-delivery，LwM2M 命令等待并重试，从不会未经确认发送。

可观察到的变化：

- **取消批次现在也会阻止已经发布、但尚未到达设备的 LwM2M 命令。** 它们记录为 `CANCELLED`。取消结果仍将其计为已发送，因为取消执行时确实已发送。
- **LwM2M 命令的 `sentTime` 现在记录真正发送到设备的时间。** 可能晚于首次发布命令的时间。
- **只要有设备身份需要服务，缺少 `infrastructure.commandDelivery` 就会使 `lwm2m-ingest` 拒绝启动。** Chart 始终配置此项，因此只影响手工配置。
- **指标。** `lwm2m-ingest` 新增 `devicechain_lwm2mingest_commands_stale_dispatch_total`（平台已进入后续派发而丢弃的旧投递；表示避免重复执行，不是故障）和 `devicechain_lwm2mingest_command_live_claim_errors_total`（因 command-delivery 无法确认而未执行的命令）。删除 `devicechain_lwm2mingest_command_drain_dedup_total`。
- **升级期间**，新版 `lwm2m-ingest` 无法向旧版 command-delivery 确认命令。该窗口内发出的 LwM2M 命令可能延迟数分钟。如果在两个服务完成升级前耗尽重试，会重新准备，并在设备下次唤醒时投递。

升级时无须操作。

##### 响应慢的 LwM2M 设备不再阻塞其他设备的命令 {#v0180-lwm2m-slow-device}

LwM2M 设备响应慢、命令积压时，其后续命令现在会暂存在 command-delivery 中，稍后按顺序投递。过去会阻塞整个适配器，所有其他 LwM2M 设备命令都在慢设备后等待。未连接设备的命令同样暂存，不占用已连接设备需要的空间。

可观察到的变化：

- **持续连接的设备无须重连，就能收到长积压中的剩余命令。** 过去每次唤醒最多投递 32 条，其他命令等下次唤醒，但持续连接的设备不会再次唤醒。现在每次投递少量，与其他设备轮流处理，直到积压清空。
- **同一设备的命令仍按发送顺序到达。** 现在，无法向 command-delivery 确认而需重试的命令也保持顺序，后续命令等待它。但两种情况仍可能让命令晚于其后发送的命令到达，两者都要求 command-delivery 故障：中断超过该命令的重试时限；或 command-delivery 已记录确认，但响应未到达 `lwm2m-ingest`，例如请求超时。这时命令保持已发送但未执行，直到平台发现其停滞；随后在设备下次连接时，排在后续命令之后投递，或者过期。
- **故障切换后，设备重连期间发出的命令会稍后投递。** 每台设备的等待命令先于新命令处理，因此短暂窗口内新命令也会暂存。故障切换后应预期 command-delivery 流量短暂增加。
- **这样暂存的命令显示为 `PARKED`**，即使设备已连接。过去 `PARKED` 仅表示设备没有实时连接。参见[命令](../concepts/commands.md)。
- **指标。** `lwm2m-ingest` 新增 `devicechain_lwm2mingest_commands_overflow_parked_total`，带 `reason` 标签（`full`、`offline`、`bind`、`unconfirmed`）；新增 `devicechain_lwm2mingest_command_overflow_blocked_total`（因 command-delivery 慢而等待）和 `devicechain_lwm2mingest_command_drain_turns_total`。删除 `devicechain_lwm2mingest_command_drain_dropped_total`：适配器忙时不再丢弃设备请求等待命令的操作。

升级时无须操作。

#### 连接器和通知 {#v0180-connectors}

##### 连接器不能再访问私有地址，连接器服务采用新客户端 {#v0180-connector-egress}

MQTT、Kafka、SNS 和 SQS 连接器现在采用 webhook 和邮件 relay 已有的连接时检查。目标解析为回环、私有、运营商级 NAT、链路本地或云元数据地址时会被拒绝。这是**最终拒绝**：派发记录为 `blocked` 死信，不重试。Kafka 会检查集群公布的每个消息代理，而不只是配置地址。检查在服务内部执行，不再依赖 `networkPolicy.enabled` 或集群是否落实该策略。

升级前请检查：

- **私有地址目标将停止接收。** 包括：
  - 集群内或通过 peering 连接的消息代理；
  - 默认使用私有地址的 **Amazon MSK 消息代理**；
  - 用于 MQTT 的 **Amazon MQ**；
  - **通过启用私有 DNS 的接口 VPC 端点访问的 SNS 和 SQS**。启用私有 DNS 后，即使默认 `sns.<region>.amazonaws.com` / `sqs.<region>.amazonaws.com` 也解析为私有地址，因此未覆盖 endpoint 的连接器同样受影响。

  在 `instance.config.infrastructure.egress.allowedDestinations` 中为每个地址添加独立 `/32`。接口端点在每个可用区都有一个地址，每个都需要独立条目。允许规则适用于所有租户、所有连接器和 webhook 路径，并不限于你计划开放的那个。
- **MQTT URL 必须使用 `tcp://`、`mqtt://`、`ssl://`、`tls://`、`mqtts://`、`ws://` 或 `wss://`**，显式指定端口，且每个条目只有一个消息代理。`tcps://`、`mqtt+ssl://` 和 `unix://` 不再接受。保存连接器时会拒绝；已有连接器使用它们时，触发后记录为 `invalid` 死信。Kafka 地址必须为 `host:port`。
- **连接器不再使用代理环境变量**（`HTTPS_PROXY`、`ALL_PROXY`），也不使用 Pod 的 `AWS_*` 变量或 AWS 配置文件。
- **Kafka 客户端已更换。**
  - 默认客户端 ID 现在为 `devicechain`（过去为 `bento`）。如果消息代理按客户端 ID 应用 ACL 或配额，请设置连接器的 `clientId`。
  - 无键记录现在采用 sticky partitioning 分配。
  - 协议版本与消息代理协商，不再固定。
  - 投递语义不变：leader 确认，没有幂等 producer。
- **SQS 消息逐条发送**（`SendMessage`），不再批量发送。IAM 权限仍为 `sqs:SendMessage`。

此外还带来两项改善：暂时不可达的 Kafka 消息代理现在会重试，不再立即记录为 `invalid` 死信；连接器服务二进制占镜像的大部分，现在大小约为原来的三分之一。

##### 连接器服务不再接受 dispatchBacklog {#v0180-dispatch-backlog}

如果 values 在 `functionalAreas.outbound-connectors.config` 下设置了 `dispatchBacklog`，请在升级**之前**删除它。保留该键会阻止服务启动，错误会指出名称。

此设置过去控制 reader 与发送 worker 之间的缓冲区，现在该缓冲区已移除。Reader 只获取当前有空闲 worker 可立即处理的派发数量（`maxConcurrentSends`），避免派发在进程中等待，而消息代理确认窗口持续消耗。通知服务也采用相同方式读取告警，每个 dispatcher 一条。

这修复了重复发送问题。过去，突发告警或连接器派发排在慢渠道之后，可能在服务内停留超过确认窗口。消息代理随后再次投递同一消息，第一份仍在等待，两份都会发送：同一个告警发出第二次通知，或再次调用同一 webhook。现在每次发送也会在确认窗口结束前预留余量并终止。

新增 `ReaderHeldMessagePastAckWait` 告警，任一服务仍持有消息超过窗口时触发。[超过确认窗口仍被持有的消息](./observability.md#held-past-ack-wait)说明了各情况的含义。

##### Webhook 通知渠道必须声明认证方式 {#v0180-webhook-auth}

Webhook 渠道配置现在需要 `auth` 键：`none`、`bearer` 或 `header`。过去，有秘密时发送 `Authorization: Bearer <secret>`，没有秘密时**不发送任何凭据**。因此，秘密缺失或已清除的渠道会继续未经认证发送请求，且没有任何提示。[配置通知渠道](../guides/notification-channels.md)提供详情。

- **本版本之前保存的 webhook 渠道没有 `auth`，升级后会停止投递**，包括当前正常工作的渠道。路由到该渠道的每个告警和升级通知，在第一次尝试就被拒绝且不重试，告警也不会为该渠道重新投递。通知服务记录租户、渠道 token 和原因，并计入 `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`。
- **升级前找出需要修复的渠道。** 在每个租户中运行 `notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) { results { token config hasSecret enabled } pagination { totalRecords } }`，查找没有 `auth` 的 `config`。如果 `totalRecords` 超过 100，继续使用下一 `pageNumber`，直到读完。
- **升级前添加 `auth`。** 当前版本接受但忽略该键，因此不会产生空档。有秘密（`hasSecret: true`）的渠道使用 `bearer`；没有且不应有秘密的渠道，例如 Slack incoming webhook，使用 `none`。设置了 `authHeader` 的渠道需要 `header`。只设置 `authScheme`（例如 `Token`）的渠道需要 `"auth":"header","authHeader":"Authorization"`。使用 `bearer` 或 `none` 时，删除 `authHeader` 和 `authScheme`：本版本会拒绝它们，而不是忽略。使用 `none` 且渠道已有秘密时，同次更新发送 `secret: null`。
- **保存时，`auth` 与秘密不一致的渠道会被拒绝。** 包括 `bearer` 或 `header` 没有秘密、`none` 带有秘密，以及清除 `bearer` 或 `header` 渠道的秘密。尚无 `auth` 的渠道轮换秘密也会被拒绝，除非同次请求添加 `auth`。仅重命名、修改描述或禁用渠道的更新不检查，因此可以先禁用故障渠道；启用时会检查。
- **SMTP 渠道有用户名却没有秘密时**，现在在连接邮件服务器前就被拒绝，不重试。过去会先连接，再因没有认证而放弃，每次尝试都重复此过程。
- **`httpCall` 动作的秘密句柄不对应任何已保存秘密时**，只记录一次 outcome 为 `invalid` 的死信，不再重试。过去会重试到重新投递上限，再记录为 exhausted，因此在该窗口内补存秘密可能让调用成功。死信不会自动重放，所以该次触发不会调用目标；请修正秘密句柄，让后续触发能够认证。调用从不会不带凭据发送。

##### 基于过期副本保存通知策略可能被拒绝 {#v0180-policy-precondition}

`updateNotificationPolicy` 新增可选 `expectedUpdatedAt`。传入最后读取的 `updatedAt`，或上次更新返回的值后，如果其他人此后修改了策略（包括其规则），过期副本的保存会被拒绝，且不写入任何内容。错误为 `notification policy was modified by another writer; reload and try again`，没有 `extensions.code`。更新响应现在从数据库重新读取 `updatedAt`，可供下一次前置条件使用。不传参数时，仍按原方式由最后写入覆盖。控制台没有通知策略编辑器，只有 API 调用者可发送该参数。参见[哪些 mutation 是部分更新](../reference/graphql-api.md#which-mutations-are-partial-updates)。

#### 持久化和状态 {#v0180-persistence}

##### 事件批量持久化 {#v0180-batched-persistence}

`event-management` 现在将等待同一 writer 的事件合并到一个事务提交，不再每事件一个事务。在有副本的事件存储中，每次提交都等待 standby；限制存储速度的是这个等待，而非数据库工作量。批处理只等待一次。存储哪些事件没有改变，事件仍只有在存储后才确认。被拒绝的事件会单独再次写入，因此与过去一样重试或报告，其余批次事件则正常提交。

新增三个可选设置：`persistence.writers`（默认 `5`，过去固定的数量）、`persistence.maxBatch`（默认 `32`；`1` 恢复每事件一个事务）和 `persistence.lingerMillis`（默认 `0`）。`device-state` 新增 `projection.writers`（默认 `5`）。参见[事件持久化](./observability.md#event-persistence)。

- **Writer 数量必须小于服务连接池大小。** 越界会阻止服务启动，错误指出设置名称。只有将 `event-management` 的 `tsdbConfiguration.maxOpenConnections`，或 `device-state` 的 `rdbConfiguration.maxOpenConnections` 设为不大于 5 时，默认值 5 才会被拒绝。升级前，将 writer 数量设为小于连接池，或提高连接池，但不要超过默认 20：平台数据库连接上限按此默认值规划。
- **`device-state` 也批量合并状态。** 参见[实时设备状态批量合并](#v0180-live-state-batches)。
- **绘制持久化指标时：** `persist_inflight` 现在可能超过 writer 数量，因为它统计等待批次提交的事件，`persist_duration_seconds` 也包含这段等待。新增 `persist_batch_size` 和 `persist_batch_fallbacks_total`。

##### 实时设备状态批量合并，持续落后的 consumer 会触发警告 {#v0180-live-state-batches}

过去，`device-state` 用两个独立事务将每个事件合并到设备实时状态（连接、活动、最新读数和最后位置）。有副本的数据库每次提交都等待 standby，因此事件到达速度超过逐条提交速度时，实时状态就落后。持续高流量后，它可能比已存储事件落后一个多小时，却没有提示。现在像 `event-management` 持久化一样，在一个事务中合并等待 writer 的事件。在带同步 standby 的 TimescaleDB 进程内测量中，五个 writer 每秒合并约 3500 个事件，过去约为 85 个。

- **设备实时状态最终内容不变**，只有一个例外：读数或位置只有严格更新时才替换已保存值，现在按数据库存储精度（微秒）比较时间。过去，与旧值处于同一微秒的读数即使实际更早也可能替换它；现在保留先存储的值。
- **事件仍只有在存储后才确认。** 如果批次中某租户的部分被拒绝，其事件会逐条重新合并，只有自身被拒绝的事件才像过去一样重试或丢弃，其他租户事件仍共同提交。
- **新增两个可选设置：** `projection.maxBatch`（默认 `32`；`1` 恢复逐事件处理）和 `projection.lingerMillis`（默认 `0`），范围与 `event-management` 相同。参见[实时设备状态](./observability.md#live-state-projection)。
- **绘制 `device-state` 指标时：** `state_inflight` 现在可能超过 writer 数量，因为它统计等待批次提交的事件，`state_duration_seconds` 也包含这段等待。新增 `state_batch_size` 和 `state_batch_fallbacks_total`。
- **每个服务现在报告其读取的每个 consumer 的等待消息数**：`jetstream_consumer_pending_messages` 和 `jetstream_consumer_ack_pending_messages`。新增 `JetStreamDurableFallingBehind` 警告，当 consumer 连续 15 分钟有超过 10000 条消息等待时触发。`event-processing` 的检测 consumer 仍由不变的 `DetectConsumerBacklogHigh` 处理。参见[持续落后的 consumer](./observability.md#consumer-backlog)。

#### 数据库和故障切换 {#v0180-databases}

##### 数据库主节点在数秒内切换 {#database-primary-failover-in-seconds}

过去，删除数据库主节点 Pod、drain 其节点或滚动应用变更时，故障切换会等待三分钟：主节点等待所有客户端断开，但平台服务从不主动断开。现在只给客户端五秒，然后关闭，因此 `--ha` 下 standby 在一分钟以内接管。参见[数据库主节点停止时](./bootstrap.md#ha-database-failover)。

- **多实例数据库现在通过 switchover 更新主节点。** 旧版原地重启主节点并等待，不提升 standby，因此每次更新都完全中断写入。
- **正在停止的数据库实例在两分钟后被强制停止**，不再等待三十分钟。过去数据库已经停止，Pod 仍可能 `Terminating` 半小时，使集群缺少一个 standby；现在最多两分钟移除。
- **实例停止时备份存储不可达**，不再为最后的预写日志归档等待最长三十分钟，而是最多两分钟。已提交数据不受影响，但归档可能出现缺口，详见上述链接。

**`dcctl install` 更新集群时，关系存储实例会重启一次。** `--ha` 下先重启 standby，再将主节点角色切换到其中一个，造成短暂写入中断。单实例部署会原地重启唯一实例，关系存储在重启完成前不可用。期间写入会重试。

**现有实例的事件存储保留旧设置。** `dcctl upgrade` 不重新应用实例数据库，因此只有本版本 bootstrap 的实例在事件存储中使用新设置。要为现有实例应用相同设置，请从下列命令中选择**一个** patch 数据库集群。这会按上述方式重启实例一次：

```bash
# under --ha: the switchover setting goes in the SAME patch as the timings
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"primaryUpdateMethod":"switchover","smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
# a single-instance install
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
```

`--ha` 下，不要将第一条命令拆成两次 patch 并先改时间设置。修改 `stopDelay` 会立即触发重启；如果 switchover 设置尚未应用，主节点将原地重启，不提升 standby，从而产生本版本旨在消除的长中断。

**`--ha` 下，数据库镜像变更和参数变更必须分别应用。** 启用 switchover 后，数据库 Operator 会拒绝同时修改镜像和任意数据库参数的更新。因此，在一次 `dcctl install` 中既升级到带新数据库镜像的版本，又修改 `--max-connections`，会被拒绝。请先不改 `--max-connections` 运行 `dcctl install`，再运行一次修改它。

:::caution 应用这些设置的重启仍使用旧的三十分钟上限
两分钟上限属于各数据库 Pod，要到替换 Pod 的重启后才生效，被替换的 Pod 仍带三十分钟设置。如果此次重启中数据库 Pod 保持 `Terminating` 超过两分钟，且日志出现 `failed waiting for all runnables to end within grace period of 30s`，数据库已经停止，Pod 不会自行结束。移除它后，Operator 会重建：

```bash
# the relational store's pods are dc-rdb-<n> in dc-system,
# the event store's are dc-tsdb-<n> in dci-<instance>
kubectl -n dc-system delete pod dc-rdb-1 --grace-period=0 --force
```

只有节点为 `Ready` 且已在日志中读到该行时，才执行此操作。节点不可达而导致 `Terminating` 是另一种情况：数据库可能仍在运行，不能强制删除。参见[节点丢失](./bootstrap.md#ha-node-loss)。
:::

##### 空闲一分钟的数据库事务会结束 {#v0180-idle-transactions}

升级时无须操作。

每个服务现在都要求数据库终止空闲 60 秒的事务，覆盖事务中途 Pod 冻结或网络断开的情况。过去，事务会一直开放并持有锁，直到连接被判定死亡；默认操作系统设置下可能长达数小时，而且网络恢复后仍可能提交。这可能在租户删除已报告完成后，再写入该租户的数据。达到上限时，数据库记录 `terminating connection due to idle-in-transaction timeout` 并回滚事务。对应请求报错，从 stream 领取的工作会重新投递。参见[数据库写入拒绝](./tenant-deletion.md#database-writes)。

##### 文档说明了节点丢失和重新加入时的表现 {#v0180-node-loss}

行为没有变化。`--ha` 下的[节点丢失](./bootstrap.md#ha-node-loss)现在按顺序说明运维人员看到的现象：消息代理、服务和数据库多快恢复；事件处理可能暂停约一分钟；为何丢失节点上的已驱逐 Pod 保持 `Terminating`，且节点不可达时不能强制删除；以及节点**重新加入**本身也会产生短暂扰动。被隔离的消息代理服务器曾自行进行选举，重新加入时其他服务器会再次选举 leader；节点返回约 45 秒后，应预期 JetStream 数秒的“temporarily unavailable”。不会丢失数据。应像规划节点丢失一样规划其返回。

服务 Pod 的 30 秒节点丢失驱逐时间没有改变，这是有意设计，现在会检查 chart 渲染的每个 Pod，包括控制台。

#### 消息处理 {#v0180-messaging}

##### 消息代理连接永久关闭的服务现在会重启 {#v0180-broker-connection-closed}

如果与消息代理的连接在非服务主动请求的情况下永久关闭，每个服务 Pod 现在会使存活检查（`/healthz`）及就绪检查失败。例如，运行中的 Pod 所用凭据被撤销或轮换，消息代理不再接受它，或消息代理发送客户端无法解析的错误。Kubernetes 会重启 Pod，新 Pod 重新读取凭据。过去，Pod 会继续显示存活和就绪，只记录一条错误，之后不再处理。服务主动请求关闭，例如正常停止，不会触发此行为。

- **凭据变更可能表现为重启。** 如果消息代理在 Pod 替换前断开其连接，例如轮换凭据期间，该 Pod 会重启一次。
- **正常停止现在会在 Pod 终止时间预算内等待消息代理连接 drain**，先处理已收到消息，再退出。停止可能比过去更久，但不超过预算。

`event-sources` 还有独立的消息代理在线状态连接，同样适用。它使用 system-account 凭据，通过该连接读取 MQTT 连接事件。过去，如果消息代理永久关闭此连接，Pod 仍显示存活和就绪，在线状态却静默冻结：既不声明也不释放设备在线状态，也没有任何机制重启它。

现在存活检查失败，由 Kubernetes 重启。被拒绝的凭据会同时影响每个副本，因此**所有 `event-sources` Pod 都会重启**；期间 HTTP 接入不可用，MQTT 遥测由消息代理保存，服务恢复后处理。如果重启后的 Pod 仍无法登录，消息代理在线状态会以 `broker_unreachable` 原因关闭，已显式声明在线的设备回退到推断在线状态，与启动时消息代理不可达的行为相同（参见[设备在线状态](../concepts/device-presence.md)）。

##### 持续失败的读取循环现在会重启服务 {#v0180-read-loops}

过去，从 stream 读取消息的循环失败后每秒重试一次，持续多久都不退出；Pod 一直报告就绪，却不消费任何内容。现在每个循环采用逐渐增加、最长五秒的重试间隔。如果连续两分钟读取失败，就以非零状态结束进程，让 Kubernetes 重启 Pod。一次成功读取会重新开始两分钟计时，足以容忍消息代理故障切换或重启。重启会重新连接消息代理、重建 consumer，并重新读取挂载的凭据。

本版本新增此机制的循环包括：

- `outbound-connectors` 的连接器派发；
- `user-management` 的死信存储；
- `command-delivery` 的死信写回；
- `event-processing` 的七个循环：已解析事件、规则和属性更新、设备名单和设备删除、地理围栏集，以及动作派发；
- `lwm2m-ingest` 的命令派发。

过去是显示就绪却不消费的 Pod，现在能看到重启次数增加。

##### Consumer 落后于已满 stream 时会发出告警 {#v0180-unread-loss}

JetStream stream 满时会丢弃最旧消息。过去，尚未读取这些消息的 consumer 会丢失它们，却没有指标或告警说明。每个服务现在对其读取的每个 durable consumer 测量此情况，并导出两个新序列：

- `devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`：consumer 未读取就越过的消息数。
- `devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`：停止读取的 consumer 前方被丢弃的消息数；停止读取表示自上次采样后未收到任何消息。仍在读取但落后的 consumer 此值为 0。

新增两个 critical 告警读取它们：`JetStreamDurableLostUnread` 和 `JetStreamDurableStalledBehindStream`。删除租户可能触发前者，因为删除会移除租户消息，包括 consumer 尚未读取的消息。告警含义和处理方式参见 [consumer 从未读取的消息](./observability.md#unread-loss)。

接近容量上限的警告从 `EventProcessingStreamNearFull` **重命名**为 **`JetStreamStreamNearFull`**，现在覆盖每个服务的 stream，而不只是 event-processing。阈值不变：连续 10 分钟达到字节上限的 80%。若 Alertmanager 路由或静默引用旧名，请改为新名。

##### 最后一次尝试没有结果的消息现在也记录为死信 {#v0180-no-outcome-dead-letters}

过去，只有服务在处理后主动放弃的消息才出现在死信列表。五次投递尝试都**没有**结果的消息，例如 Pod 中途停止或处理超出确认窗口，没有任何记录，因为未执行到写死信的代码。消息代理能够发现这种情况，现在每个服务也会根据消息代理通知记录这些消息。

可观察到的变化：

- **新增三种 kind**：`event`、`command` 和 `control-fact`，对应设备事件、命令和控制平面 stream。死信 kind 现在由消息到达的 stream 决定。`dcctl dead-letters list --kind` 提供所有类型。
- **新增 reason `no-outcome`。** 它从不结束命令，因为最后尝试可能已完成工作，只丢失确认。对于高流量 stream（设备事件、命令和检测动作），死信不复制原消息，详情指出原消息在 stream 过期移除前的位置。消息过大无法复制时也如此；连接器请求死信指向连接器服务自己的死信 stream，其中保留完整请求。
- **新增 `max-deliveries` stream**，由每个读取 stream 的服务创建。默认预留 8 MiB，现有 JetStream 卷能够容纳，无须调整。稳定状态下为空；新增 `MaxDeliveryRecordsWaiting` 告警在通知等待记录时触发（参见[耗尽投递次数的消息](./observability.md#max-delivery-records)）。
- **检测引擎放弃的投递只计数，不记录死信。** `event-processing` 从自己的 checkpoint 读取 `resolved-events`，重启后会再次读取，因此该引擎中耗尽投递次数的事件并未丢失。如果 checkpoint 长时间无法保存，超过消息代理的重新投递时限（通常是数据库中断），窗口内每个事件都会耗尽尝试；逐条死信会错误报告不存在的数据丢失。因此按 `outcome="replay-covered"` 计数，并由新增 `ReplayCoveredDeliveriesExhausted` 警告报告。其他读取 `resolved-events` 的服务仍记录死信。
- **死信 stream 新增 30 分钟去重窗口**，升级时就地应用，连接器服务自己的死信 stream 同样如此。它使服务与消息代理通知同时记录的放弃事件只落一份。
- **`DeadLetterWriteLost` 新增第三种原因：** 死信存储或命令写回耗尽尝试的死信，可能未经存储就从 stream 过期移除；最后一次尝试也可能已存储，只丢失了确认。

滚动升级期间，一次放弃可能被记录两次，分别来自旧 Pod 和消息代理通知。它们是同一故障，并不表示另有消息丢失。

##### 死信丢失计数统一为每服务一个指标 {#v0180-dead-letter-lost}

服务放弃消息后又无法写入死信时，现在在自身 subsystem 下使用 **`dead_letter_lost_total`** 计数，各服务名称一致。删除以下五个序列：

- `devicechain_eventprocessing_react_events_dead_letter_lost_total`
- `devicechain_notificationmanagement_notifications_dead_letter_lost_total`
- `devicechain_commanddelivery_command_delivery_responses_dead_letter_lost_total`
- `devicechain_devicemanagement_raise_alarm_dead_letter_lost_total`
- `devicechain_devicemanagement_alarm_event_dead_letter_lost_total`

由以下五个序列替代：

- `devicechain_eventprocessing_dead_letter_lost_total`
- `devicechain_notificationmanagement_dead_letter_lost_total`
- `devicechain_commanddelivery_dead_letter_lost_total`
- `devicechain_devicemanagement_dead_letter_lost_total`，一个计数器覆盖 device-management 的两条路径
- **新增** `devicechain_outboundconnectors_dead_letter_lost_total`。出站连接器派发在最后投递中无法写入死信副本时，过去仅计入没有告警读取的 `connector_dispatch_total{outcome="dead_write_failed"}`；该处仍计数，现在也在新指标中计数。

`DeadLetterWriteLost` 现在按名称选择这些指标，不再逐个列举，因此也覆盖出站连接器。自建仪表板或规则引用旧序列时，请更新。选择器 `{__name__=~"devicechain_[a-z0-9]+_dead_letter_lost_total"}` 覆盖所有服务。请使用 `[a-z0-9]+`，而不是 `.+`：滚动升级中尚未替换的 Pod 仍导出 device-management 两个旧名称，`.+` 会同时匹配它们。

计数器也统计服务因格式错误而**拒绝**的死信。这表示服务缺陷，而非消息代理问题。Pod 的 `LOST` 错误日志说明属于哪种情况。

#### 安全和登录 {#v0180-security}

##### 重置密码会结束会话，升级时所有用户需重新登录一次 {#v0180-sessions}

两项变更会在升级时结束所有会话。

**每个用户现在都有会话值**，所有可换取新令牌的令牌都携带它：刷新令牌、控制台选择租户前持有的登录令牌，以及 OAuth 授权码。重置密码、禁用或删除用户会改变该值，携带旧值的令牌被拒绝。过去，重置密码不影响已经签发的刷新令牌，因此被窃取的令牌只要持续使用就能不断续期。

**为所有访问和刷新令牌签名的密钥，现在由实例根密钥封装。** 过去它明文保存在 user-management 数据库中，任何能读取数据库、备份或预写日志归档的人，都能签发每个服务接受的令牌。现在它像其他凭据一样被封装，而且只有当前使用的密钥保留私钥部分。密钥轮换退出后删除私钥，只保留公钥，原令牌仍可验证到过期。升级会**删除实例此前所有签名密钥**，而不是封装它们，因为它们已经明文出现在此前每份备份中。user-management 启动时生成新密钥。

升级时可以观察到：

- **每个用户都要重新登录。** 升级前签发的令牌没有会话值，不能刷新；滚动更新完成后也不能验证。控制台、仪表板和 SDK 会话在下次刷新时结束，若未修改访问令牌有效期，最长为升级后 15 分钟。嵌入式仪表板应用会话同样结束。此时停留在租户选择器或管理员页面的控制台，可能在选择租户时报错，而不是返回登录页；退出后重新登录即可清除。
- **OAuth 客户端，包括通过 MCP 连接的 AI 助手，必须重新授权。** 其刷新令牌返回 `invalid_grant`。
- **旧密钥在滚动更新完成前仍受信任。** 最后一个旧 user-management Pod 停止前，仍用旧密钥签名并发布公钥供其他服务验证。`helm upgrade` 和 `dcctl upgrade` 会替换所有 Pod，因此结束时旧密钥不再受信任。旧 Pod 停止期间，滚动更新开始几秒内的请求可能返回 `401 invalid or expired token`，登录可能返回 `invalid or expired token`，即使令牌刚签发。旧 user-management Pod 停止后，重新登录并重试即可。
- 滚动更新期间由尚未替换的服务创建的用户没有会话值，无法登录。登录表现如密码错误，user-management 日志指出用户和原因。管理员重置其密码即可修复。

此后行为变化：

- **重置密码、禁用或删除用户，会结束该用户持有的所有会话。** 刷新令牌下次使用时失效；变更前签发的登录令牌或授权码不能再换取新会话。重新启用用户不会恢复旧会话。
- **已经签发且可直接使用的令牌不会立即撤销。** 访问令牌，以及用于管理员 API 的登录令牌，仍有效直到过期：默认 15 分钟，除非修改了访问令牌有效期。管理员登录令牌也包括在内。
- **删除用户后使用同一邮箱重新创建，会从新状态开始。** 新用户不会继承旧用户任何尚存会话。
- 修改角色或成员关系不会注销用户，与过去一样在下次刷新时生效。

升级**之前**的备份和归档预写日志仍包含旧密钥。滚动更新完成后，它们不再被任何组件信任，但这些文件仍应像原凭据一样受到保护。

##### 新签名密钥约一秒内即可获得信任 {#v0180-signing-key-trust}

User-management 开始使用新密钥签名时（如本次升级），其他服务首次看到其签名的令牌，就会获取 user-management 发布的密钥集来学习该密钥。旧版限制每个服务 Pod 最多每 30 秒获取一次。如果请求由仍发布旧密钥集的 user-management Pod 响应（滚动更新期间旧 Pod 就如此），服务会将新密钥签发的所有令牌拒绝为 `invalid or expired token`，直到 30 秒过去。升级后立即登录可能因此失败。

- **服务现在可在上次获取结束一秒后再次获取。** 获取进行中的请求等待结果，不再直接被拒绝。
- **引用 user-management 从未发布密钥的令牌**，仍最多让每个服务 Pod 每秒获取一次。
- **获取后仍找不到密钥会记录日志。** 服务记录 `JWKS refetched on an unknown kid, and the fetched set does not hold it.` 及密钥 ID，帮助区分密钥切换期间拒绝与令牌本身无效。

无须配置。

##### 每种部署配置都必须有实例根密钥 {#v0180-root-key}

过去，只有保存集成凭据的配置才需要根密钥，因此 chart 允许 `telemetry` 和 `ingest-only` 不带根密钥渲染。现在每个实例都用根密钥封装签名密钥，因此任何配置未设置根密钥时，chart 都会**渲染失败**。由 `dcctl bootstrap` 创建的实例已有根密钥，无须操作。仅通过 chart 安装的 `telemetry` 或 `ingest-only` 必须先设置密钥再升级。使用 `openssl rand -base64 32` 生成，传入 `instance.config.infrastructure.secrets.rootKey`，并妥善保留：以后每次升级都必须传入相同值。

根密钥现在也决定用户能否登录。错误或丢失的密钥会使 user-management 和每个保存集成凭据的服务拒绝启动。其他服务因无法取得令牌验证密钥而一直未就绪。整个 API 都不可用，而不只是集成。处理方式参见[灾难恢复](./disaster-recovery.md#root-key)。

删除已保存凭据时，现在会将其从数据库彻底移除。过去，加密数据行仍留在表中，只标记为已删除。本版本**之前**删除的凭据保持原状态：不能使用，但封装的数据行仍在表和备份中。

##### 超级用户不再使用默认密码 {#v0180-superuser}

旧版为每个实例的超级用户 `superuser@devicechain.local` 设置相同公开密码 `devicechain`。新实例现在获得自动生成的密码：`dcctl bootstrap` 打印一次，并保存在实例命名空间的 Secret `dci-<instance>-superuser` 中。不再有默认密码。如果 user-management 启动时身份表为空，而该 Secret 没有密码，会拒绝创建超级用户。

**升级不会修改现有超级用户密码。** 升级无法知道用户是否改过密码，因此保留原值，并在结束时为没有生成密码的实例打印警告。如果从未修改，此类实例仍使用 `devicechain`。请登录修改，或重建实例以生成密码。

`dcctl sim` 和演练工具也不再假定旧密码。`dcctl sim` 从实例 Secret 读取生成值；没有生成值的实例可传入 `--admin-password` 或 `$DC_ADMIN_PASSWORD`。

如果 values 为 user-management 设置了 `auth.superuserPassword`（`functionalAreas.user-management.config.auth.superuserPassword`），请在升级前删除。保留该键会使 user-management 拒绝启动，错误指出键名。初始密码只来自 Secret `dci-<instance>-superuser`，或由 `instance.superuserSecret` 指定的 Secret。

##### 登录受到限流，GraphQL 请求限制字段数 {#v0180-sign-in-limits}

除非自编代码或脚本执行下列操作，否则升级时无须处理。控制台、仪表板应用、SDK 和 `dcctl` 均在限额内。

- **一个操作最多选择 5 个 mutation 顶层字段或 20 个 query 顶层字段。** 别名和通过 fragment 引用的字段都计数。超限请求不执行任何内容，返回一个 code 为 `TOO_MANY_ROOT_FIELDS` 的错误。请拆分请求，或提高对应服务的 `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` / `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS`。
- **一个请求只检查一次密码。** 同请求内额外的 `login` 不执行，返回 `TOO_MANY_CREDENTIAL_CHECKS`。每请求只登录一次。
- **同一邮箱连续登录失败会减速。** 连续五次失败后，下次尝试等待一秒，逐次翻倍到五分钟。等待期间尝试返回 `THROTTLED` 和 `retryAfterSeconds`，而不是“invalid credentials”。服务器无法计数的登录返回 `UNAVAILABLE`。登录代码应将两者作为独立错误，而非密码错误。OAuth 客户端秘密不采用该减速。
- **JetStream 预留容量增加 128 MiB**（compact 预设为 16 MiB），用于登录计数桶。Compact 的各缓存桶从 8 缩小到 4 MiB，以留出空间。如果自行设置的 JetStream 卷大小接近预留量，请检查空间。
- **新增 `CredentialAttemptStoreFull` 告警**，计数桶满时触发。登录仍可使用，但连续失败不再减速。[登录退避](../reference/graphql-api.md#sign-in-backoff)说明处理方式。

详情参见[请求限制](../reference/graphql-api.md#request-limits)和[登录退避](../reference/graphql-api.md#sign-in-backoff)。

##### 会话刷新能够容忍短暂中断 {#v0180-session-refresh}

过去，刷新会话会先消耗刷新令牌，再检查会话。检查期间发生数据库或消息代理错误，会结束会话：刷新返回“invalid or expired token”，令牌也不能再使用。现在先检查。存储错误会保留令牌有效性，返回可重试错误（“the session could not be refreshed right now; try again”），OAuth token 端点返回 `server_error`，不暴露底层错误。若会话已结束、成员关系移除或禁用，或租户拒绝访问，导致刷新被拒绝，仍消耗令牌。

只有用同一个刷新令牌重试的客户端才受益。通过 MCP 连接的 AI 代理等 OAuth 客户端，在这种中断期间收到 `server_error` 而非 `invalid_grant`，可以重试，无须用户重新授权。模拟器、负载测试和 `dcctl` 使用的 Go 客户端库不再因中断丢失刷新令牌，但仍像过去一样，在任何刷新失败时退回密码登录。控制台仍会在任何刷新失败时注销用户。升级时无须操作。

##### 连续失败的 MQTT 密码连接会减速 {#v0180-mqtt-connect-backoff}

除非设备循环使用错误 MQTT 密码连接，或自行设置 JetStream 卷大小，否则无须操作。详情参见[连续失败的连接会减速](../guides/device-credentials.md#connect-backoff)。

- **同一 MQTT 用户名连续失败 10 次后，下次尝试等待一秒**，逐次翻倍到 30 秒。等待期间的连接像错误密码一样被拒绝，即使密码正确。成功连接会重置计数。访问令牌连接和事件体内凭据不受影响。
- **知道设备 MQTT 用户名的人，只要持续发送错误密码，就能阻止设备重连。** 已连接设备不受影响，直到再次连接。
- **密码连接现在需要 JetStream。** 如果计数存储不可达，密码连接被拒绝，包括 JetStream leader 切换的短暂期间，例如 NATS 节点重启。
- 数据库中断时，持续失败的密码连接同样减速，因此前 10 次后，每次拒绝只记 debug，而不是警告。计数存储不可达每分钟记录一次警告。
- **JetStream 预留容量增加 128 MiB**（compact 为 16 MiB），用于新计数桶。
- **本版本创建实例的 compact JetStream 卷从 2Gi 增至 3Gi：** 存储容量从 1 GiB 增至 2 GiB。现有 compact 实例保留 2Gi 卷，因为 `dcctl upgrade` 不重新应用基础设施，新桶仍能放入现有卷。
- **新增 warning 告警 `DeviceCredentialAttemptStoreFull`**，桶满时触发。连接继续工作，但不减速。大型重连浪潮和攻击都可能将桶填满。

##### 凭据值按原样存储 {#v0180-credential-values}

- **设备凭据的 `credentialValue` 不再移除首尾空白。** 旧版保存时移除空白，但比较设备密码时不移除，因此首尾带空格的 MQTT 密码永远无法认证。现在按发送原值存储。只有空值，或更新时显式 `null`，才表示不保存密码。
- **反方向的行为也变化。** 过去，粘贴值带末尾换行或空格时，保存会去掉它，因此设备发送不带空白的密码仍获接受。现在空白也保存，设备会被拒绝，直到重新提交不带换行的值。
- **升级不会修改旧凭据值。** 它们已按去空白后的值保存，无法恢复丢失空白。若设备配置密码带首尾空格，请用 `updateDeviceCredential` 重新发送。
- **没有颜色的等级现在在管理员 API 中返回 `color: null`**，而非 `""`。发送 `""` 或 `null` 仍清除颜色，升级将已有空颜色转换为 null。校验前现在去除首尾空格，因此 `" amber "` 作为 `amber` 接受，过去会拒绝。控制台无须操作；自编代码若比较 `color` 与 `""`，应改为检查 null。
- **`firstName` 和 `lastName` 与其他显示文本一样移除首尾空白**，适用于 `createIdentity` 和 `updateProfile`；清除名称存为 null。读取空名称原本就返回 `null`，现在仍如此；升级将已有空名称转为 null。AI provider 的空 `endpoint` 同样处理，它读取时本来就为 `null`。旧版保存的带空白名称保持原样，直到更新请求指定该字段，才移除空白。
- **超过 GraphQL `Int` 范围的存储数值现在报错，而非返回错误数值。** 通知策略的 `throttleSeconds`、`escalateAfterSeconds`、`maxEscalations`，以及管理员 API 中租户 burst、shed-priority、held-command 和 geofence 覆盖值，过去会溢出成负数。API 写入的值始终在范围内，因此只影响以其他方式直接写入数据库的值。通知策略更新未指定上述三个字段之一时，也不再重写该字段。

##### Provisioning profile 不能再使用空白秘密创建 {#v0180-provisioning-secret}

`createProvisioningProfile` 现在拒绝空或仅含空白的 `provisionKey` / `provisionSecret`。空秘密配置会匹配同样发送空秘密的设备。不过，设备提交 provisioning secret 的路径尚未发布，因此没有实例实际暴露于该问题；检查在路径发布前补上了缺口。`updateProvisioningProfile` 早已拒绝空白值。已有空秘密配置现在不匹配任何提交的秘密，无论空与否：请使用 `updateProvisioningProfile` 设置真实秘密。

#### API 与错误 {#v0180-api}

##### GraphQL WebSocket 仅用于订阅，并在令牌过期时关闭 {#v0180-websocket}

服务 GraphQL 端点接受的 WebSocket 有三项变化：

- **只执行订阅。** Query 或 mutation 会被拒绝，错误提示使用 HTTP，不执行任何操作。过去两者都能执行，使用连接打开时提供的令牌。请将 query 和 mutation 改为 HTTP 请求。
- **认证访问令牌过期时，以代码 `4401` 关闭。** 过去只要客户端回应 ping，连接就持续开放并推送订阅。要维持数据流，请用新令牌打开新连接并重新订阅。
  - `@devicechain/client` 自动处理。已建立连接因 `4401` 关闭后，它用新解析的令牌重连一次、重新订阅，并以 `connected(true)` 向 sink 报告重连。
  - .NET SDK 的 `SubscribeAsync` 会抛出包含关闭代码的异常。请再次订阅以继续，新连接从会话读取新令牌。
  - 独立仪表板查看器不刷新令牌，因此令牌过期后实时组件停止。请重新登录。
- **没有订阅的服务不再接受 WebSocket。** 升级请求返回 HTTP 400。过去连接能够打开，但其上每个操作都失败。

##### GraphQL 文档必须使用 GraphQL 的注释和字符串语法 {#v0180-graphql-syntax}

除非自编代码或脚本手写 GraphQL 文档，否则升级时无须处理。控制台、仪表板应用、SDK、`dcctl` 和 MCP 服务都不会发送下列内容。

- **使用 `//` 或 `/* */` 注释、反引号字符串或单引号字符的文档现在会因语法错误被拒绝**，其中任何内容都不执行。旧版虽然接受，但它们不是 GraphQL。注释使用 `#`，字符串使用 `"`。
- **结束 `"""` 直接跟在反斜杠之后的块字符串也被拒绝**，例如 `\"""`。这种转义在 GraphQL 中有效，但旧版从未按规范读取，而是在这三个引号处结束字符串，并从之后继续解析文档。请通过变量发送此类文本。
- **字符串后直接跟引号的形式，例如 `"x""y"`，会被拒绝。** GraphQL 将其视为两个相邻字符串，永远不是合法值；旧版误将其读为块字符串起点。只有 `"""` 才开启块字符串，空字符串 `""` 不受影响。
- **GraphQL WebSocket 上无法解析的订阅现在返回语法错误**，而非“只接受订阅”的提示。指定了文档中不存在操作的文档也有独立错误。两者都只影响该操作，连接保持开放。

详情参见[请求限制](../reference/graphql-api.md#request-limits)。

##### 删除告警 `message` 字段 {#v0180-alarm-message}

告警过去有一个从未填充、始终为 null 的 `message` 字段。现在从所有位置删除：

- **GraphQL：** 删除 `Alarm.message` 和 `AlarmEvent.message`（`alarmStream` 订阅）。仍选择 `message` 的 query 或订阅被拒绝，返回 `Cannot query field "message"`。升级前请从自编文档中删除它。
- **`@devicechain/dashboards` 和 `@devicechain/widgets`：** `AlarmRow` 不再有 `message`，读取 `AlarmRow.message` 的代码无法编译。告警表组件不再在告警键上显示 tooltip，仪表板编辑器预览不再展示虚构告警消息。旧包仍选择 `message`，因此升级后的服务器拒绝其告警列表，组件停止加载；请与平台一同升级包。
- **通知：** 告警邮件不再有 `Message` 行，webhook 载荷不再有 `message` 键。由于值始终为空，两者此前实际上也从未出现。
- **MCP：** `list_alarms` 和 `get_alarm` 不再返回 `message`。
- **数据库：** device-management 启动时删除 alarms 表中空的 `message` 列。

滚动升级期间：

- 升级前打开的控制台标签页，以及仍运行旧版的控制台、仪表板或 MCP Pod，会在告警列表报错，直到标签页重新加载或 Pod 被替换。
- 旧版 `device-management` Pod 无法存储新告警。告警约每分钟重试一次，通常由新版 Pod 存储。如果旧 Pod 继续运行超过约五分钟，例如新 Pod 始终无法就绪，告警会被放弃并记录为死信，只有条件解除后再次发生才会触发。请尽快完成滚动更新，之后使用 `dcctl dead-letters list --kind detection-action --source device-management` 查找未触发的告警。
- 由旧版 `device-management` Pod 提供的告警列表、确认和清除，每个数据库连接可能失败一次；重复请求即可成功。

##### 重复值现在返回 `CONFLICT` 错误码 {#v0180-conflict}

除非自编代码或脚本通过错误文本识别重复，否则无须操作。详情参见[必须唯一的值](../reference/graphql-api.md#unique-values)。

- **创建、更新或重命名重复了必须唯一的值时**，`extensions.code` 现在为 `CONFLICT`。请按 code 分支处理。它表示写入与唯一值冲突，但冲突值不一定是请求提供的值：同一记录两次发布争抢下一版本号也会冲突，此时重试可成功。
- **数据库原始错误文本被替换。** 过去结尾为 `duplicate key value violates unique constraint "…" (SQLSTATE 23505)` 或 `UNIQUE constraint failed: …`，现在为 `the request conflicts with an existing record: a value that must be unique is already in use`，不暴露索引或列名称。
- **已有独立错误文本的拒绝保留原文本，并新增 code：** 重命名为已使用 token、在同租户添加第二个成员关系，以及声明配置已有的命令键。
- **`dcctl sim create` 通过 code 识别已有租户、身份或成员关系**，同名重复运行可完成；过去会在成员关系步骤停止。本版本实例应配合本版本 `dcctl`：旧实例不发送此 code，新 `dcctl` 会将重复视为错误。
- **以下并非重复，不携带 `CONFLICT`：** 使用已删除租户保留 token 创建租户，以及记录自读取后发生变化而拒绝的保存。

##### 拒绝引用和非法值返回错误码 {#v0180-reference-codes}

除非自编代码或脚本通过错误文本识别这些拒绝，否则无须操作。详情参见[被拒绝的引用或值](../reference/graphql-api.md#reference-and-invalid-values)。

- **因其他记录仍引用该记录而拒绝删除时**，`extensions.code` 为 `REFERENCE_VIOLATION`：仍在使用的设备配置、设备类型或其他类型；检测规则仍限定的实体分组；仍有租户使用的等级；仍有成员关系的租户；仍获授权的 AI provider；以及策略规则仍引用的通知渠道。错误文本不变。
- **数据库原始错误文本被替换。** 过去结尾为 `violates foreign key constraint "…" (SQLSTATE 23503)`，现在为 `the request refers to a record that does not exist, or removes one that other records still refer to`，code 为 `REFERENCE_VIOLATION`。数据库因其他完整性原因拒绝值，例如缺少必需值，则返回 `the request contains a value this record does not allow` 和 `INVALID_VALUE`。两者都不暴露表、列或约束名称，也不重复请求值。
- **两者都不是 `CONFLICT`**，因此 `dcctl` 或将 `CONFLICT` 视为“已存在”的代码不会越过它们继续。涉及重复和这些条件的拒绝，现在优先返回新 code，而非 `CONFLICT`。
- **服务器将每次这类数据库拒绝记录为警告**，指出约束、表和列，因为正常情况下服务自己的检查会先响应。可能重复请求值的 detail 不记录。

##### 超过 2147483647 的 alert level 被拒绝，另外四个数值不再溢出 {#v0180-int-range}

Alert 的 `level` 过去接受到 4294967295，但通过 GraphQL `Int` 返回，其上限为 2147483647，因此更大值读回为负数。另外四个字段存在同类问题：输出时截为 32 位，使超过 2147483647 的值变成看似合理的负数。现在都返回错误，与[凭据值按原样存储](#v0180-credential-values)列出的字段相同。

- **到达时 `level` 超过 2147483647 的 alert 现在被拒绝。** 像缺少 `type` 一样，设备消息被判为非法数据：HTTP 返回 `400`，MQTT 发布记录为死信。除非设备发送此类值，否则无须修改。
- **已有此类 level 的 alert 会使包含它的列表报错。** `level` 不能为 null，因此整个 `alertEvents` 响应为 null 并带错误，而非只遗漏一个 alert。适用于升级前数据，以及滚动期间旧 `event-sources` Pod 写入的数据。数据行保留到 retention 移除。可在 event-management 数据库执行以下查询检查：`SELECT tenant_id, device_token, count(*) FROM "event-management".alert_events WHERE level > 2147483647 GROUP BY 1, 2;`
- **最新测量值的 `classifier`** 是读数绑定的测量定义 ID。ID 超过 2147483647 时，仅该字段报错并返回 null，`value`、`unit` 和时间仍返回。控制台和 MCP 不读取该字段。（已存储测量事件中的同一 ID 为字符串，没有此限制。）
- **设备状态的 `inactivityTimeout`** 和**租户品牌的 `logoMaxHeight`** 通过正常写入都在范围内，因此只影响以其他方式直接写入数据库的值。越界 `inactivityTimeout` 会使选择它的整个设备状态列表报错。越界 `logoMaxHeight` 会使控制台租户请求失败，无法加载品牌、默认语言或地图设置，品牌编辑器可能打不开。请通过 API 用 `setTenantBranding` 重新设置 logo 高度，同时发送 title 和 colors：该 mutation 整体替换这些字段，未提供的字段会被清除。

##### 管理员 API 的 `tenantDeletions` 采用分页 {#v0180-tenant-deletions}

`/api/user-management/admin/graphql` 的 `tenantDeletions` 现在接受 criteria 参数，返回一页，与管理员 API 的 `auditEvents` 和 `deadLetters` 一样。旧调用形式会被拒绝。

```graphql
# before
tenantDeletions(completed: false, limit: 50, offset: 0) { token epoch completedAt }

# now
tenantDeletions(criteria: {pageNumber: 1, pageSize: 50, completed: false}) {
  results { token epoch completedAt }
  pagination { totalRecords }
}
```

`pageNumber` 和 `pageSize` 必填。Page size 小于 1 时读取 100 条，大于 1000 时读取 1000 条，与平台其他列表相同。过去不传 `limit` 会读取全部删除历史。`completed` 仍可选。参见[租户删除](./tenant-deletion.md#stalled-alert)。

##### 审计条目记录被修改的数据行 {#v0180-audit-rows}

二十个受审计 mutation 过去写入的审计条目 row key 和 label 为空，涉及配置、分组和资产类型版本变更、设备认领、仪表板、连接器、推理 provider 和命令状态变更。现在记录受影响行的主键，其中十一个还记录 label；命令状态变更只记录主键。旧审计条目保持原样。无须操作。

#### 可观测性 {#v0180-observability}

##### 新增和重命名的告警 {#v0180-alerts}

如果按名称设置路由或静默，下表列出 chart 新增的告警和一个重命名告警。每项都链接到详细说明。

| 告警 | 严重程度 | 报告内容 | 详情 |
| --- | --- | --- | --- |
| `JetStreamStreamNearFull` | warning | 从 `EventProcessingStreamNearFull` 重命名，现覆盖所有服务的 stream | [详情](#v0180-unread-loss) |
| `JetStreamDurableLostUnread` | critical | Consumer 丢失尚未读取的消息 | [详情](#v0180-unread-loss) |
| `JetStreamDurableStalledBehindStream` | critical | 已停止的 consumer 前方持续丢失消息 | [详情](#v0180-unread-loss) |
| `JetStreamDurableFallingBehind` | warning | Consumer 连续 15 分钟有超过 10000 条消息等待 | [详情](#v0180-live-state-batches) |
| `JetStreamReplicationUnobserved` | warning | Pod 无法读取 stream 副本状态 | [详情](#v0180-new-warnings) |
| `ReaderHeldMessagePastAckWait` | warning | 消息持有超过确认窗口并被重新投递 | [详情](#v0180-dispatch-backlog) |
| `MaxDeliveryRecordsWaiting` | warning | 耗尽尝试次数的消息通知尚未记录 | [详情](#v0180-no-outcome-dead-letters) |
| `ReplayCoveredDeliveriesExhausted` | warning | 检测引擎 checkpoint 太久未保存 | [详情](#v0180-no-outcome-dead-letters) |
| `RateLimiterOverflowInUse` | warning | 未确认租户名称的 HTTP 接入共享同一配额 | [详情](#v0180-unconfirmed-tenants) |
| `TenantsMeteredAtPlatformDefault` | warning | 服务无法读取租户上限，按默认值计量 | [详情](#v0180-unconfirmed-tenants) |
| `ReactShedLettersOverBudget` | warning | 被限流动作汇总记录，而非逐条记录 | [详情](#v0180-catch-up-metering) |
| `RateMeteringClockFallback` | warning | 对外动作计量缺少触发时间 | [详情](#v0180-catch-up-metering) |
| `ConnectorDispatchRateLimited` | warning | 连接器服务丢弃检测引擎已接纳的派发 | [详情](#v0180-new-warnings) |
| `CredentialAttemptStoreFull` | critical | 登录失败计数桶已满 | [详情](#v0180-sign-in-limits) |
| `DeviceCredentialAttemptStoreFull` | warning | MQTT 密码连接失败计数桶已满 | [详情](#v0180-mqtt-connect-backoff) |
| `DeviceFactPublishFailing` | warning | 规则、设备和属性通知发送失败 | [详情](#v0180-fact-repair) |
| `DetectFactsRepaired` | warning | 检测引擎修正了未获通知的变更 | [详情](#v0180-fact-repair) |
| `DetectFactReconcileFailing` | warning | 检测引擎与 device-management 的比较失败 | [详情](#v0180-fact-repair) |
| `TenantPurgeStalled` | warning | 租户删除没有进展 | [详情](#v0180-tenant-purge-alerts) |
| `TenantPurgeVisibilityLost` | warning | 无法获知租户删除是否有进展 | [详情](#v0180-tenant-purge-alerts) |

`ReactPoisonDropping`、`DeadLetterStoreLosing` 和 `DeadLetterWriteLost` 保留名称，但移到独立规则组（[详情](#v0180-dead-letter-rule-group)）。

##### 服务默认使用 `info` 日志级别，支持配置级别 {#v0180-log-level}

过去，每个服务无论用户希望如何都使用 `debug`。现在默认为 `info`，实例配置新增 `infrastructure.logging.level`，控制整个实例的日志级别。参见[日志级别](./observability.md#logs)。

- **只接受小写 `trace`、`debug`、`info`、`warn` 或 `error`。** 其他值，包括 `INFO` 或数字，都会阻止服务启动，日志指出键和合法值。
- **默认级别下，过去可能搜索的日志行不再出现：** 接入路径逐消息日志、消息代理读写确认和类似诊断。设置 `debug` 可恢复。`debug` 和 `trace` 为每条设备消息写日志，用于诊断，不适合日常运行。
- **启动时不再将服务配置文档写入日志。** 改为记录 `config_sha256`，即文档 SHA-256 的前 16 个十六进制字符。
- **`dcctl bootstrap` 创建的实例运行于 `info`**，`dcctl` 尚无修改级别的选项。Chart 安装可与其他 values 一起设置，例如 `--set instance.config.infrastructure.logging.level=debug`。使用 `instance.existingSecret` 时，将设置加入提供的文档，并更新 `instance.existingSecretChecksum`。

##### 数据库消息改为结构化日志，查不到记录不再记为失败 {#v0180-database-log-lines}

旧版数据库日志采用独立格式：JSON 日志之外的彩色多行文本，没有 `instance` 或 `area`，也不受 `infrastructure.logging.level` 控制。每个写租户数据的服务，每次写入事务都会打印一块 `record not found`；`event-management` 中是每个存储事件一块。这只是写入前检查的正常结果，不是错误。高事件速率下，它占据大部分日志，掩盖真实故障。

- **数据库消息现在为 JSON 日志行**：语句失败（`database statement failed`）为 `error`，耗时超过 200 ms（`slow database statement`）为 `warn`，包含 `sql`、`rows`、`elapsed_ms` 和 `caller`。查询不到记录不再记为失败，包括每次写入前的检查。
- **`sql` 字段不再显示传入值。** 只显示占位符（`$1`、`$2`、…）。旧版会填入实际值，包括写入失败日志。数据库原始错误仍照常记录，其中一些会引用被拒绝的值。
- **`sqlDebug` 现在遵守日志级别。** 逐语句日志为 `info`，因此 `warn` 或 `error` 实例不显示它们。
- **启动时无法连接自身数据库的服务，每次失败都以 `error` 记录**，消息为 `failed to initialize database, got error …`。旧版在 JSON 日志之外打印文本。

如果匹配旧文本，例如 `record not found` 或 `SLOW SQL`，请改为匹配 `message` 字段。无须配置。

##### 两个新警告：不可读取的 stream，以及连接器丢弃已获检测引擎接纳的动作 {#v0180-new-warnings}

- **`JetStreamReplicationUnobserved`**（warning，`jetstream-replication` 组）：运行中的 Pod 连续 15 分钟无法读取此前可读取的 stream 副本状态时触发。过去，服务无法读取 stream 就停止报告，其他副本告警也对此 stream 静默。消息代理中断期间，它会针对每个 Pod 的每个 stream 触发，这是有意设计，因为 chart 没有其他告警报告整个消息代理中断。距最后读取六小时后解除，不论是否恢复可读。参见[副本](./observability.md#replication)。
- **`ConnectorDispatchRateLimited`**（warning，`governance` 组）：outbound-connectors 连续 15 分钟因超过租户对外速率而丢弃派发时触发。检测引擎在派发前已丢弃超额动作，因此说明两个服务上限不一致（通常平台默认值不同），或失败发送被再次重试计量。升级前检查 event-processing 和 outbound-connectors 的 `outboundMessagesPerSecond` 和 `outboundBurst` 一致，否则按平台默认计量的租户一旦超过较低值就触发。参见[按平台默认值计量的租户](./observability.md#tenant-ceilings)。
- **`JetStreamLeaseBucketNotReplicated` 新摘要为** “The partition-lease bucket is not replicated”。名称、标签和严重程度不变。请更新匹配旧摘要文本的路由或静默。

##### 死信告警移至独立规则组 {#v0180-dead-letter-rule-group}

三个死信告警 `ReactPoisonDropping`、`DeadLetterStoreLosing` 和 `DeadLetterWriteLost` 现在位于独立 `dead-letter` PrometheusRule（组 `devicechain.dead-letter`），而非 `event-processing`。名称、标签、严重程度、阈值和说明不变，因此按名称或标签匹配的路由和静默继续工作。

- **升级期间 pending 或 firing 的告警会重新开始计时。** Prometheus 视为新规则，因此先解除，若条件仍成立，则在 `for` 等待（5 或 10 分钟）后再次触发。
- **如果按名称选择 PrometheusRule 对象**，或在 Prometheus UI 查找规则组，请加入 `dead-letter`。
- **`DeadLetterStoreLosing` 和 `DeadLetterWriteLost` 不再以 `or vector(0)` 结尾。** 无结果和返回 false 比较，对告警状态相同，因此该子句没有效果。触发和解除行为不变。

##### 租户删除停滞的两个警告 {#v0180-tenant-purge-alerts}

无法完成的租户删除仍可能让协调器自身每轮指标显示健康，因此 chart 现在在新增 `devicechain.tenant-purge` 组（PrometheusRule `tenant-purge`）中直接告警：

- **`TenantPurgeStalled`**（warning）：最早未完成删除持续时间超过配置 token hold 的两倍（默认 hold 下为 24 小时），并持续 15 分钟时触发。
- **`TenantPurgeVisibilityLost`**（warning）：user-management 删除 gauge 连续 15 分钟未采集时触发，因为此时前一个告警无法触发。

User-management 导出两个相关 gauge：`devicechain_usermanagement_tenant_purge_in_flight` 和 `devicechain_usermanagement_tenant_purge_oldest_age_seconds`。与服务一致，`tokenHoldSeconds` 为 `0` 时按默认值处理。参见[如何获知状态](./tenant-deletion.md#stalled-alert)。

##### 维护任务报告每轮结果，扫描不再严格定时 {#v0180-maintenance-passes}

十个定时维护任务，包括扫描、协调器和调度器，现在各导出三个序列：`devicechain_<area>_<task>_passes_total{outcome}`、`…_pass_duration_seconds` 和 `…_last_success_timestamp_seconds`。Outcome 为 `complete`、`partial`、`failed`、`skipped` 或 `cancelled`。`skipped`（其他副本持有任务锁）和 `cancelled`（服务停止）不是故障，跳过一轮不会更新最后成功时间。[维护轮次](./observability.md#maintenance-passes)列出任务。

其中五个任务——user-management 的死信扫描和租户删除协调器、notification-management 的 retention 扫描和升级调度器，以及 event-management 的 anchor 扫描——现在每轮在间隔前后各 10% 范围内随机启动，不再精确按间隔执行，避免同时启动的副本同一时刻访问数据库。无须配置。

#### 部署和容量配置 {#v0180-deployment}

##### device-management 和 event-management 可使用更多 CPU {#v0180-cpu-limits}

`device-management` 和 `event-management` 现在各最多使用 2 核 CPU，其他后端服务保留 500m 上限。在四节点测试集群中，500m 下 `device-management` 最多每秒解析约 720 个事件，低于每租户默认每秒 1000 条消息配额，因此满配额发送会持续增加积压。`event-management` 在此速率下需要约半核，也就是原全部上限，因此会成为下一个瓶颈。参见[服务容量配置](./bootstrap.md#service-sizing)。

升级逐个 Pod 重启这两个服务一次。Requests 不变，因此节点不需为 Pod 预留更多空间，`--compact` 实例保留更低 requests。

- **服务的 `functionalAreas.<service>.resources` 现在逐键合并覆盖顶层 `resources`**，不再整体替换。控制台 `frontend.resources` 不受影响。若 values 为单服务设置 `resources`，升级前检查渲染的 Pod：
  - 只设置 `requests` 的服务过去没有 limits，现在获得顶层 limits。若要保留无上限运行，请从顶层移除该 limit，并在需要上限的服务中分别设置。
  - 只设置 `limits` 的服务过去由 Kubernetes 赋予相同 requests；现在获得顶层 requests（100m 和 128Mi，`--compact` 为 25m 和 64Mi），节点预留减少，Pod QoS class 也可能变化。请设置该服务 `requests` 以保留预留量。
  - `device-management` 或 `event-management` 的块未设置 `limits.cpu` 时，现在获得新 2 核上限。
  - Request 超过合并后的 limit 时，chart 渲染拒绝并指出服务，例如只设置 `requests.memory: 512Mi`，顶层 limit 却是 256Mi。请同时设置 limit。
  - 服务 `resources` 下除 `requests`、`limits` 和 `claims` 外的键被拒绝。
- **顶层 `resources.limits.cpu` 超过 2 核时，不再传递给 `device-management` 或 `event-management`**，因为这两个服务自己的 2 核键优先。若只提高顶层 CPU 上限，例如 4，升级会将两者**降低**到 2 核。请设置 `functionalAreas.device-management.resources.limits.cpu` 和 `functionalAreas.event-management.resources.limits.cpu` 保留原值。同理，过去可渲染的超过 2 核顶层 `resources.requests.cpu`，现在会被拒绝，错误说明它超过服务自己的 limit；也需提高该 limit。
- **命名空间的 `limits.cpu` ResourceQuota，或带 CPU `max` 的 LimitRange，可能拒绝这两个 Pod**，因为 limits 更高。DeviceChain 不创建它们，请检查自行添加的配置。
- **要保留旧上限**，将 `functionalAreas.device-management.resources.limits.cpu` 和 `functionalAreas.event-management.resources.limits.cpu` 设为 `500m`，恢复每秒约 720 事件的旧处理上限。

##### 集群内备份存储改用持续维护的镜像 {#v0180-backup-store-image}

`dcctl install` 在 `dc-system` 中运行对象存储，用于数据库备份，过去从 `quay.io/minio/minio` 拉取 MinIO 镜像。这些镜像不再发布，registry 拒绝匿名拉取，因此未缓存镜像的机器上，`dcctl install` 会在对象存储 `ImagePullBackOff` 时停止。已有缓存的集群继续运行，但仅限对象存储 Pod 留在已缓存节点；drain、驱逐或节点替换后迁移到其他节点，就可能无法启动，归档也随之停止。升级消除该风险。

本版本拉取按摘要固定的 `cgr.dev/chainguard/minio`，是同一 MinIO server 的受维护 fork 构建，仍使用 AGPL-3.0。可直接读取旧服务器写入的数据。

- **升级前，确保节点能从 `cgr.dev` 拉取**：允许通过出站规则，或按本版本固定摘要建立 `cgr.dev/chainguard/minio` 镜像副本。拉取失败时，对象存储不可用，数据库将预写日志留在本地，直到恢复。
- **默认备份目标的新安装重新可用。**
- **现有集群中，`dcctl install` 会将对象存储 Pod 重启一次到新镜像。** 保留已有备份和预写日志。重启期间归档暂停，数据库在本地保留日志，持续到新镜像拉取并启动。
- **不能通过安装旧版撤销。** 旧版 `dcctl install` 会停止对象存储，再因无法拉取其镜像失败；归档停止，直到再次运行本版本 `dcctl install`。
- **如果旧版 `dcctl install` 因此失败**，重新运行本版本 `dcctl install`：它会替换前次遗留对象存储（参见[失败的 `dcctl install` 可以重新运行](#v0180-dcctl-install-rerun)）。

无须配置。通过 `--backup-credentials-file` 使用自有对象存储的用户没有变化。

##### 失败的 `dcctl install` 可以重新运行 {#v0180-dcctl-install-rerun}

过去，如果 `dcctl install` 在备份对象存储启动时失败，例如镜像无法拉取，或在该阶段中断，后续每次运行都会在修改任何内容前报 `Unexpected Identity Change`，指出 `kubernetes_deployment_v1`。此问题发生于 Terraform 1.12 及以上，唯一出路是删除集群及 `~/.devicechain/clusters/` 中目录。现在，重跑会删除半建成对象存储、重新创建，并等待就绪。已经卡住的集群也可同样恢复：修复原因后再次运行同一个 `dcctl install`，无须手动步骤。原因仍存在时，重跑仍失败，不会误报安装完成。

- **`dcctl install` 现在在报告集群安装完成前确认备份对象存储滚动更新已完成。** 过去，变更超时（例如新镜像拉取失败）后，下一次运行会因变更已经记录而接受它。现在会等待对象存储最多五分钟，然后失败并指出对象。
- **Kubernetes provider 从 2.38.0 升至 3.2.1。** 修复包含在该版本中。每个集群首个应用或销毁基础设施的 dcctl 命令会下载它。集群内容本身不因此变化。
- **基础设施 provider 现在固定精确版本**（Kubernetes 3.2.1、Helm 2.17.0），dcctl 每次运行将各 root 的 `.terraform.lock.hcl` 更新到这些版本。过去集群保留首次安装解析出的版本。现在每次应用或销毁基础设施，包括 `dcctl destroy`，都查询 registry 获取版本，因此 registry 或已配置 provider 镜像必须可达。不再声明未使用的 TLS provider。
- **旧 dcctl 无法操作已由本版本运行过的集群。** 其 `init` 会拒绝 lock file，因为它记录的版本不被旧配置允许。
- **直接运行 OpenTofu 配置而非通过 dcctl 时**，请在每个 root 运行一次 `tofu init -upgrade`；普通 `init` 会拒绝旧版本 lock file。

##### 发布归档附带签名的构建来源证明 {#v0180-provenance}

从 `v0.18.0` 起，每个发布在 `dcctl` 和 `dc-edge-agent` 归档旁提供 `devicechain_<version>_provenance.sigstore.json`（版本不带 `v`，例如 `devicechain_0.18.0_provenance.sigstore.json`），记录并签名构建它们的 workflow 运行。验证已下载归档：

```bash
gh attestation verify <archive> --repo devicechain-io/devicechain
# or offline, against the bundle from the release
gh attestation verify <archive> --repo devicechain-io/devicechain \
  --bundle devicechain_0.18.0_provenance.sigstore.json
```

无须操作。

### v0.24.0：可自行编写的计算指标，以及承载更多事件的事件路径 {#v0240-upgrade}

`v0.24.0` 可从 `v0.23.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行
`dcctl upgrade`，如 [v0.17.0](#v0170-upgrade) 所述。除新的 `--version` 外，还要把集群安装时用的设置
（例如 `--ha` 和 `--database-node-selector`）一并传给 `dcctl install`。**无法从 `v0.22.0` 或更早版本
原地升级到本版本：请先升级到 `v0.23.0`。** 它的四项迁移共新增两张表和两个索引，不改写
任何已有的行（见[升级期间](#v0240-during)）。**升级后只能向前，不能回退：** `v0.23.0` 无法在本版本写入的
配置上启动（见[回退](#v0240-going-back)）。从 `v0.22.0` 或更早版本升级时，也请阅读
[`v0.23.0` 说明](#v0230-upgrade)；任何跨越该版本的升级都适用。

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
     `devicechain_eventsources_capture_body_credential_strip` 是否为 `1`。
  2. 推出不再在正文中发送凭据的 SDK 或固件。
  3. 观察 `devicechain_eventsources_total_msg_credential_in_mqtt_body`，直到它停止增长的时间超过你最慢那台设备的
     上报间隔。**请边观察边收集设备清单。** `event-sources` 日志中的警告会写明每个仍在发送凭据的设备所属的租户和设备
     （每个 `event-sources` pod 上每台设备每小时一次）；请在这一阶段把这些租户和设备对从日志中复制出来，因为只有这里会点名它们。
  4. 执行 `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=refuse`。此后 `event-sources`
     日志不再点名任何设备，指标也没有设备标签；留下的唯一按设备的记录是解码失败 stream，它保留设备名称并对凭据做了涂抹。
  5. 轮换第 3 步收集的清单中每台设备的凭据（以及第 4 步之后解码失败 stream 点名的设备）。

  `strip` 不会消除暴露：代理在这两种设置生效之前就已存下了正文，所以 `strip` 与 `refuse` 留下的静态明文相同，
  两者都需要第 5 步。`strip` 只作用于平台 MQTT 代理。之后不带该选项的 `dcctl upgrade` 会保留这个值。它是过渡性
  的：计划在 `v0.26.0` 中移除，届时只要仍设置着它，`event-sources` 就将拒绝启动。使用 chart 安装（不是 `dcctl`）
  时，请设置 `functionalAreas.event-sources.config.capture.bodyCredential`。

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
| `event-sources` 的 `capture.bodyCredential`（新增，固定设置，位于 `functionalAreas.event-sources.config`） | 无：平台 MQTT 代理上正文中的凭据会被接受 | `refuse` | 仅在升级期间设为 `strip`（过渡性设置，计划在 `v0.26.0` 中移除）。见[设备必须停止在 MQTT 事件正文中发送凭据](#v0240-mqtt-body-credential)。 |
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

### v0.23.0：带范围限制的客户用户，以及丢事件更少的 MQTT 接入 {#v0230-upgrade}

`v0.23.0` 可从 `v0.22.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行
`dcctl upgrade`，如 [v0.17.0](#v0170-upgrade) 所述。除新的 `--version` 外，还要把集群安装时用的设置
（例如 `--ha` 和 `--database-node-selector`）一并传给 `dcctl install`：只要集群上还有实例在运行，它就拒绝
更改集群的设置。与 [`v0.22.0`](#v0220-upgrade) 不同，本版本
**会执行迁移**：七个服务新增了列、索引或表，`user-management` 还把角色令牌上的唯一索引换成两个：一个用于
运营方的角色目录，一个用于各租户自己的角色。迁移都不会改写已有的行，仅有的回填是记录已存在的客户和
设备组的令牌，并且每项迁移被中断后都可以安全地重新运行。从 `v0.21.x` 或更早版本升级时，请先阅读
[`v0.22.0` 说明](#v0220-upgrade)；任何跨越该版本的升级都适用。由 `v0.16.0` 或更早版本构建的实例仍须
[销毁后重建](#pre-declaration-recreate)。

本版本新增**客户用户**：租户中被限制在你的部分客户或若干设备组内的成员，只能看到那部分设备，使用由
你的租户自定义的角色，并通过你自己发送的邀请加入。其余内容大多围绕两件事：不丢事件，以及不误触发
告警。保存 MQTT 设备事件、等待 `event-sources` 转发的流，现在排空深度翻倍，并且容量可以由你来设定；
阈值告警跟随设备最新的读数；设备所有者的变更即使在 pod 恰好终止时也会到达 device-state。对某些人
有影响的变化：

- **MQTT 捕获流的上限增大。** 它原来被限制在 256 MiB，现在取完整的热流上限，因此按旧数值紧凑规划的
  JetStream 卷可能让 `event-sources` 无法启动。见[捕获流的上限增大](#v0230-capture)。
- **集群化代理（`--ha`）会逐个重启其服务器，且仅一次**，以开启服务器之间的压缩。见
  [代理路由压缩](#v0230-route-compression)。
- **计算指标现在会被计算**，但在你把某个层级的 `calculatedMetricsPerProfile` 调到 0 以上之前保持关闭。
  不要在滚动升级期间这样做。见[计算指标](#v0230-calculated-metrics)。
- **阈值告警跟随设备最新的读数**，而不是最后到达的读数。见[阈值告警与迟到读数](#v0230-threshold)。
- **新增两项权限，`role:read` 和 `role:write` 现在也在租户内生效。** 见
  [客户用户、受限角色及其背后的权限](#v0230-customer-users)。
- **已删除的客户或设备组的令牌不能再被复用**，对受限于部分客户的成员，无法限定范围的操作会被拒绝，
  而不是给出应答。见[客户用户、受限角色及其背后的权限](#v0230-customer-users)。
- **事件路径上的两个消息标识符改变了格式**，滚动升级期间会出现一个很小的重复窗口。见
  [事件 id 与关联头](#v0230-ids)。
- **下一个版本会改变事件在线路上的写入方式**，并且只能经由本版本到达。见
  [先升级到本版本，再升级到下一版本](#v0230-next-wire)。
- **若干默认值变化：** 控制台不能被其他站点嵌入框架，捕获流的发布窗口为 1024，写入方向其流的
  领导者发布。见[变化的默认值](#v0230-defaults)。
- **在测试集群上测得的一些限制**适用于以 MQTT 为主的安装。见[已知限制](#v0230-known-limitations)。

#### 升级之前 {#v0230-before}

1. **如果你调高过 `streamMaxBytes`，或自行规划或限制了 JetStream 卷的大小，请先检查余量。** 捕获流
   的预留量增加 `streamMaxBytes` 减去 256 MiB，默认值下为 768 MiB，另有新的 `device-ownership` 流预留
   128 MiB。见[捕获流的上限增大](#v0230-capture)。
2. **如果有应用或脚本删除某个客户或设备组后又以同一令牌重新创建**，请计划改用新令牌。见
   [客户用户、受限角色及其背后的权限](#v0230-customer-users)。
3. **如果你依赖阈值规则**，请阅读[阈值告警与迟到读数](#v0230-threshold)，避免对现在会触发的告警
   感到意外。
4. **准备在滚动升级结束前让每个层级的 `calculatedMetricsPerProfile` 保持为 0。** `v0.22.0` 没有这项
   设置，所以每个层级都从 0 开始，升级前无需改动任何东西。在每个 `device-management` 和
   `event-management` 副本都运行本版本之前，不要调高它，也不要发布计算指标。见[计算指标](#v0230-calculated-metrics)。

#### 升级期间 {#v0230-during}

- **迁移在首次启动时运行**，涉及 `user-management`（每个成员关系的范围、每个角色的租户、邀请）、
  `device-management`（每条告警的客户、设备组的访问范围、已停用的客户和设备组令牌、所有权变更队列、
  指标定义上的公式）、`command-delivery`、`device-state`、`update-management`、
  `dashboard-management`（仪表板共享）和 `notification-management`（静默时段、告警的周期）。新增的列
  在所有已有行上为空，这是有意为之，见[下文](#v0230-customer-users)。新索引不使用 `CONCURRENTLY`
  构建，因此被建索引的表上的写入会等待构建完成，除非表非常大，否则时间很短。
- **在集群化代理上，代理的服务器会逐个重启，且仅一次**，因为新的默认压缩属于代理配置的一部分。
- **`event-sources` 在首次启动时原地调高捕获流的上限。** 不会重建任何流，也不会丢失任何消息。

升级后回退到 `v0.22.0` 读取的是同一个数据库：变化是新列、新索引、新表，以及角色令牌索引的拆分。但只要
还有成员被限制在客户或设备组内，或者有租户定义了自己的角色，就不要回退：`v0.22.0` 既不读取成员关系的
范围，也不读取角色所属的租户，这类成员将不再受到限制，租户自己的角色在它看来会像运营方目录中的角色。
请先禁用或移除这些成员，并删除租户定义的角色。在计算指标投入使用后回退 `event-management`，可能把重新
投递的消息存储两次（见[计算指标](#v0230-calculated-metrics)）。

#### 升级之后 {#v0230-after}

- **无需任何操作。** 所有新设置都有默认值，在你授予范围之前，没有成员带有范围。
- **如果之前没有检查，请检查捕获流的余量**，并保持 `metrics.enabled` 和 `metrics.alerts` 开启（默认
  均开启），以便安装 `JetStreamCaptureBacklogWillOverflow`。
- **升级之前产生的告警、命令和更新分配，对受限于客户或设备组的成员不可见。** 服务器从本版本起才记录它们的
  客户。设备的实时状态会在它下一次上报或变更所有者时被记录。

##### 捕获流的上限增大 {#v0230-capture}

来自 MQTT 设备的事件在捕获流中等待 `event-sources` 转发。代理已经向设备确认了每一条，所以流写满时
会丢弃最旧的未转发事件，而设备永远不会知道。在每秒 12,000 个事件的负载测试中，已确认事件的 7.2%
就是这样丢失的：流只有 256 MiB，而平台每秒只能跟上约 7,100 个。

- **排空深度翻倍。** `capture.publishWindow` 为 1024，原为 512。在只延迟确认的基准测试中，相同延迟下
  的转发速率翻了一倍。它是 `event-sources` 的固定设置，范围 1 到 1024；超出范围的值会使服务停止。
- **该流取完整的热上限。** 它原来被限制在 256 MiB（热上限更低时取热上限），且没有任何设置能提高这个
  上限。现在它像其他热流一样跟随 `streamMaxBytes`：默认 1 GiB，`--compact` 实例上为 64 MiB，该情形下
  没有变化。在默认值下，JetStream 磁盘预留量从 10,696 MiB 增加到 11,592 MiB，默认卷允许 14 GiB：其中
  768 MiB 来自捕获流，128 MiB 来自新的 `device-ownership` 流。
- **如果你调高了 `streamMaxBytes` 并且按紧凑的余量规划了卷，预留量会增加该值减去 256 MiB。**
  JetStream 在创建或调高流时预留其上限。预留量超过代理的限制，会使 `event-sources` 在启动时以
  `insufficient storage resources available` 停止，并持续停止，直到你增加空间（见
  [引导实例](./bootstrap.md)）或调低上限。
- **被改为丢弃新消息的流会被改回。** 写满后拒绝新消息的捕获流不会通知任何设备，因为代理先确认了发布，
  最新的事件会丢失，且没有任何计数。每个声明的流都会恢复为丢弃最旧消息，服务会记录日志。
- **新增一条严重告警 `JetStreamCaptureBacklogWillOverflow`**：当捕获积压至少占该流的十分之一，且按过去
  三分钟的增长速率将在十分钟内写满该流，并持续一分钟时触发。它在 `metrics.enabled` 和 `metrics.alerts`
  开启时随其他告警一起安装。见[可观测性](./observability.md)。

##### 代理路由压缩 {#v0230-route-compression}

集群化代理的服务器会互相复制每一个已存储的事件。`nats_route_compression` 压缩这部分流量，在
集群化代理（`--ha`，或 `nats_cluster_replicas` 大于 1）上它现在默认为 `s2_fast`。单服务器代理不受影响。
在三节点集群上，每个事件的服务器间流量减少了 60%；压缩使每台服务器多用 2.6%–2.9% 的 CPU，而代理每个
事件的总 CPU 仍下降了约 4%。`dcctl upgrade` 会逐个重启代理的服务器，仅一次，以开启
压缩。你自己设置的值（包括 `off`）会被保留。见[代理服务器之间的压缩](./bootstrap.md#route-compression)。

##### 阈值告警与迟到读数 {#v0230-threshold}

阈值告警过去跟随最后到达的读数。平台迟收到的读数，可能触发已被更新读数清除的告警，也可能清除仍被
更新读数保持的告警。现在：

- **告警处于触发状态时，比设备最新读数早不超过十分钟的读数会被丢弃并计数**，因此它无法清除告警。更早
  的读数会照常评估，因此被调慢的设备时钟无法让告警一直保持触发。
- **没有告警触发时，落后超过迟到容忍度的匹配读数，仅当它在平台收到后超过 30 秒才进入检测队列时才会
  被丢弃**，重试的发布就是这种情况；两个时间中有一个未知时也会被丢弃。容忍度从引擎见到的最新读数算起。
  其他匹配读数都会触发告警，因此时钟偏慢的设备，或上传断网期间
  所存读数的设备，现在会在这些读数到达时据此触发告警。
- **计数窗口和变化率规则按读数自身的时间定位**，与持续时长规则一致。它们的触发和清除落后于引擎见到的
  最新读数一个迟到容忍度，而静默期之前的最后一个读数，在引擎空闲之后按墙钟时间决定。
- **滑动窗口规则按设备最新读数来读取窗口**，因此迟到的读数只与当时和它处于同一窗口内的读数一起计数。

`devicechain_eventprocessing_detect_late_samples_total` 统计这些丢弃。见[检测引擎页面](./detection-engine.md)。

##### 客户用户、受限角色及其背后的权限 {#v0230-customer-users}

现在可以把成员限制在你的部分客户、动态设备组或两者之内（合计最多 16 个），他们只能看到属于这些
范围的设备、告警、事件、命令和仪表板。租户管理员在控制台中邀请成员、定义角色并共享仪表板；完整的
章节见[成员、角色与客户用户](../user-guide/members-roles-and-customer-users.md)。升级时需要关注：

- **新增两项权限。** `member:read` 列出成员和邀请，`member:write` 负责邀请、分配角色和范围、启用、
  禁用和移除。它们是租户权限，不在只读基线中。内置的租户管理员角色持有 `*`，因此两项都有。
- **`role:read` 和 `role:write` 现在在两个平面上生效。** 在实例管理平面上，它们仍然作用于运营方的
  角色目录。由租户角色持有时，它们现在允许成员列出并定义该租户自己拥有的角色。
- **成员访问权限的变更在其下一次刷新时生效**，即访问令牌的有效期内（默认 15 分钟），受限于客户或设备组
  的成员为 5 分钟。它不会让这个人退出其所属的其他租户。
- **无法限定范围的操作会被拒绝**：受限成员会得到 `FORBIDDEN`，`extensions.reason` 为
  `SCOPE_UNSUPPORTED`，无论其角色如何。可以限定范围的操作只返回该成员拥有的内容，范围之外的记录以
  `NOT_FOUND` 应答。见[当你的访问被限制在部分客户时](../reference/graphql-api.md#limited-access)。
- **已删除的客户或设备组的令牌在其租户内停用。** 以该令牌调用 `createCustomer` 或
  `createEntityGroup` 会返回 `INVALID_VALUE` 并附带说明原因的消息，只有删除整个租户才会释放它。
  仍然存在于该令牌上的客户仍返回 `CONFLICT`。否则，仅凭令牌指向客户的记录会把旧客户的历史展示给
  新客户。本版本之前已删除的客户和设备组的令牌不会被停用。
- **设备所有权的变更总会到达 device-state。** 变更与一条排队的通知一起提交，该通知会被重试直到代理
  确认，而此前是在提交之后才发布。两步之间 pod 终止时，旧所有者的成员会一直看到该设备的实时状态，
  直到它的下一个事件，而对于静默的设备，这个事件永远不会到来。
- **仪表板可以与客户和设备组共享。** 客户用户只能看到与其共享的已发布仪表板。绑定到区域或资产的
  组件需要读取关系图，而他们没有该权限，会显示**Not available for customer users**。

##### 事件 id 与关联头 {#v0230-ids}

代理用来丢弃重复发布的 id，以及在服务之间携带关联 id 的消息头，都变短了：消息头为 `Dc-Cid`（原为
`Dc-Correlation-Id`），id 使用 36 进制。新 id 绝不会等于旧 id。滚动升级时两种版本的 pod 同时存在，
因此在滚动期间，发布确认丢失、或所在 pod 未排空就停止的事件，可能被投递两次。已存储的遥测数据不会
重复，因为事件的键是其内容的摘要，但计数、重复或滑动聚合规则可能把该事件计两次。升级期间在途消息
不携带日志关联。最后一个旧 pod 消失 10 分钟后，该风险结束。

##### 先升级到本版本，再升级到下一版本 {#v0230-next-wire}

事件以 protobuf 消息的形式经过两个保留流，本版本可以读取更小的形式：时间点用纳秒计数表示，设备凭据
封印使用二进制，每条消息约小三分之一。**默认情况下它仍写入旧的形式**，因此上一版本的 pod 可以读取
全部内容。下一个版本默认写入较小的形式，早于本版本的 pod 会丢弃这些消息或将其送入死信。请先把每个
实例升级到 `v0.23.0`，之后再升级到下一个版本。`infrastructure.messaging.wireFormat`（`legacy` 或
`compact`，须完全按此书写）用于选择形式。只有在每个 pod 都运行本版本之后才设置 `compact`，回退到更早
版本之前要把它改回 `legacy`。

##### 变化的默认值 {#v0230-defaults}

- **控制台不能被其他站点嵌入框架。** 其页面带有 `Content-Security-Policy: frame-ancestors 'self'` 和
  `X-Frame-Options: SAMEORIGIN` 提供。`/dash` 查看器仍可嵌入，因为在墙面显示屏上嵌入它是受支持的用法。
  其他来源的页面若嵌入控制台本身，将不再显示。
- **`capture.publishWindow` 为 1024**（原为 512）。
- **写入方向其流的领导者发布。** `infrastructure.nats.affinity.publishToLeader` 默认开启：
  `device-management`、`event-sources` 和 `sparkplug-ingest` 中的流水线写入方会与领导其流的服务器建立
  第二条连接，失败时退回到常规连接。在三节点集群上，
  单个 `device-management` pod 的上限提高了约 6%。设为 `false` 可关闭。

##### 计算指标 {#v0230-calculated-metrics}

指标定义可以携带基于配置文件中其他指标的公式，`testMetricFormula` 可预览公式。已发布的公式现在会在解析
每个读数时计算，并作为普通测量值存储，因此历史、每小时汇总、最新状态、检测和实时流都会收到它。设备以
计算指标的键发送的值会被丢弃，以计算出的值为准；无法得出值的公式只会在该读数中跳过这一个指标。

**只要每个层级的 `calculatedMetricsPerProfile` 都为 0，计算指标就保持关闭**，这是默认值；`v0.22.0` 没有
这个设置。在租户的层级允许之前，发布带有计算指标的配置文件会以 `LIMIT_EXCEEDED` 被拒绝，因此在此之前
升级不会产生影响。升级前请检查你的层级。如果有大于 0 的，请把它设为 0，或者不要发布计算指标，直到每个
`device-management` 和 `event-management` 副本都运行本版本，然后在最后一个旧的 `device-management` pod
停止后至少再等待 `metricDefCacheTtlSeconds`（默认 300 秒）。在新旧版本混合的滚动期间发布，可能把设备发送
的值永久存储在计算指标的键下，而在新旧 `event-management` 副本之间重新投递的消息可能被存储两次。在计算
指标投入使用后回退 `event-management`，也有同样的重复风险。

##### 新增告警与指标 {#v0230-observability}

- `JetStreamCaptureBacklogWillOverflow`（严重），见上文；
- `JetStreamCaptureBacklogGrowing`（警告），它的早期预警：同样的积压检查，时间范围为二十分钟；
- `JetStreamCaptureLeaderChanged`（警告）：捕获流在有事件到达时更换了领导者，因此在切换期间发布的 MQTT
  事件可能已被确认却丢失（见[已知限制](#v0230-known-limitations)）；
- `DeviceStateDroppingEvents`（警告）：device-state 在最后一次投递尝试后丢弃了一个事件，使设备的连接
  状态、某个读数或其位置一直过时，直到更新的事件到达；
- `devicechain_notificationmanagement_quiet_hours_total{outcome}`，统计因静默时段被暂缓、释放或丢弃的
  通知；
- `devicechain_devicemanagement_calcmetric_evaluations_total`、
  `devicechain_devicemanagement_calcmetric_skipped_total{reason}` 和
  `devicechain_devicemanagement_calcmetric_device_key_dropped_total`，用于计算指标；
- `devicechain_commanddelivery_command_delivery_window_commands_undefined_total`，统计因配置文件不再
  定义而失败的命令；
- `devicechain_sparkplugingest_presence_probes_total` 和
  `devicechain_sparkplugingest_presence_probe_disconnects_total`，用于 Host 的存活探测；
- `jetstream_consumer_leader_local`、`jetstream_publish_leader_local` 和
  `jetstream_consumer_leader_moves_total`，用于领导者的放置；`jetstream_stream_leader_changes_total`，
  统计流的领导者更换。

共享处理器计数器的每种结果现在都以零导出，因此 pod 第一次被抓取之后的第一次丢弃也能被告警看到。没有任何
指标被重命名或移除。`jetstream_publish_window_full_seconds_total`、`jetstream_publish_window_full_total`
和 `jetstream_publish_duplicates_total` 新增了 `writer` 标签，这会拆分它们的序列；对该标签求和的查询结果
与之前相同。

#### 其他变化 {#v0230-what-changed}

##### 已知限制 {#v0230-known-limitations}

以下结果是在一个测试集群上测得的：三个服务节点，每个 4 个 vCPU、8 GiB 内存，代理开启 TLS，使用默认线路
格式，每次运行 300 到 600 秒。它们描述的是那个集群，而不是对你的集群的保证。

- **在高负载下捕获流更换领导者时，MQTT QoS 1 发布可能在被确认后丢失，且不被计数。** 在每秒 12,000 个
  事件时，该流的领导者让位了一次，在流没有领导者的五秒内发布的事件，在设备收到确认之后丢失了。没有任何
  计数器记录丢失的事件。`JetStreamCaptureLeaderChanged` 会报告有事件到达时发生过领导者切换，其时间界定了
  需要在你自己设备的记录中核对的时间窗口。消除该问题的工作正在进行中。
- **那个集群能持续跟上的 MQTT 速率约为每秒 6,000 个事件**，瓶颈在代理。每秒 12,000 个时积压不断增长；
  超过捕获流的上限后，最旧的事件会被丢弃，这部分丢失会被计数并触发告警。同一集群上的 HTTP 接入达到
  约每秒 14,000 个。
- **在以 MQTT 为主的负载下，代理的内存可能接近其上限。** 每台服务器使用了 3.0–3.2 GB，而其
  `GOMEMLIMIT` 为 3,276 MiB。对于以 MQTT 为主的安装，请调高 `nats_memory_limit`（默认 4Gi，在实例的
  `terraform.tfvars` 中设置，并通过 `dcctl upgrade` 应用；`GOMEMLIMIT` 随之设为它的 80%）。见
  [消息代理](./bootstrap.md#broker-sizing)。

##### 本版本的其他内容 {#v0230-also}

- **通知策略的静默时段。** 以 IANA 时区表示的每周时段，会暂缓低于你所选严重级别的通知，并在静默
  时段结束时发送。见[静默时段](../guides/notification-channels.md#quiet-hours)。
- **重新触发的告警会再次升级。** 清除后又重新触发的告警过去会保留第一个周期的确认，因此它的第二个
  周期从不升级。
- **看板读回实时推送错过的数据。** 连接中断之后，绑定到单个设备的折线图、设备群表格、每台设备一根柱的
  柱状图和统计卡片会读回中断期间发送的读数，因此图表不再在空缺处画一条直线。多设备的图表改为显示断开。
  这解决了 `v0.22.0` 已知限制的大部分。
- **设备页面新增“发送数据”标签页**，为设备的第一个读数写出 `mosquitto_pub` 或 `curl` 命令，控制台无法
  知道的内容（如实例 id、主机和密码）以占位符表示。
- **`dcctl enable-area`** 在运行中的实例上开启一个功能区域。没有关闭区域的命令。
- **Sparkplug。** 持续连接的 Host 每隔 `presence.probeSeconds`（默认 1800）重新探测其已断言设备的
  存活状态，因此死亡证书从未到达的设备不再一直显示在线。包含超过 16,384 个指标的消息，其读数一条都不会被存储。
- **MCP。** 每次工具调用都会写出一行结构化日志 `audit=mcp_tool_call`，包含租户、操作者、`client_id`、
  工具和结果。
- **命令。** 因维护窗口而被暂缓的命令，在配置文件变更使其变为普通命令时仍保持暂缓；正在等待窗口、而其
  配置文件不再定义的命令会带着原因失败，绝不会发送。
- **事件存储迁移**以 TimescaleDB 2.29 及更高版本能够规划的方式查找压缩块。
- **每个 `device-management` pod 可有多个入站读取器：** `resolution.inboundReaders`，范围 1 到 4，
  默认为 1。
- **计算指标**，默认关闭；见[计算指标](#v0230-calculated-metrics)。
- **集群化代理**还可以把消费者的领导者移到其读取方旁边：`infrastructure.nats.affinity.consumerLeaders`，
  默认关闭。
- **与自身原始发布竞争的重新发布**会被重试，不再被计为发布失败或丢失的事件。
- **仪表板加载更快。** 图表引擎、组件描述符和目录在首次需要时才加载，因此首次下载约小 173 kB（gzip）。
- **控制台**不再在令牌即将过期时连续刷新两次，没有 `device:write` 的成员看到的设备“基本”标签页为只读。

### v0.22.0：仪表板改用方形单元格网格，事件路径等待更少 {#v0220-upgrade}

`v0.22.0` 可从 `v0.21.0` 或 `v0.21.1` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行
`dcctl upgrade`，如 [v0.17.0](#v0170-upgrade) 所述。无新增迁移：没有服务改变其数据库模式。本版本
包含 [`v0.21.1`](#v0211-upgrade) 已发布的四项修复；从 `v0.21.0` 升级时，也请阅读该节中要求你做的
那一项检查。从 `v0.20.0` 或更早版本升级时，还请阅读 [`v0.21.0` 说明](#v0210-upgrade)。由
`v0.16.0` 或更早版本构建的实例仍须[销毁后重建](#pre-declaration-recreate)。

本版本的大部分内容是仪表板：随屏幕自适应的方形单元格网格、按单元格吸附的编辑器、带暂停的看板时间
范围，以及十三个新组件，其中包括每台设备一行的设备群表格。其余部分让事件路径等待更少：多个服务
保持更多在途发布，device-state 每个事件的数据库工作更少，平台每次拉取读取 256 条消息。对某些人
有影响的变化：

- **仪表板以新格式存储。** 旧看板在加载时转换，布局为近似转换。组件设置除时间序列图表的样本数外
  不会丢失任何内容。见[仪表板以新格式存储](#v0220-dashboard-format)。
- **npm 包 `@devicechain/dashboards` 和 `@devicechain/widgets` 有不兼容变更**，`0.21` 的包会把
  `v0.22.0` 保存的看板画错，且不报错。见[如果你基于 npm 包开发](#v0220-packages)。
- **绑定到设备组的单设备组件现在会拒绝该组**，而不再显示最后上报的那台设备。见
  [单设备组件拒绝设备组](#v0220-group-refusal)。
- **三个默认值变化：** 拉取批量（128 改为 256）、device-management 的 CPU 请求（800m 改为
  450m），以及外部 MQTT、Sparkplug 和在线状态保持的在途事件数；后者也提高了这些至多一次路径上
  pod 突然终止时可能丢失的数量。见[变化的默认值](#v0220-defaults)。
- **`dcctl upgrade`、`destroy` 和 `sim` 会拒绝指向与实例记录不同集群的 `--kube-context`。**
  以前它会静默覆盖记录。见[dcctl 拒绝冲突的 `--kube-context`](#v0220-kube-context)。
- **为维护窗口保留的命令跟随设备类型的当前窗口**，取消分配窗口会立即释放这些命令。见
  [保留的命令跟随当前维护窗口](#v0220-window-follow)。

#### 升级前 {#v0220-before}

1. **如果你的应用用 npm 包渲染仪表板，请计划在任何人于升级后的实例上保存看板之前把它升级到
   `0.22.0`。** 现有看板用 `0.21` 仍能正确渲染；升级后保存或发布的看板则不能。见
   [如果你基于 npm 包开发](#v0220-packages)。
2. **如果你读取外部 MQTT 或 Sparkplug 源，且 pod 突然丢失时必须与以前丢得一样少**，请在升级前
   调低它们的发布窗口。见[更多在途发布](#v0220-publish-windows)。
3. **如果你的 `terraform.tfvars` 把 `nats_memory_limit` 设为低于 4Gi，请提高到 4Gi 或删除这一行。**
   升级会保留你的值，而在 2Gi 下服务器只能持续处理约每秒 3,000 个事件。见
   [broker 在 10 分钟内记住重新投递的事件](#v0220-dedup)。

#### 升级期间 {#v0220-during}

- **不运行任何迁移**，数据库不受影响，pod 照常滚动更新。
- **控制台和 `/dash` 一经滚动更新即可读取新的仪表板格式**，因为它们随版本发布。升级期间不会转换
  任何已存储的看板；每个看板在下次加载时转换。
- **device-management 的 pod 以更低的 CPU 请求重新调度**，每个 NATS 服务器的内存上限从 2Gi 提高到
  4Gi。这是渲染出的资源值中的两处变化。见[broker 在 10 分钟内记住重新投递的事件](#v0220-dedup)。
- **在 event-processing 也完成滚动更新之前，已停用设备的连接变化可能触发或解除连接告警。**
  device-management 现在会传递已停用设备的连接和断开，而仍为 `v0.21.x` 的 event-processing pod
  不知道要忽略它们。`dcctl upgrade` 会一起滚动更新各服务，因此这只持续于发布期间。

升级后回退到 `v0.21.x` 读取的是同一个数据库。仪表板是需要注意的例外：在 `v0.22.0` 下保存或发布的
看板是新格式，`v0.21.x` 会把其中每个组件画成一个单元格宽、一个单元格高，且不显示新组件。不要在旧
控制台中编辑这样的看板，因为在那里保存会写入那种布局。

#### 升级后 {#v0220-after}

- **把你关心的每个看板检查一遍。** 在编辑器中打开它：横幅会说明它已被转换，并列出被去掉样本数的
  图表。检查布局后保存；如果看板已发布，再发布一个新版本。
- **找出现在拒绝设备组的组件**，改用设备群表格，或每台设备一根柱的柱状图。
- **无需其他操作。** 新设置都有默认值。

##### broker 在 10 分钟内记住重新投递的事件 {#v0220-dedup}

`resolved-events` 和 `failed-events` 现在在 10 分钟内丢弃重新发布的事件，而 broker 的默认值为 2
分钟。以前，若事件的来源在两次发布失败后被重新投递，该事件可能被存储两次，并被检测计数两次。传入
事件流的窗口从 30 分钟改为 10 分钟。它仍覆盖消费者运行期间的所有重新投递，以及在约 8 分钟内恢复的
`event-sources` 或 `device-management` pod。如果 pod 停机更久，或者 broker 存储了一次发布、丢失了
其确认，随后超过约 8 分钟发布失败，在途事件会被存储两次。以 2 个或更多副本运行 `event-sources` 和
`device-management` 可避免 pod 的情况。每个 NATS 服务器的内存上限从 2Gi 提高到 4Gi（`GOMEMLIMIT`
从 1638MiB 提高到 3276MiB），因为每个服务器都保存这些窗口内的每个 id：在每秒 10,000 个事件持续
运行时约 2.6 GiB。768Mi 的请求不变，因此服务器的调度位置不变。`dcctl upgrade` 先应用 broker 并
重启其服务器，在 `--ha` 下逐个重启。随后每个服务在首次启动时就地调整现有流的窗口；不会重建任何
流，也不会丢失任何消息。**如果你的 `terraform.tfvars` 把 `nats_memory_limit` 设为低于 4Gi，升级会
保留你的值。** 在 2Gi 下，服务器只能持续处理约每秒 3,000 个事件，之后 id 表就会耗尽其余量。请在
升级前把它提高到 4Gi，或删除这一行。

#### 变更内容 {#v0220-what-changed}

##### 仪表板以新格式存储 {#v0220-dashboard-format}

看板定义分两步从 `schemaVersion` 1 变为 3，没有 `schemaVersion` 的看板按其结构判断。所有加载定义
的地方都会转换：编辑器、`/dash`、克隆、导入、版本比较和回滚。加载从不回写已存储的定义。

**网格（格式 2）。** 旧网格是流式列（默认 24 列）和固定高度的行（40 px）。新网格使用方形单元格，
两个方向都以单元格计量，且两个网格组件不能重叠。两者之间没有精确对应，因此转换是近似的：

- 列线按新的列数缩放并取整，原来在一条线上相接的组件仍然相接；
- 行线按 1366 px 的参考宽度转换；
- 原来的间距成为槽宽（gutter）；
- 丢弃 `z` 和像素 `offset`，因为网格没有层叠；
- 取整后重叠的、或原来通过 `z` 重叠的组件会向下推，直到放得下，每个组件仍位于原来在它上方的组件
  之下。

每个组件都会保留，连同其类型、绑定和选项，包括本构建不认识的类型的组件。

**图表窗口（格式 3）。** `timeseries-chart` 原来保存样本数 `options.window`（默认 300）。现在它
绘制一段时间跨度：`windowSeconds`，未指定时使用看板的时间范围。样本数没有可靠的对应跨度，因为它
取决于设备的上报频率，所以转换会丢弃 `window`，图表改为跟随看板的时间范围。除非看板另行指定，
该范围为一小时，这正是每个图表原本加载的那一小时，因此图表加载时显示的内容不变。原本要更短或更长
的图表需要手动设置跨度。其他组件保留其 `window`。

**作者会看到什么。** 在编辑器中，转换过的看板会显示横幅，提示检查布局并保存以保留新格式。若有
图表失去了样本数，横幅还会按标题列出它们。即使没有其他编辑也可以保存，保存会写入新格式并移除
横幅。已发布的版本不可修改，因此它保持旧格式，每次查看时转换，直到你发布新版本。从 `v0.21.x`
导出的仪表板文件也以同样方式导入和转换。

##### 如果你基于 npm 包开发 {#v0220-packages}

`@devicechain/dashboards`、`@devicechain/widgets`、`@devicechain/widget-sdk`、`@devicechain/client`
和 `@devicechain/brand` 以 `0.22.0` 发布。

**请在看板以新格式保存之前升级到 `0.22.0`。** `0.21` 的解析器忽略 `schemaVersion`，把新的放置
读成没有跨度的方框，因此 `v0.22.0` 保存的看板会把每个组件画成一个单元格宽、一个单元格高，且不
报错。升级前存储的看板在再次保存前，用 `0.21` 仍能正确渲染。

**从 `0.22.0` 起，由更新版本保存的看板会被拒绝，而不是被画错。** `parseDashboardDefinition` 会
抛出 `code: 'schemaTooNew'` 的 `DashboardDefinitionError`，并在 `found` 中给出发现的版本。请检查
`code` 而不是 `instanceof`；当应用打包了该包的两个副本时，`instanceof` 会失效。`/dash`、
board-replay 以及控制台的详情页、回滚、导入和克隆会显示提示升级的消息，而不是看板。这只保护
`0.22.0` 及以后的查看器免受未来版本保存的看板影响：`0.21` 的查看器仍会把 `v0.22.0` 的看板画错，
因此请先升级这些包。

**`@devicechain/dashboards`** 改变了布局模型：

- 移除 `WidgetBox`、`resolveWidgetBox`、`baseBox`、`setWidgetBox`、`bringToFront`、`gridBoxToPx`
  和 `pxToGridBox`。替代项为 `Placement`、`resolvePlacement`、`basePlacement`、
  `setWidgetPlacement`、`gridPlacementToPx` 和 `pxToGridPlacement`；由于不再有层叠，
  `bringToFront` 没有替代项；
- `WidgetLayout` 把断点映射到 `Placement`，`Breakpoints` 把名称映射到 `BreakpointSpec`，
  `CanvasGrid` 用 `minCellSize`、`gutter` 和 `padding` 取代 `gap` 和 `rowHeight`，`GridGeometry`
  用 `cell` 和 `gutter` 取代其行列尺寸；
- `SYNTHETIC_GENERATORS` 只列出生成器 id。请自行提供标签；已存储的 id 不变；
- `parseDashboardDefinition` 现在拒绝重叠的网格组件。旧定义会在该检查之前转换。

**`@devicechain/widgets`：**

- `severityColor(severity)` 改为 `severityColor(colors, severity)`；
- `timeseries-chart` 的选项 `window` 由 `windowSeconds` 取代，图表的选项版本从 1 变为 2。

**`@devicechain/widget-sdk`** 没有移除导出项，但新增了许多：尺寸令牌与看板密度、适应方框的文本、
数值时效与连接状态徽标、严重级别与颜色、阈值，以及看板时间范围（`useWidgetTimeRange`、
`MAX_SPAN_SAMPLES`、`withinSpan`），另有 `/echarts` 上的新图表构建函数。`useElementSize` 新增可选
参数 `step`，`WidgetFrame` 新增可选属性 `flush` 和 `sampleTime`。有三处 TypeScript 构建可能察觉的
变化：

- `ChartTheme` 有了必填字段 `severity`。只有在你手动构建主题、而不是调用 `useChartTheme` 或
  `resolveChartTheme` 时才有影响；
- 联合类型 `WidgetChannel`（新增 `aggregate`）、`WidgetId`、`BuiltinWidgetId`、`ThemeVar` 和
  `OptionSpec` 增加了成员，对它们做穷尽 `switch` 时需要新的分支；
- 未指定精度时，`formatValue` 现在保留 3 位小数，小于 1 时保留 4 位有效数字，而以前输出未取整的
  数值。

`SDK_API_VERSION` 不变，仓库中的第三方组件示例无需修改即可基于 `0.22.0` 构建。
[`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards)、[`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) 和
[`@devicechain/widget-sdk`](https://www.npmjs.com/package/@devicechain/widget-sdk) 的 README 中的 `Upgrading from 0.21` 一节列出了
各项变化及其替代项。
**`@devicechain/client`** 只有新增：`createSessionTokens`，以及作为区域的 `update-management`。
**`@devicechain/brand`** 不变。

##### 单设备组件拒绝设备组 {#v0220-group-refusal}

测量流按测量名称保存最新值。绑定到设备组时，每台上报 `temperature` 的设备都会覆盖其他设备，因此
卡片、仪表、图表或表格会把最后上报的设备当作答案显示。这些组件现在会拒绝设备组，并给出一句话，
指向设备群表格或柱状图的每设备一根柱模式。涉及最新值卡片、各类仪表、时间序列图表、表格、饼图、
统计卡片，以及按测量模式的柱状图。编辑器只为设备群表格、柱状图、告警组件和地图提供设备组选项。

##### dcctl 拒绝冲突的 `--kube-context` {#v0220-kube-context}

`dcctl upgrade` 带上指向实例自身已记录集群的 `--kube-context` 时，现在渲染出与不带该参数时完全相同
的实例。以前它会丢掉本地 blob 存储的共置设置，chart 因而拒绝渲染。

指向与实例记录**不同**上下文的 `--kube-context` 现在会被 `dcctl upgrade`、`dcctl destroy` 和
`dcctl sim` 拒绝。以前它会静默覆盖记录。去掉该参数即可使用已记录的集群。如果错的是记录本身（集群
已重建或其上下文已改名），请删除 `~/.devicechain/instances/<instance>/instance.json`，再带上
`--kube-context` 重新运行。

##### 已停用设备的连接状态继续更新 {#v0220-disabled-presence}

已停用设备的连接、断开和降级现在会到达 device-state 和事件历史，因此停用期间其连接状态仍然准确。
检测仍会忽略这些事件，因此不会为已停用设备触发告警，其测量、位置和命令仍被拒绝。Sparkplug 边缘
节点可以让已停用的子设备显示为已连接，因为节点为其设备报告的在线状态在设计上是受信任的。新计数器
`devicechain_eventsources_presence_reconcile_unapplied_total` 统计已发送但未见生效的对账修复。

##### 保留的命令跟随当前维护窗口 {#v0220-window-follow}

为维护窗口保留的命令现在跟随其设备类型的**当前**窗口。如果把某个类型从窗口 A 改为窗口 B，已在
等待的命令会移到 B 的下一次开放。如果**取消分配**某个类型的窗口，其等待中的命令会**立即释放**。
`v0.21.0` 说明中称它们会保持保留，现在已不再如此。已停用设备的命令在任何情况下都保持保留。取消
分配窗口前，请先检查有哪些命令在等待它。

另外修复：已停用设备的排队命令不再让 LwM2M 发送端陷入紧密循环轮询；确认因窗口刚关闭而被拒绝的
命令现在会停放到下一次开放，而不是停留在 `SENT`。因设备已停用而被拒绝的认领计入
`devicechain_commanddelivery_command_delivery_claims_refused_disabled_total`。

##### 变化的默认值 {#v0220-defaults}

###### 更多在途发布 {#v0220-publish-windows}

在复制的 broker 上，一次确认需要约 8 到 11 ms，逐条等待确认再发送下一条的路径受此限制。现在有
五条路径保持一个在途发布窗口，各有一个设置。每个设置在启动时读取；超出 1 到 1024 的值，或显式的
`0`，会使服务停止。

| 设置 | 服务 | 默认值 | 以前 |
| --- | --- | --- | --- |
| `resolution.publishWindow` | `device-management` | 512 | 128，固定 |
| `capture.publishWindow` | `event-sources` | 512 | 128，固定 |
| `externalMqtt.publishWindow` | `event-sources` | 每个源 128 | 每个源 5，约每秒 440 个事件 |
| `brokerPresence.publishWindow` | `event-sources` | 128 | 每个主题一个，约每秒 88 次状态变化 |
| `ingest.publishWindow` | `sparkplug-ingest` | 每个连接 32 | 一个，约每秒 88 条消息 |

前两个从会重新投递的流中读取，因此更深的窗口只消耗内存。后三个是至多一次投递：外部 broker 或在线
状态通知不会重放。正常停止或把源交给另一个 pod 时，pod 最多等待 8 秒处理在途内容。**突然终止的
pod 会在原有基础上最多再丢失一个窗口的内容**：每个外部 MQTT 源最多 128 个事件（以前为 5），每个
Sparkplug 连接 32 条消息（以前为 1），以及 128 次在线状态变化，后者由下一轮对账修复。如果这比速率
更重要，请调低窗口。这些设置的说明见[可观测性](observability.md)和[边缘服务](edge-services.md)。

###### 拉取批量为 256 {#v0220-fetch-batch}

未设置 `infrastructure.nats.fetch.batch` 时，服务现在每次拉取 256 条消息，而不是 128 条。你设置
的值仍然优先。在 GKE 测量中，受每次拉取往返时间限制的 device-management 在更大批量下每秒多解析
10% 到 12% 的事件。每条消息的确认窗口仍从投递时开始计时，因此逐条处理消息的读取者现在每条消息约有
234 ms（以前为 469 ms），超出后满批中的最后一条会被重新投递。重新投递的消息会被再次处理，不会
丢失。如果你运行这么慢的读取者，请设置 `batch: 128`。见[预取](observability.md#fetch-ahead)。

###### device-management 请求更少的 CPU {#v0220-dm-cpu}

`functionalAreas.device-management.measuredRequests.cpu` 从 800m 降为 450m，即本版本
device-management 在每秒 6,000 个事件时的实测用量。CPU 上限仍为 2 核。你在自己的 values 中设置的
值会保留。

##### device-state 每个事件的工作更少 {#v0220-device-state}

一批普通数据事件现在用一条 upsert 写入其所有设备行，而不是每批先加锁读取再回写。device-state
读取者与写入者之间的队列按写入者池大小确定（默认 640，以前为 100），使服务落后时写入者保持忙碌。
写入者还按统一顺序获取行锁，消除了一个罕见的死锁：一个写入者创建设备行，另一个写入者同时为相同
设备认领在线状态。在每秒 6,000 个事件时，device-state 每个事件的 CPU 比 `v0.21.0` 少 21%，其中
一部分来自 `v0.21.1` 已发布的数据库读取修复。

##### GKE 实测 {#v0220-performance}

在与上一轮测量相同形态的 GKE 集群上，包含本版本 device-state、device-management 和 event-sources
变更的构建在服务放置固定的条件下，所有阶段持续 10 分钟维持每秒 10,000 个事件，600 万个事件经逐事件
核对均恰好存储一次（见[实测吞吐量](./bootstrap.md#measured-throughput)）。
这与之前的持续速率相同，而不是更高：由于摄取变快，device-management 的读取端成为最先落后的阶段，
单副本约为每秒 10,300 个事件。256 的拉取批量正是针对它的变更，在一次 3 分钟的运行中提升了 12%。
在事件路径的五个服务上，每秒 10,000 个事件时的 CPU 比那一轮低 6%；那一轮运行于 `v0.21.1` 的
数据库读取修复之前。

##### 新告警与新指标 {#v0220-observability}

启用 `metrics.alerts` 时的新告警：

- `ExternalMqttSourceLosingEvents`：某个外部 MQTT 源的事件 10 分钟内发布失败，因而丢失；
- `BrokerPresenceLosingTransitions`：在线状态变化 10 分钟内未能写入事件流；
- `DeviceCredentialCacheTooSmall` 和 `DeviceLookupCacheTooSmall`：15 分钟内 device-management
  超过 5% 的查找从已满的缓存中逐出了另一条目；
- `DetectRuleStatWriteFailing`：无法写入规则健康状况的触发计数。检测本身不受影响。

新指标：用于上述发布窗口的 `devicechain_<area>_jetstream_publish_window_full_seconds_total`、
`devicechain_<area>_jetstream_publish_window_full_total` 和
`devicechain_<area>_jetstream_publish_duplicates_total`，用于外部 MQTT 的
`devicechain_eventsources_total_msg_publish_failed{source}`，以及
`devicechain_eventprocessing_detect_rule_stat_write_failures_total`，以及 `devicechain_eventsources_presence_reconcile_unapplied_total`。没有指标被重命名或移除。

##### 端到端审查中的修复 {#v0220-review-fixes}

- **实时组件在任意时长的中断后都能恢复。** 以前在断网或服务器不可用约 40 秒后，组件会显示“数据
  不可用”，直到重新加载页面；服务器结束的数据流可能停止更新，而徽标仍显示实时。代码下载失败的
  组件现在可以用“重置组件”重试。
- **仪表板数据如实显示。** 看板暂停时把槽位改指向另一台设备，会显示新设备。每设备一根柱的柱状图
  持续更新。未绑定设备的统计卡片会提示选择设备，而不是显示“0 次读数”。上报稀少的设备在设备群
  表格中的单元格不会把某个时间段的平均值当作最新读数，超出读取上限的单元格显示“未加载”。

##### 已知限制：重新连接后的历史数据 {#v0220-known-limitation}

看板重新连接后，不会重新读取错过的历史数据。折线图会在中断期间画一条直线，设备群和聚合数值会遗漏
中断期间发送的读数。已在下一版本中修复：看板重新连接时会从已存储的历史数据中读回中断期间的数据
（参见[重新连接之后](../concepts/dashboards.md#after-a-reconnect)）。

##### 本版本还包括 {#v0220-also}

- **仪表板。** 单元格保持方形，列数随容器变化，因此手机每行显示一个组件，墙面显示屏得到更大的
  单元格。编辑器按单元格吸附、把相邻组件推开、支持键盘操作、可把组件锚定到另一个组件，并在每个
  组件上显示布局问题；布局错误会阻止发布，直到“适应限制”将其清除。编辑器还支持撤销和重做、复制组件，
  以及在看板之间复制粘贴；粘贴到设备绑定不存在的地方（例如另一个租户的看板）的组件会得到一个新的
  未绑定槽位，不保留任何设备引用。看板有时间范围（15 分钟到 7
  天）和暂停；暂停期间命令仍会发送。新组件：线性仪表和液位仪表、电池、状态灯、信号格、罗盘、进度
  环、命令滑块和命令开关、统计卡片、柱状图和饼图，以及设备群表格。组件新增阈值、单位和精度，显示
  数值的时效，并说明为何为空。`/dash` 会保持会话有效，并提供用于墙面显示屏的 kiosk 模式。
- **控制台中的空中升级：** Artifacts 和 Signers 页面，以及设备页面上的 Updates 标签页。GraphQL
  新增 `Artifact.signatures`。
- **套餐限制。** 套餐表单可设置保留命令上限、削减优先级和三项地理围栏限制，
  `AdminTenant.effectiveScalarSettings` 报告每个值的来源。
- **控制台用户指南**共 17 章，提供英文、西班牙文和中文版本。
- **告警表格在连字符处换行显示告警键和来源**（`engine-` / `overheat`），而不是在单词中间断开。
- **board-replay 不再包含 Vite 的模块预加载 polyfill。** 在 Safari 17 之前和 Firefox 115 之前的
  版本中，首次使用时才加载的组件的共享代码块会晚一个往返到达。其他均无变化。

### v0.21.1：补丁，无需操作 {#v0211-upgrade}

`v0.21.1` 可从 `v0.21.0` 直接升级：先为集群执行 `dcctl install`，再为每个实例执行 `dcctl
upgrade`，与 [v0.21.0](#v0210-upgrade) 相同。无新增迁移，不接触数据库；API、主题、权限、
chart 值、配置键均无变化，下文 v0.21.0 的描述仍完全适用。从 `v0.20.0` 或更早版本升级时，
请先按 [v0.21.0 说明](#v0210-upgrade)操作，其内容原样适用。

四项修复：

- **数据库读取的开销恢复到 v0.21.0 之前的水平。** v0.21.0 是首个使用 Go 1.27 工具链构建的
  版本，在此工具链下，PostgreSQL 驱动对读取的每一行的每一列都做额外的类型检查。读取量大的
  服务（尤其是 device-state）因此在摄取上明显多耗 CPU。现在行的读取方式恢复为 `v0.20.0`
  的方式，device-state 的摄取吞吐量随之恢复。在一个以读取为主的基准测试中，同一读取从
  85–95 ms 降到 31–35 ms。
- **Rule Health 视图在 PostgreSQL 上统计触发次数。** 此前它对所有规则都显示零次触发、无最近
  触发时间，因为 PostgreSQL 拒绝了记录触发的语句。检测本身不受影响：规则始终正常产生和清除
  告警。计数从升级时开始，之前的触发从未被记录。
- **以 `enabled: false` 创建身份会得到一个停用的身份。** 此前它被存为启用。同一修复也覆盖
  其他带启用标志的记录。在 `v0.21.0` 或更早版本中以停用状态创建的身份仍是启用的：请检查
  你本想停用的身份并将其停用。
- **停止 HTTP 事件源时会等待其连接关闭完成**，已停止的事件源不再在停止后更新其指标。

### v0.21.0：设备可被整体停用，更新带签名 {#v0210-upgrade}

`v0.21.0` 可从 `v0.20.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行 `dcctl
upgrade`，如[v0.17.0](#v0170-upgrade)所述。从 `v0.19.0` 或更早版本升级时，还请阅读
[`v0.20.0` 说明](#v0200-upgrade)：其中的行数和 chunk 数检查及各项步骤同样适用于本次升级。
由 `v0.16.0` 或更早版本构建的实例仍必须[销毁重建](#pre-declaration-recreate)。

本发布大部分是新功能，在你使用之前不会改变任何行为：设备可以被整体停用，更新工件可以签名，
设备群可以通过 CSV 文件导入，检测规则表单新增了告警预设和通知检查，单点登录连接可以信任私有证书颁发机构。
对某些人有影响的变化：只能理解未签名更新分配的设备会拒绝带签名的分配，工件只有在被活跃签名者签名后才能分配，
原始的 LwM2M 恢复出厂设置会被拒绝，被停用的设备无法获取配置、更新和 LwM2M 注册，其已排队的命令被保留，
控制台的构建文件移动到了新路径。所有二进制文件现在均使用 Go 1.27.2 构建。

#### 升级前 {#v0210-before}

1. **接收空中升级的设备：先部署能验证签名的更新程序。** 分配现在可以携带 `trust: "SIGNED"`，
   以及签名、密钥 id 和算法，只理解 `UNVERIFIED` 的设备会拒绝它。在租户注册签名者并为工件签名之前，
   不会有任何分配被签名，因此请在此之前部署能验证签名的更新程序。
   参见[更新分配可以带签名](#v0210-signed-updates)。
2. **决定如何信任更新工件。** 当 `updates.allowUnverifiedArtifacts` 保持为 `false` 时，
   只有由其租户的活跃签名者签名的工件才能被分配。如果你将该标志设为允许分配未签名的工件，它仍然生效。
   参见[更新分配可以带签名](#v0210-signed-updates)。
3. **停止发送原始的 LwM2M 恢复出厂设置。** 执行 `/3/0/5` 的命令在入队时就会被拒绝，
   来自规则或批量操作的也一样。参见[原始的 LwM2M 恢复出厂设置会被拒绝](#v0210-factory-reset)。
4. **检查有哪些内容依赖控制台的静态路径。** 其构建文件现在从 `/_app/` 而不是 `/assets/` 提供。
   参见[控制台的构建文件移动](#v0210-console-app-path)。

#### 升级期间 {#v0210-during}

- **同时升级 `device-management` 和 `command-delivery`。** `command-delivery` 保存自己的一份被停用设备记录，
  并从 `device-management` 填充。租户的命令在该租户首次同步成功之前不会被分发，
  因此无法访问带有新查询的 `device-management` Pod 的新 `command-delivery` Pod 会保留这些命令。
  命令不会丢失，同步成功后即会发出。
- **四个服务在首次启动时会应用迁移**：`device-management` 为设备表添加可为空的 `disabled_at` 列和一个部分索引，
  并添加维护窗口表、设备类型上的窗口列和命令定义上的影响列；`command-delivery` 创建两张小表，并为命令和批次
  添加结果、窗口和重试相关的列；`update-management` 创建签名者注册表；`user-management` 为身份提供商添加
  CA 证书包列。每个迁移都可单独重复执行，也都不需要销毁重建。参见[新表和新列](#v0210-migrations)。
- **`command-delivery` 的迁移会重写现有行。** 它用一条针对命令表的 `UPDATE` 语句，为每条已经结束的命令填写新的
  `outcome` 列：成功的命令记为成功，已过期或已取消的记为未送达，失败或超时的记为未知，因为旧行没有记录
  具体是哪一种。命令表很大时，`command-delivery` 的首次启动会更久，耗时与已结束命令的数量成正比；PostgreSQL
  还会为每一行写入新版本，因此预写日志和需要 autovacuum 清理的死行也会达到相同规模。该语句只写入 `outcome`
  仍为空的行，所以再次执行绝不会覆盖已有的值。Pod 的启动探针默认约允许 150 秒（chart 值中的
  `startupProbe`）；如果表大到回填需要更久，请在升级期间调高 `startupProbe.failureThreshold`，否则容器会在
  迁移完成前被重启，并重新开始迁移。本版本的其他迁移只添加表、列和索引，不写入任何行。
- **`update-management` 仅从新的事实开始读取设备名册**，因此无需追赶，也无需运维人员执行任何步骤。

升级后回退到 `v0.20.0`，会保留新列和新表，`v0.20.0` 会忽略它们。它不认识签名者，
因此只有 `allowUnverifiedArtifacts` 决定它能分配什么。

#### 升级后 {#v0210-after}

- **重新加载一次控制台。** 持有旧 `index.html` 的浏览器会请求旧的文件路径，直到重新加载。
  `index.html` 以 `no-cache` 提供，因此只需一次导航。
- **如果自定义角色要管理签名者，请授予新权限。** 租户管理员拥有 `artifact:trust`；
  你自己定义的角色没有。参见[更新分配可以带签名](#v0210-signed-updates)。
- **无需其他操作。** 停用设备、签名、导入、预设和 CA 证书包一开始都处于未使用状态。

#### 变更内容 {#v0210-what-changed}

##### 被停用的设备被整体切断，其已排队的命令被保留 {#v0210-disabled-device}

现在可以将设备整体停用，而无需逐个查找并吊销每个凭据，方法是使用 `updateDevice(enabled: false)`
或设备页面上的“停用”操作。设备被停用期间：

- 其事件在解析时被拒绝，其代理连接被拒绝，针对它的新命令被拒绝并返回 `DEVICE_DISABLED`；
- 其配置同步和状态报告被拒绝，更新进度和清单报告、下载授权及下载同样被拒绝，
  停用之前签发的下载链接会返回 403；`assignArtifact` 以 `DEVICE_DISABLED` 拒绝它；
- LwM2M 注册返回 4.03 Forbidden，已注册的设备会在下一次 Update 时被终止，从而重新注册并被拒绝；
- 其缺失规则被解除，因此不会为它触发或保留“设备静默”告警；
- 已排队的命令会被**保留**：既不发送也不取消。被保留的命令仍会按自身的 TTL 过期。
  启用设备会按从旧到新的顺序释放它们，删除被停用的设备则会取消它们。
  `deviceCommandHold` 和 `cancelDeviceCommands` 可显示并清除这些命令。

启用设备时会发送其最新配置一次，并重新提供其当前的更新分配。已经打开的会话会保持打开，
直到下次检查连接。参见[停用设备](../guides/device-credentials.md#disabling-a-device)。

如果你现在会停用设备，并依赖其已排队的命令发出，这些命令现在会等待。

##### 更新分配可以带签名 {#v0210-signed-updates}

租户可以注册其信任的、用于为更新工件签名的公钥：ECDSA P-256 或 Ed25519，格式为单个 `PUBLIC KEY` PEM。
对工件 SHA-256 摘要的签名在添加时进行验证，只要同一租户至少有一个活跃签名者为某个工件签过名，
该工件即视为**已签名**。吊销签名者（此操作是终态的）会使其工件回到未验证状态，而不会改动这些工件。
任何包含私钥的内容都会被拒绝。

这带来的变化：

- **新权限 `artifact:trust`** 用于注册和吊销签名者。租户管理员拥有它，而你定义的任何角色都没有；
  `artifact:write` 用于添加签名。
- **分配需要已签名的工件**，除非 `updates.allowUnverifiedArtifacts` 为 `true`。该设置没有变化，
  默认仍为 `false`；变化的是，已签名的工件不再需要它。
- **设备契约有所扩展。** 针对已签名工件的 `update.assignment` 携带 `trust: "SIGNED"`、
  `signature`（标准 base64）、`keyId`（密钥 DER 编码公钥的 SHA-256 小写十六进制）和 `algorithm`
  （`ECDSA_P256_SHA256` 或 `ED25519`）；两种算法签名的都是原始的 32 字节摘要。`"v":1` 保持不变，
  未签名分配与之前逐字节相同。只理解 `UNVERIFIED` 的设备会拒绝 `SIGNED` 分配，即失败时关闭。
  设备在已下载阶段的 `FAILED` 下报告 `UNKNOWN_SIGNER` 或 `SIGNATURE_INVALID`。
- **签名不绑定版本。** 对摘要签名说明的是谁生成了这些字节，而不是它们是哪个版本；
  参见[空中升级](../concepts/over-the-air-updates.md)。

分配工件时，活跃签名者最早的一份签名会被**固定**到该分配上（`UpdateAssignment.signedBy` 指明签名者），
设备收到的正是这份签名。由此有两点：

- **设备必须持有每一个可能被固定的签名者的密钥。** 在吊销旧签名者之前，先把新签名者的密钥放到设备上。
  固定之后再吊销签名者，不会改变已经发出的消息。
- **关闭 `allowUnverifiedArtifacts` 会停止发送未签名的分配**，设备同步时的重发也一样。在 `v0.20.0`
  中做出的分配没有签名，因此设置关闭后，其设备保持现状，直到你分配一个已签名的工件。固定的签名无法读取的分配不会发送，
  也绝不会以 `UNVERIFIED` 发送。

注册签名者并为工件签名，请参阅[为更新签名](../guides/signing-updates.md)。

##### 原始的 LwM2M 恢复出厂设置会被拒绝 {#v0210-factory-reset}

对 Device 对象资源 5（`/3/0/5`）的 `lwm2m.execute` 会在入队时以 `COMMAND_DESTRUCTIVE_REFUSED` 被拒绝，
适用于单条命令、批量操作以及检测规则发送的命令，路径的任何写法都一样，并且在到达设备之前会再次拒绝。
检测规则将该错误码视为永久性错误，不会重试。重启（`/3/0/4`）、其他执行操作和读取不受影响。
针对恢复出厂设置的、需确认的单设备命名操作已在计划中；在此之前，原始路径保持关闭。
执行器还会拒绝任何路径键出现多于一次的读取、写入或执行载荷。

##### 批次可以按结果重试，LwM2M 请求无应答现在记为 TIMEOUT {#v0210-batch-retry}

现在每条已结束的命令都带有一个存储的 `outcome`，由结束它的那次写入记录：`SUCCEEDED`、
`NOT_DELIVERED`（平台可以证明它从未到达设备）、`REJECTED`（设备表示没有执行）或 `UNKNOWN`
（它可能已到达设备并已执行）。迁移会添加该列，并为已经结束的命令填值：`SUCCESSFUL` 变为 `SUCCEEDED`，
`EXPIRED` 和 `CANCELLED` 变为 `NOT_DELIVERED`，其他所有已结束的命令（包括失败或超时的）变为 `UNKNOWN`。
`commands` 可以按 `outcomes` 搜索。

`retryCommandBatch` 会为你指定的、未成功的设备创建一个新批次，并与原批次双向链接（`retryOf`、`retries`）。
命令从原批次复制，不会重新输入。结果为 `UNKNOWN` 的设备会被 `RETRY_UNKNOWN_NOT_ACKNOWLEDGED` 拒绝，
除非请求设置了 `includeUnknown`，因此升级前失败或超时的命令需要该标志才能重试。在同一重试家族的任何批次中已有
进行中或成功命令的设备会被拒绝，这样就不会因为同时存在两个重试而向设备发送两次命令。需要 `command:write`。
控制台的批次页面有一个**重试…**操作。参见
[向设备群下发命令](../guides/commanding-a-fleet.md#retry-the-devices-that-did-not-succeed)。

**行为变化。** 设备对 LwM2M 请求没有应答时，现在记为 `TIMEOUT`，与其他传输一致。如果你依据 LwM2M 命令的状态
决定下一步操作，请预期这里会出现 `TIMEOUT`。

##### 破坏性命令可以等待维护窗口 {#v0210-maintenance-windows}

可以为设备类型指定一个每周循环的**维护窗口**：一个时区加 1 到 56 个每周时段，在 `device-management` API 中管理
（`device:write`）。每个命令定义新增 `impact`，取值 `NORMAL`、`DISRUPTIVE` 或 `DESTRUCTIVE`；已存在的定义按
`NORMAL` 读取，`lwm2m.reboot` 今后创建为 `DISRUPTIVE`。在为设备类型指定窗口、且该类型的命令为 `DISRUPTIVE` 或
`DESTRUCTIVE` 之前，什么都不会被拦住。

之后，对该类型的设备，在窗口关闭时入队的此类命令会被接受并**等待**，仍为 `QUEUED`，直到下一次开启。在窗口开启时入队的命令
绑定到该次开启：如果窗口关闭时仍未发出，它会等待下一次开启，而不是在设备回来时随时发出。这适用于单条命令、批次
和检测规则发出的命令，也适用于每一条发送命令或确认在途命令的路径。`expiresAt` 早于下一次开启的命令会被 `WINDOW_AFTER_EXPIRY`
拒绝，在批次中作为逐设备的拒绝。批次会报告其中有多少命令处于 `waiting`。

有两点需要了解。设备在窗口关闭后才确认在途命令时，确认会被拒绝；命令随后挂起并移到下一次开启。另外，从设备类型上取消指定窗口，
不会释放已经按它标记的命令。

设置方法见[维护窗口](../guides/maintenance-windows.md)。

##### 控制台的构建文件移动到 `/_app/` {#v0210-console-app-path}

指向资产页面的链接（`/assets/<token>`）在加载或刷新时会返回 404，因为控制台构建的 JavaScript 和 CSS 位于
`/assets/` 之下，而 Web 服务器对该路径下它没有的任何内容都返回 404。构建文件现在位于 `/_app/` 之下，
仍使用相同的一年不可变缓存，资产页面也可以从直接链接加载。`/dash` 下的仪表板应用不变。
如果你对控制台的路径做了代理、缓存或白名单，请放行 `/_app/`。

##### 缺失的记录以 `NOT_FOUND` 应答 {#v0210-not-found}

当请求指定的角色、租户层级、身份、成员关系、租户、OAuth 客户端、身份提供商或 SSO 链接请求不存在，
或者 `claimDevice` 找不到设备时，现在会以 `extensions.code` 为 `NOT_FOUND` 应答，
正如 [GraphQL 参考](../reference/graphql-api.md)所述。以前它只携带 `role not found` 之类的消息，没有错误码。
消息保持不变，因此按消息匹配的客户端仍然可用；把缺失错误码当作“其他错误”处理的客户端应改为处理该错误码。

##### Go 1.27.2 接受带 JSON 转义的事件时间 {#v0210-go127}

所有二进制文件均使用 Go 1.27.2 构建。如果设备写入的事件时间在字符串中使用了 JSON 转义，
例如用 `"2026-10-13T12:00:00Z"` 表示 `...Z`，Go 1.26 解码时会把该事件送入死信。
这是正确的 JSON，现在会被接受并存储。无需任何操作。

##### 新表和新列 {#v0210-migrations}

| 服务 | 变更 |
| --- | --- |
| `device-management` | `devices.disabled_at timestamptz`（为空表示已启用）和部分索引 `idx_devices_disabled`；`maintenance_windows` 表、`device_types.maintenance_window_id` 和 `command_definitions.impact` |
| `command-delivery` | `device_gates` 和 `device_gate_syncs`，及其唯一索引和部分索引；`commands.outcome`（为已结束的命令填值）、`command_batches.retry_of` 及其上的部分索引；`commands.dispatch_after`、`dispatch_until` 和 `window_token` 及一个部分索引，以及 `command_batches.waiting` |
| `update-management` | `artifact_signers` 和 `artifact_signatures`（仅含公钥和签名）；工件的信任现在由它们推导，旧的已存储信任列予以保留、不再写入，因此滚动升级是安全的；每个分配还新增一个可为空的 `signature_id`，即它所固定的签名 |
| `user-management` | 身份提供商上的 `ca_bundle` |

##### 单点登录可以信任私有证书颁发机构 {#v0210-sso-ca}

身份提供商连接可以携带自己的 CA 证书包，在创建时设置，或使用 `setIdpCaBundle` 设置（空字符串表示清除；
两者都需要实例管理 API 上的 `user:write`）。对该提供商的调用会信任系统根证书加上该证书包，
仅限该连接，并且仍然经过出站防护。保存之前，使用新证书包的发现必须成功。
参见[配置单点登录](../guides/configuring-sso.md)。没有证书包的连接不受任何影响。

##### 其他值得了解的行为 {#v0210-other}

- **设备群导入。** `importDevices` 最多接受 1000 行，并将每行应答为 `CREATED`、`EXISTS`、`CONFLICT`、
  `INVALID` 或 `REFUSED`；它只创建，因此重复运行同一个文件是安全的，试运行不会写入任何内容。
  它需要 `device:write`。
- **设备配置。** `restoreDeviceConfiguration` 和 `copyDeviceConfiguration` 通过设备声明的共享属性，
  重新应用早先的修订版本或另一台设备的配置，并生成新的修订版本。它们需要 `device:write`，
  并新增错误码 `CONFIGURATION_KEY_UNDECLARED` 和 `CONFIGURATION_CONFLICT`。
- **Thing Model 导出。** 已发布的配置文件版本可通过 `DeviceProfileVersion.thingModel` 作为
  W3C Web of Things Thing Model 获取。
- **LwM2M。** `lwm2m.reboot` 和 `lwm2m.readDeviceInfo` 是命名操作，设备在注册时（最多每小时一次）
  会读取其制造商、型号、序列号、固件版本和电池电量，并保存为客户端属性。
- **检测规则。** 控制台表单提供静默设备、值持续高于上限和低电量的预设，并显示规则的严重程度是否会通知到任何人。
  缺失告警会在其设备被停用、删除或更换类型时清除。
- **消息传递。** 两个副本同时迁移某个流的保留策略时，不再导致其中一个拒绝启动。
- **批次重试。** `retryCommandBatch`、每条已结束命令上的 `outcome` 以及按 `outcomes` 搜索，见[批次可以按结果重试](#v0210-batch-retry)。
- **维护窗口。** 窗口、`impact` 和拦截，见[破坏性命令可以等待维护窗口](#v0210-maintenance-windows)。

### v0.20.0：通过你自己的身份提供商登录，组件以包的形式发布 {#v0200-upgrade}

`v0.20.0` 可从 `v0.19.0` 原地升级：先为集群执行 `dcctl install`，再为每个实例执行 `dcctl
upgrade`，如[v0.17.0](#v0170-upgrade)所述。从 `v0.18.0` 升级时，还请阅读
[`v0.19.0` 说明](#v0190-upgrade)：其中的行数和 chunk 数检查及各项步骤同样适用于本次升级。
由 `v0.16.0` 或更早版本构建的实例仍必须[销毁重建](#pre-declaration-recreate)。

本发布大部分是新功能，在你使用之前不会改变任何行为：用户可以通过你自己的 OpenID Connect 身份提供商登录，
仪表板可以显示来自你自己的包的组件，设备可以接收配置，空中升级（OTA）也推出了第一部分。对所有人都有影响的变化：
每个用户需重新登录一次，速率上限按其计量对象重新命名，X.509 设备凭据被拒绝，设备密码只以单向摘要保存，
升级前发布的仪表板需要重新发布，许多 API 输入新增了上限。处理每条事件的服务使用调优后的垃圾回收器运行，
`device-management` 将其查找结果保留数分钟而不是数秒。

#### 升级前 {#v0200-before}

1. **重命名速率上限键**，凡是你设置过的服务 `config` 都要改：
   `ingestRateLimit.messagesPerSecond` 改为 `ingestRateLimit.readingsPerSecond`（`event-sources`、
   `lwm2m-ingest`、`sparkplug-ingest`），`outboundMessagesPerSecond` 改为
   `outboundCallsPerSecond`（`event-processing`、`outbound-connectors`）。仍发现旧键的服务会拒绝启动。
   通过 API 读取或设置租户上限的脚本也需要使用新字段名。参见[速率上限按其计量对象重新命名](#v0200-rate-key-rename)。
2. **让设备停用 X.509 凭据。** 类型为 `X509_CERTIFICATE` 的凭据在升级后不再能认证。请先为每台此类设备
   提供 `ACCESS_TOKEN` 或 `MQTT_BASIC` 凭据。参见[不再接受 X.509 设备凭据](#v0200-x509)。
3. **使用 S3 对象存储时，为凭据授予存储桶上的 `s3:ListBucket` 权限**，否则已删除租户的清除无法完成。参见
   [删除租户现在会清除其对象存储前缀下的全部内容](#v0200-object-prefix-erase)。
4. **事件存储较大时，考虑先构建两个索引。** `event-management` 首次启动会在尚未压缩的基础事件上构建两个索引，
   当待索引的行超过 4,000,000，或某张表的 chunk 超过 500 个时，它会拒绝执行且不做任何更改。两节都给出了
   在升级前或升级后手动构建索引的语句：[设备事件列表只读取本租户的行](#tenant-device-index)和
   [两台设备现在可以在同一时刻发送相同的 `altId`](#device-alt-id-key)。
5. **如果你在 chart values 的顶层设置了 `gogc` 或 `goMemLimitPercent`**，处理每条事件的五个服务现在
   设置了各自的值，并且以它们为准。参见[事件路径服务使用调优后的垃圾回收器运行](#v0200-gogc)。
6. **大型设备群需检查 `device-management` 的内存。** 其查找结果现在保留 5 分钟，每个副本都需要容纳整个活跃设备群。
   参见[`device-management` 将查找结果保留数分钟](#v0200-resolve-caches)。
7. **停止其他任何读取 `device-events-capture` 流的程序**，例如 `nats stream view`。
   它现在只接受 `event-sources` 拥有的消费者。参见
   [设备接入流在消息转发后即删除](#v0200-capture-stream)。
8. **重新创建邮箱地址域名含非 ASCII 字符（`user@bücher.example`）或不是有效 `local@domain` 的身份。**
   升级后，在管理员重新创建它之前，它无法登录。请删除它，用 `xn--` 形式的域名（`user@xn--bcher-kva.example`）
   重新创建，并恢复其成员关系。参见[邮箱的域名现在只有一种写法](#v0200-email-idna)。
9. **对照以下各节检查你的 API 客户端以及你所依赖构建的包：**
   [现在会被拒绝的请求](#v0200-input-bounds)、
   [仪表板：发布即让版本上线](#v0200-dashboard-publishing)和
   [发布的包](#v0200-packages)。

#### 升级期间 {#v0200-during}

- **设备密码会被转换。** 新版 `device-management` 首次启动时，会把每个已存储的 `MQTT_BASIC` 密码替换为单向摘要。
  在每个 `device-management` Pod 都运行新版本之前，旧 Pod 找不到已转换凭据的密码，因此会拒绝该设备的 MQTT 连接
  以及其事件的密码校验；设备会重试并连到新 Pod。滚动升级期间在任一版本上设置的密码，就是最终保留的那个。
- **同时升级 `event-sources` 和 `device-management`。** 两者运行不同版本时，正文中携带设备密码或访问令牌的事件
  可能被拒绝。参见[正文中携带设备密码或访问令牌的事件](#v0200-body-password)。
- **在滚动升级完成前，避免修改租户或层级。** 在被替换之前，旧的接入或对外 Pod 按平台默认值计量所有租户，
  旧的 `user-management` Pod 处理的租户读写会失败。参见[速率上限按其计量对象重新命名](#v0200-rate-key-rename)。
- **`event-management` 首次启动时会构建两个索引**；每个索引构建期间，基础事件的写入会等待，读取不会。
- **`event-sources` 先转发设备接入流中保存的消息，再重新创建该流。** 如果流在五分钟内没有变空，
  `event-sources` 会以错误停止，且不删除任何内容。
- **检测改为通过新的消费者读取已解析事件。** 参见
  [检测通过新的消费者读取其事件](#v0200-ack-floor)。

升级后回退到 `v0.19.0`，会使每台使用 `MQTT_BASIC` 密码的设备无法认证，因为 `v0.19.0` 无法读取摘要；
如果回退，请设置新密码并装入这些设备。上述索引和流的变化各自说明了回退需要做什么。

#### 升级后 {#v0200-after}

- **所有人需重新登录一次**，MCP 代理等 OAuth 客户端需重新授权。参见
  [退出登录现在会在服务器端结束会话](#v0200-server-side-logout)。
- **重新发布每个应让查看者看到的仪表板。** 在此之前，查看者会被告知该仪表板尚未发布。
  参见[仪表板：发布即让版本上线](#v0200-dashboard-publishing)。
- **可能会触发新告警。** 当某个服务在缺少一个可选安全闸门的情况下运行时，会触发 `SafetyGateDisabled`。
  其他告警列在[新告警](#v0200-alerts)中。
- **本发布之前 bootstrap 的实例没有对象存储，升级也不会添加。** 租户徽标和更新制品都需要对象存储。参见
  [`dcctl bootstrap` 可以为实例提供对象存储](#v0200-object-store)。

#### 变化内容 {#v0200-what-changed}

##### 退出登录现在会在服务器端结束会话，所有人需重新登录一次 {#v0200-server-side-logout}

以前在控制台或嵌入式仪表板应用中退出登录只会清除浏览器中的状态：刷新令牌仍然有效，最长可达 7 天，控制台保留的登录令牌在最多 15 分钟内仍可发起新会话。新的 `logout` 变更会在服务器端结束该次登录。从那一刻起，该次登录的令牌交换都会被拒绝：它的刷新令牌在任何租户中都无法再刷新，它的登录令牌无法再选择租户，在该次登录期间授权的 OAuth 授权也无法再刷新。MCP 授权页面会单独登录，因此在控制台中退出登录不会终止在那里完成的授权；该授权在那次登录到期时结束（默认 7 天）。同一账户的其他登录不受影响。已签发的访问令牌和登录令牌（包括在管理 API 上）无法撤回，在过期前仍然有效，最长 15 分钟。如果退出登录时无法连接服务器，应用仍会在本地退出，并提示服务器端会话未能结束。

每次登录现在都是一条记录，存续时间与刷新令牌相同（默认 7 天），因此持续刷新的会话会在登录 7 天后结束，用户需重新登录。这也包括 OAuth 授权（例如 MCP 代理的授权）：它在授权页面上登录 7 天后结束，客户端需重新授权。

升级前签发的令牌没有登录记录，会被拒绝，因此**所有人需重新登录一次**：控制台、仪表板应用、SDK 和 OAuth 会话在下次刷新时结束，最迟在升级后 15 分钟内（除非你修改了访问令牌有效期）。MCP 代理等 OAuth 客户端需重新授权。无需其他操作。

##### 版本混合期间，正文中携带设备密码或访问令牌的事件会受到影响 {#v0200-body-password}

从本版本起，`event-sources` 与 `device-management` 以另一种形式传递出现在事件正文中的设备密码或访问令牌，
且双方都需要新版本才能读取对方的内容。在滚动升级期间，只要两者的新旧版本并存：

- 正文中携带设备密码（`MQTT_BASIC`）的事件（通过 HTTP 或 MQTT）会被当作未认证而拒绝，并且不会重放。
- 正文中携带访问令牌（`ACCESS_TOKEN`）的事件在 `deviceAuthMode: required` 下会被拒绝；在
  `deviceAuthMode: optional` 下，这些事件按其所声明的设备令牌被接受，但不会校验其凭据。
- MQTT 连接不受影响。

请同时升级 `event-sources` 与 `device-management`，或接受这些事件的短暂丢失。

##### `MQTT_BASIC` 设备密码只显示一次，之后无法再次显示 {#v0200-device-password-show-once}

平台现在只保存 `MQTT_BASIC` 设备密码带密钥的单向摘要，而不是密码本身。API 读取时从不返回密码，因此任何查询结果都不变。变化在于，平台这边也无法再找回它：控制台在你添加凭据或设置新密码后只显示一次密码，并带复制按钮，此后只表明已设置密码。凭据所在行新增**设置新密码（Set new password）**操作，表单提供**生成强密码（Generate strong password）**。

现有密码会在 device-management 在新版本上首次启动时转换为摘要，设备继续使用已有密码认证。未记录的密码之后无法显示；请设置新密码并装入设备。参见[密码只显示一次与轮换](../guides/device-credentials.md#show-once)。

无需任何操作。

##### 设备事件列表只读取本租户的行 {#tenant-device-index}

设备事件列表索引现在以租户开头。
v0.19.0 中列表总数和按事件类型过滤的设备列表，会访问该设备 token 的所有尚未压缩行，
包括其他租户相同 token 设备的行，因此一个租户繁忙的 gateway-1 会拖慢另一个租户的 gateway-1 列表。
现在只访问设备自身行，SQL 或 BI 设备事件查询也受益。
每个基础事件仍更新相同数量索引，新索引替换旧索引，因为包含租户而稍宽。

- **升级时。**新 event-management 首次启动为未压缩基础事件建立索引，默认约最近一周，然后删除替代的旧索引。
  构建期间基础事件写入等待，读取不等待；锁表后构建上限 40 秒。
  超过 4,000,000 待索引行或 500 个 chunk 时拒绝且不改内容；构建超时会永久停止，直到索引存在。
  这些都无需重建实例，错误包含手动建立索引语句。期间旧 event-management 继续存储。
- **在安静时段手动建立。**在事件存储主实例执行以下语句，再重启 event-management，
  它找到索引后只删除被替代者。第一条移除中断构建留下的同名未完成索引，没有时无操作：

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_time;
  CREATE INDEX idx_events_tenant_device_time
    ON "event-management".events (tenant_id, device_token, occurred_time DESC)
    WITH (timescaledb.transaction_per_chunk);
  ```

  构建逐 chunk 执行，写入只等待目标 chunk 被索引时，约每条未压缩基础事件 3 微秒。
  此期间新旧 event-management 都等待向该 chunk 存储，超过代理 60 秒确认等待的事件会安全重新投递。
  繁忙实例请选择安静时段。
- **升级前建索引可避免拒绝。**超过 `4000000` 未压缩基础事件的 v0.19.0 实例，先执行两语句，
  v0.19.0 与它们兼容，升级时只删旧索引。计数使用[检查行数](#v0190-row-count)的查询，
  改为 `hypertable_name IN ('events')`。
- 回退 v0.19.0 保留新索引，旧版能使用它。

##### 两台设备现在可以在同一时刻发送相同的 `altId`，两个事件都会被存储 {#device-alt-id-key}

事件的 `altId` 是发送方自己的消息 ID，写入侧用它加上事件的 `occurredTime` 来丢弃被重新投递的副本。这个键按租户划分，因此同一租户的两台设备在同一时刻发送相同的 `altId` 时，第二个事件会被当作第一个的重新投递而被静默丢弃。现在键中包含设备：两个事件都会被存储，而同一设备在同一时刻再次发送相同的 `altId` 仍会被去重。如果你依赖一台设备的 `altId` 抑制另一台设备的事件，这种情况不会再发生；如有需要，请自行保证 `altId` 在你的设备之间唯一。

- **升级时。** 新版 `event-management` 首次启动会在尚未压缩的基础事件上构建新的唯一索引，然后删除它所取代的按租户索引；任何时刻都不会没有去重保护。构建期间，基础事件的写入会等待，读取不会；表被锁定后，构建限时 40 秒。当待索引的行超过 4,000,000，或表的分块超过 500 个时，它会拒绝执行且不做任何更改；无法锁定表或未能及时完成的启动同样不做更改，下次启动会再次尝试。这些情况都不需要重建：错误信息会附上手动构建索引的语句。期间，旧版 `event-management` 继续存储事件。
- **在安静时段手动构建。** 在事件存储的主库上执行以下语句，然后重启 `event-management`，它会发现该索引，只删除被取代的索引。第一条语句会删除被中断的构建留下的同名未完成索引，没有时则什么也不做：

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id;
  CREATE UNIQUE INDEX idx_events_tenant_device_alt_id
    ON "event-management".events (tenant_id, device_token, alt_id, occurred_time)
    WHERE alt_id IS NOT NULL;
  ```

  唯一索引一次性构建，因此基础事件的写入会在整个构建期间等待，读取不受影响。被持有超过代理 60 秒确认等待的事件会被重新投递，这是安全的。在繁忙的实例上，请选择安静的时段。
- **要避免被拒绝，请在升级前构建索引。** 在未压缩基础事件超过 `4000000` 的 `v0.19.0` 实例上，先执行上面两条语句；`v0.19.0` 可以与之配合工作，升级随后只需删除旧索引。统计基础事件数量时，使用[检查行数](#v0190-row-count)下的查询，并将条件改为 `hypertable_name IN ('events')`。
- 回退到 `v0.19.0` 会保留新索引，`v0.19.0` 可以使用它：重新投递的事件仍会被去重，但该版本仍按租户检查，因此会再次丢弃第二台设备的事件。

##### 设备接入流在消息转发后即删除 {#v0200-capture-stream}

保存每条设备发布、直到 `event-sources` 转发的流（`device-events-capture`），现在在 `event-sources` 确认消息时就将其删除。在 `v0.19.0` 中，消息转发后仍保留最多七天。尚未转发的发布与之前完全一样地保留，因此 `event-sources` 中断时仍不丢失任何消息。

- **升级时。**新 `event-sources` 首次启动会转发现有流中仍有的消息，等待流变空，然后删除该流并按新的保留方式重新创建。序列号会跳到旧流之后，因此不会重复使用。若在检查为空与重新创建之间的瞬间有设备发布到达，该消息在代理已确认后仍可能丢失。若流在五分钟内没有变空，`event-sources` 会以指明该流的错误停止，且不删除任何内容；待其可以转发后再启动。
- **其他任何程序都不能读取该流。**它只接受一个消费者，即 `event-sources` 拥有的那个，因此 `nats stream view` 和 `device-events-capture` 上的任何其他消费者都将失效。要查看设备发送的内容，请读取保存已解码事件的 `inbound-events` 流。
- 回退到 `v0.19.0` 需要先在 `event-sources` 停止时删除该流，因为 `v0.19.0` 会拒绝针对采用新保留方式的流启动；它会在下次启动时重新创建该流。删除时尚未转发的内容将丢失。

##### 无法编码失败记录的入站事件，不再存成空记录 {#v0200-failed-record-encode}

device-management 无法编码失败入站事件的记录时，此前仍存储空记录并确认事件。读回的失败没有原因和文本，真正失败没有记录在任何地方。现在不为该事件发布任何内容，确认事件，并在 `dead_letter_lost_total` 统计损失，因此 `DeadLetterWriteLost` 触发，Pod 日志的 `LOST` 行说明哪个事件。今天设备能够发送的事件都不会触发此路径；这是缺陷路径，告警让你知道此类缺陷。`DeadLetterWriteLost` 因而有第四个原因，列于[告警表](./detection-engine.md#what-to-watch)。

无需操作。

##### 启动时无法连接消息 broker 的服务会在一分钟后停止 {#v0200-broker-unreachable-at-start}

每个服务启动时都会等待消息 broker（NATS）。在 `v0.19.0` 中，它等待 30 秒后无论如何都会启动。启动时创建 bucket 的服务随后会以一个归咎于 broker 版本的错误停止（`key-value requires at least server version 2.6.2`），其他服务则报告就绪，却无法发送或接收任何内容。现在服务最多等待一分钟；如果届时 broker 仍未响应，服务会停止，错误中给出 broker 地址和最后一次连接错误，然后由 Kubernetes 重启。在这一分钟内启动的 broker 与之前一样处理。

- **Pod 以 `the NATS broker was not reachable before the startup connection wait ran out` 重启**，说明它无法连接实例配置中地址上的 broker。请检查 broker 的 Pod 是否在运行并已就绪，并阅读消息末尾的连接错误：无法解析的名称、被拒绝的连接、被拒绝的证书或凭据，各自指向不同的问题。
- **broker 离开更久只会带来重启，而不会让服务一直停止。** 每次重启都会重试（遵循 Kubernetes 通常的重启退避），broker 一旦响应，服务即可启动。

无需操作。

##### `dcctl install --dry-run` 按实际 install 拒绝重新安装 {#v0200-dry-run}

集群响应时，dry run 现在读取安装记录，设置改变时查询集群上运行哪些实例；若因现有集群内容导致安装拒绝，则使用 install 自己的消息失败：在没有集群状态的机器重新安装、有实例运行时更改设置、或无法工作的 `--backup-snapshot-class`。此前打印计划并以 0 退出。面向尚不存在或无法连接的集群仍打印计划，并说明未执行这些检查。Dry run 不检查 install 拒绝的全部情况；例如关系存储由旧发布构建的集群，仍只在真正 install 时拒绝。

无需操作。

##### `dcctl install` 和 `dcctl bootstrap` 等待每个数据库实例加入 {#v0200-wait-database-instances}

此前 `dcctl install` 在关系数据库主库接受连接后返回，`dcctl bootstrap` 只等待服务。因此任一命令都可能报告成功，bootstrap 甚至打印两个数据库已复制，而副本仍在创建。Install 现在记录自身、释放集群锁，再等待最多 15 分钟，直到关系数据库每个实例都加入。Bootstrap 先等待服务，再最多等待 15 分钟，直到实例事件存储每个实例都加入。如果仍有实例未及时加入，命令以错误退出，指出数据库和就绪实例数。重新运行同一命令可继续等待（install 会重新应用前提组件，执行期间拒绝 bootstrap；如果只想观察请用 `kubectl` 监视数据库）；这样结束的 bootstrap 不会撤销，install 已记录，所以期间仍能引导实例。健康安装额外耗时几秒。

无需其他操作。

##### 速率上限按其计量对象重新命名 {#v0200-rate-key-rename}

接入上限早已按读数计量，对外上限按连接器调用计量，但两者仍以"消息"命名。现已全面改名，不保留旧拼写：

| 原名 | 新名 |
| --- | --- |
| `ingestRateLimit.messagesPerSecond`（`event-sources`、`lwm2m-ingest`、`sparkplug-ingest` 配置） | `ingestRateLimit.readingsPerSecond` |
| `outboundMessagesPerSecond`（`event-processing`、`outbound-connectors` 配置） | `outboundCallsPerSecond` |
| `ingestMessagesPerSecond`（GraphQL 字段、层级配置键） | `ingestReadingsPerSecond` |
| `outboundMessagesPerSecond`（GraphQL 字段、层级配置键） | `outboundCallsPerSecond` |

- **升级前，重命名服务配置键。** 如果 values 在某个服务的 `config` 下设置了旧键，请改为新名。服务若仍发现旧键将拒绝启动，错误信息会给出应改用的键。它不会退回平台默认值，因为那会悄悄替换您设定的上限。
- **API 客户端。** `tenantGovernance`、管理 API 的租户类型及其创建和更新输入上的字段均已改名。选择或发送旧名的请求现在会失败，请更新管理租户上限的脚本或集成。使用旧键的层级配置会被拒绝，错误信息会列出可接受的键。
- **已存数据自动转换。** 升级会重命名租户覆盖列，并以相同取值为每个已存层级配置（包括随附层级和已删除层级）更换键名。
- **滚动升级期间。** 在所有 Pod 都运行新版本之前，旧 Pod 与新的 `user-management` 互不兼容。旧的接入或对外 Pod 请求旧的 `tenantGovernance` 字段，请求失败，该 Pod 按平台默认值计量所有租户，可能因此触发 `TenantsMeteredAtPlatformDefault`。新 Pod 重命名列后，旧的 `user-management` Pod 仍使用旧列名，它处理的租户读写会失败，直到被替换。两者均在滚动升级结束时消失；在此之前请避免修改租户或层级。

##### 删除租户现在会清除其对象存储前缀下的全部内容 {#v0200-object-prefix-erase}

此前清除已删除的租户时，只会移除 user-management 某一行所引用的对象。现在还会移除该租户在对象存储中前缀下的所有对象，无论由哪个服务存入，因此更新制品以及引用已丢失的对象都会随租户一起清除。

- **使用 S3 后端时，凭据现在还需要对存储桶拥有 `s3:ListBucket` 权限**，此外仍需 `s3:GetObject`、`s3:PutObject` 和 `s3:DeleteObject`。缺少该权限时，租户清除的对象步骤会失败并在每一轮重试，清除因此无法完成。请在升级前授予。文件系统后端无需任何改动。

##### 已删除租户的 token 会保留到为其签发的一切都已过期，使用该 token 新建的租户从干净状态开始 {#v0200-token-hold}

已删除租户的 token 过去在删除开始后的固定时间释放。现在保留期（`tokenHoldSeconds`，默认 12 小时）从最后一个存储系统完成清除该租户数据时算起，再加一分钟余量，这样为已删除租户签发的凭据在 token 可被复用之前已经过期。因此一次删除保留其 token 的时间比以前略长。有三项改动支撑这一点：

- **清除开始后不再签发设备凭据。** 从那时起，该租户设备的连接会被拒绝。
- **LwM2M 和 Sparkplug 服务会在一小时后丢弃缓存的设备查询结果**，已删除租户的 LwM2M 注册会被终止，而不是被延长。
- **签发时间早于租户创建时间（按秒比较）的刷新令牌会被拒绝**，对任何调用方都一样。租户被删除后又用相同 token 新建租户时，原先登录第一个租户的人需要重新登录。

为已删除租户保留的审计条目现在归档在该次删除名下，而不是租户的 token 名下，因此新租户自己的日志从空白开始。管理 API 的 `TenantDeletion` 类型新增 `retainedAuditTenant`，它就是读取这些条目所用的 `auditEvents` 的 `tenant` 过滤值；在控制台中，**Admin → Audit（管理 → 审计）**的租户筛选会把每次已完成的删除列为 `<token> (deleted <date>)`。无需任何操作。参见[租户删除](./tenant-deletion.md#audit)。

##### 不存在的记录以 `NOT_FOUND` 应答 {#v0200-not-found}

指向不存在记录的请求，过去在大多数服务中以不带 `extensions.code` 的 `record not found` 到达客户端。现在它携带 `extensions.code` 为 `NOT_FOUND`，消息保持不变。对记录不存在本来就有自己代码的拒绝（如 `ARTIFACT_NOT_FOUND`）会保留该代码。在本版本中，user-management 的 API 对部分记录不存在的情形（不存在的租户、角色、身份或成员关系）仍以 `tenant not found` 之类的消息应答，且不带代码。此前通过读取消息来区分记录不存在与其他失败的客户端，可以改用该代码。参见 [GraphQL 参考](../reference/graphql-api.md#not-found)。

##### 单点登录是新增功能，你无需做任何操作 {#v0200-sso}

用户现在可以通过外部 OpenID Connect 身份提供商登录，租户也可以要求使用它。在超级用户创建连接之前，一切都不会启用，因此升级后的实例行为与之前一致。无需迁移，密码登录继续可用。[服务器端退出登录](#v0200-server-side-logout)导致的一次性重新登录已在前文说明。

- **设置提供商**见[配置单点登录](../guides/configuring-sso.md)，其行为见[单点登录](../concepts/single-sign-on.md)。
- **登录界面只从电子邮件地址开始。** 域名拥有已启用且已批准连接的地址，会看到 **Continue with single sign-on**（继续使用单点登录），旁边是 **Use a password instead**（改用密码）；其他地址直接进入密码字段。带电子邮件调用 `beginSso` 时，对没有连接的域名，以及连接已禁用或等待批准的域名，都返回 `null`。按 token 指定的连接仍会以“single sign-on is not available”拒绝。密码登录不会被移除，因此只有密码的账号不会被锁在外面。
- **用户可以自行关联自己的账号。** 用密码登录的人可以从用户菜单关联单点登录账号。`beginSsoLink` 不再接收连接 token：服务器使用超级用户为此人自己的电子邮件域名验证过的那个连接。每次拒绝都带有代码（`SSO_LINK_SESSION_REQUIRED`、`SSO_LINK_PASSWORD_REQUIRED`、`SSO_LINK_NOT_ALLOWED`、`SSO_LINK_NO_CONNECTION`、`SSO_LINK_ALREADY_LINKED`），从提供商返回时的拒绝会指明哪项检查失败。
- **连接还可以同时在身份提供商处退出登录。** 在管理员开启之前它是关闭的，因为在提供商处退出会结束该提供商所服务的每个应用的会话。升级会为每个连接加上该设置（`providerSignOut`，默认关闭）；设置之前不会有任何变化。`logout` 接受可选的 `returnTo`（`CONSOLE` 或 `DASHBOARD`），并返回 `SignOut { providerSignOutUrl }`，没有更多事情要做时为 `null`。需要在提供商处登记的地址位于连接上（`signOutReturnUrls`）。参见[在提供商处退出登录](../guides/configuring-sso.md#provider-sign-out)。
- **结束会话不会收回已经签发的令牌。**在退出登录、撤销、取消链接或禁用连接之后，已签发的访问令牌和身份令牌在过期前仍然有效，默认 15 分钟。

##### 单点登录需要实例的公开地址，因此 chart 现在会自动推导 {#v0200-sso-issuer}

通过外部 OpenID Connect 身份提供方登录，以及 MCP 客户端使用的 OAuth 授权服务器，都需要知道本实例
的访问地址，因为登录完成后必须返回到某个地方。user-management 的 `auth.issuerUrl` 过去除非手动设置，
否则为空。现在，当实例通过 https 提供服务，或位于回环主机（`localhost`、`127.0.0.1`、`::1`）上时，
chart 会从 ingress 推导出 `https://<host>/api/user-management`，并且你自己设置的值始终优先。由此带来三点变化：

- **这些实例上的 OAuth 授权服务器会开启。** 在默认安装中，MCP 客户端以前无法获得令牌，因为在你设置该值
  之前它是关闭的。若要保持关闭（单点登录也随之关闭），请将
  `functionalAreas.user-management.config.auth.issuerUrl` 设为 `""`。
- **升级后签发的令牌以该地址作为签发者**，而不再是每个实例的标识符。各服务的令牌校验器既不检查签发者，
  也不检查受众，因此升级前签发的令牌在过期之前继续有效。无需任何操作。
- **使用真实主机名的纯 http 实例不会得到签发者**，因为 user-management 拒绝在回环主机之外使用 http 签发者。
  授权服务器和单点登录保持关闭，`helm install` 和 `dcctl bootstrap` 会明确提示。请通过 https 提供实例服务以开启它们。

##### 单点登录告警 {#v0200-sso-alerts}

三个告警报告单点登录的故障。如果你按名称路由或静音告警，这些是新增的。

| 告警 | 严重程度 | 报告内容 | 处理方式 |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | 十分钟内，user-management 一直无法连接某个已配置身份提供方的令牌端点或签名密钥，因此通过它登录的用户被拒绝。密码登录不受影响。 | 检查提供方是否在线、其地址能否从集群内解析、出站允许列表（`instance.config.infrastructure.egress`）是否允许本地部署提供方的地址范围；如果提供方迁移了端点，请刷新并批准该连接的发现快照。 |
| `SsoUnverifiedIdentityRefused` | warning | 提供方已为某次登录担保，但该登录仍被拒绝，结果见 `outcome` 标签：`refused_unverified_email`、`refused_unverified_domain`、`refused_required_claim` 或 `refused_system_role_identity`。没有任何人登录成功。 | 通常是连接配置有误，或有人在探测。请在审计日志中查找提供方所声明的地址，并查看提供方自己的登录日志。 |
| `SsoStateStoreFull` | critical | 保存已开始登录的存储已满，因此在条目过期（十分钟后）之前，user-management 拒绝通过任何提供方开始任何单点登录。密码登录不受影响。 | 几乎可以肯定有人在循环调用公开的登录开始接口：请在 ingress 日志中找到这些流量并在上游拦截。若流量合法，请调高 `instance.config.infrastructure.nats.kvStateMaxBytes`。 |

##### 邮箱的域名现在只有一种写法，域名含非 ASCII 字符的已存储地址将不再匹配 {#v0200-email-idna}

邮箱地址现在以唯一的规范形式存储、查找和比较：本地部分与之前一样去除空白并转为小写，域名转换为小写 ASCII（国际化域名使用 `xn--` 形式），且不带末尾的点。因此 `user@bücher.example` 与 `user@xn--bcher-kva.example` 在密码登录、单点登录和关联请求中是同一个身份，连接所声明的域名也按同样的形式匹配。不是严格 `local@domain` 形式，或域名不是有效主机名的地址会被拒绝。

- **域名为 ASCII 的地址不受影响。** 无需任何操作。
- **以非 ASCII 域名（`user@bücher.example`）存储的身份保留旧形式，在管理员重新创建它之前会被拒绝。** 它无法通过密码或单点登录登录，已持有的会话会在下一次交换时结束；此时选择租户或刷新会返回代码 `IDENTITY_EMAIL_NOT_CANONICAL`。管理操作仍可按其存储的地址找到它，因此你可以禁用它、结束其会话、取消关联并删除它。要恢复访问，请取消关联并删除旧身份，然后重新创建（域名的两种写法均可：它会以 `xn--` 形式存储），并恢复其成员关系和关联。在升级之前完成这些操作（域名使用其 `xn--` 形式）可避免中断：参见[升级前](#v0200-before)。已存储地址不是有效 `local@domain` 的身份同样适用。

##### 不再接受 X.509 设备凭据 {#v0200-x509}

在支持证书校验之前，不接受 `X509_CERTIFICATE` 凭据类型。创建该类型的凭据、把凭据改为该类型，以及用该类型
的凭据替换设备，都会以 `UNSUPPORTED` 被拒绝，且不写入任何内容。控制台不再提供该类型。升级前存储的该类型凭据
不再能认证，也不会被转换：请在升级前为每台此类设备提供 `ACCESS_TOKEN` 或 `MQTT_BASIC` 凭据。参见
[设备凭据](../guides/device-credentials.md)。

##### `MQTT_BASIC` 密码至少需要 16 字节 {#v0200-mqtt-password-length}

创建 `MQTT_BASIC` 凭据或设置其密码时，现在会拒绝短于 16 字节或长于 4096 字节的密码（按发送的内容计算）。
过短的密码以 `INVALID_VALUE` 被拒绝。设备密码是预置的而不是靠人记住的，因此下限是按生成的秘密设定的；
控制台的**生成强密码（Generate strong password）**可以生成一个。现有的较短密码在被重新设置之前继续可用。
把凭据改为 `MQTT_BASIC` 时，必须在同一请求中设置其密码。

##### 仪表板：发布即让版本上线 {#v0200-dashboard-publishing}

发布仪表板现在会让它创建的版本成为提供给查看者的版本，`activateDashboardVersion` 可让较早的版本重新上线；
没有取消发布操作。查看者通过 `publishedDashboard` 读取上线版本，`/dash` 按其令牌打开它。草稿供仪表板作者使用。

- **升级前发布的仪表板不会上线。** 之前没有记录它们提供的是哪个版本，因此在作者重新发布之前，查看者会被告知
  该仪表板尚未发布（`NOT_PUBLISHED`）。
- **`publishDashboard` 返回 `DashboardPublication`**，其中包含新的 `version` 和 `dashboard`。
- **读取仪表板的 `definition`，或通过 `dashboardVersion` 读取某个版本的正文，需要
  `dashboard:write`。** 只有 `dashboard:read` 的调用方读取 `publishedDashboard`。
- **`dashboards` 搜索返回 `DashboardSummary`**，其中没有 `definition`。
- **`dashboardVersions` 采用分页**，最新的在前：除非传入 `limit`（最多 1000）和 `offset`，否则返回 100 个版本，
  且不包含各版本的正文。
- `rollbackDashboard` 接受可选的 `expectedUpdatedAt`，并拒绝过时的回滚。
- `dashboards` 搜索的名称过滤器把 `%`、`_` 和 `\` 按字面匹配，而不是作为通配符。
- 修改正在删除的租户的仪表板会以 `TENANT_DELETED` 被拒绝；删除仪表板则不会。
- 当某个版本上有未解析的组件时，控制台不会发布该版本。参见
  [在组件未解析时发布](../guides/adding-widget-packages.md#publishing-while-a-widget-is-unresolved)。

##### 测量值聚合需要开始时间，并且有上限 {#v0200-aggregation-bounds}

`bucketedMeasurements` 现在要求提供 `startTime`，缺少时以 `INVALID_VALUE` 被拒绝；`endTime` 默认为当前时间。
当读取会产生超过 10,000 个桶，或者桶数乘以测量值名称数超过 50,000 时，读取会以 `LIMIT_EXCEEDED` 被拒绝，
而不是被截断。没有 `name` 过滤器时，计入的名称是该读取匹配到的不同名称。MCP 的
`aggregate_measurements` 工具有相同的要求和上限。

##### 现在会被拒绝的请求 {#v0200-input-bounds}

过去任意大小都会被接受的输入，现在超过以下上限时会被拒绝，除非另有说明，错误码为 `LIMIT_EXCEEDED`。
恰好等于上限的值会被接受。[GraphQL 参考](../reference/graphql-api.md#input-validation)列出了这些上限。

| 内容 | 上限 |
| --- | --- |
| 在每个服务中按令牌、ID 或外部 ID 进行的一次查找中的键，以及一次 `removeEntityRelationships` 的令牌 | 1,000 |
| JSON 输入：元数据、属性、配置、载荷、收件人等 | 64 KiB |
| 命令入队或批量提交时的载荷或元数据 | 64 KiB（`PAYLOAD_TOO_LARGE`、`METADATA_TOO_LARGE`） |
| 命令名称 | 128 字节（`COMMAND_NAME_TOO_LONG`） |
| 属性键，以及属性值 | 128 字节，以及 64 KiB |
| 检测规则的定义，及其编辑图 | 各 256 KiB |
| 一台设备的被跟踪关系 | 256 |
| 一个 profile 的指标定义、命令定义和检测规则 | 每种 1,000 |
| 一次 `createEntityRelationships` 创建的关系 | 1,000 |
| `deviceProfileVersions`、`entityGroupVersions`、`assetTypeVersions` 和 `connectorVersions` 返回的版本 | 最新的 1,000 个，除非使用新增的 `limit` 和 `offset` 分页 |
| 一条通知规则的收件人 | 100 |
| OAuth 客户端的 `redirectUris` 及其 `scopes` | 各 16 项，每项最多 2,048 和 64 个字符 |
| 租户层级，以及一次 `reorderTenantTiers` 的令牌 | 100 |
| 通过管理 API 设置的密码 | 72 字节 |
| 一次管理请求中的角色、权限或角色令牌 | 100 |
| 运行一次 GraphQL 查询或变更 | 60 秒 |
| 在 GraphQL 以及 OAuth 和 MCP 端点上接收请求正文 | 30 秒；停滞的正文会得到 `400` 响应并关闭连接 |

另有三种值现在会在创建或更新记录时被拒绝，过去它们会被存储，之后才失败或不起作用：

- 类型为 `gcp_pubsub` 的连接器，它还没有投递客户端（`UNSUPPORTED`）；之前存储的此类连接器在派发时
  仍会被记为死信；
- 设置了任何内容的 AI 提供商 `params`（`UNSUPPORTED`），因为没有提供商客户端会应用它们；
  空值、null 和 `{}` 会被接受；
- webhook 通知通道中投递时会被拒绝的自定义标头：无效名称、值中含控制字符、保留的 `Authorization` 或
  `X-DC-*` 标头，或重复标头。

##### 事件路径服务使用调优后的垃圾回收器运行 {#v0200-gogc}

`event-sources`、`event-management`、`device-management`、`device-state` 和 `event-processing` 现在以
`GOGC=400` 运行，并把软内存限制（`GOMEMLIMIT`）设为其内存限制的 75%，`event-processing` 的内存限制至少为
384Mi。在 Google Kubernetes Engine 上以每秒 6,000 事件运行，并与同一会话中的对照运行比较，每事件 CPU 在
`device-management` 和 `event-management` 中下降 18%，在 `device-state` 中下降 15%，在 `event-sources` 中下降 4%，
在 `event-processing` 中下降 5%。

它们是 chart values：`functionalAreas.<area>.gogc` 和 `functionalAreas.<area>.goMemLimitPercent`，
另有新增的顶层 `gogc`（默认 `0`，关闭），与现有的顶层 `goMemLimitPercent` 并列。功能区自身的值优先于顶层值，
因此对这五个服务而言，顶层的 `0` 不再关闭软限制：要为某个功能区关闭其中任一项，请把该功能区自身的值设为 `0`。

##### `device-management` 将查找结果保留数分钟，变更会传达到每个副本 {#v0200-resolve-caches}

`device-management` 会查找每个事件的凭据、设备、被跟踪关系、profile 和分组成员关系，过去在每个副本的内存中
保留 5 秒，因此上报间隔比这更长的设备每次都要重新查找。现在默认保留 5 分钟：

| `device-management` 配置键 | 默认值 | 范围 |
| --- | --- | --- |
| `inMemoryCache.ttlSeconds`（新增） | 300 | 1-3600 |
| `inMemoryCache.credentialCacheMiB`（新增；过去固定为 16 MiB） | 32 | 1-256 |
| `deviceCacheTtlSeconds`、`relationshipCacheTtlSeconds`、`metricDefCacheTtlSeconds`、`membershipCacheTtlSeconds`（过去为 60） | 300 | 1-3600 |

超出范围的值会使服务无法启动。现在一次变更会让每个副本丢弃存储的副本，而不仅是做出变更的那个副本；
与代理的连接恢复的副本会丢弃它持有的全部内容，清除已删除的租户会让每个副本丢弃该租户的副本。
更新设备（包括更改其类型）以及创建关系（无论单个还是批量、通过兑现或重新打开认领，还是通过更改关系类型）
过去不会丢弃它们所改动的副本，现在会。

- **按整个活跃设备群为每个副本确定规格**，而不是设备群除以副本数：每个副本几乎都会看到每台设备。
  对于 60,000 台、每台有一个被跟踪关系的设备，大约需要 `inMemoryCache.credentialCacheMiB` 为 80、
  `inMemoryCache.perDeviceCacheMiB` 为 64，内存限制为 512Mi。小于设备群的上限会使缓存几乎无法命中。
- **撤销同样受这 5 分钟约束。** 某个副本若错过了通知它丢弃已撤销凭据的消息，又没有断开与代理的连接，
  就可能在其副本过期之前继续认证该凭据的事件。连接总是对照数据库检查。
  参见[撤销多快生效](../guides/device-credentials.md#revocation-timing)。

##### 读取方默认预取 {#v0200-fetch-ahead}

读取 stream 的服务现在会在分发当前批次的同时请求下一批，并且每次请求 128 条消息而不是 64 条。在 Google
Kubernetes Engine 上实测，这让检测引擎在重负载下的积压保持在几百条消息，而不是数万条；关闭它时，吞吐上限和
每个服务的每事件 CPU 都相同。设置 `instance.config.infrastructure.nats.fetch.ahead: false` 可将其关闭。参见
[预取](./observability.md#fetch-ahead)。

##### 检测通过新的消费者读取其事件 {#v0200-ack-floor}

`event-processing` 现在在每个检查点确认一次已解析事件，通过一个名称以 `_ackfloor` 结尾的新持久消费者
（`<instance>_event-processing_resolved-events_ackfloor`）。新消费者首次启动时，从已保存状态覆盖的最后一个
事件之后开始，新版本的第一个 leader 会删除旧消费者。无需迁移任何内容。你自己的仪表板或告警若按 `durable`
标签选择 `event-processing` 的已解析事件消费者，需要使用新名称；随附的告警两者都匹配。回退到 `v0.19.0`
会使检测再读取一次 stream 中保留的事件，并跳过已经见过的事件。

实时检测现在还会回读未投递给它的一段事件，而不是跳过，并有两个告警报告这一情况（`DetectLiveGapFillFailing`、
`DetectLiveDeliveriesRecovered`）。停止推进的检测循环会触发 `DetectLoopStalled`，每个检查点现在都在一个截止时间内
运行，其下限为新增的 `checkpointTimeoutSeconds`（默认 10，最多 120）。

##### AI 起草超时 {#v0200-inference-timeout}

`ai-inference` 对模型提供商单次调用的默认上限从 60 秒降为 40 秒（`inferenceTimeoutMs`，最多 120000）。
根据描述起草规则现在会等待到该上限，总预算为 50 秒，未能及时回答的模型会如实报告为超时，而不是报告为提供商
不可用。如果你依赖 60 秒的默认值，请自行设置 `inferenceTimeoutMs`。

##### `dcctl bootstrap` 可以为实例提供对象存储 {#v0200-object-store}

租户徽标和更新制品保存在对象存储中，而由 `dcctl` 构建的实例没有对象存储，因此两者都以
`OBJECT_STORE_NOT_CONFIGURED` 被拒绝。`dcctl bootstrap` 现在接受 `--object-store none|filesystem|s3`，
并提供指定卷或存储桶的参数。不带该参数时，`dcctl` 创建的集群上的实例会得到 5Gi 的文件系统存储，通过
`--kube-context` 指定的集群上的实例则没有。所做选择会被记录，并由 `dcctl upgrade` 保留，它会拒绝会使已存对象
无处可寻的更改。**`dcctl upgrade` 不会为没有对象存储的实例添加存储**：在 `v1.0.0` 之前，请销毁该实例，再带上
该参数重新 bootstrap。参见[对象存储](./bootstrap.md#object-store)。

##### 设备可以接收配置 {#v0200-device-configuration}

设备 profile 版本声明其设备的哪些共享属性对设备可见，可在 profile 编辑器的**配置（Configuration）**标签页中
声明，或使用 `setDeviceProfileConfigurationDeclaration`。写入已声明的共享属性会创建一个带编号的配置修订版，
并在设备自己的配置主题上发送给设备；设备报告 `RECEIVED`、`APPLIED` 或 `REJECTED`，并通过 `sync` 请求当前修订版，
新发布的 profile 也是这样到达设备的。控制台中的设备页面显示声明、当前修订版以及设备最近报告的内容，
`deviceConfiguration` 和 `deviceConfigurationRevisions` 读取这些信息。写入已声明的属性时，若文档会超过 64 KiB，
以 `CONFIGURATION_TOO_LARGE` 被拒绝；若值不符合声明的类型，以 `CONFIGURATION_VALUE_TYPE_MISMATCH` 被拒绝。
目前仅支持 MQTT 设备。参见[配置投递主题](../guides/connecting-a-device.md#reserved-device-topics)。

##### 空中升级（OTA）：第一部分 {#v0200-updates}

新的 `update-management` 服务为每个租户维护固件、软件和配置镜像目录，上传时会对照声明的大小和 SHA-256 摘要
进行检查。制品一次分配给一台设备；设备在其配置主题上收到通知，使用可断点续传的短期授权下载镜像，每次尝试
都会被跟踪，直到设备运行该版本。它需要主动启用：在 `full` 部署配置中运行，或使用
`--enable-area update-management`。它需要对象存储，并且：

- **在设置 `updates.allowUnverifiedArtifacts: true` 之前，无法分配任何制品**，因为目前还没有任何机制能认证
  制品的生产者。
- **在设置 `updates.downloadBaseUrl` 之前，设备得不到下载授权。**
- **不要让下载路径的查询字符串进入访问日志。** chart 在一个关闭了访问日志的独立 ingress 上提供该路径；
  若位于其他代理之后，也请这样做。
- 目前还没有设备群推送，并且只能更新 MQTT 设备。

##### 发布的包 {#v0200-packages}

- **`@devicechain/widget-sdk` 是新增的包。** 它用于定义组件（`defineWidget`）并构建组件注册表
  （`createWidgetRegistry`）；参见[编写自定义组件](../guides/writing-a-custom-widget.md)。
  `@devicechain/widgets` 将其声明为 peer 依赖，因此请在 `@devicechain/widgets` 旁边安装它。
- **`@devicechain/widgets` 不再导出 `WIDGET_REGISTRY`、`ALARM_WIDGET_REGISTRY`、
  `CONTROL_WIDGET_REGISTRY`、`SELECTION_WIDGET_REGISTRY` 或 `LOCATION_WIDGET_REGISTRY`。** 请通过注册表查找组件，
  例如 `createBuiltinWidgetRegistry` 构建的注册表。
- **`echarts` 是 `@devicechain/widgets` 的 peer 依赖，而不是普通依赖：** 请在你的应用中添加 `echarts`
  （`^6.0.0`）。图表辅助函数位于 `@devicechain/widget-sdk/echarts`。
- **peer 版本范围已提高：** `react` 和 `react-dom` 升至 `^19.3.0`，`maplibre-gl` 升至 `^6.11.2`。
- **`DashboardHubConfig.bindings` 和 `DashboardHub.setBindings` 已从 `@devicechain/dashboards` 中移除。**
  请把绑定传给 `DashboardRenderer`，并使用 `useDashboardHub`。
  `fetchWidgetHistory` 和 `widgetSubjectLabel` 接受数据源已解析的组件。
- **`parseDashboardDefinition(raw, { catalog })`** 接受用于校验的组件目录。类型未知的组件，或目录无法读取的组件，
  会作为占位符保留（`UnknownWidgetInstance`，属于 `WidgetInstance` 联合类型），并按存储时的原样写回，
  过去这会导致整个定义失败。存储的类型为 `unbound` 的数据源会被拒绝。
- **.NET SDK** 自行建立订阅，不再经过系统 HTTP 代理。设备可以选择并发处理命令（`MaxConcurrentCommands`），
  每条命令带有其到达顺序（`DeviceCommand.Sequence`），在重新连接期间完成的响应会在重连后发送，而不是被丢弃。

##### 新告警 {#v0200-alerts}

如果你按名称路由或静音告警，除了[单点登录告警](#v0200-sso-alerts)之外，以下告警也是新增的：

| 告警 | 报告内容 |
| --- | --- |
| `SafetyGateDisabled` | 某个服务在缺少一个可选安全闸门的情况下运行：租户生命周期闸门、命令在线状态，或在发布时校验检测规则。 |
| `DetectLoopStalled` | 检测 leader 已有数分钟没有推进。 |
| `DetectLiveGapFillFailing`、`DetectLiveDeliveriesRecovered` | 实时检测无法回读，或已回读未投递给它的事件。 |
| `SparkplugIngestLeaderCountNotOne`、`Lwm2mIngestLeaderCountNotOne`、`Lwm2mIngestLeaderNotServing` | 某个边缘接入服务的 leader 不恰好是一个，或其 leader 没有在提供服务。没有任何源的 `sparkplug-ingest` 没有 leader，会触发此告警。 |
| `SparkplugIngestDroppingData`、`SparkplugIngestSubscriptionRefused`、`SparkplugIngestBrokerUnreachable`、`Lwm2mIngestDroppingNotifications` | Sparkplug 或 LwM2M 接入正在丢失数据，或无法连接其代理。 |
| `CNPGCollectorAbsent` | Prometheus 已停止抓取某个数据库的指标。 |

`dcctl install` 还会停止抓取 Google Kubernetes Engine 等托管集群不运行的控制平面组件，也不再为其告警，
因此它们的 `...Down` 告警不会再在那里触发。

##### 其他值得了解的行为 {#v0200-other}

- **Sparkplug。** 替换在线会话的节点出生消息现在会把旧会话的子设备标记为已断开；过去它们会保持在线。
- **通知。** 类型不为 `notification-management` 所知的告警事件会先重试再记为死信，而不是被丢弃。
- **死信和解码失败**保留的载荷中，凭据字段会被替换为 `[redacted]`。不是 JSON 的正文只保留其长度和 SHA-256。
- **命令响应**的 `payload` 可以是任意 JSON 值，而不仅是字符串。
- 平台生成的**关联 ID** 为 22 个字符，而不是 36 个字符的 UUID；请将其视为不透明字符串。
- **GraphQL 描述**只来自 schema 中的 `"""` 字符串，现在每个元素都有描述；内省不再显示 `#` 注释。
- **带 `--kube-context` 的 `dcctl install` 和 `dcctl bootstrap`** 不再需要 Docker 或 `kind`，并会检查目标上下文的
  Kubernetes 版本。kubeconfig 中缺少指定的上下文会使预检失败。
- **检测可以把其状态拆分**到多个 `detectShards`（1-64，默认 1）。各分片仍依次运行，因此多于一个分片目前还不会
  让检测更快。

### v0.19.0：按每秒 6,000 事件实测定规格，满流拒绝新事件 {#v0190-upgrade}

v0.19.0 可从 v0.18.0 原地升级：先为集群 dcctl install，再逐实例 dcctl upgrade，
如[v0.17.0](#v0170-upgrade)所述。
**执行本发布 dcctl install 前，先计数事件存储。**
超过 4,000,000 尚未压缩行的实例不能原地升级，必须重建并丢弃数据；
多数实际流量实例都超出边界，参见[检查行数](#v0190-row-count)。
超过 500 个 chunk 的表同样拒绝，除非先移除最旧事件，参见[计数 chunk](#v0190-chunk-count)。
v0.16.0 及更早构建者仍必须[销毁重建](#pre-declaration-recreate)。

本发布主要关于跟上流量。处理每条事件的服务按实测 CPU 请求资源，分散到节点，
用更少语句存储和合并事件，并从内存回答重复查询。
`--ha` 且无 `--compact` 时，event-management 两个 Pod。
GKE 默认 HA 安装两次接受每秒 6,000 事件、每次十分钟，逐事件验证所有接受事件恰好存储一次，
参见[性能](#v0190-performance)。
两条失败路径从无声改为可见：接入流满时拒绝新事件而非丢弃未读事件，
备份告警在归档可能阻止数据库前预警。
dcctl upgrade 现在还应用实例代理和事件存储设置，过去从不这样做，
因此升级实例取得与新实例相同的代理和事件存储。

**谁需要操作：**

- **每个实际有流量的实例**：升级前计数未压缩行和 chunk。
  超过 4,000,000 行必须重建，重建丢数据，参见[检查行数](#v0190-row-count)。
- **每个集群**：任何 bootstrap、upgrade、destroy 前先用本发布重跑 install。
  本版 dcctl 拒绝旧版 install 记录。也更新每份使用的 dcctl：
  旧版可能把新版构建实例留作未完成，普通 bootstrap 随后会覆盖运行，参见[完成失败的 bootstrap](#v0190-bootstrap-resume)。
- **非 compact 的每实例**：节点需要容纳更大 CPU 请求及代理首次资源请求，参见[腾出资源](#v0190-room)。
- **单消息超过 256 读数的设备**：现在拒绝这种消息，参见[256 读数](#v0190-reading-limit)。
- **每秒超过 1,000 读数的租户**，包括单消息多个读数的设备群，以及**每个 LwM2M 租户**：
  接入上限改按读数，超限 HTTP 返回 429，其他所有传输不告知设备地丢弃，参见[读数而非消息](#v0190-ingest-readings)。
- **发送超过 366 天前读数的设备**，或时钟未设置、报告接近 1970 的设备：这些读数现在拒绝，参见[旧读数](#v0190-event-age-limit)。
- **设置过 `backup_retention`、`tsdbConfiguration.maxOpenConnections`、`rdbConfiguration.maxOpenConnections`、
  `maxReadingsPerMessage` 或分析读取连接限制者**：参见[升级前](#v0190-before)。
- **依赖设备凭据撤销立即生效者**：一个 device-management 副本上最多延迟五秒，参见[凭据检查](#v0190-credential-cache)。
- **HTTP 接入 API 调用者**：收到带 Retry-After 的 503 要重试，参见[背压](#v0190-backpressure)。
- **从自有 MQTT 代理读取的事件源**：使用新客户端 ID，代理必须允许；滚动期间可能被读取两次，参见[自有 MQTT 代理](#v0190-mqtt-client-id)。
- **按名称或严重级别路由、静默告警者**：新增告警，JetStreamStreamNearFull 改为 info，参见[告警](#v0190-alerts)。
- **自带 values 安装 chart 者**：若干顶层资源值不再到达事件路径服务，参见[自带 chart values](#v0190-chart-values)。

其余按功能区列在[变化内容](#v0190-what-changed)。

#### 升级前 {#v0190-before}

按顺序执行。

##### 1. 检查行数 {#v0190-row-count}

新 event-management 首次启动重建五张表所有未压缩行上的事件键，默认约最近一周。
**超过 4,000,000 此类行的实例无法原地升级。**
基础事件一行，每个读数、位置、警报各一行，每个关系锚点也一行。
每事件一读数、一锚点时，400 万行约相当于一周平均每秒两事件，所以多数真实流量实例超限。

在事件存储打开 psql，命名空间是 dci- 加实例 ID，数据库是裸实例 ID。
计数只读，因此任一存储 Pod 均可；没有 dc-tsdb-1 时，`kubectl -n dci-<instance-id> get pods` 列出其他 Pod：

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

然后执行升级使用的计数。它读取每条未压缩行，大型存储会耗时：

```sql
DO $$
DECLARE c record; n bigint := 0; k bigint;
BEGIN
  FOR c IN SELECT format('%I.%I', chunk_schema, chunk_name) AS chunk
           FROM timescaledb_information.chunks
           WHERE hypertable_schema = 'event-management'
             AND hypertable_name IN ('events', 'measurement_events', 'location_events',
                                     'alert_events', 'event_anchors')
  LOOP
    EXECUTE 'SELECT count(*) FROM ONLY ' || c.chunk INTO k;
    n := n + k;
  END LOOP;
  RAISE NOTICE 'rows to rebuild: %', n;
END $$;
```

输出超过 `4000000` 时不要原地升级：取出所需内容，用新发布 destroy 和 bootstrap 重建。
在集群仍为 v0.18.0、执行本发布 install 前计数，让超限实例仍能用创建它的 dcctl 读取和销毁。

:::caution 重建会丢弃实例数据
destroy 移除实例数据库中的每个租户、设备、设备定义、仪表盘、用户和全部事件。
DeviceChain 没有导出工具，销毁前自行取出所需内容。
租户事件、读数、位置、警报可通过 [SQL 和 BI 访问](../guides/sql-and-bi-access.md)读取。
PostgreSQL pg_dump 可复制两个以实例 ID 命名的数据库：
一个在实例命名空间的事件存储，另一个在共享关系数据库 dc-system 的 dc-rdb 中。
共享存储持有所有实例数据，所以只 dump 指定数据库。
写到自己的机器，例如
`kubectl -n dc-system exec dc-rdb-1 -c postgres -- pg_dump -U postgres -Fc <instance-id> > rdb.dump`，
事件存储使用 `-n dci-<instance-id>` 和 dc-tsdb-1。
dump 中密钥仍由实例根密钥封存。
dump 是可读取或载入自己数据库的副本，没有 dcctl 把它载入新实例。

保留备份以便回退。在本发布 install 前用 v0.18.0 dcctl 销毁，它保留集群内备份。
install 后用新版 `dcctl destroy --keep-backups`，否则删除实例事件存储归档。
两种方式保留的都是**事件存储**归档，旁边的集群关系归档从不被 destroy 删除。
同集群中事件归档仅恢复事件历史，控制平面为空，没有租户、设备、用户、密钥。
完整恢复到 v0.18.0 需要新集群，关系归档恢复到销毁前时间（--restore-rdb-at），
会回退该集群每个实例，并配合实例根密钥托管和事件归档，参见[恢复实例](./disaster-recovery.md#recover)。
把事件归档恢复到 v0.19.0 不是前进路径，它带回相同行，升级再次拒绝。

保留事件归档包含实例原来每个租户的事件，包括恢复窗口内删除的租户；实例消失后无人清理它。
不需要后按[实例备份如何处理](./bootstrap.md#destroy-backups)移除。

同名再次 bootstrap 前，移走并保留 `~/.devicechain/escrow/<instance>-rootkey.escrow`，
bootstrap 不覆盖它，它是解开销毁前关系备份的唯一密钥。

没有测试过超限实例的原地路径。
:::

##### 2. 计数 chunk {#v0190-chunk-count}

重建和[索引移除](#v0190-event-store-keys)都拒绝超过 **500 个 chunk** 的表。
它们锁定所改表每个 chunk，500 是本发布实测单次启动可重建最多数量的一半，为慢存储留余量。
超限时 event-management 在锁定前拒绝，避免无法完成的重建之后阻止每次启动。
一个 chunk 覆盖一表一个区间，默认一天（lifecycle.chunkIntervalHours），
所以关闭保留时 500 约为一年四个月历史。

更短区间会更早达到 500，发送遥远过去时间的设备也会如此。
在事件存储任一 Pod 计数：

```sql
SELECT hypertable_name, count(*) AS chunks, min(range_start) AS oldest, max(range_end) AS newest
FROM timescaledb_information.chunks WHERE hypertable_schema = 'event-management'
GROUP BY 1 ORDER BY 1;
```

表超过 500 且可舍弃最旧事件时，升级前移除。
**这会永久删除这些 chunk 中所有租户的事件。**

1. **停止发送来源。**设备仍发送遥远过去的读数时，先撤销凭据；旧版接受它们，会再次创建 chunk。
2. **选择截止时间。**结束于该时刻或之前的 chunk 整个移除，跨越该时刻的 chunk 保留。
   使用实例安装日期只删除早于实例存在的事件。六张事件表使用同一截止时间，避免事件失去读数。
3. **在事件存储主实例移除。**`kubectl -n dci-<instance-id> get pods -l cnpg.io/instanceRole=primary`
   列出主实例，选择 dc-tsdb- 开头者，按上文打开 psql，把下面 2024-01-01 替换为你的截止时间。
   独立执行，勿放进 BEGIN：每次最多移除一表 100 个 chunk，每批提交，数据库不必一次锁全部。

```sql
DO $$
DECLARE
  cutoff constant timestamptz := TIMESTAMPTZ '2024-01-01';
  tbl text;
  step timestamptz;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['events', 'measurement_events', 'location_events',
                             'alert_events', 'event_anchors', 'state_change_events'] LOOP
    LOOP
      SELECT max(range_end) INTO step FROM (
        SELECT range_end FROM timescaledb_information.chunks
        WHERE hypertable_schema = 'event-management' AND hypertable_name = tbl
          AND range_end <= cutoff
        ORDER BY range_end LIMIT 100) oldest;
      EXIT WHEN step IS NULL;
      PERFORM drop_chunks(format('%I.%I', 'event-management', tbl)::regclass, older_than => step);
      COMMIT;
    END LOOP;
  END LOOP;
END $$;
```

再计数 chunk，然后升级。不能舍弃这些事件时，按[检查行数](#v0190-row-count)重建实例。

##### 3. 在初次 bootstrap 的机器升级 {#v0190-where}

dcctl upgrade 现在应用实例 OpenTofu 配置，需要状态：在初次机器执行，
或先复制 `~/.devicechain/instances/<instance>/`，保持私有权限 0700，因为含凭据。
PATH 还需要 OpenTofu 1.9 或更高的 tofu，或 Terraform 1.9 或更高。
没有状态就拒绝，不改任何内容。--skip-infrastructure 仍只移动服务，并说明保留内容。

##### 4. 腾出资源 {#v0190-room}

非 compact 实例升级后请求更多 CPU，旧新 Pod 并行时滚动还需要额外空间：

| | 升级后增加的 CPU 请求 | 服务滚动同时需要的空闲 CPU |
| --- | --- | --- |
| `--ha` | 服务 4.45 核，加三个 NATS 节点各 500m CPU、768Mi 内存 | NATS 之外 4.85：与旧 Pod 并行启动的 device-management、两个 event-management、device-state、event-sources 共 4.55；旧 Pod 停后才启动的 event-processing 另 0.3 |
| 无 `--ha` | 服务 3.55 核，加一个 NATS 服务器 500m、768Mi | NATS 之外 3.95（3.65 加 0.3） |
| `--compact` | 服务不增加；每 NATS 节点 25m、64Mi | 与以前相同，NATS 之外 0.1，每个并行启动 Pod 25m |

这些数字针对运行全部五服务的实例。
新请求为 device-management 800m，event-management 900m（HA 两 Pod，第二个另增 128Mi），
device-state 950m，event-sources 1 核，event-processing 400m；v0.18.0 各 100m。
每新 Pod 的完整请求必须在一个节点有空闲容量。
每 NATS 现在请求 500m CPU、768Mi 内存，内存限制 2Gi，过去无请求。
比较 `kubectl describe nodes` 的 Allocated resources 中空闲可分配 CPU、内存与[服务定规格](./bootstrap.md#service-sizing)。
笔记本 kind 最可能不足。

无法放置的 Pod 停在 Pending，等待后升级失败，代理最长等 15 分钟，实例部分升级：
新 Pod 成功服务为新版，其余为旧版。无 HA 的 Pending NATS 使代理停机直到腾出空间。
腾出资源再运行 upgrade 完成。dcctl 实例不能保留旧请求，只能加容量或用 compact 重建。
compact 实例保留 25m、64Mi，NATS 同样请求。

命名空间 ResourceQuota 或 LimitRange 可拒绝更大请求、event-sources 和 device-state 的 2 核限制，
或 event-processing 的 1 核限制。

##### 5. 检查跨命名空间放置配额 {#v0190-quota}

数据库 Pod 新增检查其他命名空间的放置偏好，参见[数据库主实例](#v0190-primary-spread)。
带 CrossNamespacePodAffinity scope 的 ResourceQuota 会拒绝这些 Pod，即使只是 preferred：
dc-system 重启的关系库，以及每实例命名空间事件存储。
DeviceChain 安装内容不会创建这种配额。列出现有者：

```bash
kubectl get resourcequota -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SCOPES:.spec.scopeSelector
```

空列表不能作定论。API server 配额准入配置的 limitedResources 可指定 CrossNamespacePodAffinity，
此时**没有**对应 scope 允许配额的每个命名空间都拒绝这些 Pod。
该配置位于控制平面，询问集群管理员；若启用，升级前给 dc-system 和每实例命名空间提供对应配额。
拒绝事件存储 Pod 会使存储少一个实例，非 HA 时则停机。

##### 6. 检查现在阻止服务启动的设置 {#v0190-settings}

- event-management 的 tsdbConfiguration.maxOpenConnections 或 device-state 的 rdbConfiguration.maxOpenConnections
  为 10 或以下且未设置 persistence.writers、projection.writers 时，新 Pod 拒绝，指出设置和池大小。
  将写入者设为小于池，旧默认 5；或移除池设置使用默认 20。
  11 到 19 的池会启动，并记录超过半池分给写入者。
- **分析读取者**（HA）：connection_limit 总计最多 17，不再 57，因为平台为两 event-management Pod 保留 80 而非 40。
  超过 17 在任何修改前停止。自行为第二 Pod 提高过 timescale_analytics_reserved_connections 的，改回 40，
  它现在是每 Pod 预留。参见[连接上限](../guides/sql-and-bi-access.md#connection-cap)。
- **maxReadingsPerMessage** 退役，服务忽略并警告。参见[256 读数](#v0190-reading-limit)寻找会被拒绝的设备。

##### 7. 检查设备发送内容 {#v0190-device-traffic}

- **每消息超过 256 读数**会被拒绝，按[256 读数](#v0190-reading-limit)寻找设备。
- **每租户每秒超过 1,000 读数**现在超过默认按读数而非消息的上限。
  按[读数而非消息](#v0190-ingest-readings)找到租户，先提高等级接入速率。
  超限读数丢弃，不延迟：HTTP 429，MQTT、LwM2M、Sparkplug 不告知，丢弃的 MQTT 消息已经确认。
- **到达前超过 366 天的读数**拒绝。无时钟设备应省略 occurredTime。
  平台内已有等待消息按到达时刻判断；边缘代理暂存没有自身年龄限制，参见[旧读数](#v0190-event-age-limit)。

##### 8. 重命名 backup_retention {#v0190-rename-retention}

若设置过，集群改用 backup_retention_rdb，实例改用 backup_retention_tsdb。
`-var backup_retention=…` 拒绝；tfvars 行只警告，用默认代替；TF_VAR_backup_retention 无警告忽略。

##### 9. 检查备份存储余量 {#v0190-headroom}

关系数据库备份从 7 天改为 30 天，参见[恢复窗口](#v0190-backup-retention)。
已有存储保持原大小，因此旧存储未扩容则仍 20 GiB。
余量少时先扩容，参见[备份存储大小](./bootstrap.md#backup-store-size)。

##### 10. 保留自行设置的 OpenTofu 值 {#v0190-tfvars}

升级保留状态旁 `~/.devicechain/instances/<instance>/infra/instance/` 的 terraform.tfvars
或 TF_VAR_ 声明，不保留手动 -var。
拒绝缩短事件恢复窗口或停止声明分析读取者，降低代理请求或限制时警告。
先把此类值移入文件，参见[自行设置的值](#upgrade-infrastructure)。

##### 11. 替换过事件存储数据库镜像时 {#v0190-image}

检查支持 lz4 [WAL 压缩](#v0190-wal)，不支持的镜像重启可能失败。
只有每行都输出 t 才升级：

```bash
pods=$(kubectl -n dci-<instance> get pods -l cnpg.io/cluster=dc-tsdb,cnpg.io/podRole=instance -o name)
for p in $pods; do
  kubectl -n dci-<instance> exec "$p" -c postgres -- psql -U postgres -tAc \
    "SELECT 'lz4' = ANY (enumvals) FROM pg_settings WHERE name = 'wal_compression'"
done
```

##### 12. 事件源读取自有 MQTT 代理时 {#v0190-mqtt-client-id}

每个来源从 devicechain 改为 `devicechain:<instance>:<source>:<pod>`，参见[独立客户端 ID](#v0190-external-mqtt-client-id)。
ACL、允许列表、ID 规则若指定 devicechain，改允许 devicechain: 前缀。
ID 超过 MQTT 要求最短接受上限 23 字符。
拒绝新 ID 的代理使新 Pod 启动失败，日志包含原因，升级停滞，旧 Pod 继续读取。

避免滚动期间重复存储，计划暂停代理发布者整个滚动期间，参见[升级期间](#v0190-during)。

#### 升级期间 {#v0190-during}

**dcctl install** 将共享关系数据库恢复窗口改为 30 天、归档日志 zstd、添加放置标签，
实例重启一次。HA 先备用，再切到其他节点的备用。
单实例重启期间不可用，写入重试。

**dcctl upgrade** 在移动服务前应用 NATS 和事件存储，参见[详情](#v0190-upgrade-infrastructure)：

- NATS 为新请求和限制重启，HA 逐个，非 HA 一服务器重启期间代理约一分钟不可用。
  HA 替换服务器时 HTTP 接入可能短暂 503，按通常 503 重试，参见[服务质量](../guides/connecting-a-device.md#quality-of-service)。
- 非 HA 检测在代理重启时停止，约一分钟加最多 25 秒，然后从最近检查点回放，参见[检测分区](#v0190-detect-lease-release)。
- v0.18.0 创建实例的事件存储可能为放置偏好重启一次，先备用，HA 再切换。
  非 HA 唯一实例回来前不存事件，在接入流等待。WAL 压缩和 zstd 归档无需重启。
- 代理、事件存储可同时重启，升级等待两者。

**event-sources 滚动时，自有 MQTT 来源可能读两次。**
最后一个旧 Pod 停止前，仍用旧 ID 读取，与取得来源的新版 Pod 并行，各存收到内容，窗口内消息可存两次。
这是一次性，回滚也一样。暂停发布者整个滚动期间可避免。
回滚版留下的租约 30 秒内自行过期。

**新 event-management 首次启动**逐表移除十六索引并重建键，参见[事件存储键](#v0190-event-store-keys)。
处理某表时该表读写等待，通常几秒，键重建每表最多 45 秒。
旧 event-management 继续接收存储，等待过久批次重新投递后仍只存一次。
HA Pod 逐个滚动，首个新 Pod 就绪前执行，第二新 Pod 若同时开始会等待，startup check 可能重启它一次；
事后启动者不等待，两者均预期。
工作停止（表忙、一分钟耗尽、行过多、超过 500 chunk、表耗时超过 40 秒）时，
event-management 报错指出表和处理方式，其他服务已新版，旧 event-management 继续存储。
参见[事件存储变更停止时](#v0190-event-store-keys-stops)。

##### 事件存储变更停止时 {#v0190-event-store-keys-stops}

- **行过多**：任何修改前停止并解释，upgrade 报告滚动未完成，按[检查行数](#v0190-row-count)重建。
- **表超过 500 chunk**：锁定前停止，日志列出表、数量和[计数查询](#v0190-chunk-count)。
  此启动不修改、不标记，日志列出之前完成项；移除 chunk 后下次继续。
- **表忙或一分钟耗尽**：锁尝试最多五秒，每两秒重试，最多一分钟。
  停止错误列出表、已完成项和列出占用会话的查询，重启后继续。
  持续停止时检查长 SQL/BI、租户擦除、压缩或保留任务。
- **锁定后一个表键重建超过 40 秒**：撤销，保留旧键；每次后续启动立即停止，不接触该表，避免每次重启都阻塞接入。
  重建实例，或例如换更快存储后，用错误中的 COMMENT ON INDEX 语句再试一次。
  若有遥远过去事件，先按[计数 chunk](#v0190-chunk-count)移除以缩短重建，再执行 COMMENT ON INDEX。
- **锁槽不足**：索引移除和键重建锁定表所有 chunk，错误会说明并指出要提高的设置。

#### 升级后 {#v0190-after}

- **检查两个数据库主实例位置。**切换可能留下同节点，按[主实例运行位置](./bootstrap.md#ha-database-primaries)检查并移动。
- **观察备份存储三周。**30 天窗口中的内容到 30 天才老化，约三周持续增长，
  到约当前 WAL 四倍加约 23 个夜间基础备份后趋平。
- **观察租户上限拒绝的读数**：devicechain_eventsources_total_readings_rate_limited、
  devicechain_lwm2mingest_ingest_samples_shed_total、devicechain_sparkplugingest_ingest_samples_shed_total。
  有丢弃说明发送超过等级，提高等级速率或让设备减少发送。
- **已有卷保持大小。**升级说明新实例事件存储为 32Gi；8Gi 扩容参见[事件存储卷](./bootstrap.md#event-store-volume)。备份卷同样保持。

回退 v0.18.0 不需改数据，旧版可用新键和已移除索引存读事件，event-management 回到单 Pod，上限回到消息计量。
一种读取更慢：按关系锚点列事件会扫描租户所请求时间范围全部事件，包含压缩行。
设置过 inMemoryCache.perDeviceCacheEntries 或 inMemoryCache.perDeviceCacheMiB 的 device-management
必须先移除，v0.18.0 拒绝这两个键启动。

#### 变化内容 {#v0190-what-changed}

##### 接入 {#v0190-ingest}

###### 接入流满时拒绝新事件，不再丢弃未读事件 {#v0190-backpressure}

消费者落后到未读积压填满 inbound-events 或 resolved-events 时，流过去丢弃最旧事件腾出空间，
而这些是尚无人处理、设备却已被告知接受的事件。

现在 device-management 在 inbound-events 或 event-management 在 resolved-events 的未读积压达到容量 90% 时，
平台停止接受新事件，降至 80% 以下再恢复。
两流保留一周已处理事件，历史不计入 90%，只算未读事件。
满流也会在历史快速淘汰即将波及未读事件前拒绝，参见[大事件耗尽历史前拒绝](#v0190-ingest-history-runway)。

- **HTTP** 拒绝期间返回 503、Retry-After: 10，应重试 503。
  不带 Retry-After 的 503 仍表示发布本身失败。
  租户超自身上限先得到 429，只有限内请求才得 503，参见[读数而非消息](#v0190-ingest-readings)。
- **MQTT** 已被代理确认，消息在捕获流等待接入恢复。
- **Sparkplug、LwM2M** 读数和外部 MQTT 消息丢弃并计数，因为协议不能让平台要求设备重试。
  连接和断开变化仍接受。背压门控不限制其数量，代理 tap 仍应用租户上限；
  循环重连设备群仍可能推满流，届时像以前一样丢最旧事件。
- 拒绝影响**每个租户**，因为流共享；慢 device-state 或 event-processing 不触发它。
- 模拟器和负载工具把带 Retry-After 的 503 算拒绝，不算失败；报告方式见[负载报告](#v0190-loadtest-refusals)。

参见[接入路径背压](./observability.md#ingest-backpressure)。
没有重新配置流，仍运行旧版的服务升级前保留旧行为。

###### 满流在大事件挤掉未读内容前也拒绝 {#v0190-ingest-history-runway}

上述拒绝按流平均大小估计每个未读事件，因为代理报告总字节，不报告未读字节。
满流上等价于未读事件**数量**占比。
在小型已处理历史上突发大事件，可能未读计量才约半上限就填满流，丢失未处理事件。
即使每次发布后测量也不能捕获。

- **已处理历史快耗尽时也拒绝。**满流先丢最旧，未读前的已处理历史会先耗尽。
  写流服务在按当前淘汰速率 30 秒内将清空历史时拒绝，能撑一分钟或消费者读完时恢复。
  两者使用代理序号，不依赖事件大小。消费者跟得上的满流不拒绝。
  删除租户自身不触发，除非其事件恰为满流最旧；此时可无损拒绝至消费者追上。
  此情况及不足一分钟自身流量的满流参见[接入背压](./observability.md#ingest-backpressure)。
- **已收到未确认事件算未读**，包括失败后等待重新投递的事件。
  它位于满流前端时，可持续拒绝至确认或放弃。
- **服务自上次测量写入约容量千分之一后也测量**，最多每 100 毫秒一次，此外每五秒测量。
- 新指标 `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}` 显示历史可持续多久。
  JetStreamIngestBackpressureEngaged 也针对此原因触发，描述说明如何区分两原因。
- 如果两次测量间写服务消耗的历史超过之前观察到 30 秒淘汰量，
  或流首次达上限时几乎没有已处理历史，仍可能丢未读事件。

不重新配置流，不新增设置。

###### 租户接入上限按读数，HTTP 在共享背压前检查 {#v0190-ingest-readings}

所有设备传输现在按**读数**计量。读数是一个存储值：测量条目一个键、一个位置或一个警报。
以前 HTTP、MQTT 每消息计一次，不看读数数量。
默认每秒 1000 的租户可发送 256,000 读数，远超安装容量，最终共享背压拒绝所有租户。

- **HTTP、MQTT**：解码前消息仍计一次，再逐读数计量。
  HTTP 超限 429，MQTT 确认后丢弃，与以前消息超限相同。
  消息计入新增 devicechain_eventsources_total_msg_reading_limited，读数计入
  devicechain_eventsources_total_readings_rate_limited。
  已收到，所以也计入 devicechain_eventsources_total_inbound_messages，
  不计入仍仅统计解码前丢消息的 devicechain_eventsources_total_msg_rate_limited。
- **HTTP 先检查自身上限**，平台拒绝中仍对超限返回 429，只有限内才可能背压 503。
  背压前现在读取解码正文，被背压拒绝的请求现在也计收到。
- **外部 MQTT**同样先检查租户上限，平台拒绝时超限消息计入 msg_rate_limited，不是 msg_backpressured。
- **LwM2M** 已按读数计，但使用 25 倍上限，现在按原上限，所以预算**缩小 25 倍**。
- **Sparkplug 新增租户接入上限**，读数计量：ingestRateLimit.messagesPerSecond 默认 1000、burst 默认 2000。
  超限 DATA 消息读数丢弃，计入 devicechain_sparkplugingest_ingest_samples_shed_total。
  birth、death 和消息速率不计，参见[Sparkplug 接入界限](./edge-services.md#unbounded-sparkplug-ingest)。
- **默认仍 1000/秒、burst 2000，但单位改读数**。
  每消息一读数的设备群不变；多读数批量消息现在每租户限 1000 读数/秒。
  合法流量应提高租户等级接入速率，控制台现在显示读数/秒。

默认下，即使积压排空、两个边缘服务都运行，租户自身设备最多 5,000 读数/秒，
低于默认 HA、event-management 两 Pod 在[默认允许容量](../concepts/governance.md#ingest-default)所述集群实测 6,000/秒。
较小集群或多个 event-sources 副本应降低默认。

升级前寻找超过每秒 1000 读数租户：

- JSON MQTT/HTTP：统计繁忙小时租户已存测量每秒数量。
- LwM2M：与上限本身比较，不是 25 倍。
- Sparkplug：`rate(devicechain_sparkplugingest_measurements_emitted_total[5m])` 是该 Pod 来源租户读数，每来源一租户。
- [边缘代理](./edge-services.md)重连后尽快重新发布站点缓冲，单消息多读数站点追赶时可能超限。

回退恢复按消息计量，不改存储数据或 Schema，不重命名配置。

###### 每事件最多 256 读数，网关拆分更大消息 {#v0190-reading-limit}

**任何传输每事件最多 256 读数，不可配置。**一个读数是测量一个指标值，或位置、警报一条。
以前 HTTP、MQTT JSON 默认接受 1000，可由运维人员调高调低。

升级前在 v0.18.0 event-sources 设置 maxReadingsPerMessage: 256，观察 total_msg_too_many_readings。
每条计入消息都会被新版拒绝，修改对应设备，每消息最多 256。

- **HTTP、MQTT**：超 256 整条拒绝，绝不裁剪。HTTP 400 指出数量和限额。
  MQTT 代理解码前已确认，设备不知，计入 total_msg_too_many_readings 并进入 failed-decode 流。
  升级前捕获、后解码消息，包括边缘代理暂存，都按新限额。
- **maxReadingsPerMessage 退役**，启动警告并忽略；低于 256 也不再生效，请移除。
- **Sparkplug B**：超 256 指标过去单事件，现在拆连续最多 256 的事件，各值保持时间戳。
  *事件*数查询会变多，读数相同。规则独立看每事件，所以 hold-time 或 absence 可能在一条宽消息的两个事件之间触发。
- **LwM2M**：超 256 Notify 过去只保留前 256、丢其余，现在多事件存储，按事件依次计预算，
  不接受者计入 ingest_samples_shed_total。移除 notify_samples_truncated_total，请从仪表盘和告警移除。

###### 已排队的数千读数事件可以存储 {#v0190-large-events}

超过单数据库语句容量的事件（约 5,950 测量、5,450 位置、6,550 警报，或 9,350 锚点）过去永远无法存储。
驱动拒绝语句，event-management 重试至投递耗尽，记录 failed-events 为下游故障而非事件问题。
[256 限额](#v0190-reading-limit)之前，数千指标 Sparkplug 或提高默认 1000 的 JSON 都可产生，升级时仍可能排队。
现在同事务内用所需多条语句完整存储，全部存或全不存，重新投递不增加内容。
session ID 超出数据库有符号 64 位的状态变更事件过去同样重试后下游失败，现在首投递即记录无效。

###### 到达前超过 366 天的读数拒绝 {#v0190-event-age-limit}

信封或任意条目的 occurredTime 最多早于平台接收 366 天，更旧拒绝，绝不挪到新时刻：

- **HTTP、MQTT**：与其他时间戳拒绝一样整消息拒绝，HTTP 400，MQTT 写死信，
  两者计入 event-sources 的 total_msg_invalid_event_time。
- **Sparkplug、LwM2M**：仅丢太旧读数，剩余存储。
  网关消息由网关组合，不是单设备整体发送；Sparkplug 指标时间是值最后改变时刻，
  超 366 天不变值每次 birth 都丢，直至改变。时钟接近 1970 的节点全部丢。
  分别计入 samples_too_old_dropped_total、telemetry_too_old_dropped_total，日志警告指出设备。
- 其他那么旧的事件首次投递就以 Invalid 写死信，device-management 的 resolve_event_time_too_old_total 计数。

过去接受任意过去时间，事件存储每区间一 chunk，默认每日。
设备可为直到公元一年每一天创建分区，后续触及每分区的操作，例如升级重建键，都随之变慢。
已有过多表的升级处理见[计数 chunk](#v0190-chunk-count)。

- 无时钟设备应省略 occurredTime，平台按到达时刻标记；1970-01-01T00:00:00Z 被拒绝。
- 升级时平台已有等待消息按到达时刻判断，早于该时刻超过 366 天则被新版拒绝。
- 边缘代理暂存没有自己的年龄限制，参见[暂存](./edge-services.md#the-spool)。
- 限额固定，没有设置。

###### 自有 MQTT 来源用独立 ID，每次一个 Pod 读取 {#v0190-external-mqtt-client-id}

自有 MQTT 来源过去不论实例、来源、Pod，都用 devicechain ID。
代理每 ID 一个会话，第二连接断开旧连接，因此两连接循环抢会话，重连期间消息丢失且不计数。
两个 event-sources Pod、每次新 Pod 先启动的滚动、同代理两个来源、两个实例都会发生。
默认安装自身网关读平台代理流，不受影响。

现在 ID 为 `devicechain:<instance>:<source>:<pod>`，每来源一次一个 Pod。
通过平台代理租约约定归属，其他 Pod 直到释放都不连接此来源。
参见[传输矩阵](../reference/transport-matrix.md#external-mqtt-broker)，代理需先允许什么见[步骤 12](#v0190-mqtt-client-id)，
滚动期间一次双读见[升级期间](#v0190-during)。

- 正常停止两秒内交给另一个。突然节点故障、SIGKILL、OOM，约 30 秒加重连无人读。
  仍为至多一次，代理不保留两间隙交付内容。
- 接管后无法到达代理，释放并每 15 秒重试，不停止服务；订阅被拒绝仍停止服务。
- 更多 Pod 不再代表更多读取者，过去也没增吞吐，因为抢会话；现在增加待命者。
- 新 gauge `devicechain_eventsources_external_mqtt_owner{source}` 读取 Pod 为 1、其他为 0；
  新 `devicechain_eventsources_total_msg_not_owner{source}` 计刚失所有权后丢消息。
  新 warning ExternalMqttSourceNotReadByOnePod 在两分钟零或多个读取者时触发，
  参见[无人读取外部来源](./observability.md#external-mqtt-owner)。

##### 解析 {#v0190-resolution}

###### device-management 从内存回答重复查询 {#v0190-local-cache}

- **每个 `device-management` 副本将从键值缓存读取的内容在内存中保存最多五秒**，在此期间从内存回答同一设备、设备类型或租户的重复查询。缓存生存时间不足五秒时，这段时间也会缩短。
- **变更传播到其他副本解析的事件，可能额外需要最多五秒。** 删除设备或用相同令牌重新创建设备后，其他副本在这几秒内仍可能通过旧记录解析它；刚刚更改分组作用域的规则也可能在其他副本上按旧作用域求值。所有副本仍会立即丢弃刚删除设备的告警边沿。
- 每个副本的三个设备级缓存（按令牌查找设备、设备跟踪的关系、设备分组成员关系）**各最多 131,072 条或 24 MiB**；设备类型级和租户级缓存各最多 4,096 条或 4 MiB。`inMemoryCache.perDeviceCacheEntries` 和 `inMemoryCache.perDeviceCacheMiB` 可改变上限；调高时也要提高服务内存限制。
- **上报间隔超过五秒的设备无法从内存得到查询结果**，无论缓存多大。
- **事件的配置档案、关系和分组作用域查询同时执行**，分组成员关系查询也同时执行，因此未命中内存的事件只等待一次 NATS 往返，而不是三次。数据库仍逐个执行查询，所以 `resolution.workers` 对连接数的影响不变。
- **新增指标**统计内存回答的查询、移除的条目、大小和上限（`kv_cache_local_max_entries`、`kv_cache_local_max_bytes`）；`kv_cache_request_duration_seconds{op="get"}` 现在只统计内存无法回答的查询。参见[停止响应的缓存](./observability.md#kv-caches)。

###### device-management 从内存验证重复使用的设备凭据 {#v0190-credential-cache}

- **每个副本将刚验证成功的设备凭据在内存中保存最多五秒**，设备后续事件使用这份副本验证，无需读取数据库。验证规则与存储凭据完全一致：每次事件仍比较 `MQTT_BASIC` 密码，到期时间仍按时生效。验证失败的凭据从不缓存。
- **未收到通知的副本现在可能在最多五秒后才让撤销生效。** 禁用、删除、重新指向或以其他方式更改凭据，以及替换、编辑或删除其设备，都会在执行更改的副本返回之前移除缓存，并通知其他副本移除各自缓存。如果消息丢失（副本正在重新连接 NATS，或升级时某些副本仍运行旧版），该副本的缓存会在五秒内过期。此前撤销会在所有副本的下一条事件上生效。参见[撤销多久生效](../guides/device-credentials.md#revocation-timing)。
- **MQTT 建立连接时仍每次读取数据库**，因此已撤销凭据不能在任何副本上建立新连接。
- **每个副本最多缓存 65,536 个凭据或 16 MiB**，此上限固定；默认配置下，服务的内存缓存合计上限从 80 MiB 变为 96 MiB。新增指标统计缓存回答的验证、移除的凭据、缓存大小，以及发送和接收的移除通知。

##### 存储 {#v0190-storage}

###### 事件存储的键以时间开头，并移除了十六个索引 {#v0190-event-store-keys}

防止事件重复存储的键现在以租户和事件时间开头，而不是租户和事件摘要。因此新键靠近前一个键，数据库需要重写的索引页大幅减少。移除了十六个索引：十二个重复其他索引或没有任何查询使用的索引，以及基础事件、测量、位置和警报的租户与时间索引；新键已经能够服务这些查询。每行更新的索引少于 `v0.18.0`：基础事件行从五个减为两个（有备用 ID 时从六个减为三个），测量行从五个减为三个，位置或警报行从四个减为一个，关系锚点行从四个减为两个，在线状态变更行从四个减为一个。包含一个读数、没有锚点的测量事件从更新十个索引减为五个。

如果表超过 500 个分块导致 `event-management` 拒绝启动，日志指向的分批 `drop_chunks` 操作见[统计分块](#v0190-chunk-count)。

- 事件仍只存储一次，`event-management` 服务的每个读取仍使用索引。
- **设备事件列表对近期数据需要做更多工作。** 列表总数和按事件类型过滤的列表，现在会访问该设备所有尚未压缩的行（默认最近一周），包括其他租户中具有相同令牌的设备行。当存储中的不同设备令牌较少时（测试为 40 个而不是 100 个），其他设备持续发送期间一直静默的设备，其第一页查询可能明显变慢。
- **SQL 和 BI 访问。** 租户的事件、读数、位置、警报或锚点查询即使只按时间过滤，也使用索引。仅按时间查询 `analytics.state_change_events` 会读取所涉及每个未压缩分块中该租户的所有行；添加 `device_token` 才能使用索引。将 `analytics.event_anchors` 与 `analytics.events` 连接时，应同时使用 `event_id` 和 `occurred_time`。参见[实用说明](../guides/sql-and-bi-access.md#practical-notes)。

###### 用更少的语句写入批次 {#v0190-batch-writes}

- **`event-management` 对批次中的每个租户，每张表只用一条语句写入**，仍在同一个事务内，而不是每个事件使用多条语句。存储内容不变，仍在批次提交后才确认事件，重新投递的事件不会增加任何内容。当包含多个事件的语句中有一行被数据库拒绝时，批次会逐事件重写以定位该行；`persist_batch_fallbacks_total` 统计这种回退。连接和断开事件仍逐个写入。参见[事件持久化](./observability.md#event-persistence)。
- **`device-state` 对批次中的每个租户，用一条语句写入所有已有状态的设备。** 首次出现的设备仍单独创建，每次写入状态仍推进 `updatedAt`。
- **连接池中的每个数据库连接现在会在使用间隔保持打开**，直到池大小上限；此前只有一半保持打开，使用超过半个池的服务会为每次查询重新连接。连接仍在建立一小时后关闭。忙碌期之后，数据库可能显示每个服务有更多空闲连接；默认配置的[连接预算](./bootstrap.md#connection-budget)已涵盖这一点，但如果服务的 `replicas` 超过 1，或提高了 `maxOpenConnections`，请检查实例连接限制。显式设置的 `maxIdleConnections` 仍有效。

###### 新的流水线默认值 {#v0190-pipeline-defaults}

| 设置 | `v0.18.0` | `v0.19.0` |
| --- | --- | --- |
| `event-management` `persistence.writers` | 5 | 10 |
| `event-management` `persistence.maxBatch` | 32 | 64 |
| `event-management` `persistence.lingerMillis` | 0 | 10 |
| `device-state` `projection.writers` | 5 | 10 |
| `event-processing` CPU 限制 | 500m | 1 核 |
| `--ha` 下的 `event-management` Pod（不含 `--compact --ha`） | 1 | 2 |

已显式设置这些值的安装会保留原值；`persistence.lingerMillis: 0` 仍表示不等待。写入工作线程发现事件不足一整批时，现在最多等待 10 毫秒以收集更多事件。因此每个 Pod 每秒不足几百事件时，提交时间比以前最多晚 10 毫秒，`persist_duration_seconds` 也大约增加同样幅度；存在积压时没有变化。发布基准在每秒 3,000 事件下，每个存储事件的事务数降低 37%（从 0.126 到 0.080），存储耗时中位数增加约 5 毫秒，最慢 1% 降低 18%。这次比较也改变了事件存储的键和归档压缩、服务 CPU 请求及放置、`device-state` 写入工作线程数、检测 CPU 限制，以及 NATS 服务器请求和内存限制，因此无法单独归因于等待。在五分钟、每秒提供 6,800 事件的运行中，逐事件提交路径占 `event-management` CPU 的 4.8%，每事件 0.050 个事务；此前基准为 24%，但它使用加入等待之前的构建和不同服务节点。`--compact` 安装获得相同默认值，但不增加第二个 Pod；其请求不变。

**`--ha` 下运行两个 `event-management` Pod。** 三节点服务池中，有一个节点也运行领导入站事件流的 NATS 服务器。发布基准中，调度器将唯一的 `event-management` Pod 和 `device-state` 放在该节点；节点 CPU 达到 94–95%，每秒提供 6,800 事件时，单 Pod 存储速率降至 6,592 事件，成为首个落后阶段。第二个 Pod 能使用另一节点 CPU 存储。[性能](#v0190-performance)中的数据用两个 Pod 测得。Pod 倾向不同节点，但并不保证。每个 Pod 分别填充批次，因此事件存储每个事件提交的事务数约翻倍；数据库节点 CPU 保持低于 70%。自行安装 chart 时，默认仍为一个副本：设置 `functionalAreas.event-management.replicas: 2`，若事件存储由本仓库 OpenTofu 构建，还需设置 `event_management_replicas = 2`，以预留第二个 Pod 的连接。

##### 资源与放置 {#v0190-sizing}

###### 事件路径服务按实际用量请求资源，并分散到节点 {#v0190-service-sizing}

处理每个事件的五个服务按每秒 6,000 事件时测得的 CPU 用量请求资源（见[预留空间](#v0190-room)）。此前各请求 100m，远低于实际用量，按请求放置 Pod 的调度器会把最忙服务放在一起。`event-sources` 和 `device-state` 现在与 `device-management`、`event-management` 一样，最多使用 2 核。五个服务倾向不同节点（三节点时通常每节点不超过两个）；`functionalAreas.<service>.eventPathSpread: false` 可为单个服务关闭此行为。`device-management`、`event-management` 和 `event-sources` 还倾向不运行事件存储主库的节点。偏好只在调度 Pod 时生效：故障转移后，已在新主库节点运行的 Pod 会留到下次重新调度。

新实例的事件存储卷从 8Gi 增为 32Gi。NATS 服务器请求 500m CPU、768Mi 内存，限制为 2Gi；此前没有资源请求，节点内存不足时它们会最先被驱逐。2Gi 限制按稳定摄取量确定，尚未测量重启后追赶的服务器，因此升级中出现 `OOMKilled` 时，请提高实例 `terraform.tfvars` 中的 `nats_memory_limit`，再运行 `dcctl upgrade`。参见[服务资源](./bootstrap.md#service-sizing)和[消息代理](./bootstrap.md#broker-sizing)。

###### 同一服务的多个 Pod 倾向不同节点 {#v0190-own-pods-apart}

副本超过一个时，五个事件路径服务（`device-management`、`event-management`、`device-state`、`event-sources`、`event-processing`）各自倾向不运行自己其他 Pod 的节点。使用[事件路径分散](#v0190-service-sizing)的服务不再获得集群默认分散行为，这恢复了默认行为中让同一服务副本位于不同节点的部分，但没有恢复不同可用区的部分。调度器将此偏好与服务其他偏好一起权衡（节点上的事件路径服务较少，以及 `device-management`、`event-sources`、`event-management` 不与事件存储主库共处），并非保证。

- `--ha` 下适用于两个 `event-management` Pod。
- **自行安装 chart 时**，适用于五个服务中任何 `replicas` 超过一个的服务；它位于 Pod 反亲和性中，与三个服务的事件存储主库偏好并列。`eventPathSpread: false` 的服务保留集群默认分散（也倾向不同可用区），不会添加该偏好。

###### 自行设置 chart 值 {#v0190-chart-values}

自行安装 chart 时：

- 顶层 `resources.requests.cpu` 不再作用于 `device-management`、`event-management`、`device-state`、`event-sources`、`event-processing`，它们使用测量请求值。请在 `functionalAreas.<service>.resources.requests` 下设置，或设 `useMeasuredRequests: false`，让顶层请求再次用于全部服务。后者若顶层请求超过 1 核，会使 `event-processing` 渲染失败，因为超过了该服务自身限制。
- 顶层 `resources.limits.cpu` 超过 2 核时，`event-sources` 和 `device-state` 仍降至 2 核；它不再作用于 `event-processing`，后者始终为 1 核，除非在 `functionalAreas.event-processing.resources.limits` 设置。
- 服务自己的 CPU 限制低于测量请求时，会拒绝并指出 `measuredRequests`。
- chart 顶层值和 `functionalAreas` 下各服务块，现在拒绝 chart 不读取的键，因此键名拼写错误会导致渲染失败。
- 五个服务的 `eventPathSpread` 开启；为其他服务开启会让它与这五个一起分散。
- `replicas` 超过一个的服务倾向不运行自身其他 Pod 的节点（[同一服务的 Pod 分开](#v0190-own-pods-apart)）。

###### 数据库主库倾向不同节点 {#v0190-primary-spread}

每个数据库倾向不运行其他 DeviceChain 数据库主库的节点。三个 8-vCPU 节点测试中，同时运行两个主库的节点 CPU 为 94–98%，其余两个为 45–51%。这只是偏好，较小集群仍会调度所有数据库实例。它在数据库 Pod 调度时生效，因此故障转移或切换仍可能把两个主库放在同一节点；参见[数据库主库运行位置](./bootstrap.md#ha-database-primaries)。限制跨命名空间放置的配额可能拒绝这些 Pod；参见[步骤 5](#v0190-quota)。

###### 数据库可运行在选定节点 {#v0190-database-placement}

`dcctl install` 接受 `--database-node-selector` 和 `--database-toleration`，将共享关系存储和每个实例事件存储放在带标签的节点，也包括用污点隔离其他工作负载的节点。`dcctl bootstrap` 没有这些参数：所有实例遵循安装设置。若可用节点少于数据库实例数，install 会在安装任何内容前拒绝，且每次 bootstrap 都重新检查。NATS、服务和备份对象存储不受此放置控制。集群已有运行实例时拒绝增加放置配置；没有运行实例时会移动关系存储，但其卷可能无法跟随。参见[数据库放置](./bootstrap.md#database-placement)。

##### 备份 {#v0190-backups}

###### `dcctl destroy` 删除实例的集群内备份 {#v0190-destroy-backups}

实例命名空间删除后，destroy 删除事件存储在集群自有对象存储中归档路径下的所有内容，并检查路径为空。它会在更改任何内容之前读取并打印该路径。用户提供的对象存储中的备份从不删除，只打印位置。传入 `--keep-backups` 可保留集群内备份；在用 `--restore-tsdb-from` 从实例自身备份重建前务必传入，因为不带此参数的 destroy 会删除恢复要读取的归档。如果无法访问对象存储，destroy 仍完成并说明遗留内容。本次运行未写入的归档（旧发布遗留，或通过 `--keep-backups` 保留后再恢复的归档）只列出、不移除；[实例备份如何处理](./bootstrap.md#destroy-backups)说明手动移除方式。Destroy 不再打印 OpenTofu 的 **Changes to Outputs** 列表，因为那些值是配置默认值而不是实例实际值。

###### 归档停止传送的备份会提前告警 {#v0190-backup-alerts}

数据库保留尚未传送的预写日志时，`PostgresWALArchiveBacklog` 会触发，包括归档进程缓慢或挂起的情况。`BackupDestinationFillingFast` 和 `DatabaseVolumeFillingFast` 按备份存储或事件存储卷填满的速度触发。参见[停止传送的备份](./observability.md#backup-archiving)。

###### 备份存储的容量让事件存储先填满 {#v0190-backup-store-size}

新集群的集群内备份存储从 20 GiB 增为 **160 GiB**；保留 TLS 的 `--compact` 模式从 8 GiB 增为 **20 GiB**。20 GiB 存储在持续摄取 1,200 万至 1,600 万事件后就会填满，远早于 32 GiB 事件存储；此后归档停止，事件存储主库会填满自己的卷。容量估算假设两个数据库每个事件最多约 1.9 KB 归档，这是本次发布压缩预写日志、调整事件存储键、改用 zstd 归档之前测得的，因此偏大：发布基准中仅事件存储归档每个存储事件约 0.47 KB。**现有集群保留原有容量**：卷仅在创建时定大小，对现有存储设置 `backup_object_store_storage` 无效。要扩大现有集群，需要在允许扩容的 StorageClass 上扩展卷；参见[备份存储容量](./bootstrap.md#backup-store-size)。

###### 每个数据库保留自己的恢复窗口：核心数据 30 天，事件数据 7 天 {#v0190-backup-retention}

`backup_retention` 被集群配置的 `backup_retention_rdb`（默认 `30d`）和实例配置的 `backup_retention_tsdb`（默认 `7d`）替代。保存租户、用户、设备、规则、密钥和最新状态的关系数据库，现在可恢复到最近 30 天的任意时间点，而不是 7 天；已删除租户的核心数据也保留 30 天恢复能力（[刻意保留的内容](./tenant-deletion.md#retained)）。窗口必须是整数加 `d`、`w` 或 `m`，其他格式在 apply 前拒绝。恢复窗口限制事件存储保留量时，填满 20 GiB 存储的持续摄取速率从约每秒 19 事件降至约 13；160 GiB 从约 150 降至约 100。这些速率基于本次日志压缩、新键和 zstd 之前测得的单事件成本，因此从这个角度偏低。同时它们使用仅测量一次的关系数据库日志占比：快速上报的小规模设备群约 14%；更大或更慢的设备群占比可能更高，如果全部都是关系数据库日志，160 GiB 的数字约为 35。自行应用 OpenTofu 配置时，要保留 7 天可设 `backup_retention_rdb = "7d"`；`dcctl install` 没有此选项。参见[恢复窗口](./bootstrap.md#backup-retention)。

###### 数据库基础备份可使用卷快照 {#v0190-snapshot-backups}

存储驱动支持 CSI 卷快照的集群，使用 `dcctl install --backup-snapshot-class <class>` 后，每个数据库每日基础备份使用卷快照，而不是完整复制到备份存储。不传参数则不改变。参见[卷快照基础备份](./bootstrap.md#snapshot-base-backups)。

- 日志归档不变，每周日 04:00 仍向备份存储写入完整基础备份。每次恢复都读取存储而非快照，因此可能重放最多一周日志。
- 快照保存在云提供商，集群删除后仍存在。[卷快照基础备份](./bootstrap.md#snapshot-base-backups)说明删除前后应检查什么。
- 快照类必须存在、具有 `deletionPolicy: Delete`，并属于提供数据库卷的驱动；install 和每次 bootstrap 都预先检查这三项。GKE、AKS 包含快照控制器；EKS 需先安装快照控制器附加组件。
- DeviceChain Operator 每十分钟清理旧快照，保留恢复窗口内全部快照和窗口开始前最新的一份。其 ClusterRole 在所有命名空间增加：namespaces 的 `get`；CloudNativePG Backups 的 `get`、`list`、`delete`；CloudNativePG ScheduledBackups 的 `get`、`list`、`patch`；以及 `events.k8s.io` Events 的 `create`、`patch`。它仅操作 DeviceChain 创建的命名空间及自身配置渲染的计划，并把每次执行记录在 `devicechain.io/snapshot-retention-checked-at` 注解。
- 备份存储为每个数据库多保留最多一周日志，因此填满更快：默认窗口和容量下，持续摄取约每秒 60 事件，而不是约 100（仍基于压缩前测量，因此保守）。
- 新增 `PostgresNoRecentSnapshotBackup`、`DatabaseSnapshotPruningStalled`、`DatabaseSnapshotBackupsUnobserved`；这类集群的 `PostgresNoRecentBaseBackup` 等待时间从 36 小时改为 8.5 天。
- 设置属于集群；其上有运行实例时拒绝修改。

###### 预写日志压缩，并用 zstd 归档 {#v0190-wal}

新建和升级的事件存储会压缩预写日志中的页镜像（`wal_compression = lz4`），重新加载即可，无需重启。两个数据库的日志归档从 gzip 改为 zstd；基础备份仍是 gzip。恢复和备份过期处理按每个分段名称（`.gz` 或 `.zst`）读取两种格式，因此中途改变压缩方式的归档仍可恢复；发布升级测试恢复了这种归档，取回全部事件。在每秒 3,000 事件的发布基准中，键变更和 zstd 归档合计让事件存储每个存储事件的日志减少 22%，归档进程 CPU 减少 53%，比较对象是这些改动前的开发构建。参见[事件存储卷](./bootstrap.md#event-store-volume)。

##### 运维 {#v0190-operations}

###### `dcctl upgrade` 应用实例消息代理和事件存储设置 {#v0190-upgrade-infrastructure}

`dcctl upgrade` 现在先从本发布携带的 OpenTofu 配置应用实例的 NATS 服务器和事件存储，再更新服务。此前只有 `dcctl bootstrap` 应用这些设置，因此升级实例会默默保留旧代理和事件存储设置。本次升级为 `v0.18.0` 创建的实例加入 NATS 请求和限制、压缩预写日志、zstd 归档及主库放置偏好；此类实例的事件存储实例可能因放置偏好重启一次。卷大小保留。参见[升级如何应用基础设施](#upgrade-infrastructure)。实例 OpenTofu 配置现在声明需要 OpenTofu 或 Terraform 1.9 及以上。

###### 再次运行 `dcctl bootstrap` 可完成首次引导后期失败的实例 {#v0190-bootstrap-resume}

引导已写入实例配置文档后，在安装 chart 或等待服务就绪时失败，会留下一个 `dcctl bootstrap` 拒绝再次处理的实例，仿佛它已经在运行。

现在 `dcctl bootstrap` 声明正在构建的实例时，会在声明上记录首次引导未完成（注解 `core.devicechain.io/bootstrap-unfinished`）。成功结束的 bootstrap 或 upgrade 在同一次将实例记录为 `Ready` 的写入中移除它。在此之前，再运行相同 `dcctl bootstrap` 命令可完成实例。它复用上次留在集群的根密钥、代理凭据和数据库密码，并显示失败运行尚未到达的超级用户生成密码。记录移除后，再运行仍像以前一样拒绝。

- 运行成功但不能移除记录（写入被拒绝，或集群锁先被接管）时，以错误退出并提示重新运行相同命令，不会在后续 bootstrap 仍可能覆盖的实例上报告成功。
- 旧版开始首次引导的实例没有此记录，因此和此前一样视为运行中；拒绝消息会提到 `dcctl upgrade` 和 `dcctl destroy`。参见[完成中途失败的引导](./disaster-recovery.md#resuming-a-bootstrap)。
- 在对本版本构建的实例运行命令前，升级所有使用的 `dcctl` 副本。旧版不会移除记录，因此后来用旧版升级失败，可能让运行中实例保留记录，随后普通 `dcctl bootstrap` 会覆盖该实例。

###### 代理不响应时，检测重试释放分区 {#v0190-detect-lease-release}

检测引擎 Pod 停止时会释放分区，让下一 Pod 立即开始检测。此前若代理未回应释放，例如某台代理服务器刚好重启，Pod 尝试一次就放弃。下一 Pod 会等待分区过期（最多 30 秒），再等待 20 秒交接期，才开始检测。现在停止中的 Pod 在关闭时间允许范围内持续重试，直到代理响应，同时预留一次代理超时和完成停止所需时间。代理重启期间丢失续约或释放响应，也不再导致引擎失去分区。重试由停止的 Pod 执行，因此要到安装此发布后的下一次升级才生效：安装本次发布时，被替换 Pod 仍运行旧版，只尝试一次。

仍会暂停检测的情况：

- **没有 `--ha` 时，重启代理的升级会在整个重启期间停止检测**，通常约一分钟（见[代理和事件存储条目](#v0190-upgrade-infrastructure)）。代理中断超过 30 秒后，引擎无法保留分区，也没有记录说明它干净停止。因此代理恢复后，它会等待 20 秒交接期，加上最多 5 秒重试间隔，再像任意重启一样从最后检查点重放。随后的服务滚动把分区交给新 Pod，再重放一次，但不等待。
- **Pod 停止时代理持续不可达直到其关闭后**，仍只能让分区自行过期，下一 Pod 等待最多 30 秒再加交接期。

`--ha` 下，代理服务器逐台重启时代理仍可用。引擎只有在续约失败持续 30 秒时才失去分区。

###### 告警 {#v0190-alerts}

- **新增：** [背压](#v0190-backpressure)的 `JetStreamUnreadBacklogNearFull`（warning）和 `JetStreamIngestBackpressureEngaged`（critical），后者也用于[历史余量拒绝](#v0190-ingest-history-runway)；消费者超过五分钟未读取的数据超过流容量 80% 时的 `JetStreamDurableUnreadNearFull`（warning）；三个[备份告警](#v0190-backup-alerts)；三个[快照告警](#v0190-snapshot-backups)；[自有代理上的 MQTT 来源](#v0190-external-mqtt-client-id)的 `ExternalMqttSourceNotReadByOnePod`（warning）；[连接死亡却未关闭](#v0190-broker-liveness)的 `BrokerConnectionDiedSilently`（warning）；以及 [Kubernetes 重启的容器](#v0190-read-loop-restarts)的 `InstanceContainerRestarted`（warning）。
- **变化：** `JetStreamStreamNearFull` 现在为 `info`，只对保存运维人员记录的流触发（`failed-decode`、`failed-events`、`connector-dispatch.dead`、`max-deliveries`，以及 `user-management` 未报告读取时的 `dead-letters`），同时统计消息数上限和字节上限。kube-prometheus-stack 默认 Alertmanager 配置不发送 `info` 告警。参见[消费者从未读取的消息](./observability.md#unread-loss)。
- **新增序列：** `devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}`、`devicechain_<area>_jetstream_stream_sink{stream}`，以及上面各条目列出的指标，其中完整名称包括：
  - `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`（[历史余量拒绝](#v0190-ingest-history-runway)）；
  - `devicechain_eventsources_total_msg_reading_limited` 和 `devicechain_eventsources_total_readings_rate_limited`（[按读数而非消息计量](#v0190-ingest-readings)）；
  - `devicechain_sparkplugingest_samples_too_old_dropped_total`、`devicechain_lwm2mingest_telemetry_too_old_dropped_total`、`devicechain_devicemanagement_resolve_event_time_too_old_total`（[过旧读数](#v0190-event-age-limit)）；
  - `devicechain_eventsources_external_mqtt_owner{source}` 和 `devicechain_eventsources_total_msg_not_owner{source}`（[MQTT 客户端 ID](#v0190-external-mqtt-client-id)）；
  - `devicechain_<area>_nats_connection_dead_total{detected_by}` 和 `devicechain_<area>_nats_connection_dead_last_timestamp_seconds`（[停止响应的代理服务器](#v0190-broker-liveness)）。

  **移除：** `devicechain_lwm2mingest_notify_samples_truncated_total`。

###### 任何服务都可提供 Go 运行时性能剖析，默认关闭 {#v0190-profiling}

`functionalAreas.<service>.profiler.enabled: true` 为该服务提供 CPU、堆、分配、goroutine 和执行跟踪剖析，监听 Pod 回环地址的独立监听器，通过 `kubectl port-forward` 访问。它从不成为容器端口、Service 端口或入口路由。只重启该服务的 Pod。参见[剖析服务](./observability.md#profiling)。

###### Google Kubernetes Engine 配置（新增） {#v0190-gke}

新增 `deploy/gke`：创建 DeviceChain GKE 集群的 OpenTofu 配置，包含带污点的 `database` 池（三个 4-vCPU、16 GB 节点）和 `services` 池（三个 4-vCPU、8 GB 节点），均从标准持久磁盘启动。安装时用 `--database-node-selector`、`--database-toleration` 将数据库放在 `database` 池。默认 `--ha` 安装加一个实例符合新 Google Cloud 项目的 SSD 配额；增加实例，或在任何云以其他方式建集群时，应先检查磁盘配额：[前提条件](./bootstrap.md#prerequisites)列出卷大小，`deploy/gke` 指南列出需要申请的配额。

###### 负载测试报告统计拒绝的事件 {#v0190-loadtest-refusals}

模拟器和负载测试工具现在分别统计入口拒绝事件的两种情况：

- `shed`：`429`，因租户摄取上限拒绝。
- `backpressured`：带 `Retry-After` 的 `503`，因平台落后拒绝。此拒绝适用于所有租户。

不带 `Retry-After` 的 `503` 仍计为 `failed`。统计位置：

- 模拟器 `GET /status` 显示 `emitted`、`shed`、`backpressured`、`failed`。
- L1、monitor、detection、command、presence、batch 报告的 `drive` 部分显示 `accepted`、`shed`、`backpressured`、`failed`。
- contention 报告为每个探测租户显示四项。
- self-test 报告仍只显示 `accepted`。

具体变化：

- 此前多数负载测试报告无论入口拒绝多少事件，都显示 `shed 0`；现在显示真实计数。
- 模拟器 `stats.shed` 现在只计 `429`；此前也包含 `503`。
- contention 测试在背压拒绝从不被限流的租户，或未设置 contention floor 时拒绝任一租户的情况下判失败。这类运行不能说明限流优先级。此前测试将其报告为从不限流租户被限流。设置 floor 时，测试也可能在 floor 完全没有拒绝任何事件的运行中通过。

升级无需操作。

###### 服务在 40 秒内发现停止响应的代理服务器 {#v0190-broker-liveness}

代理服务器所在机器突然停止（节点重置或断电）时，连接该服务器的服务此前可能最多六分钟才发现，因为没有东西关闭连接。如果其中包括 `event-sources`，HTTP 摄取会停止这么久；机器很快恢复时 Kubernetes 不记录任何情况。测试中，硬重置持有 `event-sources` 代理连接的节点，使摄取中断约五分半。

- 每个服务现在每 10 秒 ping 代理服务器，三个间隔没有回应便放弃连接；向连接写入连续 10 秒无进展也放弃。因此死亡连接在 40 秒内被发现，服务随后连接响应的服务器。`event-sources` 用于设备在线状态的第二条代理连接使用相同上限。
- 新增计数器 `devicechain_<area>_nats_connection_dead_total{detected_by}`、仪表 `devicechain_<area>_nats_connection_dead_last_timestamp_seconds` 和 `BrokerConnectionDiedSilently`（warning）告警。参见[静默死亡的代理连接](./observability.md#broker-connection-dead)。
- `JetStreamIngestBackpressureEngaged` 运维手册现在包含写入服务无法连接代理的情况，[节点丢失](./bootstrap.md#ha-node-loss)说明连接丢失服务器的服务需要多久重连。

升级无需操作。

###### 相隔数分钟的代理扰动不再重启服务，重启会触发告警 {#v0190-read-loop-restarts}

某个消息读取循环持续失败时，服务会自行退出，由 Kubernetes 重启（参见[持续失败的读取循环现在重启服务](#v0180-read-loops)）。静默流的循环可能很久没有消息，此前从首次失败开始计两分钟，只在收到消息后重新计时。因此相隔数分钟或数小时的两次代理扰动，如服务器重启和后续消费者领导者变化，被计为一次超过两分钟的故障，服务在第二次退出。同时停止三台代理中的两台，会使多个服务一起如此重启，包括 `event-sources`，延长摄取中断。

- 现在只要代理自上次失败后回应过，循环就重新计时：消息投递、静默循环每几秒检查消费者存在、重新挂接消费者、重新连接都算回应。连续读取失败两分钟且中间没有代理回应的循环，仍像以前一样退出进程。
- 超过两分钟的代理中断不再在恢复瞬间重启全部服务；重连被视为代理回应。
- 空读取不算回应，因为客户端即使断开也报告空读取。因此恢复后几秒内第二次故障仍可能与第一次合并计算。
- 代理中间有回应但持续重复的故障，如消费者领导者每分钟迁移，会每次重试并记录日志，但不再重启服务。
- 新告警 `InstanceContainerRestarted`（warning）针对实例命名空间内过去 15 分钟被 Kubernetes 重启的每个容器。此前没有指标报告单次重启：监控栈的 `KubePodCrashLooping` 仅在 Pod 15 分钟后仍继续重启时触发。告警读取随附监控栈安装的 kube-state-metrics。参见[重启过的容器](./observability.md#container-restarts)。

升级无需操作。

###### 负载测试报告逐个检查事件，而不仅是总数 {#v0190-loadtest-identity}

L1 和 contention 负载测试现在检查发送的每个事件，不仅确认存储数量等于接受数量。相同总数可能隐藏丢失事件被重复事件或不应存在的事件抵消。

每个事件通过设备和模拟器发送的 `occurredTime` 标识。模拟器现在写到微秒，与事件存储精度一致。运行结束的稳定等待期后，工具通过租户 `events` 查询读回每个设备的存储事件，并检查：

- 每个接受事件恰好存储一次；
- 拒绝事件没有存储：`429`、带 `Retry-After` 的 `503` 或其他 `4xx`；
- 结果不确定的事件最多存储一次：超时、连接丢失、不带 `Retry-After` 的 `503` 或其他 `5xx`；
- 运行时间窗口没有其他事件。

报告位置：

- L1 报告有 `identity` 部分，包含 `missing`、`duplicateKeys`、`unexpected`、`refusedStored`、`ambiguousStored`、`ambiguousAbsent`，每项最多十个示例事件，最后检查距离运行结束的时间（`observedUntilAfterDriveSeconds`），及 `reconciled` 判定。新增 `ingest-identity` 检查在前三项任一非零，或证据无法判定时使运行失败，例如读回时仍有事件到达。读回请求失败后间隔两秒重试两次；仍失败则 `ingest-identity` 以不确定失败，错误记录在报告，报告仍写出。缺少此部分的报告不再通过。
- contention 报告为每个探测租户提供相同部分，检查名为 `gold-identity`、`shed-identity`。
- self-test 现在也删除一个存储事件并插入另一个的重复副本，让总数再次相等；只有数量检查仍通过而身份检查指出两个事件时才通过。
- `ingest-completeness` 仍比较总数，消息现在明确说明仅比较这一项。

重新投递副本至少在首个事件一分钟后存储：丢失确认的投递经过 60 秒代理才重发，还需处理；副本自己的确认若丢失还会再次重发。因此一分钟是副本最短时间，而非最长。要发布结果的运行应留出高于此值的稳定等待余量，例如 `--quiesce-settle 90s`。

模拟器不发送 `altId`。`altId` 会让事件存储在写入前跳过第二份副本，掩盖检查寻找的重复，并改变测量工作量。

升级无需操作。

#### 性能 {#v0190-performance}

Google Kubernetes Engine 上，三个 4-vCPU、16 GB 数据库节点和三个 4-vCPU、8 GB 服务节点的默认 HA 安装，两次各持续 10 分钟接受每秒 6,000 事件，且每个接受事件恰好存储一次。逐事件检查确认：没有缺失、重复或意外事件。

解析、存储和实时设备状态在这 10 分钟各自保持跟上，负载停止后 3 秒内积压排空。上述检查未覆盖的检测阶段，在每秒 6,000 时跟得上（峰值积压低于 1,000），从提供每秒 7,600 起落后。不声称任何高于 6,000 的持续速率。参见[测量吞吐量](./bootstrap.md#measured-throughput)。

### 一次性的持久摄取切换 {#the-one-time-durable-ingest-cutover}

引入**持久 MQTT 摄取**的发布改变 `event-sources` 接收设备遥测的方式：不再作为 MQTT 客户端订阅代理，而是消费代理在确认设备前先写入的持久捕获流。这让 `event-sources` 停机时遥测不再丢失。

第一次跨越此发布是普通原地升级，但请预期**短暂的遥测重复窗口**并提前规划：

- 滚动更新期间，退出中的 Pod 仍通过 MQTT 摄取，进入的 Pod 已开始消费捕获流，所以重叠期发布的消息被两者摄取。窗口由两 Pod 共存时长限定，即新 Pod 启动加旧 Pod 排空。
- 同时带有 **`altId` 和设备提供的 `occurredTime`** 的事件不受影响：写入侧去重键为 `(tenant, altId, occurredTime)`，重复会合并。有 `altId` 但没有 `occurredTime` 的事件**不会**合并：设备省略时间时解码器填入当前时间，两份副本由不同 Pod 在不同时刻解码，得到不同时间戳，存成两行。没有 `altId` 的遥测完全不去重。
- 刻意选择重叠。相反顺序——在捕获流存在前停止旧 Pod——会丢失空档中代理确认的全部消息，且静默丢失：设备被告知已接受，却永远不存储。重复读数可见、可修正；缺失读数两者都做不到。

:::danger 不要将 `event-sources` 设置为 `Recreate`
`event-sources` 的 `strategy: Recreate` 正好产生上述丢失顺序，因为先终止旧 Pod，再由新 Pod 创建捕获流。Chart 拒绝渲染此配置，避免静默丢失遥测。`event-sources` 不是单写入者服务，使用 `Recreate` 没有收益；切换后可以运行多个副本，而被替代的 MQTT 客户端路径不能。
:::

## 数据持久性 {#data-durability}

数据库层刻意与应用**生命周期独立**。两个数据库作为带销毁保护的独立基础设施提供，因此升级、重新安装或卸载*应用*从不触及数据库。这是常见情况，也是安全的。

:::caution 从基础设施配置移除数据库是另一种操作
保护规则只在数据库*位于*基础设施配置时保护它。一旦从配置中*移除*便不再保护：移除的资源不再受配置声明规则覆盖，移除计划会成功。数据库集群还拥有自己的卷，因此移除数据库会带走数据，而不会留下未挂接的卷。

不要通过编辑基础设施配置移除数据库来替换它。

升级在数据库迁移到 Operator 之前创建的实例，是会遇到此问题的情况，计划阶段会拒绝而不冒险。先导出两个数据库，再用 `--allow-legacy-db-removal` 重新运行 `dcctl install` 处理关系数据库，并带此参数 bootstrap 处理事件存储——此参数只是声明你已处理数据，不验证任何内容。对于本地实例，重建更简单，并刻意丢弃数据：销毁实例、重建集群、再 install 和 bootstrap，见 [v0.16.0 及更早构建的实例](#pre-declaration-recreate)。
:::

这是运行卷的持久性，不能替代生产基础设施提供的计划备份和时间点恢复。参见[部署与 Operator](./kubernetes-operator.md)，了解基础设施层与应用层如何分离。
