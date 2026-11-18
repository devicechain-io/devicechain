---
title: 连接器
sidebar_position: 12
---

import GuideShot from '@site/src/components/GuideShot';

# 连接器 {#connectors}

**连接器**是检测规则可以向其发布消息的出站目的地：MQTT 代理、Apache Kafka 主题、AWS SNS 主题或 AWS SQS 队列。规则触发时，它的 `publish` 动作会通过你指定的连接器发送消息。本章介绍如何创建连接器、配置它并管理其版本，面向需要把 DeviceChain 与自己现有系统对接的租户管理员。

一次性的 Webhook 不需要连接器：规则的 Webhook 动作自带 URL。出站动作如何工作以及如何管控，请参阅[出站连接器](/concepts/outbound-connectors)。

并非每个安装都包含连接器服务。如果你的实例没有运行它，**Connectors**（连接器）页面会提示这一点，而不是显示列表。

## 连接器列表 {#the-connector-list}

打开侧边栏中的 **Connect → Connectors**。列表显示每个连接器的名称、类型和描述。点击某一行即可打开。每行的 **Delete**（删除）在你确认后删除连接器。任何通过已删除连接器发布的规则都会投递失败，直到你把它指向另一个连接器，且删除无法撤销。

<GuideShot id="connectors-list" alt="Connectors 列表，只有一个连接器 Facilities event broker，类型为 MQTT，描述为向设施团队的代理发布告警事件。该行有 Delete 操作，右上角有“New connector”按钮。" />

## 创建连接器 {#creating-a-connector}

点击 **New connector**（新建连接器）。抽屉首先是所有连接器都有的字段：

- **Token**（令牌）：规则用来指代该连接器的标识符。旁边的按钮会按实例的令牌模式生成一个。
- **Name**（名称）和 **Description**（描述）：自由文本。
- **Type**（类型）：连接器所用的发布传输方式：MQTT、Apache Kafka、AWS SNS 或 AWS SQS。更改类型会清空下方的字段，因为每种类型需要不同的设置。

表单其余部分取决于类型。标有 `*` 的字段为必填。

<GuideShot id="connectors-new" alt="New connector 抽屉，包含 Token、Name、Description 以及设置为 MQTT 的 Type，随后是 MQTT 字段：Broker URLs、Topic、设置为 Default (1) 的 QoS 和 Client ID。" />

| 类型 | 设置 |
| --- | --- |
| MQTT | **Broker URLs**（代理 URL，每行一个，每个都带明确的端口和协议方案 `tcp://`、`mqtt://`、`ssl://`、`tls://`、`mqtts://`、`ws://` 或 `wss://`）、**Topic**（主题）、**QoS**（0、1 或 2；默认为 1）、可选的 **Client ID**（客户端 ID）和 **Username**（用户名），以及可选的 **Password**（密码）。 |
| Apache Kafka | **Broker addresses**（代理地址，`host:port`，每行一个）、**Topic**、可选的 **Client ID**、**Use TLS**（使用 TLS），以及可选的 SASL 认证（包含机制、用户名和密码）。 |
| AWS SNS | **Region**（区域）、**Topic ARN**（主题 ARN）、**Access key ID**（访问密钥 ID）、必填的 **Secret access key**（秘密访问密钥），以及可选的 **Endpoint override**（端点覆盖）。 |
| AWS SQS | **Region**、**Queue URL**（队列 URL）、**Access key ID**、必填的 **Secret access key**，以及可选的 **Endpoint override**。 |

密码或密钥是连接器的**凭据**。它是只写的：一旦保存，控制台只会告诉你已配置凭据，不会再次显示它。要更改它，点击 **Replace**（替换）并输入新值；要移除它，点击 **Clear**（清除），在保存之前可以用 **Undo**（撤消）撤回 Clear。当类型要求必须有凭据时不提供 **Clear**：AWS 始终如此，Kafka 在启用 SASL 时如此。

控制台会在你输入时检查必填字段，但服务器会在你保存时以及连接器发送时再次检查整个配置。连接器发送时，平台还会拒绝解析到私有或内部地址的目的地，除非你的运维人员允许。参见[连接器可以向哪里发送](/concepts/outbound-connectors#destinations)。

点击 **Create connector**（创建连接器）保存。新连接器是**草稿**：在你发布版本之前，没有规则能通过它发布，详见下文。

## 编辑连接器 {#editing-a-connector}

打开连接器即可看到它的详情页。**Configuration**（配置）选项卡的表单与创建抽屉相同。类型可见但不可更改；要换用其他传输方式，请创建新的连接器。

<GuideShot id="connector-config" alt="Facilities event broker 连接器的 Configuration 选项卡：Name、Description、只读的 Type（MQTT）、包含一个 8883 端口 mqtts 地址的 Broker URLs、Topic 为 acme/building-ops/alarms，以及 QoS 设为 2（恰好一次）。" />

编辑字段后点击 **Save draft**（保存草稿）。存在未保存的修改时，控制台会在按钮旁显示 **Unsaved changes**（有未保存的更改）。保存只会更改草稿。在你再次发布之前，规则继续通过最近一次发布的版本发布消息。

## 版本 {#versions}

**Versions**（版本）选项卡按从新到旧的顺序列出每个已发布版本，包括其标签、发布时间和发布者。最新的一个标记为 **live**（生效中），表格上方的一行说明规则通过哪个版本分发。

<GuideShot id="connector-versions" alt="Facilities event broker 连接器的 Versions 选项卡。一行文字写道：Rules dispatch through version 2 (the latest published)。列出两个版本：2，标记为 live，标签为 Route to building-ops topic；以及 1，标签为 Initial；每个都带有发布时间、发布者和 Restore 操作。右上角有 Publish 按钮。" />

- **Publish**（发布）将已保存的草稿冻结为一个新的不可变版本，规则随后通过它发布。你可以添加标签和描述。请先保存草稿：存在未保存的修改时 **Publish** 处于禁用状态。
- **Restore**（恢复）把较早版本的配置复制回草稿，替换任何未保存的编辑。它不会改变规则所使用的版本：之后需再发布，才会使其成为 live 版本。凭据不做版本管理，因此恢复某个版本时当前凭据保持不变。

## 相关内容 {#related}

- [出站连接器](/concepts/outbound-connectors)：publish 与 Webhook 动作、投递、目的地和管控。
- [事件处理](/concepts/event-processing)：通过连接器发布的规则。
