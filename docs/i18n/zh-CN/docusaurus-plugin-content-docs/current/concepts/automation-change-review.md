---
sidebar_position: 4.1
title: 自动化变更审查
---

# 自动化变更审查 {#automation-change-review}

自动化变更审查使用已记录的设备事件，对比已发布的检测规则及其草稿。你选择一个变更应当解决的事件时段，以及一个变更不应干扰的正常运行时段。受保护的配置文件在发布前必须有已批准的审查。你可以按照[审查规则变更](../guides/reviewing-a-rule-change.md)进行尝试。

:::note 此功能处于预览阶段
GraphQL 类型和操作可能在次要版本中变更。保护需要主动启用：升级后不会自动保护任何配置文件，未受保护的配置文件行为不变。
:::

## 审查对比什么 {#what-a-review-compares}

一次审查针对一个设备配置文件中的一条检测规则，对比其当前生效（已发布）版本和草稿，使用明确指定的一组设备。你必须提供恰好一个 INCIDENT 窗口和一个 NORMAL 窗口。

平台只读取一次每个窗口中已记录的已解析事件，包括该窗口内记录的所有配置文件版本的事件。平台自己的检测求值器在同一事件列表上运行两次，每个规则版本运行一次。两边获得完全相同的输入。

不会执行任何动作：不会触发告警、发送命令，也不会有 webhook 或消息离开平台。审查将告警状态转换以及命令或连接器请求记录为**意图**，即规则本来会请求的内容。

每个窗口按设备列出变更：

| 类型 | 含义 |
| --- | --- |
| `ADDED` | 只有草稿会执行。 |
| `REMOVED` | 只有活动规则会执行。 |
| `SHIFTED` | 两边都会执行，但时间不同。配对容差默认为 15 分钟，可通过 `shiftWindowSeconds` 调整。 |
| `CONTENT_CHANGED` | 两边都会执行，但命令载荷、告警严重程度或连接器请求正文/配置摘要不同。 |

计数（`unchanged`、`added`、`removed`、`shifted`、`contentChanged`）在截断前计算，保持完整。每个窗口最多列出 2,000 项变更；`changesTruncated` 表示是否还有更多变更。

覆盖信息包括每个指标和配置文件版本的读数、两边在窗口开始时的状态（`UNKNOWN`、`CLEAR`、`RAISED`）、命令首次分歧的时刻，以及两边因迟到而拒绝的读数。

每个窗口开始前，引擎会读取预热历史以建立状态：规则的时间跨度加五分钟，最多六小时。阈值规则的时间跨度为 30 分钟，持续时间规则为其保持时间，聚合规则为其窗口长度。

## 变更类型 {#kinds-of-change}

平台将草稿与活动版本对比并分类。

| `kind` | 变更内容 |
| --- | --- |
| `RULE_CHANGE` | 恰好一条规则的定义、启用标志或分组范围发生变更，规则读取或发送的内容没有变更。回放这条规则。 |
| `NO_AUTOMATION_CHANGE` | 没有规则变更，规则读取或发送的内容也没有变更。不执行回放；结果为 `NO_AUTOMATION_CHANGE`，可以像 `PASSED` 一样批准。例如仅修改描述，或在没有规则启用时修改解码器。 |
| `UNREVIEWABLE` | 两条或更多规则变更，或规则读取或发送的指标或命令变更。结果为 `INCONCLUSIVE`；`reasons` 列出各项变更。请拆分为每次审查一条规则。 |

只要任一版本中有规则启用，添加或删除任何指标、修改指标标签以外的任何内容，或修改解码器，都属于自动化变更，即使没有规则引用该指标。测量事件按整体进行验证，解码器负责生成规则读取的值。

其他编辑会列在 `profileChanges` 中，标记为 `automation: false`。批准审查也表示认可这些编辑。省略 `ruleToken` 时，服务器会找出变更的规则；如果提供，则必须指定该规则。

## 结果 {#outcomes}

`outcome` 描述证据的结论；`state` 描述审查的生命周期。

| 结果 | 含义 |
| --- | --- |
| `PASSED` | 所有草稿期望均为 `PASS`，两个窗口各有至少一个期望，没有窗口降级，且对比完整。没有期望的审查绝不会得到 `PASSED`。 |
| `FAILED` | 至少一个期望为 `FAIL`，且没有期望为 `INCONCLUSIVE`。 |
| `INCONCLUSIVE` | 其他所有情况。不能批准。 |
| `NO_AUTOMATION_CHANGE` | 没有需要回放的自动化变更。可以批准。 |

