---
sidebar_position: 5.5
title: Grafana 仪表盘
---

# Grafana 仪表盘 {#grafana-boards}

`dcctl install` 部署的[监控栈](./observability.md#the-monitoring-stack)为每个实例提供八个 Grafana 仪表盘：一个回答“一切都在运行吗？”的**总览**，以及七个**阶段仪表盘**，分别对应消息从设备到送达通知所经过的各个阶段，外加支撑这些阶段的平台层。每个仪表盘用于回答一个问题，并链接到你接下来会打开的仪表盘。

仪表盘上的所有内容都来自服务已经导出的指标（参见[指标参考](./metrics-reference.md)），以及监控栈采集的标准 Kubernetes、数据库和消息代理序列。无需额外开启任何东西。

## 找到仪表盘 {#finding-the-boards}

[打开 Grafana](./observability.md#reaching-grafana)，找到文件夹 `devicechain-<实例>`。每个实例都有自己的文件夹和自己的一份仪表盘，标题为 `DeviceChain — <名称> [<实例>]`。仪表盘只读取所属实例的数据：没有实例选择器，一个实例的仪表盘无法显示另一个实例的数据。

每个仪表盘顶部都有 **DeviceChain boards** 菜单，列出该实例的所有仪表盘；总览末尾有一行指向各阶段仪表盘的链接。唯一需要你选择的是**数据源**，即监控栈安装的 Prometheus。

每个仪表盘都以同一张 **Firing alerts** 表开头。它列出此刻正在为该实例触发、且属于该仪表盘所在阶段的告警，因此阶段正常时阶段仪表盘是安静的。表为空表示没有告警触发。该表读取实例的 Prometheus 规则，如果这些规则没有安装，表会一直为空。总览列出全部告警，所以没有任何阶段仪表盘匹配的告警仍然能在总览中看到。

## 如何阅读面板 {#reading-a-panel}

每个面板都有说明：把鼠标悬停在面板角落的小 `i` 上即可。它会说明面板统计什么、正常值是什么样、异常时该怎么做。以下约定适用于所有仪表盘。

- **指标卡的颜色表示含义。**绿色表示健康，黄色表示尽快查看，红色表示需要处理。名称含“over the range”或“selected range”的指标卡统计你所选时间范围内发生的情况，需要回溯更远就拉长时间范围；没有这类字样的指标卡显示当前值。
- **应该保持为零的指标卡显示 `0`，而不是“No data”。**当计数没有可统计的内容时，仪表盘有意用零代替。面板显示“No data”则是在告诉你该序列尚未上报，这与什么都没发生不同。

### 积压面板，以及读取方宕机时它们显示什么 {#backlog-panels}

**积压**（backlog）是流中尚未交给某个读取方的消息数量。它是平台中最有用的早期预警，因为读取方比生产方慢时，会在任何故障出现之前很久就开始落后。积压的含义及其测量方式参见[持续落后的消费者](./observability.md#consumer-backlog)。

每个读取服务都会上报自己的积压，但已经停止的服务无法上报任何东西。读取方崩溃、正在重启或被缩容到零的时候，恰恰最需要这个数字，而此刻变成空白的仪表盘毫无用处。因此积压面板有**两个来源，按顺序取用**：

1. **读取方自己的上报**，在读取服务运行时使用。
2. **消息代理自己对同一读取方的计数**，取自代理的指标，在读取方不上报时使用。

可以这样回退的面板会在说明里写明（*“While the reading service is down this shows the message broker's own count”*）。两个来源统计的是同一批消息，因此一个接替另一个时，面板上的曲线保持连续。

共有十九个面板带有代理回退：

- **Messaging：***Unread backlog per reader*、*Handed out, not yet acknowledged* 和 *Share of stream capacity left unread*。
- **Ingest：***Backlog against its limit*、*Backlog and stream fill over time*、*Arriving vs backlog growth*（其积压增长曲线）和 *Capture backlog in messages*（其第一条线）。
- **Event processing：***Consumer lag (undelivered)*、*In-flight (delivered-unacked)*、*Consumer backlog*、*Derived-event backlog* 和 *Consumer unread backlog (share of stream capacity)*。
- **Delivery：***Waiting to be sent*、*Dispatch backlog*、*Notification backlog* 和 *Update report backlog*。
- **Persistence：***Events waiting to be stored* 和 *Updates waiting to be applied*。
- **总览：***Largest backlog share* 指标卡。

Messaging 的 *Messages waiting to be redelivered* 始终是代理自己的计数，因此没有可回退的读取方来源。

有两点限制需要了解：

- **回退依赖代理的指标。**监控栈会采集代理，所以默认安装下回退可用。如果你使用自己的 Prometheus 且没有采集代理，读取方停止时积压面板会像以前一样变成空白。
- **丢失计数器没有回退。**统计流在读取方读取之前就丢弃的消息的指标卡和曲线由读取方自己计数：Ingest 的 *Messages lost before being read, over the range* 和 *…, any consumer*，以及 *Capture backlog in messages* 的第二条线（未读缺口）；Messaging 的 *Messages lost unread (range)* 和 *Messages lost unread /s*；以及总览的 *Messages lost*。读取方宕机期间这些指标卡显示 0，这并不能证明没有丢失。中断期间被丢弃的消息只有在读取方恢复后才会被计入，作为下限，并记在其恢复的时间点。中断期间请根据仍然有效的 *Share of stream capacity left unread* 判断风险：达到 100% 时，流开始丢弃读取方尚未看到的消息。参见[消费者从未读取的消息](./observability.md#unread-loss)。

序列缺失表示“未测量”，绝不表示“没有等待的消息”。如果积压面板为空，说明两个来源都没有上报：请检查代理是否被采集（Messaging 的 *Broker servers up*），以及读取方的 durable 是否存在。

## 总览 {#overview-board}

<!-- screenshots: G13 -->

**用它一眼回答“一切都在运行吗？有没有东西在丢失？”**从这里开始，再沿链接进入不是绿色的那个阶段的仪表盘。

| 分组 | 显示内容 |
| --- | --- |
| Is everything up? | 未就绪领域、抓取目标宕机、缺失副本、容器重启、丢失消息，以及所有读取方中最大的积压占比。 |
| How much is flowing | 各阶段流经流水线的事件，以及未成功的工作，并排显示，便于对照着读。 |
| Failures that should stay at zero | 在入口被拒绝的消息、失败的消息、被放弃或无法投递的指令，以及注入的规则事件与发出的规则动作。任何非零指标卡都值得查看。 |
| Replicas and restarts | 各 deployment 的可用副本、各 Pod 的容器重启，以及下文介绍的两张领域指标卡。 |
| Stage boards | 指向七个阶段仪表盘的链接。 |

支撑它的告警：全部告警。对这个仪表盘最重要的是 [`InstanceAreaUnavailable`](./observability.md#container-restarts) 和 [`InstanceContainerRestarted`](./observability.md#container-restarts)。

### 宕机的领域与缩容到 0 的领域 {#scaled-to-zero}

某个领域的 Pod 全部消失后，它不再上报任何自己的内容：就绪状态、指标、抓取目标都没有了。此时 *Areas not ready* 和 *Scrape targets down* 指标卡可能显示零，而该领域根本没有运行，因为缺失的序列不等于零。另有两张指标卡改为读取 Kubernetes 的 deployment，它们始终记录期望什么、实际有什么：

| 指标卡 | 统计 | 颜色 | 告警 |
| --- | --- | --- | --- |
| *Areas with no pod available* | **期望**至少一个 Pod 却没有任何可用 Pod 的领域和控制台。 | 从 1 起为红色。 | `InstanceAreaUnavailable`，十分钟后触发。 |
| *Areas scaled to 0* | 被设置为运行**零**个 Pod 的领域和控制台。 | 从 1 起为蓝色。 | 无。 |

区别在于意图。降为零的领域是被手动缩容的（`kubectl scale … --replicas=0`），例如因维护而暂停，所以它作为独立状态显示，而不是故障，也绝不会告警。两张指标卡不会重复统计同一个领域。如果某个领域出现在 *Areas scaled to 0* 中而你并没有缩容它，请把它扩回去：它没有在提供服务，而且不会有任何告警通知你。这两张指标卡统计的是各领域和控制台（它同样是实例的一个 deployment）；消息代理的调试 Pod 不属于领域，因此被排除在两张指标卡和告警之外。

实例的配置档不运行的领域根本没有 deployment，因此两张指标卡里都不会出现。

## Ingest {#ingest-board}

<!-- screenshots: G13 -->

**事件没有到达、被拒绝，或 MQTT 路径承压时使用。**它覆盖前门：传输层接受什么、拒绝什么，以及等待转发的 MQTT 消息积压。

| 分组 | 显示内容 |
| --- | --- |
| Backlog of MQTT messages waiting to be forwarded | 捕获流的积压相对其上限的比例、占用率、读取前丢失的消息和 leader 变更。积压面板带有[代理回退](#backlog-panels)（*Capture backlog in messages* 的未读缺口线除外）。超过 10% 且仍在上升时，增加 `event-sources` 副本；达到 100% 时最旧的未读消息会被丢弃。 |
| Refused or lost at the door | 为保护未读数据而被拒绝、被速率限制拦下、因格式错误被拒绝、因租户已删除或 MQTT 正文中含凭据被拒绝的消息，以及失败的外部发布。 |
| Protection of the MQTT message store | 必需消费者是否在位、被恢复的存储设置、没有大小上限的网关流，以及因存储已满或繁忙而被拒绝的连接。 |
| Device presence and external sources | 未记录的上线与下线变化、读取方不是恰好一个的外部 MQTT 来源，以及设备登录缓存。 |
| Device self-registration | 已注册设备、按原因划分的拒绝，以及响应时间。 |
| Sparkplug ingest, LwM2M ingest | 仅在这些可选服务运行时出现：活跃读取方或服务状态、失败，以及各协议的流量。 |

支撑它的告警：[接入链路背压](./observability.md#ingest-backpressure)和[消费者从未读取的消息](./observability.md#unread-loss)中介绍的 MQTT 与捕获流告警，[无人读取的外部 MQTT 来源](./observability.md#external-mqtt-owner)中的外部来源告警，[设备自注册](./observability.md#self-registration-alerts)中的注册告警，以及凭据缓存和速率限制器告警，还有 Sparkplug 与 LwM2M 接入告警。

## Messaging {#messaging-board}

<!-- screenshots: G13 -->

**某处变慢或积压、但你还不知道在哪里时使用。**消息代理连接所有阶段，所以这个仪表盘展示共享的底层设施：代理服务器、它们保存的流、谁在读取以及落后多少，以及各副本是否同步。

| 分组 | 显示内容 |
| --- | --- |
| Message broker servers | 在线服务器、集群 leader、因过慢被断开的客户端、请求失败、磁盘、CPU、内存和连接。 |
| Streams | 占用超过 80% 的流、拒绝新事件的流、按大小和按消息数的占用率，以及磁盘上最大的流。 |
| Readers of streams | 每个读取方的未读积压、已交付未确认的消息、流容量中未读的占比、读取前丢失的消息，以及超过确认窗口的消息。[积压回退](#backlog-panels)适用于前三项。 |
| Publishing and backpressure | 被背压拒绝的事件、发布耗时，以及发布方等待代理的时间占比。 |
| Copies and leaders | 副本数少于配置或副本已过期的流、流 leader 变更，以及哪些服务器担任过 MQTT 网关存储的 leader。 |
| Failed deliveries and dead letters | 耗尽投递次数的消息、已保存、未保存或丢失的死信，以及静默失效的代理连接。 |
| Key-value buckets and caches | 由内存应答的缓存读取、已用内存、被逐出的条目、失败和操作耗时。 |

支撑它的告警：[`JetStream*`](./observability.md#consumer-backlog) 告警、[`BrokerConnectionDiedSilently`](./observability.md#broker-connection-dead)、[`ReaderHeldMessagePastAckWait`](./observability.md#held-past-ack-wait)、[`MaxDeliveryRecordsWaiting`](./observability.md#max-delivery-records)、[`ReplayCoveredDeliveriesExhausted`](./observability.md#ingest-backpressure)、[`BrokerPresenceLosingTransitions`](./observability.md#event-resolution)、[复制](./observability.md#replication)告警和死信告警。

## Persistence and state {#persistence-board}

<!-- screenshots: G13 -->

**事件已被接受却没有出现，或设备实时状态过期时使用。**它跟随事件进入事件存储和设备实时状态，然后检查下面的数据库。

| 分组 | 显示内容 |
| --- | --- |
| Event storage | 每秒存储的事件、未存储的事件、重试、进行中的写入、存储一个事件的耗时、每个事务的事件数，以及等待存储的事件（带有[积压回退](#backlog-panels)）。 |
| Live device state | 设备状态投影的同一组指标：每秒更新、丢失、重试、每次更新耗时，以及等待应用的更新（带有回退）。 |
| Background jobs in the event store | 距上次历史修复的时间、计算指标重建，以及汇总刷新的耗时与冲突。 |
| Databases | 服务器与集群状态、正在流复制的备库、连接、最长的未结束事务、提交与回滚、大小、备库延迟，以及哪个 Pod 是主库。 |
| Write-ahead log and backups | 等待发送的日志段、发送失败、距上次发送日志段的时间，以及最新基础备份的年龄。 |
| Disk space | 最满的数据库磁盘和备份磁盘、事件存储磁盘写满前的最短时间，以及最大的历史表。 |
| Tenant removal | 进行中的删除、最久未结束的删除、完成与失败，以及拖住删除的存储。 |

关系型数据库和备份对象存储位于集群内所有实例共享的命名空间中。这些面板在说明里写明了这一点，显示的是共享数据库，而不只是本实例的。

支撑它的告警：[`DeviceStateDroppingEvents`](./observability.md#live-state-projection)、[备份归档](./observability.md#backup-archiving)和[数据库控制平面](./observability.md#database-control-plane)告警、`Database*` 卷告警、`BackupDestination*`，以及 `TenantPurge*`。

## Event processing {#event-processing-board}

<!-- screenshots: G13 -->

**规则不触发、告警延迟，或反应没有被执行时使用。**它监控[事件处理](../concepts/event-processing.md)中介绍的检测与反应引擎。

| 分组 | 显示内容 |
| --- | --- |
| Pipeline health | 消费滞后、在途消息、水位线滞后、生效的规则、每秒应用的事件和每秒检查点。消费滞后、在途消息和消费者积压面板带有[积压回退](#backlog-panels)。 |
| DETECT engine | 扇出吞吐量、发布前被拒绝的检测结果、求值错误，以及版本丢弃安全闸。 |
| REACT dispatcher | 已分发的动作、分发故障、派生事件积压（带有回退）、并发度和分发延迟（p50 与 p99）。 |
| State budget | 存活的按键状态条目，以及超出规则、存活键或保留样本预算的租户。 |
| Snapshot and restore | 检查点开销、快照大小和启动时的恢复开销。 |
| JetStream streams | 流的占用率，以及消费者未读部分占流容量的比例。 |
| Geofence archive | 未解析或不一致的围栏几何，以及几何缓存的命中率、占用和逐出。 |
| Rule health and derived publish | 被丢弃的规则健康更新、积压与写入延迟，以及发布派生事件所占的时间比例。 |

支撑它的告警：`Detect*` 和 `React*` 告警、`GeoFenceSetPublishFailing` 和 `DeviceFactPublishFailing`。引擎的消费者如何运行与扩缩容，参见[运行检测引擎](./detection-engine.md)。

## Command delivery {#command-delivery-board}

<!-- screenshots: G13 -->

**指令没有到达设备，或批量写入被拒绝时使用。**

| 分组 | 显示内容 |
| --- | --- |
| Fleet writes | 每秒创建、重放和被拒绝的批次、已接受的设备、被部分批次排除的设备，以及按错误码和按限制（上限或预留）划分的拒绝。 |
| Dispatch | 滞留的认领、丢失的认领和无法投递的指令。 |
| Holds | 已设置与已释放的保持，以及在线状态读取错误。 |

支撑它的告警：所有 `Command*` 告警，详见[指令投递](./observability.md#command-delivery-alerts)。

## Delivery {#delivery-board}

<!-- screenshots: G13 -->

**告警已经发生但没有人收到通知，或连接器、更新卡住时使用。**这是最后一程，从规则的反应到某个人或外部系统。连接器和软件更新分组会在运行对应可选服务时出现。

| 分组 | 显示内容 |
| --- | --- |
| Outbound connectors | 已发送、重试中、已放弃、被阻止的目的地和丢失的分发，按结果、连接器类型和失败原因划分，另有分发积压。 |
| Hand-off from automation rules | 交给各出口的动作、无法执行的动作、因超出配额而暂缓的动作，以及重试后被搁置的规则结果。 |
| Alarm notifications | 已处理、重试中和从未送达的通知、拒绝、免打扰时段的暂缓、升级轮次、处理耗时和通知积压。 |
| Software updates | 按结论划分的设备上报、按结果划分的下载与授权、发布进度、失去响应的更新尝试，以及更新上报积压。 |

四个积压面板都使用[代理回退](#backlog-panels)。

支撑它的告警：`ConnectorDispatchRateLimited`、`ReactConnectorEgressShedding` 和 `ReactShedLettersOverBudget`。通知和软件更新分组没有专属告警，请在这个仪表盘上查看。

## Platform {#platform-board}

<!-- screenshots: G13 -->

**某个服务看起来不健康、需要判断是服务本身还是其下的节点有问题时使用。**这是唯一一个关注进程而非消息流的仪表盘。

| 分组 | 显示内容 |
| --- | --- |
| Service health | 缺失副本、未就绪 Pod、容器重启、上次因内存不足而停止的容器，以及可用副本与期望副本的对比。 |
| CPU and memory | 内存相对限制、CPU 相对请求，以及 CPU 限流。 |
| Service runtime | Goroutine、堆、常驻内存、垃圾回收次数与停顿，以及已打开文件句柄相对上限。 |
| Sign-in | 按结果划分的凭据检查、被限流的登录、单点登录拒绝、身份提供方获取失败和令牌密钥获取。 |
| Tenant deletion | 与 Persistence 相同的删除视图，从平台侧查看。 |
| Limits and safety gates | 被关闭的安全闸、按平台默认值计量的租户、已用的共享额度、被舍弃的出站动作，以及设备流量和出站调用触及的上限。 |
| AI inference | 按结果划分的推理调用和使用的模型令牌。 |

支撑它的告警：[`InstanceContainerRestarted`](./observability.md#container-restarts)、`Sso*` 和凭据存储告警、`TenantPurge*`、[`SafetyGateDisabled`](./observability.md#safety-gates) 和 [`TenantsMeteredAtPlatformDefault`](./observability.md#tenant-ceilings)、`RateLimiterOverflowInUse`、`RateMeteringClockFallback`、`ReactShedLettersOverBudget` 和 `ConnectorDispatchRateLimited`。

## 仪表盘未覆盖的内容 {#not-covered}

- **Kubernetes Operator** 运行在它自己的命名空间中。没有任何组件采集它的指标，也没有仪表盘显示它，连副本面板也不显示，因为那些面板只读取本实例的命名空间。
- 每个服务的 **HTTP 与 GraphQL 请求速率、错误和延迟**没有导出，因此没有仪表盘绘制它们。
- 从接入到存储或检测的**端到端事件年龄**没有被测量。积压面板只是替代：积压增长就意味着延迟增长。最接近的真实年龄度量是 `detect_watermark_lag_seconds`，Event processing 将其显示为 *Watermark lag*，即检测引擎水位线落后墙上时钟多少。
- **边缘代理**运行在客户硬件上，不被采集。

## 相关内容 {#related}

- [可观测性与指标](./observability.md)：每个服务暴露什么，以及上文提到的每个告警的运行手册条目。
- [指标参考](./metrics-reference.md)：按服务列出的所有序列。
- [调优实例](./tuning-an-instance.md)：仪表盘显示触及限制时该调整什么。
