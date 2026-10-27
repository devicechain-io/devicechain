---
title: 实例管理
sidebar_position: 15
---

import GuideShot from '@site/src/components/GuideShot';

# 实例管理 {#administering-the-instance}

本章面向**实例运维人员**：为整个 DeviceChain 安装环境中的所有人运维该环境的超级用户。如果你只在某个租户内工作，就看不到这些界面，可以跳过本章。

管理控制台用于创建和删除租户、决定每个租户的套餐层级、管理登录的人员及其角色、接入外部身份提供商、审阅整个实例中发生的变更，以及设置实例范围的默认值。

## 打开管理控制台 {#opening-the-admin-console}

超级用户登录后会直接进入管理控制台，无需选择租户。在租户控制台中，用户菜单也提供 **Admin console**（[用户菜单](/user-guide/signing-in#the-user-menu)）。管理控制台只对超级用户开放；其他人会被送回登录页面。

侧边栏在标志下方显示 **Admin Console**（管理控制台），页眉右侧有一个 **Admin**（管理）徽章。**Platform**（平台）下的条目有：

- **Tenants**（租户），包含 **List**（列表）、**Tiers**（层级）和 **Deletions**（删除）。
- **Identities**（身份），包含 **List**、**Roles**（角色）和 **Identity providers**（身份提供商）。
- **AI Providers**（AI 提供商）、**Audit**（审计）和 **Settings**（设置）。

## 租户 {#tenants}

**Tenants → List**（租户 → 列表）是实例的租户注册表。租户是平台保存的一条记录，而不是平台去部署的东西：创建租户立即生效，不需要新的服务器或服务。

<GuideShot id="admin-tenants" alt="Tenants 列表，共三行。harbor-view-pilot（名称 Harbor View pilot）属于 pilot 层级，显示 Disabled 徽章和红色的 Deleting since 10/14/2026 徽章。sim-acme 和 sim-widgets 属于 silver 层级，状态为 Enabled。三行的 Config 列均为空。右上角有“New tenant”按钮。" />

每一行显示租户的令牌和名称、以彩色标签表示的层级、状态以及任何自由格式的配置。正在删除的租户保留其行，标记为 **Deleting**（删除中）及删除开始的日期，直到删除完成。

### 创建租户 {#creating-a-tenant}

选择 **New tenant**（新建租户）。表单需要填写：

- **Token**（令牌）：租户在整个平台中的 ID。它根据名称，按实例的[令牌掩码](/concepts/system-settings#token-masks)生成，掩码显示在字段下方，旁边的按钮可再生成一个。之后不可更改。
- **Name**（名称）：显示名称。
- **Tier**（层级）：租户所遵循的套餐（参见[层级](#tiers)）。每个租户都有一个层级。
- **Config (JSON)**（配置，JSON）：可选的自由格式 JSON 对象。
- 租户自己的限额，见下文[租户的设置](#a-tenants-settings)。留空则继承。

然后选择 **Create tenant**（创建租户）。

<GuideShot id="admin-tenant-new" alt="New tenant 表单。Token 字段显示掩码 {slug} 和重新生成按钮，提示为 Pattern: {slug}。下方是 Name（占位符 Acme Corp）、显示 Select a tier… 的 Tier 选择器、Config (JSON)、Ingest rate 和 Ingest burst、Outbound rate 和 Outbound burst（均显示 default），以及未勾选的 External AI routing 复选框。" />

### 租户页面 {#a-tenants-page}

打开租户即可编辑它。页面显示名称及旁边的令牌、层级和状态徽章，以及两个操作：**Disable**（禁用；已禁用时为 **Enable**，启用）和 **Delete**（删除）。被禁用的租户不允许任何人进入，直到你重新启用。

选项卡上方的 **Single sign-on**（单点登录）面板说明该租户的人员是否必须通过单点登录登录。**Require SSO**（要求单点登录）会先让你确认，因为租户的密码会话会在下次刷新时结束。除非至少有一个已启用的身份提供商连接为该租户服务，否则会被拒绝。超级用户不受此限制，因此身份提供商故障不会把你锁在外面。参见[要求单点登录](/concepts/single-sign-on#require-sso)。

**Basic**（基本信息）选项卡可编辑名称、层级和配置。令牌可见但不可更改。更改层级会在一分钟内生效。

<GuideShot id="admin-tenant-basic" alt="租户 acme (sim) 的页面，令牌 sim-acme，带有 silver 和 Enabled 徽章，以及 Disable 和 Delete 按钮。Single sign-on 面板说明该设置并显示 Not required，带有“Require SSO”按钮。下方的选项卡为 Basic、Settings、Effective settings 和 AI models，当前打开 Basic：Token sim-acme（灰色）、Name acme (sim)、Tier Silver 和空的 Config (JSON) 字段。" />

租户页面还有一个 **Two-factor authentication（双重身份验证）** 面板，带有 **Require two-factor（要求双重验证）**（或 **Stop requiring（停止要求）**）。它对租户的密码登录要求第二因素；当租户的层级已要求时，该按钮不可用。超级用户不受约束，单点登录遵循其连接自身的要求。参见[双重身份验证](/guides/two-factor-authentication#require)。

### 租户的设置 {#a-tenants-settings}

**Settings** 选项卡保存这个租户所遵循的限额，适用于需要与其层级不同的情形。留空的字段会继承：先取层级的值，再取平台默认值。这些字段包括摄取速率和突发量、出站连接器的出站速率和突发量、**External AI routing**（租户是否同意把规则描述发送给外部 AI 模型，默认关闭）、AI 起草速率和突发量、**Held command ceiling**（暂存命令上限）、**Shed priority**（负载削减优先级）以及三个地理围栏限额：**Geofence count**（地理围栏数量）、**Points per geofence**（每个地理围栏的角点数）和 **Geofence point budget**（地理围栏角点总预算）。控制台中的每个字段都带有简短说明。**Save changes**（保存更改）会同时保存 Basic 和 Settings 选项卡。

<GuideShot id="admin-tenant-settings" alt="acme (sim) 的 Settings 选项卡：Ingest rate (readings/sec)、Ingest burst、Outbound rate (calls/sec) 和 Outbound burst，均显示 default；External AI routing 复选框“Allow external AI routing for NL→rule authoring”，未勾选；AI drafting rate (requests/min) 和 AI drafting burst；以及 Held command ceiling 的开头部分。" />

**Effective settings**（生效设置）选项卡是只读的。它显示摄取、出站和 AI 起草速率及突发量的结果：层级的值、此租户的覆盖值，以及实际生效的值，并用徽章标明来源。暂存命令上限、负载削减优先级和地理围栏限额以同样的方式解析，但此选项卡不列出它们。设置的回退顺序是：自身的覆盖值、层级、平台默认值。平台默认值是真实的限额，绝不是“无限制”。当值来自某个服务自身的默认值时，控制台显示 **Set by the service**（由服务设定）。说明中的层级名称链接到该层级。

<GuideShot id="admin-tenant-effective" alt="acme (sim) 的 Effective settings 选项卡。说明中以链接形式给出 Silver 层级。表格列出 Ingest rate、Ingest burst、Outbound rate、Outbound burst、AI drafting rate 和 AI drafting burst；Tier 和 Override 列显示短横线，每个 Effective 单元格显示 Set by the service 并带有 Platform default 徽章。" />

**AI models**（AI 模型）选项卡为租户的每项 AI 功能选择所用的 AI 模型。它需要可选的 AI 服务；在没有该服务的实例上，选项卡会说明该功能区未部署。限额和授权如何运作，请参阅[租户层级](/concepts/tenant-tiers)和[治理与配额](/concepts/governance)。

### 删除租户 {#deleting-a-tenant}

**Delete** 会打开确认框。请仔细阅读：删除无法撤销。租户的登录会立即被吊销，其设备、仪表板和历史数据随后在后台擦除，至少需要 12 小时。在此完成之前，租户的令牌保持保留状态。

<GuideShot id="admin-tenant-delete-confirm" alt="acme (sim) 页面上的“Delete tenant”确认框。内容为：Delete sim-acme? Sign-in is revoked immediately and this cannot be undone. 租户的设备、仪表板和历史数据随后在后台擦除，至少需要 12 小时。令牌 sim-acme 在此完成之前保持保留，完成后重新可用。有 Cancel 和 Delete 按钮。" />

删除进行期间，租户页面不再有 **Disable** 和 **Delete** 按钮以及单点登录面板，同时出现一个 **Deletion**（删除）选项卡。该选项卡以一行状态开头，说明删除正在等待什么。依次为：

1. **Waiting on**（正在等待）：仍保存着租户数据的一个或多个存储系统。
2. 所有存储系统都报告已清理完毕，删除再多等一小段时间，以确认没有更多数据到达。
3. 数据回收完成，令牌在重新可用之前处于保留期：默认约 12 小时。
4. 所有检查均已通过，删除将在下一次清扫时完成。

状态下方的表格中，租户有数据的每个存储系统占一行，并显示其状态：

- **Clean**（已清理）：那里没有留下该租户的任何东西。
- **Retaining**（保留中）：数据仍被保留，并会一直保留到有人更改某些设置为止。控制台知道修复方法时，会在该行下方说明该怎么做。
- **Retrying**（重试中）：一次尝试失败，下一次清扫会自动重试。

每行还显示已擦除的记录数和上次检查的时间。**Detail**（详情）列包含存储系统自己的备注。其中一些是很长的内部说明，解释系统有意不处理的内容，例如生命周期很短的登录记录；**Clean** 行上的备注只是脚注，不是需要处理的问题。删除进行期间，该选项卡会自动刷新；如果你刚删除后它是空的，说明第一次检查尚未运行，一分钟内就会开始。

<GuideShot id="admin-tenant-deletion" alt="Harbor View pilot 的页面，令牌 harbor-view-pilot，带有 pilot、Disabled 和 Deleting since 10/14/2026 徽章，没有操作按钮。Deletion 选项卡已打开。状态行写道：Data reclamation is complete. 令牌 harbor-view-pilot 约 12 小时后重新可用。表格列出存储系统，从 blob、broker、detect 和 kv 开始，每个均为 Clean，已擦除 0 条，并带有上次检查时间。kv 行的 Detail 列中有一条很长的技术备注。" />

### 删除列表 {#the-deletions-list}

**Tenants → Deletions**（租户 → 删除）按从新到旧的顺序列出实例上的所有租户删除。使用 **All**（全部）、**In progress**（进行中）和 **Completed**（已完成）按钮进行过滤。每一行显示令牌、删除的请求时间、状态（附带与该租户 Deletion 选项卡相同的一行摘要）、已擦除的记录数，以及已清理或仍保存数据的存储系统数量。

<GuideShot id="admin-deletions" alt="Tenant deletions 页面，带有 All、In progress 和 Completed 过滤按钮。一行：harbor-view-pilot，请求时间 10/14/2026 4:19:03 PM，状态为 In progress，附注数据回收已完成且令牌约 12 小时后重新可用，已擦除 0 条，6 个系统，全部为 clean。" />

这个列表是长期记录。删除完成后，租户的行和页面都会消失，因此已完成的删除只出现在这里。当有人要求你证明某个客户的数据已被擦除时，请打开它。完整时间线请参阅[租户删除](/deployment/tenant-deletion)。

## 层级 {#tiers}

层级是租户所购买的套餐：该层级的每个租户所遵循的默认限额，以及这些租户可使用哪些 AI 模型。**Tenants → Tiers**（租户 → 层级）列出它们，每个层级包括彩色标签、名称、描述和所含租户的数量。

<GuideShot id="admin-tiers" alt="Tiers 列表，共四行，每行都有拖动手柄：bronze（Bronze，入门套餐：争用时最先被卸载，AI 模型集合较少等，0 个租户）、gold（Gold，高级套餐：争用时最后被卸载，AI 模型集合最广等，0 个租户）、pilot（Pilot，限时评估：摄取上限较小，负载下最先被卸载，0 个租户）和 silver（Silver，标准套餐：平台的默认上限和授权，2 个租户）。每个层级标签有各自的颜色。右上角有“New tier”按钮。" />

拖动某行的手柄可更改层级的列出顺序。该顺序仅用于展示：它不是排名，也不据此计算任何内容。**New tier**（新建层级）创建层级。定义层级本身不会改变任何东西；只有在租户以该层级创建或迁移到该层级之后才会生效。

层级页面有三个选项卡。**Basic** 包含不可更改的令牌、名称、面向运维人员的描述，以及其标签的 **Color**（颜色）。颜色仅用于展示。

<GuideShot id="admin-tier-basic" alt="Pilot 层级的页面，令牌 pilot，带有 pilot 标签、0 个租户和 Delete 按钮。Basic 选项卡显示 Token pilot（灰色）、Name Pilot、描述“限时评估：摄取上限较小，负载下最先被卸载”、一排色板（其中青色被选中，旁边是预览标签），以及“Save changes”按钮。" />

**Settings** 保存层级的 **Ceilings**（上限）：摄取、出站和 AI 起草的速率及突发量。字段留空则使用平台默认值。仍可在租户自己的 Settings 选项卡上为其设置覆盖值。当层级下有租户时，保存后新的上限会在一分钟内对它们生效，无需重启。

层级还可以携带暂存命令上限、负载削减优先级和三个地理围栏限额，该层级的租户以同样方式继承它们。控制台没有在层级上提供这些字段：它们通过管理 API 设置，保存此选项卡时会保持原样。租户在其 Settings 选项卡上自己的值仍优先于层级的值。

<GuideShot id="admin-tier-settings" alt="Pilot 层级的 Settings 选项卡，标题为 Ceilings：Ingest rate 50、Ingest burst 100、Outbound rate 5、Outbound burst 10，AI drafting rate 和 AI drafting burst 均显示 platform default。有“Save changes”按钮。" />

**AI models** 选择该层级授予哪些 AI 模型，需要可选的 AI 服务。只要还有任何租户属于某个层级，该层级就不能删除。参见[租户层级](/concepts/tenant-tiers)。

层级的 **Settings** 选项卡也有 **Require two-factor authentication（要求双重身份验证）**。勾选后，该层级的每个租户都必须对密码登录使用第二因素，且租户无法关闭。参见[双重身份验证](/guides/two-factor-authentication#tier-floor)。

## 身份 {#identities}

**Identities → List**（身份 → 列表）是实例的人员目录。一个人就是一个以电子邮件地址为键的身份，可以是多个租户的成员。列表显示每个身份的电子邮件、姓名、状态、任何系统角色，以及所属租户的数量。

<GuideShot id="admin-identities" alt="Identities 列表：acme-properties.example 的 dana.okafor、jonas.weber 和 maria.chen，均为 Enabled；acme-properties.example 的 lee.park，为 Disabled；superuser@devicechain.local，没有姓名，为 Enabled，持有 superuser 系统角色，0 个租户；以及 sam.rivera@widgetlab.example，为 Enabled。这几个人各属于 1 个租户。右上角有“New identity”按钮。" />

**New identity**（新建身份）需要填写电子邮件、密码、名和姓，以及任何 **System roles**（系统角色）。系统角色控制对管理控制台及其 API 的访问；普通人员请留空。租户成员关系在身份创建之后再添加。

<GuideShot id="admin-identity-new" alt="New identity 表单，说明这是以电子邮件为键的全局主体，成员关系在创建后添加。字段：Email、Password、First name、Last name 和 System roles 选择器，以及“Create identity”（填写完整前禁用）和 Cancel 按钮。" />

身份页面顶部有 **Disable** 和 **Delete**。被禁用的身份无法登录，其会话在下次刷新时结束。删除会移除该身份及其所有成员关系。

- **Access**（访问）设置身份的 **System roles**（之后选择 **Save** 保存），并通过 **Set password**（设置密码）替换其密码。
- **Memberships**（成员关系）列出它所属的租户，每个都带有状态和租户角色。更改角色后选择 **Save roles**（保存角色），或对该成员关系执行 **Disable** 或 **Remove**（移除）。禁用成员关系只会让此人无法进入那一个租户。要添加成员关系，请在 **Add to tenant**（添加到租户）下选择租户和角色，然后选择 **Add membership**（添加成员关系）。

<GuideShot id="admin-identity-detail" alt="Maria Chen（maria.chen@acme-properties.example）的页面，状态为 Enabled，带有 Disable 和 Delete 按钮。Access 面板包含带 Save 的 System roles 选择器，以及带 Set 的 Set password 字段。Memberships 面板显示 sim-acme，Enabled，角色为 Facilities viewer，以及 Save roles、Disable 和 Remove 按钮。下方是 Add to tenant 和 Roles 选择器以及“Add membership”按钮。" />

列表有一个 **Two-factor（双重验证）** 列。在身份页面上，**Two-factor authentication（双重身份验证）** 区域显示它是否已开启，并为丢了手机的人提供 **Reset two-factor（重置双重验证）**：它会移除验证器和恢复码，并结束所有会话。如果该身份近期有过错误代码，其密码可能已被他人知晓，因此重置还会要求提供新密码。参见[双重身份验证](/guides/two-factor-authentication#reset)，以及无法登录的运维人员适用的 [break-glass](/guides/two-factor-authentication#break-glass)。 管理 API 的 `clearPassword` 也会移除此人的第二因素，因为没有密码后，它已无物可保护。

## 角色 {#roles}

**Identities → Roles**（身份 → 角色）是整个实例的角色目录。**system** 角色控制对管理控制台及其 API 的访问；**tenant** 角色控制成员在租户内可以做什么。每个角色列出其权限项（authorities），即它授予的权限。`*` 授予该角色范围内的全部权限。

<GuideShot id="admin-roles" alt="Roles 列表。superuser 是权限为 * 的 system 角色。facilities-viewer（Facilities viewer）是 tenant 角色，权限为 device:read、state:read、event:read、alarm:read、alarm:write、dashboard:read 和 location:read。tenant-admin（Tenant Administrator）是权限为 * 的 tenant 角色。viewer（Viewer）是 tenant 角色，权限为 device:read、event:read、state:read、command:read、alarm:read 和 dashboard:read。右上角有“New role”按钮。" />

打开某个角色可修改其名称、描述和 **Authorities**（权限项），然后选择 **Save changes**。角色一旦创建，其范围和令牌即固定。**New role**（新建角色）创建角色；**Delete** 会从所有持有者身上移除该角色。

租户自己的管理员会把这些租户角色分配给其成员，也可以定义自己的角色；这些角色只存在于该租户中，并且只携带租户权限项。他们无需管理控制台即可自行管理成员、邀请和角色；参见[成员、角色与客户用户](/user-guide/members-roles-and-customer-users)。

<GuideShot id="admin-role-detail" alt="facilities-viewer 租户角色。Scope 显示 tenant 已选中，system 不可用。Token facilities-viewer（灰色）、Name Facilities viewer、描述“读取设备、实时状态、遥测和告警，并确认告警。不改动其他任何内容。”Authorities 列出 device:read、state:read、event:read、alarm:read、alarm:write、dashboard:read 和 location:read。有“Save changes”按钮和 Delete 按钮。" />

## 身份提供商 {#identity-providers}

**Identities → Identity providers**（身份 → 身份提供商）用于接入外部 OpenID Connect 提供商（例如 Google Workspace 或 Microsoft Entra ID），人员可通过它们登录。列表显示每个连接的名称、种类、颁发者、它代表的电子邮件域、其端点是否已获批准，以及状态。连接初始为禁用，在有人批准提供商的端点之前无法启用。

<GuideShot id="admin-identity-providers" alt="Identity providers 页面，只有一个连接：Acme Google Workspace，种类 Tenant，颁发者 https://accounts.google.com，域 acme-properties.example，端点 Approved，状态 Disabled。下方的 Link requests 面板说明了被拒绝的登录，并显示 No sign-ins are waiting for a link decision。右上角有“New connection”按钮。" />

列表下方的 **Link requests**（关联请求）收集因匹配到某个现有身份、而该身份没有与该提供商建立关联而被拒绝的登录。请求在你 **Approve**（批准）之前不会授予任何权限；**Reject**（拒绝）会删除它。

**New connection**（新建连接）会打开表单。**Kind**（种类）决定连接面向的对象：

- **Tenant**（租户）：为一个或多个租户自己的人员提供的提供商。通过它登录的人只能进入它所服务的租户，永远无法触及持有系统角色的身份。
- **Instance**（实例）：运维人员自己的提供商。它只能通过显式操作与身份关联，并且必须要求某种认证方法（例如 `mfa`），或被确认为强制多因素登录的提供商。它不服务任何租户，只能以超级用户身份进入租户。

表单还需要令牌、名称、提供商的 **Issuer**（颁发者）、它给你的 **Client ID**（客户端 ID）和 **Client secret**（客户端密钥），以及可选的作用域、电子邮件声明，还有每次登录都必须携带的认证方法和声明。令牌和颁发者之后不可更改。客户端密钥是只写的：不会再次显示。如果提供商运行在你的网络内部，且其 TLS 证书由你自己的证书颁发机构签发，请把该 CA 证书（PEM）粘贴到 **Private CA certificates**（私有 CA 证书）下，或选择文件。

<GuideShot id="admin-identity-provider-new" alt="New identity provider 抽屉。Kind 选中 Tenant，另一选项是 Instance，并说明租户提供商的登录只能进入其所服务的租户。字段：Token、Name、Issuer（占位符 https://login.example.com）、Client ID、Client secret（只写）、Scopes（占位符 openid email profile）和 Email claim（占位符 email），每个都有简短说明。" />

连接的页面有四个选项卡，顶部有 **Enable**（或 **Disable**）和 **Delete**。

**Endpoints**（端点）显示发现快照：提供商最近一次获取到的授权、令牌、签名密钥和登出端点，以及指纹和批准情况。登录只使用在此处获批的端点。在批准之前，该选项卡提供 **Approve endpoints**（批准端点），而 **Enable** 保持不可用。**Refresh from provider**（从提供商刷新）会重新获取端点；如果它们发生了变化，批准会被撤回，通过该连接的登录会被拒绝，你需要审阅并批准新的端点后才能恢复。

<GuideShot id="admin-identity-provider-discovery" alt="Acme Google Workspace 连接，令牌 acme-google，Disabled，种类 Tenant，带有 Enable 和 Delete 按钮。Endpoints 选项卡显示 Discovery snapshot：Google 的授权、令牌和签名密钥端点，Sign-out endpoint 为 None，Fetched 时间、指纹，以及 Approval：Approved by superuser@devicechain.local 并附时间戳。有“Refresh from provider”按钮。" />

**Settings** 用于编辑连接。其 **General**（常规）部分显示固定的颁发者和 **Callback address**（回调地址，需在提供商处注册为重定向 URI），然后是名称、客户端 ID、作用域、电子邮件声明和必需的认证方法。其余部分处理客户端密钥、必需的声明、私有 CA，以及登出时是否同时在提供商处登出。

**Private CA**（私有 CA）部分适用于其 TLS 证书由你自己的证书颁发机构签发、位于你网络内部的提供商。你添加的证书仅对该提供商受信任，作为系统受信任根证书之外的补充，并且只接受证书，绝不接受私钥。**Verify and save bundle**（验证并保存证书包）会先用新的证书包连接提供商，如果其证书无法链到该证书包，则不保存任何内容。如果提供商随后给出不同的端点，批准会被撤回，需要你重新审阅。**Remove bundle**（移除证书包）会恢复为仅使用系统的根证书。

<GuideShot id="admin-identity-provider-settings" alt="Acme Google Workspace 的 Settings 选项卡。在 General 下：Issuer https://accounts.google.com，在创建连接时固定；Callback address http://localhost/api/user-management/sso/callback/acme-google 带复制按钮；Name Acme Google Workspace；以 apps.googleusercontent.com 结尾的 Client ID；Scopes openid email profile；Email claim email；以及 Required authentication methods 的开头部分。" />

**Tenants** 明确指定通过此连接登录可以进入哪些租户，不能进入其他任何租户。更改列表后选择 **Save tenants**（保存租户）。

<GuideShot id="admin-identity-provider-tenants" alt="Acme Google Workspace 的 Tenants 选项卡。在 Served tenants 下，Tenants served 选择器中是 acme (sim)。“Save tenants”按钮为灰色，因为没有任何更改。" />

**Domains**（域）列出此连接所代表的电子邮件域，以及每个域由谁在何时声明。在 **Add a domain**（添加域）下添加一个，或 **Remove** 它。免费邮箱提供商和公共后缀会被拒绝，且一个域只能属于一个连接。

<GuideShot id="admin-identity-provider-domains" alt="Acme Google Workspace 的 Domains 选项卡。在 Verified domains 下有一行：acme-properties.example，由 superuser@devicechain.local 声明并附时间戳，以及 Remove 按钮。下方是 Add a domain 字段（占位符 example.com）和“Add domain”按钮。" />

这些截图中的连接已获批准但仍处于禁用状态，因此目前还没有人能通过它登录。完整的分步设置请参阅[配置单点登录](/guides/configuring-sso)，其行为请参阅[单点登录](/concepts/single-sign-on)。

## AI 提供商 {#ai-providers}

**AI Providers** 注册实例可用于用自然语言起草检测规则的 AI 模型。每个提供商有种类、模型、API 密钥，需要时还有端点。密钥是只写的：不会再次显示。提供商的页面有 **Basic**、**Connection**（连接）和 **Test**（测试）选项卡，**Test provider**（测试提供商）会通过它运行一条提示来检查连接。注册提供商并不会把它开放给任何人：你需要在 **Packaging**（套餐）下把它授予某个层级，才会出现在这些租户的菜单中。

AI 提供商界面需要可选的 AI 服务。这些截图中的实例没有运行它，因此页面报告该功能区未部署：

<GuideShot id="console-area-unavailable" alt="管理控制台的 AI Providers 页面，显示标题为“Not deployed”的空状态。文字说明该实例部署时没有 ai-inference 服务，因此该功能不可用，会话本身并无问题，并且由管理员选择实例运行哪些服务。" />

参见 [AI 提供商](/concepts/ai-authoring#ai-providers-are-operator-configuration)。

## 实例审计 {#instance-audit}

**Audit** 是实例范围的日志。它记录登录、失败的登录和会话刷新（**Auth**（认证）类别），以及跨所有租户对身份、角色、租户和身份提供商的变更（**Mutation**（变更）类别）。每行显示时间、类别、执行者、操作、租户和目标。

可用 **All tenants**（已完成删除的租户也会连同日期一起列出，因此其日志仍可访问）、**All categories**（全部类别）和 **Filter by actor…**（按执行者筛选）筛选。**Export CSV**（导出 CSV）下载所有符合条件的内容，方式与租户的[审计](/user-guide/audit#exporting)导出相同。

<GuideShot id="admin-audit" alt="按 Mutation 类别和执行者 superuser@devicechain.local 过滤的 Audit 页面。每一行都是 superuser@devicechain.local 的 Mutation：对 iam_identities lee.park@acme-properties.example 的一次 Update，对 iam_memberships 和 iam_membership_tenant_roles 的 Create，对 iam_tenants harbor-view-pilot 的一次 Create 和一次 Update，以及对 acme-google 身份提供商连接及其租户和域的多次 Create 和一次 Update。Tenant 列显示短横线。右上角有“Export CSV”按钮。" />

该截图过滤为运维人员自己的变更。未过滤时，日志主要是登录记录。

## 设置 {#settings}

**Settings** 保存实例范围的设置，每项设置各占一个选项卡。默认值存在于平台代码中，只存储你的覆盖值，因此每个选项卡都显示 **Default**（默认）或 **Overridden**（已覆盖）徽章。**Save override**（保存覆盖值）存储你的值；**Reset to default**（重置为默认值）移除它。

**Branding**（品牌）设置每个租户在未自行设置时获得的默认应用标题、颜色、标志高度和标志，并带有实时预览。此处的标志是一个 https 地址，与其他内容一起通过 **Save override** 保存。参见[白标](/concepts/white-labeling)。

<GuideShot id="admin-settings" alt="System settings 页面的 Branding 选项卡，选项卡有 Token masks、Branding、Map 和 Language。设置 branding.default 带有 Default 徽章。字段：App title DeviceChain；Primary、Accent、Sidebar background 和 Sidebar foreground 颜色，均显示 inherit；Logo max height 28；以及 Logo 字段，其中是 https:// 占位符，帮助文字介绍了上传或 https 地址。Preview 卡片显示 DeviceChain、一个 Primary 按钮和示例文字。有 Save override 和 Reset to default 按钮。" />

**Token masks**（令牌掩码）设置控制台为新记录生成令牌的方式。掩码由字面文本加占位符组成：`{slug}`（取自记录的名称）、`{uuid}`、`{alphanumeric-N}` 和 `{numeric-N}`。`default` 条目适用于没有自己掩码的任何类型，每行都会预览一个示例令牌。**Add a mask**（添加掩码）为某个实体类型添加一行。参见[令牌掩码](/concepts/system-settings#token-masks)。

<GuideShot id="admin-settings-token-masks" alt="Token masks 选项卡。设置 entity.token_masks 带有 Default 徽章。先是占位符的说明，然后是一行：实体类型 default，掩码 {slug}，示例 sample-name，带删除图标。有 Add a mask、Save override 和 Reset to default 按钮。" />

**Map**（地图）设置每个租户起始使用的底图（[底图](/concepts/basemaps)），**Language**（语言）设置在租户未设置时控制台打开所用的语言（[默认语言](/concepts/system-settings#locale-default)）。租户管理员可以为自己的租户覆盖品牌、地图和语言；参见[租户设置](/user-guide/tenant-settings)。

## 相关内容 {#related}

- [多租户](/concepts/multi-tenancy)：租户如何在同一个共享安装环境中相互隔离。
- [租户层级](/concepts/tenant-tiers)和[治理与配额](/concepts/governance)：层级包含什么，以及限额如何解析。
- [租户删除](/deployment/tenant-deletion)：删除会擦除什么、保留什么，以及需要多长时间。
- [单点登录](/concepts/single-sign-on)和[配置单点登录](/guides/configuring-sso)。
- [系统设置](/concepts/system-settings)：实例范围的默认值。
