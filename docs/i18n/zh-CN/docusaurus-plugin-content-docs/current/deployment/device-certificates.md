---
title: 证书认证
---

# 证书认证 {#certificate-authentication}

本页是[设备客户端证书](../concepts/device-certificates.md)的运维一侧：使其成为可能的消息代理构建、限定首次连接时注册的设置、需要监控什么，以及升级会给已在运行的实例带来什么变化。要连接设备，见[使用证书连接设备](../guides/device-x509.md)；控制台一侧见 [预配置与信任锚](../user-guide/provisioning-and-trust-anchors.md)。

## 消息代理构建 {#broker}

NATS 服务器无法按 DeviceChain 需要的方式在其 MQTT 监听器上请求客户端证书：它的配置文件只能关闭证书，或要求每个客户端都提供证书。因此平台消息代理是 DeviceChain 自己构建的 `nats-server`，即 `dc-nats-server`：未经修改的上游 `v2.15.1`，外加一项设置，在启动时和每次配置重载时都会应用。MQTT 监听器**请求但不强制**客户端证书。

- **令牌和密码设备不受影响。** 不发送证书的客户端照常连接。与令牌或密码用户名一起发送的证书会被忽略。
- **由认证回调（auth callout）完成检查。** 消息代理把客户端出示的证书交给 device-management 中的回调，由它验证证书链。消息代理本身不对信任做任何决定。
- **镜像。** `ghcr.io/devicechain-io/dc-nats-server`，与其他镜像一样经过签名且可验证（[验证发布版本](./verifying-releases.md)）。`dcctl` 按发布时推送的摘要（digest）固定它，所以没有改动消息代理的发布不会让它重启。请把它加入你镜像同步的镜像列表。
- **消息代理以 root 身份运行**，与上游镜像相同，以便它能打开由上游构建的服务器写入的 JetStream 存储。
- **只支持一种消息代理镜像。** 上游镜像仅作为回退途径。

### 监控消息代理 {#watching}

服务器在启动时、每次重载后以及每分钟，通过与自己的 MQTT 端口握手来检查监听器是否仍在请求证书。结果在端口 `7778` 上导出（图表 PodMonitor 上的 `dc-metrics` 端点）：

| 指标 | 含义 |
| --- | --- |
| `dc_broker_mqtt_client_auth_request` | MQTT 监听器正在请求客户端证书时为 `1`，否则为 `0` |
| `dc_broker_mqtt_tls` | MQTT 监听器正在提供 TLS 时为 `1` |
| `dc_broker_info{library_version,build}` | 正在运行的是哪个二进制 |

当某个 TLS 监听器不再请求证书，或某个消息代理 Pod 没有导出 `dc_broker_info`（上游镜像就是这样）时，`BrokerMqttClientAuthLost` 触发。它在[告警手册](./observability.md)中。告警触发期间，使用证书认证的设备会被拒绝（`no_cert`）；令牌和密码设备不会。

### 回退到上游消息代理 {#rollback}

设置基础设施变量 `server_image_repository = "nats"` 和 `server_image_tag = "2.15.1-alpine"`。此后所有证书设备都会被拒绝，因为上游消息代理从不请求证书，`BrokerMqttClientAuthLost` 会说明原因。其他一切不受影响。

## 设置 {#settings}

首次连接时注册和签发方固定有以下固定设置，在 device-management 启动时读取，位于该区域的 `config` 下：

```yaml
functionalAreas:
  device-management:
    config:
      callout:
        jitMaxInFlight: 16       # default 16, 1 to 128
        jitSourceTrusted: false  # default false
        issuerPinDisabled: false      # default false
        issuerPinRefresh: 30s         # default 30s, 5s to 10m
        issuerPinMinRefresh: 2s       # default 2s, 100ms up to issuerPinRefresh
        issuerPinGatherWindow: 750ms  # default 750ms, 100ms to 2s
```

