---
title: 架构概览
---

# 架构概览

本页是简短版本。它展示一个事件从设备出发,到达仪表板、触发告警,并把命令发回设备的路径。[架构](./architecture.md)是完整版本:它描述每个组件、数据与消息主干、部署模型和密钥存储。

![设备连接到接入服务,接入服务把事件交给 device-management 解析。解析后的事件进入 event-management 存储、进入 device-state 形成实时状态、进入 event-processing 进行检测与响应。响应会产生告警、通知相关人员、把命令发回设备,并调用出站连接器。控制台、仪表板、GraphQL API 和 MCP 读取这些结果。](/img/architecture-overview.svg)

## 整体形态 {#shape}

- **设备通过接入服务到达平台。** 平台的 broker 是内置于 NATS 的 MQTT 服务器。`event-sources` 处理 MQTT 和 HTTP,`sparkplug-ingest` 和 `lwm2m-ingest` 是对应协议的可选服务,LoRaWAN 网络服务器则向[入站集成](../guides/inbound-integrations.md)推送数据。
- **每个事件只在靠前的位置解析一次。** `device-management` 识别设备并把它的上下文附加到事件上,因此下游读取到的是自描述的事件,不需要再做关联查询。
- **三个消费者各自独立地读取解析后的事件。** `event-management` 负责存储,`device-state` 维护每台设备的最近已知状态,`event-processing` 评估规则。慢的消费者不会拖慢其他消费者,因为它们之间隔着 NATS JetStream。
- **响应通过拥有相应对象的服务完成。** 告警是 `device-management` 中的对象,命令是 `command-delivery` 中的记录,出站调用由 `outbound-connectors` 在它自己的进程中投递。
- **人和工具通过 GraphQL 读取。** 控制台、仪表板、你自己的代码以及可选的 [MCP 服务器](./mcp.md),都使用各服务相同的 GraphQL API,并带着调用者自己的令牌。
- **一个实例服务多个租户。** 总线上的主题和数据库中的行都带有租户信息;参见[多租户](./multi-tenancy.md)。

## 一条消息如何流转 {#how-a-message-flows}

跟随一条读数(燃油液位)从设备到仪表板,再看规则如何处理它。

1. **设备发布。** 它用自己的凭据连接平台 broker,把一个 JSON 事件发布到自己的主题 `{instance}/{tenant}/devices/{token}/events`。broker 只允许它发布到这个主题。参见[连接设备](../guides/connecting-a-device.md)。
2. **接入服务解码。** `event-sources` 接收消息,检查消息体中的设备与主题中的设备是否一致,应用该租户的接入速率限制,并把解码后的事件发布到处理管道。无法解码的消息会进入 `failed-decode` 流。
3. **事件被解析。** `device-management` 认证设备,查找它的[设备类型与配置档](./domain-model.md)、分配关系,以及被跟踪的关系(客户、区域、资产)。它把这些信息作为索引维度写入事件,并发布解析后的事件。
4. **事件被存储。** `event-management` 将其批量写入 TimescaleDB,并按租户分区。生命周期策略(压缩、保留、汇总)在那里执行。
5. **实时状态更新。** `device-state` 消费同一个解析后的事件,更新该设备此项测量的当前值,并带上其配置档中的单位和类型。
6. **仪表板更新。** 仪表板组件针对保存数据的服务打开了 GraphQL 订阅。`event-management` 通过它的 WebSocket 桥推送新读数,组件随之重绘。参见[仪表板](./dashboards.md)。
7. **规则评估它。** `event-processing` 消费同一个解析后的事件,并交给检测规则处理。当燃油液位低于阈值时,规则在上升沿触发。参见[事件处理与告警](./event-processing.md)。
8. **触发后执行动作。** 规则的动作开始运行。其中一个在 `device-management` 中产生告警,这是一个有生命周期的对象,在下降沿自动清除。另一个动作把命令加入队列。还有的可以把载荷交给 `outbound-connectors`,由它投递到 webhook、broker 或云队列。
9. **相关人员得知。** `notification-management` 按租户策略把告警通过电子邮件或 webhook 路由出去,并且在告警一直未被确认时可以再次通知。
10. **命令完成一次往返。** `command-delivery` 持久化命令,检查设备是否在线,并把命令发布到设备自己的命令主题。设备执行后把结果发布到它自己的命令响应主题,`command-delivery` 记录结果。命令最终以成功、设备拒绝,或表示从未送达的某个状态结束;参见[命令](./commands.md#command-lifecycle)。如果设备当时离线,命令会被暂存,直到设备回来。

这些步骤运行在由 NATS JetStream 连接的独立服务上,因此每个服务都可以单独扩缩容和重启。[可观测性](../deployment/observability.md)介绍了能显示事件卡在何处的指标和告警。

## 下一步 {#where-next}

- [架构](./architecture.md):每个组件以及平台如何部署。
- [领域模型](./domain-model.md):设备、配置档、关系和事件。
- [传输能力矩阵](../reference/transport-matrix.md):每种连接方式在各个方向上能做什么。
- [实测吞吐量](../deployment/bootstrap.md#measured-throughput):测量了什么、使用什么工作负载、在什么集群上。
