---
section: whats-new
---

## en

- **Live measurement, alarm, and detection subscriptions share feeds per tenant.** Each service pod now reads each tenant's live feed once for `measurementStream`, `alarmStream`, and `detectionStream`, decoding each message once and dispatching by device, alarm originator, or detection profile before applying each subscriber's filters and access scope. A dashboard with many subscriptions no longer multiplies the decoding work per event. What a subscription receives is unchanged. Each of the three subscription types has its own ceiling of 5,000 concurrent subscriptions per tenant per pod; exceeding it returns `LIMIT_EXCEEDED`. Close unused live views or spread clients across pods.

## es

- **Las suscripciones en vivo comparten un flujo por inquilino.** Cada pod lee ahora una sola vez el flujo en vivo de un inquilino y entrega a cada suscripción solo lo que le corresponde: las lecturas de su dispositivo en `measurementStream`, las alarmas de su originador en `alarmStream` y las detecciones de su perfil en `detectionStream`, de modo que un panel con muchos dispositivos ya no multiplica el trabajo por evento. Lo que recibe una suscripción no cambia. Un pod rechaza más de 5.000 suscripciones simultáneas por inquilino a cada uno de estos flujos con `LIMIT_EXCEEDED`; cierre las vistas en vivo que no utilice o distribuya los clientes entre varios pods.

## zh-CN

- **实时订阅按租户共享一个数据流。** 每个 Pod 现在只读取一次租户的实时数据流，并只向每个订阅分发属于它的内容：`measurementStream` 收到其设备的读数，`alarmStream` 收到其发起方的告警，`detectionStream` 收到其配置文件的检测结果，因此包含大量设备的仪表板不再让每个事件的处理工作成倍增加。订阅收到的内容保持不变。每个 Pod 对每个租户的上述每种订阅最多允许 5,000 个并发订阅，超出时返回 `LIMIT_EXCEEDED`；请关闭不用的实时视图，或将客户端分散到多个 Pod。
