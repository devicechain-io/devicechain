---
title: 将 The Things Network / The Things Stack 上行发送到 DeviceChain
description: 通过自定义 webhook 把 The Things Stack 应用（无论在 The Things Network 上还是在你自己的部署中）连接到 DeviceChain，并选择由载荷格式化器或 DeviceChain 中的载荷解码器来解码上行。
---

# 将 The Things Network / The Things Stack 上行发送到 DeviceChain {#send-the-things-stack-uplinks}

The Things Stack 是 The Things Network、The Things Stack Cloud 以及自建部署背后的网络服务器，它们转发上行的方式完全相同：通过应用上的 **webhook**，把每条消息以 JSON 形式发布到你指定的 URL。DeviceChain 用发送方类型为 **The Things Stack v3** 的[入站集成](./inbound-integrations.md)接收这些请求，按 DevEUI 或设备 id 找到设备，并把读数存储为该设备的测量。此后它们与其他测量一样，可以驱动仪表板、检测规则和告警。

The Things Stack v3 发送方类型直接读取 The Things Stack 自己的消息格式。你不需要编写适配器，解码器也永远不必拆开 The Things Stack 的外层结构：平台从 `end_device_ids` 取得设备，从 `received_at` 取得时间，从 `uplink_message.decoded_payload` 取得已解码对象，从 `uplink_message.frm_payload` 取得原始帧，从 `f_port` 和 `f_cnt` 取得端口和计数器。

剩下要决定的是**在哪里解码帧**：

| | 方案 A：由 The Things Stack 解码 | 方案 B：由 DeviceChain 解码 |
| --- | --- | --- |
| 字节布局写在哪里 | 应用或终端设备上的载荷格式化器 | DeviceChain 设备配置文件上的[载荷解码器](./payload-decoders.md) |
| DeviceChain 存储什么 | `decoded_payload` 中的数字和布尔值 | 解码器从 `frm_payload` 生成的结果 |
| 版本与测试 | 由 The Things Stack 管理 | 随配置文件一起发布，保存的测试向量必须通过才能发布 |

