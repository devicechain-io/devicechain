{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# 故障排查与日常运维 {#troubleshooting}

先在下表查找症状。如果已有触发的告警，请查阅[告警手册](./alert-runbook.md)，
了解含义和处理方法。[Grafana 总览及阶段看板](./grafana-boards.md)可以帮助定位
工作停止推进的第一个阶段。

## 按症状查找 {#symptoms}

| 症状 | 首先检查 | 操作指南 |
| --- | --- | --- |
| 安装或引导在服务启动前停止 | 运行 `dcctl preflight`；查看失败步骤和 Kubernetes 事件。集群可能正由其他操作员占用。 | [引导](./bootstrap.md)、[集群锁](./cluster-lock.md)和[继续失败的引导](./disaster-recovery.md#resuming-a-bootstrap)。 |
| Pod 处于 Pending、反复重启或未就绪 | 查看受影响 Pod 的描述，检查调度、镜像、卷和探针故障。如果发生重启，在替换之前检查上一个容器的日志。 | [容器重启](./observability.md#container-restarts)和[升级说明中的启动拒绝条件](/1.6/deployment/releases-and-upgrades#v160-before-upgrade)。 |
| 恢复后 user-management 拒绝启动，其他服务始终未就绪 | 查看 user-management 的启动错误。错误的根密钥无法解开已存储的签名密钥；重启无法修复。 | [根密钥恢复](./disaster-recovery.md#root-key)和[验证托管文件](./disaster-recovery.md#verify)。 |
| 登录成功，但 API 调用返回 401 | 检查客户端令牌的受众和接收服务的拒绝日志；有效令牌也可能绑定到了其他资源。 | [令牌受众检查](./observability.md#token-audience-checks)。 |
| 用户无法通过第二因素验证 | 检查时钟同步和账户审计记录。如果在密码验证成功后的猜测尝试中被锁定，应先重置密码，再重置第二因素。 | [第二因素锁定](./observability.md#mfa-alerts)。 |
| MQTT 超时或 TLS 握手失败 | 检查 DNS、证书中的主机名、TCP 可达性和 Gateway 监听器状态。 | [外部 MQTT 故障排查](./connecting-from-outside.md#troubleshooting)。 |
| 设备已连接，但缺少遥测 | 确认租户和设备，再查看失败事件原因及设备 Connectivity 选项卡中丢弃的测量值。 | [为什么看不到我的数据？](../guides/why-isnt-my-data-showing-up.md)和[常见遥测原因](../getting-help.md#things-that-silently-swallow-data)。 |
| 遥测延迟，积压持续增长 | 在阶段看板找到第一个缓慢的消费者；区分待投递和处理中消息，并检查下游数据库及消息代理。 | [消费者积压](./observability.md#consumer-backlog)、[摄入背压](./observability.md#ingest-backpressure)和[调优](./tuning-an-instance.md)。 |
| 命令一直排队或被暂缓 | 重试前检查命令状态、设备在线状态、维护窗口和命令通道。 | [命令投递告警](./observability.md#command-delivery-alerts)和[发送命令](../guides/sending-commands.md)。 |
| 规则没有触发报警 | 检查已发布的配置文件版本、传入指标以及检测引擎的规则错误和回放状态。 | [检测引擎](./detection-engine.md)和[测试规则](../guides/testing-your-rules.md)。 |
| 已有报警，但没有收到通知 | 检查策略匹配和通道配置，再查看通知阶段看板及其触发的告警。 | [通知通道](../guides/notification-channels.md)和[Grafana 看板](./grafana-boards.md)。 |
| 存储已满，或扩容没有落实到 PVC | 检查两个数据库和消息代理卷的剩余空间及增长情况；比较请求的卷大小与 PVC。 | [事件存储容量与扩容](./tuning-an-instance.md#event-store-sizing)和[选择规模](./sizing.md)。 |
| 备份停止上传，或恢复被拒绝 | 检查备份告警、各存储的恢复窗口和根密钥托管文件；保留完整拒绝信息。 | [备份归档告警](./observability.md#backup-archiving)和[灾难恢复](./disaster-recovery.md)。 |
| 已删除的租户仍在擦除中 | 找出阻碍进度的存储系统；不能因租户从控制台消失就判断擦除完成。 | [擦除停滞](./tenant-deletion.md#stalled)和[擦除完成信号](./tenant-deletion.md#erased-signal)。 |

## 更改之前先收集证据 {#evidence}

记录时间范围和时区、实例、受影响租户、已安装版本、最后成功步骤和第一个失败步骤。
在每个 Grafana 看板选择同一时间范围。保存告警名称和标签，不要只截取颜色。

下面的只读示例使用命名空间 `dci-acme` 中的实例 `acme`。请替换为自己的命名空间和
Pod 名称。在 Bash 中运行示例；Windows 用户请使用 Git Bash 或 WSL。

```bash
dcctl version
dcctl preflight
kubectl -n dci-acme get pods
kubectl -n dci-acme get events --sort-by=.metadata.creationTimestamp
kubectl -n dci-acme describe pod YOUR_POD
kubectl -n dci-acme logs YOUR_POD --all-containers=true --since=30m
```

如果容器发生过重启，还应请求上一个容器的日志；将容器名替换为 Pod 描述中的名称。
如果不存在上一个容器，该命令会报错。

```bash
kubectl -n dci-acme logs YOUR_POD -c YOUR_CONTAINER --previous
```

日志可能已经轮转；没有日志输出并不证明没有发生故障。结合 Kubernetes 事件阅读
[结构化日志](./observability.md#logs)。在重启 Pod、更改配置或重试写入前保留证据。

:::caution 分享前先脱敏
从日志和命令输出中移除令牌、密码、连接字符串、根密钥、托管材料和私有主机名。
不要将 Kubernetes Secret 或原始配置转储附在公开 Issue 中。
请参阅[获取帮助](../getting-help.md)，了解反馈渠道，包括私下报告安全问题的方式。
:::

## 日常运维 {#routine-operations}

以下是建议的检查时机，不是自动调度计划或新增的平台限制。
请根据流量、恢复目标和维护窗口选择合适的频率。

| 时机 | 检查和操作 | 操作指南 |
| --- | --- | --- |
| 每个运维工作日及事故之后 | 检查触发的告警、Pod 重启、流水线积压和存储余量。确认告警能送达操作员；空面板可能表示缺少指标。 | [Grafana 看板](./grafana-boards.md)和[告警路由](./observability.md#alert-routing)。 |
| 每个备份周期 | 验证核心存储和事件存储都在归档，且恢复窗口满足目标。单独检查对象内容及保留的流。 | [两份数据库备份](./disaster-recovery.md#two-tiers)、[备份告警](./observability.md#backup-archiving)和[其他状态](./disaster-recovery.md#other-state)。 |
| 引导或密钥变更之后，以及恢复演练之前 | 验证托管指纹；将加密文件和口令分开保存，并存放在集群之外。 | [验证托管文件](./disaster-recovery.md#verify)。 |
| 按恢复演练计划 | 使用两个存储及原始根密钥，在新集群上演练恢复；验证登录、密钥使用和事件历史。只验证流和对象备份不算恢复演练。 | [恢复实例](./disaster-recovery.md#recover)。 |
| 升级或配置变更之前 | 阅读该版本的启动拒绝条件及支持的升级路径，审查已保存的覆盖配置，检查备份，应用前先预演。 | [版本与升级](./releases-and-upgrades.md)、[版本策略](./versioning-policy.md#upgrade-paths)和[覆盖配置预演](./tuning-an-instance.md#dry-run)。 |
| 升级或配置变更之后 | 检查发布就绪状态、看板以及代表性设备的摄入和命令结果；确认备份仍在上传。 | [可观测性](./observability.md)和[发送命令](../guides/sending-commands.md)。 |
| 容量和证书审查期间 | 将存储增长及保留周期与可用容量对照；检查即将到期的信任锚，提前安排证书轮换。 | [规模规划](./sizing.md)、[调优](./tuning-an-instance.md)和[设备证书](./device-certificates.md)。 |
| 权限审查和租户下线期间 | 审查服务账户和角色；删除租户时，验证擦除完成并执行操作员负责的清理。 | [服务账户](../user-guide/service-accounts.md)和[租户删除](./tenant-deletion.md#operator-steps)。 |

流和对象命令处于 **Preview** 阶段：`dcctl backup inspect --verify` 检查列出的字节，
`dcctl restore --plan` 只生成计划。没有执行恢复的命令，也没有协调的数据库检查点。
请遵循[导出流和对象内容](./disaster-recovery.md#export-streams-objects)中的暂停写入和验证要求。
