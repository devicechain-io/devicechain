---
section: whats-new
---

## en

#### Event anchors of a re-created entity are kept {#v170-deleted-entity-anchors}

- When a device, customer, area, asset or group is deleted, `event-management` removes the anchors of the events it received before the delete. It used to decide by the time the device reported, so a device re-created with the same token that uploaded buffered readings dated before the delete could lose the anchors of those readings, and they then no longer appeared under their customer, area or asset. It now decides by the time the platform received each event.

## es

#### Se conservan los anclajes de eventos de una entidad recreada {#v170-deleted-entity-anchors}

- Al eliminar un dispositivo, cliente, área, activo o grupo, `event-management` elimina los anclajes de los eventos que recibió antes de la eliminación. Antes lo decidía por la hora que indicaba el dispositivo, de modo que un dispositivo recreado con el mismo token que enviaba lecturas almacenadas con fecha anterior a la eliminación podía perder los anclajes de esas lecturas, que dejaban de aparecer bajo su cliente, área o activo. Ahora lo decide por la hora en que la plataforma recibió cada evento.

## zh-CN

#### 重新创建的实体会保留其事件锚点 {#v170-deleted-entity-anchors}

- 删除设备、客户、区域、资产或分组时，`event-management` 会删除在删除之前收到的事件的锚点。此前它按设备报告的时间判断，因此以相同令牌重新创建的设备若上传了日期早于删除时间的缓存读数，这些读数的锚点可能被删除，从而不再显示在其客户、区域或资产下。现在按平台收到每个事件的时间判断。
