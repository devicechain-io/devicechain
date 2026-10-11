---
title: 架构
---

# 架构 {#architecture}

DeviceChain 由基于共享核心库构建的无状态 Go 微服务组成。Kubernetes Operator 协调它们，NATS JetStream 连接它们。单个实例为所有租户提供服务，采用共享微服务模型。租户隔离在消息层和存储层强制执行，而不是为每个租户运行独立的 Pod。

## 组件 {#components}

| 组件 | 职责 |
|---|---|
| `event-sources` | 入站设备传输。解码原始消息（目前支持 JSON；计划支持 Protobuf 和自定义解码器），执行每租户接入速率限制，并将消息发布到流水线。 |
| `device-management` | 管理设备、设备类型、支持版本管理的设备配置文件、带类型的关系图、告警对象及其生命周期，以及事件解析（为每个事件附加设备和组织上下文）。 |
| `event-processing` | 检测与动作。流式核心在事件重放时产生相同结果，对已解析事件评估检测规则：阈值、持续时间、重复、变化率、缺失、连接状态、窗口聚合和区域关联。随后派发自动动作：触发告警、发送命令和调用出站连接器。检测在此运行；它触发的告警对象仍由 `device-management` 管理，连接器投递交给 `outbound-connectors`。 |
| `event-management` | 将已解析事件持久化到 TimescaleDB，执行数据生命周期策略（压缩、保留、汇总），并通过 GraphQL 提供时序查询。 |
| `device-state` | 每台设备的实时最新已知状态投影：在线状态和每项测量的当前读数。 |
| `command-delivery` | 向设备持久化投递双向命令，跟踪每条命令的生命周期。它可以一次向一台设备发送，也可以将面向整个设备群的发送记录为单个批次。 |
| `dashboard-management` | 支持版本管理的仪表板定义（草稿、发布、回滚），以不透明内容存储，由本仓库前端工作区的 React 运行时包渲染。导出定义是控制台端功能，不是服务操作。 |
| `notification-management` | 根据每租户策略，通过电子邮件（SMTP）和 webhook 将已触发的告警通知人员。策略按严重程度路由，也可以按设定间隔，对持续未确认、未解除的告警再次通知。 |
| `user-management` | 管理全局身份、各租户成员关系、角色目录，以及 JWT 签发与验证。 |
| `sparkplug-ingest` _（可选启用）_ | 有状态的 Sparkplug B 主机应用。它向各租户的客户 MQTT 代理建立*出站*连接，运行 Sparkplug 会话状态机，并将数据送入流水线，包括确定设备在线状态。同一时间由一个副本提供服务，通过带隔离保护的租约选举。参见 [Sparkplug B](./sparkplug.md)。 |
| `lwm2m-ingest` _（可选启用）_ | 通过 DTLS 终止基于 CoAP/UDP 的 OMA LwM2M 连接。设备建立*入站*连接，通过经过认证的 DTLS PSK 身份识别。注册驱动在线状态，观测资源被解码为测量，读取、写入、执行和固件更新通过命令与更新路径完成。参见 [LwM2M](./lwm2m.md)。 |
| `ai-inference` _（可选启用）_ | 根据自然语言描述起草检测规则，并使用其他编写界面所用的*同一个*编译器进行验证。模型提出规则，编译器判断其是否有效。它绝不参与规则评估路径。参见 [AI 辅助编写](./ai-authoring.md)。 |
| `outbound-connectors` _（可选启用）_ | 向外部系统投递出站动作：HTTP/webhook 调用，以及向消息代理和云队列（MQTT、Kafka、AWS SNS/SQS）执行 `publish`。它使用限定于租户、支持版本管理的连接器，凭据保存在密钥存储中。服务在独立进程中运行，因此缓慢或行为异常的外部系统不会影响检测流水线。参见[出站连接器](./outbound-connectors.md)。 |
| `mcp` _（可选启用）_ | 只读的 Model Context Protocol 服务器，使 AI 助手能够代表用户访问租户。它是 GraphQL API 上的一层轻量 OAuth 2.1 资源服务器，携带调用方自己的租户作用域令牌。它没有服务令牌，只提供经过选择的读取工具。参见 [AI 访问（MCP）](./mcp.md)。 |
| `update-management` _（可选启用）_ | 空中（OTA）更新：固件制品及其向设备的分配。本版本只提供服务本身：它可以部署、迁移自己的数据库 Schema，并报告健康、就绪状态和指标，但其 API 尚未实现，对每次调用都返回 `NOT_IMPLEMENTED` 错误。 |
| `operator` | 基于 controller-runtime 的 Operator，管理 `Instance` 自定义资源及其生命周期。目前协调循环只观察资源；聚合已渲染 Deployment 的就绪状态，是该循环计划中的后续功能。配置不会原地重新加载：服务只在启动时读取一次配置，通过滚动更新 Pod 采用更改。工作负载本身由 Helm Chart 渲染。租户是控制平面数据库记录，不是被协调的资源。 |

