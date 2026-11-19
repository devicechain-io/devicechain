---
section: before-upgrade
---

## en

- **Alarm notifications are queued in `device-management`'s database before they reach the broker.** An earlier release does not send what is queued there. If you ever have to run an earlier `device-management` again, first wait until the `alarm_events_pending` gauge reads 0 on every replica; anything still queued when the earlier release takes over is not sent.

## es

- **Las notificaciones de alarma se encolan en la base de datos de `device-management` antes de llegar al broker.** Una versión anterior no envía lo que queda en esa cola. Si alguna vez tiene que volver a ejecutar una versión anterior de `device-management`, espere primero a que el indicador `alarm_events_pending` marque 0 en todas las réplicas; lo que siga en la cola cuando la versión anterior tome el relevo no se envía.

## zh-CN

- **告警通知在到达 broker 之前，会先排入 `device-management` 的数据库。** 较早的版本不会发送排在那里的内容。如果需要重新运行较早版本的 `device-management`，请先等待所有副本上的 `alarm_events_pending` 指标变为 0；较早版本接管时仍在队列中的内容不会被发送。
