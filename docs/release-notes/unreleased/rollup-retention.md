---
section: before-upgrade
---

## en

- **Measurement aggregates now age out with retention.** With `lifecycle.retentionDays` set in event-management, the per-minute measurement aggregates behind charts and bucketed reads are now dropped one chunk interval (rounded up to a day) after the raw readings, and bucketed reads no longer return aggregates older than that window. From the first start after the upgrade, bucketed reads stop returning aggregate history older than the window, so charts over that range show no data; a background job then deletes that history, a whole storage chunk at a time. Instances with `retentionDays: 0` (the default) are unaffected. During the rolling upgrade, pods still on v1.6 keep serving older aggregates until they are replaced.

## es

- **Los agregados de mediciones ahora caducan con la retención.** Con `lifecycle.retentionDays` configurado en event-management, los agregados de mediciones por minuto que sustentan los gráficos y las lecturas por intervalos ahora se eliminan un intervalo de chunk (redondeado hacia arriba a días completos) después de las lecturas sin procesar, y las lecturas por intervalos ya no devuelven agregados anteriores a esa ventana. Desde el primer arranque tras la actualización, las lecturas por intervalos dejan de devolver el historial de agregados anterior a la ventana, por lo que los gráficos de ese rango no muestran datos; después, un trabajo en segundo plano elimina ese historial, un chunk de almacenamiento completo cada vez. Las instancias con `retentionDays: 0` (el valor predeterminado) no se ven afectadas. Durante la actualización gradual, los pods que aún ejecutan v1.6 siguen sirviendo agregados antiguos hasta que se sustituyen.

## zh-CN

- **测量聚合现在会随保留期过期。** 在 event-management 中设置 `lifecycle.retentionDays` 后，图表和分桶读取使用的每分钟测量聚合会在原始读数之后多保留一个分块间隔（向上取整到天），随后被删除；分桶读取也不再返回早于该窗口的聚合。升级后首次启动起，分桶读取即不再返回早于该窗口的聚合历史，对应时间范围的图表将不显示数据；随后由后台作业按整个存储分块删除这些历史。设置为 `retentionDays: 0`（默认值）的实例不受影响。滚动升级期间，仍运行 v1.6 的 Pod 会继续提供较早的聚合，直到被替换。
