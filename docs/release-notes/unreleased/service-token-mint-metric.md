---
section: whats-new
---

## en

Adds `devicechain_svcclient_service_token_mints_total{result=~"ok|error"}` on the existing metrics endpoint. Both result series start at zero. It counts completed shared mint operations, including HTTP, transport and invalid-response failures; cache hits, callers waiting on the same mint, and refusals before minting are excluded. Labels contain no tenant, identity, token or URL. No configuration or database migration is needed; during a rolling upgrade only updated pods expose it.

## es

Añade `devicechain_svcclient_service_token_mints_total{result=~"ok|error"}` al endpoint de métricas existente. Ambas series empiezan en cero. Cuenta las operaciones compartidas de emisión completadas, incluidos los errores HTTP, de transporte y de respuesta inválida; excluye los aciertos de caché, los llamadores que esperan la misma emisión y los rechazos previos a la emisión. Las etiquetas no contienen inquilinos, identidades, tokens ni URL. No requiere configuración ni migración de base de datos; durante una actualización gradual solo los pods actualizados la exponen.

## zh-CN

现有指标端点新增 `devicechain_svcclient_service_token_mints_total{result=~"ok|error"}`。两个结果序列均从零开始。它统计已完成的共享签发操作，包括 HTTP、传输和无效响应错误；不统计缓存命中、等待同一次签发的调用方或签发前的拒绝。标签不包含租户、身份、令牌或 URL。无需更改配置或迁移数据库；滚动升级期间仅更新后的 Pod 提供此指标。
