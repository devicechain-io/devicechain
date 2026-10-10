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
改变实例*是什么*是另一个问题，答案也不同：例如提高副本数不会重新复制按旧副本数创建的消息流。

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

### 下一发布 {#next-upgrade}

随开发合并收集 `v0.19.0` 之后版本的变化。

#### 设备事件列表只读取本租户的行 {#tenant-device-index}

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

#### 两台设备现在可以在同一时刻发送相同的 `altId`，两个事件都会被存储 {#device-alt-id-key}

事件的 `altId` 是发送方自己的消息 ID，写入侧用它加上事件的 `occurredTime` 来丢弃被重新投递的副本。这个键按租户划分，因此同一租户的两台设备在同一时刻发送相同的 `altId` 时，第二个事件会被当作第一个的重新投递而被静默丢弃。现在键中包含设备：两个事件都会被存储，而同一设备在同一时刻再次发送相同的 `altId` 仍会被去重。如果你依赖一台设备的 `altId` 抑制另一台设备的事件，这种情况不会再发生；如有需要，请自行保证 `altId` 在你的设备之间唯一。

- **升级时。** 新版 `event-management` 首次启动会在尚未压缩的基础事件上构建新的唯一索引，然后删除它所取代的按租户索引；任何时刻都不会没有去重保护。构建期间，基础事件的写入会等待，读取不会；表被锁定后，构建限时 40 秒。当待索引的行超过 4,000,000，或表的分块超过 500 个时，它会拒绝执行且不做任何更改；无法锁定表或未能及时完成的启动同样不做更改，下次启动会再次尝试。这些情况都不需要重建：错误信息会附上手动构建索引的语句。期间，旧版 `event-management` 继续存储事件。
- **在安静时段手动构建。** 在事件存储的主库上执行以下语句，然后重启 `event-management`，它会发现该索引，只删除被取代的索引。第一条语句会删除被中断的构建留下的同名未完成索引，没有时则什么也不做：

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id;
  CREATE UNIQUE INDEX idx_events_tenant_device_alt_id
    ON "event-management".events (tenant_id, device_token, alt_id, occurred_time)
    WHERE alt_id IS NOT NULL
    WITH (timescaledb.transaction_per_chunk);
  ```

  构建按分块逐个进行，因此写入只在其目标分块被索引时等待。被持有超过代理 60 秒确认等待的事件会被重新投递，这是安全的。在繁忙的实例上，请选择安静的时段。
- **要避免被拒绝，请在升级前构建索引。** 在未压缩基础事件超过 `4000000` 的 `v0.19.0` 实例上，先执行上面两条语句；`v0.19.0` 可以与之配合工作，升级随后只需删除旧索引。统计基础事件数量时，使用[检查行数](#v0190-row-count)下的查询，并将条件改为 `hypertable_name IN ('events')`。
- 回退到 `v0.19.0` 会保留新索引，`v0.19.0` 可以使用它：重新投递的事件仍会被去重，但该版本仍按租户检查，因此会再次丢弃第二台设备的事件。

#### 无法编码失败记录的入站事件，不再存成空记录 {#next-upgrade-failed-record-encode}

device-management 无法编码失败入站事件的记录时，此前仍存储空记录并确认事件。读回的失败没有原因和文本，真正失败没有记录在任何地方。现在不为该事件发布任何内容，确认事件，并在 `dead_letter_lost_total` 统计损失，因此 `DeadLetterWriteLost` 触发，Pod 日志的 `LOST` 行说明哪个事件。今天设备能够发送的事件都不会触发此路径；这是缺陷路径，告警让你知道此类缺陷。`DeadLetterWriteLost` 因而有第四个原因，列于[告警表](./detection-engine.md#what-to-watch)。

无需操作。

#### `dcctl install --dry-run` 按实际 install 拒绝重新安装 {#dcctl-install---dry-run-makes-the-re-install-refusals-the-install-makes}

集群响应时，dry run 现在读取安装记录，设置改变时查询集群上运行哪些实例；若因现有集群内容导致安装拒绝，则使用 install 自己的消息失败：在没有集群状态的机器重新安装、有实例运行时更改设置、或无法工作的 `--backup-snapshot-class`。此前打印计划并以 0 退出。面向尚不存在或无法连接的集群仍打印计划，并说明未执行这些检查。Dry run 不检查 install 拒绝的全部情况；例如关系存储由旧发布构建的集群，仍只在真正 install 时拒绝。

无需操作。

#### `dcctl install` 和 `dcctl bootstrap` 等待每个数据库实例加入 {#next-upgrade-wait-database-instances}

此前 `dcctl install` 在关系数据库主库接受连接后返回，`dcctl bootstrap` 只等待服务。因此任一命令都可能报告成功，bootstrap 甚至打印两个数据库已复制，而副本仍在创建。Install 现在记录自身、释放集群锁，再等待最多 15 分钟，直到关系数据库每个实例都加入。Bootstrap 先等待服务，再最多等待 15 分钟，直到实例事件存储每个实例都加入。如果仍有实例未及时加入，命令以错误退出，指出数据库和就绪实例数。重新运行同一命令可继续等待（install 会重新应用前提组件，执行期间拒绝 bootstrap；如果只想观察请用 `kubectl` 监视数据库）；这样结束的 bootstrap 不会撤销，install 已记录，所以期间仍能引导实例。健康安装额外耗时几秒。

无需其他操作。

#### 速率上限按其计量对象重新命名 {#next-upgrade-rate-key-rename}

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
