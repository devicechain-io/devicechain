---
section: whats-new
---

## en

- **Alarm escalation backstop fixed.** Since 1.7.0, geofence-derived alarms and alarms whose position classification was still pending stopped escalating after an hour because the alarm backstop wrongly treated them as gone. The backstop now checks their lifecycle state, so active alarms continue escalating.

## es

- **Corregida la comprobación de respaldo del escalado de alarmas.** Desde 1.7.0, las alarmas derivadas de geocercas y aquellas cuya clasificación de posición seguía pendiente dejaban de escalar después de una hora porque la comprobación de respaldo las consideraba inexistentes por error. Ahora comprueba su estado de ciclo de vida, por lo que las alarmas activas siguen escalando.

## zh-CN

- **修复告警升级的兜底检查。** 自 1.7.0 起，由地理围栏产生的告警以及位置分类仍待确定的告警，在超过一小时后会停止升级，因为兜底检查错误地将其视为已不存在。现在兜底检查会查询其生命周期状态，确保仍处于活动状态的告警继续升级。
