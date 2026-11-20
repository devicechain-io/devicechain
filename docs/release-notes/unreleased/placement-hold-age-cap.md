---
section: whats-new
---

## en

- **A held delivery gives up after 15 minutes.** While a tenant's shard placement is unavailable, event-sources holds that tenant's last-chance deliveries. A tenant whose placement never returns no longer keeps holding them: after 15 minutes the delivery is left unacknowledged, as it would be without the hold. New counter `total_placement_hold_fallback{source,reason}` counts deliveries that were not held because the hold was full (`full`) or that stopped being held (`expired`). A recovered held message is also no longer counted as inbound twice.

## es

- **Una entrega retenida se abandona a los 15 minutos.** Mientras la ubicación de shard de un inquilino no está disponible, event-sources retiene sus entregas de último intento. Un inquilino cuya ubicación nunca vuelve ya no las retiene indefinidamente: tras 15 minutos la entrega queda sin confirmar, como sin la retención. El nuevo contador `total_placement_hold_fallback{source,reason}` cuenta las entregas no retenidas porque la retención estaba llena (`full`) o que dejaron de retenerse (`expired`). Un mensaje retenido y recuperado tampoco se cuenta ya dos veces como entrante.

## zh-CN

- **被挂起的投递在 15 分钟后放弃。** 租户的分片放置不可用时，event-sources 会挂起该租户的最后一次投递。放置始终不恢复的租户不再无限期占用挂起容量：15 分钟后该投递保持未确认，与没有挂起时相同。新增计数器 `total_placement_hold_fallback{source,reason}`，统计因挂起已满而未被挂起（`full`）或已不再被挂起（`expired`）的投递。恢复后的挂起消息也不再被重复计为入站。
