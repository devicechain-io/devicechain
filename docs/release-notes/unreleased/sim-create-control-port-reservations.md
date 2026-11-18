---
section: whats-new
---

## en

- **Concurrent simulator creation.** `dcctl sim create` now reserves automatic control ports across processes, preventing concurrent creates from selecting the same port. Failed creates release their reservations; abandoned reservations without a saved simulator expire after 24 hours. Explicit `--control-addr` values are unchanged.

## es

- **Creación simultánea de simuladores.** `dcctl sim create` ahora reserva los puertos de control automáticos entre procesos, evitando que las creaciones simultáneas seleccionen el mismo puerto. Las creaciones fallidas liberan sus reservas; las reservas abandonadas sin un simulador guardado caducan tras 24 horas. Los valores explícitos de `--control-addr` no cambian.

## zh-CN

- **并发创建模拟器。** `dcctl sim create` 现在跨进程预留自动分配的控制端口，避免并发创建时选择同一端口。创建失败时会释放预留；没有已保存模拟器的遗留预留会在 24 小时后过期。显式指定的 `--control-addr` 值保持不变。
