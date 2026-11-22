---
section: before-upgrade
---

## en

- **Device attribute writes follow a writer protocol.** Every write of a device's attributes, and the tenant purge, now marks its transaction as following the device attribute writer protocol, and every set, unset or re-creation of a device attribute advances a per-key generation kept in a new table, `entity_attribute_generations`. Nothing enforces the protocol in this release; a later release relies on every pod of this one following it, so upgrade one minor release at a time. While the upgrade is rolling out, a device deleted by a pod that has not been upgraded yet can leave attribute rows behind; a cleanup pass on one replica removes them within ten minutes, and `device_attribute_orphans_removed_total{table}` counts what it removed.

## es

- **Las escrituras de atributos de dispositivo siguen un protocolo de escritura.** Toda escritura de los atributos de un dispositivo, y la purga de un inquilino, marcan ahora su transacción como conforme al protocolo de escritura de atributos de dispositivo, y cada asignación, eliminación o recreación de un atributo de dispositivo avanza una generación por clave guardada en una tabla nueva, `entity_attribute_generations`. En esta versión nada exige el protocolo; una versión posterior depende de que todos los pods de esta lo sigan, así que actualice de versión menor en versión menor. Mientras la actualización se despliega, un dispositivo eliminado por un pod que aún no se ha actualizado puede dejar filas de atributos; una pasada de limpieza en una sola réplica las elimina en un plazo de diez minutos, y `device_attribute_orphans_removed_total{table}` cuenta lo que eliminó.

## zh-CN

- **设备属性写入遵循写入协议。** 现在，对设备属性的每一次写入以及租户清除，都会将其事务标记为遵循设备属性写入协议；设备属性的每一次设置、取消设置或重新创建，都会推进保存在新表 `entity_attribute_generations` 中的按键代次。本版本尚不强制执行该协议；后续版本依赖于本版本的所有 pod 都遵循它，因此请逐个次版本升级。在升级滚动进行期间，由尚未升级的 pod 删除的设备可能会遗留属性行；在单个副本上运行的清理过程会在十分钟内将其删除，`device_attribute_orphans_removed_total{table}` 会统计删除的数量。
