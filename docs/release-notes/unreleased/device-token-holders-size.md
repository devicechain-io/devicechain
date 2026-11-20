---
section: whats-new
---

## en

- **The size of the device token holder table is reported.** The event store keeps one row for every device token a live device was ever known to hold. It is how a read by a token that was deleted and then created again returns only the new device's events. Nothing prunes that table. It now appears on the `append_only_table_bytes` and `append_only_table_rows_estimate` series (`table="device_token_holders"`, exported by event-management). It has no size alert of its own: it grows with the fleet, so a large table is normally a large fleet. Devices deleted and created again and again under new tokens also grow the deleted-entity table, which `EntityDeletionWatermarksLarge` watches, and the event store's volume alerts cover the space.

## es

- **Se informa el tamaño de la tabla de titulares de tokens de dispositivo.** El almacén de eventos conserva una fila por cada token de dispositivo que algún dispositivo activo haya tenido. Es lo que permite que una lectura por un token eliminado y vuelto a crear devuelva solo los eventos del dispositivo nuevo. Nada depura esa tabla. Ahora aparece en las series `append_only_table_bytes` y `append_only_table_rows_estimate` (`table="device_token_holders"`, exportadas por event-management). No tiene una alerta de tamaño propia: crece con la flota, así que una tabla grande suele ser una flota grande. Los dispositivos que se eliminan y se vuelven a crear una y otra vez con tokens nuevos también hacen crecer la tabla de entidades eliminadas, que vigila `EntityDeletionWatermarksLarge`, y las alertas de volumen del almacén de eventos cubren el espacio.

## zh-CN

- **现在会上报设备令牌持有者表的大小。** 事件存储为每个曾被在用设备持有过的设备令牌保留一行。正是借助这张表，按一个被删除后又重新创建的令牌读取时，只返回新设备的事件。该表不会被清理。它现在出现在 `append_only_table_bytes` 和 `append_only_table_rows_estimate` 序列中（`table="device_token_holders"`，由 event-management 导出）。它没有单独的大小告警：它随设备规模增长，所以表大通常意味着设备多。如果反复删除设备并以新令牌重新创建，已删除实体表也会随之增长，该表由 `EntityDeletionWatermarksLarge` 监控；事件存储的卷容量告警则覆盖存储空间。
