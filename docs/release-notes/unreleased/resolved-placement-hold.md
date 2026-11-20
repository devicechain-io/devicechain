---
section: whats-new
---

## en

- **Resolved events wait for shard placement.** With shard groups on resolved-events, an inbound event whose final delivery attempt had some of its resolved events refused because shard placement was unavailable is now held and finished once placement returns, instead of being recorded as a dead letter with its events never resolved. Only the refused resolved events are published again; those already stored are not repeated. This applies to inbound events, integration uplinks and resolved envelopes alike. The new `resolve_placement_held_sources` gauge shows how many final deliveries are being held, and `resolve_placement_hold_fallback_total` counts, by reason, those the hold did not take or stopped holding. The hold is bounded in size and holds a message for at most 15 minutes, then leaves it unacknowledged as before; it lasts for the current run, and shutdown abandons held messages unacknowledged, so exhausted deliveries can still be recorded. Rolling upgrades need no wire or configuration change; old pods retain the previous behavior.

## es

- **Los eventos resueltos esperan a que vuelva la ubicación de particiones.** Con los grupos de particiones activados para resolved-events, un evento entrante cuyo último intento de entrega tuvo algunos de sus eventos resueltos rechazados porque la ubicación de particiones no estaba disponible se retiene y se completa cuando esta vuelve, en lugar de registrarse como mensaje fallido sin que sus eventos se resuelvan nunca. Solo se vuelven a publicar los eventos resueltos rechazados; los que ya estaban almacenados no se repiten. Esto se aplica por igual a los eventos entrantes, a los envíos de integraciones y a los sobres de eventos resueltos. La nueva métrica `resolve_placement_held_sources` indica cuántas entregas finales están retenidas, y `resolve_placement_hold_fallback_total` cuenta, por motivo, las que la retención no aceptó o dejó de retener. La retención tiene un tamaño limitado y retiene un mensaje como máximo 15 minutos, tras lo cual lo deja sin confirmar como antes; dura durante la ejecución actual, y al detenerse, los mensajes retenidos quedan sin confirmar y las entregas agotadas aún pueden registrarse. Las actualizaciones graduales no requieren cambios de protocolo ni de configuración; los pods antiguos conservan el comportamiento anterior.

## zh-CN

- **已解析事件会等待分片放置恢复。** 当 resolved-events 启用分片组时，如果某个入站事件的最后一次投递中有部分已解析事件因分片放置不可用而被拒绝，系统现在会保留该消息，并在放置恢复后完成处理，而不是将其记录为死信、其事件也永远不会被解析。系统只会重新发布被拒绝的已解析事件，已存储的事件不会重复发布。此行为同样适用于入站事件、集成上行消息和已解析事件信封。新增的 `resolve_placement_held_sources` 指标显示正在保留的最终投递数量，`resolve_placement_hold_fallback_total` 按原因统计未被保留或不再保留的投递数量。保留数量有上限，每条消息最多保留 15 分钟，之后会像以前一样保持未确认状态；保留仅持续到本次运行结束，关闭时保留的消息不会被确认，因此耗尽投递次数的消息仍可能被记录。滚动升级无需更改通信协议或配置，旧 pod 保持原有行为。
