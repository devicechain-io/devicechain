---
sidebar_position: 3.7
title: 测试你的规则
---

# 测试你的规则 {#testing-your-rules}

没有测试过的检测规则，只是对设备行为的一种猜测。本指南说明如何编写规则测试：你描述一支设备车队会发送的事件，写明规则对此应有的反应，由 `dcctl test` 检查结果。规则由平台自己的检测引擎按事件时间求值，因此一条要等待七天静默的规则，只需不到一秒就能测完，而且它的任何动作都不会触及你的在线设备、告警或流量。

:::note 此功能处于预览阶段
场景文件和测试文件的格式带有版本 `devicechain.io/ruletest/v1alpha1`，在正式提升之前，可能在次要版本中发生不兼容的变更，并会在该版本的升级说明中注明。已安装的 `dcctl` 不认识的版本会被拒绝，不认识的键也会被拒绝：如果一个拼错的期望被忽略，测试就会错误地通过。
:::

## 测试检查什么 {#what-a-test-checks}

一个测试由两个文件构成。

- **场景**（`scenario.yaml`）说明发生了什么：有哪些设备，每个设备在何时上报什么，以及设备与平台之间出了什么问题。
- **测试文件**（`*.test.yaml`）说明应当随之发生什么：哪些告警被触发或清除、在何时，哪些从不触发，以及规则应当请求哪种副作用。

平台在场景生成的事件上对规则求值，并返回两样东西。**边沿**（edge）是规则推导出的状态转换，每个要么是触发，要么是解除。**意图**（intent）是这些边沿会引起的副作用：告警的触发或清除、带有键和参数的命令、渲染好正文的 webhook 或发布的消息。意图是规则所请求的内容，不会被执行：不会触发任何告警，不会有命令入队，也没有任何请求离开平台。

由于规则由平台运行，测试对规则含义的理解不可能与生产环境不同。`dcctl` 不保存任何检测逻辑的副本，它负责的是不适合放在服务器上的部分：根据种子生成事件，以及对照你的期望判断结果。

## 运行第一个测试 {#first-test}

创建一个包含两个文件的目录。场景描述一台锅炉：前半小时正常，随后过热，然后冷却。

```yaml title="tests/scenario.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler            # 被测规则所属设备配置文件的令牌
start: 2026-03-01T00:00:00Z
duration: 2h
seed: 1
devices:
  - token: boiler-1
    streams:
      - metric: temperature
        every: 10m
        steps:
          - { at: 0s, value: 20 }
          - { at: 30m, value: 95 }
          - { at: 1h30m, value: 20 }
```

测试文件写明：已发布的规则应在锅炉过热时触发告警，在锅炉冷却后清除告警，并请求启动风扇。

```yaml title="tests/boiler.test.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: raised when the boiler goes over 80
    raised: { device: boiler-1, after: 25m, within: 10m }
  - name: cleared once it cools
    cleared: { device: boiler-1, after: 85m, within: 10m }
  - name: the fan is started
    intent: { kind: sendCommand, command: set-fan, payload: { speed: 3 } }
```

以租户成员的身份运行。该身份需要 `device:read` 权限。

```bash
dcctl test ./tests --tenant acme --email you@example.com --password '<password>' --server localhost
```

报告会列出每条期望，失败时还会说明实际发现了什么：

```text
evaluated tests/scenario.yaml: 13 events, 2 edges, 2 intents in 41ms
PASS  tests/boiler.test.yaml: raised when the boiler goes over 80
PASS  tests/boiler.test.yaml: cleared once it cools
FAIL  tests/boiler.test.yaml: the fan is started
        expected sendCommand, command "set-fan", payload {"speed":3} from any rule on any device to be emitted at least once, anywhere in the scenario; 0 matched; the intents emitted were: raiseAlarm "overheat" (rule hot, device boiler-1); clearAlarm "overheat" (rule hot, device boiler-1)
2 passed, 1 failed, 0 errored
```

每个期望都会报告为通过、失败或出错，退出码说明这次运行属于哪种结果：

