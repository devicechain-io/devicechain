---
section: whats-new
---

## en

- **A network outage no longer signs anyone out.** A refresh that cannot reach the server (no response, a 5xx or a 429) now keeps the session for as long as its refresh token is valid, instead of ending it once the access token expires. In the console and `/dash`, requests made while the access token is expired fail with the outage and succeed again once the server is back, and `/dash` wall displays keep retrying on their own. Only a refusal from the server, or the refresh token lapsing, ends a session. For `@devicechain/client` users: in that window `getAccessToken()` still resolves to `null`, as before, but the session is no longer ended (`onEnd` is not called) and the next call tries again; `startAutoRefresh()` keeps retrying until the refresh token lapses. To have `getAccessToken()` reject with the transient error instead, so a request fails with the outage rather than going out without a token, pass `transientRefreshErrors: 'throw'` to `createSessionTokens`, as the console and `/dash` do.
- **The console's MQTT command uses port 8883.** The device page's connect command for MQTT now dials port 8883, the port the broker is published on outside the cluster, instead of 1883, which is the broker's port inside the cluster and is never published. On a local kind cluster, keep using `localhost` and port 1883.
- **Identities with a `%` in their email open in the console.** The identity page decoded its address twice, so such an identity could not be opened, or opened another one.
- **IdP-managed memberships say what to do instead.** When the server refuses changing the roles of, disabling or removing a membership because the identity provider manages it, the identity page stops offering those edits for it and names the remedies: change the person's groups at the provider, disable the identity, or stop the connection serving the tenant.

## es

- **Un corte de red ya no cierra la sesión de nadie.** Una renovación que no llega al servidor (sin respuesta, un 5xx o un 429) mantiene ahora la sesión mientras su token de renovación sea válido, en lugar de terminarla cuando caduca el token de acceso. En la consola y en `/dash`, las peticiones hechas con el token de acceso caducado fallan con el corte y vuelven a funcionar cuando el servidor regresa, y las pantallas murales de `/dash` siguen reintentando por sí solas. Solo un rechazo del servidor, o la caducidad del token de renovación, termina una sesión. Para quien usa `@devicechain/client`: en ese intervalo `getAccessToken()` sigue resolviendo a `null`, como antes, pero la sesión ya no se termina (no se llama a `onEnd`) y la siguiente llamada lo vuelve a intentar; `startAutoRefresh()` sigue reintentando hasta que caduca el token de renovación. Para que `getAccessToken()` rechace en su lugar con el error transitorio, de modo que una petición falle con el corte en vez de salir sin token, pase `transientRefreshErrors: 'throw'` a `createSessionTokens`, como hacen la consola y `/dash`.
- **El comando MQTT de la consola usa el puerto 8883.** El comando de conexión MQTT de la página del dispositivo marca ahora el puerto 8883, el puerto en el que se publica el broker fuera del clúster, en lugar del 1883, que es el puerto del broker dentro del clúster y nunca se publica. En un clúster kind local, sigue usando `localhost` y el puerto 1883.
- **Las identidades con un `%` en su correo se abren en la consola.** La página de la identidad decodificaba su dirección dos veces, así que esa identidad no se podía abrir, o se abría otra.
- **Las membresías gestionadas por un IdP dicen qué hacer en su lugar.** Cuando el servidor rechaza cambiar los roles de una membresía, deshabilitarla o quitarla porque la gestiona el proveedor de identidad, la página de la identidad deja de ofrecer esos cambios para ella y nombra las soluciones: cambiar los grupos de la persona en el proveedor, deshabilitar la identidad o hacer que la conexión deje de atender al inquilino.

## zh-CN

- **网络中断不再导致任何人退出登录。** 无法连接服务器的刷新（无响应、5xx 或 429）现在会在刷新令牌有效期内保持会话，而不是在访问令牌过期时结束会话。在控制台和 `/dash` 中，访问令牌过期期间发出的请求会因中断而失败，服务器恢复后即可再次成功，`/dash` 墙面显示屏会自行持续重试。只有服务器的拒绝或刷新令牌过期才会结束会话。对于 `@devicechain/client` 的使用者：在此期间 `getAccessToken()` 仍像以前一样解析为 `null`，但不再结束会话（不会调用 `onEnd`），下一次调用会再次尝试；`startAutoRefresh()` 会持续重试，直到刷新令牌过期。若希望 `getAccessToken()` 改为以该暂时性错误拒绝，使请求因中断而失败而不是在没有令牌的情况下发出，请向 `createSessionTokens` 传入 `transientRefreshErrors: 'throw'`，控制台和 `/dash` 正是这样做的。
- **控制台的 MQTT 命令使用 8883 端口。** 设备页面的 MQTT 连接命令现在连接 8883 端口，即代理在集群外发布的端口，而不是 1883；1883 是代理在集群内的端口，从不对外发布。在本地 kind 集群上，请继续使用 `localhost` 和 1883 端口。
- **电子邮件中含有 `%` 的身份可以在控制台中打开。** 身份页面曾对其地址解码两次，因此这类身份无法打开，或会打开另一个身份。
- **由 IdP 管理的成员资格会说明应如何处理。** 当服务器因身份提供商管理某个成员资格而拒绝更改其角色、禁用或移除它时，身份页面将不再为其提供这些编辑操作，并列出解决办法：在提供商处更改此人的组、禁用该身份，或停止该连接为该租户提供服务。
