---
sidebar_position: 3.9
title: 从 LoRaWAN 上行到告警
---

# 从 LoRaWAN 上行到告警 {#lorawan-uplink-to-alarm}

读完本页，一条二进制 LoRaWAN 上行会穿过网络服务器的集成，被你编写并发布的解码器转换成读数，触发一条检测规则，并产生一条会自行恢复的告警。你不需要网关，也不需要传感器：模拟器扮演网络服务器，发出的请求与真实的 ChirpStack 或 The Things Stack 发出的相同。这里的命令就是你在真实硬件上会用的命令，[最后一节](#real-network-server)说明有哪些不同。

预计需要四十五分钟左右，大部分时间花在安装上。

:::note 本页需要 v1.2
使用你发布的解码器来解码上行，是随 v1.2 提供的，这里用到的 `dc-simulator` 选项（`--lorawan-binary` 和 `--lorawan-print-decoder`）也是同一版本带来的。在 v1.1 或更早的版本上，不带解码对象的上行会被确认但什么也不存储，因此本页从[第 3 步](#3-the-decoder)起无法运行。开始之前请先确认实例的版本。
:::

上行所走的路径：

```text
 dc-simulator                         DeviceChain
┌──────────────────────┐  HTTPS POST  ┌─────────────────────────────────────────────────────────────┐
│ plays ChirpStack or  │─────────────▶│ inbound integration ──▶ decoder on the device profile       │
│ The Things Stack     │  raw frame,  │   finds the device        six bytes ──▶ temperature,        │
│                      │  no decoded  │   by its DevEUI           humidity, battery.voltage, door   │
└──────────────────────┘  object      │                                   │                         │
                                      │                                   ▼                         │
                                      │ alarm room-too-warm ◀── detection rule  temperature > 25   │
                                      └─────────────────────────────────────────────────────────────┘
```

配置了编解码器或载荷格式化器的网络服务器，会把已经解码好的对象交给 DeviceChain，集成会原样存储其中的数字。这里的网络服务器没有该传感器的编解码器，所以它转发原始字节，由设备配置文件上的解码器来读取。设备的字节布局因此只存在于一处，你可以与配置文件的其余部分一起对它进行版本管理、测试和回滚。

## 你需要什么 {#requirements}

- 一个在笔记本电脑上运行的 v1.2 或更高版本的实例，按[你的第一台设备](../quickstart/first-device.md)中的方式安装：`dcctl install local --compact`，然后 `dcctl bootstrap local devicechain --host localhost --no-tls --no-escrow`。实例名为 `devicechain`，省略 `--instance` 时 `dcctl` 期望的正是这个名字。
- `curl` 和 `jq`。
- `dc-simulator`，自 v1.2 起随每个发布版本与 `dcctl` 一起提供。把它放到 `PATH` 中，并用 `dc-simulator --lorawan-print-decoder` 检查，它会打印一份 JSON 文档。

这些命令通过 `http://localhost` 访问实例。如果你的控制台在其他端口上，请一次性设置好地址，并用 `--server` 把相同的主机和端口交给 `dcctl sim create`（例如 `--server localhost:8080`），如果实例不叫 `devicechain`，还要用 `--instance` 给出实例名：

```bash
export DC_URL=http://localhost
```

全部内容也以脚本的形式放在 [`examples/lorawan-uplink-to-alarm`](https://github.com/devicechain-io/devicechain/tree/main/examples/lorawan-uplink-to-alarm) 中，其中的 `run.sh` 一条命令就能完成整个演练。

## 1. 获取租户令牌 {#1-get-a-tenant-token}

`dcctl sim create` 会创建一个租户和一个限定在该租户内的身份，并把该身份的密码写入握手文件。用它来得到一个可以工作的租户：

```bash
dcctl sim create lora
```

租户是 `sim-lora`，身份是 `lora@sim.devicechain.local`。

认证需要两次调用：登录得到身份令牌，再用它换取限定在租户内的令牌。下面的辅助函数会向实例的某个服务发送一个 GraphQL 请求。把它定义一次，因为之后每一步都要用到，然后获取令牌：

```bash
gql() { # SERVICE TOKEN QUERY [VARIABLES-JSON]
  local v=${4:-'{}'}
  curl -sS -X POST "$DC_URL/api/$1/graphql" \
    -H 'Content-Type: application/json' ${2:+-H "Authorization: Bearer $2"} \
    -d "$(jq -nc --arg q "$3" --argjson v "$v" '{query:$q,variables:$v}')" | jq -c .
}

PW=$(jq -r .simPassword ~/.devicechain/sims/lora.json)
IDT=$(gql user-management '' 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
  "$(jq -nc --arg p "$PW" '{e:"lora@sim.devicechain.local",p:$p}')" | jq -r .data.login.identityToken)
export DC_TOKEN=$(gql user-management '' 'mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}' \
  "$(jq -nc --arg t "$IDT" '{t:$t,n:"sim-lora"}')" | jq -r .data.selectTenant.accessToken)
```

令牌的有效期为 15 分钟。之后的调用如果因认证错误而失败，请重新运行最后四行。

## 2. 声明传感器测量什么 {#2-the-profile}

**设备类型**指向一个**设备配置文件**，配置文件声明测量项、单位、解码器和规则。创建配置文件，并为传感器发送的每个读数创建一个指标：

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceProfile(request:{token:"room-sensor",name:"Room sensor",category:"sensor"}){token}}'

metric() { # KEY TYPE UNIT
  gql device-management "$DC_TOKEN" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){metricKey}}' \
    "$(jq -nc --arg k "$1" --arg t "$2" --arg u "$3" \
      '{r:({token:("room-sensor-"+($k|gsub("[.]";"-"))),deviceProfileToken:"room-sensor",metricKey:$k,name:$k,dataType:$t} + (if $u=="" then {} else {unit:$u} end))}')"
}
metric temperature DOUBLE Cel
metric humidity DOUBLE %
metric battery.voltage DOUBLE V
metric door BOOLEAN ''
```

```text
{"data":{"createDeviceProfile":{"token":"room-sensor"}}}
{"data":{"createMetricDefinition":{"metricKey":"temperature"}}}
{"data":{"createMetricDefinition":{"metricKey":"humidity"}}}
{"data":{"createMetricDefinition":{"metricKey":"battery.voltage"}}}
{"data":{"createMetricDefinition":{"metricKey":"door"}}}
```

解码器只能写入配置文件中声明为测量型的指标，所以要先创建它们。`Cel` 和 `V` 是 UCUM 单位代码。

## 3. 解码器 {#3-the-decoder}

模拟的传感器在 LoRaWAN 端口 2 上发送六个字节：

| 字节 | 含义 |
| --- | --- |
| 0 | 布局版本，始终为 `1` |
| 1 到 2 | 温度，单位为十分之一度，有符号，大端序 |
| 3 | 湿度，百分比 |
| 4 | 电池电压，单位为十分之一伏 |
| 5 | 第 0 位是门磁触点 |

模拟器会打印读取这一布局的解码器文档。把它保存到文件中：

```bash
dc-simulator --lorawan-print-decoder | tee decoder.json
```

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "telemetry",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "temperature", "expr": "double(i16be(frame, 1)) / 10.0"},
        {"key": "humidity", "expr": "double(u8(frame, 3))"},
        {"key": "battery.voltage", "expr": "double(u8(frame, 4)) / 10.0"},
        {"key": "door", "expr": "bit(frame, 5, 0)"}
      ]
    }
  ],
  "vectors": [
    {
      "name": "door open",
      "input": {"frameHex": "0100fa2d1f01", "fPort": 2},
      "expect": {"temperature": 25, "humidity": 45, "battery.voltage": 3.1, "door": 1}
    }
  ]
}
```

