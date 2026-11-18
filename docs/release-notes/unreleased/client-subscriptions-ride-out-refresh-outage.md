---
section: whats-new
---

## en

- **Live subscriptions ride out a token-refresh outage.** When a subscription's socket reconnected after the access token had expired and the token refresh got no response, a 5xx or a 429, every live subscription in that area ended with an error, and dashboards showed their data as unavailable until reloaded. `@devicechain/client` now retries a reconnect whose token getter rejects with that transient error (as `getAccessToken()` does under `transientRefreshErrors: 'throw'`, which the console and `/dash` use) with the same back-off as a dropped network, and reports it as a reconnect once a token is available. A refused refresh still ends the session.

## es

- **Las suscripciones en vivo resisten una caída de la renovación del token.** Cuando el socket de una suscripción se reconectaba después de que el token de acceso hubiera caducado y la renovación del token no obtenía respuesta, o devolvía un 5xx o un 429, todas las suscripciones en vivo de esa área terminaban con un error, y los paneles mostraban sus datos como no disponibles hasta recargarlos. `@devicechain/client` reintenta ahora una reconexión cuyo obtenedor de token rechaza con ese error transitorio (como hace `getAccessToken()` con `transientRefreshErrors: 'throw'`, que usan la consola y `/dash`) con la misma espera progresiva que una red caída, y la notifica como reconexión en cuanto hay un token disponible. Una renovación rechazada sigue terminando la sesión.

## zh-CN

- **实时订阅可以挺过令牌刷新中断。** 此前，当订阅的套接字在访问令牌过期后重新连接，而令牌刷新没有响应、返回 5xx 或 429 时，该区域的所有实时订阅都会以错误结束，仪表板会一直显示数据不可用，直到重新加载。现在，当令牌获取函数以该暂时性错误拒绝时（例如在 `transientRefreshErrors: 'throw'` 下的 `getAccessToken()`，控制台和 `/dash` 均使用此模式），`@devicechain/client` 会以与网络断开相同的退避策略重试该重新连接，并在获得令牌后将其报告为重新连接。被拒绝的刷新仍会结束会话。
