---
section: whats-new
---

## en

#### Publishing threshold rules keeps existing alarms {#v170-publish-keeps-alarms}

- Publishing a profile keeps a threshold rule's raised alarm when the rule's token and condition type are unchanged. This carry, together with the existing absence-rule handoff, avoids re-sending commands, webhook calls and connector publishes while a device stays in breach.
- Changes to actions or severity apply from the next rising edge for these rules.
- Other rule kinds, including repeating and correlation rules, may still repeat their actions on a publish while a device stays in breach.
- While an old event-processing pod still holds the detection lease during a rolling upgrade, no carry happens, which is the previous behaviour.
- No configuration change is needed.

## es

#### Publicar reglas de umbral conserva las alarmas existentes {#v170-publish-keeps-alarms}

- Publicar un perfil conserva la alarma activa de una regla de umbral cuando el token y el tipo de condición de la regla no cambian. Este traspaso, junto con el traspaso existente de las reglas de ausencia, evita volver a enviar comandos, llamadas webhook y publicaciones a conectores mientras un dispositivo siga incumpliendo la condición.
- Los cambios de acciones o gravedad se aplican desde el siguiente flanco de subida de estas reglas.
- Los demás tipos de regla, incluidas las de repetición y correlación, pueden seguir repitiendo sus acciones al publicar mientras el dispositivo siga incumpliendo la condición.
- Mientras un pod antiguo de event-processing siga manteniendo la concesión de detección durante una actualización gradual, no se transfiere el estado de alarma, como ocurría antes.
- No requiere cambios de configuración.

## zh-CN

#### 发布阈值规则会保留现有报警 {#v170-publish-keeps-alarms}

- 发布配置文件时，如果阈值规则的令牌和条件类型保持不变，就会保留该规则已触发的报警。此状态转移与现有的缺报规则交接行为一起，可避免在设备持续超限时重新发送命令、Webhook 请求和连接器消息。
- 对于这些规则，动作或严重程度的变更从下一次进入报警状态时生效。
- 其他规则类型（包括重复和关联规则）在设备持续超限时，发布仍可能导致动作重复执行。
- 在滚动升级期间，只要旧版 event-processing Pod 仍持有检测租约，就不会转移告警状态，行为与之前相同。
- 无需更改配置。
