---
sidebar_position: 8
title: SQL 与 BI 访问
---

# SQL 与 BI 访问 {#sql-and-bi-access}

DeviceChain 将遥测存储在 **TimescaleDB** 中，而 TimescaleDB 就是 PostgreSQL。无需绕过这一点——它本身就是集成方式。任何支持 Postgres 或 JDBC 的工具都能直接查询遥测：Metabase、Grafana、通过 PostgreSQL 或 ODBC 驱动连接的 Power BI、`psql`、笔记本环境，或你自己的报表任务。无需导出，无需同步第二个数据存储，也无需为另一个产品购买许可。

本指南配置的是不会自动具备的部分：**可以安全分发的读取角色**。平台自身的数据库凭据会让 BI 工具获得所有租户的数据及运营数据存储的写入权限。分析访问接口的目的，就是让你不必分发这些凭据。

:::note 状态
已可用。每次安装都会创建此接口；只有声明读取者后，才会有人能读取。
:::

## 分析视图 {#what-a-reader-can-see}

读取者连接事件存储，并查询 **`analytics` 模式**。其中每种事件关系对应一个视图，另外还有预聚合的测量汇总。通常应优先使用汇总：它已按时间桶聚合，因此扫描较长时间范围的成本较低。

| 视图 | 内容 |
| --- | --- |
| `analytics.events` | 基础事件封装：设备、类型、时间、来源 |
| `analytics.measurement_events` | 具名数值读数，包含单位和数据类型 |
| `analytics.measurement_rollups` | 每台设备、每项指标的每分钟总和、最小值、最大值、计数 |
| `analytics.location_events` | 位置：纬度、经度、海拔、精度、速度、航向。需要位置授权，参见[位置需要单独授权](#position-is-a-separate-grant) |
| `analytics.alert_events` | 设备报告的告警 |
| `analytics.state_change_events` | 连接/断开连接时间线 |
| `analytics.event_anchors` | 写入每个事件时附加的关系锚点 |

每个视图都会自动过滤为**读取者自己的租户**。无需记住按租户列过滤，无需为不同租户选择不同视图，查询也无法扩大结果范围。参见[如何强制执行边界](#how-the-boundary-is-enforced)。

汇总会让当前仍在填充的时间桶保持实时，因此读取它的仪表板不会只能看到上次刷新之前的数据。

## 声明读取者 {#declaring-a-reader}

读取者是名为 **`analytics_<tenant id>`** 的 PostgreSQL 登录角色。租户由角色名决定，因此 `analytics_acme` 只读取租户 `acme`。

:::danger 角色名决定租户，而且这是唯一指定租户的地方
如果租户 ID 为 `acme`，却将读取者命名为 `analytics_acmecorp`，它将读不到任何数据：所有查询都返回零行，但没有错误。没有第二个地方可以修正这个错误，也没有消息指出它。创建角色前，请在控制台核对租户 ID。租户 ID 必须不超过 53 个字符；参见[名称限制](#name-limits)。
:::

### 名称限制 {#name-limits}

租户 ID 必须**不超过 53 个字符**。PostgreSQL 将角色名限制为 63 字节，并会*截断*较长的名称，而不是拒绝，从而可能静默生成另一个租户的读取者。部署会拒绝将被截断的名称，因此应用会被拒绝，而不会产生意外结果。

### 步骤 {#steps}

1. **创建包含密码的 Kubernetes Secret。** 平台不会生成或存储该凭据；它由你掌管，数据库会协调为与其一致。Secret 放在实例自己的命名空间中，即 `dci-` 后接实例 ID，与读取它的事件存储处于同一命名空间。为它添加 `cnpg.io/reload` 标签（参见[重新加载标签](#the-reload-label)）：

   ```bash
   kubectl create secret generic analytics-acme-credentials \
     --namespace dci-<instance-id> \
     --type kubernetes.io/basic-auth \
     --from-literal=username=analytics_acme \
     --from-literal=password="$(openssl rand -base64 24)"

   kubectl label secret analytics-acme-credentials \
     --namespace dci-<instance-id> cnpg.io/reload=true
   ```

2. **在部署变量中声明角色**，并设置连接限制。对于使用 `dcctl` 构建的实例，这是在实例 OpenTofu 状态旁的 `terraform.tfvars` 文件，即 `~/.devicechain/instances/<instance-id>/infra/instance/terraform.tfvars`。每次 `dcctl` 应用实例时都会读取它，包括升级，因此角色会持续被声明；如果 `dcctl upgrade` 找不到其他声明来源，它会拒绝升级，而不是停止声明该读取者。

   ```hcl
   timescale_analytics_readers = [
     {
       name             = "analytics_acme"
       connection_limit = 5
       password_secret  = "analytics-acme-credentials"
     },
   ]
   ```

3. **应用。** 对于使用 `dcctl` 构建的实例，`dcctl upgrade <provider> <instance-id>` 会使用该文件应用实例配置。角色随之出现、加入读取者组，并能连接。无需重启任何组件。

:::warning 标签决定轮换是否生效
没有 `cnpg.io/reload`，后续 Secret 更改不会被及时发现，轮换也无法在可预测的时间内完成。参见[重新加载标签](#the-reload-label)。
:::

### 重新加载标签 {#the-reload-label}

没有 `cnpg.io/reload`，数据库在首次创建角色时仍会取得密码。但是，之后对 Secret 的**更改**不会被及时发现。该标签要求数据库 Operator 监视 Secret 更新；下述轮换步骤依赖它，以便在可预测的时间内生效。

有两个容易忽略的细节：

- 只要标签*存在*即可，任何值都有效。
- Secret 中的 `username` 必须与角色名**完全一致**，不能带尾随换行。

数据库 Operator 无法协调角色时，会在数据库 Cluster 的 `status.managedRolesStatus` 下记录角色名和原因，而不是写入 DeviceChain 自身日志。在认定密码错误之前，请先查看那里。

如果你是在本指南补充标签步骤之前创建的 Secret，请现在添加标签。在添加之前，密码更改可能在无法预测的时间内一直未被应用。

### 轮换或撤销 {#rotate-or-revoke}

要轮换密码，请更改 Secret 中的密码。数据库会协调为与之匹配，无需重启。

要撤销访问：

1. 从 `timescale_analytics_readers` 移除条目，并应用。
2. **以超级用户身份删除角色：**

   ```sql
   DROP ROLE analytics_acme;
   ```

移除条目只会停止部署对角色的声明。数据库 Operator 会保留不再管理的角色，其密码和读取者组成员资格仍保持不变。先删除角色也不可行：只要它仍被声明，Operator 就会重新创建它。

## 位置需要单独授权 {#position-is-a-separate-grant}

按上述方式声明的读取者可以读取遥测、告警、连接/断开时间线及事件封装，**但不能读取设备位置**。纬度、经度、海拔、精度、速度和航向由第二项授权控制，按读取者开启：

```hcl
timescale_analytics_readers = [
  {
    name             = "analytics_acme"
    connection_limit = 5
    password_secret  = "analytics-acme-credentials"
    reads_location   = true
  },
]
```

应用后，读取者即可查询 `analytics.location_events`。未开启时，仅该视图返回 `permission denied`，其他视图均不受影响。

这是平台本身的权限边界，不是对 BI 额外施加的谨慎措施。DeviceChain 中，读取设备**在哪里**与读取它**测量什么**始终是不同的权限。车辆或人员轨迹与温度序列属于不同种类的事实，因此位置刻意不包含在基础只读权限中，只有明确授权后才能获得。

无法向 SQL 会话询问它持有什么权限：它以角色身份认证，不携带其他内容。因此，这项权限只能以数据库能够表达的方式表示——作为授权，由读取者在你明确指定后加入的第二个组角色持有。

普通读取者仍可看到**封装**。`analytics.events` 包含所有事件，包括位置事件，因此读取者仍能看到某台设备在某个时间发生了位置事件，但无法看到位置。

:::tip 哪些读取者需要位置权限
设备群、物流、现场服务和资产跟踪仪表板需要。指标或告警仪表板通常不需要；少一个拥有位置权限的读取者，就少一份泄露后会暴露他人行踪的凭据。只在仪表板确实绘制地图或计算距离时开启。
:::

:::note 升级现有安装
在这项权限拆分之前声明的读取者曾拥有位置权限。升级后 `event-management` 首次重启时，会收回所有未标记 `reads_location = true` 的读取者的该项授权，因此绘制位置的仪表板会返回 `permission denied`，直到你设置它。这是刻意的：每次 `event-management` 启动都会从声明重新推导授权，而不是累积授权。
:::

## 连接 BI 工具 {#connecting-a-bi-tool}

将工具指向事件存储，就像连接普通 PostgreSQL 数据库一样：

| 设置 | 值 |
| --- | --- |
| 主机 | 事件存储服务（集群内为 `dc-timescaledb-single`） |
| 端口 | `5432` |
| 数据库 | 你的**实例 ID** |
| 模式 | `analytics` |
| 用户 | `analytics_<tenant id>` |
| 密码 | 放入 Secret 的密码 |

数据库名称是实例 ID，而不是固定名称，因为一台服务器为每个实例托管一个数据库。

从集群外访问时，像其他数据库一样暴露存储：临时访问使用端口转发，长期连接使用带 TLS 的正式入口。快速检查：

```bash
kubectl port-forward -n dci-<instance-id> svc/dc-timescaledb-single 5432:5432
psql "postgres://analytics_acme@localhost:5432/<instance-id>" \
  -c "SELECT device_token, name, bucket, sum_value / count_value AS avg
      FROM analytics.measurement_rollups
      WHERE bucket > now() - interval '1 hour'
      ORDER BY bucket DESC LIMIT 20;"
```

这些工具都不需要 DeviceChain 插件：

- **Grafana：**使用上述设置添加 **PostgreSQL** 数据源。
- **Metabase：**添加 **PostgreSQL** 数据库。
- **Power BI：**使用 **获取数据 → PostgreSQL 数据库**。

## 如何强制执行边界 {#how-the-boundary-is-enforced}

本节说明可以如何安全使用读取者凭据。

### 租户过滤 {#tenant-filter}

**租户过滤被编入视图，并依据经过认证的角色。** 每个视图都包含 `WHERE tenant_id = <the tenant of the authenticated role>`。该身份是会话登录时使用的角色，即 PostgreSQL 的 `session_user`，读取者无法更改：

- `SET ROLE` 只更改*当前*角色，绝不更改会话角色。
- 唯一能更改会话身份的语句 `SET SESSION AUTHORIZATION`，会拒绝非超级用户执行。
- 没有可覆盖此行为的会话设置。

名称不含可识别租户的角色解析为空，读取零行。失败始终意味着“看不到任何内容”，绝不会意味着“看到所有内容”。

### 没有表权限 {#no-table-privileges}

**读取者对底层表没有任何权限。** 它根本无法按名称访问原始超表。这也是它只读的原因：它仅对已获授权的视图持有 `SELECT`，没有其他权限，因此没有可行使的写入权限。这是授权，而不是设置，所以客户端无法关闭它。

同样的机制也隔离位置。没有 `reads_location` 的读取者不持有 `analytics.location_events` 权限，因此坐标是不可访问，而不只是被过滤。

### 每次启动都修复 {#repair-on-every-start}

**每次 `event-management` 服务启动，都会重新建立这两层保护。** 由服务执行，而非数据库，因此仅重启数据库不会修复任何内容。每次启动时，`event-management` 会：

- 重建解析会话租户的函数；
- 验证每个视图；如果缺失、暴露错误的列、丢失租户谓词，或不再是安全屏障，就重建；
- 重新收敛权限。

因此，调查期间手动授予的权限或编辑过的视图，都不会悄然延续到调查之后。重启 `event-management` 即可修复。

这涵盖了可能扩大位置授权的每种方式。无论直接向具名读取者、一般读取者组，还是 `PUBLIC` 授予 `analytics.location_events` 权限，服务下次启动都会收回。读取者持有的权限每次都从声明推导，绝不累积。因此移除 `reads_location` 确实会移除访问权限，而不会保留上次授权。

### 连接上限 {#connection-cap}

**每个角色都有强制连接上限。** `connection_limit` 在认证时执行：超过上限就拒绝连接。这可防止分析消费者耗尽平台自身连接池。否则，该故障可能悄无声息，因为连接池延迟打开连接，数据库仍报告健康，而应用已无法连接。部署会拒绝渲染没有限制的读取者，也会拒绝总限制超出服务器容量的一组读取者。

平台从事件存储 97 个可用连接中，为自身连接池保留 40 个；使用 `--ha` 安装且未使用 `--compact` 的实例则保留 80 个，因为 `event-management` 运行两个 Pod，每个各有连接池。因此，读取者的 `connection_limit` 总和最多为 57，后一种情况下最多为 17。超过时，基础设施规划会在事件存储或消息代理发生变化前失败，错误会列出这两个数字。

:::warning 上限限制的是连接数，而不是负载
限制可防止分析占用平台需要的*连接*，但无法防止这些连接上的查询争用 CPU、磁盘和 PostgreSQL 共享并行工作进程池。因此，“分析无法影响数据摄取”只**部分**成立。参见[负载与只读副本](#load-and-read-replicas)。
:::

### 负载与只读副本 {#load-and-read-replicas}

读取者连接上的查询会争用 CPU、磁盘和 PostgreSQL 共享并行工作进程池，压缩、保留和汇总刷新也使用同一个池。连接耗尽的路径已被阻断；资源争用的路径没有。

如果这对工作负载很重要，请让 BI 使用**只读副本**。复制部署已在主库旁暴露只读服务。将读取者指向它，即可将争用转移到专门为它们服务的节点。

在副本上，PostgreSQL 会通过在有限时间内延迟重放来处理冲突，然后取消长时间运行的分析查询。默认限制为 30 秒，即 `max_standby_streaming_delay`；部署保留该默认值，因此副本不会无限期落后。

### 查询成本没有上限 {#query-cost-is-not-capped}

读取者没有查询时间限制，而添加一个也并不像表面看起来那样能强制控制。PostgreSQL 的 `statement_timeout` 可以作为角色的**默认值，而不是上限**：任何客户端都能通过一条语句提高自身会话的值，且无法阻止。

仍然值得设置它，以防仪表板意外发起高成本查询。这是在数据库上由超级用户执行的操作，不是平台能够代办的事：

```sql
ALTER ROLE analytics_acme SET statement_timeout = '60s';
```

连接限制才是真正具有约束力的控制。规划该限制及存储容量时，应假设每个连接都可能运行长查询。

## 实用说明 {#practical-notes}

- **按时间过滤，并使用完整键连接事件锚点。** `analytics.events`、`analytics.measurement_events`、`analytics.location_events`、`analytics.alert_events` 和 `analytics.event_anchors` 的行按租户再按时间建立索引，因此时间范围是成本最低的过滤条件。`analytics.state_change_events` 按租户、设备、时间建立索引，应提供设备过滤条件。读数、位置或告警行带有自己的时间点，可能早于事件时间，因此应以 `event_id` 连接到 `analytics.events`，并在两侧都限制时间范围。锚点带有事件时间点，因此将 `analytics.event_anchors` 连接到 `analytics.events` 时，要同时使用 `event_id` **和** `occurred_time`：只用 `event_id`，数据库无法直接查找每个事件。
- **较长时间范围的查询应使用汇总，而不是原始表。** 它是连续聚合，计算已完成。扫描一个月的汇总成本很低，扫描一个月的原始测量则不然。
- **每个租户只有一个读取者角色，该租户的所有工具共享它。** 角色名*就是*租户，因此 `acme` 只有一个合法读取者名称，声明第二个的部署会被拒绝。同一租户的两个工具共享连接限制和位置授权决定，无法分别撤销。按全部工具的合计需求设置 `connection_limit`，只要*任意*工具需要地图，就设置 `reads_location`。
- **数据库重启会在五秒后结束读取者会话。** 事件存储主库停止时（故障转移、节点排空、配置滚动更新），已连接客户端有五秒时间，之后会话终止，因此当时运行的长查询会失败。重新连接并再次运行。参见[数据库主库停止时](../deployment/bootstrap.md#ha-database-failover)。
- **读取者能跨越模式更改继续存在，但不会自动获得新内容。** 视图暴露固定的一组列。平台日后添加的列，只有被明确添加到分析接口后才会出现。
- **读取者能看到自身租户之外的一些元数据。** 任何已连接角色都可读取 PostgreSQL 系统目录。读取者能列出服务器上的其他角色名（从而知道哪些租户拥有 BI 访问），查看这些会话何时活跃，以及内部表名和分块名，但无法读取其中的任何数据行。如果这很重要，请为每个客户提供独立实例。
- **删除租户不会删除其读取者角色。** 停用租户时，从部署变量中移除角色，然后以超级用户删除它。遥测已被擦除，因此角色读不到内容，但仍存在的登录身份意味着有人仍持有登录凭据。**租户 ID 也可以重用，而该角色届时会读取新租户的数据。** 删除角色才能关闭这两个缺口；仅从声明中移除仍会让它留在数据库中，如[声明读取者](#declaring-a-reader)所述。
