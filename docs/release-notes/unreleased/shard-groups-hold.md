---
section: before-upgrade
---

## en

- **Instances with shard groups: ingest is held, not dead-lettered.** Nothing in [Shard groups hold work instead of dead-lettering it](#v170-shard-groups) changes an instance that has not set `infrastructure.nats.shardGroups`. On one that has, a backlogged group or an unusable placement now holds ingest and retries it instead of dead-lettering it, a refused HTTP or integration request answers `503` with `Retry-After`, and a service that cannot route a sharded stream refuses it. Read that subsection before you upgrade.

## es

- **Instancias con grupos de fragmentación: la ingesta se retiene en lugar de descartarse como fallida.** Nada de [Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido](#v170-shard-groups) cambia una instancia que no haya definido `infrastructure.nats.shardGroups`. En una que sí lo haya hecho, un grupo atrasado o una asignación inutilizable retienen ahora la ingesta y la reintentan en lugar de enviarla a mensajes fallidos, una solicitud HTTP o de integración rechazada responde `503` con `Retry-After`, y un servicio que no puede enrutar un flujo fragmentado lo rechaza. Lee esa subsección antes de actualizar.

## zh-CN

- **使用分片组的实例：摄取会被暂存，而不是转入失败消息。**[分片组暂存工作，而不是将其作为失败消息丢弃](#v170-shard-groups)中的内容不会改变未设置 `infrastructure.nats.shardGroups` 的实例。对于已设置的实例，分组积压或放置表不可用时，摄取现在会被暂存并重试，而不是转入失败消息；被拒绝的 HTTP 或集成请求返回带 `Retry-After` 的 `503`；无法路由分片流的服务会拒绝该流。请在升级前阅读该小节。