从上往下读：

- `"input": "FRAME"` 让解码器读取上行的原始字节，名称为 `frame`。
- **段**在其 `when` 条件成立时生效。这一段要求端口为 2 且第一个字节为 `1`，因此对于你还没有写段的布局版本，帧会被拒绝，而不是被误读。第一个条件成立的段就是被执行的段。
- 每个**输出**指明一个指标，以及计算其值的表达式。`i16be(frame, 1)` 在字节 1 处读取一个有符号 16 位大端序数值，`bit(frame, 5, 0)` 读取字节 5 的最低位。
- **向量**是随解码器一起保存的测试用例：这个帧必须恰好产生这些读数。

[载荷解码器](./payload-decoders.md)指南描述了每一个部分。保存解码器之前，先运行它。`testPayloadDecoder` 用平台自己的求值器对一个帧求值，并且不存储任何内容：

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok section readings{key value} vectors{name ok}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d1f01",fPort:2}}')" \
  | jq '.data.testPayloadDecoder | {ok, section, readings: (.readings | map({(.key): .value}) | add), vectors}'
```

```json
{
  "ok": true,
  "section": "telemetry",
  "readings": {
    "temperature": "25",
    "humidity": "45",
    "battery.voltage": "3.1",
    "door": "1"
  },
  "vectors": [
    {
      "name": "door open",
      "ok": true
    }
  ]
}
```

帧 `0100fa2d1f01` 是版本 1，`00fa` = 250 个十分之一度 = 25 度，湿度 45%，`1f` = 31 个十分之一伏 = 3.1 伏，门的位被置位。现在把帧截短，看看拒绝是什么样子。这是同一个调用，只是帧只有四个字节，并且取结果的另一部分：

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok error{reason path message offset}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d",fPort:2}}')" \
  | jq -c .data.testPayloadDecoder
```

