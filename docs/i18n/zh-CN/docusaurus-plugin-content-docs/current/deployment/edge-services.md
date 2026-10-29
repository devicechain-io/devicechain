---
sidebar_position: 9
title: 运行边缘服务
---

# 运行边缘服务 {#running-the-edge-services}

DeviceChain 有三个组件位于平台边缘，它们都与周围的无状态服务不同：

- [Sparkplug-B 接入](../concepts/sparkplug.md)和 [LwM2M 接入](../concepts/lwm2m.md)是**权威在线状态传输**。它们会被*明确告知*设备何时连接和断开，而不是根据静默推测。
- **边缘代理**是运行在现场设备上的独立二进制程序。到云端的链路中断时，本地缓冲；连接恢复后转发。

三者都以单实例运行，并持有数据库中不存在的实时状态，但重启代价各不相同。本页介绍实例数量及原因、故障切换损失、在线状态的保证与限制，以及应监控的内容。

了解协议功能或设备如何映射到平台，请阅读 [Sparkplug-B](../concepts/sparkplug.md)、[LwM2M](../concepts/lwm2m.md)或[设备在线状态](../concepts/device-presence.md)。

两个接入服务都**需主动启用**，不在默认功能领域集合中。两者都硬性依赖设备管理服务，由它解析产出的事件。

## 各组件单实例运行的原因 {#one-instance-each-and-the-reasons-differ}

每个组件有自己的单实例原因，并非同一策略套用三次。

| 服务 | 为什么只有一个 | 第二个实例的影响 |
|---|---|---|
| **Sparkplug 接入** | 作为 Sparkplug **Host Application（主机应用）**加入消息代理，一个 Sparkplug 环境只有一个主机应用。 | 发布冲突的主机 STATE，每条消息接入两次。 |
| **LwM2M 接入** | DTLS 是**绑定在一个 UDP 套接字上的有状态会话**。 | 备用实例也绑定套接字时，会静默接收并丢弃分配给它的报文。流量消失，不会明确报错。 |
| **边缘代理** | 独占本地缓冲目录，并使用一个云上行身份。 | 共用目录的两个实例冲突于文件锁；共用身份的两个实例反复将对方踢下上行连接。 |

两个接入服务不仅靠文档要求，还由平台强制约束：

- **所有权租约。** 每个服务获取 30 秒窗口的租约。替代 Pod 在获得租约前不连接、不绑定任何内容。
- **有界重叠。** 两个 Pod 同时服务的窗口，最多是租约窗口加一个续约间隔，约十秒。失去租约的主实例只有下一次续约察觉后才自我驱逐，随后还需要释放消息代理或 DTLS 状态。因此重叠被限制，但没有消除。
- **没有写入屏障。** 两条路径都没有阻止旧主实例写入的机制。虽然携带租约 epoch，接入路径不按它拒绝写入。
- **chart 拒绝。** 任一领域超过一个副本时 chart 拒绝渲染。限制针对领域本身，不针对发布策略，因此将 `strategy` 覆盖为 `RollingUpdate` 不能绕过。

主动结束自己的 Pod 会在退出时释放租约，替代实例启动后立即获取。Pod 判定无法继续服务时会主动结束；LwM2M 接入中包括领导任期无法建立，或 CoAP/DTLS 传输停止读取。30 秒等待只适用于**突然**丢失，没有机会释放租约的节点故障、`SIGKILL` 或内存不足终止。

:::warning 两个接入服务都没有 Pod 中断预算
chart 对单副本领域跳过中断预算，否则要求一个可用 Pod 的预算会完全阻止节点排空。因此排空边缘服务所在节点会**停止该传输**，直到 Pod 重新调度并取得租约。恢复自动进行，但不是即时恢复，应优先主动发布，而不是排空节点。
:::

## 故障切换的代价 {#what-a-failover-costs}

两个服务都将重启和领导权交接视为常规操作，并自动恢复，但恢复内容不同。

| | Sparkplug 接入 | LwM2M 接入 |
|---|---|---|
| **在线状态** | **重建。** 新主实例重新建立会话，请求边缘节点重新宣告，并核对实际存活设备，因此不会遗漏交接期间的断开。 | 从存储投影和设备注册有效期**重建**，不主动探测，避免将休眠队列模式设备误报为离线。 |
| **窗口内遥测** | 丢失。消息代理不会为未连接主机保留 DATA。 | 窗口内发送的报文丢失；可确认 CoAP 消息由设备重传。 |
| **被观察资源** | 不适用。 | **不会重新建立，见下文。** |
| **恢复时间** | 替代 Pod 调度与启动时间，加最长 30 秒租约窗口。 | 同左，再加套接字绑定时间。 |

