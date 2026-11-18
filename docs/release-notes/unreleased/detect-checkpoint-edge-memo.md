---
section: whats-new
---

## en

- **Detection checkpoints carry a falling-edge memo.** The detection engine now records each rule's recent falling edges (for ten minutes past the point it has recorded up to) in its checkpoints, and reads a raise's input time when a checkpoint carries one. Every raise and resolve is decided exactly as in `v1.6`, and a `v1.6` event-processing pod can still restore this release's checkpoints during the rolling upgrade. A later release relies on these fields, so upgrade through `v1.7` rather than skipping it: skipping a minor is not a tested path ([Supported upgrade paths](./versioning-policy.md#upgrade-paths)).

## es

- **Los puntos de control de detección llevan un registro de flancos de bajada.** El motor de detección registra ahora en sus puntos de control los flancos de bajada recientes de cada regla (durante diez minutos después del punto hasta el que ha registrado) y lee la hora de entrada de una activación cuando un punto de control la incluye. Cada activación y resolución se decide exactamente como en `v1.6`, y un pod `v1.6` de event-processing aún puede restaurar los puntos de control de esta versión durante la actualización gradual. Una versión posterior depende de estos campos, así que actualiza pasando por `v1.7` en lugar de saltártela: saltarse una versión menor no es una ruta probada ([Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)).

## zh-CN

- **检测检查点携带下降沿记录。** 检测引擎现在会在检查点中记录每条规则近期的下降沿（保留到其已记录位置之后十分钟），并在检查点带有触发的输入时间时读取该时间。每次触发和解除的判定与 `v1.6` 完全相同，滚动升级期间 `v1.6` 的 event-processing Pod 仍可恢复此版本的检查点。后续版本将依赖这些字段，因此请经由 `v1.7` 升级，不要跳过它：跳过次版本不是经过测试的路径（[支持的升级路径](./versioning-policy.md#upgrade-paths)）。
