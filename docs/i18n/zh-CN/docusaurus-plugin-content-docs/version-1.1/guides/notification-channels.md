---
sidebar_position: 6
title: 配置通知渠道
---

# 配置通知渠道 {#configuring-notification-channels}

通知将已触发的**告警**送到最后一环——人员。检测规则动作触发告警后，每租户**策略**按严重程度，将它路由到配置的**渠道**：SMTP 电子邮件或 webhook。策略还提供节流，以及未确认告警的**升级通知**。

机器到人的路径有意与机器到机器的**[出站连接器](../concepts/outbound-connectors.md)**分开。连接器向*系统*传递载荷；通知通过收件人和按严重程度路由，向*人员*传递警报。告警如何触发见[事件处理与告警](../concepts/event-processing.md)。

若想在 Web 控制台中管理策略和静默时段，请参阅用户指南中的[通知策略](../user-guide/notification-policies.md)。

:::note 状态
已提供。通过 notification-management GraphQL API 管理渠道和策略。读取需要 `notification:read`，创建或修改任何内容需要 `notification:write`。
:::

## 渠道 {#channels}

**渠道**是租户配置的投递端点：某种渠道**类型**的实例，加上连接配置。查询 `notificationChannelTypes` 可获取平台定义的类型。目前 `smtp` 和 `webhook` 提供可用适配器，各类型的 `available` 标志说明适配器是否已提供。

渠道将设置分成两部分：

- **`config`**：非密钥连接设置，采用 JSON 文档（SMTP 主机/端口/发件人；webhook URL/方法/标头/认证方式）。
- **`secret`**：凭据，例如 SMTP 密码或 webhook 认证令牌。保存在平台信封加密的**密钥存储**中，**只写**。创建时提交，读取时绝不返回；渠道只暴露 `hasSecret` 布尔值。

**更新**时，`secret` 字段行为如下：

- **省略**则保留现有密钥，不必再次发送。
- 发送非空值会替换。
- 发送 `null` 或空字符串会清除。
- 配置声明 `bearer` 或 `header` 认证的 webhook 渠道，清除密钥会被拒绝。若希望端点匿名，请在同一请求将 `auth` 改为 `none`。

如果客户端逐字段绑定变量，未提供的变量会成为显式 `null`，**清除密钥**。声明 `bearer` 或 `header` 的 webhook 中，同样的 `null` 会让整个更新失败，即使本来只想重命名。请将整个请求作为单个变量发送，并省略 `secret` 键。

### 创建 SMTP 渠道 {#create-an-smtp-channel}

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-email",
    name: "Operations email",
    channelType: "smtp",
    config: "{\"host\":\"smtp.example.com\",\"port\":587,\"from\":\"alerts@example.com\",\"username\":\"alerts\",\"security\":\"starttls\"}",
    secret: "<smtp password>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

设置 `username` 的 SMTP 渠道必须有密钥。缺少密钥时，在平台连接邮件服务器之前，第一次投递就会被拒绝，不重试，并以 `reason="credential"` 计数（见下文）。这在投递时检查，不在保存时检查。

### 创建 webhook 渠道 {#create-a-webhook-channel}

webhook 渠道向 URL POST 渲染后的通知。创建方式相同，使用 `channelType: "webhook"`，配置包含 `url`、`auth` 模式，以及可选 `method` 和额外 `headers`。只接受 `POST`，它也是默认值；其他方法保存时被拒绝。

`auth` 必填，决定认证方式：

| `auth` | 发送内容 | `secret` |
| --- | --- | --- |
| `none` | 无凭据标头。URL 本身携带凭据时使用，例如 Slack 入站 webhook。 | 不得设置 |
| `bearer` | `Authorization: Bearer <secret>` | 必填 |
| `header` | 将密钥放入 `authHeader` 指定标头；设置 `authScheme` 时，在前面添加该方案和一个空格。例如 `"authHeader":"X-API-Key"` 发送原始令牌，`"authHeader":"Authorization","authScheme":"Token"` 发送 `Authorization: Token <secret>`。 | 必填 |

只有 `header` 读取 `authHeader` 和 `authScheme`。`none` 或 `bearer` 时请省略，设置它们会被拒绝，而不是静默忽略。

`auth` 与 `secret` 不一致的渠道保存时就会被拒绝，不等告警触发。包括缺少 `auth`、`bearer`/`header` 没有密钥，以及 `none` 带密钥。要让 `bearer` 渠道匿名，请在同一更新发送 `auth` 为 `none` 和 `secret: null`。只重命名、修改描述或禁用的更新不检查，因此总能关闭配置错误的渠道；启用时会检查。

