{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# 生产环境加固 {#production-hardening}

将实例开放到互联网之前，请检查其公共端口提供哪些内容，使用可信 TLS 证书，并限制集群访问。
本页描述当前版本的服务监听器。v1.6.0 之前，指标和健康探针在公共 API 端口上提供，可通过
`/api/<area>/...` 访问；依赖下述边界之前，请先升级。

## 公共端口与运维端口 {#public-port}

每个区域的 Pod 和 Service 有两个 HTTP 端口：

| 端口 | 名称 | 提供的内容 | 是否从外部路由 |
| --- | --- | --- | --- |
| **8080** | `graphql` | `/graphql`、该区域自身的 HTTP 路由，以及启用开发工具时的 `/graphiql` | 是：chart 将 `/api/<area>/<path>` 转发到 `/<path>` |
| **8083** | `ops` | `/metrics`、`/healthz` 和 `/readyz` | 否：没有任何 chart 路由指向它 |

运维端点无需身份验证即可响应，因此它们位于公共路由无法到达的端口上。通过公共前缀路由访问
`/api/<area>/metrics`、`/api/<area>/healthz` 和 `/api/<area>/readyz` 会返回 **404**：
API 端口根本不提供这些路径。Kubernetes 探针和 chart 的 Prometheus ServiceMonitor 在集群内
直接使用 `ops` 端口。参见[可观测性](./observability.md)。

启用开发工具时，`/graphiql` 无需身份验证即可提供浏览器页面。开发工具默认禁用，模式自省也随之禁用。

`/graphql` 在登录前同样可访问：身份验证和授权取决于具体操作。登录、SSO 发起和公共发现必须在
用户获得令牌前可用；受保护操作仍需要经过验证的凭据和权限。

服务令牌签发端点 `/auth/service-token` 和签名密钥集 `/auth/jwks` 位于 user-management
单独的内部监听器上，而不在 8080 端口。chart 不发布到该监听器的路由；公共端口对这些路径返回 404。
请保持其 Service 为内部访问。Go 性能分析默认关闭，启用时使用单独的回环监听器；
参见[分析服务性能](./observability.md#profiling)。

## 自有入口或边缘 {#edge-rule}

使用自己的入口控制器、Gateway 或反向代理时，只将 API 路由到 `graphql` 端口，切勿路由到 `ops`，
也切勿路由到 user-management 的内部监听器。关闭对节点端口、负载均衡器以及边缘以外服务端口的
直接公共访问，防止调用方绕过边缘访问 `ops` 端口。

如果在可从互联网访问的实例上启用了开发工具，还应在边缘阻止浏览器页面。对于位于 chart 入口或
Gateway **前方**的 NGINX 反向代理，请在实例的 HTTPS `server` 块中、通用代理 location 之前添加
以下 location。`devicechain_edge` 代表现有的通往入口或 Gateway 的 upstream：

```nginx
location ~* ^/api/[^/]+/graphiql(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

阻止浏览器页面并不会阻止 GraphQL 查询；启用开发工具时，`/graphql` 上的模式自省仍然可用。
面向互联网的实例应保持开发工具禁用。

每次安装、升级或更改边缘配置后，请在外部不带凭据进行检查：

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

`/metrics`、`/healthz` 和 `/readyz` 必须返回 **404**；如果自有边缘也阻止它们，则返回 **403**。
`/graphiql` 在开发工具禁用时必须返回 404，使用上述规则时返回 403。对每个已启用的公共区域重复检查。
确认登录和已授权的 GraphQL 请求仍然正常，探针保持健康，Prometheus 仍能抓取各服务。
如果从外部得到任何指标或健康响应，说明边缘路由到了 `ops` 端口。

## 集群与凭据控制 {#controls}

- 在公共边缘使用可信 TLS 证书，并保留代理（broker）TLS。参见
  [从外部连接](./connecting-from-outside.md)。
- 按实际代理链配置[可信代理](./bootstrap.md#trusted-proxies)，使基于客户端地址的限制使用预期的来源地址。
- 限制对数据库、代理、监控和服务内部端口（包括 `ops`）的网络访问；公共边缘无法将 Pod 与集群中的其他工作负载隔离。
- 限制读取 Secret、在 Pod 中执行命令或端口转发的 Kubernetes 权限。除受控诊断会话外，保持性能分析禁用。
- 保护实例根密钥和备份材料，并演练[灾难恢复](./disaster-recovery.md)。使用最小权限的租户角色，
  并轮换已泄露的凭据。在[安全模型](../concepts/security-model.md)中查看其余边界。
