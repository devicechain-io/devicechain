---
sidebar_position: 1
title: 初始化实例
---

# 初始化实例 {#bootstrap-an-instance}

构建 DeviceChain 部署需要两个命令。`dcctl install` 一次性准备集群，随后 `dcctl bootstrap` 在其上启动完整实例，包括基础设施和全部服务工作负载；需要多少实例，就运行多少次：

```bash
dcctl install local
dcctl bootstrap local my-instance
```

`dcctl` 内嵌自己的内容，包括 OpenTofu 配置、Helm chart 和 Operator 清单，部署无需源码检出或 `git`。

它不包含工具本身。两个命令调用 `PATH` 中的 `docker`、`kubectl`、`helm`、`tofu`（或 `terraform`），`local` 还需要 `kind`。启动前检查全部工具，缺少时停止，因此应先安装，完整列表见[前置条件](#prerequisites)。

:::note 状态
DeviceChain 尚未正式发布。`dcctl install local` 和 `dcctl bootstrap local` 已实现，并在本地 kind Kubernetes 上端到端验证。不存在 kind 集群时，install 会创建，除非指定 `--yes` 否则先确认。`gcp` 提供商是计划中的后续工作。
:::

## 安装集群 {#install}

`dcctl install <provider>` 准备承载实例的集群，安装所有实例共享的前置组件。

在 `dc-k8s-system` 中安装 **DeviceChain Operator** 和它协调的 `Instance`、`InstanceConfiguration` 两个自定义资源定义。集群只有具备它们才能承载实例：bootstrap 声明 `Instance`，资源定义不存在时不能声明。参见 [Operator](./kubernetes-operator.md)。

在 `dc-system` 中安装：

- 关系数据库 `dc-rdb`，每实例一个数据库；
- 保存数据库备份归档的对象存储。

另外各自使用独立命名空间安装：

- CloudNativePG Operator（`cnpg-system`）；
- cert-manager（`cert-manager`）；
- 监控 Prometheus、Grafana（`monitoring`），见[可观测性](./observability.md)；
- ingress 控制器（`ingress-nginx`）。

每集群一个 Operator，由全部实例共享。因此由集群命令安装，而非各实例分别安装；集群升级到新版本也从此处开始，参见[发布与升级](./releases-and-upgrades.md#zero-downtime-upgrades)。

install 还创建基础数据库身份，用于创建各实例自己的数据库登录，并在集群记录安装结果。

目标集群选择：

- **`local`**：`--cluster <name>` 指定 kind 名，默认 `devicechain`。不存在则创建，未指定 `--yes` 时先确认；存在则复用。
- **任意提供商**：`--kube-context <ctx>` 使用已有集群。`dcctl` 从不创建或删除通过此方式访问的集群。

### 重新运行安装 {#re-running-install}

在同一集群重跑会收敛：已有内容保持，缺失内容补充，包括备份存储卷保留当前大小，见[备份存储大小](#backup-store-size)。

集群有任何实例时，**拒绝改变设置**，包括 `--ha`、`--compact`、监控、备份、`--backup-snapshot-class` 和数据库放置。各实例按初始化时设置建立，设置变化不会重建它们。异地归档也一样：`--backup-credentials-file` 改端点或事件库桶会被拒绝，因为各事件库继续使用创建时归档目标。

唯一例外是运行中可提高 `--max-connections`，见[连接预算](#connection-budget)。降低仍拒绝，未指定则保留现值。

**中途失败同样重跑修复。** 原因修复后运行相同命令。例如对象存储第一次无法拉镜像而启动失败，重跑会删除、重建并等就绪，与首次相同。原因仍在则同样失败。报告安装成功前还检查备份对象存储已完成发布，避免跳过早先失败变更留下的未就绪状态。

install 还最多等十五分钟，直到关系数据库所有实例加入；未加入时错误列出数据库和就绪数量。此时安装已记录、集群锁已释放，因此等待期间可以初始化实例。用 `kubectl` 跟踪数据库；重跑同命令也继续等待，但会重新应用前置组件，并在运行期间拒绝 bootstrap。

### 安装状态存放位置 {#install-state}

前置组件由 OpenTofu 应用，状态保存在运行安装的机器：`~/.devicechain/clusters/<cluster-uid>/infra`。

目录按集群身份，即 `kube-system` 命名空间 UID，而非名称索引。同名 kind 删除重建后，context 名相同，却没有旧状态描述的资源。旁边的 `cluster.json` 记录集群名和 context，可用于对应目录：

```bash
cat ~/.devicechain/clusters/*/cluster.json
kubectl --context <kube-context> get namespace kube-system -o jsonpath='{.metadata.uid}'
```

OpenTofu 提供商固定精确版本。每次运行包括 destroy 都将 `.terraform.lock.hcl` 更新到当前 dcctl 固定版本，因此需要访问提供商注册表或配置的镜像。

重跑依赖该状态，所以**只能从持有目录的机器重新安装已安装集群**。其他机器运行时，在应用任何内容前拒绝：

```text
cluster ... is already installed (by dcctl <version>, <time>), but this machine holds no state
for it under ~/.devicechain/clusters/<cluster-uid>. It was installed from another machine, and
re-applying from empty state would try to create every prerequisite again. Run `dcctl install`
from the machine that installed it
```

空状态会把全部前置组件当新资源，运行中因名字冲突失败，所以拒绝是安全结果。但目录也因此是本页每次重跑，包括提高连接预算的前提。

这是前置组件状态的唯一副本，DeviceChain 备份不包含它。应与安装机器一起保留。交给其他机器时，先复制整个 `~/.devicechain/clusters/<cluster-uid>/`。其中含集群根状态和凭据，按托管密钥目录同样保护。

### TLS、标志与笔记本安装 {#install-tls-and-flags}

install 的 `--no-tls` 仅与 `--compact` 合用时有效，省略 cert-manager；没有 compact 时拒绝。单实例使用明文 HTTP，应对 bootstrap 指定该标志。

完整标志见[安装标志](#install-flags)。笔记本上：

```bash
dcctl install local --dev
dcctl bootstrap local devicechain --dev
```

集群 install 未完成时，bootstrap 拒绝并提示应运行的安装命令，upgrade 同样拒绝。

### 移除集群 {#removing-a-cluster}

目前没有卸载前置组件的命令。[`dcctl destroy`](#destroy) 只移除一个实例，保留前置组件。移除 install 创建的本地集群，用 kind：

```bash
kind delete cluster --name devicechain
docker rm -f kind-registry   # the local image registry, if you used --build
```

kind 不知道本地状态目录，因此留下它。下次销毁该集群曾承载的实例时，发现集群已消失，会连同实例本地状态清目录，见[移除实例](#destroy)。也可确认 `cluster.json` 所属集群后手动移除。

保留的托管集群或非 dcctl 创建集群，在最后实例销毁后仍保留前置组件。不支持手动移除，因为含共享关系库及备份、可选集群内备份存储，以及销毁保留或未能清理的实例备份，见[实例备份处理](#destroy-backups)。它们存在期间须保留集群状态目录，因为后续 install 依赖它，见[安装状态](#install-state)。

### 连接预算 {#connection-budget}

关系数据库有固定连接数，由 `--max-connections` 设置，默认 `600`。各实例按启用领域，为自己的数据库登录预留连接上限。预留超过剩余量时，bootstrap 拒绝。

检查在**写入任何实例内容之前**执行，不创建命名空间、数据库或登录，因此被拒绝后无需清理。承载多个实例或大量领域的集群需更大预算。

每个服务保留池已打开连接，最多池大小，默认 20，可由 `maxOpenConnections` 改变；连接打开一小时后关闭。预留覆盖每领域一个满池 Pod，加发布期间一个额外 Pod，因此默认池都容纳得下。`replicas` 高于一或扩大池后，忙碌期可能持有更多，应核对实例连接上限。

预算是唯一可在运行实例下改变的 install 设置，且只能提高。重跑设置更大值会逐个重启数据库实例，不是无代价操作。没有 `--ha` 时只有一个数据库实例，因此重启期间**集群所有实例短暂失去数据库**，应在低峰执行。

`dcctl upgrade` 也受同一预算约束。新版本可能改变实例关系型领域的连接需求，因此升级会在写入任何内容之前，将新需求与实例登录的现有上限比较。需求不变时不重新准入；预算无法容纳增长时，升级拒绝执行且不改变任何内容：

```text
this release needs instance "my-instance"'s database login to hold <n> connections, up from <m>,
and nothing has been changed: ... Destroy an instance, or raise the budget by re-running
`dcctl install` with a larger --max-connections
```

解决办法如上，会重启数据库。预算接近耗尽的集群应在升级窗口**之前**的低峰提高预算，避免升级时才发现不足。

可容纳的增长在服务滚动更新前应用。新版本需要的连接**减少**时，只有所有服务都在新版本上就绪后才降低登录上限。降低失败不会使升级失败，因为服务已经运行；它会打印以 ``re-run `dcctl upgrade` to finish it`` 结尾的警告。完成前，登录占用的预算仍高于所需。`dcctl upgrade --dry-run` 报告将执行的检查，不登录存储。

## 初始化过程 {#what-it-does}

初始化按顺序构建实例。失败时会指出具体步骤。

这是创建操作。数据库密码、代理的认证机构和登录、跨服务密钥、密钥存储根密钥等所有凭据都在此生成，因为它们尚不存在。针对已运行实例执行时，会在第 3 步停止，不改动基础设施或 Helm chart，并指出用于更新运行实例的 `dcctl upgrade`，详见[版本与升级](./releases-and-upgrades.md#zero-downtime-upgrades)。

中途**失败**的初始化不同，修复方法仍是重跑。第 3 步只拒绝**运行中**实例：第 8 步写入的配置文档存在，且首次初始化已完成。第 5 步声明实例时，会记录首次初始化尚未完成，只有成功结束的运行才删除此记录。因此任何步骤失败，包括 Helm 安装或等待就绪，都留下一个未完成实例；在同一台机器上重跑相同的 `dcctl bootstrap` 命令即可完成。

由早期 `dcctl` 版本开始初始化的实例没有此记录。如果在第 8 步开始后失败，第 3 步无法将它与运行实例区分，会拒绝执行，并给出两种继续方式：用 `dcctl upgrade` 执行相同的 Helm 安装和就绪等待，或用 `dcctl destroy` 删除后重新初始化。

:::warning 一次性例外：数据库迁移至 CloudNativePG 之前创建的实例
关系型数据库已从 StatefulSet 改为 CloudNativePG 集群，无法原地升级。对更早创建的实例，初始化会**拒绝**执行，并说明如何导出数据或有意丢弃数据。详见[旧数据库例外](#legacy-db-removal)。
:::

### 旧数据库例外 {#legacy-db-removal}

Operator 无法接管 StatefulSet 的数据目录，因此不支持原地升级。拒绝执行可防止旧数据库被删除后，一个空数据库接管相同主机名，导致实例看似健康却没有数据。

这是文档中唯一允许对运行实例执行 `dcctl bootstrap` 的情况，因此 `--allow-legacy-db-removal` 同时豁免此处和第 3 步的拒绝检查。其他情况没有豁免。

标志按数据库归属拆分：关系型数据库属于集群，由 `dcctl install --allow-legacy-db-removal` 处理；事件存储属于实例，由 `dcctl bootstrap --allow-legacy-db-removal` 处理。

### 一个集群上的多个实例 {#several-instances}

同一集群可承载多个 DeviceChain 实例。每个实例的服务、代理（NATS）、事件存储（TimescaleDB）和凭据都在独立命名空间中。名称为实例名加 `dci-` 前缀，例如 `devicechain` 实例位于 `dci-devicechain`。

此前缀避免实例命名空间与集群使用的命名空间冲突。实例 ID 无法占用 `monitoring`、`cert-manager`、`ingress-nginx` 等名称。

每个实例使用独立登录连接共享关系型数据库，该登录仅拥有一个数据库，因此无法访问其他实例的数据。

共享的是集群前置组件：DeviceChain Operator 及其自定义资源定义、Ingress 控制器、cert-manager、CloudNativePG Operator、监控、关系型数据库和备份对象存储。[`dcctl install`](#install) 只安装一次。每次初始化都复用它们，并遵循集群安装时的高可用、紧凑配置、监控及备份设置。

集群中以下两项只能属于一个实例，初始化会处理冲突：

- **Ingress 主机名。** 两个实例使用同一主机名时，Ingress 控制器会静默地只服务一个。主机名已被其他实例使用时初始化拒绝执行。为每个实例设置独立 `--host`，本地集群例如 `--host beta.localhost`。
- **本地 MQTT 端口。** 本地集群中，机器的 1883 端口只连接第一个实例的代理。后续实例的代理可从集群内部访问，初始化会对此提示。

#### 实例命名空间 {#instance-namespace}

初始化同时检查命名空间。实例拥有 `dci-<id>`：dcctl 在其中写入根密钥、代理 TLS 密钥对和所有数据库凭据，`dcctl destroy` 会删除整个命名空间。因此，已存在却没有 `devicechain.io/instance=<id>` 标签的 `dci-<id>` 会在任何写入前被拒绝。

只有 dcctl 会创建 `dci-` 前缀的命名空间。常见原因是此前对同一实例执行的 `dcctl destroy` 尚未完成。拒绝消息会指出原因和完成删除的命令 `dcctl destroy <provider> <id>`。仍在删除中的命名空间也会被拒绝，直到删除完成。

如果命名空间是你有意创建的，用于自定义配额、策略或 RBAC，请将其交给实例，再重跑初始化：

```bash
kubectl label namespace dci-<id> devicechain.io/instance=<id>
```

#### 实例名称 {#instance-names}

实例名称只能包含小写字母、数字和 `-`，最多 50 个字符。名称直接用作实例数据库及该数据库的登录，也用于命名空间 `dci-<名称>` 和 Helm release `dc-<名称>`。Helm release 最长 53 个字符，扣除 `dc-` 后实例名称最多 50 个字符。

独立实例命名空间引入前构建的实例，其代理和事件存储位于共享的 `dc-system` 中，无法原地迁移。初始化会拒绝并提示先删除，再重新初始化。

### 初始化步骤 {#bootstrap-steps}

以下是运行时打印的步骤（如 `[8/10] Install instance (Helm)`），可根据错误中的步骤在此定位：

1. **准备本地镜像仓库**——仅开发者 `--build` 路径：创建本地镜像仓库，将所有镜像构建并推入。使用已发布镜像时此步骤不执行，并提示原因。它位于第一步，因为后续 chart 引用的镜像由此生成。
2. **取得集群锁**——创建 Operator 命名空间，在任何应用操作前取得**集群锁**。持锁期间，针对同一集群的第二个 `dcctl bootstrap` 会被拒绝。详见[集群锁](./cluster-lock.md)，包括集群已被其他人占用时的处理方法。
3. **拒绝重建运行实例**——查询实例是否已运行，是则停止。此检查在锁之后，避免并发初始化使读到的状态失效；在实例写入之前，避免覆盖正在运行的实例。试运行也会报告实际运行将拒绝的情况。配置文档存在表示实例运行中，除非声明记录首次初始化未完成，此时允许继续完成。无法读取声明时停止，不猜测状态。
4. **检查其他实例占用的资源**——查询其他实例的 Ingress 主机名和本地 MQTT 端口。主机名冲突时停止。同时检查 `dci-<id>`；已存在却不属于本实例或仍在删除中时停止。此步骤在任何写入之前执行，拒绝不会留下资源。本地 MQTT 端口已被占用只报告，不停止。试运行也报告实际运行将拒绝的情况。详见[一个集群上的多个实例](#several-instances)，包括如何通过标签交接自建命名空间。
5. **声明实例**——向集群写入实例**声明**：所属提供方与集群、配置档、镜像版本，以及数据库是否从归档恢复。随后读取声明，后续步骤使用读回的内容，而非生成它的标志。实例的真实记录位于集群，详见[实例声明](./kubernetes-operator.md#instance-declaration)。构建实例时（第 3 步未找到配置文档，或首次初始化尚未完成），声明还包含 `core.devicechain.io/bootstrap-unfinished: "true"` 注解。成功结束时，在记录 `Ready` 的同一次写入中删除注解。
6. **生成配置**——解析实例 ID、命名空间、配置档及所有凭据：代理认证材料（共享服务密码和 callout 签发者密钥）、签署代理 TLS 证书的证书机构、跨服务认证密钥及**密钥存储根密钥**。第 3 步已确认没有可读取凭据的运行实例，因此在此生成。完成半建实例时例外：读取此前运行已写入集群的值，不另行生成，包括已写入配置文档的实例。代理证书机构是每次运行都会重新签发的唯一内容。此步骤在配置代理前将代理凭据记录到本机；代理先于实例配置，且配置后凭据无法从集群恢复，本地记录使中断后能够重跑。根密钥还会托管至你保管的加密文件，详见[灾难恢复](./disaster-recovery.md)。
7. **应用基础设施**——通过 [terraform-exec](https://github.com/hashicorp/terraform-exec) 对嵌入的 OpenTofu 配置执行 `tofu apply`。仅处理本实例：命名空间中的 NATS 代理和 TimescaleDB 事件存储，状态保存在 `~/.devicechain/instances/<instance>/infra`；同时在共享关系型数据库上创建实例自己的登录和数据库。集群共享前置组件由 [`dcctl install`](#install) 安装，不在此重新应用。后续运行是增量操作。
8. **安装实例（Helm）**——写入实例**配置文档**，所有服务从中读取凭据及端点。再通过 Helm Go SDK 部署 chart，并等待工作负载就绪。后续运行的第 3 步检查此文档；运行还需成功结束，实例才算运行中。
9. **等待就绪**——轮询每个启用领域的 Deployment，直到全部滚动至本次生成的配置。这是 Helm 等待之外的显式确认。仅有可用副本不足，因为即将被替换的旧 Pod 也可能可用；还需新模板已被观察、全部副本按新模板重建，且没有旧副本仍在运行。`dcctl upgrade` 使用相同检查。随后最多等待 15 分钟，直到事件存储的所有实例加入，因为仅主实例就绪便足以让服务就绪。缺少实例时，错误指出数据库和就绪实例数；不会回滚，再运行相同 `dcctl bootstrap` 即可完成。
10. **报告访问信息**——打印命名空间、超级用户邮箱、密码保存位置和访问方式。密码本身只显示一次：在生成它的运行中，或完成此前尚未显示密码便失败的初始化时。

:::tip `Ctrl+C` 可正常停止运行
中断会让基础设施工具正常完成当前操作并写入状态，再释放集群锁，之后重跑即可。第二次 `Ctrl+C` 立即退出，放弃两者。详见[中断运行](./cluster-lock.md#interrupt)。

已到第 8 步时，重跑仍可完成，因为首次成功结束前第 3 步允许继续。但第 8 步期间的**第二次** `Ctrl+C` 可能使 Helm release 留在进行中状态。重跑会在第 8 步报错 `another operation (install/upgrade/rollback) is in progress`，dcctl 不自动清除此状态。用 `dcctl destroy` 删除，再重新初始化。
:::

嵌入的制品与平台正式发布的制品相同，因此初始化使用真实部署，不会与生产部署偏离。

### 默认备份目的地 {#default-backup-destination}

数据库备份需要目的地。默认是 `dcctl install` 安装在 `dc-system` 命名空间的单副本 **MinIO**。因此默认安装的实例确实归档预写日志，而不是安装了备份插件却没有可用存储。

:::info 默认备份目的地使用 AGPL 组件
MinIO 采用 AGPL-3.0 许可证。社区 MinIO 于 2026 年 4 月归档，不再发布官方镜像，因此 DeviceChain 使用持续维护分支的构建（`cgr.dev/chainguard/minio`），以摘要固定版本，由节点从 `cgr.dev` 拉取。只有 DeviceChain 发布更新摘要时，修补版本才进入集群。要避免该许可证及依赖，请使用集群外部存储。
:::

这些情况不影响 DeviceChain 自身的 Apache-2.0 许可证。平台只引用镜像，不构建、修改或再分发，通过 S3 HTTP API 访问。但组件确实运行在**你的**集群中，许多组织无论用途如何都不允许 AGPL 软件。

生产环境建议使用集群外部存储，还因为集群内存储桶与集群共享故障域，无法承担灾难恢复。向 `dcctl install` 传入 `--backup-credentials-file`，指定已有对象存储。详见[灾难恢复](./disaster-recovery.md)和 OpenTofu 配置的 `backup_destination`。

#### 恢复窗口 {#backup-retention}

每个数据库独立保留恢复窗口，即可恢复到其中任意时间点的范围。保存租户、用户、设备、规则、密钥及设备最后已知状态的关系型数据库保留 **30 天**，由集群 OpenTofu 配置中的 `backup_retention_rdb` 设置。每个实例的事件存储保留 **7 天**；在实例 OpenTofu 状态目录（`~/.devicechain/instances/<instance>/infra/instance/`）旁的 `terraform.tfvars` 中设置 `backup_retention_tsdb`。所有实例 `dcctl` 应用操作，包括升级，都会读取它。关系型数据库窗口较长，因为缺少它就无法重建实例，错误迁移或误删除往往数天后才被发现。事件历史量大，有独立的[数据生命周期](../concepts/architecture.md)，其日志会占用备份存储。

窗口格式为整数加单位：`d` 表示天，`w` 表示周，`m` 表示**月**而非分钟。其他格式在应用之前被拒绝。空窗口保留所有备份。扩大窗口需要更多备份容量，见下文。

#### 备份存储容量 {#backup-store-size}

默认存储为 160 GiB，按默认事件存储大小配置，使持续摄取时事件存储卷先填满。实例空闲时归档日志几乎不占空间；持续摄取时日志随写入率增长。Google Kubernetes Engine 测量中，两数据库合计每个摄取事件最多约 1.9 KB，而存储数据约为每事件 1 KB。（本页之前给出的归档约 1 KB 低估了实际值。）160 GiB 可容纳满 32 GiB 事件存储的归档，加上每个数据库一份完整基础备份，仍有超过三分之一空闲。这样备份告警保持安静，由事件存储告警指出容量根因。

此计算适用于约一天内填满事件存储的单个实例。以下情况不适用：

- **多个实例持续摄取。** 备份存储属于集群，每个实例都增加归档。每个此类实例约需额外 160 GiB，或通过 `--backup-credentials-file` 使用自行管理的对象存储，这也是推荐生产配置。
- **事件存储填满需要超过约一天。** 存储保留每个数据库[恢复窗口](#backup-retention)中每天的一份完整基础备份，以及此后的日志。默认窗口下为关系型数据库 32 份压缩副本、事件存储 9 份压缩副本，并需与日志共同放入 160 GiB。关系型数据库通常仅为兆字节规模，因此实践中事件存储压缩后须远小于约 17 GiB。
- **实例以保留窗口（`retentionDays`）限制数据量。** 事件存储不会填满，但仍归档一周日志；关系型数据库归档 30 天日志，设备最后已知状态更新使其随摄取增长。一次小型高频上报设备群测量中，关系型数据库备份约占存储的 14%。按此比例，持续约每秒 100 个事件就会填满默认存储，尚未计入基础备份。大型或低频设备群未测量，其占比可能更高；若全部都是关系型日志，则约每秒 35 个事件便会填满。
- **扩大事件存储。** 事件存储卷每增加 1 GiB，此处约需增加 5 GiB。

此时[备份归档停止](./observability.md#backup-archiving)中的告警提供预警：占用 85% 时触发 `BackupDestinationAlmostFull`，快速增长触发 `BackupDestinationFillingFast`，归档停止后触发 `PostgresWALArchivingFailing`。基础备份一次性写入，因此超过 85% 的存储可能被下次夜间备份填满，应在首个告警时处理。存储满后，集群所有实例停止归档，各数据库在自身卷上保留未发送日志，直到卷也填满、数据库停止。

每个数据库以 zstd 压缩归档日志。上述 1.9 KB、每秒 100 和 35 个事件来自本版本为事件日志启用压缩、时间优先键和 zstd 归档之前，当时归档使用 gzip，因此估计偏大。本版本基准中，事件存储归档约为每个已存储事件 0.47 KB。关系型数据库占比未重新测量。同一日志上，zstd 输出比 gzip 小 2% 至 16%，不会使归档变大。空闲数据库提前关闭的段约 16 KiB，仍使用 gzip 的数据库约 32 KiB。

容量在 `dcctl install` 首次创建存储时确定。重跑安装（包括升级第一步）和直接 `tofu apply` 都保留当前容量。如果 StorageClass 支持卷扩容，可执行：

```bash
kubectl -n dc-system patch pvc dc-object-store-data \
  -p '{"spec":{"resources":{"requests":{"storage":"320Gi"}}}}'
```

kind 不强制卷大小限制，可使用宿主机可用磁盘。

缩短恢复窗口（事件存储的 `backup_retention_tsdb`、关系型数据库的 `backup_retention_rdb`）不能解决摄取压力：历史虽少，仍保留至少一整天日志，并以牺牲恢复范围换取空间。[备份归档停止](./observability.md#backup-archiving)中的告警会在存储或数据库卷填满之前提醒。

#### 卷快照基础备份 {#snapshot-base-backups}

存储驱动支持 CSI 卷快照的集群可用 `dcctl install --backup-snapshot-class <class>`，将每日数据库基础备份改为磁盘卷快照，而非备份存储中的完整副本。不传标志时行为不变。

- **不变部分。** 数据库仍持续向备份存储归档预写日志。每周日 04:00 仍向存储写入完整基础备份，因为存储只能依据其中的基础备份清理旧日志；缺少它会保留所有段直至填满，而且所有恢复都读取备份存储。
- **快照类。** 类必须已存在，设置 `deletionPolicy: Delete`，并属于数据库卷使用的存储驱动，通常为默认 StorageClass 的驱动。`dcctl install` 在任何改动之前检查三项；每次 `dcctl bootstrap` 也检查，及时发现安装后被删除的类。集群需 CSI 快照控制器：Google Kubernetes Engine 和 Azure AKS 的磁盘驱动包含它；Amazon EKS 需先安装快照控制器插件。
- **保留。** CloudNativePG 不删除旧快照；DeviceChain Operator 每十分钟执行清理，保留数据库[恢复窗口](#backup-retention)内所有快照及窗口之前最新的一份，删除其他快照，同时删除提供方副本。停止清理会触发 `DatabaseSnapshotPruningStalled`。每次检查时间记录在计划的 `devicechain.io/snapshot-retention-checked-at` 注解中。
- **恢复及删除限制。** 恢复不读取快照。`--restore-rdb-from`、`--restore-tsdb-from` 读取备份存储中最新每周基础备份及后续日志，可能重放最多一周日志。销毁实例时删除其命名空间及快照；删除 `dc-system` 时删除关系型数据库快照。云提供方快照可比集群存活更久，提供方副本删除可能在命名空间消失后才完成。如果此前删除集群，快照会留在提供方，包含数据库内容及租户删除已移除的数据，直到你手动删除；之后没有组件清理它们。删除集群前，检查 `kubectl get volumesnapshotcontent`，确认 `VOLUMESNAPSHOTNAMESPACE` 列没有 `dc-system` 或实例命名空间；删除后检查提供方快照列表。[Google Kubernetes Engine 指南](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#tearing-it-down)给出 GKE 命令。
- **备份存储影响。** 每个数据库最多额外保留一周日志，即恢复窗口之前最新每周基础备份开始的日志。完整副本减少，但以日志为主时会更早填满。按默认窗口、容量及上述关系型占比，持续约每秒 60 个事件填满存储，而非 100；若全部为关系型日志，约每秒 29 个事件。前述告警仍适用。默认容量不变：[上述](#backup-store-size)计算包含每数据库一份完整基础备份及满事件存储对应日志，不受基础备份计划影响。数据库创建时及每周仍有完整基础备份进入存储，所以两种方式均为 160 GiB。
- **集群级设置。** 所有实例遵循此设置。与其他[安装设置](#re-running-install)相同，有实例运行时拒绝改变。

快照告警详见[备份归档停止](./observability.md#backup-archiving)。

## 前置条件 {#prerequisites}

- **Kubernetes 1.29 或更新版本**及指向它的 kube-context。最低版本来自 CloudNativePG chart，更早版本拒绝安装。`dcctl preflight` 提前检查，避免写入根密钥托管文件后才在初始化中失败。`local` 提供方使用 kind 集群，由 `dcctl install local` 创建（`--cluster <name>`，默认 `devicechain`）。传 `--kube-context <name>` 可使用已有 kind、minikube、k3d 或 docker-desktop 集群。
- **默认 StorageClass 上的持久卷磁盘。** 非本地集群（非 kind、minikube、k3d、docker-desktop、rancher-desktop）使用默认设置，没有 `--compact`、`--no-cnpg`、`--no-monitoring`、`--backup-credentials-file` 时，`dcctl install --ha` 为集群申请 204 GiB，包括三个关系型数据库卷、160 GiB [备份存储](#backup-store-size)和 Prometheus。每实例再申请 144 GiB，用于三个事件存储卷及三个代理卷；单实例集群合计 348 GiB。无 `--ha` 时集群 188 GiB，每实例 48 GiB。每个额外持续摄取实例还需增加约 160 GiB 备份容量，详见[备份存储容量](#backup-store-size)。云环境先核对磁盘配额。新的 Google Cloud 项目每区域 SSD 默认 500 GB 配额，每 GiB 卷按一 GB 计，两种 GKE 磁盘类均计入。单实例卷容量低于配额；[GKE 指南](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#before-you-start)使用标准启动盘，计入不同配额，因此默认高可用单实例也适用。平衡型或 SSD 启动盘则计入 SSD 配额。指南说明多实例需申请的配额。本地集群监控不使用持久卷，kind 不强制容量。
- **满足请求的 CPU 和内存。** 非 `--compact` 时，处理每个事件的五个服务合计请求约 4 CPU；`--ha` 下 `event-management` 两个 Pod，合计约 5 CPU。每个 NATS 服务器请求 500m CPU 和 768Mi 内存，高可用下有三个，另需其他服务、数据库和集群组件资源。无法容纳的 Pod 保持 `Pending`，安装等待至超时。各项见[服务容量配置](#service-sizing)；笔记本等资源有限集群使用 [`--compact`](#--compact)。
- **OpenTofu 1.9 或更新版本**（`tofu`，也支持 `terraform` 1.9 或更新）位于 `PATH`。`dcctl` 使用它部署基础设施，安装见 [opentofu.org](https://opentofu.org)。`dcctl preflight local` 提前检查工具及环境，但不检查其版本；旧版本加载配置时失败。
- **`docker`、`kubectl`、`helm`** 位于 `PATH`；`local` 提供方还需 **`kind`**。缺少工具会使预检失败。Docker 应为原生引擎，而非 Docker Desktop，且守护进程可访问。
- **`ko`** 仅 `--build` 源码构建需要，缺少时预检警告而非失败。

## 镜像来源 {#image-source}

默认从 `ghcr.io/devicechain-io` 部署**已发布镜像**，无需构建：

```bash
dcctl bootstrap local my-instance
```

源码检出可使用 `--build` 构建后部署：通过 [`ko`](https://ko.build) 构建每个服务与 Operator，通过 `docker build` 构建 Web 控制台，推入本地仓库后按引用部署：

```bash
# from a source checkout; requires Docker + ko
dcctl bootstrap local my-instance --build
```

两种路径仅镜像拉取仓库不同，流水线、chart 和 Operator 完全相同。

## 初始化标志 {#useful-flags}

| 标志 | 用途 |
|------|---------|
| `--cluster <name>` | `local` 提供方：创建实例的 kind 集群，默认 `devicechain`。必须已[安装](#install)，初始化不创建集群。 |
| `--kube-context <name>` | 通过此 kube-context 选择已安装集群。 |
| `--profile <profile>` | 功能领域配置档：默认 `default` 标准系统；`full` 包含全部，增加 AI 推理、出站连接器、MCP、Sparkplug B 和 LwM2M 摄取以及更新管理；还可选 `telemetry` 或 `ingest-only`。 |
| `--build` | 将源码镜像构建至本地仓库，开发路径需源码、Docker、ko。 |
| `--registry` / `--version` | 覆盖镜像仓库或标签，默认已发布 `ghcr.io/devicechain-io`；`--build` 时为 `localhost:5000` 和 `dev`。 |
| `--host <name>` | 暴露实例的 Ingress 主机名，默认 `devicechain.local`。本地使用 `localhost` 无需修改 `/etc/hosts`。 |
| `--no-tls` | 使用纯 HTTP 而非自签名证书。配合 `--host localhost` 可免配置访问 `http://localhost/`，无证书警告。未安装 cert-manager 的集群默认启用，拒绝 `--no-tls=false`，因为无法签发证书。 |
| `--dry-run` | 打印各步骤行为，不改变内容、不取得集群锁，但仍报告其他操作者是否持锁。 |
| `--skip-preflight` | 跳过环境检查。 |
| `--escrow-passphrase-file <path>` | 从文件读取根密钥托管口令，不交互提示，见下文。 |
| `--escrow-file <path>` | 将托管文件写至 `~/.devicechain/escrow/` 之外。 |
| `--no-escrow` | **不**托管根密钥，仅用于可丢弃实例；`--dev` 隐含此设置。之后仍可增加托管，见[托管协调](./disaster-recovery.md#escrow-reconcile)。 |
| `--restore-root-key <path>` | 灾难恢复：从托管文件恢复根密钥而非生成。仅在该密钥有可解密内容时接受，即关系型存储已有此实例数据库（见[恢复实例](./disaster-recovery.md#recover)）或正在完成此前半建实例。`dcctl destroy` 后两者均不成立，因此**拒绝**此标志：同名新实例生成自己的密钥，应不带标志初始化，并先移开旧托管文件，因为初始化不覆盖它。`dcctl secrets escrow show <path>` 显示文件所属实例。 |

### 根密钥托管 {#escrow}

初始化将实例**密钥存储根密钥**的加密副本写入 `~/.devicechain/escrow/<instance>-rootkey.escrow`，使用你选定的口令封存。口令通过交互提示、`--escrow-passphrase-file` 或 `DCCTL_ESCROW_PASSPHRASE` 提供。

默认启用托管。非交互运行未提供口令时**失败**，不会无托管继续：

```bash
# automation
DCCTL_ESCROW_PASSPHRASE="$(pass show devicechain/prod-escrow)" \
  dcctl bootstrap local prod --yes

# a throwaway instance
dcctl bootstrap local scratch --dev
```

:::danger 需要保留的实例必须保存此文件
根密钥加密实例保存的所有秘密，仅存在于集群 etcd，且 **DeviceChain 备份不包含 etcd**。没有此文件，将数据库备份恢复到新集群后，恢复的秘密将无法解密。请提前阅读[灾难恢复](./disaster-recovery.md)。
:::

保存秘密的领域无法解密凭据时拒绝启动。用户管理服务也在其中：它封存令牌签名密钥，因此所有用户无法登录。问题会立即显现，但届时已无法补救。[灾难恢复](./disaster-recovery.md)说明完整流程。

使用 `--no-escrow` 或 `--dev` 创建的无托管实例可之后补充，无需重建。向 `dcctl upgrade` 提供口令会写入缺失文件，每次升级也检查已有文件。见[托管协调](./disaster-recovery.md#escrow-reconcile)。

## 安装标志 {#install-flags}

以下属于 [`dcctl install`](#install)，描述集群设置，所有初始化实例均遵循。它们不是 `dcctl bootstrap` 标志。

| 标志 | 用途 |
|------|---------|
| `--cluster <name>` | `local` 提供方的 kind 集群，默认 `devicechain`，不存在时创建。 |
| `--kube-context <name>` | 安装至此 kube-context 指向的已有集群，`dcctl` 不创建或删除它。 |
| `--compact` | 小资源配置，见下文。 |
| `--ha` | 高可用，至少需 **3 个可调度节点**，见下文。 |
| `--no-tls` | 配合 `--compact` 不安装 cert-manager，也无数据库备份；`--compact --no-tls=false` 保留两者。不带 `--compact` 时拒绝；实例纯 HTTP 用 `dcctl bootstrap --no-tls`。 |
| `--no-monitoring` | 不安装 Prometheus 和 Grafana 监控栈。 |
| `--no-cnpg` | 不安装 CloudNativePG Operator 和数据库备份插件。用于**已有 CloudNativePG** 的集群，因为 Helm 无法接管其他安装器创建的对象，不传时安装失败。 |
| `--backup-credentials-file <path>` | 用 JSON 文件描述已有对象存储，替代集群内存储，见[灾难恢复](./disaster-recovery.md)。 |
| `--backup-snapshot-class <class>` | 使用此 VolumeSnapshotClass 将每日基础备份改为 CSI 卷快照，每周仍写完整副本，恢复仍读取备份存储。类需已存在，设置 `deletionPolicy: Delete`，属于数据库卷驱动。`--no-cnpg` 或 `--compact --no-tls` 不提供备份，因此拒绝此标志。见[卷快照基础备份](#snapshot-base-backups)。 |
| `--database-node-selector <key>=<value>` | 数据库仅运行在有此标签的节点，可重复。适用于共享关系型存储和所有实例事件存储。见[数据库放置](#database-placement)。 |
| `--database-toleration <key>[=<value>][:<effect>]` | 允许数据库运行在具有此污点的节点，格式与 `kubectl taint` 相同，可重复。需 `--database-node-selector`。 |
| `--restore-rdb-from <archive>` | 灾难恢复：从备份桶内归档路径恢复共享关系型存储（未恢复过时为 `dc-rdb`），不初始化空库。仅**创建**时生效，已有存储时不移动数据，是重建而非修复操作。需备份插件，`--no-cnpg` 或 `--compact --no-tls` 时拒绝。见[恢复实例](./disaster-recovery.md#recover)。 |
| `--restore-rdb-at <timestamp>` | 在指定时间停止恢复，而非重放全部归档。用于错误迁移或误删除等被系统正常执行的数据破坏，必须选破坏之前的时间。需 `--restore-rdb-from` 和带显式偏移的 RFC 3339 时间（`2026-07-27T13:59:00Z`），否则 PostgreSQL 按恢复服务器时区解释，停止时间不同。 |
| `--max-connections <n>` | 关系型数据库连接预算，首次默认 `600`，重跑未指定时保留当前值。见[连接预算](#connection-budget)。运行实例存在时只能提高，不可降低。 |
| `--allow-legacy-db-removal` | [初始化过程](#what-it-does)所述一次性例外的关系型数据库部分。 |
| `--dry-run` | 打印行为，不改变内容、不创建集群。因此需读取集群的检查，尤其 `--ha` 节点容量检查，无法读取时报告未检查，不阻止演练。但可读集群若无法承载高可用，试运行也失败。可访问时，同样拒绝缺少集群本地状态的机器重安装、在运行实例下改变设置、不可用快照类，并使用相同错误消息；不可访问时说明未检查。 |
| `--yes` | 创建 kind 集群前不询问。 |
| `--skip-preflight` | 跳过环境检查。 |
| `--dev` | 笔记本本地便利选项，隐含 `--build --yes`。 |

### `--compact` {#--compact}

`--compact` 是安装时选择的小型集群预设。它组合现有调节选项，而不新增独立调优维度：

- 降低 JetStream 和 KV 各 stream 的容量上限，并相应缩小卷（JetStream 3Gi、关系 Postgres 2Gi、TimescaleDB 4Gi；保留 TLS 时备份存储为 20Gi）；
- 降低每个服务和 NATS 服务器的调度 **requests**（25m / 64Mi），使 Pod 能放入小节点。Limits 不变：降低内存 limit 会将压力变为 OOMKill，降低 CPU limit 会造成节流，两者都不会减少实际工作。`device-management`、`event-management`、`event-sources` 和 `device-state` 保留较高 CPU limit；默认安装的各服务独立 requests 被关闭，让所有服务采用较低 requests，参见[服务容量配置](#service-sizing)；
- 不安装占用最大的监控栈；
- 不安装 cert-manager，因为关闭 TLS 后不需要签发证书（保留 TLS 则保留 cert-manager，见下文），因此也不安装数据库备份插件。

它**不改变**运行哪些服务，仍由每个实例明确可见的 `--profile` 决定。Compact 集群拒绝比 `default` *更大*的配置，目前只有 `full`。公布的 compact 数据在 `default` 上测得，不能代表多运行六个服务（AI inference、出站连接器、MCP、Sparkplug B ingest、LwM2M ingest 和 update management）的实例。较小的 `telemetry` 和 `ingest-only` 被接受。

可以保留 TLS 和监控。`dcctl install` 显式传入 `--no-tls=false` 或 `--no-monitoring=false` 会被遵守，其他 compact 调节仍应用。保留 TLS 也保留负责签发证书的 cert-manager。没有安装 cert-manager 的集群，所有实例都不使用 TLS：`dcctl bootstrap` 默认启用 `--no-tls`，并拒绝 `--no-tls=false`。

:::note 为什么 `--compact --no-tls` 会移除备份插件
Barman Cloud 插件通过 cert-manager 签发自己的证书，因此移除 cert-manager 也移除插件。重新启用 TLS（`--no-tls=false`）会恢复两者。这需要**两个**安装标志：不带 `--compact` 的 `dcctl install --no-tls` 被拒绝；`dcctl bootstrap` 的 `--no-tls`，如下面的本地 URL 示例，只改变单个实例的访问方式。
:::

CloudNativePG Operator 本身在**每个**集群中安装，包括 compact：一个请求 100m/128Mi 的 Deployment，加其 CRD。这是 compact 无法避免的资源成本，是有意设计。备份不是高可用功能，因此所有部署的存储层采用相同结构。关系存储和事件存储两个数据库都由 Operator 管理。

:::caution 卷大小限制可运行时间，并不限制数据容量
JetStream 卷大小由各 stream 预先保留的容量上限之和推导。两个数据库卷则不是。没有机制清理命令或告警表，`retentionDays` 默认为 `0`，即永远保留数据。需要长期运行的 compact 实例应设置保留窗口，不能依赖卷大小限制增长。
:::

:::caution 在创建第一个实例之前选择
将容量上限降低到 stream 或 KV 桶已有数据量以下，会静默成功，不截断任何数据，并拒绝写入，直到数据过期。因此，只要集群中存在实例，`dcctl install` 就拒绝修改这些设置：`--compact` 必须在任何实例运行前一次确定。
:::

:::tip 无须额外配置的本地 URL
`dcctl bootstrap local my-instance --build --host localhost --no-tls` 将控制台开放在 `http://localhost/`，无须修改 hosts 文件，也不会出现证书警告。
:::

### `--ha` {#ha}

`--ha` 在安装时选择，集群中每个实例都遵循。每个实例的消息代理运行为三节点 RAFT 集群，每节点一台服务器，**每个 JetStream stream 和 KV 桶都在其中复制**。实例能够在一个节点丢失后保留消息、设备会话和实时状态。

```bash
dcctl install local --ha
dcctl bootstrap local my-instance
```

一个设置同时控制两部分，这是它的目的。消息代理规模属于基础设施（OpenTofu），各 stream 副本因子属于实例配置（Helm）。两种工具无法看到对方设置。只提高前者，就是此标志要避免的错误：三节点集群的所有 stream 仍为单副本，消耗三倍计算资源、报告三个健康 peer，却无法抵御节点丢失。

:::caution 只能容忍一个节点丢失
三台服务器需要多数提交，两台仍构成 quorum，一台不行。第二个节点丢失，包括另一个节点已停机时进行滚动节点升级，会停止写入，直到节点返回。维护时逐节点进行。容忍两个同时丢失需要五服务器集群，目前不支持该拓扑。
:::

**需要三个可调度节点，而不只是三个节点。** 服务器使用硬性 anti-affinity。如果不能每节点部署一个，多余 Pod 保持 `Pending`，而不共用节点；共置副本只增加成本，无法提供节点故障保护。`dcctl` 会统计可调度节点，在创建资源前拒绝不足的集群。本地 `kind` 需要三个 worker：kind 只在单节点集群移除控制平面 taint，因此一个控制平面加两个 worker 虽有三个节点，实际只有两个可用。

**`--ha` 还将 `event-management` 运行为两个 Pod。** 它负责存储每个事件。三节点服务池中，一个节点还运行入站事件 stream 的 NATS leader，其 CPU 消耗高于任何服务。测试时，scheduler 将唯一的 `event-management` Pod 与 `device-state` 放在该节点，CPU 达 94–95%；输入每秒 6,800 个事件时，单 Pod 在三分钟内存储速率降至每秒 6,592，成为首个落后阶段。第二个 Pod 让存储使用另一节点 CPU。两者偏好不同节点，但不保证。每个 Pod 独立填充批次，因此批次事件约减半，事件存储每事件事务数约翻倍；事件存储节点 CPU 保持低于 70%。[实测吞吐](#measured-throughput)最后一行的每秒 6,000 个事件使用两个 Pod 测得。两个 Pod 合计请求 1.8 核 CPU，为单 Pod 两倍。每个 Pod 也有自己的事件存储连接，因此这些实例为平台保留 80 个连接，而非 40（参见[连接上限](../guides/sql-and-bi-access.md#connection-cap)）。`--compact --ha` 和不带 `--ha` 时，`event-management` 仍为一个 Pod。

#### `--ha` 下的数据库 {#ha-databases}

`--ha` 还将关系数据库运行为三个同步复制实例，客户端仍使用 `dc-postgresql` 主机名。Operator 维护该主机名，并在故障切换后指向新主节点，无须修改服务配置。

同步复制要求**三个**实例，而不是两个。每次提交需一个 standby 确认，仅两个实例时，任意一个丢失就会阻塞全部写入：以更好持久性换来比单节点更差的可用性。第三个实例让一个 standby 丢失后仍有确认副本。

事件存储同样复制到三个实例，但有意采用不同策略：**不会**在没有 standby 时一直阻塞写入。它退回异步复制，standby 返回后追赶。这个取舍适合事件存储，却不适合关系存储。事件在持久化前已由上游消息层可靠保存，因此故障切换损失的写入可重放；关系存储的审计日志没有这样的上游，所以选择阻塞。代价是事件存储恢复点受复制延迟限制，而非零数据损失。

`--ha` 只修改一个服务的副本数，即 `event-management`（两个 Pod，见上文）。其他服务仍为一个副本，单独并不能在节点丢失时继续运行。复制使恢复成为可能，但不负责执行恢复。

:::caution 阻塞的写入已经提交，并非被拒绝
这适用于会阻塞的关系存储。没有 standby 时，写入不失败，而是等待；数据行已在本地提交。客户端放弃并重试，若操作不幂等就会写入两次。`statement_timeout` **不能**限制此等待，因为它发生在提交之后，而非语句执行期间。
:::

#### 数据库主节点停止时 {#ha-database-failover}

删除 Pod、drain 节点或滚动更新配置时，数据库实例会停止。它先写 checkpoint，再停止接受新连接，给已连接客户端五秒离开。平台服务在运行期间一直保持数据库连接，等待更久只会延迟后续操作。五秒后，实例终止所有连接并关闭。正在执行的写入失败，由服务重试。

`--ha` 下，旧主节点停止后提升 standby，`dc-postgresql` 或 `dc-timescaledb-single` 指向它。测试中，新主节点在旧节点结束连接后 20–25 秒接受写入，即删除 Pod 后约半分钟。采用这些设置的数据库，配置滚动更新不再原地重启主节点：先重启 standby，将主角色切换到已更新 standby，再让旧主节点作为 standby 重启。测试中 switchover 约中断写入十秒。旧事件存储仍保留原地重启，直到 `dcctl upgrade` 应用实例基础设施（参见[升级应用哪些基础设施变更](./releases-and-upgrades.md#upgrade-infrastructure)），或按[发布说明](./releases-and-upgrades.md#database-primary-failover-in-seconds)进行 patch。

单实例没有 standby 可提升，数据库在实例重启完成前不可用，写入等待。小数据库测试中，停止写入后约 15 秒恢复；需要重放更多预写日志时更久。

两种情况下，事件都由消息层保留到存储成功。每个事件最多投递五次，间隔一分钟，之后放弃并[记录为未投递](./observability.md#max-delivery-records)。因此，短于约四分钟的数据库中断不会留下未投递事件。

停止实例总共最多允许两分钟，之后强制停止并移除 Pod。最可能的原因是仍在向不可达备份存储复制最后的预写日志。已提交数据保留，但备份归档可能有缺口：时间点恢复可能无法到达缺口内时刻，且 `PostgresWALArchivingFailing` 已在触发。下一次基础备份之后的恢复点不受影响。主节点停止时正在运行的基础备份会被放弃，下次计划仍正常执行。

两分钟上限也覆盖数据库 Operator 的已知问题：实例正常关闭 PostgreSQL 后可能无法退出，日志以 `failed waiting for all runnables to end within grace period of 30s` 结束，Pod 保持 `Terminating`，但数据库已停止。数据没有问题，满两分钟后 Pod 被移除。尚未应用此上限的 Pod（设置引入前创建，或更早事件存储尚未通过升级或发布说明的 patch 更新）仍带三十分钟上限。[发布说明](./releases-and-upgrades.md#database-primary-failover-in-seconds)说明如何识别并处理。

`--ha` 下，被降级的主节点（因 switchover 或故障）若两分钟内未关闭，也会突然停止。关系存储不会丢失数据，因为每次提交都等 standby 收到；事件存储在没有 standby 时不等待，因此该期间的提交仅存在于主节点，若 standby 追赶前替换主节点就会丢失。这就是上述恢复点取舍，突然停止是另一种触发方式。

#### 验证 {#verifying-it}

高可用是否成立取决于消息代理实际状态，应直接检查，而非只看渲染配置：

```bash
dcctl ha verify --instance my-instance
```

该命令读取实际消息代理，检查每个 stream、KV 桶和 durable consumer 都具有声明的副本因子，**所有 peer 都已追赶**，且三台服务器位于不同节点。任何条件不足都会非零退出，并打印检查对象，避免将空集合检查误认为有效通过。

#### 节点丢失和重新加入 {#ha-node-loss}

`--ha` 容忍一个节点丢失。外部可按以下顺序观察到：

- **消息代理数秒内选举新 leader。** 丢失节点领导的 stream 选出新 leader。测试中，消息代理连接位于幸存服务器的服务约十秒内恢复确认写入。正在进行的发布失败，HTTP 设备可能看到 `503`，应重试。没有 `Retry-After` 的 `503` 表示发布失败，但事件可能已存储；重试会存两次，除非携带 `altId` 和 `occurredTime`（参见[服务质量](../guides/connecting-a-device.md#quality-of-service)）。通过消息代理 service 的新连接可能间歇失败约 45 秒，直到服务器恢复，或 Kubernetes 判定节点丢失并停止路由到它。
- **连接到丢失服务器的服务约一分钟内重连。** 机器突然停止不会关闭连接，服务只有在服务器不响应后才发现。每十秒 ping 一次，连续三个间隔无响应（30 秒）后放弃；或一次写入十秒无进展后放弃，两者使死亡连接在 40 秒内被发现。随后重连耗时数秒；若 service 仍将部分连接路由到丢失服务器，则更久。期间服务既不能发布也不能接收。若为 `event-sources`，HTTP 接入被拒绝或超时，因此设备应重试 `503` 和超时。超时请求也可能已存储，同样应使用 `altId` 和 `occurredTime`。
- **Kubernetes 可能没有任何报告。** 机器在节点宽限期内（约 40–50 秒）重启，永远不会标记 `NotReady`，没有节点事件或 Kubernetes 告警记录丢失。安装 chart 告警后，`BrokerConnectionDiedSilently` 会报告；参见[静默死亡的消息代理连接](./observability.md#broker-connection-dead)。
- **事件处理可能暂停约一分钟。** 丢失服务器领导入站事件 stream 时，设备事件仍被接受，但解析可能停滞约一分钟，无错误提示，之后恢复处理积压。不会丢失数据，该分钟告警和存储事件延迟到达。
- **服务 Pod 约一分十五秒后迁移。** Kubernetes 先用约 40–50 秒判断节点丢失，然后各服务 Pod 等待 `nodeLossTolerationSeconds`（默认 30；`null` 恢复 Kubernetes 默认 300）后被驱逐并在其他节点启动。默认单副本时，位于丢失节点的服务此前不可用。`--ha` 的 `event-management` 有两个 Pod；若位于不同节点（scheduler 偏好但不保证），另一 Pod 持续存储事件。数据库实例和 Operator 同样为 30 秒。消息代理服务器不同，因为 Kubernetes 无法确认旧服务器停止时，不会在另一节点重建它，缩短等待没有收益。
- **丢失节点上的数据库主节点会切换。** Operator 发现主节点不可达后提升 standby，`dc-postgresql` 或 `dc-timescaledb-single` 指向它。比[主动停止主节点](#ha-database-failover)更久，因为没有明确停止通知。测试中，Operator 位于幸存节点时，新主节点在节点丢失约两分钟后可写，期间事件在消息层等待。
- **丢失节点上已驱逐的 Pod 保持 `Terminating`，直到节点返回。** Kubernetes 无法确认其停止，因此保留。不要强制删除不可达节点的 Pod：数据库或消息代理可能仍在故障另一侧运行，强制删除会让替代实例同时启动。如果机器永久消失，先确认已断电，再删除 Node 对象，Kubernetes 随后移除其 Pod。数据库节点返回后作为 standby 加入。
- **节点返回本身也有短暂扰动。** 被隔离的消息代理服务器一直自行选举，重新加入时，其他服务器 stream 和 consumer 再次选举 leader。节点返回约 45 秒后，JetStream 可能数秒返回“temporarily unavailable”，HTTP 接入在窗口内拒绝部分发布，已有事件可能延迟处理最多一分钟。不会丢失数据，服务自行重新连接。

应像规划节点丢失一样规划其返回。在 `dcctl ha verify` 再次通过之前，不要让第二个节点停机。

#### 维护数据库节点 {#ha-database-node-maintenance}

`--ha` 下，每个数据库运行三个实例，且同一数据库不会将两个实例放在一个节点。如果集群恰好有三个可供数据库使用的节点（[为数据库划分专用节点](#database-placement)时即三个数据库节点），每个节点都运行每个数据库的一个实例。下面介绍 cordon 一个节点、执行维护、再 uncordon 同一节点的流程，例如操作系统补丁或重启：

- **节点上的主节点会切换到 standby。** 一旦 cordon，数据库 Operator 就开始将主节点切换到其他节点的 standby，drain 可在过程中驱逐旧主节点。该数据库写入在 switchover 期间暂停（参见[数据库主节点停止时](#ha-database-failover)），服务会重试。测试中，cordon 后约十秒新主节点就位。
- **被驱逐实例等待原节点返回。** 其他节点已各有同一数据库实例，因此每个数据库被驱逐的实例保持 `Pending`，直到节点 uncordon。节点离开期间，每个数据库以三个实例中的两个运行，这是预期状态，无须修复。
- **期间不要让任何数据库再失去一个实例。** 如果关系存储丢失剩余 standby，所有写入等待（参见 [`--ha` 下的数据库](#ha-databases)）。如果实例事件存储丢失 standby，会继续运行但不复制，此时恢复点受复制延迟限制。尽快完成维护，节点返回前不要让其他组件停机。
- **维护完成后 uncordon。** 等待的实例会在原节点重新启动，并以 standby 加入。测试中，节点离开不足一分钟时，uncordon 后 45 秒内每个数据库恢复三个就绪实例。没有测试替换节点而非原节点返回的情况，例如托管节点池升级；等待实例只有在卷能随其迁移的位置才能启动。

如果数据库与实例其他组件共用节点（未配置[数据库放置](#database-placement)），drain 还会停止该节点的消息代理服务器和服务 Pod，消息代理也保持 `Pending` 直到 uncordon。请将其视为计划内的[节点丢失](#ha-node-loss)，一次一个节点。

Drain 下一个节点前，确认每个数据库都已恢复完整实例数：

```bash
kubectl get clusters.postgresql.cnpg.io -A
```

每个数据库的 `READY` 应为 3。如果节点上有消息代理服务器，还要像[节点丢失](#ha-node-loss)之后一样，为实例运行 `dcctl ha verify`。

实例所需节点仍 cordon 时，重复 `dcctl install` 或新的 `dcctl bootstrap` 会被拒绝，因为 cordon 节点不计入它们检查的可用节点（参见[数据库放置](#database-placement)）。请先完成维护。

没有 `--ha` 时，每个数据库只有一个实例，没有 standby 可切换。Drain 不会被阻止：实例停止，数据库不可用，直到能重新启动。如果卷绑定到该节点，如本地 kind，则必须等节点 uncordon。

#### 数据库主节点的位置 {#ha-database-primaries}

每个数据库优先选择没有其他 DeviceChain 数据库主节点的节点。在三个 8 vCPU 节点测试中，关系存储和事件存储主节点共置时，该节点 CPU 为 94–98%，另两个为 45–51%。

- **这是偏好，不是硬性要求。** 节点更少的集群仍能调度每个数据库实例。例外是带 `CrossNamespacePodAffinity` scope 的 `ResourceQuota`：它拒绝放置规则跨命名空间的 Pod，无论规则是偏好还是要求，因此会在禁止此行为的命名空间拒绝数据库 Pod。API server 的 quota admission 配置也可在没有匹配 quota 时，对所有命名空间施加同一限制，参见[发布说明](./releases-and-upgrades.md#v0190-primary-spread)。
- **只在数据库 Pod 调度时应用。** 三节点 `--ha` 中，每个节点已有各数据库一个实例，因此实际主要决定一件事：创建实例事件存储时，首个主节点选择不运行关系存储主节点的节点。[故障切换](#ha-database-failover)、switchover 或数据库滚动更新结束时的 switchover，仍可能让两个主节点共置；偏好不会自动迁回。
- **此偏好引入前创建的事件存储，在实例升级时获得它。** 升级前，其 Pod 既没有其他数据库查找的标签，也没有偏好。`dcctl upgrade` 应用实例事件存储（参见[升级应用哪些基础设施变更](./releases-and-upgrades.md#upgrade-infrastructure)）并添加两者。标签无需重启即可到达运行中的 Pod；偏好改变 Pod spec，因此实例会重启一次：`--ha` 下先 standby，再 switchover；单实例则直接重启，事件在接入 stream 等待。使用 `--skip-infrastructure` 会让事件存储继续缺少两者，直到不带该标志升级。获得偏好并不迁移主节点：`--ha` 的重启结束时切换到已调度 standby，因此之后仍可能共置。请检查并按下述方式迁移一个。

查看主节点位置：

```bash
kubectl get pods -A -l cnpg.io/instanceRole=primary -o wide
```

如果两个主节点共用节点，请将一个数据库切换到另一节点的 standby。使用 CloudNativePG `kubectl` 插件：

```bash
kubectl cnpg promote dc-rdb dc-rdb-2 -n dc-system
```

或者不使用插件：

```bash
kubectl -n dc-system patch cluster dc-rdb --subresource=status --type=merge \
  -p '{"status":{"targetPrimary":"dc-rdb-2"}}'
```

选择第一个命令显示没有主节点的节点上的 standby。Switchover 短暂中断写入（参见[数据库主节点停止时](#ha-database-failover)），服务会重试。单实例没有 standby 可切换。

### 数据库放置 {#database-placement}

默认数据库运行在 Kubernetes 选择的节点。集群有数据库专用节点时，`dcctl install` 可将数据库放到这些节点：

```bash
dcctl install local --kube-context "$CTX" --ha \
  --database-node-selector example.com/role=database \
  --database-toleration dedicated=database:NoSchedule
```

- `--database-node-selector key=value` 只允许数据库运行在带该标签的节点，可重复使用以要求多个标签。
- `--database-toleration` 允许进入带 taint 的节点，语法与 `kubectl taint` 相同：`dedicated=database:NoSchedule` 容忍该值的 taint，`dedicated:NoSchedule` 容忍任意值。必须配合 `--database-node-selector`，因为 toleration 只是允许使用节点，不负责选择节点。

仅标签可限制数据库位置，却不能阻止其他组件进入。只有 taint 能做到，因此数据库专用池通常同时带两者。

**放置哪些组件。** 共享关系存储，以及集群中每个 bootstrap 实例的事件存储。`dcctl bootstrap` 没有放置标志，遵循 install。NATS、备份对象存储、监控栈和服务不应用此放置。NATS 不设置 toleration，因此数据库节点带 taint 时运行在其他节点。这是有意设计：负载下 NATS 服务器使用超过一核 CPU，`--ha` 的三台服务器运行在不同节点；若放入三节点数据库池，其中一台必然与最忙的数据库 Pod——事件存储主节点——共置。

**检查哪些条件。**

- 创建任何资源前，`install` 会拒绝节点无法满足的放置：每数据库每节点一个实例，因此可用节点数至少等于实例数（`--ha` 为三个，否则一个）。可用节点必须带所有要求标签、未 cordon，且所有 `NoSchedule` 或 `NoExecute` taint 均被容忍。提示列出发现的节点及各自不符合的原因。`bootstrap` 为每实例再次检查，因为安装后可能发生 drain、标签修改或 taint 修改。
- 应用时按各数据库实际实例数检查同一节点数量，在创建或修改数据库之前拒绝不足配置。
- 维护中 cordon 或未就绪的节点不计入。节点升级使一个数据库节点离线时，重复 `install` 或新 `bootstrap` 被拒绝；先完成升级再重试。节点离线期间数据库行为参见[维护数据库节点](#ha-database-node-maintenance)。
- 两个数据库主节点仍在所选节点中[偏好不同节点](#ha-database-primaries)；只选一个节点时则共用它。

查看数据库运行位置：

```bash
kubectl get pods -A -l cnpg.io/cluster -o wide
```

**修改放置。** 这是 install 设置，和其他设置一样，只要集群中有实例运行就拒绝修改（参见[重复运行 install](#re-running-install)）。包括在原本使用这些标志的集群上，不带标志重复运行 `install`。请在首次安装时确定：数据库卷保留创建位置，绑定到一个节点或可用区的存储不能随实例迁移到其他位置。

没有实例运行时，不会拒绝变更，但仍可能让关系存储停机。关系存储比所有实例存活更久，使用不同放置重复运行会将其 Pod 移到新节点。如果卷绑定原节点（kind 的 local-path 存储）或新选择之外的可用区，Pod 保持 `Pending`，存储不会恢复。`dcctl` 只统计匹配节点，不检查已有卷位置。只有存储能随之迁移，或能够接受重建存储时，才修改放置。

### 服务容量配置 {#service-sizing}

每个后端服务请求 128Mi 内存，上限 256Mi。CPU 按各服务测量结果配置：

| 服务 | CPU 请求 | CPU 上限 |
| --- | --- | --- |
| `device-management` | 800m | 2 核 |
| `event-management` | 900m | 2 核 |
| `device-state` | 950m | 2 核 |
| `event-sources` | 1 核 | 2 核 |
| `event-processing` | 400m | 1 核 |
| 其他所有后端服务 | 100m | 500m |

每个 NATS 服务器请求 500m CPU 和 768Mi 内存，内存上限 2Gi，无 CPU 上限；详见[消息代理](#broker-sizing)。

[`--compact`](#--compact) 下，每个后端服务和 NATS 服务器改为请求 25m CPU、64Mi 内存，上限不变。控制台单独配置。

前四个服务负责每事件的接收、解析、存储及合并到设备实时状态。`event-processing` 对每个事件执行检测，其上限为测量用量的两倍，见下文。前四个服务的上限针对租户默认每秒 1000 条消息的实时设备流量、每消息一个读数，以及默认安装提高事件持久化参数前持续约每秒 4,000 个事件的处理量配置，见[实测吞吐量](#measured-throughput)。

- **请求来自每秒 6,000 个事件时的用量。** 请求只是调度器在节点上为 Pod 预留的 CPU，用于决定放置位置。上述数值为各服务在每秒 6,000 个事件时的实测用量向上取整。默认高可用安装在三个 4-vCPU 数据库节点及三个 4-vCPU 服务节点上，`event-management` 两个 Pod，解析、存储及实时状态都能跟上该速率，见[实测吞吐量](#measured-throughput)。`event-processing` 例外，其请求来自 1 核上限、提供每秒 6,800 个事件时的用量。发布候选版本中，400m 请求和 1 核上限可跟上每秒 6,000 个事件（积压峰值低于 1,000），从提供每秒 7,600 个事件起开始落后；它不在[实测吞吐量](#measured-throughput)的恰好存储一次检查范围。此前较小请求（[实测吞吐量](#measured-throughput)中被后续结果替代的一行）仅为此速率用量的 15% 至 66%，使调度器将繁忙服务放在一起：每秒 6,000 个事件时两个服务节点约 80% CPU，第三个约 55%。内存仍为 128Mi：所有样本直至提供每秒 9,200 个事件，事件路径服务均未超过 51Mi。请求按 Pod 计算，两个副本请求两倍资源。`--ha` 下 `event-management` 两个 Pod 合计请求 1.8 核，见 [`--ha`](#ha)。每个 Pod 保留由单 Pod 承担全部工作时测量的请求，因此预留高于两个 Pod 的总实际用量。
- **上限不预留资源。** Kubernetes 按请求调度，提高上限不要求节点额外容量，只允许繁忙服务使用节点空闲 CPU。`--compact` 降低请求，保留上限。
- **降低 CPU 上限限制吞吐量，不节省预留容量。** 四节点高可用 kind 集群中，500m 上限下 `device-management` 最多解析约每秒 720 个事件，几乎每个调度周期都被限流，额外事件留在入站流等待。不受上限限制时测得其每事件最多使用 0.96 毫核 CPU，`event-management` 每存储事件最多 0.53 毫核，因此默认摄取上限约需一核和半核。CPU 上限按短周期执行，突发远先于平均值触及上限，所以各上限为此需求的两倍。三节点云集群中，500m 下 `event-sources` 在每秒 4,000 个事件时 84% 调度周期被限流，响应变慢使设备发送速度止于约每秒 4,300 个事件；`device-state` 最多合并约每秒 2,300 个事件，实时设备视图落后数分钟。不受上限限制时，两者每事件用量分别为 0.14、0.37 毫核，因此每秒 4,000 个事件约需半核和一核半。
- **检测最多使用一核。** `event-processing` 以单个分区检查所有事件与检测规则。此前 500m 上限下，三节点云集群每秒 6,000 个事件时约 5% 调度周期被限流，并发生积压，两次十分钟运行峰值约 41,000 和 93,000 个事件。发布候选版本采用 1 核上限、400m 请求，可跟上每秒 6,000 个事件，积压峰值低于 1,000；从提供每秒 7,600 个事件起落后，此时未被 CPU 上限限制。检测不在[实测吞吐量](#measured-throughput)的恰好存储一次检查范围，其上限为 1 核。
- **最繁忙服务避开事件存储主实例。** 默认 CloudNativePG 安装下，`device-management`、`event-management`、`event-sources` 优先选择不运行本实例事件存储主实例的节点，因为它是安装中最繁忙的单个进程。这只是偏好，节点少于繁忙服务数时仍可调度。只在 Pod 调度时生效，数据库故障切换不迁移现有 Pod。下述调优设置中，将 `event-management` 移出该节点使持续速率从约每秒 4,750 提高至 5,600 个事件；默认设置下影响未测量。单服务可通过 `functionalAreas.<service>.avoidEventStorePrimary: false` 关闭。
- **五个事件路径服务分散到节点。** `device-management`、`event-management`、`device-state`、`event-sources`、`event-processing` 优先选择运行这五个服务较少的节点，三个节点上通常每节点最多两个。这是调度器综合权衡的偏好，不保证；节点较少时仍调度，空闲 CPU 较多的节点可能胜出。现有 Pod 不会迁移。NATS 自行选举流领导者，因此服务仍可能与最繁忙 NATS 服务器同节点。服务有自己的分散策略后，不再采用集群默认的同服务副本跨节点、跨可用区分散；默认单副本无区别。多副本仍优先避开已有本服务 Pod 的节点，但不再优先不同可用区。高可用下 `event-management` 两个 Pod，也受此影响。单服务用 `functionalAreas.<service>.eventPathSpread: false` 关闭。
- **两个数据库主实例优先不同节点。** 测试中同时运行关系型与事件存储主实例的节点 CPU 达 94% 至 98%，其他节点约一半。详见[数据库主实例位置](#ha-database-primaries)，包括故障切换或升级后的检查。
- **更大流量需要更多资源。** 多租户各自达到上限、每消息携带多个读数，或租户追赶时[获准超过上限](../concepts/governance.md#ingest-above-ceiling)，均超过此配置目标。可增加水平扩展的 `device-management` 的 `replicas`；副本增加会稍微扩大单设备事件乱序，见[检测引擎配置](./detection-engine.md#configuration)的 `watermarkLatenessSeconds`。也可提高单个服务上限：

  ```yaml
  functionalAreas:
    device-management:
      resources:
        limits:
          cpu: "4"
  ```

  服务 CPU 和内存有三层来源，逐键由后者覆盖前者：顶层 `resources`、服务实测 CPU 请求 `functionalAreas.<service>.measuredRequests`、服务自己的 `functionalAreas.<service>.resources`。只需设置不同项。节点争用时要保证 CPU，也应提高服务自身 `resources` 中的请求。请求高于上限时 chart 渲染拒绝执行，并指出两个值的来源。

  事件路径服务自身 CPU 上限及请求也如此设置，因此顶层 `resources` 不覆盖它们。顶层 4 核上限会给其他所有后端服务 4 核，但五个服务仍保留自身 2 核上限（`event-processing` 为 1 核）；顶层 `requests.cpu` 不影响这五个。应按上例在 `functionalAreas` 设置。仅 chart 安装可用 `useMeasuredRequests: false` 关闭实测请求，使顶层请求适用于所有服务，这也是 `--compact` 的行为。

容器指标 `container_cpu_cfs_throttled_periods_total` 可判断服务是否被 CPU 上限限流。

#### 消息代理 {#broker-sizing}

每个 NATS 服务器请求 500m CPU、768Mi 内存，内存上限 2Gi。Go 运行时软内存限制 `GOMEMLIMIT` 为该上限的 80%，使其在内核终止之前加强垃圾回收。此前无请求和上限，节点内存紧张时最先被驱逐。

- **内存来自测量。** 发布基准中，直至提供每秒 9,200 个事件，2Gi 上限下服务器驻留内存均未超过约 830 MiB，工作集约 1.3 GiB，没有因内存被终止。768Mi 请求低于峰值，只影响放置，上限才会终止进程。测量是稳定摄取，未覆盖服务器重新加入集群或节点丢失后追赶大量积压。若 `kubectl describe pod` 出现 `OOMKilled`，应提高上限。
- **CPU 请求低于负载下用量。** 每秒 6,000 个事件时三个服务器合计约 4.4 核，入站事件流领导者占 1.6 至 1.8 核。三节点高可用下每节点恰一个服务器，请求不改变放置，但使服务器不属于最先驱逐类别，并在繁忙节点获得 CPU 份额。请求完整用量会从三节点集群预留超过 4 核，却不移动任何服务器。非高可用下单服务器承载全部事件，未测量其用量，500m 低估需求。不设置 CPU 上限，因为所有事件经过代理，上限会同时拖慢所有服务。
- **每节点需容纳一个服务器。** 高可用下三服务器必须位于不同节点。若某节点无法再容纳 500m、768Mi，该服务器保持 `Pending`，初始化最多等待 15 分钟后失败。`kubectl get pods -n <instance namespace>` 显示状态，`kubectl describe pod` 说明原因。
- **现有实例升级时获得此配置。** `dcctl upgrade` 应用实例代理配置，早期版本实例升级后获得这些请求和上限，服务器重启生效，高可用下逐个重启。详见[升级应用哪些基础设施配置](./releases-and-upgrades.md#upgrade-infrastructure)。
- 更改时，在实例 OpenTofu 状态目录（`~/.devicechain/instances/<instance>/infra/instance/`）旁的 `terraform.tfvars` 中设置 `nats_cpu_request`、`nats_memory_request`、`nats_memory_limit`。每次实例 `dcctl` 应用，包括升级，均读取文件。内存单位为 `Mi` 或 `Gi`。`--compact` 实例每次应用由 dcctl 自行传入两个请求，覆盖文件，所以此时仅 `nats_memory_limit` 可用文件修改。

#### 实测吞吐量 {#measured-throughput}

| 版本 | 集群 | 设置 | 持续速率 | 结果 |
| --- | --- | --- | --- | --- |
| v0.18.0 | Google Kubernetes Engine，3 × n2-standard-8（每个 8 vCPU），SSD 持久盘，`--ha` | v0.18.0 发布时默认值，尚未采用上述容量配置 | 约 3,800 事件/秒 | 两次十分钟测试各提供 4,000 事件/秒，均存储 2,399,000 个事件，与接受数量相同，仅比较总数而非逐事件。一轮最慢阶段保持输入的 96.5%（解析每秒 3,858），另一轮 98%。实时状态受当时 `device-state` 500m 上限影响，只能跟上约 2,300 事件/秒。 |
| v0.18.0 | 同上 | 调优，见下文 | 约 5,600 事件/秒 | 180 秒测试，提供 5,600 事件/秒时每阶段至少保持输入的 98.9%，积压 3 秒内清空，存储数与接受数相同，仅比较总数。实时状态跟得上。 |
| v0.18.0 之后、持久化默认值修改之前 | 同上 | 上述容量配置，`event-management` 的 `persistence.writers: 5`、`persistence.maxBatch: 32` | 约 3,900 事件/秒 | 两次十分钟测试各提供 4,000 事件/秒，均存储 2,400,000 个事件，与接受数量相同，仅比较总数。存储最慢，为输入的 96.7% 和 95.7%。实时状态延迟保持在约 40 秒内。 |
| v0.18.0 之后 | 同上 | 调优：`event-management` 的 `persistence.writers: 10`、`persistence.maxBatch: 64`，见下文 | 约 6,000 事件/秒 | 180 秒测试，提供 6,000 事件/秒时每阶段至少保持输入的 98%，积压 5 秒内清空，存储数等于接受数，仅比较总数。持续五分钟时存储保持 96%，因此持续值约 5,800 至 6,000。 |
| v0.18.0 之后 | Google Kubernetes Engine，3 × n2-standard-4 数据库节点（有污点，仅数据库）和 3 × n2-highcpu-4 服务节点，SSD 持久盘，`--ha` | 上述请求修改前的默认值（`device-management` 500m，`event-management`、`device-state` 400m，`event-sources` 150m，`event-processing` 100m），采用当前事件持久化默认值 | 约 6,000 事件/秒 | 已被下一行采用当前请求及默认卷大小的结果替代。两次十分钟测试各提供 6,000 事件/秒，均存储 3,600,000 个事件，与接受数量相同，仅比较总数。解析、存储及实时状态均保持至少 99.7%，积压十秒内清空。检测积压峰值约 93,000。事件存储和备份卷小于默认值。上述请求来自这些测量。 |
| v0.19.0 发布候选版本 | Google Kubernetes Engine（`us-east4-b`），3 × n2-standard-4 数据库节点（16 GB，有污点，仅数据库）和 3 × n2-custom-4-8192 服务节点（4 vCPU、8 GB），标准持久启动盘，`--ha`，默认卷大小 | 上述默认值，`event-management` 两个 Pod（高可用默认），Go 性能分析启用但空闲 | 6,000 事件/秒 | 两次十分钟测试各提供 6,000 事件/秒，实际接受 5,997 事件/秒，3,600,000 个已接受事件全部恰好存储一次。按每事件设备和发生时间逐一检查，无丢失、重复或意外事件。解析、存储及实时状态均跟得上，积压约两秒清空。该检查不覆盖的检测也跟得上，积压峰值低于 1,000，从提供 7,600 事件/秒起落后。 |

v0.18.0 两行在该版本测量，最后一行在 v0.19.0 发布候选版本，其余在两者之间的开发构建。负载生成器均在独立节点，事件存储均采用复制式高可用。v0.18.0 默认瓶颈为 `device-management` 解析器池，其后是 `event-sources`、`device-state` 的 CPU 上限，上述配置已提高它们。提高后存储成为瓶颈：5 个写入器、每批最多 32 个事件，从每秒 4,400 个事件起批次全满，每次提交约 38 毫秒，存储止于约每秒 4,200。因此 `event-management` 现默认 10 个写入器、每批最多 64 个，见[事件持久化](./observability.md#event-persistence)。

v0.18.0 调优行使用：`device-management` 两副本，`resolution.workers: 32`、`rdbConfiguration.maxOpenConnections: 48`；`event-sources` 两副本；`event-management` 的 `persistence.writers: 10`、`persistence.maxBatch: 64`，位于无事件存储主实例的节点；`device-state` 的 `projection.writers: 5`、`projection.maxBatch: 64`、`projection.lingerMillis: 25`；CPU 上限 4 核（`event-processing` 为 2 核），内存上限 1Gi。之后调优行保留单副本 `device-management` 默认值，其他事件存储和状态配置相同，对 `device-management`、`event-sources`、`event-management`、`device-state` 设置 4 核 CPU、1Gi 内存上限。`event-management` 最多约用 1.7 核，未在默认 2 核上限下测量。所有测试平均批次小于 32，不能证明 64 比 32 有帮助。每秒 6,000 个事件时平均每批约 21 个；更高输入下平均 28 至 30，尚未达到上限，但存储速度不再增长，三个节点中两个（包括事件存储节点）CPU 达 86% 至 95%。未单独确定哪个限制速率，但此三节点集群需要更多节点后才值得进一步服务调优。采用新持久化默认值的默认安装已在最后两行的分离集群上测量，持续每秒 6,000 个事件，最后一行的 `event-management` 为两个 Pod；未在其他行的三节点集群测量。

#### 事件存储卷 {#event-store-volume}

每个事件存储实例有 32Gi 卷，高可用下三个；紧凑模式每个 4Gi。每个存储读数约占 1.05 KB，备份跟得上时数据库预写日志另占约 1.1 GB，因此 32Gi 可容纳约 2,700 万事件，相当于单租户持续默认上限约七小时。

事件存储压缩日志中的页映像（`wal_compression = lz4`）。每次检查点后首次修改页面会将整页写入日志，此存储中多数为索引页。在 v0.18.0 之后的开发构建对比中，集群结构与[实测吞吐量](#measured-throughput)相同，但调优不同；提供每秒 5,200 个事件时，压缩使每存储事件日志从约 3.0 KB 降至 1.7 KB，日志容量迫使的检查点比例相近地从每百万事件 5.8 次降至 3.2 次。不改变卷可容纳事件数：备份跟得上时日志仍约 1.1 GB，因此上述容量保持成立。[备份存储容量](#backup-store-size)在此压缩、时间优先键及 zstd 归档之前测量，所以估计偏大：发布基准中事件存储归档约每事件 0.47 KB，而容量计算假设两个数据库合计 1.9 KB；关系型占比未重新测量。关系型数据库不压缩其日志。

单实例、[默认备份存储](#backup-store-size)下，持续摄取会先填满此卷，`DatabaseVolumeFillingFast` 提前预警。多个实例、备份存储较小或事件卷扩大时，备份存储可能先满，停止归档后未发送日志堆积在此卷，直到卷也满、数据库停止；[告警](./observability.md#backup-archiving)在两者之前预警。用于长期运行的实例可通过事件管理 `lifecycle` 中的 `retentionDays` 限制存储数据，但不限制归档。

容量在实例创建时固定。`dcctl upgrade` 应用其他事件存储配置，保留当前卷容量。StorageClass 支持扩容时可执行：

```bash
kubectl -n dci-<instance> patch clusters.postgresql.cnpg.io dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"64Gi"}}}'
```

扩大此卷会改变备份存储先满的临界点。备份存储需增加约此处增量的五倍，见[备份存储容量](#backup-store-size)。

## 初始化之后 {#after-bootstrap}

命令打印命名空间、**超级用户**凭据和通过集群 Ingress 访问方式。超级用户为 `superuser@devicechain.local`，没有默认密码。初始化为实例生成密码，保存于实例命名空间的 Secret `dci-<instance>-superuser`，只在生成密码的运行结束时显示一次，或完成此前尚未显示密码便失败的初始化时显示。再次读取：

```bash
kubectl -n dci-my-instance get secret dci-my-instance-superuser -o jsonpath='{.data.password}' | base64 -d
```

### 超级用户密码 {#superuser-password}

用户管理服务只在首次面对空身份表启动、创建超级用户时读取此密码。之后 Secret 仅记录最初密码。在控制台改密码不会更新它。初始化**恢复**实例及身份时，超级用户保留恢复前密码；报告不显示 Secret 值，并提示它可能不是当前超级用户密码。

早期版本初始化的实例没有此 Secret，其超级用户使用当时公开的默认密码创建。`dcctl upgrade` 或对运行实例重跑初始化（恢复或 `--allow-legacy-db-removal`）都不更改密码、不生成 Secret，并在结束时说明。如果尚未改过，请在控制台修改。

仅用 Helm 而非 `dcctl` 安装时，必须在用户管理首次启动前自行创建此 Secret，包含 `password` 键，或用 chart 值 `instance.superuserSecret` 指定其他 Secret。缺少时用户管理拒绝创建超级用户。

### 登录控制台 {#sign-in}

实例包含 **Web 控制台**。Ingress 在主机根路径 `https://<host>/` 提供控制台，将 `https://<host>/api/<area>/graphql` 路由到各领域服务。在浏览器打开后，用超级用户邮箱及密码登录。

新实例**没有租户**，会进入管理控制台 `/admin`，创建首个租户并分配成员资格。切换到租户后进入租户控制台。无界面或仅摄取实例可用 chart 的 `frontend.enabled` 禁用控制台。

检查运行实例：

```bash
kubectl --context <kube-context> get pods -n dci-my-instance
```

### 运行模拟 {#run-a-simulation}

可用**模拟**为控制台提供动态设备群。`sim create` 在实例创建具有范围限制的身份和租户，写入 `dc-simulator` 启动读取的握手文件：

```bash
dcctl sim create demo --instance my-instance --server localhost
```

模拟器通过真实硬件相同的设备协议发送遥测及告警，见[使用模拟数据体验](../intro.md#trying-it-with-simulated-data)。

## 删除实例 {#destroy}

```bash
dcctl destroy local my-instance
```

`dcctl destroy` **只删除指定实例**，依次执行：

1. 删除 Helm release。命名空间属于 release，卸载会删除命名空间及所有内容，包括 NATS 代理和事件存储。
2. 通过 `tofu destroy` 删除基础设施状态仍持有的内容。打印 OpenTofu 删除计划，但省略输出值列表，因为销毁时部分值由默认配置推导，不能正确描述实例设置。
3. 删除共享关系型数据库上的实例数据库及登录。
4. 删除尚存命名空间。Helm 安装前失败的初始化没有 release 可卸载，但可能留下命名空间。销毁等待其完全消失。
5. 如果事件存储备份位于集群自有对象存储（[默认目的地](#default-backup-destination)），删除事件存储归档路径下全部预写日志和基础备份，并检查路径已为空。
6. 确认删除对象确实不存在，才删除 `~/.devicechain/instances/<instance>/` 下本地状态。

根密钥托管文件保留，见[灾难恢复](./disaster-recovery.md#after-destroy)。

### 实例备份的处理 {#destroy-backups}

销毁在任何改动前读取并打印事件存储归档路径，待命名空间消失、没有写入者后才删除该路径。只删除该路径，不删除共享关系型备份、其他实例备份或同名实例留下的更早归档。

- **自行提供对象存储的备份**（`dcctl install --backup-credentials-file`）不删除，它们应比集群存活更久。销毁打印位置，不再需要时自行删除。
- **`--keep-backups`** 也保留集群内备份。在同集群通过 `dcctl bootstrap --restore-tsdb-from` 重建时使用，否则销毁恰好会删除恢复读取的归档。`dcctl destroy --all` 也支持并对每个实例应用此标志。
- **卷快照基础备份**（`dcctl install --backup-snapshot-class`）位于实例命名空间，无论是否 `--keep-backups` 都随命名空间删除。恢复不读取它们，`--restore-tsdb-from` 读取标志保留的备份存储。
- **对象存储不可访问**或无法确定实例路径时，销毁仍完成，但说明留下的内容，结束行不会将实例报告为完全销毁。

事件存储删除后中断的销毁，包括删除备份时中断，会记住之前读取的路径，重跑仍会删除备份。

早于本版本的销毁会留下集群内备份，没有组件自动清理。删除本实例备份后，销毁列出桶内疑似同名实例更早归档的路径，但不删除。若保留当前备份（`--keep-backups`、外部存储、无法访问或删除），则不列出旧归档。手动查找及删除可通过端口转发访问对象存储，使用任意 S3 客户端，如 AWS CLI：

```bash
kubectl -n dc-system port-forward svc/dc-object-store 9000:9000 &
export AWS_ACCESS_KEY_ID="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_USER}' | base64 -d)"
export AWS_SECRET_ACCESS_KEY="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_PASSWORD}' | base64 -d)"
aws s3 ls s3://devicechain-tsdb/ --endpoint-url http://127.0.0.1:9000
aws s3 rm --recursive s3://devicechain-tsdb/<path>/ --endpoint-url http://127.0.0.1:9000
```

各运行实例在事件存储指定的路径归档，通过 `kubectl -n dci-<instance> get clusters.postgresql.cnpg.io dc-tsdb -o yaml` 查看 `spec.plugins` 下 `serverName`。只删除没有运行实例使用且无人需要恢复的路径。

步骤失败或中断，包括命名空间仍在终止而等待超时，销毁返回错误并保留本地状态；重跑相同命令从中断处继续。

实例仍运行但本地基础设施状态丢失或位于初始化时的另一机器时，销毁**拒绝**，因为没有状态就不能执行基础设施销毁。`--without-state` 仍按 Helm release、数据库、登录和命名空间删除，并报告跳过基础设施销毁。本地状态早于基础设施拆分为集群与实例部分的实例同样在任何改动前拒绝，需此标志。`dcctl destroy --all` 也支持它。

销毁不删除集群或 `dcctl install` 的前置组件，下一次初始化无需先安装。删除再以同名初始化是重建实例的方法。但早于 `dcctl install` 的实例还需重建集群，见[版本与升级](./releases-and-upgrades.md#pre-declaration-recreate)。

kind 集群已由 `kind delete cluster` 删除时，没有可卸载对象，销毁会说明并只清理本地状态，包括实例目录及 `dcctl install` 创建、kind 删除后留下的 `~/.devicechain/clusters/<cluster-uid>/` 集群目录，见[安装集群](#install)。会打印：

```text
removing the gone cluster's local state (~/.devicechain/clusters/<cluster-uid>)
```

目前没有卸载命令。删除 `dcctl install` 创建的本地集群时，按[安装集群](#install)直接使用 kind。保留集群时见[移除集群](#removing-a-cluster)。
