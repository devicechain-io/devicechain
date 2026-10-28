---
sidebar_position: 3.7
title: 使用证书连接设备
---

# 使用证书连接设备 {#connecting-with-a-certificate}

设备可以用 X.509 客户端证书而不是令牌或密码向平台消息代理认证。本指南带你从证书颁发机构（CA）一直走到设备发布第一个事件，并展示证书成为设备的两种方式：为已存在的设备创建的**固定凭据**，以及在设备首次连接时为其注册的**证书配置**。

平台如何决定信任一张证书，以及这种信任到哪里为止，见[证书身份](../concepts/device-certificates.md)。在围绕它规划设备群之前，请先阅读其中的[限制](../concepts/device-certificates.md#not-supported)：没有 CRL 或 OCSP，带名称约束的 CA 会被拒绝，续期和 C# SDK 尚未提供。

:::note 状态
在平台消息代理上的 MQTT 中可用。HTTP 和 LwM2M 不接受客户端证书。
:::

## 你需要什么 {#what-you-need}

- 一个你打算信任其证书的 CA，以及每台设备的证书。本指南用 `openssl` 生成一个一次性的 CA，让你可以试跑整个流程；生产环境请使用你自己的 PKI。
- 一个租户，以及管理设备的权限（`device:write`）。
- 实例 CA 证书，让设备可以验证消息代理，与[连接设备](./connecting-a-device.md#publishing-an-event)中相同。

## 生成测试 CA 和设备证书 {#mint}

设备证书必须带有**客户端认证**扩展密钥用途，且不能是 CA。它的通用名称（CN）会成为设备的外部 ID，所以请使用序列号或你希望设备被识别的其他 ID。

```bash
# A CA: a P-256 key and a self-signed certificate that may sign certificates.
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Acme Device CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out ca.crt

# A device: its own key, and a certificate the CA signs for it. CN = the device's external id.
openssl ecparam -name prime256v1 -genkey -noout -out device.key
openssl req -new -key device.key -subj "/CN=SN-000123" -out device.csr
cat > device.ext <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in device.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days 90 -extfile device.ext -out device.crt

# Check what you made.
openssl x509 -in device.crt -noout -subject -ext extendedKeyUsage -enddate
openssl verify -CAfile ca.crt -purpose sslclient device.crt
```

最后一条命令必须输出 `device.crt: OK`。请妥善保管 `ca.key` 和 `device.key`。只有 `ca.crt` 和 `device.crt` 会交给平台。

平台接受 2,048 至 8,192 位的 RSA 密钥、P-256 或 P-384 的 ECDSA，以及 Ed25519，并拒绝证书链中任何位置的 SHA-1 签名。90 天的有效期是有意为之：平台没有吊销列表，短有效期就是让不需要的证书自行消失的方式。

## 将 CA 添加为信任锚 {#trust-anchor}

信任锚告诉租户该相信哪个 CA。在控制台中打开**信任锚（Trust Anchors）**，选择添加，然后粘贴或上传 `ca.crt`。控制台会拒绝包含私钥的文件。控制台页面见 [预配置与信任锚](../user-guide/provisioning-and-trust-anchors.md)。通过 GraphQL：

```graphql
mutation {
  createDeviceTrustAnchor(request: {
    token: "acme-device-ca"
    name: "Acme device CA"
    certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
  }) { token fingerprintSha256 notAfter enabled }
}
```

信任锚必须恰好是一张 CA 证书，未过期，使用受支持的密钥，且不带名称约束。如果你的设备证书由中间 CA 签发，请锚定在中间 CA 而不是根 CA 上，这样租户只信任签发你设备群的那个 CA。

## 选择设备的出现方式 {#choose}

每个设备群选择其中一种。

### 方案 A：把证书固定到你创建的设备上 {#pinned}

适用于你自己创建设备并且知道每台设备证书的情形。

1. 创建设备（批量创建见[导入设备](./importing-devices.md)）。令牌由你选择。
2. 添加一个 `X509_CERTIFICATE` 凭据。在控制台中打开设备的**凭据（Credentials）**标签页，选择证书类型，粘贴 `device.crt` 并选择信任锚。通过 GraphQL：

   ```graphql
   mutation {
     createDeviceCredential(request: {
       token: "sn-000123-cert"
       deviceToken: "sensor-001"
       credentialType: "X509_CERTIFICATE"
       enabled: true
       certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
       trustAnchorToken: "acme-device-ca"
     }) { token credentialId expiresAt certificate { subjectCn trustAnchor { token } } }
   }
   ```

3. 不要发送 `credentialId`；该类型会拒绝它。服务器把它计算为证书的 SHA-256 并返回。它是公开的，就是固定值（pin）。

平台在你创建凭据时验证证书链，并拒绝无法通过认证的凭据。一台设备最多可持有三个有效的证书凭据，因此可以在旧证书到期前先添加下一张。凭据的到期时间是整条证书链上最早的到期时间。

### 方案 B：设备首次连接时注册 {#registration}

适用于出厂时已装好证书的设备群。事先不需要存在任何设备。

1. 创建**证书配置**。在控制台中打开**自动注册配置（Provisioning Profiles）**，选择**证书（Certificate）**模式并选择信任锚。通过 GraphQL，创建一个指明信任锚的自动注册配置：

   ```graphql
   mutation {
     createProvisioningProfile(request: {
       token: "acme-sensors-certs"
       provisionKey: "acme-sensors-certs"
       strategy: "ALLOW_NEW"
       deviceTypeToken: "acme-sensor"
       enabled: true
       maxDevices: 5000
       trustAnchorToken: "acme-device-ca"
     }) { profile { token trustAnchor { token } } issuedSecret }
   }
   ```

   `issuedSecret` 返回 null：证书配置没有需要烧录、保护或轮换的密文。
2. 设置 `maxDevices`。在 `ALLOW_NEW` 下，该信任锚的 CA 签发的、通用名称尚无设备使用过的任何证书都会注册一台新设备，所以上限和较窄的信任锚决定了一次失误的影响范围。
3. 启动设备群。设备第一次连接时，平台以证书的通用名称作为外部 ID 创建设备，把证书固定到它上面并放行。之后的连接使用这个固定值。

设备令牌由其通用名称派生，这对下面的客户端 ID 很重要。首次连接时注册只会创建新设备。你创建并预指定给该配置的设备目前还不能通过证书注册，注册被重置过的设备同样不行；请为这些设备使用固定凭据（方案 A）。已持有一张证书的设备再出示另一张不同的证书，在续期功能推出之前会被拒绝，所以请先把新证书添加为固定凭据。

## 连接 {#connect}

设备通过 TLS 连接到与其他设备相同的 MQTT 端点。不同之处：

| 设置 | 值 |
| --- | --- |
| 客户端证书 | `device.crt`（后面跟任何中间 CA 证书）和 `device.key` |
| 用户名 | `{tenant}:@x509` |
| 密码 | 无。与该用户名一起发送密码会被拒绝 |
| 客户端 ID | `{instanceId}:{tenant}:{deviceToken}`，与所有设备相同 |

`@` 使该用户名不可能与凭据 ID 混淆。租户来自用户名，绝不来自证书。

```bash
mosquitto_pub \
  --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:pv-SN-000123-06e08144e3e8' \
  -u 'acme:@x509' \
  -t 'devicechain/acme/devices/pv-SN-000123-06e08144e3e8/events' \
  -m '{"device":"pv-SN-000123-06e08144e3e8","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

事件正文和主题与其他设备相同，见[连接设备](./connecting-a-device.md)。证书由中间 CA 签发的设备，必须在自己的证书之后发送该中间证书，因为平台是用设备出示的内容构建到信任锚的证书链。请发送叶子证书加至多一张中间证书：平台最多接受五张证书和 16 KiB，而且某些 TLS 栈（默认 16 KiB 输出缓冲区的 mbedTLS）无法发送更长的证书链。

### 客户端 ID {#client-id}

固定凭据的设备（方案 A）使用你给它的令牌，一如既往。首次连接时注册的设备（方案 B）使用平台根据其通用名称派生的令牌，因此设备可以根据自己的证书计算出它：

```text
pv-{slug}-{hash}
```

- `slug`：通用名称中除字母、数字、`-` 和 `_` 之外的每个字符都替换为 `-`，再去掉首尾的 `-` 和 `_`。
- `hash`：通用名称的 SHA-256 的前 12 个十六进制字符。
- 整个令牌最长 128 个字符；过长的 `slug` 会被截短以适应。

用 shell 计算上面那张证书的结果：

```bash
CN=SN-000123
slug=$(printf '%s' "$CN" | sed -E 's/[^A-Za-z0-9_-]/-/g; s/^[-_]+//; s/[-_]+$//')
hash=$(printf '%s' "$CN" | openssl dgst -sha256 -hex | sed 's/^.*= //' | cut -c1-12)
echo "devicechain:acme:pv-$slug-$hash"     # devicechain:acme:pv-SN-000123-06e08144e3e8
```

客户端 ID 不匹配的首次连接会在创建任何内容之前被拒绝，所以一次失误不会留下注册了一半的设备。通用名称必须为 1 到 256 个字符，不能是空白，也不能含有 NUL 字符。

如果你事先为某个证书配置创建设备，请把令牌留空并填写外部 ID：控制台和 API 会填入派生的令牌，并拒绝不同的值。

## 连接被拒绝时 {#refused}

与所有设备拒绝一样，设备只会看到 CONNACK 返回码 5。原因在运维方一侧，体现为计数器 `callout_x509_total` 的 `result` 标签。请按以下顺序检查：

1. **`no_cert`。** 客户端没有发送证书。检查 `--cert`，以及密钥是否与证书匹配。
2. **`chain_invalid`。** 证书链无法构建到信任锚、某张证书已过期、扩展密钥用途缺少客户端认证、签名使用了 SHA-1，或密钥不在允许范围内。可以用 `openssl verify -CAfile ca.crt -purpose sslclient device.crt` 重现，并检查设备是否发送了中间证书。
3. **`anchor_disabled`、`revoked`。** 信任锚或凭据被有意禁用。
4. **`client_id_mismatch`。** 客户端 ID 不是该设备的 `{instanceId}:{tenant}:{deviceToken}`；对于首次连接，则不是派生的那个。
5. **`unknown_pin`。** 该证书未被固定，也未链到任何可用的证书配置。
6. **`jit_*`。** 首次连接时的注册被拒绝：`jit_rate_limited` 和 `jit_busy` 表示设备应退避后重试，`jit_ambiguous` 表示两个配置并列，其余的带有注册错误码。

[可观测性](../deployment/observability.md#certificate-alerts)列出了各项告警及处理方法。与令牌或密码用户名一起发送的证书会被忽略（计为 `x509_cert_ignored`），连接按令牌或密码判断。

## 重试与有效期 {#lifetimes}

- **带退避地重试。** 因 `jit_rate_limited` 或 `jit_busy` 被拒绝的设备并没有错；MQTT 无法表达“稍后再试”。请使用带完全抖动（full jitter）的指数退避，避免一批出厂设备冲击消息代理。
- **会话在最早到期时结束。** 证书链中最早的证书到期时，平台会关闭会话，没有宽限期。请在此之前续期并用新证书重新连接。在续期功能推出之前，设备需要在旧证书到期前把新证书固定为第二个凭据（方案 A）。
- **5 分钟的容差**用于刚刚签发的证书，以及时钟略慢的设备。

## 不写固件也能试用 {#simulator}

模拟器可以扮演证书设备：`dc-simulator --x509` 生成一个一次性的 CA 和若干证书（ECDSA P-256、客户端认证、CN = 外部 ID），用 `--x509-register` 通过 API 注册它们，并让每台设备连接。`--x509-jit N` 为永不注册的设备生成证书，用于演练证书配置。参数见模拟器的 README。

## 它不做什么 {#limits}

- **没有 CRL，没有 OCSP。** 要停用一张证书，请禁用它的凭据（或该设备，或信任锚），并签发短期证书。
- **带名称约束的 CA 会被拒绝。** 信任锚是身份的边界。
- **已注册设备不能通过出示新证书续期，被重置的设备也不能重新采用。** 目前尚未提供。
- **C# SDK 中没有客户端证书。** 目前尚未提供。
- **HTTP 或 LwM2M 上没有证书认证。**

原因见[概念页](../concepts/device-certificates.md#not-supported)。消息代理一侧的内容，包括升级现有实例时会发生什么变化，见[证书认证](../deployment/device-certificates.md)。
