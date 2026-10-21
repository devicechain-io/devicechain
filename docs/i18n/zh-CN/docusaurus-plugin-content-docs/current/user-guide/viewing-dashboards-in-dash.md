---
title: 在 /dash 中查看仪表板
sidebar_position: 17
---

import GuideShot from '@site/src/components/GuideShot';

# 在 /dash 中查看仪表板 {#viewing-dashboards-in-dash}

除了控制台，实例还在 `/dash/` 提供一个独立的仪表板查看器。它只显示已发布的仪表板，别无他物：没有侧边栏，没有编辑器，也没有草稿。可用于控制室的墙面显示器、直接打开某块看板的书签，或只需要查看仪表板的人员。本章面向打开它的所有人，以及设置墙面显示器的人员。

仪表板本身在控制台中构建和发布；参见[仪表板](/user-guide/dashboards)。

## 登录 {#signing-in}

查看器有自己的登录表单，包含 **Email**（电子邮件）、**Password**（密码）、**Sign in**（登录）和 **Sign in with SSO**（使用单点登录），以及语言切换。使用单点登录时，先输入你的电子邮件地址，再选择 **Sign in with SSO**。如果你的账户属于多个租户，查看器会要求你选择其中一个。

<GuideShot id="dash-signin" alt="/dash 登录卡片，Email 字段为 sam.rivera@widgetlab.example，Password 字段为空，有 Sign in 按钮、Sign in with SSO 按钮，以及 EN English 和 ES Español 语言按钮。" />

查看器只把会话保存在页面的内存中。它不与控制台共享会话：登录控制台并不会让你登录这里，退出其中一个也不影响另一个。重新加载页面，或在另一个标签页中打开 `/dash/`，都会要求你重新登录。

## 加载仪表板 {#loading-a-dashboard}

登录后，**Load a dashboard**（加载仪表板）会列出 **Your dashboards**（你的仪表板）：你可以打开的已发布仪表板。选择其中一个即可打开。如果你的访问权限仅限于租户的部分客户或设备组，列表中是共享给这些客户或组的已发布仪表板；否则是租户中的所有已发布仪表板。列表显示前 200 个，超出时会注明。

{/* TODO(screenshot): dash-list, the Your dashboards list above the token field. */}

在列表下方，你也可以通过 **Dashboard token**（仪表板令牌）打开仪表板，令牌可在控制台中其页面上复制：输入令牌，然后选择 **Load published**（加载已发布版本）。查看器会获取最近一次发布的版本。从未发布过的仪表板无法在这里打开；查看器会提示你请作者发布它。

<GuideShot id="dash-load" alt="Load a dashboard 页面。Dashboard token 字段为 wl-gallery，提示为 Fetches the version of this dashboard that was last published，并有“Load published”按钮。下方的 Or paste an exported definition 下有 Definition (JSON) 框、带有结构示例的 Binding manifest (JSON, optional) 框，以及 Render 按钮。页眉有语言按钮和 Sign out。" />

