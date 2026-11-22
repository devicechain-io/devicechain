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
已可用：Helm Chart 渲染各服务工作负载和配置，`dcctl bootstrap` 与 `dcctl upgrade` 驱动实例生命周期。Operator 观察 `Instance` 资源，并把它观察到的实例工作负载情况写入该资源的[状态](#instance-status)。它的另一项工作针对使用 [`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups) 安装的集群：每十分钟删除已超出恢复窗口的数据库卷快照基础备份，CloudNativePG 不会执行这项清理。按环境划分的 Kustomize 覆盖层已规划，但尚未开始。
:::

`kubectl get dci` 无需读取工作负载即可回答“是否在运行、版本是什么”：

```text
NAME   PROVIDER   CLUSTER           PROFILE   REQUESTED   OPERATION   READY   RUNNING   AGE
prod   local      kind-devicechain  default   v1.0.3      Ready       True    v1.0.3    12d
```

## 使用 Helm 部署 {#deploying-with-helm}

`deploy/helm/devicechain` 中的 Chart 渲染：

- 每个已启用功能域对应一个 Deployment 和 Service；
- 各服务配置 ConfigMap；
- 实例配置 Secret。它携带持久化凭据，因此使用 Secret，而不是 ConfigMap。

每个 Pod 都暴露 `/healthz`（存活检查）和 `/readyz`（就绪检查），因此未就绪的服务不会参与流量分配。

可以通过具名配置档或显式服务集合选择运行哪些服务：

| 配置档 | 功能域 |
|---|---|
| `default` | user-management、device-management、event-sources、event-management、device-state、dashboard-management、command-delivery、notification-management、event-processing、update-management——标准系统，也是未设置配置档时的选择 |
| `full` | 此构建发布的全部功能：`default` 加 `ai-inference`、`outbound-connectors`、`mcp`、`sparkplug-ingest`、`lwm2m-ingest`；这些功能不包含在 `default` 中，因为每一项都需要明确作出决定（付费提供商密钥、出站访问接口、面向代理的 API、绑定独立入站端口的 Sparkplug B 或 LwM2M 设备传输） |
| `telemetry` | user-management、device-management、event-sources、event-management、device-state、dashboard-management |
| `ingest-only` | user-management、device-management、event-sources |

### 机密存储根密钥 {#root-key}

每种配置档都需要实例的**机密存储根密钥**。所有配置档都运行的 `user-management` 会用它封装登录令牌签名密钥。存储集成凭据的功能域也用它封装凭据。缺少时，Chart 会使渲染失败，而不是让 `user-management` 陷入崩溃重启循环。

生成一个值（`openssl rand -base64 32`），妥善保存，并在每次安装和升级时传入**同一个**值。新密钥会使旧密钥保护的已有机密不可读，并阻止所有人登录。`dcctl bootstrap` 会代你生成并托管保管此密钥；只有直接操作 Chart 时才需自行提供。

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

## 集群内执行器（Preview） {#in-cluster-executor}

Preview：本版本会安装并验证执行器，但不会执行任何操作。
每个可执行请求都会被拒绝，原因为 `NotExecutableInThisBuild`。现有实例保持不变。
`dcctl install` 会新增 `ExecutorEnrollment` 和 `InstanceOperation` 两种资源定义，
但不会注册或部署执行器。与每次发布一样，旧版 dcctl 会拒绝操作已由 1.9 版
`dcctl install` 更新资源定义的集群。

提供方管理员可以注册一个所有者以及按 SHA-256 摘要固定的镜像。注册要求集群中
**每个** Instance 都有指纹与已部署根密钥匹配的托管文件（`OwnerOffline`）。
默认使用现有的 dcctl 托管目录；`--escrow-dir` 可指定另一个目录，其中仍使用
`<instance>-rootkey.escrow` 文件名。重新运行 `dcctl bootstrap` 会核对托管文件并补写
缺失文件。发现不匹配时，应先调查原因再替换文件。

```bash
dcctl executor enroll --owner platform --image "$EXECUTOR_IMAGE" --kube-context prod
dcctl executor status --kube-context prod
dcctl executor unenroll --kube-context prod
```

将 `EXECUTOR_IMAGE` 设置为完整的 `ref@sha256:<64 位小写十六进制数字>`。注册会安装
`dc-executor-system` 命名空间、ServiceAccount、固定镜像的 Deployment、执行器 RBAC、
未绑定的请求方 ClusterRole 以及 `ExecutorEnrollment/cluster`。
它不会写入 Secret、凭据文件或 kubeconfig。权限预检仅供提前检查，不能保证应用成功；
应用失败时会逐个报告对象结果。注册最多等待五分钟，确认部署完成，且 Ready 状态是针对
当前代数及固定镜像的观察结果。`--dry-run` 执行检查并打印计划，但不应用。
Status 显示声明、能力、条件及非终态 InstanceOperation；集群未注册时也成功退出。

注销会保留实例、操作历史和资源定义。存在未完成操作时，必须先用 `--force` 将其标记为
`Refused/Unenrolled`。这一显式管理员恢复操作是唯一由执行器之外的主体写入操作状态的
情况：在移除执行器前关闭请求，并通过资源版本检查保留并发产生的终态裁决。
注销会等待执行器命名空间消失。

注册会为命名空间添加 `app.kubernetes.io/managed-by: dcctl-executor` 标签。
如果已有的 `dc-executor-system` 命名空间缺少该标签，注册和注销都会在任何更改前拒绝，
即使使用 `--force` 也一样。删除命名空间时会以已观察到的 UID 为前提条件。
如果既无注册资源也无自有命名空间，注销会报告 "not enrolled"，且不作任何更改。
修改已有注册的 `owner` 或 `escrowDelivery` 也会在应用前被拒绝；请先注销，再用新值注册。

存在非终态 InstanceOperation 时，`bootstrap` 和 `upgrade` 会拒绝操作对应实例；
`destroy` 会警告并继续，以保证仍可恢复。请等待执行器裁决，或运行
`dcctl executor unenroll --force`；即使执行器未注册，该命令也会关闭待处理请求。
拒绝信息还会为每个请求列出 `kubectl delete instanceoperation <name>`，作为另一种恢复方法。
这只是检测，不是互斥锁。AB6 不覆盖 `dcctl install`：该命令准备的是集群，
而本 Preview 无论如何都会拒绝 Install 请求。

### 执行器指标 {#executor-metrics}

执行器在 `dc-executor-system` 中自己 Pod 的指标端点上导出三个计数器。它们没有 `devicechain_` 前缀，
也不按实例或操作打标签，而且 chart 的 Prometheus 不会抓取该命名空间，如需使用请自行配置抓取。

| 指标 | 含义 |
| --- | --- |
| `dc_executor_decisions_total{verdict,reason}` | 执行器写入 `InstanceOperation` 的判定。`verdict` 是写入的阶段：`Refused` 或 `Running`。`reason` 为 `Executable`，或拒绝的原因：`StaleEnrollment`、`InstanceExists`、`InstanceNotFound`、`StaleTarget`（请求指向已被替换的注册或实例，或其存在与否与该操作不符）或 `NotExecutableInThisBuild`。本 Preview 版本中没有任何操作可执行，因此每个有效请求都以 `Refused` 和 `NotExecutableInThisBuild` 结束：这是预期的稳态，不是故障。其他任何拒绝原因上升，说明请求是针对过期的注册或实例提交的；请提交新的请求。 |
| `dc_executor_operations_skipped_total{reason}` | 执行器未处理、须留给更新版执行器的操作：`protocol`（请求需要比本执行器所构建的更新的协议）或 `action`（本执行器不认识的操作）。执行器镜像为最新时为零。上升表示来自更新版 `dcctl` 的请求在等待执行器升级：用新镜像再次运行 `dcctl executor enroll`。 |
| `dc_executor_status_writes_total{kind,result}` | 写入 `InstanceOperation` 或 `ExecutorEnrollment`（`kind`）状态的尝试，按 `result` 区分：`success`、`conflict`（另一个写入者抢先；执行器立即重试）或 `error`。偶尔的冲突属正常。`error` 速率持续偏高，说明执行器无法写入状态，操作因此一直没有判定；请检查执行器的日志及其 RBAC。 |

## 自定义资源 {#custom-resources}

`dcctl install` 定义四种集群范围的自定义资源：

- **`Instance`**（`instances.core.devicechain.io`，短名 `dci`）：每次安装一个，声明实例身份和配置。
- **`InstanceConfiguration`**（`instanceconfigurations.core.devicechain.io`，短名 `dcic`）：可保存实例配置文档的资源。定义会被安装，但 `dcctl bootstrap` 不会创建该资源，Operator 也不监视它。每个 Pod 从挂载的实例配置 Secret 读取实例配置。**已弃用：**读取或写入该资源时 API 服务器会发出警告，后续的主版本将删除该定义。

- **`ExecutorEnrollment`**（`executorenrollments.core.devicechain.io`，`dce`）——执行器的唯一注册资源，记录所有者、固定镜像和观察到的能力。
- **`InstanceOperation`**（`instanceoperations.core.devicechain.io`，`dcop`）——不可变的操作请求及其裁决；本 Preview 不执行任何操作。

`Instance` 声明中有两个字段已**弃用**，后续的主版本将删除它们。带有这两个字段的实例不受任何影响：

- `spec.grafanaSSO` 不起作用。Grafana 的登录针对整个集群配置，而不是按实例配置。
- `spec.compact` 由 `spec.size: compact` 取代。**请保留两个字段，不要删除 `compact`。** dcctl 会同时写入两者，仍读取 `compact`，并拒绝 `size` 为 `compact` 但未设置 `compact` 的声明，因此手动删除它会阻止之后的每次初始化和升级。无需任何操作。

Install 是集群级请求，禁止提供 `spec.instance` 和 `spec.instanceUID`。其他动作必须提供
`spec.instance`；Upgrade 和 Teardown 还必须提供 `spec.instanceUID`，Bootstrap 可省略它，
因为目标实例尚不存在。每个请求都必须提供当前的 `spec.enrollmentUID`。终态裁决会在
`status.conditions` 中设置 `Decided=True`，并附上裁决原因。终态的 phase、reason、message
和决定时间不可更改；条件仍可更改，以维护其时间戳。注销仅删除带有
`app.kubernetes.io/managed-by: dcctl-executor` 标签的集群级 RBAC，并使用 UID 前置条件，
同时报告跳过的对象。

[`dcctl install`](./bootstrap.md#install) 将四种定义和控制器一起安装。它们在各方面都是集群范围的：每个集群一份，由其上的所有实例共享，版本跟随集群，而不是某个实例。因此，准备集群的命令也是升级它们的命令。

`dcctl bootstrap` 和 `dcctl upgrade` 只读取定义。集群没有定义，或定义能够识别为来自不同发布版本时，它们会拒绝操作，并指出应运行的安装命令。手动安装的定义没有安装版本记录，因此允许通过，但会提示同一个命令，而不是拒绝。

### 实例状态 {#instance-status}

Operator 是 `Instance` 的 `status` 的唯一写入者，记录它**观察到**的内容：`Instance` 或其任一 Deployment 发生变化时，它都会读取该实例的 Deployment，因此状态会随滚动更新实时变化。资源上的另外两样东西性质不同：

| 位置 | 含义 | 写入者 |
|---|---|---|
| `status` | Operator 对工作负载**观察到**的情况 | 仅 Operator |
| 注解（`core.devicechain.io/phase`、`last-applied-by`、`last-applied-at`） | 上次 `dcctl` 运行**意图**或所做的事；即 `OPERATION` 列 | `dcctl` |
| Operator 命名空间中的 `dcctl` Lease | **此刻**谁在操作该集群 | `dcctl` |

`dcctl` 从不写入 `Instance` 的 `status`，Operator 也从不读取这些注解：`OPERATION` 可以显示 `Upgrading`，而 `READY` 显示 `True`，两者同时成立。

| 字段 | 含义 |
|---|---|
| `status.observedGeneration` | 计算此状态所依据的 spec 代数 |
| `status.conditions` | `Ready`、`WorkloadsAvailable`、`RolloutComplete`、`BrokerCertificateValid`（见下） |
| `status.version.requested` | `spec.imageVersion` |
| `status.version.running` | 所有需要 Pod 的功能域运行的镜像标签；各域不一致时为 `mixed`（保留值：绝不会是发布标签）；无法得出时为空 |
| `status.areas[]` | 每个功能域一项：`name`、`desiredReplicas`、`readyReplicas`、`availableReplicas`、`updatedReplicas`、`image`（完整镜像引用，而非仅标签）。各条件依据 `availableReplicas` 计算 |

状态小而有界：每个功能域一项，没有按租户或按设备的内容。

| 条件 | 原因（`reason`） | 状态 | 含义 |
|---|---|---|---|
| `WorkloadsAvailable` | `AllAreasAvailable` | True | 每个需要 Pod 的功能域都有全部 Pod 可用 |
| | `NoWorkloads` | False | 不存在功能域 Deployment（尚未渲染或已被移除），或所有功能域都缩容到零 |
| | `AreaUnavailable` | False | 至少一个功能域需要 Pod 却没有任何可用 Pod |
| | `AreaDegraded` | False | 每个功能域都至少有一个可用 Pod，但至少一个少于所需数量 |
| `RolloutComplete` | `RolloutComplete` | True | 每个 Deployment 都已收敛到当前模板，且运行版本即请求版本 |
| | `NoWorkloads` | False | 不存在功能域 Deployment |
| | `RolloutInProgress` | False | 某功能域尚未收敛：仍有旧 Pod，或 Deployment 控制器尚未观察到最新模板 |
| | `ProgressDeadlineExceeded` | False | 某 Deployment 报告其滚动更新已停止推进 |
| | `VersionMismatch` | False | 已收敛，但版本与 `spec.imageVersion` 不同 |
| | `VersionUnknown` | Unknown | 已收敛，但无法从镜像得出运行版本（镜像仅以 digest 固定） |
| `BrokerCertificateValid` | `CertificateValid` | True | 代理的 TLS 证书及其签发机构的有效期都还剩 30 天以上 |
| | `CertificateExpiringSoon` | True | 两者之一将在 30 天内到期；`dcctl upgrade` 会续期证书，但从不续期颁发机构，那需要重建实例 |
| | `CertificateExpired` | False | 两者之一已过期：在证书续期（若是颁发机构，则重建实例）之前，代理会拒绝 TLS 连接 |
| | `CertificateNotPublished` | Unknown | 尚未为该实例发布到期时间（它上次由早于此功能的 `dcctl` 操作） |
| | `CertificateUnreadable` | Unknown | 已发布的到期时间不是 RFC 3339 时间 |
| `Ready` | `Ready` | True | `WorkloadsAvailable` 与 `RolloutComplete` 均为 True，且代理证书未过期 |
| | （第一个非 True 条件的原因） | False / Unknown | |

缺失的工作负载总是带着原因报告，绝不会被当作健康。`Ready` 表示已稳定：滚动更新期间即使 Pod 仍在服务，它也为 False，因此要判断实例是否在提供服务，请读取 `WorkloadsAvailable`。

#### 代理证书到期 {#broker-certificate-expiry}

代理的 TLS 证书有效期为一年，只在 `dcctl upgrade` 运行时、于证书最后 30 天内续期。因此从不升级的实例会在引导一周年时失去代理 TLS，而没有任何提示。`BrokerCertificateValid` 就是 `Instance` 自身上的这个提示。

Operator 无权访问 Secret，所以它不读取证书。`dcctl` 在放置或续期证书时（`dcctl bootstrap` 以及每一次 `dcctl upgrade`，包括没有续期任何东西的那次，因此较旧的实例会在下一次升级时获得）把证书及其签发机构的到期时间发布为 `Instance` 上的两个注解：代理 Secret 中证书的日期，在代理开始提供该证书之后才写入。如果续期重启了代理而重启未完成，则保留较旧的日期，使条件偏向悲观：

| 注解 | 值 |
|---|---|
| `core.devicechain.io/broker-certificate-not-after` | 代理证书的到期时间，RFC 3339，UTC |
| `core.devicechain.io/broker-ca-not-after` | 签发它的证书颁发机构的到期时间 |

Operator 用自己的时钟与这些时间比较，所以即使集群中其他内容都没有变化，条件也会随时间推移而变化：它在每个边界处重新检查，且至少每小时一次。消息中写明日期，因此不会在两次检查之间变化。

`Ready` 只跟随 False 的情况。证书只是临近到期时 `Ready` 保持不变，因为实例仍在提供服务，而因一个可续期的日期就变为未就绪的状态会让读者习惯性地忽略它；证书已过期则意味着到代理的 TLS 失败，这正是 `Ready` 要表达的。`Unknown` 从不改变 `Ready`。如果工作负载同时也不健康，`Ready` 保留工作负载的原因，因为那是最先需要修复的。

启用监控的实例还会得到 `BrokerCertificateExpiringSoon` 与 `BrokerCertificateExpiryCritical` 告警，它们读取的是代理当前正在提供的证书的到期时间，而不是这些注解（参见[告警手册](./observability.md#broker-client-certificates)）。该条件覆盖未启用监控的实例，也是唯一覆盖证书颁发机构的手段。

`Instance` 是受支持的 API。可以新增状态字段、条件类型和原因；不会删除任何内容，也不会改变含义。读取方应将无法识别的条件类型或原因视为 Unknown。

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

### 读取 OPERATION 列 {#phase}

`kubectl get instances` 打印 **OPERATION** 列，其值来自声明的 `core.devicechain.io/phase` 注解。`dcctl instances list` 读取同一注解，并在 STATUS 列中用文字显示。该命令仅在引导实例的机器上工作，依据该机器的本地记录。

:::warning 操作表示意图，而不是健康状态
它记录**上次 `dcctl` 运行试图执行的操作**。写入它的流程没有检查任何 Pod，因此 `dcctl instances list` 从不报告 `running`。工作负载是否运行由 **READY** 列表示，它由 Operator 根据 Deployment 计算（参见[实例状态](#instance-status)）。
:::

| OPERATION | `dcctl instances list` STATUS | 含义 |
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

它首先检查集群的 DeviceChain Operator。如果集群的 Operator 不是当前 `dcctl` 所对应的版本，命令会拒绝执行：请使用与集群匹配的 `dcctl` 执行释放，或传入 `--force` 在警告后继续释放。完全没有 Operator 的集群会被放行，因为这正是该命令的用途。它只修改 finalizer 列表（使用 patch，而不是更新整个声明），因此需要对 `instances.core.devicechain.io` 的 `get`、`patch` 和 `delete` 权限，以及对 `customresourcedefinitions` 的 `get` 权限。

命令会打印即将执行的操作，并要求再次输入实例名称；脚本使用时可用 `--yes` 跳过提示。任何未引导该实例的机器都需要 `--kube-context`，因为它告诉 `dcctl` 哪个集群保存声明。

要移除**实例**，请运行 `dcctl destroy`。

## 职责分离 {#separation-of-concerns}

DeviceChain 刻意将每层交给一个工具：

| 层 | 工具 | 职责 |
|---|---|---|
| 基础设施 | **OpenTofu** | NATS、TimescaleDB、命名空间、入口、TLS |
| 工作负载 | **Helm Chart** | Deployment、Service 和各功能域配置 ConfigMap |
| 生命周期 | **Operator** | 监视 `Instance` 资源及其 Deployment，并写入观察到的[状态](#instance-status)；按恢复窗口清理卷快照基础备份 |
| 实例身份与配置 | **`dcctl`** | 写入 `Instance` 声明及工作负载挂载的实例配置 Secret |
| 业务配置 | 实例管理员 API / `/admin` 控制台，以及每个租户自己的 API / 控制台 | 租户及各租户自己的设置（例如品牌） |

各层在不同时间运行：

- **OpenTofu** 在安装集群时运行（`dcctl install`，配置所有实例共享的前提组件），也在引导实例时运行（配置该实例自己的消息代理和事件存储）。
- **`dcctl install`** 还会应用 Operator 及其定义，使用嵌入 CLI 的清单，而不通过其他两层。
- **Chart** 渲染工作负载。
- **Operator** 监视 `Instance` 资源和实例的 Deployment（写入的内容参见[实例状态](#instance-status)）。

集群引导从不放在应用或 Operator 代码中，而是基础设施层的工作。OpenTofu 模块位于 [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu)。它们为数据库层配置保留保护，使数据库能在应用拆除后继续存在（参见[发布与升级](./releases-and-upgrades.md#data-durability)）。