```json
{"ok":false,"error":{"reason":"eval_error","path":"sections[0].outputs[2].expr","message":"u8: reads 1 byte(s) at offset 4 of a 4-byte frame (offset 4)","offset":4}}
```

原因指出了出错的表达式，以及读取越过的偏移量。以这种方式失败的实时上行会被整条拒绝，[第 10 步](#10-when-an-uplink-does-not-decode)展示了它记录在哪里。

把解码器保存到配置文件的草稿上：

```bash
gql device-management "$DC_TOKEN" 'mutation($t:String!,$d:String!){setDeviceProfileDecoder(profileToken:$t,document:$d){token}}' \
  "$(jq -nc --rawfile d decoder.json '{t:"room-sensor",d:$d}')"
```

```text
{"data":{"setDeviceProfileDecoder":{"token":"room-sensor"}}}
```

在控制台中，同一份文档填写在 **Devices → Device Profiles →** 你的配置文件 **→ Decoder** 中，那里的字段表会替你写出这些表达式。

## 4. 规则与发布 {#4-the-rule}

**检测规则**监视某个配置文件的读数，并在某个条件开始成立时采取行动。这条规则在 `temperature` 高于 25 时产生一条警告级告警。把它保存为 `rule.json`：

```json
{
  "name": "Room too warm",
  "description": "Above 25 degrees: raise a warning alarm. The alarm clears by itself when the reading drops back to 25 or below.",
  "type": "threshold",
  "severity": "warning",
  "when": { "metric": "temperature", "op": "gt", "threshold": 25 },
  "actions": [
    { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "room-too-warm" } }
  ]
}
```

规则是边沿触发的：条件开始成立时产生告警，条件不再成立时告警恢复，恢复无需你编写。规则读取的是解码器产生的读数，而不是字节，所以无论这些读数来自哪个发送方或哪个解码器，规则都是同一条。规则以字符串中的 JSON 文档形式添加到配置文件上：

```bash
gql device-management "$DC_TOKEN" 'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
  "$(jq -nc --rawfile d rule.json '{r:{token:"room-too-warm",deviceProfileToken:"room-sensor",name:"Room too warm",enabled:true,definition:$d}}')"
```

```text
{"data":{"createDetectionRule":{"token":"room-too-warm"}}}
```

解码器和规则仍在草稿上。在你**发布**配置文件之前，它们都不会作用于实时上行；发布会把草稿冻结为一个版本：

```bash
gql device-management "$DC_TOKEN" 'mutation{publishDeviceProfile(token:"room-sensor",label:"decoder and rule"){version}}'
```

```text
{"data":{"publishDeviceProfile":{"version":1}}}
```

发布是平台检查配置文件的时刻。它会针对正在发布的指标编译解码器，运行解码器已保存的向量，并编译规则。只要有一处失败，发布就会被拒绝，并指出第一个失败的表达式或向量，所以连自己的测试用例都通不过的解码器永远不会上线。

## 5. 一台设备，由它的 DevEUI 识别 {#5-a-device}

LoRaWAN 网络服务器用 DevEUI 来称呼设备。DeviceChain 通过**别名**找到携带相同值的设备。为配置文件创建一个设备类型，再创建一台带有该别名的设备：

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceType(request:{token:"room-sensor",name:"Room sensor",profileToken:"room-sensor"}){token}}'

