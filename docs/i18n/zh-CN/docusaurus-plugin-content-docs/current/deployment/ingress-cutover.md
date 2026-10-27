---
sidebar_position: 4.7
title: 迁移到 Traefik
---

# 将现有安装迁移到 Traefik {#ingress-cutover}

新安装通过 **Traefik** 和 Kubernetes **Gateway API** 提供主机服务。早期版本使用 `ingress-nginx`，该项目已被 Kubernetes 社区停止维护。本页面向已经运行 `ingress-nginx` 的安装：迁移分步进行，主机全程持续响应。没有 `ingress-nginx` 的集群无需这些步骤，`dcctl install` 只会为它安装 Traefik。

在仍有 `ingress-nginx` 的集群上，各命令的作用如下：

- **`dcctl install`** 让 `ingress-nginx` 继续提供服务，并在它旁边安装 Traefik 和 Gateway API 定义。Traefik 拥有自己的负载均衡器，因此也有自己的地址。
- **`dcctl upgrade`** 为每个实例创建 Gateway 和 HTTPRoute，并且只要集群上还有 `ingress-nginx`，就在它们旁边保留该实例的 Ingress 对象。两者都提供该主机。
- **DNS** 由你自己在准备好时迁移。
- **`dcctl upgrade --drop-legacy-ingress`** 停止生成该实例的 Ingress 对象。
- **`dcctl install --remove-legacy-ingress`** 移除 `ingress-nginx`，且仅在没有任何 Ingress 对象指向其类时才会执行。

## 开始之前 {#ingress-cutover-before}

