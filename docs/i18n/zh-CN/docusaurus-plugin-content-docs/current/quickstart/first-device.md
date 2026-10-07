---
title: 你的第一台设备
---

# 你的第一台设备 {#your-first-device-end-to-end}

完成本页后，你创建的设备将发送一条读数，而你能在控制台中看到它。无需硬件，也无需固件。“设备”就是一个 `curl` 命令；从平台的角度看，这已经足够。

请预留大约半小时，其中大部分时间用于等待初始化完成。

:::note 本页的前提条件
你需要 `dcctl`，以及 `PATH` 中的五个工具：`docker`、`kubectl`、`helm`、
[`kind`](https://kind.sigs.k8s.io/) 和 [OpenTofu](https://opentofu.org/)（即 `tofu` 二进制文件；
也可以使用 `terraform`）。无需事先准备集群。具体要求及为什么需要 `helm`，见下文的[前提条件](#prerequisites)。
:::

## 前提条件 {#prerequisites}

`dcctl install` 和 `dcctl bootstrap` 都会先运行预检。如果这五个工具中有任何一个缺失，命令就会**停止**。这样你只会花十秒发现缺失，而不是等上十分钟。

- 必须安装 `helm`。`dcctl` 自带 Chart，并通过 Helm 的 Go 库而不是命令安装，但预检仍会检查这个二进制文件。
- 缺少 `ko` 和 `cloud-provider-kind` 只会产生警告。只有从源码构建镜像（`--build`）时才需要 `ko`。
- 无需事先准备集群。`dcctl install local` 会查找名为 `devicechain` 的 kind 集群（或通过 `--cluster` 指定的名称）；如果不存在，就会询问是否创建。`--kube-context <name>` 可指向你已有的集群，该命令绝不会创建或删除这个集群。
- 无论采用哪种方式，都需要 Kubernetes **1.29 或更新版本**。旧版本会被拒绝，因为数据库 Chart 不支持它们。

`dcctl preflight local` 会运行完全相同的检查，但不初始化任何内容。[初始化指南](../deployment/bootstrap.md#prerequisites)提供了详细说明。

下列命令假设实例可以通过 `localhost` 上的普通 HTTP 访问，这正是第 1 步中的参数所配置的方式。

## 1. 启动实例 {#1-bring-up-an-instance}

先准备一次集群，再在其中创建实例：

```bash
dcctl install local
dcctl bootstrap local devicechain --host localhost --no-tls
```

`dcctl install` 创建 kind 集群，并安装集群中所有实例共享的组件：DeviceChain Operator 及其自定义资源定义、关系数据库、CloudNativePG Operator、cert-manager、监控和入口。每个集群只需运行一次；如果集群尚未完成这一步，`dcctl bootstrap` 就会拒绝执行。参见[安装集群](../deployment/bootstrap.md#install)。

实例 ID（这里是 `devicechain`）用于两个地方：

- 它为实例的 Kubernetes 命名空间命名，前面加上 `dci-` 前缀（`dci-devicechain`）。
- 它是本页所有设备主题和接入路径的第一个片段。

如果你选择其他 ID，请替换本页所有相应位置：主题和路径中使用 ID 本身，命令中的命名空间则在 ID 前加上 `dci-` 前缀。

初始化完成后，会输出命名空间、控制台 URL 和超级用户凭据。超级用户是 `superuser@devicechain.local`。没有默认密码：初始化会为该实例生成一个密码，并在输出末尾打印一次。之后若要再次读取：

```bash
kubectl -n dci-devicechain get secret dci-devicechain-superuser -o jsonpath='{.data.password}' | base64 -d
```

这个 Secret 保存的是超级用户**最初**获得的密码。如果你在控制台中修改密码，Secret 不会更新。

打开 `http://localhost/` 上的控制台并登录。此时控制台是空的，因为还没有租户，而每台设备都必须属于一个租户。

## 2. 创建租户 {#2-create-a-tenant}

创建租户属于实例级管理。无需手动调用管理 API，使用以下命令一步完成：

```bash
dcctl sim create demo
```

这个命令会：

- 创建租户 `sim-demo`；
- 创建限定于该租户的身份 `demo@sim.devicechain.local`，赋予租户管理员角色，但不授予任何实例级权限；
- 在 `~/.devicechain/sims/demo.json` 写入握手文件。

从握手文件中读取为该身份生成的密码：

```bash
cat ~/.devicechain/sims/demo.json
```

`simPassword` 字段是 `demo@sim.devicechain.local` 的密码。下一步会用到这两项。

:::tip 借用模拟器的命令
`dcctl sim create` 是[模拟器](#where-to-go-next)工作流的前半部分。这里使用它，是因为它会创建租户及限定于该租户的身份，正好满足需求；手动操作则需要调用实例管理 API 的三个变更操作。此后的步骤都使用任何应用都能使用的普通租户 API。
:::

## 3. 获取租户令牌 {#3-get-a-tenant-token}

认证需要两次调用。第一次证明你是谁。第二次选择你在哪个租户中操作，因为一个人可以属于多个租户。

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}",
       "variables":{"e":"demo@sim.devicechain.local","p":"<simPassword from step 2>"}}'
```

这会返回 `identityToken`。它说明你是谁，但不说明你在哪个租户中操作。将它换取限定于租户的 `accessToken`：

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}",
       "variables":{"t":"<identityToken>","n":"sim-demo"}}'
```

保存这个 `accessToken`。之后的每次调用都携带它：

```bash
export DC_TOKEN='<accessToken>'
```

## 4. 创建设备 {#4-create-the-device}

设备都有类型，因此先创建设备类型。所有对象都通过你选择的 **令牌**寻址：它是稳定、可读的标识，而不是自动生成的 ID。

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}",
       "variables":{"r":{"token":"temp-probe","name":"Temperature probe"}}}'
```

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001","deviceTypeToken":"temp-probe","name":"Bench sensor"}}}'
```

现在给设备添加凭据。设备提交凭据来证明自己的身份，平台默认要求提供凭据。

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
                         "enabled":true}}}'
