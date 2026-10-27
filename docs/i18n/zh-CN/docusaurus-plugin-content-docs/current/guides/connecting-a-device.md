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
| **凭据 ID** | 设备用来证明自身身份的值。对于 `ACCESS_TOKEN` 凭据，控制台标注为**访问令牌**；对于 `MQTT_BASIC`，标注为**用户名**。 | 通过 MQTT 时认证连接，事件正文不携带凭据。通过 HTTP 时从正文认证每个事件。MQTT 用户名为 `{tenant}:{credentialId}`。访问令牌只在设置时显示一次，之后无法读回。 |
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
- `credentialType` / `credentialId`：设备**通过 HTTP** 提供的凭据。`MQTT_BASIC` 还需要 `credentialSecret`。只有实例的设备认证模式设为 `disabled` 或 `optional` 时才可省略。默认值是 `required`，因此 HTTP 事件需要携带凭据。**通过 MQTT 时，这三个字段都不要带**：设备连接时消息代理已完成认证，平台 MQTT 消息代理上携带其中任何一个的事件都会被拒绝（参见[设备协议参考](../reference/device-protocol.md#rejections-at-the-door)）。
- `payload`：结构取决于 `eventType`，所有结构都是 `{ "entries": [ … ] }`。见下文。

每个字段的类型及是否必填（包括可选的 `altId` 和 `relationship`）见[设备协议参考](../reference/device-protocol.md#device-event)，其中还提供可用于校验固件输出的 JSON Schema。

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

超限消息会**整条拒绝**，绝不会裁剪以满足限制。悄然截短的批次会收到 `202`，而两端都无法发现缺失读数。不会存储任何内容。为便于诊断，消息会被路由到解码失败流，其中携带的凭据会被移除，消息体只保留前 16 KiB。

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

:::tip 让控制台来写
控制台设备页面上的 **Send data**（发送数据）选项卡会为该设备生成这条命令以及下文的 HTTP 命令：包括它的令牌、凭据，以及其已发布配置文件所声明指标的示例读数。参见[发送数据](/user-guide/devices#send-data)。
:::

将事件正文发布到设备自己的事件主题：

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

凭据用于认证连接。消息代理由此知道每条发布来自哪台设备，因此事件本身不携带凭据；携带凭据的事件会被拒绝。TLS 主机、CA 来源及端口暴露方式取决于实例部署；参见[部署](../deployment/kubernetes-operator.md)。

代理使用 TLS，因此 `--cafile` 需要实例的 CA 证书。在本地 kind 安装中，它位于实例命名空间的 `dc-nats-tls` Secret 里，键为 `ca.crt`。用一条命令将其保存为 `instance-ca.crt`（把 `dci-` 前缀后的实例 ID 换成你自己的）：

```bash
kubectl -n dci-devicechain get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

在本地 kind 集群上，代理位于 `localhost:1883`，但其证书并非为 `localhost` 或 `host.docker.internal` 签发，因此主机名校验会失败。仅在本地测试时，可给 `mosquitto_pub` 加上 `--insecure` 跳过主机名检查：

```bash
mosquitto_pub --cafile instance-ca.crt --insecure -h localhost -p 1883 ...
```

:::warning 仅限本地测试
`--insecure` 会关闭主机名校验，连接不再能证明连到的是你的代理。切勿在生产环境使用；请改为连接证书所覆盖的主机名。
:::

### 服务质量 {#quality-of-service}

除非有明确理由，否则请以 **QoS 0** 发布遥测。上面的示例使用 QoS 0，因为它是 `mosquitto_pub` 的默认值。

QoS ≥ 1 会消耗实际的服务器存储。消息代理除在服务流中保留消息副本外，还在自身内部存储中为每条 QoS ≥ 1 消息保留第二份副本，该存储与实例的其他内容共享磁盘。PUBACK 承诺什么、积压时会发生什么，取决于一个实例设置 `infrastructure.nats.mqttFailClosed`：

- **关闭（默认）。** 平台对存储设置上限，避免占满卷。PUBACK 表示代理收下了消息，而不是平台一定会保留它：持续的积压会丢弃**最早**的未送达消息，而不是让实例宕机，且设备不会得到通知。在捕获流更换主节点期间（代理重启、升级或节点丢失都会引起），无论负载高低，代理已确认的 QoS 1 消息也可能丢失；没有任何计数器记录，`JetStreamCaptureLeaderChanged` 会警告发生过切换（见 [MQTT：平台代理](../reference/transport-matrix.md#mqtt--the-platform-broker)）。在持续负载下，即使没有更换主节点也可能丢失：当 `event-sources` 落后、捕获流的积压达到上限时，每确认一条新消息，该流就丢弃最早的一条未读消息，且设备不会得到通知。在一次负载测试中，这丢失了约 7% 到 13% 的 MQTT 流量。`devicechain_<area>_jetstream_consumer_unread_skipped_total` 会统计这些丢失，`JetStreamCaptureBacklogGrowing`、`JetStreamCaptureBacklogWillOverflow` 和 `JetStreamDurableLostUnread` 会对此告警。
- **开启。** PUBACK 表示平台已保存该消息，覆盖事件、命令响应和设备报告。当平台处理落后、存储写满时，它会**拒绝新的发布**，而不是丢弃旧消息。代理不发送 PUBACK 并关闭连接，因此设备保留该消息，并在重新连接后再次发送：设备被要求等待，数据不会丢失。存储没有主节点期间也是如此：代理不确认 QoS 1 发布，设备会再次发送。该设置会降低实例能跟上的流量，而且代理在过载下内存耗尽时仍可能丢失已确认的消息，因此请按低于其上限的规模部署。存储超过 80% 时，新的设备连接会被拒绝，直到回落到 60% 以下。拒绝表现为 MQTT CONNACK 返回码 5（未授权），因此固件在收到返回码 5 后必须继续重试，而不是放弃。以这种方式被关闭连接、并在 30 秒内重新连接的设备不会被报告为离线。一个例外：超出其租户摄取上限的消息仍会被确认并丢弃，与关闭该设置时相同。要开启该设置，请在 `dcctl bootstrap` 或 `dcctl upgrade` 上使用 `--mqtt-fail-closed`；参见 [MQTT 故障关闭](../deployment/observability.md#mqtt-fail-closed)。

QoS 0 不受该设置影响。代理从不存储 QoS 0 发布，因此不会对其施加反压，两种情况下它都保持尽力而为。

QoS 1 完全受支持。如果设备链路中丢失传输中的发布比存储成本更重要，请明确选择它，并相应规划部署的 JetStream 卷容量。开启该设置后，存储使用热流上限（`streamMaxBytes`），并像其他流一样在卷上预留。

只有在代理只服务这一个实例时才接受该设置，因为代理为其服务的所有实例只保留一个存储；共享代理上的实例开启该设置后会拒绝启动。该保证针对多副本代理（三个副本）。单副本时，节点崩溃可能丢失代理已确认但尚未写入磁盘的消息。在开启或关闭该设置期间，或存储主节点切换期间发出的发布可能到达两次，但绝不会丢失，这也是设置 `altId` 的又一个理由。边缘代理内置的 MQTT 消息代理不在覆盖范围内。

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
HTTP 摄取没有传输认证：设备凭据位于请求正文，在请求获准进入后才检查。因此，任何能访问 8081 端口且知道租户名称的人，都能消耗该租户的 HTTP 额度。除下文的集成路径外，Chart 的入口不路由此端口，默认情况下集群中的任意 Pod 都可访问。依赖它之前，应将其置于 NetworkPolicy 之后，或置于对调用方进行认证的入口或网关之后。
:::

Chart 唯一对外发布的路径是入站集成 webhook，供 ChirpStack 或 The Things Stack 等网络服务器使用。它通过实例的 HTTPS 地址提供服务，路径中不含实例 ID，发送方使用自己的 bearer 令牌放在 `Authorization` 请求头中：

```text
POST https://<instance-host>/api/integrations/{tenant}/{integration}[/{messageType}]
```

只发布该路径，且它只接受 `POST`；其他方法返回 `405`。`/{instanceId}/{tenant}/events` 仍只在集群内部可达。

发送方是由他人运营的网络服务器，因此必须信任实例的证书：使用自签名证书的实例无法被托管的网络服务器使用，因为它们不会信任该实例的 CA。请为该主机使用公共 CA 签发的证书（`ingress.tls.clusterIssuer`）。

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
- **`payload`** 可选，可以是任意 JSON 值（字符串按其文本存储，对象或数组按该 JSON 存储）；**`error`** 是可选字符串。两者都在控制台命令历史中展示，并由 API 返回。

响应未能结束其命令的所有情形，见[设备协议参考](../reference/device-protocol.md#rejections-command-responses)。

与事件和命令主题一样，该主题按设备划分，设备只获准发布到自己的主题。租户和响应设备都从主题获取，而不是正文，因此设备只能应答**自己的**命令。指定其他设备所属命令的响应会被拒绝，不会记录。

:::caution 主题已经变更
该主题曾是租户范围的（`{instanceId}/{tenant}/command-responses`，不含设备段）。消息代理现在会拒绝设备向旧主题发布，响应不会到达平台，因此对应命令会一直未完成，直到 `TIMEOUT`。请更新设备构造该主题的所有位置。
:::

:::info 响应才会完成生命周期
从未获得应答的命令会保持 `SENT`，直到 TTL 将其转为 `TIMEOUT`。没有响应，平台只知道命令已分发，不知道设备是否执行。如果设备不响应，发出命令时应设置 `expiresAt`，使其按你的时间安排达到终态，而不是采用平台默认七天。
:::

### 为何需要 nonce {#why-the-nonce-is-required}

返回 `dispatchNonce` 可告知平台，你的应答属于命令的**这一次**送达。这很重要，因为命令可能合理地被发布多次。发布报告错误时，平台无法区分消息丢失和确认丢失，因此会将命令重新排队发送。没有 nonce，第一次送达的应答若在第二次发出后才到达，就会在设备执行第二次之前关闭命令。

对于自己开发的设备，这有两个影响：

- **没有** `dispatchNonce` 的响应会被拒绝。命令不会结算，应答会记录到平台死信流，而不是丢弃，因此拒绝可见，不会静默发生。
- 引用了命令**已不再使用**的 `dispatchNonce` 的响应，也会以同样方式拒绝。请使用实际应答那次送达的 nonce。

执行命令期间，将 nonce 与命令一同保存；应答前若同一命令再次送达，则覆盖它。

## 配置下发主题 {#reserved-device-topics}

设备通过两个按设备划分的主题接收配置，并向平台报告处理结果：

```
{instanceId}/{tenant}/device-desired/{deviceToken}    （平台到设备；设备可订阅属于自己的主题）
{instanceId}/{tenant}/device-reports/{deviceToken}    （设备到平台；设备可向属于自己的主题发布）
```

与命令一样，设备只被授权使用带有自身设备令牌的主题，且仅限所示方向。平台从主题而不是消息中获取租户和设备，因此消息中不得携带设备身份（携带的会被拒绝），设备也只能为自己报告。

配置由设备的 `SHARED` 属性构建，但只包含其配置文件声明为设备可见配置的键：在控制台的配置文件编辑器中声明这些键（或使用 `setDeviceProfileConfigurationDeclaration`），配置文件发布后它们才会下发到设备。声明中未列出的共享键永远不会发送；已声明但存储值与声明类型不符的键会被省略。会使设备配置超过 64 KiB 的共享属性写入会被拒绝。控制台的设备页面显示最新修订、设备最近报告的内容以及该修订是否仍待应用；`deviceConfiguration` 查询返回相同信息。

每当设备的配置发生变化，平台会在 `device-desired` 上发布一个修订版本：

```json
{"v":1,"kind":"config.revision","revision":3,"digest":"sha256:9e39b20b9def308341baa751db8c2570bfc5b562e1d5420f9e6506510e0328da","document":"{\"sampleIntervalSeconds\":30}"}
```

`document` 是承载配置的 JSON **字符串**。请解码该 JSON 字符串的值，对其 UTF-8 字节计算 SHA-256，并将 `sha256:` 加小写十六进制摘要与 `digest` 比较；如果不一致，则不要应用任何内容。哈希之前不要解析文档再重新序列化，因为摘要覆盖的是平台发送的字节。

修订版本可能乱序到达设备，较低的修订版本可能在较高的之后到达。请忽略修订号低于你当前持有版本的 `config.revision`；如果修订号就是你已持有的版本，请再次报告 `APPLIED`，而不是重复应用。

设备在 `device-reports` 上回复。每条消息都是 JSON，不超过 4 KiB，未知字段会被拒绝。

- **`sync`** 在设备每次连接时发送，说明它持有哪个修订版本（没有则为 `0`）：`{"v":1,"kind":"sync","config":{"haveRevision":3}}`。请在发送 sync **之前**订阅 `device-desired`：平台不保留任何消息，在你订阅之前发布的回复会丢失。无论设备是否已持有，平台都会回复当前修订版本；当设备没有任何配置时则完全不回复。不要指望设备离线期间发布的消息稍后会送达：同步才是设备追上最新状态的方式。请在每次连接时同步，并定期同步（例如每隔几小时），因为平台提交后、发送前丢失的修订版本只能靠 sync 修复。距上一次 sync 仅几秒内再次发送的 sync 会被确认并忽略。
- **`config.status`** 报告设备对某个修订版本做了什么：`{"v":1,"kind":"config.status","revision":3,"digest":"sha256:…","status":"APPLIED"}`。状态为 `RECEIVED`、`APPLIED` 或 `REJECTED`。`REJECTED` 报告必须携带 `errors`，即最多 16 个字符串、每个最多 256 字节，说明原因。

只有当设备确实拥有该修订版本且摘要与之一致时，平台才会记录 `config.status`；设备对该修订版本报告 `APPLIED` 后，就不再显示为待处理。报告至少被处理一次，因此重复发送是安全的，较旧的报告也不会覆盖较新的报告。无法解码、指向未发给该设备的修订版本、或摘要与该修订版本不符的消息会被拒绝，并记录到死信流，而不是被丢弃。仅使用 HTTP 的设备不通过此通道提供服务。

### 恢复或复制配置 {#restore-copy-configuration}

设备的配置可以恢复到较早的修订版本，也可以设置为与另一台设备相同：例如撤销一次错误的更改，或让替换设备获得被替换设备的配置。两者都可以由拥有 `device:write` 的操作员在设备页面的**配置**选项卡中完成，或通过 `restoreDeviceConfiguration` 和 `copyDeviceConfiguration` 变更完成。

- **恢复（Restore）**从修订历史中选取设备自己的某个修订版本。**从其他设备复制**选取同一租户中另一台设备的最新修订版本。
- 控制台会先显示预览：每个键、其当前值、之后的值，以及它是新增、更改还是删除。在选择**应用（Apply）**之前，不会有任何更改。
- 应用时，设备已声明键的 `SHARED` 属性会被设置为该修订版本中的值；该修订版本不包含的已声明键会被删除。随后会创建一个**新的**修订版本，并照常发送给设备。较早的修订版本永远不会被修改。如果设备已经持有该配置，则不会创建任何内容。
- 如果设备的配置文件未声明修订版本中的某个键（`CONFIGURATION_KEY_UNDECLARED`）、某个值不符合其键当前声明的类型（`CONFIGURATION_VALUE_TYPE_MISMATCH`），或结果超过大小限制（`CONFIGURATION_TOO_LARGE`），整个更改都会被拒绝，且不写入任何内容。因此，在配置文件的声明更改之后恢复旧修订版本可能会被拒绝。其他租户的设备、未知的修订版本，或没有修订版本的源设备，都返回 `NOT_FOUND`。
- 如果在应用恢复或复制期间，设备配置的另一项更改先行提交，则该操作会以 `CONFIGURATION_CONFLICT` 被拒绝，且不写入任何内容；可以安全地重试。
- 复制会应用控制台预览时的源修订版本，即使源设备的配置在你选择**应用**之前发生了变化。通过 GraphQL 时，请将该修订版本作为 `fromRevision` 传入；不传时，`copyDeviceConfiguration` 复制源设备的最新修订版本。

```graphql
mutation {
  restoreDeviceConfiguration(deviceToken: "pump-7", revision: 3) {
    desired { revision document }
  }
}
```

## 空中（OTA）更新 {#over-the-air-updates}

在运行更新服务的实例上（参见[空中（OTA）更新](../concepts/over-the-air-updates.md)），同样的两个主题也传递固件和软件更新。上述规则在此同样适用：每条消息都带有 `"v":1` 和 `kind`，未知或重复的字段会被拒绝，消息不得携带设备身份。任一方向的更新消息最大 4 KiB。

当某个制品被分配给设备时，平台在 `device-desired` 上发布：

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"UNVERIFIED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true}}
```

`component` 指明要更新的设备部件；每个部件上设备最多同时处理一个分配。固件制品的 `requiresReboot` 为 true，软件和配置制品为 false。`trust` 为 `UNVERIFIED` 或 `SIGNED`。`UNVERIFIED` 表示平台只检查了字节是否与声明的一致，而不检查由谁生成；它不携带签名字段，携带则会被拒绝。`SIGNED` 增加三个字段，全部必填，如下所示，设备在报告 `VERIFIED` 之前**必须**验证它们：平台只转发签名，是否信任由设备决定。

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"SIGNED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true},"signature":"MEQCIHGf…","keyId":"f3ef3f7c…","algorithm":"ECDSA_P256_SHA256"}
```

- `signature` 是分离式签名，使用标准字母表的 base64。
- `keyId` 是 64 个小写十六进制字符：签名者公钥按 DER `SubjectPublicKeyInfo` 编码后的 SHA-256。设备在其信任的公钥中查找它。
- `algorithm` 为 `ECDSA_P256_SHA256`（ASN.1 DER 签名，即 `cosign sign-blob` 产生的形式）或 `ED25519`。其他取值会被拒绝。
- 两种算法签名的都是制品的**原始 32 字节 SHA-256 摘要**，而不是其十六进制文本，也不是 `sha256:` 字符串。Ed25519 直接以这 32 字节作为消息。请根据下载到的字节计算摘要，而不是取自 `artifact.digest`，并先确认它等于 `artifact.digest`。

手工生成签名（`file` 是制品，`k.pem` 是私钥）：

```bash
# keyId：公钥按 DER SubjectPublicKeyInfo 编码后的 SHA-256
openssl pkey -in k.pem -pubout -outform DER | openssl dgst -sha256

# ECDSA_P256_SHA256 （P-256 密钥）：对 SHA-256(file) 签名
openssl dgst -sha256 -sign k.pem -binary file | base64 | tr -d '\n'
cosign sign-blob --key k.key file          # 同样的签名，base64

# ED25519：对 32 字节摘要本身签名
openssl dgst -sha256 -binary file > digest
openssl pkeyutl -sign -rawin -inkey k.pem -in digest | base64 | tr -d '\n'
```

对于 Ed25519，`cosign` 和 `openssl pkeyutl -sign -rawin -in file` 签名的是**文件**而不是其摘要，设备会拒绝这样的签名：请使用上面的 `digest` 形式。ECDSA 是与 `cosign sign-blob` 兼容的算法。

如果签名验证失败，以 `FAILED` 终止更新，`result.code` 为 `SIGNATURE_INVALID`。如果 `keyId` 指向设备不信任的密钥，使用 `UNKNOWN_SIGNER`；当设备要求签名镜像而收到 `UNVERIFIED` 分配时，也使用该代码。二者都在下载结束时报告（在 `DOWNLOADED` 之后，与摘要不匹配相同），并且都不会改变上述阶段。签名覆盖的是镜像而不是版本，因此需要拒绝降级的设备必须自行实施。

要获取镜像，请在 `device-reports` 上申请下载授权：

```json
{"v":1,"kind":"download.request","assignmentId":"asg-91xq"}
```

平台在 `device-desired` 上回复一个 URL。在 `expiresAt` 之前（最长十五分钟），无需其他凭据即可通过该 URL 下载这一个制品：

```json
{"v":1,"kind":"download.grant","assignmentId":"asg-91xq","url":"https://…/api/update-management/updates/download/…?cap=…","expiresAt":"2026-10-12T10:15:00Z"}
```

使用 HTTP `GET` 下载。该端点支持 `Range` 请求，因此中断的下载可以从已持有的字节处续传；授权过期后，再次发送相同的 `download.request` 即可获得新的授权。如果该分配已不是当前分配，平台回复的是 `update.cancel` 而不是授权。

在 `device-reports` 上用 `update.progress` 报告每个阶段：

```json
{"v":1,"kind":"update.progress","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","artifactDigest":"sha256:…","seq":3,"stage":"DOWNLOADING","progress":{"bytes":4194304,"total":8388608},"bootId":"b-20261012-0815"}
```

- `attemptId`、`assignmentId`、`component` 和 `artifactDigest` 重复分配中的值；指向其他尝试或摘要的报告不会被应用。
- `seq` 从 1 开始，该尝试的每次报告都递增。重复发送同一报告无害；以已使用的 `seq` 发送不同内容的报告属于冲突，不会被应用。
- `stage` 取值为 `RECEIVED`、`DOWNLOADING`、`DOWNLOADED`、`VERIFIED`、`INSTALLING`、`REBOOTING`、`RUNNING`、`FAILED` 和 `ABANDONED` 之一。可以跳过报告丢失的阶段，但不得退回到更早的阶段。
- `progress`（已持有的字节数，以及已知时的总数）只允许与 `DOWNLOADING` 一起出现。
- 只有当所持字节的 SHA-256 等于 `artifact.digest`，并且（对 `SIGNED` 分配）签名验证通过时才报告 `VERIFIED`。
- `FAILED` 必须携带 `result`：`{"code":"FLASH_WRITE","detail":"…"}`，其中 code 最多 64 个大写字母、数字和下划线，detail 最多 512 字节。代码 `SIGNATURE_INVALID` 和 `UNKNOWN_SIGNER` 表示设备拒绝了镜像的签名，详见上文。
- `RUNNING` 必须携带 `running`：`{"version":"1.4.2"}`，可选附带正在运行内容的 `digest`。新版本运行后立即发送。报告其他版本的 `RUNNING` 不会被应用：如果设备未处于目标版本，请报告 `FAILED`。
- 每次报告都必须带 `bootId`：当前启动的标识，最多 128 个可打印字符，每次设备启动时都会改变。当 `requiresReboot` 为 true 时，只有来自与安装时不同的启动的 `RUNNING`，平台才接受为更新成功的证据。

`update.cancel`（`{"v":1,"kind":"update.cancel","attemptId":"att-7f3k","assignmentId":"asg-91xq"}`）要求设备停止处理该分配。如果尚未开始安装，设备报告 `stage` 为 `ABANDONED`；安装一旦开始，更新就无法撤回。

设备启动后，用 `update.inventory` 报告每个部件正在运行的内容，该消息不指向任何尝试：

```json
{"v":1,"kind":"update.inventory","component":"firmware","running":{"version":"1.4.2"},"bootId":"b-20261012-0821"}
```

当设备在安装或重启期间失去响应、平台将更新标记为 `UNKNOWN` 时，就是靠它来确定结果的。

设备每次连接时发送的 `sync`（参见[配置下发主题](#reserved-device-topics)）也会让平台重新发送设备仍应处理的每个更新，并对设备尚未确认取消的更新再次发送取消消息。分配不会被保留，因此请先订阅 `device-desired` 再发送 sync。

## 接下来会发生什么 {#what-happens-next}

1. **event-sources** 解码原始消息。
2. **device-management** 根据凭据认证设备，并解析事件。设备的每个被跟踪关系（分配到客户/区域/资产）都会记录为锚点，使读数可按每个维度查询。**未分配**的设备仍可上报：事件不带锚点，而不会被丢弃（参见[管理设备分配](./managing-assignments.md)）。
3. **event-management** 将解析后的事件持久化到 TimescaleDB 超表，**device-state** 更新设备的最新读数和连接状态。

参见[架构 → 事件流水线](../concepts/architecture.md#the-event-pipeline)。
