---
title: 将 ChirpStack 上行发送到 DeviceChain
description: 通过 ChirpStack v4 的 HTTP 集成把应用连接到 DeviceChain，并选择由 ChirpStack 编解码器或 DeviceChain 中的载荷解码器来解码上行。
---

# 将 ChirpStack 上行发送到 DeviceChain {#send-chirpstack-uplinks}

ChirpStack v4 通过应用上的 **HTTP 集成**转发网关收到的数据：它把每个事件以 JSON 形式发布到你指定的 URL。DeviceChain 用发送方类型为 **ChirpStack v4** 的[入站集成](./inbound-integrations.md)接收这些请求，按 DevEUI 找到设备，并把读数存储为该设备的测量。此后它们与其他测量一样，可以驱动仪表板、检测规则和告警。

ChirpStack v4 发送方类型直接读取 ChirpStack 自己的事件格式。你不需要编写适配器，解码器也永远不必拆开 ChirpStack 的外层结构：平台从 `deviceInfo.devEui` 取得 DevEUI，从 `time` 取得时间，从 `object` 取得已解码对象，从 `data` 取得原始帧，从 `fPort` 和 `fCnt` 取得端口和计数器。

剩下要决定的是**在哪里解码帧**：

| | 方案 A：由 ChirpStack 解码 | 方案 B：由 DeviceChain 解码 |
| --- | --- | --- |
| 字节布局写在哪里 | ChirpStack 设备配置文件上的 JavaScript 编解码器 | DeviceChain 设备配置文件上的[载荷解码器](./payload-decoders.md) |
| DeviceChain 存储什么 | ChirpStack `object` 中的数字和布尔值 | 解码器从 `data` 生成的结果 |
| 版本与测试 | 由 ChirpStack 管理 | 随配置文件一起发布，保存的测试向量必须通过才能发布 |

