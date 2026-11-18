---
section: whats-new
---

## en

- **Many devices on one measurement subscription.** `measurementStream(deviceTokens:)` streams readings from up to 1000 devices on one subscription. A pod still on v1.6 rejects the argument during the rolling upgrade, so clients should send it only once every event-management pod runs v1.7, or fall back to one subscription per device when it is refused.

## es

- **Muchos dispositivos en una sola suscripción de mediciones.** `measurementStream(deviceTokens:)` transmite lecturas de hasta 1000 dispositivos en una sola suscripción. Un pod que aún ejecute v1.6 rechaza el argumento durante la actualización gradual, por lo que los clientes solo deben enviarlo cuando todos los pods de event-management ejecuten v1.7, o volver a una suscripción por dispositivo si se rechaza.

## zh-CN

- **一个测量订阅可覆盖多个设备。** `measurementStream(deviceTokens:)` 可在一次订阅中接收最多 1000 个设备的读数。滚动升级期间，仍运行 v1.6 的 Pod 会拒绝此参数，因此客户端应等到所有 event-management Pod 都运行 v1.7 后再发送此参数，或在被拒绝时回退到每个设备单独订阅。
