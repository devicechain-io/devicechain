---
section: whats-new
---

## en

- **`@devicechain/client` can recover from an HTTP 401.** Register `setUnauthorizedHandler({ refresh, onEnd })` and a request whose access token the server rejects gets one forced refresh and one replay before the session ends; use the new `session.forceRefresh(token)` from `createSessionTokens` as `refresh`. A temporary refresh failure (network error, 5xx or 429) keeps the session and surfaces the error; only a refused refresh ends it. Without a handler the SDK behaves as before.

## es

- **`@devicechain/client` puede recuperarse de un HTTP 401.** Registre `setUnauthorizedHandler({ refresh, onEnd })` y una solicitud cuyo token de acceso rechace el servidor recibe una actualización forzada y una repetición antes de que termine la sesión; use como `refresh` el nuevo `session.forceRefresh(token)` de `createSessionTokens`. Un fallo temporal de la actualización (error de red, 5xx o 429) conserva la sesión y expone el error; solo una actualización rechazada la termina. Sin un manejador, el SDK se comporta como antes.

## zh-CN

- **`@devicechain/client` 可以从 HTTP 401 中恢复。** 注册 `setUnauthorizedHandler({ refresh, onEnd })` 后，访问令牌被服务器拒绝的请求会先进行一次强制刷新并重放一次，然后才结束会话；请将 `createSessionTokens` 新增的 `session.forceRefresh(token)` 用作 `refresh`。临时的刷新失败（网络错误、5xx 或 429）会保留会话并返回错误；只有被拒绝的刷新才会结束会话。未注册处理程序时，SDK 的行为与以前相同。