```

请选择自己的 `credentialId`：任意不可猜测的字符串即可。对于 `ACCESS_TOKEN` 凭据，`credentialId` **本身就是**设备提交的密钥，因此应像保护密码一样保护它，而不是把它当成名称。

刷新控制台的**设备（Devices）**列表。`sensor-001` 已出现在列表中，但还没有数据。

## 5. 打通接入端点的访问路径 {#5-open-a-path-to-the-ingest-endpoint}

设备流量与 API 不使用同一个入口。Ingress 对外提供控制台和 `/api/…`。设备接入监听器使用独立端口，默认安装**不会**将它暴露到集群外。通过端口转发访问：

```bash
kubectl -n dci-devicechain port-forward svc/event-sources 8081:8081
```

让这个命令在独立终端中持续运行。

:::note 为什么需要这一步
这是默认安装的行为，而不是你的环境有问题。是否让设备群的接入端点可从公网访问，应由运营方明确决定，平台不会代你做出选择。真实部署会有意暴露它；如果只是从笔记本发送一次 `curl`，端口转发是更简单的做法。
:::

## 6. 发送读数 {#6-send-a-reading}

这个 `curl` 就是设备：

```bash
curl -i -X POST http://localhost:8081/devicechain/sim-demo/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001",
       "eventType":"Measurement",
       "credentialType":"ACCESS_TOKEN",
       "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
       "payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

`202 Accepted` 表示事件已入队。请求体中有两条规则，几乎每个人都会至少踩一次坑：

- **所有载荷都要将读数放入 `entries`**，即使只有一条读数。
- **所有数值都要写成 JSON 字符串：**使用 `"21.5"`，而不是 `21.5`。裸数字会被拒绝。

路径是 `/{instanceId}/{tenant}/events`。`devicechain` 是第 1 步的实例，`sim-demo` 是第 2 步的租户。这里的 `404` 表示**实例 ID**有误，因为路由只存在于当前实例自己的 ID 下。

错误的**租户**不会返回 `404`。只要租户名称格式正确，无论该名称的租户是否存在，都会以 `202` 接受请求。事件会在下游被丢弃，响应不会说明这一点。如果收到 `202` 却没有数据，请先检查租户名称。

再发送几条不同数值的读数，这样看到的就是一条曲线，而不是一个点：

```bash
for t in 21.9 22.4 22.1 23.0; do
  curl -s -o /dev/null -X POST http://localhost:8081/devicechain/sim-demo/events \
    -H 'Content-Type: application/json' \
    -d "{\"device\":\"sensor-001\",\"eventType\":\"Measurement\",
         \"credentialType\":\"ACCESS_TOKEN\",
         \"credentialId\":\"5f989616-2a0d-4160-8ae1-da5fad2898b2\",
         \"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\"}}]}}"
  sleep 1
done
```

## 7. 查看数据 {#7-see-your-data}

