---
section: whats-new
---

## en

- **Alerts and metrics for bulk changes.** With `metrics.alerts` on, the chart adds a `bulk-changes` PrometheusRule with seven alerts — `BulkChangesNotAdvancing`, `BulkChangesStalled`, `BulkChangesDriverSilent`, `BulkChangesAuthorityUnavailable`, `BulkChangesSettlementLagging`, `BulkChangesFencePending` and `BulkChangesNotActivated` — each with an entry in the alert runbook. They read the `devicechain_devicemanagement_bulk_change_*` series, which carry no tenant, operation or device label. device-management exports those series only once a release serves bulk changes, so until then the alerts stay silent; nothing to do.

## es

- **Alertas y métricas para los cambios masivos.** Con `metrics.alerts` activado, el chart añade una PrometheusRule `bulk-changes` con siete alertas —`BulkChangesNotAdvancing`, `BulkChangesStalled`, `BulkChangesDriverSilent`, `BulkChangesAuthorityUnavailable`, `BulkChangesSettlementLagging`, `BulkChangesFencePending` y `BulkChangesNotActivated`—, cada una con su entrada en el runbook de alertas. Leen las series `devicechain_devicemanagement_bulk_change_*`, que no llevan etiqueta de tenant, de operación ni de dispositivo. device-management solo exporta esas series cuando una versión sirve los cambios masivos, así que hasta entonces las alertas permanecen en silencio; no hay nada que hacer.

## zh-CN

- **批量变更的告警和指标。** 启用 `metrics.alerts` 时，chart 会新增一个 `bulk-changes` PrometheusRule，包含七条告警——`BulkChangesNotAdvancing`、`BulkChangesStalled`、`BulkChangesDriverSilent`、`BulkChangesAuthorityUnavailable`、`BulkChangesSettlementLagging`、`BulkChangesFencePending` 和 `BulkChangesNotActivated`——每条在告警运行手册中都有对应条目。这些告警读取 `devicechain_devicemanagement_bulk_change_*` 序列，这些序列不带租户、操作或设备标签。只有在某个版本正式提供批量变更后，device-management 才会导出这些序列，因此在此之前这些告警保持静默；无需任何操作。
