---
title: 设备类型与配置文件
sidebar_position: 4
---

import GuideShot from '@site/src/components/GuideShot';

# 设备类型与配置文件

每台设备都属于一个**设备类型**，而设备类型指向一个**设备配置文件**。两者分工如下：

- **设备类型**说明设备是什么：名称、制造商和型号、在列表中的显示样式，以及其破坏性命令要等待哪个维护窗口。
- **设备配置文件**说明设备能做什么：它上报的测量值、接受的命令、接收的配置，以及监视它的规则。多个设备类型可以共用同一个配置文件。

本章同时介绍这两者，包括发布配置文件版本、回滚到较早的版本，以及把某个版本下载为 Thing Model。本章面向租户管理员，以及负责设置一类设备行为的人员。检测规则和规则健康状况同样位于配置文件上，它们有单独的章节。

这些界面背后的概念，参见[领域模型](/concepts/domain-model)。

## 设备类型 {#device-types}

在侧边栏中打开 **Devices**（设备），再打开 **Device Types**（设备类型）。列表显示每个类型的外观、令牌、描述和创建日期。**New device type**（新建设备类型）用于创建类型：填写令牌和名称，也可以再填写描述和维护窗口。

选择某个类型即可打开它。它的页面有四个选项卡：**Basic**、**Identity**、**Appearance** 和 **Profile**。

### Basic {#basic}

