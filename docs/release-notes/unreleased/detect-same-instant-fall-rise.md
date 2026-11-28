---
section: whats-new
---

## en

- **An alarm that clears and breaches again at the same instant stays active.** When two readings
  of one device shared a timestamp, the first clearing a rule and the second breaching it again,
  the rule raised a fresh alarm at the same instant it resolved the old one. The alarm could end
  cleared while the rule still counted it as raised, and later breaches raised nothing until the
  device recovered. The new raise is now stamped one microsecond after the resolve, so the alarm
  ends active, and a later fall resolves it as usual.

## es

- **Una alarma que se resuelve y vuelve a dispararse en el mismo instante queda activa.** Cuando
  dos lecturas de un dispositivo compartían marca de tiempo, la primera resolviendo una regla y la
  segunda volviendo a incumplirla, la regla levantaba una alarma nueva en el mismo instante en que
  resolvía la anterior. La alarma podía quedar resuelta mientras la regla la seguía contando como
  levantada, y los incumplimientos posteriores no levantaban nada hasta que el dispositivo se
  recuperaba. Ahora la nueva alarma se marca un microsegundo después de la resolución, así que
  queda activa, y una caída posterior la resuelve como siempre.

## zh-CN

- **在同一时刻恢复又再次越限的告警会保持活动状态。** 当同一设备的两条读数时间戳相同、第一条使规则恢复而第二条再次越限时，规则会在恢复旧告警的同一时刻触发一条新告警。告警可能最终处于已清除状态，而规则仍认为它处于触发状态，此后的越限都不会再触发告警，直到设备恢复正常。现在，新触发的时间戳比恢复晚一微秒，因此告警保持活动状态，之后的恢复照常将其清除。
