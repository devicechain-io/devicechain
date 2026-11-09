---
sidebar_position: 3.7
title: 入站集成
---

# 入站集成 {#inbound-integrations}

大多数设备会通过 MQTT 或 HTTP 自行连接 DeviceChain，但有些设备从不这样做：LoRaWAN 传感器只与网关通信，而 ChirpStack 或 The Things Stack 这样的**网络服务器**会对网关收到的数据进行解码，再通过 HTTPS 转发。**入站集成**就是这种转发的接收端。它为发送方提供一个 URL 和一个 bearer 令牌，找出每条消息所属的设备，并把读数存储为普通测量。此后它们与其他测量没有区别：有单位，可驱动仪表板，可触发检测规则并产生告警。

同一机制也适用于任何能发送 HTTP `POST`（JSON 或二进制正文）的系统，由你指明设备标识符在哪里。本页依次介绍 [ChirpStack v4](#chirpstack)、[The Things Stack v3](#the-things-stack)、[通用 JSON](#generic-json) 和[通用二进制](#generic-binary)，然后介绍存储内容、重复处理、限制，以及第 1 版不支持的功能。

## 如何协同工作 {#concept}

集成是租户的一项资源，包含以下设置：

| 设置 | 含义 |
| --- | --- |
| 令牌（Token） | 集成在租户内的名称，是 URL 的最后一段，由你选择（`chirpstack-eu868`）。不能更改。 |
| 发送方类型（Sender type） | `CHIRPSTACK_V4`、`TTS_V3`、`GENERIC_JSON` 或 `GENERIC_BINARY`。它决定如何读取正文，且不能更改。正文绝不会根据外观猜测。 |
| 设备类型（Device types） | **范围**：无论发送方声称什么，只有这些类型的设备才能通过该集成访问。至少一个，最多 50 个。 |
| 设备标识符（Device identifier） | 发送方如何称呼设备：按 `DEV_EUI`（默认）、按 `DEVICE_ID`（仅 The Things Stack），或者对通用发送方，按你指定的别名方案（`ALIAS:custom.serial`）。 |
| 通用设置 | 仅用于通用发送方：标识符在哪里、上行数组在哪里、发送方自己的时间在哪里。 |
| 存储链路指标（Store link metrics） | 默认关闭。随每条上行存储信号强度、信噪比和网关数量。 |
| 接受数据（Accepting data） | 关闭集成可立即拒绝其流量，而无需删除它。 |
| Bearer 令牌 | 由服务器生成（256 位随机数），创建集成或轮换令牌时**只显示一次**，仅以带密钥的摘要形式存储。 |

每条消息都经过相同的步骤：

1. 在读取正文之前，平台先根据 URL 中指定的集成校验 bearer 令牌。
2. 按集成的发送方类型读取正文，取出设备标识符。
3. 查找以该标识符作为[别名](#aliases)的设备。设备超出集成范围，或根本没有这样的设备，则消息不会被存储。
4. 把解码出的值转换为读数，存储为该设备的测量。

租户可以拥有多少个集成由[层级](../concepts/tenant-tiers.md)设置 **Integrations per tenant**（每租户集成数）决定。平台默认值为 5，层级最多可设为 100，0 表示不允许。超过上限的创建请求会被拒绝。

## URL {#url}

每个集成都通过同一个公开地址访问：

```text
POST https://<host>/api/integrations/{tenant}/{integration}[/{messageType}]
Authorization: Bearer <bearer-token>
```

- `<host>` 是你的实例地址，也就是打开控制台所用的地址。请自行替换；实例 ID 不属于该 URL。
- `{tenant}` 是租户的令牌，`{integration}` 是集成的令牌。
- `{messageType}` 可选。只有 The Things Stack 会用到它，因为它把每种消息发送到各自的路径。
- 只接受 `POST`，其他方法返回 `405`。
- 令牌只放在 `Authorization` 头中，不要放在别处。URL 中的令牌会被写入访问日志。

创建集成时，控制台会在一次性显示令牌的对话框中给出 URL、请求头和发送方所需的设置；之后也可以在集成页面的 **Connect the sender**（连接发送方）下再次看到。

:::warning 由他人运营的网络服务器必须信任你的证书
托管的网络服务器不会信任使用自签名证书的实例。连接托管服务器之前，请为该主机配置公共 CA 签发的证书（`ingress.tls.clusterIssuer`）。你自己运行的网络服务器可以改为信任你的 CA。
:::

如果你负责运营实例，还必须告诉 `event-sources` 应该相信其前面的哪些代理所报告的客户端地址（`infrastructure.http.trustedProxies`），否则所有发送方会共用同一份请求额度。`dcctl` 会设置它；参见[客户端地址与受信任代理](../deployment/bootstrap.md#trusted-proxies)。

## 连接发送方之前 {#before-you-start}

1. **准备一个带配置文件的设备类型。**该类型的设备才是集成可以访问的设备。为每个需要单位或校验的解码值声明一个[指标](../user-guide/device-types-and-profiles.md#metrics)，其键与解码对象产生的键相同。未声明的键同样会被存储，只是没有单位。
2. **创建集成。**在控制台侧边栏打开 **Connect → Integrations**（集成），选择 **New integration**（新建集成）。选择 **Sender type**、它可以访问的 **Device types**，对网络服务器还要选择 **Device identifier**。如果需要信号数据，开启 **Store link metrics**。选择 **Create integration**，并从对话框中复制 bearer 令牌。它只显示一次；如果丢失，请轮换。打开 Integrations 页面需要 `device:read` 权限，修改需要 `device:write`。
3. **添加带别名的设备**，见下一节。可以在发送方开始发送之前或之后进行：尚不存在的设备的消息会被列出，方便你[根据所见内容创建设备](#unregistered)。

使用 GraphQL 时，`createIntegration` 会同时返回集成和令牌：

```graphql
mutation {
  createIntegration(request: {
    token: "chirpstack-eu868"
    name: "ChirpStack EU868"
    type: "CHIRPSTACK_V4"
    enabled: true
    deviceTypeTokens: ["lora-sensor"]
    linkMetrics: true
  }) {
    integration { token type identity }
    issuedToken
  }
}
```

`rotateIntegrationToken(token)` 会替换令牌并只返回一次新令牌。旧令牌立即失效，没有重叠期，因此请随即更新发送方。`updateIntegration` 可更改除令牌和类型之外的所有设置。`deleteIntegration` 删除集成；设备上的别名会保留。

## 用别名识别设备 {#aliases}

消息使用发送方对设备的称呼，而不是 DeviceChain 的称呼。设备**别名**把这个名称记录在设备上。别名由方案和值组成：

| 方案 | 值 | 存储形式 |
| --- | --- | --- |
| `lorawan.deveui` | LoRaWAN DevEUI：恰好 16 位十六进制数字。 | 大写。`70b3d57ed0012345` 与 `70B3D57ED0012345` 是同一个别名。 |
| `tts.device-id` | 设备在 The Things Stack 中的设备 ID：1 到 36 个字母、数字和连字符，不能以连字符开头或结尾。 | 小写。 |
| `custom.<name>` | 通用发送方使用的你自己的标识符：1 到 128 个字符，不含控制字符。名称以小写字母或数字开头，其后是小写字母、数字、连字符和下划线，总共最长 63 个字符。 | 保持原样。区分大小写。 |

一台设备最多有 8 个别名，每个方案一个值；同一租户内，一个方案和值只能属于一台设备。DevEUI 用小写或带空格书写都可以：存储时和消息到达时都会规范化。不是 16 位十六进制数字的值会被拒绝。

设置别名的方式：

- 在控制台，使用设备表单的 **Aliases**（别名）字段：选择 **Add alias**，选择 **LoRaWAN DevEUI**、**Network server device id** 或 **Custom scheme**，然后输入值；
- 通过 GraphQL，在 `createDevice` 或 `updateDevice` 上使用 `aliases: [{scheme: "lorawan.deveui", value: "70B3D57ED0012345"}]`。更新时，你发送的列表会替换设备的别名，空列表会将其全部移除；
- 在 `importDevices` 中，使用同一个 `aliases` 字段。

```graphql
mutation {
  createDevice(request: {
    token: "boiler-room-3"
    deviceTypeToken: "lora-sensor"
    aliases: [{ scheme: "lorawan.deveui", value: "70b3d57ed0012345" }]
  }) { token aliases { scheme value } }
}
```

要按别名查找设备，请给 `devices` 传入条件 `alias: {scheme: "lorawan.deveui", value: "..."}`。

集成的 **Device identifier** 决定查找哪个方案：`DEV_EUI` 读取 `lorawan.deveui`，`DEVICE_ID` 读取 `tts.device-id`，`ALIAS:custom.serial` 读取 `custom.serial`。只带有其他方案的设备不会被找到。

**更改或移除设备的标识符。** 把标识符（DevEUI、设备 ID）移到另一台设备，或将其移除，约五分钟内生效。在此之前，集成端点仍可能为该标识符指向原来的设备，但平台会在处理事件时检查该设备是否仍持有该标识符，因此这些上行会被拒绝，并保留在死信存储中供检查（原因为 `IntegrationAliasMismatch`，由 `devicechain_devicemanagement_resolve_integration_alias_mismatch_total` 计数）。它们不会存入任何设备，也无法重放。变更传播完成后，上行会落到现在持有该标识符的设备上。为一个已经在到达的标识符创建设备后，一分钟内就会开始接收。

## ChirpStack v4 {#chirpstack}

ChirpStack 通过应用上的 **HTTP 集成**发送上行。

1. 在控制台中创建发送方类型为 **ChirpStack v4** 的集成，选择设备类型，需要时开启链路指标。复制 bearer 令牌。
2. 在 ChirpStack 中打开应用，添加 **HTTP** 集成并填写：

   | ChirpStack 字段 | 值 |
   | --- | --- |
   | Payload encoding | **JSON**。Protobuf 集成会被以 `415` 拒绝，消息中会指明这项设置。 |
   | Event endpoint URL(s) | `https://<host>/api/integrations/{tenant}/{integration}`。ChirpStack 会自行追加 `?event=up`。 |
   | Headers | 名称 `Authorization`，值 `Bearer <bearer-token>`。 |

3. **在 ChirpStack 设备配置文件上设置编解码器**（JavaScript 载荷编解码器）。平台存储的是网络服务器解码出的值，不会自己解码原始帧。没有编解码器时，ChirpStack 发送原始帧而没有解码对象，该上行会被[计为未解码](#undecoded)，而不会被存储。或者，给设备类型配置一个[载荷解码器](./payload-decoders.md)，由 DeviceChain 读取原始帧。
4. 在 DeviceChain 中添加设备，并把它的 DevEUI 设为 `lorawan.deveui` 别名。

上行会被存储为读数。ChirpStack 还会把 `ack` 和 `txack` 事件发送到同一个 URL：启用了 [LoRaWAN 下行](#lorawan-downlink)的安装会读取它们来结束[确认下行](#lorawan-confirmed)；未启用时它们与其他事件（join、status）一样返回 `204` 并被不加读取地忽略，无论正文内容如何。上行的 `deduplicationId` 必须是 UUID（ChirpStack 一直如此发送）；其他值会被以 `400` 拒绝。消息时间是 ChirpStack 报告的 `time`；该字段缺失时，则是平台收到消息的时间。

## The Things Stack v3 {#the-things-stack}

The Things Stack 通过应用上的 **webhook** 发送上行。

1. 在控制台中创建发送方类型为 **The Things Stack v3** 的集成。在 **Device identifier** 下选择 **DevEUI**；如果希望匹配 The Things Stack 中的设备 ID，则选择 **Device id**。复制 bearer 令牌。
2. 在 The Things Stack 中打开应用，添加 **Custom webhook**，格式设为 **JSON**，然后填写：

   | The Things Stack 字段 | 值 |
   | --- | --- |
   | Base URL | `https://<host>/api/integrations/{tenant}/{integration}` |
   | Additional headers | 名称 `Authorization`，值 `Bearer <bearer-token>`。 |
   | Enabled event types | Uplink message，启用，路径为 `/uplink-message`。平台也会读取路径 `/up`、`/uplink` 和 `/uplinks`。 |

3. **在应用或设备上设置载荷格式化器（payload formatter）**（LoRaWAN Device Repository 为许多传感器提供了现成的格式化器）。平台存储格式化器的输出，不会自己解码原始帧。没有格式化器时，该上行会被[计为未解码](#undecoded)。或者，给设备类型配置一个[载荷解码器](./payload-decoders.md)，由 DeviceChain 读取原始帧。
4. 在 DeviceChain 中添加设备，把 DevEUI 设为 `lorawan.deveui` 别名；如果选择了 **Device id**，则把设备 ID 设为 `tts.device-id` 别名。

发送到其他任何路径的消息（join accept、下行确认）会返回 `204` 并被忽略，因此启用其他消息类型不需要在 DeviceChain 中改动任何内容，但也没有必要这样做。消息时间是上行的 `received_at`，它必须存在，且不得早于 366 天前。去重键由设备、帧计数器和该时间共同组成，因为设备重新入网时计数器会重置。

## 通用 JSON {#generic-json}

通用 JSON 集成适用于任何能发送 JSON 的系统：网关厂商的云、中间件、脚本。你告诉平台各部分在哪里。创建集成时选择发送方类型 **Generic JSON**，把 **Device identifier** 设为标识符所属的别名方案（例如 `ALIAS:custom.serial`），然后填写设置。通过 GraphQL 时，它们是 `generic` 字段，即字符串中的 JSON 文档：

| 设置 | 含义 |
| --- | --- |
| `idFrom` | 必填。设备标识符在哪里：`pointer:/device/id`（指向每个条目的 [JSON 指针](https://datatracker.ietf.org/doc/html/rfc6901)）、`header:X-Device-Id`（请求头）或 `query:device`（查询参数）。请求头或查询参数中的标识符适用于该请求的所有条目。 |
| `itemsPointer` | 可选。指向正文中某个数组的指针；每个元素是一条上行。不设置时，正文就是一条上行，或是由上行组成的裸数组。 |
| `timePointer` | 可选。指向发送方记录的时间，可以是 RFC 3339 字符串，或以秒或毫秒为单位的纪元数字。不设置时使用接收时间。 |
| `framePointer`、`frameEncoding` | 可选。已编码原始帧所在位置（`HEX` 或 `BASE64`）。它只会被采样，帮助你查看未解码的上行包含什么。 |

条目中除标识符和时间之外的每个键都是候选读数。例如，`idFrom` 为 `pointer:/serial`，`itemsPointer` 为 `/readings`，`timePointer` 为 `/ts`：

```bash
curl -X POST https://dc.example.com/api/integrations/acme/vendor-cloud \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"readings": [
        {"serial": "A-100", "ts": "2026-10-27T09:00:00Z", "temperature": 21.4, "battery": {"voltage": 3.1}},
        {"serial": "A-101", "ts": "2026-10-27T09:00:00Z", "temperature": 19.8, "door": false}
      ]}'
```

一个数组最多包含 100 个条目。无法读取的正文，或没有标识符的条目，会让整个请求以 `400` 被拒绝，且不存储任何内容。租户没有的设备所对应的条目会逐个跳过，并列为[未注册](#unregistered)，其余条目照常存储。

:::warning 通用发送方的重试，在没有发送方时间的情况下会被存储两次
没有 `timePointer` 时，平台用接收时间标记条目，因此再次发送的同一条目看起来像新条目。把 `timePointer` 设为发送方每条读数只填写一次的字段，重试就会带有相同的时间，只存储一次。参见[重复与重试](#duplicates)。
:::

## 通用二进制 {#generic-binary}

通用二进制集成接受原始字节正文，标识符放在请求头或查询参数中（`idFrom` 为 `header:` 或 `query:`；二进制正文没有可指向的字段）。如果设备类型上没有[载荷解码器](./payload-decoders.md)，平台就无法读取这些字节，因此每条这类上行都会被接受并[计为未解码](#undecoded)，其前 512 字节作为样本保存在集成上。用它来了解发送方发来了什么。设备类型上有解码器时，这些字节会被解码为读数。

## 存储的内容 {#what-is-stored}

每条消息携带的解码对象（ChirpStack 的 `object`、The Things Stack 的 `decoded_payload`、通用条目）会被展平为该设备的测量。一条消息变成一个包含以下读数的事件：

- 每个数字都是一条读数，键为用点连接的路径：`{"temperature": 21.4, "battery": {"voltage": 3.1}, "sensors": [{"value": 7}]}` 会存储 `temperature`、`battery.voltage` 和 `sensors.0.value`。
- 布尔值存储为：真为 `1`，假为 `0`。
- 字符串和 `null` 会被跳过。嵌套超过 32 层的值，以及不是有效测量键的路径（字母、数字、`_`、`.` 和 `-`，最长 128 个字符，不得以 `.` 或 `-` 开头）也会被跳过。不会为了适配而重命名任何键。
- 声明同名键的指标，即可为读数提供单位和校验。没有声明指标的读数存储时没有单位。
- 发送方提供时间时，消息时间取发送方的时间，否则取平台的接收时间。
- 展平后超过 256 条读数的消息会以 `400` 整体拒绝，而不会被截断。

开启 **Store link metrics** 后，每条上行还会根据网络服务器的接收报告存储另外三条读数：`lorawan.rssi` 和 `lorawan.snr`（最强的一次接收），以及 `lorawan.gateways`（收到该上行的网关数量）。这些名称归平台所有；同名的解码值会被跳过。

### 没有可存储内容的上行 {#undecoded}

不带解码对象、或其中没有任何数值或布尔值的上行会被确认，但不存储任何内容。集成会把它计为**未解码**，并在其页面显示计数、最近一条的到达时间，以及十六进制的原始帧样本。常见原因是网络服务器中的设备配置文件没有编解码器。当大部分流量都是这种情况时，`IntegrationUndecodedHigh` 告警会通知运营人员；参见[可观测性](../deployment/observability.md)。

## 重复与重试 {#duplicates}

网络服务器若没有及时收到应答，会再次发送消息，网关也可能把同一条上行投递两次。每条消息都带有一个键，平台随事件一起存储；如果某设备在该时间已经存储了相同键的事件，就不会再次存储。

| 发送方 | 键 |
| --- | --- |
| ChirpStack v4 | `deduplicationId`。 |
| The Things Stack v3 | 设备的 DevEUI（当集成按 `DEVICE_ID` 识别设备且消息不带 DevEUI 时，用设备 ID）、帧计数器和 `received_at`。 |
| 通用，带 `timePointer` | 设备与条目的摘要，加上发送方时间。重试带有相同的条目和时间，因此只存储一次。 |
| 通用，不带 `timePointer` | 设备与条目的摘要，加上接收时间。重试会得到新的键，因此会被**存储两次**。 |

平台还会在处理之前，丢弃在第一条消息之后十分钟内到达、带有相同键的重复消息；不带 `timePointer` 的通用发送方每次发送都会得到新的键，因此这一条对它不起作用。键最长 192 字节，更长的键会被以 `400` 拒绝。两条恰好数值相同的不同上行不会被合并。

## 未注册设备 {#unregistered}

当消息中的标识符不属于集成范围内的任何设备时，平台不会存储它，而是把该标识符记录在集成上，让你看到有哪些尚未添加的设备在向你发送数据。在控制台打开集成，查看 **Unregistered devices seen**（已见的未注册设备）：每行显示方案、标识符、出现次数和时间。**Create a device with this alias**（用此别名创建设备）会打开设备表单，并已填好别名；只有当集成的范围恰好是一个设备类型时，才会同时填好类型。通过 GraphQL，该列表是 `unregisteredIntegrationDevices(criteria)`。

- 每个集成的列表最多保存 1,000 个标识符。超出后，新的标识符只计数，不再列出。
- 每个标识符在每个 `event-sources` 副本上最多每分钟记录一次，因此计数反映的是活跃程度，而不是精确的消息数量。
- 已被集成范围之外的设备持有的标识符不会被列出，因此列表绝不会显示设备已经持有的值。
- **Clear list**（`clearUnregisteredIntegrationDevices(integration)`）会清空列表。它只是观察记录，发送方仍在发送的标识符会再次出现。

## 发送方会收到什么 {#responses}

网络服务器只能看到状态码，有些服务器在 webhook 被拒绝过多次后会将其暂停。该路由的设计就考虑了这一点。

| 应答 | 含义 |
| --- | --- |
| `202` | 已接受。包括未注册设备的消息（会被跳过），以及因租户超出接入额度而被[丢弃](#limits)的消息。 |
| `204` | 不是上行的消息类型（join、status、确认）。已确认并忽略。 |
| `400` | 正文不是集成的发送方类型所期望的格式：JSON 无效、标识符缺失或格式错误、`deduplicationId` 不是 UUID、时间早于 366 天前、超过 100 个条目，或超过 256 条读数。请求中的任何内容都不会被存储。 |
| `401` | 令牌缺失、错误，或属于另一个集成。令牌错误和集成不存在有意让人无法区分。 |
| `403` | 集成已关闭。这不计为登录失败。 |
| `405` | 方法不是 `POST`。 |
| `413` | 正文超过 256 KiB。 |
| `415` | ChirpStack 正在发送 Protobuf。请把它的载荷编码改为 JSON。 |
| `429` | 该集成的登录失败次数过多，同一地址发出的、其令牌平台最近没有验证过的请求过多，或者在单个副本上，针对从未验证过的集成的请求过多。请在 `Retry-After` 指示的时间后重试。 |
| `503` | 平台目前无法校验令牌或接收消息，或正在施加背压。请重试；带有 `Retry-After` 头表示平台正在施加背压：单条上行没有被存储，但在通用数组中，被拒绝之前的条目可能已被存储，因此请给通用发送方设置 `timePointer`，让重试是安全的。 |

对集成的更改很快，但在端点处并非瞬时生效，端点会把它了解到的集成信息缓存最多一分钟（device-management 不可用期间最多 15 分钟）。轮换令牌或关闭集成之后，端点仍可能对继续使用旧令牌的发送方返回 `202`，最长一分钟，但这一分钟内发送的内容都不会被存储。重新开启集成之后，端点仍可能返回 `403`，最长一分钟。

## 限制 {#limits}

| 限制 | 值 |
| --- | --- |
| 请求正文 | 256 KiB |
| 解码对象，或一个通用条目 | 64 KiB，嵌套最多 32 层 |
| 通用数组中的条目 | 100 |
| 一条上行中的读数 | 256 |
| 发送方所报告时间的最大年龄 | 366 天 |
| 每租户集成数 | 默认 5，由层级设定，最多 100 |
| 设备上的别名 | 8，每个方案一个值 |
| 每个集成列出的未注册标识符 | 1,000 |

**租户的接入额度。**集成流量按租户的接入上限，在其自己的额度中计量，与其他传输一样以读数计，并且独立于 MQTT 和 HTTP 接入所消耗的额度。这些额度相互独立，因此同时通过多条路径发送的租户，在每个 `event-sources` 副本上总共可以获准超过一个上限；参见[接入何时可以超过租户上限](../concepts/governance.md#ingest-above-ceiling)。包含 N 条上行的数组请求按 N 次计费，因此大数组不会换来更低的费率。

**超出额度时，应答是 `202`，而不是 `429`。**拒绝反正也会丢失消息，而且有些网络服务器会暂停被拒绝的 webhook，进而丢弃本来在额度之内的流量。其后果是发送方以为数据已送达，实际上并没有。出现这种情况时，`IntegrationShedAccepted` 告警会通知运营人员；请调高租户上限，或减少集成发送的数据量。

**对未认证流量的限制。**知道集成 URL 的人无法借此耗尽租户的额度，或影响集成的正常流量：错误令牌按集成限制（突发 30 次，之后每秒 1 次），平台在最近一分钟内未验证过其令牌的请求按来源地址限制（突发 60 次，之后每秒 5 次），对从未验证过的集成名称则按副本限制（突发 100 次，之后每秒 20 次）。`ingress-nginx` 前端还将每个地址限制为每秒 200 个请求，超出时返回 `503`；只有在控制器被告知信任负载均衡器转发的地址时，它才按客户端限制，Gateway API 前端没有这项限制。网络服务器使用有效令牌发送的稳定流量，在平台自身的限制下，每分钟大约只占其地址一个请求。失败持续十分钟时，`IntegrationAuthFailuresSustained` 告警触发；仍在使用已轮换令牌的发送方就是这种表现。

## 发给集成设备的命令 {#commands}

通过集成上报的设备没有到 DeviceChain 的连接，无法向其下发命令。除非你的安装启用了 [LoRaWAN 下行](#lorawan-downlink)，否则发给这类设备的命令**在你下发时会被接受**，然后在**下一次投递扫描时失败**，错误为 `COMMAND_TRANSPORT_UNSUPPORTED`，因为平台识别该传输，却无法通过它投递。它不会一直排队到过期。

有两种情况值得了解：

- 同时通过 MQTT 连接的设备，按它最近一次上报所用的传输来判断。最近一次通过集成上报时，其命令会失败；改为通过 MQTT 上报之后，命令即可投递。
- 从未上报过的设备没有记录的传输，因此它的命令会照常下发，并因无人应答而过期，直到它首次上报为止。

对必须接收命令、且不在 ChirpStack v4 网络服务器后面的设备，请同时为它配置 MQTT 连接。参见[传输能力矩阵](../reference/transport-matrix.md)。

## LoRaWAN 下行（ChirpStack v4）{#lorawan-downlink}

当安装启用了出站连接器的下行（`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled: true`；此时 Helm chart 会让命令投递服务派发这些命令，并拒绝手工设置的该标志副本），向 ChirpStack v4 网络服务器后面的设备发送命令时，会在该服务器上排入一条下行。未启用的安装不受任何影响。

**1. 把集成关联到它的网络服务器。** 创建并发布一个类型为 `lorawan_downlink` 的连接器：

```json
{
  "networkServer": "chirpstack_v4",
  "baseUrl": "https://chirpstack.example.com",
  "integration": "my-integration-token"
}
```

`integration` 是该服务器所服务设备所属的入站集成的令牌。连接器的**密钥**是一个允许排入下行的 ChirpStack API 密钥（ChirpStack v4 的 REST API，由其 `chirpstack-rest-api` 组件提供）；它加密存储，之后不再显示。同一集成只能被一个已发布的连接器指向：没有或有两个时，命令会被拒绝而不是发出。与所有连接器一样，对 `baseUrl` 的调用要经过平台的出站策略，因此位于私有地址的服务器需要运维人员放行。

**2. 定义命令。** 在设备类型的配置中添加一个带有以下参数的命令：

| 参数 | 含义 |
| --- | --- |
| `fPort` | LoRaWAN 应用端口，1 到 223。必填。 |
| `hex` 或 `base64` | 应用负载，十六进制或带填充的标准 base64。二者给出且仅给出一个。最多 242 字节。 |
| `confirmed` | 可选，默认 `false`。`true` 要求设备应答该帧，并让命令一直等到应答为止（见[确认下行](#lorawan-confirmed)）。 |

任何其他参数名都会被拒绝。

**3. 发送。** 设备必须带有 `lorawan.deveui` [别名](#aliases)：DevEUI 取自设备，绝不取自命令。命令经由设备**最近一次上报所用**的集成发出。

**结果的含义。** 对未确认的下行，`SUCCEEDED` 表示网络服务器已**排入**该下行（结果带有 `{"stage":"queued","queueItemId":...}`）。这并不表示设备已收到：A 类设备只在下一次上行之后才会收听下行，而未确认的下行不会得到应答。对[确认下行](#lorawan-confirmed)，`SUCCEEDED` 表示设备已应答。其他结果：

| 发生了什么 | 结果 |
| --- | --- |
| 参数无效、设备没有 DevEUI、集成没有关联（或有两个）、命令已过期，或租户超出出站调用上限 | `FAILED`，未投递。可以放心重发。 |
| 网络服务器拒绝了它（4xx：API 密钥错误、设备未知、负载对设备当前数据速率过大），或在发出任何内容之前无法连接 | `FAILED`，未投递。原因会指明服务器的回应。 |
| 请求已发出，但服务器出错或没有应答（5xx、超时） | 结果**未知**：下行可能已入队，再次发送可能会发出两次。 |

按结果查看 `lpwan_downlinks_total` 指标，可以看到命令在哪一步停下。

### 确认下行 {#lorawan-confirmed}

在命令中设置 `"confirmed": true`，网络服务器会要求设备应答该帧。下行排入队列时命令**不会**得到回答：它保持 `SENT`，直到网络服务器报告结果，然后依据这些证据结束：

| 网络服务器报告 | 命令 |
| --- | --- |
| 设备应答了该帧（`ack`，已应答） | `SUCCEEDED`。结果带有 `{"stage":"acknowledged",...}`。 |
| 网络服务器不再等待应答（`ack`，未应答） | `TIMEOUT`，结果未知。设备可能已收到该帧而应答丢失，再次发送可能会动作两次。 |
| 网关接受了该帧用于发射（`txack`） | 记录下来，命令继续等待。网关发出帧不等于设备收到。 |
| 截止时间前没有任何报告 | `TIMEOUT`，结果未知。截止时间是命令自身的过期时间；没有过期时间时为 24 小时。 |

只有应用的 HTTP 集成被设置为发送 `ack` 事件时，ChirpStack 才会报告应答，而且它必须是你在 DeviceChain 中登记的那个集成（上文的 URL 和令牌）：报告通过集成自己的令牌读取，因此只能结束经该集成发出的、发往登记在它名下设备的命令。如果希望记录发射情况，也请启用 `txack` 事件；它是可选的。

与任何等待中的下行都不匹配的报告绝不会靠猜测去匹配。它会被丢弃、写入日志，并计入 `lpwan_ack_reports_total`（没有等待该队列项时为 `unmatched`，指向另一个集成或设备时为 `mismatch`）；这两项都值得告警。每条下行（无论是否确认）都会产生 `txack`，所以针对未在等待的下行的 `txack` 计为 `tx_untracked`，属于正常情况。滚动升级期间，旧版本的 Pod 仍会把确认命令按未投递拒绝，可以放心重发。

按结果查看 `lpwan_ack_reports_total`：`acknowledged`、`timed_out`、`transmitted` 和 `deadline` 即上述结果。偶尔出现一次 `unmatched` 并非故障：如果报告已被回答、但消息在 broker 上的确认丢失，该报告会再次到达并发现没有任何等待项（刚刚结束它的副本会把它计为 `redelivered`）。请对持续出现的 `unmatched` 或任何 `mismatch` 告警。

只携带 MAC 命令的下行的 `txack` 没有队列项，会返回 `204` 并被计数（`integration_downlink_reports_total`，结果 `untracked`）；没有队列项的 `ack` 会被以 `400` 拒绝。报告有自己的限额，与遥测分开：每个租户突发 100 条、持续每秒 20 条。超出后请求返回 `202` 并被计数（结果 `rate_limited`），它本可结束的命令在截止时间结束，因此一个租户的洪泛不会挤掉另一个租户的应答。

**升级。** 确认下行需要出站连接器和事件源两者都是新版本。出站连接器已是新版本而事件源仍是旧版本期间，旧 Pod 会对网络服务器的报告返回 `204` 并丢弃，因此在这一窗口内发出的确认下行会在截止时间以 `TIMEOUT`（结果未知）结束。Chart 在同一次升级中滚动二者；要避开这一窗口，请在其持续期间暂停确认命令。

**在运行中的安装上启用。** 下行通过一次 Helm 升级启用；在 Pod 滚动期间，尚未重启的命令投递 Pod 仍会像下行出现之前那样，立即以 `COMMAND_TRANSPORT_UNSUPPORTED` 使发往集成设备的命令失败。这样的命令不会发出任何内容，升级结束后可以放心重新下发。要避开这一窗口，请在升级期间暂停发往集成设备的命令。适配器首次启动前几分钟内下发的命令，在适配器启动时仍会被接收。

## 集成令牌能做什么 {#trust}

请把 bearer 令牌当作整个设备群的密码。**集成令牌代表其范围内设备类型中所有带别名的设备发言。**平台会校验令牌属于该集成，并校验设备类型在集成范围内，事件被处理时还会再校验一次，但它无法判断发送方对消息来自哪台设备的说法是否诚实。持有该令牌的人可以冒充范围内任何带别名的设备提交读数。

因此：

- 把集成的范围限定在它需要的设备类型上，并且每个网络服务器或每个设备群使用一个集成，而不是一个集成管所有。
- 令牌可能已泄露时，或知道令牌的人离开时，请轮换令牌。
- 令牌仅以带密钥的摘要形式存储，之后无论是控制台还是 GraphQL 都不会再次显示。管理员同样无法读回。
- 集成事件会标记其所经过的集成，设备无法伪造这一点：设备无法通过 MQTT 或 HTTP 发送声称来自某个集成的事件。
- Integrations 页面会显示请求最近一次认证失败的时间，并标记被拒绝的令牌。

参见[安全模型](../concepts/security-model.md)。

## 第 1 版不包含 {#not-supported}

- **面向 The Things Stack 与通用发送方的下行以及负载编码器。** [LoRaWAN 下行](#lorawan-downlink)只覆盖 ChirpStack v4 和原始负载。发给其他集成设备的命令按[发给集成设备的命令](#commands)所述失败。
- **签名载荷。**请求上没有 HMAC 或签名校验。bearer 令牌是唯一的凭据，传输必须使用 TLS。
- **Join、status 和位置消息。**它们会被确认并忽略。
- **自动创建设备。**未知标识符只会被列出，不会被创建。你需要自己根据列表添加设备。
- **没有解码器时解码通用二进制正文。**它会被接受并计数，但不会被解码，除非设备类型上有[载荷解码器](./payload-decoders.md)。

## 故障排查 {#troubleshooting}

| 现象 | 可能原因 |
| --- | --- |
| 发送方报告 `401` | 令牌错误或已被轮换；URL 中的集成令牌与 bearer 令牌所属的集成不匹配；或租户段有误。集成会显示 **Rejected token**（令牌被拒绝）。 |
| 发送方报告 `403` | 集成已关闭（**Accepting data**）。 |
| 发送方报告 `415` | ChirpStack 的载荷编码为 Protobuf。请改为 JSON。 |
| 发送方报告 `400` | 查看应答正文，其中会指明字段。常见原因：DevEUI 不是 16 位十六进制数字、`deduplicationId` 不是 UUID，以及时间早于 366 天前。 |
| 发送方报告 `202`，但没有存储任何内容 | 设备没有匹配的别名，或其类型超出集成范围：请查看 **Unregistered devices seen**。或者该上行没有可存储的内容：请查看未解码计数。或者租户超出了额度：参见 `IntegrationShedAccepted`。 |
| **Uplinks with nothing to read**（无内容可读的上行）持续增加 | 网络服务器中的设备配置文件没有编解码器或载荷格式化器，或其输出只有字符串。 |
| 值存储后没有单位 | 设备配置文件中没有为该键声明[指标](../user-guide/device-types-and-profiles.md#metrics)。键是用点连接的路径（`battery.voltage`）。 |
| 托管的网络服务器无法连接 | 实例使用的是自签名证书。请使用公共 CA 签发的证书。 |
| 所有发送方共用同一个速率限制 | `event-sources` 前面的代理不受信任。参见[客户端地址与受信任代理](../deployment/bootstrap.md#trusted-proxies)。 |

## 相关内容 {#related}

- [从 LoRaWAN 上行到告警](./lorawan-uplink-to-alarm.md)：从二进制上行出发，经过集成和解码器，直到检测规则和告警的完整演练。
- [载荷解码器](./payload-decoders.md)：把原始帧或网络服务器解码出的对象转换成测量。
- [设备](../user-guide/devices.md)：设备表单及其 **Aliases** 字段。
- [连接设备](./connecting-a-device.md)：设备自行使用的 MQTT 和 HTTP 路径。
- [传输能力矩阵](../reference/transport-matrix.md)：每种接入方式支持和不支持什么。
- [GraphQL API 参考](../reference/graphql-api.md#integrations)：集成相关操作。
