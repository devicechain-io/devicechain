---
sidebar_position: 6
title: 配置通知渠道
---

# 配置通知渠道 {#configuring-notification-channels}

通知将已触发的**告警**送到最后一环——人员。检测规则动作触发告警后，每租户**策略**按严重程度，将它路由到配置的**渠道**：SMTP 电子邮件、webhook，或原生的 Slack、Microsoft Teams、PagerDuty 渠道。策略还提供节流，以及未确认告警的**升级通知**。

机器到人的路径有意与机器到机器的**[出站连接器](../concepts/outbound-connectors.md)**分开。连接器向*系统*传递载荷；通知通过收件人和按严重程度路由，向*人员*传递警报。告警如何触发见[事件处理与告警](../concepts/event-processing.md)。

若想在 Web 控制台中管理策略和静默时段，请参阅用户指南中的[通知策略](../user-guide/notification-policies.md)。

:::note 状态
已提供。通过 notification-management GraphQL API 管理渠道和策略。读取需要 `notification:read`，创建或修改任何内容需要 `notification:write`。
:::

## 渠道 {#channels}

**渠道**是租户配置的投递端点：某种渠道**类型**的实例，加上连接配置。查询 `notificationChannelTypes` 可获取平台定义的类型。目前 `smtp`、`webhook`、`slack`、`teams` 和 `pagerduty` 提供可用适配器，各类型的 `available` 标志说明适配器是否已提供。

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

