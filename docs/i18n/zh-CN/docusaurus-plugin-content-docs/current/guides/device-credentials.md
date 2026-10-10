---
sidebar_position: 5
title: 设备凭据
---

# 设备凭据 {#device-credentials}

设备的**身份**是稳定的令牌。**凭据**是认证时提交的材料，与身份分开保存。设备可以持有多个凭据并轮换，而无需改变身份。

:::note 状态
已提供。通过控制台设备详情页的**凭据（Credentials）**选项卡，或 device-management GraphQL API 管理。
:::

## 凭据类型 {#credential-types}

| 类型 | 设备提交内容 | 保存的密钥 |
| --- | --- | --- |
| `ACCESS_TOKEN` | bearer 令牌（凭据 ID） | 无，持有 ID 本身就是证明 |
| `MQTT_BASIC` | 用户名（凭据 ID）+ 密码 | 密码 |

:::note 不接受 X.509 凭据
在证书验证功能发布之前，不支持 `X509_CERTIFICATE` 类型。创建或更新该类型的凭据会被拒绝（`UNSUPPORTED`），已存在的该类型凭据也不再通过认证。请使用 `ACCESS_TOKEN` 或 `MQTT_BASIC`。
:::

## 读取凭据需要 `device:write` {#reading-a-credential}

存在密钥的类型，其密钥是**只写**的。只有 `MQTT_BASIC` 有密钥，即密码。注册凭据时提交密码，读取时绝不返回。控制台从不显示它：在掩码字段中输入，凭据创建后字段清空。此后 API 对该值返回 `null`。

这只保护 `MQTT_BASIC` 密码。`ACCESS_TOKEN` 没有可隐藏的存储密钥，因为 **`credentialId` 本身就是 bearer 凭据**。`credentialId` 是直接可读字段。因此，无论类型，读取设备凭据都会提供以该设备身份认证所需的内容。

因此，所有返回凭据的查询都需要 **`device:write`**，而不是 `device:read`。只读用户不能列出设备凭据，所以控制台不向其显示**凭据（Credentials）**选项卡。这不会减少 `device:write` 持有者的能力：他们已经能够为租户任何设备注册凭据并模拟其身份。它阻止的是这种能力进入所有启用租户成员都获得的只读基础权限。

## 设备如何提交凭据 {#how-a-device-presents-a-credential}

任何传输都在事件体中携带凭据（参见[连接设备](./connecting-a-device.md)）：

