---
section: before-upgrade
---

## en

- **Replays are now capped by default.** Each event-processing process runs at most 8 rule previews, version comparisons and `dcctl test` evaluations at once (`preview.maxConcurrent`), with no setting needed. A busy instance may therefore see BUSY refusals or degraded preview answers after the upgrade where it saw none before; the author retries. Raise `preview.maxConcurrent` once every pod runs this release if your tenants need more.

## es

- **Las reproducciones ahora tienen un límite por defecto.** Cada proceso de event-processing ejecuta como máximo 8 previsualizaciones de reglas, comparaciones de versiones y evaluaciones de `dcctl test` a la vez (`preview.maxConcurrent`), sin necesidad de ningún ajuste. Por ello, una instancia ocupada puede ver rechazos BUSY o respuestas de previsualización degradadas tras la actualización donde antes no veía ninguno; el autor reintenta. Aumente `preview.maxConcurrent` cuando todos los pods ejecuten esta versión si sus inquilinos necesitan más.

## zh-CN

- **重放现在默认设有上限。** 每个 event-processing 进程同时最多运行 8 个规则预览、版本比较和 `dcctl test` 评估（`preview.maxConcurrent`），无需任何设置。因此繁忙的实例在升级后可能出现此前没有的 BUSY 拒绝或降级的预览结果；作者重试即可。如果租户需要更多，请在所有 Pod 都运行此版本后提高 `preview.maxConcurrent`。
