---
section: whats-new
---

## en

- **`JetStreamClusterUnused` no longer fires on a shard-group instance.** An instance with [NATS shard groups](#v160-nats-shard-groups) runs a multi-server broker with every stream at one replica on purpose, and the warning fired on it continuously. The alert now stays quiet while any service reports a shard-group stream. A clustered single-replica broker with no shard groups still raises it. The alert's name is unchanged.

## es

- **`JetStreamClusterUnused` ya no se dispara en una instancia con grupos de fragmentación.** Una instancia con [grupos de fragmentación de NATS](#v160-nats-shard-groups) ejecuta a propósito un broker de varios servidores con cada stream en una sola réplica, y la advertencia se disparaba en ella de forma continua. La alerta permanece ahora en silencio mientras algún servicio informe de un stream de grupo de fragmentación. Un broker en clúster de una sola réplica sin grupos de fragmentación la sigue generando. El nombre de la alerta no cambia.

## zh-CN

- **`JetStreamClusterUnused` 不再在分片组实例上触发。** 启用 [NATS 分片组](#v160-nats-shard-groups)的实例有意运行多服务器代理，且每个流只有一个副本，此前该警告会在这类实例上持续触发。现在只要有任何服务报告分片组流，该告警就保持静默。没有分片组的集群化单副本代理仍会触发该告警。告警名称保持不变。
