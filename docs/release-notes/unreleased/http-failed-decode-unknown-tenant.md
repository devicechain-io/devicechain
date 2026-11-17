---
section: whats-new
---

## en

- **Undecodable HTTP bodies addressed to a tenant that does not exist are no longer recorded as failed decodes.** The failed-decode record is shared by the whole instance, so a sender inventing tenant names could push real tenants' records out of it. Such a body is now counted on `total_http_ingest_refused` with the `unknown_tenant` reason instead, and the sender gets the same answer as for any other malformed body.

## es

- **Los cuerpos HTTP que no se pueden decodificar y van dirigidos a un tenant que no existe ya no se registran como decodificaciones fallidas.** El registro de decodificaciones fallidas es compartido por toda la instancia, así que un remitente que inventara nombres de tenant podía desplazar de él los registros de tenants reales. Ahora ese cuerpo se cuenta en `total_http_ingest_refused` con el motivo `unknown_tenant`, y el remitente recibe la misma respuesta que para cualquier otro cuerpo mal formado.

## zh-CN

- **发往不存在租户且无法解码的 HTTP 请求体不再记录为解码失败。** 解码失败记录由整个实例共享，因此编造租户名称的发送方可能会把真实租户的记录挤出去。现在此类请求体改为计入 `total_http_ingest_refused` 的 `unknown_tenant` 原因，发送方收到的应答与其他任何格式错误的请求体相同。
