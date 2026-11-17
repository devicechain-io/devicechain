---
section: whats-new
---

## en

Adds `devicechain_svcclient_queries_total{result="ok|error"}` and
`devicechain_svcclient_query_duration_seconds` for calls after the scoped-caller gate,
including validation and token acquisition. `devicechain_svcclient_service_token_mint_duration_seconds`
measures actual mint attempts, including failures, excluding cache hits and single-flight waiters.
`devicechain_userclient_logins_total{result="ok|error|mfa_challenge"}` counts login
exchanges; MFA verification and tenant selection are separate operations and are not counted.
`devicechain_userclient_refreshes_total{result="ok|error"}` counts refresh exchanges
before any login fallback. All new series start at zero and carry no tenant, credential,
query, or endpoint labels. Rolling upgrades need no configuration or database changes;
these metrics appear as each process upgrades.

## es

Añade `devicechain_svcclient_queries_total{result="ok|error"}` y
`devicechain_svcclient_query_duration_seconds` para llamadas posteriores al control de
alcance, incluidas la validación y la obtención del token.
`devicechain_svcclient_service_token_mint_duration_seconds` mide los intentos reales de
emisión, incluidos los fallidos, sin contar la caché ni quienes esperan una emisión compartida.
`devicechain_userclient_logins_total{result="ok|error|mfa_challenge"}` cuenta los
intercambios de login; la verificación MFA y la selección de tenant son operaciones separadas
y no se cuentan. `devicechain_userclient_refreshes_total{result="ok|error"}` cuenta
las renovaciones antes de recurrir al login. Todas las series nuevas empiezan en cero y
no incluyen etiquetas de tenant, credenciales, consulta ni endpoint. La actualización gradual
no requiere cambios de configuración ni de base de datos; las métricas aparecen al actualizar cada proceso.

## zh-CN

新增 `devicechain_svcclient_queries_total{result="ok|error"}` 和
`devicechain_svcclient_query_duration_seconds`，统计通过调用者范围检查后的调用，
包括本地校验和获取令牌的时间。`devicechain_svcclient_service_token_mint_duration_seconds`
统计实际服务令牌签发尝试的耗时，包括失败，不计缓存命中或等待共享签发的调用者。
`devicechain_userclient_logins_total{result="ok|error|mfa_challenge"}` 统计 login
交换；MFA 验证和租户选择是独立操作，不计入该指标。
`devicechain_userclient_refreshes_total{result="ok|error"}` 统计刷新交换，
在回退到 login 之前记录结果。所有新增序列从零开始，不含租户、凭据、查询或端点标签。
滚动升级无需修改配置或数据库；各进程升级后即可提供这些指标。
