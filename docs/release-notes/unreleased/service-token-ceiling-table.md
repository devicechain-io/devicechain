---
section: whats-new
---

## en

- **The service-token authority ceiling no longer flags six callers that were within it.** `devicechain_usermanagement_service_token_checks_total` counted mints by `command-delivery`, `event-sources`, `outbound-connectors` and `event-management` as `over_ceiling`, and mints by `device-state` and by `user-management` itself as `unknown_caller` under `caller="other"`, although each asked only for what it uses. The ceiling table now matches every service that mints a token, so those series stay at zero. The check still only observes: no mint is refused.

## es

- **El techo de autoridades de los tokens de servicio ya no señala a seis llamantes que estaban dentro de él.** `devicechain_usermanagement_service_token_checks_total` contaba las emisiones de `command-delivery`, `event-sources`, `outbound-connectors` y `event-management` como `over_ceiling`, y las de `device-state` y del propio `user-management` como `unknown_caller` con `caller="other"`, aunque cada uno pedía solo lo que usa. La tabla de techos coincide ahora con todos los servicios que emiten un token, así que esas series se quedan en cero. La comprobación sigue siendo solo de observación: no se rechaza ninguna emisión.

## zh-CN

- **服务令牌权限上限不再误报六个未超限的调用方。** `devicechain_usermanagement_service_token_checks_total` 曾把 `command-delivery`、`event-sources`、`outbound-connectors` 和 `event-management` 的签发计为 `over_ceiling`，并把 `device-state` 和 `user-management` 自身的签发以 `caller="other"` 计为 `unknown_caller`，而它们请求的都只是自己使用的权限。上限表现在与每个签发令牌的服务一致，因此这些序列保持为零。该检查仍然只做观测：不会拒绝任何签发。
