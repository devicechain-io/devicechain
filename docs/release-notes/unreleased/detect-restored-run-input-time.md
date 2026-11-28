---
section: whats-new
---

## en

- **DETECT checkpoint recovery.** Duration alarms restored without a saved run now use the original raise decision time, avoiding an extra alarm episode after a break when the emitted raise was shifted by one microsecond. Stale-device cleanup also uses that decision time. Older checkpoints without a recorded decision time remain supported.

## es

- **Recuperación de puntos de control de DETECT.** Las alarmas de duración restauradas sin un intervalo guardado ahora usan el instante original de la decisión de activación, evitando un episodio de alarma adicional tras una interrupción cuando la activación emitida se había desplazado un microsegundo. La limpieza del estado de dispositivos antiguos también usa ese instante. Se siguen admitiendo los puntos de control antiguos sin un instante de decisión registrado.

## zh-CN

- **DETECT 检查点恢复。** 恢复未保存持续区间的持续时间告警时，现在使用原始触发决策时间，避免在触发时间戳后移一微秒的情况下，条件中断后又产生一次额外告警。清理旧设备状态时也使用该决策时间。仍支持未记录决策时间的旧检查点。
