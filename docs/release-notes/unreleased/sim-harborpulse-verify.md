---
section: whats-new
---

## en

- **The waterfront scenario checks itself.** A `harborpulse` sim now publishes an operator board and a read-only board for the Quayside Lofts owner, reports where it is in its compressed day on its `/status`, and takes `--day-length` to stretch the day. The new `scenario-verify` tool watches one day of a running `harborpulse` sim and reads the platform back to confirm that each flood alarm escalates and clears, that the pump and charger commands are answered and change the readings, that the air-locked pump and the feeder outage are detected for exactly the right devices, that the quay correlation fires, and that buffered readings are stored at their original times. It exits 0 when all of that holds, 1 when something fails and 2 when it cannot decide.

## es

- **El escenario portuario se comprueba a sí mismo.** Una simulación `harborpulse` publica ahora un panel de operador y un panel de solo lectura para el propietario de Quayside Lofts, informa en su `/status` de en qué punto de su día comprimido se encuentra y acepta `--day-length` para alargar el día. La nueva herramienta `scenario-verify` observa un día de una simulación `harborpulse` en marcha y relee la plataforma para confirmar que cada alarma de inundación escala y se borra, que los comandos de la bomba y del cargador reciben respuesta y cambian las lecturas, que la bomba con aire y el corte de alimentación se detectan exactamente en los dispositivos correctos, que la correlación del muelle se dispara y que las lecturas almacenadas se guardan con su hora original. Sale con 0 cuando todo se cumple, con 1 cuando algo falla y con 2 cuando no puede decidir.

## zh-CN

- **港口场景可以自我检查。** `harborpulse` 模拟现在会发布一个运维看板和一个供 Quayside Lofts 业主使用的只读看板，在其 `/status` 中报告压缩的一天进行到何处，并接受 `--day-length` 以延长这一天。新的 `scenario-verify` 工具会观察正在运行的 `harborpulse` 模拟中的一天，并从平台读回数据，以确认每个洪水告警都会升级并清除、水泵和充电桩的命令得到应答并改变读数、气堵的水泵和馈线停电恰好在正确的设备上被检测到、码头关联规则会触发，以及缓存的读数按其原始时间存储。全部成立时退出码为 0，有失败时为 1，无法判定时为 2。