```json
{
  "device": "sensor-001",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "eventType": "Measurement",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

`MQTT_BASIC` 还携带 `"credentialSecret": "<password>"`。

平台将凭据解析到所属设备并验证。验证遵守凭据**有效期**，**禁用**凭据即撤销。实例设备认证模式决定执行严格程度：

- `disabled`：信任自行声明的 `device` 令牌，不需要凭据。
- `optional`：提交凭据时以凭据为准，没有凭据时信任设备令牌。
- `required`：没有有效凭据就拒绝事件。**这是默认值。**

认证成功后，以凭据解析出的设备为准。事件 `device` 令牌若指定*另一台*设备，会被拒绝，因此已认证设备无法冒充其他设备。

### 撤销多久生效 {#revocation-timing}

每个 `device-management` 副本将刚验证过的凭据在内存保留最多五秒，使后续事件无需再次读数据库。内存副本与存储凭据采用完全相同检查：每个事件都比较 `MQTT_BASIC` 密码，有效期在对应时刻生效。验证失败的凭据绝不缓存，因此修正后下一事件即可生效。

禁用、删除或修改凭据，或替换、编辑、删除设备时，执行变更的副本在回应前删除自己的缓存，然后通知其他副本删除。因此，撤销通常在设备下一事件生效。如果通知丢失，例如副本重连消息代理，或升级时部分副本仍运行上一版本，被撤销凭据仍可能在该副本上认证事件，最长为变更后五秒。

连接不受此缓存影响。每次 MQTT 连接，不论使用密码还是访问令牌，都检查数据库，因此被撤销凭据不能在任何副本建立新连接。删除租户不会清除这些缓存。移除凭据时已经排队的租户事件，之后最多五秒仍能通过内存认证。

## 两层检查：连接与事件 {#two-layers-the-connection-and-the-event}

事件体凭据用于**逐事件**检查。MQTT 和 NATS **连接**也在消息代理本身认证：

- **TLS。** MQTT 和 NATS 监听器使用 TLS，设备通过实例 CA 建立 TLS 连接。
- **Auth-callout。** 消息代理要求平台批准每个新连接（NATS auth-callout）。平台认证连接，并将其限定在该设备的主题，而非整个租户主题。设备只能发布自己的事件、读取自己的命令，不能做其他事。`MQTT_BASIC` 设备使用 MQTT 用户名 `{tenant}:{credentialId}` 和凭据密码连接，与认证事件的凭据相同。无法认证的设备连连接都无法建立。
- **客户端 ID。** 连接还必须提供 MQTT 客户端 ID `{instanceId}:{tenant}:{deviceToken}`。可以加 `:` 和设备自选后缀，例如 `{instanceId}:{tenant}:{deviceToken}:cmd`。后缀允许第二个并发会话不踢掉第一个，例如一条连接发布，另一条订阅命令。不是以设备自身 `{instanceId}:{tenant}:{deviceToken}` 开头的 ID 会被拒绝。消息代理按客户端 ID 保存会话，自由选择会让设备接管另一设备会话。

传输详情见[连接设备](./connecting-a-device.md)。

## 重复连接失败会被延缓 {#connect-backoff}

反复失败后，提供用户名和密码的 MQTT 连接会被延缓，使密码不能按代理接受连接的速率被猜测。

- **计数范围：**一个 MQTT 用户名（`{tenant}:{credentialId}`）的连续密码连接失败。未知用户名与真实用户名同样计数、同样拒绝，答案不泄露哪些用户名存在。拒绝耗时接近但不完全相同：存在的用户名需要额外读取一条数据库行，未知用户名不需要。该行只保存检查必需内容，因此差异不会随设备元数据、名称或描述大小增加。
- **时间安排：**前 10 次连续失败不延缓。第 10 次后，下一次尝试等待 1 秒。每次后续失败将等待翻倍，最多 30 秒。
- **等待期间，正确密码也被拒绝。** 消息代理给出与错误密码相同的拒绝。等待结束后正常连接。
- **成功连接重置计数。**
- **不延缓：**访问令牌连接（无密码连接），以及事件体中的凭据。逐事件检查不向发送者回应，因此无法用于猜测。

:::warning 知道设备用户名的人可以延迟其重连
计数按用户名，而用户名不是秘密。持续发送错误密码的人可以持续阻止该设备重连：每次等待最多 30 秒，但结束后可以立即开始下一次。已连接设备在重连前不受影响。不要公开设备用户名，并为每个 `MQTT_BASIC` 凭据使用强密码。
:::

计数保存在消息代理持久存储 JetStream 中，因此每个 device-management 副本看到相同计数。

- **JetStream 不可达时，密码连接被拒绝**，直到恢复，因为无法计数的连接不会完成检查。这包括保存计数的存储桶 JetStream 领导者变化的短暂窗口，例如 NATS 节点重启。访问令牌连接仍可用。
- **保存计数的存储桶有容量限制。** 每次连接，无论成功与否，都保留条目十分钟。存储桶满时，连接继续工作，但**不再延缓**，触发 `DeviceCredentialAttemptStoreFull` 告警。大量不同用户名连接，或极大设备群同时重连时会发生。十分钟后条目自行消失。需要更多空间时，提高 `instance.config.infrastructure.nats.kvStateMaxBytes`。

`devicechain_devicemanagement_credential_checks_total` 按 `outcome` 统计每次密码连接检查，结果包括：

- `throttled`：等待期间拒绝连接。
- `unavailable`：无法访问计数。
- `store_full`：存储桶满。

## 注册凭据（控制台） {#register-a-credential-console}

1. 打开设备详情页，选择**凭据（Credentials）**选项卡。
2. 选择凭据**类型**并填写对应字段：生成或粘贴访问令牌、输入 MQTT-basic 用户名和密码。
3. 对 `MQTT_BASIC`，继续前记录密码。成功后字段清空，密码不再显示。
4. 点击**添加凭据（Add credential）**。

在对应行删除凭据。之后设备无法再使用它认证。

## 注册凭据（GraphQL） {#register-a-credential-graphql}

```graphql
mutation {
  createDeviceCredential(request: {
    token: "b2e1…",                 # a fresh unique credential token
    deviceToken: "sensor-001",
    credentialType: "ACCESS_TOKEN",
    credentialId: "5f989616-2a0d-4160-8ae1-da5fad2898b2",
    enabled: true
  }) { id token credentialType credentialId enabled }
}
```

对于 `MQTT_BASIC`，还要传入 `credentialValue: "<password>"`（只写）。密码完全按提交内容保存，包括前后空格，因此必须与设备提交内容逐字节一致；空值表示不保存密码。注册凭据和列出设备凭据都需要 `device:write` 权限，参见[读取凭据需要 `device:write`](#reading-a-credential)。
