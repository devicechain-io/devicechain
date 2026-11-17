---
section: whats-new
---

## en

#### An update assigned to a deleted device is ended {#v170-deleted-device-updates}

- When a device is deleted, `update-management` ends its pending update assignments, and their rollout targets show as cancelled. A device later created with the same token is not offered them and cannot download their artifacts, even on another device profile.
- Assignments made before the upgrade are matched by time: one older than the device now holding its token is not offered to that device.
- While old and new pods overlap during the rolling upgrade, a `v1.6` pod can still serve such an assignment until it is replaced.

## es

#### Se terminan las actualizaciones asignadas a un dispositivo eliminado {#v170-deleted-device-updates}

- Al eliminar un dispositivo, `update-management` termina sus asignaciones de actualización pendientes, y sus objetivos de despliegue se muestran como cancelados. Un dispositivo creado después con el mismo token no las recibe ni puede descargar sus artefactos, aunque tenga otro perfil de dispositivo.
- Las asignaciones creadas antes de la actualización se comparan por tiempo: una anterior al dispositivo que ahora tiene su token no se ofrece a ese dispositivo.
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` aún puede servir una de esas asignaciones hasta que se sustituye.

## zh-CN

#### 分配给已删除设备的更新会被终止 {#v170-deleted-device-updates}

- 删除设备时，`update-management` 会终止其待处理的更新分配，相应的发布目标显示为已取消。之后以相同令牌创建的设备不会收到这些分配，也无法下载其制品，即使使用不同的设备配置文件。
- 升级前创建的分配按时间匹配：早于当前持有该令牌的设备的分配不会提供给该设备。
- 在滚动升级期间新旧 Pod 并存时，`v1.6` Pod 在被替换前仍可能提供此类分配。
