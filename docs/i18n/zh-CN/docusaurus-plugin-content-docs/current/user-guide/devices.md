---
title: 设备
sidebar_position: 3
---

import GuideShot from '@site/src/components/GuideShot';

# 设备

设备是连接到 DeviceChain 并上报数据的一个实体：恒温器、电表、追踪器等。本章介绍设备列表、添加设备的几种方式（逐台添加、批量添加或从 CSV 文件导入）、导出列表、禁用设备，以及设备页面的每个选项卡。

本章面向注册和维护设备的租户用户与管理员。每台设备都属于某个设备类型，因此添加设备之前，至少需要有一个设备类型。设备类型在[下一章](/user-guide/device-types-and-profiles)介绍。

## 设备列表 {#the-device-list}

在侧边栏中打开 **Devices**（设备），再打开其子菜单中的 **Devices**。

<GuideShot id="devices-list" alt="Devices 页面。右上角的按钮依次为 Export CSV、Import、Bulk create 和 New device。下方的表格包含 Status、Token、Name、Type、Description 和 Created 列。十二行显示 Online，第一行 Boiler room thermostat 的状态显示为短横线。每一行的类型都是 Building Pulse Thermostat。页脚显示“Showing 1–13 of 13 results”，并带有 Prev 和 Next 按钮。" />

列表每页显示 20 台设备。选择某一行即可打开该设备的页面。各列含义如下：

- **Status**（状态）：设备当前是否已连接。**Online**（在线）表示它已连接或最近有上报。**Disconnected**（已断开）表示它的连接报告自己已断开。**Offline**（离线）表示已经有一段时间没有收到它的消息。短横线表示目前还不知道其状态：设备从未上报，或者你无权查看其状态。
- **Token**（令牌）：设备在此租户中的唯一标识，不可更改。
- **Name**（名称）：便于阅读的名称。被禁用的设备在名称旁带有 **Disabled**（已禁用）徽章。
- **Type**（类型）：设备类型，以该类型设置的颜色和图标显示。
- **Description**（描述）和 **Created**（创建时间）。

DeviceChain 如何判断设备是否在线，参见[设备在线状态](/concepts/device-presence)。

## 添加单台设备 {#adding-one-device}

选择 **New device**（新建设备）。

<GuideShot id="devices-new" alt="设备列表上方打开的 New device 面板。包含显示“Select a device type…”的 Device type 选择器、显示 {slug} 的 Token 输入框（带重新生成按钮和提示“Pattern: {slug}”）、Name 输入框、Description 输入框，以及 Create device 按钮。" />

