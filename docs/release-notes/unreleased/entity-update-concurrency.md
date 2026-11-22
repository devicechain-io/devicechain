---
section: whats-new
---

## en

- **Protect concurrent edits.** The device-management GraphQL mutations `updateArea`, `updateAsset`, `updateCustomer` and `updateEntityGroup` now accept an optional `expectedUpdatedAt` argument. Send the entity's last-read `updatedAt` to refuse an outdated save with `STALE_WRITE`; reload before retrying. Omitting the argument preserves last-write-wins behavior.

## es

- **Protección frente a ediciones simultáneas.** Las mutaciones GraphQL de device-management `updateArea`, `updateAsset`, `updateCustomer` y `updateEntityGroup` ahora aceptan el argumento opcional `expectedUpdatedAt`. Envíe el último `updatedAt` leído de la entidad para rechazar un guardado desactualizado con `STALE_WRITE`; vuelva a cargar antes de reintentar. Omitir el argumento conserva el comportamiento en el que prevalece la última escritura.

## zh-CN

- **保护并发编辑。** device-management 的 GraphQL 变更 `updateArea`、`updateAsset`、`updateCustomer` 和 `updateEntityGroup` 现已支持可选参数 `expectedUpdatedAt`。传入上次读取的实体 `updatedAt`，即可在保存过期数据时收到 `STALE_WRITE` 错误；重试前请重新加载。省略该参数时仍保留最后写入生效的行为。
