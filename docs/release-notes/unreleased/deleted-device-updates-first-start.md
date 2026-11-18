---
section: before-upgrade
---

## en

- **The first start ends updates assigned to deleted devices.** On its first start after the upgrade, `update-management` reads the deletions still retained on the message broker and ends the pending update assignments of those devices, including deletions from before the upgrade. Their rollout targets show as cancelled. See [An update assigned to a deleted device is ended](#v170-deleted-device-updates).

## es

- **El primer arranque termina las actualizaciones asignadas a dispositivos eliminados.** En su primer arranque tras la actualización, `update-management` lee las eliminaciones que el broker de mensajes aún conserva y termina las asignaciones de actualización pendientes de esos dispositivos, incluidas las eliminaciones anteriores a la actualización. Sus objetivos de despliegue se muestran como cancelados. Consulte [Se terminan las actualizaciones asignadas a un dispositivo eliminado](#v170-deleted-device-updates).

## zh-CN

- **首次启动会终止分配给已删除设备的更新。** 升级后首次启动时，`update-management` 会读取消息代理仍保留的删除记录，并终止这些设备的待处理更新分配，包括升级前的删除。相应的发布目标显示为已取消。参见[分配给已删除设备的更新会被终止](#v170-deleted-device-updates)。
