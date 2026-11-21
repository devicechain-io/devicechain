---
section: whats-new
---

## en

- Concurrent device deletes return `false` when another delete wins, and event-source shutdowns count abandoned placement holds with `reason="shutdown"`.

## es

- Las eliminaciones simultáneas de un dispositivo devuelven `false` cuando otra eliminación se completa primero, y al detenerse las fuentes de eventos contabilizan las retenciones por ubicación abandonadas con `reason="shutdown"`.

## zh-CN

- 并发删除设备时，若另一次删除先完成，则返回 `false`；事件源关闭时会以 `reason="shutdown"` 统计放弃的分片放置等待消息。
