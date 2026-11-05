---
title: 控制台概览
sidebar_position: 2
---

import GuideShot from '@site/src/components/GuideShot';

# 控制台概览

本章简要介绍每个页面都会出现的部分：主页、侧边栏、带租户标识的页眉，以及当控制台的某个部分在你的安装环境中不可用时看到的提示。

本章面向刚接触控制台的读者。读完这一章，后面的章节会更容易理解。

## 主页 {#the-home-page}

登录后会进入主页。主页会按姓名问候你，并显示两个数字：租户中有多少台 **Devices**（设备）和多少个 **Device Types**（设备类型）。选择任一数字即可打开对应列表。

<GuideShot id="console-home" alt="控制台主页。左侧是侧边栏。页眉显示 Home，右上角是租户标识“acme (sim)”。页面标题为“Welcome, Dana Okafor”，显示两张卡片：13 Devices 和 1 Device Types。" />

侧边栏顶部的 **Home**（主页）始终会带你回到这里。它与 **Dashboards** 不同，后者存放团队创建的仪表板。

## 侧边栏 {#the-sidebar}

左侧的侧边栏是你在控制台中导航的方式。它按固定顺序列出租户的各个功能区。

<GuideShot id="console-sidebar" alt="控制台侧边栏。DeviceChain 徽标和 Management Console 字样下方列出 Home，以及六个可折叠的分区：Monitor、Devices、Connect、Organization、Access 和 Settings。每个分区标题都带有箭头，表示可展开和收起。底部是当前登录用户的姓名和邮箱。" />

**Home**（主页）是单个页面。其余每个条目都是一个**可折叠分区**，把相关页面归为一组。选择分区标题即可展开或收起，选择其中的页面即可进入该页面。

| 条目 | 内容 |
| --- | --- |
| **Home** | 主页。 |
| **Monitor**（监控） | 团队创建的 **Dashboards**（仪表板）、规则已触发的 **Alarms**（告警），以及 **Notification Policies**（通知策略）：告警会通知谁，以及暂存这些通知的静默时段。 |
| **Devices** | **Devices**、**Device Types**（设备类型）、**Device Profiles**（设备配置文件，包含该配置文件的规则、指标和命令）、**Device Groups**（设备分组）、**Command Batches**（命令批次）、**Maintenance Windows**（维护窗口）和 **Updates**（更新，其中的 **Artifacts**（制品）、**Rollouts**（发布）和 **Signers**（签名者）用于空中升级）。 |
| **Connect**（连接） | 设备和其他系统接入租户的方式：**Provisioning Profiles**（预配置文件）、**Trust Anchors**（信任锚）、**Integrations**（集成）和 **Connectors**（连接器，通向其他系统的出站连接）。 |
| **Organization**（组织） | **Assets**（资产）、**Customers**（客户）和 **Areas**（区域，含 **Geofences** 地理围栏）；**Facets**（分面），即用于描述和筛选实体的键；以及 **Browse**（浏览），按分面查找实体的查询构建器。 |
| **Access**（访问） | **Members**（成员）、**Service Accounts**（服务账号）、**Roles**（角色）、**Sign-in Security**（登录安全）和 **Audit**（审计，租户内“谁修改了什么”的记录）。 |
| **Settings**（设置） | **Branding**（品牌）、**Map**（地图）和 **Language**（语言）。 |

你只会看到角色允许访问的条目。例如，如果你的角色无权读取告警，侧边栏中就没有 **Alarms**。只有当你的安装环境运行了更新服务时，才会出现 **Updates**。**Access** 和 **Settings** 两个分区面向租户管理员。

如果你的访问权限仅限于租户的部分客户，侧边栏只有 **Home**、**Dashboards**、**Alarms** 和 **Devices**。参见[客户用户能看到什么](/user-guide/members-roles-and-customer-users#what-a-customer-user-sees)。

你的姓名和邮箱地址位于侧边栏底部，选择它们会打开用户菜单，参见[登录](/user-guide/signing-in#the-user-menu)。

若想给页面腾出更多空间，可以使用页眉左侧的按钮，把侧边栏折叠成仅含图标的窄条。再次使用该按钮即可展开。

## 页眉与租户标识 {#the-page-header-and-the-tenant-chip}

每个页面都有一个页眉栏。左侧是你所在部分的名称。在详情页（例如某一台设备）上，右侧的链接可以回到列表。

最右侧是租户标识，显示你当前所在的租户：上方是租户名称，下方是它的令牌。

<GuideShot id="console-tenant-chip" alt="租户标识：一张小卡片，包含建筑图标、租户名称“acme (sim)”，下方是等宽字体显示的令牌“sim-acme”。" />

如果租户上传了徽标，标识中会用徽标取代建筑图标。如果你的访问权限仅限于租户的部分客户，第二行会显示这些客户和你的角色，而不是令牌：例如 **Acme · viewer**，即客户名称，然后是你角色的令牌。你在控制台中看到和修改的一切都属于这个租户。若要切换到其他租户，请退出登录，再次登录时选择该租户。

## 功能区不可用时 {#when-an-area-is-unavailable}

DeviceChain 由多个服务组成，安装者决定运行哪些服务。其中一些是可选的，例如 AI 服务或出站连接器服务。如果某个页面需要你的安装环境未运行的服务，该页面会显示 **Not deployed**（未部署），而不是通常的内容。

<GuideShot id="console-area-unavailable" alt="管理控制台的 AI Providers 页面，显示标题为“Not deployed”的空状态。文字说明该实例部署时未包含 ai-inference 服务，因此该功能不可用，会话本身没有问题，并说明由管理员选择实例运行哪些服务。" />

提示信息会指出缺少的服务。这并不表示你的会话或账户有问题。如果需要该功能，请根据提示中的服务名称联系管理员。

截图展示的是实例管理页面，租户页面的表现相同。例如，出站连接器服务未运行时，**Connectors** 会显示这条提示。

## 相关内容 {#related}

- [架构](/concepts/architecture)：构成 DeviceChain 的各项服务
- [多租户](/concepts/multi-tenancy)
- [登录](/user-guide/signing-in)
