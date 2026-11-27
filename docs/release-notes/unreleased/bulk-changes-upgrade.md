---
section: before-upgrade
---

## en

- **Bulk changes apply lines only on upgraded pods.** While device-management rolls from v1.9 to v1.10, a pod still on v1.9 answers a bulk-change call as an unknown field, and `dcctl bulk-change` reports **still upgrading; retry**. Devices keep accepting ordinary attribute writes from both releases throughout. There is nothing to do: once the roll has finished, `dcctl bulk-change readiness` lists `config.set` when enforcement reads `ENFORCED` and the tenant has no orphaned attribute rows.

## es

- **Los cambios masivos solo aplican líneas en pods actualizados.** Mientras device-management pasa de la v1.9 a la v1.10, un pod que sigue en la v1.9 responde a una llamada de cambios masivos como un campo desconocido, y `dcctl bulk-change` informa **still upgrading; retry**. Los dispositivos siguen aceptando escrituras de atributos ordinarias de ambas versiones durante todo el proceso. No hay nada que hacer: cuando termina la actualización, `dcctl bulk-change readiness` incluye `config.set` si la aplicación indica `ENFORCED` y el tenant no tiene filas de atributos huérfanas.

## zh-CN

- **批量变更只在已升级的 Pod 上应用行。** device-management 从 v1.9 滚动升级到 v1.10 期间，仍运行 v1.9 的 Pod 会把批量变更调用视为未知字段，`dcctl bulk-change` 会报告 **still upgrading; retry**。整个过程中，设备仍接受来自两个版本的普通属性写入。无需任何操作：滚动升级完成后，当强制执行状态为 `ENFORCED` 且租户没有孤立属性行时，`dcctl bulk-change readiness` 会列出 `config.set`。
