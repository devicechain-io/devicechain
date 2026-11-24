---
section: whats-new
---

## en

- Retained minute summaries survive hourly backfill after raw chunks expire. Refresh-policy lock conflicts are retried on the next pass.

- **Historical hour rollups.** Event management now fills older hour rollups in the background, newest first, with at most four hourly windows and a 30-second work budget per pass. Reads continue using minute rollups until each hour is complete. The worker shares the existing backfill lock and connection-pool headroom requirement. Operators can track progress with the `devicechain_eventmanagement_rollup_hour_backfill_complete_from_timestamp_seconds`, `devicechain_eventmanagement_rollup_hour_backfill_remaining_windows`, and `devicechain_eventmanagement_rollup_hour_backfill_windows_total` metrics and the standard periodic-task metrics.

## es

- El relleno horario conserva los resúmenes por minuto cuando caducan los fragmentos de datos originales. Los conflictos de bloqueo con la política de actualización se reintentan en la siguiente pasada.

- **Resúmenes horarios históricos.** La gestión de eventos ahora completa los resúmenes horarios antiguos en segundo plano, empezando por los más recientes, con un máximo de cuatro ventanas horarias y un presupuesto de ejecución de 30 segundos por pasada. Las consultas siguen usando los resúmenes por minuto hasta que cada hora esté completa. El proceso comparte el bloqueo existente de relleno histórico y su requisito de conexiones libres. Los operadores pueden seguir el progreso con las métricas `devicechain_eventmanagement_rollup_hour_backfill_complete_from_timestamp_seconds`, `devicechain_eventmanagement_rollup_hour_backfill_remaining_windows` y `devicechain_eventmanagement_rollup_hour_backfill_windows_total`, además de las métricas estándar de tareas periódicas.

## zh-CN

- 原始数据块过期后，小时回填仍会保留分钟汇总。与刷新策略发生锁冲突时将在下一轮重试。

- **历史小时汇总。** 事件管理现在会在后台从新到旧补齐历史小时汇总，每轮最多处理四个小时的数据，工作时间预算为 30 秒。每个小时完成前，查询继续使用分钟汇总。该任务复用现有历史回填锁和连接池空闲连接要求。运维人员可以通过 `devicechain_eventmanagement_rollup_hour_backfill_complete_from_timestamp_seconds`、`devicechain_eventmanagement_rollup_hour_backfill_remaining_windows` 和 `devicechain_eventmanagement_rollup_hour_backfill_windows_total` 指标以及标准周期任务指标查看进度。