如果渠道仍以这种状态进入投递，例如在 `auth` 字段出现前保存的渠道，不会发送。第一次投递拒绝且不重试，通知服务记录租户、渠道令牌和原因。拒绝计入 `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`，可设置告警。同一计数器还用 `reason="egress"` 记录目标地址不可达的 webhook 或 SMTP 渠道，例如私有、环回或云元数据地址。它还带有 `reason="no_adapter"`：当某个渠道的类型在此版本中没有投递适配器而被跳过时计数，也就是无人会通过该渠道收到通知。`credential` 和 `egress` 的拒绝是终止结果，不重试。

```graphql
mutation {
  createNotificationChannel(request: {
    token: "oncall-hook",
    name: "On-call webhook",
    channelType: "webhook",
    config: "{\"url\":\"https://hooks.example.com/alarms\",\"auth\":\"bearer\"}",
    secret: "<token>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

Slack 入站 webhook 使用 `"auth":"none"`，省略 `secret`。

## 策略 {#policies}

**策略**决定哪些告警投递给谁、通过哪些渠道。它携带一组**规则**，每条映射：

- `severity`：`CRITICAL`、`MAJOR`、`MINOR`、`WARNING`、`INDETERMINATE`，或表示任意的 `"*"`；
- 通过令牌指定的渠道；
- 由适配器解释的 `recipients` JSON 数组：SMTP 为电子邮件地址，webhook 可以为空。

这里严重程度采用**大写**，因为它是*告警*的严重程度。检测规则编写时使用小写（`major`），触发告警时转换为大写，因此通知规则始终匹配大写形式。不属于上述值的严重程度，例如小写 `major`，会在**写入时拒绝**，而不是保存为永远无法匹配的规则。

:::caution 策略适用于整个租户
目前**不支持** `deviceTypeToken`，设置它的策略写入时被拒绝。请不要设置。原因见[设备类型作用域](#device-type-scoping)。
:::

### 设备类型作用域 {#device-type-scoping}

将策略限定于设备类型，需要从告警来源跨服务查询设备类型，该功能尚未提供。在此之前，派发器跳过限定类型的策略，而不是应用到整个租户造成过度通知。拒绝写入是有意的：接受该字段会返回成功，之后却完全不投递。

### 节流与升级 {#throttling-and-escalation}

另两类设置影响投递：

- **`throttleSeconds`** 是*同一*告警通知的最小间隔，防止反复变化的条件淹没渠道。`null` 表示不节流。
- **`escalateAfterSeconds`** 和 **`maxEscalations`**：设为大于零时，上次通知后持续**未确认且未解除**达到该时长，会重新通知，直到次数上限。`maxEscalations` 未设置或为 `0`，使用服务级默认上限 5。`escalateAfterSeconds` 为 `null`/`0` 禁用该策略升级。

升级可安全运行多个副本：每个升级层级发送前先认领，因此只有一个副本投递。每个告警有**一个共享升级时钟和层级**。多个升级策略匹配时，最短窗口决定频率，每个次数上限都针对共享层级。

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "default-routing",
    name: "Default alarm routing",
    throttleSeconds: 300,
    escalateAfterSeconds: 900,
    maxEscalations: 3,
    enabled: true,
    rules: [
      { severity: "CRITICAL", channelToken: "oncall-hook", recipients: "[]" },
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token enabled rules { severity channel { token } } }
}
```

更新请求的 `rules` 会**替换**现有规则集合。省略则保留，发送 `null` 或 `[]` 会清空。与渠道 `secret` 相同，逐字段绑定变量会将未提供的 `rules` 发送为 `null`，清空集合，因此应将整个请求作为一个变量发送。指定未知渠道令牌会让整个写入失败。

两人同时编辑同一策略可能相互覆盖：双方修改的字段只保留最后保存值，而 `rules` 替换整体集合，第二次保存会丢弃第一次所有规则。要防止，请将读取时的 `updatedAt`（或上次更新返回的值）作为 `expectedUpdatedAt` 发送。此后策略已改变时，更新拒绝，提示“notification policy was modified by another writer; reload and try again”，不写入任何内容。重新加载、重新应用更改，再次发送。

### 静默时段 {#quiet-hours}

**静默时段**用来避免策略在不合适的时间（例如深夜）打扰人员。策略处于静默时段内时，会**暂存**通知而不是发送，并在静默时段结束时发出。每条策略都有自己的静默时段，因此夜班策略和日班团队的策略可以遵循不同的时间表。

