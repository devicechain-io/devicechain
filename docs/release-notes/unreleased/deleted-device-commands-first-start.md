---
section: before-upgrade
---

## en

- **The first start cancels commands left behind by deleted devices.** On its first start after the upgrade, `command-delivery` reads the deletions still retained on the message broker and cancels the queued, held or parked commands of those devices, including deletions from before the upgrade. See [A deleted device's undelivered commands are cancelled](#v170-deleted-device-commands).

## es

- **El primer arranque cancela los comandos que dejaron los dispositivos eliminados.** En su primer arranque tras la actualización, `command-delivery` lee las eliminaciones que el broker de mensajes aún conserva y cancela los comandos en cola, retenidos o aparcados de esos dispositivos, incluidas las eliminaciones anteriores a la actualización. Consulte [Se cancelan los comandos pendientes de un dispositivo eliminado](#v170-deleted-device-commands).

## zh-CN

- **首次启动会取消已删除设备遗留的命令。** 升级后首次启动时，`command-delivery` 会读取消息代理仍保留的删除记录，并取消这些设备处于排队、保持或暂存状态的命令，包括升级前的删除。参见[已删除设备的未送达命令会被取消](#v170-deleted-device-commands)。
