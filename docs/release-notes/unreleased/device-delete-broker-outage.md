---
section: whats-new
---

## en

- **Device deletes and authority changes answer sooner while `device-management` cannot reach the broker.** Deleting or disabling a device, or changing a credential, while `device-management` was cut off from the broker used to hold the call for about a minute, so a client behind the ingress saw a gateway timeout for a change that had been saved. Once `device-management` has detected the lost connection, which takes up to about 40 seconds, the call now answers within a few seconds. A call made before that answers once the loss is detected. A delete answers `true` once it is saved, and the deletion is announced when the broker is reachable again. Where the device's open MQTT sessions cannot be confirmed ended, the call returns the existing error ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repeating the operation is safe, and the platform also retries in the background. Each cache refresh a saved change owes now holds the call for at most two seconds and then completes in the background.

## es

- **Las eliminaciones de dispositivos y los cambios de autoridad responden antes mientras `device-management` no puede llegar al broker.** Eliminar o deshabilitar un dispositivo, o cambiar una credencial, mientras `device-management` estaba aislado del broker retenía la llamada alrededor de un minuto, de modo que un cliente detrás del ingress recibía un tiempo de espera de la pasarela por un cambio que ya se había guardado. Una vez que `device-management` ha detectado la pérdida de la conexión, lo que tarda hasta unos 40 segundos, la llamada responde ahora en pocos segundos. Una llamada hecha antes responde en cuanto se detecta la pérdida. Una eliminación responde `true` en cuanto se guarda, y la eliminación se anuncia cuando el broker vuelve a estar accesible. Si no se puede confirmar que las sesiones MQTT abiertas del dispositivo terminaron, la llamada devuelve el error existente ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repetir la operación es seguro, y la plataforma también reintenta en segundo plano. Cada actualización de caché que debe un cambio guardado retiene ahora la llamada como máximo dos segundos y después se completa en segundo plano.

## zh-CN

- **在 `device-management` 无法连接 broker 时，设备删除和权限变更会更快响应。** 以前，当 `device-management` 与 broker 断开时，删除或禁用设备、或更改凭据的调用会被挂起约一分钟，因此位于 ingress 之后的客户端会对一个已经保存的变更收到网关超时。现在，一旦 `device-management` 检测到连接丢失（最多约 40 秒），调用会在几秒内响应；在此之前发起的调用会在检测到连接丢失后响应。删除在保存后即返回 `true`，删除通知会在 broker 恢复可达后发出。如果无法确认设备已打开的 MQTT 会话已结束，调用会返回现有错误（"the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"）。重复该操作是安全的，平台也会在后台重试。已保存的变更所需的每次缓存刷新现在最多挂起调用两秒，然后在后台完成。