**Basic**（基本信息）包含类型的名称、描述和 **Maintenance window**（维护窗口）。此类型设备的破坏性命令和有中断影响的命令要等到该窗口开启才会发出。留空则立即发送。维护窗口的说明参见[命令](/concepts/commands#maintenance-windows)。

### Identity {#identity}

**Identity**（标识）记录实体设备的 **Manufacturer**（制造商）和 **Model**（型号）。

<GuideShot id="device-type-identity" alt="设备类型 Building Pulse Thermostat 的页面，令牌 bp-thermostat，选项卡为 Basic、Identity、Appearance 和 Profile。Identity 选项卡已打开。说明文字指出制造商和型号是用于筛选和分组的发现分面。下方是空的 Manufacturer 和 Model 输入框，示例值分别为 Acme Corp 和 TS-100，以及不可用的 Save identity 按钮。" />

两者都用于筛选和分组设备，因此输入框会建议已经在用的值。请尽量选择已有的值以保持一致，也可以输入新值。然后选择 **Save identity**（保存标识）。

### Appearance {#appearance}

**Appearance**（外观）设置类型在各处的绘制方式，例如设备列表和每台设备的页面。

<GuideShot id="device-type-appearance" alt="Appearance 选项卡。Preview 显示类型的标签 Building Pulse Thermostat。下方是三个颜色选择器 Background、Text 和 Border，因为未设置颜色，每个旁边都显示短横线；还有一个包含 36 个图标的 Icon 网格，没有选中任何图标。提示写着“Pick an icon, or click the selected one again to clear it.”。底部是 Save appearance 按钮。" />

选择 **Background**（背景）、**Text**（文字）和 **Border**（边框）颜色，以及一个 **Icon**（图标）。**Preview**（预览）会随着你的操作实时显示效果。若要移除图标，再次选择它即可。完成后选择 **Save appearance**（保存外观）。

### Profile {#profile}

**Profile**（配置文件）显示该类型使用的设备配置文件。

<GuideShot id="device-type-profile" alt="Profile 选项卡。说明文字为“A profile is the reusable capability contract — metrics, commands, and alarm rules — that this device type adopts. Authoring lives on the profile; here you choose which one this type uses.”。一张卡片显示已关联的配置文件 Building Pulse Thermostat Profile，令牌 bp-thermostat-profile，Category: sensor，带 Change 和 Detach 按钮以及“Author this profile”链接。" />

- **Author this profile**（编写此配置文件）会打开配置文件，在其中进行编辑。
- **Change**（更换）用于关联另一个配置文件。
- **Detach**（分离）会把配置文件从该类型上移除，配置文件本身不会被删除。

当一个配置文件被多个设备类型共用时，卡片会予以说明。对它的更改会影响所有这些类型。

没有配置文件的类型是允许的，但它的**能力受限**：其设备仍会被列出和显示，但不会上报带类型的指标，不接受命令，也不会触发告警。要解决这一点，可以关联已有的配置文件，或选择 **Create a profile for this type**（为此类型创建配置文件）。

## 设备配置文件 {#device-profiles}

在侧边栏中打开 **Devices**，再打开 **Device Profiles**（设备配置文件）。列表显示每个配置文件的令牌、类别、使用它的设备类型数量、描述和创建日期。**New device profile**（新建设备配置文件）用于创建配置文件，需要填写令牌、名称、**Category**（类别，即功能分类，如恒温器或电表）和描述。

选择某个配置文件即可打开它。它的选项卡为 **Basic**、**Metrics**、**Commands**、**Configuration**、**Detection Rules**（检测规则）、**Rule Health**（规则健康状况）和 **Versions**。检测规则和规则健康状况见其单独的章节。

### 草稿与版本 {#drafts-and-versions}

配置文件带有版本。你在各选项卡中所做的一切更改，改变的都是**草稿**。设备看不到草稿，它们使用配置文件的**生效版本**，即你**发布**时生成的冻结副本。因此，对指标、命令或配置键的编辑，只有在发布配置文件之后才会对设备生效。参见[发布版本](#publishing-a-version)。

### Metrics {#metrics}

**Metrics**（指标）列出该配置文件的设备上报的带类型的测量值。

<GuideShot id="profile-metrics" alt="设备配置文件 Building Pulse Thermostat Profile 的页面，令牌 bp-thermostat-profile，选项卡为 Basic、Metrics、Commands、Configuration、Detection Rules、Rule Health 和 Versions。Metrics 选项卡已打开，带有 New metric 按钮。表格的列为 Key、Type、Unit、Range 和 Actions，列出 co2（DOUBLE，ppm）、setpoint（DOUBLE，C）、humidity（DOUBLE，%）和 temperature（DOUBLE，C），每行都有 Edit 和删除按钮。" />

选择 **New metric**（新建指标）添加指标，或选择某一行的 **Edit**（编辑）进行修改。一个指标包含：

- **Metric key**（指标键）：设备的测量值携带的名称，例如 `temperature`；
- 令牌和 **Data type**（数据类型）；
- **Unit**（单位），使用 UCUM 书写，例如 `Cel`；
- 可选的 **Min**（最小值）和 **Max**（最大值），显示在 **Range**（范围）列中；
- 名称和描述。

指标也可以是**计算**指标，即由基于其他指标的公式得出，而不是由设备上报。参见[计算指标](calculated-metrics.md)。

### Commands {#commands}

**Commands**（命令）列出该配置文件的设备接受的命令。这些命令会出现在设备的 [Commands 选项卡](/user-guide/devices#commands)中。

<GuideShot id="profile-commands" alt="Commands 选项卡。顶部的一个框为 LwM2M 设备提供“Add LwM2M standard commands”。下方是文字“The commands a device accepts, with their optional parameter schema.”和 New command 按钮。表格的列为 Key、Name、Impact、Parameters 和 Actions，列出 identify（Flash the display，Normal，declared）和 set-setpoint（Set temperature setpoint，Normal，declared）。" />

选择 **New command**（新建命令）添加命令。

<GuideShot id="profile-command-new" alt="New command 面板。包含示例为 set_point 的 Command key 输入框、带重新生成按钮的 Token 输入框、Name 和 Description 输入框、设为 Normal 的 Impact 选择器（提示“Disruptive and destructive commands wait for the device type's maintenance window; normal commands are never held.”）、带示例 JSON 数组的 Parameter schema 框，以及 Create command 按钮。" />

- **Command key**（命令键）：设备接受的命令，例如 `set_point`。
- **Token**、**Name** 和 **Description**。
- **Impact**（影响级别）：**Normal**（普通）、**Disruptive**（有中断）或 **Destructive**（破坏性）。有中断和破坏性的命令要等待设备类型的维护窗口。普通命令永远不会被暂扣。
- **Parameter schema**（参数模式，可选）：描述命令参数的 JSON 数组，每个参数带有 `name` 和 `dataType`。没有参数的命令可留空。控制台会据此模式为设备的 Commands 选项卡构建参数表单。

对于 LwM2M 设备，**Add LwM2M standard commands**（添加 LwM2M 标准命令）会把 **Reboot**（重启）和 **Read device information**（读取设备信息）声明为具名命令，这样发送时无需输入对象路径。已声明的命令保持不变。

命令如何按配置文件校验，以及影响级别的含义，参见[命令](/concepts/commands#commands-and-the-capability-contract)。

### Configuration {#configuration}

**Configuration**（配置）声明该配置文件的设备把哪些共享属性键作为其配置接收。

<GuideShot id="profile-configuration" alt="Configuration 选项卡。说明文字为“The shared attribute keys a device of this profile can see as its configuration. A device sees only the declared keys; with none declared it sees an empty document. Edits change the draft and reach devices when the profile is published.”。右上角是 Declare key 按钮。表格列出 setpoint（DOUBLE，Target temperature in degrees Celsius）、schedule（STRING，Occupancy schedule name）和 reportIntervalSeconds（LONG，How often the thermostat reports），每行都有 Edit 和删除按钮。" />

设备只能看到已声明的键。一个都没有声明时，它看到的是空文档。选择 **Declare key**（声明键）添加一个：填写 **Attribute key**（属性键）、**Value type**（值类型：**String**、**Integer (long)**、**Decimal (double)**、**Boolean** 或 **JSON**）和描述。

具体的值按设备以共享属性的方式设置。设备的 [Configuration 选项卡](/user-guide/devices#configuration)会显示结果。

### 发布版本 {#publishing-a-version}

**Versions**（版本）按从新到旧的顺序列出配置文件的每个已发布版本，并标明哪个是生效版本。

<GuideShot id="profile-versions" alt="Versions 选项卡。内容为“Active version: 2”，并说明发布会把当前草稿快照为新版本，设备解析的是生效版本。右上角是 Publish 按钮。表格的列为 Version、Label、Published、By 和 Actions，列出版本 2（生效，“Remote setpoint”）和版本 1（“Building Pulse Thermostat Profile”），均由 dana.okafor@acme-properties.example 发布。每行都有 WoT Thing Model 下载按钮和 Roll back 按钮；生效版本的 Roll back 为灰色。" />

要发布草稿，请选择 **Publish**（发布）。

<GuideShot id="profile-version-publish" alt="Publish profile 面板，说明为“Freeze the current draft into a new version and make it the version devices resolve.”。包含示例为 v1.0 的可选 Label 输入框、可选的 Description 输入框，以及 Publish 按钮。" />

你可以为版本填写 **Label**（标签）和 **Description**（描述），两者都是可选的。然后选择 **Publish**。草稿会被冻结为新版本，并立即成为生效版本。该版本包含草稿上的全部内容，包括指标、命令、配置键和检测规则。如果多个设备类型使用该配置文件，**Versions** 选项卡会警告你，发布会改变它们全部所用的内容。

配置文件首次发布之前，它的设备无法从中解析出任何内容，选项卡会显示“Not published yet”（尚未发布）。请至少发布一次。

### 回滚 {#rolling-back}

如果新版本引发问题，可以让较早的版本重新生效。在该版本所在行选择 **Roll back**（回滚），然后确认。

<GuideShot id="profile-version-rollback" alt="Versions 选项卡上方的确认对话框，标题为“Roll back”。内容为“Make version 1 the active version devices resolve? The draft is unchanged.”，带有 Cancel 和 Roll back 按钮。" />

回滚只改变哪个版本生效，不会改变你的草稿，因此你正在进行的编辑仍然保留。生效版本的 **Roll back** 按钮是灰色的，因为它已经处于生效状态。

每次发布和每次回滚也会记录在配置文件的激活日志中：哪个版本变为生效、何时生效、由谁操作。Versions 选项卡上的 **Published** 列显示的是版本的发布时间；如果之后又回滚到该版本，发布时间并不是它生效的时间，因此请用激活日志来判断某个时刻哪个版本处于生效状态。日志通过 GraphQL API 中设备配置文件的 `activations` 字段读取，按从新到旧排序。它从你升级到引入该功能的版本时开始记录，因此在此之前最后一次发布或回滚的配置文件，没有对应那次变更的条目。请把缺失的条目理解为未知，而不是未生效。

### 下载 Thing Model {#downloading-a-thing-model}

每个版本行都有一个 **WoT Thing Model** 按钮。它会把该版本下载为 W3C Web of Things 的 Thing Model：这是一个标准的 JSON-LD 文件，把配置文件的指标描述为属性，把命令描述为动作。你可以把它交给理解 Web of Things 的工具。文件以配置文件令牌和版本命名，例如 `bp-thermostat-profile.v2.tm.jsonld`。

如果某个版本使用了 Thing Model 无法表达的内容，下载会失败，并给出指明该版本的消息。导出包含哪些内容，以及何时会被拒绝，参见[将配置文件导出为 W3C Thing Model](/concepts/domain-model#wot-thing-model)。

## 相关内容 {#related}

- [设备](/user-guide/devices)
- [领域模型](/concepts/domain-model)
- [命令](/concepts/commands)
- [发送命令](/guides/sending-commands)
- [连接设备](/guides/connecting-a-device)
