---
section: whats-new
---

## en

- **Two generic warnings cover every maintenance task and processor loop.** `PeriodicTaskStalled` fires when a maintenance task has not succeeded on any replica within three times its declared pass interval plus five minutes, or has never succeeded on a replica that declares an interval. `ProcessorLoopDropping` fires when a processor loop gave up on a message in the last 15 minutes; the policy refusals counted as `unwanted` do not trigger it, and device-state's state loop stays covered by `DeviceStateDroppingEvents`. Both are warnings, and a new loop is covered without a rule of its own. During a rolling upgrade, pods still on the previous version count a disabled device's traffic as `dropped`, so `ProcessorLoopDropping` can fire for up to about 15 minutes on an instance where a disabled device is still transmitting.

## es

- **Dos advertencias genéricas cubren todas las tareas de mantenimiento y todos los bucles de procesamiento.** `PeriodicTaskStalled` se dispara cuando una tarea de mantenimiento no ha tenido éxito en ninguna réplica en tres veces su intervalo de pasada declarado más cinco minutos, o nunca ha tenido éxito en una réplica que declara un intervalo. `ProcessorLoopDropping` se dispara cuando un bucle de procesamiento renunció a un mensaje en los últimos 15 minutos; las denegaciones por política contadas como `unwanted` no la activan, y el bucle de estado de device-state sigue cubierto por `DeviceStateDroppingEvents`. Ambas son advertencias, y un bucle nuevo queda cubierto sin una regla propia. Durante una actualización progresiva, los pods que aún ejecutan la versión anterior cuentan el tráfico de un dispositivo deshabilitado como `dropped`, así que `ProcessorLoopDropping` puede dispararse hasta unos 15 minutos en una instancia donde un dispositivo deshabilitado sigue transmitiendo.

## zh-CN

- **两条通用警告覆盖所有维护任务和处理循环。** 当某个维护任务在任何副本上都没有在其声明的执行间隔的三倍加五分钟内成功过，或在声明了间隔的副本上从未成功过时，会触发 `PeriodicTaskStalled`。当某个处理循环在过去 15 分钟内放弃了某条消息时，会触发 `ProcessorLoopDropping`；计为 `unwanted` 的策略拒绝不会触发它，device-state 的状态循环仍由 `DeviceStateDroppingEvents` 覆盖。两者都是警告级别，新增的循环无需单独规则即可被覆盖。滚动升级期间，仍在运行旧版本的 Pod 会把已禁用设备的流量计为 `dropped`，因此在仍有已禁用设备持续发送数据的实例上，`ProcessorLoopDropping` 最多可能触发约 15 分钟。
