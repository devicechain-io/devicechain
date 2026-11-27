---
section: before-upgrade
---

## en

- **Credentials embedded in backup endpoints are deprecated.** `dcctl install` still accepts an `endpointUrl` with userinfo, a query or a fragment, but now warns without displaying the credential. These values will be refused in the next major release. Use a credential-free `endpointUrl` and the `accessKeyId` and `secretAccessKey` fields in `--backup-credentials-file`. The endpoint continues to be stored unchanged in the `dc-infra/dc-install` ConfigMap because later bootstrap and destroy operations need it.

## es

- **Las credenciales incluidas en los endpoints de copia de seguridad están obsoletas.** `dcctl install` sigue aceptando un `endpointUrl` con información de usuario, una consulta o un fragmento, pero ahora muestra una advertencia sin revelar la credencial. Estos valores se rechazarán en la próxima versión mayor. Use un `endpointUrl` sin credenciales y los campos `accessKeyId` y `secretAccessKey` de `--backup-credentials-file`. El endpoint sigue almacenándose sin cambios en el ConfigMap `dc-infra/dc-install` porque las operaciones posteriores de bootstrap y destroy lo necesitan.

## zh-CN

- **备份端点中嵌入凭据的用法已弃用。** `dcctl install` 仍接受包含用户信息、查询字符串或片段的 `endpointUrl`，但现在会发出警告，且不会显示凭据。下一个主版本将拒绝这些值。请使用不含凭据的 `endpointUrl`，并在 `--backup-credentials-file` 中使用 `accessKeyId` 和 `secretAccessKey` 字段。端点仍会原样存储在 `dc-infra/dc-install` ConfigMap 中，因为后续的 bootstrap 和 destroy 操作需要它。
