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
- **退出状态不变：** 运行成功或已规划（`--dry-run`）时为 `0`，否则为 `1`。
- `--output` 的值如果不是 `text` 或 `json`，会在任何操作开始前被拒绝；无法解析的命令行（例如未知的标志）也一样。两者都不会写出事件流。

默认值 `--output text` 就是 `dcctl install` 一直以来打印的输出。

## 提示 {#prompts}

事件流是非交互的：**它从不读取 stdin**。

- `--yes` 回答安装会提出的唯一确认：本地 kind 集群不存在时是否创建它。
- 没有 `--yes` 时，这个问题无法被回答。运行会在此处、在创建集群之前被拒绝，并发出一个 `step.failed` 事件，其 `code` 为 `run.prompt-unavailable`，`remedy` 为 `install.rerun-with-yes`，`effect` 为 `none_observed`。请加上 `--yes` 重新运行，或使用 `--kube-context` 安装到现有集群，这种方式不会提出任何问题。
- 安装从不索要机密，`--yes` 也从不提供机密。

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

| `kind` | 载荷 |
| --- | --- |
| `run.started` | `target`（`mode` 为 `managed-kind` 或 `existing-context`，`context` 给出其名称；上下文名称只是标签，而不是身份）、`dry_run`，以及 `manifest`，即本次运行可能执行的阶段 |
| `step.started` | `message`（一个代码）、可选的 `params`，以及当阶段的等待受已知预算约束时的 `budget_ms` |
| `step.progress` | `metric_id`、`mode`（`indeterminate` 或 `measured`）、`current`、可选的 `total`、`unit`（`checks`、`resources`、`actions`、`deployments` 或 `replicas`）和 `source`。数值是快照，可能下降 |
| `step.finished` | `outcome`：`succeeded`、`unchanged`、`planned`（试运行）或 `not_applicable`；以及 `duration_ms` |
| `step.failed` | `code`、`retry`（`transient`、`permanent` 或 `unknown`）、可选的 `remedy`、`cancelled`、`effect`（`none_observed`、`may_have_changed` 或 `applied`）和 `duration_ms`。它从不携带错误的文本 |
| `preflight.observation` | 预检报告中的一行：一个 `section`；一个带有 `verdict`（`pass`、`info`、`warn`、`fail`、`unknown`、`not_needed` 或 `not_checked`）、`text` 和 `fix` 的 `check`；或者结尾的 `summary`，带有 `fails` 和 `warns` |
| `log.line` | `level`、`source` 和 `text`：安装自身输出的一行，已去除终端转义序列 |
| `prompt.requested` | `id`、`prompt`（`confirm`、`choice` 或 `secret`）和 `question`（一个代码） |
| `prompt.resolved` | 只有 `id` 和 `disposition`（`--yes` 时为 `auto_confirmed`，无人能回答时为 `unavailable`）。它从不携带答案 |
| `run.cancelling` | 运行被中断：`reason`、当前阶段 `active_step`，以及是否在等待某个工具（`waiting_for_tool`）和等待多久（`stop_budget_ms`） |
| `run.summary` | 结论：`outcome`（`succeeded`、`planned`、`failed` 或 `cancelled`）、`install_record`、`readiness`、`claim`、`failed_step`、`partial_state`（一种影响程度，同上）以及可选的 `next` 命令 |

## 读取事件流 {#reading-the-stream}

- **最后一行就是结论。** 每个事件流都以恰好一个 `run.summary` 结束。没有它就结束的事件流（例如进程被杀死）表示结果**未知**，绝不表示成功。
- **只有 `run.summary` 说明安装是否成功。** 进度计数已满、到达某个阶段或日志没有报错都不等于成功。
- **每个开始的阶段都恰好结束一次**，以 `step.finished` 或 `step.failed` 结束。重试是一次新的尝试。
- **中断会先被报告，然后等待其完成。** 中断一到达就会发出 `run.cancelling`；被中断阶段的 `step.failed`（`cancelled: true`）和总结只有在进行中的工作停止后才会发出。
- **`effect` 说明失败的阶段可能留下了什么。** 只有 `none_observed` 表示没有任何改动。重新运行同一条命令会让被中断或失败的安装留下的半完成状态收敛。
- **日志行只提供信息，从不做决定。** 日志行有上限；输出很多时，最旧的日志行可能在慢速读取方取走之前被丢弃。其他任何类型的事件都不会被丢弃。
- **在运行开始前就被拒绝的命令**（例如标志相互冲突）只写出一行：一个 `run.summary`，其 `outcome` 为 `failed`，`failed_step` 为 `install.inputs`，`partial_state` 为 `none_observed`。
- **宽松地读取。** 后续版本可能在 `dcctl.install.events/v1` 中增加事件类型和字段；请跳过不认识的内容。会破坏读取方的变更将使用新的模式名称。

## 机密 {#secrets}

事件流从不携带安装的选项、凭据或文件内容。安装持有的机密值，例如告警 webhook URL、SMTP 密码和备份凭据，也会从进入事件流的任何文本中移除并替换为 `[redacted]`，以防某个工具回显它们。提示的解决结果只说明它如何结束，从不包含答案。

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