gql device-management "$DC_TOKEN" 'mutation{createDevice(request:{token:"room-4",name:"Room 4",deviceTypeToken:"room-sensor",aliases:[{scheme:"lorawan.deveui",value:"70B3D57ED0050004"}]}){token aliases{scheme value}}}'
```

```text
{"data":{"createDeviceType":{"token":"room-sensor"}}}
{"data":{"createDevice":{"token":"room-4","aliases":[{"scheme":"lorawan.deveui","value":"70B3D57ED0050004"}]}}}
```

DevEUI `70B3D57ED0050004` 对应的模拟温度会在十条上行里从 22.4 度升到 26.9 度，然后重新开始。正是这一点让告警能在一分钟内产生并恢复。

## 6. 集成 {#6-the-integration}

**入站集成**是网络服务器向其发送数据的地址。它限定在若干设备类型上，因此向它发送数据的一方只能触及这些类型的设备，别的都不行。为 ChirpStack v4 创建一个集成，并保存它返回的 bearer 令牌：

```bash
export INTEGRATION_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"chirpstack",name:"ChirpStack",type:"CHIRPSTACK_V4",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token type identity} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)
```

令牌只显示一次，并且只以带密钥的摘要形式存储。这条命令把它保存在 shell 变量中；如果之后还要用，请把它存放到安全的地方。如果丢失了，可以用 `rotateIntegrationToken` 重新签发一个。`linkMetrics:true` 会把每条上行的信号强度、信噪比和网关数量与它的读数一起存储。

集成现在可以通过 `$DC_URL/api/integrations/sim-lora/chirpstack` 访问：先是租户，然后是集成的令牌。参见[URL](./inbound-integrations.md#url)。

## 7. 发送上行 {#7-send-uplinks}

让模拟器扮演一个没有该传感器编解码器的 ChirpStack：十二条上行，每条间隔两秒。

```bash
DC_SIM_INTEGRATION_TOKEN="$INTEGRATION_TOKEN" dc-simulator \
  --lorawan-ns chirpstack --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/chirpstack" \
  --deveui 70B3D57ED0050004 --uplinks 12 --emit-interval 2s
```

```json
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"runId":"1793997768570354300"}
```

`--lorawan-binary` 让它成为一个没有编解码器的网络服务器：每个请求只携带原始帧（`data`，base64 编码）和端口，没有解码对象。令牌通过环境变量传入，这样它不会出现在进程列表中。汇总行是平台的回答，十二条全部是 `202 Accepted`。如果你的实例按某个在你的机器上无法解析的主机名路由，请加上 `--integration-host <host>`。

:::note 刚发布的解码器最多需要一分钟才会生效
`event-sources` 会把哪些设备类型带有解码器记住大约一分钟。在这个窗口内发送的上行，仍可能按网络服务器解码出的内容存储，而没有解码对象就意味着什么也不存储。如果你很快地走完了前面的步骤，请等一分钟再发送。
:::

## 8. 查看结果 {#8-see-the-result}

模拟器退出后，等几秒钟。设备的最新读数，带有配置文件声明的单位：

```bash
gql device-state "$DC_TOKEN" '{latestMeasurements(deviceToken:"room-4"){name value unit}}' \
  | jq -r '.data.latestMeasurements[] | "\(.name) = \(.value) \(.unit // "")"'
