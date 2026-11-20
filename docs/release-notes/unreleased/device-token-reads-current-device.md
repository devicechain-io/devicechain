---
section: whats-new
---

## en

- **A re-created device reads as itself.** When a device is deleted and a new device is created with the same token, reading events by that token (events, measurements, locations, alerts, presence changes, bucketed measurements and the calculated-metric preview, including through MCP) returns only the events the platform received for the new device, from the moment of the deletion on, buffered readings dated earlier included. A deleted device's events stay stored and still appear in reads filtered by neither a device nor an anchor, but no longer under its token. This applies to deletions processed by v1.8.0 or later; a token deleted and reused before the upgrade still shows its earlier device's events. Bucketed measurements for such a token are computed from the stored readings rather than the pre-aggregated rollups.

## es

- **Un dispositivo recreado se lee como sí mismo.** Cuando se elimina un dispositivo y se crea uno nuevo con el mismo token, la lectura de eventos por ese token (eventos, mediciones, ubicaciones, alertas, cambios de presencia, mediciones agrupadas y la vista previa de métricas calculadas, también a través de MCP) devuelve solo los eventos que la plataforma recibió para el nuevo dispositivo a partir del momento de la eliminación, incluidas las lecturas almacenadas en búfer con una fecha anterior. Los eventos del dispositivo eliminado siguen almacenados y aparecen en las lecturas sin filtro de dispositivo ni de ancla, pero ya no bajo su token. Esto se aplica a las eliminaciones procesadas por v1.8.0 o posterior; un token eliminado y reutilizado antes de la actualización sigue mostrando los eventos de su dispositivo anterior. Las mediciones agrupadas de ese token se calculan a partir de las lecturas almacenadas en lugar de los resúmenes preagregados.

## zh-CN

- **重新创建的设备只读取自身的事件。** 删除一个设备并用相同令牌创建新设备后，按该令牌读取事件（事件、测量值、位置、告警、在线状态变化、分桶测量值以及计算指标预览，包括通过 MCP）只返回平台自删除时刻起为新设备接收的事件，其中包括日期更早的缓存读数。已删除设备的事件仍然保存，并且仍会出现在既未按设备也未按锚点过滤的读取中，但不再出现在其令牌下。此行为适用于 v1.8.0 或更高版本处理的删除；在升级之前删除并重用的令牌仍会显示其先前设备的事件。此类令牌的分桶测量值改为根据已存储的读数计算，而不是使用预聚合的汇总。
