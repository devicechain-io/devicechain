---
section: whats-new
---

## en

- **The bundled boards break ingest shedding down by priority class.** The Platform board now shows the messages and readings turned away at a tenant's ingest ceiling by priority class (`shed_class`: `best-effort`, `bronze`, `silver` or `gold`) as well as its totals by source, and the Ingest board adds the per-class series for messages shed at the readings ceiling. Lower-priority traffic is shed first, so the breakdown shows how important the shed traffic was.

## es

- **Los paneles incluidos desglosan el descarte de ingesta por clase de prioridad.** El panel Platform muestra ahora los mensajes y lecturas rechazados en el límite de ingesta de un inquilino por clase de prioridad (`shed_class`: `best-effort`, `bronze`, `silver` o `gold`), además de sus totales por origen, y el panel Ingest añade las series por clase de los mensajes descartados en el límite de lecturas. El tráfico de menor prioridad se descarta primero, así que el desglose muestra la importancia del tráfico descartado.

## zh-CN

- **内置面板按优先级类别细分摄取丢弃。** Platform 面板现在除了按来源显示总数外，还按优先级类别（`shed_class`：`best-effort`、`bronze`、`silver` 或 `gold`）显示在租户摄取上限处被拒绝的消息和读数；Ingest 面板新增了在读数上限处被丢弃消息的按类别序列。低优先级流量会先被丢弃，因此该细分可以显示被丢弃流量的重要程度。