Slack 入站 webhook 使用 `"auth":"none"`，省略 `secret`。下面的原生 [Slack 渠道](#create-a-slack-channel)会为你排版消息，是更好的选择。

Webhook 通道的 JSON 正文包含告警的 `eventType`（`RAISED`、`ESCALATED`、`DEESCALATED`、`CLEARED`、`ACKNOWLEDGED`，定时重新通知则为 `RENOTIFIED`）、`alarmToken`、`alarmKey`、`severity`、`state`、来源的 `originatorType` 与 `originatorId`（已知时），以及各个时间。当告警所属设备已知时，还会包含 `deviceToken`、`deviceName` 和 `deviceTypeToken`；某个键的值未知时（例如设备没有名称）会省略该键，绝不会发送空值。定时重新通知（`RENOTIFIED`）和调度器拾取的延迟 `CLEARED` 通知也会在已知时包含这些设备和来源字段。升级前已发送过通知的告警，其后续此类通知会省略这些字段。

可在 Webhook 通道的 `config` 中设置可选的 `linkBaseUrl`，值为控制台的 `http` 或 `https` 基础 URL（推荐 `https`），例如 `"linkBaseUrl":"https://console.example.com"`。此时，正文会包含 `alarmUrl`，并在告警指明其设备时包含 `deviceUrl`。基础 URL 不得包含查询字符串或片段，因为链接通过追加路径构建。未设置 `linkBaseUrl` 时，这两个键均会省略。如果已存储的 Webhook 配置中的 `linkBaseUrl` 无效，投递时会跳过该值，省略这两个链接并记录警告。

### 原生 Slack、Teams 和 PagerDuty 渠道 {#native-channels}

这三种原生类型使用各服务自己的格式，而不是通用的 JSON 正文。它们共享三条规则：

- **凭据就是 `secret`。** Slack 和 Teams 的凭据是 webhook URL（它本身就是凭据，因此加密存储且不会返回，不放在 `config` 中）；PagerDuty 的凭据是 Events API v2 路由密钥。没有 `auth` 设置。没有密钥的原生渠道在保存时被拒绝；向没有密钥的渠道投递，会在首次尝试时被拒绝且不重试，计入 `reason="credential"`。
- **`config` 可选且严格。** 保存渠道时，未知键会被拒绝，因此拼错的设置不会被悄悄忽略。`linkBaseUrl` 是控制台的 `http` 或 `https` 地址（推荐 `https`），不得包含查询字符串或片段；设置后，消息会带有指向 `<linkBaseUrl>/alarms` 的 **Open alarms** 按钮或链接。
- **投递经过与 webhook 相同的出站防护。** 指向私有、环回或云元数据地址的目标会被拒绝且不重试（`reason="egress"`），实例的出站放行规则与 webhook 完全一样地生效。规则的 `recipients` 会被忽略：消息发往何处由 webhook 或路由密钥决定。

每条消息都会写明告警、严重程度、状态、来源设备、触发它的指标和值，以及触发时间。告警处于打开且未确认状态时，会提示在控制台中确认告警以停止后续通知；已清除或已确认的告警不显示该提示。

### 创建 Slack 渠道 {#create-a-slack-channel}

Slack 渠道向 Slack 入站 webhook URL 发布 Block Kit 消息：标题、告警字段网格、确认提示，以及可选的 **Open alarms** 按钮。消息的颜色条随严重程度变化（`CRITICAL` 为红色，`MAJOR` 为橙色，`MINOR` 和 `WARNING` 为琥珀色，`INDETERMINATE` 为灰色），告警清除时变为绿色，被确认时变为蓝色。来自告警的文本会被转义，因此无法注入 Slack 提及或链接。

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-slack",
    name: "运维 Slack",
    channelType: "slack",
    config: "{\"linkBaseUrl\":\"https://console.example.com\"}",
    secret: "https://hooks.slack.com/services/T000/B000/XXXXXXXX",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

密钥必须是 `https` URL。

### 创建 Microsoft Teams 渠道 {#create-a-microsoft-teams-channel}

Teams 渠道向 Teams **Workflows**（Power Automate）流程的 webhook URL 发布 Adaptive Card；该流程使用 *When a Teams webhook request is received* 触发器，并把卡片发布到聊天或频道。已停用的 Office 365 连接器 webhook 不受支持。卡片包含与 Slack 消息相同的信息，标题颜色随严重程度变化；设置了 `linkBaseUrl` 时还带有 **Open alarms** 操作。

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-teams",
    channelType: "teams",
    secret: "https://prod-00.westus.logic.azure.com:443/workflows/...",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

### 创建 PagerDuty 渠道 {#create-a-pagerduty-channel}

PagerDuty 渠道发送 Events API v2 事件，并把告警的生命周期映射到事件（incident）的生命周期。每个事件的 `dedup_key` 都是告警的令牌，因此一个告警的整个生命周期对应一个 incident：

| 告警事件 | PagerDuty 动作 |
| --- | --- |
| 触发、升级、降级，或被策略的升级通知再次通知 | `trigger`（对已打开 incident 的重复事件会更新它，而不是再开一个） |
| 已确认 | `acknowledge` |
| 已清除 | `resolve` |

告警严重程度映射到 PagerDuty 的严重程度：`CRITICAL` 对应 `critical`，`MAJOR` 对应 `error`，`MINOR` 和 `WARNING` 对应 `warning`，其他对应 `info`。要让策略能解决 incident，请让清除和确认这两种转换与触发走同一个渠道。

`config` 接受 `region`（`us` 为默认，`eu` 选择 `events.eu.pagerduty.com`）、`source`（事件来源，默认 `devicechain`）和 `linkBaseUrl`。

```graphql
mutation {
  createNotificationChannel(request: {
    token: "oncall-pagerduty",
    channelType: "pagerduty",
    config: "{\"region\":\"eu\",\"source\":\"plant-3\"}",
    secret: "<routing key>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

### 在控制台中 {#channels-in-the-console}

侧边栏中**通知策略**旁边的**通知渠道**页面，可用针对每种类型的表单列出、创建、编辑和删除渠道。凭据输入框只写：已有渠道的输入框会提示已存储凭据，留空则保留它。

webhook 渠道还可以对投递**签名**，让接收方能够验证。除非你要求，否则默认关闭：为渠道生成签名密钥后，在渠道的 `config` 中加入 `"signing":true`。设置了签名却没有密钥的渠道会被拒绝而不是以未签名形式发送，并计入 `reason="signing"`。参见[为出站 Webhook 签名](signed-webhooks.md)。

聊天消息和 webhook 摘要用设备名称（没有名称时用其令牌）指代设备，并列出其设备类型。当通道设置了 `linkBaseUrl` 且告警指明了其设备时，消息还会带有指向 `<linkBaseUrl>/devices/<设备令牌>` 的 **Open device** 按钮或链接；未设置 `linkBaseUrl`，或告警未指明设备时，则没有设备链接。

## 策略 {#policies}

**策略**决定哪些告警投递给谁、通过哪些渠道。它携带一组**规则**，每条映射：

- `severity`：`CRITICAL`、`MAJOR`、`MINOR`、`WARNING`、`INDETERMINATE`，或表示任意的 `"*"`；
- 通过令牌指定的渠道；
- 由适配器解释的 `recipients` JSON 数组：SMTP 为电子邮件地址，webhook 可以为空。

这里严重程度采用**大写**，因为它是*告警*的严重程度。检测规则编写时使用小写（`major`），触发告警时转换为大写，因此通知规则始终匹配大写形式。不属于上述值的严重程度，例如小写 `major`，会在**写入时拒绝**，而不是保存为永远无法匹配的规则。

:::caution 策略适用于整个租户
目前**不支持** `deviceTypeToken`，设置它的策略写入时被拒绝。请不要设置。原因见[设备类型作用域](#device-type-scoping)。
:::

### 告警清除时通知 {#notify-on-clear}

默认情况下，策略只在告警触发或升级时通知其通道。在策略上设置 `notifyOnClear: true`，即可在该策略曾通知过的告警**清除**（clear）时也通知这些通道。清除通知只发往确实为该告警投递过通知、且该策略所路由的通道和接收者，与告警清除时的严重级别无关（一个以 `CRITICAL` 触发、降为 `MAJOR` 后清除的告警，会在当初通知它的通道上被解决）。其 `eventType` 为 `CLEARED`，PagerDuty 会将其映射为 resolve。该选项默认关闭，因此在此选项出现之前创建的策略行为不变。

```graphql
mutation {
  updateNotificationPolicy(token: "ops", request: { notifyOnClear: true }) { token notifyOnClear }
}
```

清除属于信息性通知，因此处理方式与告警通知不同：

- 只发往在该周期内投递过通知的目的地。从未通知过该告警的策略（其通知被节流、因静默时段被暂存，或策略是在告警触发之后才创建的）不会发送清除通知。
- 每个周期只发送一次，且在清除被记录之后发送，因此其后不会再有“仍未解决”的升级通知。若没有任何通道接收，会重试；一旦有通道接收，就不会重复发送。若清除在其告警的通知被记录之前就被处理，则会在该通知之后立即发送；若当时发送失败，通知调度器会在之后最长 30 分钟内的各轮中再次拾取并发送。
- **不**受节流限制，不计入通知次数，也不会启动升级。
- 在策略的静默时段内发生的清除会被**丢弃**而不是暂存：数小时后才发出的“已清除”只会播报过时的消息。
- `notifyOnClear` 不能用 `null` 清除；请传 `true` 或 `false`。

### 设备类型作用域 {#device-type-scoping}

将策略限定于设备类型，需要从告警来源跨服务查询设备类型，该功能尚未提供。在此之前，派发器跳过限定类型的策略，而不是应用到整个租户造成过度通知。拒绝写入是有意的：接受该字段会返回成功，之后却完全不投递。

### 节流与升级 {#throttling-and-escalation}

另两类设置影响投递：

- **`throttleSeconds`** 是*同一*告警通知的最小间隔，防止反复变化的条件淹没渠道。提升告警严重级别的变化不受节流限制。`null` 表示不节流。
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

## 投递记录 {#delivery-ledger}

每次通过某个渠道发送告警通知的尝试，都会记录在租户的**投递记录**中，因此"这个告警是否真的通知到了、通知了谁、没通知到又是为什么"都有答案。使用 `notificationAttempts` 查询（需要 `notification:read`）：

```graphql
query {
  notificationAttempts(criteria: { pageNumber: 1, pageSize: 20, alarmToken: "<告警令牌>" }) {
    results {
      attemptedAt trigger policyToken channelToken recipients
      status attempt maxAttempts latencyMillis errorCode errorMessage
    }
  }
}
```

每条记录对应一次尝试，按时间从新到旧排列。一个允许尝试三次的渠道如果失败两次后投递成功，会留下三条记录：`FAILED`（第 1 次）、`FAILED`（第 2 次）和 `SENT`（第 3 次）。可按 `alarmToken`、`channelToken` 或 `status` 过滤，也可用 `since` 和 `until`（RFC 3339）按时间过滤。

| `status` | 含义 |
| --- | --- |
| `SENT` | 渠道已接受该通知。 |
| `FAILED` | 重试可能解决的失败：超时、错误状态码、连接被拒绝。 |
| `REFUSED` | 被永久拒绝，因此不会重试：目标是出站流量不允许到达的地址（`EGRESS_BLOCKED`），或渠道的凭据配置被拒绝（`CREDENTIAL_REFUSED`）。 |
| `NOT_ATTEMPTED` | 已计划但从未尝试：无法读取渠道的投递密钥（`SECRET_UNAVAILABLE`），或在轮到该渠道之前，该告警的投递时间已用尽（`BUDGET_SPENT`）。 |

`trigger` 说明投递的起因：`ALARM`（告警状态变化）、`ESCALATION`（对未确认的告警再次通知）或 `QUIET_HOURS_RELEASE`（静默时段结束时发送了被暂存的通知）。后续版本可能新增 `status`、`trigger` 和 `errorCode` 的取值，遇到无法识别的值时请将其视为提示信息。

`errorMessage` 是服务提供方的错误文本，其中所有 URL 都被替换为 `<url>`，因为 Slack 或 Teams 的 webhook URL 本身就是凭据，而任何拥有 `notification:read` 的人都能读取投递记录。通知内容本身不会被存储：`subjectHash`（主题行的 SHA-256）可用于判断两次尝试是否发送了相同的内容。

每个渠道还会提供从投递记录读取的 `deliveryHealth { lastSuccessAt lastFailureAt consecutiveFailures }`。`consecutiveFailures` 统计自该渠道上次投递成功以来失败和被拒绝的尝试次数。`NOT_ATTEMPTED` 记录不计入渠道的失败，因为它们描述的是平台没有执行的操作；`errorCode` 为 `BUDGET_SPENT` 的 `FAILED` 记录同样不计入：该告警所有投递的可用时间在这次尝试进行中耗尽，是平台中止了它。对于没有 `notification:read` 的调用方（例如可写但不可读的调用方在变更中拿到的渠道），`deliveryHealth` 为 null。

需要注意两点：

- **投递记录绝不会延迟通知。** 记录在尝试之后、在投递路径之外写入。如果数据库无法接收某条记录，该记录会被丢弃，并计入 `devicechain_notificationmanagement_ledger_writes_total{outcome="failed"}`（等待写入的记录过多时计入 `outcome="dropped"`）。有记录能证明发生过尝试；没有记录并不能证明没有尝试。
- **收件人属于个人数据。** 记录会在 7 天后删除（与已解决的通知状态相同的保留期），或在 notification-management 配置的 `AttemptRetentionSeconds` 之后删除。删除租户时，这些记录也会随之清除。
- **调高保留期之前先估算规模。** 投递记录每次尝试保存一条：大致等于被通知的告警状态变化数 × 每次到达的渠道数 × 每个渠道的尝试次数，再加上升级通知。一个每天通知 10,000 次、经由 3 个渠道的租户，在默认的 7 天保留期下约保存 210,000 条记录，每条都带有收件人列表。

## 端到端验证 {#verify-the-path-end-to-end}

1. **创建渠道**（如上）。确认结果 `enabled: true`；有用户名的 SMTP 或声明 `bearer`/`header` 的 webhook，应为 `hasSecret: true`；声明 `none` 的 webhook 应为 `false`。
2. **创建策略**，将关注的严重程度映射到该渠道。
3. **触发真实告警。** 在测试设备触发检测规则（参见[事件处理与告警](../concepts/event-processing.md)），确认邮件或 webhook 到达。
4. **检查投递状态。** 服务为每个告警保存已执行操作的只读记录。查询 `notificationStatesByAlarmToken(alarmTokens: [...])`，或通过 `notificationStates` 搜索。检查 `firstNotifiedAt` 和 `notifyCount`；告警未确认超过升级窗口后，检查 `escalationLevel`。
5. **查看投递记录。** `notificationAttempts(criteria: {pageNumber: 1, pageSize: 20, alarmToken: "..."})` 列出该告警的每次尝试及其结果。`FAILED` 或 `REFUSED` 记录带有 `errorCode` 和服务提供方的 `errorMessage`；参见[投递记录](#delivery-ledger)。

确认或解除告警会停止后续升级。状态行在通知历史旁记录 `acknowledgedAt`/`clearedAt`。如果告警解除后其条件再次出现，平台会重新打开同一个告警，其状态行也会为新周期重新开始：在该周期被确认或解除之前，升级会再次进行。
