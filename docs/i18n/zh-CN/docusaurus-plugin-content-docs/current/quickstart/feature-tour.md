---
title: 功能导览
---

# 功能导览 {#feature-tour}

[你的第一个设备](./first-device.md)止步于第一条读数。本导览继续往下走。完成后，一个站点会从头到尾运行起来：一个设备类型及其配置文件、两个上报读数的设备、一个仪表板、一条会触发告警并下发命令的规则、设备对该命令的应答，以及一位登录后只能看到自己设备的**客户用户**。

然后，你把同一份导览再运行一遍，作为第二个租户、面向另一个客户，只需修改一个参数文件。不需要复制或分叉任何东西。

导览是仓库中 [`examples/feature-tour`](https://github.com/devicechain-io/devicechain/tree/main/examples/feature-tour) 下的一组 shell 脚本。它们调用的就是你自己会用的公开 GraphQL API 和同一个 `dcctl`，下面每一步也都说明了控制台里在哪里做同样的事。DeviceChain 没有导入整个站点的功能：这些脚本只是普通的 API 调用，你可以阅读并修改它们。

实例就绪之后，大约需要 45 分钟。

## 你需要准备什么 {#what-you-need}

- 一个正在运行的实例及其控制台。[你的第一个设备的第 1 步](./first-device.md#1-bring-up-an-instance)会在本地 kind 集群上启动一个。你不需要用到它的租户或设备，导览会创建自己的。
- `PATH` 中有 `bash`、`curl`、`jq`、`openssl`、`docker` 和 `kubectl`。在 Windows 上请使用 Git Bash。
- 一份仓库的副本，用来获取脚本：

  ```bash
  git clone https://github.com/devicechain-io/devicechain
  cd devicechain/examples/feature-tour
  ```

有三个设置用来说明你的实例在哪里。每个都有默认值，与普通的 `dcctl install local` 之后执行 `dcctl bootstrap local tour --host localhost --no-tls` 相符，也都可以用环境变量覆盖：

| 变量 | 默认值 | 何时需要设置 |
| --- | --- | --- |
| `DC_URL` | `http://localhost` | 控制台在其他主机或端口上提供服务。 |
| `INSTANCE` | `tour` | 你的 `dcctl` 实例使用了别的名称。它决定命名空间 `dci-<instance>`，脚本从中读取代理的证书颁发机构和超级用户的密码。 |
| `MQTT_HOST` | `devicechain-control-plane` | 你的 kind 集群不叫 `devicechain`。它是集群节点的容器名称，即 `<cluster>-control-plane`。 |

如果你的 `kubectl` 有多个上下文，还需设置 `KUBE_CONTEXT`。

```bash
export DC_URL=http://localhost INSTANCE=devicechain MQTT_HOST=devicechain-control-plane
```

模拟设备是 `kind` Docker 网络上的容器，因此它们通过节点名称访问代理的 MQTT 节点端口。在不是 kind 的集群上，请在任何能访问代理的机器上运行 [`device/coldroom.sh`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/device/coldroom.sh)，或者让你自己的客户端连接到代理。参见[连接设备](../guides/connecting-a-device.md)。

## 运行它 {#run-it}

```bash
./tour.sh sites/site-a.env all
```

这会完成全部配置、启动两个设备、等待规则执行动作，然后逐条检查本页做出的每项说明，并为每项打印 `PASS` 或 `FAIL`。检查以 `tour tour-a: all checks passed` 结束。两个阶段也可以分开运行：

```bash
./tour.sh sites/site-a.env up       # provision, dashboards, customer user, start the devices
./tour.sh sites/site-a.env verify   # wait for the loop, then check the operator's and the customer's views
```

这些脚本可以放心地重复运行。已存在的内容保持不变，定义有变化的仪表板会被更新，并作为新版本发布。

脚本生成的密码（站点运维人员的、客户用户的和设备的）写入 `.state/<tenant>/`，该目录已被 git 忽略，只有你自己可读。密码永远不会被打印出来。

下面各节依次说明每个脚本做了什么，并给出控制台里的对应操作。

## 1. 租户及其管理员 {#1-the-tenant}

`01-provision.sh` 从实例级别开始。它使用 `dcctl bootstrap` 创建的 Secret 中的超级用户密码，创建租户（管理 API 上的 `createTenant`），并为站点自己的管理员 `operator@coldchain-a.example` 创建一个身份。该身份是这个租户的 `tenant-admin`，不属于任何其他租户。

之后的一切都以这位管理员的身份运行，你登录时用的也是这个身份：超级用户的控制台是实例的 **Admin** 控制台，其中没有租户页面。在控制台里，同样的操作是 Admin 控制台中的 **Tenants → New tenant** 和 **Identities**。

要登录租户控制台，请打开 `DC_URL`，输入 `operator@coldchain-a.example`，并使用 `.state/tour-a/operator-password` 中的密码。

## 2. 设备类型及其配置文件 {#2-device-type-and-profile}

**设备类型**对设备分类，它的**配置文件**则是契约：设备上报哪些测量值、接受哪些命令、哪些规则监视它的读数。脚本创建配置文件 `cold-room`，其中包含：

- 测量值 `temperature` 和 `humidity`，
- 一条命令 `boost-cooling`，以及
- 一条检测规则 `cold-room-too-warm`：当 `temperature` 超过站点的阈值时，触发一条主要告警**并**发送 `boost-cooling`。这条规则就是 [`rule.json`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/rule.json)，也就是控制台规则表单保存的内容。

接着它发布该配置文件，并在其上创建设备类型 `cold-room`。**发布很关键：**规则只有在配置文件发布之后才会作用于实时读数，平台在此时编译规则，所以无法编译的规则会在这里被拒绝，而不是日后悄悄失效。声明了命令的配置文件只接受这些命令。

在控制台中：**Devices → Device Profiles**（规则、指标和命令）以及 **Devices → Device Types**。参见[设备类型与配置文件](/user-guide/device-types-and-profiles)和[检测规则](/user-guide/detection-rules)。

## 3. 设备 {#3-devices}

脚本创建该类型的两个设备 `coldroom-a1` 和 `coldroom-a2`，并为每个设备配置一个 `MQTT_BASIC` 凭据。设备出示用户名 `<tenant>:<credentialId>` 及其密码；平台只保存密码的摘要。

在控制台中：**Devices → Devices**，然后打开设备的 **Credentials** 标签页。参见[设备](/user-guide/devices)和[设备凭据](../guides/device-credentials.md)。

## 4. 客户，以及哪个设备属于它 {#4-a-customer}

脚本创建一个客户类型和客户 `acme`，并把 `coldroom-a1` **分配**给它：即从设备到客户的 `assigned` 类型关系。`coldroom-a2` 被有意留作未分配，仍在同一个租户中。这样最后一步就能展示：客户看到一个设备，而运维人员看到两个。

在控制台中：**Organization → Customers**，以及设备的 **Assignment** 标签页。参见[资产、客户与区域](/user-guide/assets-customers-areas)和[管理分配](../guides/managing-assignments.md)。

## 5. 读数 {#5-readings}

```bash
./02-devices.sh sites/site-a.env up
```

为每个设备启动一个容器。每个容器都是围绕 `mosquitto_pub` 和 `mosquitto_sub` 写的几行 shell。冷库每四秒升温一度，发布 `temperature` 和 `humidity`，并保持一个订阅以接收命令。它对规则、告警和仪表板一无所知：它只负责上报和服从。这个订阅同时也使设备显示为在线。

几秒之内，读数就会出现在控制台（设备页面中的最新值）和 API 中：

```bash
curl -s "$DC_URL/api/device-state/graphql" -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"coldroom-a1\"){name value unit}}"}'
```

`$DC_TOKEN` 是租户访问令牌。[你的第一个设备的第 3 步](./first-device.md#3-get-a-tenant-token)给出了获取它的两次调用。

## 6. 你的第一个仪表板 {#6-first-dashboard}

仪表板是一份 JSON 定义，其中的组件绑定到**槽位**，例如“当前正在查看的设备”。创建第一个仪表板有两种方式，导览两种都用。

**在控制台中，从设备类型创建。**打开 **Devices → Device Types → Cold room**，选择 **Profile** 标签页，再选择 **Create dashboard**。抽屉会读取配置文件的声明，并预览一块看板：一个设备选择器、每个测量值一张卡片、一张图表、一个告警表，以及每条可以安全地从看板发送的命令各一个按钮。为它指定一个令牌，选择 **Create draft and open editor**，看板便带着实时数据打开。它是**草稿**：在你从编辑器的 **History** 面板发布之前，查看者什么也看不到。参见[从设备类型创建仪表板](/user-guide/dashboards#from-a-device-type)。

**用脚本。**`03-dashboards.sh` 根据 [`dashboard.jq`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/dashboard.jq) 构建同类的看板，用 `createDashboard` 创建，用 `publishDashboard` 发布，并且做两次：

- `tour-a-ops`，给运维人员，带有一个 **Send Boost cooling** 按钮；以及
- `tour-a-customer`，不带按钮，并与客户 `acme` **共享**（`shareDashboard`）。仅限某个客户的成员，只有在仪表板与该客户共享且已发布时才能看到它。

打开 `DC_URL/dashboards/tour-a-ops`。两块看板除了按钮之外没有任何不同，这正是由同一份定义生成它们的意义。

## 7. 规则触发告警 {#7-the-rule-raises-an-alarm}

等待大约半分钟。当某个冷库越过阈值，规则就会触发：**Monitor → Alarms** 中出现一条告警（看板的告警表和计数中也会出现），当读数回落时它会**自动清除**。清除不需要任何人编写：规则是边沿触发的，告警持续的时间与其条件成立的时间相同。告警列表为每个设备上的每个告警键保留一行，显示其最近一次触发。

冷库降温之后会再次升温，所以这个循环会重复：每次越限产生一条告警和一条命令。参见[告警](/user-guide/alarms)和[事件处理](../concepts/event-processing.md)。

## 8. 命令及其结果 {#8-command-outcome}

同一次触发还发送了命令 `boost-cooling`。设备在它的命令主题上收到命令，开始降温，并携带命令自己的令牌和这次投递的 nonce 进行**应答**。正是这个应答使命令从 `SENT` 变为 `SUCCESSFUL`。一个服从命令却从不应答的设备，会让命令停留在 `SENT`，直到超时。

`05-verify.sh` 还会像你那样自己发送一条命令：

```graphql
mutation {
  createCommand(request: {
    token: "my-first-command",
    deviceToken: "coldroom-a1",
    name: "boost-cooling",
    payload: "{\"targetC\":3}"
  }) {
    command { token status }
    rejection { code reason }
  }
}
```

它返回时是 `QUEUED`，几秒后变为 `SUCCESSFUL`：

```graphql
query {
  commandsByToken(tokens: ["my-first-command"]) { status outcome responsePayload }
}
```

除了错误，还要检查 `rejection`：被拒绝的命令是一个带有 `rejection` 的成功响应。在控制台中，设备页面的 **Commands** 标签页列出历史记录，看板上的按钮则会发送一条命令。参见[发送命令](../guides/sending-commands.md)和[命令](../concepts/commands.md#command-lifecycle)。

## 9. 客户看到的视图 {#9-customer-view}

`04-customer-user.sh` 邀请 `ops@acme.example` 成为租户成员，赋予内置的只读角色 `viewer`，访问范围限定为**一个客户** `acme`，然后用生成的密码接受邀请。在控制台中，这一步在 **Access → Members → Invite member** 中完成，它会显示一个一次性的邀请链接，由你亲自交给对方。脚本替你接受邀请，使导览可以无人值守地运行。

以 `ops@acme.example` 登录（密码在 `.state/tour-a/customer-password` 中）。该用户会看到：

- 只有 **Dashboards**、**Alarms** 和 **Devices** 的侧边栏，没有其他内容；
- 一个设备，`coldroom-a1`；
- 一个仪表板 `tour-a-customer`，带有实时读数和他们自己冷库的告警；
- 看不到 `coldroom-a2`，看不到它的读数和告警，也看不到运维人员的看板。

右上角的租户标签显示 `Acme Foods · viewer`。`05-verify.sh` 以客户用户的身份逐项检查以上内容，还会检查发送命令是否被拒绝。参见[成员、角色与客户用户](/user-guide/members-roles-and-customer-users)。

## 检查会打印什么 {#what-the-checks-print}

```text
PASS  the rule raised an alarm on coldroom-a1 and its command ended SUCCESSFUL
PASS  createCommand boost-cooling was accepted (QUEUED), not rejected
PASS  your own command reached SUCCESSFUL (the device answered; last status: SUCCESSFUL)
PASS  the operator sees both devices (2)
PASS  the operator sees alarms on both devices (2 devices with alarms)
PASS  the customer sees exactly coldroom-a1 (saw: coldroom-a1)
PASS  the customer sees alarms for coldroom-a1 only (saw: coldroom-a1)
PASS  the customer reads no state of coldroom-a2 (0 measurements)
PASS  the customer lists only the board shared with them (saw: tour-a-customer)
PASS  the operator's board is invisible to the customer
PASS  the read-only customer user cannot send a command (refused: FORBIDDEN)
```

客户用户请求一个不属于他们的设备时，得到的是空结果，而不是错误，因此脚本无法区分“不是你的”和“不存在”。这是有意为之。

## 同一份导览，第二个站点 {#second-site}

一个站点就是一个参数文件。[`sites/site-a.env`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/sites/site-a.env) 包含站点之间所有不同的内容：租户、它的管理员、客户、两个设备的令牌，以及规则和模拟冷库使用的数值。

```bash
./tour.sh sites/site-b.env all
```

站点 B 是另一个租户 `tour-b`，有另一个客户（`globex`，其用户为 `ops@globex.example`）、另外的设备，以及更低的告警阈值（6 度而不是 8 度）。除了这个文件，没有任何不同。要创建你自己的站点，复制一个站点文件，修改其中的值，然后运行同样的命令。每个租户都有自己的管理员、客户用户、设备密码和仪表板，彼此互不可见。

## 可选章节 {#optional-chapters}

这些章节更短，每一章都指向包含细节的页面。**默认是否开启**一列说明标准安装是否已经运行该功能，或者你是否需要先开启某些内容。

| 章节 | 增加什么 | 前提条件 | 默认是否开启？ | 从这里开始 |
| --- | --- | --- | --- | --- |
| 接入与自注册 | 设备用共享的预配密钥自我介绍，并自行生成凭据，而不是事先创建。 | 一个预配配置文件（**Connect → Provisioning Profiles**）。设备通过实例的 API 入口以 HTTPS 注册，所以需要有到达它的路由。 | 该能力属于标准安装。在你创建预配配置文件之前，不会注册任何设备。 | [设备自注册](../guides/device-self-registration.md) |
| X.509 客户端证书 | 设备用证书向代理认证，证书可以固定到某个设备，也可以通过在首次连接时注册设备的 CA 获得信任。 | `openssl` 和一个你信任的 CA（指南会生成一个一次性的）。仅限 MQTT；没有 CRL 或 OCSP。仓库中的 `examples/x509-device` 会跑完整个流程。 | 无需开启。只有当你添加信任锚或固定证书之后，信任才存在。 | [使用证书连接](../guides/device-x509.md) |
| 双因素认证 | 密码登录时使用验证器应用中的验证码，可用于单个账户，或由租户强制要求。 | 每个人的验证器应用；所有节点的时钟正确。 | 内置且**不强制**：每个人在 **Account security** 中自行开启，租户管理员可以强制要求。 | [双因素认证](../guides/two-factor-authentication.md) |
| 设备配置 | 由配置文件声明的共享属性构建的期望配置，带修订版本以及已应用或已拒绝的报告，下发到设备。 | 在配置文件上声明键并发布。设备必须使用 MQTT；只支持 HTTP 的设备无法使用该通道。 | 所有安装均可用。在配置文件声明键之前，设备不会收到任何内容。 | [连接设备](../guides/connecting-a-device.md#reserved-device-topics) |
| 已签名的空中升级 | 制品目录、签名者、已签名的分配和发布，每次尝试都会被跟踪，直到设备证明新版本已在运行。 | 用于存放制品内容的对象存储（共享卷或 S3）、一个已注册的签名者，以及一个遵循更新协议的 MQTT 设备。参考设备是 `examples/ota-device`。 | 包含在 `default` 配置档中。`telemetry` 和 `ingest-only` 配置档不包含它，没有对象存储的实例会以 `OBJECT_STORE_NOT_CONFIGURED` 拒绝制品。 | [空中升级](../concepts/over-the-air-updates.md)和[为更新签名](../guides/signing-updates.md) |
| SQL 与 BI | 通过 `analytics` 模式，对你自己租户的遥测数据进行只读 SQL 查询，供 Metabase、Grafana、Power BI 或 `psql` 使用。 | 一个由你声明的读取者。平台自己的数据库凭据绝不会交给 BI 工具。 | `analytics` 模式存在于每个安装中，在你声明读取者之前没有任何读取者。 | [SQL 与 BI 访问](../guides/sql-and-bi-access.md) |
| 自定义组件 | 你自己的 React 组件，在仪表板上绘制，数据绑定方式与内置组件相同。 | Node 和 npm，并且需要重新构建包含你的软件包的控制台或 `/dash`。没有运行时安装器。 | 关闭。只有内置组件。 | [编写自定义组件](../guides/writing-a-custom-widget.md)和[添加组件包](../guides/adding-widget-packages.md) |

## 如果有地方不对 {#if-something-did-not-work}

| 你看到的现象 | 通常意味着 |
| --- | --- |
| 脚本报 `error: ... cannot sign in` | 之前的某一步没有运行。请先运行 `./01-provision.sh <site>`；它会创建后续脚本所用的登录身份。 |
| `could not read the broker CA` | `INSTANCE` 不是你实例的名称，或者 `kubectl` 指向了另一个集群。请设置 `INSTANCE` 和 `KUBE_CONTEXT`。 |
| 检查一直等待，然后在第一行失败 | 设备没有在发布数据。运行 `./02-devices.sh <site> logs`。`publish refused` 表示凭据或租户名称不对；连接错误表示 `MQTT_HOST` 不是你集群节点的容器名称。 |
| 设备在线但没有告警 | 配置文件没有发布，或者规则的阈值高于冷库停止升温的位置。检查站点文件中的 `TOUR_ALARM_ABOVE`。 |
| 设备日志显示 `command ... answered`，但命令仍停留在 `SENT` | 应答没有被接受。应答必须携带命令自己的 `token` 和 `dispatchNonce`。 |
| 客户用户看不到任何设备 | 设备没有分配给该客户，或者成员的范围没有设置。`./04-customer-user.sh <site>` 会打印它找到的范围。 |

## 清理 {#cleaning-up}

```bash
./tour.sh sites/site-a.env down
./tour.sh sites/site-b.env down
```

会停止模拟设备。租户保留。删除租户会立即撤销访问权限，但它的令牌至少会被保留 12 小时才能再次使用，并且仍有成员的租户会被拒绝删除。参见[租户删除](../deployment/tenant-deletion.md)。要立即从零重新运行导览，请用 `dcctl destroy local <instance>` 销毁实例并重新引导一个新的，或者给站点文件换上新的租户令牌。

## 下一步 {#where-to-go-next}

- **[仪表板](/user-guide/dashboards)**：画布、组件、版本与共享。
- **[检测规则](/user-guide/detection-rules)**：表单、画布，以及让历史数据重放通过规则。
- **[通知策略](/user-guide/notification-policies)**：告警触发时通知相关人员。
- **[批量下发命令](../guides/commanding-a-fleet.md)**：一条命令发给多个设备。
