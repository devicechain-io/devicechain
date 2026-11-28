---
sidebar_position: 10
title: 指标参考
---

# 指标参考 {#metrics-reference}

每个服务都在其指标端口上导出 Prometheus 指标，监控栈无需逐服务配置即可抓取（参见[可观测性与指标](./observability.md)）。本页列出没有在其他地方随功能一并说明的服务专属指标。属于某项功能的指标位于该功能的页面：[接入链路背压](./observability.md#ingest-backpressure)、[维护轮次](./observability.md#maintenance-passes)、[缓存](./observability.md#kv-caches)、[检测循环](./detection-engine.md#loop-phases)以及[边缘服务](./edge-services.md#what-to-watch)。

名称由 `devicechain_`、去掉连字符的服务名称，再加第一列中的名称组成，因此指令投递中的 `batch_refusals_total` 即 `devicechain_commanddelivery_batch_refusals_total`。指标名称后花括号中的内容列出其标签。本页没有任何指标带租户或设备标签，因此抓取它们都不存在基数风险。计数器以 `_total` 结尾，只增不减，应按速率读取。Gauge 则按当前值读取。

下面的大部分内容用于诊断：让你无需阅读日志就能判断服务做了什么。对于运维人员应当采取行动的指标，其行会说明该怎么做，如果有监视它的告警，也会指出告警名称。


## 指令投递 {#command-delivery}

前缀：`devicechain_commanddelivery_`。

三条[指令投递告警](./observability.md#command-delivery-alerts)读取 `command_delivery_claims_stranded_total`、`command_delivery_presence_read_errors_total` 和 `batch_refusals_total{bound="reserve"}`。指令投递仪表盘绘制设备群写入计数器以及主要的领取、扣留和在线状态计数器；其余指标可用于临时查询和您自己的面板。其中有几项预期会很繁忙，并不是故障：被拒绝的触发中稳定的 `not_sole` 占比、因扫描与触发竞争而丢失的领取，以及设备离线期间产生的扣留。`command_delivery_responses_refused_total` 预期为零。`command_delivery_stranded_observed_total` 或 `command_response_lost_unsettled_total` 的速率上升，表示指令正在对没有做错任何事的设备超时。（旧名称中重复的 `command_delivery_` 是历史遗留并保持不变，因为重命名序列会破坏所有读取它的仪表盘。）

| 指标 | 含义 |
| --- | --- |
| `batch_cancel_commands_total{disposition}` | 批量取消所检查的指令，按取消对每条指令能做什么区分。持续存在的 already_sent 占比，表示制动总是来得太晚。 |
| `batch_devices_total{disposition}` | 指令批次解析出的设备，按批次是否向其入队区分。 |
| `batch_enqueues_total{target_kind, outcome}` | 已决定的指令批次，按目标的指定方式和结果区分。 |
| `batch_refusals_total{code, bound}` | 逐设备的批次拒绝，按代码区分，对于上限拒绝还按导致拒绝的边界区分。bound=reserve 表示该设备本可放入租户自己的上限，仅因投递保留量而被拒绝。 |
| `command_dead_letters_not_ours_total` | 从共享流读取到的、描述其他类型工作的死信。已确认并忽略，因为所有生产者都写入同一个流。 |
| `command_dead_letters_unreadable_total` | 此消费者无法处理的死信：没有可解析的租户、正文不是信封，或信封没有指明指令。已确认并计数而不重试，因为重投递不会让格式错误的消息变得可解析。 |
| `command_delivery_claims_lost_total{path}` | 因另一个分发器先领取了指令而放弃的分发，按失败的分发路径区分。“sweep”是周期扫描，“nudge”是指令入队时发出的分发；两者争夺同一行属预期且安全（领取是比较并设置），因此应读取某一路径上有速率而另一路径没有，而不是总数。 |
| `command_delivery_claims_stranded_total` | 因发布失败、释放也失败而停留在 SENT 的指令。在 LwM2M 上，滞留调和器会重新装载这些指令；在 MQTT 上它们仍会作为 TIMEOUT 过期，错误地归咎于设备。 |
| `command_delivery_dispatches_exhausted_total` | 因平台向其设备发布的次数达到配置上限而失败的指令，平台因此停止重试。每一行记录为 FAILED 并指明平台为原因，而不是一直重试到 TTL，再针对一个它从未到达的设备记录 TIMEOUT。 |
| `command_delivery_dispatch_deferred_total` | 因无法确定命令通道而继续排队的命令；每次延后投递尝试均计数。 |
| `command_delivery_holds_placed_total` | 因设备确定缺席而被扣留、不予分发的指令。 |
| `command_delivery_holds_released_total` | 因设备回来而被放回分发队列的扣留指令。 |
| `command_delivery_nudges_applied_total` | 恰好找到一条排队指令并使其通过投递门的分发触发。不是发布次数：在线状态门仍可能扣留该指令或使其失败，与扫描周期上的情况完全相同。 |
| `command_delivery_nudges_declined_total{reason}` | 排空流程拒绝处理的分发触发，按原因区分。较高且稳定的 reason="not_sole" 速率属预期（设备有多于一条排队指令时，触发会让步），不是故障。 |
| `command_delivery_nudges_dropped_total` | 因队列已满而丢弃的分发触发。这是延迟信号，不是错误率：指令仍会在投递扫描时发出，扫描是所有触发之下的兜底。 |
| `command_delivery_nudges_requested_total` | 被接受进入入队时分发队列的分发触发，每条通过 createCommand 创建的指令一个（机群批次不发出任何触发）。 |
| `command_delivery_presence_read_errors_total` | 无法读取在线状态投影的扫描轮次；该门失败即放行，因此这里持续有速率，表示指令正在无门控地分发。 |
| `command_delivery_presence_gate_off_dispatches_total` | 由于缺少配置（服务密钥或 device-state 端点）导致在线状态门被关闭，因而未经在线状态检查就分发的指令数。门已接通时为零。这里出现任何速率，都表示发往离线设备的指令正在被发布并丢弃：[`SafetyGateDisabled`](./observability.md#safety-gates) 告警会因同一原因触发，command-delivery 也会每小时最多一次重复输出一条点名缺失键的 WARN。 |
| `command_delivery_disabled_gate_errors_total` | 无法为某个分发批次读取已停用设备记录（该批次不分发），或无法从 device-management 同步该记录的次数。持续有速率表示指令被延迟，而不是丢失。 |
| `command_delivery_disabled_gate_ready` | 仪表。本进程列出租户并读取每个租户的已停用设备后为 `1`，此前为 `0`。尚未在任何地方填充过记录的租户在其为 `0` 时不会被分发；若它卡在 `0` 且 `command_delivery_disabled_gate_errors_total` 上升，说明重启发生在 user-management 或 device-management 故障期间。 |
| `command_delivery_disabled_gate_pending_tenants` | 仪表。本进程尚未读取其已停用设备记录的租户数；这些租户的指令在等待。已删除或不再出现在列表中的租户不再计入。 |
| `command_delivery_window_commands_undefined_total` | 在等待维护窗口、却因其设备类型当前所用的配置文件版本不再定义而失败且从未发送的指令。每条都以 `FAILED` 结束，结果为 `NOT_DELIVERED`。 |
| `command_delivery_claims_refused_disabled_total` | 因指令的设备在分发读取已停用设备记录之后才被停用，而被领取拒绝的分发。不会发送任何内容，下一次扫描会保留该指令，且不计入 `command_delivery_claims_lost_total`。 |
| `command_delivery_responses_dead_lettered_total` | 在记录它们的所有尝试都失败后写入死信流的设备指令响应，使设备确实给出的应答可以被看到，而不会让其指令看起来没有被应答。 |
| `command_delivery_responses_refused_total` | 因发布的设备并不拥有其所指指令而被拒绝的设备响应。预期为零：要么某设备在替另一设备应答，要么分发把指令发给了错误的设备。 |
| `command_delivery_stranded_observed_total` | 发现停留在 SENT 且没有结果的指令，其停留时间超过平台仍可能在重试的时长。 |
| `command_response_lost_not_actionable_total` | 此消费者不予处理的死信指令响应，因为其原因表明平台拒绝写入该应答，而不是尝试后失败。没有任何内容被结算：没有需要记录的丢失结果，所指指令可能仍然有效。若某个无人预料的原因出现速率上升，值得关注，因为此门默认刻意关闭。 |
| `command_response_lost_not_answerable_total` | 死信响应所对应的指令所处状态已无法被响应结算：它已经通过其他方式到达终态，或在应答丢失后已重新变为活跃（重新分发或被扣留）。两种情况都不会写入任何内容，因为迟到的死信不得覆盖真实发生的结果，也不得使平台仍打算投递的指令失败。 |
| `command_response_lost_settled_total` | 因设备对其的应答被死信化而被驱动到终态的指令，使平台丢失了响应的指令不再看起来仍在进行中。 |
| `command_response_lost_unsettled_total` | 耗尽所有投递尝试，但其指令的处置未被写入的死信响应。这些指令在 TTL 之前一直显示为进行中，随后过期为 TIMEOUT，错误地归咎于确实应答了的设备。 |

## 设备管理 {#device-management}

前缀：`devicechain_devicemanagement_`。

`geofence_set_publish_failures_total` 供给 [`GeoFenceSetPublishFailing`](./detection-engine.md#what-to-watch) 告警。`credential_misconfigured_total` 上升表示数据库被恢复到了与封存它的根密钥不同的根密钥旁，或设备凭据从未被存储；错误密码不在此计数。三个死信计数器表示状态变更到达了数据库却没有到达总线：用 `dcctl dead-letters` 读取它们。

| 指标 | 含义 |
| --- | --- |
| `suppression_invalid_windows_total` | 计数器。评估时跳过的无效已存储告警抑制窗口；继续发送告警通知。 |
| `suppression_evaluation_failures_total{phase}` | 计数器。告警抑制评估失败次数，按 `phase` 区分：`cycle_start`（周期开始时的记录评估）或 `live`（当前抑制评估）。当前评估失败时允许发送通知。 `status`：单个告警状态评估失败时允许通知，批次中的其他告警继续返回。 |
| `suppression_refresh_total{outcome}` | 计数器。目标设备归属变更后，告警抑制窗口可见性的刷新结果：`refreshed`（已重写存储的范围）、`unchanged`（已是最新）或 `failed`。失败绝不会延迟归属变更本身；在设备归属再次变更或窗口目标被编辑之前，新归属方的受限成员可能看不到该窗口。三种结果均从零开始。 |
| `device_configuration_reports_pruned_total{reason}` | 计数器。由每个副本每十分钟运行一次的维护轮次删除的设备配置判定历史行数，按 `reason` 区分：`orphan`（该报告所属设备已不存在，由删除了设备却没有删除其历史的 Pod 遗留）或 `retention`（超过 200 天；与设备当前修订版本和状态匹配的那一行无论多旧都会保留）。两者均以零预先存在。这是完整的指标名称，没有 `devicechain_devicemanagement_` 前缀。设备群已运行超过 200 天时，`retention` 速率平稳属正常。所有 Pod 都运行同一版本后 `orphan` 速率仍持续，说明仍有东西在删除设备却不删除其历史。失败的轮次不在此计数：它出现在[维护轮次](./observability.md#maintenance-passes)所述的 `configuration_report_maintenance` 系列中。 |
| `agent_proposals_total{kind, outcome}` | Preview（代理操作）。非试运行的提案调用数，按类型（`ALARM_ACK`、`ALARM_CLEAR`、`unknown`）和结果（`created`、`existing`、`refused`）区分；错误计为 `refused`。 |
| `agent_approvals_total{outcome}` | Preview（代理操作）。审批调用数，按最终结果（`recorded`、`executed`、`refused`、`concurrent`、`step_up_required`、`forbidden`、`fence_closed`）区分，内部重试仅计一次。没有更具体结果的错误计为 `refused`。 |
| `agent_executions_total{kind, result}` | Preview（代理操作）。最终审批执行数，按类型（`ALARM_ACK`、`ALARM_CLEAR`、`unknown`）和结果（`executed`、`dependency_changed`、`policy_refused`、`fence_closed`、`refused`、`failed`）区分，每次执行仅计一次，包括重新启用围栏后的重试。`failed` 表示事务未能完成（数据库或语句超时、策略服务或停用事件流不可用）；锁冲突作为并发审批计为 `refused`。 |
| `agent_execution_seconds` | 直方图。Preview（代理操作）。执行最终审批所用的时间，包括拒绝、失败及重新启用围栏后的重试。 |
| `agent_proposals_expired_total` | Preview（代理操作）。由过期清理任务转为 `EXPIRED` 的提案数。 |
| `agent_effects_exhausted_total` | Preview（代理操作）。用尽全部 100 次发布尝试的代理效果发件箱行数。已批准的代理操作已经改变了告警；发件箱中的这一行是通知平台其余部分的消息。失败 100 次的行会连同导致失败的错误一起保留，不再重试，因此这次告警变更永远不会被广播。每行只计一次，服务会记录 `Agent effect exhausted its attempts`，并带上租户和效果 id。没有随附告警监控它：只要增加，就说明代理长时间拒绝了这些发布，请先检查消息代理，再查看待处理的行。 |
| `alarm_event_dead_lettered_total` | 保留此计数器，但自告警事件改由 outbox 转发后不再递增。请参阅 `alarm_events_quarantined`。 |
| `alarm_events_pending` | Gauge。已提交、等待 broker 确认的告警状态转换数，不包括隔离行。 |
| `alarm_event_oldest_age_seconds` | Gauge。按 id 排序的最早待发送转换的等待秒数；队列为空时为零，表示通知的延迟。 |
| `alarm_events_quarantined` | Gauge。无法解码或编码的已存储事件数。事件及错误保留在数据库中，在操作员修复前不会重试。 |
| `callout_in_flight` | 此刻正在授权的设备认证标注请求。 |
| `callout_refused_busy_total` | 因达到并发上限而以通用拒绝回应的设备认证标注请求。 |
| `config_reports_rejected_total{reason}` | device-management 拒绝的 [device-reports 主题](../guides/connecting-a-device.md#reserved-device-topics)消息：`malformed`（无法解码）、`throttled`（在单设备冷却期内的 sync）、`unattributable`（主题中没有租户或设备）、`unknown_device`、`unknown_revision`、`digest_mismatch`、`stale`（比已记录的更旧）和 `device_disabled`（设备已被[停用](../guides/device-credentials.md#disabling-a-device)）。某一批设备持续产生此类拒绝，说明设备端存在缺陷。 |
| `credential_misconfigured_total{path}` | 因存储的密钥永远无法匹配而被拒绝的设备凭据检查：未存储任何密钥，或其摘要由与本实例根密钥所派生的不同密钥生成（最可能是数据库被恢复到了错误的根密钥旁）。path="connect" 是 MQTT 认证标注，path="event" 是逐事件检查。错误密码不在此计数。 |
| `device_reports_dead_lettered_total` | 因无法理解或引用了设备没有的配置修订而写入死信流的设备消息。使用 `dcctl dead-letters` 查看。 |
| `live_subscription_dropped_total{suffix}` | 计数器。因订阅者跟不上而被丢弃的实时值，按订阅所跟随的流区分。此处 `suffix` 为 `alarm-events`，对应 `alarmStream` 订阅。速度慢的查看者会丢失它来不及接收的实时值；已存储的内容不受影响，普通查询仍会返回同样的历史。来自单个查看者的稳定速率通常意味着某个控制台标签页或客户端卡住了，或网络链路较差。许多查看者同时出现，则说明该 Pod 过载。`event-management` 为其测量订阅导出同一指标（`suffix` 为 `resolved-events`），`event-processing` 为检测订阅导出（`derived-events`），各自使用自己的前缀。 |
| `geofence_set_publish_failures_total` | 无法发布的地理围栏集清单：序列化错误、代理拒绝或传输故障。每一条都表示事件处理未被告知某次围栏编辑，因此该租户的包含判定沿用先前的围栏集，直到协调扫描将其修复。持续的非零速率表示围栏编辑没有到达检测引擎。 |
| `fact_publish_failures_total{fact}` | 计数器。已提交的变更，其通知无法发布，按 `fact` 区分：`detection_rules`、`device_roster`、`device_attribute`、`device_desired` 和 `device_ownership`。五个值在启动时即以 0 存在。变更本身已保存；延迟的只是它的通知。检测规则、设备名册或属性的未发布，会在 event-processing 下次与 device-management 比对时修复，通常在几分钟内。`device_desired` 的未发布由设备下一次同步修复，`device_ownership` 的未发布仍留在队列中，下一轮处理（约一秒后）会再次发布。持续出现意味着变更要等几分钟才生效，请检查消息代理。任何持续十分钟的速率都会触发 [`DeviceFactPublishFailing`](./alert-runbook.md#devicefactpublishfailing) 告警。 |
| `provisioning_in_flight` | Gauge。此刻本副本正在处理的设备自注册数，从读完请求体之后到应答之前。仍在发送请求体的请求不计入。该值固定在 `provisioning.maxInFlight` 表示新请求正在被应答 `503`。 |
| `provisioning_request_duration_seconds{result}` | 直方图。按结果统计的自注册请求应答耗时，从处理程序开始计时（包含缓慢的请求体）。 |
| `provisioning_requests_total{result}` | 按结果统计的设备自注册请求：`created`、`adopted`、`replayed`、`auth_failed`（配置密钥或密文被拒绝的所有情形，有意合并计数）、`invalid`、`read_timeout`、`rate_limited_source`、`rate_limited_tenant`、`busy`、`not_registered`、`not_earmarked`、`disabled`、`type_mismatch`、`token_collision`、`already_provisioned`、`limit_exceeded`、`unavailable`、`misconfigured`、`unsupported_type` 和 `error`。每个结果从启动起就存在。没有租户标签，因为请求在通过认证之前没有可信的租户。 |
| `raise_alarm_dead_lettered_total` | 在应用它们的所有尝试都失败后写入死信流的触发告警边，使本应被触发或清除的告警可以被看到，而不是只写进日志。 |
| `resolve_device_disabled_total` | 因设备已[停用](../guides/device-credentials.md#disabling-a-device)而被拒绝的事件。这些事件被丢弃，不进入死信。 |
| `resolve_relationship_refused_total` | 设备发起的跟踪或包含关系在第一次投递时被拒绝的次数。每个事件都携带载荷和原因 `Invalid` 记录到 `failed-events`，不会重试。不带租户或设备标签；Warn 日志会记录租户和关联标识。 |
| `resolve_event_time_bounded_total` | 因报告的事件时间领先服务器时钟超过配置的容差而被拒绝、并被替换为容差允许的最晚时间的情况。 |
| `resolve_measurement_dropped_total{reason}` | 解析时被丢弃、而同一事件的其余部分已被存储的测量条目：`undeclared_non_numeric`（配置文件未声明的键携带文本值或布尔字面量）、`undeclared_unstorable`（未声明的键携带 `NaN`、无穷大或超出范围的数值）和 `declared_not_storable`（已声明的键，其数据类型无法存储，或 `STRING` 值超过 4,096 字节、不是有效的 UTF-8 或包含 NUL 字节）。设备会收到正常的确认，也不会进入死信队列，因此这是设备载荷正在丢失数据的信号。该指标不带租户、设备或指标标签；日志行（每个设备和指标一条）会指明它们，控制台中的设备页面也会列出。在配置文件中声明该指标即可存储。 |
| `resolve_not_gateway_child_total` | 因[网关](./edge-services.md#gateway-identity)为未分配给它的设备或不存在的设备发布事件，或网关已停用而被拒绝的事件数。每个事件连同载荷记录到 `failed-events` 流（原因 `NotGatewayChild`），不会重试，并通过网关的通知主题告知网关。该指标不带租户或网关标签；日志行（每个网关和设备每分钟最多一条）会指明它们。增长通常意味着某台设备从未分配给其网关，或被分配给了另一个网关。 |

### 批量变更 {#bulk-changes}

这些序列属于[批量变更](../guides/bulk-changes.md)，只有提供该功能的构建才会注册。除 `bulk_change_reconciler_lag_seconds` 外，每个序列从启动起即以零存在；该序列以 NaN（未测量）开始，因为零是它的健康读数。这些仪表是整个实例范围的，在每个副本上读数相同，因此告警用 `max()` 读取。告警说明见[告警手册](./alert-runbook.md#bulkchangesnotadvancing)。

| 指标 | 含义 |
| --- | --- |
| `bulk_change_post_attempts_total{kind, result}` | 计数器。应用一条批量变更行的尝试次数，按类型和结果：`posted`、`refused`、`skipped`、`busy`（另一个写入方占用了该设备）、`lost_race`、`fence_refused`、`fence_busy`、`lease_expired`、`not_activated` 或 `retryable`（以会被重试的方式失败）。 |
| `bulk_change_post_duration_seconds{kind}` | 直方图。一次应用行的尝试所用的时间。 |
| `bulk_change_pacer_permits_total{result}` | 计数器。向共享的每秒节流器申请的发送许可，`granted` 或 `denied`。 |
| `bulk_change_lines_settled_total{settlement, basis}` | 计数器。得到结算的行，按结算和决定它的证据（`REPORT_LOG`、`LATEST_STATE`，截止期限或人工裁定时为 `NONE`）。 |
| `bulk_change_transitions_total{to, reason}` | 计数器。已提交的控制转换，按进入的分发状态和原因（无原因时为 `none`）。 |
| `bulk_change_operations{dispatch}` | 仪表。本版本可驱动的批量变更，按分发状态。 |
| `bulk_change_holds{code}` | 仪表。在过去 60 秒内报告了暂挂的运行中批量变更，按暂挂代码。`AUTHORITY_UNAVAILABLE` 大于零会触发 `BulkChangesAuthorityUnavailable`。 |
| `bulk_change_oldest_runnable_line_age_seconds` | 仪表。在仍有待应用行且没有阻塞性暂挂的运行中批量变更中，最长一次未应用任何行的时间，按数据库时钟计；没有等待时为 0。由 `BulkChangesNotAdvancing` 和 `BulkChangesStalled` 读取。 |
| `bulk_change_deadline_lag_seconds` | 仪表。最早的未结算已应用行超过其结算期限的时长；没有时为 0。由 `BulkChangesSettlementLagging` 读取。 |
| `bulk_change_reconciler_lag_seconds` | 仪表。可以结算某个已应用行但尚未结算的最早设备报告的时长，每分钟测量一次。在本 Pod 测量之前、上次测量失败时以及由其他 Pod 测量期间为 NaN。由 `BulkChangesSettlementLagging` 读取。 |
| `bulk_change_driver_last_pass_timestamp_seconds` | 仪表。本 Pod 的批量变更驱动程序最近一次完成轮次的 Unix 时间；首次轮次前为 0。由 `BulkChangesDriverSilent` 读取。 |
| `bulk_change_authority_decisions_total{result}` | 计数器。为批量变更的受权方请求的新鲜授权答复：`qualified`、`revoked`（明确拒绝）或 `unavailable`（无答复，这会暂挂该操作）。 |
| `bulk_change_fence_wait_seconds{action}` | 直方图。批量变更事务等待其操作屏障的时间，按动作。 |
| `bulk_change_fence_pending_total{action}` | 计数器。因进行中的发送未能及时排空而以 `FENCE_PENDING` 应答的控制请求，按动作。由 `BulkChangesFencePending` 读取。 |
| `bulk_change_admission_refusals_total{reason}` | 计数器。在准入时被拒绝的创建、启动和恢复：`quota`（`QUOTA_EXCEEDED`）或 `busy`（`ADMISSION_BUSY`）。 |
| `bulk_change_redaction_repairs_total{table}` | 计数器。修复轮次为未经修复就被删除的设备而脱敏的批量变更行，按表。 |
| `bulk_change_retention_rows_deleted_total{table}` | 计数器。被保留策略删除的批量变更行，按表。 |
| `bulk_change_activated` | 仪表。数据库强制执行批量变更所依据的设备属性写入协议时为 1，未强制执行或无法读取时为 0。由 `BulkChangesNotActivated` 读取。 |

## 事件处理：检测 {#event-processing-detect}

前缀：`devicechain_eventprocessing_`。

告警所读取的指标是 `detect_is_leader`、`detect_live`、`detect_checkpoints_total`（`DetectLeaderIsNotConsuming` 告警三者都读取），以及支撑[地理围栏告警](./detection-engine.md#what-to-watch)的 `detect_fence_*` 计数器。`detect_consumer_pending` 是 `DetectConsumerBacklogHigh` 监视的滞后信号，`detect_watermark_lag_seconds` 是 `DetectWatermarkLagHigh` 背后的指标。`detect_loop_heartbeat_timestamp_seconds` 是引擎自身的存活信号：如果它已陈旧，而 `detect_is_leader` 和 `detect_live` 都读数为 1，则循环挂在了某次调用内部。

| 指标 | 含义 |
| --- | --- |
| `detect_applied_stream_seq` | 已提交快照中捕获的最高 JetStream 流序号。 |
| `detect_checkpoint_failures_total{stage}` | 未能提交的计划 DETECT 检查点，按阶段区分（publish、serialize、save）。超过截止时间的检查点调用计入其卡住的阶段。过期写入者的拒绝不在此计数：那会结束进程。 |
| `detect_checkpoint_min_spacing_seconds` | 仪表。引擎允许两次由计数触发的检查点之间至少间隔的时间：最近一次已提交快照耗时的四倍，且不超过 `checkpointIntervalSeconds`。它跟随 `detect_checkpoint_seconds`，因此会随引擎状态及其快照变大而增大。当它停在 `checkpointIntervalSeconds` 时，按计数触发的检查点已稀疏到设置所允许的程度，此时若 `detect_consumer_ack_pending` 上升、`detect_consumer_pending` 增长，就说明检查点正在拖慢消费者：请查看 `detect_snapshot_bytes` 以及序列化和保存耗时。 |
| `detect_checkpoint_save_seconds` | 每个已提交快照写入数据库所用的时间（分布）：检查点开销中序列化之后的第二部分。 |
| `detect_checkpoint_serialize_seconds` | 每个已提交快照序列化引擎状态所用的时间（分布）：检查点开销的第一部分。 |
| `detect_checkpoints_total` | 已提交的 DETECT 快照检查点。 |
| `detect_consumer_ack_pending` | 已解析事件持久消费者上已投递但未确认的消息（进行中的工作）。 |
| `detect_consumer_pending` | 已解析事件持久消费者上等待的未投递消息（主要的 DETECT 滞后信号）。 |
| `detect_derived_publish_ack_seconds` | 每个已发布的派生事件等待代理确认的时间（分布）。仅统计成功的发布。这不是检测延迟：重放时结果确定的规则，其检测会在触发后约一秒内发布，早于提交它的检查点；其他检测则等待各自的检查点。 |
| `detect_derived_publish_pass_seconds` | 每次发布（一个节拍或一个检查点）发布其检测所用的时间，从第一次发送到最后一次确认，以分布形式记录。不包括检测等待某次发布的时间。 |
| `detect_derived_publish_outstanding` | 当前已发送、正在等待消息总线确认的派生事件数（受 `derivedPublishing.maxInFlight` 限制）。 |
| `detect_derived_publish_outstanding_bytes` | 这些事件的负载字节数（受 `derivedPublishing.maxBufferedBytes` 限制，但单个更大的事件会单独发送）。 |
| `detect_derived_publish_window_full_seconds_total` | 检查点在另一个检测已可发送、但在途上限或字节预算没有空间时，等待确认所花的秒数。 |
| `detect_derived_publish_lane_waits_total` | 在 `derivedPublishing.maxInFlight` 大于 1 时，需要等待同一设备（或关联锚点）的前一个检测得到确认后才能发送的检测数。 |
| `detect_derived_publish_failures_total{outcome}` | 未得到肯定确认的派生事件发布，按 `outcome` 区分：`unknown`（超时：事件可能已被存储）或 `failed`（消息总线拒绝）。每次都会推迟其检查点，并以相同的事件和标识重试，因此已存储的事件不会被存储两次。 |
| `detect_derived_publish_stale_outcomes_total` | 因不属于当前检查点正在等待的任何发布而被拒绝的发布结果。除非出现问题，否则始终为零。 |
| `detect_derived_events_published_total` | 已发布的派生信号事件。 |
| `detect_derived_events_rejected_total{reason}` | 发布前被丢弃的检测，按原因区分（有界枚举）。 |
| `detect_events_applied_total` | 送入 DETECT 引擎的已解析事件。 |
| `detect_fact_persist_retries_total` | 因非终态错误而失败的事实投影写入（规则、名册、属性、删除）的重试。该事实保持未确认，此消费者被阻塞在其后，直到某次重试提交。 |
| `detect_fanout_events_total` | 已解析事件扇出产生的逐规则核心事件。 |
| `detect_fence_archive_skew_total` | 因设备管理不提供清单入口而失败的地理围栏归档读取，它运行的是清单投递之前的构建。该服务前滚后自行修复。 |
| `detect_fence_geometry_cache_evictions_total` | 为保持在缓存顶点上限之内而丢弃的已编译几何缓存条目。 |
| `detect_fence_geometry_cache_hits_total` | 从缓存提供的已编译几何缓存查找，既避免了跨服务读取，也避免了重新编译。 |
| `detect_fence_geometry_cache_misses_total` | 必须获取并编译文档的已编译几何缓存查找。 |
| `detect_fence_geometry_cache_vertices` | 已编译几何缓存中保存的顶点总数，即其上限所计量的数量。 |
| `detect_fence_geometry_hash_mismatch_total` | 哈希与其被请求时所用内容地址不符的地理围栏几何文档。永远是缺陷，从不是暂时的：对端返回了错误的行、传输中有东西重新编码了文档，或归档已损坏。 |
| `detect_fence_geometry_unresolved_total` | 无法从设备管理的归档取得几何数据的地理围栏清单条目。每一条都使该围栏报告无法解析而不是给出答案，如果原因是暂时的，则在下一次协调扫描时修复。 |
| `detect_group_shard_busy_seconds_total{group, shard}` | 每个分片组中每个检测分片用于应用操作的秒数。请按速率读取：它就是该分片的利用率，速率 0.5 表示该分片一半时间处于忙碌。健康的状态是每个分片都远低于 0.9，且数值相近。同一组内某个分片远高于其余分片，说明有热点键（某个设备或关联锚点发送的数据远多于其他）；所有分片都接近 1，说明限制在 `detectShards`，该组正在落后，表现为 `detect_consumer_pending` 增长。 |
| `detect_group_shard_live_keys{group, shard}` | 仪表。每个分片组中每个检测分片持有的活动窗口、定时器和锁存器数量。健康的状态是同一组内各分片大致均匀。某个分片持有的键远多于其他分片就是键倾斜：上升的将是它的队列和忙碌时间。 |
| `detect_group_shard_queue_depth{group, shard}` | 仪表。每个分片组中每个检测分片排队的操作数，按循环的周期采样。健康时保持在接近零。队列持续偏高的分片最慢，该组的循环在每次检查点、规则变更和查询时都要等待它，表现为该组落后（`detect_group_consumer_pending` 和 `detect_group_watermark_lag_seconds`）。 |
| `detect_idle_advances_total` | 产生了至少一次检测的挂钟空闲推进。 |
| `detect_idle_detections_total` | 由挂钟空闲推进产生的检测（缺席/持续时间/会话在静默时触发）。 |
| `detect_is_leader` | 此副本持有 DETECT 分区租约期间为 1，从取得租约起算，而不是从任期构建结束起算。 |
| `detect_live` | 此副本在已持有的领导任期内消费时为 1；在待命，或在构建已取得的任期时为 0。 |
| `detect_live_gap_fills_total{outcome}` | 实时消费遇到流序号比引擎序号多出不止一的消息，并从流中读取缺失范围的次数，按结果区分：filled（读取了该范围且其中有消息）、absent_only（流中没有任何内容：已被清除或淘汰）、failed（读取该范围的尝试没有完成；实时消费暂停，该尝试每个周期重复一次，因此持续的故障会计入每次尝试，而不是一次）。 |
| `detect_live_gap_sequences_total{outcome}` | 实时缺口填补中的流序号，按结果区分：applied（代理已计为投递、却从未到达循环的消息，现已应用）、absent（不在流中：已被清除或淘汰）、skipped（读回后无法处理，被记为已处理而未应用任何内容）。非零的 applied 速率表示投递正在代理与此循环之间丢失。 |
| `detect_loop_heartbeat_timestamp_seconds` | DETECT 单写入者循环最近一次通过的 Unix 时间，任期有效期间每个周期至少刷新一次。在 detect_is_leader 和 detect_live 读数为 1 时陈旧，表示循环挂在了某次调用内部。 |
| `detect_restore_seconds` | 启动时从快照存储恢复引擎状态的时间。 |
| `detect_rule_stat_write_failures_total` | 失败或超时并被丢弃（不重试）的规则健康度触发计数写入（一个租户的一个事务）。检测不受影响（计数器在后台写入），但此值上升期间控制台的规则健康度计数会偏少；丢失的触发计入 `detect_rule_stat_updates_discarded_total`，原因为 `write_failed`，故障原因见服务日志。 |
| `detect_rule_stat_write_seconds` | 后台写入程序完成每次规则健康度触发计数写入（一个租户的一个事务，包括其先进行的租户删除检查）所用的时间（分布）。这不是检测循环的时间。包含失败的写入。 |
| `detect_rule_stat_updates_admitted_total` | 加入规则健康度触发计数器、等待后台写入的已发布检测。 |
| `detect_rule_stat_updates_coalesced_total` | 其中合并到已在等待写入的规则上的部分，因此不额外占用一行。 |
| `detect_rule_stat_updates_written_total` | 已提交到数据库的规则健康度触发更新。 |
| `detect_rule_stat_updates_discarded_total` | 永远不会写入的规则健康度触发更新，按 `reason` 区分：`full`（待写入预算已用尽）、`overflow`（某规则的待写入计数达到上限）、`closed`（在引擎的领导任期结束后到达）、`write_failed`（其写入失败或超时）、`purged`（其租户先被删除）、`shutdown`（任期结束时仍在等待）。检测不受影响。 |
| `detect_rule_stat_admission_contended_total` | 等待后台写入程序完成一个简短记账步骤的规则健康度累加。每次等待都有上限，且从不包含数据库调用。 |
| `detect_rule_stat_pending_keys` | 尚有未写入触发更新的规则数（上限为 `ruleHealth.maxPendingKeys`）。 |
| `detect_rule_stat_pending_bytes` | 未写入触发更新所计的内存（上限为 `ruleHealth.maxPendingBytes`）。 |
| `detect_rule_stat_oldest_pending_seconds` | 最早一条尚未写入的触发更新的时长：控制台触发计数可能滞后的程度。 |
| `detect_rule_stat_batch_rows` | 每次规则健康度写入中的规则数（分布）。 |
| `detect_rule_stat_batch_updates` | 每次规则健康度写入中的触发更新数（分布）：它替代了多少次单行写入。 |
| `detect_rules_active` | 加载到 DETECT 引擎的规则。 |
| `detect_stale_absence_dropped_total` | 因设备离开规则范围（已删除、已禁用、重新分型，或规则版本已被取代）而在发布时丢弃的缺席**触发**。当某次触发在同一轮中被丢弃时，与之配对的清除也计入；重新发布时因新版本保留该规则而被扣下的清除同样计入。已离开范围的设备的清除不会被丢弃，因此它此前触发的告警会被清除。 |
| `detect_superseded_frontier_dropped_total` | 因其档案版本已被取代而在发布时丢弃的、随时间流逝而触发的持续时间、会话和聚合类检测。 |
| `detect_tenants_over_live_key_budget` | 当前超过逐租户活动键预算的租户。 |
| `detect_tenants_over_retained_sample_budget` | 当前超过逐租户保留样本预算的租户。 |
| `detect_tenants_over_rule_budget` | 当前超过逐租户规则数预算的租户。 |
| `detect_watermark_lag_seconds` | 挂钟时间减去最近一次检查点时引擎的水位线。 |

## 事件处理：响应动作 {#event-processing-react}

前缀：`devicechain_eventprocessing_`。

`react_events_poison_dropped_total` 是 `ReactPoisonDropping` 告警所触发的序列。`react_actions_dropped_total{reason="unknown_kind"}` 应始终为零：非零值是需要调查的缺陷，而不是负载。`react_actions_not_enabled_total` 不应出现 `raiseAlarm` 或 `clearAlarm`，因为该接收端总是已接通。

| 指标 | 含义 |
| --- | --- |
| `react_actions_dispatched_total{action}` | 交给其接收端的 REACT 动作，按动作类型区分（包括幂等重放）。 |
| `react_command_cause_omitted_total{reason}` | 规则发送的命令在去掉发送原因记录后重新发送并被 command-delivery 接受的次数，按原因区分。`peer_lacks_cause`：command-delivery 运行的版本不接受该记录，仅在升级滚动期间属于预期情况。`cause_invalid`：command-delivery 拒绝了该记录，属于缺陷。重新发送可能得到一个已存在的命令作为应答，因此该指标统计被接受的重新发送，而不是保存的命令。 |
| `react_actions_dropped_total{reason}` | 未经尝试即被跳过的 REACT 动作，按原因区分。unknown_kind：规则定义带有此构建无法分发的动作类型（伪造或手工编辑的定义；通过受支持的编写路径无法到达），因此非零值是需要调查的缺陷，而不是负载。 |
| `react_actions_not_enabled_total{action}` | 被识别但因此部署没有对应接收端而丢弃的 REACT 动作，按动作类型区分：未配置指令投递时的 sendCommand，或未启用出站连接器时的 httpCall/publish。告警接收端总是已接通，因此 raiseAlarm/clearAlarm 不应出现在这里。 |
| `react_connector_egress_shed_total{action}` | 因超过租户出站速率而在源头被丢弃的连接器分发尝试（`httpCall`、`publish`），按动作类型区分。按尝试计数：重投递可能先被丢弃、后又接纳同一动作，所以这不是被永久丢弃动作的数量。 |
| `react_command_refused_total{reason,result}` | 规则尝试发送、但在到达 command-delivery 之前被拒绝的命令，按原因区分，`result` 始终为 `"unwanted"`。`command_rate_limited`：租户超过其出站速率。`command_loop`：该规则在一分钟内已向该设备发送 120 条命令。`command_guard_full`：循环防护跟踪的规则与设备组合已达上限。每次拒绝还会在死信预算内记录为原因为 `shed` 的死信。 |
| `react_connector_shed_dead_lettered_total{action}` | 在源头被拒绝（超过租户出站速率的连接器动作，以及因 `react_command_refused_total` 中任一原因被拒绝的命令），并以原因 shed 记录为单独死信的动作，按动作类型区分（`httpCall`、`publish`、`sendCommand`）。 |
| `react_derived_in_flight` | REACT 已从流中取出但尚未处理完的派生事件：正在等待其设备的工作者，或正在分发。通常最多为 `react_workers`；当有投递等待超过其确认期限时会更多。 |
| `react_dispatch_lag_seconds` | REACT 分发落后于派生事件流的程度：每个事件从被消息代理存储到开始分发时的时长。这是一次检测的告警、命令和连接器调用在 REACT 上等待的时间。 |
| `react_events_dead_lettered_total` | 达到重投递上限后写入死信流的派生事件，使其动作可以被检查，而不是凭空消失。 |
| `react_events_orphaned_total` | 其规则已不在投影中的派生事件（未分发任何内容）。 |
| `react_events_poison_dropped_total` | 因分发持续失败而在达到重投递上限后被丢弃的派生事件。它们同时也写入死信流，因此这与 `react_events_dead_lettered_total` 统计的是同一批事件；保留它是因为 `ReactPoisonDropping` 告警依据它触发。 |
| `react_workers` | REACT 可同时分发的派生事件数（`react.workers`）。同一设备的事件按流中顺序逐个分发，但留待重新投递的事件会在其后的事件之后再次分发。 |

## 设备状态 {#device-state}

前缀：`devicechain_devicestate_`。

每十分钟由一个副本将每个租户的最近已知状态行与 device-management 比对，并修正不再对应任何设备的行。

| 指标 | 含义 |
| --- | --- |
| `orphan_sweep_deleted_total` | 计数器。清扫移除的设备状态行数，原因是其设备已不存在，或已用同一令牌重新创建。清除或删除设备之后立即出现一阵增长是预期的，删除设备期间保持非零的稳定速率也是正常的。若没有删除任何设备而速率持续上升，则是故障：有东西在删除设备却没有通知 device-state。每一轮的数量见清扫的日志行（`Orphan sweep reconciled device states`）。 |
| `orphan_sweep_stamped_total` | 计数器。清扫为其所属设备打上其版本标记（incarnation）的设备状态行数，这样之后用同一令牌重新创建的设备就会被识别为另一台设备。每行只打一次标记，因此升级后最初几轮出现一阵增长是预期的（在记录该标记之前写入的行在此补上），随后回落为零。速率一直不稳定则值得检查。 |

无法连接 device-management 的一轮会跳过该租户剩余的分页，并在下一轮重试；这不会计入上述任何一个指标。该轮本身显示在[维护任务](./observability.md#maintenance-passes)所述的 `orphan_sweep` 序列中。

## Event management {#event-management}

前缀：`devicechain_eventmanagement_`。

| 指标 | 计数内容与处理方式 |
| --- | --- |
| `rollup_hour_backfill_complete_from_timestamp_seconds` | Gauge。已完全刷新的最早小时边界（Unix 秒）。更早的小时在补齐前继续使用分钟汇总。仅在此副本首次获得回填锁并读取进度后导出。 |
| `rollup_hour_backfill_remaining_windows` | Gauge。直到最早源数据块、分钟汇总或汇总保留边界之间仍需补齐的小时窗口数。零表示已补齐。仅在首次持锁读取进度后导出。 |
| `rollup_hour_backfill_windows_total` | Counter。此副本已刷新并记录完成的小时窗口数。与刷新策略发生锁冲突时，窗口留待下一轮重试。 |
| `analytics_reconcile_newer_surface_skips_total` | 计数器。本 Pod 发现分析（SQL/BI）视图已处于比其自身版本更新的代次并保持原样的协调轮次。仅在升级滚动期间出现；在所有 Pod 运行同一版本后仍持续增长，说明仍有旧版本 Pod 在运行。 |
| `failed_events_recorded_total{source,reason}` | [失败事件索引](../guides/why-isnt-my-data-showing-up.md)读取到的、平台拒绝或无法存储的事件记录数，按写入该记录的区域和原因划分。每条记录都计数，包括被洪泛上限并入已有行的记录，因此它是平台拒绝事件的真实速率。`FailedEventsSustained` 依据它触发。 |
| `failed_events_suppressed_total{reason}` | 因设备或原因达到每小时上限而并入已有行计数、未新增行的记录数。持续偏高说明某个设备或某种原因在大量拒绝事件。 |
| `failed_events_unindexed_total{cause}` | 索引完全没有存储的记录数：`no_tenant`（主题未指明租户）、`undecodable`（记录无法解码）、`erased`（该租户的数据已被擦除）、`gave_up`（存储在每次投递时都拒绝了该记录）。只有 `gave_up` 值得排查；该记录会保留在失败流中，直到被滚动淘汰。 |

## 事件来源 {#event-sources}

前缀：`devicechain_eventsources_`。

在线状态指标属于此服务从代理读取的、由代理断言的 MQTT 在线状态。`presence_tap_off` 是应当告警的指标：当该在线状态没有在某个副本上运行时它为 1，并带有原因标签，而长期运行的 MQTT 设备群不会发出任何通告来显示这一点。金丝雀对指标告诉你在线状态是否被读取：`presence_canary_missed_total` 上升表示没有被读取。`presence_reconcile_regressed_sessions` 持续高于零表示修复没有收敛。`total_msg_*` 计数器是逐来源的。

| 指标 | 含义 |
| --- | --- |
| `command_wake_dropped_total` | 因队列已满而丢弃的指令唤醒；其指令之后由指令投递的协调轮次释放，因此这是延迟信号，而不是丢失。 |
| `command_wake_failed_total` | 无法到达指令投递的指令唤醒；协调轮次会覆盖它们。 |
| `command_wake_released_total` | 因设备重新连接而放回投递队列的扣留指令。 |
| `command_wake_requested_total` | 排队等待指令唤醒的回归设备。 |
| `presence_advisories_skipped_total{reason}` | 未产生在线状态事件的代理连接通告，按原因区分。 |
| `presence_canary_missed_total` | 其自身代理连接未被观察到的金丝雀探测：在线状态没有被读取。 |
| `presence_canary_observed_total` | 其自身代理连接被端到端观察到的金丝雀探测。 |
| `presence_events_failed_total` | 无法写入入站流的在线状态转换。 |
| `presence_events_refused_total` | 被接入准入门拒绝的在线状态转换（已删除租户或租户上限）。 |
| `presence_reconcile_regressed_sessions` | 截至最近一次协调轮次，发现处于 LIVE 状态、但会话 ID 低于投影所持有会话 ID 的设备。持续的非零值表示修复没有收敛。 |
| `presence_reconcile_repaired_total{direction}` | 因错过通告而由协调发出、且随后已在设备状态中生效的在线状态修复，按方向区分。修复在发出后的下一轮才被计数。 |
| `presence_reconcile_unapplied_total{direction}` | 到下一轮协调时仍未在设备状态中生效的在线状态修复，按方向区分。它们被拒绝或丢失，并会再次发出。持续的速率表示修复没有生效；原因之一是设备在仍显示为已连接时被删除，其修复在每一轮都会被拒绝。 |
| `presence_reconcile_runs_total{outcome}` | 在线状态协调轮次，按结果区分。 |
| `presence_reconcile_withheld_disconnects_total` | 若代理清单可证明完整，本应被标记为离线的设备。 |
| `presence_released_total` | 因此来源停止读取代理，而从断言在线状态交还为推断在线状态的设备。 |
| `presence_sessions_regressed_total` | 观察到会话 ID 低于此副本高水位线的在线状态转换（某个代理节点的时钟可能落后于其对等节点）。仅用于诊断：是否应用这类转换由下游的投影决定。 |
| `presence_still_asserted` | 在最近一次释放轮次开始时，此来源仍断言在线的设备。这项工作会自行清空，所以健康的排空会使其降到零并保持在零。 |
| `total_http_connections_closed_before_request{source}` | 在送达请求之前就关闭的 HTTP 接入监听器连接数，包括头部超时关闭，同样也包括端口扫描、TCP 健康检查或挂断的客户端。 |
| `total_msg_decode_successful{source}` | 成功解码的消息总数。 |
| `total_msg_failed_decode{source}` | 解码失败的消息总数。 |
| `total_msg_tenant_deleted{source}` | 因其租户已被删除、数据正在回收而被拒绝的入站消息数。 |

## LwM2M 接入 {#lwm2m-ingest}

前缀：`devicechain_lwm2mingest_`。

主实例和服务状态的 gauge 在[边缘服务](./edge-services.md#lwm2m-ingestion-metrics)中说明，会触发告警的指标也在那里。下面是其余指标。指令计数器讲述一条指令通过适配器的过程：尝试，然后成功或失败，离线、暂存和排空计数器显示等待设备唤醒的指令。`command_park_errors_total` 和 `command_response_publish_failures_total` 这两个指标表示，即使设备没有过错，指令也可能以 `TIMEOUT` 结束。

| 指标 | 含义 |
| --- | --- |
| `bad_requests_total` | 格式错误的 /rd 请求（例如没有位置的注册项请求）。 |
| `coap_requests_total{code}` | 由 L0 健康探针处理的传输层 CoAP 请求，按响应码区分。/rd 注册结果由注册/更新/注销/过期计数器计量。 |
| `command_drain_claim_errors_total` | 因无法与指令投递确立所有权而未分发的积压指令（失败即关闭；稍后按顺序重试）。 |
| `command_drain_claims_lost_total` | 被另一个分发器或投递扫描先领取，因此此次排空没有分发的积压指令：避免了一次重复驱动，不是故障。 |
| `command_drain_errors_total` | 失败的排空获取；设备保持在线期间，短暂延迟后重试。 |
| `command_park_errors_total` | 无法投递且无法交还给指令投递的指令；保持未确认以便重投递时重试，并一直停留在 SENT，直到某次成功。 |
| `command_park_settled_total` | 因指令已被应答、取消、过期或被重新领取而没有改动任何行的交还：已结算的结果，不是故障。 |
| `command_park_skipped_total` | 因没有接入暂存器，或投递信封没有携带分发 nonce 而未被交还的无法投递指令；它们保持 SENT 并等到 TTL。 |
| `command_response_publish_failures_total` | 本地重试后仍无法发布到 command-responses 的指令结果（操作已执行，因此指令不会被重投递；它将 TIMEOUT）。 |
| `commands_attempted_total{op}` | 分发到在线设备的 LwM2M 指令（= 成功 + 失败），按操作区分。 |
| `commands_drained_total` | 由一次排空按从旧到新的顺序分发到在线设备的积压（被扣留或暂存）指令。 |
| `commands_poison_total` | 因无法处理而丢弃的指令（主题中没有可解析的租户，或信封无法解码）。 |
| `commands_served_offline_total` | 发给此适配器所服务设备、但没有在线连接的指令：暂存在指令投递中并在设备下次唤醒时排空，或在设备始终不唤醒时于 TTL 处 EXPIRED。 |
| `commands_succeeded_total{op}` | 设备以 2.xx 确认的 LwM2M 指令，按操作区分。 |
| `commands_tenant_deleted_total` | 因其租户已被删除、数据正在回收而被确认丢弃的指令：平台拒绝驱动已退出客户的硬件。 |
| `deregistrations_total` | LwM2M 显式注销（`DELETE /rd/{id}`）。 |
| `device_info_attribute_write_errors_total` | 采集到的设备信息属性未能写入 device-management 的次数；在该设备下次采集时重新写入。 |
| `device_info_attribute_writes_shed_total` | 因属性写入池繁忙而丢弃的已采集属性批次；在该设备下次采集时写入。持续增长说明 device-management 响应缓慢。 |
| `device_info_harvest_retries_total` | 因设备未响应而重试一次的设备信息采集。 |
| `device_info_harvest_skipped_total` | 未执行的设备信息采集请求：该设备在过去一小时内已采集过、其分片队列已满、设备已不可达，或设备已被停用（或无法确认其是否被停用）。 |
| `device_info_harvests_total{outcome}` | 设备变为可达时执行的设备信息读取，按结果区分：`complete`（全部资源已读取）、`partial`（部分缺失）、`empty`（有响应但未提供任何资源）、`unanswered`（未响应）。 |
| `devices_registered_total` | 首次 LwM2M 注册时自动创建的设备。 |
| `handshakes_total` | 完成的 DTLS 握手（每次都是一个新的已认证 LwM2M 会话）。 |
| `measurements_emitted_total` | 由 LwM2M Observe/Notify 持久写入的单个测量样本。 |
| `notifies_received_total` | 在被观察对象实例上收到的 LwM2M Notify 消息。 |
| `observe_terminal_notifications_total` | 终止了一次观察的通知（RFC 7641，例如被观察实例被删除后的 4.04）。 |
| `presence_dropped_total` | 被丢弃的在线状态转换：不自动注册凭据下的未注册设备，或持久发出预算已用尽。 |
| `registrations_disabled_refused_total` | 因运维人员已停用设备而被拒绝的注册（`4.03`），以及在下一次更新时被终止的注册（`4.04`）。 |
| `sessions_rejected_total` | 因达到在线会话上限（maxSessions）而被拒绝的新会话。 |
| `telemetry_devices_registered_total` | 首个遥测样本时自动创建的设备（罕见：LwM2M 设备在 /rd 注册时创建）。 |
| `telemetry_tenant_deleted_dropped_total` | 因租户已被删除、数据正在回收而丢弃的遥测样本。 |
| `telemetry_unknown_dropped_total` | 因设备未注册（该凭据未启用自动注册）而丢弃的遥测样本。 |

## Sparkplug 接入 {#sparkplug-ingest}

前缀：`devicechain_sparkplugingest_`。

其余 Sparkplug 指标（包括主实例 gauge）在[边缘服务](./edge-services.md#sparkplug-ingestion-metrics)中说明。`samples_skipped_total` 之所以存在，是因为只发布没有数值样本的值的节点，否则看起来与空闲节点完全相同。

| 指标 | 含义 |
| --- | --- |
| `devices_registered_total` | 首次见到其 Sparkplug 身份时自动注册的设备。 |
| `samples_skipped_total{reason}` | 被接受的 Sparkplug 消息中没有产生样本的指标，按原因区分：non_numeric（布尔、字符串、字节、数据集或模板值）、null（is_null）、unnamed（没有名称且没有可解析的别名）、untyped（整数的数据类型既未在消息中声明，也未在其 birth 中声明，且其值按有符号和无符号读取时不同，因此不做猜测；服务会记录一条限频警告，指明主题和指标）或 oversize（消息携带的指标超过 16,384 个，因此其读数均未存储；服务会记录一条指明主题的警告）。只发布此类指标的节点，否则看起来与空闲节点完全相同。 |

## 出站连接器 {#outbound-connectors}

前缀：`devicechain_outboundconnectors_`。该服务运行 REACT 动作所经过的[出站连接器](../concepts/outbound-connectors.md)。

| 指标 | 含义 |
| --- | --- |
| `connector_dispatch_unlettered_total{action, outcome}` | 计数器。以终态结束但没有获得单独死信的分发数，原因是死信额度已用尽（每个租户每秒可写一条，突发 60 条；整个服务每秒十条，突发 100 条）。该分发已经结束并确认，因此无法从死信列表重放。`action` 为 `httpCall`、`publish` 或 `unknown`，`outcome` 是该分发的终态结果（`rate_limited`、`invalid`、`blocked`、`unsupported` 等）。每分钟，每个受影响的租户会得到一条汇总死信，记录按动作和结果统计的数量，可用 `dcctl dead-letters` 读取。数值上升说明有东西在大批量失败：请看 outcome。`rate_limited` 表示该租户的连接器流量超出其出站上限，`blocked` 表示某个连接器指向了租户不得访问的地址，`invalid` 表示连接器已存储的配置无法运行。请修复原因；[`ConnectorDispatchRateLimited`](./alert-runbook.md#connectordispatchratelimited) 告警监控第一种情况。 |

## 通知管理 {#notification-management}

前缀：`devicechain_notificationmanagement_`。

每次投递尝试都失败的通知会写入死信流，`dcctl dead-letters` 会列出它们。此计数器告诉你有通知从未被发出。

`quiet_hours_total` 与 `suppression_total` 按持久化审计字段 `held_by_window` 划分暂存行的结果：由抑制窗口暂存的行计入 `suppression_total`，即使随后还需等待静默时段结束；其他暂存行计入 `quiet_hours_total`。窗口结束后，通知会根据积压量在几个调度轮次内发出。

| 指标 | 含义 |
| --- | --- |
| `notifications_dead_lettered_total` | 每次投递尝试都失败后写入死信流的告警，使运维人员可以看到哪些通知从未被发出。 |
| `quiet_hours_total{outcome}` | 静默时段对一条通知的处理结果，按 outcome 分类：held（被暂存而未发送）、released（在静默时段结束时发送）、dropped_resolved（因告警已被确认或清除而丢弃）、dropped_policy（因策略被停用或删除而丢弃）、dropped_exhausted（在 5 次发送都未送达任何渠道后丢弃并记录为死信）或 superseded（因策略已针对同一告警的更新事件发出通知而丢弃）。 |
| `suppression_total{outcome}` | 计数器。告警抑制结果：`held`（暂存通知）、`rechecked`（当前窗口仍在抑制）、`escalation_skipped`（领取升级级别前跳过被抑制的升级）、`released`（通知已发送）、`superseded`（较新通知已替代暂存通知）、`dropped_resolved`（告警已确认、已清除或已进入新周期）、`dropped_policy`（策略不存在、已禁用或不再匹配路由）、`dropped_exhausted`（释放尝试耗尽或被永久拒绝）、`lookup_failed`（状态查询失败，继续发送通知）。九种结果均预置为零。 |
| `deferral_oldest_due_seconds` | 仪表。调度本轮访问的租户中，最早到期暂存通知距 `release_at` 的秒数；没有到期行时为零。包括静默时段和抑制窗口暂存，衡量到期后的等待时间，而非暂存总时长。 |
| `ledger_writes_total{outcome}` | 投递记录按结果分类：written（已写入）、failed（数据库拒绝或超时，记录丢失）、dropped（等待写入的记录过多，从未尝试写入）或 purged（租户已被删除）。投递记录绝不会延迟通知，因此 failed 或 dropped 表示缺少一条记录，而不是缺少一次通知。 |

## 用户管理 {#user-management}

前缀：`devicechain_usermanagement_`。

这些统计死信消费者，它把服务放弃的失败存入可查询的存储，使其存续时间超过流自身的七天窗口。`dead_letters_unstored_total` 是重要的那个：存储无法接收的失败不会被记录在任何地方，`DeadLetterStoreLosing` 依据它触发。

| 指标 | 含义 |
| --- | --- |
| `dead_letters_stored_total` | 写入可查询存储的死信，使消费者放弃的失败存续时间超过流自身的七天窗口。 |
| `dead_letters_unstorable_total` | 此消费者无法理解的死信消息：没有可解析的租户，或正文不是信封。它们被确认并计数而不重试，因为重投递不会让格式错误的消息变得可解析。 |
| `dead_letters_unstored_total` | 耗尽所有投递尝试仍未被存储的死信：存储不可达的时间超过了重试持续的时间。它们所描述的失败现在不被记录在任何地方，而这正是此消费者存在要防止的唯一一件事。 |

这些指标统计租户删除协调器，它从每个存储系统中擦除已删除租户的数据，然后释放其令牌。它们都不带租户标签。如何解读请参阅[租户删除](./tenant-deletion.md#blocked-store)。

| 指标 | 含义 |
| --- | --- |
| `tenant_purge_in_flight` | 当前未完成的租户删除数量。 |
| `tenant_purge_oldest_age_seconds` | 最早未完成租户删除自切断时起的时长；无未完成删除时为 0。`TenantPurgeStalled` 监控它。 |
| `tenant_purge_store_blocked{store,reason}` | 各存储系统阻塞的未完成删除数量，按存储（`rdb`、`tsdb`、`broker`、`kv`、`detect`、`blob`、`other`）和原因（`deferred`：数据仍然存在或仍在写入；`error`：无法向该存储发出请求）区分。`TenantPurgeStoreFailing` 和 `TenantPurgeStoreHolding` 监控它。 |
| `tenant_purge_store_rows_erased_total{store}` | 各存储为租户删除而擦除的行、对象、消息或键数量：被阻塞存储的进展信号。 |
| `tenant_purge_store_erase_duration_seconds{store}` | 单次存储擦除调用耗时的直方图。 |
| `tenant_purge_completed_total` | 已完成的租户删除：所有存储已清空、已稳定并已保留，屏障已解除，令牌已释放。 |
| `tenant_purge_erased_total` | 已确立的租户数据清除声明：所有存储已清空并已稳定，早于令牌保留期结束。若某个存储再次报告有数据而使声明被撤回，之后又重新确立，则再次计数。 |
| `tenant_purge_failures_total` | 协调器未能在一轮中推进某个租户删除的次数。下一轮会重试，因此统计的是尝试次数。`TenantPurgeCoordinatorFailing` 监控它。 |
| `tenant_purge_duration_seconds` | 从租户删除到其令牌被释放的秒数直方图。包含令牌保留时间。 |
| `tenant_purge_clean_duration_seconds` | 从租户删除到最后一个存储报告已清空的秒数直方图。不含随后的稳定等待和令牌保留等待：即擦除本身所用时间。 |

这些指标统计[双重身份验证](../guides/two-factor-authentication.md)。第一个和最后一个是 `MfaVerificationFailuresSustained` 与 `MfaIdentityLocked` 两条告警所监视的。都不带账号或租户标签。

| 指标 | 含义 |
| --- | --- |
| `mfa_verifications_total{method,result}` | 已检查的代码，按方式（`totp`、`recovery`）和结果统计：`ok`、`invalid`（错误）、`replay`（已使用过的正确代码）、`locked`（因锁定被拒绝）、`expired`（无法使用的质询）、`budget`（检查次数超出额度的请求）、`rate_limited` 和 `unavailable`（无法执行检查）。 |
| `mfa_challenges_total` | 因等待代码而被挂起的密码登录。 |
| `mfa_enrollments_total{result}` | 设置事件：`begun`、`confirmed`、用户自行 `disabled`、运维人员 `reset`。 |
| `mfa_identity_locks_total` | 触发账号全局锁定的尝试。任何增长都意味着有人在使用某个账号的密码，却无法通过它的第二因素。由 `MfaIdentityLocked` 监视。 |

这些指标统计各副本对实例签名密钥的跟随情况。每个 user-management Pod 每三十秒执行一次，以便一个 Pod 签发的令牌能在其他 Pod 上通过验证。

| 指标 | 含义 |
| --- | --- |
| `signing_key_follow_failures_total` | 计数器。某个 Pod 无法读取或安装实例签名密钥的轮次。该 Pod 会保留它最近一次拥有的密钥，因此偶尔一次失败不会改变任何事情。恰逢密钥轮换时的失败通常是该 Pod 与轮换发生竞争，下一轮就会恢复。若持续出现，则说明该 Pod 的密钥正在过期：下一次轮换之后，用新密钥签发的令牌可能无法在它上面通过验证。请在其日志中查找 `Signing-key follow failed`，其中会指明原因（通常是数据库）。 |

## 更新管理 {#update-management}

前缀：`devicechain_updatemanagement_`。该服务运行于 `default` 和 `full` 配置档；参见[空中（OTA）更新](../concepts/over-the-air-updates.md)。

`update_download_grants_total{result="not_configured"}` 上升表示没有设置下载基址，任何设备都无法下载：`updates.downloadBaseUrl` 被设为空字符串，或实例没有 Ingress 可供推导。`update_attempts_expired_total` 统计更新期间失去响应的设备；处于 `UNKNOWN` 的更新会等待设备的清单报告或运维人员执行 `abandonUnknownUpdate`。`UpdateDownloadsNotConfigured`、`UpdateReportsMostlyRefused`、`UpdateRolloutStepsFailing` 和 `UpdateAttemptsGoingUnknown` 监视这些指标；参见[空中升级告警](./observability.md#update-alerts)。

| 指标 | 含义 |
| --- | --- |
| `update_attempts_expired_total{state}` | 因设备失去响应而被截止期扫描移动的更新尝试：在报告开始安装之前为 `TIMED_OUT`，安装或重启期间为 `UNKNOWN`。 |
| `update_desired_publish_failures_total` | 无法写入设备 desired 主题的更新分配、下载授权和取消消息。设备会在下次 sync 时获取该状态。 |
| `update_download_bytes_total` | 向设备流式传输的制品字节数。 |
| `update_download_grants_total{result}` | 设备申请的下载授权，按结果分类：`granted`，或未授予的原因（`unknown_assignment`、`superseded`、`finished`、`cancel_requested`、`artifact_unavailable`、`not_configured`、`error`）。 |
| `update_downloads_total{result}` | 制品下载请求，按结果分类：`served`、`aborted`（客户端离开或传输中途无法读取对象），或在发送任何字节前被拒绝的原因（`capability_malformed`、`capability_invalid`、`capability_expired`、`stale`、`gone`、`range_not_satisfiable`、`busy`、`device_disabled`、`error`）。`capability_invalid` 速率上升说明有人在猜测。 |
| `update_reports_rejected_total{reason}` | update-management 在应用前拒绝的设备消息：`malformed`、`unattributable`、`throttled` 和 `device_disabled`（设备已被停用）。 |
| `update_rollout_dispatches_total{result}` | 设备组发布对设备作出的决定：`dispatched`（已分配更新，或按其已有的同一制品分配计入），或 `skipped`（被分配规则拒绝：不存在、已停用、不兼容、正忙于另一项更新）。 |
| `update_rollout_step_failures_total{stage}` | 留待下一轮处理的发布驱动失败：`step`（某个发布的步骤失败：数据库错误、制品上无法读取的兼容配置文档，或因 device-management 不可达、未为 update-management 配置而导致所有设备查询失败）或 `list`（查询运行中发布失败，因此没有任何发布被推进）。持续上升说明发布没有推进。 |
| `update_rollout_transitions_total{to}` | 服务自行改变状态的发布：`completed`、`paused_failure_threshold` 或 `paused_artifact_not_assignable`（制品被停用或失去要求的签名）。 |
| `update_reports_total{verdict,kind}` | 更新进度和清单报告，按 `kind`（`progress`、`inventory`）和更新状态机给出的结论分类：`APPLIED`，或未应用的原因（`DUPLICATE`、`STALE_ATTEMPT`、`DIGEST_MISMATCH`、`WRONG_COMPONENT`、`REGRESSION`、`UNCONFIRMED`、`NOT_CANCELLABLE`、`LATE`、`CONFLICT`、`INVALID_ATTEMPT`、`WRONG_KIND`、`UNKNOWN_ASSIGNMENT`）。某一批设备的未应用速率上升说明设备端存在缺陷；请按 `kind="progress"` 判断，因为没有任何分配的设备发送的清单报告，按设计会得到 `UNKNOWN_ASSIGNMENT` 或 `UNCONFIRMED`。 |

## MCP {#mcp}

前缀：`devicechain_mcp_`。

| 指标 | 含义 |
| --- | --- |
| `skill_reads_total{skill,kind}` | 成功读取内嵌技能的次数。`skill` 为 `alarm-storm-triage`、`device-anomaly-investigation` 或 `propose-change-for-approval`；`kind` 为 `prompt`（`prompts/get`）或 `resource`（`resources/read`，包括辅助文件）。列表查询以及未知名称或 URI 不增加计数，也不创建时间序列。不包含租户、设备或用户标签。 |

## 所有服务 {#every-service}

前缀：`devicechain_<area>_`，其中 `<area>` 是去掉连字符的服务名称（`devicechain_devicemanagement_ready`）。每个服务都导出这些指标。

`safety_gate_enabled` 是 [`SafetyGateDisabled`](./observability.md#safety-gates) 告警读取的指标。JetStream 的 gauge 报告每个流相对其配置上限的大小及其副本健康状况；[复制](./observability.md#replication)下的复制告警建立在后者之上。

| 指标 | 含义 |
| --- | --- |
| `auth_gate_attempts_total` | 后台认证门的 JWKS 获取尝试。 |
| `auth_gate_failures_total` | 后台认证门的 JWKS 获取失败。 |
| `ready` | 数据平面就绪（认证已生效）时为 1，否则为 0。 |
| `safety_gate_enabled{gate}` | 在此服务中接入了可选安全门时为 1，因其配置缺失而关闭时为 0。关闭的安全门会放行本应被它拒绝的工作（tenant_lifecycle：已删除租户的工作；presence：发给缺席设备的指令；rule_validation：档案发布时无法编译的检测规则）。 |
| `jetstream_peers_current{stream}` | 此流或 KV 存储桶当前已追平且在线的 RAFT 对等节点，包括主节点。低于 jetstream_replicas_actual 表示该流被标为已复制，实际并未复制。 |
| `jetstream_replicas_actual{stream}` | JetStream 为此流或 KV 存储桶报告的副本数。 |
| `jetstream_stream_limit_bytes{stream}` | JetStream 流配置的 MaxBytes 上限。 |
| `jetstream_stream_limit_messages{stream}` | JetStream 流配置的 MaxMsgs 上限。 |
| `jetstream_stream_used_bytes{stream}` | JetStream 流中当前存储的磁盘字节数。 |
| `jetstream_stream_used_messages{stream}` | JetStream 流中当前的消息数。 |

### 跨服务客户端 {#cross-service-client}

每个通过共享客户端调用其他服务的服务都会多导出一个系列。它是这里唯一带有自己前缀 `devicechain_svcclient_` 的系列，
其 `peer` 标签是被调用服务的基础 URL，因此由部署决定其数量，绝不随租户或设备增长。

| 指标 | 含义 |
| --- | --- |
| `devicechain_svcclient_responses_truncated_total{peer}` | 计数器。来自 `peer`、超出客户端 1 MiB 读取上限的成功（HTTP 200）响应，调用因此以“响应过大”错误失败，调用方必须对查询分页。错误状态码从不在此计数。预期为零。非零速率表示某个调用方向某服务请求的内容超出一次响应所能容纳的量，通常是设备群已增长到超过一次未分页读取所能返回的规模；失败服务的日志会指出该调用。没有任何设置可以调高该上限，解决办法是缩小请求范围或分页。 |
