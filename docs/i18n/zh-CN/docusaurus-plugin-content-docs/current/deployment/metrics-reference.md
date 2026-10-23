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

三条[指令投递告警](./observability.md#command-delivery-alerts)读取 `command_delivery_claims_stranded_total`、`command_delivery_presence_read_errors_total` 和 `batch_refusals_total{bound="reserve"}`。指令投递仪表盘绘制其余指标。其中有几项预期会很繁忙，并不是故障：被拒绝的触发中稳定的 `not_sole` 占比、因扫描与触发竞争而丢失的领取，以及设备离线期间产生的扣留。`command_delivery_responses_refused_total` 预期为零。`command_delivery_stranded_observed_total` 或 `command_response_lost_unsettled_total` 的速率上升，表示指令正在对没有做错任何事的设备超时。（旧名称中重复的 `command_delivery_` 是历史遗留并保持不变，因为重命名序列会破坏所有读取它的仪表盘。）

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
| `command_delivery_holds_placed_total` | 因设备确定缺席而被扣留、不予分发的指令。 |
| `command_delivery_holds_released_total` | 因设备回来而被放回分发队列的扣留指令。 |
| `command_delivery_nudges_applied_total` | 恰好找到一条排队指令并使其通过投递门的分发触发。不是发布次数：在线状态门仍可能扣留该指令或使其失败，与扫描周期上的情况完全相同。 |
| `command_delivery_nudges_declined_total{reason}` | 排空流程拒绝处理的分发触发，按原因区分。较高且稳定的 reason="not_sole" 速率属预期（设备有多于一条排队指令时，触发会让步），不是故障。 |
| `command_delivery_nudges_dropped_total` | 因队列已满而丢弃的分发触发。这是延迟信号，不是错误率：指令仍会在投递扫描时发出，扫描是所有触发之下的兜底。 |
| `command_delivery_nudges_requested_total` | 被接受进入入队时分发队列的分发触发，每条通过 createCommand 创建的指令一个（机群批次不发出任何触发）。 |
| `command_delivery_presence_read_errors_total` | 无法读取在线状态投影的扫描轮次；该门失败即放行，因此这里持续有速率，表示指令正在无门控地分发。 |
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
| `alarm_event_dead_lettered_total` | 无法发布到告警事件流而改写入死信流的告警状态变更事件。每一条都是到达了数据库却没有到达总线的告警转换，因此没有人因此被通知，它对运维人员可见，而不是只写进日志。 |
| `callout_in_flight` | 此刻正在授权的设备认证标注请求。 |
| `callout_refused_busy_total` | 因达到并发上限而以通用拒绝回应的设备认证标注请求。 |
| `config_reports_rejected_total{reason}` | device-management 拒绝的 [device-reports 主题](../guides/connecting-a-device.md#reserved-device-topics)消息：`malformed`（无法解码）、`throttled`（在单设备冷却期内的 sync）、`unattributable`（主题中没有租户或设备）、`unknown_device`、`unknown_revision`、`digest_mismatch`、`stale`（比已记录的更旧）和 `device_disabled`（设备已被[停用](../guides/device-credentials.md#disabling-a-device)）。某一批设备持续产生此类拒绝，说明设备端存在缺陷。 |
| `credential_misconfigured_total{path}` | 因存储的密钥永远无法匹配而被拒绝的设备凭据检查：未存储任何密钥，或其摘要由与本实例根密钥所派生的不同密钥生成（最可能是数据库被恢复到了错误的根密钥旁）。path="connect" 是 MQTT 认证标注，path="event" 是逐事件检查。错误密码不在此计数。 |
| `device_reports_dead_lettered_total` | 因无法理解或引用了设备没有的配置修订而写入死信流的设备消息。使用 `dcctl dead-letters` 查看。 |
| `geofence_set_publish_failures_total` | 无法发布的地理围栏集清单：序列化错误、代理拒绝或传输故障。每一条都表示事件处理未被告知某次围栏编辑，因此该租户的包含判定沿用先前的围栏集，直到协调扫描将其修复。持续的非零速率表示围栏编辑没有到达检测引擎。 |
| `raise_alarm_dead_lettered_total` | 在应用它们的所有尝试都失败后写入死信流的触发告警边，使本应被触发或清除的告警可以被看到，而不是只写进日志。 |
| `resolve_device_disabled_total` | 因设备已[停用](../guides/device-credentials.md#disabling-a-device)而被拒绝的事件。这些事件被丢弃，不进入死信。 |
| `resolve_event_time_bounded_total` | 因报告的事件时间领先服务器时钟超过配置的容差而被拒绝、并被替换为容差允许的最晚时间的情况。 |
| `resolve_not_gateway_child_total` | 因[网关](./edge-services.md#gateway-identity)为未分配给它的设备或不存在的设备发布事件，或网关已停用而被拒绝的事件数。每个事件连同载荷记录到 `failed-events` 流（原因 `NotGatewayChild`），不会重试，并通过网关的通知主题告知网关。该指标不带租户或网关标签；日志行（每个网关和设备每分钟最多一条）会指明它们。增长通常意味着某台设备从未分配给其网关，或被分配给了另一个网关。 |

## 事件处理：检测 {#event-processing-detect}

前缀：`devicechain_eventprocessing_`。

告警所读取的指标是 `detect_is_leader`、`detect_live`、`detect_checkpoints_total`（`DetectLeaderIsNotConsuming` 告警三者都读取），以及支撑[地理围栏告警](./detection-engine.md#what-to-watch)的 `detect_fence_*` 计数器。`detect_consumer_pending` 是 `DetectConsumerBacklogHigh` 监视的滞后信号，`detect_watermark_lag_seconds` 是 `DetectWatermarkLagHigh` 背后的指标。`detect_loop_heartbeat_timestamp_seconds` 是引擎自身的存活信号：如果它已陈旧，而 `detect_is_leader` 和 `detect_live` 都读数为 1，则循环挂在了某次调用内部。

| 指标 | 含义 |
| --- | --- |
| `detect_applied_stream_seq` | 已提交快照中捕获的最高 JetStream 流序号。 |
| `detect_checkpoint_failures_total{stage}` | 未能提交的计划 DETECT 检查点，按阶段区分（publish、serialize、save）。超过截止时间的检查点调用计入其卡住的阶段。过期写入者的拒绝不在此计数：那会结束进程。 |
| `detect_checkpoint_save_seconds` | 每个已提交快照写入数据库所用的时间（分布）：检查点开销中序列化之后的第二部分。 |
| `detect_checkpoint_serialize_seconds` | 每个已提交快照序列化引擎状态所用的时间（分布）：检查点开销的第一部分。 |
| `detect_checkpoints_total` | 已提交的 DETECT 快照检查点。 |
| `detect_consumer_ack_pending` | 已解析事件持久消费者上已投递但未确认的消息（进行中的工作）。 |
| `detect_consumer_pending` | 已解析事件持久消费者上等待的未投递消息（主要的 DETECT 滞后信号）。 |
| `detect_derived_publish_ack_seconds` | 每个已发布的派生事件等待代理确认的时间（分布）。仅统计成功的发布。这不是检测延迟：检测在发布之前要先等待其检查点。 |
| `detect_derived_publish_pass_seconds` | 每个检查点发布其检测所用的时间，从第一次发送到最后一次确认，以分布形式记录。不包括检测等待其检查点的时间。 |
| `detect_derived_publish_outstanding` | 当前已发送、正在等待消息总线确认的派生事件数（受 `derivedPublishing.maxInFlight` 限制）。检查点之间为零。 |
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
| `react_actions_dropped_total{reason}` | 未经尝试即被跳过的 REACT 动作，按原因区分。unknown_kind：规则定义带有此构建无法分发的动作类型（伪造或手工编辑的定义；通过受支持的编写路径无法到达），因此非零值是需要调查的缺陷，而不是负载。 |
| `react_actions_not_enabled_total{action}` | 被识别但因此部署没有对应接收端而丢弃的 REACT 动作，按动作类型区分：未配置指令投递时的 sendCommand，或未启用出站连接器时的 httpCall/publish。告警接收端总是已接通，因此 raiseAlarm/clearAlarm 不应出现在这里。 |
| `react_connector_egress_shed_total{action}` | 因超过租户出站速率而在源头被丢弃的连接器分发尝试（`httpCall`、`publish`），按动作类型区分。按尝试计数：重投递可能先被丢弃、后又接纳同一动作，所以这不是被永久丢弃动作的数量。 |
| `react_connector_shed_dead_lettered_total{action}` | 在源头被丢弃，并以原因 shed 记录为单独死信的连接器动作（httpCall/publish），按动作类型区分。 |
| `react_events_dead_lettered_total` | 达到重投递上限后写入死信流的派生事件，使其动作可以被检查，而不是凭空消失。 |
| `react_events_orphaned_total` | 其规则已不在投影中的派生事件（未分发任何内容）。 |
| `react_events_poison_dropped_total` | 因分发持续失败而在达到重投递上限后被丢弃的派生事件。它们同时也写入死信流，因此这与 `react_events_dead_lettered_total` 统计的是同一批事件；保留它是因为 `ReactPoisonDropping` 告警依据它触发。 |

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
| `samples_skipped_total{reason}` | 被接受的 Sparkplug 消息中没有产生样本的指标，按原因区分：non_numeric（布尔、字符串、字节、数据集或模板值）、null（is_null）、unnamed（没有名称且没有可解析的别名）或 oversize（消息携带的指标超过 16,384 个，因此其读数均未存储；服务会记录一条指明主题的警告）。只发布此类指标的节点，否则看起来与空闲节点完全相同。 |

## 通知管理 {#notification-management}

前缀：`devicechain_notificationmanagement_`。

每次投递尝试都失败的通知会写入死信流，`dcctl dead-letters` 会列出它们。此计数器告诉你有通知从未被发出。

| 指标 | 含义 |
| --- | --- |
| `notifications_dead_lettered_total` | 每次投递尝试都失败后写入死信流的告警，使运维人员可以看到哪些通知从未被发出。 |
| `quiet_hours_total{outcome}` | 静默时段对一条通知的处理结果，按 outcome 分类：held（被暂存而未发送）、released（在静默时段结束时发送）、dropped_resolved（因告警已被确认或清除而丢弃）、dropped_policy（因策略被停用或删除而丢弃）、dropped_exhausted（在 5 次发送都未送达任何渠道后丢弃并记录为死信）或 superseded（因策略已针对同一告警的更新事件发出通知而丢弃）。 |

## 用户管理 {#user-management}

前缀：`devicechain_usermanagement_`。

这些统计死信消费者，它把服务放弃的失败存入可查询的存储，使其存续时间超过流自身的七天窗口。`dead_letters_unstored_total` 是重要的那个：存储无法接收的失败不会被记录在任何地方，`DeadLetterStoreLosing` 依据它触发。

| 指标 | 含义 |
| --- | --- |
| `dead_letters_stored_total` | 写入可查询存储的死信，使消费者放弃的失败存续时间超过流自身的七天窗口。 |
| `dead_letters_unstorable_total` | 此消费者无法理解的死信消息：没有可解析的租户，或正文不是信封。它们被确认并计数而不重试，因为重投递不会让格式错误的消息变得可解析。 |
| `dead_letters_unstored_total` | 耗尽所有投递尝试仍未被存储的死信：存储不可达的时间超过了重试持续的时间。它们所描述的失败现在不被记录在任何地方，而这正是此消费者存在要防止的唯一一件事。 |

## 更新管理 {#update-management}

前缀：`devicechain_updatemanagement_`。该服务为可选启用；参见[空中（OTA）更新](../concepts/over-the-air-updates.md)。

`update_download_grants_total{result="not_configured"}` 上升表示未设置 `updates.downloadBaseUrl`，任何设备都无法下载。`update_attempts_expired_total` 统计更新期间失去响应的设备；处于 `UNKNOWN` 的更新会等待设备的清单报告或运维人员执行 `abandonUnknownUpdate`。

| 指标 | 含义 |
| --- | --- |
| `update_attempts_expired_total{state}` | 因设备失去响应而被截止期扫描移动的更新尝试：在报告开始安装之前为 `TIMED_OUT`，安装或重启期间为 `UNKNOWN`。 |
| `update_desired_publish_failures_total` | 无法写入设备 desired 主题的更新分配、下载授权和取消消息。设备会在下次 sync 时获取该状态。 |
| `update_download_bytes_total` | 向设备流式传输的制品字节数。 |
| `update_download_grants_total{result}` | 设备申请的下载授权，按结果分类：`granted`，或未授予的原因（`unknown_assignment`、`superseded`、`finished`、`cancel_requested`、`artifact_unavailable`、`not_configured`、`error`）。 |
| `update_downloads_total{result}` | 制品下载请求，按结果分类：`served`、`aborted`（客户端离开或传输中途无法读取对象），或在发送任何字节前被拒绝的原因（`capability_malformed`、`capability_invalid`、`capability_expired`、`stale`、`gone`、`range_not_satisfiable`、`busy`、`device_disabled`、`error`）。`capability_invalid` 速率上升说明有人在猜测。 |
| `update_reports_rejected_total{reason}` | update-management 在应用前拒绝的设备消息：`malformed`、`unattributable`、`throttled` 和 `device_disabled`（设备已被停用）。 |
| `update_reports_total{verdict}` | 更新进度和清单报告，按更新状态机给出的结论分类：`APPLIED`，或未应用的原因（`DUPLICATE`、`STALE_ATTEMPT`、`DIGEST_MISMATCH`、`WRONG_COMPONENT`、`REGRESSION`、`UNCONFIRMED`、`NOT_CANCELLABLE`、`LATE`、`CONFLICT`、`INVALID_ATTEMPT`、`WRONG_KIND`、`UNKNOWN_ASSIGNMENT`）。某一批设备的未应用速率上升说明设备端存在缺陷。 |

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
