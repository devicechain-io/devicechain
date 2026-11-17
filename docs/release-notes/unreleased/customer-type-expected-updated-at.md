---
section: whats-new
---

## en

Customer type edits now accept optional `expectedUpdatedAt` on `updateCustomerType`. Pass the last read `updatedAt` to reject stale saves; on "modified by another writer", reload and retry. Omitting the argument preserves last-write-wins behavior. This protection is available after all device-management replicas finish upgrading; other registry edit operations are unchanged. No database migration is required.

## es

Las ediciones de tipos de cliente aceptan ahora el argumento opcional `expectedUpdatedAt` en `updateCustomerType`. Envíe el último `updatedAt` leído para rechazar guardados obsoletos; ante "modified by another writer", vuelva a cargar e inténtelo de nuevo. Omitir el argumento conserva el comportamiento de última escritura. Esta protección está disponible cuando todas las réplicas de device-management terminan de actualizarse; las demás operaciones del registro no cambian. No se requiere migración de base de datos.

## zh-CN

客户类型编辑现支持在 `updateCustomerType` 中传入可选的 `expectedUpdatedAt`。传入上次读取的 `updatedAt` 可拒绝过期保存；遇到 "modified by another writer" 时，请重新加载后重试。省略该参数将保留最后写入生效的行为。此保护在所有 device-management 副本完成升级后可用；其他注册表编辑操作保持不变。无需数据库迁移。
