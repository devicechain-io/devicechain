---
section: whats-new
---

## en

- **A backlog of rule commands is checked against the per-device loop limit on the time it happened.** When the detection engine catches up after an outage, the commands its rules send are now counted against the limit of 120 commands per rule and device in a minute, and against each tenant's share of the loop guard, by when the events that triggered them reached the platform rather than when the backlog is sent. Minutes of normal traffic sent in a few seconds are no longer refused with `command_loop` or `command_guard_full`, while a rule that really loops through its device is still cut off. While a backlog and live traffic are sent together, the backlog is counted at the current time, as before.

## es

- **Una acumulación de comandos de reglas se compara con el límite de bucle por dispositivo según el momento en que ocurrió.** Cuando el motor de detección se pone al día tras una interrupción, los comandos que envían sus reglas se cuentan ahora contra el límite de 120 comandos por regla y dispositivo en un minuto, y contra la parte de la protección contra bucles de cada inquilino, según el momento en que los eventos que los dispararon llegaron a la plataforma y no según el momento en que se envía la acumulación. Varios minutos de tráfico normal enviados en pocos segundos ya no se rechazan con `command_loop` ni con `command_guard_full`, y una regla que de verdad entra en bucle a través de su dispositivo se sigue cortando. Mientras se envían juntos una acumulación y tráfico en vivo, la acumulación se cuenta en la hora actual, como antes.

## zh-CN

- **规则命令的积压现在按其发生时间对照每设备循环限制。** 检测引擎在故障后追赶进度时，其规则发送的命令现在按触发它们的事件到达平台的时间，而不是积压被发送的时间，计入每条规则每台设备每分钟 120 条命令的限制以及各租户在循环防护中的份额。几分钟的正常流量在几秒内发出时，不再以 `command_loop` 或 `command_guard_full` 被拒绝，而真正通过其设备不断循环的规则仍会被切断。积压与实时流量同时发送时，积压仍按当前时间计数，与以前相同。
