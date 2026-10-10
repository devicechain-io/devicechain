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

- **`/healthz`**：存活检查，判断进程是否仍能工作，还是需要重启。服务与消息代理连接永久关闭后检查失败，使 Kubernetes 重启 Pod。流读取循环若连续两分钟读取失败，期间没有代理响应，也会结束进程并由 Kubernetes 重启。此前按逐渐增加、最长五秒的间隔重试。安静流的循环每几秒检查代理是否仍响应，重新连接成功也算响应，因此相隔两分钟的两次失败不会被当成一次持续失败；长期代理中断后恢复，也不会因此重启服务。单纯代理不可达不算读取失败：读取等待，服务持续重连，代理恢复后继续。参见[容器重启](#container-restarts)。
- **`/readyz`**：就绪检查，判断是否可接收流量。未就绪服务会被 Kubernetes Service 从流量轮转中排除，参见[部署与 Operator](./kubernetes-operator.md)。

所有 Pod 使用同一约定，因此监控栈统一抓取整个实例，无需逐服务集成。

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
- **每实例一个文件夹。** 每实例有独立 `devicechain-<instance>` 文件夹，包含自己的全部面板副本。副本限定实例、标题包含实例 ID，无需选择实例。`dashboards/` 下是按实例渲染的模板，不用于手动导入。文件夹来自 ConfigMap 的 `grafana_folder: devicechain-<instance>` 注解。sidecar 必须设置 `FOLDER_ANNOTATION=grafana_folder`，提供器必须设置 `foldersFromFilesStructure: true` 才按文件夹组织；`dcctl install` 部署的栈已设置。如果安装时使用 `--no-monitoring`，自行 Grafana sidecar 未设置这两项，注解被忽略，各实例面板会平铺在一起；仍各自独立并限定实例，只是没有文件夹。
- **同集群实例的面板互不干扰。** 所有实例升级到支持独立文件夹的 chart 后，移除或升级一个实例不影响其他面板。此前旧 chart 实例仍在文件夹外共享每种仪表盘一个文件，升级任一实例会移除该共享文件；旧实例面板会缺失，直到 sidecar 下次重新扫描。监控栈早于此布局的集群，需要重新运行 `dcctl install`，否则所有实例面板仍在文件夹外。
- **旧面板链接失效。** 升级实例不再使用原共享 ID `dc-event-processing-ops`、`dc-command-delivery-ops`；全部升级后，旧书签失效。
- **删除实例留下空文件夹。** 面板移除，但空的 `devicechain-<instance>` 文件夹仍留在 Grafana，需手动删除。

## 登录 Grafana {#signing-in-to-grafana}

Grafana 使用自己的**管理员登录**，目前不能通过 DeviceChain 单点登录。

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
- 流共享，因此拒绝作用于**所有租户**。先按读数检查各租户自己的接入限额，再检查共享门控。默认每秒 1000 读数，单租户设备本身无法发送超过[默认允许量](../concepts/governance.md#ingest-default)所述集群、单个事件来源副本的默认高可用部署实测存储能力。多个租户合计、层级提高限额的租户、小集群上的租户，以及用许多租户名字提交 HTTP 的客户端仍可能超出，因为名字在验证凭据前已计量。门控随后拒绝所有人。
- 满流代理仍会丢弃最旧消息，三种情况仍可能丢失未读事件：两次测量之间写入量耗尽的历史超过前 30 秒观测的丢弃量；流首次满时几乎没有已处理历史，尚未测得丢弃速率；拒绝期间仍接受连接/断开转换，见下文。[未读损失告警](#unread-loss)会报告。
- 删除租户移除两条流中的事件，释放空间，一般不触发拒绝。例外是删除满流最旧、且数量少到删除后仍满的事件，看起来像历史快速被丢弃，可能暂时拒绝直到消费者追上，不会丢失。

拒绝期间各传输行为：

| 传输 | 设备所见 |
| --- | --- |
| HTTP | 先通过租户限额后返回 `503` 和 `Retry-After: 10`；超租户限额返回 `429`。事件未存储，应重试。 |
| MQTT（平台代理） | 无提示，代理在平台能拒绝前已确认。消息在捕获流等待，流满时丢弃最旧消息（`JetStreamDurableLostUnread`）。 |
| 外部 MQTT 代理 | 无提示，消息已确认。丢弃并计入 `devicechain_eventsources_total_msg_backpressured{source}`；超租户限额改计入 `devicechain_eventsources_total_msg_rate_limited`。 |
| Sparkplug | 读数不重试、直接丢弃，计入 `devicechain_sparkplugingest_ingest_failures_total`。 |
| LwM2M | 通知丢弃，计入 `devicechain_lwm2mingest_notify_ingest_dropped_total`，下一通知取代丢失内容。 |

代理连接通知、Sparkplug birth/death、LwM2M 注册等转换在拒绝期间仍接受，因为拒绝后无人重新发送。它们使用拒绝阈值以上保留的 10% 空间。背压不限制接受量；代理监听仍受租户限额约束。设备自行决定连接频率，循环重连设备群可能耗尽余量，代理随后像以前一样丢弃最旧事件，包括未读内容。只有两个关键消费者控制背压，设备状态或事件处理慢不会控制，仍由上述未读损失告警报告。事件处理实际位置在自己的检查点，门控不可见，由 `ReplayCoveredDeliveriesExhausted` 监控。

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

日志也明确服务器：`Disconnected from NATS: the server left 2 pings sent 10s apart unanswered…` 或 `Disconnected from NATS: a write to the server made no progress for 10s…`，随后 `Reconnected to NATS`。事件来源用于观察设备连接的第二条代理连接受同样边界限制，但不计入这些指标，日志为 `A write to the NATS server made no progress` 或 `Lost the NATS system-account connection`。

## 无人读取的外部 MQTT 来源 {#external-mqtt-owner}

你运行的外部 MQTT 代理来源，任一时刻只由一个事件来源 Pod 读取，其他备用 Pod 仍就绪，因为继续提供其他功能，参见[传输能力矩阵](../reference/transport-matrix.md#external-mqtt-broker)。无人读取来源不一定是 Pod 故障，可能接管 Pod 无法连接你的代理、每十五秒重试，或 Pod 无法连接平台代理来协商所有权。

- **`devicechain_eventsources_external_mqtt_owner{source}`**：读取来源的 Pod 为 1，其他为 0。每个 Pod 从启动起为每个外部来源导出，以 `sum` 合并，1 健康。
- **`devicechain_eventsources_total_msg_not_owner{source}`**：失去所有权后仍投递给旧 Pod 的消息，直接丢弃不存储，因为接管 Pod 收到相同消息。

总和两分钟不为 1 时，`ExternalMqttSourceNotReadByOnePod`（warning）触发。0 表示无人读取，期间外部代理投递内容丢失；最近接管 Pod 日志说明原因。没有外部来源的安装不导出此序列，告警安静。

## 超过确认窗口的消息 {#held-past-ack-wait}

代理为每条交付消息提供固定确认窗口。窗口关闭仍未确认，会重新交付，服务按新消息处理。告警通知和对外连接器的工作是向平台外慢速发送，因此可能重复通知或调用 webhook。两者仅在有空闲 worker 时读取相应数量消息，避免排队消耗窗口；每次发送也会在窗口结束前留有余量地截止。下列告警报告仍超时的情况。

| 告警 | 严重程度 | 含义 | 处理 |
| --- | --- | --- | --- |
| `ReaderHeldMessagePastAckWait` | warning | 处理器持有消息超过窗口并重投递。`stage=worker` 表示发送太久，可能发送两次；`stage=buffer` 表示交付 worker 前消息被丢弃，随后处理重投递版本。 | worker 情况检查 `durable` 指定服务的慢或无响应目标；buffer 情况表示服务跟不上流。 |

指标为 `devicechain_<area>_reader_held_past_ack_wait_total{durable, stage}`，读取器创建时就以 0 存在，`increase()` 能看到 Pod 首次发生。每 Pod 仅统计自己持有内容，用 `sum`，不用 `max`。

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

所有事件存储或评估前，都由设备管理解析。解析器先从关系数据库读取一次以认证凭据，再同时从代理键值存储查配置、关系和分组范围，然后交付解析结果供发布。键值存储无法回答时，逐个查数据库，所以最多持有一个数据库连接。多数时间用于等待响应而非 CPU。多个解析器并行；全部忙时事件按序等待，Pod 交接队列最多 100 个，最近拉取批次最多另有 64 个，其余在流中。

| 指标 | 含义 |
| --- | --- |
| `devicechain_devicemanagement_resolve_workers` | Pod 解析器数量。 |
| `devicechain_devicemanagement_resolve_inflight` | 忙碌解析器数量。持续等于 worker 数时，到达快于解析，前方排队，入站消费者待处理数增长，参见[持续落后](#consumer-backlog)。 |
| `devicechain_devicemanagement_resolve_messages_total` | 按结果统计已解析事件。全部忙时，增长速率是 Pod 每秒解析能力。 |

`resolve_duration_seconds` 包括等待交付结果的时间。最小桶 5 毫秒，低于它的分位数是估算，不是测量。

### 调优 {#tuning-it}

| 设置（`device-management`） | 默认值 | 作用 |
| --- | --- | --- |
| `resolution.workers` | `10` | 并行解析器。凭据在副本最近五秒未验证时，从数据库读取并持有一个连接，见[键值缓存](#kv-caches)。必须低于服务连接池 `rdbConfiguration.maxOpenConnections`（默认 20），池还供 GraphQL、MQTT 连接检查、告警触发解除消费者使用。允许超过一半，但启动时记录。键值查找并行，数据库查找仍串行，因此最多一个连接。 |
| `inMemoryCache.perDeviceCacheEntries` | `131072` | 每副本三个逐设备内存缓存各自的条目上限：按令牌设备、追踪关系和分组成员，见[键值缓存](#kv-caches)。 |
| `inMemoryCache.perDeviceCacheMiB` | `24` | 各缓存在每副本的内存 MiB 上限，调高时同步提高服务内存限制，见[键值缓存](#kv-caches)。 |

`resolve_inflight` 持续等于 worker 数且 CPU 有余量时可增加。CPU 已到限制时，增加解析器无效，应增加 CPU，参见[服务容量](./bootstrap.md#service-sizing)。进程内针对三节点代理测量、各查找 750 微秒且依次执行时，5 个解析器约每秒 1,500 事件，10 个约 2,900。完成顺序可能比到达乱序几分之一秒，检测按进入解析流顺序处理。超范围配置阻止启动，错误指出设置；启动日志列出实际值。

## 事件持久化 {#event-persistence}

事件管理批量写入。每个写入器拿取已等待事件到上限，在一个事务提交，对批次每个租户每张表写一次，而非每事件一次。连接和断开仍在批次中逐条写。事务提交后才确认事件。批次中某事件被拒绝时，事务全部回滚；拒绝事件单独重写，按非批量方式重试或报告，其他事件排除它后重新提交。数据库拒绝多事件语句中的行时无法指出原事件，因此先在新事务逐事件写入来定位。若失败不是单个事件原因，例如数据库连接丢失，全部事件逐个重写。

写入器拿到不足整批事件时，最多等待十毫秒（`persistence.lingerMillis`）再提交。已有积压时立即拿取，因此等待不增加积压处理成本。只有全部写入器忙时才减少提交：新事件先交给空闲写入器，而非等填满的写入器，所以每副本低于约几百事件/秒时仍单独提交，只多等最多十毫秒。复制数据库大部分提交时间等待备用副本，一批仅支付一次，因此负载下收益最大。

| 指标 | 含义 |
| --- | --- |
| `devicechain_eventmanagement_persist_batch_size` | 每提交事务事件数。小批表示跟得上，接近上限表示忙。10 个写入器共享流时，即使存储落后也很少满批，应结合积压。 |
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | 未提交、随后重写事件的批事务。偶发增长通常一个拒绝事件；多事件语句拒绝行时，定位需要再一次尝试，可能增长两次。持续增长表示反复拒绝，例如删除租户设备仍发送。每个包含这些事件的批次多花一个事务，不论数量；事件在 `persist_messages_total` 显示 `failed` 或 `retry`。 |
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

## 停止响应的缓存 {#kv-caches}

设备管理将每事件重复的设备令牌、追踪关系、类型已发布配置和分组成员查找缓存于 NATS 键值桶。查找最多等半秒。超时或无服务器响应时，跳过该桶五秒，直接查保存同数据的数据库，再试一次查找；只有成功才停止绕过。首次绕过警告 `A key-value cache stopped answering`，恢复时记录 `A key-value cache is answering again`，含时长和期间回退数据库的读写数。桶明确返回的错误，例如已满拒绝写入，计数但不触发绕过。

常见原因是 NATS 服务器网络失联却未关闭连接。桶所有副本都响应读取，其他服务器约一到一分半发现失联前，部分读取仍发往旧服务器。事件继续解析，但增加数据库读取。

每个副本还将桶读写结果在内存保留最多五秒，桶 TTL 更短时取更短，直接响应，包括桶被绕过期间。五秒从读取而非最近使用计算。三个逐设备缓存各最多 131,072 条或 24 MiB，由 `inMemoryCache.perDeviceCacheEntries` 和 `inMemoryCache.perDeviceCacheMiB` 配置，大约容纳无追踪关系 87,000 设备、有一条关系 26,000 设备。按类型和租户的配置、分组范围缓存各 4,096 条或 4 MiB。满时移除最近最少使用条目；插入时也从该端移除过期条目，遇到首个未过期即停止。NATS 报告不存在的结果不缓存。相比只用桶，变更最多额外五秒后才到达其他副本事件；期间可能按旧记录解析已删除或同令牌重建设备，或按旧分组范围评估规则。携带设备凭据的事件，设备来自下一段的凭据缓存。

**设备凭据。** 各副本将刚验证的凭据及其设备从读取起内存缓存五秒，因包含密码，绝不放键值桶。固定最多 65,536 凭据或 16 MiB。每次使用仍比较密码及到期时间，验证失败不缓存。凭据或设备变更使执行变更的副本立即清缓存，并用 NATS 通知其他副本。因此撤销或跨副本设备删除，通常所有副本下个事件生效；通知丢失也在五秒内生效。MQTT 连接始终读数据库。上报间隔超过五秒的设备，每事件仍读数据库凭据。

**上报间隔超过五秒的设备群。** 无论缓存多大，内容从读取起仅保留五秒，因此这些设备永不内存命中，每事件一次桶读取。三节点 GKE 上该读取约 1.5 毫秒；默认 10 解析器每事件花此时间，所以慢于每几秒上报的设备群。可增加池范围内 `resolution.workers` 或设备管理副本。特征是 `kv_cache_local_lookups_total{cache="relationships-by-source", result="miss"}` 接近事件率，而条目数远低上限。缓存容不下设备群则 `kv_cache_local_evictions_total{reason="capacity"}` 接近事件率，条目数或字节数到上限。此时提高边界并同步提高内存限制。默认六个内存缓存含固定 16 MiB 凭据，共最多 96 MiB；未设置 `GOMEMLIMIT` 时，回收前堆可增长至持有量约两倍。

变更后移除条目（设备删除、配置发布）绝不跳过，因为只有桶主副本接受，最多等五秒。仍失败会记录 `A key-value cache eviction failed`，旧条目可服务至配置 TTL 到期，加已有内存副本最多五秒。

- **`devicechain_devicemanagement_kv_cache_unavailable{cache}`**：桶绕过时为 1。
- **`devicechain_devicemanagement_kv_cache_failures_total{cache, op, reason}`**：超时 `timeout` 或错误 `error`。事件查找并行，绕过前多个查找可能同时超时，各自计数。
- **`devicechain_devicemanagement_kv_cache_bypassed_total{cache, op}`**：回退数据库的读写。
- **`devicechain_devicemanagement_kv_cache_request_duration_seconds{cache, op}`**：桶操作时长。读写半秒截止，移除五秒。内存命中不访问桶，所以 `op="get"` 只统计内存无法回答的查找。
- **`devicechain_devicemanagement_kv_cache_local_lookups_total{cache, result}`**：内存回答 `hit` 或交给桶 `miss`。
- **`devicechain_devicemanagement_kv_cache_local_evictions_total{cache, reason}`**：五秒过期 `expired`、满容量 `capacity`、变更移除 `deleted`。
- **`devicechain_devicemanagement_kv_cache_local_entries{cache}`**、**`devicechain_devicemanagement_kv_cache_local_bytes{cache}`**：副本内存条目和约字节数。过期条目直到查找发现、插入时从最少使用端清除，或需要空间才不再计入。
- **`devicechain_devicemanagement_kv_cache_local_max_entries{cache}`**、**`devicechain_devicemanagement_kv_cache_local_max_bytes{cache}`**：开始驱逐前的条目和字节上限。

未构建内存副本的缓存没有六项 `kv_cache_local_` 序列，目前设备管理所有缓存都有。

凭据缓存有独立序列：

- **`devicechain_devicemanagement_credential_cache_lookups_total{result}`**：内存回答 `hit` 或交数据库 `miss`。
- **`devicechain_devicemanagement_credential_cache_evictions_total{reason}`**：五秒过期 `expired`、容量已满 `capacity`，或本副本/其他副本凭据或设备变更 `revoked`。
- **`devicechain_devicemanagement_credential_cache_entries`**、**`devicechain_devicemanagement_credential_cache_bytes`**、**`devicechain_devicemanagement_credential_cache_max_entries`**、**`devicechain_devicemanagement_credential_cache_max_bytes`**：副本当前持有量和上限。
- **`devicechain_devicemanagement_cache_eviction_broadcasts_total{cache, result}`**：通知其他副本删除缓存的消息，分别为发送 `published`、未发送 `publish_failed`、接收 `received`、无法解析而丢弃 `malformed`。持续发送失败意味着其他副本只能等最多五秒缓存到期才见变更。

任何原因使解析超过五秒，都会记录 `Event resolution is slow` 警告：首个立即记录，此后最多每 30 秒一条，含数量和最慢值。

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
| event-sources | `presence_demote` |
| command-delivery | `command_sweep`, `hold_reconcile`, `stranded_reconcile` |

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
| `DatabaseVolumeFillingFast` | 事件库卷超过一半，按最近十五分钟速率将在一小时内满，持续三分钟。 | 仅主实例增长是未归档 WAL，先修复归档；全部成员增长是数据，扩卷或缩短保留。 |
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