1. 选择 **Device type**（设备类型）。
2. 输入 **Token**，或使用输入框旁的按钮，按安装环境的令牌模式生成一个。模式显示在输入框下方。`{slug}` 会根据你输入的名称自动填充。
3. 输入 **Name**，也可以填写 **Description**。
4. 如果设备不是自行连接，而是通过 LoRaWAN 网络服务器或其他发送方上报，请添加它的[别名](#aliases)。
5. 选择 **Create device**（创建设备）。

自行连接的新设备在拥有凭据之前无法上报任何数据。请在它的 [Credentials](#credentials) 选项卡中添加凭据。通过[入站集成](/guides/inbound-integrations)上报的设备则需要别名。

### 别名 {#aliases}

**别名**是其他系统对设备的称呼，例如它的 LoRaWAN DevEUI。设备表单的 **Aliases** 字段最多可保存 8 个，每个方案一个值：

1. 选择 **Add alias**。
2. 选择方案：**LoRaWAN DevEUI**（16 位十六进制数字）、**Network server device id**（设备在 The Things Stack 中的设备 ID）或 **Custom scheme**（你自己的名称和值）。
3. 输入值。无论你怎样书写，DevEUI 都以大写形式存储。

一个别名只属于租户内的一台设备。入站集成通过别名找出消息所属的设备。参见[入站集成](/guides/inbound-integrations#aliases)。

## 批量创建设备 {#creating-many-devices-at-once}

**Bulk create**（批量创建）会创建一批类型相同、按序编号的设备。

<GuideShot id="devices-bulk" alt="Bulk create devices 面板。包含 Device type 选择器、设为 10 的 Count（1–1000）和设为 1 的 Start index、值为 device-{n:04d} 的 Token template、值为“Device {n}”的 Name template，以及占位符为 VIN-{random} 的空 External ID template。Preview 框列出 device-0001 Device 1 至 device-0003 Device 3、省略号，以及 device-0010 Device 10。Create 10 devices 按钮不可用，并提示“Choose a device type.”。" />

- **Device type**：这批设备都使用此类型。
- **Count**（数量）：要创建的设备数，范围为 1 到 1000。
- **Start index**（起始编号）：使用的第一个编号。
- **Token template**（令牌模板）：必须包含 `{n}`，或使用 `{n:0Wd}` 将编号用零补齐到 W 位宽度。`device-{n:04d}` 会生成 `device-0001`、`device-0002`，依此类推。
- **Name template**（名称模板，可选）：可使用 `{n}` 或 `{n:0Wd}`。
- **External ID template**（外部 ID 模板，可选）：每台设备你自己的业务 ID。也可以使用 `{random}` 为每台设备生成随机 ID。创建多台设备时，它必须包含 `{n}`、`{n:0Wd}` 或 `{random}`，以保证每台设备的 ID 各不相同。

**Preview**（预览）框显示这批设备将得到的首尾令牌和名称。确认无误后，选择 **Create N devices**（创建 N 台设备）。

## 从 CSV 文件导入设备 {#importing-devices-from-a-csv-file}

**Import**（导入）会根据 CSV 文件创建设备。可用它加载你已在电子表格中维护的设备清单，或制造商提供的文件。

<GuideShot id="devices-import" alt="Import devices from CSV 面板。Credentials 控件提供 None（已选中）和 Factory credentials，提示“Create the devices only. Add credentials afterwards.”。下方是限制说明“A CSV file up to 5 MiB and 10000 rows, with a header row. UTF-8; comma, semicolon or tab separated.”、Choose a CSV file 按钮、带示例表头和示例行的“Or paste the rows”文本框，以及 Use pasted rows 按钮。" />

1. 在 **Credentials**（凭据）下，选择 **None** 表示只创建设备，或选择 **Factory credentials**（出厂凭据），在创建每台设备的同时创建工厂为其提供的凭据。
2. 通过 **Choose a CSV file**（选择 CSV 文件）选择文件，或粘贴行内容并选择 **Use pasted rows**（使用粘贴的行）。文件需要包含表头行，最多 10,000 行、5 MiB。
3. 在 **Map the columns**（映射列）下，检查文件中的哪一列对应哪个字段。必须映射令牌列，并且每一行都需要设备类型：映射一个类型列，或选择 **Device type for the whole file**（整个文件使用同一设备类型）。选择 **Factory credentials** 时，还必须映射凭据类型列和凭据 ID 列。预览会显示将要发送的前几行。
4. 选择 **Check the file**（检查文件）。这是一次试运行：服务器对每一行的判定与真实导入完全一致，但不会创建任何内容。
5. 阅读 **What an import would do**（导入将会执行的操作）。每一行都有一个结果：**Created**（已创建）、**Already exists**（已存在）、**Conflict**（冲突）、**Invalid**（无效）或 **Refused**（被拒绝），未创建的行附有详细说明。可以按结果筛选行。
6. 选择 **Import N devices**（导入 N 台设备），其中 N 是检查发现将被创建的行数。进度条显示当前进度。**Stop after this batch**（本批完成后停止）会在批次之间干净地停止。再次导入同一文件会从停止处继续。
7. 完成后，摘要会说明创建了多少台设备、多少台已存在、多少台需要处理。**Download results**（下载结果）会保存每一行的结果，**Download failed rows**（下载失败行）只保存需要修正的行。修正后导入该文件即可。已创建的设备会被报告为已存在，不受影响。

选择 **Factory credentials** 时，文件中包含密钥。它们只发送一次，不会显示，也不会写入结果，导入结束后会从页面中清除。文件本身需要你自行妥善保管。

列、凭据模式和各种结果的完整说明见[从 CSV 导入设备](/guides/importing-devices)。

## 导出列表 {#exporting-the-list}

**Export CSV**（导出 CSV）会把列表中的所有设备下载为 CSV 文件。文件包含每台设备的令牌、名称、类型、类型令牌、描述和创建时间，时间采用 ISO-8601 格式。文件不包含实时状态。表头与 **Import** 所需的一致，因此导出的文件可以原样再次导入。

导出过程中，按钮会显示已收集的行数，并附有 **Cancel export**（取消导出）按钮。导出完成后会有提示说明导出了多少行。导出最多 10,000 行。如果达到上限，控制台会告知文件只包含前 10,000 行。

## 禁用和启用设备 {#disabling-and-enabling-a-device}

禁用设备会切断它的连接，但不会删除它。可用它来隔离行为异常或可能已被入侵的设备，修复后再重新启用。

打开设备，选择右上角的 **Disable**（禁用）。控制台会请你确认。设备被禁用期间：

- 它的事件会被丢弃而不是存储，启用后也不会恢复；
- 它无法建立新连接，不过已有的连接会保持到结束。该连接结束时，设备会显示为已断开；
- 发给它的新命令会被拒绝；
- 此前已排队等待它的命令会被**暂扣**：既不发送，也不取消。

它的数据、凭据和配置都会保留。被禁用的设备在其页面和设备列表中带有 **Disabled** 徽章。

有命令被暂扣时，设备的 **Commands** 选项卡会显示数量，例如“2 queued commands held while this device is disabled”（设备被禁用期间暂扣了 2 条排队命令）。启用设备后，这些命令按先后顺序发出，或在各自的过期时间到达时作废。如果它们永远不应发送，请选择 **Cancel all**（全部取消）。已经发出的命令无法召回。

要让设备恢复，请选择 **Enable**（启用）。它的事件会重新被存储，也可以再次连接并接收命令。

禁用设备的完整影响（包括配置和告警）参见[禁用设备](/guides/device-credentials#disabling-a-device)。

## 设备页面 {#the-device-page}

在列表中选择一台设备即可打开其页面。页眉显示设备名称、令牌（带复制按钮）、类型，以及被禁用时的 **Disabled** 徽章。右侧是 **Disable**（或 **Enable**）和 **Delete**（删除）。删除设备无法撤销。

页面按选项卡组织。你可能看不到全部选项卡：**Credentials**、**Send data** 和 **Replacement** 需要修改设备的权限，**Updates** 只在你的安装环境运行了更新服务且你有权查看更新时才会出现。

### Basic {#basic}

**Basic**（基本信息）包含设备的类型、令牌、名称和描述。

<GuideShot id="device-detail-basic" alt="设备 Lobby thermostat 的页面，令牌 bp-therm-001，类型为 Building Pulse Thermostat。右上角是 Disable 和 Delete 按钮。选项卡依次为 Basic、Assignment、Facets、Connectivity、Events、Commands、Configuration、Updates、Credentials 和 Replacement。Basic 选项卡已打开，显示 Device type、灰色的 Token 输入框（提示“The device id; it cannot change.”）、Name 和 Description，以及 Save changes 按钮。" />

你可以修改类型、名称和描述，然后选择 **Save changes**（保存更改）。令牌不可更改。

### Assignment {#assignment}

**Assignment**（分配）把设备与它服务的客户、区域或资产关联起来，用于组织设备的数据。它不决定设备能否上报：没有任何分配的设备仍然可以上报。

<GuideShot id="device-detail-assignment" alt="Assignment 选项卡。顶部是设为 Customer 的 Target type 选择器、显示“Select a target…”的 Target 选择器和 Assign 按钮。下方的表格列出三条分配，每条都有 Unassign 按钮：资产 bp-hvac-01、客户 bp-acme 和区域 bp-bldg-01。" />

选择 **Target type**（目标类型，**Customer**、**Area** 或 **Asset**），选取 **Target**（目标），然后选择 **Assign**（分配）。一台设备可以同时有多条分配。**Unassign**（取消分配）会移除其中一条。设备已经上报的事件会保留其记录时的分配。

参见[管理设备分配](/guides/managing-assignments)。

### Facets {#facets}

**Facets**（分面）包含描述这台设备的值，例如它所在的区域或楼层。**Browse** 和动态分组正是按这些值查找设备。

<GuideShot id="device-detail-facets" alt="Facets 选项卡。在“Facet values”下有两个分面：值为 lobby 的 Zone（STRING）和值为 1 的 Floor（LONG），各带 Save 和 Clear 按钮。Zone 下方有提示“Changing this value moves this entity in or out of: Rooftop units”。再下方，“Other attributes”列出 schedule（SHARED，STRING，weekday-standard）和 setpoint（SHARED，DOUBLE，22.5），各带 Delete 按钮，另有 Add attribute 按钮。" />

- **Facet values**（分面值）列出租户已声明的分面。输入值并选择 **Save**，或选择 **Clear**（清除）。当某个值决定动态分组的成员资格时，选项卡会告诉你更改会让设备进入或退出哪些分组。
- **Other attributes**（其他属性）列出设备上存储的其余所有内容。你可以通过 **Add attribute**（添加属性）添加共享属性，也可以删除属性。设备上报的值（**CLIENT**）或平台设置的值（**SERVER**）仅供参考。共享属性同时也是设备[配置](#configuration)值所在的位置。

分面和浏览有单独的章节。

### Connectivity {#connectivity}

**Connectivity**（连接状态）显示设备是否已连接，以及它最近的读数。

<GuideShot id="device-detail-connectivity" alt="Connectivity 选项卡。Connectivity 部分显示 Online 徽章、Presence source 为“Inferred from activity”，以及 Last connect、Last activity 和 Last disconnect 行，截图中隐藏了时间。Latest readings 部分列出测量值 co2、humidity、setpoint 和 temperature，其值和更新时间已隐藏。" />

- 徽章显示 **Online**、**Disconnected** 或 **Offline**。
- **Presence source**（状态来源）说明状态是如何得知的。**Reported by the transport**（由传输层报告）表示连接本身告知了 DeviceChain。**Inferred from activity**（根据活动推断）表示 DeviceChain 根据设备最近一次发送数据的时间做出判断。
- **Last connect**（最近连接）、**Last activity**（最近活动）和 **Last disconnect**（最近断开）给出相应的时间。
- **Latest readings**（最新读数）列出每个测量值的最新取值及其到达时间。

当一台通过推断判断状态的设备安静了一段时间后，选项卡会增加一条“No recent activity”（近期无活动）的提示：它已有一段时间没有发送任何数据，而上报间隔较长的健康设备看起来也是如此。

如果设备的配置文件声明了位置，或者设备上报过位置，还会出现 **Last known position**（最后已知位置）部分，其中包含纬度、经度，以及已上报时的海拔、精度、速度和航向。截图中的恒温器两者都不满足，因此不显示该部分。

### Events {#events}

**Events**（事件）列出设备发送的最近 10 条事件。

<GuideShot id="device-detail-events" alt="Events 选项卡：一张表格，列为 Occurred、Type 和 Source。十行的类型都是 Measurement，来源都是 http1，截图中隐藏了时间。表格下方写着“Showing the 10 most recent events.”。" />

每一行显示事件发生的时间、类型（如 **Measurement**）以及事件到达时使用的来源。若要查看更长的历史，请使用仪表板。

### Commands {#commands}

**Commands**（命令）用于向这一台设备发送命令，并跟踪已发给它的命令。

<GuideShot id="device-detail-commands" alt="Commands 选项卡。Command 选择器显示“Select a command”，提示为“The commands this device's published profile declares.”，Issue command 按钮不可用。下方的表格列为 Queued、Name、Status 和 Result，显示一条命令 set-setpoint，状态为 SENT，尚无结果。" />

**Command** 选择器会列出设备配置文件声明的命令。选择一条后，控制台会显示其参数表单。

<GuideShot id="device-commands-parameter-form" alt="已选择“Set temperature setpoint (set-setpoint)”的 Commands 选项卡。出现两个参数字段：标记为必填的 setpoint，提示为“Target temperature · in Cel · 10–30”；以及显示“Select a value”的 mode 选择器，提示为“Operating mode”。Issue command 按钮现在可用。" />

填写参数并选择 **Issue command**（下发命令）。表单会在发送之前按配置文件检查每个值，例如允许的范围。如果配置文件没有声明任何命令，你可以改为输入任意命令名称和可选的载荷。

下方的表格列出设备的命令及其状态（如 **QUEUED**、**SENT** 或 **SUCCESSFUL**）和设备上报的结果。尚未到达设备的命令（**QUEUED**、**HELD** 或 **PARKED**）带有 **Cancel**（取消）按钮。已经发出的命令无法取消。

各状态的含义参见[命令](/concepts/commands#command-lifecycle)，发送和跟踪命令的方法参见[发送命令](/guides/sending-commands)。若要一次向多台设备发送命令，请使用命令批次。

### Configuration {#configuration}

**Configuration**（配置）显示 DeviceChain 为设备保存、供其应用的设置，以及设备是否已应用。

<GuideShot id="device-detail-configuration" alt="Configuration 选项卡。一个 Pending 徽章和文字“Revision #3 is waiting to be applied; the device has not reported a revision.”。下方是带 sha256 摘要的 Desired revision #3、显示“Never reported”的 Reported revision，以及 Report status、Reported 和 Last synchronized 的短横线。Declared keys 表列出 setpoint（DOUBLE）、schedule（STRING）和 reportIntervalSeconds（LONG）及其说明。“Undeclared shared attributes”显示 floor 和 zone。底部是 Desired document，一行包含 schedule 和 setpoint 的 JSON。" />

设备的配置是由其共享属性构成的一个小型文档，只包含其配置文件声明的键。每次变更都会生成一个带编号的**修订版本**，并发送给设备。

- 顶部的徽章说明设备是否是最新的。**In sync**（已同步）表示它已应用最新修订版本。**Pending**（待应用）表示有修订版本正等待应用。**Out of date**（已过期）表示自最新修订版本以来数值已发生变化。下一次写入声明的键或设备下一次同步时，会生成新的修订版本。**No revision yet**（尚无修订版本）表示还没有生成过任何修订版本。
- **Desired revision**（期望修订版本）是最新的一个，**Reported revision**（已上报修订版本）是设备最后一次声明其持有的版本。**Report status**（上报状态）是设备对它的处理结果：**Received**（已接收）、**Applied**（已应用）或 **Rejected**（已拒绝）。设备拒绝配置时，会列出原因。
- **Declared keys**（已声明的键）列出配置文件在其生效的已发布版本中声明的键。你在 [Facets](#facets) 选项卡中以共享属性的方式修改它们的值。
- **Undeclared shared attributes**（未声明的共享属性）是配置文件未提及的共享属性，它们永远不会发送给设备。
- **Desired document**（期望文档）是发送给设备的确切文档。
- **Revision history**（修订历史）按从新到旧的顺序列出设备的修订版本，并显示每个修订版本由谁生成。

你可以让设备恢复到较早的配置，或给它套用另一台设备的配置。两种情况下，控制台都会先显示预览，只有选择 **Apply**（应用）后才会生效。

要恢复较早的修订版本，请在 **Revision history** 中选择该行的 **Restore**（恢复）。

<GuideShot id="device-configuration-restore" alt="向下滚动后的 Configuration 选项卡。“Restore revision #2”预览写着“Applying changes 1 key. It creates a new revision and sends it to the device; earlier revisions are kept unchanged.”。其表格列为 Key、Current、After 和 Change：schedule 保持 weekday-standard，setpoint 从 22.5 变为 21，标记为 Changed。随后是 Apply 和 Cancel 按钮。下方是 Revision history 表，列出修订版本 #3 和 #2、它们的摘要、创建时间和修改者，#2 上带有 Restore 按钮。" />

要复制另一台设备的配置，请在 **Copy from another device**（从另一台设备复制）下选取该设备，然后选择 **Preview changes**（预览更改）。复制的是那台设备的最新修订版本。

<GuideShot id="device-configuration-copy" alt="已选择 Rooftop AHU 1 return air 的 Copy from another device 部分。标题为“Copy the configuration of “bp-therm-002””的预览写着“Applying changes 3 keys.”。其表格显示 reportIntervalSeconds 新增为 300，schedule 被移除，setpoint 从 22.5 变为 19.5。随后是 Apply 和 Cancel 按钮。" />

预览会列出每个键的 **Current**（当前值）、应用后的 **After**（新值），以及它是 **Added**（新增）、**Changed**（变更）还是 **Removed**（移除）。应用后会生成新的修订版本并发送给设备，较早的修订版本保持不变。如果该设备的配置文件没有声明配置中包含的每一个键，变更会被拒绝。

设备如何接收和上报配置，参见[恢复或复制配置](/guides/connecting-a-device#restore-copy-configuration)。

### Updates {#updates}

**Updates**（更新）显示分配给设备的空中升级及其更新历史，相关内容见空中升级一章。

### Credentials {#credentials}

**Credentials** 保存设备连接时用来证明自身身份的凭据。

<GuideShot id="device-detail-credentials" alt="设备 Rooftop AHU 2 return air 的 Credentials 选项卡。顶部是一个表单，包含设为 ACCESS_TOKEN 的 Type 选择器、带 Generate 按钮的 Access token 输入框和 Add credential 按钮。下方的表格列出两条凭据：ID 为 rooftop-ahu-2 的 MQTT_BASIC，附带说明“Password is set. It cannot be shown.”，已启用，带 Set new password 和 Delete 按钮；以及 ACCESS_TOKEN，截图中隐藏了其 ID，已启用，带 Delete 按钮。" />

有两种类型：

- **ACCESS_TOKEN**：设备出示一个令牌。可以粘贴自己的令牌，或选择 **Generate**（生成）。
- **MQTT_BASIC**：设备使用用户名和密码连接。

选择 **Type**（类型），填写字段，然后选择 **Add credential**（添加凭据）。一台设备可以有多个凭据。**Delete** 会移除其中一个，此后设备无法再用它进行认证。

MQTT 密码只显示一次：在你添加凭据之后，或选择 **Set new password**（设置新密码）更改之后。平台只保存单向摘要，因此永远无法再次显示该密码。

<GuideShot id="device-credentials-shown-once" alt="Credentials 选项卡，其中有一个高亮框，标题为“New password set”。内容写着“Copy it now. You won't see this password again; the old one no longer works.”，随后显示用户名 rooftop-ahu-2 和带复制按钮的新密码，以及 Done 按钮。" />

复制密码并交给设备，然后选择 **Done**（完成）。设置新密码后，旧密码在保存的那一刻就会失效。

参见[设备凭据](/guides/device-credentials)。

### Send data {#send-data}

**Send data**（发送数据）会生成一条可直接运行的命令，用来发送设备的第一条测量数据：MQTT 使用 `mosquitto_pub`，HTTP 使用 `curl`。选择传输方式（设备有多个凭据时还要选择凭据），然后用复制按钮复制命令。

命令使用设备的令牌和凭据，以及其配置文件**已发布**版本中的指标，并填入配置文件接受的示例值。只存在于草稿中的指标不会被使用，因为平台按已发布版本校验读数。如果已发布版本中没有示例可用的指标，命令会改为发送一个未声明的读数；只要其值是数字，就会被存储。最多包含八个指标。

这些命令是为 bash 或 zsh 等 POSIX shell 编写的。在 Windows 上，请在 WSL 或 Git Bash 中运行：PowerShell 和 `cmd.exe` 对引号和换行的解释不同，命令发送的内容将与显示的不一致。

控制台无法得知某些值，因此命令为每个这样的值留下一个占位符：

| 占位符 | 替换为 |
| --- | --- |
| `<instance-id>` | 安装实例时选定的实例 ID，即去掉 `dci-` 前缀后的实例 Kubernetes 命名空间。 |
| `<mqtt-host>` | MQTT 监听器对外暴露的主机。该监听器使用 TLS，因此请将实例 CA 证书保存为 `instance-ca.crt`。 |
| `<ingest-host>` | HTTP 摄取监听器的主机，端口 8081。默认安装不会将其暴露到集群之外，唯一例外是实例 HTTPS 地址上的入站集成 webhook `/api/integrations/{tenant}/{integration}`；参见[你的第一台设备](/quickstart/first-device#5-open-a-path-to-the-ingest-endpoint)。 |
| `<password>` | `MQTT_BASIC` 凭据的密码。它只在设置时显示一次。如果遗失，请在 **Credentials** 选项卡中设置新密码。 |

当命令不可能成功时，该选项卡不提供命令，而是说明需要修正什么：

- 设备已**禁用**。已禁用设备的事件在到达时即被丢弃。
- 设备没有已启用且未过期的凭据。默认情况下，平台要求每个连接和每个事件都携带凭据。请先在 **Credentials** 选项卡中添加一个。

未分配给任何对象的设备也可以发送数据，其事件会被存储。

与 **Credentials** 一样，该选项卡需要修改设备的权限，因为访问令牌会写入命令中。

HTTP 返回 `202` 或收到 MQTT 确认，只表示事件已被接收，并不表示已被存储。请在 **Events** 选项卡中查看。

参见[连接设备](/guides/connecting-a-device)。

### Replacement {#replacement}

**Replacement**（更换）用于更换硬件：某台设备已损坏，由新设备接替。设备保留其身份、历史、告警、分配和分组成员资格，只有凭据会改变。

<GuideShot id="device-detail-replacement" alt="设备 Boiler room thermostat 的 Replacement 选项卡。说明文字为“Bind a new physical unit to this device. Its identity, external id, type, history, alarms, assignments and group membership all carry forward — only the credentials change.”。Reason 输入框的内容是“Display unreadable after a power surge”，New unit identifier 输入框的内容是 SN-5180-C，其上方是 Replace unit 按钮。下方的历史表列为 Replaced、By、Reason、New unit 和 Credentials retired，显示一条此前的更换记录（截图中隐藏了时间）：操作人 dana.okafor@acme-properties.example，原因“Failed sensor, swapped for the stockroom spare”，新设备 SN-4471-B，停用 1 条凭据。" />

1. 填写 **Reason**（原因），它会记录在维护历史中。
2. 填写 **New unit identifier**（新设备标识）：新硬件自己的序列号或 IMEI，它会记录在历史中。设备的外部 ID 不会改变。
3. 选择 **Replace unit**（更换设备）并确认。

旧设备能够使用的所有凭据都会被禁用，并为新设备签发新的凭据。新凭据只显示一次，请在选择 **Done** 之前复制它，之后无法从历史中读回。

下方的表格列出此前的更换记录：时间、操作人、原因、新设备以及停用了多少条凭据。

## 相关内容 {#related}

- [设备类型与配置文件](/user-guide/device-types-and-profiles)
- [领域模型](/concepts/domain-model)
- [设备在线状态](/concepts/device-presence)
- [命令](/concepts/commands)
- [从 CSV 导入设备](/guides/importing-devices)
- [设备凭据](/guides/device-credentials)
- [管理设备分配](/guides/managing-assignments)
- [连接设备](/guides/connecting-a-device)
- [入站集成](/guides/inbound-integrations)