静默时段不是[维护窗口](./maintenance-windows.md)。维护窗口属于设备类型，规定破坏性命令何时可以*开始*；静默时段属于通知策略，规定该策略何时*暂存通知*。两者使用相同的时段格式，以及相同的时区和夏令时规则，除此之外互不相关，也不会相互影响。

策略通过三个字段获得静默时段。没有静默时段的策略，这三个字段均为 `null`。

| 字段 | 含义 |
| --- | --- |
| `quietHoursTimezone` | 解释时段所用的 IANA 时区，例如 `Europe/Berlin`。`Local` 和未知名称会被拒绝。始终使用策略自己的时区，而不是服务器的时区。 |
| `quietHoursSlots` | 一个 JSON **字符串**，包含 1 到 56 个每周时段，文档格式与维护窗口相同。每个时段包含 `day`（`MONDAY` 到 `SUNDAY`）、`start`（24 小时制 `HH:MM`，为该时区的挂钟时间）和 `durationMinutes`（1 到 10080，按实际经过的时间计算）。 |
| `quietHoursBypassSeverity` | `CRITICAL`、`MAJOR`、`MINOR`、`WARNING` 或 `INDETERMINATE`，或 `null`。严重程度达到或高于该值的告警，即使在静默时段内也会发送。`null` 表示所有严重程度都会被暂存。在没有静默时段的策略上允许设置，但不起作用。 |

