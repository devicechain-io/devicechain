---
sidebar_position: 11
title: 组件版本与兼容性
---

# 组件版本与兼容性 {#widget-versions-and-compatibility}

保存的仪表板会记录每个组件由哪个组件实现创作。升级组件包时，DeviceChain 会针对每个已存储的组件判断新包是否仍能渲染它。本页说明这一判断，以及对每种结果应当怎么做。

## 存储了什么 {#what-is-stored}

来自已注册包的组件在 `type` 旁带有一个 `widget` 引用：

```json
{
  "id": "tank-1",
  "type": "acme/tank-level",
  "widget": { "version": "1.2.0", "optionsVersion": 1, "sdkApiVersion": 1 },
  "layout": { "base": { "mode": "grid", "col": 0, "row": 0, "cols": 4, "rows": 3 } },
  "datasource": { "kind": "slot", "slot": "tank", "measurements": ["level"] },
  "options": { "unit": "%" }
}
```

作者添加组件时，控制台会从添加时所用的注册项复制 `version`、`optionsVersion` 和 `sdkApiVersion`，记录该引用。内置组件同样如此：每个内置组件都有自己的组件版本，从 `1.0.0` 开始；它不是平台版本，只在其渲染发生值得提示的变化时才会改变。在引用出现之前保存的内置组件没有引用，并继续正常渲染。

## 兼容性规则 {#the-compatibility-rule}

当组件的 `id`、`optionsVersion` 和 `sdkApiVersion` 都相同时，已存储的组件与已安装的包兼容。`version` 只是来源信息，不决定兼容性。

在控制台开始记录 `sdkApiVersion` 之前保存的引用只有 `version` 和 `optionsVersion`。它按 `id` 和 `optionsVersion` 匹配并继续渲染；下次作者替换该组件时，新的引用会记录全部三项。

| 已存储组件与已安装包的比较 | 结果 |
| --- | --- |
| `id`、`optionsVersion`、`sdkApiVersion` 相同，`version` 也相同 | 渲染。只有这种情况下，组件才恰好由创作它的那个版本渲染。 |
| `id`、`optionsVersion`、`sdkApiVersion` 相同，`version` 不同 | 渲染。控制台中该组件的设置面板会显示一条列出两个版本的非阻塞提示，让外观变化不会出人意料。 |
| `optionsVersion` 不同 | 占位符，并给出原因。已存储的选项对已安装的包意味着不同的东西。 |
| `sdkApiVersion` 不同 | 占位符，并给出原因。 |
| 此应用中未安装该包 | 占位符：该包未安装在此宿主中。 |
| 自定义 `id` 没有 `widget` 引用 | 占位符：已安装的组件无法与之匹配。 |
| 任何组件上格式错误的 `widget` 引用 | 占位符：已存储的版本信息无效。 |

占位符会占据画布上的位置，并保留该组件的原始 JSON，包括版本引用及其不识别的任何字段。它不打开任何数据订阅，也不提供任何操作。加载仪表板、移动占位符和保存草稿都会保留其数据，因此之后安装缺失的包就能无损地恢复该组件。只要有任何组件是占位符，控制台就会拒绝发布仪表板的新版本；参见[存在未解析组件时的发布](./adding-widget-packages.md#publishing-while-a-widget-is-unresolved)。

如果你构建自己的宿主，`@devicechain/dashboards` 导出了同样的判断：`unresolvedWidgets(definition)` 列出已解析定义中的占位符及其原因，`widgetVersionNotice(id, reference, descriptor)` 返回来源提示，版本相同时返回 `undefined`。

## 升级组件包 {#upgrading-a-package}

根据变更内容选择版本号递增方式：

- **缺陷修复或不改变已存储选项含义的视觉变更：** 递增 `version`（`1.2.0` 到 `1.2.1`）。现有仪表板继续渲染，作者在控制台中打开这些组件时会看到版本提示。
- **已存储选项的含义或结构发生变化：** 递增 `optionsVersion`，同时也递增 `version`。现有组件会变成占位符，直到作者替换它们，因为当前版本没有自动的选项迁移。优先新增带默认值的可选选项，而不是修改已有选项，这样就不需要这次递增。
- **迁移到新的 SDK 契约代际：** `sdkApiVersion` 变化，宿主只接受其支持的代际的注册项。已存储引用记录了旧代际的组件会变成占位符并给出原因，直到作者替换它们。

升级组件包绝不会改写仪表板或已发布的版本。要把一个组件迁移到新的 `optionsVersion`，作者需要替换该组件、编辑其设置并发布仪表板的新版本。以 `1.2.0` 创作、现在由 `1.2.1` 渲染的已发布版本不是精确重放；精确重放需要相同的 `version`。一个宿主对每个 id 只安装一个版本。

## 已发布仪表板中缺失的包 {#missing-packages-in-a-published-dashboard}

由于实现不属于仪表板，缺少某个包的查看器应用会显示占位符而不是错误页面，仪表板的其余部分正常渲染。请让控制台和所有查看器使用同一组组件包。
