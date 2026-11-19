---
section: whats-new
---

## en

- **Backups of sharded instances include every shard-group stream.** On an instance with `infrastructure.nats.shardGroups` configured, `dcctl backup` now exports each group stream of a sharded stream (`<stream>-g1`, `<stream>-g2`, and so on) alongside its base stream and records the layout in the manifest. A configured group stream that is missing or unreadable, or a group stream the deployed configuration does not declare, fails the export instead of producing a partial backup. `dcctl restore --plan` lists every group stream and refuses a target whose shard-group layout differs from the backup's. Backups of a sharded instance taken with an earlier dcctl contain only the base streams (group zero), not the group streams; take a new backup after upgrading dcctl. Instances without shard groups are unaffected, and their manifests are unchanged.

## es

- **Las copias de seguridad de instancias particionadas incluyen todos los streams de grupo.** En una instancia con `infrastructure.nats.shardGroups` configurado, `dcctl backup` exporta ahora cada stream de grupo de un stream particionado (`<stream>-g1`, `<stream>-g2`, etc.) junto con su stream base y registra la distribución en el manifiesto. Un stream de grupo configurado que falta o no se puede leer, o un stream de grupo que la configuración desplegada no declara, hace fallar la exportación en lugar de producir una copia parcial. `dcctl restore --plan` enumera cada stream de grupo y rechaza un destino cuya distribución de grupos difiere de la de la copia. Las copias de una instancia particionada tomadas con un dcctl anterior contienen solo los streams base (grupo cero), no los streams de grupo; tome una copia nueva tras actualizar dcctl. Las instancias sin grupos de particiones no se ven afectadas y sus manifiestos no cambian.

## zh-CN

- **分片实例的备份包含所有分片组流。** 在配置了 `infrastructure.nats.shardGroups` 的实例上，`dcctl backup` 现在会将分片流的每个分片组流（`<stream>-g1`、`<stream>-g2` 等）与其基础流一起导出，并在清单中记录分片布局。已配置的分片组流缺失或无法读取，或存在已部署配置未声明的分片组流时，导出会失败，而不会生成不完整的备份。`dcctl restore --plan` 会列出每个分片组流，并拒绝分片布局与备份不同的目标。使用较早版本 dcctl 为分片实例创建的备份只包含基础流（第 0 组），不包含分片组流；升级 dcctl 后请重新创建备份。未配置分片组的实例不受影响，其清单也保持不变。
