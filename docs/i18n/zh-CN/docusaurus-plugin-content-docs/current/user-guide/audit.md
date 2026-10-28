---
title: 审计
sidebar_position: 13
---

import GuideShot from '@site/src/components/GuideShot';

# 审计 {#audit}

**Audit**（审计）界面是租户对其注册表变更的日志：谁在何时更改了什么。平台会为它对注册表执行的每次创建、更新和删除记录一条条目；条目与变更一起保存，因此如果条目无法写入，该变更不会生效，调用会报告失败。连接器、仪表板、规则和更新的变更不在此日志中。条目从不被编辑。你可以用此界面回答诸如“谁删除了那台设备？”或“这个脚本昨晚创建了什么？”之类的问题。本章面向租户管理员以及审查租户活动的人员。

打开侧边栏中的 **Access → Audit**。只有你的角色被授予 `audit:read` 权限时才会出现。日志只显示你自己租户的条目。

## 阅读日志 {#reading-the-journal}

最新的条目在前，每页 25 条。每一行显示：

| 列 | 含义 |
| --- | --- |
| Time | 变更发生的时间。 |
| Actor | 执行者：已登录的身份，通常是电子邮件地址。 |
| Operation | **Create**（创建）、**Update**（更新）或 **Delete**（删除）。 |
| Table | 发生变更的记录类型，例如 `devices`、`assets`、`device_credentials` 或 `alarms`。 |
| Target | 发生变更的记录的标签，通常是其令牌或 ID。 |
| Rows | 该变更影响的记录数。 |

<GuideShot id="audit-journal" alt="按 Create 操作和某个执行者过滤后的 Audit 界面。每行显示时间、执行者的电子邮件地址、绿色的 Create 徽章、表名（如 device_replacements、device_credentials、devices、entity_relationships 或 assets）、目标，以及 1 row。" />

并非每一行都对应有人保存的表单。例如，确认告警是对 `alarms` 表的一次更新，以确认者的身份执行。

## 筛选 {#filtering}

右上角有两个筛选器：

- **All operations**（全部操作）选择器将日志缩小到 **Create**、**Update** 或 **Delete**。清除后即可再次看到所有操作。
- **Filter by actor…**（按执行者筛选）匹配包含你所输入内容的任何执行者，忽略大小写。输入地址的一部分即可。

<GuideShot id="audit-filter" alt="操作选择器已展开的 Audit 界面，提供 Create、Update 和 Delete。下方的日志显示 alarms 表上的 Update 行。" />

两个筛选器可以同时使用，更改任何一个都会回到第一页。

## 导出 {#exporting}

**Export CSV**（导出 CSV）下载符合当前筛选条件的所有条目，而不只是屏幕上的当前页。文件的列与表格相同，操作以 `create`、`update` 或 `delete` 表示，表名使用完整名称。导出最多 10,000 行；达到上限时控制台会提示，你可以缩小筛选范围导出其余部分。导出会显示进度，并且可以取消；导出过程中更改筛选器会放弃该导出。

## 相关内容 {#related}

- [架构](/concepts/architecture)：平台的服务和数据如何组合在一起。
- [多租户](/concepts/multi-tenancy)：一个租户的记录如何与其他租户隔离。
