---
sidebar_position: 4
title: 安装事件流
---

# 安装事件流 {#install-event-stream}

`dcctl install --output json`（或 `-o json`）完全按照原有方式执行安装，但以事件流而不是文本来报告：在 stdout 上每行一个 JSON 对象，按发生顺序输出。它面向自动化场景，例如 CI 作业、置备工具或包装脚本，这些场景需要知道安装到达了哪个阶段、每个阶段做了什么以及运行如何结束，而无需解析面向人的输出。

该事件流的模式是 `dcctl.install.events/v1`，每一行都带有它。

## 运行方式 {#running-it}

```bash
dcctl install local --kube-context prod --yes -o json > install.ndjson
echo "exit status: $?"
```

- **stdout 只包含事件。** 每个字节都属于某一行，每一行都是一个完整的 JSON 对象。没有横幅、没有颜色，也没有终端转义序列。安装自身的进度文本以 `log.line` 事件的形式出现在事件流中。
- **stderr 保留人类可读的原因。** 运行失败时，文本输出会打印的那句话照常打印在 stderr 上。运行开始前打印的提示信息也会输出到那里。
- **退出状态就是总结的结论：** 运行成功或已规划（`--dry-run`）时为 `0`，否则为 `1`，与安装所运行的工具以什么状态退出无关。在 dcctl 内部意外失败的运行同样以一个失败的总结结束事件流，并以 `1` 退出。
- `--output` 的值如果不是 `text` 或 `json`，会在任何操作开始前被拒绝；无法解析的命令行（例如未知的标志）也一样。两者都不会写出事件流。

默认值 `--output text` 就是 `dcctl install` 一直以来打印的输出，退出方式也相同：`0` 或 `1`。

`--help` 是帮助，而不是一次运行：与 `--output json` 一起使用时，帮助打印在 stderr 上，stdout 保持为空。

## 提示 {#prompts}

事件流是非交互的：**它从不读取 stdin**。

- `--yes` 回答安装会提出的唯一确认：本地 kind 集群不存在时是否创建它。它不经询问就解决这个确认，因此带 `--yes` 的运行不会为它发出 `prompt.requested` 或 `prompt.resolved` 事件。
- 没有 `--yes` 时，这个问题无法被回答。运行会在此处、在创建集群之前被拒绝，并发出一个 `step.failed` 事件，其 `code` 为 `run.prompt-unavailable`，`remedy` 为 `install.rerun-with-yes`，`effect` 为 `none_observed`。请加上 `--yes` 重新运行，或使用 `--kube-context` 安装到现有集群，这种方式不会提出任何问题。
- 安装从不索要机密，`--yes` 也从不提供机密。
- 安装所运行的任何东西也拿不到 stdin。kubeconfig 的 exec 凭据插件以非交互方式运行；要求终端的插件（`interactiveMode: Always`）会失败，而不会提问。

## 信封 {#envelope}

每一行都是包裹一个载荷的信封：

| 字段 | 含义 |
| --- | --- |
| `schema` | `dcctl.install.events/v1` |
| `run_id` | 每次调用随机生成；同一次运行的每一行都相同 |
| `seq` | 从 1 开始，每行加一，没有间隙 |
| `time` | 事件发生的时间，UTC，RFC 3339 |
| `elapsed_ms` | 自运行开始以来的毫秒数，取自单调时钟 |
| `operation` | `install` |
| `kind` | 载荷的类型（见下文） |
| `step_id` | 事件所涉及的阶段（如有） |
| `parent_id` | 嵌套阶段的父阶段 |
| `attempt` | 阶段的尝试次数，从 1 开始（如有） |
| `payload` | 事件本身 |

安装的各个阶段依次为 `install.inputs`、`install.preflight`、`install.cluster`、`install.claim`、`install.inspect`、`install.credentials`、`install.prepare`、`install.write-intent`、`install.operator`、`install.prerequisites`、`install.database-login`、`install.record`、`install.rdb-ready` 和 `install.summary`。`run.started` 在其清单中列出它们。不适用于本次运行的阶段同样会开始和结束，结果为 `not_applicable`。

## 事件类型 {#event-kinds}

第二列列出该类型的载荷可能携带的全部字段。不适用于某个事件的字段会被省略。

