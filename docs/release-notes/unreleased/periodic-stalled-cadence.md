---
section: whats-new
---

## en

- **`PeriodicTaskStalled` no longer fires on a healthy task that completes less often than it polls.** The rollup tail sweep wakes every five minutes but deliberately finishes a sweep only about once an hour, so the alert read a healthy task as stalled roughly thirty minutes after each success. A task of that kind now declares the cadence it expects between successes as `<task>_expected_success_interval_seconds`, and the alert measures against the larger of that and the polling interval. The alert name, severity and ten-minute hold are unchanged, and a task that declares no such interval is judged exactly as before.

## es

- **`PeriodicTaskStalled` ya no se dispara con una tarea sana que completa pasadas con menos frecuencia de la que sondea.** El barrido de la cola de agregados se activa cada cinco minutos pero deliberadamente termina un barrido solo aproximadamente una vez por hora, por lo que la alerta tomaba una tarea sana por detenida unos treinta minutos después de cada éxito. Una tarea de ese tipo ahora declara la cadencia que espera entre éxitos como `<task>_expected_success_interval_seconds`, y la alerta mide contra el mayor entre ese valor y el intervalo de sondeo. El nombre de la alerta, su severidad y la espera de diez minutos no cambian, y una tarea que no declara ese intervalo se evalúa exactamente igual que antes.

## zh-CN

- **`PeriodicTaskStalled` 不再对完成频率低于轮询频率的健康任务误报。** 汇总尾部清扫任务每五分钟唤醒一次，但有意只大约每小时完成一次清扫，因此该告警会在每次成功约三十分钟后把这个健康任务误判为停滞。此类任务现在会以 `<task>_expected_success_interval_seconds` 声明两次成功之间预期的间隔，告警以该值与轮询间隔中的较大者为准。告警名称、严重级别和十分钟的持续时间均不变，未声明该间隔的任务的判定方式与之前完全相同。
