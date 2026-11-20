---
section: whats-new
---

## en

- **The size of the deleted-entity table is reported, with an alert.** The event store keeps one row for every distinct entity token that was ever deleted, so a later entity created under the same token cannot read the deleted one's event history. Nothing prunes that table. It now appears on the `append_only_table_bytes` and `append_only_table_rows_estimate` series (`table="entity_deletion_watermarks"`, exported by event-management), and the new `EntityDeletionWatermarksLarge` warning fires when it stays over 1 GiB (about five million distinct deleted tokens) for an hour. Its entry in the alert runbook gives the query that names the tenant behind it.

## es

- **Se informa el tamaño de la tabla de entidades eliminadas, con una alerta.** El almacén de eventos conserva una fila por cada token de entidad distinto que se haya eliminado, de modo que una entidad creada después con el mismo token no pueda leer el historial de eventos de la eliminada. Nada depura esa tabla. Ahora aparece en las series `append_only_table_bytes` y `append_only_table_rows_estimate` (`table="entity_deletion_watermarks"`, exportadas por event-management), y la nueva advertencia `EntityDeletionWatermarksLarge` se activa cuando supera 1 GiB (unos cinco millones de tokens eliminados distintos) durante una hora. Su entrada en el runbook de alertas da la consulta que nombra al inquilino responsable.

## zh-CN

- **已删除实体表的大小现在会被上报，并带有告警。** 事件存储为每个曾被删除的不同实体令牌保留一行，使得之后以相同令牌创建的实体无法读取已删除实体的事件历史。该表不会被清理。它现在出现在 `append_only_table_bytes` 和 `append_only_table_rows_estimate` 序列中（`table="entity_deletion_watermarks"`，由 event-management 导出），新增的 `EntityDeletionWatermarksLarge` 警告会在其持续超过 1 GiB（约五百万个不同的已删除令牌）一小时后触发。告警手册中的对应条目给出了找出相关租户的查询。
