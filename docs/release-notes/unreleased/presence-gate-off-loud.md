---
section: whats-new
---

## en

- **A presence gate switched off by missing configuration now stays visible.** When command-delivery starts without the service secret or the device-state endpoint, it still dispatches every command without a presence check, as before, but its startup warning now names each missing key (`infrastructure.serviceAuth.secret`, `infrastructure.deviceState.hostname`, `infrastructure.deviceState.port`), the warning repeats at most once an hour while commands keep going out unchecked, and the new `command_delivery_presence_gate_off_dispatches_total` counter records how many did. Nothing changes for an instance whose configuration has both: the counter stays at zero. If the `SafetyGateDisabled` alert is firing for the presence gate, set the keys the warning names and restart command-delivery.

## es

- **Una puerta de presencia desactivada por falta de configuración ahora sigue siendo visible.** Cuando command-delivery arranca sin el secreto de servicio o sin el endpoint de device-state, sigue despachando todos los comandos sin comprobar la presencia, como antes, pero su aviso de arranque ahora nombra cada clave que falta (`infrastructure.serviceAuth.secret`, `infrastructure.deviceState.hostname`, `infrastructure.deviceState.port`), el aviso se repite como máximo una vez por hora mientras los comandos sigan saliendo sin comprobación, y el nuevo contador `command_delivery_presence_gate_off_dispatches_total` registra cuántos salieron. Nada cambia en una instancia cuya configuración tiene ambos: el contador se queda en cero. Si la alerta `SafetyGateDisabled` está activa para la puerta de presencia, configure las claves que nombra el aviso y reinicie command-delivery.

## zh-CN

- **因缺少配置而关闭的在线状态门现在会持续可见。** 当 command-delivery 在缺少服务密钥或 device-state 端点的情况下启动时，它仍会像以前一样不经在线状态检查就分发所有指令，但启动警告现在会点名每个缺失的键（`infrastructure.serviceAuth.secret`、`infrastructure.deviceState.hostname`、`infrastructure.deviceState.port`），在指令持续未经检查地发出期间，该警告每小时最多重复一次，新增的 `command_delivery_presence_gate_off_dispatches_total` 计数器则记录发出了多少条。配置中两者齐全的实例没有任何变化：该计数器保持为零。如果在线状态门的 `SafetyGateDisabled` 告警正在触发，请设置警告中点名的键并重启 command-delivery。
