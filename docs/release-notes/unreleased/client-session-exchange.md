---
section: whats-new
---

## en

- **`@devicechain/client` can run an operation that spends the refresh token alongside its own refreshes.** `createSessionTokens` now returns `session.exchange(spend)`: `spend` receives the held refresh token and returns the pair that replaces it, and it runs as the one use of that token, after any refresh already running and before any refresh asked for meanwhile, which takes its pair instead of spending the token again. The console's step-up runs through it, so a forced refresh after an HTTP 401 and a step-up can no longer spend one refresh token between them and sign the user out after a correct code.

## es

- **`@devicechain/client` puede ejecutar una operación que gasta el token de actualización junto a sus propias actualizaciones.** `createSessionTokens` devuelve ahora `session.exchange(spend)`: `spend` recibe el token de actualización vigente y devuelve el par que lo sustituye, y se ejecuta como el único uso de ese token, después de cualquier actualización ya en curso y antes de cualquier actualización pedida mientras tanto, que toma su par en lugar de volver a gastar el token. La verificación adicional (step-up) de la consola pasa por él, de modo que una actualización forzada tras un HTTP 401 y una verificación adicional ya no pueden gastar entre las dos un mismo token de actualización y cerrar la sesión del usuario tras un código correcto.

## zh-CN

- **`@devicechain/client` 可以在自身刷新之外执行一个会消耗刷新令牌的操作。** `createSessionTokens` 现在返回 `session.exchange(spend)`：`spend` 接收当前持有的刷新令牌，并返回替换它的令牌对；它作为该令牌的唯一一次使用运行，排在任何正在进行的刷新之后，而在此期间请求的刷新会等待并采用它的令牌对，而不是再次消耗该令牌。控制台的二次验证（step-up）通过它执行，因此 HTTP 401 之后的强制刷新与二次验证不会再同时消耗同一个刷新令牌，导致用户在输入正确验证码后被登出。
