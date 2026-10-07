---
slug: /
sidebar_position: 1
title: 简介
---

# DeviceChain {#devicechain}

DeviceChain 是一个使用 Go 和 React 构建的云原生 **IoT 应用使能平台**。它连接和管理大规模、多种类型的设备群，并处理这些设备的数据。平台涵盖设备生命周期、遥测数据接入、命令与控制、组织建模和多租户管理，并通过 GraphQL API 以及可嵌入、支持版本管理的仪表板提供这些功能。

DeviceChain 从头重建了 SiteWhere 平台。它保留了 SiteWhere 经过实践验证的领域模型，将庞大的 Java/Spring 技术栈替换为高效、运维简单的微服务，可运行于任何 Kubernetes 集群。

## 为什么选择 DeviceChain {#why-devicechain}

- **Go 原生微服务。** 服务启动不到一秒，内存占用低，以单个二进制文件交付。
- **Operator 与 CRD。** DeviceChain 使用 Kubernetes Operator 和声明式 `Instance` 资源，而不是 Shell 脚本。租户是控制平面数据库中的记录，通过管理控制台管理。
- **以 GraphQL 为核心的 API。** API 支持自省并能描述自身，无需生成客户端桩代码。
- **精简且完全开源的技术栈。** NATS JetStream 承担全部消息传递、MQTT 和 KV 基础设施。原生 JWT 负责认证，PostgreSQL 是唯一的数据存储技术（一个关系数据库存储实体数据，另一个启用了 TimescaleDB 扩展的数据库存储时序事件），OpenTofu 负责基础设施配置。本地运行只需两个依赖：**NATS + TimescaleDB**。
- **统一的关系模型。** 设备上下文使用带类型的关系图，而非固定的分配关系，因此可以组合新的实体类型，无需反复修改数据库结构。
- **可嵌入、支持版本管理的仪表板。** 仪表板采用以画布为核心的布局（分层、背景图片、按断点响应式适配），内置 Apache ECharts 组件和实时订阅。它支持草稿、发布和回滚，以及运行时绑定模型：一份定义加上宿主清单，即可在任何设备上运行。查看器以 npm 包交付，任何应用都可以嵌入。
- **自行托管，无计量收费。** DeviceChain 采用 Apache-2.0 许可证，没有开放核心与付费版本之分，也不按设备收费。设备清单、数字孪生状态、命令投递、仪表板、多租户、高可用性和 OAuth 2.1 授权服务器都属于开放平台，无需购买付费套餐。你在自己的环境中运行平台，完全拥有数据。

## 平台如何组织 {#how-the-platform-is-organized}

DeviceChain 由一组基于共享核心库协作的微服务组成：

| 服务 | 职责 |
| --- | --- |
| **event-sources** | 可插拔的入站传输（目前支持 MQTT 和 HTTP；计划支持 WebSocket），将原始设备消息解码并送入处理流水线。 |
| **sparkplug-ingest** _（可选启用）_ | [Eclipse Sparkplug B](./concepts/sparkplug.md) 主机应用。它接入现有的 Sparkplug MQTT 环境，将数据送入同一流水线，并根据上线/离线握手确定设备在线状态。 |
| **lwm2m-ingest** _（可选启用）_ | 通过 DTLS 终止 CoAP/UDP 连接的 [OMA LwM2M](./concepts/lwm2m.md) 服务器。它使用每台设备的预共享密钥身份进行认证，并根据注册生命周期确定在线状态。 |
| **device-management** | 管理设备、设备类型、支持版本管理的设备配置文件、关系图、告警对象及其生命周期，以及事件解析。 |
| **event-processing** | 处理已解析事件的检测与动作流水线。它运行流式规则（阈值、持续时间、重复、变化率、缺失、窗口聚合、区域关联），在事件重放时产生相同结果，并执行自动响应（触发告警、发送命令和调用出站连接器）。检测在此运行；它触发的告警对象仍由 device-management 管理。 |
| **event-management** | 将已解析事件持久化到 TimescaleDB，并提供时序查询，包括通过 graphql-ws 桥接提供的实时订阅。 |
| **device-state** | 每台设备的实时最新已知状态投影（每项测量的当前读数）。 |
| **command-delivery** | 向设备持久化投递双向命令。 |
| **dashboard-management** | 支持版本管理的仪表板定义（草稿、发布/回滚、导出），由可嵌入的组件包渲染。 |
| **notification-management** | 通过电子邮件（SMTP）和 webhook 将已触发的告警通知相关人员，支持按严重程度路由，并升级持续未确认、未解除的告警。 |
| **outbound-connectors** | 通过支持版本管理、使用密钥认证的连接器，将流水线的出站动作（webhook `httpCall`，以及向 MQTT/Kafka/AWS SNS/SQS 执行 `publish`）投递到外部系统。 |
| **ai-inference** _（可选启用）_ | 根据自然语言描述起草检测规则，并提交给人工编写规则所用的同一编译器。AI 只提出建议；编译器决定哪些内容有效。AI 不参与实时检测，因此规则在事件重放时产生相同结果。参见 [AI 辅助编写](./concepts/ai-authoring.md)。 |
| **user-management** | 管理全局身份、各租户的成员关系、角色目录、JWT 签发与验证，以及租户层级（平台运营方定义并分配给租户的服务组合）。 |
| **mcp** _（可选启用）_ | 只读的 [Model Context Protocol](./concepts/mcp.md) 服务器，使 AI 助手（Claude、Cursor、VS Code）能够使用用户自己的令牌，代表用户查询租户。 |
| **operator (k8s)** | 管理 `Instance` 自定义资源。目前其协调循环只观察资源；平台工作负载由 Helm Chart 渲染。 |

参见[架构](./concepts/architecture.md)，了解各部分如何协作；参见[领域模型](./concepts/domain-model.md)，了解核心概念；参见[事件处理与告警](./concepts/event-processing.md)，了解遥测数据如何转化为可采取行动的信号。

## 模拟数据 {#trying-it-with-simulated-data}

无需实体硬件，你也可以通过 **设备模拟**工具 `dcctl sim` 探索 DeviceChain。它创建一个场景的完整拓扑（客户、区域、资产和设备），然后通过**真实设备使用的同一传输通道**，向平台发送实时遥测数据并触发告警。你可以在设备群持续运行的过程中探索控制台、仪表板和查询。

模拟器与其他外部客户端一样，以限定在单个租户内的身份进行认证。它没有访问平台的特殊权限。

## 项目状态 {#project-status}

DeviceChain 尚处于正式发布前阶段，正在积极开发。这些文档页面会标明各项能力是**已提供**、**已计划**还是**设计中**。[GitHub 仓库](https://github.com/devicechain-io/devicechain) 是判断当前哪些功能能够构建和运行的权威依据。

## 许可证 {#license}

Apache License 2.0。
