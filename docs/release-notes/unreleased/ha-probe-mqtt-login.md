---
section: whats-new
---

## en

- **`dcctl ha verify --probe-mqtt` authenticates again.** From `v1.1.0`, of the per-area broker logins, the broker accepted only event-sources' over MQTT, but the probe presented the broker administrator's login, so it was refused on every instance with per-area broker logins. It now presents the event-sources login, which the broker already accepts over MQTT; no broker permission changes. A refused probe now names the login it presented. You rarely need the flag: event-sources' own connection creates the broker's MQTT streams.

## es

- **`dcctl ha verify --probe-mqtt` vuelve a autenticarse.** Desde `v1.1.0`, de los inicios de sesión del broker por área, el broker solo aceptaba por MQTT el de event-sources, pero la sonda presentaba el del administrador del broker, por lo que se rechazaba en todas las instancias con inicios de sesión del broker por área. Ahora presenta el inicio de sesión de event-sources, que el broker ya acepta por MQTT; no cambia ningún permiso del broker. Una sonda rechazada indica ahora el inicio de sesión que presentó. Rara vez hace falta la opción: la propia conexión de event-sources crea los flujos MQTT del broker.

## zh-CN

- **`dcctl ha verify --probe-mqtt` 重新可以通过认证。** 自 `v1.1.0` 起，在按功能区划分的代理登录凭据中，代理只允许 event-sources 的登录凭据通过 MQTT 连接，而探测使用的是代理管理员的登录凭据，因此在每个使用按功能区划分的代理登录凭据的实例上都会被拒绝。现在探测使用 event-sources 的登录凭据，代理本来就允许它通过 MQTT 连接；代理权限没有任何变化。探测被拒绝时，错误信息会注明所用的登录凭据。通常无需使用该选项：event-sources 自身的连接就会创建代理的 MQTT 流。
