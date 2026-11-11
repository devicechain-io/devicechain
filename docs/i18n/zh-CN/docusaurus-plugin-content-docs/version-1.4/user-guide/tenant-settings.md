---
title: 租户设置
sidebar_position: 14
---

import GuideShot from '@site/src/components/GuideShot';

# 租户设置 {#tenant-settings}

侧边栏中的 **Settings**（设置）组包含三个页面，决定租户内所有人看到的控制台外观和默认打开方式：**Branding**（品牌）、**Map**（地图）和 **Language**（语言）。本章面向租户管理员。

每个页面的模式相同。你留空的字段会继承默认值：实例运维人员设置的默认值；运维人员未设置时则使用内置默认值。每个页面都需要各自的权限。没有该权限时，该页面不会列在侧边栏中；如果通过地址直接打开，它只会显示需要申请哪项权限。

## 品牌 {#branding}

**Branding** 为你的租户对控制台进行白标化：标题、颜色和标志。

<GuideShot id="settings-branding" alt="Branding 页面。字段包括 App title、Primary、Accent、Sidebar background 和 Sidebar foreground 颜色（均显示 inherit）、设置为 28 的 Logo max height (px)，以及 Logo，带有 https URL 输入框、Apply URL 和 Upload 按钮。右侧的 Preview 卡片显示 DeviceChain 标题、一个 Primary 按钮和示例内容。右上角有“Save branding”按钮。" />

各字段：

- **App title**（应用标题）显示在浏览器标签页和控制台中。留空则继承默认值，除非运维人员更改过，默认值为 *DeviceChain*。
- **Primary**（主色）为按钮、链接和焦点环着色。**Accent**（强调色）为次要强调元素着色。
- **Sidebar background**（侧边栏背景）和 **Sidebar foreground**（侧边栏前景）为侧边栏及其上的文字和图标着色。它们不会改变页面本身，页面仍保持其浅色或深色主题。
- **Logo max height (px)**（标志最大高度，像素）设置标志在侧边栏中的高度，范围 16 到 200。留空则继承默认值，除非运维人员更改过，默认值为 28。

颜色以六位十六进制代码输入，例如 `#1f9fb7`，也可以用色板选取。清空字段即恢复为继承。**Preview**（预览）卡片会在你修改时显示标题（设置标志后则显示标志）和一个主按钮。如果某个配色难以阅读，页面会发出警告，但仍允许保存。选择 **Save branding**（保存品牌设置）以应用标题、颜色和标志高度。

**Logo**（标志）单独处理，立即生效，无需 **Save branding**：

- **Upload**（上传）不超过 1 MB 的 PNG、JPEG 或 WebP 文件，或
- 粘贴你托管的图片的 `https://` 地址（此处支持 SVG）并选择 **Apply URL**（应用 URL）。

**Remove logo**（移除标志）将其清除。登录页面始终显示标准的 DeviceChain 品牌，因为在有人登录之前不知道是哪个租户。默认值如何组合，请参阅[白标与品牌](/concepts/white-labeling)。

## 地图 {#map}

**Map** 设置此租户的地理围栏编辑器和仪表板地图组件所用的地图瓦片，以及新建地理围栏时地图打开的位置。页面顶部的栏显示 *Basemap*。

<GuideShot id="settings-basemap" alt="Map 页面。在 Tile source 下有设置为 Custom… 的 Provider 列表、Tile URL template 字段、Attribution 字段，以及一行写着 Inheriting the instance default: https://tile.openstreetmap.org/{z}/{x}/{y}.png。一条提示警告称瓦片 URL 中的 API 密钥不是机密。在 Default view 下是空的 Latitude、Longitude 和 Zoom 字段，下方是“Save basemap”按钮。" />

**Tile source**（瓦片源）：留空则继承实例默认值；页面会显示该默认值是什么。要使用你自己的提供商：

1. 从 **Provider**（提供商）中选择它。控制台会填入 **Tile URL template**（瓦片 URL 模板）以及该提供商的许可所要求的 **Attribution**（署名）。有些提供商还需要 **API key**（API 密钥）。如果提供商不在列表中，请选择 **Custom…**（自定义）并自行填写这两个字段。
2. 检查这两个字段。瓦片 URL 必须以 `https://` 开头，并包含 `{z}`、`{x}` 和 `{y}` 占位符（或 `{bbox-epsg-3857}`、`{quadkey}`）。
3. 选择 **Save basemap**（保存底图）。

瓦片 URL 及其署名要么一起保存，要么都不保存，因为显示提供商的瓦片而不带其署名会违反其许可。

:::warning 瓦片 URL 中的密钥不是机密
浏览器会带着 URL 中的密钥获取瓦片，因此使用此租户的任何人都能读到它。请改用提供商的 HTTP Referer 限制来保护密钥。
:::

**Default view**（默认视图）：**Latitude**（纬度）、**Longitude**（经度）和 **Zoom**（缩放级别）设置你开始绘制新围栏时地理围栏编辑器打开的位置。纬度和经度必须成对设置：要么都设，要么都不设。已有形状的围栏会定位到该形状，而仪表板地图组件始终适配其所显示的内容，因此两者都不使用此视图。

选择提供商和具体规则请参阅[底图](/concepts/basemaps)。

## 语言 {#language}

**Language** 设置控制台为本租户中尚未自行选择语言的成员打开时所用的语言。页面顶部的栏显示 *Locale*。

<GuideShot id="settings-locale" alt="Language 页面。Default language 提供 Inherit（已选中）、English 和 Español。一条说明指出这是默认值，而非强制覆盖，下方是“Save language”按钮。" />

选择 **Inherit**（继承）以沿用运维人员设置的全实例默认值，或选择一种语言，然后选择 **Save language**（保存语言）。

这是默认值，而非强制覆盖。已从用户菜单中选择了语言的人会保留自己的选择。控制台按以下顺序确定语言：

1. 用户自己选择的语言；
2. 本租户的默认语言；
3. 实例默认语言；
4. 用户浏览器请求的语言；
5. English。

实例级设置请参阅[默认语言](/concepts/system-settings#locale-default)。

## 相关内容 {#related}

- [白标与品牌](/concepts/white-labeling)
- [底图](/concepts/basemaps)
- [系统设置](/concepts/system-settings)：运维人员设置的全实例默认值
