---
section: whats-new
---

## en

- **Automation command safeguards.** REACT `sendCommand` now shares the tenant's outbound rate ceiling with `httpCall` and `publish` (platform default: 100 actions/second, burst 200; existing tenant overrides apply). A logical rule may attempt at most 120 commands to the same device in a one-minute window. These in-memory safeguards reset on restart or failover; a brief lease handover overlap can admit both replicas' allowances. Startup replay does not send commands or spend their allowances, so historical detections reconstructed during recovery no longer actuate devices. Preview and evaluation remain isolated. Inspect refusals with `dcctl dead-letters` (reason `shed`, detail codes `command_rate_limited`, `command_loop`, or `command_guard_full`) and `react_command_refused_total` with `result="unwanted"`. Refusals over the existing dead-letter budget are counted and summarized once per tenant per minute.

## es

- **Protección de los comandos de automatización.** REACT `sendCommand` ahora comparte el límite de salida del tenant con `httpCall` y `publish` (valor predeterminado de la plataforma: 100 acciones/segundo y ráfaga de 200; se aplican los límites existentes del tenant). Una regla lógica puede intentar como máximo 120 comandos al mismo dispositivo en una ventana de un minuto. Estas protecciones en memoria se reinician al reiniciar el servicio o cambiar de réplica; un breve solapamiento al transferir la concesión puede admitir las cuotas de ambas réplicas. La reproducción al iniciar no envía comandos ni consume sus cuotas, por lo que las detecciones históricas reconstruidas durante la recuperación ya no accionan dispositivos. La vista previa y la evaluación siguen aisladas. Consulte los rechazos con `dcctl dead-letters` (motivo `shed`, códigos de detalle `command_rate_limited`, `command_loop` o `command_guard_full`) y `react_command_refused_total` con `result="unwanted"`. Los rechazos que superan el presupuesto existente de mensajes fallidos se cuentan y resumen una vez por tenant por minuto.

## zh-CN

- **自动化命令防护。** REACT `sendCommand` 现在与 `httpCall` 和 `publish` 共用租户的出站速率上限（平台默认每秒 100 个操作，突发额度 200；现有租户自定义上限仍然适用）。同一逻辑规则在一分钟窗口内最多向同一设备尝试发送 120 条命令。这些内存防护在重启或故障转移时重置；租约交接期间的短暂重叠可能允许两个副本各自使用额度。启动重放不会发送命令或消耗命令额度，因此恢复期间重建的历史检测不再驱动设备。预览和评估继续保持隔离。可使用 `dcctl dead-letters` 查看拒绝记录（原因为 `shed`，详细代码为 `command_rate_limited`、`command_loop` 或 `command_guard_full`），并查看带有 `result="unwanted"` 的 `react_command_refused_total` 指标。超出现有死信预算的拒绝会被计数，并按租户每分钟汇总一次。
