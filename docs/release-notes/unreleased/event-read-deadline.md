---
section: whats-new
---

## en

- **Event list queries stop after 60 seconds.** `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `stateChangeEvents` and `bucketedMeasurements` reads that do not finish within 60 seconds are now cancelled with an error asking for a narrower time range, instead of holding a database connection until they finish. A scoped caller's read that names an anchor keeps its 15-second bound.

## es

- **Las consultas de listas de eventos se detienen a los 60 segundos.** Las lecturas de `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `stateChangeEvents` y `bucketedMeasurements` que no terminan en 60 segundos ahora se cancelan con un error que pide un intervalo de tiempo más acotado, en lugar de mantener ocupada una conexión a la base de datos hasta que terminen. La lectura de un usuario con ámbito limitado que especifica un ancla conserva su límite de 15 segundos.

## zh-CN

- **事件列表查询在 60 秒后停止。** `events`、`locationEvents`、`measurementEvents`、`alertEvents`、`stateChangeEvents` 和 `bucketedMeasurements` 读取若未在 60 秒内完成，现在会被取消并返回要求缩小时间范围的错误，不再一直占用数据库连接直到完成。作用域受限的调用者指定锚点的读取仍保留 15 秒的时限。
