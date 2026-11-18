---
section: whats-new
---

## en

#### A deleted device's live state is removed {#v170-deleted-device-state}

- When a device is deleted, `device-state` removes its live state, latest readings, latest position and dropped-reading records. A device later created with the same token starts with none of them, so members of its new owner do not see the old device's last position or readings.
- `device-state` now also asks `device-management` every 10 minutes which devices still exist, using the instance's service credentials, and removes live state left behind by devices deleted earlier, including before the upgrade. It never removes anything when that lookup fails. Without the service secret or the `device-management` and `user-management` endpoints configured, the check is skipped and a warning is logged at startup.
- A reading the platform accepted before the delete but processed after it can still re-create the deleted device's live state for a short time; the periodic check removes it if no device holds the token.

## es

#### Se elimina el estado en vivo de un dispositivo eliminado {#v170-deleted-device-state}

- Al eliminar un dispositivo, `device-state` elimina su estado en vivo, sus últimas lecturas, su última posición y sus registros de lecturas descartadas. Un dispositivo creado después con el mismo token no hereda nada de eso, de modo que los miembros de su nuevo propietario no ven la última posición ni las lecturas del dispositivo anterior.
- `device-state` también pregunta ahora a `device-management` cada 10 minutos qué dispositivos siguen existiendo, con las credenciales de servicio de la instancia, y elimina el estado en vivo que dejaron dispositivos eliminados antes, incluso antes de la actualización. Nunca elimina nada si esa consulta falla. Si el secreto de servicio o los endpoints de `device-management` y `user-management` no están configurados, la comprobación se omite y se registra una advertencia al arrancar.
- Una lectura que la plataforma aceptó antes de la eliminación pero procesó después aún puede recrear brevemente el estado en vivo del dispositivo eliminado; la comprobación periódica lo elimina si ningún dispositivo tiene ese token.

## zh-CN

#### 已删除设备的实时状态会被清除 {#v170-deleted-device-state}

- 删除设备时，`device-state` 会清除其实时状态、最新读数、最新位置和被丢弃读数的记录。之后以相同令牌创建的设备不会继承这些数据，因此其新所有者的成员看不到旧设备的最后位置或读数。
- `device-state` 现在还会每 10 分钟使用实例的服务凭据向 `device-management` 查询哪些设备仍然存在，并清除此前（包括升级之前）删除的设备遗留的实时状态。查询失败时不会清除任何数据。如果未配置服务密钥或 `device-management` 与 `user-management` 端点，则跳过该检查，并在启动时记录警告。
- 平台在删除前接受但在删除后才处理的读数，仍可能短暂地重新生成已删除设备的实时状态；如果没有设备持有该令牌，定期检查会将其清除。
