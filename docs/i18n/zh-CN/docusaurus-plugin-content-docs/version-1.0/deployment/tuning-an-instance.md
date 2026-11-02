---
sidebar_position: 10.5
title: 调优实例
---

# 调优实例 {#tuning-an-instance}

你可以用一个 values 文件来调优实例：副本数、CPU 和内存、某个区域自身的配置。通过 `--values` 把它交给 `dcctl bootstrap` 或 `dcctl upgrade`，`dcctl` 会**把它记录在实例上**，此后每次 `dcctl upgrade` 都会自动再次应用，无需你重复指定。没有这条记录，升级会根据实例的声明重新生成其设置，你的调优就会被还原。

```yaml title="tuning.yaml"
functionalAreas:
  device-management:
    replicas: 3
    resources:
      limits:
        cpu: "3"
  event-sources:
    config:
      capture:
        publishWindow: 512
```

```bash
# 在 bootstrap 时
dcctl bootstrap local my-instance --values tuning.yaml

# 对运行中的实例
dcctl upgrade local my-instance --values tuning.yaml

# 之后每次升级都会继续应用，无需重复
dcctl upgrade local my-instance --version <new-version>
```

## 覆盖层如何与 dcctl 设置的值组合 {#precedence}

共三层，后一层覆盖前一层：

1. chart 的默认值。
2. `dcctl` 根据实例声明计算出的值：镜像、凭据、端点、配置档和区域，以及 `--compact` 或 `--ha` 这类预设所隐含的规模设置。
3. 你的覆盖层。

映射按键逐项合并，因此你只需写出想更改的键。列表或单个值会替换其下的内容。重复使用 `--values` 可叠加多个文件，后面的文件优先。

覆盖层应用于 chart 渲染实例配置时所用的同一份值，因此像 `shutdownDrainSeconds` 这样的设置既会到达 Pod，也会进入配置文档。

## 可以设置什么 {#tunable}

覆盖层只能设置下表中的值，其他一律被拒绝。

| 值 | 控制内容 |
| --- | --- |
| `replicas`、`rollingUpdate`、`terminationGracePeriodSeconds`、`nodeLossTolerationSeconds`、`shutdownDrainSeconds`、`podDisruptionBudget`、`startupProbe`、`livenessProbe` | 所有区域的规模与滚动更新 |
| `resources`、`useMeasuredRequests`、`goMemLimitPercent`、`gogc`、`goMemLimit` | 所有区域的 CPU、内存以及 Go 的内存行为 |
| `functionalAreas.<area>.replicas`、`.strategy`、`.resources`、`.measuredRequests`、`.goMemLimitPercent`、`.memoryLimitFloor`、`.gogc`、`.goMemLimit`、`.avoidEventStorePrimary`、`.eventPathSpread` | 同上，但只针对一个区域 |
| `functionalAreas.<area>.config` | 该区域自身的配置。未知的键会使服务在启动时停止 |
| `frontend.replicas`、`frontend.resources` | 控制台的 Pod |
| `metrics.alerts`、`metrics.grafanaDashboards` | 告警规则和仪表板 |
| `ingress.className`、`ingress.annotations`、`ingress.uploadMaxBodySize`、`networkPolicy.additionalAllowedCidrs`、`serviceAccount.annotations` | 入口如何对外提供，以及谁可以访问 Pod |

`functionalAreas.event-management.replicas` 虽然属于可调优区域，但它本身不可调：它由 `--ha` 设置，事件存储的连接预留量正是按它来确定的。

可调优子树中的某些值同样会被拒绝，因为它们是安全开关或连接配置，而不是容量参数：

- 在区域的 `config` 中：`deviceAuthMode`（device-management）、`auth` 块（user-management）、`resourceUrl` 和 `issuerUrl`（mcp）、`updates.allowUnverifiedArtifacts` 和 `updates.downloadBaseURL`（update-management）、`eventSources`（event-sources）、`sources`（sparkplug-ingest）、任何区域的 `tsdbConfiguration`，以及 `rdbConfiguration.sqlDebug`。键的匹配不区分大小写，与服务读取它们的方式一致。
- 在 `ingress.annotations` 中：chart 已在其入口上设置的每一个注解（你的值会替换 chart 的值）、任何 `*-snippet` 注解、`cert-manager.io/*`，以及 `ssl-redirect` 或 `force-ssl-redirect`。

