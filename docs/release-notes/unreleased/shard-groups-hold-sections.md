---
section: whats-new
---

## en

#### Shard groups hold work instead of dead-lettering it {#v170-shard-groups}

- **A backlog in any group pauses MQTT forwarding for every group.** The capture reader stops fetching while any group's `inbound-events` stream is refusing, so the backlog waits in the capture stream undelivered. Earlier, events for a backlogged group other than group zero were retried and, after five deliveries, routed to `failed-decode`. Per-group isolation of MQTT capture is not provided.
- **HTTP ingest is admitted per tenant.** With shard groups enabled, an HTTP or integration request is refused only when its own tenant's group is refusing, and a refusal, including one caused by an unavailable placement, answers `503` with a `Retry-After` header. Other publish errors still answer a plain `503`.
- **A missing or unusable placement holds ingest.** While the placement table is absent, cannot be adopted, or names a group at or above `nats.shardGroups.count`, sharded ingest is held and retried rather than dead-lettered. The new gauge `jetstream_shard_placement_unavailable` reads 1 in each affected service, and the `JetStreamShardPlacementUnavailable` alert fires after two minutes. Restore a compatible placement table to resume.
- **A placement can no longer name a group the streams cannot route.** With shard groups configured, assigning a vpartition to a group at or above `count` is refused, and the placement mirror refuses to publish such a table (its pass fails and the `placement_mirror` alert fires). Raise `count` first.
- **A service that cannot route a sharded stream stops at startup.** If the configuration shards `resolved-events` while device-state or event-processing is deployed, those services refuse to start with a named error instead of silently reading only group zero. Shard only `inbound-events` on an instance that runs them.

## es

#### Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido {#v170-shard-groups}

- **Un atraso en cualquier grupo pausa el reenvío MQTT de todos los grupos.** El lector de captura deja de leer mientras el flujo `inbound-events` de cualquier grupo rechace mensajes, de modo que el atraso espera en el flujo de captura sin entregarse. Antes, los eventos de un grupo atrasado distinto del grupo cero se reintentaban y, tras cinco entregas, se enviaban a `failed-decode`. No se ofrece aislamiento por grupo de la captura MQTT.
- **La ingesta HTTP se admite por inquilino.** Con grupos de fragmentación activados, una solicitud HTTP o de integración solo se rechaza cuando el grupo de su propio inquilino rechaza mensajes, y un rechazo, incluido el causado por una asignación no disponible, responde `503` con una cabecera `Retry-After`. Los demás errores de publicación siguen respondiendo un `503` sin más.
- **Una asignación ausente o inutilizable retiene la ingesta.** Mientras la tabla de asignación falte, no pueda adoptarse o nombre un grupo igual o superior a `nats.shardGroups.count`, la ingesta fragmentada se retiene y se reintenta en lugar de enviarse a mensajes fallidos. El nuevo indicador `jetstream_shard_placement_unavailable` vale 1 en cada servicio afectado, y la alerta `JetStreamShardPlacementUnavailable` se activa a los dos minutos. Restaura una tabla de asignación compatible para reanudar.
- **Una asignación ya no puede nombrar un grupo que los flujos no pueden enrutar.** Con grupos de fragmentación configurados, se rechaza asignar una vpartición a un grupo igual o superior a `count`, y la réplica de la asignación se niega a publicar esa tabla (su pasada falla y se activa la alerta `placement_mirror`). Aumenta primero `count`.
- **Un servicio que no puede enrutar un flujo fragmentado se detiene al arrancar.** Si la configuración fragmenta `resolved-events` mientras device-state o event-processing están desplegados, esos servicios se niegan a arrancar con un error explícito en lugar de leer en silencio solo el grupo cero. En una instancia que los ejecute, fragmenta solo `inbound-events`.

## zh-CN

#### 分片组暂存工作，而不是将其作为失败消息丢弃 {#v170-shard-groups}

- **任一分组出现积压时，所有分组的 MQTT 转发都会暂停。**只要任一分组的 `inbound-events` 流在拒绝写入，捕获读取器就停止拉取，积压的消息留在捕获流中，不会被投递。此前，零号分组以外的积压分组中的事件会被重试，五次投递后被转入 `failed-decode`。MQTT 捕获不提供按分组隔离。
- **HTTP 摄取按租户准入。**启用分片组后，HTTP 或集成请求只会在其所属租户的分组拒绝写入时被拒绝；拒绝（包括放置表不可用导致的拒绝）返回带 `Retry-After` 头的 `503`。其他发布错误仍返回普通的 `503`。
- **放置表缺失或不可用时，摄取会被暂存。**当放置表不存在、无法采用，或指向大于等于 `nats.shardGroups.count` 的分组时，分片摄取会被暂存并重试，而不是转入失败记录。每个受影响服务中的新指标 `jetstream_shard_placement_unavailable` 为 1，两分钟后触发 `JetStreamShardPlacementUnavailable` 告警。恢复兼容的放置表即可继续。
- **放置表不能再指向流无法路由的分组。**配置分片组后，将虚拟分区分配到大于等于 `count` 的分组会被拒绝，放置表镜像也会拒绝发布这样的表（该轮同步失败并触发 `placement_mirror` 告警）。请先提高 `count`。
- **无法路由分片流的服务会在启动时停止。**如果配置在部署了 device-state 或 event-processing 的情况下对 `resolved-events` 分片，这些服务会以明确的错误拒绝启动，而不是静默地只读取零号分组。运行这些服务的实例请只对 `inbound-events` 分片。
