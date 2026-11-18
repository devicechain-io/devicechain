---
section: before-upgrade
---

## en

- **Instances with shard groups: a placement must not name a group at or above `count`.** With [NATS shard groups](#v160-nats-shard-groups) configured, assigning a vpartition to a shard group at or above `nats.shardGroups.count` is now refused, and the placement mirror refuses to publish a table that names one. A table that already names such a group is not partly honoured: every service stops forwarding into the sharded streams, for every tenant on the instance, until the table fits. Before you upgrade, make sure no vpartition is assigned to a group at or above `count`; if one is, raise `count` to cover it first. Instances without `infrastructure.nats.shardGroups` are not affected.

## es

- **Instancias con grupos de fragmentación: una asignación no debe nombrar un grupo igual o superior a `count`.** Con los [grupos de fragmentación de NATS](#v160-nats-shard-groups) configurados, asignar una vpartición a un grupo igual o superior a `nats.shardGroups.count` se rechaza ahora, y la réplica de la asignación se niega a publicar una tabla que nombre uno. Una tabla que ya nombra un grupo así no se respeta en parte: todos los servicios dejan de reenviar a los flujos fragmentados, para todos los inquilinos de la instancia, hasta que la tabla encaje. Antes de actualizar, comprueba que ninguna vpartición esté asignada a un grupo igual o superior a `count`; si alguna lo está, aumenta primero `count` para cubrirlo. Las instancias sin `infrastructure.nats.shardGroups` no se ven afectadas.

## zh-CN

- **使用分片组的实例：放置表不得指向大于等于 `count` 的分组。** 配置了 [NATS 分片组](#v160-nats-shard-groups)后，将虚拟分区分配到大于等于 `nats.shardGroups.count` 的分片组现在会被拒绝，放置表镜像也会拒绝发布指向此类分组的表。已经指向此类分组的表不会被部分采用：在表符合要求之前，所有服务都会停止向分片流转发，影响实例上的所有租户。升级前，请确认没有虚拟分区被分配到大于等于 `count` 的分组；如果有，请先提高 `count` 以覆盖它。未设置 `infrastructure.nats.shardGroups` 的实例不受影响。
