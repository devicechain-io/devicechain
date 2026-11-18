---
section: before-upgrade
---

## en

- **Instances with shard groups: a placement must not name a group at or above `count`.** With [NATS shard groups](#v160-nats-shard-groups) configured, assigning a vpartition to a shard group at or above `nats.shardGroups.count` is now refused, and user-management's placement mirror refuses to publish a table that names one: each such pass fails and is counted in its `placement_mirror` pass metrics. A table that already names such a group is not partly honoured: it is an unusable placement, so sharded ingest is held for every tenant on the instance until the table fits, as [Shard groups hold work instead of dead-lettering it](#v170-shard-groups) describes. Before you upgrade, make sure no vpartition is assigned to a group at or above `count`; if one is, raise `count` to cover it first. Instances without `infrastructure.nats.shardGroups` are not affected.

## es

- **Instancias con grupos de fragmentación: una asignación no debe nombrar un grupo igual o superior a `count`.** Con los [grupos de fragmentación de NATS](#v160-nats-shard-groups) configurados, asignar una vpartición a un grupo igual o superior a `nats.shardGroups.count` se rechaza ahora, y la réplica de la asignación de user-management se niega a publicar una tabla que nombre uno: cada pasada así falla y se cuenta en sus métricas de pasadas `placement_mirror`. Una tabla que ya nombra un grupo así no se respeta en parte: es una asignación inutilizable, de modo que la ingesta fragmentada se retiene para todos los inquilinos de la instancia hasta que la tabla encaje, como se describe en [Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido](#v170-shard-groups). Antes de actualizar, comprueba que ninguna vpartición esté asignada a un grupo igual o superior a `count`; si alguna lo está, aumenta primero `count` para cubrirlo. Las instancias sin `infrastructure.nats.shardGroups` no se ven afectadas.

## zh-CN

- **使用分片组的实例：放置表不得指向大于等于 `count` 的分组。** 配置了 [NATS 分片组](#v160-nats-shard-groups)后，将虚拟分区分配到大于等于 `nats.shardGroups.count` 的分片组现在会被拒绝，user-management 的放置表镜像也会拒绝发布指向此类分组的表：每一轮这样的同步都会失败，并计入其 `placement_mirror` 同步指标。已经指向此类分组的表不会被部分采用：它属于不可用的放置表，因此在表符合要求之前，实例上所有租户的分片摄取都会被暂存，详见[分片组暂存工作，而不是将其作为失败消息丢弃](#v170-shard-groups)。升级前，请确认没有虚拟分区被分配到大于等于 `count` 的分组；如果有，请先提高 `count` 以覆盖它。未设置 `infrastructure.nats.shardGroups` 的实例不受影响。
