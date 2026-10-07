---
sidebar_position: 3
title: 发送命令
---

# 发送命令 {#sending-a-command}

本指南介绍双向命令派发中运维人员的一侧：发出命令、区分接受与拒绝，并跟踪结果。设备一侧的接收与结果报告见[连接设备](./connecting-a-device.md#responding-to-a-command)。两侧共同推进的生命周期见[命令](../concepts/commands.md)。

使用租户访问令牌，在 `command-delivery` 端点 `https://<your-host>/api/command-delivery/graphql` 发出、读取和取消命令。发出和取消需要 `command:write`，读取历史需要 `command:read`。

下面第一步是唯一例外。查询设备接受什么是 `device-management` 查询，使用不同端点 `https://<your-host>/api/device-management/graphql` 和权限 `device:read`。

## 查询设备接受什么 {#find-out-what-the-device-accepts}

设备命令集合来自配置文件，因此应询问 `device-management`，不要猜测：

```graphql
query {
  deviceCommandVocabulary(deviceToken: "sensor-001") {
    constrained
    commands { commandKey name description parameterSchema }
  }
}
```

:::warning `commandKey` 是标识符，`name` 是标签
`PublishedCommand` 同时携带两者。接受/拒绝检查匹配 `commandKey`，应将它放入 `createCommand` 的字段，尽管该字段令人困惑地叫作 `name`。集合条目的 `name` 只是显示标签，不参与匹配。发送标签会对设备明明支持的命令返回 `COMMAND_NOT_IN_VOCABULARY`。
:::

读取 `constrained`，而不是根据 `commands` 长度判断：

- **`constrained: false`**：列表为空，接受*任何*命令键。空列表不是不接受命令，而是配置文件没有声明命令集合。
- **`constrained: true`**：键必须精确匹配某个条目，包括大小写。载荷按该命令参数模式验证。

## 发出命令 {#issue-it}

```graphql
mutation {
  createCommand(request: {
    token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
    deviceToken: "sensor-001",
    name: "reboot",          # the commandKey, not the display name
    payload: "{\"delaySeconds\":5}",
    expiresAt: "2026-08-15T00:00:00Z"
  }) {
    command { token status queuedTime }
    rejection { code reason }
  }
}
```

- `token` 由你选择，之后用于引用命令。
- `payload` 和 `metadata` 是 JSON **字符串**。
- `expiresAt` 可选，参见[设置合适的 TTL](#set-a-ttl-you-can-live-with)。

使用已有令牌重新发出，不会创建第二条命令，而是原样返回原命令。因此网络故障后的重试安全。命令是物理控制操作，响应丢失不能导致设备重启两次。

这种重放只适用于**你**拥有的命令。如果令牌属于平台为批次创建的命令，返回 `TOKEN_IN_USE`。将另一台设备的操作当作你的命令返回，比拒绝更糟。

## 入队被拒绝时 {#when-an-enqueue-is-refused}

:::danger 检查 `rejection`，不只是检查错误
`createCommand` **恰好返回** `command` 或 `rejection` 之一。入队拒绝是携带 `rejection` 的成功 GraphQL 响应，不是 GraphQL 错误。只检查 `errors` 数组的客户端会将拒绝视为成功，报告从未创建的命令。
:::

这种区分是有意的。拒绝是已确定判断：请求有误，拒绝说明具体原因。GraphQL 错误表示平台根本无法回答。无法区分的机器调用方会不断重试永久无效命令，直到重投递上限耗尽，表现与故障相同。

根据 `code` 分支，绝不根据 `reason`。原因是给人看的文字，措辞可能变化。

| `code` | 含义 | 是否重试？ |
|---|---|---|
| `HELD_CEILING_EXCEEDED` | 租户达到**未投递**命令上限：全部 `QUEUED`、`HELD`、`PARKED`，不只离线设备保留命令。 | **是**，这些命令投递后会解除 |
| `DEVICE_NOT_FOUND` | 此租户没有该令牌设备。 | 否 |
| `COMMAND_NOT_IN_VOCABULARY` | 配置文件限制命令，而此键不在集合中。检查大小写。 | 否 |
| `PAYLOAD_SCHEMA_VIOLATION` | 载荷违反参数模式：未知参数、错误类型、超范围或缺少必填参数。 | 否 |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | 字符串不是有效 JSON。 | 否 |
| `EXPIRES_AT_INVALID` | `expiresAt` 不是 RFC3339 时间戳。 | 否 |
| `TOKEN_IN_USE` | 令牌由不属于你的命令占用，实际通常是平台为批次创建的命令。 | 否，请换令牌 |
| `COMMAND_REJECTED` | 收到没有分类的拒绝。 | 否 |

列表不是封闭的。未知代码应视为无法分类的拒绝，绝不视为成功。

只有 `HELD_CEILING_EXCEEDED` 是临时情况。其他代码都说明请求下次仍有相同错误。重试浪费次数，并向能修复的人隐藏真实缺陷。

即使整个设备群在线，也可能达到上限。上限约束*未投递*工作，排队命令等待下一投递扫描时也计入。参见[租户可以保留多少积压](../concepts/commands.md#held-command-ceiling)。

## 跟踪结果 {#follow-it-to-an-outcome}

命令**没有订阅**，因此需要轮询。按令牌获取命令：

```graphql
query {
  commandsByToken(tokens: ["6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11"]) {
    token status sentTime respondedTime responsePayload error
  }
}
```

也可以搜索，用 `status` 筛选单个状态，或 `statuses` 筛选一组状态：

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    statuses: ["HELD", "PARKED", "SENT"]
  }) {
    results { token name status queuedTime }
    pagination { totalRecords }
  }
}
```

关注状态集合时使用 `statuses`。“此设备所有仍在处理的命令”包括：

- `HELD`：设备缺席，因此保留。
- `PARKED`：已发布，但设备实际未唤醒。
- `SENT`：已派发，尚无答复。

空 `statuses` 列表会被忽略，而不是匹配零条。

各终态含义见[命令](../concepts/commands.md#command-lifecycle)。要记住的区别是：`EXPIRED` 表示未到达设备，`TIMEOUT` 表示已发出。连续 `EXPIRED` 指向派发，连续 `TIMEOUT` 指向设备。

## 取消命令 {#cancel-one}

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

可以从 `QUEUED`、`HELD` 和 `PARKED` 取消，即平台仍持有的状态，记录为 `CANCELLED`。适用情况包括因设备缺席而保留，或已发布但设备实际休眠。两者都可以在投递前取消，这正是保留而不是盲目发送的主要意义。

**`SENT` 命令不会取消。** 取消无法召回已派发命令。将它改为 `CANCELLED` 不会阻止任何执行，只会使平台丢弃之后到达的真实答案。设备执行、响应消失、记录却说操作已取消。因此调用成功，但原样返回仍为 `SENT` 的命令。取消与投递竞争，未抢先属于正常情况。

取消终态命令也不是错误。它按已达到的状态原样返回。因此，取消输给响应的情况，看起来是成功调用返回 `SUCCESSFUL`。

两种情况下都应**检查返回的 `status`**，不要假定取消已生效。没有匹配命令的令牌*会*产生错误。

`cancelCommandBatch` 对整个设备群操作采用完全相同的刹车：取消同样状态，并停在 `SENT` 边界。参见[取消批次](../concepts/commands.md#cancelling-a-batch)。

## 设置合适的 TTL {#set-a-ttl-you-can-live-with}

每条命令都带有 TTL。传入 `expiresAt` 设置，否则使用平台默认**七天**。

等待七天才得知命令失败很久。如果设备不报告结果，命令会整周停在 `SENT`，才由 `TIMEOUT` 确认你早已怀疑的情况。按该操作“仍有用途”的时间设置 `expiresAt`，十分钟内未执行的重启通常不会再执行。

## 同时命令多台设备 {#commanding-many-devices-at-once}

上述操作都是向一台设备发一条命令。要向显式指定或从实体组解析的整个设备群发送同一命令，作为可审计、可取消的单个操作，参见[向设备群发送命令](./commanding-a-fleet.md)。设备群命令不是此变更操作的循环。它固定触发时分组成员关系，记录被拒绝设备及原因，并作为整体取消。

## 五种不面向你的操作 {#operations-that-are-not-for-you}

模式中有 `markCommandSent`、`confirmCommandDispatch`、`releaseHeldCommands` 和 `parkCommand`，但它们受租户访问令牌不携带的**系统级**权限控制：前两者需要 `command:claim`，后两者分别为 `command:wake`、`command:park`。它们供拥有设备连接的传输使用：

- LwM2M 设备通过刚建立的会话排空积压；
- LwM2M 适配器在执行前确认投递仍有效；
- 消息代理报告设备返回；
- 传输发现目标设备不可达，将命令退回。

应用调用这些操作，会与平台投递流程争夺物理操作控制权。

`drainableCommands` 是这些传输先进行的读取。它由 **`command:claim`** 控制，与 `markCommandSent` 相同，而不是再新增一项权限：有权认领设备命令的调用方，也正是有权查询待认领命令的调用方。给定设备令牌，它返回仍等待该设备的 `HELD` 和 `PARKED`，排除过期命令，按**最旧优先**排列，并受 `limit` 限制。省略或非正 `limit` 使用 32，上限 1000。

顺序正是该查询的目的。固件写入必须先于执行到达设备，其他顺序不仅是延迟，而是倒着执行更新流程。

`command:read` 不能使用此查询，应用也不需要它。查看设备等待内容时，使用 [`commands` 查询](#follow-it-to-an-outcome)，配合 `statuses: ["HELD", "PARKED"]`。
