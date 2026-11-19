---
section: whats-new
---

## en

- **Device changes answer promptly while `device-management` cannot reach the broker.** Deleting or disabling a device, or changing a credential, while `device-management` is cut off from the broker used to hold the call for about a minute, so a client behind the ingress saw a gateway timeout for a change that had been saved. The call now answers within a few seconds, or within about 30 seconds in the first moments of an outage, before the lost connection is detected. A delete answers `true` once it is saved, and the deletion is announced when the broker is reachable again. Where the device's open MQTT sessions cannot be confirmed ended, the call returns the existing error at once ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repeating the operation is safe, and the platform also retries in the background. Profile publishes and rollbacks, device-type and geofence edits, and relationship and group changes no longer hold the call on the broker for more than a couple of seconds either.

## es

- **Los cambios de dispositivos responden enseguida mientras `device-management` no puede llegar al broker.** Eliminar o deshabilitar un dispositivo, o cambiar una credencial, mientras `device-management` está aislado del broker retenía la llamada alrededor de un minuto, de modo que un cliente detrás del ingress recibía un tiempo de espera de la pasarela por un cambio que ya se había guardado. Ahora la llamada responde en pocos segundos, o en unos 30 segundos en los primeros momentos de una interrupción, antes de que se detecte la pérdida de la conexión. Una eliminación responde `true` en cuanto se guarda, y la eliminación se anuncia cuando el broker vuelve a estar accesible. Si no se puede confirmar que las sesiones MQTT abiertas del dispositivo terminaron, la llamada devuelve de inmediato el error existente ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repetir la operación es seguro, y la plataforma también reintenta en segundo plano. Las publicaciones y reversiones de perfiles, las ediciones de tipos de dispositivo y de geocercas, y los cambios de relaciones y grupos tampoco retienen ya la llamada en el broker más de un par de segundos.

## zh-CN

- **在 `device-management` 无法连接 broker 时，设备变更会立即响应。** 以前，当 `device-management` 与 broker 断开时，删除或禁用设备、或更改凭据的调用会被挂起约一分钟，因此位于 ingress 之后的客户端会对一个已经保存的变更收到网关超时。现在调用会在几秒内响应；在中断刚开始、连接丢失尚未被检测到时，最多约 30 秒。删除在保存后即返回 `true`，删除通知会在 broker 恢复可达后发出。如果无法确认设备已打开的 MQTT 会话已结束，调用会立即返回现有错误（"the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"）。重复该操作是安全的，平台也会在后台重试。配置文件的发布和回滚、设备类型和地理围栏的编辑，以及关系和分组的变更也不再因 broker 而挂起调用超过几秒。
