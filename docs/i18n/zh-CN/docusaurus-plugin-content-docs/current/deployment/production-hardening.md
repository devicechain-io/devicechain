{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# 生产环境加固 {#production-hardening}

将实例开放到互联网之前，请在边缘限制运维端点，使用可信 TLS 证书，并限制集群访问。
本页描述当前的服务监听器；旧版本可能暴露不同的接口。依赖下述内部监听器边界之前，请先升级。

## 公共端口与身份验证 {#public-port}

服务 HTTP 端口为 **8080**。对于具有公共 API 的区域，chart 将 `/api/<area>/<path>`
转发到 `/<path>`。此前缀路由也转发运维端点，不会添加身份验证。
以下路径无需 DeviceChain 令牌即可访问：

| 服务路径 | 外部路径 | 提供的内容 |
| --- | --- | --- |
| `/metrics` | `/api/<area>/metrics` | Prometheus 指标，包括运维标签；应仅限监控客户端访问。 |
| `/healthz` | `/api/<area>/healthz` | 存活状态；故障详情保留在服务日志中。 |
| `/readyz` | `/api/<area>/readyz` | 就绪状态。 |
| `/graphiql` | `/api/<area>/graphiql` | 仅在启用开发工具时提供浏览器 HTML；默认禁用。 |

登录前也可访问 `/graphql`：身份验证与授权取决于具体操作。登录、发起 SSO 和公共发现必须
在没有令牌时可用；受保护的操作仍要求已验证的凭据和权限。阻止浏览器不会阻止 GraphQL 查询。
默认情况下，模式内省与开发工具一起禁用。

服务令牌签发端点 `/auth/service-token` 和签名密钥集 `/auth/jwks` 位于 user-management 的
独立内部监听器上，不在端口 8080 上。chart 不向该监听器发布路由；公共端口对这些路径返回 404。
请保持其 Service 仅供内部使用。Go 性能分析默认关闭，启用时使用独立的回环监听器；
参见[分析服务性能](./observability.md#profiling)。

## 在边缘阻止运维路径 {#edge-rule}

对于位于 chart 入口或 Gateway **前方**的 NGINX 反向代理，请在实例的 HTTPS `server`
块中、通用代理 location 之前添加以下配置。`devicechain_edge` 代表现有的入口或 Gateway
上游。NGINX 使用不含查询字符串的规范化 URI 匹配 location。
此规则在重写前缀之前阻止所有区域中的这些路径及其子路径：

```nginx
location ~* ^/api/[^/]+/(metrics|healthz|readyz|graphiql)(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

这是反向代理配置，不是 ingress 注解，也不是可移植的 Gateway API 过滤器。
使用其他边缘组件时，请在转发之前对规范化路径实施相同的拒绝规则。
确保没有更早的正则 location 或 `^~` location 绕过该规则。关闭直接访问上游入口/Gateway
和服务端口的公共通道，防止调用方绕过规则。Kubernetes 探针与 Prometheus ServiceMonitor
应继续使用集群内的直接路径，不得经过此公共拒绝规则。参见[可观测性](./observability.md)。

重新加载边缘配置后，从外部在不带凭据的情况下检查：

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

每个请求都必须返回 **403**，包括带查询字符串或末尾斜杠的请求。对所有已启用的公共区域
重复检查。确认登录和已授权的 GraphQL 请求仍然有效、探针正常且 Prometheus 仍能抓取指标。
外部出现任何运维响应都说明边缘规则部署失败。

## 集群与凭据控制 {#controls}

- 在公共边缘使用可信 TLS 证书并保留 broker TLS。
  参见[从外部连接](./connecting-from-outside.md)。
- 根据实际代理链配置[可信代理](./bootstrap.md#trusted-proxies)，
  确保客户端地址限额使用预期的源地址。
- 限制数据库、broker、监控和内部服务端口的网络访问；
  公共边缘规则不会将 pod 与集群中的其他工作负载隔离。
- 限制读取 Secrets、进入 pod 执行命令或端口转发的 Kubernetes 权限。
  除受控诊断会话外，保持性能分析关闭。
- 保护实例根密钥与备份资料，并演练[灾难恢复](./disaster-recovery.md)。
  为租户角色授予最小权限，轮换泄露的凭据。
  在[安全模型](../concepts/security-model.md)中查看其余边界。
