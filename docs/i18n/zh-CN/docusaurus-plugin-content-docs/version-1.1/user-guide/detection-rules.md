---
title: 检测规则
sidebar_position: 5
---

import GuideShot from '@site/src/components/GuideShot';

# 检测规则

检测规则监视某个设备配置文件的遥测数据，并在发生特定情况时采取行动：读数越过限值、设备长时间无声、数值上升过快。规则触发时，可以触发告警、向设备回发命令、调用 Webhook 或发布到连接器。

本章面向编写和维护规则的人员，涵盖规则列表、编写规则的三种方式（表单、画布和自然语言描述）、对照已记录的历史数据预览规则，以及告诉你已发布规则是否在触发的 **Rule Health**（规则健康状况）选项卡。规则类型和动作的详细说明，参见[事件处理与告警](/concepts/event-processing)。

:::note 配置文件发布之后规则才会运行
规则属于设备配置文件，你编辑的是配置文件的草稿。新增或修改的规则，只有在你从配置文件的 **Versions**（版本）选项卡发布后，才会作用于实时遥测，相关内容见“设备类型与配置文件”一章。
:::

## 规则列表 {#the-rule-list}

从 **Devices → Device Profiles**（设备配置文件）中打开设备配置文件，选择 **Detection Rules**（检测规则）选项卡。列表显示每条规则的名称、类型以及是否启用。**Edit**（编辑）会打开规则，垃圾桶图标会在你确认后删除规则。**New detection rule**（新建检测规则）用于新建规则。

<GuideShot id="rules-list" alt="Building Pulse Thermostat Profile 的 Detection Rules 选项卡，列出两条已启用的阈值规则 Early heat warning 和 Building Pulse over-temperature，每条都带有 Edit 和删除操作，另有 New detection rule 按钮。" />

被禁用的规则仍保留在配置文件中，但不会运行。需要暂时搁置规则而不想删除时，可以这样做。

## 选择编写规则的方式 {#choosing-how-to-author-a-rule}

**New detection rule** 会打开一个抽屉，顶部有三种模式：**Form**（表单）、**Canvas**（画布）和 **Describe**（描述）。三者生成的是同一种规则，并由同一个编译器检查。编辑已有规则时只有 **Form** 和 **Canvas**；**Describe** 仅用于新规则。在画布上构建的规则，编辑时会在画布上打开。

## 在表单中编写 {#authoring-in-the-form}

表单是编写单条规则最快的方式。你需要填写名称、可选的描述、令牌（规则的永久 ID）、**Rule type**（规则类型）和 **Severity**（严重程度），然后填写该规则类型所需的字段。

<GuideShot id="rule-form-new" alt="表单模式下的 New detection rule 抽屉。顶部的 Start from a preset 框提供 Device silent、Above threshold for a duration 和 Low battery。下方是 Name、Description 和 Token 输入框，设为 Threshold 的 Rule type，设为 none 的 Severity，一个 Enabled 复选框，一个 Scope to an entity group 复选框，以及 Condition 部分。" />

表单各部分，自上而下：

