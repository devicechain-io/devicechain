---
title: 仪表板
sidebar_position: 8
---

import GuideShot from '@site/src/components/GuideShot';

# 仪表板

仪表板把实时组件（图表、仪表、卡片、告警表、命令按钮、地图）排布在同一个页面上。仪表板属于租户，并且每个都带有版本：你编辑草稿，然后将其发布为其他人看到的版本。

本章面向创建和维护仪表板的人员，涵盖仪表板列表、从零开始、从模板或从导出的文件创建仪表板、查看仪表板、在画布上编辑、配置组件、用合成数据预览、导出，以及发布和回滚版本。截图使用一个测试租户，其 *Widget Lab* 仪表板包含每种内置组件各一个。仪表板底层的工作方式，参见[仪表板](/concepts/dashboards)。

:::note 谁能做什么
租户的每位成员都可以打开仪表板。创建、编辑、发布和删除仪表板需要仪表板写入权限。没有该权限的成员看到的是已发布版本，而不是草稿。如果仪表板从未发布过，系统会告诉他们请作者发布。
:::

## 仪表板列表 {#the-dashboard-list}

在侧边栏中选择 **Dashboards**（仪表板）。

<GuideShot id="dashboards-list" alt="Dashboards 页面，带有 Import dashboard 和 New dashboard 按钮。列出两个仪表板 Widget Lab — Stress 和 Widget Lab，每个都有描述、更新时间以及 Clone 和 Delete 操作。" />

每一行显示仪表板的名称、描述和最近更新时间。选择某一行即可打开。**Clone**（克隆）会以新令牌复制一份，**Delete**（删除）会在你确认后删除仪表板，删除无法撤销。

## 创建仪表板 {#creating-a-dashboard}

**New dashboard**（新建仪表板）会打开一个抽屉，有两种开始方式。

<GuideShot id="dashboards-new" alt="New dashboard 抽屉，带有 Token、Name 和 Description 输入框以及 Create dashboard 按钮。下方的“Or start from a template”下提供三个模板：Fleet overview、Device detail 和 Alarms board，每个都有一行描述。" />

- **从零开始。** 输入令牌（仪表板的永久 ID），或使用输入框旁的按钮生成一个。令牌是唯一必填项，名称和描述是可选的。输入框会标出已被占用的令牌。选择 **Create dashboard**（创建仪表板），空白仪表板便会打开。
- **从模板开始。** 在 **Or start from a template**（或从模板开始）下，选择 **Fleet overview**（设备群概览，按严重程度统计的告警数、一张告警表和一台重点设备）、**Device detail**（设备详情，某台设备的最新值、一个仪表、一张遥测图表、位置和告警）或 **Alarms board**（告警看板，整个租户的告警数和告警表）。模板会在下文所述的导入审阅中打开，创建之前先在其中把它绑定到你自己的设备。

模板中的 `temperature` 和 `humidity` 等测量名称只是示例，请在编辑器中改成你的设备实际上报的名称。

## 导入仪表板 {#importing-a-dashboard}

**Import dashboard**（导入仪表板）会根据从另一个仪表板或另一个租户导出的 JSON 创建新仪表板。

<GuideShot id="dashboards-import" alt="Import dashboard 抽屉。它说明新仪表板将以新令牌保存为草稿，不会覆盖或发布任何内容。包含 Dashboard JSON 文本框、说明仪表板（不含缩进）最大 1 MiB 的提示，以及 Choose file 和 Review 按钮。" />

1. 将 JSON 粘贴到 **Dashboard JSON**（仪表板 JSON）中，或使用 **Choose file**（选择文件）加载 `.json` 文件。
2. 选择 **Review**（审阅）。控制台会检查定义，并列出保存之前你应该关注的内容：
   - **Bindings to review**（待审阅的绑定）：尚未绑定的槽位，或所指向的设备、客户、区域或资产在此租户中不存在的槽位。为每个槽位从此租户中选取一个，也可以让选择器保持 *Leave unbound*（保持未绑定），以后再绑定。
   - 此控制台无法显示的组件，它们会作为占位符保留，并在安装或移除之前阻止发布；
   - 需要修正的组件选项；
   - 仪表板加载图像或地图瓦片所用的外部站点。
3. 为新仪表板指定令牌，然后选择 **Create draft**（创建草稿）。