- 了解边缘有哪些变化，见[边缘有哪些变化](#ingress-cutover-differences)。
- 如果实例的证书来自 ACME 签发者（`ingress.tls.selfSigned: false` 加一个 `ClusterIssuer`），请阅读[来自 ACME 签发者的证书](#ingress-cutover-acme)：它需要在 DNS 迁移之后调整，而不是之前。
- 与任何[滚动升级](./releases-and-upgrades.md#rolling-upgrades)一样，给 `dcctl install` 传入集群安装时使用的设置（例如 `--ha`），以及新的 `--version`。

## 步骤 {#ingress-cutover-steps}

1. **安装新的控制器。** `ingress-nginx` 继续提供服务。

   ```bash
   dcctl install <provider> --version <new-version>
   ```

2. **升级每个实例。** 先用 `--dry-run` 查看该实例将使用哪种前端。

   ```bash
   dcctl upgrade <provider> <instance> --version <new-version> --dry-run
   dcctl upgrade <provider> <instance> --version <new-version>
   ```

   实例的命名空间（`dci-<instance>`）中现在有一个名为 `devicechain` 的 Gateway，带有 `http` 和 `https` 两个监听器，后面是 HTTPRoute。它的 Ingress 对象仍然存在。两者使用**各自独立的证书**：Ingress 对象保留 `devicechain-tls`（DNS 仍指向它，所以线上主机从不需要等待新证书），Gateway 使用 `devicechain-tls-gateway`。即使 Ingress 对象之后被移除，Gateway 也会一直保留这个名称。

   随后升级会检查 Gateway 的证书，见[证书关卡](#ingress-cutover-cert-gate)。

3. **找到 Traefik 的地址**，并在迁移 DNS 之前通过它检查实例。

   ```bash
   kubectl -n traefik get service traefik
   kubectl -n dci-<instance> get gateway devicechain
   curl --resolve <host>:443:<traefik-address> https://<host>/
   ```

   该 Service 的 `EXTERNAL-IP`（在 AWS 上是主机名）就是 Traefik 的地址。Gateway 应报告 `Programmed`。`--resolve` 让 `curl` 把请求发往 Traefik，同时仍使用你的主机名，因此看到的证书和路由与浏览器一致。实例使用自签名证书时，加上 `-k`（或用 `--cacert` 指定实例的 CA，见[TLS、标志与笔记本安装](./bootstrap.md#install-tls-and-flags)）。

4. **迁移 DNS。** 把主机指向 Traefik 的地址。无需其他操作，也不必赶时间：只要仍有客户端解析到旧地址，Ingress 对象就会继续响应。

5. **检查 Gateway 的证书。**对于使用自带 CA 终结 TLS 的实例（默认的自签名配置），在移除 Ingress
   对象之前，确认 Gateway 的证书由实例 CA 签发。跨越签发者切换升级的实例，Gateway 证书可能仍由先前的
   自签名签发者签发：它的主题为空，也不是由 CA 签发的叶子证书，因此基于 OpenSSL 的客户端没有可用于
   锚定它的根，而接受例外的浏览器看不出区别。

   ```bash
   kubectl -n dci-<instance> get secret devicechain-tls-gateway -o jsonpath='{.data.tls\.crt}' \
     | base64 -d | openssl x509 -noout -subject -issuer
   ```

   主题不能为空，签发者必须是实例 CA，即 `CN=<instance> DeviceChain CA`。如果不是，请让 cert-manager
   从实例 CA 重新签发，然后再次检查：

   ```bash
   cmctl renew -n dci-<instance> devicechain-tls-gateway
   # 没有 cmctl：删除该 Secret，cert-manager 会重新创建
   kubectl -n dci-<instance> delete secret devicechain-tls-gateway
   ```

   证书来自 ACME 或其他 `ClusterIssuer` 的实例无需此步。

6. **移除 Ingress 对象。** 流量迁移完成后，停止生成它们：

   ```bash
   dcctl upgrade <provider> <instance> --drop-legacy-ingress
   ```

   对集群上的每个实例都这样做。

7. **移除 `ingress-nginx`。**

   ```bash
   dcctl install <provider> --remove-legacy-ingress
   ```

   传入与第 1 步相同的设置。移除控制器并不会移除 Ingress 对象，遗留的对象会在没有任何故障报告的情况下停止响应，所以只要还有一个存在，该命令就会**拒绝执行**：

   ```text
   ingress-nginx is still serving 1 Ingress object(s), and removing the controller leaves them in place with
   nothing to serve them, so every host they carry stops answering:
     dci-beta/devicechain-api
   Move each instance to the Gateway first: point DNS at the new controller, then run
   `dcctl upgrade <provider> <instance> --drop-legacy-ingress` for it. Nothing has been changed
   ```

   列表会列出由 `ingress-nginx` 类提供服务的所有 Ingress，不论在哪个命名空间，包括你自己创建的。请把它们一并迁移或删除。安装为 Grafana 创建的那个 Ingress 不计入：它会在同一次应用中改由 Traefik 提供。

### 在本地集群上 {#ingress-cutover-local}

本地（kind）集群会发布节点的 80 和 443 端口，而这两个端口只能由一个控制器占用。两个控制器都已安装时，`ingress-nginx` 占用这两个端口，Traefik 只能通过 NodePort 访问。

在本地集群上没有 DNS 可迁移：浏览器访问的是占用节点端口的那个控制器。第 3、4 步（外部地址和 DNS）不适用，而普通的 `dcctl upgrade` 已经会把带有 Ingress 对象的实例带入迁移窗口。请连续执行第 6 步和第 7 步：从 `--drop-legacy-ingress` 开始，主机一直不响应，直到移除操作把端口交给 Traefik；期间会有一小段中断。

**全新的**本地集群没有 `ingress-nginx`，Traefik 从一开始就占用这些端口。在两者并存的本地集群上，**新**实例在移除之前由 `ingress-nginx` 提供服务。

### 多个实例 {#ingress-cutover-several}

集群这一步是共享的，每个实例按自己的节奏迁移。直到最后一个实例运行过 `--drop-legacy-ingress`，`ingress-nginx` 才会被移除。如果集群上的实例使用不同的主机，则每个主机都要迁移 DNS。

## 证书关卡 {#ingress-cutover-cert-gate}

没有证书的 Gateway 会用控制器的默认证书响应 `https`，所以迁移会在四个时间点检查 Gateway 的证书。实例终止 TLS 且集群有 cert-manager 时适用这些检查；`--no-tls` 会跳过它们。Secret 由你自己提供的 Gateway（没有 cert-manager 签发者注解，也没有 `kubernetes.io/tls-acme: "true"`）无需等待，直接通过。

- **在迁移窗口内，** `dcctl upgrade` 会等待证书 30 秒，然后**给出警告并继续**。主机仍由 Ingress 对象提供，而且许多签发者只有在 DNS 指向 Traefik 之后才能签发 Gateway 的证书。
- **`dcctl upgrade --drop-legacy-ingress`** 会在渲染或更改任何内容之前拒绝执行，除非 Gateway 的 cert-manager Certificate 处于 `Ready` 且由 Gateway 控制。实例还没有 Gateway 时它同样拒绝。请先迁移 DNS，让证书得以签发，用 `kubectl -n dci-<instance> get certificate` 查看，然后再运行该命令。
- **`dcctl install --remove-legacy-ingress`** 会检查集群上每个实例的 Gateway，并列出证书未 `Ready` 的实例。移除 `ingress-nginx` 不会改变这些实例的服务方式；这项检查是为了避免 Gateway 的证书问题在没有其他东西提供该主机之前一直无人察觉。
- **让实例仅由 Gateway 提供服务的升级** 最多等待五分钟，失败时给出原因。失败时服务已经启动。

拒绝信息以 “Nothing has been changed” 结尾。修复其中指出的问题后，**再运行一次相同的命令**。常见原因：

- **存在一个指向该 Secret 的 Certificate，但 Gateway 并不控制它。** cert-manager 不会接管不是为该 Gateway 创建的 Certificate，所以由 Ingress 遗留的或手工创建的、使用同一 Secret 名称的 Certificate，会让 Gateway 没有证书。消息会打印删除它的命令（`kubectl -n dci-<instance> delete certificate <name>`），之后会创建 Gateway 自己的 Certificate。
- **根本没有创建 Certificate。** cert-manager 的 Gateway API 支持未开启，或者它在 Gateway API 定义存在之前就已启动。`kubectl -n cert-manager logs deploy/cert-manager` 会给出说明。运行本版本的 `dcctl install` 会开启它。
- **Certificate 不是 `Ready`。** 消息中带有签发者自己的文字。使用 ACME 签发者时，见下文。

## 来自 ACME 签发者的证书 {#ingress-cutover-acme}

通过 `nginx` Ingress 类解决 HTTP-01 的签发者在迁移窗口内继续可用，包括为 Gateway 签发证书：质询由 `ingress-nginx` 应答，DNS 仍指向它。在 DNS 迁移之前保持原样。DNS 迁移之后，在运行 `--drop-legacy-ingress` 和 `--remove-legacy-ingress` 之前，为它添加 `gatewayHTTPRoute` 求解器，否则 `ingress-nginx` 消失后其续期会失败。在 DNS 迁移之前设置 `gatewayHTTPRoute` 求解器会失败，因为质询会到达没有对应路由的 `ingress-nginx`。DNS-01 求解器全程可用。

`gatewayHTTPRoute` 求解器指向该实例 Gateway `devicechain` 的 `http` 监听器：

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    # server, email and privateKeySecretRef as before
    solvers:
      - http01:
          gatewayHTTPRoute:
            parentRefs:
              - group: gateway.networking.k8s.io
                kind: Gateway
                name: devicechain
                sectionName: http
```

`parentRef` 不带命名空间，一个就能覆盖所有实例：cert-manager 在被签发的 Certificate 所在的命名空间中创建求解器的路由，因此它会关联到该实例的 `devicechain` Gateway。Chart 中从 `http` 到 `https` 的重定向，其匹配比质询路径更不具体，因此不会拦截质询。

## 回滚 {#ingress-cutover-rollback}

只要集群上还有 `ingress-nginx`，就可以把实例放回它上面：

```bash
dcctl upgrade <provider> <instance> --ingress-controller nginx
```

这个选择会**记录在实例上**，之后的每次升级都会保留，因为升级会重新推导它生成的一切，如果一次普通升级就能撤销回滚，那就不算回滚。`--ingress-controller gateway` 记录另一个选择。`--ingress-controller auto` 清除记录，实例随后重新根据集群决定：Traefik 运行的地方使用 Gateway。

- `nginx` 需要集群上有 `ingress-nginx`，`gateway` 需要 Traefik。所指定的控制器缺失时，该选择会被拒绝，且不做任何更改。
- 同时给出 `--ingress-controller nginx` 和 `--drop-legacy-ingress` 会被拒绝：前者让 Ingress 对象成为唯一前端，后者停止生成它们。
- 如果 DNS 已经指向 Traefik，请把它改回 `ingress-nginx` 的地址：回滚会移除 Gateway。
- `ingress-nginx` 一旦被移除，就无法回滚到它，安装也不会把已移除的控制器装回来。

## 边缘有哪些变化 {#ingress-cutover-differences}

Gateway 提供与 Ingress 对象相同的主机、路径和后端。变化的是由谁来配置边缘：

- **注解不再生效。** [值覆盖](./tuning-an-instance.md)中的 `ingress.className`、`ingress.annotations` 和 `ingress.uploadMaxBodySize` 属于 Ingress 对象。对仅由 Gateway 提供服务的实例，你在某次运行中为它们输入的值会被**拒绝**；实例已记录的值则在渲染时被丢弃并给出警告，仍保留在记录中，因此在 `--ingress-controller nginx` 下会再次生效。
- **上传不设上限也不缓冲。** Traefik 以流方式传递请求正文，更新服务自行执行其上限。Traefik 的读取超时约束的是**整个请求**（包括正文），因此它由集群接受的最大制品推导而来：OpenTofu 变量 `traefik_max_artifact_bytes`，默认 512 MiB，对应 2,228 秒的窗口。要提高某个实例的 `updates.maxArtifactBytes`，请**先**提高它。同一个窗口也是慢速发送请求头的客户端可以占用一条连接的时长，之前是 60 秒。
- **下载的查询字符串不会进入访问日志**（访问日志默认关闭）。下载授权就在 `?cap=` 中。
- **路径区分大小写，并在路径段边界结束。** `/API/<area>` 和 `/api/<area>X` 会落到控制台，而不会到达该功能区。
- **客户端地址。** Traefik 把真实的对端地址写入 `X-Forwarded-For`，其 Service 使用 `externalTrafficPolicy: Local`；见[客户端地址与受信任代理](./bootstrap.md#trusted-proxies)。在 AWS 上，经典负载均衡器会隐藏客户端地址。`dcctl install --aws-nlb traefik` 会为 Traefik 的 Service 配置 NLB，迁移期间应使用这个值，因为 `ingress-nginx` 继续使用自己的负载均衡器提供服务。更改 Service 会替换其负载均衡器并更改主机名，所以之后要重新指向 DNS。

## 相关 {#ingress-cutover-related}

- [`dcctl install`](../reference/dcctl.md) 和 [`dcctl upgrade`](../reference/dcctl.md) 列出了这里用到的全部标志。
- [发布与升级](./releases-and-upgrades.md#self-registration-upgrade)包含附带此变更的版本的说明。
