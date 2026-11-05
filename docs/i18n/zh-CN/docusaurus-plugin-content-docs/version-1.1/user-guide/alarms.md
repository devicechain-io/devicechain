---
title: 告警
sidebar_position: 6
---

import GuideShot from '@site/src/components/GuideShot';

# 告警

告警是检测规则在某件事需要人来关注时触发的内容。**Alarms**（告警）页面列出租户中的所有告警，并随着告警的触发、变化和解除而实时更新。本章面向监视和处理告警的运维人员：阅读列表、筛选、确认和清除告警，以及导出列表。

## 告警列表 {#the-alarm-list}

在侧边栏中选择 **Monitor → Alarms**。每一行是一条告警。

<GuideShot id="alarms-list" alt="Alarms 页面，带有 All states、All severities 和 Any acknowledgement 筛选器以及 Export CSV 按钮。描述旁有一个绿色的 Live 指示。表格列出恒温器 bp-therm-001 至 bp-therm-012 在 temperature 指标上的 Major 级 bp-over-temp 告警：三条为 Active，带 Acknowledge 和 Clear 按钮，其余为 Cleared。" />

各列含义：

- **Severity**（严重程度）：Critical（严重）、Major（主要）、Minor（次要）、Warning（警告）或 Indeterminate（不确定），由触发告警的规则设定。
- **Status**（状态）：条件成立期间为 **Active**（活动），条件解除或有人清除后为 **Cleared**（已清除），已有人接手的活动告警为 **Acknowledged**（已确认）。
- **Originator**（来源）：触发告警的设备。
- **Alarm**（告警）：告警键，下方是指标。
- **Value**（值）：告警看到的最新读数。
- **Raised**（触发时间）：告警被触发的时间。
- **Actions**（操作）：针对活动告警的 **Acknowledge**（确认）和 **Clear**（清除）。

描述旁的 **Live**（实时）指示表示页面正在实时接收变化。如果连接中断，它会显示正在重新连接，恢复后页面会自行追上。页面还会每 30 秒自动刷新一次，因此遗漏的变化不会长期滞留。

一条告警始终是一行。如果条件停止后又重新出现，同一条告警会再次变为活动，而不会新增一行。共用同一告警键的规则还可以提高或降低同一条告警的严重程度。[告警生命周期](/concepts/event-processing#the-alarm-lifecycle)对这两点都有说明。

## 筛选 {#filtering}

表格上方有三个筛选器：状态（**Active** 或 **Cleared**）、严重程度，以及确认情况（**Unacknowledged**（未确认）或 **Acknowledged**）。每个筛选器都会打开一个可搜索的简短列表。

<GuideShot id="alarms-filter" alt="Alarms 页面，状态筛选器已展开，提供 Active 和 Cleared，位于告警列表上方。" />

筛选器可以组合。例如，选择 **Active** 和 **Unacknowledged** 可以看到还没有人接手的告警。点击筛选器内的 × 即可清除该筛选。

## 确认与清除 {#acknowledging-and-clearing}

**Acknowledge** 表示“我来处理”。告警保持活动状态，状态变为 **Acknowledged**，平台会记录谁在何时确认了它。确认还会停止对该告警的重复通知。

**Clear** 用于手动关闭告警。大多数告警会在规则条件不再成立时自行清除，所以你很少需要它，但对于你确知已经解决的告警，可以用它来关闭。

<GuideShot id="alarms-acknowledged" alt="筛选为 Acknowledged 的 Alarms 页面，显示一条 Major 告警，即 bp-therm-011 上的 bp-over-temp，状态为 Acknowledged，带 Clear 按钮。显示“Showing 1–1 of 1 result.”。" />

需要了解的几点：

- 已确认的告警保留 **Clear** 按钮，但不再有 **Acknowledge**。
- 如果已确认的告警被清除，之后其条件又重新出现，平台会重新打开同一条告警，并清除其确认状态，因此需要再次确认。
- 只有你的角色允许修改告警时才会出现这些按钮。没有该权限时，页面是只读的。

## 导出列表 {#exporting-the-list}

**Export CSV**（导出 CSV）会保存所有符合当前筛选条件的告警，而不仅是你正在查看的这一页，最多 10,000 行。如果符合条件的告警更多，控制台会提示导出在上限处停止，你可以收窄筛选条件来获取其余部分。文件包含每条告警的令牌、严重程度、状态、是否已确认、来源、告警键、指标、值，以及触发和清除的时间。

## 相关内容 {#related}

- [检测规则](/user-guide/detection-rules)，这些告警由它触发
- [事件处理与告警](/concepts/event-processing#the-alarm-lifecycle)：告警生命周期与升级
- [配置通知渠道](/guides/notification-channels)，让告警通过邮件发送或发布到 Webhook
- [通知策略](/user-guide/notification-policies)，用于在静默时段内暂存策略的通知
