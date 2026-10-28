---
title: 地理围栏
sidebar_position: 10
---

import GuideShot from '@site/src/components/GuideShot';

# 地理围栏 {#geofences}

**地理围栏**是在地图上绘制的边界。检测规则会用设备上报的位置与它比对，例如当追踪器离开站点时触发告警。本章介绍如何在控制台中绘制和编辑围栏，以及如何查看围栏在过去的样子。本章面向维护围栏的租户用户和管理员。使用围栏的规则请参阅[在规则中使用围栏](/concepts/geofencing#using-a-fence-in-a-rule)。

地理围栏位于侧边栏的 **Organization → Areas → Geofences**（地理围栏）。

## 围栏列表 {#the-geofence-list}

列表显示每个围栏的令牌、名称、角点数量和创建日期。点击某一行即可打开该围栏。

<GuideShot id="geofences-list" alt="Geofences 列表，只有一个围栏 acme-campus，名称为 Acme campus perimeter，有 4 个角点。右上角有“New geofence”按钮。" />

## 绘制新围栏 {#drawing-a-new-fence}

点击 **New geofence**（新建地理围栏）。抽屉中需要填写：

- **Token**（令牌）：规则用来指代该围栏的标识符，例如在 `geo.inFence("acme-campus")` 中。旁边的按钮会按实例的令牌模式生成一个。围栏创建后令牌不可更改。
- **Name**（名称）和 **Description**（描述）：自由文本，可随时修改。
- **Boundary**（边界）：在地图上绘制的形状。

<GuideShot id="geofences-new" alt="New geofence 抽屉：Token、Name 和 Description 字段位于覆盖欧洲和非洲的 Boundary 地图上方。地图下方的计数器显示 0 of 511 corners，旁边有 Undo last point 和 Clear all points 按钮，以及一个折叠的 Basemap 区域。" />

绘制边界的步骤：

1. 缩放并平移地图到你的站点。
2. 点击地图放置每个角点。
3. 可选：再次点击第一个角点以闭合形状，这样之后在地图上点击就不会再添加角点。
4. 点击 **Create geofence**（创建地理围栏）。

绘制时，地图下方的计数器显示你已放置的角点数和允许的最大数量。**Undo last point**（撤消上一个角点）移除最近的角点，**Clear all points**（清除所有角点）则重新开始。

控制台会在你绘制时检查形状。边界至少需要三个角点，且任意两个角点不能位于同一位置；违反其中任一条的形状，控制台不会保存。如果边相互交叉，它也会警告你，因为边交叉的形状没有明确的内部。该检查只是近似判断，因此不会阻止你保存，最终以服务器的判定为准。详情请参阅[边界规则](/concepts/geofencing#what-a-boundary-may-be)。

### 围栏背后的地图 {#the-map-behind-the-fence}

地图显示租户的底图，由租户管理员在 **Settings → Map**（设置 → 地图）中统一设置。地图下方的 **Basemap**（底图）区域允许你试用其他瓦片提供商：输入 **Tile URL**（瓦片 URL）和对应的 **Attribution**（署名）。该覆盖设置只保存在你的浏览器中，不会与其他用户共享，也不会存储在服务器上。瓦片 URL 与署名必须成对出现：只有瓦片 URL 而没有署名时会被忽略，转而显示租户的底图。

如果任何地方都没有配置瓦片源，地图会回退到内置的世界地图，只含大陆和国界。绘制仍然可用，你放置的坐标也是精确的。参见[底图](/concepts/basemaps)。

## 编辑围栏 {#editing-a-fence}

打开围栏即可看到它的详情页。**Basic**（基本信息）选项卡显示令牌（只读）、名称、描述以及地图上的边界。

<GuideShot id="geofence-detail" alt="Acme campus perimeter 围栏的详情页，位于 Basic 选项卡。只读的 Token、Name 和 Description 位于芝加哥市中心街道地图上方，地图上绘有围栏轮廓、其上方两个角点以及边上的控制柄。" />

在地图上你可以：

- 拖动角点以移动它，
- 按住 Alt 点击角点以移除它，或
- 点击边中间的小控制柄，在该处添加角点。

点击 **Save changes**（保存更改）应用修改。右上角的 **Delete**（删除）在你确认后删除围栏，且删除无法撤销。仍引用已删除围栏令牌的规则将无法再对其求值，并会报告求值错误；参见[未知或缺失的围栏](/concepts/geofencing#unknown-fence)。

如果围栏使用了此编辑器无法绘制的形状，控制台会提示。该围栏会完整保存并继续生效，只是无法在这里编辑。请保持原样，不要重新创建。

## 历史 {#history}

每当围栏集合发生变化（创建围栏、编辑其边界或删除围栏），平台就会把所有围栏的形状冻结为一个新的**围栏集合版本**。重命名围栏或编辑描述不会产生新版本，因为形状没有变。

位置事件携带它们到达时生效的版本。因此，当你对过去的事件重放某条规则时，这些事件会按当时的围栏来判定。

**History**（历史）选项卡可显示此围栏在任一版本下的样子。使用 **Older**（更早）和 **Newer**（更新），或直接输入版本号。查看最新版本时会显示 **Current**（当前）。所选版本下存储的形状以实线绘制，围栏当前的形状则以虚线绘制在其后，便于比较。

<GuideShot id="geofence-history" alt="Acme campus perimeter 围栏的 History 选项卡。Fence set version 控件显示 1，带有 Older 和 Newer 按钮以及 Current 标签。方形边界绘制在内置世界地图上，版本 1 为实线，当前形状为虚线，下方有图例。" />

History 地图始终使用不带瓦片的内置世界地图，因此只显示形状。

如果围栏在你选择的版本上不存在，选项卡会说明：它是后来才创建的，或者带有该令牌的围栏曾被删除，令牌随后被重用。

## 权限 {#permissions}

读取围栏及其历史需要 `device:read`，删除围栏需要 `device:write`。创建或修改围栏还需要 `location:read`，因为绘制围栏会查询设备所在的位置。参见[权限](/concepts/geofencing#permissions)。

## 相关内容 {#related}

- [地理围栏](/concepts/geofencing)：边界规则、球面包含判断、围栏集合版本，以及在规则中使用围栏。
- [底图](/concepts/basemaps)：地图瓦片的来源。
- [资产、客户与区域](/user-guide/assets-customers-areas)
