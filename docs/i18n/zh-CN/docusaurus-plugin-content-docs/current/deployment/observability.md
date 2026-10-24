---
sidebar_position: 5
title: 可观测性与指标
---

# 可观测性与指标 {#observability--metrics}

DeviceChain 内置可观测性：每个服务都提供 **Prometheus 指标**和标准 Kubernetes 健康探针。`dcctl install` 在集群上部署完整 **Prometheus + Grafana + Alertmanager** 栈，覆盖该集群上的每个实例，新安装从第一分钟起即可监控，无需另行拼装监控项目。

:::note 状态
监控栈（由 `dcctl install` 部署 kube-prometheus-stack）及事件处理运维仪表盘已经实现并经过端到端验证。指令投递仪表盘及告警规则同时发布。其余功能领域仪表盘和 OTLP 分布式追踪属于计划中的后续工作。
:::

## 每个服务暴露什么 {#what-every-service-exposes}

每个功能领域服务使用 Prometheus 客户端指标，并提供两个标准 Kubernetes 探针：

- **`/healthz`**：存活检查，判断进程是否仍能工作，还是需要重启。服务与消息代理连接永久关闭后检查失败，使 Kubernetes 重启 Pod。流读取循环若连续两分钟读取失败，期间没有代理响应，也会结束进程并由 Kubernetes 重启。此前按逐渐增加、最长五秒的间隔重试。安静流的循环每几秒检查代理是否仍响应，重新连接成功也算响应，因此相隔数分钟的两次失败算作两次，而不是一次持续的失败；长时间的代理中断在代理恢复后，也不会导致服务重启。单纯代理不可达不算读取失败：读取等待，服务持续重连，代理恢复后继续。参见[容器重启](#container-restarts)。
- **`/readyz`**：就绪检查，判断是否可接收流量。未就绪服务会被 Kubernetes Service 从流量轮转中排除，参见[部署与 Operator](./kubernetes-operator.md)。

所有 Pod 使用同一约定，因此监控栈统一抓取整个实例，无需逐服务集成。

### 消息结果 {#message-results}

大多数消费流的服务将处理完毕的每条消息计入名为 `devicechain_<area>_<loop>_messages_total` 的计数器，例如 `devicechain_eventmanagement_persist_messages_total`。其 `result` 标签说明消息的去向：

| 结果 | 消息的去向 |
| --- | --- |
| `ok` | 已处理。 |
| `invalid` | 到达时即被丢弃：无法读取、没有租户，或因内容被拒绝。 |
| `failed` | 已放弃并记录为死信，或记录到 `failed-events` 流。 |
| `dropped` | 最后一次投递后放弃，且有意不记录，因为它本应更新的内容可以重建。 |
| `retry` | 处理因可能消失的原因失败，留待再次投递。 |
| `erased` | 租户已删除，服务的数据库拒绝了写入。消息在到达的那次投递中即被了结，不在任何地方记录。参见[租户删除](./tenant-deletion.md#database-writes)。 |

服务启动时即以零导出每个结果，因此基于其中某个结果增长的告警能看到计入的第一条消息，而不只是第二条。唯一例外是 Prometheus 首次抓取该 pod 之前就已计入的消息：该序列首次出现时即为 1，不算增长。

服务还可在独立监听器上提供 Go 运行时性能分析，默认关闭，参见[服务性能分析](#profiling)。

## 日志 {#logs}

每个服务将结构化 JSON 日志写到 stderr，每行一个对象。每行携带来源 `instance` 和 `area`；Pod 仅服务单租户时还携带 `tenant`，日志管道无需解析消息文本即可筛选。

### 日志级别 {#the-log-level}

实例配置 `infrastructure.logging.level` 统一设置整个实例的日志量，只接受以下小写值：

| 级别 | 内容 |
| --- | --- |
| `trace` | 所有内容，包括最细致诊断。 |
| `debug` | 诊断信息，其中部分在接入路径每条消息记录一次。 |
| `info` | **默认。** 启动、关闭、配置和重要事件。 |
| `warn` | 仅警告和错误。 |
| `error` | 仅错误。 |

其他值，包括 `INFO`、数字或包含空格的值，都会拒绝：服务不启动，日志列出配置键和允许值。没有关闭错误日志的级别。

`debug` 和 `trace` 用于诊断，不适合长期运行。接入路径每条设备消息一行，生产速率下会显著放大日志管道负载。

每个服务以 `info` 启动，在启动早期读完实例配置后立即切换到配置级别。切换前记录一行说明目标级别，因此即使目标为 `warn` 或 `error` 也能看到。

级别属于挂载配置，运行中不重新加载。改变它会改变配置校验和，使 Pod 重启并使用新值。

**谁可以修改，取决于实例安装方式：**

- **通过 `dcctl bootstrap` 安装：** 实例使用 `info`，`dcctl` 尚无更改级别选项。不要手动编辑配置 Secret 绕过：`dcctl` 自己写入该 Secret，下次运行会替换它；外部编辑也不会重启 Pod。
- **直接通过 Helm chart 安装：** 与其他 values 一起设置，例如 `--set instance.config.infrastructure.logging.level=debug`。
- **chart 使用 `instance.existingSecret`：** 在提供文档的 `infrastructure` 下添加 `logging.level`，并将 `instance.existingSecretChecksum` 更新为新文档校验和。校验和触发重启，未更新就不会应用新级别。

:::note 过去默认启用调试输出
级别可配置之前，每个服务都使用 `debug`，不论是否请求。跨该变更升级后，默认日志不再包含接入逐消息记录、代理读取写入确认等诊断。数据没有丢失，设置 `debug` 即可再次查看。
:::

### 在所有级别下都会写入的日志行 {#audit-lines}

有一类日志行不受级别影响：`mcp` 服务为每次 AI 工具调用写入的记录。它不带 `level` 字段，在 `warn` 和 `error` 下同样会写入，因为降低日志详细程度并不意味着决定停止审计。请按其 `audit` 字段筛选；其格式见[记录的内容](../concepts/mcp.md#audit)。

### 永远不会写入日志的内容 {#what-is-never-logged}

任何级别都不会记录服务自身配置文档，而是记录短哈希：`config_sha256`，即 SHA-256 前 16 个十六进制字符。要确认 Pod 使用哪份配置，对渲染配置 ConfigMap 中该服务条目计算哈希并比较。

### 数据库消息 {#database-messages}

数据库活动使用同一日志器，JSON 行携带 `instance`、`area`，并按 `infrastructure.logging.level` 过滤。失败语句以 `error` 记录（`database statement failed`，数据库消息位于 `error`）；超过 200 毫秒以 `warn` 记录（`slow database statement`）。每行包含语句 `sql`、受影响行数 `rows`、毫秒时长 `elapsed_ms` 和调用代码 `caller`。没有查到行不等于失败，不记录为失败；仅在缓慢或 `sqlDebug` 开启时记录。

记录每条语句由服务 datastore 配置中的独立开关 `sqlDebug` 控制，日志级别为 `info`，因此 `warn` 或 `error` 会隐藏。

任何级别、包括启用 `sqlDebug` 时，`sql` 都只显示占位符（`$1`、`$2`、…），不显示绑定值。但数据库自身错误消息原样记录，部分会引用被拒绝值，例如 `invalid input syntax for type uuid: "…"`。

## 监控栈 {#the-monitoring-stack}

[`dcctl install`](./bootstrap.md#install) 通过内嵌 OpenTofu 模块配置监控，**默认启用**；`--no-monitoring` 或 `--compact` 会省略。配置关系数据库、cert-manager 和 ingress 的同一层也部署 [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)，包括 Prometheus、Grafana 和 Alertmanager。每集群安装一次，监控该集群上初始化的所有实例：

- **跨命名空间抓取。** Prometheus 位于独立命名空间，跨命名空间抓取各实例服务，一个栈覆盖整套部署。
- **仪表盘随平台发布。** Grafana 面板位于 chart 的 `deploy/helm/devicechain/dashboards/`，由仪表盘 sidecar 自动导入。新增仪表盘属于 chart 变更，不需手动导入。
- **每实例一个文件夹。** 每实例有独立 `devicechain-<instance>` 文件夹，包含自己的全部面板副本。副本限定实例、标题包含实例 ID，无需选择实例。`dashboards/` 下是按实例渲染的模板，不用于手动导入。文件夹来自 ConfigMap 的 `grafana_folder: devicechain-<instance>` 注解。sidecar 必须设置 `FOLDER_ANNOTATION=grafana_folder`，提供器必须设置 `foldersFromFilesStructure: true` 才按文件夹组织；`dcctl install` 部署的栈已设置。如果安装时使用了 `--no-monitoring`，并让自己的 Grafana sidecar 读取这些 ConfigMap 却没有设置这两项，注解会被忽略，各实例的面板会平铺在同一处：仍是各自独立的面板、仍各自限定在自己的实例，只是不在文件夹中。
- **同集群实例的面板互不干扰。** 所有实例升级到支持独立文件夹的 chart 后，移除或升级一个实例不影响其他面板。此前旧 chart 实例仍在文件夹外共享每种仪表盘一个文件，升级任一实例会移除该共享文件；旧实例面板会缺失，直到 sidecar 下次重新扫描。监控栈早于此布局的集群，需要重新运行 `dcctl install`，否则所有实例面板仍在文件夹外。
- **旧面板链接失效。** 升级实例不再使用原共享 ID `dc-event-processing-ops`、`dc-command-delivery-ops`；全部升级后，旧书签失效。
- **删除实例留下空文件夹。** 面板移除，但空的 `devicechain-<instance>` 文件夹仍留在 Grafana，需手动删除。

## 登录 Grafana {#signing-in-to-grafana}

Grafana 使用自己的**管理员登录**。Grafana 没有与 DeviceChain 登录集成：无论是 DeviceChain 密码，还是已连接的身份提供商（参见[单点登录](../concepts/single-sign-on.md)），都不能用来登录 Grafana。

指标为实例级且跨租户，因此 Grafana 是*运营方*界面，不向租户用户开放。租户通过控制台和仪表盘查看自身数据，不通过 Grafana。

### 访问 Grafana {#reaching-grafana}

`dcctl install` 部署的栈不为 Grafana 发布 ingress 路由，Service 类型是 `ClusterIP`。转发端口后打开 `http://localhost:3000/`：

```bash
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
```

### 管理员密码 {#the-admin-password}

以 `admin` 登录。`dcctl install` 生成密码，保存在 `monitoring` 命名空间的 `dc-grafana-admin` Secret 中，键为 `admin-password`：

```bash
kubectl -n monitoring get secret dc-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
```

安装报告打印相同两条命令。Grafana **每集群运行一次**，因此这是*集群*登录，由其全部实例共用，不是每实例一个密码。轮换会让该集群所有实例的运营人员需要重新登录，不只影响你的实例。

### 轮换密码 {#rotating-it}

重新运行 `dcctl install` 会**保留**密码：读取并复用已有 Secret，只有 Secret 不存在时生成。手动编辑 Secret 也不完成轮换；Grafana 启动时将 Secret 密码读入环境变量，修改 Secret 不重启任何组件，Grafana 仍接受旧密码。

因此 Secret 可能写着 Grafana 从未见过的新密码。主动轮换需执行全部三步：

```bash
kubectl -n monitoring delete secret dc-grafana-admin
dcctl install <the flags the cluster was installed with>   # a missing Secret is minted afresh
kubectl -n monitoring rollout restart deployment/kube-prometheus-stack-grafana
```

:::caution
不要省略重启。前两步之后，Secret 已有新密码，但 Grafana 仍接受旧密码，轮换看似完成却没有生效，下一个读取 Secret 的人无法登录。重启才应用新值；这可行是因为默认 Grafana 没有持久数据库。
:::

## 事件处理运维仪表盘 {#the-event-processing-operations-board}

DETECT/REACT 引擎（参见[事件处理](../concepts/event-processing.md)）最需要运营方监控，随平台提供独立 Grafana 仪表盘和告警。指标包括**消费滞后**，即检测落后解析事件流多少，以及**规则触发计数**，可快速回答告警引擎是否跟上、正在做什么。

## 消费者从未读取的消息 {#unread-loss}

每个 JetStream 流都有上限。满时丢弃**最旧**消息以接收新消息，保持接入运行。消费者尚未读取就被丢弃的消息，永远不会读到。两条承载设备数据的接入流，在关键消费者可能丢失内容前拒绝新事件，参见[接入链路背压](#ingest-backpressure)。其他流和消费者由本节覆盖。代理不报告此损失，因此各服务按读取的每个持久消费者测量，告警如下：

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `JetStreamDurableUnreadNearFull` | warning | 消费者未读内容超过流容量 80%，持续五分钟，尚未丢失。已读消息不算，保存已处理历史的满流不触发。未读积压到达上限后，最旧消息丢弃，尚未读取的内容无法处理。对事件处理检测消费者，测量的是消费者而非重放检查点；检查点不落后于已确认位置，因此该值至少覆盖检测可能丢失内容。 | 查服务日志、数据库和 `JetStreamDurableFallingBehind`。流不足时，提高流上限并同步扩大 JetStream 卷。两条背压消费者改由 `JetStreamUnreadBacklogNearFull` 覆盖，见[背压](#ingest-backpressure)。 |
| `JetStreamCaptureBacklogWillOverflow` | critical | 捕获流保存来自 MQTT 设备的事件，直到 `event-sources` 转发它们。按其未转发积压在过去三分钟的增长速度，该流将在十分钟内写满，且积压已至少达到上限的 10%。代理已经向设备确认了其中每一个事件，因此无法拒绝它们：流一旦写满，就会丢弃最旧的未转发事件（`JetStreamDurableLostUnread`），而设备永远不会得知。它比 `JetStreamDurableUnreadNearFull` 早几分钟触发，因为快速增长的积压可能在不到其 5 分钟的时间内从空到开始丢弃。 | 设备发布的速度超过了 `event-sources` 转发的速度。如果 `event-sources` 的 CPU 并不繁忙，限制在于其发布窗口：`devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` 每秒增长接近一秒。增加 `event-sources` 副本，每个副本都有自己的窗口：`capture.publishWindow` 默认已是其最大值 1024，只有在被调低过时才需调高。如果 `JetStreamIngestBackpressureEngaged` 正在触发，慢的消费者在更下游。若需争取时间，请同时调高 `streamMaxBytes` 和 JetStream 卷；如果流先按消息数写满（负载较小时），也要调高 `streamMaxMsgs`。捕获流会完整使用这些上限，`event-sources` 在重启时应用它们。 |
| `JetStreamCaptureBacklogGrowing` | warning | `JetStreamCaptureBacklogWillOverflow` 的早期预警。捕获流保存来自 MQTT 设备的事件，直到 `event-sources` 转发它们。按其未转发积压在过去五分钟的增长速度，该流将在二十分钟内写满，且积压已至少达到上限的 10%。代理已经向设备确认了其中每一个事件，因此无法拒绝它们：流一旦写满，就会丢弃最旧的未转发事件（`JetStreamDurableLostUnread`），而设备永远不会得知。在一次负载测试中，严重级别的告警在丢失开始前两分钟触发；此告警则为先行扩容留出时间。 | 设备发布的速度超过了 `event-sources` 转发的速度。请立即增加 `event-sources` 副本，每个副本都有自己的发布窗口，并检查其 CPU 以及 `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}`。如果 `JetStreamIngestBackpressureEngaged` 正在触发，慢的消费者在更下游。若需争取时间，请同时调高 `streamMaxBytes` 和 JetStream 卷。 |
| `JetStreamCaptureLeaderChanged` | warning | 过去 15 分钟内，捕获流在有事件到达时更换了主节点（`devicechain_*_jetstream_stream_leader_changes_total`，由每 30 秒读取的代理流信息计数，并要求同一时段内 `devicechain_eventsources_total_inbound_messages` 有增长）。流没有主节点的那几秒里，代理的 MQTT 网关仍会向设备确认写入该流的 QoS 1 发布，却没有把它们存到任何地方，也没有任何计数器记录：**切换期间发布的 MQTT 事件可能已被确认却丢失**，且 `JetStreamDurableLostUnread` 不会因这种丢失触发。在每秒 12,000 个事件的测试中，一次这样的主节点退位丢失了约五秒的事件。默认设置还有第二种丢失已确认事件的方式，本告警不涵盖，且不需要更换主节点：如果 `event-sources` 在持续负载下落后，直到捕获流达到上限，该流会丢弃最早的未读消息，由 `JetStreamDurableLostUnread` 报告（来自 `devicechain_<area>_jetstream_consumer_unread_skipped_total`）。短于一个采样周期且回到同一服务器的切换不会被发现。代理的滚动重启（包括升级时的滚动）同样会移动主节点，并且无论流量大小都可能以同样的方式丢失事件；由于它会在例行重启时触发，因此这是警告级别。开启 `infrastructure.nats.mqttFailClosed` 后，QoS 1 事件由代理的消息存储保存，不会以这种方式丢失；只有 QoS 0 事件会。该限制见 [MQTT：平台代理](../reference/transport-matrix.md#mqtt--the-platform-broker)。 | 在代理日志中查找 `Leader falling behind, stepping down`：这表示主节点所在节点过载。检查代理节点的 CPU，以及捕获流的主节点是否与 inbound-events 的主节点在同一节点上（`nats stream cluster step-down --preferred` 可移动它）。丢失的事件无法恢复；切换发生的时间界定了需要在设备自身记录中核对的范围。请在低流量时段安排计划内的代理重启。 |
| `JetStreamStreamNearFull` | info | 保存运营方记录而非供服务处理消息的流，字节或消息占用超过 80% 持续十分钟，包括 `failed-decode`、`failed-events`、`connector-dispatch.dead`、`max-deliveries`，以及用户管理未报告读取时的 `dead-letters`。无人处理这些记录，满后会丢弃尚未查看的内容。服务读取的流不在此范围，因为通常保留一周历史、常接近上限，改监控消费者。 | 找到填满原因：拒绝载荷的解码器、解析/存储失败事件、目标拒绝所有发送的连接器。`dead-letters` 检查用户管理是否运行。仅在需要更长保留时提高上限。kube-prometheus-stack 默认 Alertmanager 抑制 `info`，要接收需明确路由。 |
| `JetStreamDurableLostUnread` | critical | 消费者越过读取前已被移除的消息，这些消息从未处理。 | 租户正在删除时属预期，因为删除移除了未读内容。否则是消费者落后时流已满：容量不足，或消费慢于生产。 |
| `JetStreamDurableStalledBehindStream` | critical | 消费者至少两分钟未获得消息，流却已丢弃它前面的消息。仍在读取的慢消费者不触发，其损失由 `JetStreamDurableLostUnread` 报告。 | 服务仍运行才能报告，但消费者不读取。检查处理是否卡在数据库等依赖，或 Pod 等待就绪。无法快速修复时，提高流上限以停止丢弃。 |

上限位于 `instance.config.infrastructure.nats`：高吞吐流使用 `streamMaxBytes`，其他使用 `streamMaxBytesCold`，以及 `streamMaxMsgs`。JetStream 卷按总和配置，因此提高上限时同步扩卷，参见[初始化实例](./bootstrap.md)。

告警读取以下序列，各服务为读取的每个持久消费者导出前三项：

- **`devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`**：未经读取就越过的消息数。这是下界；重新投递或消费者后方消息被删除会少计，不会多计。
- **`devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`**：自上次采样（每 30 秒）以来未收到消息的消费者前方已丢弃消息数。消费者读取时为 0，即使落后；此时损失由计数器统计。恢复读取后降至 0，由上一指标接替。
- **`devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}`**：未读积压（待投递加未确认）除以流容量，采用消息或字节中更紧的限制。两条控制背压的消费者不导出此指标，其写入服务改以 `jetstream_backpressure_unread_ratio` 导出同值。首次采样前或无法测量时不存在。

每个服务还为读写的每个流导出 **`devicechain_<area>_jetstream_stream_sink{stream}`**，运营方记录流为 1，其他为 0。`JetStreamStreamNearFull` 仅在该值为 1 时读取填充率。

前两项在服务创建消费者读取器时即以 0 存在，比率首次采样才出现。各副本报告同一消费者、统计同一损失，应用 `max` 合并，不用 `sum`。Pod 重启重置计数器，应使用 `increase()` 或 `rate()`。每个 Pod 从自身首次采样计量，因此所有读取服务 Pod 同时重启期间越过的损失可能未计。服务没有运行 Pod 时不报告这些序列，逐消费者告警无法触发，由 Pod 健康告警覆盖。

## 消费者持续落后 {#consumer-backlog}

消费者可能没有丢失消息，却严重落后；服务从流推导的状态也同样陈旧。每个服务对读取的每个持久消费者，每 30 秒采样等待量：

- **`devicechain_<area>_jetstream_consumer_pending_messages{stream, durable}`**：流中尚未交给消费者的消息。
- **`devicechain_<area>_jetstream_consumer_ack_pending_messages{stream, durable}`**：已交付但尚未确认的消息。

两项在启动后的首次采样出现，无法读取消费者时再次消失。序列缺失表示“未测量”，绝不表示“没有等待”。各副本报告同一消费者，以 `max` 合并。突发期间积压增长又减少正常，告警针对持续积压。另一个副本持续报告时，单个 Pod 重启不会重置十五分钟告警；单副本重启则在新首次采样前移除序列，十五分钟重新计时，因此积压下反复重启可能永不触发，同时监控重启计数。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `JetStreamDurableFallingBehind` | warning | 等待消息超过 10000 持续十五分钟。服务派生状态同样落后；例如设备实时状态落后已存事件。检测消费者不覆盖，因为由 `DetectConsumerBacklogHigh` 监控，接管时本就重放。 | 比较消费与生产速率。能跟上却追不上时增加容量，设备状态参见[配置](#live-state-projection)和数据库。完全停止时查 `JetStreamDurableStalledBehindStream` 和日志。追上前流满会丢弃未读消息，但两条[背压](#ingest-backpressure)消费者会先拒绝新事件。 |

## 接入链路背压 {#ingest-backpressure}

两条流承载平台处理前不能丢失的事件：`inbound-events`（等待解析）和 `resolved-events`（等待存储）。分别有一个消费者必须保护：设备管理读取前者，事件管理读取后者。对于这两个消费者，平台拒绝新事件，而不是丢弃其未读事件。

- **未读**积压达到流容量 **90%** 时，写入服务开始拒绝，低于 **80%** 后恢复。只统计未读，已处理且保留一周的事件不算。
- 满流先丢弃最旧事件，所以未读事件之前的已处理历史会先耗尽。若按当前丢弃速率，历史将在 **30 秒**内耗尽，服务也拒绝。该时间不依赖事件大小，因此大量大事件冲击小事件历史时，未读仍远低于 90% 就可能拒绝。历史足够维持**一分钟**，或消费者读完全部内容时恢复。消费者跟得上的满流不拒绝，因为丢弃内容被已处理事件替换。例外是容量不足以保留自身约一分钟流量的满流：消费者落后几秒就可能触发，只能等全部读完才恢复。小流上限，例如 `--compact` 初始化，以及大量快速到达的大事件，更容易出现。
- 已交付但未确认也算未读，包括失败后等待重投递。此事件位于满流前端时，会阻止新事件，直到确认或放弃，而不是丢弃它。
- 各服务每五秒测量一次，自上次测量写入约容量千分之一后也立即测量，最快每 100 毫秒一次。
- 三十秒无法测量积压时，视为已满并拒绝。
- `resolved-events` 拒绝时，设备管理停止读取 `inbound-events`；后者拒绝时，事件来源停止读取 MQTT 捕获流。积压留在上游，不消耗消息尝试次数。
- 流是共享的，因此拒绝作用于**所有租户**。每个租户先受自己的接入限额约束（按读数计），然后才会经过共享门控。在默认的每秒 1000 读数下，租户自己的设备单独发送的量，不会超过默认高可用部署在[默认允许量](../concepts/governance.md#ingest-default)所述集群上、使用一个 `event-sources` 副本时实测的存储能力。但多个租户合计仍可能超过，层级提高了限额的租户、较小集群上的任何租户，以及用许多租户名称通过 HTTP 提交的客户端也可能超过，因为名称在验证其凭据之前就已计量。此时门控会拒绝所有人。
- 满流代理仍会丢弃最旧消息，三种情况仍可能丢失未读事件：两次测量之间写入量耗尽的历史超过前 30 秒观测的丢弃量；流首次满时几乎没有已处理历史，尚未测得丢弃速率；拒绝期间仍接受连接/断开转换，见下文。[未读损失告警](#unread-loss)会报告。
- 删除租户移除两条流中的事件，释放空间，一般不触发拒绝。例外是删除满流最旧、且数量少到删除后仍满的事件，看起来像历史快速被丢弃，可能暂时拒绝直到消费者追上，不会丢失。

拒绝期间各传输行为：

| 传输 | 设备所见 |
| --- | --- |
| HTTP | 先通过租户限额后返回 `503` 和 `Retry-After: 10`；超租户限额返回 `429`。事件未存储，应重试。 |
| MQTT（平台代理） | 无提示，代理在平台能拒绝前已确认。消息在捕获流等待，流满时丢弃最旧消息（`JetStreamDurableLostUnread`）。开启 `infrastructure.nats.mqttFailClosed` 后，QoS 1 消息改为在代理的消息存储中等待，存储写满后会拒绝新的发布：设备收不到 PUBACK，会重新发送该消息（见 [MQTT 故障关闭](#mqtt-fail-closed)）。 |
| 外部 MQTT 代理 | 无提示，消息已确认。丢弃并计入 `devicechain_eventsources_total_msg_backpressured{source}`；超租户限额改计入 `devicechain_eventsources_total_msg_rate_limited`。 |
| Sparkplug | 读数不重试、直接丢弃，计入 `devicechain_sparkplugingest_ingest_failures_total`。 |
| LwM2M | 通知丢弃，计入 `devicechain_lwm2mingest_notify_ingest_dropped_total`，下一通知取代丢失内容。 |

代理连接通知、Sparkplug birth/death、LwM2M 注册等转换在拒绝期间仍接受，因为拒绝后无人重新发送。它们使用拒绝阈值以上保留的 10% 空间。背压不限制接受量；代理监听仍受租户限额约束。设备自行决定连接频率，循环重连设备群可能耗尽余量，代理随后会像本版本之前一样丢弃最旧事件，包括未读内容。只有两个关键消费者控制背压，设备状态或事件处理慢不会控制，仍由上述未读损失告警报告。事件处理实际位置在自己的检查点，门控不可见，由 `ReplayCoveredDeliveriesExhausted` 监控。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `JetStreamUnreadBacklogNearFull` | warning | 关键消费者落后超过流容量 80%，持续五分钟。90% 时为所有租户拒绝新事件：HTTP 返回带重试头的 `503`，MQTT 在捕获流等待，Sparkplug、LwM2M 和外部 MQTT 丢弃并计数。 | 查服务日志、数据库、`JetStreamDurableFallingBehind`。流不足时，提高上限并同步扩卷。 |
| `JetStreamIngestBackpressureEngaged` | critical | 流对所有租户拒绝新事件持续一分钟：未读接近上限，或历史将在 30 秒内耗尽，或写入服务三十秒无法测量积压。 | `JetStreamUnreadBacklogNearFull` 标识落后消费者，最常见是服务缩为零或崩溃循环。已部署却未运行仍有意阻止接入。若该告警安静，通常是历史耗尽；查看 `jetstream_backpressure_history_runway_seconds` 和写入服务日志中的消费者与速率。积压低于 80%，且历史足够一分钟或全部读完后自动恢复。均未标识消费者时，可能写入服务无法连接代理，见[代理连接静默失效](#broker-connection-dead)。 |

两条流的写入服务导出：

- **`devicechain_<area>_jetstream_backpressure_unread_ratio{stream, durable}`**：未读积压（待投递加未确认）除以容量，采用消息或字节中更紧限制。无法测量时缺失，Pod 用 `max` 合并。
- **`devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`**：按满流近期丢弃速率，未读前方已处理历史可维持多久。流未满、未丢弃或已全部读完时为 `+Inf`。低于 30 拒绝，达到 60 恢复。无法测量时缺失，Pod 用 `min` 合并。
- **`devicechain_<area>_jetstream_backpressure_engaged{stream}`**：拒绝期间为 1，包括无法测量积压时。Prometheus 抓取时读取，因此实际拒绝时不会错误显示 0。
- **`devicechain_<area>_jetstream_publish_refused_total{stream}`**：因拒绝而未发布的消息数。

## 消息代理连接静默失效 {#broker-connection-dead}

代理主机突然停止、重启或网络分区时，不会主动关闭连接；客户端连接仍开放，只是不再响应。各服务每十秒 ping，连续三个间隔无响应（30 秒），或写入十秒没有进展时放弃连接，因此 40 秒内发现失效。服务写入期间代理停止读取十秒，也同样放弃。随后通常几秒内重新连接。隔离期间既不能发布也不能接收；事件来源的 HTTP 接入返回 `503` 或超时，[背压门控](#ingest-backpressure)因无法测量积压而报告启用。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `BrokerConnectionDiedSilently` | warning | 过去十五分钟，服务放弃无响应的代理连接并重新连接。 | 检查 `kubectl get nodes` 和云提供商 VM 事件。主机在约 40–50 秒节点宽限内重启，Kubernetes 不记录。运行 `dcctl ha verify`。单服务反复出现且无节点事件时，检查服务与代理之间网络。 |

- **`devicechain_<area>_nats_connection_dead_total{detected_by}`**：放弃连接数。`ping` 表示 ping 未响应，`write` 表示写入十秒无进展。Pod 用 `sum` 合并。
- **`devicechain_<area>_nats_connection_dead_last_timestamp_seconds`**：最近放弃时刻的 Unix 秒，启动后未发生则为 0。告警读取此值，因此即使 Prometheus 在失效节点上、错过计数增长时刻，仍能触发。

服务日志说的是同一件事并指明服务器：`Disconnected from NATS: the server left 2 pings sent 10s apart unanswered…` 或 `Disconnected from NATS: a write to the server made no progress for 10s…`，随后 `Reconnected to NATS`。事件来源持有的第二条代理连接（用于观察设备与代理的连接）受同样的限制，但不计入这些指标，其日志为 `A write to the NATS server made no progress` 或 `Lost the NATS system-account connection`。

## 无人读取的外部 MQTT 来源 {#external-mqtt-owner}

你运行的外部 MQTT 代理来源，任一时刻只由一个事件来源 Pod 读取，其他备用 Pod 仍就绪，因为继续提供其他功能，参见[传输能力矩阵](../reference/transport-matrix.md#external-mqtt-broker)。无人读取来源不一定是 Pod 故障，可能接管 Pod 无法连接你的代理、每十五秒重试，或 Pod 无法连接平台代理来协商所有权。

- **`devicechain_eventsources_external_mqtt_owner{source}`**：读取来源的 Pod 为 1，其他为 0。每个 Pod 从启动起为每个外部来源导出，以 `sum` 合并，1 健康。
- **`devicechain_eventsources_total_msg_not_owner{source}`**：失去所有权后仍投递给旧 Pod 的消息，直接丢弃不存储，因为接管 Pod 收到相同消息。
- **`devicechain_eventsources_total_msg_publish_failed{source}`**：已从你的代理读取并解码，但因发布到平台事件流失败而未存储的来源事件。该来源不重试，因此每一条都会丢失。Pod 日志为每条记录一行，注明来源和租户。速率大于 0 表示平台自身的代理在拒绝或无响应，应检查平台代理，而不是你的代理。

来源以一个“窗口”的方式发布：同时有多条事件等待平台代理确认，而不是每个解码 worker 一次只等一条确认。在有复制的代理上（一次确认约 11 毫秒），后者会把一个来源限制在整个集群约每秒 440 条事件，因为每个来源只由一个 Pod 读取。

| 设置（`event-sources` 配置） | 默认值 | 作用 |
| --- | --- | --- |
| `externalMqtt.publishWindow` | `128` | 一个外部 MQTT 来源同时等待平台代理确认的事件数，范围 1 到 1024。超出范围或显式写 0，服务启动时即停止，不会被调整。发布速度不会超过该数值除以一次确认耗时。固定设置，启动时读取。**它同时决定 Pod 最多还会多丢多少。**该传输为至多一次：平台把消息放入解码队列后，就告知你的代理消息已到达，突然停止的 Pod 会丢失已取走但尚未存储的内容，即最多 105 条排队或解码中的消息，加上最多该数值条等待确认的事件。正常停止或把来源交给其他 Pod 时，会先等待这些事件存储完成（最多 8 秒）。可观察 `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="external-mqtt"}`：速率接近 1 表示窗口是发布的瓶颈。 |

总和两分钟不为 1 时，`ExternalMqttSourceNotReadByOnePod`（warning）触发。0 表示无人读取，期间外部代理投递内容丢失；最近接管 Pod 日志说明原因。没有外部来源的安装不导出此序列，告警安静。

来源因发布失败持续丢失事件（`devicechain_eventsources_total_msg_publish_failed` 上升）十分钟时，`ExternalMqttSourceLosingEvents`（warning）触发。这些事件已从你的代理读取，已经丢失，应检查平台的消息代理，而不是你的代理；event-sources 日志注明每条的来源和租户。单次突发（例如停止时放弃等待而仍在途的事件）会自行消退，不会触发。

## 超过确认窗口的消息 {#held-past-ack-wait}

代理为每条交付消息提供固定确认窗口。窗口关闭仍未确认，会重新交付，服务按新消息处理。告警通知和对外连接器的工作是向平台外慢速发送，因此可能重复通知或调用 webhook。两者仅在有空闲 worker 时读取相应数量消息，避免排队消耗窗口；每次发送也会在窗口结束前留有余量地截止。下列告警报告仍超时的情况。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `ReaderHeldMessagePastAckWait` | warning | 处理器持有消息超过窗口并重投递。`stage=worker` 表示发送太久，可能发送两次；`stage=buffer` 表示交付 worker 前消息被丢弃，随后处理重投递版本。 | worker 情况检查 `durable` 指定服务的慢或无响应目标；buffer 情况表示服务跟不上流。 |

指标为 `devicechain_<area>_reader_held_past_ack_wait_total{durable, stage}`，读取器创建时就以 0 存在，`increase()` 能看到 Pod 首次发生。每 Pod 仅统计自己持有内容，用 `sum`，不用 `max`。

## 提前拉取 {#fetch-ahead}

默认情况下，服务从代理拉取 256 条消息，并在处理手头消息的同时请求接下来的 256 条，因此每次拉取所花的时间与处理重叠，而不是叠加到处理时间上。开启它是为了让检测引擎的积压保持较小，而不是为了提高吞吐量：开启后，排在检测之前的待处理事件数保持在数百，而关闭时峰值可达数千，CPU 没有额外开销，平台可持续的速率也没有变化。`infrastructure.nats.fetch` 下的设置可以改变这一点。这些设置在服务启动时读取，因此修改后需要重启，并且不会改动代理上的任何消费者。

| 设置 | 默认值 | 作用 |
| --- | --- | --- |
| `batch` | 256 | 每次拉取请求的消息数，范围为 1 到 256（其他值在启动时被拒绝）。更大的批次把一次拉取的耗时分摊到更多消息上。它也是一次连接中断最多可能从单次拉取中丢失的数量，检测引擎会在继续之前从流中重新读取这一范围。 |
| `ahead` | 开启 | 在交出当前批次的同时请求下一批，使拉取与处理重叠。设为 `ahead: false` 则每次只拉取一批。消息仍按到达顺序处理。 |
| `aheadHoldBudgetMillis` | 1000 | 提前请求的批次要等前一批处理完，而它的确认窗口已经开始计时。如果最近的批次交出所需的时间超过该值，服务会停止提前请求，回到每次一个请求。最大为确认窗口的十分之一，否则服务拒绝启动。 |

只按空闲工作线程数读取消息的服务（告警通知和出站连接器）会忽略这些设置。服务停止时，已提前请求的批次不会被确认，因此在其窗口关闭后，代理会把它交给下一个读取者。

## 让流量留在同一台代理服务器上 {#leader-affinity}

在集群化的代理上，每个流以及读取它的每个消费者都由某一台 NATS 服务器担任领导者。连接到其他服务器的服务，对它们所做的一切都要多付一次服务器之间的转发：每次发布都被转发给流的领导者，其确认再被转发回来；每次投递和确认都要在消费者的领导者与读取方所在的服务器之间往返。在负载下的三服务器集群上，这些被转发的消息约占代理处理总量的四分之一，而代理的工作量随消息数量而不是消息大小增长。`infrastructure.nats.affinity` 的两个设置消除了这些转发。`publishToLeader` 默认开启，`consumerLeaders` 默认关闭；两者都在服务启动时读取，都不会改变存储什么、以什么顺序存储或确认什么。

| 设置 | 默认值 | 作用 |
| --- | --- | --- |
| `consumerLeaders` | 关闭 | 当消费者的领导者连续三次采样（约一分半钟）都位于与读取方不同的服务器上时，服务请求代理把领导者移到读取方所在的服务器。代理不健康时不会请求：流或消费者没有领导者或有落后的副本时，以及服务重新连接后的两分钟内。同一个消费者每分钟最多移动一次；当服务的副本分散在多台服务器上、没有任何一台占多数时，领导者保持不动。只有一个副本发出请求，其他副本不会。 |
| `publishToLeader` | 开启 | 同时有大量发布在途的服务（`device-management` 发布的已解析事件，以及 `event-sources` 转发的设备事件）会另开一条连接到领导该流的服务器。代理选出新领导者后，它们在在途发布全部确认之后切换到新领导者。如果这条连接无法建立或中断，它们就通过原来的连接发布，与关闭此设置时相同。 |

将 `publishToLeader` 设为 `false` 可关闭发布这一半，将 `consumerLeaders` 设为 `true` 可开启另一半。在负载下的三服务器集群上，向领导者发布使代理每个事件处理的消息从约 11 条降到 9 至 10 条，`device-management` 等待代理确认一个已解析事件的中位时间缩短了三分之一到将近一半，单个 `device-management` 副本能解析的最大事件量提高约 6%。在强制的领导者选举、滚动重启和扩容过程中，它持续发布并切换到新的领导者。移动消费者的领导者会短暂暂停该消费者的投递，而在同一集群上它按上述方式工作，却没有多解析任何事件，因此 `consumerLeaders` 保持关闭。单服务器代理上两者都无事可做。

- **`devicechain_<area>_jetstream_consumer_leader_local{stream, durable}`**：消费者的领导者是本服务所连接的服务器时为 1，是其他服务器时为 0。无法读取消费者时不存在。多个副本从不同服务器读取同一消费者时，处于少数的副本读数为 0，这是预期的。
- **`devicechain_<area>_jetstream_publish_leader_local{suffix, writer}`**：该流上所有同名的此类写入方都通过连接到领导者的连接发布时为 1，有任一写入方经由其他服务器发布时为 0。`writer` 与发布窗口指标所带的名称相同。
- **`devicechain_<area>_jetstream_consumer_leader_moves_total{stream, durable, result}`**：本服务向代理请求的移动次数。`result="moved"` 表示已完成的移动；`result="failed"` 表示代理拒绝或未应答的请求，未做任何改变。

关闭设置时这两个指标仍会导出，因此关闭某个设置可以看出它原本在做什么。

## 投递次数耗尽的消息 {#max-delivery-records}

五次未确认投递后，代理停止交付。最后一次确认窗口结束后，下次拉取消费者时才发布通知，因此服务停机时要等恢复。平台流 `max-deliveries` 捕获通知，各服务将自己的通知转为 `no-outcome` 死信，可用 `dcctl dead-letters list` 查看。该流为工作队列，已记录通知删除，健康实例应为空。`devicechain_<area>_max_delivery_records_total{stream,outcome}` 说明各通知处理结果。

明确声明的例外是事件处理中的检测引擎，它从自己保存的检查点读取 `resolved-events`，检查点覆盖事件后才确认，重启后也从最近检查点重读，因此耗尽投递次数不代表丢失。检查点无法保存，通常数据库不可达，持续超过代理重投递时间时，该窗口全部事件可能耗尽。通知不转死信，避免虚报数百条损失，而计入 `outcome="replay-covered"` 并由下方告警报告。读取同一流的其他服务没有检查点，仍正常转死信。

| 告警 | 含义 | 处理 |
| --- | --- | --- |
| `MaxDeliveryRecordsWaiting` | 耗尽投递次数通知等待十五分钟未记录。 | 确认所有服务运行；停机服务延迟记录。全部恢复后仍残留的通知可能引用升级后移除的消费者，已无服务读取，不会被记录，可从流删除。 |
| `ReplayCoveredDeliveriesExhausted` | 使用自己检查点的消费者在过去十五分钟耗尽投递次数，因为检查点无法保存时间超过代理重投递周期。尚未丢失。 | 修复 `job` 指定服务无法保存检查点的原因，通常数据库连接。服务持续运行时，成功检查点会保存已读取状态；若先重启，则从旧检查点重读，流已丢弃内容无法读回，同时监控 `JetStreamDurableUnreadNearFull`。 |

## 容器重启 {#container-restarts}

服务无法继续工作时，以非零状态主动结束：例如读取循环连续两分钟失败且无代理响应，或某组件报告进程不能继续。代理连接永久关闭也使存活检查失败，由 Kubernetes 重启。Pod 通常几秒后再次就绪。已读取未确认消息会重投递，因此重启不丢弃这些内容；设备或客户端执行中的请求失败，必须重试；每次重启都延长原中断。

| 告警 | 含义 | 处理 |
| --- | --- | --- |
| `InstanceContainerRestarted`（warning） | 过去十五分钟 Kubernetes 重启了 `pod`、`container` 指定容器，每容器一次，最近重启十五分钟后解除。 | 读取 `kubectl logs --previous -n <namespace> <pod>`。最后错误说明原因，通常在 “A component has declared this process unfit to continue” 后。放弃读取的循环列出流和最近代理错误。多个服务同时重启时先检查代理服务器并运行 `dcctl ha verify`。代理容器 `dc-nats-*` 也读旧日志，其流可能几分钟后才追上。`OOMKilled` 应提高内存限制。安装或升级最初几分钟重启属于预期。 |

告警使用监控栈安装的 kube-state-metrics 的 `kube_pod_container_status_restarts_total`。自建 Prometheus 没有它时不触发。升级或 `kubectl delete pod` 替换 Pod 会产生新计数，不触发此告警。持续重启还会在十五分钟后由监控栈自己的 `KubePodCrashLooping` 报告。

kube-state-metrics 本身因节点失效等原因移动期间的重启，只要十五分钟内恢复，也会报告。这种中断后，在 kube-state-metrics 中断前最近报告时刻十五分钟后解除。更长中断期间的重启不报告。

## 按平台默认限额计量的租户 {#tenant-ceilings}

执行租户限额的服务从用户管理读取各租户值。尚未获得响应前，按平台默认计量；HTTP 接入为无法确认的租户名字提供有界额度。[治理](../concepts/governance.md#unresolved-ceilings)介绍两者。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `TenantsMeteredAtPlatformDefault` | warning | `job` 服务因用户管理不可达或失败，连续十五分钟按默认限额计量。高于默认的租户提前丢弃，低于默认的租户被允许超额。 | 检查用户管理运行及服务能否访问。 |
| `RateLimiterOverflowInUse` | warning | HTTP 连续十分钟让无法确认的租户名字使用共享额度。大量未确认名字通常是虚构租户请求。 | 检查 HTTP 接入发送者，参见[无法确认的租户名称](../concepts/governance.md#unconfirmed-tenants)。 |
| `ReactShedLettersOverBudget` | warning | 检测引擎连续十分钟丢弃动作快于逐条记录，超额每租户每分钟汇总一封死信，租户明显超过对外限额。 | 用 `dcctl dead-letters` 的 `shed` 原因找到租户，检查规则；合法流量可提高限额，参见[对外治理](../concepts/outbound-connectors.md#governance)。 |
| `RateMeteringClockFallback` | warning | `job` 服务连续一小时因缺失触发时间，按代理或到达时间计量，重启追赶可能误作洪峰。触发时间晚于承载消息的代理时间也按代理时间计量，但记为 `capped`，不触发，因为这是时钟差而非缺失。 | 确认事件处理和对外连接器使用同一版本。 |
| `ConnectorDispatchRateLimited` | warning | 对外连接器连续十五分钟因租户速率超限丢弃投递。检测引擎按同一时间线先行计量，因此这些请求一端允许、另一端拒绝。单纯租户超限触发的是检测引擎 `ReactConnectorEgressShedding`，不是此告警。 | 检查两服务默认 `outboundCallsPerSecond`、`outboundBurst` 一致，以及任一服务是否触发 `TenantsMeteredAtPlatformDefault`。失败重试在连接器端再次计量，也应检查失败目标。因此单租户接近限额且目标持续失败也能单独触发，故仅 warning。丢弃投递记录为 `rate_limited` 死信。 |

告警对应序列：

- `TenantsMeteredAtPlatformDefault`：`devicechain_<area>_governance_unresolved_admissions_total{dimension, cause}`，`cause="unreachable"`。
- `RateLimiterOverflowInUse`：`devicechain_eventsources_ratelimit_overflow_admissions_total`。
- `ReactShedLettersOverBudget`：`devicechain_eventprocessing_react_connector_shed_unlettered_total{action}`。
- `RateMeteringClockFallback`：`devicechain_<area>_rate_clock_fallback_total{source}`，来源为 `append`、`capped` 或 `now`，忽略 `capped`。
- `ConnectorDispatchRateLimited`：`devicechain_outboundconnectors_connector_dispatch_total` 的 `rate_limited` 结果。

该投递指标仅为 `rate_limited` 告警。其他失败结果属于租户自身配置，例如失败 webhook 或平台拒绝访问的目标，已进入死信并可用 `dcctl dead-letters` 查看，不呼叫运营方。

## 事件解析 {#event-resolution}

所有事件存储或评估前，都由设备管理解析。解析器先从关系数据库读取一次以认证凭据，再同时从代理键值存储查配置、关系和分组范围，然后交付解析结果供发布。键值存储无法回答时，逐个查数据库，所以最多持有一个数据库连接。多数时间用于等待响应而非 CPU。多个解析器并行；全部忙时事件按序等待，Pod 交接队列最多 100 个；其后是每个入站读取器从流中拉取的批次（默认一个读取器，见 `resolution.inboundReaders`）：正在交付的批次最多 256 个，启用[预取](#fetch-ahead)时预先请求的批次最多再有 256 个；其余在流中。

| 指标 | 含义 |
| --- | --- |
| `devicechain_devicemanagement_resolve_workers` | Pod 解析器数量。 |
| `devicechain_devicemanagement_resolve_inflight` | 忙碌解析器数量。持续等于 worker 数时，到达快于解析，前方排队，入站消费者待处理数增长，参见[持续落后](#consumer-backlog)。 |
| `devicechain_devicemanagement_resolve_messages_total` | 按结果统计已解析事件。全部忙时，增长速率是 Pod 每秒解析能力。 |
| `devicechain_devicemanagement_resolve_not_gateway_child_total` | 因网关为未分配给它的设备发布事件而被拒绝的事件数。参见[指标参考](./metrics-reference.md)和[网关身份](./edge-services.md#gateway-identity)。 |

`resolve_duration_seconds` 包括等待交付结果的时间。最小桶 5 毫秒，低于它的分位数是估算，不是测量。

### 调优 {#tuning-it}

| 设置（`device-management`） | 默认值 | 作用 |
| --- | --- | --- |
| `resolution.workers` | `10` | 并行解析器。凭据在副本 `inMemoryCache.ttlSeconds` 内未验证时，从数据库读取并持有一个连接，见[键值缓存](#kv-caches)。必须低于服务连接池 `rdbConfiguration.maxOpenConnections`（默认 20），池还供 GraphQL、MQTT 连接检查、告警触发解除消费者使用。允许超过一半，但启动时记录。键值查找并行，数据库查找仍串行，因此最多一个连接。 |
| `resolution.publishWindow` | `512` | 单个 Pod 同时等待代理确认的已解析（及失败）事件数，范围 1 到 1024。超出范围时服务启动即失败，不会自动调整。发布速度不会超过该数值除以一次确认所需时间；在复制的代理上一次确认约需 8 到 11 毫秒，所以 128 会把单个 Pod 限制在约每秒 11,000 个事件，而 512 不会。当 `devicechain_<area>_jetstream_publish_window_full_seconds_total` 每秒增长接近一秒时应调高。更深的窗口会让更多源事件在 Pod 内处于未确认状态：Pod 崩溃后，这些事件会在消费者确认等待期后重新投递，并以相同消息 ID 再次发布，流会识别出重复（见 `jetstream_publish_duplicates_total`）。窗口中的每个事件连同其源事件都保留在内存中，直到被确认。固定设置，启动时读取。 |
| `resolution.inboundReaders` | `1` | 单个 Pod 在入站事件消费者上同时保持的拉取请求数，范围 1 到 4。超出范围时服务启动即失败，不会自动调整。每个读取器在解析手头批次的同时向代理请求下一批，所以一个读取器每次与代理往返最多交付一个批次：在繁忙的三节点集群上，单个读取器把 Pod 限制在约每秒 10,000 个事件，而解析器大部分时间空闲。读取器共享同一个消费者，每个事件只交给其中一个；它们共享 Pod 的解析器、缓存和发布，因此一个读取器的成本远低于一个副本。每个读取器最多持有两个未确认的批次（见上文），重启后由代理在确认等待期后重新投递。多于一个读取器时，积压的消息会按每个读取器一个批次同时交付，因此在消费者追赶期间，相隔约一个批次以内的事件可能以相反顺序到达已解析流，与多副本时相同。在事件较少的设备群上，一个批次可能跨越超过检测允许延迟的时间；把读数与上一读数比较、或按到达顺序计数的检测规则会受此影响。仅当解析器大多空闲而 `device-management` 入站消费者仍然落后时才调高：每个 Pod 每秒低于约 10,000 个事件时，拉取并非瓶颈；节点 CPU 成为瓶颈时，再加读取器无济于事。固定设置，启动时读取。 |
| `inMemoryCache.ttlSeconds` | `300` | 副本将值保留在自身内存中的时长，1 到 3600 秒。变更会同时清除所有副本中的该值，所以它只约束丢失的清除消息。键值缓存自身的存活时间对它设上限（见下文），见[键值缓存](#kv-caches)。 |
| `deviceCacheTtlSeconds`、`relationshipCacheTtlSeconds`、`metricDefCacheTtlSeconds`、`membershipCacheTtlSeconds` | `300` | 代理中各键值缓存的存活时间，1 到 3600 秒。 |
| `inMemoryCache.perDeviceCacheEntries` | `131072` | 每副本三个逐设备内存缓存各自的条目上限：按令牌设备、追踪关系和分组成员，见[键值缓存](#kv-caches)。 |
| `inMemoryCache.perDeviceCacheMiB` | `24` | 各缓存在每副本的内存 MiB 上限，调高时同步提高服务内存限制，见[键值缓存](#kv-caches)。 |
| `inMemoryCache.credentialCacheMiB` | `32` | 设备凭据缓存在每副本的内存 MiB 上限，1 到 256。默认值约容纳 29,000 个有凭据的设备，60,000 个约需 70。调高时同步提高服务内存限制，见[键值缓存](#kv-caches)。 |

`resolve_inflight` 持续等于 worker 数且 CPU 有余量时可增加。CPU 已到限制时，增加解析器无效，应增加 CPU，参见[服务容量](./bootstrap.md#service-sizing)。进程内针对三节点代理测量、各查找 750 微秒且依次执行时，5 个解析器约每秒 1,500 事件，10 个约 2,900。完成顺序可能比到达乱序几分之一秒，检测按进入解析流顺序处理。超范围配置阻止启动，错误指出设置；启动日志列出实际值。

### 接入发布窗口 {#ingest-publish-window}

`event-sources` 会把每个解码后的事件发布到代理，并与 `device-management` 一样，同时保持多个发布在等待代理确认。

| 设置（`event-sources` 配置） | 默认值 | 作用 |
| --- | --- | --- |
| `capture.publishWindow` | `1024` | 单个 Pod 同时等待代理确认的事件数，范围 1 到 1024。超出范围（包括 0）时服务启动即失败，不会自动调整。接入速度不会超过该数值除以一次确认所需时间。在空闲的复制代理上一次确认约需 8 到 11 毫秒，但在负载较高的代理上实测约为 70 毫秒，此时 512 会把单个 Pod 限制在约每秒 7,100 个事件，而 MQTT 设备发布得更快，捕获流随之写满。超过 1024 时请增加副本。当 `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` 每秒增长接近一秒时应调高。窗口中的每个事件连同其来源的设备消息都保留在内存中，直到被确认，因此内存增长量为窗口大小乘以一个事件与一个源消息的大小：普通负载下 1024 约为几 MiB。Pod 崩溃后，这些源消息会在消费者确认等待期后重新投递，并以相同消息 ID 再次发布，流会识别出重复（见 `jetstream_publish_duplicates_total`）。固定设置，启动时读取。 |
| `brokerPresence.publishWindow` | `128` | 单个 Pod 同时等待代理确认的在线状态转换数，范围 1 到 1024。超出范围（包括 0）时服务启动即失败，不会自动调整。代理在线状态读取器过去会等待每次确认后才处理下一个连接或断开，因此每个主题每秒只能处理约 88 次转换（复制的代理上每次约 11 毫秒），一个副本重连 10,000 台设备约需两分钟；设为 128 后约为每秒 11,000 次。N 台设备重连约需 N 乘以一次确认耗时再除以该值，并分摊到各副本：设为 128 时，10,000 台设备约需一秒。可观察 `devicechain_eventsources_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="presence"}`：速率接近 1 表示该窗口是在线状态读取的瓶颈。代理不会确认或重新投递连接通知，所以该值同时是丢失上限：Pod 崩溃时，已接收但尚未确认的转换最多丢失这么多，另加仍在等待读取的通知，由对账在下一轮恢复（见下文）。无法写入的转换不会被悄悄丢弃：会记录日志并计入 `devicechain_eventsources_presence_events_failed_total`，连接写入失败的设备不会唤醒其暂存的命令。固定设置，启动时读取。 |

这样丢失的转换由下一轮对账修复：对账会比较代理上的实时连接与已存储的状态。每个副本每隔 `brokerPresence.reconcileSeconds`（默认 300）运行一轮，启动时也运行一次。因此最坏情况下，连接通知丢失的设备会在最多一个间隔（默认五分钟）加上一轮对账耗时内**显示离线而实际在线**；断开通知丢失的设备则在同样时长内显示在线。替换 Pod 启动后立即运行第一轮。默认值取 128 而不是接入写入器的 512，是因为在线状态以与设备规模相当的突发到达，而非稳定流：128 已能在约一秒内处理 10,000 台设备的重连，更大的窗口只会增加 Pod 在突发中途崩溃时留给对账的量，却不会让重连明显更快。

在线状态转换持续十分钟写入事件流失败（`devicechain_eventsources_presence_events_failed_total` 上升）时，`BrokerPresenceLosingTransitions`（warning）触发。每条转换在下一轮对账前都处于丢失状态，期间设备可能在线却显示离线。应检查平台的消息代理，并在 event-sources 日志中查找 `Failed to emit a broker presence transition`，其中注明每台设备。读取器停止期间到达的转换不计入，因此滚动重启不会触发该告警，单次突发也会自行消退。

当代理 MQTT 网关的某个流（`$MQTT_msgs`、`$MQTT_out` 或 `$MQTT_qos2in`）持续十分钟没有大小上限，且 `event-sources` 无法设置上限（`devicechain_eventsources_mqtt_gateway_stream_unbounded` 为 1）时，`MqttGatewayStreamUnbounded`（warning）触发。第一个 MQTT 设备连接时，代理会以无上限方式创建这些流，在新实例上这远晚于 `event-sources` 启动，因此每个副本每 30 秒检查一次，流一出现就设置上限；`devicechain_eventsources_mqtt_gateway_stream_bounded_late_total` 记录这种事后设置的次数。该告警表示上限设置失败，最常见的原因是代理已没有可供预留的存储。无上限的流可能写满代理存储并使整个实例宕机，请释放代理上的存储，并在 `event-sources` 日志中查找 `Could not bound the MQTT gateway`。

### MQTT 故障关闭确认 {#mqtt-fail-closed}

`MqttFailClosedDurableMissing`（critical）和 `MqttFailClosedStoreNotAsDeclared`（critical）只会在开启了 `infrastructure.nats.mqttFailClosed` 的实例上触发。该设置默认关闭。开启后，代理的 QoS 1 PUBACK 意味着平台已保存该消息（设备会看到什么，见[服务质量](../guides/connecting-a-device.md#quality-of-service)）。`event-sources` 会声明代理自身的 MQTT 消息存储（`$MQTT_msgs`），使存储写满时拒绝新的发布，而不是丢弃已经确认过的消息；在该存储上为每类设备发布（事件、命令响应和设备报告）各保留一个平台自己的消费者，并转发这些消费者读到的内容。同一条发布直接进入平台流的那份副本会被跳过，因此每条发布只到达一次；QoS 0 发布从不被存储，仍经由捕获流到达平台。每个 Pod 每 30 秒检查一次存储和这些消费者，重新创建缺失的部分，并恢复被改动的设置。需要重新创建的存储在其消费者存在之前不接收任何设备发布，因此这期间设备会被拒绝，而不是被确认进一个无人读取的存储。在开启或关闭该设置的滚动更新期间，存储会保持旧设置的 Pod 所需的状态，直到最后一个这样的 Pod 停止后才改变；关闭该设置时，这些消费者会保留到被读空为止，因此其中保存的内容不会丢失。在存储交接期间发出的发布可能到达两次，但绝不会丢失。如果代理同时服务另一个实例，该设置会在启动时被拒绝，因为同一代理上的所有实例共用这个存储。

检查一旦发现其中某个消费者缺失（`devicechain_eventsources_mqtt_failclosed_durable_missing_total` 上升），`MqttFailClosedDurableMissing` 立即触发；只要有 Pod 报告某个消费者无法创建（`devicechain_eventsources_mqtt_failclosed_durable_present` 为 0），它就持续触发。消费者缺失期间，代理会确认该类设备发布却不保存，因此每条都会丢失，尽管设备已被告知送达。滚动更新不会删除消费者，因此不会触发该告警。应查明是什么删除了该消费者，并在 event-sources 日志中查找 `platform durable on the MQTT message store`。当检查不得不恢复存储的某项设置（`devicechain_eventsources_mqtt_failclosed_store_repaired_total`，按设置区分），或发现代理上还有另一个实例（`devicechain_eventsources_mqtt_failclosed_foreign_instances` 大于 0）时，`MqttFailClosedStoreNotAsDeclared` 触发。被改动的设置会在 30 秒内恢复，但在此期间，写满的存储可能丢弃已确认的消息，或拒绝所有发布。在配置中修改大小上限或副本数不属于该告警报告的改动。代理上的另一个实例会共用该存储及其大小上限，因此应为每个实例配备独立的代理，或关闭该设置。

有两项机制防止写满的存储演变成重连风暴。存储超过其大小上限的 80% 时，`device-management` 会在检查任何凭据之前拒绝所有新的 MQTT 设备连接，直到存储回落到 60% 以下。已连接的设备继续发布，平台则逐步读空存储。被拒绝的设备收到的是连接未获授权（MQTT CONNACK 返回码 5），因为代理的 MQTT 网关在这种情况下无法发送返回码 3（服务器不可用），所以设备固件在收到返回码 5 后必须继续重试，而不是放弃。此外，当代理因存储拒绝或未确认设备的某条发布而关闭其连接时（断开原因为 `Protocol Violation`），该设备在 30 秒内不会被报告为离线。在此期间重新连接的设备不会显示离线，因此命令投递不会扣留它的命令，离线规则也不会触发。未重新连接的设备会在 30 秒结束时被报告为离线，时间为其断开的时刻。`devicechain_eventsources_presence_disconnects_deferred_total` 统计以这种方式暂缓的断开。如果对账在设备被拒绝后、重新连接前运行，仍可能将其报告为离线，直到它重新连接。

当 `device-management` 因存储接近写满而持续两分钟拒绝新的 MQTT 设备连接时（`devicechain_devicemanagement_mqtt_store_backoff_engaged` 为 1），`MqttStoreBackoffEngaged`（warning）触发。`devicechain_devicemanagement_mqtt_store_fill_ratio` 表示存储的填充程度，`devicechain_devicemanagement_callout_refused_store_full_total` 统计被拒绝的连接。告警期间不会丢失任何数据，但设备被挡在平台之外，因为平台读取设备发布的速度低于设备发送的速度。`devicechain_eventsources_mqtt_failclosed_durable_pending{durable}` 和 `devicechain_eventsources_mqtt_failclosed_durable_ack_pending{durable}` 显示是哪个读取器落后：每 30 秒报告一次每个消费者尚未收到的消息数，以及已交给它但尚未确认的消息数。事件消费者读取整个存储并跳过非事件消息，因此它的待处理数包含所有类型；命令响应和设备报告消费者只计算各自类型的消息。消费者无法读取时，或其计数不可信时，这两个指标不存在：流或消费者没有领导者、某个副本离线，或者消费者报告没有待处理消息而存储中仍有它的消息（某台 NATS 服务器的存储副本落后时，在领导者变更或重启后可能会这样报告）。应检查 `event-sources` 以及读取入站事件流的组件；对于命令响应和设备报告，则检查 `command-delivery` 和 `device-management`。

该设置的已知限制：

- 该保证针对多副本代理（`streamReplicas` 为 3）。单副本时，节点崩溃可能丢失代理已确认但尚未写入磁盘的消息。
- 在代理上被手动删除的消费者会在 30 秒内重新创建。在此之前（以及之后各 Pod 重新读取存储前的几秒内），该类发布会被确认却不保存，这也是 `MqttFailClosedDurableMissing` 为 critical 的原因。
- 超出租户接入上限的消息仍会被确认后丢弃，与关闭该设置时相同。
- 边缘代理内置的 MQTT 消息代理不在覆盖范围内。它上行到平台的连接与其他设备一样是 QoS 1 发布，属于覆盖范围。
- 捕获流仍会收到每条 QoS 1 发布的一份副本，由其读取器跳过。如果它在过载下写满，其上的 `JetStreamDurableLostUnread` 也会把这些被跳过的副本计入，因此会高估丢失量：真正丢失的只有其中的 QoS 0 发布，QoS 1 发布由存储保存。同理，开启该设置后 `JetStreamCaptureLeaderChanged` 不会导致 QoS 1 丢失。
- 各 Pod 用自己的时钟判断彼此的租约。时钟相差超过约 30 秒时，Pod 之间可能来回交接存储，这只会产生重复，不会丢失。
- 存储积压大量消息时发生的代理崩溃不在覆盖范围内。在一次每秒 9,000 条 QoS 1 事件的负载测试中（当时存储读取器尚未提速），存储积累了约 150 万条未读消息，三台 NATS 服务器中有两台内存耗尽并重启，约三分之一已确认的事件始终没有进入事件存储。这些事件在哪一环丢失尚未查明。在查明之前，不要指望该设置能扛住代理崩溃，并应把持续增长的存储（`devicechain_eventsources_mqtt_failclosed_durable_pending`）当作需要解决的问题，而不是存储能够吸收的情况。
- 与捕获流相比，存储每次发布给代理带来的开销更大。拒绝而非淘汰的存储在每次发布时都要检查上限，其开销随同时发布的设备数增加而增长，因此开启该设置后 PUBACK 更慢：在一次每秒 6,000 条事件的测试中，第 99 百分位为 336 ms，关闭时为 35 ms。
- 退避机制按字节衡量存储大小，并且只拒绝新连接：已连接的设备会继续发布。即使远低于上限的 80%，积压也可能很大。
- 存储中的积压本身占用的代理内存很少，存储写满时每台服务器约为几十 MiB。每台 NATS 服务器的内存主要用于重复检测窗口，在每秒 10,000 条事件的持续负载下，无论该设置开启与否，都会接近其软内存上限。CPU 不足的服务器还会在内存中排队积压工作，因此如果某台服务器曾因内存不足（OOM）被终止，请调高 `nats_memory_limit`。

## 事件持久化 {#event-persistence}

事件管理批量写入。每个写入器拿取已等待事件到上限，在一个事务提交，对批次每个租户每张表写一次，而非每事件一次。连接和断开仍在批次中逐条写。事务提交后才确认事件。批次中某事件被拒绝时，事务全部回滚；拒绝事件单独重写，按非批量方式重试或报告，其他事件排除它后重新提交。因租户已删除而被拒绝的事件例外：拒绝适用于该租户的所有事件，因此都不再重写，而是立即确认、不作任何报告，并计为 `erased`（参见[租户删除](./tenant-deletion.md#database-writes)）。数据库拒绝多事件语句中的行时无法指出原事件，因此先在新事务逐事件写入来定位。若失败不是单个事件原因，例如数据库连接丢失，全部事件逐个重写。

写入器拿到不足整批事件时，最多等待十毫秒（`persistence.lingerMillis`）再提交。已有积压时立即拿取，因此等待不增加积压处理成本。只有全部写入器忙时才减少提交：新事件先交给空闲写入器，而非等填满的写入器，所以每副本低于约几百事件/秒时仍单独提交，只多等最多十毫秒。复制数据库大部分提交时间等待备用副本，一批仅支付一次，因此负载下收益最大。

| 指标 | 含义 |
| --- | --- |
| `devicechain_eventmanagement_persist_batch_size` | 每提交事务事件数。小批表示跟得上，接近上限表示忙。10 个写入器共享流时，即使存储落后也很少满批，应结合积压。 |
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | 未提交、随后重写事件的批事务。偶发增长通常一个拒绝事件；多事件语句拒绝行时，定位需要再一次尝试，可能增长两次。持续增长表示反复拒绝，例如删除租户设备仍发送。每个包含这些事件的批次多花一个事务，不论数量；事件在 `persist_messages_total` 显示为 `erased`。 |
| `devicechain_eventmanagement_persist_inflight` | 写入器持有事件，包括等待批提交。 |

`persist_duration_seconds` 从写入器取得事件计到批提交，包括最多 `persistence.lingerMillis` 的填批等待，因此轻载中位数比关闭等待约高十毫秒。

### 调优 {#tuning-it-1}

| 设置（`event-management`） | 默认值 | 作用 |
| --- | --- | --- |
| `persistence.writers` | `10` | 并行写入器，每个写时一个连接，须低于 `tsdbConfiguration.maxOpenConnections`（默认 20）。允许超过一半并记录，因为读取会争用剩余连接。 |
| `persistence.maxBatch` | `64` | 单事务最大事件数，范围 `1`–`64`，`1` 禁用批量。 |
| `persistence.lingerMillis` | `10` | 不满批最多等待时间，上限 `1000`；已有等待事件时不等。`0` 立即提交已等待内容。 |

默认采用最大批和默认池一半。消费者积压持续增长就说明存储落后，与批大小无关，参见[持续落后](#consumer-backlog)。增加写入器不保证修复，它们把同样事件拆成更小批，而每次提交消耗数据库 CPU。在[默认值测量](./bootstrap.md#measured-throughput)三节点集群中，约每秒 6,000 事件处存储不再增长，平均批约 21，超过该负载时约 28–30，仍低于上限；其中两个节点，包括事件库节点，CPU 为 86–95%，未隔离哪个限制吞吐。更早测量中，两副本各 20 写入器使批约 3 个事件、数据库用超过 4 核，整链存储低于单副本 10 写入器。数量按副本计。超范围设置阻止启动，错误指出键；启动日志列出值。

十毫秒等待在[实测吞吐](./bootstrap.md#measured-throughput)六节点云集群发布基准中测量。每秒 3,000 事件时，每存储事件事务数下降 37%（0.126 到 0.080），存储中位数约增加五毫秒，最慢 1% 降低 18%。但同时改变了事件库键和归档压缩、服务 CPU 请求及放置、设备状态写入器、检测 CPU 限制、NATS 请求和内存限制，不能单独归因于等待。五分钟、提供每秒 6,800 事件的运行中，逐事件提交路径占事件管理 CPU 4.8%，每事件 0.050 事务；前一基准、没有等待且服务节点不同的构建为 24%。等待延迟代价大于收益时，设置 `persistence.lingerMillis: 0`。

### 计算指标回填 {#calculated-backfill}

租户的[回填](../user-guide/calculated-metrics.md#backfilling-history)会把计算指标补入已存储的历史数据。它在 `event-management` 中运行：整个实例只有一个工作进程，按租户轮流每次处理一个任务，并限制速率。没有任务时，每十秒只花费一次加锁尝试和两次小查询。回填只写入测量值行，不向消息代理发布任何内容，也不会影响实时状态或检测。

| 设置（`event-management`） | 默认值 | 作用 |
| --- | --- | --- |
| `calculatedBackfill.enabled` | 未设置 | 三种状态。**未设置**：连接池在持久化写入器之外还有余量时（`persistence.writers` + 1 + 4 个空闲连接须能放入 `tsdbConfiguration.maxOpenConnections`）工作进程运行；容量不足时保持关闭，并在启动时给出点名这些设置的警告。**`true`**：你明确要求启用，因此容纳不下的连接池会使服务启动失败。**`false`**：工作进程不运行。此时启动回填会被拒绝并提示不可用及原因（该原因会被租户用户读到，因此只说明工作进程已关闭或连接池没有余量，具体设置和连接池大小只出现在启动警告中，不出现在原因里），控制台会在页面打开时查询 `calculatedMetricBackfillAvailability`（不轮询），并提前以同样的原因禁用该控件，因此修改此设置后，下次打开页面时即可在控制台中看到；正在运行的任务保持运行状态，直到重新启用工作进程或取消该任务。 |
| `calculatedBackfill.rowsPerSecond` | `20000` | 工作进程每秒读取与写入的行数上限，对所有任务合计，范围 `1`–`200000`。该默认值是暂定的，尚未在有负载的实例上测量：繁忙的实例请从较低值开始，并观察事件存储的 CPU。 |

工作进程在其生命周期内占用一个连接池连接。单个任务的跨度（90 天）、批大小以及每个租户同时只有一个活动任务的规则都是固定的。

| 指标 | 含义 |
| --- | --- |
| `devicechain_eventmanagement_calc_backfill_rows_written_total` | 已插入的测量值行数。 |
| `devicechain_eventmanagement_calc_backfill_samples_total` | 已评估的上报数。 |
| `devicechain_eventmanagement_calc_backfill_skipped_total{reason}` | 未计算的值，按公式的跳过原因统计，另有 `ambiguous`。 |
| `devicechain_eventmanagement_calc_backfill_windows_skipped_total{why}` | 未处理的时间段：`compressed`，或 `retention`（处于保留策略的作用范围内）。 |
| `devicechain_eventmanagement_calc_backfill_jobs_finished_total{status}` | 已结束的任务，按状态统计。 |
| `devicechain_eventmanagement_calc_backfill_oldest_active_job_age_seconds` | 最久的排队或运行中任务的存续时间；没有则为 `0`。任务卡住会使它无限增长，请对其设置告警。 |
| `devicechain_eventmanagement_calc_backfill_batch_seconds`、`..._rollup_refresh_seconds` | 处理一批的耗时，以及对一个时间段刷新测量值汇总的耗时。 |
| `devicechain_eventmanagement_calc_backfill_rollup_refresh_conflicts_total` | 与汇总自身的策略任务冲突并被重试的刷新次数。 |

每次领取任务、每个时间片结束和最终状态也都会记录一行日志，包含租户、任务令牌、配置文件、版本、计数和失败原因（如有）。

#### 停止回填 {#calculated-backfill-stop}

要停止**所有**排队和运行中的回填，请连接事件存储数据库并运行：

```sql
UPDATE "event-management".calculated_backfills
   SET cancel_requested_at = now()
 WHERE status IN ('QUEUED', 'RUNNING');
```

加上 `AND tenant_id = '<tenant>'` 即只停止某个租户的任务。工作进程会在下一次提交时发现，每个任务在一个批次内以 `CANCELLED` 结束。已写入的行会保留；尚未被工作进程领取的排队任务会在工作进程下次运行时同样结束。要停止工作进程本身，请设置 `calculatedBackfill.enabled: false` 并重启 `event-management`。

### 设备实时状态 {#live-state-projection}

设备状态服务从同一流维护连接、活动、最新读数和最近位置，并同样批量合并。每个写入器获取等待事件至上限，在一个事务合并。已有状态设备按每租户每批一条语句写入，不是逐设备；首次设备单独创建。提交后才确认。批内同设备多个事件，与逐条合并结果完全一致。读数或位置只有严格更新时才替换，旧或同时间不会覆盖；时间按数据库微秒精度比较。某租户部分拒绝时，只将其事件逐条重合并，仅拒绝事件重试，其他租户一并提交。连接丢失等非租户原因失败时，全部逐条重合并。

| 指标 | 含义 |
| --- | --- |
| `devicechain_devicestate_state_batch_size` | 每提交事务事件数，通常 `1` 表示跟得上。 |
| `devicechain_devicestate_state_batch_fallbacks_total` | 未提交、随后重合并的批次。持续增长表示某租户写入反复拒绝，例如删除租户设备仍发送。 |
| `devicechain_devicestate_state_write_conflict_retries_total` | 单事件写入被 PostgreSQL 作为死锁受害者或序列化失败中止后，就地重新执行的次数。持续增长表示不活动扫描与写入方频繁冲突；重试全部失败的事件留待重投递，并在消息结果中计为 `retry`。 |
| `devicechain_devicestate_state_inflight` | 写入器持有事件，包括等待批提交。 |

`state_duration_seconds` 从写入器取得事件计到批提交。

| 设置（`device-state`） | 默认值 | 作用 |
| --- | --- | --- |
| `projection.writers` | `10` | 并行写入器，每个合并时一个连接，低于 `rdbConfiguration.maxOpenConnections`（默认 20）。 |
| `projection.maxBatch` | `32` | 单事务最大合并事件数，范围 `1`–`64`，`1` 禁用批量。 |
| `projection.lingerMillis` | `0` | 不满批最大等待，上限 `1000`，`0` 直接合并已有内容。 |

批量支撑复制数据库吞吐。同设备合并互相等待，因此小设备群轮流发送时，更多写入器可能意味着更多批次等待同设备。三节点云集群约 1,700–1,900 台设备，5 写入器在约每秒 6,800 事件开始落后。选择 10 写入器的运行同时提高 CPU 请求和 `projection.maxBatch` 至 64，未隔离各自贡献。默认数量 10，是池一半，默认批仍 32，因为该运行平均批约 15，32 平均不构成限制。只有满批且数据库有余量才继续提高。启动日志列出实际值。实时状态仍落后时，消费者触发 `JetStreamDurableFallingBehind`，参见[持续落后](#consumer-backlog)。

`device-state` 在最后一次投递仍无法应用的事件会被确认并丢弃，不在任何地方记录，因为实时状态可由事件历史重建。它在[消息结果](#message-results)中计为 `dropped`。计入一条即触发 `DeviceStateDroppingEvents`（warning），最后一条之后 15 分钟解除。每个被丢弃的事件都会让对应设备的连接状态、某项读数或最后位置保持过时，直到该设备有更新的事件到达；已存储的事件历史不受影响。常见原因是服务数据库拒绝或写入失败的时间超过重投窗口。检查数据库，并在 `device-state` 日志中查找 `Giving up on`，其中列出写入和设备。

## 复制 {#replication}

高可用实例同时需要：流按配置副本数创建，以及足够大的 NATS 集群容纳副本。配置看似正确时，任一仍可能错误。因此每个服务每 30 秒报告代理中所用流和 KV 桶的实际状态，五个告警监控结果：

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `JetStreamNotReplicatedAsConfigured` | warning | `job` 服务观察到流副本少于配置，持续十五分钟，该流不具备预期高可用。 | NATS 服务器数应满足 `instance.config.infrastructure.nats.streamReplicas`。副本提升仅在服务启动执行，集群扩足后重启受影响 Deployment。 |
| `JetStreamReplicaPeersDegraded` | warning | 流连续二十分钟拥有的当前在线副本少于副本数。失去主副本可能丢数据或可用性。等待较长，因为新副本复制数据需要时间，高持续速率下也可能短暂落后，见下文。 | 检查 NATS Pod 健康及放置。三个副本在同节点，不耐受该节点失效。 |
| `JetStreamLeaseBucketNotReplicated` | critical | 配置多副本实例中，决定哪个 Pod 可写的租约桶少于三个副本。失去其服务器将阻止所有备用接管。 | 同副本不足告警；触发期间不能依赖切换。 |
| `JetStreamClusterUnused` | warning | 代理已集群化，所有流却配置单副本，运行多个 NATS 仍不耐受服务器丢失。 | `streamReplicas` 与集群匹配；`dcctl install --ha` 设置两者。如果只需单服务器，可缩小集群。 |
| `JetStreamReplicationUnobserved` | warning | 运行 Pod 十五分钟无法读取之前可读的流复制状态，其他告警无法判断该流，应视为未知而非健康。 | 全部流同时触发时，检查代理/JetStream、NATS Pod 和连接日志。单流时可能流失去主副本，用 `nats stream info` 检查。 |

`JetStreamReplicationUnobserved` 的限制：

- 代理中断时，对**每个 Pod 的每条流**触发。这是准确报告，chart 没有其他代理不可达告警。通知过多可按告警名分组。
- 逐 Pod 监控，所以某副本无法读流，即使同服务其他副本可读也触发。Pod 替换或新版不再使用某流，不触发。
- 只看 Pod 至少读成功一次的流。启动以来从未成功的 Pod，包括故障发生后创建的替代 Pod，不触发。原地容器重启保留 Pod 名，因此旧进程曾读取仍算，告警仍触发。
- Pod 最后成功读取六小时后会解除，即使仍不可读，因此解除通知不证明恢复。
- 未运行 Pod 不导出，不能触发，由 Pod 健康告警覆盖。

持续高事件速率下，`JetStreamReplicaPeersDegraded` 可能反复 pending 又解除，并非故障。繁忙流副本短暂落后主副本，追上前不算 current。在三服务器代理两次十分钟、每秒 6,000 事件测试中，最忙流约五次检查有一次非 current，每次仅几秒，告警反复 pending/解除。只有二十分钟每次检查都不足才触发，因此短暂滞后会解除。运行 `dcctl ha verify` 最多复查 90 秒（`--settle`）才判失败，短暂落后不导致失败；两次测试后均通过。

使用 JetStream 的服务导出：

- **`devicechain_<area>_jetstream_replicas_desired{stream}`**、**`_replicas_actual{stream}`**、**`_peers_current{stream}`**：配置副本数、代理实际数、当前在线副本数。无法读取时移除三项，不保留旧值。
- **`devicechain_<area>_jetstream_broker_clustered`**：连接代理已集群化为 1，否则包括断开时为 0，Pod 运行期间始终存在。

以下没有告警，但发布缓慢时应查看：

- **`devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`**：从发送到服务处理代理确认或失败的发布时长，按目标流区分。`sync` 为单独等待发布；`pipelined` 为多条同时执行，例如设备管理发布解析事件、事件来源转发平台 MQTT 设备事件。流水线时长还包括等待所有更早发布完成、失败后暂停，因此两模式不能直接比较。无响应同步发布在五秒上限计数，`le="5"` 桶以外即触及上限；流水线发布可超过五秒而未触限。
- **`devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix, writer}`**：流水线写入器因整个窗口都在等待确认而无法发送的秒数。速率接近 1 表示限制发布的是窗口而非代理，应调高 `resolution.publishWindow`（`device-management`）或 `capture.publishWindow`（`event-sources`）；接近 0 表示窗口有余量。多个写入器可能共用同一后缀，`writer` 标明是哪一个：`resolution`（`device-management`）、`capture`、`external-mqtt` 和 `presence`（`event-sources`）以及 `sparkplug`（`sparkplug-ingest`）。所有外部 MQTT 来源都计入 `external-mqtt`。不指定 `writer` 的查询可用 `sum by (suffix)` 读取总量。
- **`devicechain_<area>_jetstream_publish_window_full_total{suffix, writer}`**：发现窗口已满而必须等待的发送次数。上一项的秒数除以该值即为平均等待时间。
- **`devicechain_<area>_jetstream_publish_duplicates_total{suffix, writer}`**：代理确认为已持有相同消息 ID 的重复消息的发布数。消息只存储一次。Pod 重启或确认丢失后该值上升，是流在吸收重新发布，并非故障。

## 停止响应的缓存 {#kv-caches}

设备管理将每事件重复的设备令牌、追踪关系、类型已发布配置和分组成员查找缓存于 NATS 键值桶。查找最多等半秒。超时或无服务器响应时，跳过该桶五秒，直接查保存同数据的数据库，再试一次查找；只有成功才停止绕过。首次绕过警告 `A key-value cache stopped answering`，恢复时记录 `A key-value cache is answering again`，含时长和期间回退数据库的读写数。桶明确返回的错误，例如已满拒绝写入，计数但不触发绕过。

常见原因是 NATS 服务器网络失联却未关闭连接。桶所有副本都响应读取，其他服务器约一到一分半发现失联前，部分读取仍发往旧服务器。事件继续解析，但增加数据库读取。

每个副本还将桶读写结果在内存保留最多五分钟（`inMemoryCache.ttlSeconds`，且不超过缓存的存活时间），直接响应，包括桶被绕过期间。时间从读取而非最近使用计算。三个逐设备缓存各最多 131,072 条或 24 MiB，由 `inMemoryCache.perDeviceCacheEntries` 和 `inMemoryCache.perDeviceCacheMiB` 配置，大约容纳无追踪关系 87,000 设备、有一条关系 26,000 设备。按类型和租户的配置、分组范围缓存各 4,096 条或 4 MiB。满时移除最近最少使用条目；插入时也从该端移除过期条目，遇到首个未过期即停止。NATS 报告不存在的结果不缓存。

**变更同时到达所有副本，存活时间只兜底丢失的消息。** 使已缓存值失效的变更（设备更新或删除、关系增删、配置发布或回滚、分组成员变化、租户被清除）先从桶中移除该值，再通过 NATS 发送一条消息，使每个设备管理副本丢弃自己的副本。副本与 NATS 断开又重新连上时，会丢弃其持有的全部内容，因为可能漏收了消息。所以副本只有在连接期间丢失了消息（例如落后太多而未能收到）时，才会在短暂时刻之后继续提供旧值，而且只持续到时间用尽。变更发生时正在进行的读取若在变更之前读到并随后写入缓存，情况相同。消息数由 `cache_eviction_broadcasts_total` 统计。

**设备凭据。** 各副本将刚验证的凭据及其设备从读取起内存缓存五分钟，因包含密码，绝不放键值桶。每副本最多保留 `inMemoryCache.credentialCacheMiB`（默认 32 MiB）。每次使用仍比较密码及到期时间，验证失败不缓存。凭据或设备变更使执行变更的副本立即清缓存，并用 NATS 通知其他副本。因此撤销或跨副本设备删除，所有副本下个事件即生效；通知丢失时，则在副本到期时生效。MQTT 连接始终读数据库。

**按设备群规模设定上限。** 内容从读取起在内存保留五分钟，因此上报间隔不超过该时长的设备，首个事件之后每个事件都由内存回答。上报更稀疏的设备永不内存命中，每事件一次桶读取，另加一次数据库凭据读取。请让每个副本按整个活跃设备群设定上限，而不是按设备群除以副本数：入站流是所有副本共享的单个持久消费者，事件不会按设备在副本之间分配，当副本数乘以设备上报间隔小于该时间时，每个副本几乎看到每一台设备。缓存小于设备群时会循环淘汰，几乎没有命中。例如 60,000 台设备每 10 秒上报，每台有一条追踪关系，则每个副本需要 `inMemoryCache.credentialCacheMiB` 约 80（每条约 1.1 KB）和 `inMemoryCache.perDeviceCacheMiB` 约 64（一组关系约 950 字节），并将内存上限提高到 768Mi。特征是 `kv_cache_local_lookups_total{cache="relationships-by-source", result="miss"}` 接近事件率，而条目数远低上限。缓存容不下设备群则 `kv_cache_local_evictions_total{reason="capacity"}` 接近事件率，条目数或字节数到上限；凭据缓存的同类信号在 `credential_cache_evictions_total{reason="capacity"}`。chart 用 `DeviceCredentialCacheTooSmall` 和 `DeviceLookupCacheTooSmall`（warning）对此告警：某副本因容量淘汰的比例连续 15 分钟超过其查找的 5%。告警触发时，提高边界（`inMemoryCache.credentialCacheMiB` 或 `inMemoryCache.perDeviceCacheMiB`）并同步提高内存限制。默认六个内存缓存共最多 112 MiB。`device-management` 以 `GOGC=400` 运行，并把软内存上限（`GOMEMLIMIT`）设为其内存上限的 75%，因此回收器允许堆增长到持有量的约五倍，接近软上限时更努力地回收；未设置 `GOMEMLIMIT` 的地方，回收前堆可增长至持有量约两倍。想更快解析设备群，也可增加池范围内 `resolution.workers` 或设备管理副本。

变更后移除条目（设备删除、配置发布）绝不跳过，因为只有桶主副本接受，最多等五秒。仍失败会记录 `A key-value cache eviction failed`，旧条目可服务至配置 TTL 到期，已有内存副本则至内存时间到期，二者取较短者。

- **`devicechain_devicemanagement_kv_cache_unavailable{cache}`**：桶绕过时为 1。
- **`devicechain_devicemanagement_kv_cache_failures_total{cache, op, reason}`**：超时 `timeout` 或错误 `error`。事件查找并行，绕过前多个查找可能同时超时，各自计数。
- **`devicechain_devicemanagement_kv_cache_bypassed_total{cache, op}`**：回退数据库的读写。
- **`devicechain_devicemanagement_kv_cache_request_duration_seconds{cache, op}`**：桶操作时长。读写半秒截止，移除五秒。内存命中不访问桶，所以 `op="get"` 只统计内存无法回答的查找。
- **`devicechain_devicemanagement_kv_cache_local_lookups_total{cache, result}`**：内存回答 `hit` 或交给桶 `miss`。
- **`devicechain_devicemanagement_kv_cache_local_evictions_total{cache, reason}`**：超时过期 `expired`、满容量 `capacity`、变更移除 `deleted`。
- **`devicechain_devicemanagement_kv_cache_local_entries{cache}`**、**`devicechain_devicemanagement_kv_cache_local_bytes{cache}`**：副本内存条目和约字节数。过期条目直到查找发现、插入时从最少使用端清除，或需要空间才不再计入。
- **`devicechain_devicemanagement_kv_cache_local_max_entries{cache}`**、**`devicechain_devicemanagement_kv_cache_local_max_bytes{cache}`**：开始驱逐前的条目和字节上限。

未构建内存副本的缓存没有六项 `kv_cache_local_` 序列，目前设备管理所有缓存都有。

凭据缓存有独立序列：

- **`devicechain_devicemanagement_credential_cache_lookups_total{result}`**：内存回答 `hit` 或交数据库 `miss`。
- **`devicechain_devicemanagement_credential_cache_evictions_total{reason}`**：超时过期 `expired`、容量已满 `capacity`，或本副本/其他副本凭据或设备变更 `revoked`。
- **`devicechain_devicemanagement_credential_cache_entries`**、**`devicechain_devicemanagement_credential_cache_bytes`**、**`devicechain_devicemanagement_credential_cache_max_entries`**、**`devicechain_devicemanagement_credential_cache_max_bytes`**：副本当前持有量和上限。
- **`devicechain_devicemanagement_cache_eviction_broadcasts_total{cache, result}`**：通知其他副本删除缓存的消息，分别为发送 `published`、未发送 `publish_failed`、接收 `received`、无法解析而丢弃 `malformed`。持续发送失败意味着其他副本只能等缓存到期才见变更。

任何原因使解析超过五秒，都会记录 `Event resolution is slow` 警告：首个立即记录，此后最多每 30 秒一条，含数量和最慢值。

## AI 推理调用 {#ai-inference}

在启用了 AI 辅助编写的环境中（参见 [AI 辅助编写](../concepts/ai-authoring.md)），`ai-inference` 服务会统计租户向它提出的请求。这两个指标都是有界的：标签只取固定的一组值，绝不会是租户、模型或提供商，因此不会随租户数量增长。

- **`devicechain_aiinference_ai_inference_calls_total{outcome}`**：租户的推理请求，按每次请求的结束方式划分。
- **`devicechain_aiinference_ai_inference_tokens_total{direction}`**：提供商报告已使用的 token 数，按 `direction`（`input` 或 `output`）划分。这是用量（花费）信号：平台中没有其他指标统计 token。它是供观察的计数器，而不是账本，也不会据此强制执行任何限制。

| 结果 | 含义 |
| --- | --- |
| `served` | 提供商已应答，并返回了候选结果。 |
| `rate_limited` | 租户超出了推理速率上限，调用在到达任何提供商之前即被拒绝。计数持续上升说明某个租户长期超出配额。 |
| `consent_required` | 租户尚未同意外部 AI 路由，因此没有任何数据离开平台。对于尚未获得授权的租户，这是预期情况。 |
| `unavailable` | 平台未配置好来处理该调用：未分配模型、提供商已禁用、缺少密钥。服务端日志会指明具体原因。 |
| `provider_error` | 已连接到提供商，但提供商未能完成调用（传输错误、非成功状态码、空应答）。外部依赖工作异常。 |
| `timed_out` | 已连接到提供商，但它没有在 `inferenceTimeoutMs` 内应答（默认 40 秒，最长 120 秒）。作者看到的是超时，而不是笼统的失败。 |

请把 `timed_out` 和 `provider_error` 分开解读。`provider_error` 上升说明提供商在出故障。`timed_out` 上升而 `provider_error` 持平，则说明所用模型比超时时间允许的更慢：请换用更快的模型，或调大 `inferenceTimeoutMs`（chart 的 `ai-inference` 配置中提供该项，超过其上限时服务将拒绝启动）。

## 维护轮次 {#maintenance-passes}

多个服务按定时器运行清扫、协调或调度任务，以自身名字 `devicechain_<area>_<task>_…` 导出三项：

- **`<task>_passes_total{outcome}`**：按结果统计轮次。
- **`<task>_pass_duration_seconds`**：轮次时长，`skipped` 不计时。
- **`<task>_last_success_timestamp_seconds`**：最近执行工作（`complete` 或 `partial`）的 Unix 时间。首次之前为 NaN，因此 `time() - X > threshold` 不会误报刚启动 Pod。

| 服务 | 任务 |
| --- | --- |
| user-management | `dead_letter_sweep`, `tenant_purge` |
| notification-management | `retention_sweep`, `escalation_scheduler` |
| event-management | `anchor_sweep` |
| device-state | `inactivity_sweep` |
| event-sources | `presence_demote`, `presence_reconcile`, `presence_canary` |
| device-management | `ownership_drain` |
| command-delivery | `command_sweep`, `hold_reconcile`, `stranded_reconcile` |
| event-processing | `detect_tick`, `rule_health_flush`, `shed_summary` |
| lwm2m-ingest | `tenant_sweep` |
| sparkplug-ingest | `presence_probe` |

其中三项只在有驱动对象时运行：`detect_tick` 和 lwm2m-ingest 的 `tenant_sweep` 只在持有领导权的副本上运行，`presence_probe` 针对每个已连接的 Sparkplug 会话运行，所有会话共用一个序列。在备用副本上，或 sparkplug-ingest 没有会话时，最近成功时间保持 NaN 或停留在上一任期的值。与 `time()` 比较前，请先跨副本取 `max by (job)`。

例如用户管理租户删除协调器导出 `devicechain_usermanagement_tenant_purge_passes_total`。

| 结果 | 含义 |
| --- | --- |
| `complete` | 已运行并完成工作。 |
| `partial` | 已运行并完成部分工作，例如部分租户。 |
| `failed` | 无法执行工作。 |
| `skipped` | 其他副本持有锁，当前正确跳过，不是故障，不更新最近成功时间。 |
| `cancelled` | 服务停止而提前结束，不是故障。 |

应为 `failed` 和最近成功时间停止前进告警，不为 `skipped`、`cancelled`：多副本只有一个运行，其余跳过，每次发布也可能取消轮次。用户管理、通知管理和事件管理清扫，在间隔前后各 10% 范围随机开始，避免同时启动副本一起冲击数据库。

任务运行成功，也可能目标工作卡住。chart 单独为租户删除告警，参见[租户删除](./tenant-deletion.md#stalled-alert)。

## 停止归档的备份 {#backup-archiving}

备份可能静默失败。归档不在写入路径，WAL 无法到达目标不会拖慢写入或触发健康检查。但 PostgreSQL 在自己的卷保留全部未发送段，且只有主实例持有，副本不能分担。卷满后 PostgreSQL 停止，数据库 Operator 不会因磁盘已满切换。常见链条是备份对象存储满、归档失败、主数据库卷满。

chart 覆盖链条各环节：

| 告警 | 触发条件 | 处理 |
| --- | --- | --- |
| `PostgresWALArchivingFailing` | 最近失败晚于最近成功，持续五分钟。 | 检查对象存储可达、有空间、接受凭据。 |
| `PostgresWALArchiveBacklog` | 超过 32 个完成日志段（512 MiB）等待发送，持续五分钟，也覆盖慢或挂起但未报错的归档器。 | 同时失败告警时修复目标，否则查备份 sidecar 日志、存储空间；积压持续增长时扩数据库卷。 |
| `BackupDestinationFillingFast` | 集群内对象存储超过 65%，按最近十分钟速率将在一小时内满。 | 查写入者：实例接入高于容量设计、已不存在实例留下备份、基础备份调度停止导致不再清理。扩容，移除无人拥有的内容，或迁出集群。 |
| `BackupDestinationAlmostFull` | 对象存储超过 85% 持续十五分钟。 | 同上，但时间更少。 |
| `PostgresHasNeverArchivedWAL` | 某数据库持续一小时从未发送过任何日志段。第一次基础备份会强制切换日志段，因此正常工作的集群在创建后几分钟内就会离开这一状态；停留一小时意味着此处的归档从未工作过，而不是数据库安静。它不记录任何失败，所以归档失败告警看不到它：挂起而不报错的归档器看起来与此完全相同。 | 检查备份 sidecar 日志，以及对象存储是否可达。 |
| `PostgresWALArchivingNotConfigured` | 某数据库持续 15 分钟开启了归档，背后却没有备份插件。每个日志段都被一条丢弃它并报告成功的命令“归档”，因此没有其他东西会告诉你：归档计数在上升，没有失败记录，磁盘不增长，其他所有备份告警都保持正常。**该数据库无法恢复到任何时间点。** | 检查该 Cluster 在 `spec.plugins` 下是否仍带有其 barman-cloud 条目，以及其 ObjectStore 是否存在。 |
| `DatabaseVolumeFillingFast` | 事件库卷超过一半，按最近十五分钟速率将在一小时内满，持续三分钟。 | 仅主实例增长是未归档 WAL，先修复归档；全部成员增长是数据，扩卷或缩短保留。 |
| `DatabaseVolumeFillingUp` | 某数据库卷持续一小时超过 75%。这里的数据库存储不会自行恢复：有几张表按设计只追加，没有任何东西清理它们（审计日志，以及设备档案、实体组、仪表盘、连接器和地理围栏集的版本历史，外加地理围栏几何归档），因为已存储的事件会指明它被解析时所依据的版本，而回滚会回到某个已存储的版本。保留设置不适用于它们；那些设置清理的是时序存储。 | 趁还有余地时扩卷，并找出是什么在产生版本。在关系型主库上，统计某张历史表中每个租户的行数，例如 `SELECT tenant_id, count(*) FROM "device-management".geo_fence_set_versions GROUP BY tenant_id ORDER BY 2 DESC LIMIT 10;`。通常的答案是某个租户在循环中反复编写。对于事件库卷，检查数据生命周期保留设置；对于 `-wal` 卷，原因要看上面的归档告警。 |
| `DatabaseVolumeAlmostFull` | 数据库卷超过 85% 持续十五分钟。 | 立即扩卷。 |

速率告警是因为固定阈值对几分钟填满卷太慢。基准中对象存储满后，事件库主卷约九分钟从 45% 到满，速率告警约提前两分钟触发，而等待十五分钟的 85% 告警会在卷已满后才触发。

对象存储与关系数据库都由集群全部实例共享。各实例 chart 都包含这些规则，因此共享卷或数据库告警每实例产生一次。默认存储按单实例持续接入设计；多个实例可能在事件库之前先填满备份存储，应依赖这些预警。

集群内存储容量参见[备份存储大小](./bootstrap.md#backup-store-size)。

### 卷快照基础备份 {#snapshot-backup-alerts}

使用 [`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups) 安装时，每日基础备份是卷快照，备份存储中的基础备份改为每周。监控对象存储基础备份的 `PostgresNoRecentBaseBackup` 等待从 36 小时改为 8.5 天，并新增三项：

| 告警 | 触发条件 | 处理 |
| --- | --- | --- |
| `PostgresNoRecentSnapshotBackup` | 数据库 36 小时无完成快照，或计划从未完成，持续一小时。 | 读取 `<cluster>-snapshot` ScheduledBackup 和最新 Backup 的事件。VolumeSnapshotClass 被删除或驱动不同会使全部快照失败，期间仍可用每周基础备份时间点恢复。 |
| `DatabaseSnapshotPruningStalled` | Operator 超过一小时未完成某计划快照清理，持续三十分钟。 | 检查 Operator 运行且版本与集群安装一致，读取计划事件。如果运行且日志无 `snapshot retention pass failed`，计划从未有 `devicechain.io/snapshot-retention-checked-at`，且无 `SnapshotRetentionIgnored`（方法非 `volumeSnapshot`）警告，则未被选择：仅处理 DeviceChain 创建命名空间中、带 `app.kubernetes.io/component: database-snapshot-backup` 和 `devicechain.io/snapshot-retention` 的计划。恢复前全部快照保留，每个是数据库完整副本。 |
| `DatabaseSnapshotBackupsUnobserved` | 已启用快照，却无计划被监控看见，持续一小时。 | 确认 ScheduledBackup 存在，kube-state-metrics 读取 CloudNativePG Backup 和 ScheduledBackup，否则前两告警都无法触发。 |

这些通过 kube-state-metrics 读取 Backup/ScheduledBackup 对象，不使用仅在主数据库设置的 CloudNativePG 备份 gauge。

## 数据库控制平面 {#database-control-plane}

数据库可能很健康，却无法故障转移。PostgreSQL Operator（CloudNativePG）及其备份插件决定哪个实例成为主实例。Operator 无法联系到插件的 Cluster 会停止协调，因此永远不会被提升：主实例可能在健康时丢失，完全追平的副本就在那里，却没有任何东西选举其中之一。整个过程中每个实例 Pod 都保持 `Running`，上面的备份告警也都不会触发，因为 PostgreSQL 本身没有任何问题。反过来的故障也存在：Cluster 报告 `Ready`，而每个服务都被锁在外面。这五条告警覆盖这两种情况，以及会掩盖它们的监控缺口。chart 获知数据库 Operator 的命名空间后才会渲染它们。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `CNPGClusterNotReady` | critical | 某个 PostgreSQL Cluster 持续 15 分钟报告 `Ready=False`。**这会阻止故障转移。** Cluster 在切换、滚动重启和首次引导期间会经过 `Ready=False`，但恢复只需数秒，因此一刻钟并非操作进行中。“Cluster cannot proceed to reconciliation”阶段指向 Operator 或备份插件，而不是 PostgreSQL。 | 读取该 Cluster 的 `.status.phase` 和状况（告警会指出 Cluster 和原因）。如果阶段中提到插件，检查 `CNPGControlPlaneUnavailable`。 |
| `CNPGControlPlaneUnavailable` | critical | 数据库 Operator 命名空间中的某个 Deployment 持续 10 分钟没有可用副本。Operator 宕机时，没有 Cluster 能故障转移、伸缩或重新配置，数据库继续无人管理地提供服务。备份插件宕机时，Operator 无法协调任何引用它的 Cluster，因此同样阻止故障转移。 | 恢复告警所指的 Deployment。如果不恢复，预计 `CNPGClusterNotReady` 会在一刻钟内随之触发。重启 kube-state-metrics 会重置本告警的计时器，因此故障报告的时间可能晚于其实际开始时间。 |
| `CNPGClusterUnusable` | critical | 持续 5 分钟，某个 Cluster 至少有一个实例无法被其自身的指标采集器查询，而 Pod 和 Cluster 的 `Ready` 状况看起来可能都健康。告警无法说明是哪个实例。如果是主实例，客户端会受影响，最可能的原因是**连接耗尽**：每个依赖数据库的服务都会向主实例打开最多 20 个连接的连接池，滚动升级会使 Pod 短暂翻倍，随后服务以“remaining connection slots are reserved for roles with the SUPERUSER attribute”失败。如果是备用实例，客户端不受影响，是某个副本出了故障。 | 以超级用户身份（此告警触发时仍可用），在每个实例上比较 `SELECT count(*) FROM pg_stat_activity` 与 `SHOW max_connections`。更改 `max_connections` 本身就是一次滚动重启，5 分钟的等待足以涵盖它。 |
| `CNPGCollectorAbsent` | warning | 持续 10 分钟，没有任何东西在某个数据库命名空间中上报该采集器序列，因此不知道实例能否应答，而 `CNPGClusterUnusable` 因读取同一序列而无法触发。 | 检查该 Cluster 的 CloudNativePG PodMonitor 是否存在并选中其 Pod，Prometheus 能否读取该命名空间，以及实例 Pod 是否在运行并在 9187 端口提供服务。 |
| `CNPGClusterStateUnobserved` | warning | 持续 30 分钟，没有任何东西在某个数据库命名空间中上报 Cluster 状况，因此故障转移的健康状况未知，而 `CNPGClusterNotReady` 无法触发。这是监控缺口，但恰恰是掩盖故障转移阻塞的那个缺口。 | 检查 kube-state-metrics 在运行，它启动时带有自定义资源状态配置（启用 CloudNativePG 时，OpenTofu 监控模块会设置它），并且其角色可以列出 `postgresql.cnpg.io/clusters`。缺少权限只会在其日志中表现为一行 `forbidden`。 |

## 指令投递 {#command-delivery-alerts}

三条告警覆盖指令投递在无人关注时报告虚假信息的状态。服务还导出了更多计数器，列在[指标参考](./metrics-reference.md#command-delivery)中，指令投递仪表盘也会绘制它们。阈值是推理得出而非实测得到的，每个阈值都由单元测试从两个方向检查。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `CommandClaimsStranded` | critical | 过去 30 分钟内，持续 5 分钟，至少有一条指令在其发布和释放回 `QUEUED` 都失败后停留在 `SENT`。它从未被投递。在 LwM2M 上，滞留调和器会在约十分钟内重新装载它，设备下次唤醒时投递；在 MQTT 上没有任何东西重试它，它会作为 `TIMEOUT` 过期，这会把失败记在设备头上，尽管设备从未收到任何东西。 | 将此窗口内报告的每个 MQTT `TIMEOUT` 都视为未归因，不要派工程师去检查所指的设备。查找与数据库故障重叠的代理故障：两者同时发生才会使一次领取滞留。对于 LwM2M，`stranded_recovered_total{disposition="PARKED"}` 可确认重新装载，而 `CANCELLED` 表示该指令的批次已被取消，该行已被清理。 |
| `CommandPresenceReadErrors` | warning | 持续 15 分钟，投递扫描无法读取在线状态投影。扣留缺席设备指令的在线状态门按设计**失败即放行**，因此指令在没有该检查的情况下被分发。没有其他东西会显示这一点：分发看起来正常，扣留计数器降到零，读起来像“没有设备缺席”，而不是“什么也没有被检查”。 | 检查 device-state 服务和在线状态投影的健康状况。修复之前，安静的扣留面板并不能让人放心。 |
| `CommandBatchReserveRefusals` | warning | 持续 30 分钟，有机群写入被拒绝，这些写入本可放入租户自己的未投递指令上限，仅因投递机制为自己保留的余量而被拒绝。没有任何东西损坏，每个调用方都被告知了；上限与保留量对什么是正常的机群写入存在分歧。 | 有意地提高租户的上限或降低保留量。计数器不带租户标签，因此仪表盘显示哪些代码和哪个边界，而不显示哪个租户。在记录的指令批次中找出该租户：部分扇出的批次会记录逐设备的 `HELD_CEILING_EXCEEDED` 拒绝，而被整体拒绝的批次则不记录任何内容。 |

## 自行关闭的安全门 {#safety-gates}

有几个服务带有依赖另一个服务的安全门，当其配置缺失时，每个安全门都会以**关闭**状态启动，并在启动时输出一条警告。这是刻意的可用性权衡：设备平面不能硬依赖这些服务。它同时也是不可见的，因为工作仍在流动，没有任何报错，而安全门本应阻止的事情已经恢复。chart 安装总会提供该配置，因此安全门关闭意味着安装配置错误或被手工修改。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `SafetyGateDisabled` | warning | 持续 5 分钟，`job` 标签所指的服务在 `gate` 标签所指的安全门关闭的情况下运行。只要有一个副本在没有其配置的情况下启动就足够，因为它对自己承担的那份流量已经是关闭的。`tenant_lifecycle` 关闭时，已删除租户的工作会被接纳。`presence` 关闭时，发给缺席设备的指令会被发布并丢弃。`rule_validation` 关闭时，无法编译的检测规则会被允许发布，并在加载时被跳过。 | 服务在启动时记录了一条警告，指出缺少什么：服务密钥，或该安全门所查询的服务的地址。恢复它并重启服务。 |

## 服务性能分析 {#profiling}

指标说明工作量和耗时，却不说明内部时间花在哪里。服务可提供 Go 运行时 CPU、堆、分配、goroutine 栈和执行追踪，使用独立于 HTTP 端口的监听器。

### 为什么默认关闭 {#why-it-is-off}

分析暴露函数名、调用栈和分配位置等内部信息，CPU 分析和追踪运行期间还消耗 CPU。它是测量工具，不是监控数据流，因此仅按需启用目标服务。

启用后默认监听 Pod 回环 `127.0.0.1:6060`，不作为容器端口、Service 端口或 ingress 路由，无自身认证。只有集群允许向 Pod 端口转发的人可访问，因此访问由集群权限而非 DeviceChain 控制。

### 启用 {#turning-it-on}

在 `functionalAreas` 下逐服务设置，仅重启该服务 Pod：

```yaml
functionalAreas:
  device-management:
    profiler:
      enabled: true
```

- **直接安装 Helm chart：** 添加到 values，正常运行 `helm upgrade`。
- **通过 `dcctl bootstrap` 安装：** `dcctl` 无此选项，用实例当前 chart 版本执行 `helm upgrade`。`helm list -n default` 显示版本，release 名为 `dc-<instance>`：

  ```bash
  helm get values dc-<instance> -n default -o yaml > dc-values.yaml
  # add the profiler block above to dc-values.yaml
  helm upgrade dc-<instance> oci://ghcr.io/devicechain-io/charts/devicechain \
    --version <chart-version> -n default -f dc-values.yaml
  ```

  仅改 Pod 设置，不改 `dcctl` 拥有的实例配置 Secret。下次 `dcctl upgrade` 或 `dcctl bootstrap` 重新计算 release，会再次关闭监听器。

服务未部署时，安装拒绝设置，因为不会生效；地址端口已被该 Pod 使用时也拒绝。运行后记录 `Profiling listener is ON` 及地址。

### 获取分析数据 {#capturing-a-profile}

转发本地端口到 Pod，再用 Go 工具访问。自己的机器需要 Go 工具链，服务镜像不包含。

```bash
kubectl -n dci-<instance> port-forward deploy/device-management 6060:6060

# a 30-second CPU profile, opened in a browser
go tool pprof -http=:8081 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'

# the heap, saved to open later
curl -o heap.pb.gz http://127.0.0.1:6060/debug/pprof/heap

# a 5-second execution trace
curl -o trace.out 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

Deployment 转发只到**一个** Pod。多副本时按名称选择目标：`kubectl port-forward pod/<name> …`。

| `/debug/pprof/` 下路径 | 内容 |
| --- | --- |
| `profile?seconds=N` | CPU 分析，1–60 秒，默认 30。 |
| `trace?seconds=N` | 执行追踪，1–60 秒，默认 1。 |
| `heap` | 当前堆采样，`?gc=1` 先回收。 |
| `allocs` | 启动以来所有分配采样。 |
| `goroutine` | 全部 goroutine 栈，`?debug=2` 完整文本。 |
| `threadcreate` | 创建操作系统线程的栈。 |

`?debug=1` 以文本而非二进制返回快照。

### 不提供什么，以及原因 {#what-it-does-not-serve-and-why}

- **`block` 和 `mutex` 返回 404。** 服务未启用采样，总是空会被误解为无争用，实际是未测量。
- **拒绝带时间窗口的堆或分配分析（`?seconds=`）。** 获取两次快照，用 `go tool pprof -base first.pb.gz second.pb.gz` 比较。
- **不提供 `cmdline` 和 `symbol`。** 分析自身包含符号。
- **同时最多一个 CPU 分析和一个追踪。** 第二个同类请求返回 409，直到第一个结束。
- **关闭期间放弃执行中分析**，不拖延关机。CPU 返回 503，追踪断开连接，工具报错而非保存看似完整结果。
- **停止读取的下载被切断。** 分析须在请求的 `?seconds=` 加十秒内送达。卡住的端口转发或暂停 `curl` 等客户端超过此时间会关闭连接，不能长期维持追踪。关闭监听器开始后一秒仍开放的连接也关闭。
- **每个分析独立连接。** 响应结束关闭连接，复用工具下一请求重新连接。

### 使用其他地址 {#another-address}

`profiler.address` 设置绑定位置，必须 IP 加端口，例如其他 Pod 收集使用 `0.0.0.0:6060`。主机名、缺端口或服务自身 8080 会阻止启动。

:::warning 非回环地址向集群网络开放
监听器没有认证，chart 没有限制连接者的网络策略。因此非回环绑定时，能到 Pod IP 的任何对象都可读分析，除非自行添加 ingress 网络策略。服务在此地址启动时会警告。
:::

### 关闭 {#turning-it-off}

设置 `enabled: false` 或移除配置块，再按启用时方式升级 release。仅该服务 Pod 重启。

## 相关文档 {#related}

- **[初始化实例](./bootstrap.md#install)**：部署监控栈的 `dcctl install` 及标志。
- **[部署与 Operator](./kubernetes-operator.md)**：chart 如何生成带健康探针的逐服务工作负载。
