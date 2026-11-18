---
section: before-upgrade
---

## en

- **Policy refusals are counted as `unwanted`, not `dropped`.** The `resolve`, `device_reports` and `update_reports` processing loops no longer count traffic from a disabled device, or decode failures suppressed past their record budget, as `result="dropped"`. That traffic is now counted as the new `result="unwanted"`, and the overview dashboard's failure panels leave it out. `dropped` now always means a message was given up on. No metric is renamed. During a rolling upgrade, pods still on the previous version count this traffic as `dropped` until they are replaced.

## es

- **Las denegaciones por política se cuentan como `unwanted`, no como `dropped`.** Los bucles de procesamiento `resolve`, `device_reports` y `update_reports` ya no cuentan como `result="dropped"` el tráfico de un dispositivo deshabilitado ni los fallos de decodificación suprimidos tras agotar su presupuesto de registros. Ese tráfico se cuenta ahora con el nuevo `result="unwanted"`, y los paneles de fallos del panel de resumen lo excluyen. `dropped` significa ahora siempre que se renunció a un mensaje. No se renombra ninguna métrica. Durante una actualización progresiva, los pods que aún ejecutan la versión anterior cuentan este tráfico como `dropped` hasta que se reemplazan.

## zh-CN

- **策略拒绝现在计为 `unwanted`，不再计为 `dropped`。** `resolve`、`device_reports` 和 `update_reports` 处理循环不再把已禁用设备的流量，或超出记录预算而被抑制的解码失败，计为 `result="dropped"`。这类流量现在计为新的 `result="unwanted"`，概览仪表板的失败面板也不再包含它。`dropped` 现在始终表示已放弃处理某条消息。没有指标被重命名。滚动升级期间，仍在运行旧版本的 Pod 会把这类流量计为 `dropped`，直到它们被替换。
