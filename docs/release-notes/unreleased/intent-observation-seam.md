---
section: whats-new
---

## en

- **Intent observation queries (Preview, internal use).** device-management serves `configurationKeyStateDigest`, `configurationKeyState`, `groupTargetsIntersect`, `previewIntentBulkChange` and `bulkChangeGranteeStanding`: read-only questions about the members of a published device-group version and one configuration key, for the platform's automation service. Each answers only a service token whose subject is that service; people, scoped members and agents are refused. They write nothing, and a statement over its time budget, or one the service's concurrency limit cannot admit, is refused rather than answered in part. They are Preview and may change in a minor release; nothing in this release calls them yet.

## es

- **Consultas de observación de intenciones (Preview, uso interno).** device-management sirve `configurationKeyStateDigest`, `configurationKeyState`, `groupTargetsIntersect`, `previewIntentBulkChange` y `bulkChangeGranteeStanding`: preguntas de solo lectura sobre los miembros de una versión publicada de un grupo de dispositivos y una clave de configuración, para el servicio de automatización de la plataforma. Cada una responde solo a un token de servicio cuyo sujeto es ese servicio; se rechazan las personas, los miembros con ámbito limitado y los agentes. No escriben nada, y una sentencia que supera su presupuesto de tiempo, o que el límite de concurrencia del servicio no puede admitir, se rechaza en lugar de responderse en parte. Son Preview y pueden cambiar en una versión menor; en esta versión todavía no las llama nada.

## zh-CN

- **意图观测查询（Preview，内部使用）。** device-management 提供 `configurationKeyStateDigest`、`configurationKeyState`、`groupTargetsIntersect`、`previewIntentBulkChange` 和 `bulkChangeGranteeStanding`：针对某个已发布设备组版本的成员和一个配置键的只读查询，供平台的自动化服务使用。每个查询只响应主体为该服务的服务令牌；个人、具有受限范围的成员和代理会被拒绝。它们不写入任何内容；超出时间预算的语句，或服务并发上限无法接纳的语句，会被拒绝，而不会返回部分结果。它们为 Preview，可能在次版本中变更；本版本中尚无任何组件调用它们。
