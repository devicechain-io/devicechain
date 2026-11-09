---
title: 从其他物联网平台迁移到 DeviceChain
---

# 从其他物联网平台迁移到 DeviceChain

本指南面向在其他物联网平台上运行设备群、想了解迁移到 DeviceChain 需要做什么的团队。它把你已经在用的概念对应到 DeviceChain 中的等价概念，列出目前已经存在的迁移途径，坦率说明哪些内容没有直接对应，并给出建议的工作顺序。

目前没有一键转换工具。DeviceChain 不会读取其他平台的导出文件，因此迁移意味着在 DeviceChain 中重建你的模型，并迁移你的设备。分量最重的部分，即设备及其凭据，可以批量载入；属于逻辑的部分，例如规则和仪表板，需要手工重建。本指南不会点名其他产品，因为对应关系取决于概念，而不是产品。

## 概念如何对应 {#concept-map}

不同平台会用不同的词指代相似的事物，也会用同一个词指代不同的事物。请把右列理解为“DeviceChain 中最接近的概念”，并在对应不完全的地方阅读备注。

| 你当前平台中的概念 | DeviceChain 中的对应 | 备注 |
| --- | --- | --- |
| **设备** | [设备](../concepts/domain-model.md#core-entities)，由你选择的 **token** 标识 | token 即身份，不会改变。你原来的设备名称或序列号可以放在 `externalId`（在租户内唯一），原来的标识符可以放在[别名](../user-guide/devices.md#aliases)中。 |
| **设备配置文件或设备类型** | 一个[设备类型](../user-guide/device-types-and-profiles.md)，引用一个带版本的[设备配置文件](../user-guide/device-types-and-profiles.md) | DeviceChain 把这个概念拆成两部分。**类型**负责对设备分类和展示。**配置文件**是带版本的契约：它包含指标定义、命令、检测规则和配置键。多个类型可以共用一个配置文件。 |
| **遥测键** | 在配置文件上声明的**指标**，带数据类型和单位 | 读数以 `measurements` 的形式到达，以指标名称为键。指标类型为 `DOUBLE`、`INT` 或 `BOOLEAN`，并以时间序列存储（布尔值存为 0 或 1）。参见[载荷结构](./connecting-a-device.md#payload-shapes)和[测量](./connecting-a-device.md#measurement)。 |
| **属性**（客户端、服务端、共享） | [属性](../concepts/domain-model.md#attributes-vs-events)，作用域为 `CLIENT`、`SERVER` 和 `SHARED` | 作用域含义相同：由设备上报、仅平台可见、由平台为设备设置。配置文件声明为配置的共享属性会下发给设备，参见[配置下发](./connecting-a-device.md#reserved-device-topics)。属性也可以作为[分面](../user-guide/groups-facets-browse.md)，用于浏览和分组。 |
| **资产** | [资产](../user-guide/assets-customers-areas.md)，由资产类型分类 | 资产类型可以发布带版本的[属性契约](../user-guide/assets-customers-areas.md#asset-types)。资产可以嵌套，每个资产只有一个父级。 |
| 实体之间的**关系** | [有类型、有方向的关系](../concepts/domain-model.md#relationships) | 关系类型可以标记为**tracked**（被跟踪）。来自设备的被跟踪关系会作为锚点记录在每个事件上，因此可以按客户、区域或资产查询读数，即使之后重新分配设备，历史也保持不变。分组成员关系使用同一张图。参见[管理设备分配](./managing-assignments.md)。 |
| **客户** | [客户](../user-guide/assets-customers-areas.md)，以及成员资格仅限于该客户数据的**客户用户** | 客户是租户中的一条记录。让某人只能访问它是另一件事，参见[客户用户](../user-guide/members-roles-and-customer-users.md#customer-users)。客户不能嵌套。 |
| **租户**、租户配置 | [租户](../concepts/multi-tenancy.md)和[租户层级](../concepts/tenant-tiers.md) | 一套安装为多个租户服务，彼此隔离。层级是由运营方定义的套餐，用于设定租户的上限。 |
| **用户与角色** | 成员、[角色](../user-guide/members-roles-and-customer-users.md#roles)和[单点登录](../concepts/single-sign-on.md) | 单点登录使用 OpenID Connect。脚本请使用[服务账号](../user-guide/service-accounts.md)。 |
| **设备组** | [实体组](../user-guide/groups-facets-browse.md)，静态或动态 | 动态组是基于属性保存的选择器，在读取时解析。一个组只包含一类成员：设备、资产、区域或客户。 |
| **规则链、流程或工作流** | 配置文件上的[检测规则](../concepts/event-processing.md)，用[表单或画布](../user-guide/detection-rules.md)编写 | 规则监视事件，条件成立时执行动作。条件是什么、不能做什么，见[没有直接对应的内容](#no-equivalent)。 |
| **告警** | [告警](../user-guide/alarms.md)，带生命周期、严重级别和升级 | 由检测规则触发和清除。设备也可以上报告警事件。 |
| **通知或告警投递** | [通知策略](../user-guide/notification-policies.md) | 按租户设置的策略，支持邮件和 webhook 投递以及升级。 |
| **调用其他系统** | [出站连接器](../concepts/outbound-connectors.md)和 webhook 动作 | 规则动作可以调用 webhook，或发布到 MQTT、Kafka、AWS SNS 或 AWS SQS。 |
| **派生或计算的遥测** | [计算指标](../user-guide/calculated-metrics.md) | 作为独立指标存储，并且可以对历史数据回填。 |
| **对设备的 RPC** | [命令](../concepts/commands.md) | 命令在配置文件上声明，参数有类型，下发时校验，在设备可达之前保留，并一直跟踪到设备上报结果或其生存时间到期。参见[发送命令](./sending-commands.md)和[对设备群下发命令](./commanding-a-fleet.md)。 |
| **固件或软件更新** | [空中升级](../concepts/over-the-air-updates.md)：制品、签名者、分配和按组推出 | 设备端是一个需要你实现的小型协议，参见[构建可更新的设备](./building-an-updatable-device.md)。 |
| **仪表板** | [仪表板](../concepts/dashboards.md)，带版本的租户资源（草稿、发布、回滚） | 需要重建，不能导入，参见[仪表板](#dashboards)。 |
| **设备凭据**（访问令牌、基本认证、证书） | [凭据](./device-credentials.md)：`ACCESS_TOKEN`、`MQTT_BASIC` 和 [X.509](./device-x509.md) | 详见[凭据与设备连接](#connecting-devices)。 |
| **网关** | [网关设备](../deployment/edge-services.md#gateway-identity)，代表分配给它的设备发言 | 也可与边缘代理一起使用。 |
| **与 LoRaWAN 网络服务器的集成** | [入站集成](./inbound-integrations.md) | 一个 webhook，把网络服务器的上行消息映射到设备。 |

## 目前已有的迁移途径 {#paths}

下面是现在就能做的事。每一项都是有文档的功能，而不是计划。

### 通过 API 构建你的模型 {#model}

设备开始上报之前，DeviceChain 需要有它们上报所属的模型：设备类型和配置文件、资产类型和客户类型、资产、客户，以及它们之间的关系。这些没有导入文件。可以在控制台中创建，也可以用脚本调用 [GraphQL API](../reference/graphql-api.md)，每一种都有对应的 mutation（`createDeviceProfile`、`createDeviceType`、`createAsset`、`createCustomer`、`createEntityRelationships` 等）。关系 mutation 一次调用最多可带 1000 条关系。

脚本使用[服务账号](../user-guide/service-accounts.md#obtaining-a-token)登录：这是租户中的非人类账号，出示客户端 ID 和密钥，换取短期有效的令牌。请给它一个只授予脚本所需权限的角色。

配置文件是工作量最集中的地方，因为它就是契约。每个指标的键、类型和单位只需决定一次，发布配置文件，然后再接入设备。已发布的版本是冻结的，之后的修改是新版本。参见[设备类型与配置文件](../user-guide/device-types-and-profiles.md)。

### 从 CSV 文件载入设备 {#csv}

[从 CSV 导入设备](./importing-devices.md)是处理库存的批量入口。它在控制台中（**设备 → 导入**），在 GraphQL 中对应 `importDevices`。

- 列包括 `token`、`deviceTypeToken`、`externalId`、`name`、`description`、`metadata`，如果希望为设备预留给自助注册，还有 `provisioningProfileToken`。
- 使用**出厂凭据**时，文件还包含 `credentialType`、`credentialId`，`MQTT_BASIC` 还包含 `credentialValue`。这就是保留设备现有访问令牌的方法：把它填入 `credentialId`，类型为 `ACCESS_TOKEN`，出示该令牌的设备无需重新烧录新令牌即可继续工作。密钥只以带密钥的摘要形式存储，不会再次显示。
- 导入只创建，可以放心重复运行。**检查文件**这一步是试运行，它按真实导入完全相同的方式判定每一行，且不写入任何内容。
- 一个文件最多 10,000 行、5 MiB；更大的设备群请拆成多个文件。
- 用上述表头把旧库存导出为 CSV，或者以 DeviceChain 租户控制台的**导出 CSV**作为模板。

文件中的凭据必须符合 DeviceChain 的规则。`MQTT_BASIC` 密码至少 16 字节，因此旧的较短设备密码无法沿用，需要轮换。违反规则的行会以 `INVALID` 返回并附带原因，可以下载失败的行，更正后再次导入。

### 设备连接时自行注册 {#self-registration}

对于可以重新烧录或重新配置的设备，[自助注册](./device-self-registration.md)让设备凭借配置文件的密钥和密文以及序列号创建自己，并获得其 token 和 MQTT 客户端 ID。`dcctl device register` 可以在 shell 中扮演设备的角色，适合在设备使用某个配置文件之前先测试它，参见[命令参考](../reference/dcctl.md#dcctl-device-register)。使用证书的设备群可以搭配租户添加的信任锚，使用 [X.509 凭据](./device-x509.md)。

### 重新指向 MQTT 设备 {#connecting-devices}

已经使用 MQTT 的设备需要修改连接设置，通常还要修改载荷。请按下列与现状的差异做准备：

- **Broker 地址与 TLS。** 监听器使用 TLS，设备使用实例 CA 连接，参见[连接设置](./connecting-a-device.md#connection-settings)。
- **客户端 ID。** 必须是 `{instanceId}:{tenant}:{deviceToken}`，后面可以加 `:后缀`。Broker 会拒绝任何其他客户端 ID，包括客户端库自行生成的随机值。
- **用户名。** 是 `{tenant}:{credentialId}`，而不只是凭据 ID。
- **主题。** 事件发布到 `{instanceId}/{tenant}/devices/{token}/events`。
- **消息体。** 一个 JSON 信封，包含 `device`、`eventType` 和 `payload.entries`，其中每个读数都是 JSON 字符串。通过 MQTT 发送时，消息体不带凭据。参见[事件正文](./connecting-a-device.md#the-event-body)和[三种标识符](./connecting-a-device.md#three-identifiers)，后者列出了最容易混淆的三个值。

如果无法修改固件，可以在前面放一个小型桥接：一个订阅旧主题并发布到 DeviceChain 的进程，或者一个代表分配给它的一组设备发言的[网关设备](../deployment/edge-services.md#gateway-identity)。使用 HTTP 的设备把同样的信封发送到[连接设备](./connecting-a-device.md)中描述的接入端点。

位于 LoRaWAN 网络服务器之后的设备完全无需改动。添加一个[入站集成](./inbound-integrations.md)，并把网络服务器的 webhook 指向它即可。Sparkplug B 和 LwM2M 设备通过各自的适配器连接，参见 [Sparkplug](../concepts/sparkplug.md) 和 [LwM2M](../concepts/lwm2m.md)，以及[传输能力矩阵](../reference/transport-matrix.md)了解各自支持的能力。

### dcctl 用于实例，而不是你的数据 {#dcctl}

`dcctl` 负责安装、引导、升级和检查实例，参见[命令参考](../reference/dcctl.md)和[引导](../deployment/bootstrap.md)。它不会导入实体或读数。用它来搭建接收迁移的实例，以及使用它提供的检查，例如上面的 `dcctl device register`。

### 迁移历史数据 {#history}

没有任何工具会替你转换其他平台已存储的历史数据：不存在历史数据导入器，读取原平台的数据库或导出文件需要你自己完成。DeviceChain 提供的是接入路径，它接受自带时间的读数。条目的 `occurredTime` 就是它被存储、绘图和评估所依据的时刻，因此可以通过与实时数据相同的接入路径回放历史。有两个限制：`occurredTime` 比平台收到它的时间早超过 **366 天**的读数会被拒绝；接入还有按租户的速率限制，所以大规模回放应当控制节奏。请给每个回放的事件设置 `altId`，这样重试就不会重复存储。参见[缓冲上传与窗口规则](./connecting-a-device.md#buffered-uploads-and-windowed-rules)和[服务质量](./connecting-a-device.md#quality-of-service)。

如果需要更早的历史，或者数据量很大，请先判断是否真的需要把它放进新平台。让旧存储以只读方式继续支持报表，新平台从切换时开始记录，往往是更便宜的做法。若想之后让现有报表工具访问 DeviceChain 的数据，参见 [SQL 与 BI 访问](./sql-and-bi-access.md)。

### 重建仪表板 {#dashboards}

仪表板是一份 JSON 定义，DeviceChain 可以导出和导入，但只能导入 DeviceChain 自己的仪表板：组件绑定的是 DeviceChain 自己的指标和实体。请在[画布编辑器](../user-guide/dashboards.md)中重建每个仪表板，使用插槽（slot），让仪表板在打开时才绑定到某台设备或某个客户，而不是写死其中之一。导入的仪表板会以新 token 保存为草稿，不会替你发布，参见[导入仪表板](../user-guide/dashboards.md#importing-a-dashboard)。有了几个仪表板之后，可以把它们导出作为你的模板。

## 一个完整示例 {#worked-example}

仓库的 `examples/migration-pilot` 中带有一个可运行的评估示例
（[在 GitHub 上](https://github.com/devicechain-io/devicechain/tree/main/examples/migration-pilot)）。
它从一份虚构的小型“原平台”导出开始：两种类型共 25 台设备、它们的遥测键、两条阈值规则和一条基于脚本的规则、
一份仪表板描述，以及每台设备一个访问令牌。随后只用 `curl` 和 `jq` 调用公开的 GraphQL API 完成以下工作：

1. **计划。** 一个映射文件把每个概念转换为对应的 DeviceChain 概念，计划会在写入任何内容之前打印结果，
   包括不会被迁移的部分。
2. **模型。** 创建配置文件、指标、检测规则和设备类型，并发布配置文件。
3. **试运行，然后导入。** 前文所述的[设备导入](#csv)先在不写入的情况下检查全部 25 行，
   再用设备已有的访问令牌创建 23 台设备。另外两行会带着原因返回：密码短于 16 字节，以及一个序列号出现了两次。
4. **核对报告。** 导出中的每一项都被记为已创建、已存在或带原因跳过，随后从 DeviceChain 读回数量并进行比较。
5. **并行试点。** 三台模拟设备同时向原平台的替身和 DeviceChain 发布数据，并比较其数值。
   修改设备配置中的一行即可退回原平台，示例同时演示了这一点和 DeviceChain 侧的强制停用。

它是一条可行性路径，不是转换器。它不会替你读取原平台的文件，不会重建规则或仪表板，不会转换历史数据，
设备仍然需要修改连接设置和负载格式。示例对此有详细说明，包括每项限制的代价。

## 没有直接对应的内容 {#no-equivalent}

有些东西无法迁移，其中少数也没有计划。在确定日期之前，请对照你当前部署所依赖的内容检查这份清单。

- **其他平台文件的转换工具。** DeviceChain 没有针对其他平台导出格式、仪表板或规则定义的导入器，这些都需要重建。
- **自由形式的节点图规则。** 检测规则是针对事件的条件，可以用表单、画布或检测引擎使用的同一种表达式语言编写，执行的动作是触发告警、发送命令、调用 webhook 或发布到连接器。没有在流程中途运行任意脚本代码的节点，也没有通用的流程引擎。一条同时做数据增强、分支和外部调用的链，通常会变成几条规则加一个计算指标，更复杂的逻辑则应放在你通过 webhook 调用的服务中。规划迁移之前，请先阅读[事件处理](../concepts/event-processing.md)。
- **自己编写的设备载荷解码器。** 平台存储网络服务器解码出的值，并能映射通用 JSON 消息体，但不会运行你提供的解码脚本。如果设备发送不透明的二进制载荷，请在它到达 DeviceChain 之前解码，可以放在网络服务器的编解码器中，或放在桥接里。参见[入站集成](./inbound-integrations.md)。
- **文本读数作为历史。** 数值和布尔读数以时间序列存储，文本读数则不会，所以字符串类型的信号会作为状态（例如属性）保存，而不是作为历史绘图。
- **嵌套客户。** 客户没有父级。资产可以嵌套，关系可以表达其他结构。如果你依赖客户层级来控制可见性，请用分组和带范围的成员资格来建模，并测试用户实际能看到什么。
- **某些平台提供的若干空中升级行为。** 推出没有分批、金丝雀晋级或计划任务，并且只有通过 MQTT 连接的设备能接收托管更新。参见[空中升级](../concepts/over-the-air-updates.md)。
- **工业协议驱动、SNMP 和原生移动应用。** 限制与[为什么选择 DeviceChain](../why-devicechain.md#not-yet)中相同：使用能讲该协议的网关，并通过 MQTT 或 HTTP 转发。
- **Kubernetes 以外的平台。** DeviceChain 运行在 Kubernetes 上。如果你来自单服务器安装，除了数据迁移，也要为这一变化预留预算。

如果缺失的部分阻碍了你，请在项目的 [Discussions](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) 中说明；[路线图](https://github.com/devicechain-io/devicechain/blob/main/ROADMAP.md)显示了已经计划的内容。

## 建议的计划 {#plan}

下面的顺序通过先迁移少量设备、让两个平台并行运行、只在确认新平台与旧平台结果一致后才切换来降低风险。

### 1. 盘点与设计

1. 列出你运行的内容：设备类别、每类上报的遥测键和单位、属性、命令、规则、仪表板、客户、用户和集成。
2. 参照上面的表格，把每一项标记为沿用、重建或放弃。
3. 在纸上设计 DeviceChain 模型：每类设备一个配置文件、指标的键和单位、要标记为被跟踪的关系类型，以及你的客户和区域结构。选择在设备整个生命周期内都不会再改的 token。
4. 决定每类设备如何连接：位于网络服务器之后保持不变、重新配置，或者放在桥接之后。

### 2. 搭建试点

1. 试用请按[快速入门](../quickstart/first-device.md)启动实例，正式部署请按[引导](../deployment/bootstrap.md)，并对照你的设备群规模阅读[调优实例](../deployment/tuning-an-instance.md)和[实测吞吐量](../deployment/bootstrap.md#measured-throughput)。
2. 通过 API 或控制台创建模型，发布配置文件，并从 CSV 导入由几十台设备组成的**试点设备群**；如果打算保留现有凭据，就一并带上。
3. 重新指向这些设备或它们的桥接，并在设备的**连接**和**事件**标签页中确认读数以正确的单位到达。
4. 重建这些设备所需的规则以及其所有者使用的仪表板，并确认告警的触发、通知和清除方式与现在一致。

### 3. 并行运行

1. 把试点设备的读数同时发送到两个平台。设备或桥接可以同时发布到两边，网络服务器也可以配置两个 webhook。
2. 在包含你关心的各种情形的一段时间内比较两者：相同的告警在相同的时间出现，一天的合计相同，命令能到达设备并得到回复。
3. 在模型中修正差异，而不是在数据中修正。单位或键错误，就是配置文件的一个新版本。
4. 按设备类别逐步扩大试点，每一类从各自的 CSV 文件导入，检查通过后再进入下一类。
5. 培训将要运维它的人。创建他们的[成员和角色](../user-guide/members-roles-and-customer-users.md)；如果使用单点登录，请配置[单点登录](./configuring-sso.md)；在上线前邀请客户用户进入其客户自己的视图。

### 4. 切换

1. 选择一个安静的时间窗口，并准备好回退方案：旧平台继续运行，它的设备不删除。
2. 逐类重新指向其余设备，在开始下一类之前观察该类的连接数和事件数。采用自助注册的设备群会在每台设备下次被配置时迁移。
3. 回放你决定带过来的历史数据，并设置 `altId` 和 `occurredTime`。
4. 把告警通知和集成迁移到 DeviceChain，并在旧平台上关闭它们，避免同一个人被呼叫两次。
5. 在你需要旧平台历史数据的期间让它保持只读，之后再退役。

### 5. 切换之后

通过[可观测性](../deployment/observability.md)关注实例的指标和告警，配置你打算依赖的[备份与恢复](../deployment/disaster-recovery.md)，并查看[版本策略](../deployment/versioning-policy.md)，了解升级会改变什么、不会改变什么。

## 相关内容

- [为什么选择 DeviceChain](../why-devicechain.md)：DeviceChain 适用于什么，以及它目前还不做什么。
- [领域模型](../concepts/domain-model.md)：本页所对应的实体和关系。
- [从 CSV 导入设备](./importing-devices.md)和[连接设备](./connecting-a-device.md)。
- [GraphQL API](../reference/graphql-api.md)：迁移脚本所驱动的接口。
