---
sidebar_position: 9
title: 编写自定义组件
---

# 编写自定义组件 {#writing-a-custom-widget}

自定义组件由一个 React 组件和一小段描述它的纯数据组成。你使用 `@devicechain/widget-sdk` 编写这两部分，将它们放进 npm 包发布，并在显示仪表板的应用中注册该包。内置组件也通过完全相同的接口声明，因此自定义组件可获得相同的数据绑定、选项校验和错误处理。

你的组件接收的是已解析好的数据。它不负责认证、解析数据源、轮询，也不建立遥测连接。这些都由宿主完成，并把一份快照交给你的组件。

仓库中的 `examples/widget-tank-level` 是一个可直接复制的完整包。它在单体仓库之外，基于已打包的 SDK tarball 构建。

## 安装 SDK {#install-the-sdk}

```bash
npm install @devicechain/widget-sdk @devicechain/dashboards react
```

在你的组件包中，将 `@devicechain/widget-sdk`、`@devicechain/dashboards` 和 `react` 声明为**对等依赖（peer dependencies）**，不要声明为普通依赖。SDK 定义了组件读取的 React 上下文（选择、时区、主题根），而上下文由创建它的对象标识。因此宿主和每个组件包必须解析到同一份 SDK。

SDK 根入口不含图表或地图代码。只绘制 DOM 的组件从根入口导入，不会向宿主的打包产物加入 Apache ECharts、MapLibre 或内置组件。图表辅助工具（`EChart`、`buildLineOption`、`buildGaugeOption`、`buildCompassOption`、`useChartTheme`、`resolveChartTheme` 和 `severityFill`）位于 `@devicechain/widget-sdk/echarts`，而 `echarts` 是可选的对等依赖，只有导入该入口的包才需要它。

## 声明组件 {#declare-a-widget}

```tsx
import { WidgetFrame, defineWidget, formatValue, pickSample } from '@devicechain/widget-sdk';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Telemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1, default: 1 } },
    options: {
      unit: { kind: 'string', doc: 'Unit shown beside the level.', label: 'Unit' },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples kept.', label: 'Samples kept' },
    },
    defaults: { unit: '%', window: 1 },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ data }) => (
      <WidgetFrame title="Tank level">{formatValue(pickSample(data.latest, undefined)?.value, 1)}</WidgetFrame>
    ),
  },
});
```

`defineWidget` 会根据字面量推断通道和选项模式。组件数据与通道不一致、默认值与模式不一致、绑定引用了不存在的选项，或声明了该通道无法承载的交互，都会成为编译错误。宿主构建注册表时会再次检查同样的规则，以覆盖绕过编译器的调用方。

### 描述符 {#the-descriptor}

描述符是可序列化为 JSON 的纯数据，不含组件、回调、导入 URL 或令牌，因此定义解析器和校验器无需加载或信任你的代码就能读取它。

