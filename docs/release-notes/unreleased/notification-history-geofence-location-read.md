---
section: before-upgrade
---

## en

- **Location permission for notification history.** Members with `notification:read` but without `location:read` no longer see notification states or delivery attempts for geofence-derived alarms or alarms whose position classification is unknown. Rows written before the upgrade and events from old device-management replicas have no verdict and are withheld until the alarm next changes state with a new producer. These history reads consult device-management to verify cached classifications. They fail with an error while device-management is unreachable or its endpoint or the service secret is not configured. History for alarms device-management no longer has is withheld from these members. Grant `location:read` to members who need this history. Notifications sent to administrator-selected channels are unchanged.

## es

- **Permiso de ubicación para el historial de notificaciones.** Los miembros con `notification:read` pero sin `location:read` dejan de ver los estados de notificación y los intentos de entrega de alarmas derivadas de geocercas o cuya clasificación de ubicación se desconoce. Las filas anteriores a la actualización y los eventos de réplicas antiguas de device-management carecen de clasificación y se ocultan hasta que la alarma vuelva a cambiar de estado con un productor nuevo. Estas consultas verifican las clasificaciones almacenadas en caché con device-management. Fallan con un error si device-management no está disponible o si no se ha configurado su endpoint o el secreto de servicio. El historial de alarmas que device-management ya no conserva se oculta a estos miembros. Conceda `location:read` a quienes necesiten este historial. Las notificaciones enviadas a canales elegidos por el administrador no cambian.

## zh-CN

- **通知历史需要位置权限。** 拥有 `notification:read` 但没有 `location:read` 的成员将无法查看地理围栏衍生告警或位置分类未知告警的通知状态和投递尝试。升级前写入的行和旧版 device-management 副本发出的事件没有分类结果，因此会被隐藏，直到告警通过新版生产者再次改变状态。这些历史查询会向 device-management 验证缓存的分类。如果 device-management 无法访问，或未配置其端点或服务密钥，查询将返回错误。对于 device-management 已不再保存的告警，这些成员无法查看其历史。请为需要此历史的成员授予 `location:read`。发送到管理员所选渠道的通知保持不变。