### 为什么无法得出结论时必须严格处理 {#why-inconclusive-is-strict}

无法证明草稿安全的审查绝不会通过。请检查以下原因：

- 任一规则无法从已记录的遥测中重现：`fidelity.status` 为 `UNSUPPORTED`，附带原因，且不计算差异。
- 窗口降级：预热历史或窗口起点已超出流的保留范围，达到读取、扫描或时间上限，记录损坏，或求值失败。请查看 `degraded`。
- 被审查设备有因迟到而拒绝的读数、没有规则所读指标的读数，或因预热历史没有证据而在窗口开始时处于 `UNKNOWN` 状态。
- “从不”类期望在窗口开始时已有告警处于触发状态。
- 证据位于两边命令分歧之后。草稿的命令本会改变设备接下来的行为，因此后续记录的读数已无法描述草稿下的情况。

`fidelity.label` 的内容为“replay of recorded inputs”。

## 可以回放的内容 {#what-can-be-replayed}

阈值规则、持续时间规则，以及使用滚动或滑动窗口的聚合规则可以回放。支持的动作包括触发告警、发送命令、HTTP 调用以及发布到连接器。

以下情况会产生 `fidelity.status: UNSUPPORTED`、结果 `INCONCLUSIVE`，且没有差异：变化率、重复、缺失、关联和连接状态规则；使用会话或计数窗口的聚合规则；读取设备属性或地理围栏位置的规则；通过关系锚点关联设备；限定分组范围的规则；公式读取设备常量的计算指标；以及任何其他动作类型。

## 期望 {#expectations}

使用与[规则测试](../guides/testing-your-rules.md)相同的词汇，最多添加十个期望。将每个期望分配给 INCIDENT 或 NORMAL。控制台提供四种形式：触发告警（`raised`）、不触发告警（`neverRaised`）、请求命令（`intent`，类型为 `sendCommand`）以及不请求命令（`neverIntent`，类型为 `sendCommand`）。

通过 GraphQL 传入的 `expect` 是 JSON 字符串，最多 8,192 字节。`within` 从窗口开始时计时。审查期望不能使用 `count` 或 `allowDegraded`，指定的设备必须包含在审查设备中。

每个期望都有活动规则的判定（`baseline`）和草稿的判定（`verdict`）：`PASS`、`FAIL` 或 `INCONCLUSIVE`。通过 `baselineReason`、`reason` 和 `evidence` 查看解释及支持判定的读数。

## 批准 {#approvals}

只有状态为 `RAN` 且结果为 `PASSED` 或 `NO_AUTOMATION_CHANGE` 的审查才能批准。`approveAutomationReview(token, digest)` 接收批准者看到的 `reviewDigest`，确保该人员批准的正是存储的证据。

使用你自己已登录且不受范围限制的租户会话，并持有 `device:write` 和 `event:read`。代理令牌、服务令牌、服务账户以及仅限租户部分范围的成员都会被拒绝。你的第二因素必须在配置文件规定的时限内验证，默认为 15 分钟。控制台会提示追加验证；API 客户端请使用 [GraphQL 参考中的会话内追加验证](../reference/graphql-api.md)。

默认情况下，运行审查的人员和所有草稿作者都不能批准。草稿作者是自基线版本发布以来，编辑过配置文件草稿（包括指标、命令和规则）的人员，以审计日志为准。存在疑问时，平台会将该人员排除在批准者之外。策略可以允许自行批准。

默认需要一次批准；策略可以要求两名不同人员批准。不允许自行批准时，两人均不能是创建者或草稿作者。批准记录包括人员、认证方法、第二因素验证时间、批准时间和策略修订号。

批准时使用批准者自己的会话令牌进行判断。之后退出登录不会终止批准。发布时，每位批准者仍必须具有已启用、不受范围限制且持有 `device:write` 和 `event:read` 的成员资格，否则返回 `APPROVER_NO_LONGER_QUALIFIES`。

