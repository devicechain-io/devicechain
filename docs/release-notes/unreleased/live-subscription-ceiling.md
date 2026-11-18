---
section: before-upgrade
---

## en

- **Live subscriptions have a ceiling per tenant and pod.** `measurementStream`, `alarmStream` and `detectionStream` each accept at most 5,000 concurrent subscriptions per tenant on one service pod; the next one is refused with `LIMIT_EXCEEDED`. Close unused live views. The ceiling is per pod, so more replicas raise the instance's total.

## es

- **Las suscripciones en vivo tienen un límite por inquilino y pod.** `measurementStream`, `alarmStream` y `detectionStream` aceptan cada uno como máximo 5.000 suscripciones simultáneas por inquilino en un pod de servicio; la siguiente se rechaza con `LIMIT_EXCEEDED`. Cierre las vistas en vivo que no utilice. El límite es por pod, así que más réplicas aumentan el total de la instancia.

## zh-CN

- **实时订阅按租户和 Pod 设有上限。** `measurementStream`、`alarmStream` 和 `detectionStream` 在一个服务 Pod 上对每个租户各自最多接受 5,000 个并发订阅，超出的订阅会以 `LIMIT_EXCEEDED` 被拒绝。请关闭不用的实时视图。该上限按 Pod 计算，因此增加副本即可提高整个实例的总量。