:::danger LwM2M 观察关系在故障切换后丢失，不会自动重建
重启、发布或领导权交接会丢失所有 LwM2M 观察关系。在线状态会恢复，遥测却要等每个设备重新注册。默认设置下，**健康设备可能静默长达一天**，期间一直显示在线。参见[观察关系丢失](#lost-observations)。
:::

### 观察关系丢失 {#lost-observations}

这是本页最容易出乎预期的运维事实。

DeviceChain 要求 LwM2M 设备 **Observe（观察）**资源，使设备主动推送读数。观察关系只在建立它们的进程存活期间存在。重启、发布或领导权交接**会丢失全部观察关系，并且不会自动重建**。

在线状态恢复，但遥测不会。设备只有下次**重新注册**才重新上报。这由设备自身行为和时间安排决定，唯一边界是注册有效期。默认 `86400` 秒，意味着**完全健康的设备也可能静默一天**，同时在控制台一直显示在线。

无法容忍时，请缩短设备请求的注册有效期，代价是注册更新更频繁。降低 `maxLifetimeSeconds` **不能**缩短这段中断：设备不会得知该上限，仍按自己的时间安排更新。该设置限制的是死亡设备显示在线多久，参见 [LwM2M 设置](#lwm2m-settings)；它必须高于设备实际请求的最长有效期，否则每次交接都会把设备作为已死亡而过期。

## 在线状态保证 {#what-presence-guarantees}

每个设备携带**在线状态来源**，为 `INFERRED` 或 `ASSERTED`。相关约束比表面看起来更严格。

**只有来源能交还设备。** 权威传输首次代表设备发声时，设备变为 ASSERTED。超时、数据事件或静默都不会使其回到 INFERRED。唯一方法是**降级**：由*来源*声明不再代表设备，而不是声明设备状态。参见[恢复推断在线状态](#demoting-a-device)。之前通过 Sparkplug 或 LwM2M 上报、现在改用普通 MQTT 的设备，会保留权威来源，并继续豁免不活跃清扫。

**顺序由平台生成的会话标识决定，不使用设备提供的值。** 平台为每组连接/断开事件盖上自行生成的会话标记。Sparkplug birth/death 序列号仅用于匹配对应的出生与死亡，不比较大小，因为会回绕。LwM2M 注册 ID 也不作为会话身份。因此，两种传输上旧会话的延迟或重放消息，都不能拆除新会话。

标记来自接受连接的消息代理节点时钟。多节点集群中，新会话标记**不保证**大于前一次；时钟落后的节点可能生成更小值。平台会协调这种情况，而不是假设不会发生。发现存活设备实际会话标记小于存储标记时，将记录改为实际会话，使后续断开仍能识别。

**设备不能自行声明权威在线状态。** 普通设备载荷路径提交的连接/断开事件会直接拒绝，不是仅忽略。只有传输服务本身产出它们。ASSERTED 设备豁免不活跃清扫，允许设备自行声明在线就可能使它永久保持在线。

**推断超时固定十分钟，不可调整。** 无权威传输的设备静默十分钟后被清扫为离线，每分钟复查。目前没有逐设备覆盖或配置项。降级设备也重新受该超时约束，这正是降级的主要作用。

## 设备卡在在线状态 {#a-device-that-reads-online-and-is-not}

依赖在线状态向人发送告警前，应先了解该失效方式。

**ASSERTED 设备死亡却未宣告时，不会被超时捕获。** 不活跃清扫有意跳过 ASSERTED 设备，因为权威传输上的静默不能证明死亡。真正捕获这种设备的机制因传输而异，且每种都有可设置的上限：

1. **设备自己的传输产生新信号，或设备未能回答的询问。** DeviceChain 自身 MQTT 代理声明的设备无需主动操作：修复轮次定期比较代理活动连接与平台状态并纠正差异，参见[设备在线状态](../concepts/device-presence.md)。Sparkplug 设备同样如此：已连接的主机定期要求每个带有已静默 ASSERTED 设备的节点重新宣告，并将未回答的设备标记离线。LwM2M 设备在注册有效期到期且未刷新时标记离线。
2. **[降级](#demoting-a-device)。** 当来源已经消失，永远不会再发送信号时，应使用此方式。三个权威传输都适用。

每种传输如何限制一次从未被告知的死亡：

- **Sparkplug 死亡证书丢失。** 代理不投递节点 DEATH 时，设备持续在线直到下次存活探测。探测在主机连接代理时运行，之后在会话持续期间每隔 `presence.probeSeconds`（默认三十分钟）运行一次。每次周期探测要求上一个间隔内没有任何产出的每个 ASSERTED 设备所属节点 rebirth，等待 `presence.probeWindowSeconds`（默认十秒），然后将仍无产出的设备标记离线。间隔内有过消息的设备既不会被询问也不会被判定，且设备的任何消息也计入其所属节点，因此上报频率高于间隔的设备没有开销。静默死亡因此会在约两个间隔内被发现（默认约一小时），依据的是证据（已询问、无回答）而非静默。观察 `presence_probes_total{trigger="periodic"}`：已连接主机上它必须上升；平线表示探测未运行。

  **探测在安静的设备群上有开销。** Sparkplug 按例外上报，健康设备完全可能数小时不发任何消息。此时其节点每隔一个间隔就会被要求 rebirth：节点重新发送自身及所有设备的全部指标值，这些值作为读数存储（birth 不计入租户接入上限），且每个设备记录一次新的连接。节点不会被同时询问：间隔被切分为至少两个应答窗口长的时段，每个节点始终在同一时段被询问（由其分组与节点 ID 决定），因此安静设备群的 rebirth 分散在整个间隔内到达。每个时段仍会为自己的节点等待完整窗口，再开始下一个时段。如果开销对你的设备群仍然过多，请调大 `presence.probeSeconds`；检测上限随之增大。节点重新宣告某个设备若超过应答窗口，该设备会被标记离线，直到其 birth 到达，到达后会自动恢复在线；对慢速链路上的大型节点，请调大 `presence.probeWindowSeconds`。
- **节点重新宣告。** 边缘节点建立新会话时，旧会话子设备也一并替换。未在新会话中重新宣告的子设备继续显示连接，直到下一次周期探测要求其节点 rebirth，仍未出现时标记离线。
- **LwM2M 注册过长。** 消失设备在注册有效期结束时标记离线。默认 **86400 秒**，即整整一天。`maxLifetimeSeconds` 将每次注册有效期最多限制为该值，直接限制死亡 LwM2M 设备显示在线多久；例如 3600 将上限设为一小时。值必须高于设备群实际请求的最长有效期。
- **权威传输被移除。** 停用设备使用的 Sparkplug 来源或 LwM2M 凭据后，不再有任何来源发声，也不会为不再配置的来源运行探测。设备停留在最后权威状态，直到有人[释放它](#demoting-a-device)。该操作正是为此设计，因为消失的来源不再通知平台。

三个上限并列：平台代理的 `brokerPresence.reconcileSeconds`、Sparkplug 的 `presence.probeSeconds`、LwM2M 的 `maxLifetimeSeconds`。第一个默认五分钟，第二个默认三十分钟，因为询问 Sparkplug 节点需要一次完整 rebirth；第三个默认一天，因为它是设备自己的注册有效期，而非平台设置。如果在线状态对运维至关重要，仍可将连接信号与基于超时的[数据缺失规则](../concepts/event-processing.md)配对；它按静默触发，不受在线状态显示影响。

### 恢复推断在线状态 {#demoting-a-device}

**降级**是从 `ASSERTED` 回到 `INFERRED` 的唯一转换。这是来源释放管理权，不是描述设备本身，不声明连接状态。设备是否在线、最近连接、最近断开和最近上报时间都保持原样。

改变的是哪些机制可以纠正状态。ASSERTED 设备抑制平台两种修复机制；释放后交还给它们，同时修复两种冻结：

- **冻结在线。** 重新进入不活跃清扫，在真实最近活动十分钟后标记离线。
- **冻结离线。** 指令不再被暂扣。暂扣条件是设备为 ASSERTED *且*非活动，因此 INFERRED 设备可以派发指令。定期复查暂扣集合的轮次会在几分钟内释放积压，下次读数使设备重新显示在线。应及时修复，因为暂扣指令计入[每租户上限](../concepts/commands.md#held-command-ceiling)，来源离去导致离线卡住的设备，最终可能使同租户健康设备也无法入队。

降级有两条路径：来源释放自己的设备，或运营方代替已离去来源释放设备。

#### 来源关闭时自动释放 {#a-source-releases-its-own-devices-when-it-is-switched-off}

消息代理声明的 MQTT 在线状态依赖*在线状态监听连接*：`event-sources` 连接代理系统账户，接收连接与断开通知。监听因以下原因无法启动时，`event-sources` 遍历仍被其声明为 ASSERTED 的设备并释放：

- 被**明确禁用**；
- **缺少 NATS 系统账户凭据**；
- **消息代理不可达**。

这是六种启动失败原因中的三种，划分有明确依据。前两种属于配置；实例所有副本读取相同值并得出相同结论，所以释放代表实例，而不是单个副本猜测。

第三种证据不同。监听先尝试连接 30 秒，才报告代理不可达，因此意味着半分钟没有系统账户连接，而不是单次失败，可能代理停机或拒绝凭据。MQTT 网关也位于同一代理；代理不可达时，没有设备能通过它连接。释放不是对设备群的猜测，而是与代理消失一致的唯一判断。

三者中只有第三项可能在 Pod 运行期间改变，所以只有它持续复查。每次释放轮次都先重新连接系统账户，包括第一轮。**代理响应时，不释放任何设备**：服务退出并重启 Pod。替代实例正常连接并运行监听。因此代理恢复表现为 Pod 重启，而不是设备群被释放。如果不复查，释放会按协调间隔持续遍历*当前* ASSERTED 设备；其他副本持续声明时，两者会无限轮流修改每一行，中间不活跃清扫可能将已连接但安静的设备标记离线。

以下情况**不释放**：

- 已成功连接代理，但订阅失败。这只是该副本的问题，其他副本可能正常接收通知。
- 实例根本没有监听可运行的两种原因：没有指向平台代理的来源，或没有服务间配置。

六种原因都会设置 `presence_tap_off{reason}`，可据此判断具体情况。

监听运行后，如果代理永久关闭连接，服务应重启，而不是关闭监听。如果代理不再接受系统账户凭据，各 `event-sources` Pod 会在大致同一时刻健康检查失败，由 Kubernetes 重启。重启期间 HTTP 接入不可用，MQTT 遥测在代理中等待。每个重启 Pod 使用挂载凭据连接；仍被拒绝时，监听以 `broker_unreachable` 原因关闭，并执行上述释放。

依赖自动释放前，应了解三点：

- **缺少凭据和代理不可达会先等待两分钟。** 初始化会在启动服务的同一次运行中生成凭据并滚动代理，因此两者可能只是竞态。明确的 `enabled: false` 没有歧义，立即生效。代理等待后还会**复查**；凭据无法如此复查，因为配置只在启动读取一次，变更会滚动 Pod。
- **需要自己的网关来源和服务间配置**，分别提供发布身份，以及枚举租户、读取投影的方法。缺少它们时完全不运行，会记录日志并指向手动释放，此时手动操作是唯一方法。
- **限速且集合会自动减少。** 每秒释放 25 台设备，释放设备退出遍历集合，因此中断后自然继续，不需从头开始。`presence_still_asserted` 表示剩余数量；健康释放会将其降至零并保持。

Sparkplug 和 LwM2M 设备不会自动释放。这些来源消失是因为运营方移除了它们，不是标志改变，因此应由运营方释放。

#### 手动释放 {#an-operator-releases-them-by-hand}

`dcctl presence demote` 遍历某租户中一个来源的 ASSERTED 设备，并逐一释放：

```bash
dcctl presence demote --tenant acme --source sparkplug:plant-a \
  --email ops@acme.example --password "$DC_PASSWORD" \
  --reason "plant-a gateway decommissioned"
```

| 标志 | |
|---|---|
| `--tenant` | 必填。一次降级仅作用于一个租户。 |
| `--email` / `--password` | 必填。操作所授权的身份，必须是租户成员或超级用户。 |
| `--server` | API 调用的实例主机，默认 `localhost`。HTTPS 添加 `--tls`。 |
| `--source` | 必填，绝不推断，因为影响范围是整个事件来源。按设备状态报告的精确值填写：MQTT 和 HTTP 使用来源配置 ID（`mqtt1`、`http1`），Sparkplug 使用 `sparkplug:{hostId}`，LwM2M 使用 `lwm2m`。 |
| `--device` | 可重复。在该来源*内部*限定设备；省略则释放整个来源。 |
| `--reason` | 必填。写入本次运行产生的每个事件，是设备群在线状态写入的唯一记录。 |
| `--page` | 每次调用的设备数，默认 `200`。 |
| `--dry-run` | 仅报告将释放的设备，不实际释放。 |
| `--yes` | 跳过实际运行的确认提示。没有终端可确认时，除非指定该项，否则拒绝实际运行。 |

无人使用的来源不是错误，只是不匹配任何设备。但第一页匹配零台设备，比起工作已完成，更可能是 `--source` 拼错。命令会说明这一点，而不报告成功。

API 中的等效操作是 `device-state` 的 `demoteAssertedPresence` mutation，需要 `state:demote` 权限。这是写权限，不包含在所有成员默认获得的只读基线中。初始 `tenant-admin` 角色具有所有租户权限，因此包含该项；其他需要的角色应明确授予。

#### 释放计量 {#a-release-is-metered-like-any-other-presence-event}

释放与连接、断开一样，受每租户[接入上限](../concepts/governance.md)约束。已达到上限的租户，其*修复*也可能与引起压力的连接变化一起被拒绝，并计入 `presence_events_refused_total`。不会丢失：被拒绝设备保持 ASSERTED，下一轮仍会发现。修复只能按限额允许的速度推进。

## 两种传输的租户归属 {#tenancy-on-both-transports}

**两种传输都由连接确定租户，不读取设备提供的内容来决定。** 这是该部分最强的隔离性质。

- **Sparkplug。** 每条消息归属于它所到达的**消息代理连接**配置的租户。主题中的 group ID 是客户标签，不全局唯一，任何发布者都可设置，因此不用于确定租户。配置拒绝同一代理端点对应两个租户，否则 group ID 就会成为唯一隔离依据。
- **LwM2M。** 设备由握手时提供的**已认证 DTLS 预共享密钥身份**绑定租户。设备注册载荷中自报的端点名不用于身份。未预配身份握手失败，拒绝响应不回显身份，防止探测者比较错误来枚举有效凭据。

:::caution Sparkplug 设备认证位于消息代理层
`deviceAuthMode: required` 下，两种传输都提供可信设备身份，无需第二份逐事件凭据。LwM2M 身份绑定 PSK，因此逐设备认证；Sparkplug 身份来自主题，因此强制设备认证*不能*阻止发布者在**同一租户内**冒充另一设备。参见 [Sparkplug 设备认证](#device-authentication-on-sparkplug)。
:::

### Sparkplug 设备认证 {#device-authentication-on-sparkplug}

两种传输都将流量标记为经过传输认证，因此平台在 `deviceAuthMode: required` 下无需逐事件凭据即可信任设备身份。LwM2M 身份绑定已认证 PSK，是真正的逐设备身份。

Sparkplug 身份从主题推导，认证粒度只到**消息代理连接**。启用强制设备认证，*不能*阻止租户代理上的发布者以同租户另一设备身份发送。两条路径都阻止跨租户访问。若需要租户内设备身份隔离，应在**自己的消息代理**通过逐客户端凭据和主题权限实施，因为真正的边界在那里。

### 边缘传输的设备标识 {#device-identifiers}

:::danger 三个标识都不是你输入的那个名字
边缘传输上的设备有三个标识，分别承担不同工作。混淆会导致静默失败或错误路由。自动预配设备**没有名称**，控制台也不能搜索它。预配前请阅读本节。
:::

其中两个看起来像名称，第三个是生成值，因此很容易混淆。

| 标识 | 作用 | Sparkplug | LwM2M |
|---|---|---|---|
| **1. 租户锚点** | 决定数据属于哪个租户，绝不使用消息内容，见上文。 | **消息代理连接**。 | **已认证 PSK 身份**。 |
| **2. 设备解析键** | 决定*哪台设备*，即设备**外部 ID**，设备不能自行选择。 | 主题的 `group/node[/device]` 字符串，例如 `plant-a/line-3/press-1`。 | **服务配置中 PSK 身份旁边填写的外部 ID**，不是设备发送的端点名（`ep`）。 |
| **3. 设备令牌** | 控制台、API 和每个事件实际使用，**自动生成**而非自选。 | `sp-…`，例如 `sp-plant-a-line-3-press-1-9f2c1a8b4d3e`。 | `lw-…`。 |

LwM2M 的 `ep` 只记日志，其他用途忽略。固件发送 `ep=urn:imei:35…` 的设备不会按它匹配，也不会明确提示。

生成令牌是因为外部 ID 常包含 `/`、`.`、空格或非 ASCII 字符，而令牌不允许这些内容。因此 `plant-a/line-3/press-1` 转为类似 `sp-plant-a-line-3-press-1-9f2c1a8b4d3e` 的值。后缀区分规范化后相同的两个外部 ID。

实际后果是，自动预配设备**完全没有名称**。注册仅携带令牌、外部 ID 和设备类型。控制台设备列表因此以生成的 `sp-…` / `lw-…` 令牌显示，Name 列为 `—`，没有其他易识别名称。

也不能搜索。控制台设备列表**没有搜索框**，只是 Status、Token、Name、Type、Description 和 Created 的分页列表，没有外部 ID 列。API 设备搜索仅接受页码、页大小和设备类型。查找设备时：

- **控制台中**按令牌找。生成令牌包含外部 ID 的变形（`plant-a/line-3/press-1` → `sp-plant-a-line-3-press-1-…`），翻页读取 Token 列就是查找方式。
- **API 中**使用 `devicesByExternalId`，接受精确外部 ID 并返回设备。它是精确匹配查找，不支持前缀或子字符串；控制台未调用它，所以仅 API 可用。

设备完全不出现时，先检查第二个标识，再怀疑传输。尤其是 LwM2M，PSK 身份错误会在注册前的 DTLS 握手失败，拒绝响应有意不透露原因。

## LwM2M 运维 {#lwm2m-what-an-operator-must-know}

**仅解码 SenML-JSON 遥测。** 其他内容格式的通知被计数并丢弃。实际后果不容易从标准本身发现：

:::warning 仅支持 LwM2M 1.0 的合规客户端有在线状态和指令，但没有遥测
SenML 在 LwM2M 1.1 才引入。仅支持 1.0 的设备可注册、维持会话、更新在线状态并接受 Read/Write/Execute 指令，却**不会产生任何测量值**。不会明确失败。应检查 **`observe_establish_refused_total`**，而不是 `notify_unknown_content_format_total`。参见[仅支持 LwM2M 1.0 的客户端](#lwm2m-10-only-clients)。
:::

### 仅支持 LwM2M 1.0 的客户端 {#lwm2m-10-only-clients}

此类设备可以注册、保持会话、正确更新在线状态并接受 Read/Write/Execute 指令，但不会产生任何测量值，也不会明确报错，读数只是始终不出现。

应检查 **`observe_establish_refused_total`**。DeviceChain 在 Observe 请求中要求 SenML-JSON，所以合规的 1.0 客户端用 `4.06 Not Acceptable` 拒绝 Observe，此后根本不发送通知。该拒绝在此计数，也是主要原因。设备的 `notify_unknown_content_format_total` 保持 **零**，因为它统计另一种情况：设备确实通知，但格式适配器无法解码。

### 观察限制 {#observation-limits}

**观察关系有固定且不可配置的限制。** DeviceChain 仅对固定 IPSO 范围内的对象建立观察，每个对象*实例*一条，每次注册最多 **32 条**。对象允许列表是**构建的固定属性，配置无法扩展**。范围之外的资源不会被观察，任何配置都不能改变。超过每次注册上限时，监控 `observation_overflow_total`。

### 会话回收 {#session-reaping}

**默认不回收会话。** `idleTimeoutSeconds` 默认为 `0`，表示永不回收，适用于始终连接设备。**队列模式**设备群应设置为明显大于预期唤醒间隔。太短会在休眠期间驱逐会话密钥，迫使重新握手，失去 DTLS Connection ID 避免握手的意义。

### 暴露 LwM2M 端口 {#exposing-the-lwm2m-port}

:::danger 默认没有任何组件将 LwM2M 端口暴露到集群之外
面向设备的 CoAP/DTLS 端口是 **UDP 5684**，**chart 和基础设施模块都不将它暴露到集群外**。按默认安装，真实 LwM2M 设备群无法访问服务。需要自行提供 UDP 路径。
:::

所有服务都仅位于集群内部，没有会话亲和性配置，默认 ingress 控制器只处理 HTTP。

外部暴露明确由运营方决定，**没有随平台发布的实现**。自行提供 UDP 路径，例如 `LoadBalancer` 或 `NodePort` 服务，或外部 UDP 代理，必须确保同一会话的每个报文都到达实际服务的那个 Pod。

### LwM2M 设置 {#lwm2m-settings}

| 设置 | 默认值 | 作用 |
|---|---|---|
| `listen.port` | `5684` | CoAP/DTLS 服务绑定的 UDP 端口。 |
| `security.connectionIdLength` | `8` | DTLS Connection ID 字节长度。蜂窝或漫游设备群应**保持非零**，使会话在地址变化后存活。`0` 禁用，每次重新绑定都必须握手。 |
| `security.idleTimeoutSeconds` | `0` | 无流量多久后回收会话。`0` 永不回收。队列模式应高于唤醒间隔。 |
| `security.handshakeTimeoutSeconds` | `10` | 单次 DTLS 握手上限，避免停滞握手长期占用资源。 |
| `security.maxSessions` | `100000` | 活动会话表上限。超限握手被拒绝并计数，不静默接受。 |
| `maxLifetimeSeconds` | `86400` | 所有注册有效期的上限。**直接限制死亡设备显示在线多久。** 必须高于设备请求的最长有效期。 |
| `ingestRateLimit.readingsPerSecond` | `1000` | 每租户持续接入上限，按解码读数计数，也在解码前按 Notify 消息计数。未设置或非正值使用默认，不表示无限制。 |
| `ingestRateLimit.burst` | `2000` | 上述限额的突发额度。 |
| `downlink.timeoutSeconds` | `10` | 单次设备指令交换时间上限。到期报告失败，不悬而未决。慢速蜂窝休眠设备可提高。 |
| `downlink.concurrency` | `16` | 跨设备指令并行度。无论此值多少，同一设备自己的指令始终按序执行。 |

## Sparkplug 运维 {#sparkplug-what-an-operator-must-know}

**每个来源都是独立对外连接。** 来源指定一个消息代理、一个租户和订阅分组。不可达代理使用独立退避循环重试，只影响**该来源**，不影响 Pod 或其他租户来源。监控 `connect_failures_total`。

应依据该指标，而不是 Pod 健康状态判断来源连接。

**一个分组订阅被拒绝，会停止整个来源，直到修复消息代理 ACL。** 来源为全部分组发布一个统一在线/离线状态，不能部分在线。代理可能接受连接却拒绝某分组订阅，通常因为来源凭据无读取权限。此时来源：

1. 不宣告在线；
2. 不接入任何分组；
3. 断开，并使用同一退避循环重试，最大间隔 30 秒。

缺少分组却宣告在线更糟：该组边缘节点会向不存在的订阅刷新缓冲数据，随后来源因静默而将设备标记断开。断开前，来源自行发布离线状态，因为正常断开不触发遗嘱。因此代理已存储却未确认的在线宣告会被替换，不会残留。监控 **`subscribe_failures_total`**：任何增长都表示某来源停机，日志会列出被拒绝分组。

**未能注册 BIRTH 的节点会被请求重新 BIRTH。**当 BIRTH 无法注册（通常是来源的设备类型尚不存在）时，节点会被记住，并按逐步加长的间隔收到 `NCMD` `Node Control/Rebirth`，直到注册成功。请监控 **`birth_recovery_pending`**：持续大于零表示有节点或整个来源没有注册，error 级别的日志会指出设备类型。创建该类型后，该指标会自行回落到零。

**重新连接由平台处理，不交给 MQTT 客户端库。** 每次重连建立真正全新的会话和时间戳。Sparkplug 要求主机 birth 与 death 证书使用相同时间戳，使边缘节点拒绝旧会话延迟死亡。这也是所有副本共用一个客户端 ID 的原因：代理的重复 ID 接管负责驱逐僵尸主机。

:::caution Sparkplug 路径不限制消息速率或消息总大小
Sparkplug 按租户接入上限计量 DATA 消息中的读数，并丢弃超出部分。不计量消息速率、birth 或 death；消息总大小只受代理限制。参见 [Sparkplug 接入限制](#unbounded-sparkplug-ingest)。
:::

### 向 `dcctl` 提供 Sparkplug 来源 {#sparkplug-sources}

Sparkplug 来源既是连接配置也是凭据，因此 `--values` 覆盖文件会拒绝它。请改为以文件形式交给 `dcctl`，用于 `bootstrap`（同时启用 `sparkplug-ingest` 区域）或 `upgrade`（该区域必须已在运行；`dcctl enable-area` 可启用它）：

```yaml
# sources.yaml
- tenant: acme
  hostId: devicechain
  broker:
    url: ssl://broker.acme.example:8883
    username: devicechain
    password: the-broker-password      # 或 passwordFile: /path/to/file
  groups: [plant-a]
  autoRegister: true
  deviceTypeToken: sparkplug-pump      # 请在来源连接前创建，或参见下面的说明
  commands:
    enabled: true
    hostRole: primary
    writableMetrics: ["Outputs/*"]
```

```bash
dcctl bootstrap local acme --enable-area sparkplug-ingest --sparkplug-source sources.yaml
dcctl upgrade   local acme --sparkplug-source sources.yaml
```

文件在接触集群之前就会被检查（缺少租户、没有主机的代理 URL、未带设备类型的 `autoRegister`、同一代理上的两个租户、未知键）。代理密码被渲染到由 chart 拥有的 Secret 中，并作为环境变量投射到 Pod；区域配置中的来源只引用该变量名，从不保存密码，`dcctl` 也不会打印二者。来源和 Secret **会被之后的每次 `dcctl upgrade` 保留**，再次提供文件的运行会替换它们（更换密码会滚动 Pod）。不带该选项重新运行 `dcctl bootstrap` 会撤销它们。这些键与下文所述 chart 的 `sources` 列表相同。

:::note 尚不存在的设备类型
节点以来源的 `deviceTypeToken` 注册。如果设备类型在来源连接之后才创建，平台会请求未能注册的节点重新 BIRTH，直到它们注册成功；参见[先于设备类型出生的节点](../concepts/sparkplug.md#birth-recovery)。
:::

### Sparkplug 命令 {#sparkplug-commands}

[Sparkplug 命令](../concepts/sparkplug.md#commands)所述的命令下行通道**需要主动启用，默认关闭**。它按来源配置：写在交给 `dcctl` 的文件的 `commands` 块中（见[上文](#sparkplug-sources)）；如果你自己管理 Helm release，则写在 chart 的 `functionalAreas.sparkplug-ingest.config.sources` values 中，如下所示。

```yaml
functionalAreas:
  sparkplug-ingest:
    config:
      sources:
        - tenant: acme
          hostId: devicechain
          broker:
            url: ssl://broker.acme.example:8883
            username: devicechain
            passwordEnv: SPARKPLUG_ACME_PW
          groups: [plant-a]
          commands:
            enabled: true
            hostRole: primary            # 必填：primary 或 secondary
            writableMetrics:             # 为空（默认）则不允许任何写入
              - "Outputs/*"
            allowReboot: false
```

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `enabled` | `false` | 为该来源启用下行通道 |
| `hostRole` | 无，必填 | `primary` 或 `secondary`，参见[主主机与次主机](../concepts/sparkplug.md#primary-and-secondary-host) |
| `writableMetrics` | 空 | `sparkplug.write` 可以针对的指标：精确名称，或以 `/*` 结尾的前缀。为空表示**不允许**任何写入。`bdSeq` 以及 `Node Control/`、`Device Control/` 指标不能列入 |
| `allowReboot` | `false` | 允许 `sparkplug.reboot` 到达该来源的节点 |
| `confirmWindowSeconds` | `120` | 写入后等待上报的时间（5 到 900）。链路较慢的节点请调大 |
| `rebootWindowSeconds` | `600` | 重启后等待节点新 birth 的时间，也是同一节点两次重启之间的最短间隔（30 到 3600） |
| `clockSkewSeconds` | `30` | 节点自身时间戳可以早于发布时间多少仍算有效（0 到 300） |
| `publishPerSecond`、`burst` | `50`、`200` | 该来源每秒最多发出的命令数及突发量 |

服务在启动时强制执行下列规则，出错时会带着消息停止，而不是发出非预期的内容：

- **每个代理一个来源。**启用命令的来源必须是其代理端点上唯一的 Sparkplug 来源，这样节点绝不会被重复下发。请把各分组合并到一个来源。
- 启用命令时**必须提供 `hostRole`**。
- **必须部署 `command-delivery`。**chart 会根据来源自行设置 command-delivery 的 `sparkplugCommands`，当来源启用命令而 `command-delivery` 未启用，或集群内 `command-delivery` 地址为空时，渲染会失败。该设置不能手动设置。

应用设置并滚动重启之后，在设备配置文件上声明这些命令（控制台的**添加 Sparkplug 标准命令**）并发布。发往未启用命令的来源的命令会立即以未投递失败。

#### 代理 ACL {#sparkplug-commands-acl}

除了已有的读取权限和发布 `STATE` 的权限外，来源的凭据还需要**发布**命令的权限。对 Mosquitto，在 ACL 文件中为来源的 `username` 及其分组设置：

```
user devicechain
topic read spBv1.0/plant-a/#
topic write spBv1.0/STATE/devicechain
topic write spBv1.0/plant-a/NCMD/+
topic write spBv1.0/plant-a/DCMD/+/+
```

其他代理用各自的语法表达同一条规则：允许在 `spBv1.0/<group>/NCMD/<node>` 和 `spBv1.0/<group>/DCMD/<node>/<device>` 上发布。静默丢弃被拒绝发布的代理会使命令停在 `TIMEOUT`；在发布被拒绝时断开客户端的代理会打开断路器，触发 `SparkplugCommandBreakerOpen`。如果所有命令都以未确认结束，会触发 `SparkplugCommandsUnconfirmed`，首先应检查凭据的发布权限。

### Sparkplug 接入限制 {#unbounded-sparkplug-ingest}

**读数消耗租户接入限额。** NDATA 或 DDATA 中每个数值指标算一个读数，使用与其他设备传输相同的按读数计量的租户上限（参见[默认允许量](../concepts/governance.md#ingest-default)）。每条消息在存储前计费一次，处理器内部重试不重复计费。超限读数丢弃并计入 `ingest_samples_shed_total`。clean-session 主机没有代理重投递，因此丢弃即丢失，与其他传输超限读数相同。

| 设置 | 默认值 | 作用 |
|---|---|---|
| `ingestRateLimit.readingsPerSecond` | `1000` | 每租户持续接入上限，按读数计量，租户层级可覆盖。未设置或非正值使用默认，不表示无限制。 |
| `ingestRateLimit.burst` | `2000` | 上述限额的突发额度，单位读数。 |

**未计量的内容。** 主机必须观察每条消息以保持会话正确，因此不计量消息速率。birth 的指标值也不计费：Sparkplug 按变化上报，缓慢变化指标可能只在 birth 出现；重连或故障切换后的 rebirth 会一次重发所有节点指标。birth 改由会话限制，每个 birth 序列一次，请求 rebirth 使用退避。birth/death 声明的在线状态转换也不计量。失控边缘节点仍可按代理投递速度持续发送，应在代理或订阅分组范围上限制。

**平台施加背压时也丢弃读数。** 平台拒绝新事件期间（参见[接入链路背压](./observability.md#ingest-backpressure)），消息中尚未存储的读数立即丢弃，不进行处理器内部重试，计入 `ingest_failures_total`。包括不受限额丢弃的 birth 指标值；缓慢变化指标这样丢失后，要等下次变化或节点 rebirth 才恢复。birth/death 的连接和断开转换仍接受。

**发布按窗口进行，窗口同时也是丢失上限。** 每个连接按顺序逐条读取消息，因为主机必须看到每条消息才能保持会话正确。它不会等平台存储完一条消息再读下一条，而是保持一个“窗口”：同时有多条消息等待平台代理确认。以前它要逐条等待，因此一个连接最多每次确认处理一条消息，在有复制的代理上（一次确认约 11 毫秒）约为每秒 88 条，而所有 Sparkplug 来源都运行在唯一的主 Pod 上。发布失败会重试，最多共三次，间隔半秒；之后消息被丢弃，并计入 `ingest_failures_total`，与以往一样。停止或把主角色交给其他实例时，会先等待在途消息存储完成（最多 8 秒），再释放租约。

| 设置 | 默认值 | 作用 |
|---|---|---|
| `ingest.publishWindow` | `32` | 一个 Sparkplug 连接同时等待平台代理确认的消息数，范围 1 到 1024。超出范围或显式写 0，服务启动时即停止，不会被调整。发布速度不会超过该数值除以一次确认耗时（窗口为 32 时，每个连接约每秒 2,800 条）。固定设置，启动时读取，对所有来源相同。**它同时决定 Pod 最多还会多丢多少。**主机是 clean-session 客户端，一取走消息就向你的代理确认，而代理不会向已重启的主机重新投递，因此突然停止的 Pod 会丢失已取走但尚未存储的消息：每个连接最多该数值条，以前是一条；仅在发布持续失败期间，还要加上等待重试的消息（最多约再一个窗口）。新的主实例通过 rebirth 恢复各节点的在线状态和指标当前值，但不会恢复期间丢失的读数。边缘节点的存储转发覆盖主机离线的时段，不覆盖已经投递给随后崩溃的主机的消息。可观察 `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="sparkplug"}`：速率接近 1 表示窗口是发布的瓶颈。 |
| `presence.probeSeconds` | `1800` | 已连接主机重新检查其 ASSERTED 设备存活的间隔。每个间隔一次，平台认为已连接、但上一个间隔内没有任何产出的每个设备所属节点都会被要求 rebirth；应答窗口（`presence.probeWindowSeconds`）后仍静默的设备标记离线。节点分散在整个间隔内，各自在固定位置被询问，而不是同时询问。间隔内有过消息的设备既不会被询问也不会被判定，且设备的消息也计入其所属节点，因此只有带静默设备的节点会被询问，最多每隔一个间隔一次。每次询问都会让该节点重新发送全部指标值，并为其每个设备记录一次新的连接；参见[设备卡在在线状态](#a-device-that-reads-online-and-is-not)中关于开销的说明。这正是捕获代理从未投递的死亡证书（主机保持连接时）的机制；参见[设备卡在在线状态](#a-device-that-reads-online-and-is-not)。未设置或 0 表示 1800。写入低于 60 的值会在启动时停止服务，不会被调整。没有关闭开关：对周期性 rebirth 代价高的设备群应将其设长。固定设置，启动时读取，对所有来源相同。 |
| `presence.probeWindowSeconds` | `10` | 探测要求节点 rebirth 后，等待其自身及每个设备 birth 应答的时长，超时后将未收到消息的设备标记离线。连接时运行的探测同样适用。比该值更慢的节点，其设备会被标记离线，直到 birth 到达后自动恢复在线，因此慢速链路上的大型节点应调大此值。未设置或 0 表示 10。写入低于 5 的值，或不小于 `presence.probeSeconds` 的值，会在启动时停止服务，不会被调整。固定设置，启动时读取，对所有来源相同。 |

**单事件读数限制通过拆分执行。** 此路径的[每事件读数上限](../guides/connecting-a-device.md#how-much-one-message-may-carry)拆分消息，而不是拒绝。包含数千指标的 DDATA 变为多个事件，每个指标仍对应一条存储读数，各自需要行写入、状态更新和共享引擎上的规则评估。拆分限制单个事件，限额限制每秒读数。单条消息同样有上限：16,384 个指标。超过上限的消息不存储任何读数，其指标计入 `samples_skipped_total{reason="oversize"}`，服务记录一条指明主题的警告日志。该消息仍计入会话，因此超过上限的 birth 仍会连接节点并声明其别名，节点之后的 DATA 照常读取。应在边缘节点限制每次发布指标数。

[租户生命周期门控](./tenant-deletion.md)仍适用。删除中租户流量与其他路径一样拒绝，计入 `tenant_deleted_dropped_total`。

### 未知身份 {#unknown-identities}

**如何处理未知身份由配置决定。** 启用自动注册时，无对应设备的 Sparkplug 身份会创建设备；关闭时丢弃遥测，并计入 `unknown_device_dropped_total`。边缘节点正在发布却没有数据显示时，检查该指标。

## 边缘代理 {#the-edge-agent}

边缘代理**不是第四条接入路径**。它运行在现场，为本地设备提供普通 MQTT 端点，将发布内容缓冲到本地磁盘，再以[网关](#gateway-identity)身份发布到云端：它是一台有自己身份的普通设备，平台只允许它代表你分配给它的设备发言。平台端没有专门针对代理的服务，云服务中也没有专门针对代理的配置。

**它不是 chart 功能领域。** 不出现在领域列表或部署配置中，不能像服务一样启用。它作为静态二进制和容器镜像发布，由你自行部署，例如现场网关 systemd 单元、容器或边缘手写 Kubernetes 清单。

**每个 [GitHub 发布版本](https://github.com/devicechain-io/devicechain/releases)都附带这些二进制文件**，文件名为 `dc-edge-agent_<version>_<os>_<arch>.tar.gz`，提供 Linux 和 macOS 的 `amd64` 与 `arm64` 版本。其 SHA-256 摘要位于同一发布版本的 `dc-edge-agent_checksums.txt` 中，在 Linux 上可用 `sha256sum --ignore-missing -c dc-edge-agent_checksums.txt` 校验下载的文件。Linux 是边缘部署目标；macOS 版本用于在工作站上试用代理。不提供 Windows 版本。

### 网关身份 {#gateway-identity}

代理是一个**网关**：平台上的一台普通设备，代表其他设备发言。它以自己的身份登录，使用自己的设备凭据，并在自己的命名空间 `{instanceId}/{tenant}/gateways/{gateway}/devices/{device}/events` 中发布每台本地设备的事件（参见[网关命名空间](../reference/device-protocol.md#gateway-namespace)）。平台只接受**分配给该网关**的设备的事件。这是唯一的上行模式。

**设置方法：**

1. 创建网关设备（任意设备类型），并为它创建一个 `MQTT_BASIC` [凭据](../guides/device-credentials.md)。设备令牌就是代理的 `uplink.gatewayToken`。凭据 id 以 `{tenant}:{credentialId}` 的形式填入 `uplink.username`，其密钥放入 `uplink.passwordEnv` 所指的环境变量。
2. 创建现场所代表的设备，并把每台设备分配给该网关，可使用 `assignDeviceGateway`，或在创建时指定 `gatewayToken`。一台设备最多有一个网关，网关本身不能再有网关。分配需要与管理设备凭据相同的权限。
3. 启动代理。本地设备继续向它发布各自的主题，无需改动。

代理使用上面的 `MQTT_BASIC` 凭据登录。它目前还没有客户端证书设置，所以网关现在还不能通过[证书](../concepts/device-certificates.md)认证。

分配在该设备的下一个事件生效，数秒内完成，无需重连。清除分配同样如此，网关的其他设备不受影响。停用网关或吊销其凭据会结束它的会话。

**代理在上行方向做什么：**

- 它使用网关设备的普通 MQTT 客户端 id 连接，因此消息代理像对待任何设备一样断言网关自身的在线状态。没有按代理区分的客户端 id：两个代理使用同一台网关设备会互相抢占会话。
- 它只用子设备令牌构造每个上行主题，因此无法在自己的命名空间之外发布。消息代理无法拒绝超出授权的发布（MQTT 3.1.1 没有这种机制），而是确认后丢弃。无法构造这样的主题，正是消除这一故障的办法。
- 它只捕获所配置租户的设备事件。指向其他租户的发布会计入 `tenant_mismatched_total`，且从不缓冲。
- 转发前，它会从事件正文中移除 `credentialType`、`credentialId` 和 `credentialSecret`，并计入 `body_credentials_stripped_total`。在经消息代理认证的主题上，平台会拒绝携带其中任何一项的事件。请修改设备，使其不再发送。
- 它最多保持 **64 个发布在途**，并按各自的确认逐个确认缓冲事件。如果每发一条都等待确认再发下一条，每次往返只能发一个事件，50 ms 时约每秒 20 个，繁忙的现场在中断后永远追不上。重放的事件保留首次存储的时间，云端仍会合并重复项。
- 每次连接时，它都会订阅网关的通知主题。如果消息代理拒绝该订阅，上行保持断开，日志会说明原因。

**事件被拒绝时。** 对于未分配给该网关或不存在的设备的事件，平台会拒绝。它把事件连同载荷记录到 `failed-events` 流，原因为 `NotGatewayChild`，计入 `resolve_not_gateway_child_total`，并向网关发送一条*通知*。消息代理已经确认了该发布，因此没有别的途径能告诉代理。收到通知时，代理会记录一条指明设备的警告（每台设备每 30 秒最多一条），计入 `refused_total{reason="not_gateway_child"}`，并把事件写入存储目录中的 `refused-events.jsonl`。该文件仅对代理的用户可访问，每行一个 JSON 对象，达到 16 MiB 时轮转一次。

有三点限制。通知是尽力而为的：发送时离线的网关收不到它，因此可以依赖的是平台的记录和计数器。代理只保留所转发内容的一份有限副本（最近 8192 个事件，且最多 16 MiB）用来匹配通知，因此副本被淘汰后才到达的通知会计入 `refused_unmatched_total` 并记录日志，但不会保留。该文件保存的是转发时的载荷，凭据已被移除。修正分配后恢复被拒绝的事件需要手动重放。

代理还会在不发送的情况下拒绝平台只可能拒绝的事件：本地设备使用了网关自己的令牌（`reason="gateway_self"`），或使用了平台无法存储的令牌（`reason="invalid_device_token"`）。它们被写入同一个文件。

:::warning 在把代理切换到网关上行之前，先升级平台
先完成平台升级：每个 `event-sources` 和 `device-management` 副本，以及消息代理的授权。开启 `infrastructure.nats.mqttFailClosed` 时，较旧的 `event-sources` 会确认无法归属的网关消息并丢弃它们，于是代理看到成功，而实际上什么都没有存储。消息代理尚未升级的代理无法订阅其通知，也不会报告上行已连接（`uplink_connected` 保持为 0）。
:::

**网关的可信范围。** 网关代表分配给它的设备发言。被攻陷的网关只能伪造分配给它的设备的事件，并且只在它被停用或其凭据被吊销之前有效。它所称事件来自*哪个本地客户端*，其可信度只取决于现场本地认证的强度。

目前尚不路由发往网关后设备的命令，代理只转发事件。

### 缓冲存储 {#the-spool}

**缓冲采用丢弃最旧内容的环形存储。** 本地存储是持久磁盘缓冲，默认 `1 GiB`。满时丢弃**最旧**的尚未转发事件，以接收新事件，不丢弃最新事件。

这是刻意选择。设备发布时，代理依据自己的持久存储立即确认。丢弃最新事件就会丢弃刚承诺保留的内容，并使中断结束时缓冲只剩陈旧数据，而非最新数据。

每次丢弃都会统计，以缓冲自身首序列减去该代理已转发且已确认事件数计算。后者是投递账目，并且**持久化**，因此重启后计数不会归零。

有一种情况不覆盖：持久计数不存在，例如首次启动或进度文件被删除时，从缓冲当前首序列初始化。之前驱逐的内容视为已处理，因此这种状态下重启会重置证据。如果丢弃计数重要，应在重启间完整保留存储目录。

**缓冲限制字节数，不限制年龄。** 等待事件保留设备上报时间；没有时间的 JSON 事件按代理存储时刻补齐。因此到达平台时，至少与链路中断一样旧，若设备之前也缓冲则更旧。平台拒绝比到达时刻早超过 366 天的读数，长期保留事件到达时会丢失。代理不会警告，应观察 `spool_oldest_age_seconds`。在超过 335 天（28,944,000 秒）时告警，能留下一个月恢复上行链路。

:::caution 重连去重仅覆盖 JSON 载荷
上行恢复后，代理重新转发全部缓冲。对于 **JSON 对象载荷**，会添加重放稳定身份和事件时间，使已经投递消息在云端唯一性检查中合并，仅显示一次。

**其他载荷形状原样转发，采用至少一次投递**，不稳定连接后的重连可能投递两次。边缘代理后使用非 JSON 解码器时，应让读数处理容忍重复。
:::

### 部署代理之前 {#before-you-deploy-an-agent}

- **不配置凭据时，本地 MQTT 监听开放。** 设置 `local.username` 和 `local.passwordEnv` 以要求凭据。开放是受信任局域网中的有效选择，代理启动时会明确警告，使选择可见。无论哪种，都只是网络访问控制，不是逐设备身份；明文 MQTT 会在局域网中明文传送密码。
- **指标和健康端点仅绑定回环地址。** 设计上设备 MQTT 端口是代理唯一向局域网暴露的入口。其他主机抓取代理指标时，需要在该机器上提供转发。

### 边缘代理设置 {#edge-agent-settings}

| 设置 | 默认值 | 作用 |
|---|---|---|
| `instanceId` | — | 必填。转发目标云实例。其他实例的发布不转发，计入 `instance_mismatched_total`。 |
| `local.listenPort` | `1883` | 现场设备连接的 MQTT 端口。 |
| `local.storeDir` | — | 必填。持久缓冲目录，每目录一个代理。 |
| `local.spoolMaxBytes` | `1 GiB` | 缓冲预算，超出丢弃最旧事件，最低 16 MiB。 |
| `local.metricsPort` | `9090` | 仅回环的指标和健康端口，明确设置 `0` 禁用。 |
| `uplink.brokerUrl` | — | 必填。云端 MQTT 转发端点。 |
| `uplink.tenant` | — | 必填。网关设备所属的租户。它是每个上行主题的一部分，也是代理从其设备接收事件的唯一租户。 |
| `uplink.gatewayToken` | — | 必填。该代理所登录的网关设备令牌。**每台网关设备只能有一个代理**：两个代理共用一台会循环互相抢占会话。 |
| `uplink.username` | — | 必填。`{tenant}:{credentialId}`，即网关设备的凭据。缺少租户前缀的名称会在代理启动时被拒绝。 |
| `uplink.passwordEnv` | — | 必填。保存凭据密钥的环境变量。 |
| `uplink.connectTimeoutSeconds` | `30` | 单次上行连接尝试时间上限。 |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | 链路中断期间重连退避边界。 |

## 应监控什么 {#what-to-watch}

:::note 这两个服务附带十条告警
Chart 为 sparkplug-ingest 和 lwm2m-ingest 附带十条告警规则，见[随附告警](#edge-alerts)：每种传输是否在被读取、卡住的 LwM2M 主实例，两个服务丢弃数据的几种情形，以及 Sparkplug 命令下行通道。**下表中其余所有指标都没有告警，也没有仪表盘面板。** 指标都会发出并被抓取，但编写自己的规则前，不会通知你。边缘代理没有任何随附告警。
:::

所有指标带 `devicechain_` 和服务段前缀：`devicechain_sparkplugingest_`、`devicechain_lwm2mingest_`、`devicechain_edge_`。没有逐设备标签。只有 `command_breaker_state` 带有 `tenant` 和 `source` 标签，其数量受你配置的来源限制，因此抓取不存在高基数风险。

### 随附告警 {#edge-alerts}

| 告警 | 严重程度 | 含义 | 处理方法 |
| --- | --- | --- | --- |
| `Lwm2mIngestLeaderCountNotOne` | critical | 持续 2 分钟，报告持有领导租约的 lwm2m-ingest 副本数不是恰好 1。为 0 时，无人读取 CoAP/DTLS 传输，所有 LwM2M 设备都不可达，尽管每个 Pod 看起来都健康。大于 1 时，两个副本同时在提供服务。 | 查看 lwm2m-ingest 日志，了解没有副本取得租约的原因；通常是平台的消息代理不可达。 |
| `Lwm2mIngestLeaderNotServing` | critical | 持续 5 分钟，某个 Pod 持有 LwM2M 租约，但其 CoAP/DTLS 读取循环没有运行（`is_leader` 为 1 且 `is_serving` 为 0）。它卡在构建任期的过程中，通常是在从 device-state 读取已断言在线的设备，并且因为它一直持有租约，没有备用实例能够接管。没有任何 LwM2M 设备得到服务。 | 查看该 Pod 日志中 `Acquired LwM2M leadership` 前后的内容，然后重启该 Pod，让备用实例取得租约。 |
| `Lwm2mIngestDroppingNotifications` | warning | 过去 10 分钟内，持续 10 分钟，lwm2m-ingest 因接入失败或被拒绝（包括背压）而丢弃了 Notify 消息的读数。Notify 路径没有重试，因此上报不频繁的设备会丢失其读数。 | 检查消息代理、入站事件流的积压（参见[接入链路背压](./observability.md#ingest-backpressure)）以及 lwm2m-ingest 日志。计入 `notify_ingest_dropped_total`。 |
| `SparkplugIngestLeaderCountNotOne` | critical | 持续 2 分钟，报告持有租约的 sparkplug-ingest 副本数不是恰好 1。为 0 时，没有任何 Sparkplug 来源在被读取。大于 1 时，两个副本连接相同的来源。部署时未配置来源的 Pod 永远不会取得租约，其值永久为 0。 | 为该 Pod 配置来源，或将该服务从部署中去掉。否则查看 sparkplug-ingest 日志，了解没有副本取得租约的原因。 |
| `SparkplugIngestBrokerUnreachable` | warning | 过去 10 分钟内，持续 10 分钟，sparkplug-ingest 连接某个已配置的 Sparkplug 代理失败。就绪状态刻意不以代理可达为条件，因此 Pod 看起来健康，而该来源什么也没有接入。 | 主机错误、凭据被拒绝或代理故障都会触发。sparkplug-ingest 日志会指出来源和原因。计入 `connect_failures_total`。 |
| `SparkplugIngestSubscriptionRefused` | warning | 过去 15 分钟内，持续 5 分钟，某个 Sparkplug 来源因其代理接受了连接，却拒绝或从未确认某个分组订阅，而被放弃并重试。只要有任一分组被拒绝，该来源就不接入其任何分组，也从不宣告 ONLINE。 | 将代理对该来源凭据的访问规则与为其配置的分组进行比较；最可能是该凭据无权读取其中某个分组。计入 `subscribe_failures_total`。 |
| `SparkplugIngestDroppingData` | warning | 过去 10 分钟内，持续 10 分钟，sparkplug-ingest 放弃了它已接受的消息：其读数或在线状态转换在重试预算用尽后、在背压下立即，或在关闭时被丢弃。Sparkplug 主机不会得到代理的重投递，因此每一条都会丢失。 | 检查 device-management 和消息代理，并在 sparkplug-ingest 日志中查找 `Dropping Sparkplug samples`。计入 `ingest_failures_total`。 |
| `SparkplugCommandsUnconfirmed` | warning | 持续 15 分钟，过去 30 分钟内发布的 Sparkplug 命令（至少 20 条）中超过 90% 在结束时节点都没有报告所请求的值。Sparkplug 没有命令确认机制，命令通路不通时就是这种表现。这些命令记录为 TIMEOUT，而不是失败。 | 检查来源的凭据是否可以在 `NCMD` 和 `DCMD` 主题上发布，`commands.hostRole` 是否与节点的配置相符（节点可能忽略不是其主主机的主机），以及 `commands.confirmWindowSeconds` 是否短于节点的上报延迟。计入 `commands_total{result="unconfirmed"}`。 |
| `SparkplugCommandBreakerOpen` | warning | 过去 20 分钟内，某个来源的命令断路器曾打开。客户代理在命令刚发布后就断开 Host 会话时它会打开，这几乎总是意味着凭据无权在 `spBv1.0/<group>/NCMD/+` 和 `spBv1.0/<group>/DCMD/+/+` 上发布。打开期间，发往该来源的命令立即以未送达失败；该来源的遥测不受影响。 | 在代理上授予发布权限。告警在断路器最后一次打开 20 分钟后恢复安静，不等待断路器关闭。暂停（5 分钟，每次重新打开翻倍，最长 1 小时）结束后断路器进入半开，`command_breaker_state` 读数为 2，只有下一条命令发布后没有断连才会关闭，因此没有待发命令的来源会停在 2，但不会告警。告警会指出租户和来源。 |
| `SparkplugCommandResponsesFailing` | warning | 持续 5 分钟，sparkplug-ingest 一直在统计无法完成处理的命令，按 `result` 区分：`claim_failed`、`park_failed` 和 `response_failed` 表示无法连接 command-delivery 或其拒绝了调用；`kv_failed` 表示无法写入待确认命令的存储，因此该命令未发布；`publish_failed` 表示一次发布可能到达也可能没有到达节点。 | 检查 command-delivery 和消息代理，以及 sparkplug-ingest 日志。被拒绝的命令和未确认的命令是结果而不是故障，不计入此处。计入 `commands_total`。 |

### 主实例告警 {#no-leader-alert}

两条 `LeaderCountNotOne` 告警是整个部分最关键的信号：零表示无人提供该传输服务，其所有设备都静默不可达。它们在总和不为一时触发，并且刻意**不带 `absent()` 分支**。未部署的服务不导出任何内容，而 Chart 的规则并不知道某次安装运行了哪些服务。已部署但没有运行 Pod 的服务同样不导出任何内容；那是一个 Pod 自身通过重启和就绪状态大声报告的故障。如果希望在序列完全不存在时也收到通知，请自行添加配对表达式：

> `sum(devicechain_lwm2mingest_is_leader) != 1 or absent(devicechain_lwm2mingest_is_leader)`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

未配置来源的 Sparkplug Pod 在初始化时就注册其 `is_leader` 指标，先于检查是否有服务内容，随后永远不会取得租约。它整个生命周期都发布读数为 **0** 的序列，`SparkplugIngestLeaderCountNotOne` 会自行对此触发。这是刻意的：没有内容可服务的传输服务，要么是需要修正的配置，要么是应从部署中去掉的服务。

两服务的 `is_leader` 在副本**取得租约**时变为一，不等领导任期状态构建完成。正常接管不会在重建期间显示无主；但此时可能隐藏**卡在构建**中的主实例。LwM2M 的第二个指标 `is_serving` 可定位，见下文以及上文的 `Lwm2mIngestLeaderNotServing`。

### Sparkplug 接入指标 {#sparkplug-ingestion-metrics}

前缀：`devicechain_sparkplugingest_`。

| 信号 | 含义 |
|---|---|
| `is_leader` | 服务 Pod 为 1，其他为 0。总和不为 1 时触发 `SparkplugIngestLeaderCountNotOne`。 |
| `connect_failures_total` | 配置的消息代理不可达。增长表示某来源停机，而 Pod 看起来健康。供给 `SparkplugIngestBrokerUnreachable`。 |
| `subscribe_failures_total` | 代理接受连接，却拒绝或未确认某分组订阅，通常凭据无读取权限。来源保持离线，不接入任何分组并重试。增长时触发 `SparkplugIngestSubscriptionRefused`。 |
| `birth_recovery_pending` | BIRTH 未能注册（多数是因为来源的 `deviceTypeToken` 指向尚不存在的设备类型）、正被请求重新 BIRTH 的节点数。持续非零表示有节点或来源没有注册。 |
| `birth_recovery_rebirths_total` | 发给这些节点的 rebirth 命令数，间隔翻倍，最长一分钟。该序列在 `birth_recovery_pending` 非零时持续增长，表示注册一直失败。 |
| `messages_total` | 入站 Sparkplug 流量。活跃设备群中平线表示订阅丢失或来源死亡。 |
| `presence_emitted_total` | 产出的连接/断开信号。 |
| `rebirth_requests_total` | 请求节点重新宣告的次数。持续增长表示节点无法重新同步。存活探测发出的 rebirth 也计入此处，因此带静默设备的设备群会在每个 `presence.probeSeconds` 内持续出现分散的少量增长。 |
| `presence_probes_total` | 得出结论的存活探测次数，按 `trigger` 区分：`connect`（每个代理会话运行一次的协调）或 `periodic`（稳定会话上轮转的每个时段一次，每个 `presence.probeSeconds` 内访问每个节点一次）。探测要求其负责的已静默 ASSERTED 设备所属节点 rebirth，并将未回答的设备标记离线；未发现静默设备的时段同样计数。**已连接主机上 periodic 序列必须上升。** 平线表示探测未运行，而安静的设备群与从未运行的探测在其他方面无法区分。 |
| `presence_probe_disconnects_total` | 存活探测因窗口内未回答 rebirth 而标记离线的 ASSERTED 设备数，按同一 `trigger` 区分。健康设备群中接近零。持续的 `periodic` 速率表示节点与主机之间正在丢失死亡证书。 |
| `rebirth_enqueued_total` / `rebirth_dropped_total` | 会话状态机请求的 rebirth，以及发布队列已满而未接收的数量。丢弃属于延迟信号，不等于终止失败：节点下一窗口会重新请求。但持续丢弃表示请求速度超过发布速度。结合只统计实际发上线路、受发布能力而非需求限制的 `rebirth_requests_total`：**丢弃且请求量升至稳定上限**表示扇出超过健康发布者能力；**丢弃且请求量平线**表示发布本身卡住，应检查代理连接。 |
| `unknown_device_dropped_total` | 关闭自动注册时，无对应设备身份的流量。 |
| `decode_errors_total` | 格式错误载荷。 |
| `ingest_failures_total` | 已接受消息的读数，或 birth/death 声明的连接转换被丢弃，以及在线状态协调产生但无法存储的断开批次。读数在平台背压时立即丢弃；其他情况在重试耗尽后（设备管理或代理不可达，或平台代理未确认发布），等待重试的发布队列已满时，或连接关闭期间丢弃。拆分为多事件的消息，其中部分事件可能已存储。clean-session 主机没有重投递，因此这些内容均丢失。供给 `SparkplugIngestDroppingData`。 |
| `ingest_samples_shed_total` | 租户超过接入限额而丢弃的 DATA 读数，表示发送量超过层级允许值。 |
| `tenant_deleted_dropped_total` | 租户正在删除而拒绝的流量。 |
| `samples_too_old_dropped_total` | 时间戳早于主机接收时刻超过 366 天的读数，消息其他部分仍存储。超过这么久未变化的指标，每次 birth 都会丢弃；未设时钟、时间接近 1970 的节点全部读数会丢弃。服务警告日志列出设备。 |
| `commands_total` | 下行通道处理的命令，按 `result` 区分。结果：`published`、`confirmed`、`confirmed_unchanged`、`unconfirmed`、`superseded`、`rearmed`、`parked`、`drained`；拒绝：`refused_disabled`、`refused_breaker`、`refused_expired`、`refused_payload`、`refused_not_allowed`、`refused_metric`、`refused_datatype`、`refused_reboot_gate`；不属于本服务：`not_served`、`poison`、`claim_lost`、`tenant_gone`；故障：`claim_failed`、`park_failed`、`publish_failed`、`kv_failed`、`response_failed`。在有来源启用命令之前为零。供给 `SparkplugCommandsUnconfirmed`（未确认除以已发布）和 `SparkplugCommandResponsesFailing`（五种故障）。 |
| `commands_awaiting` | 已发布、正等待节点上报所请求值的命令。 |
| `command_confirm_seconds` | 已确认命令从发布到节点上报所用的时间。未确认的命令不在此处观测。 |
| `command_breaker_state` | 按 `tenant` 和 `source`：0 关闭，1 打开（命令立即失败），2 半开（放行一条命令）。供给 `SparkplugCommandBreakerOpen`。 |
| `command_breaker_trips_total` | 断路器打开的次数。此处有速率而 `SparkplugCommandBreakerOpen` 保持安静，说明代理曾断开过一次会话。 |

### LwM2M 接入指标 {#lwm2m-ingestion-metrics}

前缀：`devicechain_lwm2mingest_`。

| 信号 | 含义 |
|---|---|
| `is_leader` | 持有租约的 Pod 从取得租约起为 1。总和不为 1 时触发 `Lwm2mIngestLeaderCountNotOne`。 |
| `is_serving` | Pod 的 CoAP/DTLS 读取循环实际运行后为 1。必须**结合** `is_leader`：这是区分正常重建注册表和卡在重建的唯一方式，其他就绪、存活和 `is_leader` 信号都可能健康。`is_leader == 1 and is_serving == 0` 持续超过正常接管时长时，`Lwm2mIngestLeaderNotServing` 即对此告警。 |
| `active_registrations` / `active_sessions` / `active_observations` | 服务看到的活动设备群。**重启前后观察 `active_observations`**，可发现上述观察关系丢失及恢复。 |
| `registrations_total` / `registration_updates_total` | 设备加入和保持会话。 |
| `registration_expiries_total` | 未注销而注册过期，表示设备消失。 |
| `handshake_failures_total` / `auth_errors_total` | DTLS 失败设备和未预配身份。 |
| `observe_establish_refused_total` | 设备拒绝或其他原因失败的 Observe。**仅支持 1.0 的客户端症状**：用 `4.06` 拒绝 SenML Observe，之后不通知。 |
| `notify_unknown_content_format_total` | 确实发送通知，但格式无法解码。仅支持 1.0 的客户端该值为零。 |
| `notify_decode_failures_total` | 格式错误载荷。 |
| `notify_records_non_numeric_total` / `notify_records_non_finite_total` / `notify_records_unnamed_total` | Notify 中未产生测量值的读数。**非数值是正常情况**：布尔或字符串 IPSO 读数说明设备正常，该计数可与完全静默区别，否则表面相同。后两项是固件错误：无穷或 NaN，或没有资源路径。 |
| `observation_overflow_total` | 超过每次注册 32 条观察上限，部分资源未观察。 |
| `telemetry_too_old_dropped_total` | 读数时间早于适配器接收时刻超过 366 天，Notify 其他部分存储。服务警告日志列出设备。 |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | 超出租户接入上限的 Notify 消息和读数。 |
| `notify_ingest_dropped_total` | 平台拒绝或无法接收，导致读数丢弃的 Notify，包括背压期间。没有重试，下一 Notify 取代丢失内容。拆分消息的前部分事件可能已存储。供给 `Lwm2mIngestDroppingNotifications`。 |
| `shadows_reconstructed_total` | 领导权变更后重建的在线状态，突增是故障切换特征。 |
| `commands_failed_total` / `commands_not_served_total` | 未成功到达的下行指令。 |
| `command_live_claim_errors_total` | 因指令投递服务无法确认而**未执行**的指令。每个指令在到达设备前立即向指令投递服务确认，未确认绝不发送。持续增长表示 LwM2M 指令都无法到达设备，**应为此告警**。指令会重试，不会丢失。 |
| `commands_stale_dispatch_total` | 平台已重新准备或发送指令，因此丢弃的投递。**不是故障**，每次都代表避免了一次重复物理执行。中断或切换后增长是正常现象。 |
| `commands_overflow_parked_total{reason}` | 在指令投递服务暂存、稍后按序发送的指令。`full`：设备队列已满，响应慢。`offline`：没有活动连接。`bind`：刚连接，等待指令尚未交付；**故障切换后预期突增**，设备同时重连。`unconfirmed`：前面的指令无法确认。 |
| `command_overflow_blocked_total` | 无法足够快暂存指令而必须等待的次数。只在指令投递服务缓慢或不可达时增长，此时所有 LwM2M 指令都等待。 |
| `command_drain_turns_total` | 排空轮次，每轮最多投递某设备 4 个暂存指令。本指标增长而 `commands_overflow_parked_total` 平线，表示正在清除积压。 |

### 边缘代理指标 {#edge-agent-metrics}

前缀：`devicechain_edge_`。

| 信号 | 含义 |
|---|---|
| `uplink_connected` | 0 表示现场正在缓冲。 |
| `spool_oldest_age_seconds` | **首要积压信号**，按实际时间表示代理落后多久。 |
| `spool_used_bytes` / `spool_limit_bytes` | 缓冲距离开始丢弃还有多少余量。 |
| `dropped_total` | **数据已经丢失**，为腾出空间驱逐最旧事件。任何增长都是真实丢失。 |
| `forward_errors_total` | 转发失败次数，事件仍缓冲以便再次投递。 |
| `received_total` / `forwarded_total` | 接收和转发吞吐。 |
| `malformed_total` | 无法转发而被丢弃的事件，避免阻塞后续队列。 |
| `tenant_mismatched_total` | 指向 `uplink.tenant` 之外租户的设备发布。它们不会缓冲或转发。 |
| `body_credentials_stripped_total` | 转发前从事件正文中移除的凭据成员数。设备应停止发送它们。 |
| `refused_total{reason}` | **为该网关被拒绝的事件。** `not_gateway_child`：平台因事件所属设备未分配给该网关或不存在而拒绝它。`gateway_self` 和 `invalid_device_token`：代理不愿发送。`other`：此版本代理不认识的原因。任何增长都需要关注；参见[网关身份](#gateway-identity)。 |
| `refused_kept_total` / `refused_unmatched_total` | 写入 `refused-events.jsonl` 的被拒绝事件数 / 其事件已不在本机保留的拒绝数（平台的 `failed-events` 记录中有它）。 |
| `uplink_inflight` | 等待确认的发布数，最多 64。积压排空期间保持在 64 是正常的。 |
| `local_auth_enabled` | 0 表示现场 MQTT 监听不要求凭据。 |

## 验证范围限制 {#what-is-not-validated}

这些服务的验证有两项限制：

- **没有随项目发布的测试工具演练真实 Sparkplug 设备群。** 项目不对第三方边缘节点或消息代理进行端到端驱动，Sparkplug 行为由针对平台自身实现的测试覆盖。
- **LwM2M 路径验证更充分。** 使用独立第三方 LwM2M 客户端栈，并从服务端读取判断结果，防止行为错误的客户端制造通过结果。套件按计划运行，属于参考验证，不是发布门禁。

边缘代理由自身测试覆盖，没有集群内部署验证。推广到设备群前，先在一个现场试点部署。
