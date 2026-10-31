---
title: 登录
sidebar_position: 1
---

import GuideShot from '@site/src/components/GuideShot';

# 登录

本章介绍如何进入控制台，以及登录后可以修改自己账户的哪些内容：登录步骤、单点登录、选择租户、会话过期后会发生什么，以及用户菜单。

本章面向所有使用控制台的人。实例超级用户登录的是实例管理（Admin）控制台，其中没有租户页面；要使用租户控制台，需要租户管理员身份：首个租户可由 `dcctl sim create` 创建，或由管理员通过管理 API 的 `createIdentity` 和 `addMembership` 创建。账户由管理员提供，无法在登录页自行创建。

## 登录步骤 {#the-sign-in-steps}

登录页先要求输入邮箱地址，之后才会要求其他信息。

<GuideShot id="signin-identify" alt="控制台登录页。DeviceChain 徽标和“Sign in to the management console”下方是一张卡片，包含 Email 输入框、Continue 按钮和 Sign in with SSO 按钮。卡片下方有语言切换器，可选 English 和 Español。" />

1. 输入邮箱地址，然后选择 **Continue**（继续）。
2. 控制台会检查你的组织是否已为你的邮箱域名配置单点登录：
   - 如果没有，会要求输入密码。
   - 如果有，页面会提示“This address signs in through your organization's single sign-on.”（该地址通过你所在组织的单点登录进行登录）。选择 **Continue with single sign-on**（使用单点登录继续）前往身份提供商。如果你的账户只有密码，请选择 **Use a password instead**（改用密码）。
3. 输入密码，然后选择 **Sign in**（登录）。

<GuideShot id="signin-password" alt="登录的第二步。Email 输入框现在显示已输入的地址，下方依次是空的 Password 输入框、Sign in 按钮、Sign in with SSO 按钮和 Use a different email 链接。" />

完成第一步后，邮箱地址仍显示在屏幕上，但不能再编辑。若要改用其他地址重新开始，请选择 **Use a different email**（使用其他邮箱）。

第 2 步的检查针对的是邮箱的**域名**，而不是你本人。无论该地址是否存在对应账户，得到的结果都相同，因此无法借此探知谁拥有账户。

如果邮箱或密码有误，控制台会提示“Invalid email or password.”，不会说明是哪一项错误。多次登录失败后，会要求你稍等片刻再试。

### 选择语言 {#choosing-a-language}

登录卡片下方的语言切换器用于设置控制台的语言：**English** 或 **Español**。所选语言会保存在当前浏览器中并立即生效。之后也可以在[用户菜单](#the-user-menu)中再次更改。

## 单点登录 {#single-sign-on}

如果你的组织通过身份提供商登录，可以在任一步骤中选择 **Sign in with SSO**（使用 SSO 登录）。请先输入邮箱地址：控制台需要它来找到你所在组织的提供商。你会被带到提供商页面，完成登录后回到控制台。

如果你的地址没有配置单点登录，控制台会给出提示，并改为显示密码输入框。

单点登录的工作原理以及管理员如何配置，参见[单点登录](/concepts/single-sign-on)和[配置单点登录](/guides/configuring-sso)。

## 双重身份验证 {#two-factor}

如果你的账号设有双重身份验证，输入正确的密码还不够。控制台接下来会要求输入**验证码**：

- 输入验证器应用中的六位代码。一个代码只能用一次，所以如果刚用过一个，请等待下一个。
- 或者选择 **Use a recovery code instead（改用恢复码）**，输入设置时保存的恢复码之一。它只能用一次。

如果该步骤超时（五分钟、五次尝试），你会回到密码步骤。要开启它，请打开用户菜单并选择 **Account security（账号安全）**；步骤见[双重身份验证](/guides/two-factor-authentication#enrol)。如果你的组织要求使用而你尚未设置，登录后控制台会带你前往设置。

## 选择租户 {#choosing-a-tenant}

租户是容纳贵组织设备、规则和仪表板的工作空间。一个账户可以属于多个租户。

- 如果你只属于**一个**租户，会直接进入该租户。
- 如果你属于**多个**租户，控制台会显示“Choose a tenant to continue”（选择租户以继续），每个租户对应一个按钮。选择要进入的租户即可。**Back**（返回）会回到登录表单。
- 要求单点登录的租户仍会列出，并标注“Requires single sign-on”（需要单点登录），但使用密码登录后无法进入。请改用 SSO 登录。
- 如果你的账户尚未加入任何租户，控制台会提示这一点。请联系管理员将你加入某个租户。

实例运维人员（超级用户）登录时不选择租户，而是进入管理控制台，详见[管理实例](/user-guide/administering-the-instance)。

## 会话过期 {#when-your-session-expires}

会话不会永久有效。如果你离开的时间足够长，会话失效后，控制台会把你带回登录页并说明原因。

<GuideShot id="signin-session-expired" alt="登录页，卡片上方有一条提示：“Your session expired. Please sign in again.”，Email 输入框为空。" />

重新登录即可继续。如果你用密码登录期间，租户开始要求单点登录，提示会改为说明这一点，并要求你使用 **Sign in with SSO**。

## 用户菜单 {#the-user-menu}

你的姓名和邮箱地址位于侧边栏底部。选择它们即可打开用户菜单。

<GuideShot id="user-menu" alt="控制台主页，左下角打开了用户菜单。菜单显示用户姓名和邮箱，一个带太阳、月亮和显示器图标的三选一主题控件，一个语言控件（已选 English，另有 Español），以及 Edit profile、Link single sign-on account 和 Sign out 三项。" />

在用户菜单中可以：

- **更改主题。** 三个图标依次是 **Light**（浅色）、**Dark**（深色）和 **System**（跟随系统）。System 跟随你计算机的设置。
- **更改语言**，与登录页相同。
- **编辑个人资料**（见下文）。
- **将账户关联到单点登录。** **Link single sign-on account**（关联单点登录账户）会把你现有的账户与组织的身份提供商关联起来，此后即可通过它登录。此操作需要最近一次密码登录，因此通过单点登录登录时不提供该选项。参见[关联](/concepts/single-sign-on#linking)。
- **退出登录**，即 **Sign out**（退出登录）。

实例运维人员还会看到 **Admin console**（管理控制台），在实例提供该功能时还会看到 **Metrics**（指标）。

### 编辑个人资料 {#editing-your-profile}

**Edit profile**（编辑个人资料）会打开一个面板，用于修改控制台显示的你的名字。

<GuideShot id="user-profile-drawer" alt="屏幕右侧打开的 Edit profile 面板，说明文字为“Update your display name.”。面板显示灰色的 Email 输入框及提示“Your sign-in identity; it can't change.”，以及 First name、Last name 输入框和 Save changes 按钮。" />

修改 **First name**（名）和 **Last name**（姓），然后选择 **Save changes**（保存更改）。邮箱地址是你的登录身份，因此只显示，不能在此修改。

## 相关内容 {#related}

- [单点登录](/concepts/single-sign-on)
- [配置单点登录](/guides/configuring-sso)
- [多租户](/concepts/multi-tenancy)
