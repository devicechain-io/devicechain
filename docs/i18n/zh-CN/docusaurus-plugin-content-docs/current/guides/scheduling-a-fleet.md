---
sidebar_position: 8
title: 为设备群安排计划
---

# 为设备群安排计划

**活动（campaign）** 按计划向某个实体组中的设备发送命令，并为它尝试过的每台设备保留记录。路灯照明正是
它的设计场景：让每盏灯在各自的日落时调暗、在各自的日出时恢复，跳过公共假日，并在第二天早晨知道哪些灯
没有执行以及原因。

一个活动由三部分组成：

- **触发器**，说明何时触发：cron 表达式、日历日期，或太阳；
- 一个或多个**步骤**，每个步骤是一条命令，可带载荷，并可在触发后延迟执行；
- **目标**，即一个设备实体组。

每次触发器触发时，平台会规划一个**周期（cycle）**。一个周期为每台设备的每个步骤包含一个**元素**，元素
是跟踪与重试的单位。在底层，每一组到期的元素会成为一个普通的[命令批次](./commanding-a-fleet.md)，因此
每台设备都会依据其能力契约校验、按其[维护窗口](./maintenance-windows.md)暂缓、沿命令生命周期被跟踪，并
按生存时间到期，与其他任何命令完全一致。请先阅读这两篇指南；本篇只介绍计划新增的部分。

活动位于 `command-delivery` 端点 `https://<your-host>/api/command-delivery/graphql`。创建、启停和删除活动
以及重试失败项需要 `command:write`，读取需要 `command:read`。所有操作还需要 `device:read`，因为活动指向
一个组，其跟踪会列出组内设备。仅限于租户部分范围的成员不能使用活动。

## 创建活动 {#create-a-campaign}

```graphql
mutation {
  createCampaign(request: {
    token: "dusk-dim"
    name: "Dim at dusk"
    trigger: "{\"kind\":\"SOLAR\",\"zone\":\"Europe/Berlin\",\"event\":\"SUNSET\",\"offsetMinutes\":-15}"
    steps: [
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":40}" }
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":20}", delaySeconds: 21600 }
    ]
    groupToken: "city-streetlights"
  }) {
    campaign { token enabled plannedThrough lastError }
    rejection { code reason }
  }
}
```

无法接受的定义会以 `rejection` 返回，而不是错误。代码包括 `CAMPAIGN_TRIGGER_INVALID`、
`CAMPAIGN_STEPS_INVALID`、`CAMPAIGN_STEP_UNSUPPORTED`、`CAMPAIGN_TARGET_INVALID`、
`CAMPAIGN_FIELD_INVALID`、`TOKEN_IN_USE`，以及单条命令被拒绝时使用的载荷和命令名称代码。

| 字段 | 含义 |
| --- | --- |
| `token` | 在租户内唯一。 |
| `trigger` | 一个 JSON 文档，见下文。 |
| `steps` | 一到十个步骤。步骤为 `COMMAND`，带 `name`、可选的 JSON `payload`，以及触发后 0 到 86400 的 `delaySeconds`。 |
| `groupToken` | 目标实体组。动态组可以选择某个区域的设备。 |
| `maxLatenessSeconds` | 已规划的命令最多还能晚发多久，1 到 86400。默认 900。 |
| `expiresInSeconds` | 每条命令的生存时间，1 到 604800，从创建时起算。默认 3600。它决定命令可以为不可达的设备或维护窗口等待多久，因此可能长于 `maxLatenessSeconds`；后者限制的是调度器何时发送，而不是设备何时收到。如果迟到的送达比不送达更糟，请把它设短。 |
| `enabled` | 是否一开始就启用。默认 true。 |

`ARTIFACT` 步骤类型属于词汇表，但本版本不执行；它会以 `CAMPAIGN_STEP_UNSUPPORTED` 被拒绝，而不是被忽略。
若要按计划推出更新，请使用[分批发布](../concepts/over-the-air-updates.md)。

## 触发器 {#triggers}

每个触发器都需要在 `zone` 中给出 IANA 时区。空时区或 `Local` 会被拒绝：跟随主机的计划会在 pod 迁移时跟着
移动。

### Cron

```json
{"kind":"CRON","zone":"America/New_York","cron":"30 18 * * *"}
```

五个字段：分、时、日、月、星期。每个字段接受 `*`、数字、`a-b`、`*/n`、`a-b/n` 及逗号列表。星期取值 0 到 7，
0 和 7 都表示星期日。当两个日字段都有限制时，满足任一即匹配。名称、`?`、`L` 和 `@daily` 会被拒绝。

### 日历

```json
{"kind":"CALENDAR","zone":"Europe/London","dates":["2026-12-24","2026-12-31"],"times":["07:00","17:30"]}
```

在列出的每个日期的每个时间触发，按该时区计算。