- **Start from a preset**（从预设开始）会用常见告警填充表单，详见下一节。
- **Rule type** 决定规则检测什么：Threshold（阈值）、Duration（持续时间）、Absence（缺失）、Connectivity（连接状态）、Windowed aggregate（窗口聚合）、Rate of change（变化率）、Repeating（重复）或 Area correlation（区域关联）。每种类型会显示各自的字段。各[条件类型](/concepts/event-processing#condition-types)的说明见相应章节。
- **Severity** 是可选项，除非规则要触发告警，此时告警会带上这一严重程度。
- **Enabled**（已启用）用于开启或关闭规则而无需删除。
- **Scope to an entity group**（限定到实体分组）会把规则限制在某个动态分组的一个已发布版本的成员上。缺失规则和关联规则无法用这种方式限定范围。
- **Condition**（条件）是规则触发所依据的比较：把指标与某个值或设备属性做结构化比较、一个高级 CEL 表达式，或者对某些类型而言的 **Match every event**（匹配每个事件）。缺失规则和连接状态规则没有条件。
- **Actions**（动作）是规则触发时发生的事情：**Raise alarm**（触发告警）、**Send command**（发送命令）、**Call a webhook**（调用 Webhook）或 **Publish to a connector**（发布到连接器）。没有动作的规则不会触发告警。区域关联规则不能带有动作。**Send command** 动作还可以填写可选的 **Expires after**（例如 `15m` 或 `2h`，范围为一分钟到七天），用于限制命令等待设备的时间，从规则触发的那一刻起计算；留空则沿用平台默认的七天。超出该范围或不是时长的值，会在保存规则时被拒绝。

输入时，表单会向编译器询问规则是否有效，并把答案显示在字段下方，例如 **The rule compiles. ✓**（规则编译通过）或编译器给出的错误。再往下是 **Preview against history**（对照历史预览）面板（参见[对照历史预览](#previewing-against-history)），对于会触发告警的规则，还有 **Notifications**（通知）检查。**Create detection rule**（创建检测规则）会把规则保存到配置文件的草稿中。

### 从预设开始 {#start-from-a-preset}

预设行提供大多数设备群都需要的三种告警：

- **Device silent**（设备静默）：当配置文件下的某台设备停止上报 15 分钟时触发告警。
- **Above threshold for a duration**（持续高于阈值）：当某个指标持续高于限值 10 分钟时触发告警。指标和限值由你选择。
- **Low battery**（电量低）：当电池电量持续低于 20% 达 10 分钟时触发告警。

选择预设会填充表单。在你创建规则之前不会保存任何内容，每个字段仍可编辑。对于监视某个指标的两个预设，表单会在你创建规则前要求你确认该指标。每个预设所填的确切值，见[预设表](/concepts/event-processing#start-from-a-preset)。

### 会有人收到通知吗？ {#will-anyone-be-notified}

当规则触发带严重程度的告警时，**Notifications** 面板会检查是否有已启用的通知策略，通过可用的渠道发送该严重程度的告警，并回答 *Covered*（已覆盖）或 *Not covered*（未覆盖）。

策略按严重程度而不是按规则来路由告警。如果没有任何覆盖，**Create a policy for** *严重程度*（为该严重程度创建策略）可以当场添加一条：选择渠道，对于邮件还要填写收件人。该策略随后会发送租户内这一严重程度的每条告警，不论来自哪条规则。你需要先有一个投递渠道，参见[配置通知渠道](/guides/notification-channels)。

只有在你有权管理通知策略时才会出现该按钮。这项检查仅供参考，绝不会阻止你保存规则。

## 在画布上编写 {#authoring-on-the-canvas}

画布把规则呈现为一个小型流程：一个 **Source**（来源，即配置文件的遥测）连接到条件，条件再连接到一个或多个动作。它适合有分支或需要复用计算值的规则。

<GuideShot id="rule-canvas" alt="画布模式下规则 bp-early-heat 的 Edit detection rule 抽屉。调色板提供 Source、Threshold、Duration、Absence、Connectivity、Windowed aggregate、Rate of change、Repeating、Area correlation、Compute、Branch 和 Action。画布显示配置文件的 Source 节点连接到 Threshold 节点（Early heat warning，temperature > 29），再连接到触发告警 bp-early-heat 的 Action 节点。Save changes 旁边有一个 Compiles · cost 5 徽章。" />

画布的使用方法：

- 从 **Add:** 一行的调色板中添加节点。每种规则类型都有对应的节点，另外还有 **Compute**（命名一个派生值）、**Branch**（根据条件把一次触发送往不同的动作）和 **Action**。
- 把来源连接到条件，再把条件连接到动作。
- 选择节点，即可在右侧面板中编辑。
- 留意保存按钮旁的徽章。**Compiles**（编译通过）表示规则有效；成本数字是编译器对该规则求值开销的估计。

表单和画布编辑的是同一条规则。在画布上编写的守卫条件，在表单中打开规则时仍会显示并保留。如果已保存的规则含有画布无法显示的设置，画布会说明这一点并关闭保存，而不会丢弃这些设置。

## 用自然语言描述规则 {#describing-a-rule-in-plain-language}

**Describe** 让你输入想要的规则，例如 *Raise a warning when the lobby thermostat stays above 28 degrees for ten minutes*，然后选择 **Draft rule**（起草规则）。AI 模型会起草规则，编译器进行检查，草稿随后在表单中打开供你审阅和保存。在你保存之前不会保存任何内容。

<GuideShot id="rule-describe" alt="Describe 模式下的 New detection rule 抽屉。文本框中已输入描述，并已按下 Draft rule。下方的黄色提示写着：NL rule drafting is not enabled on this deployment。" />

Describe 需要实例的 AI 服务，并且租户必须被允许使用它。不可用时（如截图中的实例），抽屉会显示提示，你仍然可以使用表单或画布。如果草稿经过多次尝试仍无法编译，控制台会说明这一点，并显示 AI 的尝试内容，便于你改进描述。起草的工作方式以及由谁启用，参见[AI 辅助编写](/concepts/ai-authoring)。

## 对照历史预览 {#previewing-against-history}

发布规则之前，可以看看它本来会做什么。**Preview against history** 会让配置文件已记录的遥测数据重放通过该规则，并列出它本会触发和解除的告警。不会发布任何内容，也不会创建任何告警。

<GuideShot id="rule-replay-preview" alt="画布下方的 Preview against history 面板，已选中 Last 24h 时间窗口。它报告最近 24 小时有 157 次触发、扫描了 1620 个事件、耗时 75 ms，有一条提示说明最早保留事件之前的历史不可用，并列出恒温器 bp-therm-003 至 bp-therm-007 的 RAISE 和 RESOLVE 行及其时间。" />

1. 选择时间窗口：**Last 1h**、**Last 6h** 或 **Last 24h**。
2. 选择 **Run preview**（运行预览）。
3. 查看结果：触发了多少次、扫描了多少个事件，以及 **RAISE**（触发）和 **RESOLVE**（解除）行的列表，每行注明设备和时间。

在画布上，选择一次触发会追踪它经过各节点的路径，让你看到哪个条件匹配、哪个动作执行了。表单中有同样的面板，但没有追踪功能。只有规则编译通过后才能运行预览。

如果配置文件保存的历史没有回溯到窗口所需的时间，面板会说明这一点，并从它拥有的最早事件开始预览。预览是参考而非保证：它从冷启动开始重放，因此等待长时间静默的规则，预览结果会不够准确。

## 规则健康状况与实时数据流 {#rule-health-and-the-live-feed}

**Rule Health** 选项卡回答“我的规则在工作吗？”，针对的是配置文件的生效已发布版本。

<GuideShot id="rule-health" alt="Building Pulse Thermostat Profile 的 Rule Health 选项卡。表格列出规则 Building Pulse over-temperature，状态为 Active，没有最近触发时间，触发 0 次。下方是标有 live 的 Live detections 部分，内容为：Waiting for detections… firings appear here as rules trigger。" />

- 表格列出每条已发布的规则，包括它的 **Status**（状态）、**Last fired**（最近触发时间）和累计触发的 **Fires**（次数）。**Active**（运行中）表示规则正在运行。**Compile error**（编译错误）表示已发布的规则无法编译，并附有消息说明原因。
- **Live detections**（实时检测）会在触发发生时显示它们，每条都标明是触发还是解除。标题旁的圆点表示数据流是否已连接。

这里只显示已发布的规则。在截图中，表格里没有 *Early heat warning*，因为它只存在于配置文件的草稿中。

## 相关内容 {#related}

- [事件处理与告警](/concepts/event-processing)：规则类型、动作和告警生命周期
- [AI 辅助编写](/concepts/ai-authoring)
- [配置通知渠道](/guides/notification-channels)
- [出站连接器](/concepts/outbound-connectors)，用于 Webhook 和发布动作