`serviceAccount.annotations` 是允许的，因为 s3 对象存储的云身份（workload identity，或服务账号的 IAM 角色）正是在这里绑定的。实例的所有服务 Pod 共用这个服务账号，因此在这里绑定的角色它们全都持有：授予的权限不要超过存储桶所需。

`dcctl` 拥有的一切都会被拒绝：镜像与版本、凭据和密钥库根密钥、代理与数据库端点、入口主机和 TLS、配置档和区域集合（由 `dcctl enable-area` 更改）、对象存储设置、安全上下文，以及 `dcctl` 自行在版本之间延续的 LwM2M 设置。这里刻意采用允许列表：chart 在后续版本中新增的值，在被加入此列表之前一直会被拒绝，而不会意外变成可设置。

拒绝信息会在任何更改发生之前，列出每一个违规的键及其所在文件：

```text
--values tuning.yaml sets values dcctl does not let an overlay set, so nothing has been changed:
  image.tag: this is not an operator-tunable value: dcctl or the chart owns it (images, credentials, endpoints, exposure, the area set, security contexts)
```

在应用任何内容之前，覆盖层还会对照 chart 的 values 模式进行校验，并与实例的其他值一起渲染。类型错误的值、超过上限的请求量，或 chart 拒绝的组合，都会使本次运行在未做任何更改的情况下停止。

## 事件库容量规划 {#event-store-sizing}

事件库是唯一由流量决定增长的卷，因此需要有意识地规划。三个数字决定其大小。

- **每行存储字节数。** 一行测量数据在磁盘上约占 600 字节（含索引、未压缩）：在每秒 10,000 条读数的基准中，
  32Gi 的卷约存了 5100 万行后写满。更早的一次测量连同预写日志约为 1 KB；除非您测过自己的数据，请按较大的数字规划，直到测出自己的数据；下面的公式把每行字节数作为变量。
  事件库会对早于 `lifecycle.compressAfterDays`（默认 7）的数据块做无损压缩（在 `event-management` 配置中），
  但对最近一周没有作用：请至少按这一周未压缩的数据量规划卷大小。600 字节是未压缩的数字，
  压缩比取决于您的数据，请先实测再依赖。
- **每次读数对应的行数。** 适用于某个配置文件的每个[计算指标](../user-guide/calculated-metrics.md)，
  每次读数都会多存一行。每个公式每次读数多存一行，所以带有 N 个计算指标的配置文件每次读数存储 (1 + N) 行，所以 N 个公式最多把体积放大到 1 + N 倍。档位上限 `calculatedMetricsPerProfile` 默认是 0（关闭），最大 32；它只是上限，成本取决于实际使用。
- **保留期。** `lifecycle.retentionDays` 默认是 0，即永久保留数据。打算长期运行的实例应当设置它，
  这样卷只需容纳保留窗口加上未压缩的一周。

经验公式：未压缩窗口需要 每秒读数 × (1 + 计算指标数) × 每行字节数 × 86,400 秒 × 天数，并为预写日志、维护和增长
留出四分之一的卷空间。按实测的 600 字节、每秒 10,000 条读数计，约为每天 520 GB，所以默认的七天未压缩数据约为 3.6 TB。
再加上压缩后的历史数据和保留窗口所需的空间。

