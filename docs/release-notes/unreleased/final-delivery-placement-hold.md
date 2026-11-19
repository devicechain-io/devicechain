---
section: whats-new
---

## en

- **Captured messages wait for shard placement.** With shard groups on, a captured message whose final delivery attempt met unavailable shard placement is now held and forwarded once placement returns, instead of being recorded as a dead letter without its payload. This applies to both the capture stream and the MQTT message store. The new `placement_held_messages` gauge shows how many final deliveries are being held. The hold is bounded per source and lasts for the current run; shutdown abandons held messages unacknowledged, so exhausted deliveries can still be recorded. Rolling upgrades need no wire or configuration change; old pods retain the previous behavior.

## es

- **Los mensajes capturados esperan a que vuelva la ubicación de particiones.** Con los grupos de particiones activados, un mensaje capturado cuyo último intento de entrega encontró la ubicación de particiones no disponible se retiene y se reenvía cuando esta vuelve, en lugar de registrarse como mensaje fallido sin su contenido. Esto se aplica tanto al stream de captura como al almacén de mensajes MQTT. La nueva métrica `placement_held_messages` indica cuántas entregas finales están retenidas. La retención tiene un límite por origen y dura durante la ejecución actual; al detenerse, los mensajes retenidos quedan sin confirmar y las entregas agotadas aún pueden registrarse. Las actualizaciones graduales no requieren cambios de protocolo ni de configuración; los pods antiguos conservan el comportamiento anterior.

## zh-CN

- **捕获的消息会等待分片放置恢复。** 启用分片组时，如果捕获消息的最后一次投递遇到分片放置不可用，系统现在会保留该消息，并在放置恢复后转发，而不是将其记录为不含载荷的死信。此行为适用于捕获流和 MQTT 消息存储。新增的 `placement_held_messages` 指标显示正在保留的最终投递数量。每个来源的保留数量有上限，且仅持续到本次运行结束；关闭时保留的消息不会被确认，因此耗尽投递次数的消息仍可能被记录。滚动升级无需更改通信协议或配置，旧 pod 保持原有行为。
