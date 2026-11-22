---
section: whats-new
---

## en

- **Device configuration report history.** The service now keeps a 200-day history of accepted device configuration verdicts internally; this history is not yet served through the API. The new `device_configuration_reports_pruned_total{reason="orphan|retention"}` metric counts rows removed by maintenance.

## es

- **Historial de informes de configuración de dispositivos.** El servicio conserva internamente un historial de 200 días de los resultados de configuración de dispositivos aceptados; este historial todavía no está disponible en la API. La nueva métrica `device_configuration_reports_pruned_total{reason="orphan|retention"}` cuenta las filas eliminadas por el mantenimiento.

## zh-CN

- **设备配置报告历史。** 服务现在在内部保留 200 天的已接受设备配置结果历史；该历史尚未通过 API 提供。新增指标 `device_configuration_reports_pruned_total{reason="orphan|retention"}` 统计维护过程中删除的行数。
