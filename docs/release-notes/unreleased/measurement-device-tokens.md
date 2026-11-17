---
section: whats-new
---

## en

- `measurementStream(deviceTokens:)` streams readings from up to 1000 devices on one subscription. A pod still on v1.6 rejects the argument during the rolling upgrade, so clients should send it only once every event-management pod runs v1.7, or fall back to one subscription per device when it is refused.
- **Live measurement, alarm, and detection subscriptions share feeds per tenant.** Each service pod now reads each tenant's live feed once for `measurementStream`, `alarmStream`, and `detectionStream`, decoding each message once and dispatching by device, alarm originator, or detection profile before applying each subscriber's filters and access scope. A dashboard with many subscriptions no longer multiplies the decoding work per event. What a subscription receives is unchanged. Each of the three subscription types has its own ceiling of 5,000 concurrent subscriptions per tenant per pod; exceeding it returns `LIMIT_EXCEEDED`. Close unused live views or spread clients across pods.

## es

- `measurementStream(deviceTokens:)` transmite lecturas de hasta 1000 dispositivos en una sola suscripción. Un pod que aún ejecute v1.6 rechaza el argumento durante la actualización gradual, por lo que los clientes solo deben enviarlo cuando todos los pods de event-management ejecuten v1.7, o volver a una suscripción por dispositivo si se rechaza.
- **Las suscripciones de mediciones, alarmas y detecciones en vivo comparten flujos por inquilino.** Cada pod de servicio ahora lee una sola vez cada flujo en vivo del inquilino para `measurementStream`, `alarmStream` y `detectionStream`, decodifica cada mensaje una vez y lo distribuye por dispositivo, originador de alarma o perfil de detecci?n antes de aplicar los filtros y el ?mbito de acceso de cada suscriptor. Un panel con muchas suscripciones ya no multiplica el trabajo de decodificaci?n por evento. Lo que recibe una suscripci?n no cambia. Cada uno de los tres tipos de suscripci?n tiene su propio l?mite de 5.000 suscripciones simult?neas por inquilino y por pod; superarlo devuelve `LIMIT_EXCEEDED`. Cierre las vistas en vivo que no utilice o distribuya los clientes entre varios pods.

## zh-CN

- `measurementStream(deviceTokens:)` 可在一次订阅中接收最多 1000 个设备的读数。滚动升级期间，仍运行 v1.6 的 Pod 会拒绝此参数，因此客户端应等到所有 event-management Pod 都运行 v1.7 后再发送此参数，或在被拒绝时回退到每个设备单独订阅。
- **?????????????????????** ???? Pod ??????????????????? `measurementStream`?`alarmStream` ? `detectionStream` ????????????????????????????????????????????????????????????????????????????????????????????????????????????? Pod ?? 5,000 ?????????????? `LIMIT_EXCEEDED`?????????????????????? Pod?
