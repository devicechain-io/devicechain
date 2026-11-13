---
sidebar_position: 3.8
title: 审查规则变更
---

# 审查规则变更 {#reviewing-a-rule-change}

使用 Towerpulse 的 Night Shift 情节，对比规则的已发布版本与两个草稿。第一个草稿解决了事件，却在正常升温期间请求制冷。第二个草稿应当通过两个窗口的检查。有关模型和保护保证，请参阅[自动化变更审查](../concepts/automation-change-review.md)。

:::note 此功能处于预览阶段
GraphQL 类型和操作可能在次要版本中变更。保护需要主动启用：升级后不会自动保护任何配置文件，未受保护的配置文件行为不变。
:::

## 开始之前 {#before-you-start}

你需要运行中的 DeviceChain 实例、`dcctl`，以及在 `dcctl sim create` 为模拟器创建的租户中拥有成员资格的两名人员。编辑和运行审查的人员需要在自己的无范围限制会话中持有 `device:write` 和 `event:read`。批准者需要这些权限、不受范围限制的成员资格，并已注册双因素认证。读取审查还需要 `device:read` 和 `event:read`。

请由另一名人员批准：默认情况下，运行者和草稿作者都不能批准。模拟器使用第二因素登录时，`dcctl sim` 支持 `--otp`；请参阅 [dcctl 双因素认证](../guides/two-factor-authentication.md#dcctl)。

## 记录 Night Shift 情节 {#record-night-shift}

创建模拟器租户和登录账户：

```bash
dcctl sim create nightshift --manifest towerpulse --episode night-shift
```

在独立终端中运行模拟器，并保持其在前台运行：

```bash
dcctl sim run nightshift
```

在另一个终端中获取记录的窗口：

```bash
dcctl sim windows nightshift
```

输出包含两行，以制表符分隔角色、开始和结束时间，时间格式为 RFC3339 UTC。以下仅展示带占位符的格式：

```text
INCIDENT	<start RFC3339>	<end RFC3339>
NORMAL	<start RFC3339>	<end RFC3339>
```

情节开始前，该命令会提示你先运行模拟器。情节开始时会记录起始时刻。INCIDENT 为起始后的第 45 至 95 分钟；NORMAL 为第 105 至 160 分钟。请等待到开始后约 2 小时 42 分钟，使 NORMAL 的结束时间至少早于审查两分钟。

前 45 分钟用于稳定运行。INCIDENT 期间，`tp-vav-tower-f1-south` 温度过高，但随附规则未能检测到。NORMAL 期间，`tp-vav-tower-f3-north` 有一段不应触发制冷的正常短暂升温；`tp-vav-tower-f5-west` 举行会议，作为对照。

五台设备为 `tp-vav-tower-f1-north`、`tp-vav-tower-f1-south`、`tp-vav-tower-f3-north`、`tp-vav-tower-f3-south` 和 `tp-vav-tower-f5-west`。请参阅 [create](../reference/dcctl.md#dcctl-sim-create)、[run](../reference/dcctl.md#dcctl-sim-run) 和 [windows](../reference/dcctl.md#dcctl-sim-windows) 命令参考。

## 修改规则：草稿 A {#draft-a}

打开配置文件 `tp-vav-profile` 草稿中的检测规则。编辑 `tp-rule-zone-hot`，即“Towerpulse zone hot while occupied”。已发布规则检查 `temp_c` 的五分钟滑动平均值是否大于 26，同时要求 `occupancy > 0`。它触发严重程度为 `major` 的 `zone-hot` 告警，并请求载荷为 `{"minutes":15}` 的 `boost-cooling` 命令。

草稿 A 将阈值降至 25.5，保留五分钟窗口。只修改这一条规则。

## 运行审查 {#run-a-review}

### 在控制台中 {#run-in-console}

打开 **Change review** → **Run review**。将 `dcctl sim windows` 输出的 UTC 时间转换为本地时间，输入两个窗口的起止时间。输入五台设备的令牌，以逗号或换行分隔。**Let the server choose the rule** 会将 `ruleToken` 留空。

添加以下两个期望。为 INCIDENT 选择触发告警，为 NORMAL 选择不请求命令。表单最多支持十个期望，提供触发告警、不触发告警、请求命令和不请求命令四种形式。

```json
{"raised":{"rule":"tp-rule-zone-hot","device":"tp-vav-tower-f1-south","within":"30m"}}
```

```json
{"neverIntent":{"kind":"sendCommand","command":"boost-cooling","device":"tp-vav-tower-f3-north"}}
```

`within` 从所属窗口的开始时刻计时。不要使用 `count` 或 `allowDegraded`。选择 **Run review**。

### 通过 GraphQL {#run-over-graphql}

向 `https://<your-host>/api/device-management/graphql` 发送操作，在 `Authorization: Bearer` 中使用你的租户访问令牌。此操作通过变量接收输入，因此你可以提供实际记录的窗口时间和自己的 `requestId`，无需复制示例输出。

```graphql
mutation RunReview($input: RunAutomationReviewInput!) {
  runAutomationReview(input: $input) {
    token kind state outcome reviewDigest matchesCurrentDraft
    reasons profileChanges { area key change automation }
    fidelity { status reasons label }
    expectations {
      index name window baseline verdict baselineReason reason
      evidence { device at value spanSeconds }
    }
    windowResults {
      role start end leadInStart degraded changesTruncated
      counts { unchanged added removed shifted contentChanged }
      devices {
        device readings { key count } versions { key count }
        stateBase stateDraft divergence lateBase lateDraft
      }
      changes {
        kind device subject baseAt draftAt shiftSeconds contentChanged
        baseContent { severity payload configDigest bodyDigest }
        draftContent { severity payload configDigest bodyDigest }
        afterDivergence
      }
    }
  }
}
```

将 `input.profileToken` 设为 `tp-vav-profile`，`deviceTokens` 设为这五台设备，`windows` 设为两个包含 `role`、`start` 和 `end` 的对象。使用 `INCIDENT` 和 `NORMAL`，以及实际的 RFC3339 时间。每个窗口时长必须为正，最多 24 小时，且至少在运行前两分钟结束。

将 `expectations` 设为两个包含 `window` 和 `expect` 的对象：`expect` 是上方对应 JSON 编码后的 JSON 字符串，不是对象。`name` 可选。省略 `ruleToken` 让服务器选择，或使用 `tp-rule-zone-hot`。`shiftWindowSeconds` 可选，默认对应 15 分钟。

使用 1–128 个可打印字节的 `requestId`。相同输入重试时使用同一 ID 以获取同一审查；输入变更时使用新 ID。

## 阅读结果 {#read-the-result}

草稿 A 的预期结果是 `FAILED`。INCIDENT 中，`baseline` 失败，`verdict` 通过。NORMAL 中，草稿失败：它会在正常升温期间向 `tp-vav-tower-f3-north` 请求 `boost-cooling`。原因会指出命令、`{"minutes":15}` 以及两边命令分歧的时刻。这些是预期结果，并非实际运行输出示例。

按活动规则（`baseline`）到草稿（`verdict`）的方向阅读 `expectations`，再查看原因和证据。在 `windowResults` 中按设备对比 `ADDED`、`REMOVED`、`SHIFTED` 和 `CONTENT_CHANGED`。即使 `changesTruncated` 表示列表超过 2,000 项变更，计数仍然完整。

检查指标和配置文件版本覆盖信息，以及 `stateBase`、`stateDraft`、`lateBase`、`lateDraft` 和 `divergence`。在相信没有动作的结果前，先阅读 `fidelity` 和 `degraded`。不支持的规则、历史不完整、达到上限、时间限制、记录损坏、求值错误、迟到读数被拒绝、指标读数缺失、初始状态未知、“从不”期望的告警已触发，或命令分歧后的证据，都可能导致 `INCONCLUSIVE`。此结果不能批准。请参阅[为什么无法得出结论时必须严格处理](../concepts/automation-change-review.md#why-inconclusive-is-strict)。

## 尝试草稿 B {#draft-b}

保留 25.5 的阈值，将滑动窗口增至 15 分钟。使用相同设备、窗口和期望，以及新的 `requestId`，运行新审查。草稿 B 的预期结果是 `PASSED`：它能检测到过热区域，同时不会在正常升温期间请求制冷。

检查实际证据。通过审查要求草稿的所有期望通过、每个窗口至少有一个期望，且对比完整、没有窗口降级。运行后编辑草稿会使该审查过时；`matchesCurrentDraft` 和控制台提示会显示这一点。

## 批准审查 {#approve-the-review}

选择 **Copy link**，将链接交给第二名人员。页面路径为 `/automation-reviews/<token>`。对方使用自己的已登录会话打开页面，阅读证据并选择 **Approve**。除非策略允许自行批准，否则批准者必须与运行审查的人员以及所有草稿编辑者不同。

批准者需要 `device:write`、`event:read`、无范围限制的成员资格，以及近期验证的第二因素：默认在 15 分钟内验证。控制台会提示追加验证。API 客户端请使用租户 API 的 `stepUp(refreshToken, code)`，详见 [GraphQL 参考](../reference/graphql-api.md)。

```graphql
mutation ApproveReview($token: String!, $digest: String!) {
  approveAutomationReview(token: $token, digest: $digest) {
    token state requiredApprovals approvals { principal approvedAt policyRevision }
    approvedAt expiresAt
  }
}
```

提供审查的 `token` 和该人员实际查看的准确 `reviewDigest`。只有 `RAN` 状态且结果为 `PASSED` 或 `NO_AUTOMATION_CHANGE` 的审查才能批准。如果需要两次批准，必须由两名符合条件的不同人员批准。**Reject** 可拒绝待处理或已批准的审查，备注可选，无需追加验证。

批准有效期默认为 24 小时。请检查 `expiresAt`；过期的已批准审查在读取时显示为 `EXPIRED`，不能满足发布要求。退出登录不会终止批准，但发布时会再次检查每位批准者已启用的成员资格和权限。

## 保护配置文件 {#protect-the-profile}

在 **Change review** → **Publish protection** → **Edit protection** 中启用 **Require an approved review to publish**。设置 **Required approvals**、**Allow self-approval**、**Approval lifetime (hours)** 和 **Second-factor maximum age (minutes)**，然后选择 **Save protection**。默认值为一次批准、不允许自行批准、24 小时和 15 分钟。保护要求配置文件已发布，且活动版本中已启用的规则符合条件。

也可以使用 API：

```graphql
mutation ProtectProfile($profileToken: String!, $settings: AutomationReviewPolicyInput!) {
  setAutomationReviewPolicy(profileToken: $profileToken, settings: $settings) {
    applied
    policy {
      effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds }
      revision pendingChange { token effectiveAt }
    }
  }
}
```

使用 `required: true`。`requiredApprovals` 为 1 或 2，`approvalTtlSeconds` 范围为 900–604800，`stepUpMaxAgeSeconds` 范围为 60–3600。使用你自己的无范围限制会话，并持有 `device:write`。

收紧立即生效。放宽需等待第二名人员以近期验证的第二因素批准；提议者不能批准。如果当前策略允许自行批准，放宽也会在 24 小时后自动生效，由下一次受保护操作应用，除非第二名人员提前批准。可使用 **Cancel policy change** 取消。待处理期间配置文件仍受保护。如果收紧使现有批准不再满足策略，请在发布前获取新审查和批准。

## 使用审查发布 {#publish-with-the-review}

打开 **Versions**，在 **Approved review** 中选择当前草稿的已批准审查，然后发布。发布者需要 `device:write`，但不必是批准者。

```graphql
mutation PublishReviewedProfile($token: String!, $automationReviewToken: String!) {
  publishDeviceProfile(token: $token, automationReviewToken: $automationReviewToken) {
    version automationReviewToken
    automationReview { token approvals { principal } }
  }
}
```

使用配置文件令牌和已批准审查令牌。发布只会消耗该审查一次。草稿或基线变更、证据过期，或批准不再满足当前策略时，发布会被阻止。未受保护配置文件会忽略 `automationReviewToken`。

## 从版本追溯审查 {#trace-a-version}

`DeviceProfileVersion.automationReviewToken` 标识已消耗的审查。`DeviceProfileVersion.automationReview` 包含批准、期望、`inputDigest` 和结果。审查的 `consumedVersion` 指回已发布版本。

```graphql
query ReadReview($token: String!) {
  automationReview(token: $token) {
    token profileToken baselineVersion state outcome consumedVersion
    reviewDigest inputDigest resultDigest
    approvals { principal approvedAt policyRevision }
    expectationInputs { window name expect }
    result
  }
}
```

使用 `device:read` 和 `event:read` 读取，且不受范围限制。运行、批准和拒绝也记录在审计日志中。

## 执行应急操作 {#break-glass}

回滚受保护配置文件时，使用自己的会话并持有 `device:write`，提供不超过 512 个字符的非空原因，并在配置文件要求的第二因素时限内完成验证。

```graphql
mutation RollBackProtectedProfile($token: String!, $version: Int!, $reason: String!) {
  rollbackDeviceProfile(token: $token, version: $version, breakGlassReason: $reason) {
    token
  }
}
```

在控制台中，**Change a protected profile** 会询问原因，再按需要求第二因素。将设备类型重新指向受保护配置文件或从中移出（`updateDeviceType`），以及将设备移出绑定类型（`updateDevice`），也有同样要求。每个操作都接受 `breakGlassReason`。

使用 `device:read` 且无范围限制的会话，读取 **Break-glass record** 或 `automationReviewBreakGlass(profileToken)`。其中记录动作、目标、原因、操作者和时间。回滚会使现有批准因 `REVIEW_STALE_BASELINE_CHANGED` 失败；请重新运行并批准审查。不允许回滚到不符合条件的规则。

## 可能遇到的问题 {#what-can-go-wrong}

运行时拒绝不会存储审查。`INCONCLUSIVE` 审查则存储了无法证明变更安全的证据。

| 拒绝代码 | 处理方法 |
| --- | --- |
| `REVIEW_REFUSED` | 阅读拒绝原因并修正输入，例如无效规则。 |
| `REVIEW_UNAVAILABLE` | 恢复对比服务或完成升级；受保护发布还要求能够检查批准者资格。 |
| `NO_BASELINE`、`PROFILE_NOT_PUBLISHED` | 先发布配置文件，再审查或启用保护。 |
| `RATE_LIMITED` | 等待过去一小时的审查数少于 60。 |
| `CONFLICT` | 为不同输入使用新的 `requestId`。 |
| `NOT_APPROVABLE`、`REVIEW_NOT_PENDING` | 检查结果和状态；只有符合条件的 `RAN` 审查才能批准。 |
| `SECOND_FACTOR_REQUIRED`、`STEP_UP_REQUIRED` | 使用有第二因素的自己的会话，或完成追加验证。 |
| `APPROVER_IS_AN_AGENT`、`SERVICE_TOKEN_REFUSED`、`NOT_A_PERSON`、`SCOPED_GRANT_NOT_SUPPORTED` | 使用人员自己的无范围限制租户会话。 |
| `SELF_APPROVAL_REFUSED` | 请另一名符合条件的人员操作。策略变更提议者绝不能批准自己的变更。 |
| `CONCURRENT_APPROVAL` | 在另一方批准后重新加载。 |
| `REVIEW_STALE_DRAFT_CHANGED`、`REVIEW_STALE_BASELINE_CHANGED` | 针对当前草稿和基线重新运行并批准审查。 |
| `DIGEST_MISMATCH` | 重新加载并核对准确的证据和摘要；存储的证据必须通过验证。 |
| `REVIEW_REQUIRED`、`REVIEW_NOT_FOUND`、`REVIEW_WRONG_PROFILE` | 选择此配置文件当前草稿的已批准审查。 |
| `REVIEW_NOT_APPROVED`、`REVIEW_ALREADY_USED`、`REVIEW_EXPIRED` | 获取当前有效且未使用的已批准审查。 |
| `REVIEW_STALE_POLICY_CHANGED` | 获取满足当前保护策略的批准。 |
| `APPROVER_NO_LONGER_QUALIFIES` | 请在发布时仍符合条件的人员批准。 |
| `PROTECTION_INELIGIBLE` | 阅读原因，移除不符合条件的规则依赖。 |
| `INVALID_POLICY` | 使用支持的批准数、有效期和第二因素时限范围。 |
| `BREAK_GLASS_REASON_REQUIRED` | 提供不超过 512 个字符的非空原因。 |
| `PROFILE_PROTECTED` | 删除前先移除保护；放宽需要等待批准或延迟期。 |

## 相关页面 {#related-pages}

- [自动化变更审查](../concepts/automation-change-review.md)
- [测试你的规则](../guides/testing-your-rules.md)
- [双因素认证](../guides/two-factor-authentication.md)
- [GraphQL API](../reference/graphql-api.md)
