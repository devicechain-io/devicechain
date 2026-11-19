---
sidebar_position: 9
title: 告警维护
---

# 告警维护

告警抑制窗口会在计划维护期间暂存通知。告警仍会触发并保持可见，同时显示抑制标记。检测、严重程度和贡献规则照常运行。此功能独立于[命令维护窗口](./maintenance-windows.md)。

通过 device-management GraphQL API 管理窗口：`https://<your-host>/api/device-management/graphql`。

## 选择窗口 {#choose-the-window}

提供窗口 token 和必填原因。目标支持 `DEVICE`、`DEVICE_TYPE`、`ENTITY_GROUP`、`AREA`、`ASSET`、`CUSTOMER` 和 `TENANT`；除 `TENANT` 外均需目标 token。任一目标匹配即可生效。`alarmKeys` 为空时匹配全部告警键，否则仅匹配所列键。达到或超过 `bypassSeverity` 的告警照常发送通知。

`ONE_SHOT` 使用 `startTime` 和 `endTime`；`RECURRING` 使用 IANA `timezone` 和每周时段 `slots`（JSON 字符串），可用 `validFrom`、`validUntil` 限定有效期。通过 `openings` 预览后续区间。区间包含开始时间、不包含结束时间；多个窗口匹配时采用最晚的结束时间。

```graphql
mutation {
  createAlarmSuppressionWindow(request: {
    token: "pump-service"
    reason: "水泵检查"
    targets: [{type: "DEVICE", token: "pump-007"}]
    kind: ONE_SHOT
    startTime: "2026-12-01T10:00:00Z"
    endTime: "2026-12-01T12:00:00Z"
    alarmKeys: ["temperature"]
    bypassSeverity: "CRITICAL"
  }) { token status openings { start end } }
}
```

每个窗口最多 20 个目标和 20 个告警键。一次性窗口最长 31 天。每个租户最多 500 个有效窗口，包括尚未结束或过期的预定窗口。

## 权限与范围 {#permissions-and-scope}

读取需要 `alarm:read`。小范围窗口仅使用 `DEVICE` 目标、类型为 `ONE_SHOT` 且不超过 24 小时，创建或编辑需要 `alarm:write`。更大范围的窗口还需要 `notification:write`。编辑必须同时满足现有窗口和修改后窗口的权限要求。

`endAlarmSuppressionWindow` 只需要 `alarm:write` 且窗口对您可见。重复结束会保留首次结束时间。`deleteAlarmSuppressionWindow` 需要创建该窗口时所需的权限。受范围限制的成员只能查看和管理完全位于其范围内的窗口，不能修改目标。设备归属变化会刷新窗口可见性；受限成员创建的窗口仍受创建者原始范围约束。

## 观察通知释放 {#follow-the-release}

窗口结束后，如果告警仍处于活动状态且尚未确认，通知会根据积压情况在几次调度器遍历内发送。如果该周期尚未发送过通知，窗口期间确认或清除告警不会发送任何通知，包括清除通知。该周期已通知过的目标仍按现有策略接收清除通知。策略静默时段可能继续延长等待。结束、延长或删除窗口会在下次状态复查时生效。释放的通知会说明维护暂存原因，升级计时从发送时开始。

观察 [`suppression_total` 和 `deferral_oldest_due_seconds`](../deployment/metrics-reference.md#notification-management)。计数器记录暂存、复查、释放及查询失败；仪表衡量逾期释放工作，当已访问租户均无到期行时归零。状态查询失败或不受支持时允许发送通知。

**维护期间触发**标记记录处理该告警周期时已声明的窗口，并按周期事件时间求值。维护结束后历史标记仍保留。使用告警搜索条件 `raisedDuringSuppression` 筛选有标记或无标记的周期；这是历史筛选条件，并非实时抑制筛选条件。

滚动升级期间，窗口内的告警仍可能发送通知。请在 device-management 和 notification-management 均完成部署后再依赖抑制功能。
