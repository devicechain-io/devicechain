---
section: before-upgrade
---

## en

- **Wrong device passwords count once, whichever way they arrive.** A wrong password presented on the HTTP device-uplink route now counts toward the same per-credential hold as a wrong MQTT password, across all replicas, so guesses can no longer be split between the two to get twice the attempts. While a credential is held, an HTTP uplink with it is answered `503` with `Retry-After` and an MQTT connect with it is refused, as before.

## es

- **Las contraseñas incorrectas de dispositivo cuentan una sola vez, lleguen por donde lleguen.** Una contraseña incorrecta presentada en la ruta HTTP de cargas útiles del dispositivo ahora cuenta para la misma pausa por credencial que una contraseña MQTT incorrecta, en todas las réplicas, por lo que ya no se pueden repartir los intentos entre ambas vías para duplicarlos. Mientras una credencial está en pausa, una carga útil HTTP que la usa recibe `503` con `Retry-After` y una conexión MQTT que la usa se rechaza, como antes.

## zh-CN

- **设备密码猜测统一计数，无论通过哪种途径到达。** 在 HTTP 设备上行路由上出示的错误密码，现在与错误的 MQTT 密码计入同一个按凭据实施的暂停，所有副本共享计数，因此无法再将猜测分散到两种途径以获得双倍尝试次数。凭据暂停期间，使用它的 HTTP 上行请求会收到 `503` 和 `Retry-After`，使用它的 MQTT 连接则会被拒绝，与之前一样。
