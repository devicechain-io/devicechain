---
sidebar_position: 10
title: 添加组件包
---

# 将组件包添加到控制台和 /dash {#adding-widget-packages-to-the-console-and-dash}

组件包会被打包进显示它的应用。添加一个组件包属于构建时变更：添加依赖，把它的定义加入应用的注册表，然后重新构建。没有运行时安装器，也没有上传。导出的仪表板是可移植的 JSON，不包含组件的实现。

在某个应用中注册的包不会自动安装到其他应用。编辑用的控制台，以及每个会显示使用该组件的仪表板的应用，都必须包含同一个包，否则该组件在那里会显示为占位符。参见[组件版本与兼容性](./widget-versions-and-compatibility.md)。

## 在你自己的应用中 {#in-your-own-application}

如果你使用 `@devicechain/dashboards` 和 `@devicechain/widgets` 嵌入仪表板，请用内置定义加上你自己的定义构建一个注册表，并把同一个注册表同时交给解析器和渲染器：

```tsx
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { builtinWidgetDefinitions, DashboardRenderer } from '@devicechain/widgets';
import { tankLevelWidgets } from '@acme/devicechain-tank-level';

const registry = createWidgetRegistry([...builtinWidgetDefinitions, ...tankLevelWidgets]);
const definition = parseDashboardDefinition(rawDefinition, { catalog: registry.catalog });

return <DashboardRenderer definition={definition} hub={hub} registry={registry} />;
```

用不包含你的组件的目录解析定义，再用包含它们的注册表渲染，是行不通的：解析器已经把该组件变成了占位符。请把同一个注册表传给两者。

`createWidgetRegistry` 会校验并封存注册表。它会拒绝重复的 id（包括试图复用内置 id）、格式错误的描述符、不受支持的 `sdkApiVersion`，以及无效的默认值或绑定，并在同一个错误中一并报告。不存在全局注册，同一页面上的两个注册表彼此独立。一个注册表对每个 id 只保存一个确切的版本。

## 在 DeviceChain 控制台和 /dash 中 {#in-the-devicechain-console-and-dash}

控制台、`/dash` 查看器和录制回放播放器（board-replay）都由前端源码树中同一个共享组合模块 `frontend/apps/host-widgets` 构建注册表。它们都不维护自己的列表，因此在那里添加的包会同时到达所有这些地方：定义解析器、选项校验器、渲染器、编辑器的“添加”菜单和设置面板、合成预览以及录制回放播放器。

安装组件包的步骤：

1. 把该包添加到 `frontend/apps/host-widgets/package.json` 的依赖中，然后在 `frontend/` 中运行 `npm install`。
2. 在 `frontend/apps/host-widgets/src/extensions.ts` 中导入该包的定义，并把它们列入 `extensionWidgets`：

   ```ts
   import type { ErasedWidgetDefinition } from '@devicechain/widget-sdk';
   import { tankLevelWidgets } from '@acme/devicechain-tank-level';

   export const extensionWidgets: readonly ErasedWidgetDefinition[] = [...tankLevelWidgets];
   ```

3. 重新构建。在 `frontend/` 中运行 `npm run build` 会构建控制台、`/dash` 和录制回放播放器。控制台和 `/dash` 一起包含在由 `frontend/Dockerfile` 构建的前端容器镜像中；构建该镜像并用它替换标准镜像进行部署（即 Helm chart 的 `frontend.image` 配置项）。如果你提供录制回放播放器，也要部署它重新构建后的产物。

请同时部署所有应用。在控制台中使用你的组件创作的仪表板，在不包含该包的 `/dash` 或回放构建中会显示占位符。

如果组合无效，例如两个包声明了同一个 id，或某个描述符格式错误，注册表会在应用首次构建它时（首次打开或编辑仪表板时）抛出一个列出所有问题的错误。这样，损坏的包会在你测试的构建中暴露出来，而不是在生产环境中逐个组件地出现问题。

这个列表是编译进应用的受信任代码。它不是运行时安装器，仪表板也无法向其中添加内容。

## 存在未解析组件时的发布 {#publishing-while-a-widget-is-unresolved}

如果某个组件的包缺失，或者已安装的包无法读取其已存储的设置版本，它会被解析为占位符。在控制台中，你仍然可以打开仪表板、移动和调整占位符大小并保存草稿；它已存储的 JSON 会被原样保留。只要还有组件未解析，就无法发布新版本，版本历史面板会列出每个未解析的组件及原因。安装该包或删除该组件后再发布。

这一限制位于控制台中。服务器将仪表板定义作为不透明的 JSON 存储，不会进行这项检查。

## 发布前检查组件包 {#check-a-package-before-you-ship-it}

针对打包后的产物构建它，就像使用方获得 SDK 的方式，而不是针对单体仓库构建。仓库中的 `hack/widget-package-proof.sh all` 对示例包做了这件事：它打包 SDK，把 tarball 安装到仓库之外的副本中，进行构建，并检查该包没有私有或单体仓库导入、只解析到一份 SDK，且其仅 DOM 入口不会打包进 ECharts、MapLibre 或内置组件代码。随后它会在仅 DOM 入口中植入一个图表导入，并要求检查失败，以发现“看不见”的检查。CI 会在每次变更时运行它；它需要 Node、npm、已安装的前端（在 `frontend/` 中运行 `npm ci`）以及对 npm 仓库的访问。在 Windows 上请用 Git Bash 运行。对你自己的包采用同样的检查。
