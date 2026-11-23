---
section: whats-new
---

## en

- **The `/dash` viewer app can check its requests against the schema its instance serves.** Like the console, the viewer asks each GraphQL endpoint for the schema it serves, using an authenticated `OPTIONS` request, and refuses a request the instance cannot serve before sending it, with an error that says what is missing. It is on only where the viewer can be a different release from its instance: under `vite dev`, or in a build made with `VITE_SCHEMA_NEGOTIATION=on`, which is the setting for a viewer you host apart from the instance it reaches. A viewer served by its own instance stays off, so a proxy that answers `OPTIONS` cannot break it; set `VITE_SCHEMA_NEGOTIATION=off` to turn it off in dev too. Sign-in, token refresh and sign-out are not checked. An instance older than v1.9.0 does not offer the discovery, so a viewer with negotiation on refuses requests to it with an error naming v1.9.0. If a CORS proxy or web application firewall answers `OPTIONS` itself, discovery fails and every request is refused, so let `OPTIONS` reach the GraphQL endpoints.

## es

- **La aplicación de visualización `/dash` puede comprobar sus solicitudes con el esquema que sirve su instancia.** Igual que la consola, el visor pide a cada endpoint GraphQL el esquema que sirve, mediante una solicitud `OPTIONS` autenticada, y rechaza antes de enviarla una solicitud que la instancia no puede atender, con un error que indica qué falta. Solo está activa donde el visor puede ser una versión distinta de su instancia: con `vite dev` o en una compilación hecha con `VITE_SCHEMA_NEGOTIATION=on`, que es el ajuste para un visor que usted aloja aparte de la instancia a la que llega. Un visor servido por su propia instancia la mantiene desactivada, de modo que un proxy que responda a `OPTIONS` no puede romperlo; use `VITE_SCHEMA_NEGOTIATION=off` para desactivarla también en desarrollo. No se comprueban el inicio de sesión, la renovación de tokens ni el cierre de sesión. Una instancia anterior a v1.9.0 no ofrece el descubrimiento, por lo que un visor con la negociación activada rechaza las solicitudes dirigidas a ella con un error que menciona v1.9.0. Si un proxy CORS o un firewall de aplicaciones web responde a `OPTIONS` por sí mismo, el descubrimiento falla y se rechazan todas las solicitudes, así que deje que `OPTIONS` llegue a los endpoints GraphQL.

## zh-CN

- **`/dash` 查看器应用现在可以对照其实例所提供的 schema 检查自己的请求。** 与控制台一样，查看器会通过带认证的 `OPTIONS` 请求向每个 GraphQL 端点获取其所提供的 schema，并在发送之前拒绝实例无法处理的请求，错误信息会说明缺少什么。它仅在查看器可能与其实例版本不同的场景下启用：使用 `vite dev` 时，或使用 `VITE_SCHEMA_NEGOTIATION=on` 构建时（适用于与所连接实例分开托管的查看器）。由其自身实例提供服务的查看器保持关闭，因此应答 `OPTIONS` 的代理不会使其失效；设置 `VITE_SCHEMA_NEGOTIATION=off` 可在开发模式下也将其关闭。登录、令牌刷新和退出登录不受检查。早于 v1.9.0 的实例不提供该发现功能，因此开启协商的查看器会拒绝向其发送请求，并给出提及 v1.9.0 的错误。如果 CORS 代理或 Web 应用防火墙自行应答 `OPTIONS`，发现将失败并拒绝所有请求，因此请让 `OPTIONS` 到达 GraphQL 端点。
