---
sidebar_position: 7
title: 向设备群发送命令
---

# 向设备群发送命令 {#commanding-a-fleet}

**命令批次**将同一条命令作为一次有记录的操作发送给多台设备。可以直接指定设备，也可以让平台从实体组解析设备。返回的是平台尝试执行操作的持久化记录：目标解析出了多少台设备、实际入队多少条命令，以及哪些设备被拒绝及其原因。

每台设备仍按单条命令的方式处理。命令根据该设备的能力契约验证，在设备离线时暂存，沿用相同的生命周期跟踪，并遵守相同的 TTL。请先阅读[发送命令](./sending-commands.md)；本指南仅介绍目标为设备群时的变化。

循环调用 `createCommand` 也可以向同样的设备发送命令，但无法：

- 留下尝试执行操作的记录；
- 固定组成员，避免循环期间修改选择器导致目标变化；
- 将整次操作统一取消。

批次位于 `command-delivery` 端点 `https://<your-host>/api/command-delivery/graphql`，使用租户访问令牌。发起和取消批次需要 `command:write`。读取批次记录需要 `command:read`。

:::warning 以组为目标还需要 `device:read`
将组解析为成员时，平台以自身身份读取设备注册表，结果会返回给你：拒绝列表包含设备令牌，`resolved` 会披露组大小。因此，以组为目标、读取组目标批次记录以及取消此类批次，都在命令权限之外额外需要 **`device:read`**。直接指定设备只需要命令权限，因为调用方已经知道这些设备。
:::

## 选择目标：设备或组 {#name-the-target-devices-or-a-group}

`deviceTokens` 和 `groupToken` 是互斥选项，必须**恰好提供一个**。同时提供或都不提供会以 `BATCH_TARGET_AMBIGUOUS` 拒绝，而不是按优先级处理，因为同时发送二者的调用方无法确定自己刚刚控制了哪个设备群。

### 指定设备 {#naming-devices}

- 一次请求最多指定 10,000 个令牌。超过该数量会返回 `BATCH_TOO_LARGE`，需要拆分操作。
- 顺序很重要。部分接纳的批次会按提供的顺序接纳设备，因此应将最重要的设备放在前面。
- 重复指定的令牌只计数一次。

### 指定组 {#naming-a-group}

- 组必须包含设备。
- 动态组必须已经发布。批次解析已发布的选择器，绝不解析草稿，因为设备群控制不能随编辑器中最近输入的内容变化。
- 传入 `groupVersion` 可固定特定的冻结版本，省略则使用当前已发布版本。为静态组指定版本会被拒绝，而不是忽略；没有指定组却指定版本也会被拒绝。
- 组解析出超过 10,000 台设备时，返回 `BATCH_TOO_LARGE`。平台会拒绝操作，而不会向前 10,000 台发送命令后报告成功。

记录会保存解析目标集合时使用的组版本。因此，即使之后有人编辑选择器，审计仍能回答批次发起时该组的含义。静态组从不进行版本管理，其存储版本为 null；设备列表批次也为 null。

