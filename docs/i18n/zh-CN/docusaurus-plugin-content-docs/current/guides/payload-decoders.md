---
sidebar_position: 3.8
title: 载荷解码器
---

# 载荷解码器 {#payload-decoders}

**载荷解码器**把设备或网络服务器发来的内容转换成测量。你只需在[设备配置文件](../user-guide/device-types-and-profiles.md)上写一次，它是一份简短的规则列表：把偏移 1 处的两个字节读作有符号数，除以 10，存为 `temperature`。它可以读取二进制帧、JSON 正文，或网络服务器已经解码出的对象，产生的测量与任何其他来源相同，并带有你所声明指标的单位和校验。

:::note 实时解码已随 v1.2 提供
你发布的解码器会在[入站集成](./inbound-integrations.md)的实时上行上运行：当上行所属的设备，其设备类型的配置文件版本带有解码器时，该上行会用它来解码，而不是按网络服务器解码出的内容存储。新发布的解码器可能需要约一分钟才会生效。没有解码器的设备类型照旧存储。升级时如何滚动部署见[发布与升级](../deployment/releases-and-upgrades.md#v120-payload-decoders)。
:::

## 什么时候需要 {#when}

如果网络服务器已经解码了帧，你大概不需要解码器。配置了编解码器的 ChirpStack 和配置了载荷格式化器的 The Things Stack 会发送解码后的对象，集成会[原样存储其中的数字](./inbound-integrations.md#what-is-stored)。在以下情况下你需要解码器：

- 网络服务器没有为该设备配置编解码器或格式化器，而你有它的帧字节布局；
- 你想重命名、重新缩放或重新组织网络服务器解码出的内容，例如把华氏度转换成摄氏度，或把 `bat.mv` 读作 `battery`；
- 发送方是[通用 JSON](./inbound-integrations.md#generic-json) 或[通用二进制](./inbound-integrations.md#generic-binary)集成，而你想要的不止它展平出来的数字；
- 帧中携带 Cayenne LPP 载荷，`lpp.decode` 一次调用就能读取。

## 解码器是什么 {#what-it-is}

解码器属于某个设备配置文件，并随它一起流转：

- 你在配置文件的**草稿**上编辑它，位置是 **Decoder** 选项卡。保存只写入草稿。
- **发布**配置文件会把解码器冻结进该版本。已发布版本的解码器永远不会改变，回滚到较早的版本时，它的解码器也会一并恢复。
- 一条上行会用其设备所解析到的那个版本的解码器来解码。

解码器是一份用小型、无循环语言（CEL）写成的表达式文档。它有一个要读取的**输入**类型、可选的**共享值**，以及一个或多个**分段**；每个分段有若干**行**，每一行说明它写入哪个指标，以及如何计算该值。这门语言没有时钟、没有随机性、没有副作用，因此相同的输入总是得到相同的读数。

一个解码器最多有 8 个分段、16 个共享值和 20 个已保存的测试向量，整份文档最大 32 KiB。[函数参考](../reference/decoder-functions.md#decoder-functions-limits)列出了这些限制。

## 输入 {#inputs}

解码器的**输入**说明它读取上行的哪一部分。如果上行不带这一部分，解码会以 `missing_input` 失败，绝不会被当作零来读取。

| 输入 | 含义 |
| --- | --- |
| **Binary frame**（二进制帧，`FRAME`） | 设备消息的原始字节，最大 4 KiB。 |
| **Generic JSON**（通用 JSON，`JSON`） | 通用 JSON 集成中的一个条目，不含标识设备和时间的字段。最大 64 KiB，嵌套最多 32 层。 |
| **Decoded object**（解码对象，`DECODED`） | 网络服务器解码出的对象（ChirpStack 的 `object`，The Things Stack 的 `decoded_payload`），上限相同。 |

表达式可以看到这些名称：

| 名称 | 类型 | 含义 |
| --- | --- | --- |
| `frame` | bytes | 原始帧。 |
| `json` | map | 通用 JSON 条目。 |
| `decoded` | map | 网络服务器的解码对象。 |
| `fPort` | int | LoRaWAN 应用端口；上行并非来自 LoRaWAN 网络服务器时为 `-1`。 |
| `fCnt` | int | LoRaWAN 上行帧计数器，或 `-1`。 |
| `link` | double 的 map | `rssi`、`snr` 和 `gateways`，前提是网络服务器报告了它们，且集成开启了 **Store link metrics**。 |
| 你的共享值 | 其表达式的类型 | 你在 **Shared values**（共享值）下命名的任何值，其后的所有内容都可使用。 |

两个 map 输入用常量键读取：`decoded.battery.mv` 或 `decoded["battery"]["mv"]`。键不能计算得出，因此 JSON 数组不能按下标访问。要读取可能缺失的键，先用 `has(decoded.humidity)` 判断。

ChirpStack 提供 `frame`（`data`）、`decoded`（`object`）、`fPort` 和 `fCnt`。The Things Stack 提供 `frame`（`frm_payload`）、`decoded`（`decoded_payload`）、`fPort`（`f_port`）和 `fCnt`（`f_cnt`）。通用 JSON 集成提供 `json`，设置了它的 `framePointer` 时还提供 `frame`。通用二进制集成提供 `frame`。

## 分段与字段 {#sections}

解码器是一个**分段**列表，**条件成立的第一个分段会被执行**。没有条件的分段总是匹配。用分段为每种消息类型指定各自的布局，通常按 `fPort` 区分：Decoder 选项卡有一个 **fPort** 框，它是条件 `fPort == 2` 的快捷方式，另有一个 **Only when**（仅当）框用于任何其他条件。

- **标记为忽略的分段**会接受上行，但不存储任何内容。用它处理你不想要的消息类型。它被计为已忽略，而不是失败。
- **没有任何分段匹配**会让解码以 `no_section` 失败。如果希望静默接受所有其他消息，请在最后放一个不带条件的忽略分段。
- **匹配了但没有产生任何读数的分段**（每一行的条件都为假）会以 `no_outputs` 失败。

每一行写入一条测量。行有三种类型。

### 字段表 {#field-table}

Decoder 选项卡以一个**字段表**开始，它的每一行都会变成一个表达式。你选择：

- **Metric**（指标）：值存储时使用的键。只提供配置文件上已声明为测量型（而非计算型）的指标。
- 对于帧：**类型**（8、16、24 或 32 位的无符号或有符号整数，32 或 64 位浮点数，单个位，位域，有符号位域，或 BCD 数字）、需要时的**字节序**，以及**字节偏移**。位域还需要位偏移和 1 到 32 的宽度；BCD 需要 1 到 8 个字节的长度。
- 对于 JSON：消息中的**路径**，例如 `bat.mv`，它是数字还是真/假值，以及路径不存在时是否静默跳过该行。
- 可选的**乘以**、**除以**和**加上**，按此顺序应用。
- 可选的 **Only when**（仅当）条件。条件为假的行会被跳过，不报错也不计数。

字段表只是为你写出表达式的便利工具。转换成表达式的行会一直保持为表达式：没有任何功能会把表达式再转回字段表的行。

### 原始表达式 {#raw-cel}

**Expression**（表达式）类型的行有一个指标键和一个自由表达式。表达式必须产生数字或真/假值；真/假值存为 `1` 或 `0`。表达式可以调用的全部内容见[函数参考](../reference/decoder-functions.md)。

```text
double(i16be(frame, 1)) / 10.0
```

表达式语言是有类型的，以下三条规则能解决大多数初次尝试时的问题：

- **整数相除得到整数。** `u16be(frame, 3) / 7` 会丢掉余数。想要小数时，写 `10.0`，或用 `double(...)` 转换。
- **比较需要编译器确知是数字的操作数。** `decoded.t > 1` 会被拒绝，因为 `decoded.t` 可能是任何类型。写成 `double(decoded.t) > 1.0`。
- **`&&` 或 `||` 的另一侧已经决定了结果时，这一侧的错误不算错误。** `has(decoded.x) && double(decoded.x) > 1.0` 在 `x` 缺失时就是假。其他任何错误都会让解码失败。

位从字节的最低有效位开始计数：`bit(frame, 0, 0)` 是第一个字节的最低位，`bit(frame, 0, 7)` 是最高位。位域会延伸到下一个字节，因此对字节 `AB CD` 执行 `bits(frame, 0, 4, 8)`，会从第一个字节的第 4 位开始读取八位，结果是 `0xDA`。

### 映射 {#maps}

**Map**（映射）类型的行从一个表达式取出一整组键，例如 `lpp.decode(frame)`，或输入的某个子对象，例如 `decoded.values`。你需要给它一个**键前缀**（Key prefix）和**最多键数**（`maxKeys`，1 到 64）：

- 映射的每个键存储为前缀加键。不是已声明指标的键会被跳过，而不是报错，因为传感器可能新增通道；测试面板会把它列为未存储。
- 键数超过 `maxKeys` 的映射会让解码以 `map_overflow` 失败。它绝不会被截断。
- 如果同一条上行中有多行写入同一个键，只保留一个条目，其余的会被跳过，并在测试面板中列为 `collision`。字段行优先于映射；两个映射之间，靠前的行优先。
- 以 `lorawan.` 开头的键会被跳过：这个前缀属于平台追加的链路指标。

### 共享值 {#shared-values}

**共享值**是一个有名字的表达式，每条上行在分段之前按你列出的顺序计算一次。当多行都需要同一个东西时使用它，例如头字节或切出的载荷片段：

```text
hdr = u8(frame, 0)
```

名称由字母、数字和 `_` 组成，不能以数字开头，也不能是内置名称之一（`frame`、`fPort`、`math` 等）。

### 读数数量 {#max-readings}

解码器的**最大读数数**是一条上行最多能产生的读数：对最大的那个分段，是它的字段行和表达式行，加上映射行的 `maxKeys`。每个分段最多为 64，它显示在 Decoder 选项卡的解码器旁边，形如 “Up to N readings per uplink”。有解码器的设备类型的上行，在被接受时计入租户的接入额度：至少是这个数量的读数（集成存储链路指标时再加 3），运行代价高的解码器更多，且最多 256，无论它最终实际产生多少。

### 存储什么 {#output-values}

每个标量行都必须指定**同一配置文件上已声明的测量型指标**，值按该指标的类型写入：

| 指标类型 | 值必须是什么 |
| --- | --- |
| 整数 | 64 位范围内的整数。`12.0` 被接受，`12.5` 被拒绝。 |
| 双精度 | 任何有限数。 |
| 布尔 | 真/假值，或恰好为 `0` 或 `1`。 |

随后，值会经过指标自身的校验（最小值、最大值和允许的取值），与设备发来的值一样。校验失败的值会以 `invalid_output` 终止整个解码。没有部分结果：**只要有一行失败，这条上行的任何内容都不会被存储。**

一个键在一个分段中只能写入一次。`lorawan.*` 键保留给链路指标，不能作为输出；计算型指标也不能作为输出，因为平台会丢弃该值而采用计算出的值。这些在保存时就会被拒绝。

## 测试向量与发布 {#vectors}

**测试向量**是一份带有预期读数的已保存输入：名称、帧（十六进制或 base64）、JSON 对象或解码对象，可选的 `fPort` 和 `fCnt`，以及预期的键值对。只有当解码器产生**恰好**这些键、且数值相等时，向量才通过：缺少一个键会失败，多出一个键也会失败。针对忽略分段的向量不期望任何读数。

你可以保存带有失败向量的解码器，因此进行中的工作不会丢失。**只要有一个向量失败，就不能发布该配置文件。**发布配置文件时，会用正在发布的指标编译解码器，并运行每个已保存的向量，发布会被拒绝，并指出第一个失败的表达式或向量。回滚到较早的版本会对该版本执行同样的检查，如果它的解码器不再能编译，或其某个向量失败，回滚就会被拒绝。一个解码器最多可以有 20 个向量。

为设备发送的每一种消息布局保存一个向量，再为每个曾经出过错的情形保存一个。这样你才能知道修改解码器没有改变旧消息的结果。

## 测试面板 {#test-panel}

Decoder 选项卡的 **Test**（测试）面板会按**编辑器中的当前状态（无论是否已保存）**运行解码器，使用平台自己的编译器和求值器，并对照配置文件草稿的指标。不会存储任何内容。你可以选择测试的内容：

- **Frame**（帧）：粘贴十六进制或 base64（忽略空白，最多 4,096 字节），也可以加上 `fPort` 和 `fCnt`。
- **JSON**：一个 JSON 对象，交给解码器所读取的 `json` 或 `decoded`。
- **Webhook body**（Webhook 正文）：选择一个已保存的集成，粘贴**整个请求正文**，并在集成会读取的地方填入请求头、查询参数、消息类型和内容类型。面板用解析实时请求的同一份代码来解析它，因此你看到的就是集成将交给解码器的内容。如果正文包含多条上行，会测试第一条，面板会告诉你一共有几条。如果集成开启了 **Store link metrics**，链路读数也会出现在结果中。

结果会显示：

- **Decoded**（已解码）、**Ignored**（已忽略）或 **Failed**（失败），以及匹配的分段名称；
- **读数**，即它们将被存储的样子；
- **未存储**的键及各自的原因：`undeclared`（没有声明这样的指标）、`collision`（另一行已经写入该键）或 `reserved`；
- 失败时，显示**原因**、失败的**表达式**（例如 `sections[0].outputs[1].expr`）、消息，以及读取函数拒绝的字节偏移；
- 解码花费的**成本**；
- 每个已保存的向量，无论通过与否。**Save as vector**（保存为向量）会把一次成功的 Frame 或 JSON 测试变成一个向量；它需要编辑设备的权限，Webhook 正文的测试不能直接保存。

无法编译的文档会在同一个位置显示为编辑器旁的错误，而不是请求失败。

## 成本限制 {#cost}

解码器不能循环，但仍然可能很昂贵：读取一个长片段、解码一段长文本或一帧很长的 LPP，比读取一个数字花费更多。因此每个表达式都有一个**成本**，用来计量它所做的工作：每个名称、字面量、运算符和类型转换约为 1，每次调用再加上[函数参考](../reference/decoder-functions.md#decoder-functions-library)中该函数所示的成本，映射行每个键为 1。

- **运行之前**，平台会把最坏情况加起来。单个表达式超过 **10,000**，或整个解码器超过 **50,000**，都会在保存或发布时被拒绝，错误会指出是哪个表达式。完整读取 4 KiB 约为 4,100，因此普通的解码器远低于这一限制。
- **运行期间**，花费超过估算值的解码会以 `cost_exceeded` 停止。结构正确的解码器不会出现这种情况；它是估算之后的一道护栏。
- **读取 JSON 的成本是每字节 1。** Decoder 选项卡中显示的估算值包含解析最大允许 JSON（65,536）的成本，所以读取 `json` 或 `decoded` 的解码器，即使表达式很小，估算成本也会达到几万。这个数字不计入 50,000 的限制；真实的上行按其实际大小计价。

读取固定字节数的函数，无论帧多长成本都是 1。切片、解码文本和 `lpp.decode` 按长度计费，所以先切片再解码，并且只解码你需要的部分。

## 解码何时被拒绝 {#refusals}

### 保存和发布时 {#refusals-save}

保存、发布和回滚会拒绝不合格的解码器，并指出位置。GraphQL 错误在 `extensions.code` 中带有一个代码：

| 代码 | 含义 |
| --- | --- |
| `INVALID_DECODER` | 文档无法解析（未知的键会被拒绝而不是忽略）、无法编译、指向未声明、计算型或保留的指标、同一个键写入两次、超出成本上限或大小限制，或者在发布或回滚时有已保存的向量失败。消息会指出表达式或向量。 |
| `UNSUPPORTED_DECODER_KIND` | 文档写着 `"kind": "WASM"`。只有 `CEL` 能运行；另一种类型会被拒绝而不是被跳过。 |
| `RATE_LIMITED` | 测试请求过多；参见[测试速率限制](#rate-limit)。 |
| `INVALID_VALUE` | 测试请求指定了多个输入，或指定了 webhook 正文却没有集成。 |
| `LIMIT_EXCEEDED` | 测试输入超出了其限制。 |

一次保存会一并报告编译器发现的所有问题，每个问题都由其路径定位，你可以一起修复：

```text
sections[0].outputs[1].key: "nope" is not a declared measured metric of this profile (a calculated metric cannot be a decoder output)
```

### 在测试或上行中 {#refusal-reasons}

失败的解码会报告以下原因之一。这条上行不会存储任何内容。

| 原因 | 含义 |
| --- | --- |
| `compile_failed` | 解码器无法编译。 |
| `env_unsupported` | 解码器指定的函数库版本，本构建不认识。 |
| `missing_input` | 上行不带解码器所读取的输入。 |
| `no_section` | 没有任何分段的条件成立。 |
| `no_outputs` | 匹配的分段没有产生任何读数。 |
| `eval_error` | 某个表达式失败：读取超出帧末尾，未用 `has()` 就读取了缺失的 JSON 键，值不是有限数，或转换失败。读取错误会指出函数和偏移，如 `u16be: reads 2 byte(s) at offset 3 of a 2-byte frame (offset 3)`。 |
| `invalid_output` | 某个值无法为其指标存储：类型错误、不是有限数，或超出指标的限制。 |
| `map_overflow` | 某个映射行产生的键数超过其 `maxKeys`。 |
| `cost_exceeded` | 解码花费超过了它的估算值。 |
| `input_too_large` | 帧或 JSON 超出上限。 |
| `input_malformed` | JSON 无法解析或嵌套超过 32 层，或测试输入不是有效的十六进制或 base64。 |
| `unsupported_encoding` | 测试面板的失败：粘贴的正文使用了集成不接受的编码，例如 ChirpStack 的 Protobuf 编码。 |
| `ignored_message` | 测试面板的失败：集成会忽略这种消息类型（例如 join），因此没有可解码的内容。 |

解码失败的上行不会被存储，而是被记录为原因为 `DECODE_FAILED` 的失败事件；其错误文本的开头就是本表中的原因。它在第一次投递时记录，不会重试。

## 通过 GraphQL 编写 {#graphql}

控制台所做的一切，都可以在 device-management 的 GraphQL API 上完成。保存和清除需要编辑设备的权限；测试需要读取设备的权限，访问范围仅限于租户一部分的调用者会被拒绝。

```graphql
# schema: device-management
mutation SaveDecoder($token: String!, $document: String!) {
  setDeviceProfileDecoder(profileToken: $token, document: $document) {
    token
  }
}
```

`document` 是以 JSON 文本表示的解码器。`clearDeviceProfileDecoder(profileToken)` 会撤销草稿解码器；已发布的版本保留各自的解码器。配置文件的草稿解码器是 `DeviceProfile.decoder`，冻结在某个版本中的解码器是 `DeviceProfileVersion.decoder`；两者都报告 `maxReadings` 和 `estimatedCost`。

```graphql
# schema: device-management
query TestDecoder($input: PayloadDecoderTestInput!) {
  testPayloadDecoder(input: $input) {
    ok
    ignored
    section
    cost
    readings { key value }
    skipped { key reason }
    error { reason path message offset }
    vectors { name ok error }
    uplinks
  }
}
```

在 `PayloadDecoderTestInput` 中最多指定一个输入：`frameHex`、`frameBase64`、`json`，或带有 `integrationToken` 的 `webhookBody`。一个都不指定，则只运行已保存的向量。控制台显示的函数表就是 `payloadDecoderFunctions`。

## 实战示例 {#examples}

### ChirpStack：二进制帧 {#example-chirpstack}

一台房间传感器在端口 2 上发送六个字节，而 ChirpStack 设备配置文件没有配置编解码器。

| 字节 | 含义 |
| --- | --- |
| 0 到 1 | 温度，单位为十分之一度，有符号，大端 |
| 2 | 湿度，步长为百分之零点五 |
| 3 到 4 | 电池电压，单位为毫伏，大端 |
| 5 | 第 0 位是门磁触点 |

在设备配置文件上声明指标：`temperature`（double）、`humidity`（double）、`battery`（整数）和 `door`（布尔）。然后把解码器的输入设为 **Binary frame**，添加两个分段：一个设置 **fPort** 为 2 并包含四个字段，另一个**忽略**其他所有端口。下面是该选项卡保存的文档：

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
    },
    { "name": "other port", "input": { "frameHex": "00", "fPort": 9 }, "expect": {} }
  ]
}
```

在测试面板中选择 **Frame**，粘贴 `00D75A0CE401`，把 `fPort` 设为 2 并运行：读数为 `temperature` 21.5、`humidity` 45、`battery` 3300 和 `door` 1。用 `fPort` 9 运行同一帧，结果是 **Ignored**。只发送前三个字节 `00D75A`，结果是在 `sections[0].outputs[2].expr` 处的 `eval_error`：`u16be: reads 2 byte(s) at offset 3 of a 3-byte frame (offset 3)`。

如果要演练整个请求而不只是字节，选择 **Webhook body**，选中 ChirpStack 集成，粘贴 ChirpStack 发送的正文。

### The Things Stack：重新组织解码对象 {#example-tts}

The Things Stack 中的载荷格式化器已经解码了帧，但得到的是 `{"temp_f": 70.7, "bat": {"mv": 3300}, "rh": 45}`。如果不处理，平台会存储 `temp_f`、`bat.mv` 和 `rh`，没有一个使用你配置文件中的键。针对 **Decoded object** 输入的解码器可以重命名它们并转换单位：

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "DECODED",
  "sections": [
    {
      "name": "uplink",
      "outputs": [
        { "key": "temperature", "expr": "(double(decoded.temp_f) - 32.0) / 1.8" },
        { "key": "battery", "expr": "int(decoded.bat.mv)" },
        { "key": "humidity", "when": "has(decoded.rh)", "expr": "double(decoded.rh)" }
      ]
    }
  ],
  "vectors": [
    {
      "name": "with humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 }, "rh": 45 } },
      "expect": { "temperature": 21.5, "battery": 3300, "humidity": 45 }
    },
    {
      "name": "without humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 } } },
      "expect": { "temperature": 21.5, "battery": 3300 }
    }
  ]
}
```

`humidity` 行带有条件，这样没有 `rh` 的上行也能解码；没有条件的话，缺失的键会让整个解码以 `eval_error` 失败。这个解码器的估算成本超过 65,000，几乎全部来自为解析一个完整的 64 KiB 对象所预留的部分；参见[成本限制](#cost)。在测试面板中选择 **Webhook body**，选中 The Things Stack 集成，粘贴整个 webhook 正文，并把消息类型设为 `uplink-message`。

### Cayenne LPP {#example-lpp}

发送 Cayenne LPP 的设备不需要字段表：一个 **Map** 行就能解码整帧。帧 `01 67 00 F5 02 68 6C` 包含两个通道：通道 1 是温度（类型 `67`，单位为十分之一度），通道 2 是湿度（类型 `68`，步长为百分之零点五）。

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "lpp",
      "outputs": [{ "map": "lpp.decode(frame)", "prefix": "lpp.", "maxKeys": 8 }]
    }
  ],
  "vectors": [
    {
      "name": "two channels",
      "input": { "frameHex": "016700F502686C" },
      "expect": { "lpp.temperature_1": 24.5, "lpp.humidity_2": 54 }
    }
  ]
}
```

`lpp.decode` 按类型和通道为每个值命名，因此加上前缀后键为 `lpp.temperature_1` 和 `lpp.humidity_2`。请把它们声明为指标。你没有声明的通道会被列为未存储，超过八个值的帧会以 `map_overflow` 失败。当 LPP 载荷前面有一个头部时，先把它切出来：`lpp.decode(slice(frame, 4, size(frame) - 4))`。

## 解码器不做什么 {#limits}

解码器刻意保持小巧，使它能在摄取速率下对每条上行运行，并且不会损害平台。

- **没有循环。**没有 `for`，没有列表推导，也没有递归。唯一的宏是 `has()`。变长数据由 `lpp.decode` 这样的内置函数处理，而不是由你写的循环处理。
- **没有网络、文件、时钟或随机性。**解码器不能对外调用，不能读取输入以外的任何内容，对相同的输入也不会给出不同的答案。
- **没有文本处理。**没有字符串函数，也没有正则表达式。文本可以从十六进制或 base64 解码，并用 `==` 比较；输出始终是数字或真/假值，绝不是文本。
- **没有列表和计算键。**JSON 数组不能按下标访问，键始终是常量字符串。列表字面量只用于 `in`，最多 16 个值。
- **没有其他引擎。**`WASM` 是被拒绝的保留值，而不是一个能运行的引擎。
- **它不能设置读数的时间。**消息自身的时间始终具有权威性，这正是[重复检测](./inbound-integrations.md#duplicates)得以正常工作的原因。
- **它不能写入计算型指标，也不能写入 `lorawan.` 键。**

函数一旦发布，其行为就不会改变。修复会以新名称出现在新的函数库版本中，而解码器在 `envVersion` 中保留它所针对的版本。

## 测试速率限制 {#rate-limit}

`testPayloadDecoder` 每次调用都会编译解码器，因此限制为**每个租户每秒 10 个请求，突发 20 个，针对每个 `device-management` 副本**。限制是按副本计算，而不是按实例：副本多时，租户的总速率可以更高。超出限制的请求会以 `RATE_LIMITED` 被拒绝；稍等片刻再重试。控制台会显示 “Too many tests too quickly”。保存、发布和函数列表不受此限制。

## 相关内容 {#related}

- [解码器函数参考](../reference/decoder-functions.md)：函数库中的函数及其成本，以及各项限制。
- [入站集成](./inbound-integrations.md)：解码器读取的发送方。
- [设备类型与配置文件](../user-guide/device-types-and-profiles.md)：指标、草稿与发布。
- [传输能力矩阵](../reference/transport-matrix.md#inbound-integrations)：每种接入方式支持什么。
- [发布与升级](../deployment/releases-and-upgrades.md#v120-payload-decoders)：本版本有哪些变化。
