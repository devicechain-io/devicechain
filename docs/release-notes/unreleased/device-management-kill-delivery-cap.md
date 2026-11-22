---
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
section: whats-new
---

## en

- **Agent kills stay fail-closed when delivery retries run out.** If device-management cannot apply a kill on its final delivery, it writes an `unprocessable` dead letter and blocks agent alarm executions for every tenant in that process until restart. After storage recovers, repeat the kill and then restart the affected device-management processes; successful kills and healthy broker polls do not clear this latch.

## es

- **Las órdenes de parada de agentes mantienen el bloqueo al agotar los reintentos de entrega.** Si device-management no puede aplicar una orden de parada en su última entrega, genera un mensaje fallido con motivo `unprocessable` y bloquea las ejecuciones de alarmas de agentes de todos los inquilinos de ese proceso hasta su reinicio. Una vez recuperado el almacenamiento, repita la orden de parada y reinicie los procesos afectados de device-management; las órdenes aplicadas correctamente y las consultas satisfactorias al broker no eliminan este bloqueo.

## zh-CN

- **代理停止指令耗尽投递重试次数后仍保持执行阻断。** 如果 device-management 在最后一次投递时仍无法应用停止指令，它会写入原因为 `unprocessable` 的死信，并阻止该进程内所有租户的代理告警执行，直到进程重启。存储恢复后，请重新发出停止指令，然后重启受影响的 device-management 进程；成功应用后续停止指令或正常轮询消息代理均不会解除此锁定。
