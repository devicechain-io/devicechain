---
section: whats-new
---

## en

- **The detection engine spaces its snapshots by their cost.** Reaching `checkpointEvents` events no longer snapshots at once: the engine also waits until four times the last snapshot's cost has passed, so with large detection state the engine snapshots less often, never more than `checkpointIntervalSeconds` apart, and always when buffered acknowledgements reach half the consumer's limit. After a crash it may replay more than `checkpointEvents` events. No setting changes.

## es

- **El motor de detección espacia sus instantáneas según su coste.** Alcanzar `checkpointEvents` eventos ya no crea una instantánea de inmediato: el motor espera además a que pase cuatro veces el coste de la última instantánea, por lo que con un estado de detección grande el motor crea instantáneas con menos frecuencia, sin superar `checkpointIntervalSeconds` entre ellas, y siempre cuando las confirmaciones almacenadas alcanzan la mitad del límite del consumidor. Tras un fallo puede reproducir más de `checkpointEvents` eventos. No hay cambios de configuración.

## zh-CN

- **检测引擎根据快照开销安排快照间隔。** 达到 `checkpointEvents` 个事件后不再立即生成快照：引擎还会等待上一次快照开销的四倍时间，因此检测状态较大时，引擎降低快照频率，但间隔最长不超过 `checkpointIntervalSeconds`，且缓冲的确认达到消费者上限的一半时始终触发检查点。崩溃后可能重放超过 `checkpointEvents` 个事件。无需更改设置。
