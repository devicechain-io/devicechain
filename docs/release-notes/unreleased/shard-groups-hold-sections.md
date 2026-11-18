---
section: whats-new
---

## en

#### Shard groups hold work instead of dead-lettering it {#v170-shard-groups}

- **A backlog in any group pauses MQTT forwarding for every group.** The capture reader stops fetching while any group's `inbound-events` stream is refusing, so the backlog waits in the capture stream undelivered. Earlier, events for a backlogged group other than group zero were retried and, after five deliveries, routed to `failed-decode`. Per-group isolation of MQTT capture is not provided.
- **HTTP ingest is admitted per tenant.** With shard groups enabled, an HTTP or integration request is refused only when its own tenant's group is refusing, and a refusal, including one caused by an unavailable placement, answers `503` with a `Retry-After` header. Other publish errors still answer a plain `503`.
- **A missing or unusable placement holds ingest.** While the placement table is absent, cannot be adopted, or names a group at or above `nats.shardGroups.count`, sharded ingest is held and retried rather than dead-lettered. The new gauge `jetstream_shard_placement_unavailable` reads 1 in each affected service, and the `JetStreamShardPlacementUnavailable` alert fires after two minutes. Restore a compatible placement table to resume.
- **Every writer follows the placement.** The `inbound-events` writers in device-state, sparkplug-ingest and lwm2m-ingest now publish each tenant to its own group. Before, they published every tenant to group zero, bypassing the placement and the backpressure above.
- **A service that cannot route a sharded stream refuses it.** A service that has not enabled shard routing refuses to read or write a sharded stream with a named error, instead of silently using only group zero. `dcctl` and the chart still refuse to shard `resolved-events` while device-state or event-processing is deployed, so shard only `inbound-events` on an instance that runs them.
- **Groundwork for sharding `resolved-events` ships unused.** device-state can read every group of a sharded `resolved-events`, the broker client can replay one group or the tenant's own group, and event-processing's rule preview and version compare read the tenant's own group. Per-group detection workers are built but the service does not start them yet. None of it runs while the refusal above stands.

## es

#### Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido {#v170-shard-groups}

- **Un atraso en cualquier grupo pausa el reenvío MQTT de todos los grupos.** El lector de captura deja de leer mientras el flujo `inbound-events` de cualquier grupo rechace mensajes, de modo que el atraso espera en el flujo de captura sin entregarse. Antes, los eventos de un grupo atrasado distinto del grupo cero se reintentaban y, tras cinco entregas, se enviaban a `failed-decode`. No se ofrece aislamiento por grupo de la captura MQTT.
- **La ingesta HTTP se admite por inquilino.** Con grupos de fragmentación activados, una solicitud HTTP o de integración solo se rechaza cuando el grupo de su propio inquilino rechaza mensajes, y un rechazo, incluido el causado por una asignación no disponible, responde `503` con una cabecera `Retry-After`. Los demás errores de publicación siguen respondiendo un `503` sin más.
- **Una asignación ausente o inutilizable retiene la ingesta.** Mientras la tabla de asignación falte, no pueda adoptarse o nombre un grupo igual o superior a `nats.shardGroups.count`, la ingesta fragmentada se retiene y se reintenta en lugar de enviarse a mensajes fallidos. El nuevo indicador `jetstream_shard_placement_unavailable` vale 1 en cada servicio afectado, y la alerta `JetStreamShardPlacementUnavailable` se activa a los dos minutos. Restaura una tabla de asignación compatible para reanudar.
- **Todos los escritores siguen la asignación.** Los escritores de `inbound-events` de device-state, sparkplug-ingest y lwm2m-ingest publican ahora cada inquilino en su propio grupo. Antes publicaban todos los inquilinos en el grupo cero, sin pasar por la asignación ni por la contrapresión descrita arriba.
- **Un servicio que no puede enrutar un flujo fragmentado lo rechaza.** Un servicio que no ha activado el enrutamiento por grupos se niega a leer o escribir un flujo fragmentado con un error explícito, en lugar de usar en silencio solo el grupo cero. `dcctl` y el chart siguen rechazando fragmentar `resolved-events` mientras device-state o event-processing estén desplegados, así que en una instancia que los ejecute fragmenta solo `inbound-events`.
- **La base para fragmentar `resolved-events` se incluye sin usarse.** device-state puede leer todos los grupos de un `resolved-events` fragmentado, el cliente del broker puede reproducir un grupo o el grupo del propio inquilino, y la vista previa de reglas y la comparación de versiones de event-processing leen el grupo del propio inquilino. Los trabajadores de detección por grupo están construidos, pero el servicio aún no los inicia. Nada de ello se ejecuta mientras se mantenga el rechazo anterior.

## zh-CN

#### 分片组暂存工作，而不是将其作为失败消息丢弃 {#v170-shard-groups}

- **任一分组出现积压时，所有分组的 MQTT 转发都会暂停。**只要任一分组的 `inbound-events` 流在拒绝写入，捕获读取器就停止拉取，积压的消息留在捕获流中，不会被投递。此前，零号分组以外的积压分组中的事件会被重试，五次投递后被转入 `failed-decode`。MQTT 捕获不提供按分组隔离。
- **HTTP 摄取按租户准入。**启用分片组后，HTTP 或集成请求只会在其所属租户的分组拒绝写入时被拒绝；拒绝（包括放置表不可用导致的拒绝）返回带 `Retry-After` 头的 `503`。其他发布错误仍返回普通的 `503`。
- **放置表缺失或不可用时，摄取会被暂存。**当放置表不存在、无法采用，或指向大于等于 `nats.shardGroups.count` 的分组时，分片摄取会被暂存并重试，而不是转入失败记录。每个受影响服务中的新指标 `jetstream_shard_placement_unavailable` 为 1，两分钟后触发 `JetStreamShardPlacementUnavailable` 告警。恢复兼容的放置表即可继续。
- **所有写入方都遵循放置表。**device-state、sparkplug-ingest 和 lwm2m-ingest 中的 `inbound-events` 写入方现在会把每个租户发布到其所属分组。此前它们把所有租户都发布到零号分组，绕过了放置表和上述背压机制。
- **无法路由分片流的服务会拒绝该流。**未启用分片路由的服务会以明确的错误拒绝读取或写入分片流，而不是静默地只使用零号分组。在部署了 device-state 或 event-processing 时，`dcctl` 和 chart 仍会拒绝对 `resolved-events` 分片，因此运行这些服务的实例请只对 `inbound-events` 分片。
- **对 `resolved-events` 分片的基础功能已随版本提供，但尚未启用。**device-state 可以读取分片后 `resolved-events` 的所有分组；代理客户端可以重放单个分组或租户自身的分组；event-processing 的规则预览和版本比较会读取租户自身的分组。按分组的检测工作器已经构建，但该服务尚未启动它们。只要上述拒绝仍然存在，这些功能都不会运行。