向多台设备分发同一条命令属于 `command-delivery`，不是独立服务。参见[一条命令，多台设备](./commands.md#command-batches)。调度功能仍在计划中；当前状态以仓库为准。

## 数据与消息基础设施 {#the-data-and-messaging-backbone}

**NATS JetStream** 是以下三项功能的统一基础设施：

- 异步消息传递
- MQTT 接入：设备连接 NATS 内置的 MQTT 服务器，端口为 1883
- 键值缓存与锁

没有独立的 Kafka、Redis 或 MQTT 代理。

**PostgreSQL** 存储所有数据，使用两个结构一致、相互独立的数据库：

- *关系*存储保存实体数据：租户、用户、设备、关系。
- *事件*存储启用 TimescaleDB 扩展，将时序事件保存在支持压缩和连续聚合的超表中。

`event-management` 管理事件存储的模式。它是唯一向该存储写入事件或从中提供事件查询的服务。另一个连接该存储的服务是 `user-management` 的租户清除协调器，而且仅用于删除已移除租户的数据行。因此，两类存储能够独立备份、调整容量和恢复。

两类存储都作为由 Operator 管理的 PostgreSQL 集群运行，因此高可用性取决于实例数量，而不是不同的存储套餐：默认各一个实例，高可用安装各三个实例。有副本时，两者的复制行为不同：

- 关系存储会等待副本确认后才完成写入。
- 事件存储在有可用副本时采用相同方式，但没有副本时退回异步复制，因为尚未持久化的事件仍然持久保存在消息层，可以重放。

两类存储也会持续归档，默认启用：预写日志流加上定时基础备份，各自写入独立存储桶。因此，两者都可以恢复到保留窗口中的任意时间点，而不仅是昨晚。恢复是在*创建*集群时进行的，不是对运行中集群执行的操作；参见[灾难恢复](../deployment/disaster-recovery.md)。

主题按租户划分作用域（`{instance}.{tenant}.{suffix}`），数据库中的事件数据也按租户划分。这使共享服务能够安全地为多个租户服务。

## 事件流水线 {#the-event-pipeline}

```
device → MQTT/NATS → event-sources → (decoded event)
       → device-management → (resolved event: device + relationship context attached)
       → event-management → TimescaleDB
```

解析期间，`device-management` 查找设备的**被跟踪**关系，并将它们作为索引维度附加到事件上。之后，“客户 X 的所有事件”等下游查询就不需要联表。参见[领域模型](./domain-model.md)。

## 部署模型 {#deployment-model}

OpenTofu 分两层配置基础设施：

1. 集群中每个实例共享的资源（关系数据库、Ingress、TLS、监控），在安装集群时配置一次。
2. 每个实例自己的代理（NATS）和事件存储（TimescaleDB），在初始化该实例时配置。

Helm Chart 渲染平台工作负载：每个已启用功能区域对应一个 Deployment 和 Service。你可以通过部署**配置档**（`default` / `full` / `telemetry` / `ingest-only`）或显式集合选择功能区域。依赖检查会在安装时拒绝无效选择。

Operator 假定基础设施已经存在，负责 `Instance` 生命周期，而不是创建工作负载。租户是控制平面数据库记录，不是被协调的资源。这种分离避免了将集群初始化放入应用代码。参见[部署](../deployment/kubernetes-operator.md)。

## 配置、健康状态与启动 {#configuration-health-and-startup}

每个服务将配置加载到带类型的模式中，如果配置有误就拒绝启动。未知或拼写错误的键、错误类型和无效值都会在启动时被拒绝，而不是被静默忽略。错误配置会立即暴露，不会等到之后表现为异常行为。

每个服务都向 Kubernetes 提供两个 HTTP 端点：

- **`/healthz`**（存活）在进程仍能完成工作时返回 `200`。如果服务未主动要求断开，而消息代理连接已永久关闭，则返回 `503`；例如运行中的 Pod 所使用的代理凭据被更改。随后 Kubernetes 重启 Pod，Pod 使用收到的凭据重新连接。
- **`/readyz`**（就绪）在服务认证尚未生效时返回 `503`，之后返回 `200`。服务关闭期间，以及 `/healthz` 返回 `503` 时，它也会返回 `503`。

服务启动时为**未就绪**状态，在后台从 `user-management` 获取 JWT 签名公钥。未就绪期间，服务会从 Service 端点中移除，消息消费者保持暂停。因此，短暂的 `user-management` 故障只会使服务降级，不会令其崩溃；任何请求或消息都不会在未经认证验证的情况下被处理。

## 密钥处理 {#secret-handling}

有些值绝不保存在明文配置或可逆编码的列中：

- 集成与提供方凭据，例如 SMTP 密码、webhook bearer 令牌，或出站连接器的代理/云凭据
- 为每个登录令牌签名的密钥中的私钥部分

这些值保存在**加密密钥存储**中。每个值在静态存储时使用独立的 AES-256-GCM 数据密钥加密，数据密钥再由密钥加密密钥（KEK）封装。默认 KEK 是实例现有 Kubernetes Secret 中的根密钥，因此无需额外基础设施即可获得静态加密。

该存储围绕可插拔密钥提供方构建，因此外部密钥管理器可以接管密钥封装，而无需改变使用方存储或解析句柄的方式。目前只提供实例根密钥这一种提供方。

使用方只保存不透明的**句柄**。API 中的值是只写的：在使用时于服务器内部解析，绝不返回明文。密钥变更会被审计（谁、何时、哪个句柄，绝不包含值）。

## API 接口 {#api-surface}

平台的数据和管理 API 使用 GraphQL，支持自省并描述自身。每个提供 API 的服务都提供自己的模式；`user-management` 和 `ai-inference` 还提供独立的管理模式。没有 gRPC，也没有需要与这些模式并行维护的领域 REST API。

服务间的大多数流量通过 NATS 异步传递。如果服务在执行操作时需要另一个服务立即返回结果或执行动作，则使用短期服务令牌直接调用该服务的 GraphQL 端点。例如，命令入队前检查设备是否存在，以及检测规则发送命令。

除了每个服务提供的 `/healthz`、`/readyz` 和 `/metrics` 端点，以及只在启用开发者工具时提供的 `/graphiql` 浏览器外，非 GraphQL 的 HTTP 端点包括标准规定的端点和三个用途有限的端点。标准端点有：

- 令牌验证器获取的 JWKS 文档
- OAuth 2.1 授权服务器端点（`/oauth/authorize`、`/oauth/token`、`/oauth/userinfo`、`/oauth/jwks` 和 RFC 8414 元数据文档），由 `user-management` 在配置 issuer URL 后提供
- 可选启用的 [MCP 服务器](./mcp.md)使用的基于 HTTP 的 JSON-RPC 传输，以及它发布的受保护资源元数据文档（RFC 9728）

另外三个端点是：

- 面向设备的 HTTP 事件接入端点（`POST /{instance}/{tenant}/events`），由 `event-sources` 在独立端口提供；默认配置包含一个 HTTP 来源
- `user-management` 上的租户徽标上传与下载端点（`/branding/logo`）
- `user-management` 上供服务获取短期服务令牌的端点（`/auth/service-token`）