两种方案使用同一个集成和同一个 webhook。下面的步骤在[第 5 步](#5-decoding)之前完全相同，到那一步再选择其一。

## 开始之前 {#before-you-start}

- 一个 The Things Stack 应用，其中的终端设备已经入网。你需要每台设备的 DevEUI；如果更希望按终端设备 id 匹配，则需要设备 id。
- 一个使用公共 CA 签发证书的 DeviceChain 实例。The Things Network 和 The Things Stack Cloud 不会向使用自签名证书的实例发布数据；你自己运行的部署则可以改为导入实例的 CA。
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

- **Sender type**（发送方类型）：**The Things Stack v3**。创建后不能更改。
- **Token**（令牌）：集成在 URL 中的名称，例如 `ttn-eu1`。创建后不能更改。
- **Device types**（设备类型）：`Room sensor`。
- **Device identifier**（设备标识符）：**DevEUI**，或选择 **Device id**（设备 id）改为按 The Things Stack 中的终端设备 id 匹配。对注册时没有 DevEUI 的设备，请选择 **Device id**：在 **DevEUI** 模式下，不带 DevEUI 的消息会以 `400` 被拒绝。
- **Store link metrics**（存储链路指标）：如果希望在每条上行的读数旁存储信号强度、信噪比和网关数量（`lorawan.rssi`、`lorawan.snr` 和 `lorawan.gateways`），请开启。

选择 **Create integration**（创建集成）。**Integration created** 对话框只显示一次 **bearer 令牌**。请立即复制：它只以摘要形式存储，无法再次显示。如果丢失，**Rotate token**（轮换令牌）会签发新令牌，旧令牌立即失效。

使用 GraphQL 时，`createIntegration` 在 `issuedToken` 中返回令牌：

```graphql
mutation {
  createIntegration(request: {
    token: "ttn-eu1"
    name: "The Things Network EU1"
    type: "TTS_V3"
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

按设备 id 匹配时，设置 `identity: "DEVICE_ID"`。集成的地址是：

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` 是你打开控制台所用的主机，`<tenant>` 是租户令牌，`<integration>` 是集成的令牌。控制台中集成页面的 **Connect the sender**（连接发送方）会显示完整 URL，并附带复制按钮。

## 3. 在 The Things Stack 中添加 webhook {#3-webhook}

在 The Things Stack 控制台中打开应用，选择 **Integrations → Webhooks**，再选择 **Add webhook**，然后选择 **Custom webhook**：

| The Things Stack 设置 | 值 |
| --- | --- |
| Webhook ID | 任意名称，例如 `devicechain`。 |
| Webhook format | **JSON**。 |
| Base URL | `https://<host>/api/integrations/<tenant>/<integration>` |
| Additional headers | 一个请求头：名称 `Authorization`，值 `Bearer <bearer-token>`。 |
| Enabled event types | 启用 **Uplink message**，路径为 `/uplink-message`。 |

The Things Stack 会把每种事件类型的路径追加到基础 URL 后，因此上行会发布到 `.../<integration>/uplink-message`。DeviceChain 会在 `/uplink-message`、`/uplink`、`/uplinks` 和 `/up` 这些路径上读取上行；路径留空时，也会在基础 URL 本身上读取。其他任何路径（例如入网接受或位置解算）都会得到 `204` 响应并被忽略，除非它携带的是 DeviceChain 所发下行的报告（参见 [The Things Stack 下行](./inbound-integrations.md#lorawan-downlink-tts)）。无需启用它们。

**Downlink API key** 留空即可，DeviceChain 不使用它。令牌只放在请求头中。不要把它写进 URL，否则会被记录到访问日志。

## 4. 注册设备 {#4-devices}

DeviceChain 通过设备**别名**找到上行所属的设备。为每个传感器创建一台第 1 步中类型的设备，并添加集成的 **Device identifier** 所读取的别名：

| 设备标识符 | 别名方案 | 值 |
| --- | --- | --- |
| DevEUI | `lorawan.deveui`（控制台中为 **LoRaWAN DevEUI**） | 16 位十六进制数字，大小写均可。 |
| Device id | `tts.device-id`（控制台中为 **Network server device id**） | 终端设备 id，例如 `room-4-sensor`。 |

在控制台中，使用设备表单的 **Aliases**（别名）字段：**Add alias**（添加别名），选择方案并填写值。使用 GraphQL：

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

也可以让传感器自己“报到”。如果上行的标识符不属于集成设备类型中的任何设备，它不会被存储，而是列在集成页面的 **Unregistered devices seen**（已见的未注册设备）中。**Create a device with this alias**（用此别名创建设备）会打开已填好别名的设备表单。参见[未注册设备](./inbound-integrations.md#unregistered)。

## 5. 选择在哪里解码帧 {#5-decoding}

### 方案 A：由 The Things Stack 载荷格式化器解码 {#option-a-formatter}

在 The Things Stack 中，为应用或某个终端设备设置上行**载荷格式化器**；终端设备上的格式化器优先于应用上的格式化器。许多设备在 LoRaWAN Device Repository 中已有现成的格式化器。如果使用自定义 JavaScript 格式化器，请编写 `decodeUplink` 函数；The Things Stack 会把返回值中的 `data` 作为上行的 `decoded_payload` 发送。对于本例的房间传感器，其端口 2 上的六字节帧依次为：以十分之一度为单位的有符号温度、以半个百分点为步长的湿度、以毫伏为单位的电池电压，以及最后一个字节第 0 位的门磁触点：

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    return { errors: ["unexpected frame on port " + input.fPort] };
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

DeviceChain 一侧无需再做任何事：让设备配置文件保持**没有**解码器。DeviceChain 会把 `decoded_payload` 展平为读数：

- 每个数字都是一条读数，以其键名存储；嵌套值以点连接的路径为键（`{"battery": {"mv": 3300}}` 即 `battery.mv`）；
- `true` 和 `false` 存储为 `1` 和 `0`；
- 字符串和 `null` 会被跳过，因此格式化器返回的 `"label": "north"` 不会存储任何内容。

让格式化器返回的键与你声明的指标同名，或者按格式化器返回的键声明指标。Device Repository 中的格式化器自行决定键名；The Things Stack 控制台中上行的 **Live data** 会显示它们。完整规则见[存储内容](./inbound-integrations.md#what-is-stored)。

如果上行没有 `decoded_payload`，或其中没有任何数字或布尔值，平台会确认收到但不存储任何内容。集成会把它计为**未解码**并保留其帧的样本：没有载荷格式化器时，上行就是这种表现。

:::tip 调整格式化器的输出
如果格式化器返回的键或单位与你的配置文件不同（例如华氏度的 `temp_f`、`bat.mv`），无需修改格式化器。基于 **Decoded object**（已解码对象）输入的 DeviceChain 解码器可以重命名并换算它们；[调整已解码对象](./payload-decoders.md#example-tts)正是这种情况。
:::

### 方案 B：由 DeviceChain 解码原始帧 {#option-b-decoder}

为设备配置文件添加[载荷解码器](./payload-decoders.md)后，The Things Stack 就不需要载荷格式化器。每条上行都在 `frm_payload` 中携带原始帧，集成会把以下内容交给解码器：

| 解码器中的名称 | 来自 The Things Stack 消息 |
| --- | --- |
| `frame` | `uplink_message.frm_payload`，即帧的字节 |
| `fPort` | `uplink_message.f_port` |
| `fCnt` | `uplink_message.f_cnt` |
| `decoded` | `uplink_message.decoded_payload`，当运行了载荷格式化器时 |
| `link` | `rx_metadata` 中最强的一次接收及网关数量，当开启 **Store link metrics** 时 |

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

发布前先试一试解码器。在 **Test**（测试）面板中选择 **Frame**（帧），粘贴 `00D75A0CE401` 并把 `fPort` 设为 2；或者选择 **Webhook body**（Webhook 正文），选中该集成，粘贴一条 The Things Stack 发布的完整消息，并把消息类型设为 `uplink-message`。然后**发布**配置文件。新发布的解码器大约需要一分钟才会生效。

设备类型有了解码器之后，存储什么由解码器决定。即使也运行了载荷格式化器，其 `decoded_payload` 也不会被存储，只会以 `decoded` 的形式提供给解码器。解码失败时，该上行的任何内容都不会存储，并会记录为原因为 `DECODE_FAILED` 的失败事件；配置文件的 **Decoder** 选项卡会在 **Health**（健康状况）和 **Recent failures**（最近失败）中显示。原因列表见[解码器拒绝原因](./payload-decoders.md#refusal-reasons)。

DeviceChain 解码器不是 JavaScript，而是一组没有循环的类型化表达式，因此 JavaScript 格式化器不能直接粘贴进来，需要把其中的字节读取改写为解码器行。语言说明见[载荷解码器](./payload-decoders.md)和[函数参考](../reference/decoder-functions.md)。

## 6. 确认上行已到达 {#6-check}

在配置 webhook 之前，你可以用一个与 The Things Stack 请求形状相同的请求测试集成。这个请求同时带有 `decoded_payload`（方案 A）和 `frm_payload` 中的原始帧（方案 B），所以两种方案都适用。`received_at` 是必填项，且不能早于 366 天之前，因此命令会填入当前时间：

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/ttn-eu1/uplink-message" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "end_device_ids": {
      "device_id": "room-4-sensor",
      "application_ids": { "application_id": "my-application" },
      "dev_eui": "70B3D57ED0050004"
    },
    "received_at": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
    "uplink_message": {
      "f_port": 2,
      "f_cnt": 1,
      "frm_payload": "ANdaDOQB",
      "decoded_payload": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
    }
  }'
```

`202 Accepted` 表示请求已被读取且令牌有效，并不表示已经存储了任何内容。接着在 DeviceChain 中查看：

- 设备页面显示读数：`temperature` 21.5、`humidity` 45、`battery` 3300 和 `door` 1。
- 集成页面在请求认证失败时显示 **Rejected token**（令牌被拒绝），显示没有可存储内容的上行的**未解码**计数，并在 **Unregistered devices seen** 中列出范围内没有任何设备持有的标识符。
- 使用方案 B 时，配置文件的 **Decoder** 选项卡会显示解码成功和失败的上行数量。

webhook 启用后，真实上行也会以同样方式出现。The Things Stack 控制台中应用的 **Live data** 会显示它转发的上行。

## 故障排除 {#troubleshooting}

| 现象 | 可能原因 |
| --- | --- |
| `401` | bearer 令牌错误或已被轮换，或基础 URL 中的租户令牌或集成令牌错误。集成会显示 **Rejected token**。 |
| `403` | 集成已被关闭。重新开启 **Accepting data**（接收数据）。 |
| `400` | 响应正文会指出出错的字段：在 **DevEUI** 模式下缺少 DevEUI（请选择 **Device id**）、设备 id 或 DevEUI 格式错误、缺少 `received_at` 或早于 366 天之前，或正文不是 JSON（请把 webhook 格式设为 **JSON**）。 |
| `204`，且没有存储任何内容 | 上行被发布到了 DeviceChain 不作为上行读取的路径。请使用 `/uplink-message`。 |
| `202`，但没有存储任何内容 | 集成设备类型中没有设备持有该标识符（查看 **Unregistered devices seen**），或该上行没有可存储的内容（查看未解码计数），或在方案 B 中解码失败（查看 **Decoder** 选项卡）。 |
| 读数没有单位 | 配置文件中没有为该键声明指标，或声明后尚未发布配置文件。 |
| webhook 完全无法连接 | The Things Stack 不信任实例的证书。 |

[为什么我的设备数据没有出现](./why-isnt-my-data-showing-up.md)介绍了平台对其拒绝内容的记录，[发送方收到的响应](./inbound-integrations.md#responses)列出了所有响应。

## 后续步骤 {#next-steps}

- 把读数变成告警：[从 LoRaWAN 上行到告警](./lorawan-uplink-to-alarm.md)让同一个传感器继续触发检测规则、告警和命令。
- 通过 The Things Stack 下发命令：[The Things Stack 下行](./inbound-integrations.md#lorawan-downlink-tts)。
- 了解重复与重试：[重复与重试](./inbound-integrations.md#duplicates)。重试的请求带有相同的 DevEUI、帧计数器和 `received_at`，因此只会存储一次。
- 保护好令牌：[集成令牌能做什么](./inbound-integrations.md#trust)。

## 另请参阅 {#see-also}

The Things Stack 一侧设置的官方文档（英文）：

- [Creating webhooks](https://www.thethingsindustries.com/docs/integrations/webhooks/creating-webhooks/)：自定义 webhook 表单、基础 URL 和事件类型路径。
- [Data formats](https://www.thethingsindustries.com/docs/integrations/data-formats/)：上行消息及其字段。
- [Payload formatters](https://www.thethingsindustries.com/docs/integrations/payload-formatters/)：应用和终端设备的格式化器，以及 JavaScript `decodeUplink` 函数。
