---
section: whats-new
---

## en

- **Staged OTA rollouts.** Optionally declare named canaries followed by cumulative fleet percentages. Each cohort waits for every preceding device to confirm success; manual promotion is available. An optional failure-rate policy pauses new assignments when failed, timed-out or unknown outcomes exceed a percentage over a trailing window with a minimum sample. The pause reason, numbered halt and sample counts remain visible; explicit resume overrides retain the evidence. Existing rollouts keep their defaults and concurrency limits. Enable these new policies after all update-management replicas have upgraded.

## es

- **Despliegues OTA por cohortes.** Puedes definir un grupo canario con nombre seguido de porcentajes acumulativos de la flota. Cada cohorte espera a que todos los dispositivos anteriores confirmen el éxito; también puedes promoverla manualmente. Una política opcional pausa las nuevas asignaciones cuando los resultados fallidos, agotados o desconocidos superan un porcentaje durante una ventana de observación con una muestra mínima. El motivo, el número de parada y los recuentos siguen visibles; la reanudación explícita conserva las pruebas. Los despliegues existentes mantienen sus valores predeterminados y límites de concurrencia. Activa estas políticas después de actualizar todas las réplicas de update-management.

## zh-CN

- **分阶段 OTA 发布。** 可选择先指定具名金丝雀设备，再按累计设备百分比分阶段发布。每个阶段都等待前面所有设备确认成功，并支持手动推进。可选的失败率策略在观察窗口内、达到最小样本数后，因失败、超时或未知结果的比例超过阈值而暂停新分配。暂停原因、暂停序号和样本计数均可查询；显式恢复会保留相关证据。现有发布的默认行为及并发限制保持不变。请在所有 update-management 副本升级后启用这些新策略。
