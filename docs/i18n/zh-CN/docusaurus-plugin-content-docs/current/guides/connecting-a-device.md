---
sidebar_position: 2
title: 连接设备
---

# 连接设备 {#connecting-a-device}

设备通过 **MQTT** 或 **HTTP** 向 DeviceChain 发送事件。MQTT 由 NATS 内置的 MQTT 服务器直接在 1883 端口提供，无需独立消息代理。两种传输都进入同一条解码 → 解析 → 持久化流水线，因此 JSON 事件正文完全相同。

:::note 状态
MQTT 和 HTTP 摄取已可用。资源受限设备也可通过 [LwM2M 摄取](../concepts/lwm2m.md)，使用带 DTLS 的 CoAP/UDP 连接；既有设备群可使用 [Sparkplug B](../concepts/sparkplug.md)。WebSocket 传输及完整的自助配置/认领流程仍在规划中。
:::

连接在消息代理处受到保护。MQTT/NATS 监听器使用 TLS，NATS auth-callout 对每个连接进行认证，并将其绑定到该设备自己的主题，因此设备只能发布自己的事件、读取自己的命令。流水线还根据凭据逐事件强制执行设备认证。默认设备认证模式为 `required`，因此连接和事件都需要凭据。参见[设备凭据](./device-credentials.md)。LwM2M 和 Sparkplug B 在传输握手时认证，而不是逐事件认证。

## 三种标识符 {#three-identifiers}

通过 MQTT 连接设备，需要正确设置三种不同的标识符。它们用途不同，不能互换，而控制台中的某个地方又都将它们称作令牌或 ID。

| 标识符 | 含义 | 使用位置 |
| --- | --- | --- |
| **设备令牌** | 注册表中的设备身份，例如 `sensor-001`。由你选择。 | 事件正文的 `device` 字段和主题中的 `{token}` 段。二者必须一致：事件声称的设备与主题不同，就会被拒绝。 |
| **凭据 ID** | 设备用来证明自身身份的值。对于 `ACCESS_TOKEN` 凭据，控制台标注为**访问令牌**；对于 `MQTT_BASIC`，标注为**用户名**。 | 进行两次认证：MQTT 连接时，以及流水线逐事件处理时。MQTT 用户名为 `{tenant}:{credentialId}`。 |
| **MQTT 客户端 ID** | `{instanceId}:{tenant}:{deviceToken}`。它是会话键，不是标签。 | 用于 MQTT 连接，不会出现在控制台中。 |

:::warning MQTT 用户名不是单独的凭据 ID
它是 **`{tenant}:{credentialId}`**。直接将控制台的“用户名”值复制到客户端，是最常见的连接失败原因。
:::