```

```text
battery.voltage = 3.1 V
door = 1 
humidity = 57 %
lorawan.gateways = 2 
lorawan.rssi = -80 
lorawan.snr = 8.5 
temperature = 22.9 Cel
```

其中四个读数来自解码器。三个 `lorawan.` 读数是集成附加的链路指标。最后一条上行，即第十二条，读数为 22.9：传感器温度升过了 25 又落了回来，告警随之而动：

```bash
gql device-management "$DC_TOKEN" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state raisedTime clearedTime lastValue}}}' \
  '{"c":{"pageNumber":1,"pageSize":5,"originatorType":"device","originatorToken":"room-4"}}' \
  | jq '.data.alarms.results'
```

```json
[
  {
    "alarmKey": "room-too-warm",
    "severity": "WARNING",
    "state": "CLEARED",
    "raisedTime": "2026-11-06T20:43:01.144383Z",
    "clearedTime": "2026-11-06T20:43:09.16384Z",
    "lastValue": 25.4
  }
]
```

第七条上行读数为 25.4 度，是第一个高于 25 的读数，它产生了告警。第十一条上行读数为 22.4，使告警恢复，比产生时晚了四条上行、八秒。规则里没有任何关于如何恢复的内容。在控制台中，告警位于 **Monitor → Alarms**，读数位于设备页面。

如果没有看到告警，请过一分钟后再发送一次十二条上行，并阅读[为什么我的设备数据没有出现](./why-isnt-my-data-showing-up.md)。

## 9. 换成 The Things Stack {#9-the-things-stack}

The Things Stack 以另一种形状发送同样的信息：帧是 `frm_payload`，端口是 `f_port`，消息类型是路径的最后一段。为它再添加一个集成，限定在同一个设备类型上，并让模拟器扮演 The Things Stack：

```bash
export TTS_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"tts",name:"The Things Stack",type:"TTS_V3",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)

DC_SIM_INTEGRATION_TOKEN="$TTS_TOKEN" dc-simulator \
  --lorawan-ns tts --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/tts" \
  --deveui 70B3D57ED0050004 --uplinks 3 --emit-interval 1s
```

```json
{"ns":"tts","sent":3,"resent":0,"statuses":{"202":3},"errors":0,"runId":"1793997852808847000"}
```

设备、解码器和规则都与第一次运行时相同。解码器读取的是 `frame` 和 `fPort`，两个发送方都会提供，所以它不需要改动。变的只有集成，而理解发送方形状的正是集成。

## 10. 上行无法解码时 {#10-when-an-uplink-does-not-decode}

解码失败时，该上行的任何读数都不会被存储，并且会被记录为一条原因为 `DECODE_FAILED` 的失败事件。故意发一条：同样的六个字节，发到端口 9，而解码器的段不接受这个端口。模拟器始终使用端口 2，所以这条要手工发送，就像 ChirpStack 会发送的那样：

```bash
curl -sS -i -X POST "$DC_URL/api/integrations/sim-lora/chirpstack?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"deduplicationId":"3f0c2f1e-6b1d-4a54-9a43-6a1f0f6f5e01","time":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
       "deviceInfo":{"devEui":"70b3d57ed0050004"},"fCnt":99,"fPort":9,"data":"AQD6LR8B"}' | head -1
```

```text
HTTP/1.1 202 Accepted
```

网络服务器得到的是 `202`，因为集成接受了这条消息。拒绝是你这一侧的一条记录。用 `dcctl events failed` 读取它，带上租户、身份及其密码。如果实例不在 `localhost` 上，请加上带有你的主机和端口的 `--server`（例如 `localhost:8080`），实例不在 `localhost` 的 80 端口时才需要；这个命令没有 `--instance`：

```bash
dcctl events failed -t sim-lora --email lora@sim.devicechain.local --password "$PW"
```

```text
WHEN                         REASON         DEVICE  SOURCE             FOLDED  PAYLOAD    ERROR
2026-11-06T20:43:50.708786Z  DECODE_FAILED  room-4  device-management  0       AVAILABLE  decode_failed: no_section: no section matched this uplink