三条告警按您希望收到的顺序监视该卷：`DatabaseVolumeFillsWithinDay`（将在 24 小时内写满）、
`DatabaseVolumeFillingFast`（一小时内）和 `DatabaseVolumeCritical`（已满 95%）。如果数据库仍然停止写入，
`EventStoreRefusingWrites` 会触发，`event-management` 会保留无法存储的事件而不是丢弃它们
（见[事件库无法写入时](./observability.md#event-store-down)）。

### 在线扩卷 {#event-store-expand}

在允许卷扩容的 StorageClass 上，提高事件库 Cluster 的容量。数据库算子会就地调整每个实例的卷声明，无需重启：

```bash
kubectl -n <instance namespace> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"48Gi"}}}'
kubectl -n <instance namespace> get pvc -w
```

如果数据库 Pod 已因磁盘写满而反复崩溃重启，请在其卷声明扩容后删除该 Pod，让它在更大的文件系统上重启。
随后被保留的事件及其后的积压会入库，告警也会随空闲比例上升而恢复。还要把安装时 OpenTofu 配置中的 `timescale_storage` 设为新容量。Cluster 由该值应用：只要配置值不变，Helm 的三方合并就会保留手工修改的容量；配置值更小会让数据库算子拒绝缩容并使升级失败；灾难恢复重建则会回到配置的容量。

## 副本数与数据库的连接预算 {#connections}

每个实例的数据库登录账号被限制为其服务能够打开的连接数：每个 Pod 一个完整的连接池，并为滚动更新预留一倍。覆盖层如果提高了使用关系型数据库的区域的副本数，或该区域的连接池（`functionalAreas.<area>.config.rdbConfiguration.maxOpenConnections`），这个上限也会随之提高，`dcctl` 会在做任何更改之前向共享数据库申请更大的上限。如果集群的连接预算无法满足，本次运行会在未做任何更改的情况下停止：降低覆盖层中的值，或重新运行安装该集群时使用的 `dcctl install`，并加上取值更大的 `--max-connections`（例如 `dcctl install local --max-connections 1000`；如果集群安装时用了 `--kube-context`、`--ha` 或 `--compact`，也要一并带上）来提高预算。移除覆盖层会归还这些连接。

## 更改或移除覆盖层 {#changing}

`dcctl upgrade` 上的 `--values` 会**整体替换**已记录的覆盖层，而不是与之合并。要增加一个键，请把它与已有的键一起写进文件。要移除覆盖层、让实例回到 `dcctl` 计算的值：

```bash
dcctl upgrade local my-instance --reset-values
```

`--values` 与 `--reset-values` 不能同时使用。`dcctl enable-area`，以及两个标志都没有给出的升级，会原样保留已记录的覆盖层。

## 先看会有什么变化 {#dry-run}

`--dry-run` 会打印已记录的覆盖层与本次运行结束后的覆盖层之间的差异：

```text
  Overlay: changes to the recorded tuning:
    ~ functionalAreas.device-management.replicas: 3 -> 4
    + gogc: 400
    - functionalAreas.device-state.replicas: 2
[dry-run] would record the values overlay in the instance declaration
```

`+` 表示新增，`-` 表示移除，`~` 表示更改。

## 覆盖层保存在哪里 {#where}

覆盖层保存在集群内实例的声明上，注解名为 `core.devicechain.io/values-overlay`。任何能访问该集群的机器升级时都会用到它；不会在执行 bootstrap 的那台机器上留下文件。销毁实例会一并将其移除。

任何能读取实例声明的人都能读取覆盖层。它不能设置凭据，但区域的 `config` 是自由格式，因此请不要在其中放入机密。

## 升级拒绝已记录的覆盖层时 {#stale}

新版本可能重命名某个值，或收紧其可接受的范围。升级会在更改任何内容之前，对照目标版本检查已记录的覆盖层，不再适用时会点名相关的键并停止。可用 `--values <修正后的文件>` 修正，或用 `--reset-values` 移除。

## 应用它会发生什么 {#rollout}

覆盖层与其他所有设置经由同一个发布版本应用，因此改变 Pod 设置或某区域 `config` 的更改会滚动该区域的 Pod；运行中的 Pod 不会重新读取它。会在服务之间转移负载的调优，例如事件路径上的副本数，建议先用 `--dry-run` 演练，并在实例空闲时再推出。
