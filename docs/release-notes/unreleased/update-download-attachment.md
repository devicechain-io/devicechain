---
section: before-upgrade
---

## en

- **Update downloads are always served as attachments.** The artifact download endpoint now answers every request, including range and `HEAD` requests, with `Content-Type: application/octet-stream`, `Content-Disposition: attachment` and a restrictive `Content-Security-Policy`, whatever media type the artifact was declared with. The declared media type is unchanged in the catalogue and in the API, and the download also carries it in the `X-DeviceChain-Artifact-Media-Type` response header. A device client that read the download's `Content-Type` to learn the artifact's type should read that header instead; clients that only store the bytes and check the digest need no change.

## es

- **Las descargas de actualizaciones se sirven siempre como adjuntos.** El endpoint de descarga de artefactos responde ahora a todas las solicitudes, incluidas las de rango y las `HEAD`, con `Content-Type: application/octet-stream`, `Content-Disposition: attachment` y una `Content-Security-Policy` restrictiva, sea cual sea el tipo de medio con el que se declaró el artefacto. El tipo de medio declarado no cambia en el catálogo ni en la API, y la descarga también lo incluye en la cabecera de respuesta `X-DeviceChain-Artifact-Media-Type`. Un cliente de dispositivo que leía el `Content-Type` de la descarga para conocer el tipo del artefacto debe leer esa cabecera; los clientes que solo guardan los bytes y comprueban el resumen no necesitan cambios.

## zh-CN

- **更新下载始终以附件形式提供。** 制品下载端点现在对所有请求（包括范围请求和 `HEAD` 请求）都返回 `Content-Type: application/octet-stream`、`Content-Disposition: attachment` 以及严格的 `Content-Security-Policy`，无论制品声明的媒体类型是什么。声明的媒体类型在目录和 API 中保持不变，下载响应还会在 `X-DeviceChain-Artifact-Media-Type` 响应头中携带它。若设备客户端此前通过下载的 `Content-Type` 获取制品类型，应改为读取该响应头；只保存字节并校验摘要的客户端无需任何更改。
