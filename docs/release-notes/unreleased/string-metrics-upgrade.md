---
section: before-upgrade
---

## en

- **`STRING` metric definitions that already exist start storing readings on upgrade.** Declaring one used to be refused, so a definition can exist only if it was created some other way; once the upgrade is finished its readings are stored as text instead of being dropped.
- **During a rolling upgrade**, pods of the previous release treat a `STRING` metric differently, so declare `STRING` metrics after the upgrade has finished. A device-management pod that has not been upgraded drops a declared `STRING` value as `declared_not_storable`; if a message is redelivered between an old and a new device-management pod, its numeric readings can be stored twice, doubling the hourly rollup's sum and count for that message. A device-state pod that has not been upgraded stores a `STRING` value that looks like a number (`"12"`) as the number 12 and leaves any earlier text beside it until a newer reading replaces the row. An event-processing pod that has not been upgraded lets a rule read such a value as a number.

## es

- **Las definiciones de métrica `STRING` que ya existen empiezan a guardar lecturas al actualizar.** Antes se rechazaba declararlas, así que una definición solo puede existir si se creó de otra manera; cuando termine la actualización, sus lecturas se guardan como texto en lugar de descartarse.
- **Durante una actualización progresiva**, los pods de la versión anterior tratan una métrica `STRING` de otra manera, así que declara las métricas `STRING` cuando la actualización haya terminado. Un pod de device-management sin actualizar descarta un valor `STRING` declarado como `declared_not_storable`; si un mensaje se reentrega entre un pod antiguo y uno nuevo de device-management, sus lecturas numéricas pueden almacenarse dos veces y duplicar la suma y el recuento del agregado horario de ese mensaje. Un pod de device-state sin actualizar guarda un valor `STRING` que parece un número (`"12"`) como el número 12 y deja el texto anterior a su lado hasta que una lectura más reciente reemplace la fila. Un pod de event-processing sin actualizar permite que una regla lea ese valor como número.

## zh-CN

- **已存在的 `STRING` 指标定义会在升级后开始存储读数。** 以前声明 `STRING` 会被拒绝，因此这样的定义只可能通过其他方式创建；升级完成后，它的读数会作为文本存储，而不再被丢弃。
- **滚动升级期间**，上一版本的 Pod 对 `STRING` 指标的处理不同，因此请在升级完成之后再声明 `STRING` 指标。尚未升级的 device-management Pod 会把已声明的 `STRING` 值作为 `declared_not_storable` 丢弃；如果一条消息在新旧 device-management Pod 之间被重新投递，其数值读数可能被存储两次，使该消息在小时汇总中的总和与计数翻倍。尚未升级的 device-state Pod 会把看起来像数字的 `STRING` 值（`"12"`）存为数字 12，并让之前的文本留在旁边，直到更新的读数替换该行。尚未升级的 event-processing Pod 会让规则把这样的值当作数字读取。
