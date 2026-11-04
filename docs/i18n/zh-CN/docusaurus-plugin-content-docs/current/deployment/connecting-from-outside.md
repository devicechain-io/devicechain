---
sidebar_position: 4.8
title: 从集群外部连接设备
---

# 从集群外部连接设备 {#connecting-from-outside}

设备通过 TLS 在 TCP **8883** 端口连接平台的 MQTT 消息代理。在云上的集群里，除非你主动开启，否则不会向互联网开放任何端口：全新安装和每次升级都让 MQTT 留在集群内部，你用 `dcctl` 按实例开启对外暴露。本页介绍两种做法、设备所需的 DNS 名称和证书、防火墙，以及如何从集群外的机器验证结果。

在本地 `kind` 集群上不需要这些：`dcctl` 已经把你机器上的 1883 端口映射到消息代理，见[连接设备](../guides/connecting-a-device.md#publishing-an-event)。

:::note 明文 MQTT 永远不会被暴露
两种模式都只转发消息代理的 TLS 监听器。明文端口不会被发布，并且 `dcctl` 会拒绝在不提供 TLS 的实例（`--no-tls`）上暴露 MQTT。TLS 由消息代理自己终止，设备认证（包括[设备证书](./device-certificates.md)）也由它完成；入口控制器永远看不到明文流量。
:::

## 选择模式 {#modes}

| | `gateway` | `loadbalancer` |
| --- | --- | --- |
| 创建什么 | 实例 Gateway 上的一条路由，由 Traefik 在 TCP 8883 端口提供 | 一个类型为 `LoadBalancer` 的 `Service`，TCP 8883 直达消息代理 Pod |
| 设备如何找到自己的实例 | 通过连接时使用的主机名（TLS 服务器名称指示，SNI） | 通过负载均衡器自身的地址；主机名只用于 DNS |
| 每个地址可承载的实例数 | 多个：共用一个地址，各有自己的主机名 | 每个实例一个负载均衡器 |
| 消息代理看到的设备地址 | 入口控制器的地址 | 设备自己的地址（云负载均衡器保留客户端地址时） |
| 前提 | Gateway 前端和 Gateway API（全新安装的默认值），以及一次性的集群步骤 | 能够提供 `LoadBalancer` Service 的云 |

如果实例已经通过 Traefik 提供服务，并且希望多个实例共用一个地址，用 `gateway`。在不支持 Gateway API 的集群上，或希望为消息代理单独配置一个 L4 负载均衡器（例如限制为一组来源网段）时，用 `loadbalancer`。

## 开启 {#enable}

为实例选一个**主机名**，例如 `mqtt.acme.example.com`。设备拨号连接这个名称，消息代理的证书包含它，`gateway` 模式下 Gateway 也按它路由。它必须是完整限定的小写 DNS 名称，且集群上的每个实例都需要各自的主机名。

### Gateway 模式 {#enable-gateway}

Gateway 模式需要 Traefik 监听 8883 端口。这会在它的负载均衡器上发布一个新端口，因此是一个需要你有意识地执行一次的集群步骤：

```bash
dcctl install <provider> --mqtt-gateway
```

给 `install` 传入与安装集群时相同的参数（例如 `--ha`），和任何[滚动升级](./releases-and-upgrades.md#rolling-upgrades)一样。不带 `--mqtt-gateway` 时，安装会保留控制器现有的状态，因此之后的安装不会意外关闭该端口；`--mqtt-gateway=false` 会关闭它。

然后按实例执行：

```bash
dcctl bootstrap <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
# 或者，对已存在的实例：
dcctl upgrade   <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
```

在更改任何内容之前，如果 Traefik 没有监听 8883、实例不是由 Gateway 前端提供服务，或者另一个实例已经使用该主机名，`dcctl` 会拒绝请求。

### 负载均衡器模式 {#enable-loadbalancer}

```bash
dcctl bootstrap <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
dcctl upgrade   <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
```

这会在实例的命名空间中创建 `dc-nats-mqtt-lb` Service，其 `externalTrafficPolicy` 为 `Local`。各云厂商特有的注解（例如内部负载均衡器）和允许的来源网段列表，通过[调优覆盖](./tuning-an-instance.md)中的 `mqttExposure.loadBalancer` 设置：

```yaml
mqttExposure:
  loadBalancer:
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-scheme: internal
    sourceRanges:
      - 203.0.113.0/24
```

在 EKS 上，`LoadBalancer` Service 除非带有 Network Load Balancer 的注解，否则得到的是 Classic 负载均衡器；在此处添加 `service.beta.kubernetes.io/aws-load-balancer-type: nlb`。对于 gateway 模式下 Traefik 自己的负载均衡器，用 `dcctl install --aws-nlb traefik`。

### 升级会做什么 {#enable-effects}

该设置会记录在实例上，并被之后的每次 `dcctl upgrade` 保留，因此参数只需给一次。主机名会加入消息代理的证书；尚未包含该名称的消息代理会获得新证书并**重启**。消息代理重启对已连接的设备是一次短暂中断，并且和任何消息代理滚动一样，除非开启了 [fail-closed MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker)，否则会有一个已确认消息可能丢失的窗口。请像对待版本升级一样规划它。

用 `--mqtt-exposure none` 关闭暴露。它会移除路由或 Service；证书在下次续期前仍带有多出的名称，这是无害的。

## DNS {#dns}

把主机名指向现在在 8883 端口应答的地址：

```bash
# gateway：入口控制器的地址
kubectl -n traefik get service traefik
# loadbalancer：实例的消息代理负载均衡器
kubectl -n dci-acme get service dc-nats-mqtt-lb
```

按云厂商报告的内容，IP 地址用 `A` 记录，域名用 `CNAME`（EKS 报告域名，GKE 和 AKS 报告 IP 地址）。主机名可以与实例提供控制台所用的主机名相同，也可以单独使用例如 `mqtt.acme.example.com`。在 gateway 模式下，多个实例共用控制器的地址，各有自己的名称。

## 端口与防火墙 {#firewall}

| 端口 | 谁来连接 | 对谁开放 |
| --- | --- | --- |
| TCP 8883 | 设备，连接到控制器或消息代理负载均衡器的地址 | 你的设备所在的网络 |

MQTT 不需要再开放其他端口。在 GKE 和 AKS 上，云会为 `LoadBalancer` Service 创建规则。在 AWS 上，请检查负载均衡器及其后端节点的安全组是否允许来自设备的 TCP 8883。需要在第 4 层透传连接的云负载均衡器：第 7 层的 HTTP 负载均衡器无法承载 MQTT。

在 gateway 模式下，消息代理看到的所有设备来源地址都是入口控制器的地址，而不是设备自己的地址。因此，在消息代理处凭设备来源地址区分的任何内容，会被同一个控制器后面的所有设备共享。使用能保留客户端地址的负载均衡器（AWS 上的 Network Load Balancer、Google Cloud 的网络负载均衡器）的负载均衡器模式没有这个限制。

## 设备必须信任的证书 {#certificates}

消息代理出示由实例自己的证书颁发机构签发的证书，你选定的主机名在它的名称之中。设备必须信任该机构，和对实例的任何 [MQTT 连接](../guides/connecting-a-device.md#publishing-an-event)一样。在能访问集群的机器上导出它：

```bash
kubectl -n dci-acme get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

把 `instance-ca.crt` 随设备固件或配置一起分发。它是公开证书。消息代理的证书在到期前 30 天内由 `dcctl upgrade` 续期；颁发机构保持不变，因此设备在续期前后始终信任它。

设备必须把主机名作为 TLS 服务器名称发送。在 gateway 模式下，正是靠它找到正确的实例：不发送服务器名称的连接，或 Gateway 没有对应路由的连接，会被关闭。因此在 gateway 模式下按 IP 地址连接会失败，请按名称连接。

## 从外部验证 {#verify}

在 DNS 解析生效后，从集群外的机器执行：

```bash
# 1. 应答的是正确的证书，并且能用实例 CA 验证通过
openssl s_client -connect mqtt.acme.example.com:8883 \
  -servername mqtt.acme.example.com -CAfile instance-ca.crt </dev/null \
  | grep -E 'subject=|Verify return code'
```

你应当看到主题（或备用名称）中有该主机名，并且 `Verify return code: 0 (ok)`。

```bash
# 2. 设备可以连接并发布
mosquitto_pub --cafile instance-ca.crt \
  -h mqtt.acme.example.com -p 8883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

加上 `-d` 可以看到消息代理的 `CONNACK` 和 `PUBLISH`。使用证书认证的设备再加上 `--cert` 和 `--key`，见[使用证书连接设备](../guides/device-x509.md)。

## 不工作时 {#troubleshooting}

| 你看到的现象 | 含义 |
| --- | --- |
| `dcctl` 拒绝，提示 "needs the ingress controller to listen on 8883" | 对集群执行一次 `dcctl install <provider> --mqtt-gateway`，或改用 `--mqtt-exposure loadbalancer` |
| `dcctl` 因 TLS 关闭而拒绝 | 该实例提供明文 HTTP（`--no-tls`）。明文 MQTT 永远不会被暴露；请让实例使用 TLS |
| `dcctl` 因主机名已被使用而拒绝 | 集群上的另一个实例使用了该名称。请为每个实例指定各自的名称 |
| 握手期间连接被关闭，`no peer certificate available` | gateway 模式下：未发送服务器名称，或没有路由匹配它。请使用准确的主机名连接；检查 `kubectl -n dci-acme get tlsroute devicechain-mqtt` |
| `certificate verify failed` 或主机名不匹配 | 设备信任的是另一个颁发机构，或拨号的名称不在证书中。请对照上面第 1 步的输出检查名称 |
| 超时 | DNS 指向了别处，或防火墙/安全组阻止了 TCP 8883。在 gateway 模式下，检查 `kubectl -n traefik get service traefik` 是否列出 8883 |
| Gateway 的 `mqtt-tls` 监听器不是 `Programmed` | `kubectl -n dci-acme describe gateway devicechain` 会说明原因；常见原因是控制器没有 8883 入口点 |

无论设备以哪种方式连接到消息代理，平台的 MQTT 行为、摄取限制和投递保证都相同；见 [MQTT 传输](../reference/transport-matrix.md#mqtt--the-platform-broker)。
