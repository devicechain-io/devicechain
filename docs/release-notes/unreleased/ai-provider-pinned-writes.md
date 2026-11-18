---
section: whats-new
---

## en

- **AI-provider updates and deletes act on the provider they checked.** An `updateAiProvider` without an `expectedUpdatedAt` precondition now answers not-found when the provider was deleted between the read and the write, instead of re-creating it. It also no longer reverts a concurrent rename. Deleting a provider removes the row that was checked for grants, even when a rename and a new provider with the old token land in between, and the delete's audit-journal entry records the provider's id and label.

## es

- **Las actualizaciones y eliminaciones de proveedores de IA actúan sobre el proveedor que comprobaron.** Un `updateAiProvider` sin la precondición `expectedUpdatedAt` responde ahora que no existe cuando el proveedor se eliminó entre la lectura y la escritura, en lugar de volver a crearlo. Tampoco revierte ya un cambio de nombre simultáneo. Eliminar un proveedor borra la fila cuyas concesiones se comprobaron, aunque entretanto se produzcan un cambio de nombre y un proveedor nuevo con el token anterior, y la entrada del registro de auditoría de la eliminación registra el id y la etiqueta del proveedor.

## zh-CN

- **AI 提供商的更新和删除作用于其检查过的提供商。** 未带 `expectedUpdatedAt` 前置条件的 `updateAiProvider`，如果提供商在读取与写入之间已被删除，现在会返回未找到，而不会重新创建它；它也不再撤销并发的重命名。删除提供商时，删除的是已检查过授权的那一行，即使其间发生了重命名并以旧令牌创建了新的提供商；删除操作的审计日志条目会记录该提供商的 id 和标签。
