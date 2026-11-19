---
section: whats-new
---

## en

- **Alarm notifications survive a broker outage or a restart of `device-management`.** An alarm state change (raised by detection, escalated, cleared, or acknowledged or cleared by an operator) is now recorded together with the change itself and sent to the broker from that record until the broker accepts it. Previously a change made while the broker was unreachable, or just before `device-management` restarted, could be saved with no notification sent. While the broker is unreachable, acknowledging or clearing alarms now answers within about two seconds, even for a bulk acknowledgement, where each alarm used to hold the call for about 30 seconds. The notification follows once the broker is back. Maintenance-window suppression is decided when the change is made, not when the notification is finally sent. Three new gauges report the backlog: `alarm_events_pending`, `alarm_event_oldest_age_seconds` and `alarm_events_quarantined`. A quarantined event could not be encoded and is kept for inspection instead of being retried. `alarm_event_dead_lettered_total` stays registered but no longer increases.

## es

- **Las notificaciones de alarma sobreviven a una caída del broker o a un reinicio de `device-management`.** Un cambio de estado de una alarma (generada por la detección, escalada, borrada, o reconocida o borrada por un operador) se registra ahora junto con el propio cambio y se envía al broker desde ese registro hasta que el broker lo acepta. Antes, un cambio hecho mientras el broker era inaccesible, o justo antes de que `device-management` se reiniciara, podía guardarse sin que se enviara ninguna notificación. Mientras el broker es inaccesible, reconocer o borrar alarmas responde ahora en unos dos segundos, incluso en un reconocimiento masivo, donde cada alarma retenía antes la llamada unos 30 segundos. La notificación llega cuando el broker vuelve. La supresión por ventana de mantenimiento se decide cuando se hace el cambio, no cuando por fin se envía la notificación. Tres indicadores nuevos informan del retraso: `alarm_events_pending`, `alarm_event_oldest_age_seconds` y `alarm_events_quarantined`. Un evento en cuarentena no se pudo codificar y se conserva para su inspección en lugar de reintentarse. `alarm_event_dead_lettered_total` sigue registrado pero ya no aumenta.

## zh-CN

- **告警通知在 broker 中断或 `device-management` 重启后不再丢失。** 告警状态变更（由检测触发、升级、清除，或由运维人员确认或清除）现在会与变更本身一起记录，并从该记录发送到 broker，直到 broker 接受为止。以前，在 broker 不可达时或 `device-management` 重启前做出的变更可能已保存，却没有发出任何通知。在 broker 不可达期间，确认或清除告警现在约两秒内即可返回，批量确认也是如此；以前每个告警会使调用挂起约 30 秒。通知会在 broker 恢复后发出。维护窗口抑制在变更发生时判定，而不是在通知最终发出时判定。三个新指标报告积压情况：`alarm_events_pending`、`alarm_event_oldest_age_seconds` 和 `alarm_events_quarantined`。被隔离的事件无法编码，会被保留以供检查，不会重试。`alarm_event_dead_lettered_total` 仍然注册，但不再增长。
