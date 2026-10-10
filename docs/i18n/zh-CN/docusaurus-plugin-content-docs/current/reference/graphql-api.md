---
sidebar_position: 1
title: GraphQL API
---

# GraphQL API

每个向外提供 API 的 DeviceChain 服务都使用 **GraphQL**。
本页列出端点、获取 Schema 的方法、所有变更遵循的约定，以及每个请求受到的限制。

:::note 状态
DeviceChain 处于预发布阶段，Schema 仍会演进。发布的 Schema 文件是权威参考。
默认禁用内省，参见[探索 Schema](#exploring-the-schema)。
:::

## 下载 Schema {#download-the-schemas}

所有 Schema 都在这里发布，根据服务启动时解析的文件生成：

| | |
|---|---|
| **索引** | [`/schema/index.json`](pathname:///schema/index.json)：列出所有功能区、认证平面、端点和 Schema 文件 |
| **Schema** | `/schema/<area>.graphql`；对提供另外两个平面的功能区，还包含 `-admin` 和 `-settings` 文件 |

请先查看索引。它集中列出每个功能区的 Schema 所属认证平面，以及该平面接受的令牌。
每个发布的 Schema 文件开头也有注释，说明自身的平面、端点和令牌。
认证平面很重要：向租户开发者提供管理员变更接口，会让他们面对永远无法获得授权的调用。

文件以纯文本提供，允许跨域访问，可以直接获取：

```bash
curl -s https://docs.devicechain.io/schema/index.json | jq '.areas[] | {area, endpoint}'
curl -s https://docs.devicechain.io/schema/device-management.graphql
```

## 端点 {#endpoints}

Ingress 将 `/api/<area>/graphql` 路由到各功能区服务，并移除前缀，
让请求到达服务自身的 `/graphql`。因此以下每个端点都是 `https://<your-host>/api/<area>/graphql`：

| 功能区 | 覆盖内容 |
|---|---|
| `user-management` | 认证：`login`、`selectTenant`、`refresh`，以及租户自身的治理视图 |
| `device-management` | 设备、设备类型、配置文件、资产、区域、客户、组、关系、告警、凭据、检测规则编写 |
| `event-management` | 时序事件查询：`events`、`locationEvents`、`measurementEvents`、`alertEvents`、`bucketedMeasurements` |
| `device-state` | 实时最后已知状态：`latestMeasurements`、`latestLocation`、`deviceStates`；以及把事件源明确声明在线的设备转回推断在线状态的 `demoteAssertedPresence` |
| `command-delivery` | 命令派发：`createCommand`、`cancelCommand`、设备群批次（`createCommandBatch`、`cancelCommandBatch`）、命令历史 |
| `event-processing` | 检测规则验证、回放预览、规则健康状况 |
| `dashboard-management` | 仪表盘增删改查和版本管理 |
| `outbound-connectors` | 每租户出站连接器增删改查 |
| `notification-management` | 通知渠道和策略 |
| `ai-inference` | 支持自然语言规则编写的单一调用 `inferRuleCandidate`；仅在启用可选推理服务时存在 |

另有三个端点位于**独立的身份令牌平面**，而非租户平面，只授权给超级用户或运维人员：

| 端点 | 覆盖内容 |
|---|---|
| `/api/user-management/admin/graphql` | 实例管理 API：身份目录、成员关系、角色目录、租户注册表和等级 |
| `/api/user-management/settings/graphql` | 实例设置 |
| `/api/ai-inference/admin/graphql` | 运维人员注册的推理提供商 |

数据平面服务使用**基于能力的授权**：每个解析器检查调用者租户令牌携带的特定权限，例如 `device:write`。
部分权限可能与直觉不同：

- 读取设备凭据需要 `device:write`，不是 `device:read`。
- `latestLocation` 需要 `location:read`，其他同属 `device-state` 的接口需要 `state:read`。
- `demoteAssertedPresence` 需要 `state:demote`，不同于前两者。
  默认没有角色直接声明它；只有持有 `*` 超级权限的角色，例如预置的 `tenant-admin`，无需明确授权就能使用。
  它是事件流水线之外唯一写入实时状态投影的接口，一次调用会影响整个事件源的设备。

`sparkplug-ingest` 和 `lwm2m-ingest` 完全不提供 GraphQL，且有意不进入 `/api` 路由。
`event-sources` 虽有路由，但只返回占位 Schema；设备通过传输协议接入它，而不是通过该 API。

### 通过 WebSocket 订阅 {#subscriptions-over-websocket}

提供 GraphQL 订阅的服务也在其 GraphQL 端点接受 WebSocket。
客户端必须协商 `graphql-transport-ws` 子协议，并在 `connection_init` 载荷中发送访问令牌，
格式为 `{"Authorization": "Bearer <token>"}` 或 `{"token": "<token>"}`。
令牌在连接打开时检查一次。

- **WebSocket 只运行订阅。**通过它发送查询或变更会被拒绝，返回错误
  `only subscription operations are accepted over a WebSocket; send queries and mutations over HTTP`，
  不执行任何操作。查询和变更应通过 HTTP 请求发送。
- **访问令牌过期时，连接以代码 `4401` 关闭。**要继续接收数据，需要用新令牌建立新连接并重新订阅。
  `@devicechain/client` 会自动这样重试一次：已建立的连接因 `4401` 关闭后，
  它重新解析令牌、重连、重新订阅，并通过 `connected(true)` 向接收端报告重连。
  .NET SDK 会让 `SubscribeAsync` 抛出包含关闭代码的异常；需要重新订阅才能继续。
- **没有订阅的服务拒绝协议升级**，返回 HTTP 400（`this service offers no GraphQL subscriptions`）。

服务器无法解析的订阅会得到语法错误，文档指定了其中不存在的操作时也会得到对应错误。
两者只影响该操作，连接保持打开。

## 查询事件 {#querying-events}

event-management 提供持久事件历史的读取查询。
每个查询接受搜索条件，包括设备、事件类型、发生时间范围、关系锚点（`{type, token}`）和分页，并返回分页结果：

```graphql
query {
  measurementEvents(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    anchor: { type: "customer", token: "acme-corp" }
  }) {
    results { deviceToken occurredTime name value }
    pagination { totalRecords }
  }
}
```

- 所有实体都以 token 命名，包括锚点内部。
- 两个时间边界都包含端点，按 `occurredTime` 筛选：即设备报告的时刻，而非平台存储的时刻。
- 结果按从新到旧排列。
- 页码从 1 开始。

所有事件查询都**自动限定于租户**：结果仅包含调用者的租户，没有解析到租户的查询会被拒绝。

事件的 `processedTime` 是平台**收到**它的时刻。
使用平台代理的 MQTT 时，这是代理存储消息的时刻；event-sources 中断后，这可能远早于事件被处理的时间。
其他传输中，它是接入服务收取事件的时刻。

**`measurementEvents` 不按测量名称过滤。**其条件没有 `name` 字段，
所以无法直接表达“该设备的温度读数”。可以在客户端按 `results[].name` 过滤，
或使用接受 `name`、返回时间桶的 `bucketedMeasurements`：

```graphql
query {
  bucketedMeasurements(criteria: {
    deviceToken: "sensor-001",
    name: "temperature",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    intervalSeconds: 300
  }) { bucketStart name avg min max sum count }
}
```

:::caution 深度回填的读数不会出现在 `bucketedMeasurements` 中
**现在写入，但按设备控制的 `occurredTime` 标为 30 天前**的读数，
会由 `measurementEvents` 返回，却不由 `bucketedMeasurements` 返回，也没有错误提示。
这只影响缓冲超过一个月的设备，或时钟存在如此大偏差的设备。
参见[回填读数和汇总](#backfilled-readings-and-the-rollup)。
:::

### 回填读数和汇总 {#backfilled-readings-and-the-rollup}

`intervalSeconds` 为 60 的整倍数且没有锚点过滤的分桶查询，使用预聚合汇总，而非原始读数。
汇总会更新**最近 30 天的窗口**。更早的数据只在数据库创建时物化过一次。

现在写入但标记为 30 天前的读数落在两者之间：对刷新窗口而言太旧，对一次性处理而言太晚。
原始历史完整，所以 `measurementEvents` 会返回它；`bucketedMeasurements` 则不会显示。

这个边界看的是读数被标记为多么**久远**的过去，而非数据本身有多旧。
回填一小时、一天或三周的读数会在一分钟内被纳入，没有问题。
小于一分钟的间隔，以及按锚点限定的查询使用原始读数，不受影响。

## 探索 Schema {#exploring-the-schema}

**默认禁用内省。**未额外设置的生产部署不暴露内省接口。
如果 GraphQL 客户端指向端点并期待自动发现文档，内省查询会被拒绝。

因此有两种方式读取 Schema。

**已发布的 Schema 文件**，见[下载 Schema](#download-the-schemas)。
这是可靠方式：无需运行中的实例，也无需令牌，尤其适合仍在评估 DeviceChain 的阶段。
每次文档构建都会根据各服务的 Schema 源文件生成，因此不会与服务解析的 Schema 偏离。
也可以读取仓库中提交的源文件。每个端点对应一个 `.graphql` 文件：
租户 API 使用 `schema.graphql`；同时提供身份令牌 API 的功能区还包含 `admin_schema.graphql` 和 `settings_schema.graphql`。

**开发实例上的内省。**在服务上设置 `DC_GRAPHQL_DEV_TOOLS=true` 以启用。
仅应在开发实例这样设置；默认关闭是有意的。
任何无法解析为布尔值的值都会被视为禁用，不作猜测。启用后可以使用通常的查询：

```graphql
query {
  __schema {
    types { name kind }
  }
}
```

启用开发工具还会在每个服务的 `/graphiql` 提供 **GraphiQL 浏览器**。
通过 Ingress 访问时是 `/api/<area>/graphiql`；直接端口转发到 Pod 时是 `/graphiql`。
浏览器向自身访问路径对应的端点提交请求，因此 Ingress、端口转发和控制台开发代理三种路由都可用。
在 `v0.12.0` 之前，页面能够加载，但所有查询都会失败，因为它指向没有任何服务提供的路径。

## 约定 {#conventions}

- 除内部 ID 外，还通过人类可读的 **token** 定位实体。
- 列表查询接受带分页的搜索条件输入。
- 变更遵循 `create* / update* / delete*` 命名方式。

### 更新写入记录的哪些部分 {#an-update-replaces-the-whole-record}

**每个 `update*` 变更都是部分更新，而且只有一个约定。**
它们使用专用的 `*UpdateRequest`，绝不使用对应 `create*` 的输入，并区分三种状态，而非两种：

| 字段发送内容 | 存储值的变化 |
| --- | --- |
| 不发送，字段缺失 | 保持原样 |
| 明确的 `null` | 清除 |
| 一个值 | 设置为该值 |

个别**字段**仍可能不同：不允许清除的必需引用、只写密钥、根本不在更新输入中的字段。
这些在[默认规则不适用的情况](#where-the-default-does-not-hold)中列出。
自动化任何操作前请先阅读。

改名只是改名：

```graphql
# Changes the name. The description, the externalId, the metadata and the device's
# type are all left exactly as they were, because none of them is mentioned.
mutation {
  updateDevice(token: "sensor-001", request: { name: "Cold store probe" }) {
    token
    name
  }
}
```

**只发送打算修改的内容。**完整替换 API 容易让人形成读取记录、再把整个记录提交回去的习惯，
但这里不应这样做。它增加工作量，也扩大覆盖并发编辑的窗口；对只写 `secret` 字段而言，
甚至会造成破坏，参见[下方警告](#where-the-default-does-not-hold)。

部分更新缩小了并发冲突，但没有消除冲突。
修改不同字段的两个写入者不再相互覆盖，修改同一字段仍会覆盖。
`updateDashboard`、`updateConnector`、`updateAiProvider` 和 `updateNotificationPolicy`
接受可选的 `expectedUpdatedAt`；存储时间戳在读取后发生变化时会拒绝写入。
传入最近读取的 `updatedAt`，或省略以采用最后写入者获胜的语义。
不要直接发送 `create*` 响应中的 `updatedAt`：先重新读取记录，否则即使没有其他修改，第一次受保护更新也可能被判定过时。

#### `token` 参数指定记录 {#the-token-argument-names-the-record}

每个 `update*` 都通过**参数**而非载荷指定记录，且**参数决定写入哪条记录**。
除两个特例外都是 `token: String!`；`updateRole` 还接受 `scope: String!`，由 scope 与 token 共同定位角色。
`updateOauthClient` 改用 `clientId: String!`；`updateProfile` 不接受定位参数，因为它修改当前登录身份。

载荷不携带 token。每个[部分更新](#which-mutations-are-partial-updates)输入都没有 `token` 字段，
因此载荷 token 与参数不一致的情况无法表达：Schema 会拒绝它。

以前有两种其他行为，现在都已消失：

- 载荷 token 必须与参数一致；不一致时拒绝，空值视为“未指定”。最后两个采用这种行为的变更也已转换。
- 载荷 token 表示记录的新 token，过去用于重命名配置文件、连接器、提供商和通知渠道。
  现在四者都有[独立的重命名变更](#renaming-a-record)，平台最后一个携带 token 的更新输入也随之消失。

所有更新现在有两点一致：载荷 token 不能再**清空**记录，也不能让变更写入 `token:` 指定记录之外的记录。

#### 重命名记录 {#renaming-a-record}

四种记录过去通过在完整替换更新载荷中发送不同 token 来重命名。
现在各自有专用变更，新 token 只可能有一种含义：

```graphql
renameDeviceProfile(token: String!, newToken: String!): DeviceProfile!
renameConnector(token: String!, newToken: String!): Connector!
renameAiProvider(token: String!, newToken: String!): AiProvider!
renameNotificationChannel(token: String!, newToken: String!): NotificationChannel!
```

四者遵循同一约定：

- 空白 `newToken`（空字符串或只有空白）会被拒绝，避免存在无法定位的记录。
- 重命名为当前已有 token 是幂等成功，返回该记录，因此部分失败后的重试安全。
- 同类其他记录已占用的 token 会被明确拒绝，`extensions.code` 为 `CONFLICT`，
  无论冲突来自查询，还是抢先完成的并发重命名。参见[必须唯一的值](#unique-values)。
- 所需权限与对应更新相同：重命名是编辑记录，不是新的操作类型。

这些记录一直设计为可以重命名，因为依赖者通过内部 ID 而非 token 引用它们。
包括渠道的投递密钥和策略规则保存的渠道 ID、连接器凭据、提供商 API 密钥及其等级授权和各租户模型分配。
重命名不会使这些引用成为孤儿。

重命名仍会影响两项内容，请事先检查：

- 规则动作通过 token 指定连接器，因此指向已重命名连接器的规则必须更新引用。
- `renameDeviceProfile` 在配置文件已发布或被设备类型采用后会直接拒绝重命名，
  因为从此发布规则和设备名册都通过 token 指定它。

`updateNotificationPolicy` 不需要重命名变更：没有对象按策略 token 引用它，
所以通过创建新策略并删除旧策略来迁移。

**地理围栏的 token 不可变。**`updateGeoFence` 过去协调两个 token 并拒绝不一致。
现在输入不携带 token，所以没有请求能要求重命名。
原因不变：检测规则在已编译表达式中通过 token 指定围栏，而本服务无法重写这些表达式，
重命名会让所有规则引用不存在的围栏，却返回成功。
需要不同 token 的围栏时，**先创建新的，再删除旧的**。
反过来做，可能失去已保留的顶点容量，导致无法重新创建围栏。

:::note[此行为已改变]
本版本之前，更新中的 token 处理既不统一，也不安全，两类失败都返回成功。
参见[token 处理如何改变](#how-token-handling-changed)。
:::

##### token 处理如何改变 {#how-token-handling-changed}

过去大多数 `update*` 变更按载荷 token 定位记录，完全忽略参数。
如果 `token:` 指定一个实体，`request.token` 指定另一个，会无声更新后者并返回它。
其余变更虽然遵循参数，但会把载荷 token 写入存储值，因此载荷仍然移动了记录。
空载荷 token 会清空记录的 token，让仍存在的行无法被定位；`token: String!` 允许这种值，因为 `""` 是有效的非空 String。

依赖载荷指定记录的客户端，现在会收到错误，而不是写错行。
在更新中发送 `token: ""` 的客户端，现在也会收到错误，而不是破坏记录身份：
重命名变更会拒绝它；部分更新则由 Schema 拒绝，因为输入没有可发送 `token` 的字段。
还有一组变更过去会*忽略*空 token，即“必须一致”规则，它们也已全部转换。

### 默认规则不适用的情况 {#where-the-default-does-not-hold}

以下是本版本 API 的字段级例外。最后两行描述字段类别并举例，不逐项穷举。
[发布的 Schema](#download-the-schemas) 中每个字段的注释都会说明是否可清除。
不是例外的字段遵循前述三种状态：缺失则保持，`null` 清除，值则设置。

| 字段 | 省略后的行为 |
| --- | --- |
| `updateNotificationChannel`、`updateConnector`、`updateAiProvider` 的 `secret` | **保留。**发送值会轮换；`null` 或空字符串会删除。密钥无法读回，因此省略就是“保持凭据”。webhook 配置声明 `bearer` 或 `header` 认证的通知渠道不允许没有密钥 |
| `updateTenantTier` 的 `config` | **保留。**清除等级设置会改变该等级所有租户的配额，省略不能清除；发送 `null` 或 `{}` 才会清除 |
| `updateEntityGroup` 的 `selector` | 省略则**保留**。与大多数部分更新字段不同，它不能*清除*：拒绝 `null`，因为无选择器的动态组不匹配任何实体且无法修复。静态组完全拒绝选择器 |
| `updateDashboard` 的 `definition` | 省略则**保留**，允许只改名而不重发文档。与上述 `selector` 一样，不能*清除*：拒绝 `null`，因为没有定义的仪表盘无效。无效定义会拒绝整个更新，所以同时发送的改名也不应用 |
| `updateProvisioningProfile` 的 `credentialType` | **不在更新输入中。**目前自动注册只能签发一种凭据类型，因此该字段只能重复已存储值。以前省略它的任何更新都会把它*重置*为 `ACCESS_TOKEN` |
| 设备配置文件或实体组的 `activeVersion` | 无影响：这里完全不可写，只能通过发布和回滚改变 |
| `updateEntityGroup` 的 `memberType` / `membershipMode` | **不在更新输入中。**两者均属于身份，不能表达修改，而非提交后拒绝 |
| `updateTenant` 上的租户[治理覆盖值](../concepts/governance.md) | **保留。**发送 `null` 会移除覆盖，表示**先继承等级，再继承平台默认值**，绝非零，也不是“无限制” |
| 记录存在所必需的字段，例如 `updateConnector` 的 `type` 和 `config`；`updateAiProvider` 的 `kind`、`model`、`enabled`；`updateNotificationChannel` 的 `channelType` 和 `enabled`；`updateNotificationPolicy` 的 `enabled`；`updateTenant` 的 `tierToken`；`updateOauthClient` 的 `redirectUris` 和 `scopes`；以及[值得了解的字段](#two-fields-on-converted-mutations)中列出的必需引用和字段 | 省略则**保留**。明确的 `null` 会被拒绝，不会清除 |
| 与另一字段一起验证的字段，例如 `updateAiProvider` 的 `endpoint`，以及 `updateDetectionRule` 的 `entityGroupToken` / `entityGroupVersion` | 省略则**保留**。只有与另一字段值组合后仍有效时，`null` 才能清除：没有默认地址的提供商类型拒绝 `endpoint: null`；只清除规则组范围的一半而不清除另一半也会被拒绝 |

:::danger 空字符串不表示“保持原样”
对每个只写 `secret` 字段，**`""` 会删除存储的凭据**，变更返回成功。
唯一例外是 webhook 配置声明 `bearer` 或 `header` 认证的通知渠道，此时拒绝整个更新。
填入所有字段的客户端会删除本来不想修改的凭据，失去凭据的连接器每次出站派发都会认证失败。
**省略该字段。**参见[密钥和空字符串](#secrets-and-empty-strings)。
:::

#### 密钥和空字符串 {#secrets-and-empty-strings}

密钥无法读回，因此没有内容可以重发；API 的规则是省略即保留。
“读取记录、改一处、全部提交回去”是完整替换 API 培养的习惯，针对那种 API 编写的客户端仍会这样做。
填满所有字段意味着给原本不想修改的凭据发送 `secret: ""`，从而删除它。

`null` 同样删除凭据。这是平台对 null 的一般含义，不是例外：null 清除指定字段。
这些字段过去采用相反的规则，null 保留、只有 `""` 删除；现在已不再如此。

#### 文本去除首尾空格，凭据不处理 {#text-and-credentials}

名称、描述以及类似的显示文本，例如名字、姓氏、图标、单位、等级颜色，存储时都会去除首尾空格。
空值或纯空白值会清除它，读回为 `null`。创建和更新都如此，
因此重新发送读取到的值对遵循此规则的内容不是修改。
旧版本保存的带首尾空格的值，例如人名，会在更新第一次指定该字段时去除空格。
`metadata`、渠道 `config`、仪表盘 `definition` 等结构化文本不作这种处理。

设备凭据的 `credentialValue` 同样不去除空格：按照发送内容原样存储，包含所有空格，
因为设备逐字节提供密码。只有空 `credentialValue` 或更新时明确的 `null` 才表示不存储密码；无密码的凭据无法认证。

### 哪些变更是部分更新 {#which-mutations-are-partial-updates}

**全部都是。**转换按功能区逐步完成，现在已全部完成。
本节记录各功能区的变化，供针对旧行为编写客户端的人了解。

**device-management。**每个 `update*` 使用专用 `*UpdateRequest`：

`updateDeviceType` · `updateDevice` · `updateAssetType` · `updateAsset` · `updateCustomerType` ·
`updateCustomer` · `updateAreaType` · `updateArea` · `updateMetricDefinition` ·
`updateCommandDefinition` · `updateDetectionRule` · `updateGeoFence` · `updateEntityGroup` ·
`updateDeviceCredential` · `updateProvisioningProfile` · `updateEntityRelationshipType` ·
`updateDeviceProfile`

**出站连接器和 AI 推理。**各自转换了一个更新：`updateConnector` 和 `updateAiProvider`。
原来通过载荷 token 重命名的能力都迁移到[专用重命名变更](#renaming-a-record)，没有被移除。
两者都保留可选 `expectedUpdatedAt`。
分别对 `type`/`config` 和 `kind`/`endpoint` 这一对字段，按记录更新后的值一起验证。
指定其中一个字段时，会重新检查已存储的另一个；使记录无法使用的变更会在写入时拒绝，而非第一次使用时才失败。

**notification-management。**两个 `update*` 变更均已转换：`updateNotificationChannel` 和 `updateNotificationPolicy`。
发送策略前应了解三点：

- **`rules` 可选，省略则规则集完全不变**：保留同样的行，不重建副本。
  以前它必填，每次更新都替换全部规则；只改名也会删除并重建所有规则，省略 `rules` 则会清空策略并返回成功。
  仍可完整替换：发送列表即可。`null` 或 `[]` 都会清空规则集；对列表来说，它们是同一请求的两种写法。
- **`deviceTypeToken` 完全不在更新输入中。**非空值在写入时被拒绝，
  因为派发器跳过按设备类型限定的策略；接受它会得到不投递任何内容却返回成功的策略。
  因此该字段除了无操作外，没有任何可接受请求。它仍在创建输入中，拒绝时会解释原因。
- **接受可选 `expectedUpdatedAt`。**发送最后读取的 `updatedAt`，或更新返回的值；
  如果此后任何人修改策略（包括规则），会拒绝更新且不写入任何内容。省略仍表示最后写入者获胜。

**dashboard-management。**`updateDashboard` 接受 `DashboardUpdateRequest`，完全不携带 token。
特别之处是 `definition`：字段可空是为了能够*省略*，允许只改名而不重发完整文档；
明确的 `null` 会被拒绝，因为无定义的仪表盘无效。保留可选 `expectedUpdatedAt` 前置条件。
完全不指定字段的更新不写入任何内容，连 `updatedAt` 也不写；
但其过时的前置条件仍会被拒绝为过时写入，不带 `extensions.code`。

**user-management。**每个 `update*` 同样接受专用请求：

`updateRole` · `updateTenant` · `updateTenantTier` · `updateOauthClient` · `updateProfile`

**这就是全部更新接口。**没有任何 `update*` 使用对应 `create*` 的输入，
因此本页不再提供供你逐项核对的变更清单。早期版本曾两次提供：
先是未转换功能区名单，后来是因两种约定并存而以签名为准的规则。
两者都在前提消失后过时。现在由覆盖整个 API 的[三种状态](#an-update-replaces-the-whole-record)
和[字段级例外](#where-the-default-does-not-hold)取代。

:::caution[检查具体输入在 Schema 中声明的字段]
一个约定不表示每种输入都接受每个字段。
有些字段有意不在 `*UpdateRequest` 中，有些接受值却拒绝 `null`。
前者以[下载的 Schema](#download-the-schemas)为准，后者由[例外表](#where-the-default-does-not-hold)
和各字段的 Schema 注释说明。参见[更新输入能够表达什么](#what-an-update-input-can-express)。
:::

#### 更新输入能够表达什么 {#what-an-update-input-can-express}

更新*能够*表达的内容，就是其 `*UpdateRequest` 声明的内容。
部分字段有意缺失，例如 `updateNotificationPolicy` 的 `deviceTypeToken`、
`updateEntityGroup` 的 `memberType`、`updateProvisioningProfile` 的 `credentialType`，因为它们没有任何可接受的请求。
其他字段接受值但拒绝 `null`，[例外表](#where-the-default-does-not-hold)和每个字段的 Schema 注释都会说明。
这与变更使用哪种约定无关，因为现在只有一种。

:::note[user-management 的行为已改变]
四个 user-management 更新过去会写入输入声明的所有字段。
重新运行旧客户端前，先阅读 [user-management 变化](#user-management-changes)，从 `updateTenant` 开始。
:::

#### user-management 变化 {#user-management-changes}

`updateRole`、`updateTenant`、`updateTenantTier` 和 `updateOauthClient`
过去会写入其输入声明的每个字段，所以只指定 `name` 的请求会清空其余字段并返回被清空的记录。
首先重新检查 `updateTenant`：省略治理覆盖值过去会**删除**它，因此重命名租户会移除运维人员设置的全部上限。
现在省略保持不变，只有明确的 `null` 才移除。

`updateTenant` 的 `tierToken` 变为可选。
省略则租户保留当前等级；明确的 `null` 被拒绝，因为每个租户必须有等级。

`authorities`、`redirectUris` 和 `scopes` 变为可空列表（`[String!]`，不是 `[String!]!`），因此有了缺失状态：

- 省略则保持不变。
- 发送列表则完整替换。
- `null` 和 `[]` 都表示“清空”。

角色权限可以清空，因为可以创建不授予任何权限的角色。
OAuth 客户端的重定向 URI 和 scope 不可清空：空重定向允许列表不匹配任何地址，客户端永远无法完成授权。

`updateProfile` 现在接受 `request: ProfileUpdateRequest!`，不再直接接受 `firstName` / `lastName` 参数。
只写入发送的名称。名称与其他显示文本一样去除首尾空格；`""`、纯空白值或 `null` 会清除它，读回为 `null`。

#### 已转换变更中值得了解的字段 {#two-fields-on-converted-mutations}

- **必需引用不能清除。**`updateAsset` 的 `assetTypeToken`，以及设备、客户、区域上的同类字段，
  发送值则重新指向实体，不发送则保持原样。明确的 `null` 被拒绝，因为这些实体不能处于“无类型”状态。
  未知 token 也被拒绝，而且完全拒绝：不写入任何内容。
- **`updateDeviceType` 的 `profileToken` *可以*清除**，因为设备类型可以没有[设备配置文件](../concepts/domain-model.md)。
  在旧的完整替换形式下，改类型名称时省略它会**解除配置文件关联**，无声撤销该类型所有设备的位置能力声明并返回成功。
  现在省略保留当前配置文件；`null` 或空 token 会解除关联。
  检测规则可选的 `entityGroupToken` 也可清除，但必须成对操作：
  对 `entityGroupToken` 和 `entityGroupVersion` 都发送 `null`。
  清除一项却保留另一项会被拒绝，因为范围同时需要两项。
- **必需字段即使不是引用，也不能清除。**指标的 `dataType`、凭据的 `credentialType` 和 `enabled`、
  规则的 `definition` 和 `enabled`、围栏的 `geometry`、自动注册配置的 `provisionKey` 和 `provisionSecret`：
  发送值则修改，省略则保持，明确的 `null` 被拒绝。
  这防止一种不可见失败：把 `enabled: null` 转成 `false` 会禁用凭据或停用规则并返回成功，
  而 `false` 又是完全合法的明确值。
- **省略密钥现在会保留它。**`updateDeviceCredential` 的 `credentialValue` 和
  `updateProvisioningProfile` 的 `provisionSecret` 过去会在任何未重发它们的更新中被清空。
  设备或整批自动注册设备会在下次连接时离线，而破坏它们的编辑却返回 `200`。

发送 `metadata` 时会完整替换，`null` 会清除。
它在 Schema 中是不透明 JSON 字符串，而非映射，因此没有按键合并可选；API 从来不能单独定位某个键。

## 输入验证 {#input-validation}

**Schema 未定义的输入字段会被拒绝。**发送未声明字段会使整个请求失败，
错误明确指出该字段，并建议可能想使用的已声明字段：

```json
{
  "errors": [{
    "message": "Variable \"request\" has invalid value.\nField \"deviceProfileToken\" is not defined by type \"DeviceTypeCreateRequest\". Did you mean \"profileToken\"?"
  }]
}
```

无论值作为查询中的字面量发送，还是通过变量提供，都遵循此规则。

这不只是拼写检查。无声丢弃的字段与已应用的字段无法区分：变更返回成功，
却只得到部分配置的实体，没有任何提示指出值丢失。
拒绝未知字段，才能让成功响应表示整个输入都被理解。

### token 可以包含什么 {#what-a-token-may-contain}

每个实体 token 和租户 ID 都必须匹配：

```
^[A-Za-z0-9][A-Za-z0-9_-]*$
```

即字母（大小写均可）、数字、连字符和下划线，以字母或数字开头，最多 128 个字符。
其他内容在创建*和*更新写入时都会被拒绝，而且在任何存储之前拒绝。

这是安全规则，而不是风格要求，因此范围很窄。
token 会拼入基础设施命名空间：租户 ID 成为 NATS subject 中按 `.` 拆分还原的片段，
设备 token 成为 MQTT 主题片段。`.` 会改变 subject 分段，`*`、`>`、`+` 和 `#`
会注入能够**跨租户**匹配的通配符。有意允许大写，因为设备序列号、VIN 等机器提供的标识符通常大写。

集成者最常想到的标识符恰好会被拒绝：`sensor.001`、MAC 地址 `AA:BB:CC:DD:EE:FF`、
`plant/line-2`、任何带空格的内容。**把这些放进 `externalId`。**
它是不透明值，没有格式约束，存在时在租户内唯一。
为实体选择一个 token，并在旁边保留设备原有标识符。

控制台按每实体类型的模板自动生成 token，所以这种问题在那里很少出现，主要影响 API 和脚本化自动注册。

### 必须唯一的值 {#unique-values}

某些值必须唯一：租户内的 token、设备 `externalId`、配置文件内的命令键、身份电子邮件、
每个身份与租户之间唯一的成员关系。导致重复的创建、更新或重命名会被拒绝，
错误的 `extensions.code` 为 `CONFLICT`：

```json
{
  "errors": [{
    "message": "the request conflicts with an existing record: a value that must be unique is already in use",
    "path": ["createDeviceType"],
    "extensions": { "code": "CONFLICT" }
  }]
}
```

按代码而非消息分支。如果服务有更具体的说明，例如重命名为已占用 token，消息会使用自己的句子，但代码相同。
数据库自身的冲突文本会被上面的句子取代，所以消息不暴露数据库索引或列名称。
服务自己的说明可能重复你发送的 token。

`CONFLICT` 表示写入与必须唯一的值冲突。通常是你发送的值，也可能是服务器写入时分配的值，
例如两个并发发布竞争同一记录的下一个版本号；后一种情况重试会成功。
所以仅凭该代码不能认定请求的记录已存在。只有唯一值本身由你提供时才有此含义，例如正在创建的租户 token。

一些类似的拒绝不携带 `CONFLICT`：

- 因读取后记录发生变化而拒绝保存（“modified by another writer; reload and try again”）是过时写入，不是重复。
- 已删除租户的 token 会保留到删除完成。在该 token 创建租户会被拒绝，但没有 `CONFLICT`，因为它不是由可用租户占有。
- 用已有 token 创建命令不会拒绝，而是返回原命令。参见[发送命令](../guides/sending-commands.md#issue-it)。
- 删除仍被其他记录引用的记录会携带 `REFERENCE_VIOLATION`。参见[引用或值被拒绝](#reference-and-invalid-values)。

### 引用或值被拒绝 {#reference-and-invalid-values}

另外两种拒绝也使用相同响应方式，各有自己的代码。

`REFERENCE_VIOLATION` 表示写入因记录间的引用关系被拒绝。

删除仍被其他记录引用的记录会这样拒绝，例如设备类型仍使用的设备配置文件、
设备仍使用的设备类型、租户仍采用的等级、仍有成员关系的租户、仍有授权的 AI 提供商、
策略规则仍引用的通知渠道。消息由服务自行生成，说明哪些内容仍引用记录，可能重复你发送的 token：

    entity is still referenced and cannot be deleted: 1 device type(s) reference device profile "rover"

删除或重新分配仍然引用该记录的内容后，再重试。

如果写入被数据库而非服务自身检查拒绝，消息为：

    the request refers to a record that does not exist, or removes one that other records still refer to

当检查与写入之间记录变化时可能发生，例如写入引用的记录在执行期间被删除。
重新加载后重试，就会得到服务自己的说明，或写入成功。
如果相同请求持续收到此响应，说明 API 中看不到的内容仍引用该记录。
请附上请求时间报告，服务器日志会记录导致拒绝的引用。

指定不存在记录的写入，通常在写入前被服务查询拒绝，消息指出 token，但没有代码。
只有该记录在写入进行期间被删除时，才携带 `REFERENCE_VIOLATION`。

`INVALID_VALUE` 表示请求包含记录不允许的值，而服务未在写入前发现。消息为：

    the request contains a value this record does not allow

请修改该值。重复发送相同请求通常仍会得到相同响应。

两种情况都会替换数据库自身文本，因此这两句话不包含表、列或约束名称，也不重复发送的值。
两种代码都不是 `CONFLICT`，所以把 `CONFLICT` 当作“记录已存在”的代码不会把它们当作成功。
同时涉及唯一值和其中一种问题的拒绝，会携带 `REFERENCE_VIOLATION` 或 `INVALID_VALUE`，绝不携带 `CONFLICT`，
消息使用对应代码的句子，而非值已占用的句子。

## 请求限制 {#request-limits}

每个 GraphQL 端点都会在执行前拒绝过大或工作量过多的请求。
唯一例外是凭据检查上限，它在执行期间生效，参见[每请求凭据检查](#credential-checks-per-request)。

所有服务使用相同限制，可通过表中环境变量逐服务修改。
值缺失、不是数字或小于 1 时回退到默认值；这些限制都不能关闭。

| 限制 | 默认值 | 变量 | 拒绝内容 |
| --- | --- | --- | --- |
| 请求正文 | 4 MiB | `DC_GRAPHQL_MAX_BODY_BYTES` | 整个 HTTP 正文，包括变量。返回 HTTP 400。 |
| 执行时间 | 60 秒 | `DC_GRAPHQL_EXEC_TIMEOUT`（整数秒） | 到达期限时仍在运行的查询或变更会被取消，正在进行的数据库语句也一并取消；响应中带有错误。不适用于订阅。客户端还必须在 30 秒内送达请求正文。 |
| 查询长度 | 100,000 字节 | `DC_GRAPHQL_MAX_QUERY_LENGTH` | 查询字符串本身。 |
| 嵌套深度 | 15 | `DC_GRAPHQL_MAX_DEPTH` | 超过此深度的嵌套选择。 |
| 每查询根字段数 | 20 | `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS` | 顶层字段数超过此值的查询操作。 |
| 每变更根字段数 | 5 | `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` | 顶层字段数超过此值的变更操作。 |
| 每请求凭据检查数 | 1 | `DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` | 一次请求中超过此数的密码检查，见[下文](#credential-checks-per-request)。 |

除了正文限制和凭据检查限制，被拒绝的请求会返回 HTTP 200，`errors` 中只有一项，无 `data`，且不执行任何内容。
凭据检查限制只拒绝超限检查，各自返回错误，其他请求内容仍执行。
根字段拒绝携带 `extensions.code: TOO_MANY_ROOT_FIELDS`：

```json
{
  "errors": [{
    "message": "mutation (anonymous) selects 6 root fields; the maximum is 5",
    "extensions": { "code": "TOO_MANY_ROOT_FIELDS" }
  }]
}
```

**根字段按响应键计数：**

- 每个别名算独立字段。
- 通过片段到达的字段，与直接展开书写一样计数。
- 重复的相同键算一个字段。
- 不评估 `@skip` 和 `@include`，因此条件字段无论是否执行都计数。
- 文档中每个操作都计数，不仅是 `operationName` 选择的操作。
- 规则同时适用于 WebSocket 和 HTTP。

变更限制更严格，因为变更字段依次执行。
否则一个请求可以包含数百个昂贵变更的别名副本。
控制台、仪表盘应用、SDK、`dcctl` 和 MCP 服务器每个请求发送一个变更字段，最多两个查询字段。
限制只针对顶层字段，嵌套字段的别名不计数。

### 列表、输入和聚合的大小限制 {#size-bounds}

除上述请求形态限制外，部分读取和输入还有大小限制。超出限制的请求会被拒绝，`extensions.code`
为 `LIMIT_EXCEEDED`。不会截断，也不会部分应用，请缩小请求后重新发送。

| 限制 | 数值 | 适用于 |
| --- | --- | --- |
| 每次聚合的时间桶数 | 10,000 | `bucketedMeasurements`：时间范围除以 `intervalSeconds`，向上取整。 |

`bucketedMeasurements` 还要求提供 `startTime`；`endTime` 默认为当前时间。

### 仅接受 GraphQL 语法 {#graphql-syntax-only}

**文档必须使用 GraphQL 自身语法。**包含以下任一内容的文档会因语法错误被拒绝，不执行任何内容：

- `//` 或 `/* */` 注释。应使用 `#` 注释。
- 反引号字符串或单引号字符。
- 结束 `"""` 紧跟反斜杠的块字符串，即 `\"""` 转义。
  此转义符合 GraphQL 规范，但服务器从未按规范读取它，因此应通过变量发送此类文本。
- 字符串后直接紧跟引号，例如 `"x""y"`。
  GraphQL 将它解读为两个相邻字符串；服务器过去会把它误读为块字符串开始。只有 `"""` 才能开始块字符串。

WebSocket 同样遵循此规则：服务器无法读取的订阅会得到语法错误，而非[仅订阅提示](#subscriptions-over-websocket)。

### 每请求凭据检查 {#credential-checks-per-request}

无论如何书写，一次请求只能检查有限个密码，默认一个。
限额以内的 `login` 字段正常执行。同一请求中其余 `login`（例如另一个别名）不执行：
不检查密码，不查询内容，也不记录审计日志。
它会得到自己的错误，而非密码是否正确的结果；其他字段仍返回数据：

```json
{
  "errors": [{
    "message": "this request has already made its credential checks; send one sign-in per request",
    "path": ["a2"],
    "extensions": { "code": "TOO_MANY_CREDENTIAL_CHECKS" }
  }]
}
```

这不依赖文档的写法，所以通过根字段限制的文档仍受约束。
拒绝发生在查看电子邮件地址之前，因此账户是否存在都得到同样结果。
DeviceChain 提供的每个客户端每次请求只发送一次登录，所以不受影响。

`DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` 可以逐服务提高限额，与其他限制一样不能关闭。
拒绝计入 `devicechain_usermanagement_credential_checks_total`，标记 `outcome="request_budget"`。

### 登录退避 {#sign-in-backoff}

失败的密码登录会减缓针对同一电子邮件地址的后续尝试，适用于 `login` 和 OAuth 登录表单。

- 每个地址最初五次失败尝试立即评估。
- 之后等待 1 秒才评估下一次，然后 2 秒、4 秒，逐次翻倍，最多 5 分钟。
- 成功登录会重置计数；最后一次被评估的尝试之后安静 10 分钟也会重置。

计数属于输入的地址，无论该地址是否存在账户，因此延迟不会泄露已注册地址。
服务的所有副本共用该计数。

:::warning 知道地址的人可以阻止其所有者登录
只要有人持续向一个地址提交错误密码，所有者即使提供正确密码也会因限流被拒绝。
攻击停止后，最多经过一次不超过 5 分钟的等待，所有者就能重新登录。
参见[让地址持续处于退避状态](#holding-an-address-at-the-backoff)。
:::

#### 让地址持续处于退避状态 {#holding-an-address-at-the-backoff}

计数按地址保留，不按地址与网络位置组合保留，避免攻击者通过多台机器分散猜测而取得新额度。
代价是任何知道电子邮件地址的人都可以持续为它提交错误密码。
持续这样做时，每个评估机会都由他们占用，所有者即使密码正确也被限流拒绝。
这不是永久锁定。`devicechain_usermanagement_credential_checks_total` 的
`outcome="throttled"` 指标显示账户何时以这种方式被阻止。

#### OAuth 客户端密钥 {#oauth-client-secrets}

**OAuth 客户端密钥不实施退避。**管理 API 创建的客户端密钥具有 256 位随机性，无法通过猜测找到，
而客户端 ID 是公开的，出现在每个授权 URL 中。对客户端密钥实施退避不能保护它，
反而让任何人都能阻止某个机密客户端，进而阻止通过它的所有登录。
通过配置预置的客户端也应使用同样强的密钥。公共客户端完全没有密钥。

#### 等待期间的尝试 {#attempts-during-the-wait}

等待期间的尝试完全不评估：不检查密码，不记录审计日志。
它返回独立错误，而非密码错误，因为密码可能是正确的：

```json
{
  "errors": [{
    "message": "too many failed sign-in attempts; try again in 8 seconds",
    "path": ["login"],
    "extensions": { "code": "THROTTLED", "retryAfterSeconds": 8 }
  }]
}
```

如果服务无法访问保留计数的存储，会完全拒绝检查密码，而不是不计数地检查。
此时 `login` 错误携带 `extensions.code: UNAVAILABLE`。
应把它视为服务中断，而非凭据被拒绝。OAuth 令牌端点不使用该存储，所以客户端认证仍工作。

#### 尝试存储已满时 {#when-the-attempt-store-is-full}

存储大小固定，每个被尝试的地址占用一个位置 10 分钟，无论账户是否存在。
针对足够多不同地址发送登录请求的人可以填满它。

存储已满时，登录仍可使用。密码仍正常检查并返回结果，但不记录新的失败，
所以尚未等待的地址不会被减速，直到旧条目过期。
已经等待的地址仍等待，但最多到当前等待结束，即最多 5 分钟。
此后它的失败也不再计数，所以存储满期间被攻击的账户不受退避保护。

这是有意的：如果改为拒绝所有登录，任何能填满存储的人都能把实例所有用户挡在外面。
猜测仍受到每请求字段上限和每次密码检查成本的限制。

这样检查的每次尝试都计入 `devicechain_usermanagement_credential_checks_total`，
标记 `outcome="store_full"`。启用 chart 告警规则时，只要出现这种情况，
`CredentialAttemptStoreFull` 告警就会触发。持续期间 user-management 日志每分钟最多记录一次警告。

告警触发时，很可能有人正在尝试许多地址：

1. 找到登录流量来源，在上游阻止它。
2. 如果流量合法，提高 `instance.config.infrastructure.nats.kvStateMaxBytes`。
   此大小适用于每个状态桶，因此确认 JetStream 卷有足够空间容纳增加的容量。

Schema 稳定后，将根据它们生成详细的逐类型参考页面。
