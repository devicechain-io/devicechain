---
sidebar_position: 7
title: 设备 API 一览
---

# 设备 API 一览 {#device-apis}

设备接入 DeviceChain 的所有方式，每种都附有一条可直接粘贴到终端的命令。本页是一张地图，而不是规范：每一行都链接到解释该传输方式的页面，[设备协议参考](./device-protocol.md)列出每个字段和每种拒绝情形。各传输方式能做什么、不能做什么，见[传输能力矩阵](./transport-matrix.md)。

示例使用本地 kind 安装（租户 `acme`，实例 `devicechain`）。请把主机、租户和凭据替换成你自己的。

| 传输方式 | 方向 | 认证方式 | 前往 |
| --- | --- | --- | --- |
| [MQTT](#mqtt) | 遥测上行，命令下行 | 每次连接使用凭据 ID 和密钥 | [连接设备](../guides/connecting-a-device.md#mqtt) |
| [HTTP](#http) | 遥测上行 | 请求体中的凭据 | [连接设备](../guides/connecting-a-device.md#http) |
| [LwM2M](#lwm2m) | 遥测上行，写入下行 | DTLS 预共享密钥 | [LwM2M](../concepts/lwm2m.md) |
| [Sparkplug B](#sparkplug) | 遥测上行，命令下行 | 数据源的 broker 凭据 | [Sparkplug B](../concepts/sparkplug.md) |
| [X.509 证书](#x509) | 同 MQTT | 客户端证书 | [证书设备](../guides/device-x509.md) |
| [自注册](#self-registration) | 获取设备和凭据 | 预配密钥和密文 | [自注册](../guides/device-self-registration.md) |

## MQTT {#mqtt}

把一个[设备事件](./device-protocol.md#device-event)发布到平台 broker 上该设备的事件主题。用户名是 `{tenant}:{credentialId}`，事件正文不带凭据。

```bash
mosquitto_pub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

命令会到达设备订阅的主题：

```bash
mosquitto_sub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/device-commands/sensor-001'
```

## HTTP {#http}

用 `POST` 把同样的事件正文发送到 `event-sources` 服务。HTTP 方式下凭据放在正文中，事件被接受时返回 `202`。`429`，或带 `Retry-After` 的 `503`，表示稍后重发。

```bash
curl -i -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

HTTP 只能上行：HTTP 设备无法接收命令。8081 端口没有传输层认证，请把它放在网络控制之后。

## LwM2M {#lwm2m}

LwM2M 客户端通过 DTLS 上的 CoAP（**UDP 5684**）注册，并出示一个预共享密钥身份，平台会把它解析为租户和设备。任何支持 PSK 的 LwM2M 客户端都可以；例如使用 Leshan 的演示客户端：

```bash
./leshan-client-demo -u <lwm2m-host>:5684 -n sensor-001 -i <pskIdentity> -p <pskKeyHex>
```

随后平台会观察设备的传感器对象，并把每次通知存为一条测量。在线状态、写入以及不支持的内容见 [LwM2M](../concepts/lwm2m.md)。

## Sparkplug B {#sparkplug}

平台是 Sparkplug 主机：它订阅你自己的 broker，不接受边缘节点的连接。把一个已配置的[数据源](../concepts/sparkplug.md)指向该 broker，然后像往常一样先发布节点 birth，再发布数据。主题名称就是 Sparkplug 的主题名称；载荷是 Protobuf，因此由 Sparkplug 库构造。用普通 MQTT 客户端可以确认该主题可达：

```bash
mosquitto_sub -h <your-broker> -p 1883 -u <user> -P <password> -t 'spBv1.0/plant-a/#' -v
```

每个 `{group}/{node}` 会成为该数据源所属租户中一台设备的外部 ID。

## X.509 证书 {#x509}

持有受信任 CA 签发证书的设备，使用用户名 `{tenant}:@x509` 且不带密码连接。

```bash
mosquitto_pub --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 -i 'devicechain:acme:<deviceToken>' -u 'acme:@x509' \
  -t 'devicechain/acme/devices/<deviceToken>/events' \
  -m '{"device":"<deviceToken>","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## 自注册 {#self-registration}

持有预配密钥和密文的设备可以自行创建设备及其凭据。发送前先生成并保存凭据，然后注册：

```bash
curl -sS https://<instance-host>/api/device-management/provisioning/acme/register \
  -H 'Content-Type: application/json' \
  -d '{"provisionKey":"acme-sensors-2026","provisionSecret":"<secret>","externalId":"SN-000123","credentialSecret":"<credential you saved>"}'
```

响应会给出设备令牌和用于连接的 MQTT 客户端 ID。重复发送同一请求是安全的，返回 `REPLAYED`。参见[重试约定](./device-protocol.md#self-registration-retry)。
