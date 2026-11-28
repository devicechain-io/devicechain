---
section: whats-new
---

## en

- **Bulk changes finish, resume and report their errors reliably.** A bulk change whose lines have all been applied now always reaches `DISPATCHED`, even when its failure or ambiguity limit is reached by the last lines and the pass that applied them was interrupted; before, it could be paused for good. A resumed bulk change now always gets its own 24 hours before it is paused as `DEPENDENCY_UNAVAILABLE`, so a wait left from before the pause can no longer pause it again at once, and a resume that took effect is reported as done. `lastErrorCode` now reports the last retryable error any line met, as documented. `bulkChangeLines` accepts a new `afterOrdinal` criterion, and `dcctl bulk-change lines` pages on it, so filtering by `--posting` or `--settlement` on a running bulk change no longer skips lines. The `posting` and `settlement` filters of `bulkChangeLines` and the `dispatch` and `evidence` filters of `bulkChanges` now refuse a value that is not a state of the bulk change (for example a lower-case `ambiguous`) with `INVALID_REQUEST`, instead of answering with no lines.

## es

- **Los cambios masivos terminan, se reanudan e informan de sus errores de forma fiable.** Un cambio masivo cuyas líneas se han aplicado todas llega ahora siempre a `DISPATCHED`, aunque las últimas líneas alcancen su límite de fallos o de ambigüedad y se haya interrumpido la pasada que las aplicó; antes podía quedar en pausa para siempre. Un cambio masivo reanudado dispone ahora siempre de sus propias 24 horas antes de pausarse como `DEPENDENCY_UNAVAILABLE`, de modo que una espera anterior a la pausa ya no puede volver a pausarlo al instante, y una reanudación que surtió efecto se informa como hecha. `lastErrorCode` informa ahora del último error reintentable que encontró cualquier línea, como estaba documentado. `bulkChangeLines` acepta un nuevo criterio `afterOrdinal` y `dcctl bulk-change lines` pagina con él, así que filtrar por `--posting` o `--settlement` en un cambio masivo en ejecución ya no omite líneas. Los filtros `posting` y `settlement` de `bulkChangeLines` y los filtros `dispatch` y `evidence` de `bulkChanges` rechazan ahora con `INVALID_REQUEST` un valor que no es un estado del cambio masivo (por ejemplo `ambiguous` en minúsculas), en lugar de responder sin líneas.

## zh-CN

- **批量变更能够可靠地完成、恢复并报告错误。** 所有行都已应用的批量变更现在总会到达 `DISPATCHED`，即使最后几行使其达到失败或不明确上限、且应用这些行的那一轮被中断；此前它可能被永久暂停。恢复后的批量变更现在总会先获得属于自己的 24 小时，才会以 `DEPENDENCY_UNAVAILABLE` 暂停，因此暂停之前遗留的等待不会再让它立即再次暂停，已生效的恢复也会报告为已完成。`lastErrorCode` 现在按文档所述报告任一行遇到的最后一个可重试错误。`bulkChangeLines` 新增 `afterOrdinal` 条件，`dcctl bulk-change lines` 按其分页，因此在运行中的批量变更上按 `--posting` 或 `--settlement` 过滤不会再漏掉行。`bulkChangeLines` 的 `posting` 和 `settlement` 过滤条件以及 `bulkChanges` 的 `dispatch` 和 `evidence` 过滤条件，现在会以 `INVALID_REQUEST` 拒绝不属于批量变更状态的值（例如小写的 `ambiguous`），而不是返回空结果。
