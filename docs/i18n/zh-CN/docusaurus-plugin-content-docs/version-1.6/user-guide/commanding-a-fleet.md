---
title: 向设备群下发命令
sidebar_position: 7
---

import GuideShot from '@site/src/components/GuideShot';

# 向设备群下发命令

**命令批次**会把一条命令作为一次带记录的操作发送给许多设备：为一栋楼里的每台恒温器设置新的设定点，或重启一组网关。批次会记录尝试了什么、哪些设备接受了命令、哪些被拒绝以及原因。你可以跟踪它的命令、终止它，并对未成功的设备重试。

本章面向批量向设备下发命令的运维人员。它假定你已了解单条命令的工作方式，参见[发送命令](/guides/sending-commands)。批次背后的完整规则，参见指南中的[向设备群下发命令](/guides/commanding-a-fleet)。

## 批次列表 {#the-batch-list}

在侧边栏中选择 **Devices → Command Batches**（命令批次）。列表按从新到旧显示所有批次。

<GuideShot id="batches-list" alt="Command Batches 页面，带有 Command 和 Group token 筛选器、Any target 筛选器和 New batch 按钮。列出三个设备列表批次：night-setback，0 of 2 accepted，2 refused，Cancelled；set-setpoint，2 of 2，0 refused，Cancelled；identify，2 of 2，0 refused。" />

各列含义：

- **Fired**（发出时间）：批次发送的时间。
- **Command**（命令）：它发送的命令键。
- **Target**（目标）：如果指定了设备，则为 **Device list**（设备列表）；如果解析的是设备分组，则为 **Group**（分组）。
- **Accepted / resolved**（已接受 / 已解析）：目标解析出的设备中，有多少台接受了命令。
- **Refused**（已拒绝）：有多少台设备被拒绝。
- **Cancelled**（已取消）：批次被终止时显示的徽章。

可以按命令键、分组令牌或目标类型筛选。选择某一行即可打开该批次。

## 创建批次 {#creating-a-batch}

**New batch**（新建批次）会打开一个抽屉。

<GuideShot id="batches-new" alt="New batch 抽屉。Target 设为 Device list。下方依次是 Devices 选择器、Paste device tokens 框、显示 0 devices will be targeted 的计数、设为“No device — write the command key and payload by hand”的 Type the command against 选择器、Command key 输入框和 Payload 输入框。" />

1. **Target。** 选择 **Device list** 以指定设备，或选择 **Group** 向某个设备分组发送。
   - 使用设备列表时，可以在 **Devices**（设备）中按名称选取设备，或在 **Paste device tokens**（粘贴设备令牌）中粘贴令牌，每行一个或用逗号分隔。粘贴是指定大型设备群的方式，一个批次最多允许 10,000 台设备。顺序很重要：如果只能接纳批次的一部分，设备会按你给出的顺序被接纳。同一台设备被指定两次，只会收到一次命令。
   - 使用分组时，选择分组。对于动态分组，可以固定一个 **Group version**（分组版本），也可以保持在生效的已发布版本上。分组会在批次发出的时刻被解析为其设备。
2. **Command。** 使用设备列表时，表单跟随你指定的第一台设备，提供其配置文件发布的命令，并带有参数的类型化表单。使用 **Type the command against**（针对哪台设备输入命令）可以选择另一台设备，或清除它使其显示 *No device — write the command key and payload by hand*（不选设备，手动填写命令键和载荷），从而自己填写 **Command key**（命令键）和 **Payload**（载荷）。使用分组目标时，选择器一开始是空的。批次发出时，每台目标设备都会按其自身的配置文件检查，因此使用其他配置文件的设备仍可能被拒绝。
3. **If some devices cannot receive it**（如果部分设备无法接收）。没有默认值，你必须选择：
   - 发送给能够接收的设备，并把其余设备记录为拒绝；或
   - 拒绝整个批次，即除非每台设备都能接收该命令，否则不发送也不记录任何内容。