| 字段 | 含义 |
| --- | --- |
| `id` | 带命名空间的 id，形如 `scope/name`，例如 `acme/tank-level`，只能使用小写字母、数字和连字符。不含斜杠的 id 保留给内置组件。当前版本中命名空间的归属只是运维人员之间的约定，并不证明包的发布者是谁。 |
| `version` | 该组件的发布版本，格式为 `MAJOR.MINOR.PATCH`。它是来源信息。参见[组件版本与兼容性](./widget-versions-and-compatibility.md)。 |
| `sdkApiVersion` | 组件所针对的 SDK 契约代际，目前为 `1`。 |
| `optionsVersion` | 已存储选项的结构版本。仅当已存储选项的含义发生变化时才递增。 |
| `display` | `title`，以及可选的 `description` 和 `category`，用于宿主的“添加”菜单。`titleId` 和 `categoryId` 是可选的消息 id，见[标签与帮助文字](#labels-and-help-text)。 |
| `channel` | 组件消费哪类数据。见下文。 |
| `datasource` | `{ mode: 'none' }`，或 `mode: 'optional'` / `'required'` 加上作者可选的 `selectors`：`device`、`anchor` 或 `slot`。`control` 通道的组件只针对一台设备，因此只提供 `device` 和 `slot`。 |
| `binding` | 宿主如何订阅，用常量和对你自己选项键的引用来表示。 |
| `options` | 选项模式。 |
| `constraints` | 可选，跨多个选项的规则，例如 `greater-than`。 |
| `defaults` | 新添加组件的初始选项。 |
| `interactions` | 组件请求的宿主回调。 |
| `freshness` | 可选，设为 `true` 即启用。仅限 `measurement` 通道：框架会显示组件最新样本的时间间隔。若组件还声明了数值选项 `staleAfterSeconds`，超过该时长的数值会变灰并标记为过期。没有该选项时只显示时间间隔，不会标记任何内容为过期。只显示单个数值的组件应通过 `<WidgetFrame sampleTime={sample?.occurredTime ?? null}>` 传入该样本的时间，使时间间隔对应屏幕上的数值，而不是最后到达的测量值。 |
| `layout` | 可选的尺寸限制，以网格单元计。参见[尺寸](#size)。 |

### 通道与绑定策略 {#channels-and-binding-policy}

一个组件声明一个通道：

| 通道 | 组件接收 | 绑定策略 |
| --- | --- | --- |
| `measurement` | 按测量名称组织的 `latest`、滚动的 `samples` 窗口和 `error`；车队组件还会收到 `members` | `window`：保留多少个样本。`windowSeconds`：让组件改为绘制一段时间跨度（参见[看板的时间范围](#the-boards-time-range)）。`members`：使其成为车队组件（见下文） |
| `aggregate` | `aggregates`（每个测量在跨度内的读数个数、最小值、最大值、总和与平均值，或无法计算的原因），以及每个测量最新的实时读数 | `windowSeconds`：这些数值覆盖的跨度 |
| `alarm` | 当前告警行、总数以及加载与错误标志 | `pageSize`，以及可选的 `state`、`severity`、`acknowledged` 过滤器 |
| `control` | 目标设备的命令历史 | `pageSize` |
| `location` | 所绑定实体的位置样本 | 无 |
| `selection` | 无数据。组件根据宿主的候选项查询来驱动一个槽位。 | 无 |
| `none` | 无数据。静态组件。 | 无 |

绑定值要么是常量 `{ const: 25 }`，要么是对选项键的引用 `{ option: 'window', min: 1, default: 1 }`。引用在编译时会按选项类型检查。你不能提供 GraphQL 字符串或订阅函数：传输由宿主负责。`selection` 和 `none` 通道不打开任何数据通道，且必须声明 `datasource: { mode: 'none' }`。

声明了 `members` 的 `measurement` 组件会逐台设备读取其数据源：绑定到设备组时，`data.members` 列出每台设备及其各测量的最新读数，因此上报同名测量的两台设备不会互相覆盖。`{ const: true }` 始终开启该模式；带 `equals` 的选项引用则在某个枚举选项取特定值时开启，例如柱状图的*每台设备一根柱*。没有该模式的 `measurement` 或 `aggregate` 组件只显示一台设备：只提供 `device` 和 `slot` 选择器，因为宿主会拒绝到达它的设备组，并给出一句指向车队表格的说明，而不是显示最后上报的那台设备。

当槽位没有设备，或所绑定的设备被删除时，宿主会显示其标准的未绑定或不可用状态，不会把之前那个实体的数据交给你的组件。

## 选项 {#options}

选项有 `kind`（`string`、`number`、`boolean`、`enum` 或 `list`）和一句 `doc` 说明。数字可声明 `min`、`max` 和 `integer`。枚举声明其 `values`。字符串可声明 `format: 'json'`，要求其值能解析为 JSON。`required` 标记一个缺失就会让组件无内容可显示的选项；空字符串视为缺失。宿主会依据模式校验已存储的选项，控制台也据此生成设置表单。

`list` 选项存储一个由小对象组成的数组，例如组件的阈值。它声明一个 `item` 对象，其字段为 `string`、`number`、`boolean` 或 `enum` 规格（字段可以是 `required`，列表不能嵌套），可选的 `maxItems`，以及在条目顺序有意义时使用的 `ordered: true`（设置表单随后会为每一行提供上移和下移）。存储的条目若含有 `item` 未声明的字段，则会被拒绝；设置表单为每个条目渲染一行，每个字段一个控件。大多数内置遥测组件都带有的 `thresholds` 选项就是这样一个列表：每个条目指明一个严重级别（`normal`、`warning`、`minor`、`major` 或 `critical`），而不是颜色，因此由主题决定它在浅色和深色下的外观。只有异常级别才有颜色：处于 `normal` 区间的值与没有阈值时完全相同，因此正常读数永远不会被着色。

`optThresholds`、`bandAt` 和 `thresholdBands` 可在你自己的组件中读取同一选项，`useSeverityColors` 则给出主题中每个异常级别的颜色。

使用 SDK 的辅助函数 `optString`、`optNumber` 和 `optBoolean` 以防御方式读取选项。对于手工编辑后类型错误的值，它们不会抛出异常：`optString` 和 `optNumber` 返回 `undefined`，`optBoolean` 对除已存储的 `true` 以外的任何值都返回 `false`。

### 标签与帮助文字 {#labels-and-help-text}

为每个选项提供可移植的 `label`，需要时再提供一句 `help`。从未见过你的组件的宿主会原样显示这些文字，因此你的选项无需宿主提供任何翻译即可被理解。每段文字还可以带一个消息 id，宿主会先在自己的翻译中查找它：

| 文字 | 可移植文字 | 可选的消息 id |
| --- | --- | --- |
| 选项标签 | `label` | `labelId` |
| 选项帮助 | `help` | `helpId` |
| 枚举选项 | `valueLabels`，按值索引 | `valueLabelIds`，按值索引 |
| 枚举的未设置项 | `placeholder` | `placeholderId` |
| 组件标题与类别 | `display.title`、`display.category` | `display.titleId`、`display.categoryId` |

控制台在自己的翻译定义了该消息 id 时使用它，否则使用你的可移植文字。两者都没有的标签会回退为可读化的选项键（`maxRows` 显示为“Max rows”），两者都没有的枚举选项显示其原始值。宿主未定义的消息 id 不是错误，因此你的包可在从未见过它的宿主中正常工作。

```tsx
options: {
  show: {
    kind: 'enum',
    values: ['bar', 'number'],
    doc: 'Whether the card draws the fill bar under the number.',
    label: 'Show',
    valueLabels: { bar: 'Number and fill bar', number: 'Number only' },
    placeholder: 'Number and fill bar',
  },
},
```

### 专用编辑控件 {#specialized-authoring-controls}

选项可以在其 `control` 字段中指定一个专用的编辑流程。它与 `control` 数据通道无关。不指定时，选项使用与其类型对应的通用字段：文本框、数字框、复选框或下拉列表。

该词汇表是开放的。任何非空字符串都可以注册，未实现所指定控件的宿主会改为显示该选项的通用字段。因此，即使宿主比包所指定的控件更旧，包仍然可以安装。请在你自己的命名空间中命名你的控件，例如 `acme/capacity-presets`。不带前缀的名称属于宿主。

控制台实现了 `HOST_OPTION_CONTROLS` 中列出的控件，该常量由 `@devicechain/dashboards` 导出：

| 控件 | 流程 |
| --- | --- |
| `command-vocabulary` | 从所绑定设备的命令中选择一个，并一起写入其键、标签和参数。 |
| `slot-target` | 从仪表板的槽位中选择一个。 |
| `device-type` | 从租户目录中选择一个设备类型并保存其令牌。可用于字符串选项或列表项的字符串字段。 |
| `basemap` | 标记栅格地图瓦片源及其署名。 |

控制台未实现的控件（包括所有带命名空间的控件）使用通用字段。若要为你的选项提供专属的编辑体验，请提供一个编辑器。

### 自定义编辑器 {#a-custom-editor}

`implementation.editor` 是一个可选的 React 组件。组件提供了编辑器时，控制台会用它替代生成的选项字段。数据源字段和控制台的提示仍由控制台负责。编辑器接收由 `@devicechain/widget-sdk` 导出的 `WidgetEditorProps`：

| 属性 | 含义 |
| --- | --- |
| `options` | 组件已存储的选项，只读。 |
| `setOptions(patch)` | 修改选项的唯一方式。一次调用中的多个键会一起生效。 |
| `services` | 宿主服务，以显式方式传入。见下文。 |
| `descriptor` | 你所注册的描述符，用于读取其选项的标签、边界和枚举值。 |

`setOptions` 在生效前会依据你的选项模式进行检查。模式未声明的键或类型错误的值，会在编辑器出错的那一刻抛出异常，而不是被存储下来却无人读取。控制台会捕获该错误并显示在你的编辑器旁边，且不会写入任何内容。`undefined` 和空字符串会删除一个键，布尔选项的 `false` 也是如此。超出声明边界的数字会被限制到边界之内。

`services`（`WidgetEditorServices`）包含：

| 服务 | 含义 |
| --- | --- |
| `slots` | 仪表板的槽位，按名称索引。仪表板没有槽位时为空。 |
| `deviceToken` | 组件直接绑定的设备的令牌（如果是直接绑定）。 |
| `commandChoices(deviceToken)` | 设备接受的命令。可选：仅当你的描述符在某个选项上声明了 `command-vocabulary` 控件时才会提供；没有命令词汇表的宿主也不提供它。 |

编辑器不会获得 hub、客户端或访问令牌，也不得从宿主应用导入任何内容。宿主无法提供的服务会缺失，你的编辑器必须能够应对。如果编辑器在渲染时抛出异常，控制台会用一条错误消息和一个重试按钮替换它，作者在仪表板上的其他更改都会保留。

```tsx
implementation: {
  kind: 'trusted-react',
  component: TankLevelCard,
  editor: function TankLevelCardEditor({ options, setOptions, descriptor }) {
    return (
      <label>
        {descriptor.options.unit?.label ?? 'unit'}
        <input value={options.unit ?? ''} onChange={(e) => setOptions({ unit: e.target.value })} />
      </label>
    );
  },
},
```

在 `defineWidget` 中，`options` 和 `setOptions` 的类型来自你的选项模式，因此拼错的键也会成为编译错误。示例包中的卡片组件带有一个完整的编辑器。

## 尺寸 {#size}

组件可在其描述符的可选 `layout` 块中声明自己可以有多大。尺寸以网格单元计（横向列数、纵向行数），而不是像素：

```ts
layout: {
  defaultSpan: { cols: 4, rows: 3 }, // 新添加组件的尺寸
  minSpan: { cols: 3, rows: 2 },     // 可缩小到的最小尺寸（默认 1 x 1）
  maxSpan: { cols: 8, rows: 6 },     // 可放大到的最大尺寸（默认：无限制）
  resize: 'both',                    // 'both'（默认）、'horizontal'、'vertical' 或 'none'
},
```

每个值都是正整数，并且 `minSpan` 不得大于 `defaultSpan`，`defaultSpan` 不得大于 `maxSpan`。`resize` 取 `both`
以外的值时会把另一个方向固定为 `defaultSpan`：`'horizontal'` 固定行数，`'vertical'` 固定列数，`'none'` 两者都固定。
你声明的 `minSpan` 或 `maxSpan` 在固定方向上必须与 `defaultSpan` 一致。宿主构建注册表时，会拒绝违反这些规则的块。

编辑器以 `defaultSpan` 添加新组件。`@devicechain/dashboards` 中的 `lintLayout` 会把超出限制放置该组件的仪表板报告为错误。请把 `minSpan` 设为组件仍然清晰可读的最小尺寸。
未声明 `layout` 块的组件会以 4 x 2 个单元添加且不设限制，`lintLayout` 会提示它没有声明。

单元格是正方形，仪表板显示的单元格永远不会小于其最小单元格尺寸（默认 56 像素；屏幕更窄时仪表板会重新排列）。因此请按这个最小单元格来确定 `minSpan`：使用默认的 8 像素间隔时，2 个单元为 120 像素，3 个为 184 像素，4 个为 248 像素。在更宽的屏幕上单元格会变大，组件只会变得更大。正方形单元格网格之前的版本按 24 个窄列、40 像素行的网格计数；仍声明这些数值的包可以继续使用，但其尺寸大约宽了一倍，`lintLayout` 会报告把它放在限制之外的仪表板。请把列数减半并重新检查行数。

文字遵循仪表板的密度。`WidgetFrame` 会为组件添加内边距，并把组件的内容区设为尺寸容器，因此在其中，CSS 容器单位（`cqw`、`cqh`）
量到的就是组件实际拥有的空间。请使用字号阶梯（`fontVar('md')` 以及 `xs` 到 `display` 的其他档位）和 `spaceVar(1)` 到
`spaceVar(4)` 的间距，而不要写像素值：两者都会乘以仪表板的密度（紧凑、舒适或墙面显示），密度由仪表板作者设定，查看者可以覆盖。
大号读数可用 `fitValueFontCss(chars)` 得到按字符数计算的 CSS 字号。画布无法读取 CSS 变量，请在框架内渲染的组件中调用
`useWidgetScale()`（或给框架传入函数子节点），获取 `sizeClass`（`compact`、`regular` 或 `large`）、以像素计的内容尺寸，
以及某个令牌的像素值 `px('font-md')`。组件变矮时，框架会先隐藏副标题，再隐藏标题；标题仍可供辅助技术读取。

## 看板的时间范围 {#the-boards-time-range}

看板有一个查看者可以更改和暂停的时间范围（参见[仪表板](../concepts/dashboards.md#time-range-and-pause)）。声明了 `binding.windowSeconds` 的 `measurement` 组件绘制的是一段时间跨度，而不是样本数量：跨度由所引用的选项固定，选项未设置时使用看板的时间范围。宿主会预先加载该跨度的历史数据，并最多保留 `MAX_SPAN_SAMPLES`（2,000）个实时样本；如果不得不丢弃仍在跨度内的旧样本，`data.truncatedBefore` 会说明窗口实际从何时开始覆盖，你的组件应当说明这一点，而不是用长跨度的标签标注一个短窗口。

`useWidgetTimeRange()` 返回时间范围：`spanSeconds`、`paused`，以及暂停时的 `pausedAt`。组件在看板之外渲染时它为 `undefined`，因此请处理这种情况，而不要假定为一小时。`withinSpan(samples, spanSeconds, end)` 会丢弃早于跨度的样本。看板暂停时，宿主会把除 `control` 以外的所有数据通道保持在屏幕上的快照，因此你的组件无需自行实现暂停逻辑。

看板也可以显示固定的起止窗口。此时组件读到的时间范围看起来像一个被冻结的滚动范围：`paused` 为 `true`，`pausedAt` 是窗口的结束时间，`spanSeconds` 是窗口长度，因此只理解滚动范围的组件会准确绘制该窗口（实时数据流已经关闭），无需任何修改。想要表明“固定窗口”的组件可以读取可选字段 `absolute`（`fromMs` 与 `toMs`，纪元毫秒），它仅在此情形下存在。

不指定精度时，`formatValue(value)` 最多显示 3 位小数；小于 1 的数值保留 4 位有效数字。传入精度可固定小数位数。

## 交互与信任模型 {#interactions-and-the-trust-model}

`interactions` 列出组件使用的宿主回调：`alarmActions`（确认与清除，仅限告警通道）、`commandSend`（仅限控制通道）、`selection` 和 `candidates`。宿主只提供你声明过的回调，并进一步限制在宿主支持且查看者被允许的范围内。没有回调的组件表现为只读。服务器始终是权威：查看者无权执行的操作会被服务器拒绝，且该拒绝会传达到你的组件。

**组件包是受信任的代码。** 它在构建时被打包进宿主应用，并在宿主页面中以宿主的权限运行。描述符中的声明用于规范 API，并不是安全边界，无法阻止包中的恶意代码。SDK 不会把访问令牌、客户端或原始 hub 传给你的组件，但同一页面中的代码仍然可能获取它们。只安装你已审查过、来自可信发布者的包。宿主会用错误边界包裹每个组件，因此抛出异常的组件只会在自己的框内显示错误，同级组件继续渲染。但这无法遏制死循环或恶意代码。

## 下一步 {#next}

- [将该包添加到控制台和仪表板应用](./adding-widget-packages.md)。
- [已存储的组件如何在包升级后保持兼容](./widget-versions-and-compatibility.md)。
