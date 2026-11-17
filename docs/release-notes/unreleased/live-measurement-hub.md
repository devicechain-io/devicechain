---
section: whats-new
---

## en

- **Live measurement subscriptions share one feed per tenant.** Each event-management pod now reads a tenant's live readings once and hands each `measurementStream` subscription only its device's readings, so a dashboard with many devices no longer multiplies the work per event. What a subscription receives is unchanged. A pod refuses more than 5,000 concurrent `measurementStream` subscriptions per tenant with `LIMIT_EXCEEDED`; close unused live views or spread clients across pods.

## es

- **Las suscripciones de mediciones en vivo comparten un flujo por inquilino.** Cada pod de event-management ahora lee una sola vez las lecturas en vivo de un inquilino y entrega a cada suscripción `measurementStream` solo las lecturas de su dispositivo, de modo que un panel con muchos dispositivos ya no multiplica el trabajo por evento. Lo que recibe una suscripción no cambia. Un pod rechaza más de 5.000 suscripciones `measurementStream` simultáneas por inquilino con `LIMIT_EXCEEDED`; cierre las vistas en vivo que no utilice o distribuya los clientes entre varios pods.

## zh-CN

- **实时测量订阅按租户共享一个数据流。** 每个 event-management Pod 现在只读取一次租户的实时读数，并仅向每个 `measurementStream` 订阅分发其设备的读数，因此包含大量设备的仪表板不再让每个事件的处理工作成倍增加。订阅收到的内容保持不变。每个 Pod 对每个租户最多允许 5,000 个并发 `measurementStream` 订阅，超出时返回 `LIMIT_EXCEEDED`；请关闭不用的实时视图，或将客户端分散到多个 Pod。