| `kind` | 字段 | 含义 |
| --- | --- | --- |
| `run.started` | `target`, `dry_run`, `manifest` | `target` 说明运行的去向：其 `mode` 为 `managed-kind` 或 `existing-context`，其 `context` 给出名称（上下文名称只是标签，而不是身份）。`manifest` 列出本次运行可能执行的阶段 |
| `step.started` | `message`, `params`, `budget_ms` | `message` 是一个代码，可带可选的 `params`。当阶段的等待受已知预算约束时，会出现 `budget_ms` |
| `step.progress` | `metric_id`, `mode`, `current`, `total`, `unit`, `source` | `mode` 为 `indeterminate` 或 `measured`，`total` 可选，`unit` 为 `checks`、`resources`、`actions`、`deployments` 或 `replicas`。数值是快照，可能下降。数值会被合并：见[读取事件流](#reading-the-stream) |
| `step.finished` | `outcome`, `duration_ms` | `outcome` 为 `succeeded`、`unchanged`、`planned`（试运行）或 `not_applicable` |
| `step.failed` | `code`, `retry`, `remedy`, `remedy_params`, `cancelled`, `effect`, `duration_ms` | `retry` 为 `transient`、`permanent` 或 `unknown`。`remedy` 是可选的代码，可带可选的 `remedy_params`。`effect` 为 `none_observed`、`may_have_changed` 或 `applied`。它从不携带错误的文本 |
| `preflight.observation` | `observation`, `section`, `verdict`, `text`, `highlight`, `fix`, `fails`, `warns` | 预检报告中的一行，`observation` 说明是哪一种行。`section`：一组检查开始，由 `section` 代码命名。`check`：一项检查，带有其 `verdict`（`pass`、`info`、`warn`、`fail`、`unknown`、`not_needed` 或 `not_checked`）、`text`、可选的 `highlight`（检查特别指出的值，例如当前的 kube 上下文，显示在 `text` 之后），以及警告或失败时的 `fix`。`summary`：报告结尾的 `fails` 和 `warns` 计数 |
| `log.line` | `level`, `source`, `code`, `text` | 安装自身输出的一行，已去除终端转义序列。该行带有代码时会出现 `code` |
| `prompt.requested` | `id`, `prompt`, `question`, `params` | `prompt` 为 `confirm`、`choice` 或 `secret`。`question` 是一个代码，可带可选的 `params` |
| `prompt.resolved` | `id`, `disposition` | 提示如何结束，从不包含答案。安装唯一的提示在无人能回答时解决为 `unavailable`，在提问期间运行被中断时解决为 `cancelled` |
| `run.cancelling` | `reason`, `active_step`, `waiting_for_tool`, `stop_budget_ms` | 运行被中断：原因、当时所在的阶段，以及是否在等待某个工具停止、给了该工具多长时间 |
| `run.summary` | `outcome`, `install_record`, `readiness`, `claim`, `failed_step`, `partial_state`, `next`, `credentials`, `delivery_incomplete` | 结论。`outcome` 为 `succeeded`、`planned`、`failed` 或 `cancelled`。`partial_state` 是一种影响程度，同上。`next` 列出接下来要运行的命令。`credentials` 给出保存安装所创建凭据的 Kubernetes Secret（`namespace`、`name` 和 `key`），从不包含其值。某个事件无法写出时 `delivery_incomplete` 为 `true`，这样的运行绝不会报告成功 |

## 读取事件流 {#reading-the-stream}

- **最后一行就是结论。** 每个事件流都以恰好一个 `run.summary` 结束。没有它就结束的事件流（例如进程被杀死）表示结果**未知**，绝不表示成功。
- **只有 `run.summary` 说明安装是否成功。** 进度计数已满、到达某个阶段或日志没有报错都不等于成功。
- **每个开始的阶段都恰好结束一次**，以 `step.finished` 或 `step.failed` 结束。重试是一次新的尝试。
- **中断会先被报告，然后在预算内等待其完成。** 中断一到达就会发出 `run.cancelling`。被中断阶段的 `step.failed`（`cancelled: true`）和总结会在进行中的工作停止后发出，只有一个例外：被中断的 OpenTofu 命令在其优雅停止预算及其后的一段余量内仍未被观察到结束。此时 dcctl 不再等待它，该阶段以 `code` `run.tool-abandoned` 和 `effect` `may_have_changed` 失败，随后发出总结，而该命令或它启动的提供程序插件可能仍在更改集群，即使 dcctl 已经退出。在这样的命令退出之前，dcctl 不会在同一进程中开始另一次运行，但新的 `dcctl install` 看不到它：重新运行之前，请确认被中断运行的 OpenTofu 进程已经全部结束。
- **`effect` 说明失败的阶段可能留下了什么。** 只有 `none_observed` 表示没有任何改动。重新运行同一条命令会让被中断或失败的安装留下的半完成状态收敛。
- **日志行只提供信息，从不做决定。** 日志行有上限；输出很多时，最旧的日志行可能在慢速读取方取走之前被丢弃。
- **进度会被合并。** 当某个阶段报告进度的速度快于事件流被写出或读取的速度时，同一指标的较新值会替换尚未写出的值，因此中间值可能被跳过。阶段结束前报告的最后一个值总会送达，并且早于它的 `step.finished` 或 `step.failed`。
- **其他任何类型的事件都不会被丢弃。**
- **在运行开始前就被拒绝的命令**（例如标志相互冲突）只写出一行：一个 `run.summary`，其 `outcome` 为 `failed`，`failed_step` 为 `install.inputs`，`partial_state` 为 `none_observed`。
- **宽松地读取。** 后续版本可能在 `dcctl.install.events/v1` 中增加事件类型和字段；请跳过不认识的内容。会破坏读取方的变更将使用新的模式名称。

## 机密 {#secrets}

事件流从不携带安装的选项、凭据或文件内容。提示的解决结果只说明它如何结束，从不包含答案。

作为第二道防线，为防某个工具回显它们，以下值会从 dcctl 打印的任何文本中移除并替换为 `[redacted]`：

- 安装持有的机密值，例如告警 webhook URL、SMTP 密码和备份凭据；
- kubeconfig 中的凭据：令牌、密码、客户端密钥、服务器或代理 URL 中的密码（原样形式和 URL 转义形式），exec 插件环境变量和认证提供程序设置中的值，以及 exec 插件参数中包含凭据的部分（如 `--client-secret` 或 `--token` 这类标志的值，以及看起来像密钥的参数）。

使用 `--output json` 时，这涵盖每个事件、打印在 stderr 上的错误（包括附加在其后的工具诊断信息），以及安装运行期间到达 dcctl 的 stdout 或 stderr 的其他所有内容，包括 kubeconfig 的 exec 凭据插件的 stderr。该 stderr 按行处理，因此可能比插件写出时稍晚到达。

存在以下限制：

- 短于 4 字节的值无法在不连带移除普通文本的情况下移除，因此不会被替换。当这样的值是凭据（令牌、密码、客户端密钥，或名称表明其中含有凭据的 exec 插件或认证提供程序设置）时，事件流会在一条 `warning:` 日志行中说明发现了多少个。文本输出不会打印这条警告，`AWS_PROFILE=dev` 这类设置也绝不会被警告。
- 来自 kubeconfig 的凭据只在作为完整单词出现时被替换，绝不在更长的单词内部被替换，也绝不会改变运行的行为。设置 `AWS_PROFILE=prod` 时，`--profile prod` 显示为 `--profile [redacted]`，而 `production` 保持不变；安装从不使用的、密码为 `install` 的用户也不会让安装停止。dcctl 自身持有的机密则在出现的任何位置都会被替换。
- 使用 `--output text` 时，只有 dcctl 打印的错误会被替换。其余内容就是 `dcctl install` 一直以来打印的输出，其中可能有意向你显示凭据（bootstrap 的访问摘要），而它运行的工具（包括 kind 和 kubeconfig 的 exec 凭据插件）照常直接写入终端。

dcctl 无法读取、或无法为某个上下文构建客户端的 kubeconfig，只按问题类型、文件和上下文名称报告，绝不附带 Kubernetes 客户端给出的原因，因为该原因可能引用文件中的凭据和 URL。`--output text` 也是如此。

## 示例 {#example}

一次没有 kind 集群、也没有 `--yes` 的安装，在确认处被拒绝（清单已缩短）：

```json
{"schema":"dcctl.install.events/v1","run_id":"…","seq":1,"time":"2026-11-26T12:00:00Z","elapsed_ms":0,"operation":"install","kind":"run.started","payload":{"target":{"mode":"managed-kind","context":"kind-devicechain"},"dry_run":false,"manifest":[{"id":"install.inputs"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":6,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.started","step_id":"install.cluster","attempt":1,"payload":{"message":"install.cluster"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":7,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.requested","step_id":"install.cluster","payload":{"id":"prompt-1","prompt":"confirm","question":"install.create-kind-cluster","params":[{"name":"cluster","value":"devicechain"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":8,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.resolved","step_id":"install.cluster","payload":{"id":"prompt-1","disposition":"unavailable"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":9,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.failed","step_id":"install.cluster","attempt":1,"payload":{"code":"run.prompt-unavailable","retry":"permanent","remedy":"install.rerun-with-yes","cancelled":false,"effect":"none_observed","duration_ms":0}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":10,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"run.summary","payload":{"outcome":"failed","install_record":"not_read","readiness":"not_checked","claim":"not_claimed","failed_step":"install.cluster","partial_state":"none_observed"}}
```

实时跟踪一次运行，并在得到结论时停止：

```bash
dcctl install local --yes -o json | jq -c 'select(.kind == "step.started" or .kind == "run.summary")'
```

`dcctl install` 的全部标志见[命令参考](./dcctl.md#dcctl-install)。