| 退出码 | 含义 |
| --- | --- |
| `0` | 所有期望都成立。 |
| `1` | 至少有一个期望经过判定且不成立，并且没有出错。 |
| `2` | 有内容无法判定：文件未能加载、平台拒绝了评估、登录或网络失败，或某个期望无法得出结论（参见[无法判定的结果](#cannot-judge)）。 |

没有任何 `*.test.yaml` 文件的目录会报错，因为什么都不检查的运行不应当算作通过。若要为 CI 系统保留结果，请添加 `--junit report.xml`；即使文件完全无法加载，报告中也会有一个出错的用例说明原因。为了不让密码留在 shell 历史中，请设置 `DCCTL_PASSWORD`，而不是传入 `--password`。

## 测试哪些规则 {#which-rules}

默认情况下，场景对配置文件的**已发布**规则求值，即当前在线的规则。若要在发布前测试规则，请改为对**草稿**求值。将规则定义（规则存储时的 JSON）或画布图作为文件放在场景旁边并指明：

```yaml title="tests/scenario.yaml"
profile: boiler
ruleSet: DRAFT
draft:
  ruleDefinition: hot.rule.json      # 或：graph: hot.canvas.json
```

草稿是在场景生成的事件上求值的，所以配置文件无需已发布。一张画布图可以包含多条检测规则，每条都会求值，并在结果中以其条件节点的 id 命名。`ruleDefinition` 草稿的名称是 `draft`。

测试在期望中用 `rule:` 指明规则。对已发布的规则，它就是规则的令牌。

## 编写场景 {#scenario}

| 字段 | 含义 |
| --- | --- |
| `apiVersion`、`kind` | `devicechain.io/ruletest/v1alpha1` 和 `Scenario`。必填。 |
| `profile` | 被求值规则所属的设备配置文件。必填。 |
| `ruleSet` | `PUBLISHED`（默认）或 `DRAFT`。 |
| `draft` | 用于 `DRAFT`：`ruleDefinition` 与 `graph` 二选一，为相对于场景的路径。 |
| `start` | 场景开始的时刻，RFC 3339 时间。必填：默认取“现在”会让每次运行都不同。 |
| `duration` | 持续时间，例如 `90m` 或 `7d`。必填。最后一个事件之后，引擎时钟会推进到 `start + duration`，这决定了等待静默的规则。 |
| `seed` | 为所有随机选择提供种子。相同的种子总是生成相同的事件。 |
| `devices` | 模拟设备：一个 `token` 及其 `streams`。 |
| `faults` | 事件到达平台途中出现的问题。 |

时长支持 Go 的单位，另加 `d`（24 小时）和 `w`（七天）：`90m`、`36h`、`7d`、`1d12h`。单独的数字会被拒绝。

### 数据流 {#streams}

数据流（stream）是某台设备上一个指标的序列。它指明指标、`every` 间隔以及且仅一种行为。`from` 与 `until` 是相对于 `start` 的偏移，用来限定数据流的范围：在场景结束前停止的数据流，就是让设备“沉默”的方式。

| 行为 | 示例 | 产生 |
| --- | --- | --- |
| `constant` | `constant: 21.5` | 每次采样都是同一个值。 |
| `randomWalk` | `randomWalk: { start: 20, step: 0.5, min: 15, max: 30 }` | 每次采样在 `[-step, +step]` 内均匀变化，并保持在 `[min, max]` 之内的随机游走。 |
| `steps` | `steps: [{ at: 0s, value: 20 }, { at: 1h, value: 95 }]` | 每个阶跃的值保持到下一个阶跃。第一个阶跃必须位于 `0s`。 |
| `playback` | `playback: { file: recorded.csv }` | 来自录制文件的读数。 |

回放文件是带表头行的 CSV，或 NDJSON（`.ndjson` 或 `.jsonl`）。每一行有一个 `at`（RFC 3339 时间，或相对于 `start` 的偏移，如 `90m`）和一个 `value`，也可以指明 `metric`；未指明的行使用数据流的指标。文件路径相对于场景，不能离开场景所在目录。

```csv title="tests/recorded.csv"
at,value
0m,21.4
10m,21.9
2026-03-01T00:20:00Z,34.0
```

同一设备在同一时刻的读数作为一个事件送达，这正是在一次采样中上报多个指标的设备所发送的形式。

### 故障 {#faults}

故障改变的是事件如何到达平台，而不是设备测得了什么。未指明 `device` 的故障适用于所有设备。

| 故障 | 示例 | 效果 |
| --- | --- | --- |
| `gap` | `gap: { device: boiler-1, from: 1h, for: 30m }` | 窗口内的采样永久丢失。`metric` 可将其限定到单个指标。 |
| `offline` | `offline: { device: boiler-1, from: 1h, for: 30m }` | 设备断线期间继续采样，重新连接后上传：采样保留各自的时间，并在窗口结束时一起到达，落在之后发生的读数后面。 |
| `duplicate` | `duplicate: { probability: 0.1 }` | 每个事件以该概率被再投递一次。 |
| `reorder` | `reorder: { probability: 0.2, maxDelay: 10m }` | 每个事件以该概率被延迟至多 `maxDelay` 的随机时间，因此落在后来的事件之后到达。 |

`gap` 与 `offline` 的区别很重要：gap 测试数据缺失时规则怎么做，offline 窗口测试数据迟到时规则怎么做。

事件按送达顺序交给引擎。引擎对迟到事件的处理与生产环境一致：它让自己的时钟落后于最新事件，落后的幅度与实例实时检测所用的延迟容许量相同，因此在该容许量内乱序到达的读数会被使用，更晚到达的读数会被拒绝。读数被拒绝时，结果会说明这一点并被标记为部分结果（参见[无法判定的结果](#cannot-judge)）。

## 编写测试 {#test-file}

测试文件包含 `apiVersion` 和 `kind: RuleTest`，可选的 `name`，可选的 `scenario`（相对于该文件的路径；默认是同一目录下的 `scenario.yaml`），以及 `expect` 块列表。每个块指明且仅指明下列之一：

| 块 | 成立条件 |
| --- | --- |
| `raised` | 窗口内至少有一个匹配的触发边沿。 |
| `cleared` | 窗口内至少有一个匹配的解除边沿。 |
| `neverRaised` | 窗口内没有匹配的触发边沿。 |
| `intent` | 窗口内至少请求了一个匹配的副作用。 |
| `neverIntent` | 窗口内没有请求匹配的副作用。 |

每个块都可带可选的 `name`，用于在报告和 JUnit 中标识它，以及可选的 `allowDegraded`（参见[无法判定的结果](#cannot-judge)）。下列字段都用来缩小匹配范围，省略的字段匹配任何值：

- `rule` 和 `device` 选定规则和设备。`device` 必须是场景中定义的设备，否则测试文件在读取时就会被拒绝。
- `after` 和 `within` 以相对于场景 `start` 的偏移给出窗口：从 `after` 到 `after + within`，两端都包含。省略 `within` 则一直到场景结束。
- `count`（用于 `raised`、`cleared`、`intent`）要求恰好有该数量的匹配，而不是至少一个。

`intent` 块还可以匹配副作用的内容：

| 字段 | 匹配 |
| --- | --- |
| `kind` | `raiseAlarm`、`clearAlarm`、`sendCommand`、`httpCall` 或 `publish`。 |
| `edge` | `raised` 或 `resolved`：引起它的边沿。 |
| `command`、`payload` | 命令的键及其参数对象。payload 按 JSON 比较，因此键的顺序和空白无关紧要，没有参数的命令等同于 `{}`。 |
| `alarmKey` | 告警被触发或清除时所用的键。 |
| `connector`、`url`、`body`、`bodyContains` | 消息发布所经的连接器、webhook 调用的端点，以及规则模板渲染出的正文（完全匹配或按片段匹配）。 |

命令、webhook 和发布的消息只在触发边沿上请求；告警在触发它的规则的解除边沿上清除。

## 无法判定的结果 {#cannot-judge}

有些问题评估无法回答，`dcctl test` 会把这类期望报告为出错，而绝不会报告为通过：

- **未被评估的规则。** 结果会列出平台运行过的规则。期望中指定了其他任何规则（无论是拼错的还是被跳过的，参见[不模拟的内容](#limits)），都会报错，并说明实际运行了哪些规则。否则，`neverRaised: { rule: hott }` 将永远通过。
- **基于部分结果的"不存在"或计数。** 当有读数到达太晚而无法使用、规则谓词在某些事件上失败、某个限制截断了边沿或意图、平台因规则存储的动作无法执行而跳过了该动作，或者某条规则读取了设备属性时，结果就是部分结果。部分结果仍然可以证明某件事发生过，因此不带 `count` 的 `raised`、`cleared` 和 `intent` 照常判定。它无法证明某件事*没有*发生，也无法证明发生了多少次，因此 `neverRaised`、`neverIntent` 以及任何带 `count` 的块都会报告为无法得出结论。请修正场景，或在该块上设置 `allowDegraded: true` 以仍然进行判定。

什么都没有评估的运行会被直接拒绝：没有事件、没有可评估的规则、平台上没有空闲的评估槽位，或评估超时。平台繁忙时，`dcctl test` 会等待并重试几次，然后才报告错误。

## 会失败的测试 {#tests-that-can-fail}

没有见过失败的测试毫无价值。测试通过后，请有意在规则里植入一个缺陷，确认指明它的期望会变红：改动阈值、拼错命令键、反转比较。如果缺陷改变了规则而没有任何期望察觉，说明该测试检查的并不是你以为的内容。平台自身使用的植入缺陷测试集就采用这种方法：一个场景、六条具名期望、五个刻意植入的缺陷，每个缺陷必须让指明它的期望失败，而不影响其他期望。

窗口是测试最常见的过于宽松之处。`raised: { device: boiler-1 }` 只要锅炉在任何时候触发过就会通过，包括错误的时刻。只要时间重要，就应当为窗口给出起点和终点。

## 在 CI 中运行 {#ci}

`dcctl test` 需要一个身份和一个运行中的实例，并写出 JUnit XML，所有 CI 系统都能读取。

```bash
dcctl test ./tests \
  --tenant acme --email ci@example.com \
  --server platform.example.com --tls \
  --junit report.xml
```

从 CI 系统的密钥存储中把密码设置到 `DCCTL_PASSWORD` 环境变量里。只给该身份所需的最小权限：租户上的 `device:read`。如果账户启用了双因素认证，请参阅[命令参考](../reference/dcctl.md#dcctl-test)中的 `--otp`。

## 它不模拟什么 {#limits}

评估对规则是精确的，对所略去的内容是坦率的。

- **设备属性。** 读取设备属性的规则在没有属性的情况下评估，因为这里的设备没有属性；结果会被标记为部分结果，这样针对它的 `neverRaised` 会被报告为无法得出结论，而不是悄悄通过。
- **按分组限定范围的规则。** 限定到实体分组的已发布规则会被跳过，并作为警告列出，因为生成的事件不带分组成员关系。草稿不能限定到分组。
- **关联规则。** 在区域或其他锚点下关联设备的已发布规则会被跳过，草稿中的此类规则会被拒绝，因为生成的事件不带锚点成员关系。
- **地理围栏规则。** 测试地理围栏包含关系的规则会被拒绝，因为事件不带位置。
- **没有事件携带的指标。** 如果规则监视的指标在场景的任何事件中都没有上报，它只能保持静默；结果中会带有一条指出该指标的警告。
- **命令和连接器。** 意图是规则所请求的内容。命令是否存在于设备上、连接器是否存在于租户中，是在请求发出的地方检查的，而不是在这里。
- **限制。** 一次评估最多接受 250,000 个事件和 256 条规则，请求正文受实例的 GraphQL 正文大小限制；超过任一限制的场景会被拒绝，并说明原因。它与重放预览共用每租户的并发限制，并且平台在所有租户之间同时只运行少量评估，因此评估不会挤占实时检测。

## 确定性 {#determinism}

相同的场景文件总是产生相同的事件，相同的事件总是产生相同的结果。每个随机选择都来自一个生成器，它由场景的 `seed` 和被生成对象的标识共同提供种子，因此向场景中添加一台设备不会改变另一台设备的上报。评估不读取任何时钟：命令的过期时间从引起它的边沿的时刻起算，而不是从你运行测试的时间起算。

正是这一性质使失败值得处理。失败的测试会以同样的方式再次失败，直到规则或期望被修正，而修正的证明就是同一次运行通过。