下半部分接受你从控制台导出的仪表板：粘贴定义，以及可选的、将其槽位指向特定设备的绑定清单，然后选择 **Render**（渲染）。参见[导出](/user-guide/dashboards#exporting)，绑定清单请参阅[嵌入](/concepts/dashboards#embedding-definitions-slots-and-binding-manifests)。

## 看板 {#the-board}

加载后的看板占满窄页眉下方的整个页面。页眉显示仪表板的标题、时间范围和 **Pause**（参见[时间范围与暂停](#time-range-and-pause)）以及连接指示器，并提供语言切换、**Dark theme**（深色主题；或 **Light theme**，浅色主题）、返回加载表单的 **Change**（更换），以及 **Sign out**（退出登录）。

<GuideShot id="dash-view" alt="/dash 查看器中的 Widget Lab 看板，浅色主题。页眉显示 Widget Lab、时间范围 15m、1h（已选中）、6h、24h 和 7d、一个 Pause 按钮、一个 Live 指示器，以及 English、Español、Dark theme、Change 和 Sign out 按钮。下方有：标题卡片、温度读数、温度仪表、Active alarms 计数、一张图片、标有 15m 的温度和湿度折线图，以及 Latest values 表格。" />

组件是实时的：数值、图表和告警随数据到达而更新，看板的实时连接正常时，连接指示器显示 **Live**；设备只是暂时没有上报数据并不会改变它。连接中断时它显示 **Reconnecting**，中断 20 秒后显示 **Offline**，如果浏览器本身断网则立即显示 **Offline**；连接恢复后，组件会自行继续更新。查看器无法修改仪表板，但组件的操作对有权使用它们的人仍然有效。角色被授予 `alarm:write` 的人可以在看板上确认和清除告警，拥有 `command:write` 的人可以向真实设备发送命令。服务器会对每个操作检查这些权限。参见[操作与权限](/concepts/dashboards#actions-and-permissions)。

仪表板和组件的标题是仪表板作者写下的文本，因此无论查看器设置为何种语言，都保持原样。

### 浅色与深色 {#light-and-dark}

主题按钮在浅色和深色之间切换看板，此浏览器会记住你的选择供下次使用。

<GuideShot id="dash-dark" alt="同一块 Widget Lab 看板的深色主题：深色背景、浅色文字，页眉按钮现在显示 Light theme，读数、仪表、告警计数、图表和表格都针对深色背景重新设计了样式。" />

## 时间范围与暂停 {#time-range-and-pause}

页眉带有时间范围：**15m**、**1h**、**6h**、**24h** 和 **7d**。它决定图表绘制多少历史数据，从现在往回计算。看板打开时使用作者选定的时间跨度，作者没有选择时为一小时；你选择的跨度会应用到所有跟随看板时间范围的图表。作者为某个图表固定了自己的跨度时，该图表保持其跨度不变，并且只要它与页眉中的时间范围不同，就会在角落显示自己的跨度（例如 **1h**），这样固定的一小时不会被误读为一天的数据。较长的范围使用平均后的数据点绘制，而不是每一条读数，因此一周的数据也能快速加载。

一个图表最多保留 2,000 条读数。在较长的时间范围内，对于上报频繁的设备，这可能无法覆盖整个范围：最旧的读数会先被丢弃，图表随后会在角落标明它从何时开始（例如 **from 14:03**），因此不完整的窗口不会被误认为完整的时间范围。

**Pause**（暂停）会冻结看板。数值、图表、告警计数、告警表格和地图都停在原处，控件旁会出现 **Paused** 徽标和你暂停的时间。冻结的看板上时间仍在流逝：数值旁的时长（例如“12 秒前”）会从你正在看的那条读数开始继续累加，所以暂停的数值不会看起来是最新的。选择 **Resume**（恢复）后，看板会补上这段时间内到达的所有数据。暂停期间仍可更改时间范围；看板显示的是你暂停那一刻的该跨度。

暂停冻结的是看板显示的内容，而不是它执行的操作。暂停期间命令按钮仍然可用，它们显示的最近命令列表也一样；因此 **Paused** 徽标还会显示“commands still send”（命令仍会发送）：从冻结的看板发出的命令会立即发送。

## 展示模式与墙面显示器链接 {#kiosk-mode-and-links-for-wall-displays}

查看器的地址可以携带参数，这样书签或墙面显示器就能以正确的模式打开正确的看板：

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark
```

| 参数 | 取值 | 效果 |
| --- | --- | --- |
| `dashboard` | 仪表板令牌 | 登录后（包括单点登录后）直接加载该仪表板。 |
| `kiosk` | `1` 或 `true` | 展示模式：隐藏页眉及其控件，改为显示 **Fullscreen**（全屏）按钮。 |
| `chrome` | `0` 或 `false` | 与 `kiosk` 一起使用时，同时隐藏连接指示器和 **Fullscreen** 按钮，适用于自行为看板加框的页面。只要看板不处于实时状态，指示器仍会出现。 |
| `theme` | `dark` 或 `light` | 设置主题。与主题按钮不同，该参数不会被记住。 |
| `density` | `compact`、`comfortable` 或 `wall` | 缩放看板的文字和间距：`compact` 更小，`wall` 更大，适合从房间另一端观看的显示器。会覆盖看板保存时的密度。 |
| `range` | `15m`、`1h`、`6h`、`24h` 或 `7d` | 以该时间范围打开看板，而不是看板自带的范围。 |
| `paused` | `1` | 在加载的那一刻冻结看板并显示 **Paused** 徽标。其他任何取值都以实时方式打开。 |
| `bind.<slot>` | 设备令牌 | 以为该 slot 选好的设备打开看板。参见[打开已选好设备的看板](#linking-to-a-device)。 |

取值不被查看器识别的参数会被忽略，其他参数仍然生效。

在展示模式下，看板占满整个页面。连接指示器位于左上角。唯一的控件是右上角的 **Fullscreen**，它会变成 **Exit fullscreen**（退出全屏）；如果浏览器无法进入全屏，则会隐藏。展示模式下没有退出登录控件，因此墙面显示器会一直停留在其看板上。展示模式同样没有时间范围和暂停控件，但以 `paused=1` 打开的看板仍会在连接指示器旁显示 **Paused** 徽标，所以冻结的墙面显示器会如实说明。

<GuideShot id="dash-kiosk" alt="展示模式下的 Widget Lab 看板：没有页眉，左上角有 Live 指示器，右上角有 Fullscreen 按钮，下方是标题卡片和同样的实时组件。" />

## 打开已选好设备的看板 {#linking-to-a-device}

链接还可以通过指明看板的 slot 和设备令牌，直接打开已选好设备的看板：

```text
/dash/?dashboard=<token>&bind.machine=hauler-7
```

每个要设置的 slot 重复一次 `bind.<slot>`。`<slot>` 是看板作者给该 slot 起的名称。查看器会先用你自己的权限检查链接中的每台设备是否存在且你有权查看，通过之后才显示看板；否则它会说明原因，而不是显示看板。设备不存在和你无权查看两种情况的提示相同，因此无法借助链接探测哪些设备存在。随后 **Open the board without it**（不带它打开看板）会以看板自己的默认设备打开。指向看板上没有的 slot、或不是设备令牌的取值，同样会被拒绝。

在看板上选择设备（点击地图标记、设备表中的一行或选择器）会就地更新地址，不会新增浏览器历史记录，因此地址栏中的地址始终是指向当前画面的链接。通过 **Back** 或 **Forward** 回到指明另一台设备的地址时，会选择那台设备。**Change**（返回加载表单）会把已选设备从地址中去掉。同样的链接也适用于控制台，地址为 `/dashboards/<token>?bind.<slot>=<设备令牌>`，参见[选择设备并分享链接](/user-guide/dashboards#sharing-a-link)。

## 保持登录状态 {#staying-signed-in}

墙面显示器可能无人值守地运行数天，因此查看器会在会话到期前几分钟自动续期。网络短暂中断（例如 Wi-Fi 重新连接）不会结束会话：查看器会不断重试，直到当前的访问权限失效。

如果会话无法续期，原因是登录已在服务器上被终止，或中断持续太久，看板会被全屏的 **Your session has ended**（你的会话已结束）通知取代，并带有 **Sign in again**（重新登录）按钮。显示器绝不会停留在过时的数值上。当地址带有 `dashboard` 参数时，重新登录会直接带回同一块看板。

更多细节见[会话刷新与展示模式](/concepts/dashboards#session-refresh-and-kiosk-mode)。

## 相关内容 {#related}

- [仪表板](/user-guide/dashboards)：在控制台中构建、发布和导出仪表板。
- [仪表板](/concepts/dashboards)：组件、数据源、版本管理和嵌入。
- [登录](/user-guide/signing-in)：控制台自己的登录。
