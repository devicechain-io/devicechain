---
sidebar_position: 3.6
title: 设备自注册
---

# 设备自注册 {#device-self-registration}

逐台手动创建设备，或导入一份设备文件，适用于事先已知整个设备群的情形。**自注册**针对另一种情形：设备群从生产线下线时统一烧录了一个共用密文，每台设备在首次上电时自行报到。设备通过 HTTPS 发送自己的序列号，自己生成凭据，并取回设备令牌和 MQTT 客户端 ID。除非你希望如此，否则无需事先录入设备的任何信息。

本页面向运维人员：需要准备什么、如何启动一批设备，以及出问题时怎么办。固件作者需要的确切请求、响应和错误码，见[设备协议参考](../reference/device-protocol.md#self-registration)。

## 如何协同工作 {#concept}

**自动注册配置（provisioning profile）**是整个设备群共用的入口。它包含：

| 字段 | 含义 |
| --- | --- |
| 预配密钥（Provision key） | 设备群出示的名称，在租户内唯一。不是机密。 |
| 预配密文（Provision secret） | 由服务器生成（256 位随机数），创建或轮换配置时**只显示一次**，仅以摘要形式存储。把它烧录进设备群的固件。 |
| 策略 | `ALLOW_NEW` 会创建平台从未见过的设备，也会采用你已预指定的设备。`CHECK_PRE_PROVISIONED` 只注册已存在且已为该配置预指定的设备。 |
| 设备类型 | 该配置注册的每台设备的类型，也是它唯一会采用的类型。 |
| 最大设备数 | 可选，限制该配置注册的设备数量。未设置时，配置没有上限。 |
| 启用、过期时间 | 已禁用或已过期的配置会立即拒绝所有注册。 |

设备持有**它自己的凭据**：它生成 32 个随机字节，编码为 base64url，保存起来，并在注册请求中发送。平台只存储摘要，因此任何人（包括管理员）都无法读回。凭据是访问令牌：设备用用户名 `{tenant}:{credential}`（无密码）和响应中给出的客户端 ID 通过 MQTT 连接。

## 设置配置 {#setup}

1. **创建配置**：在控制台的**自动注册配置（Provisioning Profiles）**下，或使用 `createProvisioningProfile`。选择设备类型、策略、可选的上限，以及（如果设备群分批出厂）过期时间。
2. **复制密文**：从随后弹出的对话框中复制。它只显示一次。如果丢失，请轮换它；旧密文会立即失效。
3. **确认已启用 API Ingress。**该端点与 GraphQL API 位于同一条 `/api/device-management` 路由。`ingress.enabled: false` 时，实例没有通往它的公开路径。
4. **检查客户端地址设置**，如果你运行自己的 Ingress 控制器或云负载均衡器：[客户端地址与受信任代理](../deployment/bootstrap.md#trusted-proxies)。那里出错不会阻止注册，但所有设备会因此共用同一份请求配额。

### 需要轮换 {#rotate-required}

在服务器开始生成密文之前创建的配置，其密文是由运维人员选定的。在你轮换它之前，它会被**拒绝**（`401 PROVISIONING_FAILED`，与任何错误密文一样），控制台会将其标记为**需要轮换（Rotate required）**。轮换会签发一个新密文（只显示一次），旧密文立即失效。更新固件后，设备再用新密文注册。

## 预指定 {#earmarking}

**预指定（earmark）**表示“这台已有设备可以被该配置接管”。无论哪种策略，配置只会采用已为它预指定的设备。这是有意为之：否则，任何持有设备群密文并知道运维人员所建设备序列号的人，都可以接管该设备。

创建设备时预指定：

- 在控制台中，使用设备表单的**预期自动注册配置（Expected provisioning profile）**字段；
- 在 CSV 导入中，使用 `provisioningProfileToken` 列（[列说明](./importing-devices.md#columns)）；
- 通过 GraphQL，在 `createDevice` 或 `importDevices` 上使用 `provisioningProfileToken`。

配置必须存在于该租户中，且设备类型与设备相同。设备详情页会显示它是如何加入平台的，并提供**重置预配（Reset provisioning）**；设备列表可以按签发其凭据的配置，或按它预期所属的配置筛选（`DeviceSearchCriteria` 中的 `provisioningProfile` 和 `expectedProvisioningProfile`）。

删除配置不会删除预指定：这些设备会回到没有预指定的状态，此后会被拒绝（`DEVICE_NOT_EARMARKED`）。

## 设备看到什么 {#what-a-device-sees}

对于密钥为 `acme-sensors-2026` 的配置和序列号为 `SN-000123` 的设备，固件所做的相当于：

```bash
# once, on the device: make the credential and keep it BEFORE sending anything
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' > /data/credential

curl -sS https://dc.example.com/api/device-management/provisioning/acme/register \
  -H 'Content-Type: application/json' \
  -d '{
    "provisionKey": "acme-sensors-2026",
    "provisionSecret": "'"$PROVISION_SECRET"'",
    "externalId": "SN-000123",
    "credentialSecret": "'"$(cat /data/credential)"'"
  }'
```

```json
{"outcome":"CREATED","tenant":"acme","deviceToken":"pv-sn-000123-1a2b3c4d","externalId":"SN-000123","credentialType":"ACCESS_TOKEN","mqtt":{"clientId":"<instanceId>:acme:pv-sn-000123-1a2b3c4d"}}
```

无需编写固件就可以试走整个流程。[`dcctl device register`](../reference/dcctl.md#dcctl-device-register) 扮演设备：它生成凭据，在发送任何内容**之前**先把它保存到 `--material-file` 指定的文件，遵循重试规则，并打印设备令牌和客户端 ID。再运行一次，它会报告 `REPLAYED`。

```bash
dcctl device register --server dc.example.com --tenant acme \
  --key acme-sensors-2026 --secret-file ./provision.secret \
  --external-id SN-000123 --material-file ./SN-000123.credential
```

有两条规则比其余都重要：

- **在第一次发送之前保存凭据。**平台只保留摘要。丢失凭据的设备无法通过再次注册来找回：它会为一台已持有凭据的设备出示新内容，并被 `409 ALREADY_PROVISIONED` 拒绝。此时运维人员需要使用**重置预配**，设备才能再次注册。
- **用同一份凭据重试，并且要等待。**响应丢失的注册会被识别为重放（`200 REPLAYED`），可以安全重复。遇到 `429` 和 `503` 时，请等待 `Retry-After` 再加随机抖动；不要连续猛发。[重试约定](../reference/device-protocol.md#self-registration-retry)列出了哪些响应应当重试，哪些需要人工处理。

## 轮换与重叠 {#rotation}

`rotateProvisioningProfileSecret`（控制台的**轮换密文**）是硬切换：旧密文在下一个请求起就失效，没有重叠。已经注册的设备不受影响，因为它们用自己的凭据连接，而不是用设备群密文。

如果轮换需要重叠，例如工厂仍在烧录旧密文，请创建**第二个配置**，使用它自己的密钥和密文，并为旧配置设置过期时间。

## 密文泄露时 {#compromise}

密文是首次接触时的全部信任基础：持有它的人可以注册一台尚未注册的设备。其影响取决于策略。在 `ALLOW_NEW` 下，是任何尚无人注册的序列号。在 `CHECK_PRE_PROVISIONED` 下，只是你预指定且尚未注册的序列号。`maxDevices` 上限对两者都有约束。

恢复步骤：

1. **轮换密文**，使泄露的密文失效。
2. **吊销该配置签发的设备**，如果其中有不属于你的设备（`revokeProvisionedDevices`，控制台的**吊销设备**）。它们的凭据会被禁用，其在线连接会被终止。
3. **查明注册了什么、何时注册。**按配置（`provisioningProfile`）筛选设备列表，并查看每台设备的创建时间。注册也会作为 `device.provision.created` 和 `device.provision.adopted` 记录在[审计日志](../user-guide/audit.md)中，归属于 `provisioning-profile:<token>`。重放会被计数，但不写入日志。
4. **重置你确实拥有的设备**（`resetDeviceProvisioning`），使它们可以用新密文再次注册。被吊销的设备在你这么做之前一直被拒绝。

## 禁用设备，不要删除 {#disable-dont-delete}

要把某台设备挡在外面，请禁用它，不要删除。被删除设备的序列号会重新变成未知，因此在 `ALLOW_NEW` 下，下一台出示该序列号的设备会被注册为新设备。

## 删除配置及其设备 {#deleting}

只要还有设备持有该配置签发的凭据（包括已吊销的），`deleteProvisioningProfile` 就会被拒绝：*“revoke the profile's devices first, then reset their provisioning or delete them”*。这样可避免设备记录的凭据来源指向一个不存在的对象。同样，只要还有自动注册配置使用某个设备类型，该设备类型就不能删除。

把 `maxDevices` 调低到低于已注册数量，只会拒绝后续注册；不会移除任何内容。

## 运行与维护 {#operating}

- **限制。**注册按来源地址、按租户、按副本限速，每个副本最多同时处理 64 个请求。数值以及各自的响应见[限制](../reference/device-protocol.md#self-registration-limits)。每租户上限属于租户[治理](../concepts/governance.md)的一部分，可以在其层级上或按租户设置（`provisioningRegistrationsPerSecond` 和 `provisioningBurst`）。
- **指标与告警。**`provisioning_requests_total` 按结果统计每个请求，包括探测产生的 `auth_failed` 洪流。三条告警监视它：[`ProvisioningAuthFailuresSustained`、`ProvisioningRateLimited` 和 `ProvisioningMisconfigured`](../deployment/observability.md#self-registration-alerts)。计数器见[指标参考](../deployment/metrics-reference.md)。
- **仅限 HTTPS。**设备不能通过 MQTT 连接来注册。
- **不签发 MQTT_BASIC。**自注册只签发 `ACCESS_TOKEN` 凭据。配置所要求的凭据类型若此构建无法签发，则以 `501` 拒绝。
