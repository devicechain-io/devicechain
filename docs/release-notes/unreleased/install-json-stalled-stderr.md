---
section: whats-new
---

## en

- **Bounded JSON-mode shutdown.** `dcctl install --output json` no longer hangs while draining process output when the original stderr consumer stops reading. Diagnostics received by the reader are preserved through the drain deadline; pending diagnostics may be dropped after that deadline.

## es

- **Cierre acotado en modo JSON.** `dcctl install --output json` ya no se bloquea al vaciar la salida del proceso cuando el consumidor original de stderr deja de leer. Los diagnósticos recibidos por el lector se conservan hasta el límite de tiempo de vaciado; los diagnósticos pendientes pueden descartarse después de ese límite.

## zh-CN

- **JSON 模式可在限定时间内结束。** 当原始 stderr 的使用方停止读取时，`dcctl install --output json` 不再因排空进程输出而挂起。读取器已收到的诊断信息会保留至排空期限；超过期限后，尚未输出的诊断信息可能被丢弃。
