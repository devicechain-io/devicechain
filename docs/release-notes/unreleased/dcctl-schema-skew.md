---
section: whats-new
---

## en

- **dcctl negotiates API capabilities.** Tenant and admin commands check the target schema before sending operations or input fields. Unsupported features report an upgrade remedy. Each session caches successful discovery per exact endpoint, and discovery refuses redirects. Newer instances advertise their actual schema to authenticated callers without enabling anonymous introspection. For older instances, dcctl uses the instance's recorded release from its cluster Instance resource, located through the local instance record or `--instance`. A recorded stable v1.7.x/v1.8.x release permits the conservative v1.7 contract on matching endpoints. An OPTIONS rejection never identifies a release: unknown versions still require discovery, with guidance to select a readable instance record or use v1.9 discovery. Core client callers register independently verified releases with `WithLegacyEndpointVersion`; preview features and features outside the legacy contract require v1.9 discovery. Commands and flags remain available.

## es

- **dcctl negocia las capacidades de la API.** Los comandos de tenant y administración comprueban el esquema de destino antes de enviar operaciones o campos de entrada. Las funciones no compatibles indican cómo actualizar. Cada sesión conserva los resultados correctos por URL exacta y rechaza las redirecciones de detección. Las instancias recientes publican su esquema real a clientes autenticados sin habilitar la introspección anónima. Para las instancias antiguas, dcctl usa la versión registrada en el recurso Instance del clúster, localizado mediante el registro local de la instancia o `--instance`. Una versión estable v1.7.x/v1.8.x registrada permite el contrato conservador v1.7 en los endpoints correspondientes. Un rechazo de OPTIONS nunca identifica la versión: una versión desconocida sigue requiriendo detección, con indicaciones para seleccionar un registro de instancia legible o usar la detección de v1.9. Los clientes de la biblioteca core registran versiones verificadas de forma independiente con `WithLegacyEndpointVersion`; las funciones en vista previa y las que están fuera del contrato antiguo requieren la detección de v1.9. Los comandos y las opciones siguen disponibles.

## zh-CN

- **dcctl 协商 API 能力。** 租户和管理命令在发送操作或输入字段之前检查目标实例的架构。不支持的功能会给出升级建议。每个会话按完整端点 URL 缓存成功的发现结果，并拒绝发现请求的重定向。较新的实例向已认证的客户端提供实际架构，无需启用匿名自省。对于较旧的实例，dcctl 使用集群 Instance 资源中记录的实例版本，通过本地实例记录或 `--instance` 定位该资源。记录为稳定版 v1.7.x/v1.8.x 的实例可在匹配的端点上使用保守的 v1.7 API 契约。OPTIONS 请求被拒绝不能证明目标版本：版本未知时仍须发现架构，并提示选择可读取的实例记录或使用 v1.9 的架构发现。core 库调用方通过 `WithLegacyEndpointVersion` 注册经过独立验证的版本；预览功能和超出旧版契约的功能需要 v1.9 的架构发现。现有命令和参数仍然可用。
