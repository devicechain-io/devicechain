---
title: 服务账号
sidebar_position: 20
---

# 服务账号

本章面向要把集成（脚本、ETL 作业、后端服务）接入 DeviceChain 的**租户管理员**。**服务账号**是你租户中的非人类账号。集成向 OAuth 令牌端点出示该账号的**客户端 ID**和**客户端密钥**，换取一个短期访问令牌，然后像其他令牌一样把它发送给各个 API。整个过程不涉及任何人的登录，所以有人离职、重置密码或更换角色时，集成仍能继续工作。

**Service accounts**（服务账号）位于侧边栏 **Settings**（设置）下，紧挨着 **Members**（成员）。它使用相同的权限：`member:read` 用于列出账号，`member:write` 用于创建或修改账号。被限定于租户一部分的成员（客户用户）看不到此页面。

## 账号能做什么 {#what-an-account-can-do}

账号的权限**恰好是其角色的权限**。与成员不同，它不会额外获得只读的查看者基线权限：如果集成需要读取设备，就给它一个授予 `device:read` 的角色（内置的 **Viewer**（查看者）角色即可）。

与成员一样，账号也可以被限定于**特定的客户或组**。这样它的令牌只能看到这些客户和组的设备及其数据，与[客户用户](./members-roles-and-customer-users.md#customer-users)完全相同。

保护委派管理的规则在这里同样适用：

- 你只能给账号分配那些你自己持有其全部权限的角色。
- 只有当你持有账号所持有的全部权限时，才能修改、轮换、禁用或删除它；否则控制台会拒绝该修改并说明原因。
- 账号不能管理租户：无论其角色如何，它都不能管理成员、角色或其他服务账号。

一个租户最多可以拥有 200 个服务账号。

## 创建账号 {#creating-an-account}

选择 **New service account**（新建服务账号）并填写：

- **Token**（令牌标识）：租户内唯一的标识符（字母、数字、连字符和下划线）。它在审计日志中代表该账号，创建后不能更改。
- **Name**（名称）和 **Description**（描述），可选。
- **Roles**（角色）：账号可以做什么。
- **Can see**（可见范围）：**Whole tenant**（整个租户），或 **Specific customers or groups**（特定客户或组）。

选择 **Create service account**（创建服务账号）。随后控制台会显示 **Client ID**（客户端 ID）、**Client secret**（客户端密钥）和 **Token endpoint**（令牌端点）。

{/* TODO(screenshot): service-account-secret, the one-time client credentials panel. */}

密钥**只显示一次**。DeviceChain 只保存它的带密钥摘要，从不保存密钥本身，因此任何地方都无法再次向你显示它。请立即把它复制到集成的密钥存储中。如果丢失，请轮换密钥（见下文）。

每个密钥都会过期。新密钥默认有效 365 天，可通过 API 指定其他值（1 到 730 天）。**Secrets**（密钥）列显示有多少个密钥可用、它们的最后四个字符，以及下一个密钥何时过期。

## 获取令牌 {#obtaining-a-token}

集成在控制台显示的令牌端点（实例的签发者 URL 加上 `/oauth/token`）使用 OAuth `client_credentials` 授权方式请求令牌，并通过 HTTP Basic 进行身份验证：

```bash
curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials \
  https://dc.example.com/api/user-management/oauth/token
```

如果客户端无法设置 `Authorization` 请求头，也可以把凭据作为表单字段发送：

```bash
curl -s \
  -d grant_type=client_credentials \
  --data-urlencode "client_id=$CLIENT_ID" \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  https://dc.example.com/api/user-management/oauth/token
```

响应是一个 JSON 对象，包含 `access_token` 及其 `expires_in`。把该令牌作为 Bearer 令牌发送给任意租户 API：

```bash
curl -s -X POST https://dc.example.com/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"query($c:DeviceSearchCriteria!){devices(criteria:$c){results{token}}}",
       "variables":{"c":{"pageNumber":1,"pageSize":10}}}'
```

关于令牌需要了解：

- **它有效 5 分钟，并且没有刷新令牌。** 过期时请求新令牌即可；每次请求都会重新读取账号，因此你所做的修改会在集成获取下一个令牌时生效。
- **它只指向账号所属的租户**，不会是其他租户。请求无法要求其他租户。
- 两个可选的表单字段可以收窄它。`scope` 把令牌限制在某个 OAuth 作用域的上限之内（例如 `read-only`）。`resource` 按地址（一个绝对 `https` URI）把它绑定到一个资源服务器：例如 MCP 服务器只接受绑定到其自身地址的令牌。
- 令牌端点有速率限制。每个账号在突发 10 次之后，大约每 30 秒可以获取一个令牌；每个来源地址在突发 20 次之后，大约每 3 秒可以尝试一次。请求过于频繁的客户端会收到 `429`（`slow_down`）和以秒为单位的 `Retry-After` 响应头；请在令牌即将过期前一直复用它，而不是每次调用都请求一个新令牌。

每一次失败的尝试（未知的客户端 ID、错误或已过期的密钥、已禁用的账号）都会得到相同的响应 `401 invalid_client`，因此调用方无法分辨是哪一种情况。属于已禁用或正在删除的租户的账号会收到 `400 invalid_grant`。

租户自身的单点登录要求只适用于人员，不适用于服务账号。

## 轮换密钥 {#rotating-a-secret}

在该行的菜单中选择 **Rotate secret**（轮换密钥）。设置 **Overlap (hours)**（重叠时长，小时），即当前密钥还能继续使用多久（0 到 168，默认 24），然后确认。控制台会显示一次新密钥。

在重叠期内两个密钥都可用，因此你可以在不中断服务的情况下部署新密钥。重叠期结束后，旧密钥停止工作。一个账号同时可用的密钥永远不超过两个：在重叠期内再次轮换会立即停用最旧的那个。重叠时长为 0 会立即停用当前密钥，这正是密钥泄露时应采取的做法。

## 禁用与删除 {#disabling-and-deleting}

**Disable**（禁用）在你确认后会阻止账号获取新令牌；**Enable**（启用）则恢复。**Delete**（删除）会移除账号及其全部密钥。它的令牌标识可以被新账号重新使用，新账号会获得新的客户端 ID，因此旧密钥永远不会对它生效。

这两种操作都不会收回已经签发的令牌：它在过期前（最多 5 分钟）仍然有效。修改账号的角色或可见范围也是如此。

被服务账号持有的角色，在没有任何账号（也没有任何成员）持有它之前不能删除。

## 审计日志 {#the-audit-log}

账号所做的操作会以它自己的名称 `service-account:<token>` 记录，因此[审计日志](./audit.md)绝不会把集成所做的修改归到某个人头上。它每获取一个令牌，都会记录一个 `service_account_token` 事件。创建、修改和删除账号会记录在执行操作的管理员名下。失败的尝试不会被记录。

## API {#the-api}

该页面是租户 user-management API 的一个视图。例如，创建一个账号：

```graphql
mutation {
  createServiceAccount(request: { token: "ci-bot", name: "CI", roleTokens: ["viewer"] }) {
    clientId
    clientSecret
    tokenEndpoint
  }
}
```

其余操作由 `serviceAccounts`、`serviceAccount`、`setServiceAccountAccess`、`setServiceAccountEnabled`、`rotateServiceAccountSecret` 和 `deleteServiceAccount` 完成；[GraphQL API 参考](../reference/graphql-api.md)对每一个都有说明。