4. 选择 **Fire batch**（发出批次）。

如果批次发出，控制台会打开它的详情页。如果平台拒绝整个批次，抽屉会解释原因，并确认没有创建任何内容，这样你可以修正请求后再次发出。

## 跟踪批次 {#following-a-batch}

批次的详情页分为三部分：**Summary**（摘要）、**Refusals**（拒绝）以及该批次创建的 **Commands**（命令）。名称下方的徽章显示目标类型，以及是否允许部分扇出（**Partial fan-out allowed**）或不允许（**All or nothing**）。

<GuideShot id="batch-detail-completed" alt="identify 批次 batch-identify-rooftop 的详情页，带有 Device list 和 All or nothing 徽章以及 Retry… 和 Cancel batch 按钮。Summary 显示 Fired、Resolved 2、Accepted 2、Refused 0，一行 resolved = accepted + refused — 2 = 2 + 0，0 of 2 waiting for a maintenance window when this batch fired，以及载荷。Refusals 部分写着：Every device the target resolved to was accepted。Commands 部分从下方开始。" />

**Summary** 记录批次发出那一刻的情况：目标解析出多少台设备、多少台接受、多少台被拒绝。这些数字之后不会变化，并且总是满足：已解析 = 已接受 + 已拒绝。摘要还会说明有多少条已接受的命令在等待维护窗口（参见下方的[维护窗口](#maintenance-windows)）。

若要了解命令**现在**的位置，请查看 **Commands**。它按设备被接纳的顺序列出每台设备的命令，包括其状态、结果、排队时间和返回结果。可以按状态筛选，并使用 **Export CSV**（导出 CSV）保存符合筛选条件的所有命令，最多 10,000 行。

## 拒绝 {#refusals}

当部分设备无法接收命令时，**Refusals** 部分会按原因对它们分组。

<GuideShot id="batch-detail-refusals" alt="night-setback 批次的详情页，带有 Device list、Partial fan-out allowed 和 Cancelled — 0 stopped 徽章以及 Retry… 按钮。Summary 显示 Resolved 2、Accepted 0、Refused 2。Refusals 部分写着 2 devices were refused and received nothing，并列出代码 COMMAND_NOT_IN_VOCABULARY，被拒绝的 2 台设备为 bp-therm-002 和 bp-therm-003，原因均为设备不接受命令 night-setback。" />

每一组显示拒绝代码、涉及的设备数量以及设备本身，并附有各自的原因。在这个例子中，恒温器的配置文件没有声明 `night-setback` 命令，因此两台设备都以 `COMMAND_NOT_IN_VOCABULARY` 被拒绝。记录中每个代码最多列出 100 台设备；被拒绝的设备更多时，控制台会说明显示了多少台以及确切总数。各设备代码的含义，参见[入队被拒绝时](/guides/sending-commands#when-an-enqueue-is-refused)。

## 取消批次 {#cancelling-a-batch}

**Cancel batch**（取消批次）会终止该批次，停止平台仍为该批次暂存的所有命令。它无法召回已经交给设备的命令：设备仍会执行它。

<GuideShot id="batch-cancel-confirm" alt="标题为 Call off “identify”? 的确认对话框。内容说明平台目前没有暂存该批次的任何命令，因此这可能不会停止任何内容，并且已经交给设备的命令无法召回。按钮为 Leave it running 和 Cancel batch。" />

确认之前，对话框会告诉你平台仍在暂存该批次的多少条命令，让你知道取消能停止多少。选择 **Cancel batch** 继续，或选择 **Leave it running**（让它继续运行）。之后控制台会报告结果：停止了多少条命令、多少条已到达设备、多少条已经完成。

被取消的批次会标注取消时间以及因此停止的命令数。

<GuideShot id="batch-detail-cancelled" alt="set-setpoint 批次 batch-setpoint-cancelled 的详情页，带有 Device list、Partial fan-out allowed 和 Cancelled — 2 stopped 徽章以及 Retry… 按钮。Summary 显示 Resolved 2、Accepted 2、Refused 0，以及一行：Called off on 10/14/2026, 3:01:33 PM, which stopped 2 commands still held by the platform。" />

徽章和摘要保留第一次取消的时间和数量。如果平台仍为已取消的批次暂存着命令，按钮会显示为 **Cancel again**（再次取消），再次取消会停止剩余部分，而不会改写那条记录。没有可停止的内容时，该按钮会消失。

## 对未成功的设备重试 {#retrying-the-devices-that-did-not-succeed}

**Retry…**（重试）会用相同的载荷，把批次的命令重新发送给未成功的设备。它会在批次页面顶部打开 **Retry unsuccessful devices**（重试未成功的设备）部分。重试会创建一个与当前批次关联的新批次，当前批次保持不变。目前还没有展示重试部分的截图。

该部分按设备的实际情况对未成功的设备分组：

- **Never delivered**（从未送达）：平台可以证明命令从未到达设备，因此再次发送不会让设备执行两次。默认已选中。
- **Device rejected**（设备已拒绝）：设备收到了命令，并回复说没有执行。默认已选中。
- **Refused when fired**（发出时被拒绝）：设备在批次发出时被拒绝。仅当批次记录列出了以该代码拒绝的每一台设备时才会提供（每个代码最多保留 100 台），但不会默认选中，因为原因可能仍然存在。
- **Outcome unknown**（结果未知）：命令已发出但没有收到回复，设备可能已经执行。默认不选中，并且处于锁定状态，直到你勾选复选框确认再次发送可能让这些设备执行两次。

仍在途中的命令会被计数，但无法选择。选择设备，然后选择重试按钮，按钮会注明你选了多少台设备（例如 **Retry 2 devices**）。新批次创建后会打开。它的页面显示 **Retry of batch:**（重试自批次）并带有返回链接，原批次则显示 **Retried by:**（被以下批次重试）并带有指向每次重试的链接。重试与新批次经过相同的检查，因此现在无法接收命令的设备，会在新批次中被列为拒绝。完整规则参见[对未成功的设备重试](/guides/commanding-a-fleet#retry-the-devices-that-did-not-succeed)。

## 维护窗口 {#maintenance-windows}

有些命令（例如重启）应该只在设备可以被打断时才开始。**维护窗口**是按时区设定的每周计划，会暂扣这些命令，直到窗口开启。

三项设置协同工作：

- 窗口在 **Devices → Maintenance Windows**（维护窗口）下定义，包括一个时区和一个或多个每周时段。
- 设备类型在其 **Maintenance window**（维护窗口）字段中指定一个窗口。该类型的每台设备都遵循它。
- 设备配置文件上的命令定义带有 **Impact**（影响）：Normal（普通）、Disruptive（中断性）或 Destructive（破坏性）。只有中断性和破坏性命令才会等待窗口。普通命令永远不会被暂扣。

当批次向窗口处于关闭状态的设备发送中断性或破坏性命令时，这些命令会被接受并等待。批次摘要会记录发出时有多少条在等待，**Commands** 列表中每条等待的命令都会显示 *Waiting for maintenance window until*（等待维护窗口，直到）窗口开启的时间。等待中的命令可以像其他排队命令一样被取消。如果窗口在命令过期之前不会开启，设备会改为以 `WINDOW_AFTER_EXPIRY` 被拒绝。

目前还没有展示维护窗口页面的截图。设置方法参见[维护窗口](/guides/maintenance-windows)，工作原理参见[概念页面](/concepts/commands#maintenance-windows)。

## 相关内容 {#related}

- [向设备群下发命令](/guides/commanding-a-fleet)：目标、拒绝代码、重试与取消的详细说明
- [维护窗口](/guides/maintenance-windows)
- [发送命令](/guides/sending-commands)
- [命令](/concepts/commands)：命令生命周期与结果