消息代理拒绝不符合 `{instanceId}:{tenant}:{deviceToken}` 或该值加 `:suffix` 的客户端 ID，包括未设置时客户端库自动生成的随机 ID。后缀让同一设备能建立两个连接；参见 [MQTT](#mqtt)。

从设备端看，这三种错误完全相同。被拒绝的连接只收到通用回答：消息代理返回 MQTT CONNACK 返回码 5（*未授权*），然后关闭连接。该代码刻意保持通用：无论客户端 ID、`{tenant}:` 前缀还是凭据错误，结果都相同，因此拒绝不会说明哪项检查失败。客户端会报告“未授权”；如果没有读取 CONNACK，则可能只报告随后发生的连接重置或意外 EOF。自动重连的设备会陷入循环。

设备无法连接时，请按以下顺序检查：

1. 客户端 ID。这是控制台从不显示、需要你自行构造的值。
2. 用户名中的 `{tenant}:` 前缀。
3. 凭据本身。

稍后还有第四种标识符：**命令**封装中的 `token` 标识*命令*，不是设备。如果在命令响应中返回设备令牌，将无法匹配任何命令。响应不会结算任何命令，而会进入平台死信流，供运维人员查看；命令仍处于未完成状态。参见[响应命令](#responding-to-a-command)。

## 事件正文 {#the-event-body}

通过任何传输进入的每个事件都是 JSON 对象：

```json
{
  "device": "sensor-001",
  "eventType": "Measurement",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

- `device`：设备的稳定令牌。
- `eventType`：`Measurement`、`Location` 或 `Alert`（也支持 `NewRelationship`）。
- `credentialType` / `credentialId`：设备提供的凭据。`MQTT_BASIC` 还需要 `credentialSecret`。只有实例的设备认证模式设为 `disabled` 或 `optional` 时才可省略。默认值是 `required`，因此需要凭据。
- `payload`：结构取决于 `eventType`，所有结构都是 `{ "entries": [ … ] }`。见下文。

### 载荷结构 {#payload-shapes}

每个载荷都以 `entries` 数组包裹内容，其结构固定了各个值的 JSON 类型：

- 测量值和所有 `Location` 字段都是 **JSON 字符串**（`"21.5"`，不是 `21.5`）。
- 告警的 `level` 是 0 到 2147483647 之间的**未加引号的 JSON 整数**。

这两条规则都会强制执行。没有条目的载荷、没有内容的条目、JSON 类型错误的值（应为字符串却传入数字，或告警 `level` 被加上引号），以及范围之外的告警 `level`，都会被拒绝，而不会静默接受：HTTP 返回 `400`，MQTT 发布进入死信流。

一个条目是一组在某个时间点采集的读数。条目可以携带自己的 `occurredTime`。存储、图表、评估和返回读数时都会使用该时间，因此设备离线期间缓存的读数可以成批上传（不超过下述限制），并保留实际记录的历史。

- 没有 `occurredTime` 的条目采用封装的时间。
- 没有 `occurredTime` 的封装采用平台收到消息时的时间。故障期间在平台中等待的消息会保留到达时间，而不是处理时间。
- 无论出现在哪里，`occurredTime` 都采用 RFC 3339 格式（`2026-08-09T12:00:00.125Z`）。不符合格式的值会被拒绝，并指出有问题的条目，绝不会静默替换。
- 封装或任意条目的 `occurredTime` 如果比平台收到消息的时间早 **366 天**以上，都会被拒绝。缓存上传必须在该时限内到达平台；更早的读数不会存储到任何地方。

有一个有效的 RFC 3339 值也会被拒绝：**`0001-01-01T00:00:00Z`**，平台将其保留为“未报告时间”的标记。没有时钟的设备应省略 `occurredTime`，而不是发送占位值：纪元时间 `1970-01-01T00:00:00Z` 也会因早于 366 天而被拒绝。与其他时间戳拒绝一样，这是终止性拒绝，会连带拒绝**整条消息**，包括同一批次中的所有其他读数。应在固件中排除这类值，而不是到死信队列中才发现。

### 一条消息可以携带多少内容 {#how-much-one-message-may-carry}

所有传输中的单个事件**最多携带 256 个读数**。该限制固定，不能配置。在本页介绍的传输中（MQTT 和 HTTP 上的 JSON 设备事件），一条消息就是一个事件，因此超限消息会被拒绝。协议网关则会拆分：[LwM2M](../concepts/lwm2m.md) Notify 或 [Sparkplug B](../concepts/sparkplug.md) 消息包含更多读数时，会变成多个连续事件，每个最多 256 个（参见[运维人员必须了解的事项](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)）。

一个读数就是一个存储数据项。对于测量，是一个*指标键*，因此包含十二项指标的条目算十二个读数。对于位置和告警，一个条目算一个读数。限制统计键而不是条目，是因为一个条目可能包含数千项指标，而最终成为存储行、状态更新和规则评估的是读数，不是条目。

该限制就是为了约束这种分发放大。逐租户摄取上限按时间统计读数，因此约束租户每秒发送多少读数，而且一条消息的成本不能超过套餐的突发额度。但突发额度是套餐设置：如果某个套餐将突发额度提高到四万，就可能发送一条包含四万个读数的消息。没有此限制时，一条消息对整个实例的成本就会由所有套餐允许的最大突发额度决定。积压更多的设备应将其拆成多条消息上传。

超限消息会**整条拒绝**，绝不会裁剪以满足限制。悄然截短的批次会收到 `202`，而两端都无法发现缺失读数。不会存储任何内容，也不会丢失任何内容：消息会完整路由到解码失败流。

设备如何得知结果取决于传输：

- **HTTP** 返回 `400`，指出计数和限制。
- **MQTT** 不会向设备通知任何内容。消息代理在持久捕获发布时就确认，此时消息尚未解码。因此 `PUBACK` 并不承诺消息已被接受，之后发生的拒绝只有运维人员能看到。

运维人员可通过 `total_msg_too_many_readings` 计数器看到每次拒绝。该限制不可配置。从允许更多读数的版本升级后，已经捕获但尚未解码的消息，也会按新限制被拒绝。

:::caution 深度缓存的批次会完整存储，但检测可能看不到全部读数
只要消息在最早读数的 [366 天](#payload-shapes)内到达，存储就会按每个读数自己的时间完整保存；含有更早读数的消息会整条拒绝。检测不同：设备离线后一次性上传全部历史时，使用时间窗口或保持时长的规则可能丢弃较早读数，且不产生日志或告警。参见[缓存上传与窗口规则](#buffered-uploads-and-windowed-rules)。
:::

#### 缓存上传与窗口规则 {#buffered-uploads-and-windowed-rules}

检测引擎在整个实例内跟踪一个统一的时间前沿，并根据每条消息自己的时间推进它。离线一段时间后一次性上传全部历史的设备，其较早读数可能到达时已落在前沿之后。带时间窗口的规则会丢弃窗口已被前沿越过的读数：包括滚动窗口聚合、会话/间隔规则，以及滑动类型（重复、滑动聚合和关联）。持续时间规则会丢弃满足条件、但落后前沿超过规则保持时长的读数，其余读数按自身时间放置。丢弃不会产生日志或告警记录。

滑动类型和持续时间规则会在 `detect_late_samples_total` 指标中统计丢弃数量。滚动窗口聚合及会话/间隔规则静默丢弃，不计入该指标。

阈值、计数窗口和速率规则仍会评估这些读数。

容忍度由 [`watermarkLatenessSeconds`](../deployment/detection-engine.md) 控制，默认 5 秒。提高它只能在一定范围内有效：前沿是共享的，因此无论某个安静设备离线多久，活跃设备都会持续推进前沿。

如果使用窗口规则的设备群会缓存数据，要么**按跨度小于迟到容忍度的批次上传**，要么不要对这些设备报告的指标使用窗口规则。两种选择都不会影响存储、图表及[事件查询](../reference/graphql-api.md)，所有读数仍然存在。

### 超前的时钟 {#clocks-that-run-ahead}

报告时间戳不能远远超出平台自身时钟。超出的值会按上限时间存储。容忍度足以容纳普通时钟漂移，因此只会影响时钟确实出错的设备。应校准时钟，而不是依赖上限：按上限存储的读数仍然存储在错误时间。远远落后的时钟则有不同处理：早于 366 天的读数会被拒绝，而不是移到另一个时间（参见[载荷结构](#payload-shapes)）。

### 测量 {#measurement}

**`Measurement`**：一个或多个具名读数：

```json
"payload": { "entries": [ { "measurements": { "temperature": "21.5", "humidity": "48" } } ] }
```

### 位置 {#location}

**`Location`**：设备所在位置：

```json
"payload": {
  "entries": [
    {
      "latitude":  "33.74900000",
      "longitude": "-84.38800000",
      "elevation": "320.5",
      "accuracy":  "4.2",
      "speed":     "0.0",
      "heading":   "271.5"
    }
  ]
}
```

`latitude` 和 `longitude` 必填，其余可选。发送接收器实际知道的值，而不是占位值。单位在整个平台固定，不能按设备配置：

| 字段 | 单位 | 范围 |
| --- | --- | --- |
| `latitude` / `longitude` | WGS84（EPSG:4326）十进制度 | ±90 / ±180 |
| `elevation` | 相对于 WGS84 **椭球面**的米数，不是相对于平均海平面 | — |
| `accuracy` | 水平精度，米 | 大于等于 0 |
| `speed` | 米/秒 | 大于等于 0 |
| `heading` | 从真北顺时针计算的度数 | 大于等于 0，小于 360 |

:::caution 高度相对于椭球面，而不是海平面
报告平均海平面以上高度的接收器必须在发送前转换。真实地形中两者可相差数十米，足以让机器落在地理围栏的错误一侧。两种值看起来都合理，因此错误会产生看似可信但错误的位置，而不是可见的报错。
:::

超出范围的值会在首次送达时作为错误数据拒绝，不会重试。最常见的错误是发送乘以 10⁷ 的度数（某些 GPS 和 LwM2M 协议栈的惯例），而 `337490000` 无论如何都不是有效纬度。

### 告警 {#alert}

**`Alert`**：设备希望让人或规则注意到的情况：

```json
"payload": { "entries": [ { "type": "overheat", "level": 5, "message": "coolant over limit", "source": "ecu" } ] }
```

`type` 必填。通知策略、规则和控制台过滤器都依据该分类字段路由，因此无类型的告警是无法采取行动的记录。`level`、`message` 和 `source` 可选。

## MQTT {#mqtt}

MQTT 主题直接映射为 NATS subject。发布到 `{instanceId}/{tenant}/devices/{token}/events` 的消息，会由 `event-sources` 以 subject `{instanceId}.{tenant}.devices.{token}.events` 消费。

- 设备只获准发布到**自己的**事件主题。
- 主题中的 `{token}` 必须与正文中的 `device` 匹配。声称来自其他设备的事件会被拒绝。
- 第一段是**实例 ID**（部署时的 `instance.id`，例如 `devicechain`）。它为设备平面提供命名空间，避免共享消息代理的实例发生交叉；设备凭据只授权其自身实例的主题树。

### 连接设置 {#connection-settings}

监听器使用 TLS，连接由消息代理认证。使用实例 CA 通过 TLS 连接，并将设备凭据作为 MQTT 用户名 **`{tenant}:{credentialId}`** 和密码提供。

将 MQTT **客户端 ID** 设为 `{instanceId}:{tenant}:{deviceToken}`，让连接声明自己属于哪台设备。消息代理拒绝其他值，包括未设置时客户端库生成的随机值。唯一例外是设备令牌后可以带下述 `:suffix`。

客户端 ID 不是简单的记录信息。它是消息代理保存设备会话所用的键；协议规定，使用已在使用的 ID 的连接会*接管该会话*：原设备会断开，新连接继承其订阅。依据消息代理已认证的身份派生 ID，可防止本租户或其他租户的一台设备挤掉另一台。这也让平台在删除租户时能查找并移除其会话状态。

设备需要多个连接时，为每个连接添加后缀：`{instanceId}:{tenant}:{deviceToken}:pub`、`…:sub` 等。第三个 `:` 后的内容可自行选择。共享同一客户端 ID 的两个连接会争用同一会话，并不断互相断开。用一个连接发布、另一个连接订阅命令的设备，必须为二者指定不同后缀。

:::tip 诊断被拒绝的客户端 ID
错误客户端 ID、缺少 `{tenant}:` 前缀及错误凭据都会产生相同的 CONNACK 返回码 5，因此曾能连接的设备突然无法连接时，应先检查客户端 ID，再检查凭据（参见[三种标识符](#three-identifiers)）。持续使用错误 MQTT 密码重试的设备还会被限速：连续失败 10 次后，即使密码正确，其连接也会在每次最长 30 秒内被拒绝（参见[减缓重复失败的连接](./device-credentials.md#connect-backoff)）。修复密码后，等待半分钟，再判断修复是否有效。
:::

### 发布事件 {#publishing-an-event}

将事件正文发布到设备自己的事件主题：

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC","credentialId":"<credentialId>","credentialSecret":"<credentialSecret>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

凭据会认证连接（消息代理）和事件（流水线）。TLS 主机、CA 来源及端口暴露方式取决于实例部署；参见[部署](../deployment/kubernetes-operator.md)。

### 服务质量 {#quality-of-service}

除非有明确理由，否则请以 **QoS 0** 发布遥测。上面的示例使用 QoS 0，因为它是 `mosquitto_pub` 的默认值。

QoS ≥ 1 会消耗实际的服务器存储。消息代理除在服务流中保留消息副本外，还在自身内部存储中为每条 QoS ≥ 1 消息保留第二份副本，该存储与实例的其他内容共享磁盘。平台对其设置上限，避免占满卷，因此持续的 QoS ≥ 1 积压会丢弃**最早**的未送达消息，而不是让实例宕机。

QoS 1 完全受支持。如果设备链路中丢失传输中的发布比存储成本更重要，请明确选择它，并相应规划部署的 JetStream 卷容量。

使用 QoS 1 时，**在事件上设置 `altId` 和 `occurredTime`**。QoS 1 是*至少一次*语义，因此确认丢失会让设备重传，默认会将事件存储两次，使测量重复计数。稳定、由设备生成的 `altId` 会让事件启用去重。匹配同时依据 `altId` 和封装的 `occurredTime`，因此必须在封装上发送二者。条目上的 `occurredTime` 不参与该匹配：

```json
{"altId":"sensor-001-4417","occurredTime":"2026-08-09T12:00:00.125Z","device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}
```

携带已见过的 `altId` 和 `occurredTime` 的重新送达事件会被检测并跳过。没有 `altId` 时，会再次插入。封装有 `altId` 但没有自己的 `occurredTime` 时，时间采用到达时刻，因此设备再次发送的副本会有不同时间，无法匹配首次事件，也会再次存储。这适用于任何至少一次路径，而不只是 MQTT QoS 1；它是让重试安全的唯一方式。

**QoS 2 默认被拒绝。** 在这里，它没有提供 `altId` 无法以更低成本提供的收益，成本却更高：消息代理会保留每条 QoS 2 发布，直到收到 PUBREL，因此设备开始握手却不完成时，会累积无法回收的服务器状态。为了避免留下这个缺口，消息代理直接拒绝 QoS 2 发布。

拒绝方式并不温和。消息代理会断开**连接**，而不是只拒绝一条消息，因此循环以 QoS 2 发布的固件会陷入重连循环。QoS 2 Will 更早在 CONNECT 时就被拒绝。如果设备无明显原因地不断重连，请先检查发布使用的 QoS。

请使用 QoS 0，或携带 `altId` 和 `occurredTime` 的 QoS 1。确实需要 QoS 2 的运维人员可通过部署变量 `nats_mqtt_reject_qos2_publish` 关闭拒绝。无论哪种情况，其填充的缓冲区仍有上限，因此实例磁盘仍受保护。

## HTTP {#http}

`event-sources` 也在 **8081** 端口接受 HTTP 事件。实例 ID 和租户来自路径 `/{instanceId}/{tenant}/events`，与 MQTT 主题约定对应；设备及其凭据放在正文中。

- 事件入队后，`POST` 返回 **202 Accepted**。
- 租户超过 HTTP 摄取上限时返回 **429 Too Many Requests**。上限按读数计数，因此包含多个读数的消息比单个读数消息成本更高。MQTT 路径会丢弃超限消息，而不返回响应。
- 正文无法读取或解码，或路径中的租户不是有效令牌时，返回 **400 Bad Request**。重新发送相同请求仍会得到相同结果。
- 事件未被接受时返回 **503 Service Unavailable**。带 `Retry-After` 响应头时，平台正在拒绝新事件（[背压](../deployment/observability.md#ingest-backpressure)），事件未被存储：等待指定秒数后重新发送。不带该响应头时，发布失败，但事件仍可能已经存储，因此除非带 `altId` 和 `occurredTime`，重发就可能存储两次（参见[服务质量](#quality-of-service)）。两种情况都应重试。租户自身上限先被检查，因此超出租户上限会返回 `429`，而不是这里的 `503`。[传输矩阵](../reference/transport-matrix.md#http)列出完整结果。

HTTP 摄取有独立的逐租户额度，与租户 MQTT 流量消耗的额度分开，因此指定租户名称的 HTTP 请求无法耗尽该租户的 MQTT 遥测额度（参见[未确认的租户名称](../concepts/governance.md#unconfirmed-tenants)）。

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

:::warning 8081 端口必须置于网络控制之后
HTTP 摄取没有传输认证：设备凭据位于请求正文，在请求获准进入后才检查。因此，任何能访问 8081 端口且知道租户名称的人，都能消耗该租户的 HTTP 额度。Chart 的入口不路由此端口，默认情况下集群中的任意 Pod 都可访问。依赖它之前，应将其置于 NetworkPolicy 之后，或置于对调用方进行认证的入口或网关之后。
:::

### 请求时间限制 {#time-limits-on-a-request}

摄取监听器限制请求耗时。设备有 **5 秒**发送请求头，有 **60 秒**发送完整请求（包括头和正文）。二者均可在实例的 `event-sources` 功能域 `httpIngest` 设置中配置。

超过任一限制的请求都会被**关闭连接**。服务器在事件创建前关闭连接，因此没有响应、没有事件，也没有流水线记录可供追踪。症状看起来像偶发的设备端不稳定，且只影响最慢的设备。在受限链路（NB-IoT、2G、卫星）上，完成请求耗时数秒很常见，此时应提高限制，而不是让设备静默失败。`total_http_connections_closed_before_request` 指标统计未能提交请求的连接，这正是这类设备留下的痕迹。

## 接收命令 {#receiving-commands}

设备在**自己的**主题上接收命令：

```
{instanceId}/{tenant}/device-commands/{deviceToken}
```

设备只获准订阅该主题。它无法看到发给其他设备的命令，也无需自行过滤。使用发布事件时的相同凭据订阅：

```bash
mosquitto_sub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001:sub' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/device-commands/sensor-001"
```

每条消息都是 JSON 封装：

```json
{
  "token": "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
  "deviceToken": "sensor-001",
  "name": "reboot",
  "payload": {"delaySeconds": 5},
  "dispatchNonce": "0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94"
}
```

- **`token`** 标识命令，而不是设备。响应时应返回它，它是关联二者的唯一字段。
- **`name`** 是命令键。如果设备配置档声明了命令词汇表，这就是其中一个已发布命令，且 `payload` 已按该命令的参数模式验证。参见[命令与能力契约](../concepts/commands.md#commands-and-the-capability-contract)。
- **`dispatchNonce`** 标识本次命令送达。它是不透明值，设备端不应读取或解释，必须在响应中原样返回。保存它直到应答。如果同一命令再次到达，应返回**最近一次**送达中的 nonce，而不是最初保存的值。

## 响应命令 {#responding-to-a-command}

通过发布到设备**自己的**命令响应主题报告结果：

```
{instanceId}/{tenant}/command-responses/{deviceToken}
```

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/command-responses/sensor-001" \
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` 必须是送达封装中的 `token`**，即命令令牌，而不是设备令牌。在这里发送设备令牌是最常见的错误。它不匹配任何命令，因此响应不会结算任何内容：响应会反复重新送达，直到消息代理送达上限（五次尝试），然后以原因 `exhausted` 记录到死信流，供运维人员查看；命令仍未完成。
- **`dispatchNonce` 必须来自你正在应答的送达封装。** 它是必填项。省略它，或引用同一命令较早送达中的 nonce，都不会结算命令。参见[为何需要 nonce](#why-the-nonce-is-required)。
- **`success`** 将命令转为 `SUCCESSFUL` 或 `FAILED`。
- **`payload`** / **`error`** 是可选字符串，在控制台命令历史中展示，并由 API 返回。

与事件和命令主题一样，该主题按设备划分，设备只获准发布到自己的主题。租户和响应设备都从主题获取，而不是正文，因此设备只能应答**自己的**命令。指定其他设备所属命令的响应会被拒绝，不会记录。

:::caution 主题已经变更
该主题曾是租户范围的（`{instanceId}/{tenant}/command-responses`，不含设备段）。消息代理现在会拒绝设备向旧主题发布，响应不会到达平台，因此对应命令会一直未完成，直到 `TIMEOUT`。请更新设备构造该主题的所有位置。
:::

:::info 响应才会完成生命周期
从未获得应答的命令会保持 `SENT`，直到 TTL 将其转为 `TIMEOUT`。没有响应，平台只知道命令已分发，不知道设备是否执行。如果设备不响应，发出命令时应设置 `expiresAt`，使其按你的时间安排达到终态，而不是采用平台默认七天。
:::

### 为配置下发预留的主题 {#reserved-device-topics}

:::note 已预留，尚未启用
为设备配置下发预留了另外两个按设备划分的主题。目前没有任何组件向它们发布或从中读取，请勿基于它们开发。在此列出，是为了避免设备将这些名称用于其他用途。

```
{instanceId}/{tenant}/device-desired/{deviceToken}    （平台到设备；设备可订阅属于自己的主题）
{instanceId}/{tenant}/device-reports/{deviceToken}    （设备到平台；设备可向属于自己的主题发布）
```

与命令一样，设备只被授权使用带有自身设备令牌的主题，且仅限所示方向。
:::

### 为何需要 nonce {#why-the-nonce-is-required}

返回 `dispatchNonce` 可告知平台，你的应答属于命令的**这一次**送达。这很重要，因为命令可能合理地被发布多次。发布报告错误时，平台无法区分消息丢失和确认丢失，因此会将命令重新排队发送。没有 nonce，第一次送达的应答若在第二次发出后才到达，就会在设备执行第二次之前关闭命令。

对于自己开发的设备，这有两个影响：

- **没有** `dispatchNonce` 的响应会被拒绝。命令不会结算，应答会记录到平台死信流，而不是丢弃，因此拒绝可见，不会静默发生。
- 引用了命令**已不再使用**的 `dispatchNonce` 的响应，也会以同样方式拒绝。请使用实际应答那次送达的 nonce。

执行命令期间，将 nonce 与命令一同保存；应答前若同一命令再次送达，则覆盖它。

## 接下来会发生什么 {#what-happens-next}

1. **event-sources** 解码原始消息。
2. **device-management** 根据凭据认证设备，并解析事件。设备的每个被跟踪关系（分配到客户/区域/资产）都会记录为锚点，使读数可按每个维度查询。**未分配**的设备仍可上报：事件不带锚点，而不会被丢弃（参见[管理设备分配](./managing-assignments.md)）。
3. **event-management** 将解析后的事件持久化到 TimescaleDB 超表，**device-state** 更新设备的最新读数和连接状态。

参见[架构 → 事件流水线](../concepts/architecture.md#the-event-pipeline)。
