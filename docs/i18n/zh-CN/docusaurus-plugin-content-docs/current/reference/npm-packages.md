---
sidebar_position: 3
title: npm 包
---

# npm 包 {#npm-packages}

仪表盘运行时已发布到 npm。本仓库之外的 React 应用也可以安装它，展示实时 DeviceChain 仪表盘。

:::note 状态
这些包处于 **1.0 之前**：类型、属性和导出项可能在版本之间变化，升级前请阅读发布说明。
它们仅提供 ESM，并面向打包工具构建；参见[这些包的使用前提](#what-these-packages-assume)。
:::

## 已发布的包 {#what-is-published}

| 包 | 用途 |
|---|---|
| [`@devicechain/client`](https://www.npmjs.com/package/@devicechain/client) | TypeScript SDK：处理认证令牌、通过 fetch 调用 GraphQL、解码 JWT，以及实时订阅。不依赖特定框架，也不依赖 React。 |
| [`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards) | 提供管理连接、解析选择器和复用遥测订阅的 `DashboardHub`，以及定义、选择器、槽位和绑定清单的类型。 |
| [`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) | React 组件和负责仪表盘布局的渲染器。 |
| [`@devicechain/brand`](https://www.npmjs.com/package/@devicechain/brand) | 品牌设计变量和生成的样式表。此包可选：组件完全通过 CSS 自定义属性设置主题，不要求使用它。 |

```bash
npm install @devicechain/widgets @devicechain/dashboards @devicechain/client \
            graphql react react-dom
```

`@devicechain/dashboards` 和 `@devicechain/client` 是组件包的 **peer dependencies**，锁定为完全相同的版本。
npm 7 及更高版本会自动安装这些依赖，因此只需运行上面的安装命令。

锁定版本是有意的：它防止 npm 在组件包下面悄悄嵌套安装第二份 SDK。
两份 SDK 比报错更糟。运行时通过捕获到的错误类的身份，区分“你无权查看”与“连接中断”。
一份 SDK 抛出的错误不是另一份 SDK 中错误类的实例，权限拒绝就会被误认为服务中断。

## 版本和 dist-tag {#versions-and-dist-tags}

四个包随平台发布一起发布，使用同一个版本号。组件的 `0.14.0` 版本对应 SDK 的 `0.14.0` 版本，
无需考虑独立的包版本规则。

| 标签 | 指向 |
|---|---|
| `latest` | 最新的**稳定**版本。不指定版本的 `npm install` 会选择它。 |
| `next` | 最新的**预发布**版本（`-rc.1` 等）。通过 `npm install @devicechain/widgets@next` 主动选择。 |

发布工作流发布的每个版本都携带 [npm provenance](https://docs.npmjs.com/generating-provenance-statements)：
一份签名的、可公开验证的声明，说明构建该版本的仓库、工作流和提交。npmjs.com 的包页面会展示它。
从 `0.14.0` 起的每个版本都带有此声明。

例外是用于初始化发布的 `0.14.0-0` 版本。它们通过手动发布来首次创建这些包，早于提供签名的工作流。

## 渲染仪表盘 {#render-a-board}

```tsx
import { DashboardRenderer } from '@devicechain/widgets';
import { DashboardHub, parseDashboardDefinition } from '@devicechain/dashboards';

const hub = new DashboardHub({ resolver, authorities: user.scopes });
const definition = parseDashboardDefinition(json);

<DashboardRenderer definition={definition} hub={hub} actions={hub} />;
```

严格只读的挂载方式可以省略 `actions`；具备操作能力的组件此时不会执行操作：

- 告警表完全隐藏确认和清除控件。
- 命令组件仍显示参数表单和发送按钮，但会禁用它们，并提示查看者没有发送命令的权限。

这就是启用操作能力的全部设置，也是服务器检查之外的第二层控制。
没有收到 `actions` 的查看器，无论仪表盘包含什么，都无法通过仪表盘写入；
服务器始终独立强制检查 `alarm:write` 和 `command:write`。

参见[仪表盘](../concepts/dashboards.md)，了解定义的含义、槽位和绑定清单如何让一个定义服务于多个设备，
以及独立 `/dash` 查看器如何使用它们。

## 渲染地图 {#map-host-wiring}

地图组件需要你提供一项设置；省略它，地图会无声地失败。

MapLibre 在 Web Worker 中解析矢量瓦片，并在运行时根据自身模块的 URL 推导 Worker URL。
这是打包工具无法追踪的动态字符串，因此：

1. 打包工具不会输出 Worker 文件。
2. 浏览器请求它时收到 404。
3. `new Worker()` 不会抛出异常。
4. 地图呈现为空框，控制台也没有任何信息。

只有你的打包工具能输出该文件，因此 URL 必须由你提供。

### Vite {#vite}

使用 Vite 时，只需一次导入：

```tsx
import { MapRuntimeProvider } from '@devicechain/widgets';
import { viteMapRuntime } from '@devicechain/widgets/vite';

<MapRuntimeProvider runtime={viteMapRuntime}>
  <DashboardRenderer definition={definition} hub={hub} />
</MapRuntimeProvider>;
```

### 其他打包工具 {#other-bundlers}

其他打包工具需要你自己提供 URL。唯一的要求，也是全部难点，是 MapLibre 把该 URL 作为
**module worker** 加载，因此提供的文件必须是一个没有任何未解析导入的模块。
单独的 `maplibre-gl/dist/maplibre-gl-worker.mjs` 并不满足要求：它的第一行导入同目录的 `maplibre-gl-shared.mjs`。

:::danger 不要指向单独复制的 Worker 文件
尤其不要使用 `new URL('maplibre-gl/dist/maplibre-gl-worker.mjs', import.meta.url)`。
Webpack 会把这一文件作为资源复制，但不会输出它的同级依赖，因此 Worker 在第一行就终止。
Worker URL 返回 200，画布显示出来，标记也已放置，构建退出码为 0，控制台毫无信息——但地图中没有底图。
:::

有两种可行方案。在 **webpack** 中，为 Worker 设置单独的入口，让 webpack 把它的导入一起打包：

```js
entry: {
  main: './src/index.tsx',
  'maplibre-worker': 'maplibre-gl/dist/maplibre-gl-worker.mjs',
},
output: {
  filename: (data) =>
    data.chunk.name === 'maplibre-worker' ? 'maplibre-worker.js' : '[name].[contenthash].js',
},
```

然后指向你选择的文件名：

```tsx
const runtime = { workerUrl: '/maplibre-worker.js' };
```

或者，在**任何打包工具**中，自己把两个文件复制到同一个可访问的目录：

```tsx
//   maplibre-gl/dist/maplibre-gl-worker.mjs  ->  public/vendor/
//   maplibre-gl/dist/maplibre-gl-shared.mjs  ->  public/vendor/
const runtime = {
  workerUrl: '/vendor/maplibre-gl-worker.mjs',
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
```

`loadStyles` 是可选项；你也可以自行提前导入 MapLibre 的样式表。
在此提供它，会让这 83 KB（gzip 后为 10.7 KB）保留在地图的延迟加载分块中；从不打开地图的查看者无需下载它。

地图组件上方没有 Provider 时，会显示明确提示，而非空白画布。
这是有意的，让未完成接线的宿主应用知道缺少什么，无需猜测。

### `maplibre-gl` 是 peer dependency {#maplibre-gl-is-a-peer-dependency}

在你自己的应用中，与组件包一起声明它：

```bash
npm install maplibre-gl
```

原因有两个：

- **归属。** Worker URL 由你负责输出，因此该库也应由你的应用管理。
- **版本不一致。** 如果应用提供的 Worker 来自一个 MapLibre 版本，而组件使用自己的另一份 MapLibre，
  一个版本的 Worker 就会驱动另一个版本的主线程，仍然会得到空白地图。peer dependency 保证共用一份库。

如果应用没有声明它，npm 的依赖提升仍可能让导入成功。这会一直工作，直到有人使用 pnpm 的严格模式
或类似的隔离布局安装；在这些布局中，未声明的依赖就不会被提供。

## 底图瓦片 {#basemap-tiles}

`TenantBasemapProvider` 提供瓦片源，它是可选的：没有 Provider 时，地图退回到普通视图。
解析优先级位于 `@devicechain/client` 中，因此 DeviceChain 的所有界面都以相同方式，让用户覆盖项优先于租户默认值。
参见[底图](../concepts/basemaps.md)。

## 主题 {#theming}

组件使用的每一种颜色、圆角和字体都来自 CSS 自定义属性，因此只需在任意祖先元素上设置变量就能更换样式。
它不需要 Tailwind，不包含全局样式表，也不规定应用外壳的样式。
DeviceChain 的默认变量通过 `@devicechain/brand` 提供，但并不要求使用。

## 这些包的使用前提 {#what-these-packages-assume}

| | |
|---|---|
| **模块格式** | 仅 ESM，不提供 CommonJS 构建。 |
| **React** | 19。 |
| **打包工具** | Vite 或 webpack。 |
| **TypeScript 解析** | 输出的声明使用没有扩展名的模块说明符；它们在 `moduleResolution: "bundler"` 下可解析，在 `node16`/`nodenext` 下不可解析。 |

这些是受支持的组合，“受支持”与“可能没问题”之间的界线是有意划定的。
测试会使用打包后的 tarball，在本仓库之外构建三个应用，并在真实浏览器中验证：

- 一个使用 Vite；
- 一个使用 webpack；
- 一个采用上面的复制 Worker 方案，同样使用 webpack 构建。

每个应用都会渲染地图，并检查实际获取的瓦片和实际放置的标记。
“渲染出来了”并不足够，因为地图组件的失败模式恰好也能通过这一断言。

这些测试没有覆盖 Rollup 或 esbuild 使用者、Next.js 或 React Server Components、CommonJS 使用者，
也没有覆盖 `node16`/`nodenext` 解析。Rollup 和 esbuild 读取相同的普通 ESM，预计可以工作，
但属于“可能没问题”的一侧。其余组合尚未测试，而不是已知损坏。
情况变化时，本页会明确说明。

## 针对工作区构建 {#building-against-the-workspace-instead}

以上内容介绍从注册表安装。在本仓库内，这些包同样是唯一来源：控制台和 `/dash` 查看器
都针对消费者下载的同一份 `dist` 构建，而不是直接针对 TypeScript 源码构建。
因此发布产物中的损坏也会破坏这些应用。如果你正在开发 DeviceChain 本身，
参见[本地开发指南](../guides/local-development.md)。
