---
section: before-upgrade
---

## en

- **Campaign error details are sanitized.** `Campaign.lastError` keeps actionable configuration messages; internal failures show a generic retry message, with full details logged server-side. Messages remain capped at 500 bytes and truncate at a UTF-8 boundary. The GraphQL field and type are unchanged.

## es

- **Se filtran los detalles de error de las campañas.** `Campaign.lastError` conserva los mensajes de configuración que permiten actuar; los fallos internos muestran un mensaje genérico de reintento y los detalles completos se registran en el servidor. Los mensajes siguen limitados a 500 bytes y se truncan en un límite UTF-8. El campo GraphQL y su tipo no cambian.

## zh-CN

- **活动错误详情经过脱敏。** `Campaign.lastError` 保留可据以修正配置的消息；内部故障显示通用重试消息，完整详情记录在服务端日志中。消息仍限制为 500 字节，并在 UTF-8 字符边界截断。GraphQL 字段及其类型保持不变。
