---
section: before-upgrade
---

## en

- **MCP session limits.** New sessions are refused with HTTP 429 and `Retry-After` once a caller has 16 live sessions or the MCP process has 1,024. Agents should reuse sessions, DELETE unused sessions and honor `Retry-After`. Existing sessions remain usable; deleted sessions and sessions reaped after 30 idle minutes free capacity. Review clients that repeatedly initialize before upgrading; no configuration change is required.

## es

- **Límites de sesiones MCP.** Las sesiones nuevas se rechazan con HTTP 429 y `Retry-After` cuando un usuario tiene 16 sesiones activas o el proceso MCP tiene 1.024. Los agentes deben reutilizar sesiones, cerrar las que no usan mediante DELETE y respetar `Retry-After`. Las sesiones existentes siguen disponibles; las sesiones eliminadas y las que caducan tras 30 minutos de inactividad liberan capacidad. Revise los clientes que inicializan repetidamente antes de actualizar; no es necesario cambiar la configuración.

## zh-CN

- **MCP 会话数量限制。** 当同一用户已有 16 个存续会话，或 MCP 进程已有 1,024 个存续会话时，新建会话将收到 HTTP 429 和 `Retry-After`。代理应复用会话，通过 DELETE 关闭闲置会话，并遵守 `Retry-After`。现有会话仍可使用；已删除的会话以及空闲 30 分钟后被清理的会话会释放容量。升级前请检查反复初始化的客户端；无需修改配置。
