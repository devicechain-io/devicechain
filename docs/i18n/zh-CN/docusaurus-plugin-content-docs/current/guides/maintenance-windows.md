---
sidebar_position: 8
title: 维护窗口
---

# 维护窗口 {#maintenance-windows}

维护窗口可以避免重启等破坏性命令在错误的时间开始。本指南介绍如何设置窗口，并观察命令如何等待窗口开启。背后的原理见[维护窗口](../concepts/commands.md#maintenance-windows)。

窗口在设备管理的 GraphQL API 中管理，地址为 `https://<your-host>/api/device-management/graphql`。命令按[发送命令](./sending-commands.md)所述，通过命令投递 API 发出。

## 创建窗口 {#create-a-window}

窗口由一个时区加 1 到 56 个每周时段组成。`slots` 是 JSON **字符串**。每个时段包含 `day`（`MONDAY` 到 `SUNDAY`）、`start`（24 小时制 `HH:MM`，该时区的墙上时钟时间）和 `durationMinutes`（1 到 10080，经过的时间）。

```graphql
mutation {
  createMaintenanceWindow(request: {
    token: "weekend-night",
    name: "Weekend night",
    timezone: "Europe/Berlin",
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"22:00\",\"durationMinutes\":240}]"
  }) {
    token timezone slots
  }
}
```

`Local` 和未知的时区名称会被拒绝。时段可以跨过午夜，所以上例在周六夜间到周日的 22:00 至 02:00 开启。要检查窗口的行为，可以询问它何时开启：

```graphql
query {
  maintenanceWindowOpening(token: "weekend-night", at: "2026-11-02T12:00:00Z") {
    found openNow dispatchAfter closes
  }
}
```

窗口开启时，`dispatchAfter` 为你询问的时刻，否则为下一次开启时间。省略 `at` 表示当前时刻。

## 分配给设备类型 {#assign-it-to-a-device-type}

一个设备类型最多有一个窗口，该类型的所有设备都遵循它。

```graphql
mutation {
  updateDeviceType(token: "pump-controller", request: {
    maintenanceWindowToken: "weekend-night"
  }) {
    token
    maintenanceWindow { token }
  }
}
```

发送 `null` 或空值即可解除。没有窗口的设备类型会像以前一样立即发送所有命令。

## 将命令标记为 DISRUPTIVE {#mark-a-command-disruptive}

只有声明了 `DISRUPTIVE` 或 `DESTRUCTIVE` 影响级别的命令才会等待。在命令定义上设置它，然后**发布配置文件**；闸门读取的是已发布的词汇表。

```graphql
mutation {
  createCommandDefinition(request: {
    token: "pump-reboot",
    deviceProfileToken: "pump-profile",
    commandKey: "reboot",
    impact: "DISRUPTIVE"
  }) { token impact }
}
```

省略时 `impact` 为 `NORMAL`。控制台的 **Add LwM2M standard commands** 会将 `lwm2m.reboot` 创建为 `DISRUPTIVE`。此前声明的定义不会改变。键未声明为这些影响级别的命令（包括原始 `lwm2m.execute`）从不保留。

## 发送命令并观察其等待 {#send-and-watch-it-wait}

照常发送命令。窗口关闭时，命令被接受并等待。窗口开启时，命令立即发送，但仅限于窗口关闭之前。

```graphql
mutation {
  createCommand(request: {
    token: "reboot-pump-7",
    deviceToken: "pump-007",
    name: "reboot"
  }) {
    command { token status dispatchAfter maintenanceWindowToken }
    rejection { code reason }
  }
}
```

命令保持 `QUEUED`，`dispatchAfter` 为下一次开启时间，`maintenanceWindowToken` 为 `weekend-night`。窗口开启后，它与其他命令一样被发送，包括稍后醒来的休眠设备。等待中的命令计入租户的未送达命令上限，并可像任何排队命令一样单个或整批取消。批次记录的 `waiting` 表示有多少成员因窗口而被保留。

如需在不发送任何命令的情况下查看会发生什么，可用设备令牌、命令键和可选的 `expiresAt` 查询 `commandDispatchSchedule`。

窗口只控制开始。在窗口内已开始的命令何时结束就何时结束。

## 窗口开启得太晚：WINDOW_AFTER_EXPIRY {#window-after-expiry}

除非设置 `expiresAt`，命令在七天后过期。如果下一次开启时间晚于此，入队会以代码 `WINDOW_AFTER_EXPIRY` 被拒绝，因为接受它只会以 `EXPIRED` 告终。在批次中它是逐设备拒绝，除非设置 `allowPartial`，否则整个批次被拒绝。原样重试没有意义。可以采取以下任一办法修复：

- 设置更晚的 `expiresAt`，使命令能撑过等待；
- 为窗口增加时段，或把下一个时段提前，使其在命令过期之前开启。

## 编辑或删除窗口 {#edit-or-delete-a-window}

`updateMaintenanceWindow` 是部分更新：省略的字段保持不变。

```graphql
mutation {
  updateMaintenanceWindow(token: "weekend-night", request: {
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"23:00\",\"durationMinutes\":180}]"
  }) { token slots }
}
```

已在等待的命令会在大约一分钟内跟随新的时间表，因为命令投递是轮询而不是被通知。不要指望立即生效。

只要有设备类型在使用该窗口，`deleteMaintenanceWindow` 就会被拒绝；请先解除。从类型上解除窗口不会释放已经在等待它的命令：它们继续等待该窗口开启，或等待窗口本身被删除（删除会释放它们）。如果设备在整个开启期间都离线，其命令会顺延到下一次开启，而不是在设备返回时立即发送。

## 限制 {#limits}

- 窗口属于设备类型。没有分组、按设备或租户默认的窗口，也没有禁止窗口或节假日。
- 空中升级的控制尚未构建。
- 入队时如果无法访问设备管理，命令会被拒绝，而不是不受控制地发送。

计数器 `command_delivery_window_restamps_total` 统计其保留因编辑、删除，或窗口在设备返回前再次关闭而被移动的命令数。
