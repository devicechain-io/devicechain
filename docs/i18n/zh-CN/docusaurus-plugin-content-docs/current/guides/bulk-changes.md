---
title: 设备批量变更
---

# 设备批量变更

**Preview.** 使用 `dcctl bulk-change` 为多个设备规划、创建、启动、查询、暂停、恢复、中止或撤销 `config.set` 变更，并对结果不明确的行作出裁定。它设置或移除一个已声明的 SHARED 配置键。

批量变更自 v1.10 起提供，可通过 device-management GraphQL API（`createBulkChange`、`startBulkChange`、`bulkChange` 等操作列于 [GraphQL 模式参考](/reference/graphql/)）以及这些命令使用。两者均为 Preview：可能在次版本中变更。v1.10 之前的服务器返回 **this instance does not serve bulk changes**。滚动升级到 v1.10 期间，发现的模式与处理请求的 Pod 可能不同，且只有已升级的 Pod 会应用行；**still upgrading; retry** 表示应等待后重试。API 不可用不会被报告为令牌不存在。

只有实例已为其启用时变更才会执行：当数据库强制执行设备属性写入协议、且你的租户没有孤立属性行时，`dcctl bulk-change readiness` 会在可执行类型中列出 `config.set`。启动和恢复检查的正是这两项。

## 访问权限 {#access}

通过 `--tenant`、`--email` 和 `--password` 以租户成员身份登录；需要第二因素时使用 `--otp`。通过 `--server` 和 `--tls` 选择主机，或使用 `--instance` 读取已声明的主机与 TLS 设置。

规划、创建、启动、恢复、撤销和裁定需要 `device:write` 与 `fleet:operate`。暂停和中止需要 `device:write`；读取需要 `device:read`。代理身份、服务身份及受范围限制的成员无法使用这些操作。已弃用的 `dcctl fleet-op` 名称仅保留 `readiness`。

## 规划与创建 {#plan-and-create}

准备 UTF-8 文件，每个非空行包含一个设备令牌，或重复指定 `--device TOKEN`。只能选择一种目标来源。最多接受 10,000 个输入令牌和 1 MiB 文件数据。

```bash
cat > devices.txt <<'EOF'
meter-001
meter-002
EOF

dcctl bulk-change plan --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password>
dcctl bulk-change create --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password> -o json
```

规划不写入数据，会报告接受与拒绝的数量、拒绝详情以及服务器摘要。创建将目标冻结为 READY，并返回合同及其摘要。不存在的设备成为被拒绝的行。

使用 `--value V --type T`，类型可为 STRING、LONG、DOUBLE、BOOLEAN 或 JSON；也可使用 `--unset`，此时不提供值与类型。STRING 允许空值。可选限制包括 `--max-posts-per-second`（默认 10，最大 50）、`--failure-threshold` 和 `--ambiguity-threshold`（正整数，省略表示无限制），以及 `--settle-deadline`（默认 24h，范围 60s–168h，可使用时长或整数秒）。

默认令牌是 `tenant || 文件原始字节 || kind || 规范化意图` 的 SHA-256。重复的 `--device` 值使用有序 JSON 数组代替文件字节。完全相同的请求返回原变更；同一令牌用于不同请求会产生冲突。文件换行符会影响令牌。通过 `--token` 显式指定令牌，通过 `--name` 指定显示名称；名称默认等于令牌。使用默认令牌修改限制可能引发冲突：新请求应选择新令牌。

## 审核与启动 {#review-and-start}

审核创建操作返回的合同，再将服务器返回的完整 `contractDigest` 复制到 start 命令。客户端从不计算批准摘要。

```bash
dcctl bulk-change start <token> --contract-digest <reviewed-server-digest> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change status <token> --watch --tenant acme --email operator@example.com --password <password>
```

状态始终分别显示分发状态和证据状态。退出码：

| 退出码 | 含义 |
| --- | --- |
| 0 | DISPATCHED 且证据为 CLOSED |
| 1 | 未使用 watch 时仍在进行、未知状态、中断或错误 |
| 3 | PAUSED |
| 4 | ABORTED，即使证据仍为 OPEN |

DISPATCHED 且证据为 OPEN 表示仍在等待设备结论。Watch 持续轮询，直到证据关闭、变更暂停或中止，或你中断等待。使用 `-o json` 时，每次采样在独立的一行输出一个 JSON 对象。

## 查询行 {#inspect-lines}

客户端按输入序号读取所有页面。筛选由服务器执行。JSON 包含应用状态、结论、证据依据、修订号、错误和标识符擦除信息；CSV 会正确引用逗号、引号和换行符。

