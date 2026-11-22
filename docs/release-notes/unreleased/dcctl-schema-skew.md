---
section: whats-new
---

## en

- **dcctl negotiates API capabilities.** Tenant and admin commands check the target schema before sending operations or input fields. Unsupported features report an upgrade remedy. Each session caches successful discovery per exact endpoint, and discovery refuses redirects. Newer instances advertise their actual schema to authenticated callers without enabling anonymous introspection. An OPTIONS rejection cannot identify a legacy release: dcctl refuses targets it cannot discover. Core client callers may use the conservative v1.7 contract only after independently verifying a stable v1.7/v1.8 release for that exact endpoint with `WithLegacyEndpointVersion`; preview features and features outside that contract require v1.9 discovery. Commands and flags remain available.

## es

- **dcctl negocia las capacidades de la API.** Los comandos de tenant y administración comprueban el esquema de destino antes de enviar operaciones o campos de entrada. Las funciones no compatibles indican cómo actualizar. Cada sesión conserva los resultados correctos por URL exacta y rechaza las redirecciones de detección. Las instancias recientes publican su esquema real a clientes autenticados sin habilitar la introspección anónima. Un rechazo de OPTIONS no identifica una versión antigua: dcctl rechaza los destinos cuyo esquema no puede detectar. Los clientes de la biblioteca core pueden usar el contrato conservador v1.7 solo tras verificar de forma independiente una versión estable v1.7/v1.8 de ese destino exacto con `WithLegacyEndpointVersion`; las funciones en vista previa y las que están fuera de ese contrato requieren la detección de v1.9. Los comandos y las opciones siguen disponibles.

## zh-CN

- **dcctl 协商 API 能力。** 租户和管理命令在发送操作或输入字段之前检查目标实例的架构。不支持的功能会给出升级建议。每个会话按完整端点 URL 缓存成功的发现结果，并拒绝发现请求的重定向。较新的实例向已认证的客户端提供实际架构，无需启用匿名自省。OPTIONS 请求被拒绝不能证明目标使用旧版本：dcctl 会拒绝无法发现架构的目标。core 库的调用方只有在独立验证该确切端点运行稳定版 v1.7/v1.8 后，才能通过 `WithLegacyEndpointVersion` 使用保守的 v1.7 API 契约；预览功能和超出该契约的功能需要 v1.9 的架构发现。现有命令和参数仍然可用。