### 太阳

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","offsetMinutes":-15}
```

在 `SUNSET` 或 `SUNRISE` 加上 `offsetMinutes`（负数表示提前）时触发，**针对每台设备依据其自身的安装位置
单独计算**。因此同一个触发器在每盏灯上触发的时刻都不同，一座城市的灯会随太阳依次调暗。

安装位置即设备的安装位置记录，通过 `setInstalledPosition` 变更或设备批量导入的安装位置列写入：

```graphql
mutation {
  setInstalledPosition(family: DEVICE, token: "lamp-0001",
    position: { latitude: 52.52, longitude: 13.40, source: SURVEY }) { latitude longitude }
}
```

请在灯具调试投运时设置。它刻意不是最近一次上报的位置：灯不会移动，接收机故障的灯也不应在错误的日落时刻
调暗。该计算在大约北纬与南纬 60 度之间精度约为两分钟。

太阳事件不涉及时区，因为太阳并不知道时区。这里的时区只用来指明日期和假日所属的公历日。

两类设备会被记录为**跳过**，而不是被猜测：

- 没有安装位置的设备记为 `NO_SITE`；
- 当天太阳不升起或不落下的设备（极圈以内）记为 `POLAR_DAY` 或 `POLAR_NIGHT`。

如果活动带有太阳触发器而设备位置完全无法读取，规划会失败，原因显示在活动的 `lastError` 中；不会发送任何内容。

### 假日

任何触发器都可以带 `holidays`，即该时区中不触发的本地日期列表：

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","holidays":["2026-12-24","2026-12-25"]}
```

假日会抑制一次触发，绝不会把它挪到其他日子。

### 夏令时 {#daylight-saving}

cron 或日历中的时间是该时区的挂钟时间。在每年两个存在歧义的日子里：

- 落在春季跳变空档内的时间，在时钟跳变的那一刻触发一次；
- 在秋季回拨的那个小时内出现两次的时间，仅在其第一次出现时触发。

## 维护窗口 {#maintenance-windows}

活动本身没有窗口。它创建的每条命令都经过与其他命令相同的关卡：发往其设备类型设有
[维护窗口](./maintenance-windows.md)的设备的破坏性命令会被暂缓，直到窗口开启；若窗口要到命令过期之后才
开启，则直接拒绝。被暂缓的命令在周期计数中算作 `waiting`。请把 `expiresInSeconds` 设得足够长，以覆盖你愿意
允许的等待时间。

## 延迟或宕机时会怎样 {#late-and-down}

调度器在每个副本中每 15 秒运行一次，没有主节点；无论有多少副本在运行或重启，一台设备只会被下发一次命令：
每个元素的认领是数据库中的比较并设置操作，它所创建的批次带有由其元素派生的令牌，因此接手已宕机副本工作的
副本是重放该批次而不是重复创建。

超过**迟到上限**的命令绝不会发送。一个到期后未在 `maxLatenessSeconds` 内被认领的元素（发生了宕机，或活动
被关闭）会被标记为 `SKIPPED`，原因为 `MISSED`。午夜才发出的调暗命令比不发更糟，所以活动绝不会补发。

关闭活动会停止规划与发送。删除活动会取消所有已规划但尚未发送的内容，标记为 `SKIPPED`，原因为
`CANCELLED`；已创建的命令不受影响。

租户的[未送达命令上限](./commanding-a-fleet.md)依然适用。如果一个非常大的设备群在同一时刻到期，超出上限的
设备会被记录为 `REFUSED`，原因为 `HELD_CEILING_EXCEEDED`，并可以重试。

## 跟踪一个周期 {#follow-a-cycle}

```graphql
query {
  campaignCycles(campaignToken: "dusk-dim", limit: 3) {
    key day resolved
    counts { pending dispatched refused skipped succeeded inFlight waiting failed unknown }
  }
}
```

`counts` 回答第二天早晨的问题。前五个数字按活动的状态统计元素：`pending`、`claimed`、`dispatched`、
`refused`、`skipped`。其余数字统计已下发元素背后的命令：`succeeded`、`inFlight`（其中因窗口而 `waiting`
的数量）、`failed`（未成功结束且可以安全重发）以及 `unknown`（可能已经执行）。

要查看设备，可按状态分页浏览元素：

```graphql
query {
  campaignElements(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", status: "SKIPPED", limit: 200) {
    id deviceToken step dueAt status reason commandStatus commandOutcome
  }
}
```

把最后一个 `id` 作为 `afterId` 传入即可获取下一页。

## 重试失败项 {#retry-from-failed}

```graphql
mutation {
  retryCampaignFailed(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", includeUnknown: false) {
    batches retried held
  }
}
```

这会把该周期中未成功的元素交给平台的[批次重试](./commanding-a-fleet.md)，因此它的安全保障同样适用：命令仍在
进行中或已成功的设备绝不会收到第二份副本；结果未知的设备只有在你传入 `includeUnknown: true`（并已确认执行
两次无害）时才会收到。被批次拒绝的元素也会被重试。`held` 统计因重发不安全而被保持不动的元素。

一个元素只重试一次。如果重试本身失败，请通过[批次 API](./commanding-a-fleet.md) 重试那个批次。

## 限制 {#limits}

- 一个周期最多规划 100,000 台设备。
- 周期中的设备是该周期被规划时（最早在触发前 36 小时）组内的成员。之后加入组的设备会进入下一个周期。
- 已结束的周期及其元素保留 30 天。
- 目标是一个实体组。不支持直接以区域为目标；请使用选择该区域设备的动态组。
- 步骤是一条命令。本版本不执行更新步骤。