导入绝不会覆盖已有的仪表板，也不会替你发布。**Clone** 和模板使用相同的审阅流程。

## 查看仪表板 {#viewing-a-dashboard}

打开仪表板会以实时数据显示它。

<GuideShot id="dashboard-view" alt="查看模式下的 Widget Lab 仪表板，带有 Preview、Export、History、Edit 和 Delete 按钮。组件包括标题标签、wl-sensor-01 温度的最新值卡片和仪表、wl-zone-01 的 Active alarms 计数、一张图片、Temperature and humidity 图表，以及列出 battery、humidity、pressure 和 temperature 的 Latest values 表。" />

顶部的按钮：

- **Preview**（预览）用生成的数据替换实时数据（参见[用合成数据预览](#previewing-with-synthetic-data)）。
- **Export**（导出）复制或下载仪表板（参见[导出](#exporting)）。
- **History**（历史）打开版本历史（参见[版本](#versions)）。
- **Edit**（编辑）切换到编辑器。
- **Delete** 删除仪表板。

带有操作的组件（例如确认告警或发送命令）在这里同样可用。平台会根据你自己的权限检查每个操作。

## 在画布上编辑 {#editing-on-the-canvas}

**Edit** 会把仪表板变成画布。按钮变为 **Add widget**（添加组件）、**Preview**、**Export**、**History**、**Save**（保存）和 **Done**（完成），同时出现一个布局设置栏。

<GuideShot id="dashboard-edit" alt="编辑模式下的 Widget Lab 仪表板。页眉显示 Add widget、Preview、Export、History、不可用的 Save 和 Done。设置栏显示 Title Widget Lab、Columns 12、Min cell 56、Gutter 8、Padding 0 和 Size Fill area。组件在画布上带有轮廓，有些显示短横线或 No data yet。" />

- **Title**（标题）是仪表板的名称。
- **Columns**（列数）、**Min cell**（最小单元格）、**Gutter**（间隔）和 **Padding**（边距）设置组件吸附的网格。单元格是正方形，会拉伸以填满宽度；**Min cell** 是单元格允许的最小尺寸（像素）。**Gutter** 是单元格之间的间距，**Padding** 是仪表板四周的边距。
- **Size**（尺寸）决定仪表板如何填充页面：**Fill area**（填充区域）、**Fixed width**（固定宽度）或 **Fixed height**（固定高度）。

拖动组件可移动，拖动边缘可调整大小。组件从不重叠：移动或调整大小后压到其他组件上时，会把它们向下推开；新组件放在第一个空位。尚未收到数据的组件会显示短横线或 *No data yet*。

由早期版本保存的仪表板在打开时会转换为当前网格。转换是近似的，因此编辑器会显示提示，请你检查布局。保存后，仪表板以当前格式存储，提示随之消失。

**Save** 会把你的更改存入草稿。在没有可保存内容时它是灰色的。**Done** 返回查看视图；未保存的更改会保留，**Save** 在那里仍然可用。如果你有未保存的更改时关闭选项卡或使用 **Dashboards** 返回链接，控制台会在丢弃之前询问。在侧边栏中选择其他页面则不会询问，所以请先保存。保存只会改变草稿。在你发布版本之前，查看者看不到任何新内容。

### 添加组件 {#adding-a-widget}

**Add widget** 会打开组件菜单，按各组件的用途分组。

<GuideShot id="dashboard-add-widget" alt="编辑模式下打开的 Add widget 菜单，分为 Telemetry（Timeseries chart、Latest card、Gauge、Table）、Static（Label、Image）、Alarms（Alarm table、Alarm count）、Control（Command button）、Navigation（Entity selector）和 Location（Map）。每个条目显示其 ID 和版本，例如 timeseries-chart · v1.0.0。" />

| 分组 | 组件 |
|---|---|
| Telemetry（遥测） | Timeseries chart（时序图表）、Latest card（最新值卡片）、Gauge（仪表）、Table（表格） |
| Static（静态） | Label（标签）、Image（图片） |
| Alarms（告警） | Alarm table（告警表）、Alarm count（告警计数） |
| Control（控制） | Command button（命令按钮） |
| Navigation（导航） | Entity selector（实体选择器），让查看者选择仪表板显示哪台设备或哪个实体 |
| Location（位置） | Map（地图），绘制在租户的地图上（参见[租户设置](/user-guide/tenant-settings)） |

选择一个即可将其添加到画布，然后进行配置。

### 配置组件 {#configuring-a-widget}

在画布上选择一个组件，其设置会在右侧打开。**Front** 和 **Delete** 显示在组件本身上。

<GuideShot id="dashboard-widget-config" alt="编辑模式下选中了 Temperature latest card，组件上显示 Front 和 Delete 按钮。右侧标题为 Latest card 的设置面板显示 Title Temperature、Context 部分（Binds to 设为 Scoped to a parent，Parent slot 为 zone，Strategy 为 Manual pick）、Data source 部分（说明它派生自父级上下文，槽位为 sensor）以及 Measurement 输入框。" />

设置因组件而异。常见的有：

- **Title**：显示在组件上的标题。
- **Context**（上下文）：组件绑定到什么。**Root context**（根上下文）把它直接绑定到某台设备或实体。**Scoped to a parent**（限定到父级）使它跟随另一个组件的上下文，例如仪表板上其他位置选中的那栋楼里的某台设备。有父级时，**Strategy**（策略）决定它跟随父级的 **First member**（第一个成员），还是你自己选择的成员（**Manual pick**（手动选择），手动选择）。
- **Data source**（数据源）选择组件读取的设备或实体。对于限定到父级的组件，它会说明该组件跟随父级。
- **Measurement**（测量）以及显示选项，例如单位、范围、精度和保留多少个样本。

组件选项的问题，例如它不读取的选项、留空的必填项或超出范围的值，会列在 **Version history**（版本历史）中。问题修复之前，**Publish version** 保持不可用。

## 用合成数据预览 {#previewing-with-synthetic-data}

**Preview** 会用生成的值替换实时数据，让你在任何设备上报之前就能检查刻度、阈值和布局。按钮旁的选择器用于选择数据的形状：**Sine wave**（正弦波）、**Ramp**（斜坡）或 **Random walk**（随机游走）。预览在查看模式和编辑模式下都可用。再次选择 **Preview** 即可回到实时数据。

<GuideShot id="dashboard-preview" alt="预览模式下的 Widget Lab 仪表板，生成器选择器设为 Sine wave，Preview 按钮高亮。图表显示平滑的正弦曲线，告警计数显示 1 alarm, worst Major，卡片和仪表显示生成的值。" />

生成的数据只保留在你的浏览器中，永远不会被保存。

## 导出 {#exporting}

**Export** 提供四个选项。

<GuideShot id="dashboard-export" alt="Widget Lab 仪表板上打开的 Export 菜单，提供 Download JSON、Copy JSON、Copy as template (unbound) 和 Clone as new dashboard…。" />

- **Download JSON**（下载 JSON）把仪表板保存为文件。
- **Copy JSON**（复制 JSON）把它复制到剪贴板。
- **Copy as template (unbound)**（复制为模板，未绑定）复制时不含其设备和实体绑定，这样导入的人可以选择自己的绑定。
- **Clone as new dashboard…**（克隆为新仪表板）会用此仪表板打开导入审阅，以新令牌创建一份副本。

## 版本 {#versions}

**History** 会打开版本历史。仪表板的草稿是你编辑的内容；**版本**是草稿的冻结副本，展示给查看者。

<GuideShot id="dashboard-history" alt="Version history 面板。Publish current draft 部分有可选的 Label 输入框、Description 输入框、说明发布会使新版本成为查看者所见版本的提示，以及 Publish version 按钮。下方的 Published versions 列出版本 1，标记为 Live，由 dana.okafor@acme-properties.example 发布，带有 Roll back 按钮。" />

- **Publish current draft**（发布当前草稿）。可以添加可选的标签（例如发布名称）和描述，然后选择 **Publish version**（发布版本）。新版本立即生效。请先保存更改：控制台发布的是已保存的草稿。
- **Published versions**（已发布版本）列出每个版本以及发布的时间和发布者。生效的版本标有 **Live**（生效中）。
- **Make live**（设为生效）会把较早的版本提供给查看者，而不影响你的草稿。
- **Roll back**（回滚）会用较早的版本替换草稿。草稿中尚未发布的内容都会丢失，控制台会请你确认。

历史只增不减。发布、设为生效和回滚都会保留较早的版本。

## 相关内容 {#related}

- [仪表板](/concepts/dashboards)：组件、数据源、版本管理与嵌入
- [底图](/concepts/basemaps)，用于地图组件的瓦片
