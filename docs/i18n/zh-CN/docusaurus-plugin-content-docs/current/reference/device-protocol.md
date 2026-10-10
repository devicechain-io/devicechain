---
sidebar_position: 5
title: 设备协议参考
---

# 设备协议参考 {#device-protocol-reference}

本页是设备通过 MQTT 和 HTTP 与 DeviceChain 交换的每一种消息的参考：设备发送的事件、设备接收的命令，以及设备回送的响应。本页列出每个字段、平台拒绝一条消息时会发生什么，以及在哪里能看到这次拒绝。如需分步说明，请先阅读[连接设备](../guides/connecting-a-device.md)。

LwM2M 和 Sparkplug B 设备不使用这些消息。它们的协议网关会将其转换为平台的内部格式；请参阅 [LwM2M 接入](../concepts/lwm2m.md)和 [Sparkplug-B 接入](../concepts/sparkplug.md)。

## 机器可读的 Schema {#schemas}

本页的每种消息都以 JSON Schema（draft 2020-12）形式发布。可用它们校验固件输出，或生成类型：

| Schema | 描述 |
| --- | --- |
| [`device-event.schema.json`](pathname:///schema/device/device-event.schema.json) | 设备发送的[事件信封](#device-event) |
| [`measurement-payload.schema.json`](pathname:///schema/device/measurement-payload.schema.json) | [`Measurement`](#measurement-payload) 事件的载荷 |
| [`location-payload.schema.json`](pathname:///schema/device/location-payload.schema.json) | [`Location`](#location-payload) 事件的载荷 |
| [`alert-payload.schema.json`](pathname:///schema/device/alert-payload.schema.json) | [`Alert`](#alert-payload) 事件的载荷 |
| [`new-relationship-payload.schema.json`](pathname:///schema/device/new-relationship-payload.schema.json) | [`NewRelationship`](#new-relationship-payload) 事件的载荷 |
| [`command-delivery.schema.json`](pathname:///schema/device/command-delivery.schema.json) | 平台下发给设备的[命令](#command-delivery) |
| [`command-response.schema.json`](pathname:///schema/device/command-response.schema.json) | 设备回送的[响应](#command-response) |

同一份列表（使用绝对 URL）位于 [`/schema/index.json`](pathname:///schema/index.json) 的 `deviceProtocol` 下。信封 Schema 通过相对 URL 引用各载荷 Schema，因此按 URL 加载它的校验器会自行解析这些引用。

这些 Schema 与解码这些消息的代码一同提交到代码库中；如果某个字段在一侧被重命名、新增或删除而另一侧没有，测试会让平台构建失败。

Schema 表达不了的两点：

- **未知成员会被忽略，而不是被拒绝。** 拼错的可选字段（把 `altId` 写成 `altID` 没问题，写成 `alt_id` 则不行）会被静默丢弃，而不是被拒绝。在本地校验时额外加上 `additionalProperties: false`，可以在测试中发现这类问题。
- **"必填"描述的是设备必须发送的内容。** 其中大部分由平台自行强制执行，[拒绝表](#rejections)准确列出了平台会拒绝什么。平台唯一容忍缺失的字段在其所在行中注明。

## 版本 {#versioning}

消息中没有版本字段。契约就是随你所用版本的文档发布的 Schema 所描述的内容，且 Schema 与解码其消息的代码在同一个版本中变更。在 v1.0.0 之前，任何版本都可能改变契约，因此请比较你当前运行的版本与目标版本之间的 Schema。

## 主题与端点 {#topics}

平台 broker 是 NATS 内置的 MQTT 服务器。MQTT 主题与平台读取的 NATS subject 是同一个名称，只是 `/` 写作 `.`：

| 消息 | 方向 | MQTT 主题 | NATS subject |
| --- | --- | --- | --- |
| [事件](#device-event) | 设备 → 平台 | `{instanceId}/{tenant}/devices/{deviceToken}/events` | `{instanceId}.{tenant}.devices.{deviceToken}.events` |
| [命令下发](#command-delivery) | 平台 → 设备 | `{instanceId}/{tenant}/device-commands/{deviceToken}` | `{instanceId}.{tenant}.device-commands.{deviceToken}` |
| [命令响应](#command-response) | 设备 → 平台 | `{instanceId}/{tenant}/command-responses/{deviceToken}` | `{instanceId}.{tenant}.command-responses.{deviceToken}` |

- 设备只被授权在带有**自身**设备令牌的主题上发布和订阅。它无法读取其他设备的命令，也无法冒充其他设备发布。
- **设备通过 MQTT 连接，而不是直接使用 NATS。** 设备凭据只授权 MQTT 连接：出示设备凭据的普通 NATS 客户端在连接时即被拒绝。subject 一列是运维人员在 NATS 工具中看到的名称，而不是第二条接入途径。连接设置（client id、`{tenant}:{credentialId}` 用户名、TLS）见[连接设置](../guides/connecting-a-device.md#connection-settings)。
- 以 QoS 0 发布事件，或以 QoS 1 发布并携带 [`altId`](#device-event)。QoS 2 发布默认被拒绝，且 broker 会关闭连接；见[服务质量](../guides/connecting-a-device.md#quality-of-service)。

通过 **HTTP**，设备只能发送事件。将同样的[事件正文](#device-event)以 `POST` 发送到 `event-sources` 服务（端口 8081）的 `/{instanceId}/{tenant}/events`。HTTP 没有下行通道：只使用 HTTP 的设备无法接收命令。

## 设备事件 {#device-event}

每条 MQTT 消息或每个 HTTP 请求一个 JSON 对象。一条消息就是一个事件。

| 字段 | 类型 | 必填 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `device` | string | 是¹ | 发送事件的设备令牌。在 MQTT 上必须等于主题中的 `{deviceToken}`。当凭据认证该事件时，它必须是该凭据所属的设备。 | `"sensor-001"` |
| `eventType` | string | 是 | `Measurement`、`Location`、`Alert` 或 `NewRelationship`，区分大小写。决定载荷结构。 | `"Measurement"` |
| `payload` | object | 是 | 事件内容。其结构取决于 `eventType`；见[载荷](#payloads)。 | `{"entries":[…]}` |
| `occurredTime` | string，RFC 3339 | 否 | 事件发生的时间。省略时，以平台收到消息的时间作为事件时间。 | `"2026-08-09T12:00:00.125Z"` |
| `altId` | string | 否 | 由设备选定的幂等键。如果事件的 `altId` **和**信封 `occurredTime` 都与同一租户中已存储的某个事件相同，它会被当作重复投递而跳过。见[下文](#altid)。 | `"sensor-001-4417"` |
| `relationship` | string | 否 | 会被接受并沿处理链路传递，但**不会被使用**。无论此字段为何值，平台都会在事件上记录该设备所有被跟踪的关系。不要依赖它。 | — |
| `credentialType` | string | 见下文 | `ACCESS_TOKEN` 或 `MQTT_BASIC`。与 `credentialId` 一起认证该事件。 | `"ACCESS_TOKEN"` |
| `credentialId` | string | 见下文 | 对 `ACCESS_TOKEN` 而言就是令牌本身。对 `MQTT_BASIC` 而言是用户名，不带 MQTT 连接所用的 `{tenant}:` 前缀。 | `"5f98…98b2"` |
| `credentialSecret` | string | 仅 `MQTT_BASIC` | `MQTT_BASIC` 的密码。 | |

¹ 每个事件都应发送 `device`。严格来说，当凭据认证了该事件时，平台容忍其缺失，并将事件归属于该凭据所属的设备。

**凭据。** 默认的设备认证模式是 `required`：没有凭据的事件会被拒绝（见[拒绝表](#rejections)）。只有在配置为 `optional` 或 `disabled` 的实例上才可以省略凭据。只有当 `credentialType` 和非空的 `credentialId` 同时存在时，平台才会读取凭据。见[设备凭据](../guides/device-credentials.md)。

**时间戳。** 每个 `occurredTime`，无论在信封上还是在条目上，都是 RFC 3339 格式。平台会拒绝不符合该格式的值、等于 `0001-01-01T00:00:00Z` 的值（保留用于表示"未报告时间"），以及早于平台收到消息时刻超过 **366 天**的值。远远超前于平台时钟的时间会被按上限存储，而不是被拒绝；见[超前的时钟](../guides/connecting-a-device.md#clocks-that-run-ahead)。

### `altId` 与重复 {#altid}

至少一次投递（MQTT QoS 1、在 `503` 之后重试 HTTP 请求）可能把一个事件投递两次。没有 `altId` 时，两份副本都会被存储。有 `altId` 时，第二份会被跳过：

- 匹配同时基于 `altId` **和**信封的 `occurredTime`。条目上的 `occurredTime` 不计入匹配。
- 请在信封上发送 `occurredTime`。没有它，每份副本都以到达时间为准，两个时间不同，两份都会被存储。
- 匹配范围是**按租户**，而不是按设备。两台设备在同一时刻使用相同的 `altId` 会发生冲突，其中一个事件会被跳过。请让该值在整个设备群中唯一，例如以设备令牌作为前缀。
- 与已有事件 `altId` 和 `occurredTime` 都相同的第二个事件即使内容不同也会被跳过。

### 载荷 {#payloads}

`Measurement`、`Location` 和 `Alert` 载荷把内容包装在 `entries` 数组中。一个条目是某一时刻的一次读数，每个条目都可以携带自己的 `occurredTime`；没有的条目沿用信封上的时间。没有条目的载荷，或把内容直接放在 `payload` 下的载荷，都会被拒绝。

一个事件**最多携带 256 个读数**，两种传输方式都一样。一个读数是一个测量键，或一个位置条目或告警条目。超过上限的消息会被整体拒绝，绝不会被截断。

#### Measurement {#measurement-payload}

| 字段 | 类型 | 必填 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `entries` | array | 是 | 样本。至少一个。 | |
| `entries[].measurements` | string → **string** 的对象 | 是 | 指标名到值的映射。每个值都是 JSON **字符串**：`"21.5"`，而不是 `21.5`。不带引号的数字会导致整条消息失败。至少一个指标。 | `{"temperature":"21.5"}` |
| `entries[].occurredTime` | string，RFC 3339 | 否 | 该样本的时刻。 | |

#### Location {#location-payload}

每个位置字段都是 JSON **字符串**，包括数值字段。每个值都必须能解析为其范围内的有限数值。

| 字段 | 类型 | 必填 | 含义 | 范围 |
| --- | --- | --- | --- | --- |
| `entries` | array | 是 | 定位点。至少一个。 | |
| `entries[].latitude` | string | 是 | WGS84 十进制度 | −90 到 90 |
| `entries[].longitude` | string | 是 | WGS84 十进制度 | −180 到 180 |
| `entries[].elevation` | string | 否 | 高于 WGS84 **椭球面**的米数，而非高于平均海平面 | 绝对值 ≤ 99999999 |
| `entries[].accuracy` | string | 否 | 水平精度，米 | 0 到 99999999 |
| `entries[].speed` | string | 否 | 米每秒 | 0 到 99999999 |
| `entries[].heading` | string | 否 | 自真北起顺时针的度数 | 0 到 360（不含 360）；359.99995 及以上会被拒绝 |
| `entries[].occurredTime` | string，RFC 3339 | 否 | 该定位点的时刻 | |

#### Alert {#alert-payload}

| 字段 | 类型 | 必填 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `entries` | array | 是 | 告警。至少一个。 | |
| `entries[].type` | string | 是 | 通知策略、规则和控制台筛选器用来路由的分类。为空会被拒绝。 | `"overheat"` |
| `entries[].level` | **integer** | 否 | 严重程度，0 到 2147483647 之间不带引号的 JSON 整数。带引号的级别会导致整条消息失败。省略时为 0。 | `5` |
| `entries[].message` | string | 否 | 供人阅读的文本。 | `"coolant over limit"` |
| `entries[].source` | string | 否 | 设备上产生该告警的部件。 | `"ecu"` |
| `entries[].occurredTime` | string，RFC 3339 | 否 | 该告警的时刻。 | |

#### NewRelationship {#new-relationship-payload}

从发送事件的设备到同一租户中另一实体创建一条关系。此载荷没有 `entries` 数组，且三个键按确切名称读取，因此大小写有影响。

| 字段 | 类型 | 必填 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `relationshipType` | string | 是 | 关系类型的令牌：租户中定义的类型，或平台的保留类型之一（首次使用时自动创建）。 | `"member"` |
| `targetType` | string | 是 | `device`、`asset`、`area`、`customer` 或 `group`。 | `"group"` |
| `target` | string | 是 | 目标实体的令牌。 | `"building-7-sensors"` |

平台无法创建的关系（未知的目标类型、不存在的目标、未知的关系类型）不会被创建。与任何无法解析的事件一样，它会被重试，然后进入死信。

## 命令下发 {#command-delivery}

设备在其 `device-commands` 主题上收到的内容，命令每派发一次就收到一次。下发是**仅实时**的：命令发布时未连接并订阅的设备收不到它，平台也无从得知。见[接收命令](../guides/connecting-a-device.md#receiving-commands)。

| 字段 | 类型 | 总是存在 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `token` | string | 是 | **命令的**令牌。它标识命令，而不是设备。在响应中以 `commandToken` 回送。 | `"6f1c0f8e-…"` |
| `deviceToken` | string | 是 | 命令的目标设备：总是命令所到达主题对应的设备。 | `"sensor-001"` |
| `name` | string | 是 | 命令键。当设备的配置文件声明了命令词汇表时，为其中已发布的命令之一。 | `"reboot"` |
| `dispatchNonce` | string | 是 | 标识该命令的这一次派发。它是不透明的：不要解析它。在响应中原样回送。如果同一命令再次到达，请使用最近一次下发中的 nonce 作答。 | `"0f6f4a2c-…"` |
| `payload` | 任意 JSON 值 | 否 | 命令的参数，与下达时完全一致。如果配置文件为该命令声明了参数 Schema，则已经过校验。命令下达时没有参数则不出现。 | `{"delaySeconds":5}` |

## 命令响应 {#command-response}

设备在其 `command-responses` 主题上发布的内容，用于结束一条命令。平台从**主题**中获取响应设备，从不从正文中获取。

| 字段 | 类型 | 必填 | 含义 | 示例 |
| --- | --- | --- | --- | --- |
| `commandToken` | string | 是 | 所响应的那次下发中的 `token`：命令的令牌，而不是设备的令牌。 | `"6f1c0f8e-…"` |
| `dispatchNonce` | string | 是 | 所响应的那次下发中的 `dispatchNonce`。 | `"0f6f4a2c-…"` |
| `success` | boolean | 是 | `true` 将命令结束为 `SUCCESSFUL`，`false` 结束为 `FAILED`。省略时按 `false` 处理。 | `true` |
| `payload` | **string** | 否 | 结果文本，随命令存储并由 API 返回。它是 JSON **字符串**，不是对象：要返回结构化数据，请将其编码进字符串。此处若为对象，整条响应都无法解码，并会被丢弃（见下文）。 | `"rebooting in 5s"` |
| `error` | string | 否 | 命令失败的原因。仅在 `success` 为 `false` 时存储；为 `true` 时被忽略。 | `"actuator jammed"` |

```json
{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":false,"error":"actuator jammed"}
```

## 拒绝与错误 {#rejections}

平台不接受的消息会怎样，取决于它在哪里被拒绝以及使用哪种传输方式。HTTP 会响应请求，因此设备能在入口处得知拒绝。MQTT 在 broker 捕获消息后立即确认发布（`PUBACK`），这发生在**解码之前**，因此 MQTT 设备永远不会收到通知：broker 之后的任何拒绝只有运维人员能看到。

### 入口处 {#rejections-at-the-door}

由 `event-sources` 在接收和解码消息时检查。

| 情形 | HTTP | MQTT | 运维人员在哪里看到 |
| --- | --- | --- | --- |
| 路径或主题中的实例 id 不是本实例的 | `404` | broker 拒绝该发布：设备未被授权使用该主题 | — |
| 路径中的租户不是有效令牌 | `400` | broker 拒绝该发布 | — |
| 租户超出其接入上限（按消息数或按读数） | `429`，带 `Retry-After: 1` | 先确认，然后**丢弃** | `total_msg_rate_limited`、`total_msg_reading_limited` |
| 正文无法读取，或超过 1 MiB | `400` | — | — |
| 正文无法解码：不是 JSON、未知或仅限平台的 `eventType`（`StateChange`、`CommandInvocation`、`CommandResponse`）、值的 JSON 类型错误、没有条目、空条目、位置字段缺失或超出范围、告警缺少 `type` 或 `level` 大于 2147483647 | `400`，正文中给出原因 | 先确认，然后转入 `failed-decode` 流 | `total_msg_failed_decode` |
| 时间戳不是 RFC 3339、等于 `0001-01-01T00:00:00Z`，或早于 366 天以上 | `400`，并指出字段 | 转入 `failed-decode` | `total_msg_failed_decode`、`total_msg_invalid_event_time` |
| 超过 256 个读数 | `400`，给出数量和上限 | 转入 `failed-decode` | `total_msg_failed_decode`、`total_msg_too_many_readings` |
| 正文中的 `device` 不是主题中的设备 | — | 转入 `failed-decode` | `total_msg_failed_decode` |
| 平台正在施加[背压](../deployment/observability.md#ingest-backpressure) | `503`，**带** `Retry-After`：未存储，请重新发送 | 不会被拒绝：它在捕获流中等待；只有该流写满时，最早捕获的消息才会被丢弃 | |
| 事件无法交给处理链路 | `503`，**不带** `Retry-After`：可能已被存储，因此重发会存储两次，除非携带 [`altId`](#altid) | — | |
| 已接受 | `202` | `PUBACK`（QoS 1） | |

`400` 是终态：同一请求总会得到同样的答复。入口处的拒绝会拒绝**整条消息**，包括其中的所有其他条目。

### 接受之后 {#rejections-after-acceptance}

被接受的事件（`202`，或已被 broker 捕获）随后由 `device-management` 解析，它会认证设备并把事件关联到该设备。这些拒绝在两种传输方式上相同，且不会通知任何设备。事件会被重试，然后以下列原因之一记录到 `failed-events` 流：

| 原因 | 起因 |
| --- | --- |
| `Unauthenticated` | 设备认证为 `required` 时没有凭据；凭据认证失败；或正文中的 `device` 不是该凭据所属的设备。 |
| `DeviceNotFound` | 未使用凭据，且 `device` 令牌未在租户中注册。 |
| `Invalid` | 事件早于 366 天以上，或无法按其类型解释。 |
| `ApiCallFailed` | 解析所需的某次查询或写入失败，包括平台无法创建的 [`NewRelationship`](#new-relationship-payload)。 |

被拒绝的事件会一直重试到第五次投递，因此如果设备在此期间完成注册，或数据库已恢复，事件仍会被存储。例外是过旧而无法存储的事件，它在第一次投递时就被记录，因为任何重试都无法改变它的时间。

### 命令响应 {#rejections-command-responses}

无论使用哪种传输方式，命令响应都不会得到回复。它的去向如下：

| 响应 | 结果 |
| --- | --- |
| 对应该设备拥有的命令，且带有其当前派发的 `dispatchNonce` | 命令结束为 `SUCCESSFUL` 或 `FAILED`。 |
| 响应一条已经结束的命令 | 被忽略；命令保持原有结果。 |
| 无法解码：不是 JSON，或字段类型错误，例如 `payload` 是对象或 `success` 带引号 | **被丢弃**：会记录日志并计数，但不进入死信。命令保持 `SENT` 直至超时，除非设备再次正确响应。 |
| 没有 `dispatchNonce` | 不结束命令；以原因 `unprocessable` 记录到 `dead-letters` 流。 |
| 带有命令已经弃用的那次派发的 `dispatchNonce` | 不结束命令；以原因 `unprocessable` 进入死信。 |
| `commandToken` 不对应任何命令（最常见的是误发了设备自己的令牌） | 重试到第五次投递，然后以原因 `exhausted` 进入死信。 |
| 命令属于另一台设备 | 被拒绝：记录日志并计数，不记入该命令，也不进入死信。 |

无人响应的命令会一直保持 `SENT`，直到过期将其变为 `TIMEOUT`。见[为何需要 nonce](../guides/connecting-a-device.md#why-the-nonce-is-required)。