生命周期为 `RAN` → 足够人员批准后的 `APPROVED` → 发布使用后的 `CONSUMED`。`consumedVersion` 标识对应版本。可以通过 `rejectAutomationReview(token, note)` 拒绝 `RAN` 或 `APPROVED` 审查，无需追加验证。运行、批准和拒绝均以该人员为操作者写入审计日志。

批准有效期默认为 24 小时。当存储的 `expiresAt` 或当前策略的有效期到期时（包括到期时刻），读取和状态筛选均返回 `EXPIRED`。发布时应用相同规则，证据过期时返回 `REVIEW_EXPIRED`。到期状态按需计算，无需后台任务或更新记录；待批准、已拒绝和已使用的审查保留原状态。

批准时也会检查草稿、基线和存储的证据是否仍然匹配。`matchesCurrentDraft` 表示当前草稿是否仍为已审查的草稿；草稿变更后，控制台会显示提示。你可以将 `/automation-reviews/<token>` 分享给任何有权读取审查的人员。

## 受保护的配置文件 {#protected-profiles}

保护要求配置文件已发布。在受保护的配置文件上，`publishDeviceProfile` 必须通过 `automationReviewToken` 指定恰好对应当前草稿的已批准审查。发布者需要 `device:write`，但不必是批准者。一次审查只会被一次发布消耗。未受保护的配置文件忽略该令牌，发布方式不变。

通过 `automationReviewPolicy(profileToken)` 读取 `effective` 设置、`revision` 和 `pendingChange`。通过 `setAutomationReviewPolicy(profileToken, settings)` 修改 `required`、`requiredApprovals`、`allowSelfApproval`、`approvalTtlSeconds` 和 `stepUpMaxAgeSeconds`；它返回 `applied` 和 `policy`。

收紧保护立即生效：开启保护、增加批准数、禁止自行批准，或缩短批准有效期或第二因素验证时限。放宽保护会以 `policy.pendingChange` 等待，且 `applied: false`。第二名人员使用自己的无范围限制会话、`device:write` 和近期验证的第二因素，通过 `approveAutomationReviewPolicyChange(token)` 批准。提议者绝不能批准自己的变更。

如果当前策略允许自行批准，放宽保护也会在 24 小时后的 `effectiveAt` 自动生效，由下一次受保护操作应用，除非第二名人员提前批准。两种方式都可通过 `cancelAutomationReviewPolicyChange(token)` 取消。变更待处理期间，配置文件保持受保护状态。

控制台 **Change review** 标签页包含 **Publish protection**、**Edit protection**、待处理变更批准、**Cancel policy change** 和 **Break-glass record**。**Versions** 标签页提供当前草稿的 **Approved review** 选择器。

### 保护保证 {#the-guarantee}

在受保护的配置文件上，设备执行的规则和绑定到该配置文件的设备类型，只能通过经过审查并批准的发布或已记录的应急操作来变更。这涵盖发布、回滚、将设备类型重新指向该配置文件或从中移出、将设备移出绑定类型，以及删除配置文件。

受保护配置文件的已发布版本不能修改。版本只能随其配置文件一起删除；没有操作可以单独删除一个版本。删除受保护配置文件会被拒绝，因此保护开启期间版本历史会保留。

数据库对所有客户端强制执行该保证，包括滚动升级期间仍运行上一版本的服务 pod。任何会改变受保护配置文件上设备所执行内容的语句都会被拒绝，不会应用。

### 保护不涵盖的内容 {#what-protection-does-not-cover}

保护不涵盖创建设备、启用、停用或删除设备；向绑定类型添加设备；创建绑定到配置文件的新类型或删除空类型；设备属性和元数据；以及实体组成员关系。因此，受保护的规则不能依赖属性、常量、分组、锚点或地理围栏。

## 资格条件 {#eligibility}

开启保护前，活动版本中所有已启用规则都必须符合条件。受保护发布的草稿也必须通过资格检查。任何已启用规则都不能读取设备属性、读取公式使用设备常量的计算指标、限定分组范围、通过关系锚点关联设备，或读取地理围栏位置。

这些输入无需发布就能改变规则行为，因此已记录的遥测无法支持保证。检查采取保守方式：只要提及此类输入，或检查无法读取某条规则，就视为使用了这些输入。不符合条件的保护请求会返回 `PROTECTION_INELIGIBLE` 和原因。

## 应急操作 {#break-glass}

