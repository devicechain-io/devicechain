---
section: whats-new
---

## en

#### A deleted device's undelivered commands are cancelled {#v170-deleted-device-commands}

- When a device is deleted, `command-delivery` cancels the commands it still held for it (queued, held or parked) and refuses to send any command created before the delete, including one already handed to a transport and not yet confirmed. A device later created with the same token does not receive them; commands created for it after the delete are sent as usual.
- On first start after the upgrade, `command-delivery` also reads the deletions still retained on the message broker, so commands left behind by devices deleted in the last few days are cancelled the same way.
- While old and new pods overlap during the rolling upgrade, a `v1.6` pod can still send such a command until it is replaced.

## es

#### Se cancelan los comandos pendientes de un dispositivo eliminado {#v170-deleted-device-commands}

- Al eliminar un dispositivo, `command-delivery` cancela los comandos que aún tenía para él (en cola, retenidos o aparcados) y se niega a enviar cualquier comando creado antes de la eliminación, incluido uno ya entregado a un transporte y aún no confirmado. Un dispositivo creado después con el mismo token no los recibe; los comandos creados para él tras la eliminación se envían con normalidad.
- En el primer arranque tras la actualización, `command-delivery` también lee las eliminaciones que el broker de mensajes aún conserva, de modo que los comandos que dejaron los dispositivos eliminados en los últimos días se cancelan de la misma forma.
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` aún puede enviar uno de esos comandos hasta que se sustituye.

## zh-CN

#### 已删除设备的未送达命令会被取消 {#v170-deleted-device-commands}

- 删除设备时，`command-delivery` 会取消仍为该设备保留的命令（排队、保持或暂存状态），并拒绝发送删除前创建的任何命令，包括已交给传输通道但尚未确认的命令。之后以相同令牌创建的设备不会收到这些命令；删除后为新设备创建的命令照常发送。
- 升级后首次启动时，`command-delivery` 还会读取消息代理仍保留的删除记录，因此最近几天内删除的设备遗留的命令也会以同样方式取消。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` Pod 在被替换前仍可能发送此类命令。