在控制台中打开 `http://localhost/devices/sensor-001`。设备现在显示为**在线（Online）**，并显示 `temperature` 及其最新值。没有任何组件主动声明它在线：对于使用 HTTP 的设备，在线状态是根据收到事件这一事实推断的。

通过 API 查看同样的最新值：

```bash
curl -s -X POST http://localhost/api/device-state/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"sensor-001\"){name value unit occurredTime}}"}'
```

查看历史数据，而不仅是最新值：

```bash
curl -s -X POST http://localhost/api/event-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{measurementEvents(criteria:{pageNumber:1,pageSize:20,deviceToken:\"sensor-001\"}){results{name value occurredTime} pagination{totalRecords}}}"}'
```

现在，你已经打通设备的完整流程：注册、配置凭据、上报数据并查询数据。

## 故障排查 {#if-something-did-not-work}

| 现象 | 常见原因 |
| --- | --- |
| 接入 `POST` 返回 `404` | 路径中的**实例 ID**有误。如果你没有修改，它应是 `devicechain`。错误的租户不会产生这个状态码。 |
| `:8081` 连接被拒绝 | 第 5 步的端口转发没有运行。 |
| 接入 `POST` 返回 `400` | 使用了裸数字而非字符串、读数未放入 `entries`，或租户路径片段不是有效的令牌。 |
| 返回 `202`，但没有数据显示 | **租户**不存在（格式正确的名称都会被接受，无论它是否对应真实租户），或凭据不匹配。请求体中的 `credentialId` 必须与第 4 步创建的完全一致。 |
| 接入 `POST` 返回 `429` | 租户超过接入上限：你每秒发送的读数多于其层级允许的数量。事件未被接受。响应带有 `Retry-After` 标头，请退避后重试。 |
| 接入 `POST` 返回带 `Retry-After` 标头的 `503` | 平台正在拒绝所有租户的新事件（[背压](../deployment/observability.md#ingest-backpressure)）。事件**没有**被存储。等待 `Retry-After` 指定的秒数（10），再重新发送。`curl -i` 会输出响应标头。 |
| 接入 `POST` 返回不带 `Retry-After` 标头的 `503` | 事件无法交给消息流，但仍**可能**已被存储。请重新发送，不过除非事件包含 `altId` 和 `occurredTime`，否则重发会存储第二份副本（参见[服务质量](../guides/connecting-a-device.md#quality-of-service)）。 |
| API 调用返回未授权 | 访问令牌已过期，或你发送的是第 3 步第一次调用返回的 `identityToken`，而不是第二次返回的 `accessToken`。 |

`202`、`400` 和 `404` 对于该次请求都是终止结果：不要原样重发。对于 `429` 和两种 `503`，请按上文说明重试。

## 下一步 {#where-to-go-next}

- **运行模拟设备群。** 一台设备还不算设备群。第 2 步的 `dcctl sim create` 同时配置了一个模拟场景。构建并运行模拟器，让它创建包含完整数据的租户并持续发送数据：

  ```bash
  cd backend/sims/dc-simulator && make build
  ./build/dc-simulator --handshake ~/.devicechain/sims/demo.json
  ```

  然后使用 `dcctl sim status demo`、`dcctl sim stop demo` 和 `dcctl sim start demo` 控制它。模拟器访问的是同一个接入端点，因此同样需要第 5 步的端口转发。

- **[连接设备](../guides/connecting-a-device.md)**：使用真实传输方式 MQTT，在连接和事件中都提供凭据，并了解所有载荷格式和流水线执行的规则。
- **[传输能力矩阵](../reference/transport-matrix.md)**：在选定传输方式之前，了解各方式在两个方向上支持的功能。
- **[发送命令](../guides/sending-commands.md)**：了解另一个方向的通信。
- **[事件处理](../concepts/event-processing.md)**：将这些读数转化为告警。

## 清理 {#cleaning-up}

```bash
dcctl sim destroy demo
dcctl destroy local devicechain
```

`dcctl destroy` 删除实例，保留已安装的集群，方便下一次初始化。它会等待实例命名空间完全消失，因此集群可以立即使用相同名称重新初始化。如果命令被中断，再次运行就能完成清理。

:::warning 先移走密钥托管文件
再次使用相同名称初始化之前，请移走 `destroy` 指出的密钥托管文件。下一个实例会生成自己的密钥，初始化不会覆盖旧文件。
:::

如果还要删除集群：

```bash
kind delete cluster --name devicechain
```