```bash
dcctl bulk-change lines <token> --posting POSTED --settlement AMBIGUOUS -o csv --tenant acme --email operator@example.com --password <password>
dcctl bulk-change lines <token> --devices-file devices.txt -o json --tenant acme --email operator@example.com --password <password>
```

可选的 `--devices-file` 按序号关联原始文件，添加 `inputToken`，明确标记为调用者提供的来源信息，与服务器的 `targetToken` 分开。请使用顺序一致的原始文件。重复令牌的规范化方式与创建时相同。已擦除的标识符保持隐藏。空结果可能表示行记录已过期而头记录仍保留；头记录不存在则返回错误。

## 暂停、恢复与中止 {#pause-resume-and-abort}

```bash
dcctl bulk-change pause <token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resume <token> --max-posts-per-second 2 --tenant acme --email operator@example.com --password <password>
dcctl bulk-change abort <token> --tenant acme --email operator@example.com --password <password>
```

暂停会停止新应用，直到恢复。恢复时省略的限制保持不变。Resume 接受速率和阈值选项，以及可选的 `--contract-digest`；省略摘要时，客户端请求服务器恢复预览并使用返回的摘要。本版本拒绝放宽合同，也拒绝在阈值仍被触及时恢复。

中止会永久取消尚未应用的行。已应用的行继续等待结论，证据可能仍为 OPEN。控制隔离锁超时会返回错误；请先查询状态，再决定下一步。

## 撤销批量变更 {#reverse}

撤销是一个新的批量变更，它逐行恢复每台设备在原变更之前的值。只有处于 DISPATCHED 或 ABORTED 的批量变更可以撤销，撤销本身不能再被撤销。

```bash
dcctl bulk-change reverse <token> --token <reversal-token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change reverse <token> --token <reversal-token> --create -o json --tenant acme --email operator@example.com --password <password>
```

不加 `--create` 时不写入任何内容：服务器报告将撤销多少行、多少行在原变更应用后又被修改、多少台设备已被擦除、多少行已由其他撤销恢复、多少行从未应用，以及各项摘要。加上 `--create` 后，撤销以 READY 状态创建。审核其合同，并像其他批量变更一样使用其 `contractDigest` 启动。以相同的 `--token` 和相同的请求重复 `--create` 会返回第一次创建的撤销。

`--token` 为必填项，用于标识该撤销；`--name` 默认与其相同。`--ordinals 1,4,9` 仅撤销原变更中的这些行；省略时撤销所有已应用的行。速率、阈值和 `--settle-deadline` 选项与创建时相同。

如果某行的值在原变更应用后又被修改，该行会以 CHANGED_SINCE 被拒绝，因此之后的修改不会被意外覆盖。若仍要应用，请添加 `--override-changed`（所有选中的行）或 `--override-ordinals 4,9`（仅这些行）。创建带覆盖的撤销需要在最近五分钟内验证过的第二因素；参见[第二因素](#second-factor)。

## 裁定不明确的行 {#resolve}

当截止时间之前没有任何设备证据作出判定时，该行变为 AMBIGUOUS。人员可以为其记录结论：

```bash
dcctl bulk-change resolve <token> --ordinals 3,7 --verdict confirmed --note "checked on site" --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resolve <token> --all-ambiguous --verdict failed --note "devices offline since the outage" --tenant acme --email operator@example.com --password <password>
```

使用 `--ordinals` 选择行，或使用 `--all-ambiguous` 选择所有不明确的行。`--verdict` 为 `confirmed` 或 `failed`，`--note`（1 到 1000 个字符）说明原因。非不明确的行保持不变。结论是人员的判断，而非设备证据：如果之后的上报与其矛盾，该行保留结论并设置证据标记，可在 `lines -o json` 中看到。裁定始终需要近期的第二因素。

## 第二因素 {#second-factor}

裁定以及创建带覆盖的撤销，都需要你本人登录并在最近五分钟内验证过第二因素。没有第二因素的账户会被拒绝；请先设置身份验证器应用。代理身份和服务身份始终被拒绝。

dcctl 在命令开始时登录，因此通过 `--otp` 提供的代码通常足够新。如果服务器仍要求更新的验证（例如登录时使用了恢复码，此处不算数），命令会重新登录一次，并在终端中请求新的身份验证器代码。没有终端时，命令会停止并提示 `--otp`：请使用新代码重新运行命令。只有在该拒绝之后才会重新发送请求，而该拒绝不会写入任何内容。

所有选项请参阅[命令参考](../reference/dcctl.md#dcctl-bulk-change)。
