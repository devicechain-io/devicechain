---
section: whats-new
---

## en

- **`resolved-events` can be sharded on a new installation.** With `resolved-events` in `infrastructure.nats.shardGroups.suffixes`, event-processing now runs one detection worker per shard group, each with its own checkpoint, and device-state reads every group. This is supported only when the instance is first installed: on an existing instance where event-processing is deployed, `dcctl upgrade` and the chart refuse to start sharding `resolved-events` or to change `count` while it is sharded, and event-processing refuses to start on an instance that already has detection history, on a changed group count or suffix, or with sharding turned off after it was on. Keep tenant placement fixed once `resolved-events` is sharded. Within each group, detection watermarks and timer finality advance on that group's own traffic. Sharding is still off unless you configure it.

## es

- **`resolved-events` se puede particionar en una instalación nueva.** Con `resolved-events` en `infrastructure.nats.shardGroups.suffixes`, event-processing ejecuta ahora un trabajador de detección por grupo de particiones, cada uno con su propio punto de control, y device-state lee todos los grupos. Solo se admite cuando la instancia se instala por primera vez: en una instancia existente con event-processing desplegado, `dcctl upgrade` y el chart se niegan a empezar a particionar `resolved-events` o a cambiar `count` mientras está particionado, y event-processing se niega a arrancar en una instancia que ya tiene historial de detección, con un número de grupos o un sufijo cambiado, o con el particionado desactivado después de haberlo activado. Mantenga fija la ubicación de los inquilinos una vez particionado `resolved-events`. Dentro de cada grupo, las marcas de agua de detección y la finalización de temporizadores avanzan con el tráfico propio de ese grupo. El particionado sigue desactivado salvo que lo configure.

## zh-CN

- **新安装的实例可以对 `resolved-events` 分片。** 当 `infrastructure.nats.shardGroups.suffixes` 包含 `resolved-events` 时，event-processing 现在为每个分片组运行一个检测工作器，每个工作器有自己的检查点，device-state 读取所有分片组。仅在实例首次安装时支持：在已部署 event-processing 的现有实例上，`dcctl upgrade` 和 chart 拒绝开始对 `resolved-events` 分片，也拒绝在分片期间更改 `count`；event-processing 在已有检测历史的实例上、在分片组数量或后缀发生变化时、或在启用分片后又关闭时拒绝启动。`resolved-events` 分片后请保持租户放置不变。在每个分片组内，检测水位线和定时器的最终确定随该组自身的流量推进。除非您进行配置，分片仍保持关闭。
