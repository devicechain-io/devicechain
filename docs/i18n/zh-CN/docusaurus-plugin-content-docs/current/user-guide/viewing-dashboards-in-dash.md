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

登录后，**Load a dashboard**（加载仪表板）会要求输入 **Dashboard token**（仪表板令牌）。输入仪表板的令牌（可在控制台中其页面上复制），然后选择 **Load published**（加载已发布版本）。查看器会获取最近一次发布的版本。从未发布过的仪表板无法在这里打开；查看器会提示你请作者发布它。

<GuideShot id="dash-load" alt="Load a dashboard 页面。Dashboard token 字段为 wl-gallery，提示为 Fetches the version of this dashboard that was last published，并有“Load published”按钮。下方的 Or paste an exported definition 下有 Definition (JSON) 框、带有结构示例的 Binding manifest (JSON, optional) 框，以及 Render 按钮。页眉有语言按钮和 Sign out。" />

下半部分接受你从控制台导出的仪表板：粘贴定义，以及可选的、将其槽位指向特定设备的绑定清单，然后选择 **Render**（渲染）。参见[导出](/user-guide/dashboards#exporting)，绑定清单请参阅[嵌入](/concepts/dashboards#embedding-definitions-slots-and-binding-manifests)。

## 看板 {#the-board}

加载后的看板占满窄页眉下方的整个页面。页眉显示仪表板的标题，并提供语言切换、**Dark theme**（深色主题；或 **Light theme**，浅色主题）、返回加载表单的 **Change**（更换），以及 **Sign out**（退出登录）。

<GuideShot id="dash-view" alt="/dash 查看器中的 Widget Lab 看板，浅色主题。页眉显示 Widget Lab 以及 English、Español、Dark theme、Change 和 Sign out 按钮。下方有：标题卡片、35.0 C 的温度读数、温度仪表、Active alarms 计数 2（最高严重度 Major）、一张图片、温度和湿度折线图、Latest values 表格、含两条 Major 活动告警并带 Ack 和 Clear 按钮的 Alarms 表格，以及带有“Send Set Setpoint”按钮的 Setpoint 命令表单。" />

组件是实时的：数值、图表和告警随数据到达而更新。查看器无法修改仪表板，但组件的操作对有权使用它们的人仍然有效。角色被授予 `alarm:write` 的人可以在看板上确认和清除告警，拥有 `command:write` 的人可以向真实设备发送命令。服务器会对每个操作检查这些权限。参见[操作与权限](/concepts/dashboards#actions-and-permissions)。

仪表板和组件的标题是仪表板作者写下的文本，因此无论查看器设置为何种语言，都保持原样。

### 浅色与深色 {#light-and-dark}

主题按钮在浅色和深色之间切换看板，此浏览器会记住你的选择供下次使用。

<GuideShot id="dash-dark" alt="同一块 Widget Lab 看板的深色主题：深色背景、浅色文字，页眉按钮现在显示 Light theme，图表、仪表、告警表格和命令表单都针对深色背景重新设计了样式。" />

## 展示模式与墙面显示器链接 {#kiosk-mode-and-links-for-wall-displays}

查看器的地址可以携带参数，这样书签或墙面显示器就能以正确的模式打开正确的看板：

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark
```

| 参数 | 取值 | 效果 |
| --- | --- | --- |
| `dashboard` | 仪表板令牌 | 登录后（包括单点登录后）直接加载该仪表板。 |
| `kiosk` | `1` 或 `true` | 展示模式：隐藏页眉及其控件，改为显示 **Fullscreen**（全屏）按钮。 |
| `theme` | `dark` 或 `light` | 设置主题。与主题按钮不同，该参数不会被记住。 |
| `density` | `compact`、`comfortable` 或 `wall` | 可被接受，但目前不会改变布局。 |

取值不被查看器识别的参数会被忽略，其他参数仍然生效。

在展示模式下，看板占满整个页面。唯一的控件是右上角的 **Fullscreen**，它会变成 **Exit fullscreen**（退出全屏）；如果浏览器无法进入全屏，则会隐藏。展示模式下没有退出登录控件，因此墙面显示器会一直停留在其看板上。

<GuideShot id="dash-kiosk" alt="展示模式下的 Widget Lab 看板：没有页眉，顶部是标题卡片，其右上角有 Fullscreen 按钮，下方是同样的实时组件，包括带 Ack 和 Clear 的告警表格以及 Setpoint 命令表单。" />

## 保持登录状态 {#staying-signed-in}

墙面显示器可能无人值守地运行数天，因此查看器会在会话到期前几分钟自动续期。网络短暂中断（例如 Wi-Fi 重新连接）不会结束会话：查看器会不断重试，直到当前的访问权限失效。

如果会话无法续期，原因是登录已在服务器上被终止，或中断持续太久，看板会被全屏的 **Your session has ended**（你的会话已结束）通知取代，并带有 **Sign in again**（重新登录）按钮。显示器绝不会停留在过时的数值上。当地址带有 `dashboard` 参数时，重新登录会直接带回同一块看板。

更多细节见[会话刷新与展示模式](/concepts/dashboards#session-refresh-and-kiosk-mode)。

## 相关内容 {#related}

- [仪表板](/user-guide/dashboards)：在控制台中构建、发布和导出仪表板。
- [仪表板](/concepts/dashboards)：组件、数据源、版本管理和嵌入。
- [登录](/user-guide/signing-in)：控制台自己的登录。