两种方案使用同一个集成和同样的 ChirpStack 设置。下面的步骤在[第 5 步](#5-decoding)之前完全相同，到那一步再选择其一。

## 开始之前 {#before-you-start}

- 一台 ChirpStack v4 服务器，包含一个应用、一个设备配置文件以及已经入网的设备。你需要每台设备的 DevEUI。
- 一个 ChirpStack 能通过 HTTPS 访问的 DeviceChain 实例。你自己运行的 ChirpStack 可以导入实例的 CA；托管的 ChirpStack 则要求实例使用公共 CA 签发的证书。
- 一个拥有 `device:write` 权限的租户成员。控制台步骤使用租户控制台；每一步也有 GraphQL 写法，发送到 `https://<host>/api/device-management/graphql`，并在 `Authorization: Bearer` 请求头中携带租户访问令牌（[如何获取](./lorawan-uplink-to-alarm.md#1-get-a-tenant-token)）。

## 1. 为传感器准备设备类型 {#1-device-type}

集成只能访问你为它限定的设备类型中的设备，所以先准备一个设备类型及其配置文件。为传感器上报的每个值声明一个[指标](../user-guide/device-types-and-profiles.md#metrics)，键名与解码后的值一致：指标为读数提供单位和校验。本指南使用一个房间传感器，它上报温度、湿度、以毫伏为单位的电池电压和门磁触点：

| 指标键 | 数据类型 | 单位 |
| --- | --- | --- |
| `temperature` | Double | `Cel` |
| `humidity` | Double | `%` |
| `battery` | Integer | `mV` |
| `door` | Boolean | |

在控制台中，按照[设备类型与配置文件](../user-guide/device-types-and-profiles.md)创建配置文件和指标，再创建一个使用该配置文件的设备类型，然后**发布**配置文件。使用 GraphQL 时需分两次请求发送，因为一个 mutation 最多只能选择[五个根字段](../reference/graphql-api.md#request-limits)，多出一个就会导致整个请求被拒绝：

```graphql
mutation {
  createDeviceProfile(request: { token: "room-sensor", name: "Room sensor", category: "sensor" }) { token }
  temperature: createMetricDefinition(request: { token: "room-sensor-temperature", deviceProfileToken: "room-sensor", metricKey: "temperature", name: "Temperature", dataType: "DOUBLE", unit: "Cel" }) { metricKey }
  humidity: createMetricDefinition(request: { token: "room-sensor-humidity", deviceProfileToken: "room-sensor", metricKey: "humidity", name: "Humidity", dataType: "DOUBLE", unit: "%" }) { metricKey }
  battery: createMetricDefinition(request: { token: "room-sensor-battery", deviceProfileToken: "room-sensor", metricKey: "battery", name: "Battery", dataType: "INT", unit: "mV" }) { metricKey }
  door: createMetricDefinition(request: { token: "room-sensor-door", deviceProfileToken: "room-sensor", metricKey: "door", name: "Door", dataType: "BOOLEAN" }) { metricKey }
}
```

```graphql
mutation {
  createDeviceType(request: { token: "room-sensor", name: "Room sensor", profileToken: "room-sensor" }) { token }
}
```

如果选择[方案 B](#option-b-decoder)，请在发布前先添加解码器；否则现在就发布：

```graphql
mutation { publishDeviceProfile(token: "room-sensor", label: "metrics") { version } }
```

没有对应指标的解码键仍会被存储，只是没有单位。

## 2. 创建集成 {#2-integration}

在控制台中打开 **Connect → Integrations**（集成），选择 **New integration**（新建集成）：

- **Sender type**（发送方类型）：**ChirpStack v4**。创建后不能更改。
- **Token**（令牌）：集成在 URL 中的名称，例如 `chirpstack-eu868`。创建后不能更改。
- **Device types**（设备类型）：`Room sensor`。
- **Device identifier**（设备标识符）：**DevEUI**。
- **Store link metrics**（存储链路指标）：如果希望在每条上行的读数旁存储信号强度、信噪比和网关数量（`lorawan.rssi`、`lorawan.snr` 和 `lorawan.gateways`），请开启。

选择 **Create integration**（创建集成）。**Integration created** 对话框只显示一次 **bearer 令牌**。请立即复制：它只以摘要形式存储，无法再次显示。如果丢失，**Rotate token**（轮换令牌）会签发新令牌，旧令牌立即失效。

使用 GraphQL 时，`createIntegration` 在 `issuedToken` 中返回令牌：

```graphql
mutation {
  createIntegration(request: {
    token: "chirpstack-eu868"
    name: "ChirpStack EU868"
    type: "CHIRPSTACK_V4"
    enabled: true
    deviceTypeTokens: ["room-sensor"]
    identity: "DEV_EUI"
    linkMetrics: true
  }) {
    integration { token type identity }
    issuedToken
  }
}
```

集成的地址是：

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` 是你打开控制台所用的主机，`<tenant>` 是租户令牌，`<integration>` 是集成的令牌。控制台中集成页面的 **Connect the sender**（连接发送方）会显示完整 URL，并附带复制按钮。

## 3. 在 ChirpStack 中添加 HTTP 集成 {#3-chirpstack-http}

在 ChirpStack 网页界面中打开应用，进入 **Integrations**，添加 **HTTP** 集成：

| ChirpStack 设置 | 值 |
| --- | --- |
| Payload encoding | **JSON**。DeviceChain 会以 `415` 拒绝 Protobuf 编码。 |
| Event endpoint URL(s) | `https://<host>/api/integrations/<tenant>/<integration>`，后面不加任何内容。ChirpStack 会自动在每个请求后加上 `?event=up`（或其他事件名）。 |
| Headers | 一个请求头：名称 `Authorization`，值 `Bearer <bearer-token>`。 |

保存后，ChirpStack 会把该应用中设备的每个事件发布到 DeviceChain。只有 `up` 事件会被存储。`join`、`status`、`log` 等其他事件会得到 `204` 响应并被忽略；`ack` 和 `txack` 只有在运维人员开启 [LoRaWAN 下行](./inbound-integrations.md#lorawan-downlink)后才会被读取。因此可以保留所有事件处于启用状态。

令牌只放在请求头中。不要把它写进 URL，否则会被记录到访问日志。

## 4. 按 DevEUI 注册设备 {#4-devices}

DeviceChain 通过保存 DevEUI 的设备**别名**找到上行所属的设备。为每个传感器创建一台第 1 步中类型的设备，并添加别名 `lorawan.deveui`。在控制台中，使用设备表单的 **Aliases**（别名）字段：**Add alias**（添加别名）、**LoRaWAN DevEUI**，再填写 16 位十六进制数字，大小写均可。使用 GraphQL：

```graphql
mutation {
  createDevice(request: {
    token: "room-4"
    name: "Room 4"
    deviceTypeToken: "room-sensor"
    aliases: [{ scheme: "lorawan.deveui", value: "70B3D57ED0050004" }]
  }) { token aliases { scheme value } }
}
```

也可以让传感器自己“报到”。如果上行的 DevEUI 不属于集成设备类型中的任何设备，它不会被存储，而是列在集成页面的 **Unregistered devices seen**（已见的未注册设备）中。**Create a device with this alias**（用此别名创建设备）会打开已填好 DevEUI 的设备表单。参见[未注册设备](./inbound-integrations.md#unregistered)。

## 5. 选择在哪里解码帧 {#5-decoding}

### 方案 A：由 ChirpStack 编解码器解码 {#option-a-codec}

在 ChirpStack 中打开传感器使用的设备配置文件，进入 **Codec** 选项卡，选择 JavaScript 函数作为载荷编解码器，并编写 `decodeUplink` 函数。ChirpStack 对每条上行运行它，并把返回值中的 `data` 作为上行事件的 `object` 发送。对于本例的房间传感器，其端口 2 上的六字节帧依次为：以十分之一度为单位的有符号温度、以半个百分点为步长的湿度、以毫伏为单位的电池电压，以及最后一个字节第 0 位的门磁触点：

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    throw new Error("unexpected frame on port " + input.fPort);
  }
  var t = (b[0] << 8) | b[1];
  if (t & 0x8000) t -= 0x10000;
  return {
    data: {
      temperature: t / 10,
      humidity: b[2] / 2,
      battery: (b[3] << 8) | b[4],
      door: (b[5] & 1) === 1
    }
  };
}
```

DeviceChain 一侧无需再做任何事：让设备配置文件保持**没有**解码器。DeviceChain 会把 `object` 展平为读数：

- 每个数字都是一条读数，以其键名存储；嵌套值以点连接的路径为键（`{"battery": {"mv": 3300}}` 即 `battery.mv`）；
- `true` 和 `false` 存储为 `1` 和 `0`；
- 字符串和 `null` 会被跳过，因此编解码器返回的 `"status": "ok"` 不会存储任何内容。

让编解码器返回的键与你声明的指标同名，或者按编解码器返回的键声明指标。完整规则见[存储内容](./inbound-integrations.md#what-is-stored)。

如果上行事件没有 `object`，或其中没有任何数字或布尔值，平台会确认收到但不存储任何内容。集成会把它计为**未解码**并保留其帧的样本：设备配置文件没有编解码器时，上行就是这种表现。

:::tip 调整编解码器的输出
如果厂商编解码器返回的键或单位与你的配置文件不同（例如华氏度的 `temp_f`、`bat.mv`），无需修改编解码器。基于 **Decoded object**（已解码对象）输入的 DeviceChain 解码器可以重命名并换算它们；参见[调整已解码对象](./payload-decoders.md#example-tts)。
:::

### 方案 B：由 DeviceChain 解码原始帧 {#option-b-decoder}

为设备配置文件添加[载荷解码器](./payload-decoders.md)后，ChirpStack 就不需要编解码器。每个 `up` 事件都在 `data` 中携带原始帧，集成会把以下内容交给解码器：

| 解码器中的名称 | 来自 ChirpStack 事件 |
| --- | --- |
| `frame` | `data`，即帧的字节 |
| `fPort` | `fPort` |
| `fCnt` | `fCnt` |
| `decoded` | `object`，当 ChirpStack 配置了编解码器时 |
| `link` | `rxInfo` 中最强的一次接收及网关数量，当开启 **Store link metrics** 时 |

在控制台中打开配置文件的草稿，进入 **Decoder**（解码器）选项卡，把输入设为 **Binary frame**（二进制帧），并为 **fPort** 2 添加一个包含四行的分区。下面是它保存的文档；也可以通过 GraphQL 的 `setDeviceProfileDecoder(profileToken: "room-sensor", document: ...)` 设置同一文档：

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "uplink",
      "when": "fPort == 2",
      "outputs": [
        { "key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0" },
        { "key": "humidity", "expr": "double(u8(frame, 2)) / 2.0" },
        { "key": "battery", "expr": "u16be(frame, 3)" },
        { "key": "door", "expr": "bit(frame, 5, 0)" }
      ]
    },
    { "name": "other ports", "ignore": true }
  ],
  "vectors": [
    {
      "name": "room sensor",
      "input": { "frameHex": "00D75A0CE401", "fPort": 2 },
      "expect": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": 1 }
    }
  ]
}
```

第二个分区接受其他任何端口的上行，但不存储任何内容；没有它，这些上行会以 `no_section` 解码失败。向量是保存下来的测试：发布时会运行它，如果解码器产生的读数与之不完全一致，发布会被拒绝。

发布前先试一试解码器。在 **Test**（测试）面板中选择 **Frame**（帧），粘贴 `00D75A0CE401` 并把 `fPort` 设为 2；或者选择 **Webhook body**（Webhook 正文），选中该集成，粘贴一条 ChirpStack 发布的完整事件。然后**发布**配置文件。新发布的解码器大约需要一分钟才会生效。

设备类型有了解码器之后，存储什么由解码器决定。即使 ChirpStack 也配置了编解码器，其 `object` 也不会被存储，只会以 `decoded` 的形式提供给解码器。解码失败时，该上行的任何内容都不会存储，并会记录为原因为 `DECODE_FAILED` 的失败事件；配置文件的 **Decoder** 选项卡会在 **Health**（健康状况）和 **Recent failures**（最近失败）中显示。原因列表见[解码器拒绝原因](./payload-decoders.md#refusal-reasons)。

DeviceChain 解码器不是 JavaScript，而是一组没有循环的类型化表达式，因此厂商的 JavaScript 编解码器不能直接粘贴进来，需要把其中的字节读取改写为解码器行。语言说明见[载荷解码器](./payload-decoders.md)和[函数参考](../reference/decoder-functions.md)。

## 6. 确认上行已到达 {#6-check}

在配置 ChirpStack 之前，你可以用一个与 ChirpStack 请求形状相同的请求测试集成。这个请求同时带有已解码的 `object`（方案 A）和 `data` 中的原始帧（方案 B），所以两种方案都适用；`deduplicationId` 必须是 UUID，且每次都要换新的，否则上行只会存储一次：

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/chirpstack-eu868?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "deduplicationId": "'"$(uuidgen)"'",
    "deviceInfo": { "devEui": "70b3d57ed0050004" },
    "fPort": 2,
    "fCnt": 1,
    "data": "ANdaDOQB",
    "object": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
  }'
```

`202 Accepted` 表示请求已被读取且令牌有效，并不表示已经存储了任何内容。接着在 DeviceChain 中查看：

- 设备页面显示读数：`temperature` 21.5、`humidity` 45、`battery` 3300 和 `door` 1。
- 集成页面在请求认证失败时显示 **Rejected token**（令牌被拒绝），显示没有可存储内容的上行的**未解码**计数，并在 **Unregistered devices seen** 中列出范围内没有任何设备持有的 DevEUI。
- 使用方案 B 时，配置文件的 **Decoder** 选项卡会显示解码成功和失败的上行数量。

ChirpStack 开始发送后，真实上行也会以同样方式出现。ChirpStack 自己的设备页面会显示它转发的 `up` 事件。

## 故障排除 {#troubleshooting}

| 现象 | 可能原因 |
| --- | --- |
| `401` | bearer 令牌错误或已被轮换，或 URL 中的租户令牌或集成令牌错误。集成会显示 **Rejected token**。 |
| `403` | 集成已被关闭。重新开启 **Accepting data**（接收数据）。 |
| `415` | HTTP 集成的载荷编码是 Protobuf。请改为 **JSON**。 |
| `400` | 响应正文会指出出错的字段：DevEUI 不是 16 位十六进制数字、`deduplicationId` 不是 UUID、`time` 早于 366 天之前，或请求没有 `?event=`（端点 URL 的结尾不是 ChirpStack 能追加参数的位置）。 |
| `202`，但没有存储任何内容 | 集成设备类型中没有设备持有该 DevEUI（查看 **Unregistered devices seen**），或该上行没有可存储的内容（查看未解码计数），或在方案 B 中解码失败（查看 **Decoder** 选项卡）。 |
| 读数没有单位 | 配置文件中没有为该键声明指标，或声明后尚未发布配置文件。 |
| ChirpStack 完全无法连接 | ChirpStack 不信任实例的证书。 |

[为什么我的设备数据没有出现](./why-isnt-my-data-showing-up.md)介绍了平台对其拒绝内容的记录，[发送方收到的响应](./inbound-integrations.md#responses)列出了所有响应。

## 后续步骤 {#next-steps}

- 把读数变成告警：[从 LoRaWAN 上行到告警](./lorawan-uplink-to-alarm.md)让同一个传感器继续触发检测规则、告警和命令。
- 通过 ChirpStack 下发命令：[LoRaWAN 下行](./inbound-integrations.md#lorawan-downlink)。
- 了解重复与重试：[重复与重试](./inbound-integrations.md#duplicates)。键是 ChirpStack 的 `deduplicationId`，因此重试的请求只会存储一次。
- 保护好令牌：[集成令牌能做什么](./inbound-integrations.md#trust)。

## 另请参阅 {#see-also}

ChirpStack 一侧设置的官方文档（英文）：

- [HTTP integration](https://www.chirpstack.io/docs/chirpstack/integrations/http.html)：端点、`event` 查询参数和 JSON 编码。
- [Event types](https://www.chirpstack.io/docs/chirpstack/integrations/events.html)：`up` 事件及其字段。
- [Device profiles](https://www.chirpstack.io/docs/chirpstack/use/device-profiles.html)：**Codec** 选项卡和 JavaScript 编解码器函数。