1 of 1 record(s).
```

错误文本的开头就是[拒绝原因表](./payload-decoders.md#refusal-reasons)中的原因：这里是 `no_section`，因为端口 9 没有匹配任何段。帧太短（`eval_error`）和数值超出指标限制（`invalid_output`）各有各的原因。PAYLOAD 列表示平台是否仍保留原始消息。同样的记录也可以在控制台的 **Monitor → Ingest failures** 中看到。这条命令里的 `deduplicationId` 是固定的；要再发送另一条失败消息，请修改它的最后几位，因为十分钟内的重复消息会在处理之前被丢弃。

## 11. 收到通知（可选） {#11-get-told}

没人看到的告警用处不大。**通知渠道**是发送告警的去处，**策略**规定哪些告警发往哪个渠道。这一步把 room-too-warm 告警发到一个 webhook，这也是接入 Slack 或值班呼叫服务的方式。通过 SMTP 发邮件的做法相同，参见[渠道指南](./notification-channels.md)。

你需要一个能接收 `POST`、且实例能通过公网访问的 URL：Slack 传入 webhook，或你自己的端点。笔记本上的接收端不行，因为实例发出的调用会拒绝私有地址和回环地址；需要时，运维人员可以[放行某个特定地址](../concepts/outbound-connectors.md#destinations)。把 URL 放进一个变量。Slack 传入 webhook 的凭据就在 URL 里，所以渠道不需要单独的密钥（`"auth":"none"`）：

```bash
export WEBHOOK_URL='https://hooks.example.com/alarms'

gql notification-management "$DC_TOKEN" 'mutation($r:NotificationChannelCreateRequest!){createNotificationChannel(request:$r){token channelType enabled}}' \
  "$(jq -nc --arg u "$WEBHOOK_URL" '{r:{token:"ops-hook",name:"Ops webhook",channelType:"webhook",config:({url:$u,auth:"none"}|tojson),enabled:true}}')"

gql notification-management "$DC_TOKEN" 'mutation($r:NotificationPolicyCreateRequest!){createNotificationPolicy(request:$r){token enabled rules{severity channel{token}}}}' \
  '{"r":{"token":"warm-rooms","name":"Warm rooms","enabled":true,"rules":[{"severity":"WARNING","channelToken":"ops-hook","recipients":"[]"}]}}'
```

```text
{"data":{"createNotificationChannel":{"token":"ops-hook","channelType":"webhook","enabled":true}}}
{"data":{"createNotificationPolicy":{"token":"warm-rooms","enabled":true,"rules":[{"severity":"WARNING","channel":{"token":"ops-hook"}}]}}}
```

规则的严重级别是小写的 `warning`，因为检测规则就是这样编写的。它产生的告警是大写的 `WARNING`，通知规则匹配的是告警的形式。策略对整个租户生效，`recipients` 为空，因为 webhook 没有需要指名的接收人。

现在重复[第 7 步](#7-send-uplinks)。十二条上行再次越过 25 度，规则再次产生告警，策略把它发出去。告警产生时，接收端会收到一次 `POST`：

```json
{
  "acknowledged": false,
  "alarmKey": "room-too-warm",
  "alarmToken": "e0a22e6d-08d9-4963-8124-5e672ccebdf3",
  "eventType": "RAISED",
  "metricKey": "temperature",
  "occurredTime": "2026-11-07T06:38:37Z",
  "originatorId": 1,
  "originatorType": "device",
  "raisedTime": "2026-11-07T06:38:37Z",
  "severity": "WARNING",
  "state": "ACTIVE",
  "text": "[WARNING] Alarm raised: room-too-warm on device 1",
  "value": 25.4
}
```

四条上行之后告警恢复，此处不会发送任何内容：这条策略在告警产生时发出通知，如果告警一直未被确认且策略会升级，则再次通知。服务还会为每条告警保存一份它所做之事的记录。用告警的令牌查询它，该令牌在消息里是 `alarmToken`，在[第 8 步](#8-see-the-result)的告警上是 `token`：

```bash
gql notification-management "$DC_TOKEN" 'query($t:[String!]!){notificationStatesByAlarmToken(alarmTokens:$t){alarmToken firstNotifiedAt notifyCount}}' \
  '{"t":["e0a22e6d-08d9-4963-8124-5e672ccebdf3"]}'
