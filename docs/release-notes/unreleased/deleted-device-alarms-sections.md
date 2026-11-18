---
section: whats-new
---

## en

#### Alarms of a deleted device end, and stop escalating {#v170-deleted-device-alarms}

- When a device or other entity is deleted, each of its active alarms now ends with a `CLEARED` event carrying the new `clearReason: ORIGINATOR_DELETED` (also a new nullable `clearReason` field on the `alarmStream` subscription's `AlarmEvent`). Escalation stops, and policies with `notifyOnClear` say the alarm ended because its device was deleted.
- `notification-management` also checks with `device-management`, using the instance's service credentials, before re-paging, and ends open notifications for alarms that no longer exist, including ones left by deletes before the upgrade. If the check fails it pages anyway; failures are counted by `escalation_lookup_errors_total`. See [Alarms of a deleted device](../guides/notification-channels.md#deleted-originator).
- While old and new pods overlap during the rolling upgrade, a `v1.6` `notification-management` pod treats such a clear as an ordinary one, and a delete handled by a `v1.6` `device-management` pod emits no clear; the check before each re-page stops their escalation, and the periodic check ends those notifications once they are an hour old.

## es

#### Las alarmas de un dispositivo eliminado terminan y dejan de escalar {#v170-deleted-device-alarms}

- Al eliminar un dispositivo u otra entidad, cada una de sus alarmas activas termina ahora con un evento `CLEARED` que lleva el nuevo `clearReason: ORIGINATOR_DELETED` (también un nuevo campo `clearReason`, que admite nulo, en el `AlarmEvent` de la suscripción `alarmStream`). El escalado se detiene, y las políticas con `notifyOnClear` indican que la alarma terminó porque se eliminó su dispositivo.
- `notification-management` también consulta a `device-management`, con las credenciales de servicio de la instancia, antes de volver a notificar, y termina las notificaciones abiertas de alarmas que ya no existen, incluidas las que dejaron eliminaciones anteriores a la actualización. Si la consulta falla, notifica igualmente; los fallos se cuentan en `escalation_lookup_errors_total`. Consulta [Alarmas de un dispositivo eliminado](../guides/notification-channels.md#deleted-originator).
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` de `notification-management` trata ese despeje como uno normal, y una eliminación que atiende un pod `v1.6` de `device-management` no emite despeje; la comprobación previa a cada nueva notificación detiene su escalado, y la comprobación periódica termina esas notificaciones cuando tienen una hora.

## zh-CN

#### 已删除设备的告警会结束并停止升级 {#v170-deleted-device-alarms}

- 删除设备或其他实体时，其每个活动告警现在都会以携带新的 `clearReason: ORIGINATOR_DELETED` 的 `CLEARED` 事件结束（`alarmStream` 订阅的 `AlarmEvent` 上也新增了可为空的 `clearReason` 字段）。升级随即停止，启用了 `notifyOnClear` 的策略会说明告警因其设备被删除而结束。
- `notification-management` 在再次发送通知前还会使用实例的服务凭据向 `device-management` 查询，并结束已不存在的告警的未结束通知，包括升级前的删除操作遗留的通知。如果查询失败，仍会发送通知；失败次数计入 `escalation_lookup_errors_total`。请参阅[已删除设备的告警](../guides/notification-channels.md#deleted-originator)。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` 的 `notification-management` Pod 会将此类清除视为普通清除，由 `v1.6` 的 `device-management` Pod 处理的删除不会发出清除事件；每次再次通知前的检查会停止其升级，定期检查会在这些通知满一小时后将其结束。
