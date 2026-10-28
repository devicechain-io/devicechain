---
title: 为什么选择 DeviceChain
---

# 为什么选择 DeviceChain

DeviceChain 是一个采用 Apache 2.0 许可的物联网平台,运行在 Kubernetes 上。设备可通过 MQTT、HTTP、[Sparkplug B](./concepts/sparkplug.md) 或 [LwM2M](./concepts/lwm2m.md) 接入,也可以经由 ChirpStack 之类的 LoRaWAN 网络服务器接入。规则会产生告警并把命令发回设备。仪表板、[空中升级](./concepts/over-the-air-updates.md)和[多租户的客户访问](./user-guide/members-roles-and-customer-users.md#customer-users)均已内置。没有设备数量上限,也没有企业版。

本页帮助你判断它是否适合你。它拿 DeviceChain 与几类方案做比较,而不是与具体产品比较,说明每一类方案在哪些情况下更合适,并在最后列出 DeviceChain 目前还没有做到的事。

## 适合谁 {#who-it-is-for}

**你可能是正在评估开源物联网平台的平台工程师。** 你想自己运行它,放在你掌控的集群里,让数据留在那里。你希望无论有 100 台还是 100,000 台设备,许可条款都不变;你也想要关于它实际表现的证据,而不只是一份功能清单。请从[架构概览](./concepts/architecture-at-a-glance.md)和[实测吞吐量](./deployment/bootstrap.md#measured-throughput)开始。

**你可能已经在运行 LoRaWAN 网络服务器或 Sparkplug B 产线。** 你的网络服务器负责解码无线帧,或者你的边缘节点向 MQTT broker 发布数据,而你需要的是它们之上的一层:设备注册表、规则、告警、命令、仪表板和客户访问。DeviceChain 可以[接收来自网络服务器的数据](./guides/inbound-integrations.md),设备无需直接连接它;也可以作为[主应用](./concepts/sparkplug.md)(Host Application)加入现有的 Sparkplug 环境。

**你可能在打造联网产品,需要让每位客户看到自己的设备。** 你是 OEM 或集成商,希望用一个平台服务许多客户:客户的登录只能看到属于自己的内容,控制台可以[贴牌](./concepts/white-labeling.md),并且可以向你交付的设备推送升级。

## 按方案类型比较,你能得到什么 {#comparison}

各列是方案的类型,每个单元格都是概括性的说法:某一类中的具体产品可能做得更好,也可能更差。请把它当作向你实际考虑的方案提出正确问题的一种方式。

| | 用零件自行搭建(broker、时序数据库、规则引擎、仪表板) | 托管的物联网云 | 其他自托管平台,其开源版本受设备数量上限或企业版限制 | DeviceChain |
| --- | --- | --- | --- | --- |
| **许可** | 每个零件各有许可,集成由你负责。 | 一项服务,通常按设备或消息计费。 | 开源内核,部分功能或规模保留给付费版。 | Apache 2.0,没有设备数量上限,也没有付费版。高可用、多租户和命令投递都在开放的平台中。 |
| **运行位置** | 你放在哪里就在哪里。 | 供应商的云。 | 你的基础设施,或供应商的。 | 在你掌控的 Kubernetes 集群中,数据就留在你放置集群的地方。参见[引导安装](./deployment/bootstrap.md)。 |
| **运维** | 每个零件都由你运维和升级。 | 由供应商负责。 | 由你负责。 | 由你负责。命令行工具 `dcctl` 负责安装和升级平台;参见[发布与升级](./deployment/releases-and-upgrades.md)。 |
| **多租户与客户访问** | 由你设计,通常做得晚、做得难。 | 通常是账户级隔离;面向客户的视图要你自己构建。 | 通常有,有时只在付费版中。 | 一个实例服务多个[租户](./concepts/multi-tenancy.md),租户可以为自己的客户提供[受限范围的登录](./user-guide/members-roles-and-customer-users.md#customer-users)。 |
| **闭环命令** | 队列、重试和结果跟踪都要自己构建。 | 通常有命令或任务功能,结果跟踪的程度不一。 | 不一。 | [命令](./concepts/commands.md#command-lifecycle)会被持久化并跟踪到某个[结果](./concepts/commands.md#outcome)。设备离线时,它的命令会被暂存,规则也可以发送命令。 |
| **规则与告警** | 自行挑选并接入规则引擎。 | 通常是规则服务,往往对每条消息无状态。 | 不一。 | [流式检测规则](./concepts/event-processing.md)在事件重放时给出相同结果,[告警](./concepts/event-processing.md#the-alarm-lifecycle)有生命周期,并有通知到人的途径。 |
| **空中升级** | 制品库、签名和跟踪都要自己构建。 | 通常作为托管服务提供。 | 不一,有时是企业版功能。 | [已签名的制品、可跟踪的升级尝试和按分组的批量发布](./concepts/over-the-air-updates.md),适用于使用 MQTT 的设备。限制见[下文](#not-yet)。 |
| **运维方面的证据** | 取决于你自己的测量。 | 有服务级别条款,但你无法检查平台本身。 | 不一。 | 一张说明了工作负载和注意事项的[实测吞吐量](./deployment/bootstrap.md#measured-throughput)表、可以验证的[已签名发布](./deployment/verifying-releases.md),以及带有恢复步骤的[灾难恢复](./deployment/disaster-recovery.md)。仓库的 [`hack/`](https://github.com/devicechain-io/devicechain/tree/main/hack) 目录中有高可用和恢复的验证装置,需要手动针对真实集群运行。 |

## 什么时候别的方案更合适 {#when-something-else-fits}

每一类方案在某些情况下都是正确答案。

- **自行搭建**:当你只需要一两个组件,而不是一整个平台时。如果一个 broker 加一个时序数据库已能满足需求,或者你的需求特殊到平台的模型会与之冲突,零件能给你平台给不了的控制权。代价是设备注册表、多租户、命令跟踪和升级都会变成你自己的产品。
- **托管的物联网云**:当你不想运行 Kubernetes,当你的设备和其他工作负载已经在同一个云里并希望在那里集成,或者你现在就需要跨多个区域的托管规模时。DeviceChain 给你控制权和一份不按设备计费的许可,同时也给你运维工作。[实例调优](./deployment/tuning-an-instance.md)说明了这些工作包括什么。
- **其他自托管平台**:当你需要 DeviceChain 目前没有的东西(例如工业协议驱动,见下文),当较长的发展历史对你很重要,或者你想要供应商的商业支持时。DeviceChain 是一个年轻的项目,围绕它的社区和集成生态都比较小。

## 我们目前还没有做到的 {#not-yet}

列出这些,是为了让你在采用 DeviceChain 之前而不是之后了解它们。

- **工业现场总线驱动。** 没有 BACnet、Modbus 或 OPC-UA 驱动,项目发布的内容也都不支持这些协议。受支持的做法是使用本地网关与现场总线通信,再通过 MQTT 或 HTTP 转发;协议转换由你自己提供。参见[不可用](./reference/transport-matrix.md#not-available)。
- **SNMP。** DeviceChain 不从 SNMP 代理采集数据,也不接收 SNMP trap。请使用能通过 MQTT 或 HTTP 转发的采集器。
- **其他传输方式。** 不支持 WebSocket 设备接入;CoAP 只能通过 LwM2M 到达平台。只有平台自带的 MQTT broker 支持用客户端证书认证设备;HTTP 使用令牌或基本凭据,LwM2M 使用预共享密钥。参见[传输能力矩阵](./reference/transport-matrix.md#not-available)。
- **向网络服务器背后的设备下发命令。** 通过[入站集成](./reference/transport-matrix.md#inbound-integrations)没有下行链路。针对此类设备的命令会被接受,随后以 `COMMAND_TRANSPORT_UNSUPPORTED` 失败。
- **移动应用。** DeviceChain 提供网页控制台和能适应手机屏幕的仪表板,也提供 [JavaScript](./reference/npm-packages.md) 客户端库,以及仓库 [`sdks/`](https://github.com/devicechain-io/devicechain/tree/main/sdks) 目录中的 .NET 和 Unity 库。它没有原生移动应用。
- **空中升级的限制。** 批量发布没有分批、金丝雀推进、定时或维护窗口。只有通过 MQTT 连接的设备才能接收托管升级:只使用 HTTP 的设备不能,LwM2M 设备通过普通命令升级,Sparkplug 没有定义固件升级。参见[哪些设备可以升级](./concepts/over-the-air-updates.md#transports)。
- **Kubernetes 以外的平台。** 平台运行在 Kubernetes 上。本地实例运行在 [kind](./quickstart/first-device.md) 集群中,正式实例运行在你自己的集群中。
- **外部密钥管理器。** 密钥由实例根密钥加密保存。存储在设计上允许由密钥管理器接管,但目前没有随附任何一个。参见[密钥处理](./concepts/architecture.md#secret-handling)。
- **v1.0.0 之前的兼容性承诺。** 语义化版本以及对设备通信协议、GraphQL API 和 npm 包的承诺从 v1.0.0 开始。此前的版本可能更改 API 和模式。参见[版本策略](./deployment/versioning-policy.md)。

## 下一步 {#next}

- [快速入门:你的第一台设备](./quickstart/first-device.md)在你的机器上运行一个实例。
- [架构概览](./concepts/architecture-at-a-glance.md)展示消息如何在平台中流转,[架构](./concepts/architecture.md)列出每个组件。
- [获取帮助](./getting-help.md)说明在哪里提问,或报告本页遗漏的内容。
