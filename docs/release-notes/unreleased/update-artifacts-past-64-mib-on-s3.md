---
section: whats-new
---

## en

- **Update artifacts larger than 64 MiB can be stored on an S3 object store.** Uploads to the S3 backend now stream to the store in parts instead of being held in memory, so the 64 MiB limit that applied only to S3 is gone. An artifact is limited by `updates.maxArtifactBytes` (512 MiB by default) on every backend, the size and digest are still verified while the upload streams, and a refused upload leaves no partial object behind. Raising `updates.maxArtifactBytes` above 64 MiB now takes effect on S3; it was silently held at 64 MiB before. The `ARTIFACT_TOO_LARGE_FOR_BACKEND` refusal is no longer returned.

## es

- **Los artefactos de actualización de más de 64 MiB pueden guardarse en un almacén de objetos S3.** Las subidas al backend S3 ahora se envían al almacén por partes en lugar de mantenerse en memoria, por lo que desaparece el límite de 64 MiB que solo se aplicaba a S3. Un artefacto queda limitado por `updates.maxArtifactBytes` (512 MiB por defecto) en todos los backends, el tamaño y el digest se siguen verificando mientras la subida avanza, y una subida rechazada no deja ningún objeto parcial. Subir `updates.maxArtifactBytes` por encima de 64 MiB ahora surte efecto en S3; antes se mantenía en 64 MiB sin avisar. El rechazo `ARTIFACT_TOO_LARGE_FOR_BACKEND` ya no se devuelve.

## zh-CN

- **超过 64 MiB 的更新制品现在可以存储在 S3 对象存储上。** 上传到 S3 后端时，内容现在会分段流式写入存储，而不再缓冲在内存中，因此只适用于 S3 的 64 MiB 限制已经取消。在所有后端上，制品大小都由 `updates.maxArtifactBytes`（默认 512 MiB）限制；上传过程中仍会校验大小和摘要，被拒绝的上传不会留下任何残缺对象。将 `updates.maxArtifactBytes` 调高到 64 MiB 以上现在会在 S3 上生效，此前它会被悄悄限制在 64 MiB。`ARTIFACT_TOO_LARGE_FOR_BACKEND` 拒绝码不再返回。
