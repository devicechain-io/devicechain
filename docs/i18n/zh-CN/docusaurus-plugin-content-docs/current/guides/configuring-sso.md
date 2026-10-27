---
title: 配置单点登录
---

# 配置单点登录 {#configure-single-sign-on}

本指南连接一个 OpenID Connect 身份提供商，让租户的用户使用其组织账号登录。关于该功能的行为和限制，请先阅读[单点登录](../concepts/single-sign-on.md)。

:::caution 提供商配置方案尚未经过真实环境测试
下面 Microsoft Entra ID、Okta 和 Google Workspace 的配置方案，依据各提供商的文档以及 DeviceChain 检查的声明编写。它们**尚未针对这些提供商的真实租户运行过**。登录流程本身已针对一个替身 OpenID Connect 提供商完成端到端测试。请预期可能需要调整某个控制台标签或声明名称，并欢迎告诉我们你的发现。
:::

## 开始之前 {#before}

- **实例需要一个公开地址。**单点登录需要 `auth.issuerUrl`（见[下文](#deployment)）。如果它为空，创建连接会失败，并提示 "sso requires a public issuer URL"。
- **你需要以超级用户身份登录**控制台（**Admin → Identity providers（管理 → 身份提供商）**）或管理 API。系统角色拥有 `user:write` 的身份也可以使用管理 API，但不能使用控制台的管理页面，这些页面只对超级用户开放。
- **在提供商处**，你需要把 DeviceChain 注册为一个应用（使用授权码流程的“web”或“confidential”客户端），并记下它的客户端 ID 和客户端密钥。
- **确定租户和域名。**租户连接为你指定的租户服务，并代表你断言的邮箱域名。

## 创建连接 {#create}

1. 在控制台中打开 **Admin → Identity providers**，选择 **New connection**。填写令牌（字母、数字和连字符）、名称、类型（`tenant`）、提供商的 **issuer**，以及客户端 ID 和密钥。issuer 和类型之后无法更改；不同的 issuer 就是不同的连接。如果提供商的证书由私有证书颁发机构签发，请同时在此提供该机构的证书（[详情](#private-ca)）。
2. DeviceChain 获取提供商的发现文档，并保留以待批准。连接初始为禁用状态。页面会显示其回调 URL（标签为 **Callback address**），即 `<issuerUrl>/sso/callback/<token>`。请在提供商处将它原样注册为重定向 URI。
3. 审阅端点，然后选择 **Approve endpoints**。
4. 添加提供商所代表的邮箱域名（例如 `example.com`）。
5. 选择它所服务的租户。
6. 如果要在首次登录时创建用户，请开启预配并选择默认角色（`setIdpJit`）。如果提供商会发送组，请添加组到角色的映射（`setIdpRoleMappings`）。控制台暂时还没有这些设置项，请使用管理 API。
7. 选择 **Enable**。
8. 可选：设置会话上限（默认 12 小时；控制台暂时还没有该设置项，请使用管理 API 中的 `setIdpSessionMaxAge`）；等每个人都有了登录途径后，再为该租户开启 **Require SSO**（**Admin → Tenants**）。 控制台会先请求确认，因为这会结束该租户内的密码会话。

同样的步骤也可以通过管理 GraphQL API 完成：`createIdpConnection`、`approveIdpDiscovery`、`addIdpDomain`、`setIdpConnectionTenants`、`setIdpJit`、`setIdpRoleMappings`、`updateIdpConnection`（`enabled: true`）、`setIdpSessionMaxAge` 和 `setTenantSsoEnforced`。超级用户还可以使用 `setIdpRequiredClaims` 更改连接所要求的声明。

实例连接（供你自己的运营人员使用）的创建方式相同，类型为 `instance`，另外需要 `requiredAmr`（例如 `["mfa"]`）或 `mfaEnforcedByIdp: true`。请用 `linkIdentity` 逐个显式链接运营人员；系统不会自动链接任何人。

## 双重身份验证 {#two-factor}

租户的[双重验证要求](./two-factor-authentication.md#require)只约束**密码**登录。通过单点登录登录的人不会遇到它，租户的该项设置也不适用于他们。决定 SSO 登录的是连接：在连接上要求某种认证方式（`requiredAmr`，例如 `["mfa"]`），使身份提供商必须已执行自己的第二因素。开启 **Require SSO** 的租户已没有可适用该要求的密码登录。

## 在提供商处退出登录 {#provider-sign-out}

默认情况下，从控制台或仪表盘应用退出登录只会结束 DeviceChain 会话。用户在身份提供商处仍保持登录状态，因此下次 **Sign in with SSO** 可能无需输入密码即可直接通过。连接也可以在用户退出登录时将其发送到提供商自己的退出登录页面（OpenID Connect RP 发起的登出）。此功能**默认关闭**，因为在提供商处退出登录通常会结束该用户在提供商所服务的所有应用中的会话。

1. 确认已批准的发现文档显示了 **Sign-out endpoint**。不发布该端点的提供商无法以这种方式退出登录：此设置不起作用，退出登录仍会结束 DeviceChain 会话。
2. 在连接的 **Settings** 选项卡中的 **Sign out at the provider** 下，复制两个 **Post-logout redirect addresses**，并在提供商处将它们注册为退出登录后（post-logout）重定向 URI。它们是控制台的 `<源>/login` 和仪表盘应用的 `<源>/dash/`，其中 `<源>` 是实例[公共地址](#issuer-url)的协议和主机。
3. 勾选 **Also sign people out at the provider**。在管理 API 中对应 `updateIdpConnection` 并设置 `providerSignOut: true`。

用户退出登录时会发生什么：

- **DeviceChain 会话总是先结束**，在服务器上完成，无论提供商之后如何处理。之后浏览器才会被发送到提供商，提供商再将其送回登录页面。
- 请求只携带连接的客户端 ID 和返回地址，不包含其他内容。DeviceChain 不保存提供商的 ID 令牌，因此不会发送 `id_token_hint`，提供商可能会要求用户确认。如果提供商拒绝不带 `id_token_hint` 的退出登录请求，则不能使用此设置，请保持关闭。
- 返回地址由服务器根据实例的公共地址构建，调用方无法指定。如果未在提供商处注册该地址，提供商会显示自己的错误页面，而不会把用户送回；无论如何，DeviceChain 会话都已结束。
- 仅适用于该连接处于启用状态且其发现文档已批准时通过该连接的登录。密码登录只会退出 DeviceChain。

## 提供商配置方案 {#recipes}

在每个方案中，请将 `<callback URL>` 替换为该连接的回调 URL。

### Microsoft Entra ID {#entra}

1. 在 Entra 管理中心，选择 **App registrations → New registration**。账号类型选 **Accounts in this organizational directory only**。重定向 URI：平台选 **Web**，值为 `<callback URL>`。
2. **Certificates & secrets → New client secret.** 复制该值。
3. **Token configuration → Add optional claim → ID token**，添加 `email` 和 `xms_edov`。Microsoft 不发送 `email_verified`；DeviceChain 仅在 `xms_edov` 为 true 时信任地址，因此缺少该声明时，每次尚未链接到身份的登录都会被拒绝（`refused_unverified_email`），也不会留下链接请求。
4. 如需组映射：**Token configuration → Add groups claim**，选择 **Groups assigned to the application**。此时 `groups` 声明携带的是组对象 ID，因此请映射这些 ID，而不是组名。
5. 按以下内容创建连接：
   - issuer 为 `https://login.microsoftonline.com/<tenant GUID>/v2.0`，其中填入你目录的租户 ID。`common`、`organizations` 和 `consumers` 会被拒绝。
   - 要求声明 `tid` 等于同一个租户 GUID。对 Microsoft 颁发者这是必填项。
   - scopes 为 `openid`、`email`、`profile`。
6. **组过多（groups overage）。**一个令牌最多可列出约 200 个组；超过后 Entra 会发送一个指针而不是列表（`hasgroups` 或 `_claim_names`）。当连接映射了组时，DeviceChain 会**拒绝该次登录**（`refused_groups_overage`；用户会被告知联系其管理员），而不是把此人当作没有组。避免的方法是只把所需的组分配给应用。主权云的 issuer 主机与 `login.microsoftonline.com` 同样处理。
7. 对于实例连接，Entra 在执行了多因素认证时会在 `amr` 中报告 `mfa`；请将它设为必需。

### Okta {#okta}

1. 在 Okta 管理控制台，选择 **Applications → Create App Integration → OIDC - OpenID Connect → Web Application**。Sign-in redirect URI：`<callback URL>`。分配可以使用它的用户或组。
2. 复制客户端 ID 和客户端密钥。
3. Issuer：组织授权服务器使用你的 Okta 组织 URL（`https://<org>.okta.com`），或使用自定义授权服务器（`https://<org>.okta.com/oauth2/<id>`）。它必须与该服务器发现文档中的 `issuer` 完全一致。
4. Okta 会发送 `email` 和 `email_verified`。如果你的 Okta 用户的邮箱未经验证，登录会被拒绝。
5. 如需组映射：把 `groups` scope 加入连接的 scopes，并添加一个 **groups claim**（Sign On 选项卡，或授权服务器的 Claims），使用 “Starts with” 或 “Matches regex” 之类的过滤器，只发送你想要的组。映射组名。使用了多因素认证时，Okta 的 `amr` 会包含 `mfa`。
6. Okta 没有像 `tid` 这样的租户固定声明；每个组织有自己的 issuer，由它起到固定作用。

### Google Workspace {#google}

1. 在 Google Cloud 控制台，选择 **APIs & Services → OAuth consent screen**：用户类型选 **Internal**（将登录限制在你的 Workspace 内）。**Credentials → Create credentials → OAuth client ID → Web application**，授权重定向 URI 为 `<callback URL>`。
2. Issuer 为 `https://accounts.google.com`。要求声明 `hd` 等于你的 Workspace 主域名（例如 `example.com`）。这是必填项：Google 会为每个 Google 账号（包括个人账号）签发令牌，只有 Workspace 账号才带有 `hd`。令牌中的 `hd` 还必须等于地址的域名。
3. 把同一个域名添加为连接的已验证域名。
4. Google 的 ID 令牌**不带组**，也没有 `amr`。请使用连接的默认角色而不是组映射；除非你设置了 `mfaEnforcedByIdp` 并在 Workspace 中强制执行两步验证，否则不要将 Google 用于实例连接。

### 其他提供商 {#other}

任何通过 https 提供发现文档、使用 RS256、ES256 或 PS256 对 ID 令牌签名，并发送 `email` 及 `email_verified` 的 OpenID Connect 提供商都可以使用。`none` 和 HMAC 签名会被拒绝。如果提供商使用不同的名称，请在连接上设置邮箱声明和组声明的名称。

## 部署与配置 {#deployment}

### 公开地址 {#issuer-url}

user-management 的 `auth.issuerUrl` 必须是其 API 可从外部访问的地址，例如 `https://dc.example.com/api/user-management`。chart 会从 ingress（`ingress.host`，配合 `ingress.tls.enabled`）推导它，因此常规安装无需任何设置。如需覆盖，请在 `functionalAreas.user-management.config.auth.issuerUrl` 下自行设置；设为 `""` 则保持单点登录和 OAuth 授权服务器关闭。使用真实主机名的 http 地址会在启动时被拒绝；请通过 https 提供实例服务。你的 ingress 必须把 `/api/user-management/sso/` 路由到 user-management（chart 的 ingress 已经这样做）。

### 访问提供商 {#egress}

对提供商的每个请求都经过出口防护，它会拒绝私有、环回和链路本地目的地。公共提供商无需任何设置。对于本地部署的提供商（你网络中的 ADFS、Keycloak、Dex），请在 `instance.config.infrastructure.egress.allowedDestinations` 下以尽量窄的 CIDR 允许其地址范围。该允许列表对整个实例生效，也适用于 webhook 和连接器，因此请优先使用 /32。提供商的证书必须能链到 user-management 镜像中的系统根证书，或链到你为该连接提供的[私有 CA 证书包](#private-ca)。

### 使用私有 CA 证书的提供商 {#private-ca}

本地部署的提供商通常提供由贵组织自己的证书颁发机构签发的 TLS 证书，系统根证书中不包含它。若不处理，创建连接会失败并提示 “certificate signed by unknown authority”。请把该 CA 的证书提供给这个连接，而不是在整个实例范围内放宽任何校验：

1. 以 PEM 文本获取该 CA 的证书（以及中间证书，如有）：一个或多个 `-----BEGIN CERTIFICATE-----` 块，最大 64 KiB。绝不要提供私钥；文本中出现私钥会被拒绝，并且不会被回显。
2. 创建连接时，把它粘贴到 **Private CA certificates**，或选择文件。对已有连接，使用 **Settings → Private CA**，然后选择 **Verify and save bundle**。在管理 API 中，对应 `createIdpConnection` 的 `caBundle`，或 `setIdpCaBundle`。
3. 核对列出的证书：每个证书都会显示其主体、到期时间和 SHA-256 指纹。请将指纹与你的 CA 公布的指纹比对。声明自己不是证书颁发机构的证书会被当作恰好这一张证书来信任，并标记为 **Not a CA**。

它做什么、不做什么：

- 证书包**仅适用于该连接**：发现、密钥集和令牌交换都信任它，其他连接和功能则不信任。它是在系统根证书**之外额外**信任的，因此使用公共证书的提供商在设置了证书包后依然可用。
- 它不会改变出口防护。提供商的地址仍须符合[允许列表](#egress)；证书永远不会让被禁止的地址变得可访问。
- 证书包只能粘贴或上传，绝不会从某个地址下载，因为下载得到的信任根将由响应下载请求的一方决定。
- 保存新证书包时，会先用它访问提供商的发现地址，如果证书无法链到它，就会拒绝（不保存任何内容）。如果提供商随后给出的端点与你已批准的不同，批准会被撤销，你需要重新审核；如果相同，批准保持有效，因此轮换 CA 不会让任何人被锁在门外。
- 提供商的证书仍须在主体备用名称中包含其地址（与 issuer 匹配的 DNS 名称或 IP 地址），并处于有效期内。请在 CA 证书到期前替换证书包。**Remove bundle**（以空字符串调用 `setIdpCaBundle`）会让该连接仅使用系统根证书。
- 证书包是公开证书，因此与连接一起存储而不是作为机密存储，并会包含在数据库备份中。

### 位于负载均衡器之后 {#load-balancer}

请保留客户端地址（`externalTrafficPolicy: Local` 或代理协议），否则所有用户会共享同一份限流配额。参见[概念页面](../concepts/single-sign-on.md#load-balancer)。

### 告警 {#alerts}

| 告警 | 严重级别 | 含义 | 处理办法 |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | user-management 已连续十分钟无法访问某个提供商的令牌端点或签名密钥，因此通过它登录的用户会被拒绝。密码登录不受影响。 | 检查提供商是否在线、能否从集群内解析，出口允许列表是否放行了本地部署的地址范围；如果提供商迁移了端点，请刷新并批准发现快照。 |
| `SsoUnverifiedIdentityRefused` | warning | 某次登录虽然得到提供商担保，但仍被拒绝：`refused_unverified_email`、`refused_unverified_domain`、`refused_required_claim` 或 `refused_system_role_identity`（`outcome` 标签）。没有任何人被登录。 | 通常是连接配置有误，或有人在探测。请查看审计日志中提供商断言的地址，以及提供商自己的登录日志。 |
| `SsoStateStoreFull` | critical | 保存已发起登录的存储已满，因此在已发起的登录过期之前（即洪流停止十分钟后），任何提供商都无法发起单点登录。密码登录不受影响。 | 很可能有人在循环调用公开的登录发起入口：在 ingress 日志中找到来源，并在上游封禁。如果流量是合法的，请调大 `instance.config.infrastructure.nats.kvStateMaxBytes`。 |

它们背后的指标是 `devicechain_usermanagement_sso_logins_total{connection,outcome}` 和 `devicechain_usermanagement_sso_idp_fetch_failures_total{connection}`。第一个指标中你还可能看到这些结果：`state_invalid`（已过期、被重放或不匹配的登录）、`idp_error`、`rate_limited`、`refused_link_required`、`refused_groups_overage`、`refused_token_invalid`、`refused_issuer_mismatch`。

## 故障排查 {#troubleshooting}

| 现象 | 可能原因 |
| --- | --- |
| 控制台对本应使用单点登录的地址要求输入密码，或 **Sign in with SSO** 提示 "Single sign-on is not set up for that email address" | 没有可用的连接负责该域名：该域名没有在任何连接上断言，或其连接已禁用，或其发现文档尚未批准。按地址登录时，这几种情况的回应相同（该连接会计入 `refused_connection_unavailable` 结果）；实例未启用单点登录时，控制台同样会要求输入密码，因此请检查 **Admin → Identity providers**。 |
| "Single sign-on is not available right now" | 该实例尚未配置单点登录（见[部署](#deployment)），或同一地址发起的登录过多：请稍等片刻再试。 |
| "Your account exists but is not linked" | 该地址已有身份：请批准它的链接请求。 |
| 每次登录都因未验证而被拒绝 | 提供商没有发送已验证的邮箱（对 Entra，请添加 `xms_edov`）。 |
| 提供商变更后被拒绝 | 提供商迁移了端点：**Refresh from provider** 并重新批准。 |
| "no enabled identity provider connection serves this tenant" | 请先连接并启用一个提供商，再开启 **Require SSO**。 （错误码 `SSO_ENFORCE_NO_CONNECTION`。） |
| "this is the last enabled identity provider connection" | 请先为该租户关闭 **Require SSO**，或启用另一个为它服务的连接，再禁用或删除该连接。 （错误码 `SSO_ENFORCED_TENANT_STRANDED`；控制台会给出说明。） |
| 链接账号时：“Single sign-on is not set up for your email domain” | 没有任何已启用并已批准的租户连接拥有该用户的邮箱域名。请把该域名添加到连接，或启用、批准该连接。（`SSO_LINK_NO_CONNECTION`。） |
| 链接账号时：“sign in again” | 密码登录已超过 15 分钟、已结束，或是通过单点登录完成的。请用密码登录后再链接。（`SSO_LINK_SESSION_REQUIRED`、`SSO_LINK_PASSWORD_REQUIRED`，或返回时的 `link_session_ended`。） |
| 链接账号时：“a different email address” | 用户在提供商处以其他人身份登录，或提供商报告了另一个地址。（返回时的 `link_email_mismatch`。） |
| 链接账号时：“already linked” | 该身份已在该连接上链接，或该提供商账号已链接到另一个身份：请先解除链接。（`SSO_LINK_ALREADY_LINKED`，或返回时的 `link_already_linked`。） |
| 链接账号时：“Ask a superuser to link it” | 该身份持有系统角色；请用 `linkIdentity` 链接。（`SSO_LINK_NOT_ALLOWED`。） |
