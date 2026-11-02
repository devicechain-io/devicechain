---
title: 分组、分面与浏览
sidebar_position: 11
---

import GuideShot from '@site/src/components/GuideShot';

# 分组、分面与浏览 {#groups-facets-and-browse}

随着设备群增长，你需要办法挑出自己关心的设备（或资产、区域、客户）：屋顶上的机组、三楼的传感器。DeviceChain 为此提供三种工具，本章逐一介绍：

- **分组**（Groups）：同一类别的具名集合，可以是你手工维护的列表，也可以是已保存的筛选条件。
- **分面**（Facets）：分类键，例如 `zone` 或 `floor`，只需声明一次，然后在每个实体上设置值。
- **浏览**（Browse）：按分面筛选某一类别、实时显示匹配结果的界面，并可把筛选条件保存为**动态分组**，其成员会自动保持最新。

本章面向组织设备群的租户用户和管理员。其背后的模型请参阅[分面与动态分组](/concepts/domain-model#facets-and-dynamic-groups)。

## 分组 {#groups}

每个类别都有自己的分组界面：**Devices → Device Groups**（设备分组）、**Organization → Assets → Asset Groups**（资产分组）、**Organization → Customers → Customer Groups**（客户分组）和 **Organization → Areas → Area Groups**（区域分组）。一个分组只包含同一类别的成员。列表显示每个分组的令牌、名称、描述和创建日期。

<GuideShot id="device-groups-list" alt="Devices 菜单下的 Device Groups 列表，只有一个分组 rooftop-units，名称为 Rooftop units，描述为屋顶上的所有温控器。右上角有“New device group”按钮。" />

分组有两种：

- **静态**分组保存明确的成员列表。使用 **New device group**（新建设备分组，其他类别为对应的同名按钮）创建，然后打开它，在 **Members**（成员）选项卡中添加和移除成员。
- **动态**分组保存的是筛选条件而不是列表。其成员在每次读取时重新计算，因此始终反映实体当前的分面值。你在 [Browse](#browse) 界面创建它。动态分组也会出现在该类别的分组列表中，上面的 `rooftop-units` 就是一例。要查看动态分组当前的成员，请使用 Browse 界面 **Saved dynamic groups**（已保存的动态分组）下的 **Members** 按钮，它会根据筛选条件计算成员。分组自身页面上的 **Members** 选项卡管理的是手工维护的列表，因此不会显示筛选条件所匹配的成员。

## 分面 {#facets}

**分面**是你为某一类别声明的分类轴属性键，例如设备的 `zone`。声明分面有两个作用：使该键可作为 Browse 界面上的筛选条件，并在每个实体的 **Facets**（分面）选项卡上为该键提供一个取值字段。

值本身保存在实体上，而不在此界面。已声明但没有任何实体设置值的分面不会匹配任何内容。

打开侧边栏中的 **Organization → Facets**。顶部的标签按成员类别过滤列表：**All**（全部）、**Device**（设备）、**Asset**（资产）、**Area**（区域）或 **Customer**（客户）。每一行显示成员类别、键、值类型、建议值和标签。

<GuideShot id="facets-list" alt="Facets 界面，过滤条件为 All。两个设备分面：zone，STRING 类型，建议值为 lobby、rooftop、offices、amenities、technical 和 circulation，标签为 Zone；floor，LONG 类型，没有值列表，标签为 Floor。每行都有 Delete 操作。" />

### 声明分面 {#declaring-a-facet}

声明或删除分面以及保存动态分组需要 `device:write` 权限；没有该权限时这些按钮会被隐藏。

点击 **New facet**（新建分面）。抽屉中需要填写：

- **Member family**（成员类别）：Device、Asset、Area 或 Customer。
- **Attribute key**（属性键）：值存储所用的键，例如 `climate`。
- **Value type**（值类型）：`STRING`、`LONG`、`DOUBLE`、`BOOLEAN` 或 `JSON`。为分面输入的值会按它校验。
- **Values**（值）：可选。以逗号分隔的建议值列表，输入时会作为提示给出。留空则表示自由格式的值。
- **Label**（标签）：可选。分面显示的名称，默认为键。

<GuideShot id="facets-new" alt="New facet 抽屉，字段包括设置为 Device 的 Member family、占位符为 climate 的 Attribute key、设置为 STRING 的 Value type、占位符为 arid, temperate, tropical 的 Values 以及 Label，下方是“Save facet”按钮。" />

点击 **Save facet**（保存分面）。没有单独的编辑界面：要修改分面的类型、值或标签，请用相同的成员类别和键重新声明，新的声明会替换旧的。

**Delete**（删除）只移除声明。实体上已存储的值保持不变，使用该分面的已保存动态分组也继续有效。

### 设置分面值 {#setting-facet-values}

要为实体设置分面值，请打开该实体并进入其 **Facets** 选项卡。为该类别声明的每个分面都会列出，显示其当前值，或显示 **Not set**（未设置）。输入值并点击 **Save**（保存），或点击 **Clear**（清除）将其移除。如果有动态分组依赖该分面，选项卡会告知这次修改会使该实体进入或离开哪些分组。该选项卡在[资产、客户与区域](/user-guide/assets-customers-areas#facets)中有说明；设备也有相同的选项卡。

## 浏览 {#browse}

**Browse**（浏览）把分面变成筛选条件。用顶部的标签选择成员类别（**Device**、**Asset**、**Area** 或 **Customer**）。**Facet axes**（分面轴）面板列出为该类别声明的每个分面，每个分面带一个运算符：

- **— (ignore)**（忽略）：该分面不参与筛选。这是默认值。
- **is**（等于）和 **is not**（不等于）：匹配某个值。对于带建议值的文本分面，**is** 允许选择多个值，实体匹配其中任意一个即可。
- **has any value**（有任意值）：实体在该分面上有某个值。这是 `JSON` 分面唯一可用的运算符。
- 对于 `LONG` 和 `DOUBLE` 分面，还提供 `<`、`≤`、`>` 和 `≥` 比较。

设置条件时，**Matches**（匹配结果）面板会显示控制台生成的 **Selector**（选择器）、匹配的成员数量以及部分样本。停止输入片刻后，数量会更新。

<GuideShot id="browse-filter" alt="选择了 Device 类别的 Browse 界面。在 Facet axes 下，Zone 设为 is rooftop，Floor 被忽略。Matches 面板显示选择器 attr zone equals rooftop，匹配 2 个成员，列出 bp-therm-002 和 bp-therm-003，并有“Save as group”按钮。下方的 Saved dynamic groups 列出 rooftop-units，带有 dynamic 徽章、其选择器和 Members 按钮。" />

选择器按其实际存储的形式显示。它使用 CEL 编写，与检测规则使用的表达式语言相同。

### 保存动态分组 {#saving-a-dynamic-group}

当筛选条件匹配了你想要的内容，点击 **Save as group**（另存为分组）。抽屉再次显示选择器，并要求填写 **Token**（令牌，旁边的按钮可生成一个）和可选的 **Name**（名称）。点击 **Save group**（保存分组）。

<GuideShot id="browse-save-group" alt="Save as dynamic group 抽屉，显示选择器 attr zone equals offices、带生成按钮的 Token 字段、可选的 Name 字段以及“Save group”按钮。" />

保存时平台会再次检查选择器，并拒绝它无法高效运行的选择器。保存后，该分组会出现在该类别的 **Saved dynamic groups** 下，显示其令牌、名称和选择器。点击 **Members** 可查看当前属于它的成员；每次打开时都会根据选择器重新计算该列表。

## 相关内容 {#related}

- [分面与动态分组](/concepts/domain-model#facets-and-dynamic-groups)
- [资产、客户与区域](/user-guide/assets-customers-areas)
- [向设备群下发命令](/guides/commanding-a-fleet)：向分组中的每台设备发送命令。