受保护配置文件上有三种操作必须提供 `breakGlassReason`：`rollbackDeviceProfile`；将类型重新指向该配置文件或从中移出时的 `updateDeviceType`；以及将设备移出绑定类型时的 `updateDevice`。

使用人员自己的会话，持有 `device:write`，提供不超过 512 个字符的非空原因，并在配置文件的第二因素时限内完成验证。控制台通过 **Change a protected profile** 重试，先询问原因，再按需要求第二因素。

平台在同一事务中记录动作（`ROLLBACK`、`REPOINT_TYPE`、`MOVE_DEVICE`）、目标、原因、操作者和时间，并写入审计日志。通过 `automationReviewBreakGlass(profileToken)` 读取最多 200 条最新记录，或打开 **Break-glass record**。

回滚会移动基线。现有批准随后会因 `REVIEW_STALE_BASELINE_CHANGED` 失败；请重新运行并批准审查。不允许应急回滚到包含不符合条件规则的版本。

## 删除配置文件 {#deleting-a-profile}

删除受保护配置文件会返回 `PROFILE_PROTECTED`。请先移除保护；这种放宽需要等待第二名人员或延迟期。删除未受保护配置文件会移除其策略、待处理策略变更和未消耗审查，并保留已消耗审查和应急记录作为历史。

## 限制 {#limits}

| 项目 | 限制或默认值 |
| --- | --- |
| 设备 | 1–50 台不同设备。 |
| 窗口 | 恰好一个 INCIDENT 和一个 NORMAL，每个时长为正。重放规则变更时，每个窗口最多 24 小时，结束时间至少早于运行两分钟。 |
| 自动预热历史 | 最多六小时。 |
| 期望 | 最多 10 个；每个最多 8,192 字节。 |
| 列出的变更 | 每个窗口最多 2,000 项；计数完整。 |
| 运行次数 | 每个租户过去一小时最多 60 次。 |
| `requestId` | 1–128 个可打印字节。相同输入重复请求会返回同一审查，不计为新运行；同一 ID 使用不同输入会返回 `CONFLICT`。 |
| 所需批准数 | 默认为 1；可设为 1 或 2。 |
| 自行批准 | 默认禁用。 |
| 批准有效期 | 默认 86,400 秒；范围 900–604,800 秒（15 分钟至 7 天）。 |
| 第二因素验证时限 | 默认 900 秒；范围 60–3,600 秒。 |
| 拒绝备注 | 最多 512 字节。 |
| 应急原因 | 非空；最多 512 个字符。 |

## 所需权限 {#required-permissions}

不受范围限制的会话是指成员资格不限于租户的部分范围。

| 操作 | 要求 |
| --- | --- |
| 运行 | `device:write` + `event:read`；人员自己的无范围限制会话。 |
| 读取（`automationReview`、`automationReviews`、分享链接） | `device:read` + `event:read`，无范围限制。审查包含设备读数。 |
| 批准 | `device:write` + `event:read`；自己的无范围限制会话，近期验证的第二因素，不是创建者或草稿作者，除非策略允许自行批准。 |
| 拒绝 | `device:write` + `event:read`；自己的无范围限制会话。 |
| 读取保护或应急记录 | `device:read`，无范围限制。 |
| 修改保护或取消待处理变更 | `device:write`；自己的无范围限制会话。 |
| 批准待处理的放宽 | `device:write`；自己的无范围限制会话，近期验证的第二因素，不是提议者。 |
| 发布受保护配置文件 | `device:write` 和已批准审查。 |
| 应急操作 | `device:write`；自己的会话、原因、近期验证的第二因素。 |

## 升级 {#upgrading}

滚动升级时，先升级 user-management，再升级 device-management：user-management 版本较旧时，受保护发布会返回 `REVIEW_UNAVAILABLE`。仅在部署完成后开启保护。如果 event-processing 比 device-management 旧，运行审查会返回 `REVIEW_UNAVAILABLE`，直到 event-processing 完成升级。

## 相关页面 {#related-pages}

- [审查规则变更](../guides/reviewing-a-rule-change.md)
- [测试你的规则](../guides/testing-your-rules.md)
- [事件处理](../concepts/event-processing.md)
- [双因素认证](../guides/two-factor-authentication.md)
- [GraphQL API](../reference/graphql-api.md)
