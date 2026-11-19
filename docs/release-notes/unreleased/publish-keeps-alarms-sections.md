---
section: whats-new
---

## en

#### Publishing threshold rules keeps existing alarms {#v170-publish-keeps-alarms}

Publishing can preserve an already-raised **threshold rule** when its rule token, condition type and alarm actions remain compatible. The absence-rule handoff uses the same alarm-action check. For a carried episode, publishing does not re-send commands, webhook calls or connector publishes while a device stays in breach; changes to non-alarm actions or severity apply from its next rising edge. Recovery through the successor clears its alarm keys, and a later breach fires its actions. Other rule kinds, including repeating and correlation rules, may repeat actions on a publish. A new rule token or condition type starts independently.

Carry is limited to one observed transition from the immediately previous active version. A removed, disabled or differently kinded rule in an intermediate version breaks continuity. Every transition, including rollback, clears historical threshold and absence latches in the destination before carrying from the previous version. Threshold carry requires uninterrupted membership: deletion, re-typing or a new membership start time cancels deferred carry and clears the previous membership's threshold latches without emitting a recovery.

A reading under a registered, non-active version prevents a later carry into that version for that device. A successor reading also cancels a carry waiting for output publication. An acknowledged old-version threshold recovery, live or replayed, clears the paired active latch so the next breach can raise; a dropped recovery does not clear it. Startup restores checkpointed latches without carrying historical latches into the active version. A crash before the handoff is checkpointed, or uncertainty about continuity, can repeat actions; delivery remains at least once.

Alarm-action compatibility requires the same set of alarm keys, key-template expressions and raise guards. Adding or removing an alarm action, or changing its key, template or guard, prevents carry. Severity and non-alarm action edits may still carry. Changing or removing alarm keys does not migrate existing alarms or guarantee recovery under their previous keys; resolve those alarms separately. Test the first breach after an alarm-action edit, and recovery followed by a fresh breach for a carried episode.

While an old event-processing pod still holds the detection lease during a rolling upgrade, no carry happens, which is the previous behaviour.

- **No upgrade action is required.**

## es

#### Publicar reglas de umbral conserva las alarmas existentes {#v170-publish-keeps-alarms}

Al publicar se puede conservar el estado de una **regla de umbral** que ya está en alarma si mantiene su token, su tipo de condición y acciones de alarma compatibles. El traspaso de ausencia aplica la misma comprobación de acciones de alarma. En un episodio transferido, publicar no vuelve a enviar comandos, llamadas webhook ni publicaciones a conectores mientras persista el incumplimiento; los cambios de acciones que no sean de alarma o de gravedad se aplican desde el siguiente flanco de subida. La recuperación de la nueva versión despeja sus claves de alarma y un incumplimiento posterior ejecuta sus acciones. Otros tipos de regla, incluidas repetición y correlación, pueden repetir acciones al publicar. Un token o tipo de condición nuevo comienza de forma independiente.

El traspaso solo abarca una transición observada desde la versión activa inmediatamente anterior. Una regla eliminada, deshabilitada o de otro tipo en una versión intermedia rompe la continuidad. Cada transición, incluida una reversión, borra los estados históricos de umbral y ausencia en la versión de destino antes de transferir los de la versión anterior. El traspaso de umbral exige una pertenencia ininterrumpida: eliminar el dispositivo, cambiar su perfil o iniciar una nueva pertenencia cancela el traspaso pendiente y borra los estados de umbral de la pertenencia anterior sin emitir una recuperación.

Una lectura de una versión registrada pero no activa impide un traspaso posterior a esa versión para ese dispositivo. Una lectura de la nueva versión también cancela un traspaso pendiente de publicación. Una recuperación de umbral de una versión anterior, en vivo o reproducida, borra el estado activo emparejado solo cuando su publicación se confirma; el siguiente incumplimiento puede volver a disparar. Una recuperación descartada no lo borra. Al iniciar se restauran los estados guardados sin transferir estados históricos a la versión activa. Un fallo antes de guardar el traspaso, o la incertidumbre sobre la continuidad, puede repetir acciones; la entrega sigue siendo al menos una vez.

La compatibilidad de acciones de alarma exige el mismo conjunto de claves de alarma, expresiones de plantilla de clave y condiciones de ejecución de subida. Añadir o quitar una acción de alarma, o cambiar su clave, plantilla o condición, impide el traspaso. Los cambios de gravedad y de acciones que no sean de alarma pueden conservarlo. Cambiar o quitar claves no migra las alarmas existentes ni garantiza su recuperación con las claves anteriores; resuelve esas alarmas por separado. Prueba el primer incumplimiento tras editar una acción de alarma y, para un episodio transferido, una recuperación seguida de un nuevo incumplimiento.

Mientras un pod antiguo de event-processing siga manteniendo la concesión de detección durante una actualización gradual, no se transfiere el estado de alarma, como ocurría antes.

- **La actualización no requiere ninguna acción.**

## zh-CN

#### 发布阈值规则会保留现有报警 {#v170-publish-keeps-alarms}

发布时，如果**阈值规则**的令牌、条件类型及报警动作保持兼容，就可以保留已经触发的状态。缺报规则交接也使用相同的报警动作兼容性检查。对于已转移的状态，设备持续超限时，发布不会重复发送命令、Webhook 请求或连接器消息；非报警动作或严重程度的变更从下一次触发时生效。新版本的恢复事件清除其报警键，之后再次超限会执行其动作。其他类型（包括重复和关联规则）在发布时仍可能重复执行动作。新的规则令牌或条件类型会独立开始运行。

状态转移仅限于一次已观察到的活动版本切换，来源必须是紧接之前的活动版本。中间版本删除、禁用规则或更改条件类型都会中断连续性。每次切换（包括回滚）都会先清除目标版本中历史遗留的阈值和缺报报警状态，再从前一个版本转移状态。阈值状态转移要求设备的成员关系持续不变：删除设备、更换配置文件或更新成员关系开始时间，都会取消待完成的转移，并清除上一段成员关系的阈值状态，不发送恢复事件。

设备在已注册但尚未活动的版本下产生读数后，后续切换到该版本时不会为该设备转移状态。新版本的读数也会取消等待输出发布的转移。旧版本的阈值恢复事件无论来自实时处理还是重放，都只在发布确认后清除配对的活动状态，使下一次超限能够再次触发；被丢弃的恢复事件不会清除它。启动时只恢复检查点中的状态，不会将历史状态转移到活动版本。若在保存交接检查点之前崩溃，或无法确认连续性，动作可能重复执行；交付仍采用至少一次语义。

报警动作兼容要求报警键、键模板表达式和触发动作的上升沿条件集合相同。添加或删除报警动作，或更改其键、模板或条件，都会阻止状态转移。严重程度和非报警动作的修改仍可保留转移。更改或删除报警键不会迁移已有报警，也不保证用旧键清除报警；请另行处理这些报警。修改报警动作后应测试首次超限；对于已转移的状态，应测试恢复正常后再次超限。

在滚动升级期间，只要旧版 event-processing Pod 仍持有检测租约，就不会转移告警状态，行为与之前相同。

- **升级无需额外操作。**