| 键 | 含义 |
| --- | --- |
| `callout.jitMaxInFlight` | 一个副本同时处理多少个首次连接时的注册。再来的一个会被拒绝（`jit_busy`），设备退避后重试。这些名额取自回调自身的并发上限，所以一波首次连接绝不会占用重新连接的设备所需的名额，已固定的证书、令牌或密码也无需等待它们。 |
| `callout.jitSourceTrusted` | 增加按客户端地址的限制，其大小由 `provisioning.sourceBurst` 和 `provisioning.sourceEvery` 决定。**仅当** MQTT Service 保留客户端地址（`externalTrafficPolicy: Local` 或 kind 的 NodePort）时才启用。在源地址 NAT 之后，所有设备共用一个地址和一份额度，一台设备的重试就会拒绝整个设备群。关闭时，持续的拒绝会触发告警，而不是按地址限流。 |
| `callout.issuerPinDisabled` | 关闭[签发方固定](#issuer-pin)。默认开启。 |
| `callout.issuerPinRefresh` | device-management 多久重新获取一次代理的在线服务器。超过此值的三倍未刷新的列表不再被信任。 |
| `callout.issuerPinMinRefresh` | 两次服务器探测之间的最短间隔，无论有多少请求指向固定机制不认识的服务器。这也是设备因上次刷新之后才加入的代理服务器而被拒绝的最长时间。 |
| `callout.issuerPinGatherWindow` | 一次探测等待服务器应答的时间。所有服务器都应答后会提前结束。 |

每租户的限制不在这里设置：它是租户的注册上限，与[自注册](../guides/device-self-registration.md)计入的是同一个，与租户的其他治理限制一起管理。对同一配置的注册在该配置上轮流进行，所有副本合计约每个配置每秒 100 到 200 个，所以一万台设备的首次连接高峰，会在设备一两分钟的重试内完成注册。每租户额度对每次证书链验证通过的尝试都计费，包括之后被拒绝的那次，所以用轮换后的证书反复重试的设备会消耗它。

`callout` 下出现未知的键，或值超出范围，服务会在启动时停止。

## 将请求固定到代理 {#issuer-pin}

证书连接不需要设备提供任何密钥：证书是公开的。因此 callout 不会仅因为请求到达了 callout 的 subject 就信任它。代理用启动时生成的密钥为每个授权请求签名，其服务器 ID 就是该密钥的公钥部分。所以 device-management 通过 NATS 系统账户获取在线代理服务器的 ID，只对由其中之一签名、且以其自身为发送方的请求授予证书连接。由其他任何对象签名的请求都会被拒绝（`issuer_unpinned`），无论其中的证书多么合格。

- **为什么需要。** 客户端不能向 callout 的 subject 发布，这是第一道防线。固定机制是第二道，用于该限制被误配置的那一天：伪造请求需要代理服务器的私钥，而私钥从不离开其进程。
- **自动获取，无需配置。** device-management 每隔 `callout.issuerPinRefresh` 向系统账户探测一次服务器 ID。来自未见过的 ID 的请求会在被拒绝之前多触发一次探测，因此滚动重启期间新加入的代理 Pod 会被第一个遇到它的设备学到。无论到达多少请求，探测都限制为每 `callout.issuerPinMinRefresh` 一次，所以大量伪造请求不会变成大量系统账户流量。在该间隔内被拒绝的设备按自己的退避重试，之后即可被接纳。
- **失败即关闭。** 当系统账户连接断开，或最近三个刷新周期内没有获取到列表时，所有证书连接都会被拒绝（`issuer_pin_unavailable`）。令牌和密码连接不受固定机制约束，也不受影响。
- **需要系统登录。** 引导流程会在每个运行 callout 的实例上配置它。开启了固定机制却没有系统登录的服务不会启动。设置 `callout.issuerPinDisabled` 可在没有固定机制的情况下运行，此时代理的发布限制是唯一的防线；服务启动时会记录一条警告。

## 限制 {#limits}

| 限制 | 值 |
| --- | --- |
| 每租户信任锚 | 32 |
| 每租户证书配置 | 8 |
| 每台设备的有效证书凭据 | 3 |
| 设备出示的证书 | 5 张，最多 16 KiB |
| 信任锚文件 | 一张 CA 证书 |

## 需要监控什么 {#monitoring}

计数器 `callout_x509_total{result}` 按结果统计每一次证书连接，所有结果从启动起即以零存在。结果包括 `ok`、`no_cert`、`chain_invalid`、`anchor_disabled`、`revoked`、`client_id_mismatch`、`unknown_pin`、`issuer_unpinned`、`issuer_pin_unavailable`、`x509_cert_ignored`、`jit_created`、`jit_replayed`、`jit_rate_limited`、`jit_busy`、`jit_ambiguous`，以及每种注册拒绝对应的一个 `jit_<code>`。`callout_jit_in_flight` 是当前正在进行的注册数，`device_trust_anchors_expiring{window="30d"}` 是整个实例中 30 天内到期的已启用信任锚的计数。

[告警](./observability.md#certificate-alerts)包括 `DeviceX509ChainFailuresSustained`、`DeviceX509JitRefusedSustained`、`DeviceX509JitMisconfigured`、`DeviceTrustAnchorsExpiringSoon`，以及消息代理一侧的 `BrokerMqttClientAuthLost`。

信任锚和凭据的变更会记入审计日志。连接结果只计数，不记入日志。

撤销设备或禁用信任锚会结束相关设备已打开的会话。如果平台无法确认某个会话已结束，变更会保留，平台会持续重试直到该会话消失；重复该变更会再次询问。

## 升级已在运行的实例 {#cutover}

对已在运行的实例，证书认证是通过添加信任锚来启用的，而不是靠升级。

- **令牌和密码客户端不受影响。** 消息代理的 MQTT 监听器现在会向每个客户端请求证书；没有证书的客户端以空证书应答，照常连接。客户端同时发送证书和令牌或密码时同样如此：证书被忽略。升级设备群之前，请对它所用的每种 TLS 栈各试一台设备。
- **如果消息代理镜像尚未更换，则会更换。** 从仍运行上游消息代理的版本升级的实例会重启一次消息代理，而消息代理重启会出现一个短暂窗口，期间除非开启了 fail-closed MQTT，否则已确认的 MQTT 消息可能丢失；见 [MQTT 传输](../reference/transport-matrix.md#mqtt--the-platform-broker)。已经在使用 `dc-nats-server` 的实例，在摘要未变时不会重启。
- **不会自动注册或信任任何东西。** 升级后没有任何信任锚，所以在你添加之前，没有任何证书能通过认证。
- **在支持证书之前创建的证书凭据不会通过认证。** 早期版本拒绝该类型，已存在的行没有信任锚。请把它重新创建为指明信任锚的固定凭据。
- **数据库结构新增**信任锚表，以及凭据和自动注册配置上指向信任锚的列。升级会应用这些变更；不会重写任何数据。

目前尚未提供，也不属于升级的内容：已注册设备的证书续期、重置注册后重新采用设备、C# SDK 中的客户端证书，以及 HTTP 或 LwM2M 上的证书认证。完整列表见[概念页](../concepts/device-certificates.md#not-supported)，其中包括带名称约束的 CA 会被拒绝，以及没有 CRL 或 OCSP。
