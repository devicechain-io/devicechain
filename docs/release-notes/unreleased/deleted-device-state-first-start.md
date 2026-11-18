---
section: before-upgrade
---

## en

- **The first start removes live state left behind by deleted devices.** On its first start after the upgrade, `device-state` removes the live state, latest readings, latest position and dropped-reading records of devices that no longer exist, including devices deleted before the upgrade, and then repeats the check every 10 minutes. See [A deleted device's live state is removed](#v170-deleted-device-state).

## es

- **El primer arranque elimina el estado en vivo que dejaron los dispositivos eliminados.** En su primer arranque tras la actualización, `device-state` elimina el estado en vivo, las últimas lecturas, la última posición y los registros de lecturas descartadas de los dispositivos que ya no existen, incluidos los eliminados antes de la actualización, y después repite la comprobación cada 10 minutos. Consulte [Se elimina el estado en vivo de un dispositivo eliminado](#v170-deleted-device-state).

## zh-CN

- **首次启动会清除已删除设备遗留的实时状态。** 升级后首次启动时，`device-state` 会清除已不存在的设备的实时状态、最新读数、最新位置和被丢弃读数的记录，包括升级前删除的设备，之后每 10 分钟重复一次该检查。参见[已删除设备的实时状态会被清除](#v170-deleted-device-state)。
