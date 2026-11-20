---
section: whats-new
---

## en

- **Replicated streams stay replicated across a broker restart.** On a clustered broker, a service whose connection was briefly between servers during a rolling restart treated the broker as a single server. It logged that every stream and bucket was being created with one replica, and a stream or bucket created at that moment could be created with one replica and stay that way. A service now keeps the topology it last saw while it reconnects, and before it has seen any it uses the configured replica count. A stream or bucket that is already below the configured count is reported by the `JetStreamNotReplicatedAsConfigured` alert, and `dcctl broker reconcile` raises it.

## es

- **Los streams replicados siguen replicados durante un reinicio del broker.** En un broker en clúster, un servicio cuya conexión estaba brevemente entre servidores durante un reinicio escalonado trataba el broker como un único servidor. Registraba que todos los streams y buckets se estaban creando con una sola réplica, y un stream o bucket creado en ese momento podía crearse con una réplica y quedarse así. Ahora un servicio conserva la topología que vio por última vez mientras se reconecta, y antes de haber visto ninguna usa el número de réplicas configurado. Un stream o bucket que ya está por debajo del número configurado lo señala la alerta `JetStreamNotReplicatedAsConfigured`, y `dcctl broker reconcile` lo eleva.

## zh-CN

- **代理重启期间，副本流保持副本状态。** 在集群模式的代理上，如果某个服务的连接在滚动重启期间短暂处于两个服务器之间，该服务会把代理当作单个服务器。它会记录所有流和存储桶都以单副本创建，而在那一刻创建的流或存储桶可能以单副本创建，并一直保持如此。现在，服务在重新连接期间会沿用它最后看到的拓扑；在尚未看到任何拓扑之前，则使用配置的副本数。副本数已经低于配置值的流或存储桶会由 `JetStreamNotReplicatedAsConfigured` 告警报告，`dcctl broker reconcile` 会将其提升。