请同时设置 `quietHoursTimezone` 和 `quietHoursSlots`，或者让两者都为 `null`。

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "night-shift",
    name: "Night shift pages",
    enabled: true,
    quietHoursTimezone: "Europe/Berlin",
    quietHoursSlots: "[{\"day\":\"MONDAY\",\"start\":\"22:00\",\"durationMinutes\":480},{\"day\":\"TUESDAY\",\"start\":\"22:00\",\"durationMinutes\":480}]",
    quietHoursBypassSeverity: "CRITICAL",
    rules: [
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token quietHoursTimezone quietHoursSlots quietHoursBypassSeverity }
}
```

这条策略在柏林时间每周一、周二的 22:00 到次日 06:00 处于静默。该时段内的 `CRITICAL` 告警仍会立即发送，其他告警则等到 06:00。

`updateNotificationPolicy` 是部分更新，三个静默时段字段各有三种状态：省略表示保持不变，发送 `null` 表示清除，发送具体值表示设置。服务器在应用更新之后再对这些字段一并校验，因此要**移除**静默时段，请在同一个请求中清除 `quietHoursTimezone` 和 `quietHoursSlots`（如果也想去掉 `quietHoursBypassSeverity`，一并清除）。

```graphql
mutation {
  updateNotificationPolicy(
    token: "night-shift",
    request: { quietHoursTimezone: null, quietHoursSlots: null }
  ) { token quietHoursTimezone quietHoursSlots }
}
```

#### 静默时段内会发生什么 {#quiet-hours-behavior}

- **先暂存，再发送。** 当一条已触发或已升级的告警匹配到一条处于静默时段内的策略，并且告警的严重程度低于放行严重程度（或未设置放行严重程度）时，通知会被**暂存**，而不是丢弃。静默时段结束时，策略按其规则发送这条通知。升级调度器在每一轮处理中释放暂存的通知，该轮默认每分钟运行一次，因此发送最多可能晚一轮。如果发送过程被通知服务的重启打断，通知不会丢失，而是在几分钟后发送。在少数情况下，这意味着同一条通知会被发送两次。
- **确认或清除会使其被丢弃。** 如果告警在被暂存的那个事件之后被确认或清除，暂存的通知会被丢弃，而不是发送。
- **每个告警在每条策略下只暂存一条通知。** 如果同一条策略又暂存了同一告警之后的触发或升级事件，后者会取代前者。早晨收到的通知描述的是告警最后的状态，一个在夜间反复抖动的告警只产生一次通知，而不是每次抖动一次。
- **放行严重程度立即发送。** 严重程度达到或高于放行严重程度的告警会立即发送，即使在静默时段内也是如此。严重程度不属于已知等级的告警始终会发送。
- **先做节流。** 被策略节流抑制的通知是被抑制，而不是被暂存。
- **升级会等待。** 策略处于静默时段内时，对于严重程度低于放行严重程度的告警，其升级再通知会等待，并在静默时段结束后恢复：仍未确认、未清除的告警会在静默时段结束后的第一轮处理中再次通知。静默时段不会消耗升级层级。
- **各策略相互独立。** 没有静默时段、或此刻不处于静默时段的策略，仍会针对同一告警立即发出通知。即使另一条策略已经通知了某人，暂存的通知仍会在静默时段结束时发送，因为两者的接收人可能不同。
- **新的通知会取代暂存的通知。** 当策略本身针对该告警发出通知时（因为其静默时段已结束，或告警达到了放行严重程度），它为该告警暂存的通知会被丢弃，而不会在之后再发送。
- **编辑会重新评估已暂存的内容。** 修改策略的静默时段或放行严重程度，或者启用、停用策略，会在下一轮处理中生效。如果策略不再暂存某条通知，该通知会被发送；如果仍然暂存，则继续暂存到新的结束时间。被停用或删除的策略会丢弃它所暂存的通知。
- **失败的发送会重试。** 如果一次发送没有送达任何渠道（所有投递均失败），会在 1、2、4 和 8 分钟后重试，共尝试 5 次。之后丢弃、计数，并记录为死信，与触发时未能通知到任何人的告警相同。

#### 时区与夏令时 {#quiet-hours-time-zones}

时段是策略 `quietHoursTimezone` 所在时区的挂钟时间，因此静默时段会随该时区一起遵循夏令时。起始时间落在春季向前跳变的空档（不存在的时间）内的时段，会在空档结束时开始。起始时间落在秋季回拨重叠（出现两次的时间）内的时段，会在第一次出现时开始。时段时长按实际经过的时间计算，因此跨越时钟调整的长时段，在挂钟上会提前或推后一小时结束。

时段可以跨越午夜。跨过周日结束的时段会延续到周一。

#### 服务器拒绝的情形 {#quiet-hours-validation}

每一种违规都会以 `extensions.code` 为 `INVALID_VALUE` 的错误响应，且不会写入任何内容。

- `quietHoursTimezone` 和 `quietHoursSlots` 没有同时设置。
- 时区为 `Local` 或未知名称。
- 某个时段的 `day` 未知、`start` 不是 24 小时制 `HH:MM`、`durationMinutes` 不在 1 到 10080 之间，或者列表中的时段数不在 1 到 56 之间。
- 两个时段按书写的一周来看**重叠**。仅首尾相接的时段是允许的；跨过午夜的周日时段会与周一的时段做检查。
- 所有时段合计**覆盖了整周**（10080 分钟）。若要让策略完全静默，请改为停用该策略。

#### 观测 {#quiet-hours-metric}

计数器 `devicechain_notificationmanagement_quiet_hours_total` 统计静默时段的处理结果，带有标签 `outcome`：

| `outcome` | 计数时机 |
| --- | --- |
| `held` | 一条通知被暂存而没有发送。 |
| `released` | 一条暂存的通知在静默时段结束时被发送。 |
| `dropped_resolved` | 一条暂存的通知因告警已被确认或清除而被丢弃。 |
| `dropped_policy` | 一条暂存的通知因其策略被停用或删除而被丢弃。 |
| `dropped_exhausted` | 一条暂存的通知在 5 次发送都没有送达任何渠道后被丢弃，并同时记录为死信。 |
| `superseded` | 一条暂存的通知因策略已针对同一告警的更新事件发出通知而被丢弃。 |

在控制台中，静默时段在 **Monitor → Notification Policies** 页面编辑；参见[通知策略](../user-guide/notification-policies.md)。

## 端到端验证 {#verify-the-path-end-to-end}

1. **创建渠道**（如上）。确认结果 `enabled: true`；有用户名的 SMTP 或声明 `bearer`/`header` 的 webhook，应为 `hasSecret: true`；声明 `none` 的 webhook 应为 `false`。
2. **创建策略**，将关注的严重程度映射到该渠道。
3. **触发真实告警。** 在测试设备触发检测规则（参见[事件处理与告警](../concepts/event-processing.md)），确认邮件或 webhook 到达。
4. **检查投递状态。** 服务为每个告警保存已执行操作的只读记录。查询 `notificationStatesByAlarmToken(alarmTokens: [...])`，或通过 `notificationStates` 搜索。检查 `firstNotifiedAt` 和 `notifyCount`；告警未确认超过升级窗口后，检查 `escalationLevel`。

确认或解除告警会停止后续升级。状态行在通知历史旁记录 `acknowledgedAt`/`clearedAt`。如果告警解除后其条件再次出现，平台会重新打开同一个告警，其状态行也会为新周期重新开始：在该周期被确认或解除之前，升级会再次进行。
