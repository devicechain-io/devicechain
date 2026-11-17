---
section: before-upgrade
---

## en

- **Location permission for notification history.** Members with `notification:read` but without `location:read` no longer see notification states or delivery attempts for geofence-derived alarms or alarms whose position classification is unknown. Rows written before the upgrade and events from old device-management replicas have no verdict and are withheld until the alarm next changes state with a new producer. Grant `location:read` to members who need this history. Notifications sent to administrator-selected channels are unchanged.

## es

- **Permiso de ubicación para el historial de notificaciones.** Los miembros con `notification:read` pero sin `location:read` dejan de ver los estados de notificación y los intentos de entrega de alarmas derivadas de geocercas o cuya clasificación de ubicación se desconoce. Las filas anteriores a la actualización y los eventos de réplicas antiguas de device-management carecen de clasificación y se ocultan hasta que la alarma vuelva a cambiar de estado con un productor nuevo. Conceda `location:read` a quienes necesiten este historial. Las notificaciones enviadas a canales elegidos por el administrador no cambian.

## zh-CN

- **通知历史需要位置权限。** 拥有 `notification:read` 但没有 `location:read` 的成员将无法查看地理围栏衍生告警或位置分类未知告警的通知状态和投递尝试。升级前写入的行和旧版 device-management 副本发出的事件没有分类结果，因此会被隐藏，直到告警通过新版生产者再次改变状态。请为需要此历史的成员授予 `location:read`。发送到管理员所选渠道的通知保持不变。
