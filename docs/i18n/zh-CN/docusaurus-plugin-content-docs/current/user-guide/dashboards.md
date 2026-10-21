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

<GuideShot id="dashboard-view" alt="查看模式下的 Widget Lab 仪表板。页眉有时间范围 15m、1h（已选中）、6h、24h 和 7d，一个 Pause 按钮，一个 Live 指示器，以及 Preview、Export、History、Edit 和 Delete 按钮。组件包括标题标签、wl-sensor-01 温度的最新值卡片和仪表、wl-zone-01 的 Active alarms 计数、一张图片、标有 15m 的 Temperature and humidity 图表，以及列出 battery、humidity、pressure 和 temperature 的 Latest values 表。" />

顶部：

- **15m**、**1h**、**6h**、**24h** 和 **7d** 设置图表绘制的时间范围，**Pause**（暂停）冻结仪表板。它们与 /dash 查看器中的用法相同，参见[时间范围与暂停](/user-guide/viewing-dashboards-in-dash#time-range-and-pause)。
- **Live**（实时）表示仪表板的实时连接正常；设备只是暂时没有上报数据并不会改变它。连接中断时它显示 **Reconnecting**（正在重新连接），中断 20 秒后显示 **Offline**（离线），如果浏览器本身断网则立即显示 **Offline**。连接恢复后，组件会自行继续更新。
- **Preview**（预览）用生成的数据替换实时数据（参见[用合成数据预览](#previewing-with-synthetic-data)）。
- **Export**（导出）复制或下载仪表板（参见[导出](#exporting)）。
- **History**（历史）打开版本历史（参见[版本](#versions)）。
- **Edit**（编辑）切换到编辑器。
- **Delete** 删除仪表板。

带有操作的组件（例如确认告警或发送命令）在这里同样可用。平台会根据你自己的权限检查每个操作。

### 组件的样子 {#what-the-widgets-look-like}

测试租户的 *Widget Lab* 仪表板包含每种内置组件各一个。页面再往下：

<GuideShot id="dashboard-gauges" alt="wl-sensor-01 上的指示类组件：温度的线性仪表和温度计，刻度上标出了各自的阈值区间；按湿度百分比填充的 Tank level 液位仪表；显示电量的 Battery 图标；显示 Normal 的状态灯；信号格；显示 Off 的 Powered 灯；下方还有一个罗盘和两个进度环。" />

线性仪表、液位仪表、电池、状态灯、信号格、罗盘和进度环各显示一台设备的一个测量值。阈值会在刻度上标出各自的区间，如上图中的线性仪表和温度计；读数落在正常区间以外的任何区间时，会显示该区间的颜色。

<GuideShot id="dashboard-stat-cards" alt="wl-sensor-01 的八张统计卡片：Average temperature、Peak temperature（红色）、Lowest temperature 和 Readings，各自标有统计量和 1h；然后是 Humidity total、显示读数（标为 Latest）及其到达时间的 Latest temperature、标有 Average · 5m 的 Average, last 5 min，以及 Peak (smallest)。" />

统计卡片显示仪表板时间范围内的一个数值，并标出统计量及其覆盖的跨度，例如 *Maximum · 1h*。设置了自己 **Time span** 的卡片会保留该跨度，如上图中的 *Average, last 5 min*。开启 **Show trend**（显示趋势）后，卡片会在数值下方绘制该测量在同一跨度内的趋势，让“是否在上升”直接在卡片上得到回答。这条线始终是中性色，无论数值处于哪个区间；卡片小到无法保持数值清晰可读时，会省略这条线。

<GuideShot id="dashboard-bar-pie" alt="两个柱状图，显示 wl-sensor-01 最新的温度、湿度和电量读数：Latest readings 按数据源顺序绘制竖向柱，Largest first 按从大到小绘制横向柱。下方是同样读数的两个饼图。" />

柱状图和饼图用于比较一台设备的最新读数。柱状图也可以改为比较一个设备组中各设备的同一测量值（参见下文的 **One bar per**）。柱状图在较宽的组件中绘制竖向柱，在较高的组件中绘制横向条。

<GuideShot id="dashboard-fleet-table" alt="wl-zone-01 的 Sensors in the zone 车队表格：每台设备一行（wl-sensor-01 和 wl-sensor-03），列为按其排序的 Temperature (C)、Humidity (%) 和 Battery (%)，每个值都带有其时长。下方是一个较小的车队表格和一个 Temperature by sensor 柱状图。" />

车队表格为设备组中的每台设备显示一行，为每个测量显示一列。点击列标题即可按该列排序。其设置见[配置组件](#configuring-a-widget)。

车队计数卡片在该表格之上给出一个数字：设备组中有多少台设备需要关注（*3 / 21*），或者设备组共有多少台设备。其设置见[配置组件](#configuring-a-widget)。

## 在画布上编辑 {#editing-on-the-canvas}

**Edit** 会把仪表板变成画布。按钮变为 **Undo**（撤销）、**Redo**（重做）、**Add widget**（添加组件）、**Paste**（粘贴）、**Preview**、**Export**、**History**、**Save**（保存）和 **Done**（完成），同时出现一个布局设置栏。

<GuideShot id="dashboard-edit" alt="编辑模式下的 Widget Lab 仪表板。页眉显示 Live 指示器、Undo 和 Redo、Add widget、Paste、Preview、Export、History、不可用的 Save 和 Done。设置栏显示 Title Widget Lab、Columns 12、Min cell 56、Gutter 8、Padding 0、Size Fill area、Time range 1h 和 Density Comfortable。组件在画布上带有轮廓，各自显示最新读数。" />

- **Title**（标题）是仪表板的名称。
- **Columns**（列数）、**Min cell**（最小单元格）、**Gutter**（间隔）和 **Padding**（边距）设置组件吸附的网格。单元格是正方形，会拉伸以填满宽度；**Min cell** 是单元格允许的最小尺寸（像素）。**Gutter** 是单元格之间的间距，**Padding** 是仪表板四周的边距。
- **Size**（尺寸）决定仪表板如何填充页面：**Fill area**（填充区域）、**Fixed width**（固定宽度）或 **Fixed height**（固定高度）。
- **Time range**（时间范围）是仪表板打开时其图表绘制的跨度：15m、1h、6h、24h 或 7d，默认一小时。查看者可以在页眉中选择其他范围并暂停看板，参见[在 /dash 中查看仪表板](/user-guide/viewing-dashboards-in-dash#time-range-and-pause)。
- **Density**（密度）缩放所有组件的文字和间距：**Compact**（紧凑）、**Comfortable**（舒适，默认）或 **Wall display**（墙面显示，适合从房间另一端观看的屏幕）。/dash 查看器可以为某一块屏幕覆盖此设置。

拖动组件可移动。虚线轮廓会显示松开后它将占据的单元格，组件会正好落在那里。挡路的组件会移到上方或下方最近的空位，因此把一个组件拖到另一个组件上会交换两者的位置。其他组件都不会移动：你特意留出的空位会保留。网格上的组件从不重叠，新组件放在能容纳它的第一个空位。选中组件后，拖动其边缘或角上的手柄可调整大小。拖动时尺寸以“列×行”显示，例如 *2×3*，组件会停在它允许的最小和最大尺寸。只能朝一个方向调整大小的组件，只在该侧显示手柄。拖动时按 **Esc** 可取消。组件获得焦点后，方向键可将其移动一格，**Shift** 加方向键可调整其大小。超出所支持尺寸或越过右边缘的组件，会带有相应的提示。尚未收到数据的组件会显示短横线或 *No data yet*。

仪表板测量的是分配给它的空间，而不是屏幕。只要各列在 **Min cell** 或更大的单元格下放得下，就按你排好的布局显示，单元格会拉伸以填满宽度，因此大屏幕得到的是更大的单元格，而不是更多的单元格。空间更窄时，它会使用放得下的列数，并按阅读顺序（自上而下、从左到右）把组件依次排入其中。宽度小于 600 px 时（例如手机），每个组件独占一行并占满宽度，仪表板呈现为单列。每个组件的大小保持在它允许的范围内，相对于其他组件放置的组件会随之移动。

由早期版本保存的仪表板在打开时会转换为当前网格。转换是近似的，因此编辑器会显示提示，请你检查布局。保存后，仪表板以当前格式存储，提示随之消失。

由早期版本保存的时序图表保留的是样本数量，而不是时间跨度。不知道设备多久上报一次，样本数量就无法换算成跨度，所以图表不会被转换成猜测出来的跨度，而是跟随仪表板的时间范围，编辑器会列出受影响的图表。对需要不同跨度的图表设置 **Time span** 后保存即可。

**Save** 会把你的更改存入草稿。在没有可保存内容时它是灰色的。**Done** 返回查看视图；未保存的更改会保留，**Save** 在那里仍然可用。如果你有未保存的更改时关闭选项卡或使用 **Dashboards** 返回链接，控制台会在丢弃之前询问。在侧边栏中选择其他页面则不会询问，所以请先保存。保存只会改变草稿。在你发布版本之前，查看者看不到任何新内容。

### 添加组件 {#adding-a-widget}

**Add widget** 会打开组件菜单，按各组件的用途分组。

<GuideShot id="dashboard-add-widget" alt="编辑模式下打开的 Add widget 菜单，显示其 Telemetry 分组：Timeseries chart 6×4、Latest card 3×2、Gauge 3×3、Table 6×3、Linear gauge 4×3、Level gauge 3×4、Battery 3×2、Status light 2×2、Signal bars 2×2、Compass 4×4、Progress ring 3×3、Stat card 3×3、Bar chart 6×4、Pie chart 4×4 和 Fleet table 12×6。每个条目显示其 ID 和版本（例如 timeseries-chart · v1.0.0）以及以单元格计的尺寸。" />

| 分组 | 组件 |
|---|---|
| Telemetry（遥测） | Timeseries chart（时序图表）、Latest card（最新值卡片）、Gauge（仪表）、Table（表格）、Linear gauge（线性仪表）、Level gauge（液位仪表）、Battery（电池）、Status light（状态灯）、Signal bars（信号格）、Compass（罗盘）、Progress ring（进度环）、Stat card（统计卡片）、Bar chart（柱状图）、Pie chart（饼图）、Fleet table（车队表格）、Fleet count（车队计数） |
| Static（静态） | Label（标签）、Image（图片） |
| Alarms（告警） | Alarm table（告警表）、Alarm count（告警计数） |
| Control（控制） | Command button（命令按钮）、Command slider（命令滑块）、Command toggle（命令开关） |
| Navigation（导航） | Entity selector（实体选择器），让查看者选择仪表板显示哪台设备或哪个实体 |
| Location（位置） | Map（地图），绘制在租户的地图上（参见[租户设置](/user-guide/tenant-settings)） |

每一项都会显示该组件添加时的尺寸（以单元格计），例如 *4×3*。滚动菜单可看到 **Telemetry** 之后的分组。选择一个组件即可将其添加到画布上的第一个空位，然后进行配置。

### 配置组件 {#configuring-a-widget}

在画布上选择一个组件，其设置会在右侧打开。**Grid**、**Relative to…**、**Duplicate**（复制一份）、**Copy**（复制）和 **Delete** 显示在组件正上方的工具栏中（上方没有空间时显示在下方），其边缘和角上的手柄用于调整大小。

<GuideShot id="dashboard-widget-config" alt="编辑模式下选中了 Temperature latest card，组件正上方的工具栏中显示 Grid、Relative to…、Duplicate、Copy 和 Delete 按钮，组件的边缘和角上有调整大小的手柄。右侧标题为 Latest card 的设置面板显示 Title Temperature、Context 部分（Binds to 设为 Scoped to a parent，Parent slot 为 zone，Strategy 为 Manual pick），以及 Data source 部分的开头。" />

设置因组件而异。常见的有：

- **Title**：显示在组件上的标题。
- **Context**（上下文）：组件绑定到什么。**Root context**（根上下文）把它直接绑定到某台设备或实体。**Scoped to a parent**（限定到父级）使它跟随另一个组件的上下文，例如仪表板上其他位置选中的那栋楼里的某台设备。有父级时，**Strategy**（策略）决定它跟随父级的 **First member**（第一个成员），还是你自己选择的成员（**Manual pick**，手动选择）。
- **Data source**（数据源）选择组件读取的设备或实体。对于限定到父级的组件，它会说明该组件跟随父级。只有车队表格、车队计数和设置为每台设备一根柱的柱状图可以读取设备组；其他遥测组件只显示一台设备，绑定到设备组时会明确说明这一点，而不会混合多台设备的读数。
- **Measurement**（测量）以及显示选项，例如单位、范围和精度。
- **Time span**（时间跨度，时序图表）：图表绘制多少历史数据，单位为秒，范围 60 到 604800。留空时图表跟随看板的时间范围，查看者可以更改；填写后，无论查看者选择什么范围，图表都保持该跨度。
- **Statistic**（统计量，统计卡片）：显示跨度内的哪个数值：平均值（默认）、最小值、最大值、总和、读数个数，或最新读数。卡片会在数值下方写出统计量及其覆盖的跨度，例如“Maximum · 1h”。同一个 **Time span** 设置可以把卡片固定在自己的跨度上。卡片无法得出数值时（数据源无法计算聚合、读取失败，或跨度内没有读数），会说明原因，而不会显示一个它没能算出的数字。没有读数的跨度，其读数个数为 0。
- **Show trend**（显示趋势，统计卡片）：默认关闭。开启后，卡片会在数值下方用一条细的中性色线绘制该测量在其跨度内的趋势，跨度图表的每个分桶对应一个点，所绘统计量即卡片显示的统计量（最新读数卡片绘制各分桶的平均值）。读数中的空缺在线上也是空缺，单个点绘制为一个圆点，没有读数的跨度则什么也不绘制。无论是否开启，数值都是同一个数字。
- **Order**（顺序，柱状图、饼图）：测量按数据源列出的顺序排列，或按从小到大、从大到小排序。尚未上报的测量排在最后。柱状图在宽组件中绘制纵向柱，在高组件中绘制横向条。
- **One bar per**（每根柱代表，柱状图）：*Measurement*（测量，默认）为一台设备的每个测量绘制一根柱；*Device*（设备）为组件所绑定设备组中的每台设备绘制一根柱，针对你选择的那一个 **Measurement**，因此数据源必须是设备组而不是单台设备。过大的设备组会按所选顺序截取前若干根柱，图表会注明“Showing N of M devices”。
- **Fleet table**（车队表格）：组件所绑定设备组中每台设备一行，数据源所选的每个测量一列。请在组件的数据源上设置设备组，并选择要显示的测量。**Columns** 为每个测量设置标题、单位和精度；**Thresholds** 为某个测量一列的单元格着色（正常区间保持表格原有颜色）；**Stale after** 把早于该秒数的读数标记为过期，不设置时表格只显示每个读数的时效。**Show age when behind**（落后时显示时效）只在某个读数比表格中最新的读数早至少该秒数（或已过期）时才显示其时效，这样每隔几秒上报一次的设备群每行只占一行，停止上报的设备会突显出来；组件底部仍显示最新读数的时效。点击列标题（或用键盘按下）即可排序；**Order by** 与 **Largest first** 设置初始顺序，排序列中没有数值的设备始终排在最后。**Devices per page** 为较长的设备群分页。**Target slot** 让设备名称成为链接，在绑定到该槽位的组件中选中该设备。看板较窄时，表格会变成每台设备一张卡片。没有数值的单元格会说明原因：*no data in range*（范围内无数据）、*does not report this*（该设备上报其他测量但从不上报这一项）或 *could not be read*（无法读取）。表格读取设备组的前 500 台设备，设备组更大时会注明“Showing 500 of N devices”。它遵循看板的时间范围，看板暂停时表格不再变动。

- **Fleet count**（车队计数）：为设备组给出一个数字，其绑定方式与车队表格相同。**Count**（计数）选择计什么。*Devices needing attention*（需要关注的设备，默认）统计 **Measurement** 的最新读数处于卡片 **Thresholds** 某个异常区间的设备数，显示为 *3 / 21*；当所有被标出的设备都处于同一个带标签的区间时，以该区间的标签命名（标签为 *Low* 的区间显示为“Low”），否则显示“need attention”。数字采用被标出设备中最差区间的颜色；没有设备被标出，或整个设备群都在正常区间时，保持普通文字颜色，因此健康的设备群显得平静。没有该测量读数的设备既不算健康也不算被标出，而是单独计数（“2 not reporting”）。*Devices*（设备）显示设备组共有多少台设备，无论它们上报什么。**Stale after** 与 **Time span** 的作用与车队表格相同。设备组超过所读取的 500 台设备时，会注明“Showing 500 of N devices”，关注计数涵盖所显示的设备。

组件选项的问题，例如它不读取的选项、留空的必填项或超出范围的值，会列在 **Version history**（版本历史）中。问题修复之前，**Publish version** 保持不可用。

放置方面的问题同样会让 **Publish version** 保持不可用：组件超出所支持的尺寸、越过右边缘，或因相对于其他组件放置而落到仪表板之外。编辑器自己不会产生这些问题；它们来自导入的仪表板、较旧的组件版本或通过 API 写入的仪表板。**Fit to limits**（适应限制）会把每个组件限制在它支持的尺寸内，并把落到仪表板之外的组件移回仪表板上；被变大的组件覆盖的组件会下移，其余组件保持原位；只要仪表板存在此类问题，它就会出现在编辑器标题栏和 **Version history** 中。检查结果后再保存。

### 相对于其他组件放置组件

组件通常位于网格上。也可以把组件设为 **Relative to…**（相对于）另一个组件，例如地图上的角标或图表下方的说明文字。选中该组件，选择 **Relative to…**，再点击它要依附的组件，或将焦点移到该组件上并按 **Enter**。随后会出现带九个锚点的选择器；选择它要连接到宿主的哪个角、边或中心。

相对组件保持自己的尺寸，宿主移动时随之移动，可以覆盖宿主，并绘制在网格之上。一条虚线把它与宿主连起来。拖动它可按整格偏离锚点，按住 **Shift** 则按四分之一格移动。选择 **Grid** 可把它放回网格上的第一个空位。相对组件本身不能作为宿主。

### 撤销、复制与粘贴 {#undo-duplicate-and-paste}

编辑器按钮最前面的 **Undo**（撤销）和 **Redo**（重做）可以在你的编辑之间后退和前进：移动、调整大小、添加和删除组件，组件的设置和数据源，布局设置，以及 **Fit to limits**。**Ctrl+Z**（Mac 上为 **Cmd+Z**）撤销，**Ctrl+Shift+Z** 或 **Ctrl+Y**（**Cmd+Shift+Z**）重做。一次拖动或调整大小无论指针移动多远都算一步；在同一个字段中输入，直到离开该字段都算一步。编辑器保留最近 100 步。在字段中输入时，这些按键撤销和重做的是该字段中的输入。屏幕阅读器会读出每一步撤销了什么，例如 “Undid move of Tank level”。

撤销只会回到仪表板真实存在过的状态：它可以恢复一个被 **Fit to limits** 修复的放置问题，但绝不会制造仪表板原本没有的问题。离开仪表板或恢复某个版本之前，这些步骤一直保留。保存和发布不会清除它们，因此你可以撤销刚刚保存的编辑；之后仪表板会再次有未保存的更改。

选中组件上的 **Duplicate**（复制一份），或 **Ctrl+D**（**Cmd+D**），会在第一个放得下的空位添加一个尺寸、设置和数据源都相同的副本。

选中组件上的 **Copy**（复制），或 **Ctrl+C**（**Cmd+C**），会复制该组件。编辑器标题栏中的 **Paste**（粘贴）会把在当前浏览器标签页中最后复制的组件添加到正在编辑的仪表板的第一个空位，因此可以在一个仪表板上复制、在另一个仪表板上粘贴。**Ctrl+V**（**Cmd+V**）从剪贴板粘贴，也可以粘贴到在另一个标签页中打开的仪表板。此控制台没有的组件类型不会被粘贴，控制台会指出是哪一种。在更新版本的 DeviceChain 中复制的组件同样不会被粘贴：控制台会说明原因，升级后即可粘贴。

在同一个仪表板上，副本保留其数据源。粘贴到另一个仪表板时，只有当该仪表板有一个同名并绑定到同一设备或实体的槽位时才保留：槽位按仪表板编号，所以两个仪表板上的同名槽位通常指向两台不同的设备。否则组件在粘贴后没有数据源，编辑器会说明这一点，指出组件和它原先绑定的槽位，直到你在组件设置中选择数据源。在另一个租户中复制的组件无论粘贴到哪个仪表板都不会保留数据源：设备属于各自的租户，因此粘贴后它没有数据源，编辑器会说明原因。相对于其他组件放置的组件会以自身尺寸复制到网格上；选择 **Relative to…** 可以重新依附。

## 用合成数据预览 {#previewing-with-synthetic-data}

**Preview** 会用生成的值替换实时数据，让你在任何设备上报之前就能检查刻度、阈值和布局。按钮旁的选择器用于选择数据的形状：**Sine wave**（正弦波）、**Ramp**（斜坡）或 **Random walk**（随机游走）。预览在查看模式和编辑模式下都可用。再次选择 **Preview** 即可回到实时数据。

<GuideShot id="dashboard-preview" alt="预览模式下的 Widget Lab 仪表板，生成器选择器设为 Sine wave，Preview 按钮高亮。图表显示平滑的正弦曲线，告警计数显示 1 alarm, worst Major，卡片和仪表显示生成的值，表格只列出一个生成的值。" />

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

<GuideShot id="dashboard-history" alt="Version history 面板。Publish current draft 部分有可选的 Label 输入框、Description 输入框、说明发布会使新版本成为查看者所见版本的提示，以及 Publish version 按钮。下方的 Published versions 列出标记为 Live 的版本 2 和版本 1，均由 sam.rivera@widgetlab.example 发布。版本 1 带有 Make live 按钮，每个版本都有 Roll back 按钮。" />

- **Publish current draft**（发布当前草稿）。可以添加可选的标签（例如发布名称）和描述，然后选择 **Publish version**（发布版本）。新版本立即生效。请先保存更改：控制台发布的是已保存的草稿。
- **Published versions**（已发布版本）列出每个版本以及发布的时间和发布者。生效的版本标有 **Live**（生效中）。
- **Make live**（设为生效）会把较早的版本提供给查看者，而不影响你的草稿。
- **Roll back**（回滚）会用较早的版本替换草稿。草稿中尚未发布的内容都会丢失，控制台会请你确认。

历史只增不减。发布、设为生效和回滚都会保留较早的版本。

## 与客户和组共享 {#sharing-with-customers}

访问仅限部分客户或设备组的成员，只有在仪表板共享给其中某个客户或组并且已发布之后才能看到它。在仪表板列表中，对某个仪表板选择 **Share**（共享），选择 **Customers**（客户）和 **Groups of devices**（设备组），然后选择 **Save sharing**（保存共享）。你保存的列表会替换之前的列表，空列表表示不与任何人共享该仪表板。你可以在仪表板发布之前共享它；发布后，共享对象中的成员即可看到。

只会列出用作访问范围的组。如果缺少某个组，请在该组的页面上把它启用为访问范围，见[将成员限定于设备组](/user-guide/members-roles-and-customer-users#group-scopes)。不再是访问范围的组会保留已有的共享，但在它重新成为访问范围之前无法再次保存：保存时会被拒绝，并提示某个所选组已不再是访问范围。

{/* TODO(screenshot): dashboard-share, the Share drawer with customers and groups picked. */}

请把共享的仪表板的组件绑定到设备，或绑定到仪表板所共享的客户。绑定到客户并把 **Relationship**（关系）设为 `assigned`（设备的 **Assignment** 选项卡所创建的关系）的组件，会向每个客户用户显示该客户的设备，前提是该客户是他们自己的客户；绑定到其他任何客户时，组件会向他们显示 **You do not have access to this data**（你无权访问这些数据）。绑定到资产、区域或组的组件需要通过关系查找其设备，而访问受限的成员无法读取关系，因此该组件会向他们显示 **Not available for customer users**（不对客户用户提供），而不是其数据。在共享给某个组的仪表板上，请把组件绑定到该组的设备。

程序可以通过需要 `dashboard:write` 的 `shareDashboard` 变更完成同样的操作：

```graphql
mutation {
  shareDashboard(
    token: "plant-overview"
    anchors: [{ kind: CUSTOMER, token: "acme" }, { kind: GROUP, token: "north-wing" }]
  ) {
    token
    sharedWith { kind token }
  }
}
```

- 你发送的列表会替换之前的列表。空列表表示不与任何人共享。
- 每个客户都必须存在，每个组都必须存在且是访问范围。若有一个不满足，请求以 `NOT_FOUND` 失败，且不做任何更改。存在但不是访问范围的组，与不存在的组得到完全相同的回答。
- 这样的成员只能在列表中看到共享给其客户或组的**已发布**仪表盘，并打开其已发布版本。它永远看不到草稿或版本历史。其他任何仪表盘都按不存在作答。
- 共享仪表盘并不共享数据。每个小部件仍只显示该成员可以查看的内容。
- 访问受限的成员不能共享仪表盘，也不能查看仪表盘共享给了谁。
- 删除仪表盘会删除其共享。删除客户或组会删除指向它的共享。

## 相关内容 {#related}

- [仪表板](/concepts/dashboards)：组件、数据源、版本管理与嵌入
- [底图](/concepts/basemaps)，用于地图组件的瓦片
