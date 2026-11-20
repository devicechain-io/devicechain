---
section: whats-new
---

## en

- **A re-created device no longer shows the deleted device's events.** When a device is deleted and a new device is created with the same token, reading events by that token (events, measurements, locations, alerts, presence changes, bucketed measurements and the calculated-metric preview, including through MCP) returns only what the platform received for the new device, from the moment of the deletion on, buffered readings dated earlier included. A deleted token that has not been created again still reads its device's full history, as before. Bucketed measurements for a re-created token are computed from the stored readings rather than the pre-aggregated rollups. This applies to deletions processed by v1.8.0 or later.

## es

- **Un dispositivo recreado ya no muestra los eventos del dispositivo eliminado.** Cuando se elimina un dispositivo y se crea uno nuevo con el mismo token, la lectura de eventos por ese token (eventos, mediciones, ubicaciones, alertas, cambios de presencia, mediciones agrupadas y la vista previa de métricas calculadas, también a través de MCP) devuelve solo lo que la plataforma recibió para el nuevo dispositivo a partir del momento de la eliminación, incluidas las lecturas almacenadas en búfer con una fecha anterior. Un token eliminado que no se ha vuelto a crear sigue leyendo el historial completo de su dispositivo, como antes. Las mediciones agrupadas de un token recreado se calculan a partir de las lecturas almacenadas en lugar de los resúmenes preagregados. Esto se aplica a las eliminaciones procesadas por v1.8.0 o posterior.

## zh-CN

- **重新创建的设备不再显示已删除设备的事件。** 删除一个设备并用相同令牌创建新设备后，按该令牌读取事件（事件、测量值、位置、告警、在线状态变化、分桶测量值以及计算指标预览，包括通过 MCP）只返回平台自删除时刻起为新设备接收的内容，其中包括日期更早的缓存读数。尚未重新创建的已删除令牌仍像以前一样读取其设备的完整历史。重新创建的令牌的分桶测量值改为根据已存储的读数计算，而不是使用预聚合的汇总。此行为适用于 v1.8.0 或更高版本处理的删除。
