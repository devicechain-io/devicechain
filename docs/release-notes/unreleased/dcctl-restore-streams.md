---
section: whats-new
---

## en

- **Restore captured JetStream streams with dcctl.** `dcctl restore <instance> --from <backup-set> --apply --only stream` restores captured messages and consumer configuration/state without writing objects. Existing streams require the same explicit `--overwrite-target` flag as object restore; replacement deletes the old stream and is not atomic. Bundles are checked again before replacement, uploads wait for chunk acknowledgments, and failures return nonzero with the stream name. The current broker-admin grant lacks restore/upload permissions and most stream-delete permissions; arrange these before attempting replacement. dcctl does not change broker grants. See [stream restore](disaster-recovery.md#restore-streams).

## es

- **Restaurar streams de JetStream capturados con dcctl.** `dcctl restore <instance> --from <backup-set> --apply --only stream` restaura mensajes y configuración/estado de consumidores sin escribir objetos. Los streams existentes requieren el mismo indicador explícito `--overwrite-target` que la restauración de objetos; el reemplazo elimina el stream anterior y no es atómico. Los archivos se verifican de nuevo antes del reemplazo, la carga espera confirmaciones por fragmento y los fallos devuelven un estado distinto de cero con el nombre del stream. Los permisos actuales de broker-admin no incluyen restauración/carga ni la eliminación de la mayoría de los streams; prepárelos antes de reemplazar. dcctl no cambia los permisos del broker. Consulte [restaurar streams](disaster-recovery.md#restore-streams).

## zh-CN

- **使用 dcctl 恢复备份中的 JetStream 流。** `dcctl restore <instance> --from <backup-set> --apply --only stream` 恢复消息以及消费者配置和状态，不写入对象。已有流与对象恢复一样，需要显式指定 `--overwrite-target`；替换会删除旧流，且不是原子操作。替换前会重新校验备份文件，上传会等待每块确认，失败时返回非零状态并指出流名称。当前 broker-admin 授权缺少恢复、上传以及大多数流的删除权限；替换前请先配置这些权限。dcctl 不会修改 broker 授权。参见[恢复流](disaster-recovery.md#restore-streams)。