:::note 组目标在发起时冻结
批次发起后再编辑动态组，不会改变已经发出的内容。参见[分面与动态组](../concepts/domain-model.md#facets-and-dynamic-groups)。
:::

## 决定如何处理部分分发 {#decide-what-a-partial-fan-out-means}

在实际设备群中，有些设备无法接收命令：某台不在注册表中，另一台的配置档未声明该命令，还有一台超出租户上限。`allowPartial` 决定此时的行为：

| `allowPartial` | 任意设备无法接收命令时 |
|---|---|
| `false` | 拒绝整个批次，**不创建任何内容**，包括批次记录——没有发生操作，因此无事可记录。拒绝结果列出导致拒绝的设备。 |
| `true` | 尽力执行。可以接收命令的设备获得命令。其余设备完全不会创建命令行，并出现在记录的拒绝列表中。 |

该标志对所有拒绝原因具有相同含义。它不只是对容量问题的容忍：启用后也意味着接受某台设备因配置档拒绝命令而被直接排除。

:::warning `allowPartial` 没有默认值，必须发送
它是没有默认值的非空 Boolean，因此省略该字段的请求无效。此字段决定物理控制是否可以只影响设备群中的部分设备，所以你需要明确意图，而不能继承一个可能未阅读过的模式定义中的默认选择。
:::

## 发起批次 {#fire-it}

```graphql
mutation {
  createCommandBatch(request: {
    token: "nightly-reboot-2026-08-14",
    name: "reboot",              # the commandKey, not the display name
    payload: "{\"delaySeconds\":5}",
    groupToken: "pumps-arid-us",
    allowPartial: true
  }) {
    batch {
      token targetKind groupToken groupVersion
      resolved accepted
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
    rejection {
      code reason resolved
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
  }
}
```

`name` 是设备词汇表中的 `commandKey`，与 `createCommand` 完全一致——参见[`commandKey` 是标识符](./sending-commands.md#find-out-what-the-device-accepts)。每个目标设备收到相同的键和载荷，这也是平台能够以合理成本验证设备群写入操作的基础。

`expiresAt` 设置批次创建的每条命令的 TTL。省略时，所有命令采用平台默认的七天。`metadata` 记录在批次记录上，不会复制到各条命令。

:::danger 检查 `rejection`，不要只检查错误
`createCommandBatch` **恰好返回** `batch` 或 `rejection` 之一。被拒绝的批次是携带 `rejection` 的成功 GraphQL 响应，而不是 GraphQL 错误。如果返回 GraphQL 错误而非二者之一，表示根本无法对批次作出决定：没有创建任何内容，令牌尚未使用，可以重试请求。
:::

### 令牌是幂等键 {#the-token-is-an-idempotency-key}

由你选择的 `token` 会在之后标识整个操作。重新提交已经对应某个批次的令牌，会返回**该批次，保持不变**。它绝不会追加更多设备，因为在同一令牌下接纳更多设备，会让 `accepted` 成为变化的数字，使记录无法审计。

因此，网络故障后的重试是安全的。这比单条命令更重要：结果未知的请求可能已让一万台泵重启。

批次没有 `TOKEN_IN_USE` 拒绝。已在使用的令牌不是冲突，而是重放。

## 批次被拒绝时 {#when-a-batch-is-refused}

根据 `code` 分支处理，**绝不要根据 `reason`**。原因是供人阅读的文字，措辞可能变化。

| `code` | 含义 | 是否重试？ |
|---|---|---|
| `BATCH_PARTIAL_REFUSED` | 至少一台设备无法接收命令，且未启用 `allowPartial`。没有创建任何内容。 | 阅读拒绝列表：每台设备自己的代码说明下次是否仍会被拒绝 |
| `HELD_CEILING_EXCEEDED` | 批次需要的未送达命令空间超过租户剩余容量。 | 可以：积压消退后即可解除 |
| `BATCH_TARGET_AMBIGUOUS` | 同时指定了两个目标、均未指定，或没有组却指定 `groupVersion`。 | 不应重试 |
| `BATCH_TOO_LARGE` | 设备数量超过单批次允许控制的数量，无论是直接指定还是从组解析。 | 不应原样重试：拆分操作或缩小组 |
| `BATCH_GROUP_UNUSABLE` | 组不存在、包含的不是设备、从未发布、指定版本不存在，或为静态组指定了版本。组服务自身的代码随原因返回。 | 不应重试 |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | 字符串不是有效 JSON。 | 不应重试 |
| `PAYLOAD_TOO_LARGE` / `METADATA_TOO_LARGE` | 载荷或元数据字符串超过 64 KiB。 | 否 |
| `COMMAND_NAME_TOO_LONG` | 命令名称超过 128 字节。 | 否 |
| `EXPIRES_AT_INVALID` | `expiresAt` 不是 RFC3339 时间戳。 | 不应重试 |

该列表可扩展。将无法识别的代码视为无法分类的拒绝，**绝不能视为成功**。

`BATCH_PARTIAL_REFUSED` 是唯一无法单独回答是否重试的代码，因此会附带问题设备。命令词汇表中缺少该命令的设备需要修改配置档，而容量不足导致拒绝的设备在积压消退后即可成功。一个代码无法同时表达两者，因此它不作判断，而是交由列表说明。

拒绝结果的 `refusals` 列表仅在代码为 `BATCH_PARTIAL_REFUSED` 时填充；其他所有代码都为空，包括 `HELD_CEILING_EXCEEDED`。这种差异是刻意设计的：

- 部分拒绝由特定设备引起，列出它们可避免你手动二分排查设备群。
- 上限拒绝由租户积压引起。请求中的设备并无问题，替换成员也不会改变结果；此时列出设备会诱导你修复本来正常的设备。处理办法写在 `reason` 中。

:::note 拒绝结果中的 `resolved` 可为 null，null 不等于零
`null` 表示从未确定目标集合：拒绝发生在任何解析之前。`0` 表示目标确实解析出零台设备，这是一个真实且成功的批次，而不是拒绝。
:::

## 读取记录 {#read-the-record}

```graphql
query {
  commandBatchesByToken(tokens: ["nightly-reboot-2026-08-14"]) {
    token name targetKind groupToken groupVersion allowPartial
    resolved accepted
    refusals { deviceToken code reason }
    refusalCounts { code count }
  }
}
```

也可以按命令键、组或 `targetKind`（`DEVICE_LIST` 或 `GROUP`）搜索：

```graphql
query {
  commandBatches(criteria: {
    pageNumber: 1, pageSize: 25,
    groupToken: "pumps-arid-us"
  }) {
    results { token name resolved accepted createdAt }
    pagination { totalRecords }
  }
}
```

:::warning `resolved` 和 `accepted` 描述批次发起时，而不是现在
它们是已存储的事实，不是实时计数。要查看当前送达状态，请搜索命令（参见[跟踪批次创建的命令](#follow-the-commands-it-created)）。
:::

命令行不会永久存在：可以软删除，也可以随租户一并擦除。如果通过实时查询推导 `accepted`，它可能降到创建时的真实数量以下，却没有拒绝记录解释差额，因此记录会存储这个值。

### `refusals` 是样本；`refusalCounts` 是完整计数 {#refusals-is-a-sample-refusalcounts-is-complete}

`refusals` **每个代码最多保留 100 条**，因此针对大组发起的批次，实际拒绝的设备可能多于记录列出的设备。`refusalCounts` 是每个代码的完整总数，从不截断，因此记录本身即可用于审计：

```
resolved = accepted + the sum of refusalCounts
```

该恒等式始终成立。样本可能较短；将其长度与计数比较，即可判断是否达到截断上限。

每台设备的 `code` 使用与单条入队拒绝相同的开放词汇：

- `DEVICE_NOT_FOUND`
- `COMMAND_NOT_IN_VOCABULARY`
- `PAYLOAD_SCHEMA_VIOLATION`，从设备配置档转达
- `HELD_CEILING_EXCEEDED`，表示设备超出租户剩余容量

各代码的含义参见[入队被拒绝时](./sending-commands.md#when-an-enqueue-is-refused)。

## 跟踪批次创建的命令 {#follow-the-commands-it-created}

批次记录刻意保持不变。要询问设备群写入正在做什么，例如“已排队的 5,000 条中，多少已经发出？”，请使用 `batchToken` 搜索命令：

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    batchToken: "nightly-reboot-2026-08-14",
    statuses: ["QUEUED", "HELD", "PARKED"]
  }) {
    results { token deviceToken status queuedTime }
    pagination { totalRecords }
  }
}
```

平台生成各条命令的令牌；你选择的是批次令牌，不是命令令牌。因此应通过 `batchToken` 查找，而不是自行构造令牌。

关联也支持反向查询。命令行带有可读取的 `batchToken` 字段，因此持有单条命令的调用方——可能来自设备历史，或没有上下文的响应——可以查明哪个设备群操作创建了它：

```graphql
query {
  commands(criteria: { pageNumber: 1, pageSize: 20, deviceToken: "gw-4471" }) {
    results { token name status batchToken }
  }
}
```

单独发送的命令，其 `batchToken` 为 null。这是区分两者的唯一字段。批次发送的命令键和载荷与设备单独接收时相同，因此行上的其他内容没有差别。

这一查询方向很重要，因为单条命令行无法展示设备群操作中值得关注的部分：被拒绝的设备。它们没有获得命令，因此不会出现在任何设备的历史中。只有批次记录知道它们曾被设为目标。

## 取消批次 {#call-the-whole-thing-off}

```graphql
mutation {
  cancelCommandBatch(token: "nightly-reboot-2026-08-14") {
    cancelled
    alreadySent
    alreadyFinished
    matched
  }
}
```

| 字段 | 含义 |
|---|---|
| `cancelled` | 权威数量。相应数量的命令从 `QUEUED`、`HELD` 或 `PARKED` 转为 `CANCELLED`，不会再送达。 |
| `alreadySent` | 已经分发到设备的命令。设备**仍会执行这些命令**。 |
| `alreadyFinished` | 已达到终态的命令：`SUCCESSFUL`、`FAILED`、`TIMEOUT`、`EXPIRED` 或 `CANCELLED`。 |
| `matched` | 当时仍有效的批次命令行数量（见[下文](#matched-and-the-other-counts)）。 |

这与 `cancelCommand` 对单条命令施加的制动相同：二者都会取消 `QUEUED`、`HELD` 和 `PARKED`，都不会触及 `SENT`。为何以 `SENT` 为界，参见[取消批次](../concepts/commands.md#cancelling-a-batch)。

取消**绝不会被拒绝**。如果因为设备群的一部分已行动而拒绝制动，其余设备仍会受命行动，这是最糟的结果。因此，即使所有命令都已发送，调用仍会成功并报告 `cancelled: 0`。请阅读计数，不要假设调用没有作用。不匹配任何批次的令牌则会产生 GraphQL 错误。

取消需要 `command:write`；以组为目标的批次还需要 `device:read`，原因与发起时相同。

批次记录本身会写入 `cancelledAt` 和 `cancelledCount`，因此取消操作与分发操作一样可审计。`cancelledCount` 是该次调用捕获的数量。记录采用**首次生效**规则：第二次取消不会覆盖第一次的记录。

### `matched` 与其他计数 {#matched-and-the-other-counts}

`matched` 是实时计数，这四个数字不一定相加吻合。

`matched` 统计当时仍有效的批次命令行，而不是批次创建过的数量。通过清理或删除移除的行已不存在，无法匹配。因此 `matched` 小于批次的 `accepted` 很正常，并不能说明取消结果。

`matched` 也可能*大于* `cancelled + alreadySent + alreadyFinished`。送达失败的命令可能在取消与计数之间返回队列。这样的命令不会归入这三个类别中的任何一个，而不会被计入 `alreadyFinished`，因为这套词汇要避免的正是将仍有效的命令报告为已完成。再次取消即可捕获它。

取消标记本身使这种情况很少发生：一旦取消已提交，送达失败就会终结命令，而不是将其放回队列。例外是恰好在取消瞬间释放的命令，它会在已取消的批次中继续有效。应再次取消，而不是等待。

## 批次与单条命令共享的限制 {#what-a-batch-does-not-change}

批次受到的限制与循环发送单条命令完全相同。接纳依据租户的[未送达命令上限](../concepts/commands.md#held-command-ceiling)，减去[为平台自身送达预留的份额](../concepts/commands.md#delivery-machinery-reserve)。二者都无法绕过，采用哪种形式都没有优势。

实际中，租户接近上限时：

- 启用 `allowPartial` 后，大规模分发可能只得到部分接纳。容量不足的设备会返回逐设备的 `HELD_CEILING_EXCEEDED` 拒绝。
- 未启用时，整个批次以该代码拒绝，不创建任何内容。

无论哪种情况，这都是暂时状态，而不是请求本身的缺陷。积压消退后，使用**新**令牌即可向剩余设备发送命令。重放原令牌无法做到这一点，因为重放会返回已有批次。