```

```text
{"data":{"notificationStatesByAlarmToken":[{"alarmToken":"e0a22e6d-08d9-4963-8124-5e672ccebdf3","firstNotifiedAt":"2026-11-07T06:38:37.847456Z","notifyCount":1}]}}
```

如果什么都没收到，先看这份记录：没有记录，说明告警没有到达策略（检查严重级别）；有记录但接收端没有动静，说明投递被拒绝（检查 URL 是否为公网地址）。限流、对无人确认的告警升级以及静默时段，参见[配置通知渠道](./notification-channels.md#policies)。

## 真实的网络服务器 {#real-network-server}

用 ChirpStack 或 The Things Stack 替换模拟器，DeviceChain 一侧保持你搭建的样子。变化发生在网络服务器上：

- **ChirpStack v4。** 在应用上添加 **HTTP** 集成，载荷编码选 **JSON**，事件端点为 `https://<host>/api/integrations/<tenant>/chirpstack`，请求头为 `Authorization: Bearer <token>`。设备配置文件不需要编解码器：发布了解码器之后，上行由它来解码，网络服务器解码出的内容不会被存储。
- **The Things Stack v3。** 在应用上添加 **Custom webhook**，格式选 **JSON**，基础 URL 为 `https://<host>/api/integrations/<tenant>/tts`，请求头为 `Authorization: Bearer <token>`，上行消息路径为 `/uplink-message`。出于同样的原因，应用也不需要载荷格式化器。
- **设备。** 在 DeviceChain 中为每台设备设置 `lorawan.deveui` 别名，值为它的 DevEUI；如果集成按 `DEVICE_ID` 识别设备，则设置 `tts.device-id` 别名，值为它的设备 ID。没有任何设备携带的 DevEUI 会列在集成的 **Unregistered devices seen** 下，你可以从这个列表创建设备。
- **公共证书。** 托管的网络服务器不会信任自签名证书，所以实例需要有来自公共 CA 的证书，网络服务器才能向它发送数据。你自己运行的网络服务器可以配置为信任你的 CA。

另一个变化是传感器自己的帧。本指南中的解码器读取的是模拟传感器的布局。对于真实的传感器，请根据其数据手册编写解码器，用真实的帧在 `testPayloadDecoder` 中测试，并为它发送的每一种布局保存一个向量，这样发布就能证明解码器仍能读懂它们。两个发送方逐个字段的设置见[入站集成](./inbound-integrations.md#chirpstack)，在网络服务器已解码的对象之上编写解码器的完整例子见[载荷解码器](./payload-decoders.md#example-tts)。

## 清理 {#clean-up}

规则、设备和集成都属于租户 `sim-lora`。要删除该租户及其身份：

```bash
dcctl sim destroy lora
```

如果实例也不再需要，`dcctl destroy local devicechain` 会将其删除。

## 相关内容 {#related}

- [载荷解码器](./payload-decoders.md)：解码器的每个部分、函数库、测试面板和各种拒绝。
- [入站集成](./inbound-integrations.md)：发送方、别名、存储的内容以及各项限制。
- [配置通知渠道](./notification-channels.md)：电子邮件和 webhook 渠道、策略、升级与静默时段。
- [检测规则](../user-guide/detection-rules.md)和[告警](../user-guide/alarms.md)：编写规则，以及处理它们产生的告警。
- [为什么我的设备数据没有出现](./why-isnt-my-data-showing-up.md)：平台对其所拒绝内容的记录。
