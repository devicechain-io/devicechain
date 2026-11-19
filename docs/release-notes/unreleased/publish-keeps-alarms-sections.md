---
section: whats-new
---

## en

#### Publishing threshold rules keeps existing alarms {#v170-publish-keeps-alarms}

Publishing a profile preserves an already-raised threshold rule when its rule token and condition type continue. This carry, together with the existing absence-rule handoff, avoids re-sending commands, webhook calls and connector publishes while a device stays in breach. Changes to actions or severity apply from the next rising edge for these rules. Other rule kinds, including repeating and correlation rules, may still repeat their actions on a publish while a device stays in breach.

Carry is limited to one observed transition from the immediately previous active version. A removed, disabled or differently kinded rule in an intermediate version breaks continuity. Every transition, including rollback, clears historical threshold and absence latches in the destination before carrying from the previous version. Only devices currently rostered in the profile can carry state. Startup restores checkpointed latches without carrying historical latches into the active version. A crash before the handoff is checkpointed can repeat one action on the successor's next breach; delivery remains at least once. A successor reading cancels a carry still waiting for old output to publish, which can also repeat an action. Include these cases when testing action delivery.

While an old event-processing pod still holds the detection lease during a rolling upgrade, no carry happens, which is the previous behaviour.

- **Nothing needs doing.**

## es

#### Publicar reglas de umbral conserva las alarmas existentes {#v170-publish-keeps-alarms}

Publicar un perfil conserva el estado de una regla de umbral que ya está en alarma si mantiene su token y su tipo de condición. Este traspaso, junto con el traspaso existente de las reglas de ausencia, evita volver a enviar comandos, llamadas webhook y publicaciones a conectores mientras un dispositivo siga incumpliendo la condición. Los cambios de acciones o gravedad se aplican desde el siguiente flanco de subida de estas reglas. Los demás tipos de regla, incluidas las de repetición y correlación, pueden seguir repitiendo sus acciones al publicar mientras el dispositivo siga incumpliendo la condición.

El traspaso solo abarca una transición observada desde la versión activa inmediatamente anterior. Una regla eliminada, deshabilitada o de otro tipo en una versión intermedia rompe la continuidad. Cada transición, incluida una reversión, borra los estados históricos de umbral y ausencia en la versión de destino antes de transferir los de la versión anterior. Solo se transfiere el estado de los dispositivos que pertenecen actualmente al perfil. Al iniciar se restauran los estados guardados sin transferir estados históricos a la versión activa. Un fallo antes de guardar el traspaso puede repetir una acción en el siguiente incumplimiento de la nueva versión; la entrega sigue siendo al menos una vez. Una lectura de la nueva versión cancela un traspaso pendiente de publicación, lo que también puede repetir una acción. Incluye estos casos al probar la entrega de acciones.

Mientras un pod antiguo de event-processing siga manteniendo la concesión de detección durante una actualización gradual, no se transfiere el estado de alarma, como ocurría antes.

- **No hay que hacer nada.**

## zh-CN

#### 发布阈值规则会保留现有报警 {#v170-publish-keeps-alarms}

发布配置文件时，如果阈值规则的规则令牌和条件类型保持不变，就会保留已触发的报警状态。此状态转移与现有的缺报规则交接行为一起，可避免在设备持续超限时重新发送命令、Webhook 请求和连接器消息。对于这些规则，动作或严重程度的变更从下一次进入报警状态时生效。其他规则类型（包括重复和关联规则）在设备持续超限时，发布仍可能导致动作重复执行。

状态转移仅限于一次已观察到的活动版本切换，来源必须是紧接之前的活动版本。中间版本删除、禁用规则或更改条件类型都会中断连续性。每次切换（包括回滚）都会先清除目标版本中历史遗留的阈值和缺报报警状态，再从前一个版本转移状态。只有当前属于该配置文件的设备才能转移状态。启动时只恢复检查点中的状态，不会将历史状态转移到活动版本。若在保存交接检查点之前崩溃，新版本下一次超限时可能重复执行一次动作；交付仍采用至少一次语义。新版本的读数会取消仍在等待旧输出发布的交接，因此也可能导致动作重复执行。测试动作交付时应包含这些情况。

在滚动升级期间，只要旧版 event-processing Pod 仍持有检测租约，就不会转移告警状态，行为与之前相同。

- **无需操作。**
